import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { SheetViewportResponse } from '../contracts/workbook.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import {
  GridNavigation,
  makeCellData,
  makeDefaultNavigationDeps,
  makeDefaultViewportPipelineDeps,
  makeRowViewState,
  ViewportPipeline,
} from '../test-helpers/grid-fixture.ts';
import { MergedCells, type MergedCellElement } from './merges.ts';
import { hyperlinkVisitKey, revealCellOffset } from './navigation.ts';
import {
  createInitialWorkbookState,
  type GridCellElement,
  type GridRowElement,
  type ViewportRequest,
} from './state.ts';
import {
  viewportBounds,
  viewportCoversRequest,
  viewportCoversVisible,
  viewportHasHeadroom,
  viewportKey,
} from './viewport-cache.ts';

function makeRequest(overrides: Partial<ViewportRequest> = {}): ViewportRequest {
  return {
    context: '',
    filtered: false,
    filterId: '',
    viewId: '',
    excludeThroughRow: 0,
    displayStart: 1,
    displayEnd: 2,
    visibleStart: 1,
    visibleEnd: 2,
    visibleColStart: 1,
    visibleColEnd: 1,
    row: 1,
    offset: 0,
    limit: 2,
    startCol: 1,
    cols: 1,
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
    limit: 2,
    totalRows: 2,
    filtered: false,
    maxRow: 2,
    maxCol: 3,
    startCol: 1,
    colCount: 1,
    rows: [],
    ...overrides,
  };
}

test('a clickable first preview paints the same single-cell frame as the indexed sheet', () => {
  const dom = installDom({ fromIndexHtml: true });
  try {
    const rawGrid = dom.document.getElementById('grid');
    assert.ok(rawGrid);
    rawGrid.clientWidth = 500;
    rawGrid.clientHeight = 300;
    const region = document.createElement('div');
    region.className = 'grid-region';
    const layer = document.getElementById('rangeFrames');
    assert.ok(layer);
    region.appendChild(layer);
    document.body.appendChild(region);

    const S = createInitialWorkbookState(1_048_576, 16_384);
    S.previewOnly = true;
    S.indexReady = false;
    S.selected = {
      sheet: 'S1',
      row: 3,
      col: 2,
      ref: 'B3',
      raw: '',
      display: '',
      hasFormula: false,
    };
    S.selectedVisualIndex = 3;

    const pipeline = new ViewportPipeline(makeDefaultViewportPipelineDeps({ S }));
    pipeline.paintRangeFrames();
    assert.equal(region.classList.contains('has-cell-selection'), true);
    const previewHtml = [...layer.children].map((n) => (n as HTMLElement).style.cssText);
    assert.ok(previewHtml.length > 0);

    S.previewOnly = false;
    S.indexReady = true;
    pipeline.paintRangeFrames();
    const indexedHtml = [...layer.children].map((n) => (n as HTMLElement).style.cssText);
    assert.deepEqual(indexedHtml, previewHtml);

    S.indexReady = false;
    pipeline.paintRangeFrames();
    assert.equal(layer.children.length, 0);
    assert.equal(region.classList.contains('has-cell-selection'), false);
  } finally {
    dom.restore();
  }
});

test('navigation keeps wide cells and tall rows visible between frozen headers and the viewport edge', () => {
  for (const offset of [0, 578]) {
    assert.equal(revealCellOffset(offset, 352, 26, 162, 384, 384), 136);
    assert.equal(revealCellOffset(offset, 420, 120, 200, 500, 1000), 80);
  }
  assert.equal(revealCellOffset(0, 360, 80, 300, 200, 200), 220);
  assert.equal(revealCellOffset(75, 100, 100, 140, 64, 64), 75);
});

