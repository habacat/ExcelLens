/**
 * Pivot table axis layout editor (rows, columns, and lazy-loaded page item filters).
 */
import { t as localize } from '../core/i18n.ts';
import type { PivotInfo, PivotItem, PivotPage } from '../contracts/edit.ts';
import { errorMessage } from '../core/display-format.ts';
import { requireEl, requireInput, requireSelect } from '../core/dom.ts';
import { decodePivotChoicesResponse } from '../core/http-decoders.generated.ts';

export type PivotAxisKey = 'rows' | 'columns' | 'pages';

export interface MutablePivotPage {
  field: string;
  items: PivotItem[];
}

export interface MutablePivotAxes {
  rows: string[];
  columns: string[];
  pages: MutablePivotPage[];
}

export interface PivotLayoutOwner {
  readonly data: {
    readonly document: { readonly id: string; readonly binding: string; readonly revision: string };
  } | null;
  readonly tools: {
    readonly measures: { positions(): void };
  };
  post(command: string, body: unknown, signal?: AbortSignal): Promise<unknown>;
  controls(): void;
}

const AXIS_LABELS: Readonly<Record<PivotAxisKey, string>> = {
  rows: localize('ui.editPivot.rowField'),
  columns: localize('ui.editPivot.columnFields'),
  pages: localize('ui.editPivot.paginationFilter'),
};

const AXIS_KEYS: readonly PivotAxisKey[] = ['rows', 'columns', 'pages'];

function keyOf(item: PivotItem): string {
  return JSON.stringify([item.type, item.value]);
}

function display(item: PivotItem): string {
  if (item.type === 'm') return localize('ui.editPivot.blank');
  if (item.type === 'b') return item.value === '1' ? 'TRUE' : 'FALSE';
  if (item.type === 'd') {
    const trimmed = item.value.endsWith('T00:00:00') ? item.value.slice(0, -9) : item.value;
    return trimmed.replace('T', ' ');
  }
  return item.value;
}

function fieldName(f: string | PivotPage): string {
  return typeof f === 'string' ? f : f.field;
}

export function createPivotActionButton(
  label: string,
  path: string,
  click: () => void,
): HTMLButtonElement {
  const button = document.createElement('button');
  button.type = 'button';
  button.className = 'edit-pivot-action';
  button.title = label;
  button.setAttribute('aria-label', label);
  const icon = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  const stroke = document.createElementNS(icon.namespaceURI, 'path');
  icon.setAttribute('viewBox', '0 0 24 24');
  icon.setAttribute('aria-hidden', 'true');
  stroke.setAttribute('d', path);
  icon.append(stroke);
  button.append(icon);
  button.addEventListener('click', click);
  return button;
}

export class PivotLayout {
  private readonly editor: PivotLayoutOwner;
  private info: PivotInfo | null = null;
  public axes: MutablePivotAxes | null = null;
  private requests: AbortController | null = null;

  constructor(editor: PivotLayoutOwner) {
    this.editor = editor;
  }

  action(label: string, path: string, click: () => void): HTMLButtonElement {
    return createPivotActionButton(label, path, click);
  }

  bind(info: PivotInfo | null): void {
    this.info = info;
    if (!info) {
      this.axes = null;
    } else if (info.axes) {
      this.axes = {
        rows: [...info.axes.rows],
        columns: [...info.axes.columns],
        pages: info.axes.pages.map((p) => ({
          field: p.field,
          items: [...(p.items ?? [])],
        })),
      };
    } else {
      this.axes = {
        rows: info.row ? [info.row] : [],
        columns: [],
        pages: [],
      };
    }
    this.draw();
  }

