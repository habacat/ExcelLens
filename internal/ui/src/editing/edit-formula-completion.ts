/**
 * Worksheet formula completion popup: function catalog, workbook defined names,
 * Excel table names, and structured table-column selector completions with
 * virtualized option rendering and active argument hints.
 */
import type { ApiClient } from '../contracts/common.ts';
import type { EditAddress, FormulaSymbol } from '../contracts/edit.ts';
import { parseRef } from '../core/cell-address.ts';
import { requireEl } from '../core/dom.ts';
import {
  columnContext,
  formulaContext,
  type FormulaColumnContext,
  type FormulaContext,
} from './formula-entry-model.ts';
import {
  decodeFormulaSymbols,
  decodeFunctionCatalogResponse,
} from '../core/http-decoders.generated.ts';

/** @httpResponse FormulaSymbols */
export type FormulaSymbols = readonly FormulaSymbol[];

export interface WorksheetFunctionSpec {
  readonly name: string;
  readonly signature: string;
}

export interface FormulaCompletionItem {
  readonly name: string;
  readonly signature: string;
  readonly kind?: 'name' | 'table' | 'column';
}

export type FormulaCompletionAcceptFn = (
  field: HTMLTextAreaElement,
  from: number,
  to: number,
  value: string,
  offerNext?: boolean,
) => void;

export interface FormulaCompletionHost {
  readonly draft: { readonly address: EditAddress } | null;
  readonly owner: {
    readonly api: ApiClient;
    readonly data: {
      readonly book: string;
      readonly document: {
        readonly id: string;
        readonly binding: string;
        readonly revision: string;
      };
    } | null;
    post(command: string, body: unknown, signal?: AbortSignal): Promise<unknown>;
  };
}

/** @httpResponse FunctionCatalogResponse */
export interface FunctionCatalogResponse {
  readonly items: readonly WorksheetFunctionSpec[];
}

export class WorksheetCompletion {
  private readonly editor: FormulaCompletionHost;
  private readonly api: ApiClient;
  private readonly accept: FormulaCompletionAcceptFn;
  private readonly popup: HTMLElement;
  private readonly list: HTMLElement;
  private readonly hint: HTMLElement;
  private items: FormulaCompletionItem[] = [];
  private generation = 0;
  private field: HTMLTextAreaElement | null = null;
  private index = 0;
  private virtual = false;
  private firstOption = -1;
  private rowHeight = 0;
  private catalog: readonly WorksheetFunctionSpec[] | null = null;
  private loading: Promise<void> | null = null;
  private symbolKey: string | null = null;
  private symbolsReady: Promise<readonly FormulaSymbol[]> | null = null;

  constructor(editor: FormulaCompletionHost, accept: FormulaCompletionAcceptFn) {
    this.editor = editor;
    this.api = editor.owner.api;
    this.accept = accept;
    this.popup = requireEl('editFormulaPopup');
    this.list = requireEl('editFormulaSuggestions');
    this.hint = requireEl('editFormulaHint');
    this.popup.addEventListener('pointerdown', (event: PointerEvent) => event.preventDefault());
    this.list.addEventListener('scroll', () => this.renderSlice());
    this.list.addEventListener('click', (event: MouseEvent) => {
      const target = event.target;
      const option = target instanceof Element ? target.closest('[role="option"]') : null;
      if (option instanceof HTMLElement) this.choose(Number(option.dataset.index));
    });
  }

  hide(): void {
    ++this.generation;
    this.popup.hidden = true;
    this.list.hidden = true;
    this.hint.hidden = true;
    this.field?.removeAttribute('aria-activedescendant');
    this.field?.setAttribute('aria-expanded', 'false');
    this.field = null;
    this.items = [];
  }

  context(text: string, caret: number): FormulaColumnContext | FormulaContext | null {
    return columnContext(text, caret) ?? formulaContext(text, caret);
  }

  async symbols(): Promise<readonly FormulaSymbol[]> {
    const data = this.editor.owner.data;
    if (!data) return [];
    const d = data.document;
    const key = [d.id, d.binding, data.book].join('|');
    if (key !== this.symbolKey) {
      this.symbolKey = key;
      this.symbolsReady = this.editor.owner
        .post('formula-symbols', {
          document: { id: d.id, binding: d.binding },
          revision: d.revision,
        }).then(decodeFormulaSymbols)
        .catch(() => {
          if (this.symbolKey === key) this.symbolKey = null;
          return [];
        });
    }
    return (await this.symbolsReady) ?? [];
  }

