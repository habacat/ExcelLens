/**
 * Pivot table label filter, date grouping, and embedded date filter coordinator.
 */
import type {
  PivotDateFilter as PivotDateFilterSpec,
  PivotDateGroup,
  PivotInfo,
  PivotLabelFilter,
} from '../contracts/edit.ts';
import { requireButton, requireEl, requireInput, requireSelect } from '../core/dom.ts';
import { PivotDateFilter } from './edit-pivot-date-filter.ts';

const LABEL_CRITERIA: Readonly<Record<string, string>> = {
  captionEqual: '等于',
  captionNotEqual: '不等于',
  captionBeginsWith: '开头是',
  captionNotBeginsWith: '开头不是',
  captionEndsWith: '结尾是',
  captionNotEndsWith: '结尾不是',
  captionContains: '包含',
  captionNotContains: '不包含',
  captionGreaterThan: '大于',
  captionGreaterThanOrEqual: '大于或等于',
  captionLessThan: '小于',
  captionLessThanOrEqual: '小于或等于',
  captionBetween: '介于',
  captionNotBetween: '不介于',
  clear: '清除筛选',
};

function options(names: readonly string[]): HTMLOptionElement[] {
  return names.map((n) => new Option(n, n));
}

function requireDetails(id: string): HTMLDetailsElement {
  const node = requireEl(id);
  if (!(node instanceof HTMLDetailsElement)) {
    throw new Error(`Expected HTMLDetailsElement for #${id}`);
  }
  return node;
}

export type PivotCriteriaChange =
  | { readonly kind: 'pivotLabelFilter'; readonly labelFilter: PivotLabelFilter }
  | { readonly kind: 'pivotDateGroup'; readonly dateGroup: PivotDateGroup }
  | { readonly kind: 'pivotDateFilter'; readonly dateFilter: PivotDateFilterSpec };

export type PivotCriteriaCommitFn = (change: PivotCriteriaChange) => void;

export class PivotCriteria {
  private readonly commit: PivotCriteriaCommitFn;
  public readonly dateFilter: PivotDateFilter;
  private info: PivotInfo | null = null;

  constructor(commit: PivotCriteriaCommitFn) {
    this.commit = commit;
    this.dateFilter = new PivotDateFilter((spec) =>
      commit({ kind: 'pivotDateFilter', dateFilter: spec }),
    );
    requireSelect('editCopyPivotLabelKind').replaceChildren(
      ...Object.entries(LABEL_CRITERIA).map(([k, v]) => new Option(v, k)),
    );
    requireSelect('editCopyPivotLabelField').onchange = (): void => this.label();
    const updateControls = (): void => this.controls();
    requireSelect('editCopyPivotLabelKind').onchange = updateControls;
    requireInput('editCopyPivotLabelStart').oninput = updateControls;
    requireInput('editCopyPivotLabelEnd').oninput = updateControls;
    requireSelect('editCopyPivotDateField').onchange = (): void => this.date();
    requireEl('editCopyPivotDateUnits').onchange = updateControls;
    requireInput('editCopyPivotInterval').oninput = updateControls;
    requireInput('editCopyPivotDateStart').oninput = updateControls;
    requireInput('editCopyPivotDateEnd').oninput = updateControls;
    requireButton('editCopyPivotLabelApply').onclick = (): void =>
      this.commit({ kind: 'pivotLabelFilter', labelFilter: this.labelSpec() });
    requireButton('editCopyPivotDateApply').onclick = (): void =>
      this.commit({ kind: 'pivotDateGroup', dateGroup: this.dateSpec() });
  }

  bind(info: PivotInfo | null): void {
    this.info = info;
    const axis = [...(info?.axes?.rows ?? []), ...(info?.axes?.columns ?? [])];
    requireEl('editCopyPivotLabels').hidden = !axis.length;
    requireSelect('editCopyPivotLabelField').replaceChildren(...options(axis));
    const dates = [
      ...new Set(
        axis.flatMap((field) => {
          const base = info?.groupFields?.[field]?.source;
          return base ? [base] : info?.dateFields?.includes(field) ? [field] : [];
        }),
      ),
    ];
    requireEl('editCopyPivotDates').hidden = !dates.length;
    requireSelect('editCopyPivotDateField').replaceChildren(...options(dates));
    this.dateFilter.bind(info, dates);
    this.label();
    this.date();
  }