  draw(): void {
    this.requests?.abort();
    this.requests = new AbortController();
    const root = requireEl('editCopyPivotAxes');
    root.replaceChildren();
    if (!this.axes || !this.info) return;
    const fieldCount = this.info.fields?.length ?? 0;
    for (const key of AXIS_KEYS) {
      const label = AXIS_LABELS[key];
      const area = document.createElement('section');
      area.className = 'edit-pivot-axis';
      area.dataset.axis = key;
      const heading = document.createElement('span');
      heading.className = 'edit-pivot-heading';
      heading.id = `editPivotAxis${key}`;
      heading.textContent = label;
      area.setAttribute('aria-labelledby', heading.id);
      const fields = document.createElement('div');
      fields.className = 'edit-pivot-fields';
      const group: readonly (string | MutablePivotPage)[] = this.axes[key];
      group.forEach((field, index) => {
        const name = fieldName(field);
        const row = document.createElement('div');
        row.className = 'edit-pivot-field';
        const select = this.select(key, index, name);
        if (key === 'rows' && index === 0) select.id = 'editCopyPivotRows';
        const controls = document.createElement('div');
        controls.className = 'edit-pivot-order';
        const up = createPivotActionButton(localize('ui.editPivot.moveUp', { name }), 'm6 14 6-6 6 6', () =>
          this.move(key, index, -1),
        );
        up.disabled = index === 0;
        const down = createPivotActionButton(localize('ui.editPivot.moveDown', { name }), 'm6 10 6 6 6-6', () =>
          this.move(key, index, 1),
        );
        down.disabled = index === group.length - 1;
        controls.append(
          up,
          down,
          createPivotActionButton(localize('ui.editPivot.remove', { name }), 'm7 7 10 10 M17 7 7 17', () =>
            this.change(key, index, ''),
          ),
        );
        row.append(select, controls);
        if (key === 'pages' && typeof field !== 'string') row.append(this.pageFilter(field));
        fields.append(row);
      });
      if (group.length < fieldCount) fields.append(this.select(key, group.length, ''));
      area.append(heading, fields);
      root.append(area);
    }
  }

  private select(key: PivotAxisKey, index: number, name: string): HTMLSelectElement {
    const select = document.createElement('select');
    select.dataset.index = String(index);
    select.setAttribute(
      'aria-label',
      name ? `${AXIS_LABELS[key]} ${index + 1}` : localize('ui.editPivot.add47ab4c6', { value: AXIS_LABELS[key] }),
    );
    if (!name) {
      select.className = 'edit-pivot-add';
      select.add(new Option(localize('ui.editPivot.add', { value: AXIS_LABELS[key] }), ''));
    }
    const existing: readonly (string | MutablePivotPage)[] = this.axes ? this.axes[key] : [];
    for (const f of this.info?.fields ?? []) {
      if (f !== name && existing.some((a) => fieldName(a) === f)) continue;
      select.add(new Option(f, f));
    }
    select.value = name;
    select.addEventListener('change', () => this.change(key, index, select.value));
    return select;
  }

  focus(key: PivotAxisKey, index: number): void {
    const fields = requireEl('editCopyPivotAxes').querySelectorAll<HTMLSelectElement>(
      `[data-axis="${key}"] select`,
    );
    fields[Math.min(index, fields.length - 1)]?.focus();
  }

  change(key: PivotAxisKey, index: number, name: string): void {
    if (!this.axes) return;
    if (!name) {
      this.axes[key].splice(index, 1);
    } else {
      if (key !== 'rows') this.axes.rows = this.axes.rows.filter((f) => f !== name);
      if (key !== 'columns') this.axes.columns = this.axes.columns.filter((f) => f !== name);
      if (key !== 'pages') this.axes.pages = this.axes.pages.filter((p) => p.field !== name);
      if (key === 'pages') this.axes.pages[index] = { field: name, items: [] };
      else this.axes[key][index] = name;
    }
    this.updated(key, index);
  }

  move(key: PivotAxisKey, index: number, delta: number): void {
    if (!this.axes) return;
    const other = index + delta;
    if (key === 'pages') {
      const a = this.axes.pages[index];
      const b = this.axes.pages[other];
      if (a && b) {
        this.axes.pages[index] = b;
        this.axes.pages[other] = a;
      }
    } else {
      const group = this.axes[key];
      const a = group[index];
      const b = group[other];
      if (a !== undefined && b !== undefined) {
        group[index] = b;
        group[other] = a;
      }
    }
    this.updated(key, other);
  }

