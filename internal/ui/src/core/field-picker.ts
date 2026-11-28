/**
 * Bounded source field selection modal shared by append composition and
 * two-table relation workflows.
 */
import { t as localize, th as localizeHTML } from './i18n.ts';
import { labelColumn, searchFields, type SearchableField } from './composition-model.ts';
import { requireButton, requireEl, requireInput } from './dom.ts';

export interface FieldPickerOptions {
  readonly id?: string;
  readonly pageRows: number;
}

export interface SingleFieldPickerConfig {
  readonly multiple?: false;
  readonly fields: readonly SearchableField[];
  readonly selected?: readonly number[];
  readonly disabled?: readonly number[];
  readonly allowEmpty?: boolean;
  readonly title?: string;
  readonly current?: () => boolean;
  readonly onChoose: (column: number) => void;
}

export interface MultiFieldPickerConfig {
  readonly multiple: true;
  readonly fields: readonly SearchableField[];
  readonly selected?: readonly number[];
  readonly disabled?: readonly number[];
  readonly limit: number;
  readonly title?: string;
  readonly current?: () => boolean;
  readonly onChoose: (columns: number[]) => void;
}

export type FieldPickerConfig = SingleFieldPickerConfig | MultiFieldPickerConfig;

function makeMiniButton(text: string, action: () => void): HTMLButtonElement {
  const node = document.createElement('button');
  node.type = 'button';
  node.className = 'mini-button';
  node.textContent = text;
  node.onclick = action;
  return node;
}

export class FieldPicker {
  private readonly id: string;
  private readonly pageRows: number;
  private serial = 0;
  public page = 0;
  private config: FieldPickerConfig | null = null;
  public selected = new Set<number>();
  public readonly dialog: HTMLDialogElement;

  constructor(options: FieldPickerOptions) {
    this.id = options.id ?? 'sourceFieldPicker';
    this.pageRows = options.pageRows;
    this.dialog = document.createElement('dialog');
    this.dialog.id = this.id;
    this.dialog.className = 'float-dialog field-picker';
    this.dialog.setAttribute('aria-labelledby', `${this.id}Title`);
    this.dialog.innerHTML = `<form method="dialog" class="dialog-head"><div id="${this.id}Title" class="dialog-title">${localizeHTML('ui.fieldPicker.selectFields')}</div><button class="dialog-close" aria-label="${localizeHTML('ui.fieldPicker.disableFieldSelection')}" value="cancel">×</button></form><div class="field-picker-body"><input id="${this.id}Search" class="field-control" type="search" placeholder="${localizeHTML('ui.fieldPicker.searchFieldNamesOrColumn')}" aria-label="${localizeHTML('ui.fieldPicker.searchSourceFields')}"><div id="${this.id}Tools" class="field-picker-tools" hidden></div><div id="${this.id}Items" class="field-picker-items"></div><div id="${this.id}Paging" class="field-picker-paging"></div><div id="${this.id}Status" role="status" aria-live="polite"></div><button id="${this.id}Apply" class="primary-button compact" type="button" hidden>${localizeHTML('ui.fieldPicker.ok')}</button></div>`;
    document.body.append(this.dialog);

    requireInput(`${this.id}Search`).oninput = (): void => {
      this.page = 0;
      this.render();
    };
    requireButton(`${this.id}Apply`).onclick = (): void => this.apply();
    this.dialog.addEventListener('keydown', (event: KeyboardEvent) => event.stopPropagation());
    this.dialog.addEventListener('close', () => {
      if (!this.dialog.open) this.close();
    });
  }

  close(): void {
    ++this.serial;
    this.config = null;
    requireEl(`${this.id}Items`).replaceChildren();
    requireEl(`${this.id}Tools`).replaceChildren();
    requireEl(`${this.id}Paging`).replaceChildren();
    if (this.dialog.open) this.dialog.close();
  }

  open(config: FieldPickerConfig): void {
    if (!Number.isInteger(this.pageRows) || this.pageRows < 1 || !Array.isArray(config.fields)) {
      throw new Error(localize('ui.fieldPicker.invalidFieldSelectionCapacity'));
    }
    ++this.serial;
    this.config = config;
    this.page = 0;
    this.selected = new Set(config.selected ?? []);
    const searchInput = requireInput(`${this.id}Search`);
    searchInput.value = '';
    requireEl(`${this.id}Title`).textContent = config.title || localize('ui.common.selectFields');
    requireEl(`${this.id}Status`).textContent = '';
    this.render();
    if (!this.dialog.open) this.dialog.showModal();
    searchInput.focus();
  }

