import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { RecordCardLimits, SourceCapabilities } from '../contracts/formats.ts';
import type { CellData } from '../contracts/workbook.ts';
import { columnName } from '../core/cell-address.ts';
import { RecordCardModel, type RecordCardRequest } from './record-card-model.ts';

const source: Pick<SourceCapabilities, 'maxRows' | 'maxColumns'> = {
  maxRows: 1_000_000,
  maxColumns: 16_384,
};

const limits: RecordCardLimits = {
  columns: 32,
  bytes: 8 << 20,
  requestBytes: 8192,
  textUnits: 4096,
};

const request: RecordCardRequest = {
  workbookKey: 'content',
  book: 'path',
  sheet: '明细',
  viewId: 'view',
  filterId: '',
  row: 7,
  headerRow: 1,
  firstColumn: 33,
  columns: 32,
  pinnedThroughRow: 2,
};

interface MutableRecordField {
  column: number;
  header?: Record<string, unknown>;
  cell: Record<string, unknown>;
}

interface MutableRecordPage {
  workbookKey: unknown;
  book: unknown;
  sheet: unknown;
  viewId: unknown;
  filterId?: unknown;
  row: unknown;
  headerRow: unknown;
  firstColumn: unknown;
  columns: unknown;
  pinnedThroughRow: unknown;
  position: unknown;
  totalRecords: unknown;
  previousRow: unknown;
  nextRow: unknown;
  totalColumns: unknown;
  rowStyle: unknown;
  headerStyle: unknown;
  rowIndexed: unknown;
  notices: unknown;
  fields: MutableRecordField[];
}

function makePage(): MutableRecordPage {
  return {
    ...request,
    columns: 3,
    position: 3,
    totalRecords: 5,
    previousRow: 9,
    nextRow: 2,
    totalColumns: 35,
    rowStyle: 0,
    headerStyle: 0,
    rowIndexed: true,
    notices: [],
    fields: [33, 34, 35].map((column) => ({
      column,
      header: {
        ref: `${columnName(column)}1`,
        col: column,
        raw: '同名',
        display: '同名',
        hasFormula: false,
        style: 0,
        type: 's',
      },
      cell: {
        ref: `${columnName(column)}7`,
        col: column,
        raw: '9007199254740993.31',
        display: '9,007,199,254,740,993.31',
        hasFormula: false,
        style: 0,
      },
    })),
  };
}

test('record model accepts the real short last page and optional CellData properties without converting source strings', () => {
  const p = makePage();
  delete p.filterId;
  const validated = RecordCardModel.validate(p, request, limits, source, columnName, 35);
  assert.equal(validated, p);
  const firstField = validated.fields[0];
  assert.ok(firstField);
  assert.equal(RecordCardModel.value(firstField.cell, 'raw'), '9007199254740993.31');
  assert.equal(RecordCardModel.value(firstField.cell, 'display'), '9,007,199,254,740,993.31');

  const text: CellData = {
    ref: 'A1',
    col: 1,
    raw: '0000123',
    display: '0000123',
    hasFormula: false,
    type: 's',
  };
  assert.equal(RecordCardModel.value(text, 'raw'), '0000123');
  assert.equal(RecordCardModel.type(text), '文本');

  const conditional: CellData = {
    ref: 'B1',
    col: 2,
    raw: '12.34',
    display: '12.34',
    hasFormula: false,
    conditional: { display: '12.3元' },
  };
  assert.equal(RecordCardModel.value(conditional, 'display'), '12.3元');
  assert.equal(RecordCardModel.value(conditional, 'raw'), '12.34');
});

