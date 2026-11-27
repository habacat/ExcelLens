import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { PrintingLimits, SourceCapabilities } from '../contracts/formats.ts';
import {
  PrintingModel,
  type PrintBinding,
  type PrintFormInput,
  type PrintPageResponse,
  type PrintSummaryResponse,
} from './printing-model.ts';

export const testPrintLimits: PrintingLimits = {
  outputPages: 20,
  outputBytes: 65536,
  pages: 20,
  rows: 100,
  columns: 20,
  cells: 500,
  areas: 4,
  parts: 50,
  bytes: 65536,
  requestBytes: 8192,
  notices: 32,
  domPixels: 8_000_000,
  unitsPerPoint: 1_000_000_000,
  firstPageMin: -32768,
  firstPageMax: 4_294_967_295,
};

export const testPrintSource: SourceCapabilities = {
  id: 'xls',
  maxRows: 65536,
  maxColumns: 256,
  styled: true,
  textAmounts: false,
  conditionalMetadata: false,
  catalogueMetadata: false,
  dataRuleMetadata: false,
  noteMetadata: false,
  pictureMetadata: false,
  printMetadata: true,
  textClustering: false,
};

export const testPrintBinding: PrintBinding = {
  workbookKey: 'content',
  book: 'path-one',
  sheet: '明细',
};

export const testPrintJobId = 'print-job';

export function testPrintInput(overrides: Partial<PrintFormInput> = {}): PrintFormInput {
  return {
    defaults: true,
    paper: 'source',
    width: '210',
    height: '297',
    orientation: '',
    scaleMode: 'source',
    percent: '100',
    fitWidth: '1',
    fitHeight: '0',
    area: '',
    repeatRows: '',
    repeatColumns: '',
    clearRepeatRows: false,
    clearRepeatColumns: false,
    marginMode: 'source',
    left: '20',
    right: '20',
    top: '20',
    bottom: '20',
    header: '8',
    footer: '8',
    pageOrder: '',
    firstPage: '',
    rowHeight: '',
    columnWidth: '',
    ignoreBreaks: false,
    ...overrides,
  };
}

export function testPrintFixture(): {
  readonly s: PrintSummaryResponse;
  readonly p: PrintPageResponse;
} {
  const s: PrintSummaryResponse = {
    id: testPrintJobId,
    ...testPrintBinding,
    sourceComplete: true,
    sourceProperties: {},
    sourceNotices: [],
    summary: {
      sourceId: 'a'.repeat(64),
      sheet: testPrintBinding.sheet,
      pages: 1,
      paper: { width: '100000000000', height: '150000000000' },
      margins: { left: '0', right: '0', top: '0', bottom: '0', header: '0', footer: '0' },
      headings: { width: '0', height: '0' },
      scale: { numerator: '1', denominator: '2', percent: '50' },
      areas: [{ row: 1, col: 1, endRow: 1, endCol: 1 }],
      repeatRows: [],
      repeatColumns: [],
      pageOrder: 'downThenOver',
      firstPageNumber: 1,
      defaults: {},
      approximate: false,
      origins: { paper: 'source' },
      notices: [],
    },
  };
  const p: PrintPageResponse = {
    id: testPrintJobId,
    ...testPrintBinding,
    page: 0,
    content: {
      page: {
        number: 1,
        area: 1,
        offsetX: '0',
        offsetY: '0',
        content: { width: '15000000000', height: '7500000000' },
        body: { width: '15000000000', height: '7500000000' },
        rows: [{ first: 1, last: 1, offset: '0', length: '15000000000', repeated: false }],
        columns: [{ first: 1, last: 1, offset: '0', length: '30000000000', repeated: false }],
        oversize: false,
      },
      rows: [
        {
          number: 1,
          part: 0,
          start: '0',
          size: '15000000000',
          clipStart: '0',
          clipSize: '15000000000',
          repeated: false,
        },
      ],
      columns: [
        {
          number: 1,
          part: 0,
          start: '0',
          size: '30000000000',
          clipStart: '0',
          clipSize: '30000000000',
          repeated: false,
        },
      ],
      cells: [
        {
          row: 1,
          col: 1,
          ref: 'A1',
          style: 0,
          cell: {
            ref: 'A1',
            col: 1,
            raw: '001',
            display: '001',
            hasFormula: false,
            style: 0,
            type: 'inlineStr',
          },
          box: { x: '0', y: '0', width: '30000000000', height: '15000000000' },
          clip: { x: '0', y: '0', width: '30000000000', height: '15000000000' },
        },
      ],
      styles: { '0': {} },
      notices: [],
    },
  };
  return { s, p };
}

