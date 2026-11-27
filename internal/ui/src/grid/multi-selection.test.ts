import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { CellPoint, SelectionArea } from '../contracts/common.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import { MultiSelection } from './multi-selection.ts';
import {
  paintCrosshairTails,
  paintRangeFrames,
  type RangeFrameView,
} from './range-frame.ts';

function rect(a: CellPoint, b: CellPoint, filtered = false): SelectionArea {
  const area = MultiSelection.rectangle(a, b, filtered);
  assert.ok(area);
  return area;
}

test('a sorted range crossing frozen rows keeps pinned and body coordinates distinct', () => {
  const range = MultiSelection.rectangle(
    MultiSelection.cell(2, 2, 2),
    MultiSelection.cell(98, 4, 3),
    true,
  );
  assert.equal(MultiSelection.contains(range, 1, 3, 1, true, 2), false);
  for (const [row, visual] of [
    [2, 2],
    [83, 1],
    [98, 3],
  ] as const) {
    assert.equal(MultiSelection.contains(range, row, 3, visual, true, 2), true);
  }
  assert.equal(MultiSelection.contains(range, 77, 3, 4, true, 2), false);
});

test('frozen and scrolled panes draw only the actual edges and one endpoint', () => {
  const dom = installDom();
  try {
    const layer = document.createElement('div');
    const view: RangeFrameView = {
      width: 220,
      height: 120,
      left: 15,
      top: 10,
      rowHead: 20,
      header: 20,
      frozenWidth: 40,
      frozenHeight: 20,
      colLeft: (c) => (c - 1) * 40,
      rowBounds: (r) => [(r - 1) * 20, r * 20],
    };
    const area = rect(MultiSelection.cell(1, 1), MultiSelection.cell(5, 4));
    paintRangeFrames(layer, [area], view);
    const frames = [...layer.children].filter((n) => n.className === 'range-frame');
    assert.equal(frames.length, 4);
    assert.equal(layer.children.length, 5);
    assert.ok((frames[0] as HTMLElement).style.cssText.includes('border-width: 2px 0 0 2px'));
    assert.ok((frames[0] as HTMLElement).style.cssText.startsWith('left: 20px; top: 20px;'));
    assert.ok((frames[3] as HTMLElement).style.cssText.includes('border-width: 0 2px 2px 0'));
    assert.ok(
      (layer.lastElementChild as HTMLElement).style.cssText.startsWith(
        'left: 160.5px; top: 105.5px; width: 7px; height: 7px',
      ),
    );

    const single = rect(MultiSelection.cell(3, 3), MultiSelection.cell(3, 3));
    paintRangeFrames(layer, [single], view);
    assert.ok(
      (layer.children[0] as HTMLElement).style.cssText.startsWith(
        'left: 85px; top: 50px; width: 40px; height: 20px;',
      ),
    );
    assert.ok(
      (layer.lastElementChild as HTMLElement).style.cssText.startsWith(
        'left: 120.5px; top: 65.5px; width: 7px; height: 7px',
      ),
    );

    paintRangeFrames(layer, [single], {
      ...view,
      left: 0,
      top: 0,
      frozenWidth: 0,
      frozenHeight: 0,
      colLeft: (c) => (c - 1) * 2,
      rowBounds: (r) => [(r - 1) * 4, r * 4],
    });
    assert.ok(
      (layer.children[0] as HTMLElement).style.cssText.includes(
        'width: 2px; height: 4px; border-width: 2px 1px 2px 1px',
      ),
    );
    paintRangeFrames(layer, [area], { ...view, width: 110, height: 80 });
    assert.ok([...layer.children].every((n) => n.className === 'range-frame'));
    paintRangeFrames(layer, [], view);
    assert.equal(layer.children.length, 0);
  } finally {
    dom.restore();
  }
});