  symbolChoices(
    items: readonly FormulaSymbol[],
    at: FormulaColumnContext | FormulaContext,
  ): FormulaCompletionItem[] {
    const draft = this.editor.draft;
    if (!draft) return [];
    const addr = draft.address;
    const tables = items.filter((x) => x.kind === 'table');
    if ('table' in at) {
      const table = tables.find((t) => {
        if (at.table) return t.name.toUpperCase() === at.table.toUpperCase();
        if (t.scope !== addr.sheet || !t.ref) return false;
        const parts = t.ref.split(':');
        const a = parseRef(parts[0], Infinity, Infinity);
        const b = parseRef(parts[1], Infinity, Infinity);
        return Boolean(
          a && b && addr.row >= a.row && addr.row <= b.row && addr.col >= a.col && addr.col <= b.col,
        );
      });
      if (!table) return [];
      return (table.columns ?? []).map((name) => ({
        name,
        kind: 'column',
        signature: `${table.name} · 表列`,
      }));
    }
    const found = new Map<string, FormulaCompletionItem>();
    for (const x of items) {
      if (x.kind === 'table' || !x.scope || x.scope === addr.sheet) {
        const upper = x.name.toUpperCase();
        if (!x.scope && found.has(upper)) continue;
        found.set(upper, {
          name: x.name,
          kind: x.kind,
          signature: x.kind === 'table' ? 'Excel 表格' : `${x.scope || '工作簿'} · 定义名称`,
        });
      }
    }
    return [...found.values()];
  }

  async update(field: HTMLTextAreaElement, text: string): Promise<void> {
    const ticket = ++this.generation;
    const at = this.context(text, field.selectionStart);
    const isColumn = at !== null && 'table' in at;
    if (
      !at ||
      (!at.prefix && (isColumn ? false : !at.fn) && !isColumn) ||
      field.selectionStart !== field.selectionEnd
    ) {
      this.hide();
      return;
    }
    if (!this.catalog) {
      this.loading ??= this.api(
        '/api/calculator/functions?scope=worksheet',
       undefined, decodeFunctionCatalogResponse)
        .then((data) => {
          this.catalog = data.items;
        })
        .finally(() => {
          this.loading = null;
        });
      try {
        await this.loading;
      } catch {
        if (ticket === this.generation) this.hide();
        return;
      }
    }
    const symbols = await this.symbols();
    if (ticket !== this.generation || field !== document.activeElement || !this.editor.draft) {
      return;
    }
    const catalog = this.catalog ?? [];
    const prefix = at.prefix.toUpperCase();
    const choices = this.symbolChoices(symbols, at);
    const matches: FormulaCompletionItem[] = isColumn
      ? choices.filter((x) => x.name.toUpperCase().startsWith(prefix))
      : prefix
        ? [
            ...catalog.filter((f) => f.name.startsWith(prefix)),
            ...choices.filter((x) => x.name.toUpperCase().startsWith(prefix)),
          ]
        : [];
    const fn = !isColumn && at.fn ? catalog.find((f) => f.name === at.fn) : undefined;
    if (!matches.length && !fn) {
      this.hide();
      return;
    }
    if (this.field && this.field !== field) this.hide();
    this.field = field;
    this.items = matches;
    this.index = 0;
    this.list.replaceChildren();
    this.virtual = matches.length > 80;
    this.firstOption = -1;
    this.list.hidden = !matches.length;
    this.hint.hidden = !fn && !matches.length;
    if (fn && !matches.length && !isColumn) {
      const args = fn.signature.slice(fn.name.length + 1, -1).split(/,\s*/);
      const active = Math.min(at.arg, args.length - 1);
      this.hint.replaceChildren(`${fn.name}(`);
      args.forEach((arg, i) => {
        if (i) this.hint.append(', ');
        const span = document.createElement(i === active ? 'strong' : 'span');
        span.textContent = arg;
        this.hint.append(span);
      });
      this.hint.append(')');
    }
    this.popup.hidden = false;
    this.popup.classList.toggle('hint-only', !matches.length);
    this.list.scrollTop = 0;
    this.rowHeight = 0;
    if (this.virtual) {
      const sample = this.makeOption(0);
      this.list.append(sample);
      this.rowHeight = sample.getBoundingClientRect().height;
      if (!this.rowHeight) this.virtual = false;
    }
    this.renderSlice();
    this.highlight();
    this.position();
  }

  private makeOption(i: number): HTMLDivElement {
    const option = document.createElement('div');
    option.setAttribute('role', 'option');
    option.id = `editFormulaOption${i}`;
    option.dataset.index = String(i);
    option.textContent = this.items[i]?.name ?? '';
    option.setAttribute('aria-posinset', String(i + 1));
    option.setAttribute('aria-setsize', String(this.items.length));
    if (i === this.index) option.setAttribute('aria-selected', 'true');
    return option;
  }

