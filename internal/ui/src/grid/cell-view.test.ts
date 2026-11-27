import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { MergeRange } from '../contracts/common.ts';
import type { CellData, RowData, SheetViewportResponse, VisualStyle } from '../contracts/workbook.ts';
import { unpackWireRows } from '../core/wire.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import { makeDefaultViewportPipelineDeps } from '../test-helpers/grid-fixture.ts';
import {
  cellDisplayValue,
  CellPresenter,
  selectionFill,
  type ColumnLayoutItem,
  type PresenterOptions,
} from './cell-view.ts';
import { FrozenPanes } from './freeze.ts';
import { createInitialWorkbookState, type GridCellElement, type ViewportRequest } from './state.ts';
import { ViewportPipeline } from './viewport-pipeline.ts';

function setup() {
  const dom = installDom();
  const measured: number[] = [];

  // Override canvas 2d measureText for deterministic width measurements (10px/char)
  const origCreate = dom.document.createMockElement.bind(dom.document);
  dom.document.createMockElement = (tag: string) => {
    const el = origCreate(tag);
    if (tag.toLowerCase() === 'canvas') {
      Reflect.set(el, 'getContext', () => ({
        font: '',
        measureText: (s: string) => {
          measured.push(s.length);
          return { width: s.length * 10 };
        },
      }));
    }
    return el;
  };

  const state = createInitialWorkbookState(1_048_576, 16_384);
  state.showFormulas = false;
  state.showStyles = true;
  state.styles = [{}];
  state.zoom = 100;
  state.indexReady = true;
  state.source = { format: 'xlsx' };

  const appliedStyles = new WeakMap<Element, number>();
  let currentScale = 1;
  let mergeFn: (row: number, col: number) => MergeRange | null = () => null;
  const richLimits = { cellRuns: 4096, viewRuns: 8192 };

  const options: PresenterOptions = {
    state,
    applyStyle: (node, c) => {
      appliedStyles.set(node, c.style ?? 0);
    },
    colName: (c) => String.fromCharCode(c + 64),
    scale: () => currentScale,
    fontStack: (s) => s,
    merge: (row, col) => mergeFn(row, col),
    richLimits,
  };
  const presenter = new CellPresenter(options);

  const makeNode = (): GridCellElement => {
    const n = document.createElement('div') as GridCellElement;
    n.className = 'grid-cell';
    n.dataset.row = '2';
    n.dataset.col = '1';
    n.dataset.ref = 'A2';
    n.style.width = '100px';
    return n;
  };
  const layouts: ColumnLayoutItem[] = [1, 2, 3, 4].map((col) => ({ col, width: 100 }));

  return {
    state,
    presenter,
    appliedStyles,
    makeNode,
    layouts,
    measured,
    richLimits,
    setScale: (s: number) => {
      currentScale = s;
    },
    setMerge: (fn: (row: number, col: number) => MergeRange | null) => {
      mergeFn = fn;
    },
    restore: () => dom.restore(),
  };
}

const longText = '审计财务说明'.repeat(8);

function makeCell(extra: Partial<CellData>): CellData {
  const raw = extra.raw ?? '';
  return { ref: 'A2', col: 1, raw, display: extra.display ?? raw, hasFormula: false, ...extra };
}

