import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { SheetViewportResponse } from '../contracts/workbook.ts';
import { borderRuns } from './grid-borders.ts';
import { MergedCells } from './merges.ts';
import type { ViewportRequest } from './state.ts';
import { TileCache } from './viewport-cache.ts';
import { ViewportScheduler, type SchedulerItem } from './viewport-scheduler.ts';
import { ScrollMotion, VirtualScroll } from './virtual-scroll.ts';

const turn = (): Promise<void> =>
  new Promise((resolve) => {
    setImmediate(resolve);
  });

function makeRequest(overrides: Partial<ViewportRequest> = {}): ViewportRequest {
  return {
    context: 'one',
    filtered: false,
    filterId: '',
    viewId: '',
    excludeThroughRow: 0,
    displayStart: 1,
    displayEnd: 3,
    visibleStart: 1,
    visibleEnd: 3,
    visibleColStart: 1,
    visibleColEnd: 3,
    row: 1,
    offset: 0,
    limit: 3,
    startCol: 1,
    cols: 3,
    selectedCols: null,
    leadRow: 0,
    leadCol: 0,
    ...overrides,
  };
}

function makeResponse(overrides: Partial<SheetViewportResponse> = {}): SheetViewportResponse {
  return {
    sheet: 'S1',
    offset: 0,
    limit: 3,
    totalRows: 3,
    filtered: false,
    maxRow: 3,
    maxCol: 3,
    startCol: 1,
    colCount: 3,
    rows: [],
    ...overrides,
  };
}

test('a press on a cached style tile resolves hidden bands, merge anchors and sorted source rows before DOM commit', () => {
  const cache = new TileCache(10000);
  const req = makeRequest();
  const data = makeResponse({
    rows: [
      {
        number: 1,
        cells: [{ ref: 'A1', col: 1, raw: 'anchor', display: 'anchor', hasFormula: false }],
      },
    ],
  });
  cache.store('plain', data, req);
  // The second row and column are hidden. Their far edge belongs to the third band.
  const edges = [0, 0, 11, 11, 25];
  const edge = (i: number): number => edges[i] ?? 25;
  const point = {
    x: 11,
    y: 11,
    lastCol: 3,
    lastRow: 3,
    rowTop: edge,
    colLeft: edge,
    mergeAt: () => null,
  };
  const empty = cache.sourceAtPoint(req, point);
  assert.ok(empty);
  assert.deepEqual([empty.row, empty.col, empty.cell], [3, 3, undefined]);

  const merged = cache.sourceAtPoint(req, {
    ...point,
    mergeAt: () => ({ row: 1, col: 1, endRow: 3, endCol: 3 }),
  });
  assert.ok(merged);
  assert.deepEqual(
    [merged.row, merged.col, merged.cell?.raw, merged.crosshair?.row, merged.crosshair?.col],
    [1, 1, 'anchor', 3, 3],
  );
  assert.equal(cache.sourceAtPoint(req, { ...point, x: 25 }), null);
  assert.equal(cache.sourceAtPoint({ ...req, context: 'another revision' }, point), null);

  const sorted = makeRequest({ filtered: true, filterId: 'sort', displayEnd: 2, limit: 2 });
  cache.store(
    'sorted',
    makeResponse({
      filtered: true,
      rows: [
        { number: 84, cells: [] },
        {
          number: 17,
          cells: [{ ref: 'C17', col: 3, raw: '0017', display: '0017', hasFormula: false }],
        },
      ],
    }),
    sorted,
  );
  const hit = cache.sourceAtPoint(sorted, {
    ...point,
    y: 12,
    lastRow: 2,
    rowTop: (i) => (i - 1) * 10,
  });
  assert.ok(hit);
  assert.deepEqual([hit.row, hit.col, hit.visual, hit.cell?.raw], [17, 3, 2, '0017']);
});

