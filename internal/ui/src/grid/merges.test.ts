import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { MergeRange } from '../contracts/common.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import { makeDefaultViewportPipelineDeps } from '../test-helpers/grid-fixture.ts';
import { MergedCells, type MergedCellElement } from './merges.ts';
import { RowVisibility } from './row-visibility.ts';
import { ViewportPipeline } from './viewport-pipeline.ts';

function makeCell(): MergedCellElement {
  return document.createElement('div');
}

test('copying one covered cell or dragging backwards includes every intersected Excel merge', () => {
  const m = new MergedCells();
  m.load([
    { row: 2, col: 2, endRow: 3, endCol: 4 },
    { row: 4, col: 1, endRow: 5, endCol: 2 },
    { row: 1, col: 6, endRow: 7, endCol: 6 },
  ]);
  const expand = (
    r1: number,
    c1: number,
    r2: number,
    c2: number,
    vis?: { visible(r: number): boolean; previous(r: number): number },
  ) => m.expand({ row1: r1, col1: c1, row2: r2, col2: c2, visual1: r1, visual2: r2 }, vis);

  const a = expand(3, 3, 3, 3);
  assert.deepEqual(
    { row1: a.row1, col1: a.col1, row2: a.row2, col2: a.col2 },
    { row1: 2, col1: 2, row2: 3, col2: 4 },
  );
  const b = expand(5, 5, 3, 3);
  assert.deepEqual(
    { row1: b.row1, col1: b.col1, row2: b.row2, col2: b.col2 },
    { row1: 5, col1: 5, row2: 2, col2: 1 },
  );
  const hidden = {
    visible: (r: number) => r !== 3 && r !== 4,
    previous: (r: number) => (r === 3 || r === 4 ? 2 : r),
  };
  const c = expand(2, 2, 2, 2, hidden);
  assert.deepEqual(
    { row1: c.row1, col1: c.col1, row2: c.row2, col2: c.col2 },
    { row1: 2, col1: 2, row2: 2, col2: 4 },
  );
  const d = expand(5, 2, 5, 2, hidden);
  assert.deepEqual(
    { row1: d.row1, col1: d.col1, row2: d.row2, col2: d.col2 },
    { row1: 5, col1: 2, row2: 5, col2: 2 },
  );
});

test('HTML title/header merged anchors and covered positions keep their source coordinates', () => {
  const dom = installDom();
  try {
    const m = new MergedCells();
    m.load([
      { row: 2, col: 1, endRow: 2, endCol: 13 },
      { row: 8, col: 2, endRow: 9, endCol: 2 },
    ]);
    assert.equal(m.at(2, 13)?.col, 1);
    assert.equal(m.at(9, 2)?.row, 8);
    assert.equal(m.at(7, 2), null);
    assert.equal(m.at(2, 14), null);

    const node = makeCell();
    m.decorate(node, m.at(2, 1), 2, 1, (c) => (c - 1) * 132, (r) => (r - 1) * 25, () => 132);
    assert.equal(node.style.width, '1716px');
    assert.equal(node.style.height, '25px');
    assert.equal(node.style.zIndex, '2');
    m.decorate(node, m.at(9, 2), 9, 2, (c) => (c - 1) * 132, (r) => (r - 1) * 25, () => 132);
    assert.equal(node.style.display, 'none');
    m.load([]);
    m.decorate(node, null, 9, 2, (c) => (c - 1) * 132, (r) => (r - 1) * 25, () => 132);
    assert.equal(node.style.display, '');
    assert.equal(node.style.width, '132px');
  } finally {
    dom.restore();
  }
});

test('merged lookup agrees with a source-range oracle and does not touch ordinary cell styles', () => {
  const dom = installDom();
  try {
    const m = new MergedCells();
    const ranges: MergeRange[] = [];
    for (let i = 0; i < 1000; i++) {
      ranges.push({ row: i * 20 + 1, col: 2, endRow: i * 20 + 3, endCol: 5 });
    }
    m.load(ranges);
    for (let row = 1; row < 20000; row += 7) {
      for (let col = 1; col < 8; col++) {
        const want = ranges.find(
          (r) => row >= r.row && row <= r.endRow && col >= r.col && col <= r.endCol,
        );
        assert.equal(m.at(row, col)?.row, want?.row);
      }
    }
    const node = makeCell();
    m.decorate(node, null, 1, 1, () => 0, () => 0, () => 132);
    assert.equal(node.style.cssText, '');
  } finally {
    dom.restore();
  }
});

