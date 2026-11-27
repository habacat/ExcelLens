import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { ApiRequestOptions } from '../contracts/common.ts';
import type { AxisViewSettings } from '../contracts/analysis.ts';
import type { SheetViewportResponse } from '../contracts/workbook.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import {
  makeCellData,
  makeDefaultSheetControllerDeps,
  makeDefaultViewportPipelineDeps,
  makeRowViewState,
  SheetController,
  ViewportPipeline,
} from '../test-helpers/grid-fixture.ts';
import { RowVisibility } from './row-visibility.ts';
import { createInitialWorkbookState, type ViewportRequest } from './state.ts';
import { TileCache } from './viewport-cache.ts';

const tick = (): Promise<void> =>
  new Promise((resolve) => {
    setImmediate(resolve);
  });

function setupSheetViews(
  override?: (pathname: string, sheet: string | null) => unknown,
) {
  const dom = installDom({ fromIndexHtml: true });
  const calls: { readonly path: string; readonly sheet: string | null }[] = [];
  const notices: [string, string | undefined][] = [];
  const cardResets: (string | null)[] = [];
  const quickResets: (string | null)[] = [];
  const detailResets: (string | null)[] = [];

  const S = createInitialWorkbookState(50_000_000, 16_384);
  S.opened = true;
  S.source = { format: 'xlsx' };
  S.workbookKey = 'qa-book';
  const tiles = new TileCache(24 * 1024 * 1024);

  const api = async <T = unknown>(path: string, options?: ApiRequestOptions): Promise<T> => {
    const url = new URL(path, 'http://qa');
    const body =
      typeof options?.body === 'string'
        ? (JSON.parse(options.body) as { sheet?: string })
        : undefined;
    const sheet = body?.sheet ?? url.searchParams.get('sheet');
    calls.push({ path: url.pathname, sheet });
    const custom = override?.(url.pathname, sheet);
    if (custom !== undefined) return (await Promise.resolve(custom)) as T;
    if (url.pathname === '/api/index/start') {
      const res: unknown = { ready: true, maxRow: 80, maxCol: 4, sheet };
      return res as T;
    }
    if (url.pathname === '/api/layout') {
      const res: unknown = {
        rows: { '2': sheet === 'A' ? 40 : 55 },
        cols: { '1': 110 },
        font: '',
        sourceMerges: [],
        sourceRows: [],
        sourceCols: {},
      };
      return res as T;
    }
    if (url.pathname === '/api/view') {
      const res: unknown = { rows: 0, cols: 0 };
      return res as T;
    }
    if (url.pathname === '/api/sheet') {
      const sheetName = sheet ?? '';
      const res: unknown = {
        sheet: sheetName,
        offset: 0,
        limit: 1,
        totalRows: 1,
        filtered: false,
        maxRow: 80,
        maxCol: 4,
        startCol: 1,
        colCount: 1,
        rows: [
          {
            number: 1,
            cells: [makeCellData(1, sheetName, { ref: 'A1' })],
          },
        ],
      };
      return res as T;
    }
    throw new Error(`unexpected API ${path}`);
  };

  let navigateImpl: () => Promise<boolean> = async () => true;
  const deps = makeDefaultSheetControllerDeps({
    S,
    api,
    tiles,
    toast: (msg, tone) => notices.push([msg, tone]),
    invalidateViewport: (options) => {
      assert.equal(options?.preserveDOM, true);
      tiles.clear();
      S.viewportData = null;
      S.window = null;
    },
    loadViewport: async (_force, _epoch, strict) => {
      assert.equal(strict, true);
      const key = S.sheet ?? '';
      let entry = tiles.entries.get(key);
      if (!entry) {
        const data = await api<SheetViewportResponse>(`/api/sheet?sheet=${encodeURIComponent(key)}`);
        const req: ViewportRequest = {
          context: '',
          filtered: false,
          filterId: '',
          viewId: '',
          excludeThroughRow: 0,
          displayStart: 1,
          displayEnd: 1,
          visibleStart: 1,
          visibleEnd: 1,
          visibleColStart: 1,
          visibleColEnd: 1,
          row: 1,
          offset: 0,
          limit: 1,
          startCol: 1,
          cols: 1,
          selectedCols: null,
          leadRow: 0,
          leadCol: 0,
        };
        entry = tiles.store(key, data, req);
      }
      S.viewportData = entry;
      const cell = entry.data.rows[0]?.cells[0];
      if (cell) S.cellMap.set('A1', cell);
    },
    selectInitialCell: () => {
      const cell = S.cellMap.get('A1') ?? makeCellData(1, '', { ref: 'A1' });
      S.selected = { ...cell, row: 1, col: 1, sheet: S.sheet ?? '' };
    },
    navigateToCell: () => navigateImpl(),
    panels: {
      searchPanel: {
        leavePages: () => undefined,
        buildHits: () => undefined,
        applyHighlightClasses: () => undefined,
        displaySummary: () => undefined,
        renderResults: () => undefined,
        syncMode: () => undefined,
        poll: async () => undefined,
      },
      exportCenter: { adoptRecovered: () => false },
      recordCardCenter: {
        resetBook: () => cardResets.push(S.sheet),
        controls: () => undefined,
      },
      selectionQuick: {
        resetBook: () => quickResets.push(S.sheet),
        changed: () => undefined,
      },
      cellExplanation: {
        resetBook: () => detailResets.push(S.sheet),
        changed: () => undefined,
      },
      transposeCenter: { resetBook: () => undefined, changed: () => undefined },
      profileCenter: { resetBook: () => undefined, changed: () => undefined },
    },
  });
  const controller = new SheetController(deps);
  controller.startConditionalStats = async () => undefined;
  controller.cancelConditionalStats = () => undefined;
  controller.updateAutoFilterButton = () => undefined;
  controller.updateFreezeUI = () => undefined;

  return {
    S,
    deps,
    controller,
    calls,
    notices,
    cardResets,
    quickResets,
    detailResets,
    setNavigate: (fn: () => Promise<boolean>) => {
      navigateImpl = fn;
    },
    restore: () => dom.restore(),
  };
}