  valid(): boolean {
    return this.config !== null && (!this.config.current || this.config.current());
  }

  select(column: number): void {
    if (!this.valid() || !this.config) {
      this.close();
      return;
    }
    const c = this.config;
    if (c.multiple) {
      if (this.selected.has(column)) {
        this.selected.delete(column);
      } else {
        if (this.selected.size >= c.limit) {
          requireEl(`${this.id}Status`).textContent = localize('ui.fieldPicker.canSelectMaximumFields', { limit: c.limit });
          this.render();
          return;
        }
        this.selected.add(column);
      }
      this.render();
      return;
    }
    this.close();
    c.onChoose(column);
  }

  apply(): void {
    if (!this.valid() || !this.config?.multiple) {
      this.close();
      return;
    }
    const c = this.config;
    const chosen = c.fields
      .filter((field) => this.selected.has(field.column))
      .map((field) => field.column);
    this.close();
    c.onChoose(chosen);
  }

  render(): void {
    if (!this.config) return;
    const c = this.config;
    const host = requireEl(`${this.id}Items`);
    const paging = requireEl(`${this.id}Paging`);
    const tools = requireEl(`${this.id}Tools`);
    host.replaceChildren();
    paging.replaceChildren();
    tools.replaceChildren();

    const searchInput = requireInput(`${this.id}Search`);
    const filtered = searchFields(c.fields, searchInput.value, 0, c.fields.length);
    const totalPages = Math.max(1, Math.ceil(filtered.count / this.pageRows));
    this.page = Math.min(this.page, totalPages - 1);
    const items = filtered.items.slice(this.page * this.pageRows, (this.page + 1) * this.pageRows);
    const revision = this.serial;
    const blocked = new Set(c.disabled ?? []);

    const append = (text: string, column: number): void => {
      const node = makeMiniButton(text, () => {
        if (this.serial === revision && this.config === c) this.select(column);
      });
      node.className = `field-picker-item${this.id === 'compositionPicker' ? ' composition-picker-item' : ''}`;
      node.disabled = column !== 0 && blocked.has(column);
      node.dataset.column = String(column);
      if (c.multiple) {
        const isSelected = this.selected.has(column);
        node.setAttribute('aria-pressed', String(isSelected));
        node.classList.toggle('is-selected', isSelected);
      }
      host.append(node);
    };

    if (!c.multiple && c.allowEmpty) append(localize('ui.fieldPicker.emptyColumns'), 0);
    for (const field of items) {
      append(`${labelColumn(field.column)}　${field.name}`, field.column);
    }
    if (!items.length) {
      const empty = document.createElement('div');
      empty.className = 'field-picker-empty';
      empty.textContent = localize('ui.fieldPicker.noMatchingFields');
      host.append(empty);
    }

    const previous = makeMiniButton(localize('ui.common.previousPage'), () => {
      --this.page;
      this.render();
    });
    const next = makeMiniButton(localize('ui.common.nextPage'), () => {
      ++this.page;
      this.render();
    });
    const info = document.createElement('span');
    previous.disabled = this.page === 0;
    next.disabled = (this.page + 1) * this.pageRows >= filtered.count;
    info.textContent = `${this.page + 1} / ${totalPages}`;
    paging.append(previous, info, next);

    tools.hidden = !c.multiple;
    requireButton(`${this.id}Apply`).hidden = !c.multiple;
    if (c.multiple) {
      const count = document.createElement('span');
      count.textContent = localize('ui.fieldPicker.selected', { size: this.selected.size, limit: c.limit });
      tools.append(
        count,
        makeMiniButton(localize('ui.fieldPicker.selectThisPage'), () => {
          const add = items.filter(
            (field) => !blocked.has(field.column) && !this.selected.has(field.column),
          );
          if (this.selected.size + add.length > c.limit) {
            requireEl(`${this.id}Status`).textContent = localize('ui.fieldPicker.canSelectMaximumFields', { limit: c.limit });
            return;
          }
          for (const field of add) this.selected.add(field.column);
          this.render();
        }),
        makeMiniButton(localize('ui.fieldPicker.clearThisPage'), () => {
          for (const field of items) this.selected.delete(field.column);
          this.render();
        }),
      );
    }
  }
}
