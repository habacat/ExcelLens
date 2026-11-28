/**
 * Pivot table date filter sub-controller supporting relative dates, fixed date
 * comparisons/ranges with optional time precision, and annual quarter/month filters.
 */
import { t as localize } from '../core/i18n.ts';
import type {
  PivotDateFilter as PivotDateFilterSpec,
  PivotInfo,
} from '../contracts/edit.ts';
import { requireButton, requireEl, requireInput, requireSelect } from '../core/dom.ts';

const FIXED_KINDS: Readonly<Record<string, string>> = {
  dateEqual: localize('ui.common.equal'),
  dateNotEqual: localize('ui.common.notEqual'),
  dateOlderThan: localize('ui.editPivotDateFilter.earlierThan'),
  dateOlderThanOrEqual: localize('ui.editPivotDateFilter.earlierThanOrEqual'),
  dateNewerThan: localize('ui.editPivotDateFilter.laterThan'),
  dateNewerThanOrEqual: localize('ui.editPivotDateFilter.laterThanOrEqual'),
  dateBetween: localize('ui.common.between'),
  dateNotBetween: localize('ui.common.neither'),
};

const RELATIVE_KINDS: Readonly<Record<string, string>> = {
  today: localize('ui.editPivotDateFilter.today'),
  yesterday: localize('ui.editPivotDateFilter.yesterday'),
  tomorrow: localize('ui.editPivotDateFilter.tomorrow'),
  thisWeek: localize('ui.editPivotDateFilter.thisWeek'),
  lastWeek: localize('ui.editPivotDateFilter.lastWeek'),
  nextWeek: localize('ui.editPivotDateFilter.nextWeek'),
  thisMonth: localize('ui.editPivotDateFilter.thisMonth'),
  lastMonth: localize('ui.editPivotDateFilter.lastMonth'),
  nextMonth: localize('ui.editPivotDateFilter.nextMonth'),
  thisQuarter: localize('ui.editPivotDateFilter.thisQuarter'),
  lastQuarter: localize('ui.editPivotDateFilter.lastQuarter'),
  nextQuarter: localize('ui.editPivotDateFilter.nextQuarter'),
  thisYear: localize('ui.editPivotDateFilter.thisYear'),
  lastYear: localize('ui.editPivotDateFilter.lastYear'),
  nextYear: localize('ui.editPivotDateFilter.nextYear'),
  yearToDate: localize('ui.editPivotDateFilter.yearDate'),
};

const ANNUAL_KINDS: Record<string, string> = {};
for (let i = 1; i <= 4; i++) ANNUAL_KINDS[`Q${i}`] = localize('ui.editPivotDateFilter.quarter', { i });
for (let i = 1; i <= 12; i++) ANNUAL_KINDS[`M${i}`] = localize('ui.editPivotDateFilter.month', { i });

const ALL_NAMES: Readonly<Record<string, string>> = {
  ...FIXED_KINDS,
  ...RELATIVE_KINDS,
  ...ANNUAL_KINDS,
};

function iso(value: string | undefined, withTime: boolean): string {
  if (!value) return '';
  if (!withTime) return value.slice(0, 10);
  if (value.length === 10) return `${value}T00:00:00`;
  return value.length === 16 ? `${value}:00` : value;
}

function describe(f: PivotDateFilterSpec): string {
  const bounds = [f.start, f.end]
    .filter((v): v is string => Boolean(v))
    .map((v) => v.replace('T', ' '));
  return [f.field, ALL_NAMES[f.kind], bounds.join(` ${localize('ui.editPivotDateFilter.until')} `)].filter(Boolean).join(' ');
}

function requireDetails(id: string): HTMLDetailsElement {
  const node = requireEl(id);
  if (!(node instanceof HTMLDetailsElement)) {
    throw new Error(`Expected HTMLDetailsElement for #${id}`);
  }
  return node;
}

export type PivotDateFilterApplyFn = (spec: PivotDateFilterSpec) => void;

export class PivotDateFilter {
  private readonly panel: HTMLDetailsElement;
  private readonly field: HTMLSelectElement;
  private readonly kind: HTMLSelectElement;
  private readonly start: HTMLInputElement;
  private readonly end: HTMLInputElement;
  private readonly time: HTMLInputElement;
  private info: PivotInfo | null = null;
  private binding = '';
  private storedTime = false;
  private editing = false;
  private available = false;
  private blocked = false;
  private valid = false;

  constructor(apply: PivotDateFilterApplyFn) {
    this.panel = requireDetails('editCopyPivotDateFilters');
    this.field = requireSelect('editCopyPivotDateFilterField');
    this.kind = requireSelect('editCopyPivotDateFilterKind');
    this.start = requireInput('editCopyPivotDateFilterStart');
    this.end = requireInput('editCopyPivotDateFilterEnd');
    this.time = requireInput('editCopyPivotDateFilterTime');
    const groups: readonly (readonly [string, Readonly<Record<string, string>>])[] = [
      [localize('ui.editPivotDateFilter.relativeDate'), RELATIVE_KINDS],
      [localize('ui.editPivotDateFilter.specifyDate'), FIXED_KINDS],
      [localize('ui.editPivotDateFilter.specifiedTimePeriodEachYear'), ANNUAL_KINDS],
    ];
    for (const [name, items] of groups) {
      const group = document.createElement('optgroup');
      group.label = name;
      group.append(...Object.entries(items).map(([key, text]) => new Option(text, key)));
      this.kind.append(group);
    }
    this.field.onchange = (): void => this.load();
    this.kind.onchange = (): void => this.controls();
    this.time.onchange = (): void => {
      this.storedTime = false;
      this.precision();
      this.controls();
    };
    this.start.oninput = (): void => this.controls();
    this.end.oninput = (): void => this.controls();
    const applyBtn = requireButton('editCopyPivotDateFilterApply');
    applyBtn.onclick = (): void => {
      if (!applyBtn.disabled) apply(this.read());
    };
    requireButton('editCopyPivotDateFilterClear').onclick = (): void => {
      if (this.current) apply({ field: this.field.value, kind: 'clear' });
    };
    this.panel.addEventListener('toggle', () => this.activate(this.panel.open));
    requireEl('editCopyPivotFields').addEventListener('focusin', (e: FocusEvent) => {
      const target = e.target;
      this.activate(this.panel.open && target instanceof Node && this.panel.contains(target));
    });
  }

