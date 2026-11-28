/**
 * Strict wire response validator and chart data builder for Selection Quick Analysis.
 */
import { t as localize } from '../core/i18n.ts';
import type { QuickSegment, QuickSummary } from '../contracts/analysis.ts';
import type { QuickCounts, SelectionQuickResponse } from '../contracts/features.ts';
import type { SelectionQuickLimits } from '../contracts/formats.ts';

export interface SelectionQuickRequest {
  readonly workbookKey: string;
  readonly book: string;
  readonly sheet: string;
  readonly viewId: string;
  readonly filterId: string;
  readonly excludeThroughRow: number;
  readonly start: string;
  readonly end: string;
  readonly visualStart?: number;
  readonly visualEnd?: number;
  readonly skipFirstRow: boolean;
  readonly minuend: number;
  readonly subtrahend: number;
}

export interface SelectionQuickColumn {
  readonly column: number;
  readonly counts: QuickCounts;
  readonly summary: QuickSummary;
  readonly segments: readonly QuickSegment[];
}

export interface SelectionQuickDifference {
  readonly paired: number;
  readonly unpaired: number;
  readonly summary: QuickSummary;
  readonly segments: readonly QuickSegment[];
}

export interface SelectionQuickWireResponse extends SelectionQuickResponse {
  readonly visualStart?: number;
  readonly visualEnd?: number;
  readonly excludeThroughRow?: number;
}

export interface SelectionQuickSeries {
  readonly summary: QuickSummary;
  readonly segments: readonly QuickSegment[];
}