  private updated(key: PivotAxisKey, index: number): void {
    if (!this.axes?.rows.length) {
      requireSelect('editCopyPivotFilter').value = 'keep';
      requireInput('editCopyPivotItem').value = '';
    }
    this.draw();
    this.editor.tools.measures.positions();
    this.editor.controls();
    this.focus(key, index);
  }

  private pageFilter(page: MutablePivotPage): HTMLDetailsElement {
    const box = document.createElement('details');
    box.className = 'edit-pivot-filter';
    const summary = document.createElement('summary');
    const selected = new Map<string, PivotItem>(page.items.map((item) => [keyOf(item), item]));
    const caption = (): void => {
      const first = selected.values().next().value;
      summary.textContent =
        selected.size > 1
          ? localize('ui.editPivot.itemsSelected', { size: selected.size })
          : first
            ? display(first)
            : localize('ui.editPivot.allItems');
    };
    caption();
    summary.setAttribute('aria-label', localize('ui.editPivot.filterItems', { field: page.field }));
    box.append(summary);
    const panel = document.createElement('div');
    panel.className = 'edit-pivot-options';
    box.append(panel);
    const search = document.createElement('input');
    search.type = 'search';
    search.placeholder = localize('ui.editPivot.searchItems');
    search.setAttribute('aria-label', localize('ui.editPivot.searchProject', { field: page.field }));
    const list = document.createElement('div');
    list.className = 'edit-pivot-items';
    const message = document.createElement('output');
    message.setAttribute('aria-live', 'polite');
    const more = document.createElement('button');
    more.type = 'button';
    more.className = 'mini-button';
    more.textContent = localize('ui.editPivot.moreItems');
    more.hidden = true;
    const all = document.createElement('button');
    all.type = 'button';
    all.className = 'mini-button';
    all.textContent = localize('ui.editPivot.showAll');
    panel.append(search, all, list, message, more);
    let offset = 0;
    let ticket = 0;
    let loaded = false;
    const signal = this.requests?.signal;
    const sync = (): void => {
      page.items = [...selected.values()];
      caption();
    };
    all.onclick = (): void => {
      selected.clear();
      sync();
      for (const cb of list.querySelectorAll<HTMLInputElement>('input')) cb.checked = false;
    };
    const load = async (reset: boolean): Promise<void> => {
      const current = ++ticket;
      if (reset) {
        offset = 0;
        list.replaceChildren();
      }
      more.hidden = true;
      message.textContent = localize('ui.common.reading');
      const doc = this.editor.data?.document;
      const info = this.info;
      if (!doc || !info) return;
      try {
        const reply = await this.editor.post(
          'pivot-items',
          {
            document: { id: doc.id, binding: doc.binding },
            revision: doc.revision,
            sheet: info.sheet,
            pivot: info.name,
            field: page.field,
            search: search.value,
            offset,
          },
          signal,
        ).then(decodePivotChoicesResponse);
        if (current !== ticket || signal?.aborted || !box.isConnected) return;
        for (const item of reply.items) {
          const label = document.createElement('label');
          const check = document.createElement('input');
          check.type = 'checkbox';
          const key = keyOf(item);
          check.checked = selected.has(key);
          check.onchange = (): void => {
            if (check.checked) selected.set(key, item);
            else selected.delete(key);
            sync();
          };
          label.append(check, document.createTextNode(display(item)));
          list.append(label);
        }
        offset += reply.items.length;
        more.hidden = !reply.more;
        message.textContent = offset ? '' : localize('ui.editPivot.noMatchingItems');
        loaded = true;
      } catch (error: unknown) {
        if (current === ticket && !signal?.aborted) {
          message.textContent = errorMessage(error);
          loaded = false;
        }
      }
    };
    let timer: ReturnType<typeof setTimeout> | undefined;
    search.oninput = (): void => {
      ++ticket;
      if (timer !== undefined) clearTimeout(timer);
      timer = setTimeout(() => {
        if (!signal?.aborted) void load(true);
      }, 180);
    };
    more.onclick = (): void => {
      void load(false);
    };
    box.ontoggle = (): void => {
      if (box.open && !loaded) void load(true);
    };
    return box;
  }
}
