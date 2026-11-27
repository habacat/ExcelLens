import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { CompositionField, CompositionSourceInfo, SourceScope } from '../contracts/analysis.ts';
import type { RelationCounts, RelationLimits, RelationQuery } from '../contracts/features.ts';
import {
  RelationsModel,
  type RelationOutputModeId,
} from './relations-model.ts';

interface MutableRecord {
  position: number;
  number: number;
  values: Record<string, Record<string, unknown>>;
}

interface MutableRow {
  sequence: string;
  left?: MutableRecord;
  right?: MutableRecord;
  status: string;
  rightMatches: string;
  reason?: string;
}

interface MutableData {
  id: string;
  mode: string;
  outputMode: string;
  parentId: string;
  workbookKey: string;
  book: string;
  left: Record<string, unknown>;
  right: Record<string, unknown>;
  query: {
    keys: Record<string, unknown>[];
    leftColumns: number[];
    rightColumns: number[];
  };
  counts: { -readonly [K in keyof RelationCounts]: RelationCounts[K] | number };
  preview: {
    total: string;
    truncated: boolean;
    rows: MutableRow[];
  };
}

interface MutableSourceInfo {
  ref: {
    book: string;
    source: string;
    headerRow: number;
    fingerprint: string;
    stamp: string;
    scope?: SourceScope;
  };
  name: string;
  sheet: string;
  kind: 'workbook' | 'analysis';
  format: string;
  rows: number;
  fields: {
    column: number;
    name: string;
    headerHash?: string;
    headerLoaded: boolean;
    headerUnique: boolean;
    truncated?: boolean;
  }[];
}

function makeFixture() {
  const limits: RelationLimits = {
    keys: 4,
    columns: 64,
    previewRows: 100,
    defaultRows: 1_000_000,
    maximumRows: 1_000_000_000,
    requestBytes: 16_384,
    resultBytes: 8_388_608,
    attachments: 0,
    viewRows: 20,
    viewColumns: 12,
    initialFields: 8,
    snippetUnits: 256,
    zipRows: 0,
  };
  const formats = { maxRows: 1_000_000_000, maxColumns: 16_384 };
  const fields: CompositionField[] = [
    {
      column: 1,
      name: '编号',
      headerHash: 'id',
      headerLoaded: true,
      headerUnique: true,
      truncated: false,
    },
    {
      column: 2,
      name: '金额',
      headerHash: 'amount',
      headerLoaded: true,
      headerUnique: true,
      truncated: false,
    },
  ];
  const makeInfo = (side: 'left' | 'right'): MutableSourceInfo => ({
    ref: {
      book: `${side}-book`,
      source: 'wb:数据',
      headerRow: 1,
      fingerprint: `${side}-fp`,
      stamp: `${side}-stamp`,
    },
    name: `${side}.csv`,
    sheet: '数据',
    kind: 'workbook',
    format: 'csv',
    rows: 2,
    fields: structuredClone(fields),
  });
  const infos: [MutableSourceInfo, MutableSourceInfo] = [makeInfo('left'), makeInfo('right')];
  const query: RelationQuery = {
    keys: [{ left: 1, right: 1, mode: '', trim: false, ignoreCase: false }],
    leftColumns: [1, 2],
    rightColumns: [1, 2],
  };
  const record = (position: number, number: number, id: string): MutableRecord => ({
    position,
    number,
    values: {
      1: { raw: id, type: 's', exists: true },
      2: {
        raw: '9007199254740993.31',
        display: '9,007,199,254,740,993.31',
        type: 'n',
        exists: true,
      },
    },
  });
  const sideEnvelope = (info: MutableSourceInfo) => ({
    id: `${info.ref.book}/${info.ref.source}`,
    name: info.name,
    sheet: info.sheet,
    kind: info.kind,
    format: info.format,
    fingerprint: info.ref.fingerprint,
    stamp: info.ref.stamp,
    headerRow: 1,
    fields: structuredClone(fields),
    total: 2,
  });
  const data: MutableData = {
    id: 'job',
    mode: 'prepare',
    outputMode: 'left',
    parentId: 'job',
    workbookKey: 'main-fp',
    book: 'main-book',
    left: sideEnvelope(infos[0]),
    right: sideEnvelope(infos[1]),
    query: { keys: [{ left: 1, right: 1 }], leftColumns: [1, 2], rightColumns: [1, 2] },
    counts: {
      leftRecords: '2',
      rightRecords: '2',
      leftInvalid: '0',
      rightInvalid: '0',
      leftDuplicateGroups: '0',
      rightDuplicateGroups: '0',
      manyToManyGroups: '0',
      matchingGroups: '1',
      leftMatched: '1',
      rightMatched: '1',
      leftOnly: '1',
      rightOnly: '1',
      innerRows: '1',
      leftJoinRows: '2',
    },
    preview: {
      total: '2',
      truncated: false,
      rows: [
        {
          sequence: '1',
          left: record(1, 2, '001'),
          right: record(1, 2, '001'),
          status: 'matched',
          rightMatches: '1',
        },
        {
          sequence: '2',
          left: record(2, 4, 'left-only'),
          status: 'unmatched',
          rightMatches: '0',
        },
      ],
    },
  };
  const expected: {
    id: string;
    identity: { bookKey: string; book: string };
    mode: RelationOutputModeId;
    operation: 'prepare' | 'preview';
    parentId?: string;
    query: RelationQuery;
    infos: [CompositionSourceInfo, CompositionSourceInfo];
  } = {
    id: 'job',
    identity: { bookKey: 'main-fp', book: 'main-book' },
    mode: 'left',
    operation: 'prepare',
    query,
    infos,
  };
  return { limits, formats, infos, query, data, expected, record };
}

