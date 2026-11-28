import { t as localize } from '../core/i18n.ts';
import type {
  AnalysisAggregateKind,
  AnalysisField,
  AnalysisFilterOp,
  AnalysisMetric,
  AnalysisTransformOp,
  ComparisonMode,
} from '../contracts/analysis.ts';
import { icon } from '../core/display-format.ts';
import { requireEl, requireInput, requireSelect } from '../core/dom.ts';
import { COMPARISON_MODES, isRunningComparison } from './comparisons.ts';
import { type DashboardFieldChoice } from './dashboard.ts';
import { AGGREGATIONS, metricLabel, type EditableAnalysisQuery } from './pivot-view.ts';
import type { ToastFn } from '../contracts/common.ts';

const VALID_AGGREGATES = new Set<string>(['sum', 'count', 'average', 'min', 'max', 'distinct']);

const VALID_COMPARISON_MODES = new Set<string>([
  '',
  'percent_grand',
  'percent_row',
  'percent_column',
  'running_total',
  'running_percent',
]);

const FILTER_OPERATIONS: readonly (readonly [AnalysisFilterOp, string])[] = Object.freeze([
  ['eq', localize('ui.common.equal')] as const,
  ['neq', localize('ui.common.notEqual')] as const,
  ['contains', localize('ui.common.including')] as const,
  ['notcontains', localize('ui.common.notIncluded')] as const,
  ['startswith', localize('ui.common.beginning')] as const,
  ['endswith', localize('ui.common.ending')] as const,
  ['gt', localize('ui.common.greaterThan')] as const,
  ['gte', localize('ui.common.greaterThanOrEqual')] as const,
  ['lt', localize('ui.common.lessThan')] as const,
  ['lte', localize('ui.common.lessThanOrEqual')] as const,
  ['between', localize('ui.queryEditor.withinRange')] as const,
  ['in', localize('ui.queryEditor.multipleValueMatching')] as const,
  ['blank', localize('ui.common.ifEmptyThenLeaveEmpty')] as const,
  ['notblank', localize('ui.common.notEmpty')] as const,
]);

const VALID_FILTER_OPS = new Set<string>(FILTER_OPERATIONS.map((x) => x[0]));

const TRANSFORM_OPERATIONS: readonly (readonly [AnalysisTransformOp, string])[] = Object.freeze([
  ['date', localize('ui.queryEditor.convertDate')] as const,
  ['time', localize('ui.queryEditor.convertTime')] as const,
  ['datetime', localize('ui.queryEditor.transferDateTime')] as const,
  ['text', localize('ui.queryEditor.convertText')] as const,
  ['number', localize('ui.queryEditor.conversionValue')] as const,
  ['trim', localize('ui.queryEditor.goSpace')] as const,
  ['upper', localize('ui.queryEditor.capital')] as const,
  ['lower', localize('ui.queryEditor.lowercase')] as const,
  ['year', localize('ui.queryEditor.extractYears')] as const,
  ['month', localize('ui.queryEditor.extractMonths')] as const,
  ['yearmonth', localize('ui.queryEditor.extractMonthsYears')] as const,
  ['abs', localize('ui.queryEditor.absoluteValue')] as const,
  ['multiply', localize('ui.queryEditor.constantMultiplier')] as const,
  ['divide', localize('ui.queryEditor.divideConstant')] as const,
  ['prefix', localize('ui.queryEditor.addPrefix')] as const,
  ['suffix', localize('ui.queryEditor.addSuffix')] as const,
  ['replace', localize('ui.queryEditor.replaceText')] as const,
  ['length', localize('ui.queryEditor.textLength')] as const,
]);

const VALID_TRANSFORM_OPS = new Set<string>([
  ...TRANSFORM_OPERATIONS.map((x) => x[0]),
  'map_text',
]);

const NUMERIC_TRANSFORM_OPS = new Set<AnalysisTransformOp>([
  'number',
  'abs',
  'multiply',
  'divide',
  'length',
]);

