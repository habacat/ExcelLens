import { test } from 'node:test';
import assert from 'node:assert/strict';
import type {
  AnalysisCell,
  ComparisonSnapshot,
  SnapshotComparison,
  SnapshotDelta,
} from '../contracts/analysis.ts';
import type { ApiRequestOptions, HttpError } from '../contracts/common.ts';
import type { SnapshotLimits } from '../contracts/formats.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import { SnapshotModel } from './snapshot-model.ts';
import { SnapshotsController } from './snapshots.ts';

function deferred<T>() {
  let resolve: (value: T | PromiseLike<T>) => void = () => undefined;
  let reject: (reason?: unknown) => void = () => undefined;
  const promise = new Promise<T>((a, b) => {
    resolve = a;
    reject = b;
  });
  return { promise, resolve, reject };
}

const limits: SnapshotLimits = {
  entries: 24,
  rows: 200,
  series: 12,
  bytes: 1 << 20,
  storeBytes: 16 << 20,
  pageRows: 40,
};

const cell = (exact: string): AnalysisCell => ({
  exact,
  display: exact,
  count: exact ? 1 : 0,
});

function snapshot(id: string): ComparisonSnapshot {
  const big = cell('9007199254740993.31');
  return {
    version: 1,
    id,
    name: `快照 ${id}`,
    file: '<原文件>.xlsx',
    created: '2026-11-17T00:00:00Z',
    analysisAt: '2026-11-17T00:00:00Z',
    workbookKey: 'content',
    book: 'path',
    sourceStamp: 'stamp-1',
    metricIndex: 0,
    metric: { field: 'C2', aggregate: 'sum' },
    fields: [{ id: 'C2', name: '金额', column: 2 }],
    query: { source: 'wb:原表', rows: [], aggregate: 'sum' },
    result: {
      rowFields: [],
      columnKeys: ['金额'],
      rows: [{ keys: ['001'], values: [big], total: big, count: 1 }],
      totals: [big],
      grand: big,
      scanned: 1,
      included: 1,
      invalidNumbers: 0,
      missingFormulaCaches: 0,
      aggregation: 'sum',
    },
  };
}

function comparison(left: ComparisonSnapshot, right: ComparisonSnapshot): SnapshotComparison {
  const delta: SnapshotDelta = {
    left: cell('1'),
    right: cell('2'),
    difference: cell('1'),
    change: { mode: 'diff', display: '100.00%', exact: '1', percent: true },
  };
  return {
    leftId: left.id,
    rightId: right.id,
    columnKeys: ['金额'],
    totals: [delta],
    rows: [{ keys: ['001'], values: [delta], total: delta }],
    grand: delta,
  };
}

function harness() {
  const dom = installDom();
  const draws: (readonly [
    HTMLCanvasElement,
    readonly (AnalysisCell | null | undefined)[],
    readonly (AnalysisCell | null | undefined)[] | undefined,
  ])[] = [];
  const messages: (readonly [string, string | undefined])[] = [];

  let apiImpl: (url: string, opts?: ApiRequestOptions) => Promise<unknown> = async () => {
    throw new Error('Unexpected request');
  };

  const controller = new SnapshotsController({
    api: async <T = unknown>(url: string, opts?: ApiRequestOptions): Promise<T> =>
      (await apiImpl(url, opts)) as T,
    limits,
    toast: (msg, tone) => {
      messages.push([msg, tone]);
    },
    drawSparkline: (canvas, cells, domain) => {
      draws.push([canvas, cells, domain]);
    },
  });

  controller.dialog.open = true;
  controller.store = {
    version: 1,
    revision: 4,
    items: [
      { id: 'a', name: '基准', file: '原表', created: '2026-11-17T00:00:00Z' },
      { id: 'b', name: '对比', file: '原表', created: '2026-11-17T00:00:00Z' },
    ],
  };
  const leftSel = document.getElementById('snapshotsLeft');
  const rightSel = document.getElementById('snapshotsRight');
  assert.ok(leftSel instanceof HTMLSelectElement && rightSel instanceof HTMLSelectElement);
  leftSel.value = 'a';
  rightSel.value = 'b';

  const defaultApi = async (url: string): Promise<unknown> => {
    if (url.includes('/compare?')) {
      const p = new URL(`http://local${url}`).searchParams;
      return comparison(snapshot(p.get('left') ?? ''), snapshot(p.get('right') ?? ''));
    }
    if (url.includes('?id=')) {
      return snapshot(decodeURIComponent(url.split('=')[1] ?? ''));
    }
    return controller.store;
  };
  apiImpl = defaultApi;

  return {
    controller,
    draws,
    messages,
    defaultApi,
    setApi: (fn: typeof apiImpl) => {
      apiImpl = fn;
    },
    restore: () => dom.restore(),
  };
}

