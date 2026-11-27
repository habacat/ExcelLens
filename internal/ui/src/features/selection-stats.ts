/**
 * Status-bar statistics for any combination of Ctrl-selected cells/rectangles.
 * All values come from Go's exact numeric scan of the original workbook, not
 * from cached visible DOM cells or JavaScript floating-point arithmetic.
 */
import type { ApiClient, ToastFn } from '../contracts/common.ts';
import type { SelectionStatsResponse } from '../contracts/features.ts';
import { errorMessage } from '../core/display-format.ts';
import { byId, requireEl } from '../core/dom.ts';
import { MultiSelection } from '../grid/multi-selection.ts';
import type { WorkbookState } from '../grid/state.ts';

export type SelectionStatsMetric =
  | 'average'
  | 'nonEmpty'
  | 'number'
  | 'minimum'
  | 'maximum'
  | 'sum';

export type SelectionStatsFormat = 'units' | 'thousands' | 'myriad' | 'plain';

const METRICS: readonly (readonly [SelectionStatsMetric, string, string])[] = [
  ['average', '平均值', 'A'],
  ['nonEmpty', '计数', 'C'],
  ['number', '数值计数', 'O'],
  ['minimum', '最小值', 'I'],
  ['maximum', '最大值', 'M'],
  ['sum', '求和', 'S'],
];

const FORMATS: readonly (readonly [Exclude<SelectionStatsFormat, 'plain'>, string, string])[] = [
  ['units', '带中文单位分隔', 'H'],
  ['thousands', '使用千位分隔符', 'T'],
  ['myriad', '按万位分隔', 'E'],
];

const SETTINGS_KEY = 'ExcelLens.selectionStats';

interface MenuButtonEntry {
  readonly button: HTMLButtonElement;
  readonly value: HTMLSpanElement | null;
  readonly key: string;
}

export interface EditBindingSnapshot {
  readonly document: {
    readonly id: string;
    readonly binding: unknown;
  };
  readonly revision: number;
  readonly book: string;
}

export interface SelectionStatsOptions {
  readonly state: WorkbookState;
  readonly api: ApiClient;
  readonly colName: (col: number) => string;
  readonly toast: ToastFn;
  readonly profile: () => void;
  readonly excludeThroughRow?: () => number;
  readonly editBinding?: () => EditBindingSnapshot | undefined;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function isMetric(value: unknown): value is SelectionStatsMetric {
  return typeof value === 'string' && METRICS.some(([id]) => id === value);
}

function readClipboard(
  nav: { readonly clipboard?: Partial<Clipboard> },
): Partial<Clipboard> | undefined {
  return nav.clipboard;
}

function isFormat(value: unknown): value is SelectionStatsFormat {
  return (
    value === 'plain' ||
    (typeof value === 'string' && FORMATS.some(([id]) => id === value))
  );
}

export function formatStatusNumber(
  value: string | number,
  mode: SelectionStatsFormat = 'units',
): string {
  const raw = String(value);
  const match = /^([+-]?)(\d+)(\.\d+)?(\s*≈)?$/.exec(raw);
  if (!match?.[2] || mode === 'plain') return raw;
  const sign = match[1] ?? '';
  const digits = match[2];
  const frac = match[3] ?? '';
  const approx = match[4] ?? '';
  const size = mode === 'thousands' ? 3 : 4;
  if (digits.length <= size || (mode === 'units' && digits.length > 16)) return raw;
  const groups: string[] = [];
  for (let end = digits.length; end > 0; end -= size) {
    groups.unshift(digits.slice(Math.max(0, end - size), end));
  }
  if (mode !== 'units') return sign + groups.join(',') + frac + approx;
  const units = ['', '万', '亿', '万亿'] as const;
  let result = '';
  groups.forEach((group, i) => {
    const power = groups.length - i - 1;
    if (/^0+$/.test(group) && !frac) return;
    result += group + (units[power] ?? '');
  });
  return sign + result + frac + approx;
}

export class SelectionStats {
  public static readonly formatNumber = formatStatusNumber;

  private readonly s: WorkbookState;
  private readonly api: ApiClient;
  private readonly colName: (col: number) => string;
  private readonly toast: ToastFn;
  private readonly profile: () => void;
  private readonly excludeThroughRow: () => number;
  private readonly editBinding?: () => EditBindingSnapshot | undefined;
  private readonly host: HTMLElement;
  private readonly menu: HTMLElement | null;

  private ticket = 0;
  private timer: ReturnType<typeof setTimeout> | undefined;
  private abort: AbortController | null = null;
  private readonly cache = new Map<string, SelectionStatsResponse>();
  private lastKey = '';
  private current: SelectionStatsResponse | null = null;
  private status = '';
  public metrics: Set<SelectionStatsMetric>;
  public format: SelectionStatsFormat;
  private menuButtons: Map<SelectionStatsMetric | SelectionStatsFormat, MenuButtonEntry> | null =
    null;
  private menuStatus: HTMLDivElement | null = null;