const TEMPORAL_TRANSFORM_OPS = new Set<AnalysisTransformOp>(['date', 'time', 'datetime']);

const ARGLESS_TRANSFORM_OPS = new Set<AnalysisTransformOp>([
  'text',
  'number',
  'trim',
  'upper',
  'lower',
  'year',
  'month',
  'yearmonth',
  'abs',
  'length',
  'map_text',
]);

export function parseAggregateKind(raw: string): AnalysisAggregateKind {
  return VALID_AGGREGATES.has(raw) ? (raw as AnalysisAggregateKind) : 'count';
}

function parseComparisonMode(raw: string): ComparisonMode {
  return VALID_COMPARISON_MODES.has(raw) ? (raw as ComparisonMode) : '';
}

function parseFilterOp(raw: string): AnalysisFilterOp {
  return VALID_FILTER_OPS.has(raw) ? (raw as AnalysisFilterOp) : 'eq';
}

function parseTransformOp(raw: string): AnalysisTransformOp {
  return VALID_TRANSFORM_OPS.has(raw) ? (raw as AnalysisTransformOp) : 'trim';
}

interface QueryEditorState {
  readonly fields: readonly AnalysisField[];
  readonly query: EditableAnalysisQuery | null;
}

export class QueryEditor {
  private readonly state: QueryEditorState;
  private readonly toast: ToastFn;
  private readonly loadFieldProfile: (field: AnalysisField) => Promise<void>;

  constructor(state: QueryEditorState, toast: ToastFn, loadFieldProfile: (field: AnalysisField) => Promise<void>) {
    this.state = state;
    this.toast = toast;
    this.loadFieldProfile = loadFieldProfile;
  }

  private fieldName(id: string | undefined): string {
    if (!id) return localize('ui.common.numberRecords');
    const f =
      this.state.query?.transforms.find((x) => x.id === id) ??
      this.state.fields.find((x) => x.id === id);
    return f?.name || id;
  }

  private metricCaption(m: AnalysisMetric): string {
    return metricLabel(m, (id) => this.fieldName(id));
  }

  private syncFirstMetric(): void {
    const q = this.state.query;
    const first = q?.metrics[0];
    if (q && first) {
      q.value = first.field || '';
      q.aggregate = first.aggregate;
    }
  }

  addMetric(field: string): void {
    const q = this.state.query;
    if (!q) return;
    if (!q.metrics.length && q.value) {
      q.metrics.push({ field: q.value, aggregate: q.aggregate, name: '' });
    }
    if (q.metrics.length >= 6) {
      this.toast(localize('ui.queryEditor.upIndicatorsPerAnalysis'), 'warning');
      return;
    }
    const fields = this.allFields();
    const choice = field || fields.find((f) => f.numeric)?.id || '';
    q.metrics.push({
      field: choice,
      aggregate: fields.find((f) => f.id === choice)?.numeric ? 'sum' : 'count',
      name: '',
    });
    this.syncFirstMetric();
    this.renderBuilder();
  }

