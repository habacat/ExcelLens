/**
 * In-grid data validation dropdown picker for list-constrained cells in editable copies.
 * Merges source list areas with live edit rules inside an interval tree for O(log n) lookup.
 */
import type { ActivityHandle, HttpError, MergeRange } from '../contracts/common.ts';
import type {
  EditChange,
  EditGridRuleArea,
  EditSheetMeta,
  ListControlArea,
} from '../contracts/edit.ts';
import type { SelectedCell } from '../contracts/workbook.ts';
import { beginActivity } from '../core/activity.ts';
import { errorMessage } from '../core/display-format.ts';
import { requireButton, requireSelect } from '../core/dom.ts';
import { MergedCells } from '../grid/merges.ts';

function areaKey(r: MergeRange): string {
  return [r.row, r.col, r.endRow, r.endCol].join(',');
}

function httpStatus(error: unknown): number | undefined {
  if (typeof error === 'object' && error !== null && 'status' in error) {
    const status = (error as HttpError).status;
    if (typeof status === 'number') return status;
  }
  return undefined;
}

interface ChoicesResponse {
  readonly choices?: readonly string[];
}

export interface GridValidationListHost {
  readonly active: boolean;
  readonly blocked: boolean;
  stale: boolean;
  readonly book: string;
  readonly draft: unknown;
  readonly selected: SelectedCell | null;
  readonly shape: EditSheetMeta | undefined;
  readonly region: HTMLElement;
  readonly owner: {
    readonly data: {
      readonly encoding?: string;
      readonly document: { readonly revision: string };
    } | null;
    post<T = unknown>(command: string, body: unknown, signal?: AbortSignal): Promise<T>;
    commit(
      action: 'change',
      changes: readonly EditChange[],
      options?: { readonly direct?: boolean },
    ): Promise<boolean | undefined>;
  };
  readonly ranges: {
    target(): {
      readonly document: { readonly id: string; readonly binding: string };
      readonly revision: string;
      readonly sheet: string;
    };
  };
  readonly view: {
    ready(): boolean;
    bounds(ref: string): {
      readonly rect: {
        readonly left: number;
        readonly right: number;
        readonly top: number;
        readonly bottom: number;
      };
    } | null;
    move(deltaRow: number, deltaCol: number, extend?: boolean): Promise<void> | void;
  };
  focus(): void;
  controls(): void;
  status(message: string, error?: boolean): void;
  act(
    next: ((activity?: ActivityHandle | null) => Promise<boolean | undefined> | boolean | undefined) | null,
    confirmDraft?: boolean,
    label?: string,
  ): Promise<boolean>;
}

export class GridValidationList {
  private readonly grid: GridValidationListHost;
  public readonly button: HTMLButtonElement;
  public readonly list: HTMLSelectElement;
  public tree: MergedCells | null = null;
  private lookup: AbortController | null = null;
  private source: AbortController | null = null;
  private key = '';
  private point = '';
  private sheet = '';
  private base: readonly ListControlArea[] = [];
  private rules: readonly EditGridRuleArea[] | undefined;
  private rulesStamp = '';
  private error = '';
  private ready: Promise<void> = Promise.resolve();

  constructor(editor: GridValidationListHost) {
    this.grid = editor;
    this.button = requireButton('editGridListButton');
    this.list = requireSelect('editGridList');
    this.button.onclick = (): void => {
      if (this.list.hidden) void this.open();
      else this.dismiss(true);
    };
    this.list.addEventListener('click', (event: MouseEvent) => {
      if (event.target instanceof Element && event.target.tagName === 'OPTION') this.choose();
    });
    this.list.addEventListener('keydown', (event: KeyboardEvent) => {
      if (event.key === 'Escape' || (event.altKey && event.key === 'ArrowUp')) {
        this.dismiss(true);
      } else if (event.key === 'Enter' || event.key === 'Tab') {
        this.choose(event.key === 'Tab' ? (event.shiftKey ? -1 : 1) : 0);
      } else {
        return;
      }
      event.preventDefault();
      event.stopPropagation();
    });
    document.addEventListener(
      'pointerdown',
      (event: PointerEvent) => {
        const target = event.target;
        if (
          target instanceof Node &&
          !this.button.contains(target) &&
          !this.list.contains(target)
        ) {
          this.dismiss();
        }
      },
      true,
    );
  }

  dismiss(focus = false): void {
    this.lookup?.abort();
    this.lookup = null;
    this.list.hidden = true;
    this.button.setAttribute('aria-expanded', 'false');
    if (focus) this.grid.focus();
  }

  reset(): void {
    this.dismiss();
    this.source?.abort();
    this.source = null;
    this.key = '';
    this.point = '';
    this.tree = null;
    this.button.hidden = true;
  }

  setRules(rules: readonly EditGridRuleArea[] | undefined): void {
    const stamp = JSON.stringify(rules ?? []);
    if (stamp === this.rulesStamp) return;
    this.rulesStamp = stamp;
    this.rules = rules;
    this.rebuild();
  }

  private rebuild(): void {
    const items = new Map<string, ListControlArea>(this.base.map((r) => [areaKey(r), r]));
    for (const r of this.rules ?? []) {
      if (r.start.sheet !== this.sheet) continue;
      const rect: ListControlArea = {
        row: r.start.row,
        col: r.start.col,
        endRow: r.endRow,
        endCol: r.endCol,
      };
      const key = areaKey(rect);
      if (!r.list) items.delete(key);
      else if (!items.get(key)?.hidden) items.set(key, rect);
    }
    this.tree = new MergedCells();
    this.tree.load([...items.values()].filter((r) => !r.hidden));
  }