test('source thickness moves the centre of the selection without changing its pen', () => {
  const dom = installDom();
  try {
    const layer = document.createElement('div');
    let offset = -2;
    const view: RangeFrameView = {
      width: 250,
      height: 150,
      top: 0,
      left: 0,
      frozenWidth: 0,
      frozenHeight: 0,
      header: 20,
      rowHead: 20,
      colLeft: (c) => (c - 1) * 40,
      rowBounds: (r) => [(r - 1) * 20, r * 20],
      seams: (x1, x2, y1, y2) => ({
        top: [{ from: x1, to: x2, offset: -1 }],
        bottom: [{ from: x1, to: x2, offset: offset / 2 }],
        left: [{ from: y1, to: y2, offset }],
        right: [{ from: y1, to: y2, offset }],
      }),
    };
    const numAttr = (el: Element | undefined, name: string): number =>
      Number(el?.getAttribute(name) ?? NaN);
    for (offset of [-2, -1, -0.5, 0]) {
      paintRangeFrames(
        layer,
        [rect(MultiSelection.cell(2, 2), MultiSelection.cell(2, 2))],
        view,
      );
      const svg = layer.firstElementChild?.firstElementChild;
      const rects = [...(svg?.children ?? [])];
      const [top, bottom, left, right, handle, face] = rects;
      assert.deepEqual(
        [
          numAttr(top, 'height'),
          numAttr(bottom, 'height'),
          numAttr(left, 'width'),
          numAttr(right, 'width'),
        ],
        [2, 2, 2, 2],
      );
      assert.deepEqual(
        [
          numAttr(top, 'y') + 1,
          numAttr(bottom, 'y') + 1,
          numAttr(left, 'x') + 1,
          numAttr(right, 'x') + 1,
        ],
        [-1, 20 + offset / 2, offset, 40 + offset],
      );
      for (const [square, size] of [
        [handle, 7],
        [face, 5],
      ] as const) {
        assert.deepEqual([numAttr(square, 'width'), numAttr(square, 'height')], [size, size]);
        assert.deepEqual(
          [numAttr(square, 'x') + size / 2, numAttr(square, 'y') + size / 2],
          [
            numAttr(right, 'x') + numAttr(right, 'width') / 2,
            numAttr(bottom, 'y') + numAttr(bottom, 'height') / 2,
          ],
        );
      }
    }
  } finally {
    dom.restore();
  }
});

test('MultiSelection core containment, areas, removeSingleton, points, and rowCount invariants', () => {
  const a = MultiSelection.cell(1, 2);
  const b = MultiSelection.cell(3, 4);
  const r = rect(a, b);
  assert.equal(MultiSelection.contains(r, 2, 3, 0), true);
  assert.equal(MultiSelection.contains(r, 4, 3, 0), false);
  const filtered = rect(
    MultiSelection.cell(100, 2, 7),
    MultiSelection.cell(5, 4, 9),
    true,
  );
  assert.equal(MultiSelection.contains(filtered, 998, 3, 8, true), true);
  assert.equal(MultiSelection.contains(filtered, 5, 3, 10, true), false);
  const selected = {
    multiRanges: [r],
    range: filtered,
    selected: { row: 5, col: 4 },
    selectedVisualIndex: 9,
  };
  assert.equal(MultiSelection.areas(selected).length, 2);
  assert.equal(
    MultiSelection.areas(
      { selected: { row: 3, col: 1 }, selectedVisualIndex: 3, filtered: false },
      true,
    ).length,
    1,
  );
  assert.deepEqual(MultiSelection.removeSingleton([r, rect(a, a)], 1, 2, 1, false), [
    r,
  ]);
  assert.equal(MultiSelection.removeSingleton([r], 2, 3, 2, false), null);
  assert.deepEqual(
    MultiSelection.points(r, (c) => String.fromCharCode(64 + c)),
    { start: 'B1', end: 'D3' },
  );

  for (const area of [
    { row1: 1, row2: 8, col1: 1, col2: 1, visual1: 1, visual2: 2 },
    { row1: 8, row2: 1, col1: 1, col2: 1, visual1: 2, visual2: 1 },
  ]) {
    assert.equal(MultiSelection.rowCount(area, true, 3), 5);
  }
  assert.equal(
    MultiSelection.rowCount({ row1: 8, row2: 4, col1: 1, col2: 1, visual1: 1, visual2: 2 }, true, 3),
    2,
  );
  assert.equal(
    MultiSelection.rowCount({ row1: 3, row2: 1, col1: 1, col2: 1, visual1: 3, visual2: 1 }, true, 3),
    3,
  );
});