test('Go omitted default key flags retain exact source values/types/coordinates and detached ownership', () => {
  const h = makeFixture();
  const accepted = RelationsModel.validate(h.data, h.expected, h.limits, h.formats);
  const firstLeft = accepted.preview.rows[0]?.left;
  assert.ok(firstLeft);
  assert.equal(firstLeft.values['1']?.raw, '001');
  assert.equal(firstLeft.values['2']?.raw, '9007199254740993.31');
  assert.equal(accepted.preview.rows[1]?.left?.number, 4);
  if (h.data.preview.rows[0]?.left?.values['2']) {
    h.data.preview.rows[0].left.values['2'].raw = 'mutated';
  }
  assert.equal(firstLeft.values['2'].raw, '9007199254740993.31');
});

test('potential quintillion counts are formatted exactly without Number conversion or product expansion', () => {
  const h = makeFixture();
  for (const info of h.infos) {
    info.rows = 1_000_000_000;
    info.ref.headerRow = 0;
  }
  for (const side of ['left', 'right'] as const) {
    h.data[side].total = 1_000_000_000;
    h.data[side].headerRow = 0;
  }
  Object.assign(h.data.counts, {
    leftRecords: '1000000000',
    rightRecords: '1000000000',
    leftMatched: '1000000000',
    rightMatched: '1000000000',
    leftOnly: '0',
    rightOnly: '0',
    leftDuplicateGroups: '1',
    rightDuplicateGroups: '1',
    manyToManyGroups: '1',
    innerRows: '1000000000000000000',
    leftJoinRows: '1000000000000000000',
  });
  h.data.preview.rows.length = 1;
  if (h.data.preview.rows[0]) h.data.preview.rows[0].rightMatches = '1000000000';
  h.data.preview.total = '1000000000000000000';
  h.data.preview.truncated = true;
  assert.equal(
    RelationsModel.validate(h.data, h.expected, h.limits, h.formats).counts.innerRows,
    '1000000000000000000',
  );
  assert.equal(
    RelationsModel.formatCount(h.data.preview.total),
    '1,000,000,000,000,000,000',
  );
});

const MODES: readonly RelationOutputModeId[] = [
  'inner',
  'left_matched',
  'left_unmatched',
  'right_unmatched',
  'left_invalid',
  'right_invalid',
];

for (const mode of MODES) {
  test(`${mode} draws only the corresponding source columns and validates its own row shape`, () => {
    const h = makeFixture();
    h.data.mode = 'preview';
    h.expected.operation = 'preview';
    h.data.outputMode = mode;
    h.expected.mode = mode;
    h.data.parentId = 'parent';
    h.expected.parentId = 'parent';
    const firstRow = h.data.preview.rows[0];
    const secondRow = h.data.preview.rows[1];
    assert.ok(firstRow && secondRow);
    if (mode === 'inner') h.data.preview.rows = [firstRow];
    if (mode === 'left_matched') {
      h.data.preview.rows = [firstRow];
      delete firstRow.right;
    }
    if (mode === 'left_unmatched') h.data.preview.rows = [secondRow];
    if (mode === 'right_unmatched') {
      h.data.preview.rows = [
        {
          sequence: '1',
          right: h.record(2, 3, 'right-only'),
          status: 'unmatched',
          rightMatches: '0',
        },
      ];
    }
    if (mode.endsWith('_invalid')) h.data.preview.rows = [];
    h.data.preview.rows.forEach((row, i) => {
      row.sequence = String(i + 1);
    });
    h.data.preview.total = RelationsModel.total(
      {
        leftRecords: String(h.data.counts.leftRecords),
        rightRecords: String(h.data.counts.rightRecords),
        leftInvalid: String(h.data.counts.leftInvalid),
        rightInvalid: String(h.data.counts.rightInvalid),
        leftDuplicateGroups: String(h.data.counts.leftDuplicateGroups),
        rightDuplicateGroups: String(h.data.counts.rightDuplicateGroups),
        manyToManyGroups: String(h.data.counts.manyToManyGroups),
        matchingGroups: String(h.data.counts.matchingGroups),
        leftMatched: String(h.data.counts.leftMatched),
        rightMatched: String(h.data.counts.rightMatched),
        leftOnly: String(h.data.counts.leftOnly),
        rightOnly: String(h.data.counts.rightOnly),
        innerRows: String(h.data.counts.innerRows),
        leftJoinRows: String(h.data.counts.leftJoinRows),
      },
      mode,
    );
    const result = RelationsModel.validate(
      h.data,
      h.expected,
      h.limits,
      h.formats,
    );
    const sides = [...new Set(RelationsModel.columns(result).map((field) => field.side))];
    assert.deepEqual(sides, [...(RelationsModel.mode(mode)?.sides ?? [])]);
  });
}