  bind(info: PivotInfo | null, fields: readonly string[]): void {
    const binding = JSON.stringify([info?.part, fields, info?.dateFilters]);
    if (binding === this.binding) return;
    this.binding = binding;
    const same = this.info?.part === info?.part;
    const previous = same ? this.field.value : '';
    this.info = info;
    const box = this.panel;
    box.hidden = !fields.length;
    const active = (info?.dateFilters ?? []).filter((f) => fields.includes(f.field));
    this.field.replaceChildren(
      ...fields.map(
        (name) => new Option(active.some((f) => f.field === name) ? localize('ui.editPivotDateFilter.filtered', { name }) : name, name),
      ),
    );
    this.field.value = fields.includes(previous)
      ? previous
      : (active[0]?.field ?? fields[0] ?? '');
    const summary = box.querySelector('summary');
    const firstActive = active[0];
    if (summary) {
      summary.textContent =
        active.length === 1 && firstActive
          ? localize('ui.editPivotDateFilter.dateFilteringf3131b8', { describe: describe(firstActive) })
          : active.length
            ? localize('ui.editPivotDateFilter.dateFilterItem', { count: active.length })
            : localize('ui.editPivotDateFilter.dateFiltering');
    }
    this.load();
  }

  get current(): PivotDateFilterSpec | undefined {
    return this.info?.dateFilters?.find((f) => f.field === this.field.value);
  }

  activate(editing: boolean): void {
    this.editing = editing;
    this.footer(this.available, this.blocked);
  }

  footer(available: boolean, blocked: boolean): void {
    this.available = available;
    this.blocked = blocked;
    const editing = available && !this.panel.hidden && this.panel.open && this.editing;
    const applyBtn = requireButton('editCopyPivotDateFilterApply');
    applyBtn.hidden = !editing;
    applyBtn.disabled = blocked || !this.valid;
    requireButton('editCopyPivotApply').hidden = !available || editing;
  }

  load(): void {
    const filter = this.current;
    this.kind.value = filter?.kind || 'thisMonth';
    this.time.checked = Boolean(filter?.start && !filter.wholeDay);
    this.storedTime =
      Boolean(filter?.wholeDay) && [filter?.start, filter?.end].some((v) => v?.includes('T'));
    this.precision();
    this.start.value = iso(filter?.start, this.withTime);
    this.end.value = iso(filter?.end, this.withTime);
    this.controls();
  }

  get withTime(): boolean {
    return this.time.checked || this.storedTime;
  }

  private precision(): void {
    for (const input of [this.start, this.end]) {
      const text = input.value;
      input.type = this.withTime ? 'datetime-local' : 'date';
      input.step = '1';
      input.value = iso(text, this.withTime);
    }
  }

  read(): PivotDateFilterSpec {
    const kind = this.kind.value;
    if (FIXED_KINDS[kind]) {
      return {
        field: this.field.value,
        kind,
        start: iso(this.start.value, this.withTime),
        ...(!this.time.checked ? { wholeDay: true } : {}),
        ...(kind.endsWith('Between') ? { end: iso(this.end.value, this.withTime) } : {}),
      };
    }
    return { field: this.field.value, kind };
  }

  controls(): void {
    const spec = this.read();
    const bounded = Boolean(FIXED_KINDS[spec.kind]);
    const range = spec.end !== undefined;
    requireEl('editCopyPivotDateFilterBounds').hidden = !bounded;
    if (this.end.parentElement) this.end.parentElement.hidden = !range;
    requireEl('editCopyPivotDateFilterStartLabel').textContent = range ? localize('ui.editPivotDateFilter.startDate') : localize('ui.common.date');
    const reversed = range && Boolean(spec.start) && Boolean(spec.end) && (spec.end ?? '') < (spec.start ?? '');
    const first = iso('0001-01-01', this.withTime);
    const last = this.withTime ? '9999-12-31T23:59:59' : '9999-12-31';
    this.start.min = first;
    this.end.max = last;
    this.end.min = range && this.start.value ? this.start.value : first;
    this.start.max = range && this.end.value ? this.end.value : last;
    const outOfBounds = (input: HTMLInputElement): boolean =>
      input.validity.rangeUnderflow || input.validity.rangeOverflow;
    const outside = outOfBounds(this.start) || (range && outOfBounds(this.end));
    requireEl('editCopyPivotDateFilterError').textContent = !bounded
      ? ''
      : reversed
        ? localize('ui.editPivotDateFilter.endDateCannotBeEarlier')
        : outside
          ? localize('ui.editPivotDateFilter.enterDateBetweenYears')
          : '';
    this.valid =
      Boolean(spec.field) &&
      (!bounded ||
        (Boolean(spec.start) &&
          this.start.validity.valid &&
          (!range || (Boolean(spec.end) && this.end.validity.valid))));
    this.footer(this.available, this.blocked);
    requireButton('editCopyPivotDateFilterClear').hidden = !this.current;
  }
}