  label(): void {
    const field = requireSelect('editCopyPivotLabelField').value;
    const s = this.info?.labelFilters?.find((f) => f.field === field);
    requireSelect('editCopyPivotLabelKind').value = s?.kind || 'captionContains';
    requireInput('editCopyPivotLabelStart').value = s?.value || '';
    requireInput('editCopyPivotLabelEnd').value = s?.second || '';
    this.controls();
  }

  labelSpec(): PivotLabelFilter {
    const field = requireSelect('editCopyPivotLabelField').value;
    const kind = requireSelect('editCopyPivotLabelKind').value;
    if (kind === 'clear') return { field, kind };
    const value = requireInput('editCopyPivotLabelStart').value;
    return kind.endsWith('Between')
      ? { field, kind, value, second: requireInput('editCopyPivotLabelEnd').value }
      : { field, kind, value };
  }

  date(): void {
    const field = requireSelect('editCopyPivotDateField').value;
    const s = this.info?.dateGroups?.find((g) => g.field === field);
    const activeUnits = s?.units ?? ['years', 'months'];
    for (const input of requireEl('editCopyPivotDateUnits').querySelectorAll<HTMLInputElement>(
      'input',
    )) {
      input.checked = activeUnits.includes(input.value);
    }
    requireInput('editCopyPivotDateStart').value = s?.start || '';
    requireInput('editCopyPivotDateEnd').value = s?.end || '';
    requireDetails('editCopyPivotBounds').open = Boolean(s?.start || s?.end);
    requireInput('editCopyPivotInterval').value = String(s?.interval || 1);
    this.controls();
  }

  dateSpec(): PivotDateGroup {
    const field = requireSelect('editCopyPivotDateField').value;
    const units = [
      ...requireEl('editCopyPivotDateUnits').querySelectorAll<HTMLInputElement>('input:checked'),
    ].map((n) => n.value);
    const interval =
      units.length === 1 && units[0] === 'days'
        ? requireInput('editCopyPivotInterval').valueAsNumber
        : undefined;
    const normalizeBound = (id: string): string | undefined => {
      const value = requireInput(id).value;
      if (!value) return undefined;
      return value.length === 16 ? `${value}:00` : value;
    };
    const start = units.length ? normalizeBound('editCopyPivotDateStart') : undefined;
    const end = units.length ? normalizeBound('editCopyPivotDateEnd') : undefined;
    return {
      field,
      units,
      ...(interval !== undefined ? { interval } : {}),
      ...(start !== undefined ? { start } : {}),
      ...(end !== undefined ? { end } : {}),
    };
  }

  controls(): void {
    const label = this.labelSpec();
    const date = this.dateSpec();
    const labelStartParent = requireInput('editCopyPivotLabelStart').parentElement;
    if (labelStartParent) labelStartParent.hidden = label.kind === 'clear';
    const labelEndParent = requireInput('editCopyPivotLabelEnd').parentElement;
    if (labelEndParent) labelEndParent.hidden = !label.kind.endsWith('Between');
    requireButton('editCopyPivotLabelApply').disabled =
      !label.field ||
      (label.kind === 'clear'
        ? !this.info?.labelFilters?.some((f) => f.field === label.field)
        : !label.value || label.second === '');
    const intervalParent = requireInput('editCopyPivotInterval').parentElement;
    if (intervalParent) intervalParent.hidden = date.interval === undefined;
    const bounds = requireDetails('editCopyPivotBounds');
    bounds.hidden = !date.units.length;
    const summary = bounds.querySelector('summary');
    if (summary) {
      summary.textContent =
        date.start || date.end ? '起止时间（自定义）' : '起止时间（自动）';
    }
    const dateStartInput = requireInput('editCopyPivotDateStart');
    const dateEndInput = requireInput('editCopyPivotDateEnd');
    dateStartInput.max = date.end || '';
    dateEndInput.min = date.start || '';
    const invalidBounds =
      Boolean(date.units.length) && (!dateStartInput.validity.valid || !dateEndInput.validity.valid);
    const dateApply = requireButton('editCopyPivotDateApply');
    dateApply.textContent = date.units.length ? '应用分组' : '取消分组';
    dateApply.disabled =
      !date.field ||
      invalidBounds ||
      (!date.units.length && !this.info?.dateGroups?.some((g) => g.field === date.field)) ||
      (date.interval !== undefined &&
        !(Number.isInteger(date.interval) && date.interval >= 1 && date.interval <= 32767));
  }
}
