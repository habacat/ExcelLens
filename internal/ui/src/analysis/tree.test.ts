import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { AnalysisResult, AnalysisTreeStatusResponse } from '../contracts/analysis.ts';
import type { ApiRequestOptions } from '../contracts/common.ts';
import type { AnalysisTreeLimits } from '../contracts/formats.ts';
import {
  AnalysisTree,
  type AnalysisTreeChangeEvent,
  type AnalysisTreeRoot,
} from './tree.ts';
import type { WorkspaceAnalysisQuery } from './results.ts';

const tick = (): Promise<void> =>
  new Promise((resolve) => {
    setImmediate(resolve);
  });

function gate<T>() {
  let resolve: (value: T | PromiseLike<T>) => void = () => undefined;
  const promise = new Promise<T>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

async function until(predicate: () => boolean): Promise<void> {
  for (let i = 0; i < 200; i++) {
    if (predicate()) return;
    await tick();
  }
  throw new Error('model did not settle');
}

interface TreeStartRequest {
  readonly jobId: string;
  readonly path: readonly string[];
  readonly book?: string;
  readonly query?: unknown;
}

interface TreeCall {
  readonly url: string;
  readonly data: Record<string, unknown> | null;
}

function setup(limits: Partial<AnalysisTreeLimits> = {}) {
  const state = { bookKey: 'content', book: 'path', opened: true };
  const calls: TreeCall[] = [];
  const started = new Map<string, TreeStartRequest>();
  const messages: string[] = [];
  const changes: AnalysisTreeChangeEvent[] = [];

  const query: WorkspaceAnalysisQuery = {
    source: 'wb:来源',
    layout: 'tree',
    rows: ['C1', 'C2', 'C3'],
    columns: ['C4'],
    aggregate: 'sum',
    value: 'C5',
  };
  const result: AnalysisResult = {
    tree: { path: [], levels: 3, hasChildren: true },
    rowFields: ['C1'],
    columnKeys: ['', 'X'],
    totals: [],
    grand: { exact: '9007199254740993.31', display: '9007199254740993.31', count: 4 },
    scanned: 4,
    included: 4,
    invalidNumbers: 0,
    missingFormulaCaches: 0,
    aggregation: 'sum',
    rows: ['甲', '乙', '丙', '丁'].map((value) => ({
      keys: [value],
      values: [],
      total: { exact: '1', display: '1', count: 1 },
      count: 1,
    })),
  };
  const root: AnalysisTreeRoot = {
    bookKey: state.bookKey,
    book: state.book,
    job: 'root',
    query,
    result,
  };

  const reply = (
    request: TreeStartRequest,
    overrides: Partial<AnalysisTreeStatusResponse> = {},
  ): AnalysisTreeStatusResponse => ({
    status: 'done',
    stage: '完成',
    progress: 2,
    total: 2,
    error: '',
    rootJobId: request.jobId,
    path: [...request.path],
    result: {
      tree: { path: [...request.path], levels: 3, hasChildren: request.path.length + 1 < 3 },
      rowFields: query.rows.slice(0, request.path.length + 1),
      columnKeys: ['X'],
      totals: [],
      scanned: 2,
      included: 2,
      invalidNumbers: 0,
      missingFormulaCaches: 0,
      aggregation: 'sum',
      rows: ['1', '001'].map((value) => ({
        keys: [...request.path, value],
        values: [],
        total: { exact: '1', display: '1', count: 1 },
        count: 1,
      })),
      grand: { exact: '2', display: '2', count: 2 },
    },
    ...overrides,
  });

  let next = 0;
  let handler: ((url: string, data: Record<string, unknown> | null) => unknown) | undefined;

  const api = async <T = unknown>(url: string, options?: ApiRequestOptions): Promise<T> => {
    const data =
      typeof options?.body === 'string'
        ? (JSON.parse(options.body) as Record<string, unknown>)
        : null;
    calls.push({ url, data });
    if (handler) {
      const value = handler(url, data);
      if (value !== undefined) return (await Promise.resolve(value)) as T;
    }
    if (url.endsWith('/start')) {
      const id = `child-${++next}`;
      if (data) {
        started.set(id, {
          jobId: typeof data.jobId === 'string' ? data.jobId : '',
          path: Array.isArray(data.path) ? data.path.map(String) : [],
          book: typeof data.book === 'string' ? data.book : undefined,
        });
      }
      const res: unknown = { jobId: id };
      return res as T;
    }
    if (url.includes('/status?')) {
      const id = new URL(url, 'http://local').searchParams.get('id') ?? '';
      const req = started.get(id);
      assert.ok(req);
      return reply(req) as T;
    }
    const empty: unknown = {};
    return empty as T;
  };

  const tree = new AnalysisTree({
    api,
    context: () => state,
    limits: { nodes: 8, bytes: 100_000, pageRows: 2, active: 2, ...limits },
    sleep: tick,
    onChange: (event) => changes.push(event),
    onMessage: (message) => messages.push(message),
  });
  tree.setRoot(root);

  return {
    tree,
    state,
    root,
    query,
    result,
    calls,
    messages,
    changes,
    reply,
    started,
    setHandler: (fn: typeof handler) => {
      handler = fn;
    },
  };
}

test('collapsed branches reuse bounded data while root totals and source queries remain immutable', async () => {
  const h = setup();
  const node = h.tree.expand(['甲']);
  assert.ok(node);
  await until(() => node.phase === 'done' && !node.running);
  assert.equal(h.tree.nodes.size, 1);
  assert.equal(h.calls.filter((c) => c.url.endsWith('/start')).length, 1);
  assert.equal(h.calls.filter((c) => c.url.endsWith('/clear')).length, 1);
  assert.equal(JSON.stringify(node.result?.rows[1]?.keys), '["甲","001"]');

  h.tree.collapse(['甲']);
  h.tree.expand(['甲']);
  await tick();
  assert.equal(h.calls.filter((c) => c.url.endsWith('/start')).length, 1);
  assert.equal(h.result.grand.exact, '9007199254740993.31');
  assert.equal(h.query.sourceId, undefined);
  assert.equal(h.calls[0]?.data?.jobId, 'root');
  assert.equal(h.calls[0].data.book, 'path');
  assert.equal(h.calls[0].data.query, undefined);
});

test('cancel then immediate retry keeps the newer node intent when a start ID arrives late', async () => {
  const h = setup();
  const held = gate<{ jobId: string }>();
  let starts = 0;
  h.setHandler((url) => {
    if (url.endsWith('/start') && starts++ === 0) return held.promise;
    return undefined;
  });
  const node = h.tree.expand(['甲']);
  assert.ok(node);
  await tick();
  h.tree.cancel(['甲']);
  h.tree.expand(['甲']);
  held.resolve({ jobId: 'late-cancelled' });
  await until(() => node.phase === 'done' && !node.running);
  assert.equal(starts, 2);
  assert.equal(node.error, '');
  assert.ok(h.calls.some((c) => c.url.endsWith('/clear') && c.data?.id === 'late-cancelled'));
  assert.equal(h.tree.active, 0);
});

test('old root completion cannot publish and its exit wakes the new root queue', async () => {
  const h = setup({ active: 1 });
  const held = gate<{ jobId: string }>();
  let first = true;
  h.setHandler((url) => {
    if (url.endsWith('/start') && first) {
      first = false;
      return held.promise;
    }
    return undefined;
  });
  h.tree.expand(['甲']);
  await tick();
  h.tree.setRoot({ ...h.root, job: 'new-root' });
  const current = h.tree.expand(['乙']);
  assert.ok(current);
  assert.equal(h.tree.active, 1);
  assert.equal(current.phase, 'queued');
  held.resolve({ jobId: 'old-root-late' });
  await until(() => current.phase === 'done' && !current.running);
  assert.equal(h.tree.get(['甲']), null);
  assert.equal(h.tree.root?.job, 'new-root');
  assert.ok(h.calls.some((c) => c.url.endsWith('/clear') && c.data?.id === 'old-root-late'));
  assert.equal(h.tree.nodes.size, 1);
});

test('same content at a different path invalidates the old source without keeping a late response', async () => {
  const h = setup();
  const held = gate<{ jobId: string }>();
  h.setHandler((url) => (url.endsWith('/start') ? held.promise : undefined));
  const node = h.tree.expand(['甲']);
  assert.ok(node);
  h.state.book = 'another-path';
  held.resolve({ jobId: 'old-path' });
  await until(() => h.tree.active === 0);
  assert.equal(node.result, null);
  assert.ok(h.calls.some((c) => c.url.endsWith('/clear') && c.data?.id === 'old-path'));
});

test('client queue bounds simultaneous tasks and cancelling a waiting branch never starts it', async () => {
  const h = setup({ active: 1 });
  const held = gate<{ jobId: string }>();
  let first = true;
  h.setHandler((url) => {
    if (url.endsWith('/start') && first) {
      first = false;
      return held.promise;
    }
    return undefined;
  });
  const a = h.tree.expand(['甲']);
  const b = h.tree.expand(['乙']);
  const c = h.tree.expand(['丙']);
  assert.ok(a && b && c);
  h.tree.cancel(['丙']);
  held.resolve({ jobId: 'first' });
  h.started.set('first', { jobId: 'root', path: ['甲'] });
  await until(() => a.phase === 'done' && b.phase === 'done' && h.tree.active === 0);
  assert.equal(c.phase, 'idle');
  assert.equal(h.calls.filter((call) => call.url.endsWith('/start')).length, 2);
  assert.equal(h.tree.diagnostics().queued, 0);
});

test('LRU eviction removes a whole subtree without changing its root result', async () => {
  const h = setup({ nodes: 3 });
  const a = h.tree.expand(['甲']);
  assert.ok(a);
  await until(() => a.phase === 'done');
  const leaf = h.tree.expand(['甲', '1']);
  assert.ok(leaf);
  await until(() => leaf.phase === 'done');
  const b = h.tree.expand(['乙']);
  assert.ok(b);
  await until(() => b.phase === 'done');
  const c = h.tree.expand(['丙']);
  assert.ok(c);
  await until(() => c.phase === 'done');
  assert.equal(h.tree.get(['甲']), null);
  assert.equal(h.tree.get(['甲', '1']), null);
  assert.ok(h.tree.nodes.size <= 3);
  assert.equal(h.result.rows.length, 4);
  assert.equal(h.result.grand.exact, '9007199254740993.31');
});

test('oversized results and mismatched response ownership become explicit branch errors', async () => {
  for (const kind of ['size', 'owner', 'path', 'column'] as const) {
    const h = setup({ bytes: 6000 });
    h.setHandler((url) => {
      if (!url.includes('/status?')) return undefined;
      const request = [...h.started.values()][0];
      assert.ok(request);
      const base = h.reply(request);
      assert.ok(base.result);
      if (kind === 'size') {
        return {
          ...base,
          result: {
            ...base.result,
            rows: [
              {
                keys: [request.path[0] ?? '甲', '大'.repeat(10_000)],
                values: [],
                total: { exact: '1', display: '1', count: 1 },
                count: 1,
              },
              ...base.result.rows.slice(1),
            ],
          },
        };
      }
      if (kind === 'owner') return { ...base, rootJobId: 'other' };
      if (kind === 'path') {
        return {
          ...base,
          result: {
            ...base.result,
            tree: { path: ['乙'], levels: 3, hasChildren: true },
          },
        };
      }
      return { ...base, result: { ...base.result, columnKeys: ['unexpected'] } };
    });
    const node = h.tree.expand(['甲']);
    assert.ok(node);
    await until(() => !node.running && !node.pending);
    assert.equal(node.result, null);
    assert.equal(node.phase, 'error');
    assert.match(node.error, kind === 'size' ? /缓存容量/ : /不一致/);
    assert.ok(h.tree.bytes <= 6000);
    assert.ok(h.calls.some((c) => c.url.endsWith('/clear')));
  }
});

test('pages enumerate only loaded branches and collapsing cancels work without querying more source rows', async () => {
  const h = setup();
  const first = h.tree.pageRows();
  assert.equal(first.rows.length, 2);
  assert.equal(first.more, true);
  assert.equal(h.calls.length, 0);

  h.tree.page = 1;
  const page1First = h.tree.pageRows().rows[0];
  assert.ok(page1First);
  assert.ok(page1First.kind === 'row');
  assert.equal(page1First.row.keys[0], '丙');

  const node = h.tree.expand(['甲']);
  assert.ok(node);
  await until(() => node.phase === 'done');
  assert.equal(h.tree.page, 0);
  assert.equal([...h.tree.rows()].length, 6);

  h.tree.collapseAll();
  assert.equal([...h.tree.rows()].length, 4);
  assert.equal(h.calls.filter((c) => c.url.endsWith('/start')).length, 1);
});
