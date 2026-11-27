import { test } from 'node:test';
import assert from 'node:assert/strict';
import { columnName, parseColumn } from '../core/cell-address.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import { ColumnController } from './columns.ts';
import { Geometry } from './geometry.ts';
import { createInitialWorkbookState } from './state.ts';

function deferred<T = void>() {
  let resolve: (value: T) => void = () => undefined;
  let reject: (reason?: unknown) => void = () => undefined;
  const promise = new Promise<T>((ok, no) => {
    resolve = ok;
    reject = no;
  });
  return { promise, resolve, reject };
}

test('ColumnController.applyVisibility combines source ranges, modes, and explicit overrides in Geometry', () => {
  const dom = installDom({ fromIndexHtml: true });
  try {
    const s = createInitialWorkbookState(1_048_576, 16_384);
    s.indexReady = true;
    s.sheet = 'Sheet1';
    s.maxCol = 6;
    s.sourceColumnWidths = { '1': 90, '2': 100, '3': 110 };
    s.columnSourceRanges = [{ min: 2, max: 3 }];
    s.columnOverrides = { '3': false, '5': true };
    const geometry = new Geometry(1_048_576, 16_384);
    const ctrl = new ColumnController({
      state: s,
      api: async <T = unknown>() => {
        const empty: unknown = {};
        return empty as T;
      },
      toast: () => undefined,
      geometry,
      baseRow: 25,
      baseCol: 132,
      colName: columnName,
      parseColumn: (v) => parseColumn(v, 16_384),
      colLimit: () => 16_384,
      relayoutGrid: () => undefined,
      selectCellByAddress: () => undefined,
      hideCellMenu: () => undefined,
      selectionStats: () => null,
      calculator: () => null,
    });

    ctrl.applyVisibility();
    assert.deepEqual([...s.hiddenColumns].sort((a, b) => a - b), [2, 5]);
    assert.equal(geometry.cols.measure(1), 90);
    assert.equal(geometry.cols.measure(2), 0);
    assert.equal(geometry.cols.measure(3), 110);
    assert.equal(geometry.cols.measure(5), 0);
    assert.equal(ctrl.nextVisible(2), 3);
    assert.equal(ctrl.previousVisible(2), 1);

    s.columnMode = 'show-all';
    s.columnOverrides = {};
    ctrl.applyVisibility();
    assert.equal(s.hiddenColumns.size, 0);
    assert.equal(geometry.cols.measure(2), 100);
  } finally {
    dom.restore();
  }
});

test('ColumnController.setVisibility moves selection off hidden column, serializes saves, and skips rollback for superseded failures', async () => {
  const dom = installDom({ fromIndexHtml: true });
  try {
    const s = createInitialWorkbookState(1_048_576, 16_384);
    s.indexReady = true;
    s.sheet = 'Sheet1';
    s.sheetEpoch = 1;
    s.maxCol = 5;
    s.selected = {
      ref: 'B3',
      row: 3,
      col: 2,
      sheet: 'Sheet1',
      raw: 'v',
      display: 'v',
      hasFormula: false,
    };
    s.selectedVisualIndex = 3;

    const pendingSaves: ReturnType<typeof deferred<void>>[] = [];
    const sentBodies: Record<string, unknown>[] = [];
    const selectedCalls: string[] = [];
    const toasts: string[] = [];
    const geometry = new Geometry(1_048_576, 16_384);

    const ctrl = new ColumnController({
      state: s,
      api: async <T = unknown>(_url: string, opts?: { readonly body?: BodyInit | null }) => {
        if (typeof opts?.body === 'string') {
          sentBodies.push(JSON.parse(opts.body) as Record<string, unknown>);
        }
        const d = deferred();
        pendingSaves.push(d);
        await d.promise;
        const empty: unknown = {};
        return empty as T;
      },
      toast: (msg) => toasts.push(msg),
      geometry,
      baseRow: 25,
      baseCol: 132,
      colName: columnName,
      parseColumn: (v) => parseColumn(v, 16_384),
      colLimit: () => 16_384,
      relayoutGrid: () => undefined,
      selectCellByAddress: (ref) => selectedCalls.push(ref),
      hideCellMenu: () => undefined,
      selectionStats: () => null,
      calculator: () => null,
    });

    // First save hides column B (active cell B3 -> moves selection to C3)
    const first = ctrl.setVisibility('column', 2, true);
    assert.equal(s.hiddenColumns.has(2), true);
    assert.deepEqual(selectedCalls, ['C3']);
    // Second save hides column D while first save is still in flight
    const second = ctrl.setVisibility('column', 4, true);
    await new Promise<void>((resolve) => setImmediate(resolve));
    assert.equal(sentBodies.length, 1, 'second layout save must wait in serial queue');

    // First save fails AFTER a newer revision superseded it: must NOT roll back column 4 or column 2!
    pendingSaves[0]?.reject(new Error('disk busy'));
    await first;
    await new Promise<void>((resolve) => setImmediate(resolve));
    assert.equal(sentBodies.length, 2, 'serial queue must stay healthy after a failure');
    assert.equal(s.hiddenColumns.has(2), true, 'superseded failure must not roll back newer state');
    assert.equal(s.hiddenColumns.has(4), true);

    // Second (latest) save fails: MUST roll back to state before second call!
    pendingSaves[1]?.reject(new Error('still busy'));
    await second;
    assert.equal(s.hiddenColumns.has(2), true);
    assert.equal(s.hiddenColumns.has(4), false, 'latest failure must roll back its own change');
    assert.match(toasts.at(-1) ?? '', /保存列显隐失败：still busy/);
  } finally {
    dom.restore();
  }
});

test('ColumnController.parseSpan and selectSpan normalize reversed anchors and enforce bounds', () => {
  const dom = installDom({ fromIndexHtml: true });
  try {
    const s = createInitialWorkbookState(1_048_576, 16_384);
    s.indexReady = true;
    const ctrl = new ColumnController({
      state: s,
      api: async <T = unknown>() => {
        const empty: unknown = {};
        return empty as T;
      },
      toast: () => undefined,
      geometry: new Geometry(1_048_576, 16_384),
      baseRow: 25,
      baseCol: 132,
      colName: columnName,
      parseColumn: (v) => parseColumn(v, 16_384),
      colLimit: () => 16_384,
      relayoutGrid: () => undefined,
      selectCellByAddress: () => undefined,
      hideCellMenu: () => undefined,
      selectionStats: () => null,
      calculator: () => null,
    });

    assert.deepEqual(ctrl.parseSpan(' $C : $A '), { start: 1, end: 3 });
    assert.equal(ctrl.parseSpan('A:XFE'), null);
    assert.equal(ctrl.parseSpan('A:B:C'), null);

    ctrl.selectSpan(4, 2);
    assert.deepEqual(ctrl.selectedSpan(), { start: 2, end: 4 });
    assert.equal(ctrl.spanLabel(ctrl.selectedSpan()), 'B:D');
    assert.equal(document.getElementById('selectionStatus')?.textContent, 'B:D · 3 列');
  } finally {
    dom.restore();
  }
});