const REJECTIONS: readonly (readonly [string, (h: ReturnType<typeof makeFixture>) => void])[] = [
  [
    'number-rounded counter',
    (h) => {
      h.data.counts.innerRows = 1;
    },
  ],
  [
    'noncanonical count',
    (h) => {
      h.data.counts.leftOnly = '01';
    },
  ],
  [
    'contradictory totals',
    (h) => {
      h.data.counts.leftMatched = '2';
    },
  ],
  [
    'wrong path identity',
    (h) => {
      h.data.left.id = 'foreign/wb:数据';
    },
  ],
  [
    'changed stamp',
    (h) => {
      h.data.right.stamp = 'other';
    },
  ],
  [
    'wrong format',
    (h) => {
      h.data.right.format = 'xls';
    },
  ],
  [
    'changed header',
    (h) => {
      h.data.left.headerRow = 2;
    },
  ],
  [
    'wrong query mode',
    (h) => {
      if (h.data.query.keys[0]) h.data.query.keys[0].mode = 'number';
    },
  ],
  [
    'wrong job parent',
    (h) => {
      h.data.parentId = 'other';
    },
  ],
  [
    'source precision coerced to number',
    (h) => {
      if (h.data.preview.rows[0]?.left?.values['2']) {
        h.data.preview.rows[0].left.values['2'].raw = Number('9007199254740993.31');
      }
    },
  ],
  [
    'false unmatched count',
    (h) => {
      if (h.data.preview.rows[1]) h.data.preview.rows[1].rightMatches = '1';
    },
  ],
  [
    'false matched without right side',
    (h) => {
      if (h.data.preview.rows[0]) delete h.data.preview.rows[0].right;
    },
  ],
  [
    'reordered positions',
    (h) => {
      if (h.data.preview.rows[1]?.left) h.data.preview.rows[1].left.position = 0;
    },
  ],
  [
    'missing source existence',
    (h) => {
      if (h.data.preview.rows[0]?.left?.values['1']) {
        delete h.data.preview.rows[0].left.values['1'].exists;
      }
    },
  ],
  [
    'foreign column data',
    (h) => {
      if (h.data.preview.rows[0]?.left) {
        h.data.preview.rows[0].left.values['99'] = { raw: 'x', exists: true };
      }
    },
  ],
  [
    'partial preview disguised as complete',
    (h) => {
      h.data.preview.truncated = true;
    },
  ],
];

for (const [name, change] of REJECTIONS) {
  test(`rejects ${name} before replacing an accepted result`, () => {
    const h = makeFixture();
    change(h);
    assert.throws(() => RelationsModel.validate(h.data, h.expected, h.limits, h.formats));
  });
}

test('field capture binds path/content/header/full-source signature and tolerates optional empty hashes', () => {
  const h = makeFixture();
  const info = structuredClone(h.infos[0]);
  if (info.fields[0]) delete info.fields[0].headerHash;
  const data = {
    id: 'fields',
    mode: 'fields',
    workbookKey: h.expected.identity.bookKey,
    book: h.expected.identity.book,
    info,
  };
  assert.equal(
    RelationsModel.validateFields(
      data,
      { book: info.ref.book, source: info.ref.source, headerRow: 1 },
      h.expected.identity,
      h.formats,
    ).fields?.length,
    2,
  );
  info.ref.scope = { version: 1, mode: 'visible', fingerprint: 'left-fp' };
  assert.throws(() =>
    RelationsModel.validateFields(
      data,
      { book: info.ref.book, source: info.ref.source, headerRow: 1 },
      h.expected.identity,
      h.formats,
    ),
  );
});

test('result byte budget and malformed key objects fail closed', () => {
  const h = makeFixture();
  if (h.data.preview.rows[0]?.left?.values['2']) {
    h.data.preview.rows[0].left.values['2'].raw = 'x'.repeat(2000);
  }
  assert.throws(() =>
    RelationsModel.validate(h.data, h.expected, { ...h.limits, resultBytes: 1024 }, h.formats),
  );
  const badQuery = { ...h.query, keys: [null] };
  assert.throws(() => RelationsModel.validateQuery(badQuery, h.infos, h.limits), /关联键/);
});
