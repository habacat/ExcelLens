/**
 * Non-modal Excel-style column AutoFilter checklist dropdown UI.
 */
import type { ApiClient, ToastFn } from '../contracts/common.ts';
import type { AutoFilterState, DistinctValue, FilterDistinctResponse } from '../contracts/workbook.ts';
import { errorMessage, isComposing } from '../core/display-format.ts';
import { queryInput, requireButton, requireEl, requireInput } from '../core/dom.ts';
import type { WorkbookState } from './state.ts';

export interface AutoFilterApplyOptions {
  readonly preserveRules?: boolean;
  readonly recoverable?: boolean;
}

export interface AutoFilterDropdownOptions {
  readonly state: WorkbookState;
  readonly api: ApiClient;
  readonly colName: (col: number) => string;
  readonly toast: ToastFn;
  readonly onApply: (options: AutoFilterApplyOptions) => Promise<void>;
}

export class AutoFilterDropdown {
  private readonly s: WorkbookState;
  private readonly api: ApiClient;
  private readonly colName: (col: number) => string;
  private readonly toast: ToastFn;
  private readonly onApply: (options: AutoFilterApplyOptions) => Promise<void>;
  private readonly panel: HTMLElement;
  private records: DistinctValue[] = [];
  private selected = new Set<string>();
  private initialSelected: Set<string> | null = null;
  private sheet = '';
  private column = 0;
  private configAtOpen: AutoFilterState | null = null;
  private displayLimit = 180;
  private truncated = false;
  private pending: AbortController | null = null;
  private request = 0;

  constructor(options: AutoFilterDropdownOptions) {
    this.s = options.state;
    this.api = options.api;
    this.colName = options.colName;
    this.toast = options.toast;
    this.onApply = options.onApply;
    this.panel = requireEl('autoFilterPopover');
    this.bind();
  }

  get config(): AutoFilterState | null {
    return this.s.autoFilter;
  }

  isHeader(row: number, col: number): boolean {
    const c = this.config;
    if (!c) return false;
    return c.row === row && col >= c.startCol && col <= c.endCol;
  }

  hasFilter(col: number): boolean {
    return this.config?.rules.has(col) ?? false;
  }

  private bind(): void {
    document.addEventListener(
      'click',
      (e: MouseEvent) => {
        const target = e.target;
        const arrow = target instanceof Element ? target.closest('.auto-filter-button') : null;
        if (arrow instanceof HTMLElement) {
          e.preventDefault();
          e.stopPropagation();
          const col = Number(arrow.dataset.column);
          if (this.panel.hidden || this.column !== col || this.sheet !== this.s.sheet) {
            void this.open(col, arrow);
          } else {
            this.hide();
          }
          return;
        }
        if (
          !this.panel.hidden &&
          !(target instanceof Element && target.closest('#autoFilterPopover'))
        ) {
          this.hide();
        }
      },
      true,
    );
    requireButton('autoFilterClose').addEventListener('click', () => this.hide());
    requireButton('autoFilterCancel').addEventListener('click', () => this.hide());
    requireButton('autoFilterApply').addEventListener('click', () => {
      void this.apply();
    });
    requireButton('autoFilterClear').addEventListener('click', () => {
      void this.clearColumn();
    });
    requireButton('autoFilterSortAsc').addEventListener('click', () => {
      void this.sort(false);
    });
    requireButton('autoFilterSortDesc').addEventListener('click', () => {
      void this.sort(true);
    });
    requireInput('autoFilterFind').addEventListener('input', () => {
      const q = this.needle();
      if (q) {
        this.selected = new Set(
          this.records
            .filter((v) => v.value.toLocaleLowerCase().includes(q))
            .map((v) => v.value),
        );
      }
      this.renderOptions();
    });
    requireInput('autoFilterAll').addEventListener('change', (e: Event) => {
      const target = e.target;
      if (!(target instanceof HTMLInputElement)) return;
      if (!target.checked && !this.needle()) this.selected.clear();
      for (const item of this.filteredRecords()) {
        if (target.checked) this.selected.add(item.value);
        else this.selected.delete(item.value);
      }
      this.renderOptions();
    });
    requireButton('autoFilterMore').addEventListener('click', () => {
      this.displayLimit = Math.min(this.records.length, this.displayLimit + 200);
      this.renderOptions();
    });
    document.addEventListener(
      'keydown',
      (e: KeyboardEvent) => {
        if (this.panel.hidden || isComposing(e)) return;
        if (e.key === 'Escape') {
          e.preventDefault();
          this.hide();
        }
        if (e.key === 'Enter' && this.panel.contains(document.activeElement)) {
          e.preventDefault();
          void this.apply();
        }
      },
      true,
    );
    window.addEventListener('resize', () => this.hide());
    requireEl('grid').addEventListener('scroll', () => this.hide(), { passive: true });
  }

