import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { RowData } from '../contracts/workbook.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import { makeCellData, makeDefaultViewportPipelineDeps } from '../test-helpers/grid-fixture.ts';
import { borderRuns, paintBorder, parseBorder, resolveBorderCorner } from './grid-borders.ts';
import { MergedCells } from './merges.ts';
import type { GridCellElement } from './state.ts';
import { ViewportPipeline } from './viewport-pipeline.ts';

function setupBorders() {
  const dom = installDom({ fromIndexHtml: true });
  const merge = new MergedCells();
  merge.load([
    { row: 8, col: 5, endRow: 9, endCol: 5 },
    { row: 8, col: 6, endRow: 8, endCol: 7 },
    { row: 8, col: 8, endRow: 8, endCol: 9 },
    { row: 8, col: 10, endRow: 9, endCol: 10 },
    { row: 8, col: 11, endRow: 8, endCol: 12 },
  ]);
  const black = '0.667px solid #000000';
  const deps = makeDefaultViewportPipelineDeps({ mergedCells: merge });
  deps.S.indexReady = true;
  deps.S.showStyles = true;
  deps.S.styles = [
    {},
    { borderTop: black, borderBottom: black, borderLeft: black, borderRight: black },
  ];
  const pipeline = new ViewportPipeline(deps);
  const makeNode = (row: number, col: number): GridCellElement => {
    const el = document.createElement('div') as GridCellElement;
    el.dataset.row = String(row);
    el.dataset.col = String(col);
    return el;
  };
  const row: RowData = {
    number: 8,
    cells: [
      makeCellData(6, '', { ref: 'F8', style: 1 }),
      makeCellData(8, '', { ref: 'H8', style: 1 }),
      makeCellData(10, '', { ref: 'J8', style: 1 }),
      makeCellData(5, '', { ref: 'E8', style: 1 }),
      makeCellData(11, '', { ref: 'K8', style: 1 }),
    ],
  };
  return { dom, S: deps.S, pipeline, makeNode, row, black };
}

test('all preceding cells draw a merged header edge at the same trailing pixel as ordinary cells', () => {
  const { dom, pipeline, makeNode, row, black } = setupBorders();
  try {
    const anchor = makeNode(8, 6);
    pipeline.appearance.updateSharedBorders(
      anchor,
      row.cells[0],
      row,
      null,
      row,
      makeCellData(6, '', { ref: 'F9', style: 1 }),
      { number: 9, cells: [] },
    );
    assert.equal(anchor.style.borderTop, '');
    assert.equal(anchor.style.borderLeft, '');
    for (const col of [6, 7]) {
      const above = makeNode(7, col);
      pipeline.appearance.updateSharedBorders(
        above,
        null,
        { number: 7, cells: [] },
        null,
        null,
        col === 6 ? row.cells[0] : null,
        row,
      );
      assert.equal(
        above.style.borderBottom,
        black,
        `covered column ${col} must borrow the merged border`,
      );
    }
  } finally {
    dom.restore();
  }
});

test('adjacent merged headers choose their real outer seam and frozen anchors resolve identically', () => {
  const { dom, S, pipeline, makeNode, row, black } = setupBorders();
  try {
    const left = makeNode(8, 6);
    pipeline.appearance.updateSharedBorders(left, row.cells[0], row, null, row, null, { number: 9, cells: [] });
    assert.equal(left.style.borderRight, black);
    const j8 = row.cells[2];
    assert.ok(j8);
    S.frozenCellMap.set('J8', j8);
    const below = makeNode(9, 9);
    pipeline.appearance.updateSharedBorders(
      below,
      null,
      { number: 9, cells: [] },
      null,
      { number: 9, cells: [] },
      null,
      null,
    );
    assert.equal(below.style.borderRight, black);
    const frozen = makeNode(8, 10);
    pipeline.appearance.updateSharedBorders(frozen, j8, row, null, row, null, { number: 9, cells: [] });
    assert.equal(frozen.style.borderLeft, '');
    assert.equal(frozen.style.borderTop, '');
  } finally {
    dom.restore();
  }
});