test('snapshot model retains exact strings and refuses wrong identities, shapes and capacity', () => {
  const s = snapshot('a');
  assert.equal(SnapshotModel.snapshot(s, 'a', limits).result.grand.exact, '9007199254740993.31');
  assert.throws(() => SnapshotModel.snapshot(s, 'b', limits));
  const row0 = s.result.rows[0];
  assert.ok(row0);
  assert.throws(() =>
    SnapshotModel.snapshot({ ...s, result: { ...s.result, rows: Array(201).fill(row0) } }, 'a', limits),
  );
  assert.throws(() =>
    SnapshotModel.snapshot({ ...s, result: { ...s.result, grand: { exact: 123, display: '123', count: 1 } } }, 'a', limits),
  );
  assert.throws(() => SnapshotModel.comparison(comparison(s, snapshot('b')), s, snapshot('c'), limits));
  assert.equal(SnapshotModel.metric(s), '金额 求和');
});

test('a late pair cannot replace the newly selected complete comparison', async () => {
  const { controller: c, defaultApi, setApi, restore } = harness();
  try {
    const gate = deferred<ComparisonSnapshot>();
    setApi((url) => (url.endsWith('?id=a') ? gate.promise : defaultApi(url)));
    const first = c.loadPair();
    const leftSel = document.getElementById('snapshotsLeft');
    const rightSel = document.getElementById('snapshotsRight');
    assert.ok(leftSel instanceof HTMLSelectElement && rightSel instanceof HTMLSelectElement);
    leftSel.value = 'c';
    rightSel.value = 'd';
    await c.loadPair();
    const accepted = c.accepted;
    assert.ok(accepted);
    assert.equal(accepted.left.id, 'c');

    gate.resolve(snapshot('a'));
    await first;
    assert.equal(c.accepted, accepted);
    assert.equal(c.accepted.comparison?.rightId, 'd');
  } finally {
    restore();
  }
});

test('read failure retains previous accepted results and stale pair data is never shown as current', async () => {
  const { controller: c, setApi, restore } = harness();
  try {
    await c.loadPair();
    const old = c.accepted;
    setApi(async () => {
      throw new Error('读取失败');
    });
    const rightSel = document.getElementById('snapshotsRight');
    assert.ok(rightSel instanceof HTMLSelectElement);
    rightSel.value = 'c';
    await c.loadPair();
    assert.equal(c.accepted, old);
    assert.match(document.getElementById('snapshotsStatus')?.textContent ?? '', /读取失败.*保留上次/);
  } finally {
    restore();
  }
});

test('a save conflict preserves user name, accepted results and expected revision', async () => {
  const { controller: c, setApi, restore } = harness();
  try {
    await c.loadPair();
    const old = c.accepted;
    let request: Record<string, unknown> | undefined;
    c.capture = {
      job: 'finished',
      metric: 1,
      workbookKey: 'content',
      book: 'path',
      title: '月末比较',
      current: () => true,
    };
    const nameInput = document.getElementById('snapshotsName');
    assert.ok(nameInput instanceof HTMLInputElement);
    nameInput.value = '月末比较';
    setApi(async (_url, opts) => {
      request =
        typeof opts?.body === 'string'
          ? (JSON.parse(opts.body) as Record<string, unknown>)
          : undefined;
      const err: HttpError = new Error('目录已变化，请刷新');
      err.status = 409;
      throw err;
    });
    await c.save();
    assert.equal(c.accepted, old);
    assert.equal(c.store?.revision, 4);
    assert.equal(nameInput.value, '月末比较');
    assert.equal(request?.jobId, 'finished');
    assert.equal(request.metricIndex, 1);
    assert.equal(request.expectedRevision, 4);
    assert.ok(!('result' in request));
    assert.equal(c.busy, false);
  } finally {
    restore();
  }
});