test('returning to a visited sheet restores source layout and cell data without metadata reads', async () => {
  const h = setupSheetViews();
  try {
    await h.controller.selectSheet('A');
    if (h.S.index) {
      h.S.index = {
        id: 'idx',
        sheet: 'A',
        status: 'done',
        stage: 'done',
        progress: 100,
        doneBytes: 0,
        totalBytes: 0,
        totalRows: 80,
        maxRow: 80,
        maxCol: 4,
      };
    }
    await h.controller.selectSheet('B');
    h.calls.length = 0;
    await h.controller.selectSheet('A');
    assert.deepEqual(h.calls, []);
    assert.equal(h.S.rowSizes['2'], 40);
    assert.equal(h.S.selected?.raw, 'A');
    assert.equal(h.S.sheetLoadingEpoch, 0);
    assert.equal(document.getElementById('grid')?.inert, false);
    assert.deepEqual(h.cardResets, [null, 'A', 'B']);
    assert.deepEqual(h.quickResets, [null, 'A', 'B']);
    assert.deepEqual(h.detailResets, [null, 'A', 'B']);
  } finally {
    h.restore();
  }
});

test('an older layout failure cannot clear the newly selected sheet or show a stale error', async () => {
  let rejectLayout: ((err: Error) => void) | undefined;
  const pending = new Promise((_, r) => {
    rejectLayout = r;
  });
  const h = setupSheetViews((path, sheet) =>
    path === '/api/layout' && sheet === 'A' ? pending : undefined,
  );
  try {
    const first = h.controller.selectSheet('A');
    await tick();
    await h.controller.selectSheet('B');
    rejectLayout?.(new Error('late A failure'));
    await first;
    assert.equal(h.S.sheet, 'B');
    assert.equal(h.S.rowSizes['2'], 55);
    assert.equal(h.S.indexReady, true);
    assert.equal(h.S.selected?.raw, 'B');
    assert.equal(h.notices.length, 0);
  } finally {
    h.restore();
  }
});

test('rapid A to B to A ignores an obsolete layout response even when transport ignores abort', async () => {
  let release: ((value: unknown) => void) | undefined;
  const pending = new Promise((r) => {
    release = r;
  });
  const h = setupSheetViews((path, sheet) =>
    path === '/api/layout' && sheet === 'B' ? pending : undefined,
  );
  try {
    await h.controller.selectSheet('A');
    const middle = h.controller.selectSheet('B');
    await tick();
    await h.controller.selectSheet('A');
    release?.({ rows: { '2': 999 }, cols: {}, sourceMerges: [] });
    await middle;
    assert.equal(h.S.sheet, 'A');
    assert.equal(h.S.rowSizes['2'], 40);
    assert.equal(h.S.selected?.raw, 'A');
    assert.equal(document.getElementById('grid')?.inert, false);
  } finally {
    h.restore();
  }
});

test('view reuse is bounded and excludes incomplete views, changed filters and oversized tiles', async () => {
  const h = setupSheetViews();
  try {
    for (let i = 0; i < 9; i++) await h.controller.selectSheet(String(i));
    assert.ok(h.S.sheetViews.size <= 6);
    assert.ok(h.S.sheetViewsBytes <= 16 * 1024 * 1024);

    h.S.filterJob = 'pending';
    const count = h.S.sheetViews.size;
    h.controller.rememberSheetView();
    assert.equal(h.S.sheetViews.size, count);
    h.S.filterJob = null;

    if (h.S.viewportData) {
      h.S.viewportData = {
        ...h.S.viewportData,
        data: {
          ...h.S.viewportData.data,
          rows: [{ number: 1, cells: [makeCellData(1, 'x'.repeat(9_000_000))] }],
        },
      };
    }
    h.controller.rememberSheetView();
    assert.equal(h.S.sheetViews.has(h.S.sheet ?? ''), false);

    h.S.filtersBySheet.set('7', 'new-filter');
    h.calls.length = 0;
    await h.controller.selectSheet('7');
    assert.ok(h.calls.some((c) => c.path === '/api/layout' && c.sheet === '7'));
  } finally {
    h.restore();
  }
});

