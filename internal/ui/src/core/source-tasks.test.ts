import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { ApiClient, ApiRequestOptions } from '../contracts/common.ts';
import { SourceTasks, type SourceTaskContext } from './source-tasks.ts';

function deferred<T>() {
  let resolve: (value: T) => void = () => undefined;
  const promise = new Promise<T>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

interface RecordedCall {
  readonly path: string;
  readonly request: Record<string, unknown> | null;
}

function setup() {
  const context: { bookKey: string; book: string; opened: boolean } = {
    bookKey: 'fingerprint',
    book: 'path-book',
    opened: true,
  };
  const calls: RecordedCall[] = [];
  let handler: ApiClient = async <T = unknown>(
    path: string,
    options?: ApiRequestOptions,
  ): Promise<T> => {
    calls.push({
      path,
      request:
        typeof options?.body === 'string'
          ? (JSON.parse(options.body) as Record<string, unknown>)
          : null,
    });
    if (path.endsWith('/start')) {
      const res: unknown = { jobId: 'job' };
      return res as T;
    }
    if (path.includes('/status')) {
      const res: unknown = { status: 'done' };
      return res as T;
    }
    if (path.includes('/result')) {
      const res: unknown = {
        id: 'job',
        source: 'append:digest',
        workbookKey: context.bookKey,
        book: context.book,
      };
      return res as T;
    }
    const empty: unknown = {};
    return empty as T;
  };
  const task = new SourceTasks({
    context: (): SourceTaskContext => context,
    sleep: async () => undefined,
    api: <T = unknown>(path: string, options?: ApiRequestOptions) => handler<T>(path, options),
  });
  return {
    task,
    context,
    calls,
    getApi: () => handler,
    setApi: (next: ApiClient) => {
      handler = next;
    },
  };
}

test('cancelled delayed starts observe and release their exact token', async () => {
  const h = setup();
  const gate = deferred<{ jobId: string }>();
  const base = h.getApi();
  h.setApi(<T = unknown>(path: string, opts?: ApiRequestOptions) =>
    path.endsWith('/start') ? (gate.promise as Promise<T>) : base<T>(path, opts),
  );
  const pending = h.task.run({ mode: 'append' });
  h.task.reset();
  gate.resolve({ jobId: 'late' });
  await assert.rejects(pending, { name: 'AbortError' });
  assert.deepEqual(
    h.calls.map((c) => c.request?.id),
    ['late'],
  );
  assert.equal(h.task.pending.size, 0);
});

test('accepted tasks transfer ownership; stale identity and result mismatch clear it', async () => {
  const h = setup();
  const result = await h.task.run({ mode: 'append' });
  assert.equal(result.id, 'job');
  assert.equal(
    h.calls.some((c) => c.path.endsWith('/clear')),
    false,
  );
  const base = h.getApi();
  h.setApi(async <T = unknown>(path: string, opts?: ApiRequestOptions) => {
    const value = await base<T>(path, opts);
    if (path.includes('/result')) {
      h.context.book = 'different-path';
    }
    return value;
  });
  await assert.rejects(h.task.run({ mode: 'append' }), { name: 'AbortError' });
  assert.equal(h.calls.at(-1)?.request?.id, 'job');
});

test('server errors release failed jobs and leave other successful jobs alone', async () => {
  const h = setup();
  const base = h.getApi();
  h.setApi(async <T = unknown>(path: string, opts?: ApiRequestOptions) => {
    if (path.includes('/status')) {
      const res: unknown = { status: 'error', error: '源文件已改变' };
      return res as T;
    }
    return base<T>(path, opts);
  });
  await assert.rejects(h.task.run({ mode: 'append' }), /改变/);
  assert.equal(h.calls.at(-1)?.request?.id, 'job');
});