export interface SelectionQuickChartData {
  readonly columnKeys: readonly [string];
  readonly rows: readonly {
    readonly keys: readonly [string];
    readonly values: readonly [
      {
        readonly exact: string;
        readonly display: string;
        readonly count: number;
      },
    ];
  }[];
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function isUnknownArray(value: unknown): value is readonly unknown[] {
  return Array.isArray(value);
}

function isInteger(n: unknown, min = 0, max = Number.MAX_SAFE_INTEGER): n is number {
  return typeof n === 'number' && Number.isSafeInteger(n) && n >= min && n <= max;
}

function isExact(s: unknown): s is string {
  return (
    typeof s === 'string' &&
    s.length <= 8192 &&
    (/^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?$/.test(s) || /^[+-]?\d+\/[1-9]\d*$/.test(s))
  );
}

function normalizeOptional(value: unknown): unknown {
  return value || '';
}

function fail(): never {
  throw new Error(localize('ui.selectionQuickModel.quickAnalysisResultsDoNot'));
}

const EXACT_MATCH_KEYS = [
  'workbookKey',
  'book',
  'sheet',
  'viewId',
  'start',
  'end',
  'skipFirstRow',
  'minuend',
  'subtrahend',
] as const;

const OPTIONAL_MATCH_KEYS = [
  'filterId',
  'visualStart',
  'visualEnd',
  'excludeThroughRow',
] as const;

function readSummary(s: unknown, total: number, maxRow: number): QuickSummary {
  if (!isRecord(s) || !isInteger(s.count, 0, total) || !isExact(s.sum)) fail();
  if (s.count === 0) {
    if (
      s.sum !== '0' ||
      s.average !== '' ||
      s.minimum !== '' ||
      s.maximum !== '' ||
      s.minimumRow !== 0 ||
      s.maximumRow !== 0
    ) {
      fail();
    }
    return {
      count: 0,
      sum: '0',
      average: '',
      minimum: '',
      maximum: '',
      minimumRow: 0,
      maximumRow: 0,
    };
  }
  if (
    !isExact(s.average) ||
    !isExact(s.minimum) ||
    !isExact(s.maximum) ||
    !isInteger(s.minimumRow, 1, maxRow) ||
    !isInteger(s.maximumRow, 1, maxRow)
  ) {
    fail();
  }
  return {
    count: s.count,
    sum: s.sum,
    average: s.average,
    minimum: s.minimum,
    maximum: s.maximum,
    minimumRow: s.minimumRow,
    maximumRow: s.maximumRow,
  };
}

function readSeries(
  s: unknown,
  count: number,
  pageRows: number,
  maxSegments: number,
  maxRow: number,
): SelectionQuickSeries {
  if (!isRecord(s)) fail();
  const summary = readSummary(s.summary, pageRows, maxRow);
  if (
    summary.count !== count ||
    !isUnknownArray(s.segments) ||
    s.segments.length !== Math.min(pageRows, maxSegments)
  ) {
    fail();
  }
  let next = 1;
  let total = 0;
  const segments: QuickSegment[] = [];
  for (const seg of s.segments) {
    if (
      !isRecord(seg) ||
      seg.first !== next ||
      !isInteger(seg.last, seg.first, pageRows) ||
      !isInteger(seg.firstRow, 1, maxRow) ||
      !isInteger(seg.lastRow, 1, maxRow)
    ) {
      fail();
    }
    const segSummary = readSummary(seg.summary, seg.last - seg.first + 1, maxRow);
    total += segSummary.count;
    next = seg.last + 1;
    segments.push({
      first: seg.first,
      last: seg.last,
      firstRow: seg.firstRow,
      lastRow: seg.lastRow,
      summary: segSummary,
    });
  }
  if (next !== pageRows + 1 || total !== count) fail();
  return { summary, segments };
}

function readCounts(c: unknown, pageRows: number): QuickCounts {
  if (!isRecord(c)) fail();
  const numCount = c.number;
  const negative = c.negative;
  const zero = c.zero;
  const positive = c.positive;
  const blank = c.blank;
  const other = c.other;
  const errors = c.errors;
  const missing = c.missing;
  const invalid = c.invalid;
  if (
    !isInteger(numCount, 0, pageRows) ||
    !isInteger(negative, 0, pageRows) ||
    !isInteger(zero, 0, pageRows) ||
    !isInteger(positive, 0, pageRows) ||
    !isInteger(blank, 0, pageRows) ||
    !isInteger(other, 0, pageRows) ||
    !isInteger(errors, 0, pageRows) ||
    !isInteger(missing, 0, pageRows) ||
    !isInteger(invalid, 0, pageRows)
  ) {
    fail();
  }
  if (
    numCount !== negative + zero + positive ||
    numCount + blank + other + errors + missing + invalid !== pageRows
  ) {
    fail();
  }
  return {
    number: numCount,
    negative,
    zero,
    positive,
    blank,
    other,
    errors,
    missing,
    invalid,
  };
}

export function validateSelectionQuick(
  page: unknown,
  request: SelectionQuickRequest,
  limits: SelectionQuickLimits,
  rows: number,
  first: number,
  last: number,
  maxRow: number,
): SelectionQuickWireResponse {
  if (!isRecord(page) || new TextEncoder().encode(JSON.stringify(page)).length > limits.bytes) {
    fail();
  }
  for (const key of EXACT_MATCH_KEYS) {
    if (page[key] !== request[key]) fail();
  }
  for (const key of OPTIONAL_MATCH_KEYS) {
    if (normalizeOptional(page[key]) !== normalizeOptional(request[key])) fail();
  }
  const pageRows = rows - (request.skipFirstRow ? 1 : 0);
  if (
    page.selectedRows !== rows ||
    page.rows !== pageRows ||
    page.firstColumn !== first ||
    page.lastColumn !== last ||
    !isUnknownArray(page.columns) ||
    page.columns.length !== last - first + 1
  ) {
    fail();
  }
  const columns: SelectionQuickColumn[] = [];
  for (let i = 0; i < page.columns.length; i++) {
    const col = page.columns[i];
    if (!isRecord(col) || col.column !== first + i) fail();
    const counts = readCounts(col.counts, pageRows);
    const series = readSeries(col, counts.number, pageRows, limits.segments, maxRow);
    columns.push({
      column: col.column,
      counts,
      summary: series.summary,
      segments: series.segments,
    });
  }

  let difference: SelectionQuickDifference | undefined;
  if (request.minuend) {
    const d = page.difference;
    if (
      !isRecord(d) ||
      !isInteger(d.paired, 0, pageRows) ||
      !isInteger(d.unpaired, 0, pageRows) ||
      d.paired + d.unpaired !== pageRows
    ) {
      fail();
    }
    const series = readSeries(d, d.paired, pageRows, limits.segments, maxRow);
    difference = {
      paired: d.paired,
      unpaired: d.unpaired,
      summary: series.summary,
      segments: series.segments,
    };
  } else if (page.difference != null) {
    fail();
  }

  return {
    workbookKey: request.workbookKey,
    book: request.book,
    sheet: request.sheet,
    viewId: request.viewId,
    filterId: request.filterId || undefined,
    start: request.start,
    end: request.end,
    visualStart: request.visualStart,
    visualEnd: request.visualEnd,
    excludeThroughRow: request.excludeThroughRow,
    skipFirstRow: request.skipFirstRow,
    minuend: request.minuend,
    subtrahend: request.subtrahend,
    selectedRows: rows,
    rows: pageRows,
    firstColumn: first,
    lastColumn: last,
    columns,
    difference,
  };
}

export function buildSelectionQuickChart(series: SelectionQuickSeries): SelectionQuickChartData {
  return {
    columnKeys: [localize('ui.selectionQuickModel.segmentAverage')],
    rows: series.segments.map((p, i) => ({
      keys: [String(i + 1)],
      values: [
        {
          exact: p.summary.average,
          display: p.summary.average,
          count: p.summary.count,
        },
      ],
    })),
  };
}

export const SelectionQuickModel = Object.freeze({
  validate: validateSelectionQuick,
  chart: buildSelectionQuickChart,
});