  hide(): void {
    if (this.pending) {
      this.pending.abort();
      this.pending = null;
    }
    this.panel.hidden = true;
    this.column = 0;
    this.configAtOpen = null;
    this.records = [];
    ++this.request;
  }

  private needle(): string {
    return requireInput('autoFilterFind').value.trim().toLocaleLowerCase();
  }

  private filteredRecords(): DistinctValue[] {
    const q = this.needle();
    return q ? this.records.filter((v) => v.value.toLocaleLowerCase().includes(q)) : this.records;
  }

  private position(anchor: Element): void {
    const r = anchor.getBoundingClientRect();
    const width = 300;
    const height = 430;
    this.panel.style.left = `${Math.max(6, Math.min(window.innerWidth - width - 8, r.left))}px`;
    this.panel.style.top = `${Math.max(6, Math.min(window.innerHeight - height - 8, r.bottom + 4))}px`;
  }

  async open(col: number, anchor: Element): Promise<void> {
    const c = this.config;
    if (!this.s.indexReady || !c || !this.s.sheet || !this.isHeader(c.row, col)) return;
    this.hide();
    this.column = col;
    this.sheet = this.s.sheet;
    this.configAtOpen = c;
    this.displayLimit = 180;
    const ticket = ++this.request;
    this.panel.hidden = false;
    this.position(anchor);
    requireEl('autoFilterTitle').textContent =
      c.table && c.names?.[col - 1] ? (c.names[col - 1] ?? '') : `${this.colName(col)} 列筛选`;
    requireInput('autoFilterFind').value = '';
    requireEl('autoFilterOptions').replaceChildren();
    requireEl('autoFilterSummary').textContent = '正在读取本列选项…';
    requireButton('autoFilterApply').disabled = true;
    requireButton('autoFilterMore').hidden = true;
    const abort = new AbortController();
    this.pending = abort;
    try {
      const query = new URLSearchParams({
        sheet: this.sheet,
        column: String(col),
        headerRow: String(c.row),
      });
      const response = await this.api<FilterDistinctResponse>(
        `/api/filter/distinct?${query.toString()}`,
        { signal: abort.signal },
      );
      if (
        ticket !== this.request ||
        abort.signal.aborted ||
        this.sheet !== this.s.sheet ||
        c !== this.config
      ) {
        return;
      }
      this.pending = null;
      this.records = [...(response.values ?? [])].sort((a, b) =>
        a.value.localeCompare(b.value, 'zh-CN', { numeric: true }),
      );
      this.truncated = response.truncated;
      const active = c.rules.get(col);
      this.selected =
        active?.operator === 'in'
          ? new Set(active.values ?? [])
          : new Set(this.records.map((v) => v.value));
      this.initialSelected = new Set(this.selected);
      this.renderOptions();
      requireButton('autoFilterApply').disabled = false;
      requireInput('autoFilterFind').focus({ preventScroll: true });
    } catch (err: unknown) {
      if (abort.signal.aborted) return;
      this.pending = null;
      requireEl('autoFilterSummary').textContent = `读取失败：${errorMessage(err)}`;
      requireButton('autoFilterApply').disabled = true;
    }
  }