test('General fits its precision while fixed format keeps its digits and regional dates retain their source', () => {
  const h = setup();
  try {
    const n = h.makeNode();
    const cell = makeCell({ raw: '278606.25', display: '278606.25', type: 'n', style: 0 });
    h.presenter.paint(n, cell, {});
    h.presenter.fitNumber(n, cell, 80, 0);
    assert.equal(n.textContent, '278606.3');
    h.presenter.fitNumber(n, cell, 100, 0);
    assert.equal(n.textContent, cell.raw);

    const fixed = { ...cell, displayNumFmt: 2 };
    h.presenter.paint(n, fixed, {});
    h.presenter.fitNumber(n, fixed, 80, 0);
    assert.match(n.textContent, /^#+$/);

    const date = makeCell({
      raw: '60',
      display: '1900-02-29',
      type: 'n',
      displayNumFmt: 14,
      shortDate: 'yyyy/m/d',
    });
    assert.equal(h.presenter.value(date), '1900/2/29');
    assert.equal(date.display, '1900-02-29');
    assert.equal(h.presenter.value({ ...date, type: 'inlineStr', raw: date.display }), date.display);
  } finally {
    h.restore();
  }
});

test('source numeric overflow shows hashes instead of a misleading suffix and restores exact spans when widened', () => {
  const h = setup();
  try {
    const cell = makeCell({
      col: 1,
      raw: '9007199254740993.31',
      display: '9,007,199,254,740,993.31',
      type: 'n',
      style: 0,
    });
    const before = JSON.stringify(cell);
    const node = h.makeNode();
    h.presenter.paint(node, cell, {});
    const original = node.childNodes[0];
    assert.equal(h.presenter.fitNumber(node, cell, 100, 14), true);
    assert.match(node.textContent, /^#+$/);
    assert.match(node.title, /9,007,199,254,740,993\.31/);
    assert.equal(node._xlModel, cell);
    assert.equal(JSON.stringify(cell), before);

    const measurements = h.measured.length;
    h.presenter.fitNumber(node, cell, 110, 14);
    assert.equal(h.measured.length, measurements);

    const duplicate = { ...cell };
    const other = h.makeNode();
    h.presenter.paint(other, duplicate, {});
    h.presenter.fitNumber(other, duplicate, 100, 14);
    assert.equal(
      h.measured.length,
      measurements,
      'equal value/font remeasured across different source cells',
    );
    assert.equal(h.presenter.fitNumber(node, cell, 400, 14), false);
    assert.equal(node.childNodes[0], original);
    assert.equal(node.textContent, cell.display);
    assert.equal(node.title, '');
  } finally {
    h.restore();
  }
});

test('width markers respect source text/boolean/formula-cache semantics and preserve numeric date rich spans', () => {
  const h = setup();
  try {
    const node = h.makeNode();
    for (const cell of [
      makeCell({ raw: '000000000000001', display: '000000000000001', type: 'inlineStr' }),
      makeCell({ raw: '1', display: 'TRUE', type: 'b' }),
      makeCell({ raw: '', display: '', hasFormula: true, cacheMissing: true }),
    ]) {
      h.presenter.paint(node, cell, {});
      assert.equal(h.presenter.fitNumber(node, cell, 10, 0), false);
      assert.equal(node.textContent, cell.display);
    }
    const cell = makeCell({
      raw: '45292',
      display: '2024-01-01',
      type: 'n',
      conditional: { style: { font: { vertical: 'superscript' } }, visual: { fontSize: 12 } },
    });
    h.presenter.prepare([{ number: 2, cells: [cell] }]);
    h.presenter.paint(node, cell, {});
    const rich = node._xlRichNode;
    assert.ok(h.presenter.fitNumber(node, cell, 20, 0));
    assert.equal(h.presenter.fitNumber(node, cell, 400, 0), false);
    assert.equal(node._xlRichNode, rich);
    assert.equal(node.textContent, cell.display);

    h.presenter.fitNumber(node, cell, 20, 0);
    h.presenter.paint(node, makeCell({ raw: '短文', display: '短文', type: 'inlineStr' }), {});
    assert.equal(node.textContent, '短文');
    assert.ok(!node.title.includes('宽度不足'));
  } finally {
    h.restore();
  }
});

test('numeric fitting uses inherited row wrapping and the full merged width without changing source values', () => {
  const h = setup();
  try {
    const cell = makeCell({ raw: '1234567890', display: '1,234,567,890' });
    const node = h.makeNode();
    h.state.styles[1] = { wrap: true };
    h.presenter.paint(node, cell, { style: 1 });
    h.presenter.overflow(node, cell, { 1: cell }, h.layouts, 0);
    assert.equal(node.textContent, cell.display);
    assert.equal(h.measured.length, 0);

    const unstyled = { ...cell, style: 0 };
    h.presenter.paint(node, unstyled, { style: 1 });
    h.presenter.overflow(node, unstyled, { 1: cell }, h.layouts, 0);
    assert.match(node.textContent, /^#+$/);

    h.setMerge(() => ({ row: 2, col: 1, endRow: 2, endCol: 3 }));
    node.style.width = '300px';
    h.presenter.overflow(node, unstyled, { 1: cell }, h.layouts, 0);
    assert.equal(node.textContent, cell.display);
    assert.equal(node._xlOverflowSpan, undefined);
    assert.equal(cell.raw, '1234567890');
  } finally {
    h.restore();
  }
});

test('ordinary source XF fonts retain superscripts and double underlines without a live condition', () => {
  const h = setup();
  try {
    const cell = makeCell({ col: 1, raw: '中😀', display: '中😀', style: 0 });
    const node = h.makeNode();
    h.state.styles[0] = {
      fontSize: 12,
      fontVertical: 'superscript',
      underline: true,
      underlineStyle: 'double',
    };
    h.presenter.prepare([{ number: 2, cells: [cell] }]);
    h.presenter.paint(node, cell, {});
    const span = node._xlRichNode?.childNodes[0] as HTMLElement | undefined;
    assert.ok(span);
    assert.equal(span.textContent, cell.raw);
    assert.equal(span.style.getPropertyValue('vertical-align'), 'super');
    assert.equal(span.style.getPropertyValue('text-decoration-style'), 'double');
    assert.equal(span.style.getPropertyValue('font-size'), '10.4px');

    const explicit: CellData = {
      ...cell,
      richText: { runs: [{ end: 3, font: { vertical: 'baseline', underline: 'none' } }] },
    };
    h.presenter.prepare([{ number: 2, cells: [explicit] }]);
    h.presenter.paint(node, explicit, {});
    const span2 = node._xlRichNode?.childNodes[0] as HTMLElement | undefined;
    assert.equal(span2?.style.getPropertyValue('vertical-align'), 'baseline');
    assert.equal(span2.style.getPropertyValue('text-decoration-line'), 'none');
  } finally {
    h.restore();
  }
});

test('conditional font properties override rich runs and derived display never replaces source evidence', () => {
  const h = setup();
  try {
    const text = '甲😀乙';
    const cell = makeCell({
      col: 1,
      raw: text,
      display: text,
      type: 'inlineStr',
      style: 0,
      richText: {
        runs: [
          { end: 3, font: { bold: true, color: '#112233', underline: 'single' } },
          { end: 4, font: { italic: true } },
        ],
      },
      conditional: {
        style: { font: { bold: false, color: '#FF0033', underline: 'none' } },
        visual: { bold: false, color: '#FF0033', fontSize: 11 },
        matched: 1,
      },
    });
    const before = JSON.stringify(cell);
    const node = h.makeNode();
    h.presenter.prepare([{ number: 2, cells: [cell] }]);
    h.presenter.paint(node, cell, {});
    assert.equal(node.textContent, text);
    for (const child of node._xlRichNode?.childNodes ?? []) {
      const span = child as HTMLElement;
      assert.equal(span.style.getPropertyValue('font-weight'), '400');
      assert.equal(span.style.getPropertyValue('--rich-color'), '#FF0033');
      assert.equal(span.style.getPropertyValue('text-decoration-line'), 'none');
    }
    assert.equal(JSON.stringify(cell), before);
    assert.equal(h.presenter.style(cell)?.color, '#FF0033');

    const numeric = makeCell({
      raw: '9007199254740993.31',
      display: 'source display',
      conditional: { display: 'derived display', style: { font: {} } },
    });
    assert.equal(h.presenter.value(numeric), 'derived display');
    h.state.showStyles = false;
    assert.equal(h.presenter.value(numeric), 'source display');
    h.state.showStyles = true;
    h.presenter.paint(node, cell, { presentationNotice: '条件格式读取超限' });
    assert.equal(node.title, '条件格式读取超限');
    assert.ok(node.classList.contains('cell-presentation-notice'));
    assert.ok(h.presenter.needsPaint(node, cell, {}));
    h.presenter.paint(node, makeCell({ raw: 'plain', display: 'plain' }), {});
    assert.equal(node.title, '');
    assert.ok(!node.classList.contains('cell-presentation-notice'));
  } finally {
    h.restore();
  }
});

test('plain conditional superscripts share span budget and full text survives a fallback', () => {
  const h = setup();
  try {
    const cell = makeCell({
      raw: '中😀',
      display: '中😀',
      conditional: { style: { font: { vertical: 'superscript' } }, visual: { fontSize: 12 } },
    });
    const node = h.makeNode();
    h.presenter.prepare([{ number: 2, cells: [cell] }]);
    h.presenter.paint(node, cell, {});
    assert.equal(
      (node._xlRichNode?.childNodes[0] as HTMLElement | undefined)?.style.getPropertyValue(
        'vertical-align',
      ),
      'super',
    );
    assert.equal(node.textContent, cell.raw);
    h.richLimits.viewRuns = 1;
    h.presenter.prepare([{ number: 2, cells: [cell] }]);
    h.presenter.paint(node, cell, {});
    assert.equal(node._xlRichNode, null);
    assert.equal(node.textContent, cell.raw);
    assert.match(node.title, /容量/);
  } finally {
    h.restore();
  }
});

test('range tint remains distinct on colored fills while retaining source text contrast', () => {
  const rgb = (hex: string): number[] =>
    [0, 2, 4].map((i) => parseInt(hex.slice(1 + i, 3 + i), 16));
  const luminance = (c: readonly number[]): number =>
    c.reduce((n, v, i) => {
      const scaled = v / 255;
      const weights = [0.2126, 0.7152, 0.0722];
      return (
        n +
        (weights[i] ?? 0) * (scaled <= 0.04045 ? scaled / 12.92 : ((scaled + 0.055) / 1.055) ** 2.4)
      );
    }, 0);
  const contrast = (a: number, b: number): number =>
    (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);

  for (const [background, foreground] of [
    ['#ffffff', '#304355'],
    ['#2f79cf', '#ffffff'],
    ['#fff176', '#304355'],
    ['#008000', '#ffffff'],
    ['#000000', '#ffffff'],
    ['#dbe8f6', '#304355'],
    ['#e53935', '#ffffff'],
  ] as const) {
    const base = rgb(background);
    const text = rgb(foreground);
    const parts = (selectionFill(background, foreground).match(/[\d.]+/g) ?? []).map(Number);
    const alpha = parts[3] ?? 0;
    const painted = base.map((c, i) => c * (1 - alpha) + (parts[i] ?? 0) * alpha);
    assert.ok(
      painted.reduce((n, c, i) => n + Math.abs(c - (base[i] ?? 0)), 0) / 3 >= 20,
      `${background} did not show the range`,
    );
    const original = contrast(luminance(base), luminance(text));
    const selected = contrast(luminance(painted), luminance(text));
    assert.ok(selected >= Math.min(4.5, original * 0.9) - 0.01, `${background} lost text contrast`);
  }
});

test('XLSX, BIFF XLS, HTML XLS, TXT and CSV use identical text overflow rules', () => {
  const h = setup();
  try {
    let expected: string | undefined;
    for (const [format, type] of [
      ['xlsx', 's'],
      ['xlsx', 'inlineStr'],
      ['xls', 'inlineStr'],
      ['xls-html', 'inlineStr'],
      ['txt', 'inlineStr'],
      ['csv', 'inlineStr'],
      ['txt', undefined],
      ['txt', 'text'],
    ] as const) {
      h.state.source = { format };
      const cell = makeCell({ col: 1, raw: longText, display: longText, type });
      const node = h.makeNode();
      h.presenter.paint(node, cell, {});
      h.presenter.overflow(node, cell, { 1: cell }, h.layouts, 0);
      assert.ok(node.classList.contains('cell-overflow'), `${format}/${String(type)}`);
      const width = node.style.getPropertyValue('--overflow-width');
      expected ??= width;
      assert.equal(width, expected);
      assert.equal(node.style.width, '100px');
      assert.equal(cell.raw, longText);
      assert.equal(node._xlOverflowSpan?.textContent, longText);
    }
  } finally {
    h.restore();
  }
});

test('the first actual value blocks spilling, while a style-only empty cell does not', () => {
  const h = setup();
  try {
    const cell = makeCell({ type: 'inlineStr', raw: longText, display: longText });
    for (const occupied of [
      makeCell({ raw: '0', type: 'n' }),
      makeCell({ raw: ' ' }),
      makeCell({ display: '', raw: '', hasFormula: true }),
      makeCell({ display: 'x' }),
    ]) {
      assert.equal(h.presenter.spillWidth(cell, { 2: occupied }, h.layouts, 0), 0);
      assert.equal(h.presenter.spillWidth(cell, { 3: occupied }, h.layouts, 0), 194);
    }
    assert.ok(
      h.presenter.spillWidth(cell, { 2: makeCell({ raw: '', display: '', style: 5 }) }, h.layouts, 0) >
        200,
    );
  } finally {
    h.restore();
  }
});

test('numbers, errors and explicit wrapping/alignment follow the same rules in every format', () => {
  const h = setup();
  try {
    for (const format of ['xlsx', 'xls', 'xls-html', 'txt']) {
      h.state.source = { format };
      for (const type of ['n', 'e', 'b']) {
        assert.equal(
          h.presenter.spillWidth(
            makeCell({ raw: longText, display: longText, type }),
            {},
            h.layouts,
            0,
          ),
          0,
        );
      }
      const styles: readonly VisualStyle[] = [
        { wrap: true },
        { horizontal: 'right' },
        { horizontal: 'center' },
        { horizontal: 'fill' },
      ];
      for (const style of styles) {
        h.state.styles[0] = style;
        assert.equal(
          h.presenter.spillWidth(makeCell({ raw: longText, type: 'inlineStr' }), {}, h.layouts, 0),
          0,
        );
      }
      h.state.showStyles = false;
      assert.ok(
        h.presenter.spillWidth(makeCell({ raw: longText, type: 'inlineStr' }), {}, h.layouts, 0) > 0,
      );
      h.state.showStyles = true;
    }
  } finally {
    h.restore();
  }
});

test('very long TXT values spill without measuring or copying the entire string', () => {
  const h = setup();
  try {
    const cell = makeCell({ raw: '长'.repeat(100_000), type: 'inlineStr' });
    const node = h.makeNode();
    h.presenter.paint(node, cell, {});
    h.presenter.overflow(node, cell, {}, h.layouts, 0);
    assert.equal(node._xlOverflowSpan?.textContent.length, 100_000);
    assert.equal(node.style.getPropertyValue('--overflow-width'), '394px');
    assert.ok(h.measured.every((n) => n <= 2048));
  } finally {
    h.restore();
  }
});

test('hidden columns are skipped and frozen-column boundaries remain clipped', () => {
  const h = setup();
  try {
    const cell = makeCell({ raw: longText, type: 'inlineStr' });
    const l0 = h.layouts[0];
    const l2 = h.layouts[2];
    const l3 = h.layouts[3];
    assert.ok(l0 && l2 && l3);
    const columns = [l0, l2, l3];
    assert.ok(h.presenter.spillWidth(cell, { 2: makeCell({ raw: 'hidden' }) }, columns, 0) > 100);
    assert.equal(h.presenter.spillWidth(cell, {}, [l0], 0), 0);
    h.setMerge(() => ({ row: 2, col: 1, endRow: 2, endCol: 3 }));
    const node = h.makeNode();
    h.presenter.paint(node, cell, {});
    h.presenter.overflow(node, cell, {}, h.layouts, 0);
    assert.equal(node._xlOverflowSpan, undefined);
  } finally {
    h.restore();
  }
});

test('full and compact transports preserve the same display value and blank semantics', () => {
  const h = setup();
  try {
    const full = makeCell({ col: 1, raw: longText, display: longText, type: 'inlineStr' });
    const compact = unpackWireRows([{ n: 2, c: [{ c: 1, v: longText, t: 'inlineStr' }] }])[0]
      ?.cells[0];
    assert.ok(compact);
    assert.equal(
      h.presenter.spillWidth(full, {}, h.layouts, 0),
      h.presenter.spillWidth(compact, {}, h.layouts, 0),
    );
    assert.equal(cellDisplayValue({ raw: 'raw only' }), 'raw only');
    assert.equal(cellDisplayValue({ raw: 'value', display: '' }), '');
  } finally {
    h.restore();
  }
});

test('reused cells remove obsolete overflow without removing filter controls', () => {
  const h = setup();
  try {
    const cell = makeCell({ raw: longText, type: 'inlineStr' });
    const node = h.makeNode();
    h.state.autoFilter = {
      row: 2,
      startCol: 1,
      endCol: 4,
      rules: new Map(),
      sortColumn: 0,
      descending: false,
    };
    h.presenter.paint(node, cell, {});
    h.presenter.overflow(node, cell, {}, h.layouts, 0);
    const button = [...node.childNodes].find(
      (n) => n instanceof Element && n.tagName === 'BUTTON',
    );
    assert.ok(button);
    h.presenter.overflow(node, cell, { 2: makeCell({ raw: 'block' }) }, h.layouts, 0);
    assert.equal(node._xlOverflowSpan, null);
    assert.ok([...node.childNodes].includes(button));
    assert.equal(node.childNodes[0]?.textContent, longText);
  } finally {
    h.restore();
  }
});

test('row style inheritance and merged destinations do not diverge by source format', () => {
  const h = setup();
  try {
    const cell = makeCell({ type: 'inlineStr', raw: longText });
    const node = h.makeNode();
    h.state.styles[1] = { wrap: true };
    h.presenter.paint(node, cell, { style: 1 });
    h.presenter.overflow(node, cell, {}, h.layouts, 0);
    assert.equal(h.appliedStyles.get(node), 1);
    assert.equal(node._xlOverflowSpan, undefined);

    const zero = { ...cell, style: 0 };
    h.presenter.paint(node, zero, { style: 1 });
    h.presenter.overflow(node, zero, {}, h.layouts, 0);
    assert.ok(node._xlOverflowSpan);

    h.setMerge((row, col) => (col === 2 ? { row, col, endRow: row, endCol: 3 } : null));
    h.presenter.overflow(node, cell, {}, h.layouts, 0);
    assert.equal(node._xlOverflowSpan, null);
  } finally {
    h.restore();
  }
});

test('rich text keeps Unicode, explicit false and inherited decorations through overflow and reuse', () => {
  const h = setup();
  try {
    const text = `中😀${longText}`;
    const cell = makeCell({
      type: 'inlineStr',
      raw: text,
      display: text,
      richText: {
        runs: [
          { end: 3, font: { bold: false, underline: 'none', strike: false } },
          { end: text.length, font: { bold: true, color: '#1255AA', vertical: 'superscript' } },
        ],
      },
    });
    h.state.styles[0] = { bold: true, underline: true, strike: true, fontSize: 11 };
    const node = h.makeNode();
    h.presenter.prepare([{ number: 2, cells: [cell] }]);
    h.presenter.paint(node, cell, {});
    const root = node._xlRichNode;
    assert.ok(root);
    const first = root.childNodes[0] as HTMLElement;
    const second = root.childNodes[1] as HTMLElement;
    assert.equal(first.textContent, '中😀');
    assert.equal(first.style.getPropertyValue('font-weight'), '400');
    assert.equal(first.style.getPropertyValue('text-decoration-line'), 'none');
    assert.equal(node.style.textDecoration, 'none');
    assert.equal(second.style.getPropertyValue('--rich-color'), '#1255AA');
    assert.equal(second.style.getPropertyValue('vertical-align'), 'super');
    h.presenter.overflow(node, cell, {}, h.layouts, 0);
    assert.equal(node.textContent, text);
    assert.equal(node._xlOverflowSpan?.childNodes[0], root);
    h.presenter.overflow(node, cell, {}, h.layouts, 0);
    assert.equal(node.textContent, text);
    assert.equal(root.childNodes.length, 2);
    h.presenter.overflow(node, cell, { 2: makeCell({ raw: 'block' }) }, h.layouts, 0);
    assert.equal(node._xlOverflowSpan, null);
    assert.equal(node.childNodes[0], root);
    assert.equal(node.textContent, text);
    h.presenter.paint(node, makeCell({ raw: 'plain', display: 'plain' }), {});
    assert.equal(node._xlRichNode, null);
    assert.equal(node.textContent, 'plain');
  } finally {
    h.restore();
  }
});

test('format, formula, font and scale changes invalidate previously painted rich text', () => {
  const h = setup();
  try {
    const cell = makeCell({
      raw: '中😀',
      display: '中😀',
      type: 'inlineStr',
      hasFormula: true,
      formula: '="中😀"',
      richText: { runs: [{ end: 3, font: { name: '宋体', size: 12 } }] },
    });
    const node = h.makeNode();
    h.presenter.paint(node, cell, {});
    assert.ok(node._xlRichNode);
    assert.equal(h.presenter.needsPaint(node, cell), false);
    h.state.fontOverride = 'Arial';
    assert.equal(h.presenter.needsPaint(node, cell), true);
    h.presenter.paint(node, cell, {});
    assert.equal(
      (node._xlRichNode.childNodes[0] as HTMLElement | undefined)?.style.getPropertyValue(
        'font-family',
      ),
      'Arial',
    );
    h.setScale(1.5);
    assert.equal(h.presenter.needsPaint(node, cell), true);
    h.presenter.paint(node, cell, {});
    assert.equal(
      (node._xlRichNode.childNodes[0] as HTMLElement | undefined)?.style.getPropertyValue(
        'font-size',
      ),
      '24px',
    );
    h.state.showFormulas = true;
    h.presenter.paint(node, cell, {});
    assert.equal(node._xlRichNode, null);
    assert.equal(node.textContent, cell.formula);
    h.state.showStyles = false;
    h.state.showFormulas = false;
    h.presenter.paint(node, cell, {});
    assert.equal(node._xlRichNode, null);
    assert.equal(node.textContent, cell.raw);
  } finally {
    h.restore();
  }
});

test('viewport span budget falls back truthfully without truncating text and can recover on another tile', () => {
  const h = setup();
  try {
    const makePair = (): CellData =>
      makeCell({
        raw: '甲乙',
        type: 'inlineStr',
        richText: {
          runs: [
            { end: 1, font: { bold: true } },
            { end: 2, font: { italic: true } },
          ],
        },
      });
    const a = makePair();
    const b = makePair();
    h.richLimits.viewRuns = 3;
    h.presenter.prepare([{ number: 2, cells: [a, b] }]);
    const node = h.makeNode();
    h.presenter.paint(node, b, {});
    assert.equal(node._xlRichNode, null);
    assert.equal(node.textContent, b.raw);
    assert.match(node.title, /视口容量/);
    assert.equal(h.presenter.allowed(a), true);
    h.presenter.prepare([{ number: 2, cells: [b] }]);
    assert.equal(h.presenter.needsPaint(node, b), true);
    h.presenter.paint(node, b, {});
    assert.equal(node.textContent, b.raw);
    assert.ok(node._xlRichNode);
    assert.equal(node.title, '');
  } finally {
    h.restore();
  }
});

test('actual style painter clears recycled conditional fills, font flags and number-format color', () => {
  const dom = installDom({ fromIndexHtml: true });
  try {
    const deps = makeDefaultViewportPipelineDeps();
    deps.S.showStyles = true;
    deps.S.indexReady = true;
    deps.S.styles = [{}];
    const pipeline = new ViewportPipeline(deps);
    const node = document.createElement('div') as GridCellElement;
    node.className = 'grid-cell';
    node.dataset.row = '2';
    node.dataset.col = '1';
    node.dataset.ref = 'A2';
    node.style.width = '100px';
    const on = makeCell({
      raw: '1',
      display: '1',
      style: 0,
      valueColor: '#008800',
      conditional: {
        style: { font: {} },
        visual: { bold: true, italic: true, background: '#FFFF00', color: '#FF0033' },
        valueColor: '',
      },
    });
    pipeline.cellPresenter.paint(node, on, {});
    assert.equal(node.style.fontWeight, '700');
    assert.equal(node.style.getPropertyValue('--xl-color'), '#FF0033');
    assert.equal(node.style.getPropertyValue('--xl-fill'), '#FFFF00');
    pipeline.cellPresenter.paint(node, makeCell({ raw: '2', display: '2', style: 0 }), {});
    assert.equal(node.style.fontWeight, '');
    assert.equal(node.style.fontStyle, '');
    assert.equal(node.style.getPropertyValue('--xl-fill'), '');
    assert.equal(node.style.getPropertyValue('--xl-color'), '');
    assert.equal(node.style.width, '100px');
  } finally {
    dom.restore();
  }
});

test('the actual main and frozen cell factories share text, styles, filters and spill painting', async () => {
  const dom = installDom({ fromIndexHtml: true });
  try {
    const origCreate = dom.document.createMockElement.bind(dom.document);
    dom.document.createMockElement = (tag: string) => {
      const el = origCreate(tag);
      if (tag.toLowerCase() === 'canvas') {
        Reflect.set(el, 'getContext', () => ({
          font: '',
          measureText: (s: string) => ({ width: s.length * 10 }),
        }));
      }
      return el;
    };
    const deps = makeDefaultViewportPipelineDeps();
    deps.S.sheet = 'Sheet1';
    deps.S.sheetEpoch = 1;
    deps.S.indexReady = true;
    deps.S.showStyles = true;
    deps.S.styles = [{ background: '#EEF4FF', bold: true }];
    deps.S.freeze = { rows: 2, cols: 0 };
    deps.S.maxRow = 10;
    deps.S.maxCol = 4;
    const rawGrid = dom.document.getElementById('grid');
    assert.ok(rawGrid);
    rawGrid.clientWidth = 1000;
    rawGrid.clientHeight = 600;
    const grid = document.getElementById('grid');
    assert.ok(grid);

    const cell = makeCell({ col: 1, ref: 'A2', raw: longText, display: longText, type: 'inlineStr', style: 0 });
    const row: RowData = { number: 2, cells: [cell] };
    const byColumn: Record<number, CellData> = { 1: cell };
    const layouts = [1, 2, 3, 4].map((col) => ({
      col,
      width: 100,
      left: (col - 1) * 100,
      letter: String.fromCharCode(64 + col),
    }));
    const pipeline = new ViewportPipeline(deps);
    const firstLayout = layouts[0];
    assert.ok(firstLayout);
    const main = pipeline.makeGridCell(cell, row, 2, 2, firstLayout);
    pipeline.cellPresenter.overflow(main, cell, byColumn, layouts, 0);

    const request: ViewportRequest = {
      context: 'ctx',
      filtered: false,
      filterId: '',
      viewId: '',
      excludeThroughRow: 0,
      displayStart: 1,
      displayEnd: 10,
      visibleStart: 1,
      visibleEnd: 10,
      visibleColStart: 1,
      visibleColEnd: 4,
      row: 1,
      offset: 0,
      limit: 10,
      startCol: 1,
      cols: 4,
      selectedCols: null,
      leadRow: 0,
      leadCol: 0,
    };
    const frozen = new FrozenPanes({
      state: deps.S,
      grid,
      rowHead: 26,
      api: async <T = unknown>(): Promise<T> => {
        const res: SheetViewportResponse = {
          sheet: 'Sheet1',
          offset: 0,
          limit: 2,
          totalRows: 10,
          filtered: false,
          maxRow: 10,
          maxCol: 4,
          startCol: 1,
          colCount: 4,
          rows: [{ number: 1, cells: [] }, row],
        };
        return res as T;
      },
      getRequest: () => request,
      rowHeight: () => 25,
      colWidth: () => 100,
      rowTop: (r) => (r - 1) * 25,
      colLeft: (c) => (c - 1) * 100,
      headerHeight: () => 27,
      colName: (c) => String.fromCharCode(64 + c),
      bindResizeGrip: () => undefined,
      getVirtualScrollTop: () => 0,
      updateSharedBorders: () => undefined,
      isPrecedent: () => false,
      isSearchMatch: () => false,
      applyCellStyle: (node, c) => pipeline.appearance.applyCellStyle(node, c),
      paintCell: (node, c, r) => pipeline.cellPresenter.paint(node, c, r),
      extendTextIntoBlanks: (node, c, map, cols, index) =>
        pipeline.cellPresenter.overflow(node, c, map, cols, index),
      onRender: () => undefined,
      onError: () => undefined,
    });
    await frozen.refresh(true);
    const pinned = frozen.cells.get('A2');
    assert.ok(pinned);
    assert.equal(main._xlOverflowSpan?.textContent, pinned._xlOverflowSpan?.textContent);
    assert.equal(
      main.style.getPropertyValue('--overflow-width'),
      pinned.style.getPropertyValue('--overflow-width'),
    );
    assert.equal(main.style.fontWeight, pinned.style.fontWeight);
    assert.equal(
      main.style.getPropertyValue('--xl-fill'),
      pinned.style.getPropertyValue('--xl-fill'),
    );
  } finally {
    dom.restore();
  }
});