test('two-row currency and direction headers retain the entire right border next to one-row merges', () => {
  const { dom, S, pipeline, makeNode, row, black } = setupBorders();
  try {
    const pairs: readonly (readonly [number, number])[] = [
      [5, 6],
      [10, 11],
    ];
    for (const [col, neighbor] of pairs) {
      const tall = row.cells.find((c) => c.col === col);
      const short = row.cells.find((c) => c.col === neighbor);
      assert.ok(tall && short);
      S.cellMap.set(`${String.fromCharCode(64 + col)}8`, tall);
      const a = makeNode(8, col);
      pipeline.appearance.updateSharedBorders(a, tall, row, short, row, null, { number: 9, cells: [] });
      assert.equal(a.style.borderRight, black);
      const b = makeNode(8, neighbor);
      pipeline.appearance.updateSharedBorders(b, short, row, null, row, null, { number: 9, cells: [] });
      assert.equal(b.style.borderLeft, '');
    }
  } finally {
    dom.restore();
  }
});

test('merged XLS/XLSX perimeter reads borders from styled covered corners', () => {
  const { dom, S, pipeline, makeNode, row, black } = setupBorders();
  try {
    S.styles.push({ borderTop: black, borderLeft: black });
    S.styles.push({ borderBottom: black, borderRight: black });
    S.cellMap.set('E9', makeCellData(5, '', { ref: 'E9', style: 3 }));
    const anchor = makeNode(8, 5);
    pipeline.appearance.updateSharedBorders(
      anchor,
      makeCellData(5, '', { ref: 'E8', style: 2 }),
      row,
      null,
      row,
      null,
      { number: 9, cells: [] },
    );
    assert.equal(anchor.style.borderBottom, black);
    assert.equal(anchor.style.borderRight, black);
    S.cellMap.clear();
    S.frozenCellMap.set('G8', makeCellData(7, '', { ref: 'G8', style: 3 }));
    const wide = makeNode(8, 6);
    pipeline.appearance.updateSharedBorders(
      wide,
      makeCellData(6, '', { ref: 'F8', style: 2 }),
      row,
      null,
      row,
      null,
      { number: 9, cells: [] },
    );
    assert.equal(wide.style.borderBottom, black);
    assert.equal(wide.style.borderRight, black);
  } finally {
    dom.restore();
  }
});

test('conditional borders use the same seam and merged-perimeter model and clear when absent or disabled', () => {
  const { dom, S, pipeline, makeNode, row, black } = setupBorders();
  try {
    const red = '1.15px solid #FF0033';
    const blue = '0.55px solid #1255AA';
    const cell = makeCellData(2, '', {
      ref: 'B2',
      style: 1,
      conditional: { visual: { borderRight: red, borderBottom: blue } },
    });
    const edge = makeNode(2, 2);
    const before = JSON.stringify(cell);
    pipeline.appearance.updateSharedBorders(edge, cell, { number: 2, cells: [] }, null, null, null, null);
    assert.equal(edge.style.borderRight, red);
    assert.equal(edge.style.borderBottom, blue);
    assert.equal(JSON.stringify(cell), before);
    pipeline.appearance.updateSharedBorders(
      edge,
      { ...cell, conditional: { visual: {} } },
      { number: 2, cells: [] },
      null,
      null,
      null,
      null,
    );
    assert.equal(edge.style.borderRight, '');
    assert.equal(edge.style.borderBottom, '');
    S.cellMap.set(
      'E9',
      makeCellData(5, '', {
        ref: 'E9',
        style: 1,
        conditional: { visual: { borderRight: red, borderBottom: blue } },
      }),
    );
    S.styles.push({ borderTop: black, borderLeft: black });
    const anchor = makeNode(8, 5);
    pipeline.appearance.updateSharedBorders(
      anchor,
      makeCellData(5, '', { ref: 'E8', style: 2 }),
      row,
      null,
      null,
      null,
      null,
    );
    assert.equal(anchor.style.borderRight, red);
    assert.equal(anchor.style.borderBottom, blue);
    S.showStyles = false;
    pipeline.appearance.updateSharedBorders(
      anchor,
      makeCellData(5, '', { ref: 'E8', style: 2 }),
      row,
      null,
      null,
      null,
      null,
    );
    assert.equal(anchor.style.borderRight, '');
    assert.equal(anchor.style.borderBottom, '');
  } finally {
    dom.restore();
  }
});