  public renderMetricEditor(): void {
    const q = this.state.query;
    if (!q) return;
    const list = requireEl('analysisMetricEditor');
    list.replaceChildren();
    const metrics: AnalysisMetric[] = q.metrics.length
      ? q.metrics
      : [{ field: q.value || '', aggregate: q.aggregate, name: '' }];
    const adopt = (): void => {
      if (!q.metrics.length) q.metrics = metrics;
    };
    metrics.forEach((metric, i) => {
      const row = document.createElement('div');
      row.className = 'analysis-metric-row';
      const field = this.fieldSelect(metric.field || '', localize('ui.common.numberRecords'));
      field.title = localize('ui.queryEditor.fieldsInvolvedStatistics');
      field.onchange = (): void => {
        adopt();
        metric.field = field.value;
        if (i === 0) this.syncFirstMetric();
        this.renderBuilder();
      };
      const agg = document.createElement('select');
      agg.className = 'analysis-control';
      for (const [value, label] of AGGREGATIONS) agg.append(new Option(label, value));
      agg.value = metric.aggregate;
      agg.onchange = (): void => {
        adopt();
        metric.aggregate = parseAggregateKind(agg.value);
        if (i === 0) this.syncFirstMetric();
        this.renderBuilder();
      };
      const remove = document.createElement('button');
      remove.type = 'button';
      remove.textContent = '×';
      remove.title = localize('ui.queryEditor.removeMetrics');
      remove.onclick = (): void => {
        adopt();
        q.metrics.splice(i, 1);
        if (!q.metrics.length) {
          q.value = '';
          q.aggregate = 'count';
        } else {
          this.syncFirstMetric();
        }
        this.renderBuilder();
      };

      const presentation = document.createElement('div');
      presentation.className = 'analysis-metric-comparison';
      const compareLabel = document.createElement('label');
      compareLabel.textContent = localize('ui.queryEditor.resultDisplay');
      const compare = document.createElement('select');
      compare.className = 'analysis-control analysis-comparison-mode';
      compare.setAttribute('aria-label', localize('ui.queryEditor.metricResultDisplay', { i: i + 1 }));
      for (const [value, label] of COMPARISON_MODES) compare.append(new Option(label, value));
      compare.value = metric.comparison || '';
      compare.onchange = (): void => {
        adopt();
        metric.comparison = parseComparisonMode(compare.value);
        if (!isRunningComparison(metric)) delete metric.comparisonAxis;
        this.syncFirstMetric();
        this.renderBuilder();
      };
      compareLabel.append(compare);
      presentation.append(compareLabel);
      if (isRunningComparison(metric)) {
        const axisLabel = document.createElement('label');
        axisLabel.textContent = localize('ui.queryEditor.cumulativeDirection');
        const axis = document.createElement('select');
        axis.className = 'analysis-control analysis-comparison-axis';
        axis.setAttribute('aria-label', localize('ui.queryEditor.metricCumulativeDirection', { i: i + 1 }));
        axis.append(new Option(localize('ui.queryEditor.alongRowSortedCategory'), 'rows'), new Option(localize('ui.queryEditor.sortColumnSortedColumnLabel'), 'columns'));
        axis.value = metric.comparisonAxis || 'rows';
        axis.onchange = (): void => {
          adopt();
          metric.comparisonAxis = axis.value === 'columns' ? 'columns' : 'rows';
        };
        axisLabel.append(axis);
        presentation.append(axisLabel);
      }
      row.append(field, agg, remove, presentation);
      list.append(row);
    });
  }

  allFields(): DashboardFieldChoice[] {
    const additional: DashboardFieldChoice[] = (this.state.query?.transforms ?? []).map((t) => ({
      id: t.id,
      name: t.name,
      numeric: NUMERIC_TRANSFORM_OPS.has(t.op),
    }));
    return [...this.state.fields, ...additional];
  }

  private fieldSelect(
    value = '',
    emptyLabel = localize('ui.common.selectFields'),
    choices: readonly { readonly id: string; readonly name: string }[] = this.allFields(),
  ): HTMLSelectElement {
    const el = document.createElement('select');
    el.className = 'analysis-control';
    el.append(new Option(emptyLabel, ''));
    for (const f of choices) el.append(new Option(f.name, f.id));
    el.value = value;
    return el;
  }