  selection(): void {
    const g = this.grid;
    const cell = g.selected;
    const shape = g.shape;
    const data = g.owner.data;
    if (!cell || !g.active || !g.view.ready() || !shape || shape.readOnly || !data || data.encoding) {
      this.reset();
      return;
    }
    const key = JSON.stringify([g.book, shape.name]);
    if (key !== this.key) {
      this.reset();
      this.key = key;
      this.sheet = shape.name;
      this.base = [];
      this.error = '';
      this.rebuild();
      if (shape.sourceRules) {
        const controller = new AbortController();
        this.source = controller;
        this.ready = g.owner
          .post<readonly ListControlArea[]>(
            'list-areas',
            { book: g.book, sheet: shape.name },
            controller.signal,
          )
          .then((areas) => {
            if (this.source !== controller) return;
            this.base = areas;
            this.rebuild();
            this.position();
          })
          .catch((error: unknown) => {
            if (this.source === controller) this.error = errorMessage(error);
          });
      } else {
        this.ready = Promise.resolve();
      }
    }
    const point = JSON.stringify([key, cell.ref, data.document.revision]);
    if (point !== this.point) {
      this.dismiss();
      this.point = point;
    }
    this.position();
  }

  position(): void {
    const g = this.grid;
    const cell = g.selected;
    const hit =
      g.active && !g.draft && !g.stale && cell ? this.tree?.at(cell.row, cell.col) : null;
    const rect = hit && cell ? g.view.bounds(cell.ref)?.rect : undefined;
    if (!rect) {
      this.button.hidden = true;
      if (!this.list.hidden) this.dismiss();
      return;
    }
    const region = g.region.getBoundingClientRect();
    const x = rect.right - region.left;
    const y = rect.bottom - region.top;
    this.button.hidden = false;
    this.button.style.left = `${Math.max(0, Math.min(x, region.width - 20))}px`;
    this.button.style.top = `${Math.max(0, Math.min(y - 20, region.height - 20))}px`;
    if (this.list.hidden) return;
    const width = Math.min(
      region.width,
      Math.max(156, rect.right - rect.left + 20, this.list.scrollWidth + 4),
    );
    this.list.style.width = `${width}px`;
    this.list.style.left = `${Math.max(0, Math.min(rect.left - region.left, region.width - width))}px`;
    const above = rect.top - region.top;
    const below = region.height - y;
    const up = below < this.list.offsetHeight && above > below;
    this.list.style.maxHeight = `${Math.max(0, up ? above : below)}px`;
    this.list.style.top = `${up ? above - this.list.offsetHeight : y}px`;
  }

  async open(): Promise<void> {
    const g = this.grid;
    if (g.blocked || g.stale || !g.active) return;
    this.selection();
    const point = this.point;
    const cell = g.selected;
    if (!point || !cell) return;
    this.dismiss();
    const lookup = new AbortController();
    this.lookup = lookup;
    const current = (): boolean =>
      this.lookup === lookup && point === this.point && g.active && !g.blocked;
    const activity = beginActivity('正在读取选项', { signal: lookup.signal });
    try {
      await this.ready;
      if (!current()) return;
      if (this.error) {
        const message = this.error;
        this.key = '';
        throw new Error(message);
      }
      if (this.button.hidden) throw new Error('当前格没有可选列表');
      const target = g.ranges.target();
      const result = await g.owner.post<ChoicesResponse>(
        'choices',
        { ...target, ref: cell.ref },
        lookup.signal,
      );
      if (!current()) return;
      const choices = result.choices;
      if (!choices?.length) throw new Error('当前列表没有可选值');
      this.list.replaceChildren(...choices.map((value) => new Option(value, value)));
      this.list.size = Math.max(2, Math.min(9, choices.length));
      this.list.value = cell.raw;
      if (this.list.selectedIndex < 0) this.list.selectedIndex = 0;
      this.list.removeAttribute('style');
      this.list.hidden = false;
      this.button.setAttribute('aria-expanded', 'true');
      this.position();
      this.list.focus({ preventScroll: true });
    } catch (error: unknown) {
      if (current() && !lookup.signal.aborted) {
        if (httpStatus(error) === 409) {
          g.stale = true;
          g.controls();
        }
        g.status(errorMessage(error), true);
      }
    } finally {
      activity?.end();
    }
  }

  choose(step = 0): void {
    const cell = this.grid.selected;
    if (this.list.hidden || this.list.selectedIndex < 0 || !cell) return;
    const g = this.grid;
    const raw = this.list.value;
    this.dismiss(true);
    void g.act(async () => {
      const addr = { sheet: cell.sheet, row: cell.row, col: cell.col };
      if (
        cell.raw !== raw ||
        cell.hasFormula ||
        !['s', 'str', 'inlineStr', 'text'].includes(cell.type ?? '')
      ) {
        if (
          !(await g.owner.commit('change', [{ ...addr, value: { kind: 'text', raw } }], {
            direct: true,
          }))
        ) {
          return false;
        }
      }
      if (step) await g.view.move(0, step);
      g.focus();
      return true;
    });
  }
}
