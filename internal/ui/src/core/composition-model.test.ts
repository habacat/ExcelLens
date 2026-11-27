import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { CompositionField } from '../contracts/analysis.ts';
import {
  buildCompositionDefinition,
  compositionTypeWarning,
  hasTypeConflict,
  initialCompositionRows,
  labelColumn,
  searchFields,
  suggestFieldMatches,
  validateComposition,
  type CompositionInputSlot,
  type CompositionMappingRow,
} from './composition-model.ts';

function field(column: number, name: string, hash: string): CompositionField {
  return { column, name, headerHash: hash, headerLoaded: true, headerUnique: true };
}

test('only complete unique original headers suggest alignment; display names and positions cannot substitute', () => {
  const expected: CompositionField[] = [
    field(1, '甲', 'a'),
    field(2, '乙', 'b'),
    { ...field(3, '重复', 'c'), headerUnique: false },
    { ...field(4, '截取', 'd'), truncated: true },
  ];
  const actual: CompositionField[] = [
    field(7, '乙', 'b'),
    field(8, '甲', 'a'),
    field(9, '重复', 'c'),
    field(10, '截取', 'd'),
  ];
  assert.deepEqual(suggestFieldMatches(expected, actual), [8, 7, null, null]);
  actual.push(field(11, '另一个甲', 'a'));
  assert.equal(suggestFieldMatches(expected, actual)[0], null);
  assert.equal(
    suggestFieldMatches([field(1, '同名', 'original')], [field(1, '同名', 'other')])[0],
    null,
  );
});

test('explicit missing columns differ from unconfirmed mappings and duplicate source bindings are rejected', () => {
  const inputs: CompositionInputSlot[] = [
    {
      info: {
        name: '甲',
        sheet: '甲',
        kind: 'sheet',
        rows: 10,
        ref: { book: 'a', source: 'wb:甲', stamp: 'a', headerRow: 1, fingerprint: '' },
        fields: [field(1, '编号', 'id'), field(2, '金额', 'money')],
      },
    },
    {
      info: {
        name: '乙',
        sheet: '乙',
        kind: 'sheet',
        rows: 10,
        ref: { book: 'b', source: 'wb:乙', stamp: 'b', headerRow: 1, fingerprint: '' },
        fields: [field(7, '金额', 'money')],
      },
    },
  ];
  const rows: CompositionMappingRow[] = [
    { name: '编号', columns: [1, null] },
    { name: '金额', columns: [2, 7] },
  ];
  const limits = { sources: 8, columns: 256 };
  assert.match(validateComposition('联合', rows, inputs, limits), /确认/);
  rows[0] = { name: '编号', columns: [1, 0] };
  assert.equal(validateComposition('联合', rows, inputs, limits), '');

  const definition = buildCompositionDefinition(' 联合 ', rows, inputs);
  assert.equal(definition.name, '联合');
  assert.deepEqual(definition.inputs[1]?.columns, [0, 7]);
  const copy = { ...definition.inputs[0]?.ref, book: 'changed' };
  assert.equal(copy.book, 'changed');
  assert.equal(inputs[0]?.info?.ref.book, 'a');

  rows[0] = { name: '编号', columns: [1, 7] };
  assert.match(validateComposition('联合', rows, inputs, limits), /重复/);
  rows[0] = { name: '编号', columns: [1, 0] };
  rows[1] = { name: '编号', columns: [2, 7] };
  assert.match(validateComposition('联合', rows, inputs, limits), /重复/);
});

test('wide source fields are searched and paged without inventing or renumbering source columns', () => {
  const fields = Array.from({ length: 16384 }, (_, i) => field(i + 1, `字段${i + 1}`, `h${i}`));
  const rows = initialCompositionRows(fields, 256);
  assert.equal(rows.length, 256);
  assert.equal(labelColumn(16384), 'XFD');
  const result = searchFields(fields, 'XFD', 0, 40);
  assert.equal(result.count, 1);
  assert.equal(result.items[0]?.column, 16384);
  assert.equal(searchFields(fields, '', 1, 40).items[0]?.column, 41);
});

test('sample type conflicts distinguish numeric-eligible amounts from native text without asserting full-column types', () => {
  const row: CompositionMappingRow = { name: 'c', columns: [1, 2] };
  const f1: {
    column: number;
    name: string;
    headerLoaded: boolean;
    headerUnique: boolean;
    sampleType: 'text' | 'number' | 'unknown' | 'mixed';
  } = {
    column: 1,
    name: 'A',
    headerLoaded: true,
    headerUnique: true,
    sampleType: 'text',
  };
  const inputs: CompositionInputSlot[] = [
    {
      info: {
        name: '1',
        sheet: '1',
        kind: 'sheet',
        rows: 10,
        ref: { book: 'a', source: 'wb:1', stamp: 'a', headerRow: 1, fingerprint: '' },
        fields: [f1],
      },
    },
    {
      info: {
        name: '2',
        sheet: '2',
        kind: 'sheet',
        rows: 10,
        ref: { book: 'b', source: 'wb:2', stamp: 'b', headerRow: 1, fingerprint: '' },
        fields: [{ column: 2, name: 'B', headerLoaded: true, headerUnique: true, sampleType: 'number' }],
      },
    },
  ];
  assert.equal(hasTypeConflict(row, inputs), true);
  f1.sampleType = 'number';
  assert.equal(hasTypeConflict(row, inputs), false);
  f1.sampleType = 'unknown';
  assert.equal(hasTypeConflict(row, inputs), false);
  assert.equal(compositionTypeWarning(row, inputs), '类型未确认');
  f1.sampleType = 'mixed';
  assert.equal(hasTypeConflict(row, inputs), true);
});

test('wide exact-header matching stays bounded when every field is reversed', () => {
  const size = 4096;
  let reads = 0;
  const actual: CompositionField[] = Array.from({ length: size }, (_, i) => ({
    column: i + 1,
    name: `c${i}`,
    headerLoaded: true,
    headerUnique: true,
    truncated: false,
    get headerHash(): string {
      if (++reads > size * 12) {
        throw new Error('wide field matching repeated the full candidate scan');
      }
      return `hash-${i}`;
    },
  }));
  const expected: CompositionField[] = actual.map((_, i) => ({
    column: i + 1,
    name: `e${i}`,
    headerHash: `hash-${size - i - 1}`,
    headerLoaded: true,
    headerUnique: true,
    truncated: false,
  }));
  const result = suggestFieldMatches(expected, actual);
  assert.equal(result[0], size);
  assert.equal(result.at(-1), 1);
  assert.ok(reads <= size * 12);
});
