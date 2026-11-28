/**
 * Multi-level sort UI: collects at most five keys and delegates scanning plus
 * stable sorting to Go. Never loads a whole sheet into browser memory.
 */
import { t as localize } from '../core/i18n.ts';
import type { ToastFn } from '../contracts/common.ts';
import type { SourceSortKey } from '../contracts/workbook.ts';
import { errorMessage } from '../core/display-format.ts';
import { requireButton, requireDialog, requireEl, requireInput } from '../core/dom.ts';
import type { WorkbookState } from '../grid/state.ts';

export interface MultiSortValues {
  readonly keys: SourceSortKey[];
  readonly header: number;
}

export interface MultiSortOptions {
  readonly state: WorkbookState;
  readonly parseColumn: (value: string | number | null | undefined) => number;
  readonly colName: (col: number) => string;
  readonly toast: ToastFn;
  readonly openDialog: (id: string) => void;
  readonly submit: (keys: SourceSortKey[], header: number) => Promise<void> | void;
  readonly clear: () => Promise<void> | void;
}

export class MultiSort {
  private readonly s: WorkbookState;
  private readonly parseColumn: (value: string | number | null | undefined) => number;
  private readonly colName: (col: number) => string;
  private readonly toast: ToastFn;
  private readonly openDialog: (id: string) => void;
  private readonly submit: (keys: SourceSortKey[], header: number) => Promise<void> | void;
  private readonly clear: () => Promise<void> | void;
  private readonly holder: HTMLElement;

  constructor(options: MultiSortOptions) {
    this.s = options.state;
    this.parseColumn = options.parseColumn;
    this.colName = options.colName;
    this.toast = options.toast;
    this.openDialog = options.openDialog;
    this.submit = options.submit;
    this.clear = options.clear;
    this.holder = requireEl('sortLevels');

    requireButton('sortAddLevel').addEventListener('click', () => this.add());
    requireButton('sortApply').addEventListener('click', () => {
      void this.apply();
    });
    requireButton('sortReset').addEventListener('click', () => {
      void this.reset();
    });
  }

  show(): void {
    if (!this.s.sheet || !this.s.indexReady) return;
    const header = this.s.autoFilter?.row ?? Math.min(1, this.s.usedMaxRow);
    requireInput('sortHeaderRow').value = String(
      this.s.sortHeaderBySheet.get(this.s.sheet) ?? header,
    );
    this.holder.replaceChildren();
    const keys = this.s.sortKeysBySheet.get(this.s.sheet) ?? [
      { column: this.s.selected?.col || 1, descending: false },
    ];
    for (const k of keys) this.add(k);
    this.openDialog('multiSortDialog');
  }

  add(key: Partial<SourceSortKey> = { column: 1, descending: false }): void {
    if (this.holder.children.length >= 5) {
      this.toast(localize('ui.multiSort.sortUpLevels'), 'warning');
      return;
    }
    const row = document.createElement('div');
    row.className = 'sort-level';
    const number = document.createElement('span');
    number.className = 'sort-level-number';
    const col = document.createElement('input');
    col.className = 'field-control sort-column';
    col.placeholder = localize('ui.multiSort.columnLettersSerialNumbers');
    col.title = localize('ui.multiSort.exampleAbOr');
    col.value = this.colName(key.column || 1);
    col.setAttribute('aria-label', localize('ui.multiSort.sort'));
    col.spellcheck = false;

    const dir = document.createElement('select');
    dir.className = 'field-control sort-direction';
    dir.setAttribute('aria-label', localize('ui.multiSort.sortingDirection'));
    for (const [value, label] of [
      ['asc', localize('ui.multiSort.fromSmallestLargest')],
      ['desc', localize('ui.multiSort.fromLargestSmallest')],
    ] as const) {
      const opt = document.createElement('option');
      opt.value = value;
      opt.textContent = label;
      dir.appendChild(opt);
    }
    dir.value = key.descending ? 'desc' : 'asc';

    const mode = document.createElement('select');
    mode.className = 'field-control sort-mode';
    mode.setAttribute('aria-label', localize('ui.multiSort.sortingMethod'));
    for (const [value, label] of [
      ['', localize('ui.multiSort.originalType')],
      ['number', localize('ui.multiSort.adjustAccordingValuesAmounts')],
      ['text', localize('ui.common.pressDisplayText')],
      ['natural', localize('ui.multiSort.naturalOrderTextNumbers')],
    ] as const) {
      const opt = document.createElement('option');
      opt.value = value;
      opt.textContent = label;
      mode.appendChild(opt);
    }
    mode.value = key.mode || '';

    const remove = document.createElement('button');
    remove.type = 'button';
    remove.className = 'sort-level-remove';
    remove.textContent = '×';
    remove.title = localize('ui.multiSort.deleteSortingConditions');
    remove.addEventListener('click', () => {
      if (this.holder.children.length === 1) {
        this.toast(localize('ui.multiSort.retainAtLeastOneSorting'), 'warning');
        return;
      }
      row.remove();
      this.relabel();
    });
    row.append(number, col, mode, dir, remove);
    this.holder.appendChild(row);
    this.relabel();
  }

  relabel(): void {
    [...this.holder.children].forEach((r, i) => {
      const label = r.querySelector('.sort-level-number');
      if (label) label.textContent = String(i + 1).padStart(2, '0');
    });
  }

  values(): MultiSortValues {
    const keys: SourceSortKey[] = [];
    const seen = new Set<number>();
    for (const row of this.holder.children) {
      const colInput = row.querySelector('.sort-column');
      const modeSelect = row.querySelector('.sort-mode');
      const dirSelect = row.querySelector('.sort-direction');
      const raw = colInput instanceof HTMLInputElement ? colInput.value : '';
      const column = this.parseColumn(raw);
      if (!column || column > this.s.usedMaxCol) {
        throw new Error(localize('ui.multiSort.sortingInvalidOrOutValid', { raw }));
      }
      if (seen.has(column)) throw new Error(localize('ui.multiSort.sortDuplicate', { colName: this.colName(column) }));
      seen.add(column);
      keys.push({
        column,
        mode: modeSelect instanceof HTMLSelectElement ? modeSelect.value : '',
        descending: dirSelect instanceof HTMLSelectElement && dirSelect.value === 'desc',
      });
    }
    const header = Number(requireInput('sortHeaderRow').value);
    if (!Number.isInteger(header) || header < 0 || header >= this.s.usedMaxRow) {
      throw new Error(localize('ui.multiSort.headerRowMustBeLess'));
    }
    return { keys, header };
  }

  async apply(): Promise<void> {
    let args: MultiSortValues;
    try {
      args = this.values();
    } catch (err: unknown) {
      this.toast(errorMessage(err), 'warning');
      return;
    }
    requireDialog('multiSortDialog').close();
    if (this.s.sheet) {
      this.s.sortKeysBySheet.set(this.s.sheet, args.keys);
      this.s.sortHeaderBySheet.set(this.s.sheet, args.header);
    }
    await this.submit(args.keys, args.header);
  }

  async reset(): Promise<void> {
    requireDialog('multiSortDialog').close();
    if (this.s.sheet) {
      this.s.sortKeysBySheet.delete(this.s.sheet);
      this.s.sortHeaderBySheet.delete(this.s.sheet);
    }
    await this.clear();
  }
}
