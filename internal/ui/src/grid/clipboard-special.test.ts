import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { CellData, ClipboardPayload } from '../contracts/workbook.ts';
import { configureClipboard, parseClipboardTSV } from './clipboard.ts';
import { buildPastePlan, buildSpecialSelection } from './clipboard-special.ts';

function configureDefault(): void {
  configureClipboard({
    rows: 6,
    columns: 4,
    cells: 24,
    textBytes: 100_000,
    htmlBytes: 100_000,
    tsvRows: 2000,
    tsvColumns: 200,
    tsvCells: 50000,
  });
}

function makeCell(col: number, raw: string, extra: Partial<CellData> = {}): CellData {
  return {
    ref: '',
    col,
    raw,
    display: raw,
    type: 'inlineStr',
    hasFormula: false,
    ...extra,
  };
}

function fixture(): ClipboardPayload {
  return {
    startCol: 2,
    endCol: 4,
    header: {
      row: 1,
      cells: [makeCell(2, '金额'), makeCell(3, '金额'), makeCell(4, '')],
    },
    rows: [
      {
        row: 9,
        cells: [
          makeCell(2, '9007199254740993.31', {
            display: '9,007,199,254,740,993.31',
            type: 'n',
          }),
          makeCell(3, '001'),
          makeCell(4, '<img>|q\tline\n尾'),
        ],
      },
      {
        row: 4,
        cells: [
          makeCell(2, '2', {
            type: 'n',
            hasFormula: true,
            formula: '=B3+1',
            display: '2.00',
          }),
          makeCell(3, '  '),
        ],
      },
    ],
  };
}

test('the optional header and reordered source rows retain their coordinates for Excel HTML spans', () => {
  configureDefault();
  const header = { row: 4, cells: [makeCell(2, '报告')] };
  const payload: ClipboardPayload = {
    startCol: 2,
    endCol: 3,
    header,
    rows: [{ row: 12, cells: [] }, header, { row: 11, cells: [] }],
  };
  const mergeAt = (row: number, col: number) =>
    row === 4 && col === 2 ? { endCol: 3, endRow: 4, visibleRows: 1 } : null;
  const result = buildSpecialSelection(payload, { format: 'tsv', includeHeader: true, mergeAt });
  assert.ok(result.html);
  assert.match(result.html, /<td colspan="2"[^>]*>报告<\/td>/);
  assert.equal((result.html.match(/<td\b/g) ?? []).length, 5);
  assert.equal(result.text, '报告\t\n\t\n\t');
});

test('source-order metadata, duplicate/empty headers and exact decimals survive structured JSON without numeric coercion', () => {
  configureDefault();
  const payload = fixture();
  const before = JSON.stringify(payload);
  const result = buildSpecialSelection(payload, {
    format: 'json',
    includeHeader: true,
    sheet: '原表',
  });
  const doc = JSON.parse(result.text) as {
    sheet: string;
    columns: { header: string }[];
    header: { cells: { ref: string }[] };
    rows: { row: number; cells: { raw: string; type: string; present: boolean; formula: string }[] }[];
  };
  assert.equal(doc.sheet, '原表');
  assert.deepEqual(
    doc.columns.map((c) => c.header),
    ['金额', '金额', ''],
  );
  assert.deepEqual(
    doc.rows.map((r) => r.row),
    [9, 4],
  );
  assert.equal(doc.rows[0]?.cells[0]?.raw, '9007199254740993.31');
  assert.equal(doc.rows[0].cells[0].type, 'n');
  assert.equal(doc.rows[0].cells[1]?.raw, '001');
  assert.equal(doc.rows[1]?.cells[2]?.present, false);
  assert.equal(doc.rows[1].cells[0]?.formula, '=B3+1');
  assert.equal(doc.header.cells[0]?.ref, 'B1');
  assert.equal(JSON.stringify(payload), before);

  const plan = buildPastePlan(result.text, { format: 'json', startRow: 2, startCol: 1, columns: 4 });
  assert.deepEqual(
    plan.changes.map((c) => c.value),
    ['9007199254740993.31', '001', '<img>|q\tline\n尾', '2', '  ', ''],
  );
});

test('explicit display/raw/formula modes share safe TSV while Markdown escapes source HTML and delimiters', () => {
  configureDefault();
  const payload = fixture();
  const raw = buildSpecialSelection(payload, { format: 'tsv', mode: 'raw', includeHeader: true });
  assert.deepEqual(parseClipboardTSV(raw.text)[0], ['金额', '金额', '']);
  assert.equal(parseClipboardTSV(raw.text)[1]?.[0], '9007199254740993.31');
  assert.equal(
    parseClipboardTSV(buildSpecialSelection(payload, { mode: 'display' }).text)[0]?.[0],
    '9,007,199,254,740,993.31',
  );
  assert.equal(
    parseClipboardTSV(buildSpecialSelection(payload, { mode: 'formula' }).text)[1]?.[0],
    '=B3+1',
  );
  const md = buildSpecialSelection(payload, { format: 'markdown' }).text;
  assert.ok(md.startsWith('| B | C | D |'));
  assert.ok(md.includes('&lt;img&gt;\\|q&#9;line<br>尾'));
  assert.ok(md.includes('&#32;&#32;'));
  assert.ok(!md.includes('<img>'));

  const withHeader: ClipboardPayload = {
    ...payload,
    rows: payload.header ? [payload.header, ...payload.rows] : payload.rows,
  };
  assert.equal(
    parseClipboardTSV(buildSpecialSelection(withHeader, { includeHeader: true }).text).length,
    3,
    'selected header must not be duplicated',
  );
});

