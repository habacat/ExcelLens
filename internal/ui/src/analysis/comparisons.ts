/**
 * Presentation and explanation only; all ratios and prefixes come from the
 * exact Go aggregation result. No client arithmetic over rounded subtotals.
 */
import type {
  AnalysisCell,
  AnalysisMetric,
  AnalysisQuery,
  ComparedValue,
  ComparisonMode,
} from '../contracts/analysis.ts';

export const COMPARISON_MODES: readonly (readonly [ComparisonMode, string])[] = Object.freeze([
  ['', '原始聚合'] as const,
  ['percent_grand', '占范围总计'] as const,
  ['percent_row', '占行合计'] as const,
  ['percent_column', '占列合计'] as const,
  ['running_total', '累计'] as const,
  ['running_percent', '累计占比'] as const,
]);

const MODE_LABELS = new Map<string, string>(COMPARISON_MODES);
const STATE_LABELS = new Map<string, string>([
  ['empty', '无有效值'],
  ['empty_denominator', '分母无有效值'],
  ['zero_denominator', '分母为0'],
]);

export interface ComparisonCellLike {
  readonly display?: string;
  readonly exact?: string;
  readonly comparison?: ComparedValue;
}

export function isRunningComparison(
  metric: Pick<AnalysisMetric, 'comparison'> | null | undefined,
): boolean {
  return metric?.comparison === 'running_total' || metric?.comparison === 'running_percent';
}

export function selectedComparisonCell(
  cell: AnalysisCell | ComparisonCellLike | null | undefined,
): ComparedValue | ComparisonCellLike {
  return cell?.comparison ?? cell ?? {};
}

export function comparisonLabel(
  metric: Pick<AnalysisMetric, 'comparison' | 'comparisonAxis'> | null | undefined,
): string {
  if (!metric?.comparison) return '';
  const base = MODE_LABELS.get(metric.comparison) ?? '未知比较方式';
  return isRunningComparison(metric)
    ? `${metric.comparisonAxis === 'columns' ? '沿列' : '沿行'}${base}`
    : base;
}

export function comparisonTitle(
  cell: AnalysisCell | ComparisonCellLike | null | undefined,
): string {
  const derived = cell?.comparison;
  if (!derived) return cell?.exact ? `精确值：${cell.exact}` : '';
  const kind = MODE_LABELS.get(derived.mode) ?? '指标比较';
  const items: string[] = [
    kind + (derived.axis ? `（${derived.axis === 'columns' ? '沿列' : '沿行'}）` : ''),
  ];
  if (derived.status) {
    items.push(STATE_LABELS.get(derived.status) ?? '比较结果不可用');
  } else {
    items.push(
      `${derived.percent ? '精确比值：' : '精确值：'}${derived.exact}${derived.percent ? '（1=100%）' : ''}`,
    );
  }
  const exactText = cell.exact ?? '';
  items.push(`本分组原始聚合：${exactText === '' ? '无' : exactText}`);
  if (derived.numerator !== undefined) {
    items.push(`${derived.axis ? '累计聚合：' : '分子：'}${derived.numerator || '无'}`);
  }
  if (derived.denominator !== undefined) {
    items.push(`完整范围分母：${derived.denominator || '无'}`);
  }
  if (derived.axis) {
    items.push('来源明细仅查看当前分组；累计还包含此前分类');
  }
  return items.join('；');
}

export function comparisonScope(
  query: Pick<AnalysisQuery, 'metrics' | 'layout'> | null | undefined,
): string {
  const metrics = query?.metrics ?? [];
  if (!metrics.some((metric) => Boolean(metric.comparison))) return '';
  const notes = ['占比使用完整范围分母；百分比精确值为比值，1=100%'];
  if (query?.layout === 'tree') {
    notes.push('每层范围为该父分组，子层占比不代表整个源表');
  }
  if (metrics.some(isRunningComparison)) {
    notes.push(
      '累计按原始聚合排序后的全部分类计算，再截取前 N 项；沿列按列标签排序；明细点击只查看当前分组',
    );
  }
  if (
    metrics.some(
      (metric) =>
        Boolean(metric.comparison) &&
        ['average', 'distinct', 'min', 'max'].includes(metric.aggregate),
    )
  ) {
    notes.push('平均、去重与极值按原聚合口径比较，占比不保证相加为100%');
  }
  return notes.join('；');
}

export const AnalysisComparisons = Object.freeze({
  modes: COMPARISON_MODES,
  running: isRunningComparison,
  selected: selectedComparisonCell,
  label: comparisonLabel,
  title: comparisonTitle,
  scope: comparisonScope,
});
