import { test } from 'node:test';
import assert from 'node:assert/strict';
import type {
  AnalysisSourceBindingResponse,
  CompositionDefinition,
  CompositionTaskResult,
  SourceScope,
} from '../contracts/analysis.ts';
import type { ApiRequestOptions } from '../contracts/common.ts';
import {
  AnalysisSources,
  type AnalysisSourceMode,
  type GridSourceContextData,
} from './sources.ts';

function deferred<T>() {
  let resolve: (value: T | PromiseLike<T>) => void = () => undefined;
  const promise = new Promise<T>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

interface SourceCall {
  readonly path: string;
  readonly data: Record<string, unknown>;
}

function setup() {
  const c = { bookKey: 'fingerprint', book: 'path-book', opened: true };
  const calls: SourceCall[] = [];
  let apiImpl: (path: string, data: Record<string, unknown>) => Promise<unknown> = async () => ({});
  let gridImpl: (mode: Exclude<AnalysisSourceMode, 'full'>) => GridSourceContextData = () => ({
    workbookKey: c.bookKey,
    book: c.book,
    source: 'wb:源',
    filterId: '',
    viewId: 'view',
    excludeThroughRow: 0,
  });
  let taskRunImpl: (
    request: Record<string, unknown>,
    guard?: () => boolean,
  ) => Promise<unknown> = async () => ({});

  const h = new AnalysisSources({
    context: () => ({ ...c }),
    grid: (mode) => gridImpl(mode),
    api: async <T = unknown>(path: string, options?: ApiRequestOptions): Promise<T> => {
      const data =
        typeof options?.body === 'string'
          ? (JSON.parse(options.body) as Record<string, unknown>)
          : {};
      calls.push({ path, data });
      return (await apiImpl(path, data)) as T;
    },
    tasks: {
      run: async <T = unknown>(
        request: Record<string, unknown>,
        guard?: () => boolean,
      ): Promise<T> => (await taskRunImpl(request, guard)) as T,
    },
  });

  const response = (
    id: string,
    mode: SourceScope['mode'] = 'visible',
  ): AnalysisSourceBindingResponse => ({
    id,
    source: 'wb:源',
    rows: 10,
    workbookKey: c.bookKey,
    book: c.book,
    sourceScope: { version: 1, mode, fingerprint: c.bookKey },
  });

  return {
    h,
    c,
    calls,
    response,
    setApi: (fn: typeof apiImpl) => {
      apiImpl = fn;
    },
    setGrid: (fn: typeof gridImpl) => {
      gridImpl = fn;
    },
    setTaskRun: (fn: typeof taskRunImpl) => {
      taskRunImpl = fn;
    },
  };
}

const sampleComposition: CompositionDefinition = {
  version: 1,
  name: '联合',
  fields: [],
  inputs: [],
};

test('cancel and replacement release late owners without clearing the newer binding', async () => {
  const { h, calls, response, setApi } = setup();
  const gate = deferred<AnalysisSourceBindingResponse>();
  setApi(async (path) => (path.endsWith('/capture') ? gate.promise : {}));

  const late = h.prepare({ source: 'wb:源', mode: 'visible' });
  h.cancel();
  const full = await h.prepare({ source: 'wb:源', mode: 'full' });
  h.commit(full);
  gate.resolve(response('late'));
  await assert.rejects(late, { name: 'AbortError' });
  assert.equal(h.binding, full);
  assert.deepEqual(
    calls.filter((call) => call.path.endsWith('/clear')).map((call) => call.data.id),
    ['late'],
  );
});

test('a candidate cannot publish before schema validation and failed preparation retains the old owner', async () => {
  const { h, calls, response, setApi, setGrid } = setup();
  setApi(async (path) => (path.endsWith('/capture') ? response('visible') : {}));

  const old = await h.prepare({ source: 'wb:源', mode: 'visible' });
  h.commit(old);
  const candidate = await h.prepare({ source: 'wb:源', mode: 'full' });
  assert.equal(h.binding, old);
  h.release(candidate);
  assert.equal(h.binding, old);

  setGrid(() => {
    throw new Error('仍在加载');
  });
  await assert.rejects(h.prepare({ source: 'wb:源', mode: 'selection' }), /仍在加载/);
  assert.equal(h.binding, old);
  assert.equal(
    calls.some((call) => call.path.endsWith('/clear')),
    false,
  );
});

test('same content at a different path and a closed workspace invalidate delayed restores', async () => {
  const mutations: readonly ((ctx: { bookKey: string; book: string; opened: boolean }) => void)[] =
    [
      (ctx) => {
        ctx.book = 'other-path';
      },
      (ctx) => {
        ctx.opened = false;
      },
    ];
  for (const mutate of mutations) {
    const { h, c, calls, response, setApi } = setup();
    const gate = deferred<AnalysisSourceBindingResponse>();
    const reply = response('stale');
    setApi(async (path) => (path.endsWith('/restore') ? gate.promise : {}));
    const pending = h.prepare({ source: 'wb:源', sourceScope: reply.sourceScope });
    mutate(c);
    gate.resolve(reply);
    await assert.rejects(pending, { name: 'AbortError' });
    assert.equal(h.binding, null);
    assert.equal(calls.at(-1)?.data.id, 'stale');
  }
});

test('runtime tokens stay outside durable queries and only match their source and scope', async () => {
  const { h, response, setApi } = setup();
  const data = response('owner');
  setApi(async () => data);
  h.commit(await h.prepare({ source: 'wb:源', mode: 'visible' }));

  const query = {
    source: 'wb:源',
    rows: [] as string[],
    aggregate: 'count' as const,
    sourceScope: data.sourceScope,
  };
  assert.equal(h.query(query).sourceId, 'owner');
  assert.equal((query as { sourceId?: string }).sourceId, undefined);
  assert.equal(h.query({ ...query, source: 'wb:另一表' }).sourceId, undefined);
  assert.equal(h.query({ source: 'wb:源', rows: [], aggregate: 'count' }).sourceId, undefined);

  const candidate = await h.prepare({ source: 'wb:源', mode: 'full' });
  h.commit(candidate);
  assert.equal(h.binding?.sourceScope, null);
});

test('an obsolete schema intent cannot commit its prepared source', async () => {
  const { h, response, setApi } = setup();
  let current = true;
  setApi(async () => response('pending'));
  const candidate = await h.prepare({ source: 'wb:源', mode: 'visible' }, () => current);
  current = false;
  assert.throws(() => h.commit(candidate), { name: 'AbortError' });
  assert.equal(h.binding, null);
  assert.equal(h.candidates.size, 0);
});

test('append definitions use composition owners and atomically replace only after fields succeed', async () => {
  const { h, calls, setTaskRun } = setup();
  setTaskRun(
    async (): Promise<CompositionTaskResult> => ({
      id: 'append-job',
      mode: 'append',
      source: 'append:hash',
      rows: 5,
      previewTruncated: false,
      workbookKey: 'fingerprint',
      book: 'path-book',
      definition: sampleComposition,
    }),
  );
  const candidate = await h.prepare({ source: 'append:hash', composition: sampleComposition });
  assert.equal(h.binding, null);
  h.commit(candidate);
  assert.equal(
    h.query({
      source: 'append:hash',
      rows: [],
      aggregate: 'count',
      composition: candidate.composition,
    }).sourceId,
    'append-job',
  );
  h.reset();
  assert.equal(calls.at(-1)?.path, '/api/composition/clear');
});

test('a fresh prepared append replaces an equal definition owner and releases the prior token', async () => {
  const { h, calls, setTaskRun } = setup();
  const payload = {
    mode: 'append' as const,
    source: 'append:hash',
    rows: 5,
    previewTruncated: false,
    workbookKey: 'fingerprint',
    book: 'path-book',
    definition: sampleComposition,
  };
  setTaskRun(async () => {
    throw new Error('prepared source must not rescan');
  });
  h.commit(
    await h.prepare({
      source: payload.source,
      composition: sampleComposition,
      prepared: { ...payload, id: 'old' },
    }),
  );
  h.commit(
    await h.prepare({
      source: payload.source,
      composition: sampleComposition,
      prepared: { ...payload, id: 'new' },
    }),
  );
  assert.equal(h.binding?.id, 'new');
  assert.equal(calls.at(-1)?.path, '/api/composition/clear');
  assert.equal(calls.at(-1)?.data.id, 'old');
});