function setupNavHarness(
  options: {
    readonly row?: number;
    readonly visual?: number;
    readonly pinned?: number;
    readonly rows?: readonly (readonly [number, number])[];
    readonly total?: number;
  } = {},
) {
  const dom = installDom({ fromIndexHtml: true });
  const row = options.row ?? 6;
  const visual = options.visual ?? 2;
  const pinned = options.pinned ?? 0;
  const rows = options.rows ?? [
    [1, 4],
    [2, 6],
    [6, 3],
  ];
  const total = options.total ?? 6;

  const rawGrid = dom.document.getElementById('grid');
  assert.ok(rawGrid);
  rawGrid.clientHeight = 300;
  rawGrid.clientWidth = 400;

  const S = createInitialWorkbookState(1_048_576, 16_384);
  S.book = 'book';
  S.workbookKey = 'key';
  S.sheet = '账表';
  S.sheetEpoch = 1;
  S.indexReady = true;
  S.filtered = true;
  S.filterId = 'sort';
  S.filterTotal = total;
  S.rowView = makeRowViewState({ id: 'view', sheet: '账表', filterId: 'sort', totalRows: total });
  S.window = {
    context: '',
    filtered: true,
    filterId: 'sort',
    viewId: 'view',
    excludeThroughRow: pinned,
    displayStart: 1,
    displayEnd: total,
    startCol: 1,
    endCol: 3,
    columns: null,
  };
  S.selected = {
    sheet: '账表',
    row,
    col: 1,
    ref: `A${row}`,
    raw: `A${row}`,
    display: `A${row}`,
    hasFormula: false,
  };
  S.selectedVisualIndex = visual;
  S.selectedFrozenThrough = pinned;
  S.rowNumberByVisual = new Map(rows);
  S.rangeAnchor = { row, col: 1, visual };
  for (let r = 1; r <= 6; r++) {
    for (const [idx, c] of (['A', 'B', 'C'] as const).entries()) {
      S.cellMap.set(`${c}${r}`, makeCellData(idx + 1, `${c}${r}`, { ref: `${c}${r}` }));
    }
  }

  let windowCovers = true;
  let loadViewportImpl: () => Promise<void> = async () => undefined;
  const selected: [string, number, number, string][] = [];
  const ranges: [number, number, number, number, number | undefined][] = [];

  const deps = makeDefaultNavigationDeps({
    S,
    api: async <T = unknown>(url: string): Promise<T> => {
      const params = new URL(url, 'http://qa').searchParams;
      const r = Number(params.get('row'));
      for (const [index, source] of rows) {
        if (source === r) {
          const res: unknown = { found: true, row: r, visualIndex: index };
          return res as T;
        }
      }
      const empty: unknown = { found: false, row: r, visualIndex: 0 };
      return empty as T;
    },
    windowContains: () => windowCovers,
    loadViewport: () => loadViewportImpl(),
  });
  // Override frozen effective rows
  const frozenPanes = deps.getFrozenPanes();
  frozenPanes.effective = () => ({ rows: pinned, cols: 0 });

  const nav = new GridNavigation(deps);
  const origSelectCell = nav.selectCell.bind(nav);
  nav.selectCell = (cell, r, c, ref, v, preserveTrace, keepSelection, crosshair) => {
    origSelectCell(cell, r, c, ref, v, preserveTrace, keepSelection, crosshair);
    selected.push([ref, r, v, cell?.raw ?? '']);
  };
  const origExtend = nav.extendRange.bind(nav);
  nav.extendRange = (anchor, r, c, v) => {
    origExtend(anchor, r, c, v);
    if (anchor) ranges.push([anchor.row, anchor.visual ?? anchor.row, r, c, v]);
  };

  return {
    S,
    nav,
    selected,
    ranges,
    setWindowContains: (v: boolean) => {
      windowCovers = v;
    },
    setLoadViewport: (fn: () => Promise<void>) => {
      loadViewportImpl = fn;
    },
    restore: () => dom.restore(),
  };
}

test('horizontal keyboard movement and extension keep the sorted source row and visual index paired', async () => {
  const h = setupNavHarness();
  try {
    await h.nav.moveSelection(0, 1, true);
    assert.deepEqual(h.selected, [['B6', 6, 2, 'B6']]);
    assert.deepEqual(h.ranges, [[6, 2, 6, 2, 2]]);
  } finally {
    h.restore();
  }
});

test('a filtered arrow waits for an actual source row before selecting a visual position', async () => {
  const h = setupNavHarness();
  try {
    h.setWindowContains(false);
    let reads = 0;
    h.setLoadViewport(async () => {
      reads++;
    });
    await h.nav.moveSelection(1, 0);
    assert.ok(reads > 0);
    assert.equal(h.S.selected?.ref, 'A6');
    assert.equal(h.S.selectedVisualIndex, 2);
    assert.deepEqual(h.selected, []);
  } finally {
    h.restore();
  }
});

test('jumping to a frozen cell uses its source row as the selection index', async () => {
  const h = setupNavHarness({ pinned: 1 });
  try {
    assert.equal(await h.nav.navigateToCell('账表', 'B1'), true);
    assert.deepEqual(h.selected, [['B1', 1, 1, 'B1']]);
  } finally {
    h.restore();
  }
});

