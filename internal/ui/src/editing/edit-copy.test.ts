import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { EditChange, EditSheetMeta } from '../contracts/edit.ts';
import type { EditLimits } from '../contracts/formats.ts';
import { parseClipboardTSV } from '../grid/clipboard.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import { EditModel, type EditGridInputKind, type EditModelLimits } from './edit-copy-model.ts';
import {
  GridRangeEditor,
  type GridRangeEditorHost,
  type GridRangeEditorOwner,
  type GridRangeEditorView,
} from './edit-grid-range.ts';

const limits: EditModelLimits = { cellBytes: 1024, batchCells: 100, requestBytes: 8192 };
const fullLimits: EditLimits = {
  extensions: ['.xlsx'],
  cellBytes: 1024,
  formulaBytes: 1024,
  batchCells: 100,
  requestBytes: 8192,
};
const sheet: EditSheetMeta = { name: '台账', firstRow: 1, firstCol: 1, rows: 8, cols: 4 };

test('direct grid typing preserves identifiers, source text, explicit types and CSV literals', () => {
  const parse = (
    raw: string,
    original = 'blank',
    kind: EditGridInputKind = 'auto',
    csv = false,
  ): [string, string] => {
    const result = EditModel.gridValue(raw, kind, original, csv, limits);
    return [result.kind, result.raw];
  };
  for (const text of ['0012', '1234567890123456', '2026-11-18']) {
    assert.deepEqual(parse(text), ['text', text]);
  }
  assert.deepEqual(parse('3.50', 'text'), ['text', '3.50']);
  assert.deepEqual(parse('3.50', 'number'), ['number', '3.50']);
  assert.deepEqual(parse('true'), ['boolean', 'TRUE']);
  assert.deepEqual(parse('=A1+1'), ['formula', '=A1+1']);
  assert.deepEqual(parse("'=A1+1"), ['text', '=A1+1']);
  assert.deepEqual(parse('=A1+1', 'text', 'auto', true), ['text', '=A1+1']);
  assert.deepEqual(parse('', 'number'), ['blank', '']);
});

test('edit input reversibly represents CR, CRLF, LF and backslashes', () => {
  const value = '\\r 原文\r\n\n 字段\r结束';
  assert.equal(EditModel.decode(EditModel.encode(value, true), true), value);
  assert.throws(() => EditModel.decode('末尾\\', true), /反斜线/);
  assert.equal(
    EditModel.value('text', '001234567890123456789', limits).raw,
    '001234567890123456789',
  );
});

test('paste preview uses source coordinates and treats formulas as literal text', () => {
  const anchor = EditModel.target('b2', sheet);
  const raw = '000001\t"多行\r\n原文"\r\n=SUM(B2)\t';
  const plan = EditModel.paste(raw, anchor, sheet, true, limits, parseClipboardTSV);
  assert.equal(plan.changes.length, 3);
  assert.equal(plan.changes[1]?.value.raw, '多行\r\n原文');
  assert.equal(plan.changes[2]?.row, 3);
  assert.equal(plan.changes[2].value.kind, 'text');
  assert.equal(plan.changes[2].value.raw, '=SUM(B2)');
  assert.throws(
    () => EditModel.paste(raw, EditModel.target('D8', sheet), sheet, false, limits, parseClipboardTSV),
    /最多|超出/,
  );
});

test('equal-step fill keeps exact decimal text and one source axis', () => {
  const a = EditModel.target('B5', sheet);
  const b = EditModel.target('B2', sheet);
  const plan = EditModel.fill(
    a,
    b,
    { kind: 'text', raw: '9007199254740993.25' },
    '-0.01',
    true,
    true,
    limits,
  );
  assert.equal(plan.changes[0]?.row, 4);
  assert.equal(plan.changes[0].value.raw, '9007199254740993.24');
  assert.equal(plan.changes[2]?.value.raw, '9007199254740993.22');
  assert.throws(
    () => EditModel.fill(a, EditModel.target('C2', sheet), { kind: 'text', raw: '1' }, '', false, false, limits),
    /同行/,
  );
});

test('a repeated clipboard field stops expanding at the serialized request limit', () => {
  let visited = 0;
  function* repeated(): Generator<EditChange> {
    for (let row = 1; row <= 10000; row++) {
      visited++;
      yield {
        sheet: '预算',
        row,
        col: 1,
        value: { kind: 'text', raw: '引号"与换行\n'.repeat(28) },
      };
    }
  }
  assert.throws(
    () => EditModel.bounded(repeated(), { batchCells: 10000, requestBytes: 2048 }),
    /容量/,
  );
  assert.ok(visited > 0 && visited < 10);
});

test('clearing a sorted range edits only returned source rows and keeps covered merge members', async () => {
  const dom = installDom({ fromIndexHtml: true });
  try {
  let committed: readonly EditChange[] = [];
  const owner: GridRangeEditorOwner = {
    limits: fullLimits,
    pending: null,
    data: { document: { id: 'one', binding: 'bound', revision: '4' } },
    tools: { tableAt: () => null },
    colName: (c) => String.fromCharCode(64 + c),
    advanced: () => null,
    post: async (): Promise<unknown> => {
      const empty: unknown = {};
      return empty;
    },
    commit: async (_action, changes) => {
      committed = changes ?? [];
      return true;
    },
  };
  const view: GridRangeEditorView = {
    ready: () => true,
    filtered: () => true,
    mappedRows: () => true,
    clipboardIdentity: () => 'id',
    areas: () => [{ row1: 2, row2: 5, col1: 1, col2: 2, visual1: 1, visual2: 2 }],
    expand: (area) => area,
    mergeAt: (r, c) => (r === 5 && c <= 2 ? { row: 5, col: 1, endRow: 5, endCol: 2 } : null),
    nextRow: (r) => r + 1,
    previousRow: (r) => Math.max(1, r - 1),
    nextCol: (c) => c + 1,
    previousCol: (c) => Math.max(1, c - 1),
    moveInside: async () => undefined,
    visibleSelection: () => ({
      book: 'book',
      workbookKey: 'key',
      sheet: '台账',
      styled: false,
      styles: [],
      fontOverride: '',
      current: () => true,
      sourceCurrent: () => true,
      rows: 2,
      firstColumn: 1,
      lastColumn: 2,
      start: 'A2',
      end: 'B5',
      maxRow: 8,
      headerRow: 0,
      label: 'A2:B5',
      readTSV: async () => '',
      read: async () => ({
        startCol: 1,
        endCol: 2,
        rows: [
          { row: 5, cells: [] },
          { row: 2, cells: [] },
        ],
      }),
    }),
    pasteRows: async () => [],
    copy: async () => true,
    positionMenu: () => undefined,
    redraw: () => undefined,
  };
  const grid: GridRangeEditorHost = {
    active: true,
    blocked: false,
    stale: false,
    selected: {
      sheet: '台账',
      row: 5,
      col: 1,
      ref: 'A5',
      raw: '',
      display: '',
      hasFormula: false,
    },
    shape: sheet,
    queue: [],
    owner,
    view,
    focus: () => undefined,
    status: () => undefined,
    copyForCut: async () => undefined,
    act: async (next) => Boolean(await next?.(null)),
  };
  const range = new GridRangeEditor(grid);
  assert.equal(await range.clear(), true);
  assert.deepEqual(
    Array.from(committed, (c) => [c.row, c.col, c.value.kind]),
    [
      [5, 1, 'blank'],
      [2, 1, 'blank'],
      [2, 2, 'blank'],
    ],
  );
  } finally {
    dom.restore();
  }
});