test('a fill hides the default seam from either side and keeps explicit neighboring borders', () => {
  const { dom, S, pipeline, makeNode, black } = setupBorders();
  try {
    S.styles.push({ background: '#DBDBDB' });
    const edge = makeNode(2, 2);
    const row: RowData = { number: 2, cells: [] };
    const below: RowData = { number: 3, cells: [] };
    const next = { nextCol: 3 };
    pipeline.appearance.updateSharedBorders(
      edge,
      makeCellData(2, '', { ref: 'B2', style: 2 }),
      row,
      null,
      row,
      null,
      below,
      next,
    );
    assert.equal(edge.style.borderRight, '0px');
    assert.equal(edge.style.borderBottom, '0px');
    pipeline.appearance.updateSharedBorders(
      edge,
      makeCellData(2, '', { ref: 'B2' }),
      row,
      makeCellData(3, '', { ref: 'C2', style: 2 }),
      row,
      makeCellData(2, '', { ref: 'B3', style: 1 }),
      below,
      next,
    );
    assert.equal(edge.style.borderRight, '0px');
    assert.equal(edge.style.borderBottom, black);
    pipeline.appearance.updateSharedBorders(
      edge,
      makeCellData(2, '', { ref: 'B2' }),
      row,
      null,
      row,
      null,
      below,
      next,
    );
    assert.equal(edge.style.borderRight, '');
    assert.equal(edge.style.borderBottom, '');
  } finally {
    dom.restore();
  }
});

test('a long shared edge preserves partial neighbor styles and removes segments when its node is reused', () => {
  const dom = installDom();
  try {
    const black = '0.667px solid #000000';
    const red = '2px dashed #FF0000';
    const runs = borderRuns(
      2,
      6,
      5,
      (n) => n,
      (n) => (n < 4 ? black : n === 4 ? red : ''),
      '',
    );
    assert.ok(Array.isArray(runs));
    assert.deepEqual(
      runs.map((part) => [part.start, part.end, part.style]),
      [
        [2, 4, black],
        [4, 5, red],
        [5, 7, ''],
      ],
    );
    const cell = document.createElement('div') as GridCellElement;
    const offset = (c: number): number => c * 31;
    paintBorder(cell, 'bottom', runs, offset);
    assert.equal(cell.style.borderBottom, '0px');
    const layer = cell.children[0];
    assert.ok(layer);
    assert.deepEqual(
      [...layer.children].map((line) => {
        const el = line as HTMLElement;
        return [el.style.left, el.style.width, el.style.borderBottom];
      }),
      [
        ['0px', '62px', black],
        ['62px', '31px', red],
        ['93px', '62px', 'var(--grid-default-seam)'],
      ],
    );
    paintBorder(cell, 'bottom', '', offset, black);
    assert.equal(cell.children[0]?.className, 'grid-default-bottom');
    assert.equal(cell.children[0].children.length, 0);
    paintBorder(cell, 'bottom', black, offset);
    assert.equal(cell.children.length, 0);
    assert.equal(cell.style.borderBottom, black);
  } finally {
    dom.restore();
  }
});

test('resolveBorderCorner closes missing top-left junctions and skips covered or weaker intersections', () => {
  assert.deepEqual(parseBorder('3px solid #000000'), {
    css: '3px solid #000000',
    width: 3,
    kind: 'solid',
    color: '#000000',
    score: 305,
  });
  assert.equal(parseBorder('0px'), null);
  assert.equal(parseBorder(''), null);

  assert.deepEqual(resolveBorderCorner('', '', '3px solid #000000', '3px solid #000000'), {
    width: 3,
    height: 3,
    kind: 'solid',
    color: '#000000',
  });
  assert.deepEqual(resolveBorderCorner('', '', '3px solid #000000', '1px solid #000000'), {
    width: 1,
    height: 3,
    kind: 'solid',
    color: '#000000',
  });
  assert.deepEqual(resolveBorderCorner('', '', '3px double #112233', '3px double #112233'), {
    width: 3,
    height: 3,
    kind: 'double',
    color: '#112233',
  });
  assert.deepEqual(resolveBorderCorner('', '', '3px double #000000', '3px solid #000000'), {
    width: 3,
    height: 3,
    kind: 'solid',
    color: '#000000',
  });
  assert.equal(
    resolveBorderCorner('3px solid #000000', '', '3px solid #000000', '3px solid #000000'),
    null,
    'an existing thick north edge already covers the corner',
  );
  assert.equal(
    resolveBorderCorner('', '3px solid #000000', '3px solid #000000', '3px solid #000000'),
    null,
    'an existing thick west edge already covers the corner',
  );
  assert.equal(
    resolveBorderCorner('3px solid #FF0000', '3px solid #FF0000', '1px solid #000000', '1px solid #000000'),
    null,
    'a weaker diagonal corner must not dent a thicker north/west border',
  );
  assert.deepEqual(
    resolveBorderCorner('1px solid #888888', '1px solid #888888', '3px solid #000000', '3px solid #000000'),
    {
      width: 3,
      height: 3,
      kind: 'solid',
      color: '#000000',
    },
    'a thicker diagonal corner extends over thinner north/west borders',
  );
});

