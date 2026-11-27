/**
 * Strict validation for bounded profile responses, preserving exact numeric
 * representations without converting financial values to floating point.
 */
import type {
  DistributionBin,
  DistributionQuantile,
  NumericDistribution,
} from '../contracts/analysis.ts';
import type { ProfileResponse } from '../contracts/features.ts';
import type { ProfileLimits, SourceCapabilities } from '../contracts/formats.ts';

export interface ProfileRequest {
  readonly workbookKey: string;
  readonly book: string;
  readonly sheet: string;
  readonly bindingViewId: string;
  readonly bindingFilterId: string;
  readonly start: string;
  readonly end: string;
  readonly filterId: string;
  readonly includeDistribution: boolean;
  readonly visualStart?: number;
  readonly visualEnd?: number;
  readonly excludeThroughRow?: number;
  readonly viewId?: string;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function isUnknownArray(value: unknown): value is readonly unknown[] {
  return Array.isArray(value);
}

function isInteger(
  value: unknown,
  min = 0,
  max = Number.MAX_SAFE_INTEGER,
): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= min && value <= max;
}

function isExactNumber(value: unknown): value is string {
  return (
    typeof value === 'string' &&
    value.length <= 8192 &&
    /^(?:[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?|[+-]?\d+\/[1-9]\d*)$/.test(value)
  );
}

function fail(): never {
  throw new Error('数据洞察结果与当前来源或范围不符，请重试');
}

function sameOptionalValue(a: unknown, b: unknown): boolean {
  const normA = a === undefined || a === null || a === 0 || a === '' ? '' : a;
  const normB = b === undefined || b === null || b === 0 || b === '' ? '' : b;
  return normA === normB;
}

export function validateProfileResponse(
  page: unknown,
  request: ProfileRequest,
  limits: ProfileLimits | null | undefined,
  source: Pick<SourceCapabilities, 'maxRows' | 'maxColumns'>,
): ProfileResponse {
  if (
    !isRecord(page) ||
    !limits?.bytes ||
    new TextEncoder().encode(JSON.stringify(page)).length > limits.bytes
  ) {
    fail();
  }
  if (
    page.workbookKey !== request.workbookKey ||
    page.book !== request.book ||
    page.sheet !== request.sheet ||
    page.start !== request.start ||
    page.end !== request.end ||
    page.bindingViewId !== request.bindingViewId
  ) {
    fail();
  }
  if (
    !sameOptionalValue(page.filterId, request.filterId) ||
    !sameOptionalValue(page.bindingFilterId, request.bindingFilterId) ||
    !sameOptionalValue(page.viewId, request.viewId) ||
    !sameOptionalValue(page.visualStart, request.visualStart) ||
    !sameOptionalValue(page.visualEnd, request.visualEnd) ||
    !sameOptionalValue(page.excludeThroughRow, request.excludeThroughRow)
  ) {
    fail();
  }
  if (
    Boolean(page.includeDistribution) !== request.includeDistribution ||
    page.range !== `${request.start}:${request.end}` ||
    page.filtered !== Boolean(request.filterId)
  ) {
    fail();
  }
  if (
    !isInteger(page.rows, 0, source.maxRows) ||
    !isInteger(page.firstColumn, 1, source.maxColumns) ||
    !isInteger(page.lastColumn, page.firstColumn, source.maxColumns) ||
    !isInteger(page.total, 0, limits.cells) ||
    page.total !== page.rows * (page.lastColumn - page.firstColumn + 1)
  ) {
    fail();
  }
  if (
    !isInteger(page.nonEmpty, 0, page.total) ||
    !isInteger(page.blank, 0, page.total) ||
    !isInteger(page.number, 0, page.total) ||
    !isInteger(page.text, 0, page.total) ||
    !isInteger(page.formulas, 0, page.total) ||
    !isInteger(page.cacheMissing, 0, page.total) ||
    !isInteger(page.errors, 0, page.total)
  ) {
    fail();
  }
  if (
    page.nonEmpty + page.blank !== page.total ||
    page.number + page.text !== page.nonEmpty ||
    page.formulas > page.nonEmpty ||
    page.cacheMissing > page.formulas ||
    page.errors > page.text ||
    !isExactNumber(page.sumExact)
  ) {
    fail();
  }
  if (
    typeof page.sum !== 'string' ||
    page.sum.length > 8192 ||
    typeof page.average !== 'string' ||
    page.average.length > 8192 ||
    typeof page.minimum !== 'string' ||
    page.minimum.length > 8192 ||
    typeof page.maximum !== 'string' ||
    page.maximum.length > 8192
  ) {
    fail();
  }
  if (page.number === 0) {
    if (
      page.sumExact !== '0' ||
      page.averageExact !== '' ||
      page.minimumExact !== '' ||
      page.maximumExact !== ''
    ) {
      fail();
    }
  } else if (
    !isExactNumber(page.averageExact) ||
    !isExactNumber(page.minimumExact) ||
    !isExactNumber(page.maximumExact)
  ) {
    fail();
  }
  if (
    !isInteger(page.unique, 0, page.nonEmpty) ||
    typeof page.uniqueTruncated !== 'boolean' ||
    !isUnknownArray(page.topValues)
  ) {
    fail();
  }
  const topValues: { readonly value: string; readonly count: number }[] = [];
  for (const item of page.topValues) {
    if (!isRecord(item) || typeof item.value !== 'string' || !isInteger(item.count, 1, page.nonEmpty)) {
      fail();
    }
    topValues.push({ value: item.value, count: item.count });
  }
  const parseRefs = (raw: unknown): string[] => {
    if (!isUnknownArray(raw)) fail();
    const out: string[] = [];
    for (const ref of raw) {
      if (typeof ref !== 'string' || !/^[A-Z]+[1-9]\d*$/.test(ref)) fail();
      out.push(ref);
    }
    return out;
  };
  const errorRefs = parseRefs(page.errorRefs);
  const missingRefs = parseRefs(page.missingRefs);
  if (page.warning !== undefined && typeof page.warning !== 'string') fail();

  let distribution: NumericDistribution | undefined;
  if (request.includeDistribution) {
    const d = page.distribution;
    if (
      !isRecord(d) ||
      d.method !== 'exact' ||
      d.definition !== 'inclusive-linear' ||
      d.count !== page.number ||
      !isUnknownArray(d.quantiles) ||
      !isUnknownArray(d.bins)
    ) {
      fail();
    }
    const percents: readonly number[] = page.number ? [5, 25, 50, 75, 95] : [];
    if (d.quantiles.length !== percents.length) fail();
    const quantiles: DistributionQuantile[] = [];
    for (let i = 0; i < d.quantiles.length; i++) {
      const q = d.quantiles[i];
      const expectedPercent = percents[i];
      if (expectedPercent === undefined || !isRecord(q) || q.percent !== expectedPercent || !isExactNumber(q.value)) {
        fail();
      }
      quantiles.push({ percent: expectedPercent, value: q.value });
    }
    if (
      (page.number === 0 && d.bins.length !== 0) ||
      (page.number > 0 && d.bins.length !== limits.bins && d.bins.length !== 1)
    ) {
      fail();
    }
    const bins: DistributionBin[] = [];
    let totalCount = 0;
    for (const [at, rawBin] of d.bins.entries()) {
      if (
        !isRecord(rawBin) ||
        !isExactNumber(rawBin.lower) ||
        !isExactNumber(rawBin.upper) ||
        !isInteger(rawBin.count, 0, page.number) ||
        rawBin.upperInclusive !== (at === d.bins.length - 1)
      ) {
        fail();
      }
      const prevBin = at > 0 ? bins[at - 1] : undefined;
      if (prevBin && prevBin.upper !== rawBin.lower) fail();
      totalCount += rawBin.count;
      bins.push({
        lower: rawBin.lower,
        upper: rawBin.upper,
        upperInclusive: rawBin.upperInclusive,
        count: rawBin.count,
      });
    }
    const firstBin = bins[0];
    const lastBin = bins.at(-1);
    if (
      firstBin &&
      lastBin &&
      (firstBin.lower !== page.minimumExact || lastBin.upper !== page.maximumExact)
    ) {
      fail();
    }
    if (
      totalCount !== page.number ||
      (bins.length === 1 && firstBin && (firstBin.lower !== firstBin.upper || firstBin.count !== page.number))
    ) {
      fail();
    }
    distribution = {
      method: d.method,
      definition: d.definition,
      count: d.count,
      quantiles,
      bins,
    };
  } else if (page.distribution !== undefined && page.distribution !== null) {
    fail();
  }

  return {
    workbookKey: request.workbookKey,
    book: request.book,
    sheet: request.sheet,
    range: page.range,
    rows: page.rows,
    firstColumn: page.firstColumn,
    lastColumn: page.lastColumn,
    total: page.total,
    nonEmpty: page.nonEmpty,
    blank: page.blank,
    number: page.number,
    text: page.text,
    formulas: page.formulas,
    cacheMissing: page.cacheMissing,
    errors: page.errors,
    sum: page.sum,
    average: page.average,
    minimum: page.minimum,
    maximum: page.maximum,
    sumExact: page.sumExact,
    averageExact: page.averageExact,
    minimumExact: page.minimumExact,
    maximumExact: page.maximumExact,
    unique: page.unique,
    uniqueTruncated: page.uniqueTruncated,
    topValues,
    errorRefs,
    missingRefs,
    filtered: page.filtered,
    warning: page.warning,
    distribution,
  };
}

export const ProfileModel = Object.freeze({
  validate: validateProfileResponse,
});