test('a real opening failure keeps the old grid inert and reports the actual error', async () => {
  const h = setupSheetViews((path, sheet) =>
    path === '/api/index/start' && sheet === 'bad'
      ? Promise.reject(new Error('source unavailable'))
      : undefined,
  );
  try {
    await h.controller.selectSheet('A');
    await h.controller.selectSheet('bad');
    assert.equal(h.S.indexReady, false);
    assert.equal(document.getElementById('grid')?.inert, true);
    assert.equal(document.getElementById('indexOverlay')?.hidden, false);
    assert.equal(h.notices[0]?.[0], 'source unavailable');
  } finally {
    h.restore();
  }
});

test('an expired filter during visible-row preparation restores source order while retaining the captured visibility', async () => {
  const h = setupSheetViews();
  try {
    const settings: AxisViewSettings = {
      mode: 'hide-all',
      ranges: [{ min: 1, max: 9, hidden: false }],
    };
    const prepared: { readonly signal?: AbortSignal; readonly settings?: AxisViewSettings }[] = [];
    h.S.filtersBySheet.set('A', 'expired-filter');
    const rowViews = {
      busy: false,
      serial: 0,
      cache: new Map<string, { readonly settings?: AxisViewSettings }>([['A', { settings }]]),
      cancel: () => undefined,
      prepare: async (options: {
        readonly signal?: AbortSignal;
        readonly settings?: AxisViewSettings;
      }) => {
        prepared.push(options);
        if (prepared.length === 1) throw new Error('筛选结果已失效，请重新筛选');
        h.S.rowVisibility = new RowVisibility(80);
        h.S.rowView = makeRowViewState({ id: 'rebuilt' });
        return h.S.rowView;
      },
      prepareFilter: async () => makeRowViewState(),
      build: async () => makeRowViewState(),
      install: () => undefined,
      validate: () => undefined,
      cleanup: () => undefined,
    };
    const controller = new SheetController({
      ...h.deps,
      panels: { ...h.deps.panels, rowViews },
    });
    controller.startConditionalStats = async () => undefined;
    controller.cancelConditionalStats = () => undefined;
    controller.updateAutoFilterButton = () => undefined;
    controller.updateFreezeUI = () => undefined;

    await controller.selectSheet('A');
    assert.equal(prepared.length, 2);
    assert.equal(prepared[1]?.settings, settings);
    assert.equal(h.S.indexReady, true);
    assert.equal(h.S.filtered, false);
    assert.equal(h.S.filterId, '');
    assert.equal(h.S.filtersBySheet.has('A'), false);
    assert.ok(h.notices.some((x) => x[0].includes('已保留行显隐')));
  } finally {
    h.restore();
  }
});

test('a slow trace destination cannot reopen the preparation overlay after the sheet is ready', async () => {
  const h = setupSheetViews();
  try {
    let release: (() => void) | undefined;
    let started = false;
    h.setNavigate(async () => {
      started = true;
      await new Promise<void>((resolve) => {
        release = resolve;
      });
      return true;
    });
    const opening = h.controller.selectSheet('B', 'A60', true);
    await tick();
    assert.equal(started, true);
    release?.();
    await opening;
    assert.equal(document.getElementById('indexOverlay')?.hidden, true);
    assert.equal(document.getElementById('grid')?.inert, false);
  } finally {
    h.restore();
  }
});

test('first-tile failures propagate during a sheet handoff while ordinary scroll keeps bounded retries', async () => {
  for (const strict of [false, true]) {
    const dom = installDom({ fromIndexHtml: true });
    try {
      const S = createInitialWorkbookState(1_048_576, 16_384);
      S.sheet = 'A';
      S.sheetEpoch = 1;
      S.indexReady = true;
      const pipeline = new ViewportPipeline(
        makeDefaultViewportPipelineDeps({
          S,
          api: async () => {
            throw new Error('first tile failed');
          },
        }),
      );
      let retries = 0;
      const origSchedule = window.setTimeout.bind(window);
      Reflect.set(window, 'setTimeout', (_fn: unknown, _ms?: number) => {
        retries++;
        return 1;
      });
      try {
        if (strict) {
          await assert.rejects(pipeline.loadViewport(true, 1, true), /first tile failed/);
        } else {
          await pipeline.loadViewport(false, 1, false);
        }
        assert.equal(retries, strict ? 0 : 1);
        assert.equal(S.window, null);
      } finally {
        Reflect.set(window, 'setTimeout', origSchedule);
      }
    } finally {
      dom.restore();
    }
  }
});