test('source settings stay absent until explicit overrides; source coordinates and empty title lists are distinct', () => {
  assert.deepEqual(
    PrintingModel.options(testPrintInput(), testPrintSource, testPrintLimits),
    { usePreviewDefaults: true },
  );
  const input = testPrintInput({
    paper: 'custom',
    width: '215.9',
    height: '279.4',
    orientation: 'landscape',
    scaleMode: 'fit',
    fitWidth: '1',
    fitHeight: '0',
    area: '$A$1:$B$4，C8',
    repeatRows: '1:2,5',
    repeatColumns: '$A:$B',
    marginMode: 'custom',
    left: '0',
    clearRepeatColumns: true,
    firstPage: '-1',
    rowHeight: '15.25',
    columnWidth: '12.5',
  });
  const out = PrintingModel.options(input, testPrintSource, testPrintLimits);
  assert.deepEqual(out.paper, { width: '215.9mm', height: '279.4mm' });
  assert.deepEqual(out.fit, { width: 1, height: 0 });
  assert.deepEqual(out.repeatColumns, []);
  assert.deepEqual(out.repeatRows, [
    { first: 1, last: 2 },
    { first: 5, last: 5 },
  ]);
  assert.deepEqual(out.areas, [
    { row: 1, col: 1, endRow: 4, endCol: 2 },
    { row: 8, col: 3, endRow: 8, endCol: 3 },
  ]);
  assert.equal(out.margins?.left, '0mm');
  assert.equal(out.firstPageNumber, -1);
  assert.equal(out.missingRowHeight, '15.25');
  assert.equal(out.missingColumnWidth, '12.5');
});

test('user ranges are bounded by the supplied source registry and do not silently reverse or truncate', () => {
  for (const area of ['A0', 'A1:IV65537', 'IW1', 'B2:A1', 'A1,,B2', 'Other!A1', 'A1:B2:C3']) {
    assert.throws(() =>
      PrintingModel.options(testPrintInput({ area }), testPrintSource, testPrintLimits),
    );
  }
  const invalidOverrides: readonly Partial<PrintFormInput>[] = [
    { percent: '0', scaleMode: 'percent' },
    { percent: '401', scaleMode: 'percent' },
    { paper: 'custom', width: 'NaN' },
    { scaleMode: 'fit', fitWidth: '0', fitHeight: '0' },
    { repeatRows: '2:1' },
    { repeatColumns: 'IW' },
    { firstPage: '4294967296' },
  ];
  for (const override of invalidOverrides) {
    assert.throws(() =>
      PrintingModel.options(testPrintInput(override), testPrintSource, testPrintLimits),
    );
  }
  const wider: SourceCapabilities = { ...testPrintSource, maxRows: 1_000_000, maxColumns: 300 };
  assert.equal(
    PrintingModel.options(testPrintInput({ area: 'IW1' }), wider, testPrintLimits).areas?.[0]?.col,
    257,
  );
});

test('an accepted page retains source text/type and exact nanopts while projecting only final CSS geometry', () => {
  const { s, p } = testPrintFixture();
  const before = JSON.stringify(p);
  PrintingModel.summary(s, testPrintBinding, testPrintJobId, testPrintLimits, testPrintSource);
  PrintingModel.page(p, testPrintBinding, testPrintJobId, 0, s, testPrintLimits, testPrintSource);
  const projection = PrintingModel.project(s, p, testPrintLimits);
  assert.ok(projection.cells[0]);
  assert.equal(projection.cells[0].width, 20);
  assert.equal(projection.cells[0].height, 10);
  assert.equal(projection.width, 133.333333);
  assert.equal(JSON.stringify(p), before);
  assert.equal(p.content.cells[0]?.cell?.raw, '001');
  assert.equal(PrintingModel.units('9007199254740993'), 9007199254740993n);
  const first = PrintingModel.units('9007199254740990000');
  const last = first + 2_250_000_000n;
  assert.equal(PrintingModel.pixels(last - first, testPrintLimits), 3);
});