test('a delayed filtered jump cannot replace a newer target or pair an address with another source row', async () => {
  const h = setupNavHarness();
  try {
    let resume: (() => void) | undefined;
    h.setLoadViewport(
      () =>
        new Promise<void>((resolve) => {
          resume = resolve;
        }),
    );
    const first = h.nav.navigateToCell('账表', 'B6');
    await new Promise<void>((resolve) => {
      setImmediate(resolve);
    });
    assert.equal(typeof resume, 'function');
    h.setLoadViewport(async () => undefined);
    assert.equal(await h.nav.navigateToCell('账表', 'C4'), true);
    resume?.();
    assert.equal(await first, false);
    assert.deepEqual(h.selected, [['C4', 4, 1, 'C4']]);
  } finally {
    h.restore();
  }
});

test('viewport tiles with the same filter and view cannot cover a different frozen row prefix', () => {
  const request = makeRequest({
    filtered: true,
    filterId: 'sort',
    viewId: 'view',
    excludeThroughRow: 1,
  });
  const tile = viewportBounds(
    makeResponse({
      filtered: true,
      rows: [
        { number: 1, cells: [] },
        { number: 2, cells: [] },
      ],
    }),
    request,
  );
  const changed = { ...request, excludeThroughRow: 0 };
  assert.ok(viewportCoversVisible(tile, request) && viewportCoversRequest(tile, request));
  assert.equal(viewportCoversVisible(tile, changed), false);
  assert.equal(viewportCoversRequest(tile, changed), false);
  assert.notEqual(viewportKey(1, request), viewportKey(1, changed));
});

test('a sparse tile cannot cover columns inside its bounds that were never fetched', () => {
  const request = makeRequest({
    displayStart: 1,
    displayEnd: 8,
    visibleStart: 2,
    visibleEnd: 4,
    startCol: 1,
    cols: 3,
    visibleColStart: 1,
    visibleColEnd: 3,
  });
  const tile = viewportBounds(makeResponse({ startCol: 1, columns: [1, 3], colCount: 2 }), request);
  assert.equal(viewportCoversVisible(tile, request), false);
  assert.equal(viewportCoversRequest(tile, request), false);
  assert.equal(viewportCoversVisible(tile, { ...request, visibleColEnd: 1 }), true);
  assert.equal(
    viewportHasHeadroom(tile, { ...request, visibleColEnd: 1, leadRow: 4, leadCol: 2 }),
    false,
  );
});

test('changing the frozen prefix preserves the active source cell and rebases its next move', async () => {
  const h = setupNavHarness({
    pinned: 2,
    rows: [
      [1, 12],
      [2, 4],
      [3, 6],
      [4, 3],
    ],
  });
  try {
    h.S.selectedFrozenThrough = 14;
    h.S.range = { row1: 4, row2: 6, col1: 1, col2: 1, visual1: 1, visual2: 2 };
    h.nav.syncFilteredSelection(2);
    assert.equal(h.S.selected?.ref, 'A6');
    assert.equal(h.S.selectedVisualIndex, 3);
    assert.equal(h.S.range, null);
    await h.nav.moveSelection(1, 0, true);
    assert.deepEqual(h.selected, [['A3', 3, 4, 'A3']]);
    assert.deepEqual(h.ranges, [[6, 3, 3, 1, 4]]);
  } finally {
    h.restore();
  }
});

test('setting the active cell updates the selection outline inside the frozen panes', () => {
  const dom = installDom({ fromIndexHtml: true });
  try {
    const region = document.createElement('div');
    region.className = 'grid-region';
    const makePinned = (ref: string, col: number): HTMLDivElement => {
      const el = document.createElement('div');
      el.className = 'pinned-cell';
      el.dataset.ref = ref;
      el.dataset.row = '14';
      el.dataset.col = String(col);
      el.dataset.visualIndex = '14';
      region.appendChild(el);
      return el;
    };
    const first = makePinned('A14', 1);
    const second = makePinned('B14', 2);
    document.body.appendChild(region);

    const S = createInitialWorkbookState(1_048_576, 16_384);
    S.freeze = { rows: 14, cols: 0 };
    S.selected = {
      sheet: 'S1',
      ref: 'A14',
      row: 14,
      col: 1,
      raw: '',
      display: '',
      hasFormula: false,
    };
    const pipeline = new ViewportPipeline(makeDefaultViewportPipelineDeps({ S }));
    pipeline.paintRangeFrames = () => undefined;

    pipeline.applySelectionClasses();
    assert.equal(first.classList.contains('selected'), true);
    assert.equal(second.classList.contains('selected'), false);

    S.selected = { ...S.selected, ref: 'B14', col: 2 };
    pipeline.applySelectionClasses();
    assert.equal(first.classList.contains('selected'), false);
    assert.equal(second.classList.contains('selected'), true);

    S.selected = { ...S.selected, crosshair: { row: 15, col: 1 } };
    pipeline.applySelectionClasses();
    assert.equal(second.classList.contains('selected'), true);
    assert.equal(first.classList.contains('active-column'), true);
    assert.equal(second.classList.contains('active-column'), false);
    assert.equal(second.classList.contains('crosshair-row'), false);
  } finally {
    dom.restore();
  }
});