test('thick, double, and nested box top-left corners join cleanly and clear on reuse or style toggle', () => {
  const { dom, S, pipeline, makeNode } = setupBorders();
  try {
    const thick = '3px solid #000000';
    const thin = '1px solid #888888';
    const dbl = '3px double #000000';
    S.styles.push(
      { background: '#FFC000', borderTop: thick, borderLeft: thick }, // style 2
      { borderRight: thin, borderBottom: thin }, // style 3
      { borderTop: dbl, borderLeft: dbl }, // style 4
    );
    const row1: RowData = { number: 1, cells: [makeCellData(2, 'N', { ref: 'B1' })] };
    const row2: RowData = { number: 2, cells: [makeCellData(2, '[000', { ref: 'B2', style: 2 })] };
    const a1 = makeNode(1, 1);
    pipeline.appearance.updateSharedBorders(a1, null, row1, row1.cells[0], row1, null, row2, {
      nextCol: 2,
      belowRightCell: row2.cells[0],
      mergeFree: true,
    });
    assert.equal(a1.style.borderRight, '0px');
    assert.equal(a1.style.borderBottom, '0px');
    assert.equal(a1._xlborderRightParts?.className, 'grid-default-right');
    assert.equal(a1._xlborderBottomParts?.className, 'grid-default-bottom');
    const thickCap = a1._xlCornerCap;
    assert.ok(thickCap);
    assert.equal(thickCap.className, 'grid-border-corner');
    assert.equal(thickCap.style.borderRight, '3px solid transparent');
    assert.equal(thickCap.style.borderBottom, '3px solid transparent');
    assert.equal(thickCap.style.backgroundColor, '#000000');

    // Thin explicit borders on A1 are inset so the 3x3 thick corner cap sits flush at the outer edge.
    const thinA1 = makeCellData(1, '', { ref: 'A1', style: 3 });
    pipeline.appearance.updateSharedBorders(a1, thinA1, row1, row1.cells[0], row1, null, row2, {
      nextCol: 2,
      belowRightCell: row2.cells[0],
      mergeFree: true,
    });
    assert.equal(a1.style.borderRight, '0px');
    assert.equal(a1.style.borderBottom, '0px');
    const insetR = a1._xlborderRightParts;
    const insetB = a1._xlborderBottomParts;
    assert.equal(insetR.className, 'grid-inset-border');
    assert.equal(insetR.style.borderRight, thin);
    assert.equal(insetB.className, 'grid-inset-border');
    assert.equal(insetB.style.borderBottom, thin);
    const thinCap = a1._xlCornerCap;
    assert.ok(thinCap);
    assert.equal(thinCap.style.backgroundColor, '#000000');

    // Double border corner uses border-top + border-left double miter.
    const dblRow2: RowData = { number: 2, cells: [makeCellData(2, '', { ref: 'B2', style: 4 })] };
    pipeline.appearance.updateSharedBorders(a1, null, row1, row1.cells[0], row1, null, dblRow2, {
      nextCol: 2,
      belowRightCell: dblRow2.cells[0],
      mergeFree: true,
    });
    const dblCap = a1._xlCornerCap;
    assert.ok(dblCap);
    assert.equal(dblCap.style.borderTop, dbl);
    assert.equal(dblCap.style.borderLeft, dbl);
    assert.equal(dblCap.style.backgroundColor, '');

    // Recycling for a plain diagonal neighbor clears the corner cap and restores normal seams.
    pipeline.appearance.updateSharedBorders(a1, null, row1, null, row1, null, { number: 2, cells: [] }, {
      nextCol: 2,
      belowRightCell: null,
      mergeFree: true,
    });
    assert.equal(a1._xlCornerCap, null);
    assert.equal(a1.children.length, 0);
    assert.equal(a1.style.borderRight, '');
    assert.equal(a1.style.borderBottom, '');

    // Disabling source styles removes the corner cap immediately.
    pipeline.appearance.updateSharedBorders(a1, null, row1, row1.cells[0], row1, null, row2, {
      nextCol: 2,
      belowRightCell: row2.cells[0],
      mergeFree: true,
    });
    assert.ok(a1._xlCornerCap);
    S.showStyles = false;
    pipeline.appearance.updateSharedBorders(a1, null, row1, row1.cells[0], row1, null, row2, {
      nextCol: 2,
      belowRightCell: row2.cells[0],
      mergeFree: true,
    });
    assert.equal(a1._xlCornerCap, null);
    assert.equal(a1.children.length, 0);
  } finally {
    dom.restore();
  }
});