  renderFields(): void {
    const list = requireEl('analysisFields');
    list.replaceChildren();
    const q = requireInput('analysisFieldSearch').value.trim().toLocaleLowerCase();
    const fragment = document.createDocumentFragment();
    for (const field of this.allFields()) {
      if (q && !`${field.name} ${field.id}`.toLocaleLowerCase().includes(q)) continue;
      const wrapper = document.createElement('div');
      wrapper.className = 'analysis-field-wrapper';
      const chip = document.createElement('button');
      chip.type = 'button';
      chip.className = 'analysis-field-chip';
      chip.draggable = true;
      chip.title = localize('ui.queryEditor.dragRowsColumnsOrSummary');
      chip.dataset.field = field.id;
      const type = document.createElement('span');
      type.className = 'analysis-type';
      type.textContent = field.numeric ? '#' : 'Aa';
      const name = document.createElement('span');
      name.textContent = field.name;
      chip.append(type, name);
      chip.addEventListener('dragstart', (e: DragEvent) => {
        e.dataTransfer?.setData('text/plain', field.id);
        if (e.dataTransfer) e.dataTransfer.effectAllowed = 'copyMove';
      });
      chip.addEventListener('click', () => this.addField(field.id, field.numeric ? 'values' : 'rows'));
      wrapper.append(chip);
      if (!field.id.startsWith('D_')) {
        const sourceField = this.state.fields.find((f) => f.id === field.id);
        if (sourceField) {
          const info = document.createElement('button');
          info.type = 'button';
          info.className = 'analysis-field-info-btn';
          info.title = localize('ui.queryEditor.viewFieldStatisticsCommonValues');
          info.setAttribute('aria-label', localize('ui.queryEditor.viewFieldStatistics', { name: field.name }));
          info.innerHTML = icon('info', 'icon icon-small');
          info.onclick = (): void => {
            void this.loadFieldProfile(sourceField);
          };
          wrapper.append(info);
        }
      }
      fragment.append(wrapper);
    }
    list.append(fragment);
  }

  addField(id: string, zone: 'rows' | 'columns' | 'values'): void {
    const q = this.state.query;
    const fields = this.allFields();
    if (!q || !fields.some((f) => f.id === id)) return;
    // Rows and Columns are mutually exclusive; Values may reuse a dimension.
    if (zone === 'rows' || zone === 'columns') {
      q.rows = q.rows.filter((x) => x !== id);
      q.columns = q.columns.filter((x) => x !== id);
    }
    if (zone === 'rows') {
      if (q.rows.length >= 3) {
        this.toast(localize('ui.queryEditor.supportsUpRowDimensions'), 'warning');
        return;
      }
      q.rows.push(id);
    }
    if (zone === 'columns') {
      q.columns = [id];
    }
    if (zone === 'values') {
      if (q.metrics.length) {
        this.addMetric(id);
        return;
      }
      q.value = id;
      if (q.aggregate === 'count' && fields.find((f) => f.id === id)?.numeric) {
        q.aggregate = 'sum';
      }
      q.metrics = [{ field: id, aggregate: q.aggregate, name: '' }];
      this.syncFirstMetric();
    }
    this.renderBuilder();
  }

  renderBuilder(): void {
    const q = this.state.query;
    if (!q) return;
    requireSelect('analysisLayout').value = q.layout;
    for (const zone of ['rows', 'columns', 'values'] as const) {
      const zoneId =
        zone === 'rows'
          ? 'analysisRowsZone'
          : zone === 'columns'
            ? 'analysisColumnsZone'
            : 'analysisValuesZone';
      const el = requireEl(zoneId);
      el.replaceChildren();
      const metrics = q.metrics;
      const ids: readonly string[] =
        zone === 'values'
          ? metrics.length
            ? metrics.map((m) => m.field || '')
            : q.value
              ? [q.value]
              : []
          : q[zone];
      ids.forEach((id, index) => {
        const chip = document.createElement('div');
        chip.className = 'analysis-selected-chip';
        chip.draggable = zone !== 'values';
        const title = document.createElement('span');
        const metricItem = zone === 'values' ? metrics[index] : undefined;
        title.textContent = metricItem ? this.metricCaption(metricItem) : this.fieldName(id);
        const remove = document.createElement('button');
        remove.type = 'button';
        remove.textContent = '×';
        remove.title = localize('ui.queryEditor.removeFields');
        remove.addEventListener('click', () => {
          if (zone === 'values') {
            if (metrics.length) {
              q.metrics.splice(index, 1);
              this.syncFirstMetric();
            } else {
              q.value = '';
            }
            if (!q.metrics.length) {
              q.value = '';
              q.aggregate = 'count';
            }
          } else {
            q[zone] = q[zone].filter((v) => v !== id);
          }
          this.renderBuilder();
        });
        if (zone !== 'values') {
          chip.addEventListener('dragstart', (e: DragEvent) => {
            e.dataTransfer?.setData('text/plain', id);
            if (e.dataTransfer) e.dataTransfer.effectAllowed = 'move';
          });
        }
        chip.append(title, remove);
        el.append(chip);
      });
    }
    const aggSelect = requireSelect('analysisAggregate');
    aggSelect.value = q.aggregate;
    aggSelect.disabled = q.metrics.length > 1;
    requireSelect('analysisSort').value = q.sort || 'value_desc';
    requireInput('analysisTop').value = String(q.top);
    requireInput('analysisPrecision').value = String(q.precision);
    requireSelect('analysisChartSelect').value = q.chart || 'bar';
    this.renderMetricEditor();
    this.renderRules();
    this.renderTransforms();
  }