  constructor(options: SelectionStatsOptions) {
    this.s = options.state;
    this.api = options.api;
    this.colName = options.colName;
    this.toast = options.toast;
    this.profile = options.profile;
    this.excludeThroughRow = options.excludeThroughRow ?? (() => 0);
    this.editBinding = options.editBinding;
    this.host = requireEl('quickStats');

    let saved: Record<string, unknown> | null = null;
    try {
      const raw = localStorage.getItem(SETTINGS_KEY);
      if (raw) {
        const parsed: unknown = JSON.parse(raw);
        if (isRecord(parsed)) saved = parsed;
      }
    } catch {
      // Ignore invalid or inaccessible local preferences.
    }
    const savedMetrics =
      saved && Array.isArray(saved.metrics) ? saved.metrics.filter(isMetric) : null;
    this.metrics = new Set<SelectionStatsMetric>(
      savedMetrics ?? ['average', 'nonEmpty', 'sum'],
    );
    this.format = saved && isFormat(saved.format) ? saved.format : 'units';

    this.menu = byId('quickStatsMenu');
    if (this.menu) this.buildMenu(this.menu);

    this.host.addEventListener('click', (event: MouseEvent) => {
      const target = event.target instanceof Element ? event.target : null;
      const value = target?.closest<HTMLElement>('[data-stat-value]')?.dataset.statValue;
      if (value !== undefined) {
        const clipboard = readClipboard(navigator);
        if (typeof clipboard?.writeText === 'function') {
          clipboard
            .writeText(value)
            .then(() => this.toast('已复制', 'success'))
            .catch(() => this.toast('无法复制统计结果', 'error'));
        } else {
          this.toast('当前环境不支持直接复制，请使用系统剪贴板快捷键', 'warning');
        }
        return;
      }
      if (this.editBinding?.()) {
        byId('quickStatsOptions')?.click();
        return;
      }
      const areas = MultiSelection.areas(this.s, true);
      if (areas.length > 1) {
        this.toast('多选区精确统计显示在状态栏；详细分析可选择一个连续区域');
        return;
      }
      this.profile();
    });
    this.host.setAttribute('role', 'status');
    this.host.setAttribute('aria-live', 'polite');
  }

  private buildMenu(menu: HTMLElement): void {
    const buttons = new Map<SelectionStatsMetric | SelectionStatsFormat, MenuButtonEntry>();
    this.menuButtons = buttons;
    const add = (
      entries: readonly (readonly [SelectionStatsMetric | SelectionStatsFormat, string, string])[],
      role: 'menuitemcheckbox' | 'menuitemradio',
    ): void => {
      for (const [id, label, key] of entries) {
        const button = document.createElement('button');
        const name = document.createElement('span');
        button.type = 'button';
        button.setAttribute('role', role);
        button.setAttribute('data-menu-keep', '');
        name.textContent = `${label} (${key})`;
        button.append(name);
        const value = role === 'menuitemcheckbox' ? document.createElement('span') : null;
        if (value) {
          value.className = 'stat-result';
          button.append(value);
        }
        button.addEventListener('click', () => {
          if (role === 'menuitemcheckbox' && isMetric(id)) {
            if (this.metrics.has(id)) this.metrics.delete(id);
            else this.metrics.add(id);
          } else if (isFormat(id)) {
            this.format = this.format === id ? 'plain' : id;
          }
          try {
            localStorage.setItem(
              SETTINGS_KEY,
              JSON.stringify({ metrics: [...this.metrics], format: this.format }),
            );
          } catch {
            // Ignore storage write failures.
          }
          if (this.current) this.render(this.current);
          else this.updateMenu();
        });
        buttons.set(id, { button, value, key });
        menu.append(button);
      }
    };

    this.menuStatus = document.createElement('div');
    this.menuStatus.className = 'stat-status';
    menu.append(this.menuStatus);
    add(METRICS, 'menuitemcheckbox');
    const divider = document.createElement('div');
    divider.className = 'menu-divider';
    divider.setAttribute('role', 'separator');
    menu.append(divider);
    add(FORMATS, 'menuitemradio');

    menu.addEventListener('menuopen', () => this.updateMenu());
    menu.addEventListener('keydown', (event: KeyboardEvent) => {
      if (event.ctrlKey || event.metaKey || event.altKey) return;
      const entry = [...buttons.values()].find((item) => item.key === event.key.toUpperCase());
      if (entry) {
        event.preventDefault();
        event.stopPropagation();
        entry.button.focus();
        entry.button.click();
      }
    });
    this.updateMenu();
  }

  updateMenu(): void {
    if (!this.menuButtons) return;
    for (const [id, { button, value }] of this.menuButtons) {
      const checked = (isMetric(id) && this.metrics.has(id)) || id === this.format;
      button.setAttribute('aria-checked', String(checked));
      if (value && isMetric(id)) {
        const raw = this.metricValue(id);
        value.textContent = raw == null ? '—' : formatStatusNumber(raw, this.format);
        value.title = raw == null ? '' : String(raw);
      }
    }
    if (this.menuStatus) {
      this.menuStatus.textContent = this.status;
      this.menuStatus.hidden = !this.status;
    }
  }

