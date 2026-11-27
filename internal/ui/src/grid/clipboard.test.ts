import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { HttpError } from '../contracts/common.ts';
import type { ClipboardLimits } from '../contracts/formats.ts';
import {
  buildClipboard,
  characterCount,
  clipboardBatches,
  copyByBudget,
  parseClipboardTSV,
  pasteChangesFromTSV,
  tsvCell,
  utf8Length,
  validateClipboardChanges,
  type ClipboardSelectionSnapshot,
} from './clipboard.ts';
import { MergedCells } from './merges.ts';

test('read-only copy chooses a bounded representation and exports only the current selection', async () => {
  const limits: ClipboardLimits = {
    rows: 250,
    columns: 50,
    cells: 5000,
    textBytes: 100_000,
    htmlBytes: 100_000,
    tsvRows: 2000,
    tsvColumns: 200,
    tsvCells: 50000,
  };
  interface MutableSnap {
    rows: number;
    firstColumn: number;
    lastColumn: number;
    current: () => boolean;
  }
  async function tryCopy(
    rows: number,
    callbacks: Partial<Record<'rich' | 'tsv' | 'export', (s: MutableSnap) => unknown>> = {},
    plain = false,
  ): Promise<string[]> {
    const calls: string[] = [];
    const snapshot: MutableSnap = { rows, firstColumn: 1, lastColumn: 2, current: () => true };
    const step = (name: 'rich' | 'tsv') => async () => {
      calls.push(name);
      return callbacks[name]?.(snapshot);
    };
    await copyByBudget(
      snapshot as ClipboardSelectionSnapshot,
      limits,
      plain,
      step('rich'),
      step('tsv'),
      () => {
        calls.push('export');
        callbacks.export?.(snapshot);
      },
    );
    return calls;
  }
  const over = (): never => {
    const err: HttpError = new Error('capacity');
    err.status = 413;
    throw err;
  };
  assert.deepEqual(await tryCopy(250), ['rich']);
  assert.deepEqual(await tryCopy(251), ['tsv']);
  assert.deepEqual(await tryCopy(2, {}, true), ['tsv']);
  assert.deepEqual(await tryCopy(2, { rich: over, tsv: over }), ['rich', 'tsv', 'export']);
  assert.deepEqual(await tryCopy(2001), ['export']);
  assert.deepEqual(
    await tryCopy(251, {
      tsv: (s) => {
        s.current = () => false;
        over();
      },
    }),
    ['tsv'],
  );
  await assert.rejects(
    () =>
      tryCopy(251, {
        tsv: () => {
          throw new Error('来源已变化');
        },
      }),
    /来源已变化/,
  );
});

test('HTML keeps the merged grid while TSV keeps a placeholder for every covered position', () => {
  const limits: ClipboardLimits = {
    rows: 12,
    columns: 8,
    cells: 40,
    textBytes: 20000,
    htmlBytes: 20000,
    tsvRows: 100,
    tsvColumns: 20,
    tsvCells: 1000,
  };
  const merges = new MergedCells();
  merges.load([
    { row: 7, col: 2, endRow: 9, endCol: 3 },
    { row: 7, col: 4, endRow: 7, endCol: 5 },
  ]);
  const makeRow = (n: number) => ({
    row: n,
    cells: [2, 3, 4, 5, 6].map((col) => ({
      ref: `R${n}C${col}`,
      col,
      raw: `${n}-${col}`,
      display: `${n}-${col}`,
      hasFormula: false,
    })),
  });
  const build = (numbers: readonly number[], visibility?: { count(a: number, b: number): number }) =>
    buildClipboard(
      { startCol: 2, endCol: 6, rows: numbers.map(makeRow) },
      [],
      false,
      '',
      null,
      limits,
      (r, c) => merges.copyAnchor(r, c, visibility),
    );
  const result = build([7, 8, 9]);
  assert.match(result.html, /<td colspan="2" rowspan="3"[^>]*>7-2<\/td>/);
  assert.match(result.html, /<td colspan="2"[^>]*>7-4<\/td>/);
  assert.equal((result.html.match(/<td\b/g) ?? []).length, 9);
  assert.equal(result.text, '7-2\t\t7-4\t\t7-6\n\t\t8-4\t8-5\t8-6\n\t\t9-4\t9-5\t9-6');
  assert.doesNotMatch(result.html, /8-2/);

  const hidden = build([7, 9], { count: () => 1 });
  assert.match(hidden.html, /colspan="2" rowspan="2"/);
  for (const rows of [
    [7, 10, 9],
    [7, 9, 8],
    [9, 7],
    [7, 9],
  ]) {
    const reordered = build(rows);
    assert.doesNotMatch(reordered.html, /rowspan/);
    assert.match(reordered.html, /colspan="2"/);
    assert.ok(reordered.html.includes(`${rows.at(-1)}-6`));
  }
});