  renderRules(): void {
    const q = this.state.query;
    if (!q) return;
    const list = requireEl('analysisFilterList');
    list.replaceChildren();
    q.filters.forEach((f, i) => {
      const box = document.createElement('div');
      box.className = 'analysis-rule';
      const h = document.createElement('div');
      h.className = 'analysis-rule-header';
      const select = this.fieldSelect(f.field, localize('ui.common.field'));
      select.onchange = (): void => {
        f.field = select.value;
      };
      h.append(select);
      const remove = document.createElement('button');
      remove.type = 'button';
      remove.className = 'analysis-rule-remove';
      remove.textContent = '×';
      remove.onclick = (): void => {
        q.filters.splice(i, 1);
        this.renderRules();
      };
      h.append(remove);

      const controls = document.createElement('div');
      controls.className = 'analysis-rule-controls';
      const op = document.createElement('select');
      op.className = 'analysis-control';
      for (const [val, label] of FILTER_OPERATIONS) op.append(new Option(label, val));
      op.value = f.op;
      controls.append(op);

      const input = document.createElement('input');
      input.className = 'analysis-control';
      input.value = f.value || '';
      input.onchange = (): void => {
        f.value = input.value;
      };
      input.oninput = (): void => {
        f.value = input.value;
      };
      const syncOp = (): void => {
        f.op = parseFilterOp(op.value);
        input.disabled = f.op === 'blank' || f.op === 'notblank';
        input.placeholder =
          f.op === 'between'
            ? localize('ui.queryEditor.orDateDate')
            : f.op === 'in'
              ? localize('ui.queryEditor.multipleItemsSeparated')
              : localize('ui.queryEditor.matchValue');
      };
      op.onchange = syncOp;
      syncOp();
      controls.append(input);
      box.append(h, controls);
      list.append(box);
    });
  }

  public removeTransform(id: string): void {
    const q = this.state.query;
    if (!q) return;
    const removed = new Set([id]);
    for (const step of q.transforms) {
      if (removed.has(step.source)) removed.add(step.id);
    }
    q.transforms = q.transforms.filter((t) => !removed.has(t.id));
    q.filters = q.filters.filter((f) => !removed.has(f.field));
    q.rows = q.rows.filter((rowId) => !removed.has(rowId));
    q.columns = q.columns.filter((colId) => !removed.has(colId));
    q.metrics = q.metrics.filter((m) => !m.field || !removed.has(m.field));
    if (removed.has(q.value)) {
      q.value = '';
      q.aggregate = 'count';
    }
    this.syncFirstMetric();
    this.renderFields();
    this.renderBuilder();
  }