test('foreign sources, wrong columns, forged coordinates and inconsistent record neighbors are rejected', () => {
  const mutations: readonly ((p: MutableRecordPage) => void)[] = [
    (p) => {
      p.book = 'foreign';
    },
    (p) => {
      p.workbookKey = 'foreign';
    },
    (p) => {
      p.sheet = 'foreign';
    },
    (p) => {
      p.viewId = 'foreign';
    },
    (p) => {
      p.filterId = 'foreign';
    },
    (p) => {
      p.pinnedThroughRow = 0;
    },
    (p) => {
      p.headerRow = 0;
    },
    (p) => {
      p.columns = 32;
    },
    (p) => {
      p.totalColumns = 36;
    },
    (p) => {
      p.fields.reverse();
    },
    (p) => {
      const f = p.fields[0];
      if (f) f.cell.ref = 'AG8';
    },
    (p) => {
      const f = p.fields[0];
      if (f) f.cell.col = 1;
    },
    (p) => {
      const f = p.fields[0];
      if (f?.header) f.header.ref = 'AG2';
    },
    (p) => {
      const f = p.fields[0];
      if (f) f.cell.raw = 9007199254740992;
    },
    (p) => {
      const f = p.fields[0];
      if (f) f.cell.hasFormula = undefined;
    },
    (p) => {
      const f = p.fields[0];
      if (f) f.cell.cacheMissing = 'true';
    },
    (p) => {
      p.previousRow = 7;
    },
    (p) => {
      p.previousRow = 0;
    },
    (p) => {
      p.nextRow = 9;
    },
    (p) => {
      p.position = 0;
    },
    (p) => {
      p.totalRecords = source.maxRows + 1;
    },
  ];
  for (const mutate of mutations) {
    const p = makePage();
    mutate(p);
    assert.throws(() => RecordCardModel.validate(p, request, limits, source, columnName, 35));
  }
});

test('header row zero preserves column identity and refuses a silently substituted header', () => {
  const req: RecordCardRequest = { ...request, headerRow: 0 };
  const p = makePage();
  p.headerRow = 0;
  for (const field of p.fields) delete field.header;
  assert.equal(RecordCardModel.validate(p, req, limits, source, columnName, 35), p);

  const first = p.fields[0];
  assert.ok(first);
  first.header = {};
  assert.throws(() => RecordCardModel.validate(p, req, limits, source, columnName, 35));
});

test('missing formula caches remain unavailable; unrecoverable formulas never become fabricated expressions', () => {
  const missing: CellData = {
    ref: 'D7',
    col: 4,
    raw: '',
    display: '#N/A',
    hasFormula: true,
    cacheMissing: true,
  };
  assert.equal(RecordCardModel.value(missing, 'raw'), null);
  assert.equal(RecordCardModel.value(missing, 'display'), null);
  assert.match(RecordCardModel.type(missing), /无保存结果/);

  const broken: CellData = {
    ref: 'E7',
    col: 5,
    raw: '17',
    display: '17',
    hasFormula: true,
    sharedFormula: true,
    formula: '[共享公式组 si=9：未找到主公式]',
  };
  assert.equal(RecordCardModel.value(broken, 'raw'), '17');
  assert.equal(RecordCardModel.formula(broken), null);
  assert.equal(RecordCardModel.formula({ ...broken, formula: '=1+1' }), '=1+1');
  assert.equal(RecordCardModel.formula({ ...broken, formula: '' }), null);
});

test('all Unicode segments reassemble exact source text and never split a surrogate pair', () => {
  for (const units of [2, 3, 7, 4096]) {
    const samples = [
      '',
      '中😀文'.repeat(2500),
      `${'x'.repeat(units - 1)}😀${'尾'.repeat(units + 3)}`,
      '\r\n\t<&>"\u2028'.repeat(1800),
    ];
    for (const text of samples) {
      const positions = RecordCardModel.offsets(text, units);
      const parts: string[] = [];
      for (let i = 0; i < positions.length - 1; i++) {
        const start = positions[i] ?? 0;
        const end = positions[i + 1] ?? start;
        const part = text.slice(start, end);
        assert.ok(part.length <= units);
        assert.ok(!/^[\uDC00-\uDFFF]|[\uD800-\uDBFF]$/.test(part));
        parts.push(part);
      }
      assert.equal(parts.join(''), text);
      assert.equal(RecordCardModel.preview(text, units), parts[0]);
    }
  }
});

test('page byte limits use the serialized strings and reject excess as a whole', () => {
  const p = makePage();
  const size = new TextEncoder().encode(JSON.stringify(p)).length;
  assert.equal(
    RecordCardModel.validate(p, request, { ...limits, bytes: size }, source, columnName, 35),
    p,
  );
  assert.throws(
    () =>
      RecordCardModel.validate(p, request, { ...limits, bytes: size - 1 }, source, columnName, 35),
    /超过/,
  );
});

test('unknown source types remain literal labels even when their names match JavaScript prototype properties', () => {
  for (const type of ['constructor', '__proto__', 'toString', 'unknown']) {
    const cell: CellData = {
      ref: 'A1',
      col: 1,
      raw: '原文',
      display: '原文',
      hasFormula: false,
      type,
    };
    assert.equal(RecordCardModel.type(cell), `源类型 ${type}`);
  }
});
