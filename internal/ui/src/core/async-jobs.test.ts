import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { ApiRequestOptions } from '../contracts/common.ts';
import { AsyncJobs, type AsyncJobStatusEnvelope } from './async-jobs.ts';

const tick = (): Promise<void> =>
  new Promise((resolve) => {
    setImmediate(resolve);
  });

interface PendingStart {
  readonly resolve: (value: unknown) => void;
  readonly options: ApiRequestOptions | undefined;
}

interface PendingStatus {
  readonly resolve: (value: unknown) => void;
  readonly url: string;
}

function setup(maxPolls?: number) {
  let book = 'one';
  const starts: PendingStart[] = [];
  const statuses: PendingStatus[] = [];
  const cancelled: string[] = [];
  const busy: boolean[] = [];
  const progress: AsyncJobStatusEnvelope<Record<string, unknown>>[] = [];

  const jobs = new AsyncJobs<
    Record<string, unknown>,
    AsyncJobStatusEnvelope<Record<string, unknown>>
  >({
    identity: () => book,
    sleep: async () => undefined,
    onBusy: (value) => busy.push(value),
    onProgress: (value) => progress.push(value),
    ...(maxPolls !== undefined ? { maxPolls } : {}),
    api: <T = unknown>(url: string, options?: ApiRequestOptions): Promise<T> => {
      if (url.endsWith('/start')) {
        return new Promise<T>((resolve) =>
          starts.push({ resolve: (v) => resolve(v as T), options }),
        );
      }
      if (url.endsWith('/cancel')) {
        const body =
          typeof options?.body === 'string'
            ? (JSON.parse(options.body) as { id?: string })
            : {};
        if (body.id) cancelled.push(body.id);
        const empty: unknown = {};
        return Promise.resolve(empty as T);
      }
      return new Promise<T>((resolve) =>
        statuses.push({ resolve: (v) => resolve(v as T), url }),
      );
    },
  });

  return {
    jobs,
    starts,
    statuses,
    cancelled,
    busy,
    progress,
    setBook: (value: string) => {
      book = value;
    },
  };
}

test('cancelling before start returns cancels the late owned job without polling or publishing', async () => {
  const h = setup();
  const pending = h.jobs.execute({}, { main: true }).catch((error: unknown) => error);
  assert.deepEqual(h.busy, [true]);
  const headers = h.starts[0]?.options?.headers as Record<string, string> | undefined;
  assert.equal(headers?.['X-ExcelLens-Workbook'], 'one');
  h.jobs.cancelMain();
  h.starts[0]?.resolve({ jobId: 'late' });
  const error = await pending;
  await tick();
  assert.ok(error instanceof Error);
  assert.equal(error.name, 'AbortError');
  assert.deepEqual(h.cancelled, ['late']);
  assert.equal(h.statuses.length, 0);
  assert.equal(h.jobs.tasks.size, 0);
  assert.deepEqual(h.busy, [true, false]);
});

test('out-of-order starts and obsolete results cannot finish the newer analysis UI', async () => {
  const h = setup();
  const old = h.jobs.execute({}, { main: true }).catch((error: unknown) => error);
  const fresh = h.jobs.execute({}, { main: true });
  h.starts[1]?.resolve({ jobId: 'new' });
  await tick();
  assert.ok(h.jobs.main);
  h.starts[0]?.resolve({ jobId: 'old' });
  await old;
  assert.equal(h.busy.at(-1), true);
  assert.deepEqual(h.cancelled, ['old']);
  h.statuses[0]?.resolve({ status: 'done', result: { amount: '2' } });
  const result = await fresh;
  assert.equal(result.job, 'new');
  assert.equal(result.result.amount, '2');
  assert.equal(result.current(), true);
  assert.equal(h.busy.at(-1), false);
});

test('cancelled polling ignores an in-flight completion even if the transport ignores abort', async () => {
  const h = setup();
  const old = h.jobs.execute({}, { main: true }).catch((error: unknown) => error);
  h.starts[0]?.resolve({ jobId: 'old' });
  await tick();
  h.jobs.cancelMain();
  const fresh = h.jobs.execute({}, { main: true });
  h.starts[1]?.resolve({ jobId: 'new' });
  await tick();
  h.statuses[0]?.resolve({ status: 'done', result: { old: true } });
  const err = await old;
  assert.ok(err instanceof Error);
  assert.equal(err.name, 'AbortError');
  assert.equal(h.progress.length, 0);
  assert.equal(h.busy.at(-1), true);
  h.statuses[1]?.resolve({ status: 'done', result: { fresh: true } });
  await fresh;
  assert.equal(h.progress.length, 1);
});

test('workbook changes invalidate background jobs and clean up their late responses', async () => {
  const h = setup();
  const pending = h.jobs.execute({}).catch((error: unknown) => error);
  h.starts[0]?.resolve({ jobId: 'dashboard' });
  await tick();
  h.setBook('two');
  h.jobs.reset();
  h.statuses[0]?.resolve({ status: 'done', result: {} });
  const err = await pending;
  assert.ok(err instanceof Error);
  assert.equal(err.name, 'AbortError');
  assert.deepEqual(h.cancelled, ['dashboard']);
  assert.equal(h.jobs.tasks.size, 0);
});

test('malformed start IDs fail before polling or cancelling an unrelated identifier', async () => {
  for (const value of [{}, { jobId: 1 }, { jobId: '' }, { jobId: 'foreign?task' }]) {
    const h = setup();
    const pending = h.jobs.execute({}, { main: true }).catch((error: unknown) => error);
    h.starts[0]?.resolve(value);
    const err = await pending;
    assert.ok(err instanceof Error);
    assert.match(err.message, /标识无效/);
    assert.equal(h.statuses.length, 0);
    assert.deepEqual(h.cancelled, []);
    assert.equal(h.jobs.tasks.size, 0);
    assert.equal(h.busy.at(-1), false);
  }
});

test('unknown states and mismatched response IDs stop unbounded polling and clean the owned task', async () => {
  for (const value of [{}, { status: 'mystery' }, { status: 'done', id: 'unrelated', result: {} }]) {
    const h = setup(0);
    const pending = h.jobs.execute({}, { main: true }).catch((error: unknown) => error);
    h.starts[0]?.resolve({ jobId: 'owned' });
    await tick();
    h.statuses[0]?.resolve(value);
    const err = await pending;
    assert.ok(err instanceof Error);
    assert.match(err.message, /状态响应无效/);
    assert.equal(h.progress.length, 0);
    assert.deepEqual(h.cancelled, ['owned']);
    assert.equal(h.jobs.tasks.size, 0);
    assert.equal(h.busy.at(-1), false);
  }
});