test('missing formula cache and unavailable formulas are never silently copied as a valid value or expression', () => {
  configureDefault();
  const cell = makeCell(1, '', {
    ref: 'A3',
    hasFormula: true,
    formula: '=1+1',
    cacheMissing: true,
  });
  const payload: ClipboardPayload = { startCol: 1, endCol: 1, rows: [{ row: 3, cells: [cell] }] };
  for (const mode of ['raw', 'display'] as const) {
    assert.throws(() => buildSpecialSelection(payload, { mode }), /缓存/);
  }
  assert.equal(buildSpecialSelection(payload, { mode: 'formula' }).text, '=1+1');
  const json = buildSpecialSelection(payload, { format: 'json' }).text;
  assert.equal(
    (JSON.parse(json) as { rows: { cells: { cacheMissing: boolean }[] }[] }).rows[0]?.cells[0]
      ?.cacheMissing,
    true,
  );
  assert.throws(
    () => buildPastePlan(json, { format: 'json', startRow: 0, startCol: 0, columns: 1 }),
    /缓存/,
  );
  assert.equal(
    buildPastePlan(json, { format: 'json', mode: 'formula', startRow: 0, startCol: 0, columns: 1 })
      .changes[0]?.value,
    '=1+1',
  );

  const noFormulaPayload: ClipboardPayload = {
    startCol: 1,
    endCol: 1,
    rows: [{ row: 3, cells: [{ ...cell, formula: '' }] }],
  };
  assert.throws(() => buildSpecialSelection(noFormulaPayload, { mode: 'formula' }), /无法还原/);
  assert.equal(
    (
      JSON.parse(buildSpecialSelection(noFormulaPayload, { format: 'json' }).text) as {
        rows: { cells: { formula: string }[] }[];
      }
    ).rows[0]?.cells[0]?.formula,
    '',
  );
});

test('header and escaped output share source budgets; no oversized clipboard representation is published', () => {
  configureDefault();
  const payload = fixture();
  configureClipboard({
    rows: 2,
    columns: 4,
    cells: 8,
    textBytes: 100_000,
    htmlBytes: 100_000,
    tsvRows: 2000,
    tsvColumns: 200,
    tsvCells: 50000,
  });
  assert.throws(() => buildSpecialSelection(payload, { includeHeader: true }), /上限/);
  configureClipboard({
    rows: 6,
    columns: 4,
    cells: 24,
    textBytes: 50,
    htmlBytes: 100_000,
    tsvRows: 2000,
    tsvColumns: 200,
    tsvCells: 50000,
  });
  assert.throws(() => buildSpecialSelection(payload, { format: 'json' }), /容量/);
  assert.throws(() => buildSpecialSelection({ ...payload, startCol: 1.5 }), /上限/);
});

test('transpose, exact-empty skipping and header removal build one bounded atomic text edit without clipping', () => {
  configureDefault();
  const plan = buildPastePlan('H1\tH2\tH3\n001\t\t  \nx\ty\n', {
    skipFirstRow: true,
    transpose: true,
    skipBlank: true,
    startRow: 3,
    startCol: 1,
    columns: 4,
  });
  assert.equal(plan.rows, 3);
  assert.equal(plan.columns, 2);
  assert.equal(plan.skipped, 2);
  assert.deepEqual(plan.changes, [
    { row: 3, col: 1, value: '001' },
    { row: 3, col: 2, value: 'x' },
    { row: 4, col: 2, value: 'y' },
    { row: 5, col: 1, value: '  ' },
  ]);
  assert.throws(
    () =>
      buildPastePlan('a\tb\nc\td', {
        transpose: true,
        skipBlank: true,
        startRow: 9999,
        startCol: 0,
        columns: 2,
      }),
    /边界/,
  );
  assert.throws(
    () => buildPastePlan('\t\t', { skipBlank: true, startRow: 0, startCol: 0, columns: 2 }),
    /边界/,
  );
  assert.throws(() => buildPastePlan('"bad', { startRow: 0, startCol: 0, columns: 4 }), /引号/);
  assert.throws(
    () => buildPastePlan('head', { skipFirstRow: true, startRow: 0, startCol: 0, columns: 4 }),
    /没有/,
  );
  assert.equal(
    buildPastePlan('宽表头\t多一列\n001', {
      skipFirstRow: true,
      startRow: 0,
      startCol: 0,
      columns: 1,
    }).changes[0]?.value,
    '001',
  );
});

test('JSON numeric primitives are refused, not rounded; malformed types, overlong input and excess request bytes fail before edits', () => {
  configureDefault();
  const target = { format: 'json' as const, startRow: 0, startCol: 0, columns: 4 };
  assert.throws(() => buildPastePlan('[[9007199254740993.31]]', target), /字符串/);
  assert.throws(() => buildPastePlan(JSON.stringify(Array(2001).fill([])), target), /容量/);
  assert.deepEqual(
    buildPastePlan('[["9007199254740993.31","001",null]]', target).changes.map((c) => c.value),
    ['9007199254740993.31', '001', ''],
  );
  const doc = JSON.parse(buildSpecialSelection(fixture(), { format: 'json' }).text) as {
    rows: { cells: { raw: unknown }[] }[];
  };
  if (doc.rows[0]?.cells[0]) doc.rows[0].cells[0].raw = 99;
  assert.throws(() => buildPastePlan(JSON.stringify(doc), target), /类型/);
  assert.throws(
    () => buildPastePlan('😀'.repeat(4001), { startRow: 0, startCol: 0, columns: 1 }),
    /4000/,
  );
  const escaped = JSON.stringify(Array.from({ length: 90 }, () => ['\u0001'.repeat(4000)]));
  assert.throws(() => buildPastePlan(escaped, { ...target, columns: 1 }), /2 MiB/);
});