test('range selection extends header active marks, cell crosshair bands, merged bands and tails, and cleans up on collapse', () => {
  const dom = installDom({ fromIndexHtml: true });
  try {
    const region = document.createElement('div');
    region.className = 'grid-region';
    const layer = document.getElementById('rangeFrames');
    const tails = document.getElementById('crosshairTails');
    assert.ok(layer && tails);
    region.append(layer, tails);

    const colHeads: HTMLDivElement[] = [];
    for (const c of [1, 2, 3, 4, 5]) {
      const h = document.createElement('div');
      h.className = 'column-head';
      h.dataset.col = String(c);
      region.appendChild(h);
      colHeads.push(h);
    }
    const pinnedRowHeads: HTMLDivElement[] = [];
    for (const r of [1, 2]) {
      const h = document.createElement('div');
      h.className = 'pinned-row-head';
      h.dataset.row = String(r);
      region.appendChild(h);
      pinnedRowHeads.push(h);
    }
    const pinnedCells: HTMLDivElement[] = [];
    for (const [ref, row, col] of [
      ['B1', 1, 2],
      ['C2', 2, 3],
    ] as const) {
      const p = document.createElement('div');
      p.className = 'pinned-cell';
      p.dataset.ref = ref;
      p.dataset.row = String(row);
      p.dataset.col = String(col);
      p.dataset.visualIndex = String(row);
      region.appendChild(p);
      pinnedCells.push(p);
    }
    const mergedNode = document.createElement('div') as MergedCellElement;
    mergedNode.className = 'merged-cell';
    mergedNode._xlMerge = { row: 1, col: 2, endRow: 1, endCol: 4 };
    region.appendChild(mergedNode);

    const renderedRows = new Map<number, GridRowElement>();
    for (const r of [3, 4, 5, 6]) {
      const row = document.createElement('div') as GridRowElement;
      row.dataset.row = String(r);
      row.dataset.visualIndex = String(r);
      row._xlCells = new Map<number, GridCellElement>();
      for (const c of [1, 2, 3, 4, 5]) {
        const cell = document.createElement('div') as GridCellElement;
        cell.dataset.ref = `${String.fromCharCode(64 + c)}${r}`;
        cell.dataset.row = String(r);
        cell.dataset.col = String(c);
        cell.dataset.visualIndex = String(r);
        row._xlCells.set(c, cell);
      }
      renderedRows.set(r, row);
    }
    document.body.appendChild(region);

    const S = createInitialWorkbookState(1_048_576, 16_384);
    S.indexReady = true;
    S.selected = {
      sheet: 'S1',
      ref: 'B3',
      row: 3,
      col: 2,
      raw: '',
      display: '',
      hasFormula: false,
    };
    S.selectedVisualIndex = 3;
    S.renderedRows = renderedRows;
    S.freeze = { rows: 2, cols: 0 };

    const mergedCells = new MergedCells();
    mergedCells.load([{ row: 1, col: 2, endRow: 1, endCol: 4 }]);
    const mergeCalls: unknown[] = [];
    mergedCells.paintCrosshair = (_node, target) => {
      mergeCalls.push(target);
    };

    const deps = makeDefaultViewportPipelineDeps({ S, mergedCells });
    const frozen = deps.getFrozenPanes();
    frozen.effective = () => ({ rows: 2, cols: 0 });
    const pipeline = new ViewportPipeline(deps);

    // Initial single cell B3
    pipeline.applySelectionClasses();
    assert.deepEqual(
      colHeads.filter((n) => n.classList.contains('active')).map((n) => n.dataset.col),
      ['2'],
    );
    assert.equal(tails.hidden, false);
    assert.equal(mergeCalls.length, 1);

    // Drag range B3:D5
    S.range = { row1: 3, col1: 2, row2: 5, col2: 4, visual1: 3, visual2: 5 };
    pipeline.applySelectionClasses();
    assert.deepEqual(
      colHeads.filter((n) => n.classList.contains('active')).map((n) => n.dataset.col),
      ['2', '3', '4'],
    );
    assert.deepEqual(
      [...renderedRows.values()]
        .filter((r) => r.classList.contains('active-row'))
        .map((r) => r.dataset.row),
      ['3', '4', '5'],
    );
    assert.deepEqual(
      [...(renderedRows.get(6)?._xlCells?.entries() ?? [])]
        .filter(([, n]) => n.classList.contains('active-column'))
        .map(([c]) => c),
      [2, 3, 4],
    );
    assert.equal(pinnedCells[0]?.classList.contains('active-column'), true);
    assert.equal(pinnedCells[1]?.classList.contains('active-column'), true);
    assert.equal(tails.hidden, false);
    assert.equal(mergeCalls.length, 2);
    assert.deepEqual(mergeCalls.at(-1), { rowSpans: [[3, 5]], colSpans: [[2, 4]] });

    // Range crossing frozen row 2 (B2:C4)
    S.range = { row1: 2, col1: 2, row2: 4, col2: 3, visual1: 2, visual2: 4 };
    pipeline.applySelectionClasses();
    assert.deepEqual(
      pinnedRowHeads.filter((n) => n.classList.contains('active')).map((n) => n.dataset.row),
      ['2'],
    );
    assert.equal(pinnedCells[0].classList.contains('crosshair-row'), false);
    assert.equal(pinnedCells[1].classList.contains('crosshair-row'), true);

    // Collapse back to single cell A6
    S.range = null;
    S.selected = { ...S.selected, ref: 'A6', row: 6, col: 1 };
    S.selectedVisualIndex = 6;
    pipeline.applySelectionClasses();
    assert.deepEqual(
      colHeads.filter((n) => n.classList.contains('active')).map((n) => n.dataset.col),
      ['1'],
    );
    assert.deepEqual(
      pinnedRowHeads.filter((n) => n.classList.contains('active')).map((n) => n.dataset.row),
      [],
    );
    assert.deepEqual(
      [...renderedRows.values()]
        .filter((r) => r.classList.contains('active-row'))
        .map((r) => r.dataset.row),
      ['6'],
    );
    assert.deepEqual(
      [...(renderedRows.get(3)?._xlCells?.entries() ?? [])]
        .filter(([, n]) => n.classList.contains('active-column'))
        .map(([c]) => c),
      [1],
    );
  } finally {
    dom.restore();
  }
});