test('source/path/task and physical coverage mismatches fail before adoption', () => {
  const mutatePage = (mutator: (p: Record<string, unknown>) => void) => {
    const { s, p } = testPrintFixture();
    const copy = JSON.parse(JSON.stringify(p)) as Record<string, unknown>;
    mutator(copy);
    assert.throws(() =>
      PrintingModel.page(
        copy,
        testPrintBinding,
        testPrintJobId,
        0,
        s,
        testPrintLimits,
        testPrintSource,
      ),
    );
  };
  mutatePage((p) => {
    p.book = 'another-path';
  });
  mutatePage((p) => {
    p.id = 'another-job';
  });
  mutatePage((p) => {
    const c = p.content as { cells: { cell: { ref: string } }[] };
    const first = c.cells[0];
    if (first) first.cell.ref = 'B1';
  });
  mutatePage((p) => {
    const c = p.content as { cells: { clip: { width: string } }[] };
    const first = c.cells[0];
    if (first) first.clip.width = '30000000001';
  });
  mutatePage((p) => {
    const c = p.content as { rows: { clipStart: string }[] };
    const first = c.rows[0];
    if (first) first.clipStart = '1';
  });
  mutatePage((p) => {
    const c = p.content as { rows: { number: number }[] };
    const first = c.rows[0];
    if (first) first.number = 65537;
  });
  mutatePage((p) => {
    const c = p.content as { page: { number: number } };
    c.page.number = 2;
  });
  mutatePage((p) => {
    const c = p.content as { columns: { part: number }[] };
    const first = c.columns[0];
    if (first) first.part = 2;
  });

  const mutateSummary = (mutator: (s: Record<string, unknown>) => void) => {
    const { s } = testPrintFixture();
    const copy = JSON.parse(JSON.stringify(s)) as Record<string, unknown>;
    mutator(copy);
    assert.throws(() =>
      PrintingModel.summary(
        copy,
        testPrintBinding,
        testPrintJobId,
        testPrintLimits,
        testPrintSource,
      ),
    );
  };
  mutateSummary((s) => {
    (s.summary as { scale: { denominator: string } }).scale.denominator = '0';
  });
  mutateSummary((s) => {
    (s.summary as { scale: { numerator: string } }).scale.numerator = '9';
  });
  mutateSummary((s) => {
    s.sourceComplete = 'yes';
  });
  mutateSummary((s) => {
    (s.summary as { sourceId: string }).sourceId = 'not-an-identity';
  });
  mutateSummary((s) => {
    (s.summary as { paper: { width: string } }).paper.width = '1e9';
  });
});

test('oversized source boxes stay intact; an unrenderable browser layout is an explicit error', () => {
  const { s, p } = testPrintFixture();
  const mutable = JSON.parse(JSON.stringify(p)) as {
    content: { cells: { box: { height: string } }[]; rows: { size: string }[] };
  };
  if (mutable.content.cells[0]) mutable.content.cells[0].box.height = '9223372036854775807';
  if (mutable.content.rows[0]) mutable.content.rows[0].size = '9223372036854775807';
  const validated = PrintingModel.page(
    mutable,
    testPrintBinding,
    testPrintJobId,
    0,
    s,
    testPrintLimits,
    testPrintSource,
  );
  assert.throws(() => PrintingModel.project(s, validated, testPrintLimits), /浏览器预览容量/);
  assert.equal(validated.content.cells[0]?.box.height, '9223372036854775807');
  for (const value of ['01', 'NaN', '-0', '9223372036854775808', 1, 1n]) {
    assert.throws(() => PrintingModel.units(value));
  }
});

test('an empty preview has no invented A1 page or fabricated scale', () => {
  const { s } = testPrintFixture();
  const empty = {
    ...s,
    summary: {
      ...s.summary,
      pages: 0,
      areas: null,
      scale: { numerator: '', denominator: '', percent: '' },
      paper: { width: '0', height: '0' },
    },
  };
  assert.equal(
    PrintingModel.summary(empty, testPrintBinding, testPrintJobId, testPrintLimits, testPrintSource),
    empty,
  );
  assert.throws(() =>
    PrintingModel.page(
      testPrintFixture().p,
      testPrintBinding,
      testPrintJobId,
      0,
      empty,
      testPrintLimits,
      testPrintSource,
    ),
  );
});