test('closing and reopening ignores late catalog and late committed-save callbacks', async () => {
  const { controller: c, defaultApi, setApi, restore } = harness();
  try {
    const readGate = deferred<{ version: number; revision: number; items: [] }>();
    const writeGate = deferred<{ version: number; revision: number; items: [] }>();
    setApi(() => readGate.promise);
    const oldRead = c.refresh();
    c.close();
    c.dialog.open = true;
    setApi(defaultApi);
    c.store = { version: 1, revision: 4, items: [] };
    await c.refresh();
    const current = c.store;
    readGate.resolve({ version: 1, revision: 1, items: [] });
    await oldRead;
    assert.equal(c.store, current);

    c.capture = {
      job: 'job',
      metric: 0,
      workbookKey: 'content',
      book: 'path',
      title: '新快照',
      current: () => true,
    };
    const nameInput = document.getElementById('snapshotsName');
    assert.ok(nameInput instanceof HTMLInputElement);
    nameInput.value = '新快照';
    setApi(() => writeGate.promise);
    const saving = c.save();
    c.close();
    c.dialog.open = true;
    setApi(defaultApi);
    c.store = current;
    const epoch = c.epoch;
    writeGate.resolve({ version: 1, revision: 5, items: [] });
    await saving;
    assert.equal(c.epoch, epoch);
    assert.equal(c.store, current);
  } finally {
    restore();
  }
});

test('series/page/size redraws are local and both mini trends share the aligned domain', async () => {
  const { controller: c, draws, setApi, restore } = harness();
  try {
    await c.loadPair();
    setApi(() => {
      throw new Error('Redraw must not make a request');
    });
    const seriesSel = document.getElementById('snapshotsSeries');
    assert.ok(seriesSel instanceof HTMLSelectElement);
    seriesSel.value = '0';
    c.renderRows();
    c.draw();
    c.draw();
    const last = draws.slice(-2);
    assert.equal(last[0]?.[2], last[1]?.[2]);
    assert.equal(last[0]?.[1][0]?.exact, '1');
    assert.equal(last[1]?.[1][0]?.exact, '2');
    assert.equal(document.getElementById('snapshotsPage')?.textContent, '1 / 1');
  } finally {
    restore();
  }
});

test('delete requires a second deliberate action for the same selected snapshot', async () => {
  const { controller: c, restore } = harness();
  try {
    let count = 0;
    let request: Record<string, unknown> | undefined;
    c.write = async (r) => {
      count++;
      request = r;
    };
    await c.remove('Left');
    assert.equal(count, 0);
    assert.equal(document.getElementById('snapshotsDeleteLeft')?.textContent, '确认删除');

    const rightSel = document.getElementById('snapshotsRight');
    const leftSel = document.getElementById('snapshotsLeft');
    assert.ok(rightSel instanceof HTMLSelectElement && leftSel instanceof HTMLSelectElement);
    rightSel.value = 'a';
    await c.remove('Right');
    assert.equal(
      count,
      0,
      'the other side needs its own confirmation even for the same snapshot',
    );
    assert.equal(document.getElementById('snapshotsDeleteLeft')?.textContent, '删除');

    leftSel.value = 'c';
    await c.remove('Left');
    assert.equal(count, 0);
    await c.remove('Left');
    assert.equal(count, 1);
    assert.equal(request?.id, 'c');
  } finally {
    restore();
  }
});
