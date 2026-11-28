/**
 * Presentation and explanation only; all ratios and prefixes come from the
 * exact Go aggregation result. No client arithmetic over rounded subtotals.
 */
import { t as localize } from '../core/i18n.ts';
import type {
  AnalysisCell,
  AnalysisMetric,
  AnalysisQuery,
  ComparedValue,
  ComparisonMode,
} from '../contracts/analysis.ts';

export const COMPARISON_MODES: readonly (readonly [ComparisonMode, string])[] = Object.freeze([
  ['', localize('ui.comparisons.originalAggregation')] as const,
  ['percent_grand', localize('ui.comparisons.totalPercentageRange')] as const,
  ['percent_row', localize('ui.comparisons.totalOccupiedRows')] as const,
  ['percent_column', localize('ui.comparisons.columnTotal')] as const,
  ['running_total', localize('ui.comparisons.cumulative')] as const,
  ['running_percent', localize('ui.comparisons.cumulativeProportion')] as const,
]);

const MODE_LABELS = new Map<string, string>(COMPARISON_MODES);
const STATE_LABELS = new Map<string, string>([
  ['empty', localize('ui.comparisons.noValidValues')],
  ['empty_denominator', localize('ui.comparisons.denominatorNoValidValue')],
  ['zero_denominator', localize('ui.comparisons.denominator')],
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
  const base = MODE_LABELS.get(metric.comparison) ?? localize('ui.comparisons.unknownComparisonMethod');
  return isRunningComparison(metric)
    ? `${metric.comparisonAxis === 'columns' ? localize('ui.comparisons.alongColumns') : localize('ui.comparisons.moveAlong')}${base}`
    : base;
}

export function comparisonTitle(
  cell: AnalysisCell | ComparisonCellLike | null | undefined,
): string {
  const derived = cell?.comparison;
  if (!derived) return cell?.exact ? localize('ui.comparisons.exactValue', { exact: cell.exact }) : '';
  const kind = MODE_LABELS.get(derived.mode) ?? localize('ui.comparisons.metricComparison');
  const items: string[] = [
    kind + (derived.axis ? `（${derived.axis === 'columns' ? localize('ui.comparisons.alongColumns') : localize('ui.comparisons.moveAlong')}）` : ''),
  ];
  if (derived.status) {
    items.push(STATE_LABELS.get(derived.status) ?? localize('ui.comparisons.comparisonResultsUnavailable'));
  } else {
    items.push(
      `${derived.percent ? localize('ui.comparisons.exactRatio') : localize('ui.comparisons.exactValue2674e22')}${derived.exact}${derived.percent ? '（1=100%）' : ''}`,
    );
  }
  const exactText = cell.exact ?? '';
  items.push(localize('ui.comparisons.originalAggregationThisGroup', { detail: exactText === '' ? localize('ui.common.none') : exactText }));
  if (derived.numerator !== undefined) {
    items.push(`${derived.axis ? localize('ui.comparisons.cumulativeAggregation') : localize('ui.comparisons.numerator')}${derived.numerator || localize('ui.common.none')}`);
  }
  if (derived.denominator !== undefined) {
    items.push(localize('ui.comparisons.fullRangeDenominator', { denominator: derived.denominator || localize('ui.common.none') }));
  }
  if (derived.axis) {
    items.push(localize('ui.comparisons.detailsSourceOnlyAvailableCurrent'));
  }
  return items.join('；');
}

export function comparisonScope(
  query: Pick<AnalysisQuery, 'metrics' | 'layout'> | null | undefined,
): string {
  const metrics = query?.metrics ?? [];
  if (!metrics.some((metric) => Boolean(metric.comparison))) return '';
  const notes = [localize('ui.comparisons.percentageCalculationsUseFullRange')];
  if (query?.layout === 'tree') {
    notes.push(localize('ui.comparisons.eachLayerCoversParentGroup'));
  }
  if (metrics.some(isRunningComparison)) {
    notes.push(
      localize('ui.comparisons.calculateAllCategoriesChronologicalOrder'),
    );
  }
  if (
    metrics.some(
      (metric) =>
        Boolean(metric.comparison) &&
        ['average', 'distinct', 'min', 'max'].includes(metric.aggregate),
    )
  ) {
    notes.push(localize('ui.comparisons.averageDeduplicationExtremeValuesCompared'));
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