test('merged crosshair hit and paint use the same visible row and column edges', () => {
  const dom = installDom();
  try {
    const m = new MergedCells();
    const range: MergeRange = { row: 2, col: 2, endRow: 4, endCol: 5 };
    const lefts = [0, 0, 50, 75, 75, 148.5, 180];
    const tops = [0, 0, 17, 39.5, 85, 110];
    const left = (c: number): number => lefts[c] ?? 0;
    const top = (r: number): number => tops[r] ?? 0;
    const node = makeCell();
    node.className = 'merged-cell';
    node._xlMerge = range;
    node.getBoundingClientRect = () => ({
      x: 9.5,
      y: 21,
      left: 9.5,
      top: 21,
      right: 140,
      bottom: 120,
      width: 130.5,
      height: 99,
      toJSON() {
        return this;
      },
    });
    const click = (x: number, y: number, detail = 1) =>
      m.pointerCell({ type: 'click', detail, target: node, clientX: x, clientY: y }, left, top);
    assert.deepEqual(click(34.49, 43.49), { row: 2, col: 2 });
    assert.deepEqual(click(34.5, 43.5), { row: 3, col: 4 });
    assert.deepEqual(click(138, 110), { row: 4, col: 5 });
    assert.equal(click(0, 0, 0), null);

    const hitCases: readonly (readonly [
      { readonly row: number; readonly col: number } | null,
      string,
      string,
    ])[] = [
      [{ row: 1, col: 2 }, '0px 0, 0 0px', '25px 100%, 100% 0px'],
      [{ row: 6, col: 4 }, '25px 0, 0 0px', '73.5px 100%, 100% 0px'],
      [{ row: 4, col: 1 }, '0px 0, 0 68px', '0px 100%, 100% 25px'],
      [click(60, 76), '25px 0, 0 22.5px', '73.5px 100%, 100% 45.5px'],
    ];
    for (const [point, position, size] of hitCases) {
      m.paintCrosshair(node, point, left, top);
      assert.ok(node.classList.contains('merge-crosshair'));
      assert.equal(node.style.getPropertyValue('--merge-band-position'), position);
      assert.equal(node.style.getPropertyValue('--merge-band-size'), size);
    }
    m.paintCrosshair(node, { row: 7, col: 6 }, left, top);
    assert.ok(!node.classList.contains('merge-crosshair'));
    m.paintCrosshair(node, { row: 2, col: 2 }, left, top);
    m.decorate(node, null, 2, 2, left, top, () => 25);
    assert.ok(!node.classList.contains('merge-crosshair'));
    assert.equal(click(35, 45), null);
  } finally {
    dom.restore();
  }
});

test('merged crosshair supports contiguous and multi-range band spans and clears custom gradient layers', () => {
  const dom = installDom();
  try {
    const m = new MergedCells();
    const range: MergeRange = { row: 2, col: 2, endRow: 4, endCol: 5 };
    const lefts = [0, 0, 50, 75, 75, 148.5, 180];
    const tops = [0, 0, 17, 39.5, 85, 110];
    const left = (c: number): number => lefts[c] ?? 0;
    const top = (r: number): number => tops[r] ?? 0;
    const node = makeCell();
    node._xlMerge = range;

    m.paintCrosshair(node, { rowSpans: [[5, 8]], colSpans: [[4, 6]] }, left, top);
    assert.ok(node.classList.contains('merge-crosshair'));
    assert.equal(node.style.getPropertyValue('--merge-band-image'), '');
    assert.equal(node.style.getPropertyValue('--merge-band-position'), '25px 0, 0 0px');
    assert.equal(node.style.getPropertyValue('--merge-band-size'), '105px 100%, 100% 0px');

    m.paintCrosshair(
      node,
      {
        rowSpans: [[6, 7]],
        colSpans: [
          [2, 2],
          [5, 5],
        ],
      },
      left,
      top,
    );
    assert.ok(node.classList.contains('merge-crosshair'));
    assert.equal(
      node.style.getPropertyValue('--merge-band-image'),
      'linear-gradient(var(--crosshair-column-fill), var(--crosshair-column-fill)), linear-gradient(var(--crosshair-column-fill), var(--crosshair-column-fill))',
    );
    assert.equal(node.style.getPropertyValue('--merge-band-position'), '0px 0, 98.5px 0');
    assert.equal(node.style.getPropertyValue('--merge-band-size'), '25px 100%, 31.5px 100%');

    m.paintCrosshair(node, { rowSpans: [[6, 7]], colSpans: [[4, 4]] }, left, top);
    assert.equal(node.style.getPropertyValue('--merge-band-image'), '');
    assert.equal(node.style.getPropertyValue('--merge-band-position'), '25px 0, 0 0px');
    assert.equal(node.style.getPropertyValue('--merge-band-size'), '73.5px 100%, 100% 0px');

    m.paintCrosshair(
      node,
      {
        rowSpans: [[6, 7]],
        colSpans: [
          [4, 5],
          [2, 3],
        ],
      },
      left,
      top,
    );
    assert.equal(node.style.getPropertyValue('--merge-band-image'), '');
    assert.equal(node.style.getPropertyValue('--merge-band-position'), '0px 0, 0 0px');
    assert.equal(node.style.getPropertyValue('--merge-band-size'), '130px 100%, 100% 0px');
  } finally {
    dom.restore();
  }
});

test('actual grid helpers disable source merges during filtering or across a frozen boundary', () => {
  const dom = installDom({ fromIndexHtml: true });
  try {
    const m = new MergedCells();
    m.load([{ row: 2, col: 1, endRow: 2, endCol: 13 }]);
    const deps = makeDefaultViewportPipelineDeps({ mergedCells: m });
    deps.S.indexReady = true;
    deps.S.filtered = false;
    deps.S.freeze = { rows: 0, cols: 0 };
    const pipeline = new ViewportPipeline(deps);

    assert.equal(pipeline.activeMerge(2, 1)?.endCol, 13);
    deps.S.filtered = true;
    assert.equal(pipeline.activeMerge(2, 1), null);
    deps.S.filtered = false;
    deps.S.freeze.cols = 1;
    assert.equal(pipeline.activeMerge(2, 1), null);
    deps.S.freeze.cols = 0;
    deps.S.freeze.rows = 2;
    assert.equal(pipeline.activeMerge(2, 1)?.endCol, 13);
    deps.S.rowVisibility = new RowVisibility(10, [{ min: 2, max: 2 }]);
    assert.equal(pipeline.activeMerge(2, 1), null);
    deps.S.rowVisibility = null;
    assert.equal(pipeline.activeMerge(2, 1)?.endCol, 13);
    deps.S.indexReady = false;
    assert.equal(pipeline.activeMerge(2, 1), null);
  } finally {
    dom.restore();
  }
});
