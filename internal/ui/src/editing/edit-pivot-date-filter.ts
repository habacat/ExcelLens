/**
 * Pivot table date filter sub-controller supporting relative dates, fixed date
 * comparisons/ranges with optional time precision, and annual quarter/month filters.
 */
import type {
  PivotDateFilter as PivotDateFilterSpec,
  PivotInfo,
} from '../contracts/edit.ts';
import { requireButton, requireEl, requireInput, requireSelect } from '../core/dom.ts';

const FIXED_KINDS: Readonly<Record<string, string>> = {
  dateEqual: '等于',
  dateNotEqual: '不等于',
  dateOlderThan: '早于',
  dateOlderThanOrEqual: '早于或等于',
  dateNewerThan: '晚于',
  dateNewerThanOrEqual: '晚于或等于',
  dateBetween: '介于',
  dateNotBetween: '不介于',
};

const RELATIVE_KINDS: Readonly<Record<string, string>> = {
  today: '今天',
  yesterday: '昨天',
  tomorrow: '明天',
  thisWeek: '本周',
  lastWeek: '上周',
  nextWeek: '下周',
  thisMonth: '本月',
  lastMonth: '上月',
  nextMonth: '下月',
  thisQuarter: '本季度',
  lastQuarter: '上季度',
  nextQuarter: '下季度',
  thisYear: '今年',
  lastYear: '去年',
  nextYear: '明年',
  yearToDate: '年初至今',
};

const ANNUAL_KINDS: Record<string, string> = {};
for (let i = 1; i <= 4; i++) ANNUAL_KINDS[`Q${i}`] = `第${i}季度`;
for (let i = 1; i <= 12; i++) ANNUAL_KINDS[`M${i}`] = `${i}月`;

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
  return [f.field, ALL_NAMES[f.kind], bounds.join(' 至 ')].filter(Boolean).join(' ');
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
      ['相对日期', RELATIVE_KINDS],
      ['指定日期', FIXED_KINDS],
      ['每年的指定时段', ANNUAL_KINDS],
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
        (name) => new Option(active.some((f) => f.field === name) ? `${name} · 已筛选` : name, name),
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
          ? `日期筛选 · ${describe(firstActive)}`
          : active.length
            ? `日期筛选 · ${active.length} 项`
            : '日期筛选';
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
    requireEl('editCopyPivotDateFilterStartLabel').textContent = range ? '开始日期' : '日期';
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
        ? '结束日期不能早于开始日期'
        : outside
          ? '请输入 1 至 9999 年之间的日期'
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
