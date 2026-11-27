/**
 * Pivot table value-filter sub-controller (top/bottom N, percent, sum, and
 * numeric comparison/between filters bound to a selected measure).
 */
import type { PivotInfo, PivotValueFilter } from '../contracts/edit.ts';
import { requireButton, requireEl, requireInput, requireSelect } from '../core/dom.ts';

const MODES: readonly (readonly [string, string])[] = [
  ['topCount', '前 N 项'],
  ['bottomCount', '后 N 项'],
  ['topPercent', '累计占比最高'],
  ['bottomPercent', '累计占比最低'],
  ['topSum', '累计值最高'],
  ['bottomSum', '累计值最低'],
  ['valueGreaterThan', '大于'],
  ['valueGreaterThanOrEqual', '大于或等于'],
  ['valueLessThan', '小于'],
  ['valueLessThanOrEqual', '小于或等于'],
  ['valueEqual', '等于'],
  ['valueNotEqual', '不等于'],
  ['valueBetween', '介于'],
  ['valueNotBetween', '不介于'],
  ['clear', '清除筛选'],
];

export type PivotValueFilterApplyFn = (filter: PivotValueFilter) => void;

export class PivotValues {
  private readonly measure: HTMLSelectElement;
  private readonly field: HTMLSelectElement;
  private readonly kind: HTMLSelectElement;
  private readonly start: HTMLInputElement;
  private readonly end: HTMLInputElement;
  private readonly apply: HTMLButtonElement;
  private info: PivotInfo | null = null;

  constructor(apply: PivotValueFilterApplyFn) {
    this.measure = requireSelect('editCopyValueMeasure');
    this.field = requireSelect('editCopyValueField');
    this.kind = requireSelect('editCopyValueKind');
    this.start = requireInput('editCopyValueStart');
    this.end = requireInput('editCopyValueEnd');
    this.apply = requireButton('editCopyValueApply');
    this.kind.replaceChildren(...MODES.map(([value, label]) => new Option(label, value)));
    this.field.addEventListener('change', () => this.select());
    this.kind.addEventListener('change', () => this.controls());
    for (const input of [this.start, this.end]) {
      input.addEventListener('input', () => this.controls());
    }
    this.apply.addEventListener('click', () => {
      const filter = this.read();
      if (filter) apply(filter);
    });
  }

  bind(info: PivotInfo | null): void {
    if (this.info === info) return;
    this.info = info;
    const fields = [...(info?.axes?.rows ?? []), ...(info?.axes?.columns ?? [])];
    const measures = info?.measures ?? [];
    requireEl('editCopyValueMeasureField').hidden = measures.length < 2;
    this.measure.replaceChildren(
      ...measures.map((m, i) => new Option(m.caption || m.field, String(i))),
    );
    const previous = this.field.value;
    this.field.replaceChildren(...fields.map((value) => new Option(value, value)));
    if (fields.includes(previous)) this.field.value = previous;
    requireEl('editCopyPivotValues').hidden = !fields.length;
    this.select();
  }

  select(): void {
    const filter = this.info?.valueFilters?.find((item) => item.field === this.field.value);
    this.measure.value = String(filter?.measure || 0);
    this.kind.value = filter?.kind || 'topCount';
    this.start.value = String(filter?.value ?? 10);
    this.end.value = filter?.second !== undefined ? String(filter.second) : '';
    this.controls();
  }

  read(): PivotValueFilter | null {
    const field = this.field.value;
    const kind = this.kind.value;
    if (!field || !kind) return null;
    if (kind === 'clear') {
      return this.info?.valueFilters?.some((f) => f.field === field)
        ? { field, kind, value: 0 }
        : null;
    }
    const value = this.start.valueAsNumber;
    if (!Number.isFinite(value)) return null;
    if (kind.endsWith('Count') && (!Number.isInteger(value) || value < 1)) return null;
    if (kind.endsWith('Percent') && (value <= 0 || value > 100)) return null;
    if (kind.endsWith('Sum') && value <= 0) return null;
    const measure = this.measure.selectedIndex > 0 ? this.measure.selectedIndex : undefined;
    if (kind.endsWith('Between')) {
      const second = this.end.valueAsNumber;
      if (!Number.isFinite(second) || second < value) return null;
      return {
        field,
        kind,
        value,
        second,
        ...(measure !== undefined ? { measure } : {}),
      };
    }
    return {
      field,
      kind,
      value,
      ...(measure !== undefined ? { measure } : {}),
    };
  }

  controls(): void {
    const kind = this.kind.value;
    this.start.hidden = kind === 'clear';
    this.end.hidden = !kind.endsWith('Between');
    requireEl('editCopyPivotThresholdLabel').textContent = kind.endsWith('Count')
      ? '数量'
      : kind.endsWith('Percent')
        ? '占比（%）'
        : kind.endsWith('Between')
          ? '下限'
          : '数值';
    this.start.step = kind.endsWith('Count') ? '1' : 'any';
    this.apply.disabled = !this.read();
  }
}