test('following valid internal and external hyperlinks marks origin cells visited while invalid targets do not', async () => {
  const h = setupNavHarness();
  const opened: string[] = [];
  const origOpen = Object.getOwnPropertyDescriptor(window, 'open');
  try {
    h.S.sheets = [{ name: '账表' }, { name: '汇总' }];
    const row = document.createElement('div') as GridRowElement;
    const cellA6 = document.createElement('div') as GridCellElement;
    cellA6.className = 'grid-cell hyperlink-cell';
    cellA6.dataset.ref = 'A6';
    row._xlCells = new Map([[1, cellA6]]);
    h.S.renderedRows.set(6, row);
    window.open = (url?: string | URL) => {
      opened.push(String(url ?? ''));
      return null;
    };

    await h.nav.followCellHyperlink(
      { kind: 'external', url: 'javascript:alert(1)' },
      { sheet: '账表', ref: 'A6' },
    );
    assert.equal(h.S.visitedHyperlinks.size, 0);
    assert.equal(cellA6.classList.contains('hyperlink-visited'), false);

    await h.nav.followCellHyperlink(
      { kind: 'internal', sheet: '不存在的工作表', ref: 'A1' },
      { sheet: '账表', ref: 'A6' },
    );
    assert.equal(h.S.visitedHyperlinks.size, 0);

    await h.nav.followCellHyperlink(
      { kind: 'external', url: 'https://example.com/report' },
      { sheet: '账表', ref: 'A6' },
    );
    assert.deepEqual(opened, ['https://example.com/report']);
    assert.equal(h.S.visitedHyperlinks.has(hyperlinkVisitKey('账表', 'A6')), true);
    assert.equal(cellA6.classList.contains('hyperlink-visited'), true);
    assert.equal(cellA6._xlHyperlinkVisited, true);

    await h.nav.followCellHyperlink(
      { kind: 'internal', sheet: '账表', ref: 'B6' },
      { sheet: '账表', ref: 'C4' },
    );
    assert.equal(h.S.visitedHyperlinks.has(hyperlinkVisitKey('账表', 'C4')), true);
  } finally {
    if (origOpen) Object.defineProperty(window, 'open', origOpen);
    else Reflect.deleteProperty(window, 'open');
    h.restore();
  }
});