  metricValue(id: SelectionStatsMetric): string | number | null {
    const d = this.current;
    if (!d) return null;
    if (id === 'nonEmpty' || id === 'number') return d[id];
    if (d.number <= 0) return null;
    return d[id] ?? null;
  }

  clear(): void {
    ++this.ticket;
    if (this.timer !== undefined) {
      clearTimeout(this.timer);
      this.timer = undefined;
    }
    this.abort?.abort();
    this.abort = null;
    this.current = null;
    this.status = '';
    this.updateMenu();
    this.host.replaceChildren();
    this.lastKey = '';
    this.host.removeAttribute('title');
  }

  resetBook(): void {
    this.clear();
    this.cache.clear();
  }

  stat(label: string, value: string, copyable: boolean | string = false): HTMLSpanElement {
    const wrap = document.createElement('span');
    wrap.className = 'quick-stat';
    const name = document.createElement('span');
    name.textContent = `${label}=`;
    const strong = document.createElement('b');
    strong.textContent = value;
    if (copyable) {
      const copyVal = typeof copyable === 'string' ? copyable : value;
      strong.dataset.statValue = copyVal;
      strong.title = `${label}=${copyVal}`;
      strong.classList.add('quick-stat-copy');
    }
    wrap.append(name, strong);
    return wrap;
  }

  show(...pairs: readonly (readonly [string, string | number, (boolean | string)?])[]): void {
    this.host.replaceChildren(
      ...pairs.map(([label, value, copyable]) => this.stat(label, String(value), copyable)),
    );
  }

  schedule(): void {
    ++this.ticket;
    if (this.timer !== undefined) {
      clearTimeout(this.timer);
      this.timer = undefined;
    }
    this.abort?.abort();
    this.abort = null;
    const areas = MultiSelection.areas(this.s, true);
    this.current = null;
    this.status = '';
    this.lastKey = '';
    if (!areas.length || !this.s.sheet || !this.s.indexReady) {
      this.host.replaceChildren();
      this.updateMenu();
      return;
    }
    const ranges = areas.map((area) => MultiSelection.points(area, this.colName));
    const through = this.excludeThroughRow();
    const upper = areas.reduce((sum, a) => {
      const rows = this.s.rowVisibility
        ? this.s.rowVisibility.selectionCount(a, this.s.filtered, through)
        : MultiSelection.rowCount(a, this.s.filtered, through);
      return sum + rows * (Math.abs(a.col2 - a.col1) + 1);
    }, 0);
    if (areas.length > MultiSelection.LIMIT || upper > 400000) {
      this.status = '超出即时统计限额';
      this.updateMenu();
      this.show(
        ['选区', areas.length > 1 ? `${areas.length} 处` : `${upper.toLocaleString()} 格`],
        ['统计', this.status],
      );
      return;
    }
    this.show(['选区', areas.length > 1 ? `${areas.length} 处` : `${upper.toLocaleString()} 格`]);
    const filterId = this.s.filtered ? this.s.filterId : '';
    const viewId = this.s.rowView?.id ?? '';
    const effectiveThrough = filterId ? through : 0;
    const edit = this.editBinding?.();
    const sheet = this.s.sheet;
    const key = JSON.stringify([
      this.s.sheetEpoch,
      sheet,
      filterId,
      viewId,
      effectiveThrough,
      ranges,
      edit,
    ]);
    this.lastKey = key;
    const cached = this.cache.get(key);
    if (cached) {
      this.render(cached);
      return;
    }
    this.status = '正在统计…';
    this.updateMenu();
    const ticket = this.ticket;
    this.timer = setTimeout(() => {
      void (async () => {
        const ctrl = new AbortController();
        this.abort = ctrl;
        try {
          const result = await this.api<SelectionStatsResponse>('/api/selection/stats', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
              sheet,
              filterId,
              ...(viewId ? { viewId } : {}),
              excludeThroughRow: effectiveThrough,
              ranges,
              edit,
            }),
            signal: ctrl.signal,
          });
          if (ctrl.signal.aborted || ticket !== this.ticket || key !== this.lastKey) return;
          this.cache.set(key, result);
          if (this.cache.size > 64) {
            const oldest = this.cache.keys().next();
            if (!oldest.done) this.cache.delete(oldest.value);
          }
          this.render(result);
        } catch (err: unknown) {
          if (ctrl.signal.aborted || ticket !== this.ticket) return;
          this.status = errorMessage(err) || '统计暂不可用';
          this.updateMenu();
          this.show(['选区', `${areas.length} 处`], ['统计', '暂不可用']);
        } finally {
          if (this.abort === ctrl) this.abort = null;
        }
      })();
    }, upper > 6000 ? 420 : 180);
  }

  render(d: SelectionStatsResponse | null | undefined): void {
    if (!d) return;
    this.current = d;
    this.status = '';
    this.updateMenu();
    const pairs: [string, string, string][] = [];
    for (const [id, label] of METRICS) {
      const value = this.metricValue(id);
      if (this.metrics.has(id) && value != null) {
        pairs.push([label, formatStatusNumber(value, this.format), String(value)]);
      }
    }
    this.show(...pairs);
    this.host.removeAttribute('title');
  }
}
