import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { ApiRequestOptions } from '../contracts/common.ts';
import type { SheetViewportResponse } from '../contracts/workbook.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import { FrozenPanes } from './freeze.ts';
import { createInitialWorkbookState, type ViewportRequest } from './state.ts';

const tick = (): Promise<void> =>
  new Promise((resolve) => {
    setImmediate(resolve);
  });

interface PendingApiCall {
  readonly url: string;
  readonly signal: AbortSignal;
  readonly resolve: (data: SheetViewportResponse) => void;
  readonly reject: (err: Error) => void;
}

function fixture() {
  const dom = installDom({ fromIndexHtml: true });
  const calls: PendingApiCall[] = [];
  let request: ViewportRequest = {
    context: 'ctx',
    filtered: false,
    filterId: '',
    viewId: 'view-a',
    excludeThroughRow: 2,
    startCol: 3,
    cols: 10,
    displayStart: 3,
    displayEnd: 40,
    row: 3,
    offset: 2,
    limit: 38,
    visibleStart: 3,
    visibleEnd: 38,
    visibleColStart: 3,
    visibleColEnd: 12,
    selectedCols: null,
    leadRow: 0,
    leadCol: 0,
  };
  const state = createInitialWorkbookState(1_048_576, 16_384);
  state.indexReady = true;
  state.sheet = 'A';
  state.sheetEpoch = 1;
  state.freeze = { rows: 2, cols: 2 };
  state.rowView = {
    id: 'view-a',
    workbookKey: 'book-a',
    book: 'book-a',
    revision: 0,
    sheet: 'A',
    filterId: '',
    totalRows: 100,
    maxRow: 100,
    digest: '',
    notice: '',
    settingsWarning: '',
    matchedPinnedRows: null,
    hiddenRanges: [],
    groupCount: 0,
    settings: { mode: 'original' },
  };
  const grid = document.getElementById('grid');
  const rawGrid = dom.document.getElementById('grid');
  assert.ok(grid);
  assert.ok(rawGrid);
  rawGrid.clientHeight = 900;
  rawGrid.clientWidth = 1200;

  const panes = new FrozenPanes({
    state,
    grid,
    rowHead: 26,
    api: <T = unknown>(url: string, opts?: ApiRequestOptions): Promise<T> =>
      new Promise<T>((resolve, reject) => {
        const signal = opts?.signal ?? new AbortController().signal;
        calls.push({
          url,
          signal,
          resolve: (data) => resolve(data as T),
          reject,
        });
        signal.addEventListener('abort', () => reject(signal.reason as Error), { once: true });
      }),
    getRequest: () => request,
    rowHeight: () => 24,
    colWidth: () => 100,
    rowTop: (r) => (r - 1) * 24,
    colLeft: (c) => (c - 1) * 100,
    headerHeight: () => 27,
    colName: (c) => String.fromCharCode(64 + c),
    bindResizeGrip: () => undefined,
    getVirtualScrollTop: () => 0,
    updateSharedBorders: () => undefined,
    isPrecedent: () => false,
    isSearchMatch: () => false,
    applyCellStyle: () => undefined,
    onRender: () => undefined,
    paintCell: () => undefined,
    onError: () => undefined,
  });
  const pending = <T>(promise: Promise<T>): Promise<T> => {
    promise.catch(() => undefined);
    return promise;
  };
  return {
    state,
    panes,
    calls,
    pending,
    move() {
      request = {
        ...request,
        displayStart: 103,
        displayEnd: 140,
        row: 103,
        visibleStart: 103,
        visibleEnd: 138,
      };
    },
    restore: () => dom.restore(),
  };
}

test('identical frozen refreshes share the active read during a warm sheet handoff', async () => {
  for (const force of [false, true]) {
    const h = fixture();
    try {
      const first = h.pending(h.panes.refresh(true));
      await tick();
      const second = h.pending(h.panes.refresh(force));
      try {
        assert.equal(h.calls.length, 3, 'The same corner/top/left read must not be restarted');
        assert.ok(
          h.calls.every((call) => !call.signal.aborted),
          'A scheduled refresh must not abort the sheet-opening refresh',
        );
      } finally {
        h.panes.reset();
        await Promise.allSettled([first, second]);
      }
    } finally {
      h.restore();
    }
  }
});

test('a superseded frozen read does not turn a current sheet into a read failure', async () => {
  const h = fixture();
  try {
    const first = h.pending(h.panes.refresh(true));
    await tick();
    h.move();
    const second = h.pending(h.panes.refresh());
    try {
      await first;
      assert.equal(h.calls.length, 4);
      assert.deepEqual(
        h.calls.map((call) => call.signal.aborted),
        [false, false, true, false],
        'vertical motion shares the unchanged corner and top',
      );
      assert.equal(h.state.frozenCellMap.size, 0);
    } finally {
      h.panes.reset();
      await Promise.allSettled([first, second]);
    }
  } finally {
    h.restore();
  }
});

test('disabling effective frozen panes cancels an in-flight frozen read', async () => {
  const h = fixture();
  try {
    const first = h.pending(h.panes.refresh(true));
    await tick();
    h.state.freeze = { rows: 0, cols: 0 };
    try {
      await h.panes.refresh();
      assert.ok(h.calls.every((call) => call.signal.aborted));
      await first;
      assert.equal(h.state.frozenCellMap.size, 0);
    } finally {
      h.panes.reset();
      await Promise.allSettled([first]);
    }
  } finally {
    h.restore();
  }
});

test('a current frozen read still propagates genuine source failures', async () => {
  const h = fixture();
  try {
    const reading = h.pending(h.panes.refresh(true));
    h.calls[0]?.reject(new Error('source CRC mismatch'));
    await assert.rejects(reading, /source CRC mismatch/);
    assert.ok(
      h.calls.slice(1).every((call) => call.signal.aborted),
      'A failed part releases its sibling reads',
    );
    h.panes.reset();
  } finally {
    h.restore();
  }
});