test('adjacent row and column tiles join by sorted rank, preserve empty cells and reject gaps or a new revision', () => {
  const cache = new TileCache(100000);
  const base = makeRequest({
    displayStart: 1,
    displayEnd: 3,
    filtered: true,
    filterId: 'sort',
    viewId: 'visible',
    context: 'rev1',
    excludeThroughRow: 2,
    startCol: 2,
    cols: 2,
    selectedCols: [2, 7],
  });
  for (const [r1, r2] of [
    [1, 2],
    [3, 3],
  ] as const) {
    for (const c of [2, 7]) {
      const request = {
        ...base,
        displayStart: r1,
        displayEnd: r2,
        startCol: c,
        cols: 1,
        selectedCols: [c],
      };
      const rows = [14, 9, 280].slice(r1 - 1, r2).map((n) => ({
        number: n,
        cells:
          n === 9 && c === 2
            ? []
            : [
                {
                  ref: `C${n}`,
                  col: c,
                  raw: `00${n}`,
                  display: `00${n}`,
                  formula: c === 7 ? 'B7' : '',
                  hasFormula: c === 7,
                  style: 4,
                },
              ],
      }));
      cache.store(
        `${r1}-${c}`,
        makeResponse({
          filtered: true,
          startCol: c,
          colCount: 1,
          columns: [c],
          rows,
          totalRows: 3,
        }),
        request,
      );
    }
  }
  const result = cache.project(base, [1, 2, 3]);
  assert.ok(result);
  assert.deepEqual(
    Array.from(result.data.rows, (r) => [r.number, r.cells.map((c) => c.col).join(',')]),
    [
      [14, '2,7'],
      [9, '7'],
      [280, '2,7'],
    ],
  );
  assert.equal(result.data.rows[2]?.cells[0]?.raw, '00280');
  assert.equal(result.data.rows[0]?.cells[1]?.formula, 'B7');
  assert.equal(cache.reader(base)(2, 2)?.cell, undefined);
  assert.equal(cache.reader(base)(2, 4), null);
  assert.equal(cache.project({ ...base, selectedCols: [2, 4, 7] }, [1, 2, 3]), null);
  assert.equal(cache.project({ ...base, context: 'rev2' }, [1, 2, 3]), null);
  assert.equal(cache.project({ ...base, excludeThroughRow: 1 }, [1, 2, 3]), null);
});

test('reversal promotes an outstanding prediction and distant demand has a reserved slot', async () => {
  interface ReadEntry {
    readonly key: string;
    readonly signal: AbortSignal;
    readonly ok: (value: string) => void;
  }
  const reads: ReadEntry[] = [];
  const sched = new ViewportScheduler<SchedulerItem, string>(
    (job, signal) =>
      new Promise<string>((ok, no) => {
        reads.push({ key: job.key, signal, ok });
        signal.addEventListener('abort', () => no(signal.reason as Error), { once: true });
      }),
  );
  const tile = (key: string): SchedulerItem => ({ key });
  await sched.plan([], ['ahead', 'other', 'queued'].map(tile));
  await turn();
  assert.deepEqual(
    reads.map((r) => r.key),
    ['ahead', 'other'],
  );
  const p = sched.plan(['ahead', 'jump'].map(tile), []);
  await turn();
  assert.equal(reads.length, 3);
  assert.equal(reads[0]?.signal.aborted, false);
  reads[0].ok('retained');
  reads[2]?.ok('arrived');
  assert.deepEqual(Array.from(await p), ['retained', 'arrived']);
  sched.reset();
  await turn();
  assert.equal(sched.stats.peak, 3);
  assert.equal(sched.active.size, 0);
});

test('queued old-generation work cannot start or strand a new job with the same key after reset', async () => {
  interface ReadEntry {
    readonly key: string;
    readonly signal: AbortSignal;
    readonly ok: (value: string) => void;
  }
  const reads: ReadEntry[] = [];
  const s = new ViewportScheduler<SchedulerItem, string>(
    (job, signal) =>
      new Promise<string>((ok) => {
        reads.push({ key: job.key, signal, ok });
      }),
    1,
  );
  const first = s.plan([{ key: 'same' }, { key: 'obsolete' }], []);
  await turn();
  s.reset();
  const next = s.plan([{ key: 'same' }], []);
  reads[0]?.ok('stale');
  await first;
  await turn();
  assert.deepEqual(
    reads.map((j) => j.key),
    ['same', 'same'],
  );
  reads[1]?.ok('fresh');
  assert.deepEqual(Array.from(await next), ['fresh']);
});

test('prediction uses logical scroll distance, reverses immediately and stays capped after slow reads', () => {
  const s = new VirtualScroll(1000);
  s.configure(100000, 100);
  const m = new ScrollMotion();
  m.sample(0, s.logical(0), 0);
  m.sample(30, s.logical(8), 16);
  let p = m.predict(300, 200, 17);
  assert.ok(p.y[1] > p.y[0] && p.x[1] > p.x[0]);
  m.observe('read', 4000);
  m.sample(10, s.logical(1), 32);
  p = m.predict(300, 200, 33);
  assert.ok(p.y[0] > p.y[1] && p.x[0] > p.x[1]);
  assert.ok(p.y[0] < 200 * 4);
  assert.equal(m.predict(300, 200, 600).y[0], m.predict(300, 200, 600).y[1]);
});

test('an offscreen merge uses bounded perimeter work regardless of anchor distance', () => {
  const m = new MergedCells();
  m.load([{ row: 1, col: 1, endRow: 900000, endCol: 1200 }]);
  assert.equal(m.intersect(800000, 800020, 500, 512).length, 1);
  let probes = 0;
  const edges = borderRuns(
    1,
    900000,
    800020,
    (i) => i,
    () => {
      probes++;
      return '1px solid red';
    },
    '',
    800000,
  );
  assert.equal(probes, 21);
  assert.ok(Array.isArray(edges));
  assert.equal(edges.length, 3);
});