  private renderOptions(): void {
    const visible = this.filteredRecords();
    const holder = requireEl('autoFilterOptions');
    const frag = document.createDocumentFragment();
    for (const item of visible.slice(0, this.displayLimit)) {
      const line = document.createElement('label');
      line.className = 'auto-filter-item';
      const check = document.createElement('input');
      check.type = 'checkbox';
      check.checked = this.selected.has(item.value);
      check.addEventListener('change', () => {
        if (check.checked) this.selected.add(item.value);
        else this.selected.delete(item.value);
        this.updateAllCheckbox();
      });
      const name = document.createElement('span');
      name.className = 'auto-filter-value';
      name.textContent = item.value === '' ? '（空白）' : item.value;
      name.title = name.textContent;
      const count = document.createElement('span');
      count.className = 'auto-filter-count';
      count.textContent = String(item.count);
      line.append(check, name, count);
      frag.append(line);
    }
    holder.replaceChildren(frag);
    const more = requireButton('autoFilterMore');
    more.hidden = visible.length <= this.displayLimit;
    more.textContent = `显示更多（剩余 ${Math.max(0, visible.length - this.displayLimit)} 项）`;
    requireEl('autoFilterSummary').textContent = this.truncated
      ? '选项未全部列出；请使用条件筛选缩小范围'
      : `${visible.length.toLocaleString()} 个选项 · ${this.selected.size.toLocaleString()} 已勾选`;
    this.updateAllCheckbox();
  }

  private updateAllCheckbox(): void {
    const rows = this.filteredRecords();
    const count = rows.filter((v) => this.selected.has(v.value)).length;
    const all = requireInput('autoFilterAll');
    all.checked = rows.length > 0 && count === rows.length;
    all.indeterminate = count > 0 && count < rows.length;
  }

  async apply(): Promise<void> {
    if (!this.config || this.config !== this.configAtOpen || this.sheet !== this.s.sheet) return;
    const col = this.column;
    const unchanged =
      this.initialSelected !== null &&
      this.selected.size === this.initialSelected.size &&
      [...this.selected].every((value) => this.initialSelected?.has(value));
    if (unchanged) {
      this.hide();
      return;
    }
    if (!this.selected.size) {
      this.toast('至少勾选一个值；要清除条件，请点击「清除此列」', 'warning');
      return;
    }
    if (this.selected.size > 2500) {
      this.toast('勾选值超过2500项，请先清除此列或缩小选择范围', 'warning');
      return;
    }
    const full =
      !this.truncated &&
      this.selected.size === this.records.length &&
      this.records.every((item) => this.selected.has(item.value));
    if (full) {
      this.config.rules.delete(col);
    } else {
      this.config.rules.set(col, { column: col, operator: 'in', values: [...this.selected] });
    }
    this.hide();
    await this.onApply({ recoverable: Boolean(queryInput('autoFilterRecoverable')?.checked) });
  }

  async clearColumn(): Promise<void> {
    if (!this.config || this.config !== this.configAtOpen || this.sheet !== this.s.sheet) return;
    this.config.rules.delete(this.column);
    this.hide();
    await this.onApply({ recoverable: Boolean(queryInput('autoFilterRecoverable')?.checked) });
  }

  async sort(descending: boolean): Promise<void> {
    if (!this.config || this.config !== this.configAtOpen || this.sheet !== this.s.sheet) return;
    if (this.s.sheet) {
      this.s.sortKeysBySheet.delete(this.s.sheet);
      this.s.sortHeaderBySheet.delete(this.s.sheet);
    }
    this.config.sortColumn = this.column;
    this.config.descending = descending;
    this.hide();
    await this.onApply({
      preserveRules: true,
      recoverable: Boolean(queryInput('autoFilterRecoverable')?.checked),
    });
  }
}
