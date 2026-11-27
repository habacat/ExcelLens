/**
 * Pure validation and description helpers for Comparison Snapshots.
 */
import type {
  AnalysisCell,
  ComparisonSnapshot,
  ComparisonSnapshotCatalog,
  SnapshotComparison,
  SnapshotDelta,
} from '../contracts/analysis.ts';
import type { SnapshotLimits } from '../contracts/formats.ts';
import { boundedStringify } from './chart-export-model.ts';
import { comparisonLabel } from './comparisons.ts';
import { sourceScopeCaption } from './sources.ts';

export interface ValidatedSnapshotComparison extends SnapshotComparison {
  readonly totals: readonly SnapshotDelta[];
}

const encoder = new TextEncoder();

const AGGREGATE_LABELS: Readonly<Record<string, string>> = {
  sum: '求和',
  average: '平均',
  count: '计数',
  distinct: '去重计数',
  min: '最小',
  max: '最大',
};

const FILTER_OP_LABELS: Readonly<Record<string, string>> = {
  eq: '等于',
  neq: '不等于',
  contains: '包含',
  notcontains: '不包含',
  startswith: '开头是',
  endswith: '结尾是',
  blank: '为空',
  notblank: '不为空',
  gt: '大于',
  gte: '不小于',
  lt: '小于',
  lte: '不大于',
  between: '介于',
  in: '属于',
};

function inIntegerRange(n: unknown, min: number, max: number): n is number {
  return typeof n === 'number' && Number.isSafeInteger(n) && n >= min && n <= max;
}

function isPlainRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function isUnknownArray(value: unknown): value is readonly unknown[] {
  return Array.isArray(value);
}

function failInvalid(): never {
  throw new Error('快照数据无效，请刷新后重试');
}

function ensureBounded(value: unknown, limit: number): void {
  if (encoder.encode(boundedStringify(value, limit)).length > limit) failInvalid();
}

function validateCell(c: unknown): void {
  if (
    !isPlainRecord(c) ||
    typeof c.exact !== 'string' ||
    typeof c.display !== 'string' ||
    !inIntegerRange(c.count, 0, Number.MAX_SAFE_INTEGER)
  ) {
    failInvalid();
  }
  if (c.comparison !== undefined && c.comparison !== null) {
    if (
      !isPlainRecord(c.comparison) ||
      !['', 'empty', 'empty_denominator', 'zero_denominator'].includes(
        typeof c.comparison.status === 'string' ? c.comparison.status : '',
      ) ||
      typeof c.comparison.exact !== 'string' ||
      typeof c.comparison.display !== 'string'
    ) {
      failInvalid();
    }
  }
}

function assertSnapshotCatalog(
  data: unknown,
  limits: SnapshotLimits,
): asserts data is ComparisonSnapshotCatalog {
  ensureBounded(data, limits.bytes);
  if (
    !isPlainRecord(data) ||
    data.version !== 1 ||
    !inIntegerRange(data.revision, 0, Number.MAX_SAFE_INTEGER - 1) ||
    !isUnknownArray(data.items) ||
    data.items.length > limits.entries
  ) {
    failInvalid();
  }
  const ids = new Set<string>();
  for (const item of data.items) {
    if (
      !isPlainRecord(item) ||
      typeof item.id !== 'string' ||
      !item.id ||
      ids.has(item.id) ||
      typeof item.name !== 'string' ||
      typeof item.file !== 'string' ||
      typeof item.created !== 'string' ||
      !Number.isFinite(Date.parse(item.created))
    ) {
      failInvalid();
    }
    ids.add(item.id);
  }
}

export function validateSnapshotCatalog(
  data: unknown,
  limits: SnapshotLimits,
): ComparisonSnapshotCatalog {
  assertSnapshotCatalog(data, limits);
  return data;
}

function assertSnapshot(
  data: unknown,
  id: string,
  limits: SnapshotLimits,
): asserts data is ComparisonSnapshot {
  ensureBounded(data, limits.bytes);
  if (!isPlainRecord(data) || !isPlainRecord(data.result)) failInvalid();
  const r = data.result;
  if (
    data.version !== 1 ||
    data.id !== id ||
    typeof data.name !== 'string' ||
    typeof data.file !== 'string' ||
    typeof data.workbookKey !== 'string' ||
    !data.workbookKey ||
    typeof data.book !== 'string' ||
    !data.book ||
    !isPlainRecord(data.query) ||
    !isUnknownArray(data.fields) ||
    typeof data.created !== 'string' ||
    !Number.isFinite(Date.parse(data.created)) ||
    !isUnknownArray(r.columnKeys) ||
    r.columnKeys.length > limits.series ||
    !isUnknownArray(r.rows) ||
    r.rows.length > limits.rows ||
    !isUnknownArray(r.totals) ||
    r.totals.length !== r.columnKeys.length
  ) {
    failInvalid();
  }
  const keys = new Set<string>();
  for (const row of r.rows) {
    if (
      !isPlainRecord(row) ||
      !isUnknownArray(row.keys) ||
      !row.keys.every((s) => typeof s === 'string') ||
      !isUnknownArray(row.values) ||
      row.values.length !== r.columnKeys.length
    ) {
      failInvalid();
    }
    const k = JSON.stringify(row.keys);
    if (keys.has(k)) failInvalid();
    keys.add(k);
    for (const v of row.values) validateCell(v);
    validateCell(row.total);
  }
  for (const t of r.totals) validateCell(t);
  validateCell(r.grand);
}