test('highlight spans extend to ranges, multi-ranges, filtered frozen crossings and exclude columns on whole-row selection', () => {
  const rangeHl = MultiSelection.highlight({
    range: MultiSelection.rectangle(MultiSelection.cell(5, 8), MultiSelection.cell(8, 11)),
    multiRanges: [],
    selected: { row: 5, col: 8 },
  });
  assert.deepEqual(rangeHl.rowSpans, [[5, 8]]);
  assert.deepEqual(rangeHl.colSpans, [[8, 11]]);
  assert.equal(rangeHl.hasMultiCol, true);
  assert.equal(rangeHl.singleCol, 0);
  for (const r of [5, 6, 7, 8]) assert.equal(rangeHl.hasRow(r, r), true);
  assert.equal(rangeHl.hasRow(4, 4), false);
  assert.equal(rangeHl.hasRow(9, 9), false);
  for (const c of [8, 9, 10, 11]) assert.equal(rangeHl.hasCol(c), true);
  assert.equal(rangeHl.hasCol(7), false);
  assert.equal(rangeHl.hasCol(12), false);

  const multiHl = MultiSelection.highlight({
    multiRanges: [
      rect(MultiSelection.cell(2, 2), MultiSelection.cell(3, 3)),
      rect(MultiSelection.cell(4, 5), MultiSelection.cell(5, 6)),
    ],
    range: rect(MultiSelection.cell(7, 3), MultiSelection.cell(8, 4)),
    selected: { row: 7, col: 3 },
  });
  assert.deepEqual(multiHl.rowSpans, [
    [2, 5],
    [7, 8],
  ]);
  assert.deepEqual(multiHl.colSpans, [[2, 6]]);
  assert.equal(multiHl.hasRow(6, 6), false);
  assert.equal(multiHl.hasRow(7, 7), true);

  const filteredRange = MultiSelection.rectangle(
    MultiSelection.cell(2, 2, 2),
    MultiSelection.cell(98, 4, 3),
    true,
  );
  const filteredHl = MultiSelection.highlight({
    filtered: true,
    selectedFrozenThrough: 2,
    range: filteredRange,
    multiRanges: [],
    selected: { row: 2, col: 2 },
  });
  assert.equal(filteredHl.hasRow(1, 1), false);
  assert.equal(filteredHl.hasRow(2, 2), true);
  assert.equal(filteredHl.hasRow(83, 1), true);
  assert.equal(filteredHl.hasRow(98, 3), true);
  assert.equal(filteredHl.hasRow(77, 4), false);

  const wholeRowHl = MultiSelection.highlight({
    wholeRowSelected: true,
    selected: { row: 5, col: 1 },
    range: { row1: 5, row2: 5, col1: 1, col2: 20, visual1: 5, visual2: 5 },
    multiRanges: [],
  });
  assert.deepEqual(wholeRowHl.rowSpans, [[5, 5]]);
  assert.deepEqual(wholeRowHl.colSpans, []);
  assert.equal(wholeRowHl.hasRow(5, 5), true);
  assert.equal(wholeRowHl.hasCol(1), false);
  assert.equal(wholeRowHl.hasCol(8), false);

  const hiddenWholeRow = MultiSelection.highlight({
    filtered: true,
    selectedFrozenThrough: 2,
    wholeRowSelected: true,
    selected: { row: 9, col: 1 },
    selectedVisualIndex: 0,
    multiRanges: [],
  });
  assert.deepEqual(hiddenWholeRow.rowSpans, []);
  assert.equal(hiddenWholeRow.hasRow(9, 9), false);
  assert.equal(MultiSelection.containsRow(filteredRange, 2, 0, true, 2), true);
  assert.equal(filteredHl.hasRow(2, 0), true);
  assert.equal(filteredHl.hasRow(83, 0), false);
});

test('paintCrosshair projects range and multi-range tails without overlapping strips', () => {
  const dom = installDom();
  try {
    const layer = document.createElement('div');
    const view: RangeFrameView = {
      width: 300,
      height: 200,
      left: 0,
      top: 0,
      rowHead: 20,
      header: 20,
      frozenWidth: 0,
      frozenHeight: 0,
      colLeft: (c) => (c - 1) * 30,
      rowBounds: (r) => [(r - 1) * 20, r * 20],
    };
    paintCrosshairTails(
      layer,
      [rect(MultiSelection.cell(2, 2), MultiSelection.cell(4, 3))],
      view,
      150,
      120,
    );
    assert.equal(layer.children.length, 2);
    const tail0 = layer.children[0] as HTMLElement;
    const tail1 = layer.children[1] as HTMLElement;
    assert.equal(tail0.className, 'row-tail');
    assert.equal(tail0.hidden, false);
    assert.equal(tail0.style.cssText, 'left: 170px; top: 40px; width: 130px; height: 60px;');
    assert.equal(tail1.className, 'column-tail');
    assert.equal(tail1.hidden, false);
    assert.equal(tail1.style.cssText, 'left: 50px; top: 140px; width: 60px; height: 60px;');

    paintCrosshairTails(
      layer,
      [
        rect(MultiSelection.cell(1, 1), MultiSelection.cell(2, 2)),
        rect(MultiSelection.cell(4, 4), MultiSelection.cell(5, 4)),
      ],
      view,
      150,
      120,
    );
    assert.equal([...layer.children].filter((n) => n.className === 'row-tail').length, 2);
    assert.equal([...layer.children].filter((n) => n.className === 'column-tail').length, 2);
  } finally {
    dom.restore();
  }
});