  renderTransforms(): void {
    const q = this.state.query;
    if (!q) return;
    const list = requireEl('analysisTransformList');
    list.replaceChildren();
    q.transforms.forEach((t, i) => {
      const box = document.createElement('div');
      box.className = 'analysis-rule';
      const row = document.createElement('div');
      row.className = 'analysis-rule-header';
      const name = document.createElement('input');
      name.className = 'analysis-control';
      name.placeholder = localize('ui.queryEditor.newColumnName');
      name.value = t.name;
      name.oninput = (): void => {
        t.name = name.value || localize('ui.queryEditor.newColumn');
        this.renderFields();
      };
      row.append(name);
      const remove = document.createElement('button');
      remove.type = 'button';
      remove.className = 'analysis-rule-remove';
      remove.textContent = '×';
      remove.title = localize('ui.queryEditor.removeCalculationColumn');
      remove.onclick = (): void => this.removeTransform(t.id);
      row.append(remove);

      const temporal = TEMPORAL_TRANSFORM_OPS.has(t.op);
      const operations: (readonly [AnalysisTransformOp, string])[] = [...TRANSFORM_OPERATIONS];
      if (t.op === 'map_text') operations.push(['map_text', localize('ui.queryEditor.textMapping')]);
      const ops = document.createElement('select');
      ops.className = 'analysis-control';
      for (const [val, label] of operations) ops.append(new Option(label, val));
      ops.value = t.op;
      ops.onchange = (): void => {
        t.op = parseTransformOp(ops.value);
        delete t.mapping;
        t.arg = TEMPORAL_TRANSFORM_OPS.has(t.op) ? 'auto' : '';
        this.renderFields();
        this.renderTransforms();
      };

      const sourceChoices = [
        ...this.state.fields.map((f) => ({ id: f.id, name: f.name })),
        ...q.transforms.slice(0, i).map((step) => ({ id: step.id, name: step.name })),
      ];
      const source = this.fieldSelect(t.source, localize('ui.queryEditor.sourceColumn'), sourceChoices);
      source.onchange = (): void => {
        t.source = source.value;
      };

      let argControl: HTMLSelectElement | HTMLInputElement;
      if (temporal) {
        const sel = document.createElement('select');
        sel.className = 'analysis-control';
        sel.setAttribute('aria-label', localize('ui.queryEditor.dateOrTimeInputFormat'));
        const dateFormats: readonly (readonly [string, string])[] = [
          ['auto', localize('ui.queryEditor.yearAtFrontTimeAutomatically')],
          ['compact', localize('ui.queryEditor.dateYearMonthArrangedClosely')],
          ['dmy', localize('ui.queryEditor.dayMonthYear')],
          ['mdy', localize('ui.queryEditor.monthDayYear')],
        ];
        for (const [val, label] of dateFormats) sel.append(new Option(label, val));
        sel.value = t.arg || 'auto';
        sel.onchange = (): void => {
          t.arg = sel.value;
        };
        argControl = sel;
      } else {
        const inp = document.createElement('input');
        inp.className = 'analysis-control';
        inp.setAttribute('aria-label', localize('ui.queryEditor.conversionParameters'));
        inp.placeholder = localize('ui.queryEditor.constantFindReplace');
        inp.value = t.arg || '';
        inp.hidden = ARGLESS_TRANSFORM_OPS.has(t.op);
        inp.onchange = (): void => {
          t.arg = inp.value;
        };
        inp.oninput = (): void => {
          t.arg = inp.value;
        };
        argControl = inp;
      }

      const controls = document.createElement('div');
      controls.className = 'analysis-rule-controls';
      controls.append(source, ops);
      argControl.style.marginTop = '7px';
      box.append(row, controls, argControl);
      if (temporal) {
        const note = document.createElement('small');
        note.className = 'analysis-conversion-note';
        note.textContent = localize('ui.queryEditor.convertDuringAnalysisInvalidOr');
        box.append(note);
      }
      if (t.op === 'map_text') {
        const detail = document.createElement('details');
        detail.className = 'analysis-mapping-details';
        const summary = document.createElement('summary');
        summary.textContent = localize('ui.queryEditor.textMappingViewOriginal', { detail: t.mapping?.length ?? 0 });
        const content = document.createElement('pre');
        detail.append(summary, content);
        detail.addEventListener('toggle', () => {
          if (detail.open) {
            content.textContent = (t.mapping ?? [])
              .map((entry) => `${JSON.stringify(entry.from)} → ${JSON.stringify(entry.to)}`)
              .join('\n');
          }
        });
        box.append(detail);
      }
      list.append(box);
    });
  }
}