test('Excel/WPS quoted TSV round trips literal tabs, CRLF, quotes, empty cells and text identifiers', () => {
  const rows = [
    ['000123', '1,234.50', 'line1\r\nline2', '"quoted"', '\t'],
    ['', 'tail', '', '', ''],
  ];
  const text = `${rows.map((row) => row.map(tsvCell).join('\t')).join('\r\n')}\r\n`;
  assert.deepEqual(parseClipboardTSV(text), rows);
  assert.deepEqual(parseClipboardTSV('"a\nb"\t"q""s"\rx\t\r'), [
    ['a\nb', 'q"s'],
    ['x', ''],
  ]);
  assert.deepEqual(parseClipboardTSV('\n\n'), [[''], ['']]);
});

test('paste planning preserves all values and rejects the entire payload instead of clipping rows or columns', () => {
  assert.deepEqual(
    pasteChangesFromTSV('"a\nb"\t00042\r\nx\ty\r\n', {
      startRow: 2,
      startCol: 1,
      rowLimit: 10,
      columns: 3,
    }),
    [
      { row: 2, col: 1, value: 'a\nb' },
      { row: 2, col: 2, value: '00042' },
      { row: 3, col: 1, value: 'x' },
      { row: 3, col: 2, value: 'y' },
    ],
  );
  assert.throws(
    () => pasteChangesFromTSV('a\tb\nc\td', { startRow: 0, startCol: 1, columns: 2 }),
    /边界/,
  );
  assert.throws(
    () => pasteChangesFromTSV('a\nb', { startRow: 9999, startCol: 0, columns: 1 }),
    /边界/,
  );
});

test('malformed or excessive clipboard data is refused before building any edits', () => {
  for (const text of ['"unterminated', '"closed"tail', 'a'.repeat(4001)]) {
    assert.throws(() => parseClipboardTSV(text));
  }
  assert.throws(() => parseClipboardTSV('a\tb', { maxCols: 1 }), /1列/);
  assert.throws(() => parseClipboardTSV('a\nb\nc', { maxRows: 2 }), /2行/);
  assert.throws(() => parseClipboardTSV('a\tb\nc\td', { maxCells: 3 }), /3个单元格/);
  assert.throws(() => parseClipboardTSV('a'.repeat(2 * 1024 * 1024 + 1)), /2 MB/);
});

test('4000 Unicode characters fit a cell regardless of UTF-8 bytes or UTF-16 pairs', () => {
  for (const symbol of ['文', '😀']) {
    const value = symbol.repeat(4000);
    assert.equal(parseClipboardTSV(value)[0]?.[0], value);
    assert.equal(
      pasteChangesFromTSV(value, { startRow: 0, startCol: 0, columns: 1 })[0]?.value,
      value,
    );
    assert.throws(() => parseClipboardTSV(value + symbol), /4000字符/);
    assert.equal(characterCount(value), 4000);
    assert.equal(utf8Length(value), Buffer.byteLength(value, 'utf8'));
  }
});

test('paste is atomic under UTF-8 and JSON request budgets; explicit retries may form bounded batches', () => {
  const unicode = Array<string>(180).fill('文'.repeat(4000)).join('\n');
  assert.ok(unicode.length < 2 * 1024 * 1024);
  assert.throws(() => parseClipboardTSV(unicode), /2 MB/);
  const escaped = Array<string>(90).fill('\u0001'.repeat(4000)).join('\n');
  assert.throws(
    () => pasteChangesFromTSV(escaped, { startRow: 0, startCol: 0, columns: 1 }),
    /请求超过 2 MB/,
  );
  const changes = Array.from({ length: 200 }, (_, row) => ({
    row,
    col: 0,
    value: '文'.repeat(4000),
  }));
  const batches = Array.from(clipboardBatches(changes));
  assert.equal(batches.length, 2);
  assert.equal(batches.flat().length, 200);
  for (const batch of batches) {
    validateClipboardChanges(batch);
    assert.ok(
      Buffer.byteLength(
        JSON.stringify({
          id: 'sheet',
          workbookKey: 'a'.repeat(64),
          expectedRevision: 1,
          changes: batch,
        }),
        'utf8',
      ) <=
        2 * 1024 * 1024,
    );
  }
});