  private renderSlice(): void {
    if (this.list.hidden || !this.field) return;
    const first =
      this.virtual && this.rowHeight > 0
        ? Math.max(0, Math.floor(this.list.scrollTop / this.rowHeight) - 3)
        : 0;
    if (first === this.firstOption) return;
    this.firstOption = first;
    const end =
      this.virtual && this.rowHeight > 0
        ? Math.min(
            this.items.length,
            first + Math.ceil(this.list.clientHeight / this.rowHeight) + 8,
          )
        : this.items.length;
    const fragment = document.createDocumentFragment();
    const space = (n: number): void => {
      if (n > 0) {
        const gap = document.createElement('div');
        gap.setAttribute('aria-hidden', 'true');
        gap.style.height = `${n * this.rowHeight}px`;
        fragment.append(gap);
      }
    };
    space(first);
    for (let i = first; i < end; i++) fragment.append(this.makeOption(i));
    space(this.items.length - end);
    this.list.replaceChildren(fragment);
    const active = this.list.querySelector('[aria-selected="true"]');
    if (active) this.field.setAttribute('aria-activedescendant', active.id);
    else this.field.removeAttribute('aria-activedescendant');
  }

  private highlight(): void {
    if (!this.field) return;
    if (this.virtual) {
      const top = this.index * this.rowHeight;
      const bottom = top + this.rowHeight;
      if (top < this.list.scrollTop) this.list.scrollTop = top;
      else if (bottom > this.list.scrollTop + this.list.clientHeight) {
        this.list.scrollTop = bottom - this.list.clientHeight;
      }
      this.renderSlice();
    }
    const option = this.list.querySelector(`[data-index="${this.index}"]`);
    this.list.querySelector('[aria-selected="true"]')?.setAttribute('aria-selected', 'false');
    this.field.setAttribute('aria-expanded', String(Boolean(option)));
    if (!(option instanceof HTMLElement)) {
      this.field.removeAttribute('aria-activedescendant');
      return;
    }
    const item = this.items[this.index];
    this.hint.textContent = item?.signature ?? '';
    option.setAttribute('aria-selected', 'true');
    this.field.setAttribute('aria-activedescendant', option.id);
    if (option.offsetTop < this.list.scrollTop) this.list.scrollTop = option.offsetTop;
    else if (option.offsetTop + option.offsetHeight > this.list.scrollTop + this.list.clientHeight) {
      this.list.scrollTop = option.offsetTop + option.offsetHeight - this.list.clientHeight;
    }
  }

  position(): void {
    if (this.popup.hidden || !this.field) return;
    const anchor =
      this.field.style.opacity === '0' ? requireEl('editGridFormula') : this.field;
    const r = anchor.getBoundingClientRect();
    const box = this.popup;
    const gap = 3;
    const margin = 5;
    const below = window.innerHeight - r.bottom - margin - gap;
    const above = r.top - margin - gap;
    box.style.maxHeight = `${Math.max(25, Math.max(below, above))}px`;
    const top =
      below >= box.offsetHeight || below >= above ? r.bottom + gap : r.top - box.offsetHeight - gap;
    box.style.top = `${Math.max(margin, top)}px`;
    box.style.left = `${Math.max(margin, Math.min(r.left, window.innerWidth - box.offsetWidth - margin))}px`;
  }

  choose(i: number): void {
    const option = this.items[i];
    const field = this.field;
    if (!option || !field) return;
    const cursor = field.selectionStart;
    const at = this.context(field.value, cursor);
    if (!at) return;
    if (option.kind) {
      const suffix = field.value.slice(cursor);
      const end =
        option.kind === 'column'
          ? at.end
          : cursor + (/^[\p{L}\p{N}_.]*/u.exec(suffix)?.[0].length ?? 0);
      this.hide();
      const closures = 'closures' in at ? at.closures : 1;
      const value =
        option.kind === 'column'
          ? option.name.replace(/['#@[\]]/g, "'$&") + ']'.repeat(closures)
          : option.name + (option.kind === 'table' ? '[' : '');
      this.accept(field, at.start, end, value, option.kind !== 'name');
      return;
    }
    const tail = /^[A-Za-z0-9_.]*/.exec(field.value.slice(cursor))?.[0] ?? '';
    const end = cursor + tail.length;
    const hasParen = field.value[end] === '(';
    this.hide();
    this.accept(field, at.start, end + (hasParen ? 1 : 0), `${option.name}(`);
  }

  key(event: KeyboardEvent): boolean {
    if (
      this.list.hidden ||
      this.popup.hidden ||
      !this.items.length ||
      event.ctrlKey ||
      event.metaKey ||
      event.shiftKey ||
      event.altKey
    ) {
      return false;
    }
    if (event.key === 'ArrowUp' || event.key === 'ArrowDown') {
      this.index =
        (this.index + (event.key === 'ArrowUp' ? -1 : 1) + this.items.length) % this.items.length;
      this.highlight();
    } else if (event.key === 'Tab' || event.key === 'Enter') {
      this.choose(this.index);
    } else if (event.key === 'Escape') {
      this.hide();
    } else {
      return false;
    }
    return true;
  }
}