export function validateSnapshot(
  data: unknown,
  id: string,
  limits: SnapshotLimits,
): ComparisonSnapshot {
  assertSnapshot(data, id, limits);
  return data;
}

function assertSnapshotComparison(
  data: unknown,
  left: Pick<ComparisonSnapshot, 'id'>,
  right: Pick<ComparisonSnapshot, 'id'>,
  limits: SnapshotLimits,
): asserts data is ValidatedSnapshotComparison {
  ensureBounded(data, 8 * limits.bytes);
  if (
    !isPlainRecord(data) ||
    data.leftId !== left.id ||
    data.rightId !== right.id ||
    !isUnknownArray(data.rows) ||
    data.rows.length > limits.rows * 2 ||
    !isUnknownArray(data.columnKeys) ||
    data.columnKeys.length > limits.series * 2 ||
    !isUnknownArray(data.totals) ||
    data.totals.length !== data.columnKeys.length
  ) {
    failInvalid();
  }
  const validateDelta = (d: unknown): void => {
    if (
      !isPlainRecord(d) ||
      !['', 'only_left', 'only_right', 'empty', 'unavailable', 'missing'].includes(
        typeof d.status === 'string' ? d.status : '',
      )
    ) {
      failInvalid();
    }
    if (d.left !== undefined && d.left !== null) validateCell(d.left);
    if (d.right !== undefined && d.right !== null) validateCell(d.right);
    validateCell(d.difference);
    if (
      !isPlainRecord(d.change) ||
      typeof d.change.exact !== 'string' ||
      typeof d.change.display !== 'string'
    ) {
      failInvalid();
    }
  };
  for (const row of data.rows) {
    if (
      !isPlainRecord(row) ||
      !isUnknownArray(row.keys) ||
      !row.keys.every((s) => typeof s === 'string') ||
      !isUnknownArray(row.values) ||
      row.values.length !== data.columnKeys.length
    ) {
      failInvalid();
    }
    for (const v of row.values) validateDelta(v);
    validateDelta(row.total);
  }
  for (const t of data.totals) validateDelta(t);
  validateDelta(data.grand);
}

export function validateSnapshotComparison(
  data: unknown,
  left: Pick<ComparisonSnapshot, 'id'>,
  right: Pick<ComparisonSnapshot, 'id'>,
  limits: SnapshotLimits,
): ValidatedSnapshotComparison {
  assertSnapshotComparison(data, left, right, limits);
  return data;
}

export function snapshotMetricLabel(s: ComparisonSnapshot): string {
  const m = s.metric;
  const q = s.query;
  const field = (id?: string): string =>
    q.transforms?.find((f) => f.id === id)?.name ||
    s.fields.find((f) => f.id === id)?.name ||
    id ||
    '记录数';
  const baseName = m.name || `${field(m.field)} ${AGGREGATE_LABELS[m.aggregate] ?? m.aggregate}`;
  return baseName + (m.comparison ? ` ${comparisonLabel(m)}` : '');
}

export function snapshotSeriesCells(
  s: ComparisonSnapshot,
  key: string | null,
): (AnalysisCell | null)[] {
  const j = key === null ? -1 : s.result.columnKeys.indexOf(key);
  return s.result.rows.map((row) =>
    key === null ? row.total : j < 0 ? null : (row.values[j] ?? null),
  );
}

export function snapshotDescription(s: ComparisonSnapshot): string {
  const q = s.query;
  const field = (id: string): string =>
    q.transforms?.find((f) => f.id === id)?.name ||
    s.fields.find((f) => f.id === id)?.name ||
    '字段';
  const source =
    q.composition?.name || (q.source.startsWith('wb:') ? q.source.slice(3) : '分析工作表');
  const lines: string[] = [
    `${s.file} / ${source}`,
    `分析时间：${new Date(s.analysisAt || s.created).toLocaleString()}`,
    `参与记录：${s.result.included.toLocaleString()}`,
  ];
  if (q.sourceScope) lines.push(`范围：${sourceScopeCaption(q.sourceScope)}`);
  if (q.headerRow) lines.push(`表头：第 ${q.headerRow} 行`);
  if (q.rows.length) lines.push(`分类：${q.rows.map(field).join(' / ')}`);
  if (q.columns?.length) lines.push(`系列：${q.columns.map(field).join(' / ')}`);
  for (const f of q.filters ?? []) {
    lines.push(
      `筛选：${field(f.field)} ${FILTER_OP_LABELS[f.op] ?? f.op}${f.value ? ` ${f.value}` : ''}`,
    );
  }
  for (const t of q.transforms ?? []) {
    lines.push(`计算列：${t.name}`);
  }
  return lines.join('\n');
}

export const SnapshotModel = Object.freeze({
  catalog: validateSnapshotCatalog,
  snapshot: validateSnapshot,
  comparison: validateSnapshotComparison,
  metric: snapshotMetricLabel,
  seriesCells: snapshotSeriesCells,
  description: snapshotDescription,
});
