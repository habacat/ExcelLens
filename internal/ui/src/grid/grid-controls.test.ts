import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { ActivityHandle } from '../contracts/common.ts';
import { columnName, columnNumber } from '../core/cell-address.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import {
  GridControls,
  GridInteractions,
  makeDefaultGridControlsDeps,
  makeDefaultGridInteractionsDeps,
  makeDefaultWorkbookIODeps,
} from '../test-helpers/grid-fixture.ts';
import { ColumnController } from './columns.ts';
import { Geometry } from './geometry.ts';
import { WorkbookIO } from './grid-workbook-io.ts';
import { createInitialWorkbookState } from './state.ts';

test('grid controls clamp zoom steps and preserve range-aware tooltip suppression', () => {
  const dom = installDom({ fromIndexHtml: true });
  try {
    let viewportScheduled = 0;
    let framesPainted = 0;
    const S = createInitialWorkbookState(1_048_576, 16_384);
    S.zoom = 100;
    S.selected = {
      sheet: 'S1',
      row: 2,
      col: 3,
      ref: 'C2',
      raw: '',
      display: '',
      hasFormula: false,
    };
    const controls = new GridControls(
      makeDefaultGridControlsDeps({
        S,
        scheduleViewport: () => {
          viewportScheduled++;
        },
        paintRangeFrames: () => {
          framesPainted++;
        },
        isDragging: () => true,
      }),
    );

    controls.setZoom(143);
    assert.equal(S.zoom, 125);
    assert.equal(document.getElementById('zoomValue')?.textContent, '125%');
    assert.equal(viewportScheduled, 1);
    assert.equal(framesPainted, 1);
    assert.equal(document.documentElement.style.getPropertyValue('--row-height'), '31px');

    controls.setZoom(42);
    assert.equal(S.zoom, 70);
    assert.equal(document.getElementById('zoomValue')?.textContent, '70%');

    // While dragging a selection, hover tooltips stay hidden.
    const tooltip = document.getElementById('cellTooltip');
    assert.ok(tooltip);
    tooltip.hidden = false;
    const cell = document.createElement('div');
    cell.className = 'grid-cell';
    cell.dataset.ref = 'A1';
    controls.showTooltip({ target: cell, clientX: 50, clientY: 50 });
    assert.equal(tooltip.hidden, true);
  } finally {
    dom.restore();
  }
});

test('grid interactions ribbon access keys Alt+H, O, I/A/W/H trigger resize commands and expire cleanly', () => {
  const dom = installDom({ fromIndexHtml: true });
  try {
    const calls: [string, 'row' | 'col', number][] = [];
    const S = createInitialWorkbookState(1_048_576, 16_384);
    S.selected = {
      sheet: 'S1',
      row: 5,
      col: 4,
      ref: 'D5',
      raw: '',
      display: '',
      hasFormula: false,
    };
    const interactions = new GridInteractions(
      makeDefaultGridInteractionsDeps({
        S,
        autoFitSize: async (kind, index) => {
          calls.push(['fit', kind, index]);
        },
        promptResize: async (kind, index) => {
          calls.push(['prompt', kind, index ?? 0]);
        },
      }),
    );
    const take = (fn: () => unknown): void => {
      fn();
    };
    const keyEv = (key: string, altKey = false) => ({
      altKey,
      ctrlKey: false,
      metaKey: false,
      shiftKey: false,
      key,
    });

    assert.equal(interactions.handleRibbonSequence(keyEv('h', true), 'h', true, take), true);
    assert.equal(interactions.handleRibbonSequence(keyEv('o', false), 'o', true, take), true);
    assert.equal(interactions.handleRibbonSequence(keyEv('i', false), 'i', true, take), true);
    assert.deepEqual(calls, [['fit', 'col', 4]]);

    // Expired sequence should not fire.
    assert.equal(interactions.handleRibbonSequence(keyEv('h', true), 'h', true, take), true);
    interactions.ribbonExpires = Date.now() - 1;
    assert.equal(interactions.handleRibbonSequence(keyEv('o', false), 'o', true, take), false);
  } finally {
    dom.restore();
  }
});

test('column visibility follows the active source when switching between BIFF and OOXML', async () => {
  const dom = installDom({ fromIndexHtml: true });
  try {
    const state = createInitialWorkbookState(1_048_576, 16_384);
    state.indexReady = true;
    state.sheet = 'Data';
    state.columnMode = 'hide-all';
    state.columnOverrides = {};
    const geometry = new Geometry(1_048_576, 16_384);
    let sourceLimit = 256;
    const controller = new ColumnController({
      state,
      colLimit: () => sourceLimit,
      geometry,
      baseRow: 25,
      baseCol: 132,
      colName: columnName,
      parseColumn: columnNumber,
      toast: () => undefined,
      relayoutGrid: () => undefined,
      selectCellByAddress: () => undefined,
      hideCellMenu: () => undefined,
      selectionStats: () => null,
      calculator: () => null,
      api: () => {
        throw new Error('outside source columns');
      },
    });
    controller.applyVisibility();
    assert.equal(state.hiddenColumns.size, 256);
    await controller.setVisibility('column', 257, true);
    assert.deepEqual(state.columnOverrides, {});
    sourceLimit = 16_384;
    controller.applyVisibility();
    assert.equal(state.hiddenColumns.size, 16_384);
  } finally {
    dom.restore();
  }
});

test('index progress distinguishes source extents, bytes and statistics rows', () => {
  const dom = installDom({ fromIndexHtml: true });
  try {
    const io = new WorkbookIO(
      makeDefaultWorkbookIODeps({
        fmtBytes: (n) => `${Number(n ?? 0)} B`,
      }),
    );
    const calls: (number | string | undefined)[][] = [];
    const activity: ActivityHandle = {
      pause: () => undefined,
      resume: () => undefined,
      stage: () => undefined,
      report: (done, total, stage, detail) => {
        calls.push([done, total, stage, detail]);
      },
      onCancel: () => undefined,
      paintIfDue: () => undefined,
      job: () => undefined,
      end: () => undefined,
    };
    io.reportIndexProgress(activity, {
      stage: '索引完成',
      doneBytes: 42,
      totalBytes: 42,
    });
    io.reportIndexProgress(activity, {
      stage: '条件统计',
      doneBytes: 0,
      totalBytes: 0,
      statisticsProgress: { done: 4, total: 8 },
    });
    assert.deepEqual(calls, [
      [42, 42, '索引完成', '42 B / 42 B'],
      [4, 8, '条件统计', '4 / 8 行'],
    ]);
  } finally {
    dom.restore();
  }
});
