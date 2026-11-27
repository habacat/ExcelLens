import { test } from 'node:test';
import assert from 'node:assert/strict';
import { decodeSampledRows, decodeSheetPreview } from './viewport-response.ts';
import { unpackWireRows } from './wire.ts';

test('compact viewport reconstructs exact identifiers, display and metadata', () => {
  const rows = unpackWireRows([
    {
      n: 7,
      s: 2,
      c: [
        { c: 1, v: '00012345678901234567', t: 'inlineStr' },
        { c: 2, v: '1.2300001', d: '', s: 8, o: '#ff0000' },
        { c: 3, v: '', f: '=A1+1', k: 7 },
        {
          c: 4,
          v: 'A&B',
          d: '链接',
          r: 'D9',
          h: { kind: 'external', url: 'https://example.com/a?b=1&c=2' },
        },
        {
          c: 16384,
          v: '🙂跨行\n原文',
          t: 'inlineStr',
          k: 8,
          x: {
            runs: [
              { end: 2, font: { bold: false, underline: 'none' } },
              { end: 7, font: { color: '#1255AA' } },
            ],
            notice: '部分样式待核对',
          },
        },
      ],
    },
  ]);
  const row = rows[0];
  assert.ok(row);
  assert.equal(row.cells[0]?.raw, '00012345678901234567');
  assert.equal(row.cells[0].ref, 'A7');
  assert.equal(row.cells[1]?.display, '');
  assert.equal(row.cells[1].valueColor, '#ff0000');
  assert.equal(row.cells[2]?.hasFormula, true);
  assert.equal(row.cells[2].cacheMissing, true);
  assert.equal(row.cells[2].sharedFormula, true);
  assert.equal(row.cells[3]?.ref, 'D9');
  assert.equal(row.cells[3].hyperlink?.url, 'https://example.com/a?b=1&c=2');
  assert.equal(row.cells[4]?.ref, 'XFD7');
  assert.equal(row.cells[4].raw, '🙂跨行\n原文');
  assert.equal(row.cells[4].richText?.runs?.[0]?.font.bold, false);
  assert.equal(row.cells[4].richText.runs[0].font.underline, 'none');
  assert.equal(row.cells[4].richText.runs[1]?.font.color, '#1255AA');
  assert.equal(row.cells[4].richText.notice, '部分样式待核对');
  assert.equal(row.cells[4].style, 0);
  assert.equal(row.style, 2);
});

test('invalid nested cells and unsupported wire versions are rejected before publishing partial rows', () => {
  const first = { n: 1, c: [{ c: 1, v: '001' }] };
  for (const cell of [
    { c: 1, v: 123 },
    { c: 1, v: '', k: 1.5 },
    { c: 1, v: '', h: { kind: 'broken' } },
    { c: 1, v: '', x: { runs: [{ end: '3', font: {} }] } },
  ]) {
    assert.throws(() => unpackWireRows([first, { n: 2, c: [cell] }]));
  }
  assert.throws(() => decodeSampledRows({ rows: [first] }, 2), /版本/);
  assert.deepEqual(decodeSampledRows({ rows: null }), []);
  assert.deepEqual(decodeSheetPreview({ available: false, partial: true }), { available: false, partial: true });
  assert.throws(() => decodeSheetPreview({ available: true, partial: true }));
});