test('merged cells suppress phantom interior corners and close interior T-junction transitions', () => {
  const dom = installDom({ fromIndexHtml: true });
  try {
    const merge = new MergedCells();
    merge.load([
      { row: 2, col: 1, endRow: 2, endCol: 2 }, // A2:B2 horizontal merge
      { row: 4, col: 1, endRow: 5, endCol: 1 }, // A4:A5 vertical merge beside B4 (plain) & B5 (thick)
      { row: 7, col: 1, endRow: 7, endCol: 2 }, // A7:B7 horizontal merge above A8 (plain) & B8 (thick)
    ]);
    const thick = '3px solid #000000';
    const deps = makeDefaultViewportPipelineDeps({ mergedCells: merge });
    deps.S.indexReady = true;
    deps.S.showStyles = true;
    deps.S.styles = [
      {},
      { borderTop: thick, borderBottom: thick, borderLeft: thick, borderRight: thick },
    ];
    const pipeline = new ViewportPipeline(deps);
    const makeNode = (r: number, c: number): GridCellElement => {
      const el = document.createElement('div') as GridCellElement;
      el.dataset.row = String(r);
      el.dataset.col = String(c);
      return el;
    };

    const a2 = makeCellData(1, '', { ref: 'A2', style: 1 });
    deps.S.cellMap.set('A2', a2);
    const a1 = makeNode(1, 1);
    pipeline.appearance.updateSharedBorders(
      a1,
      null,
      { number: 1, cells: [] },
      null,
      { number: 1, cells: [] },
      a2,
      { number: 2, cells: [a2] },
      { nextCol: 2, belowRightCell: null },
    );
    assert.equal(a1._xlCornerCap ?? null, null, 'horizontal merge A2:B2 has no interior vertical seam');

    const b5 = makeCellData(2, '', { ref: 'B5', style: 1 });
    deps.S.cellMap.set('B5', b5);
    const a4 = makeNode(4, 1);
    pipeline.appearance.updateSharedBorders(
      a4,
      null,
      { number: 4, cells: [] },
      null,
      { number: 4, cells: [] },
      null,
      { number: 6, cells: [] },
      { columns: [1, 2], firstRow: 4, lastRow: 5 },
    );
    const vLayer = a4._xlInnerCorners;
    assert.ok(vLayer);
    assert.equal(vLayer.className, 'grid-border-corners');
    assert.equal(vLayer.children.length, 1);
    const vCap = vLayer.children[0] as HTMLElement;
    assert.equal(vCap.style.backgroundColor, '#000000');
    assert.equal(vCap.style.right, '0');
    assert.ok(vCap.style.bottom.startsWith('calc(100% - '));

    const b8 = makeCellData(2, '', { ref: 'B8', style: 1 });
    deps.S.cellMap.set('B8', b8);
    const a7 = makeNode(7, 1);
    pipeline.appearance.updateSharedBorders(
      a7,
      null,
      { number: 7, cells: [] },
      null,
      { number: 7, cells: [] },
      null,
      { number: 8, cells: [b8] },
      { columns: [1, 2], firstRow: 7, lastRow: 8 },
    );
    const hLayer = a7._xlInnerCorners;
    assert.ok(hLayer);
    assert.equal(hLayer.className, 'grid-border-corners');
    assert.equal(hLayer.children.length, 1);
    const hCap = hLayer.children[0] as HTMLElement;
    assert.equal(hCap.style.backgroundColor, '#000000');
    assert.equal(hCap.style.bottom, '0');
    assert.ok(hCap.style.right.startsWith('calc(100% - '));
  } finally {
    dom.restore();
  }
});
