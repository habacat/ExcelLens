/**
 * Explicit, source-bound range profiling dialog. Exact statistics stay in the
 * accepted model; display preferences and layout changes never re-query the source.
 */
import type { AnalysisDisplayOptions } from '../contracts/analysis.ts';
import type { ApiClient, ToastFn } from '../contracts/common.ts';
import type { ProfileResponse } from '../contracts/features.ts';
import type { ProfileLimits, SourceCapabilities } from '../contracts/formats.ts';
import type { IndexStartResponse, IndexStatusResponse } from '../contracts/workbook.ts';
import { errorMessage } from '../core/display-format.ts';
import {
  requireButton,
  requireDialog,
  requireEl,
  requireInput,
} from '../core/dom.ts';
import { selectionPoints, type SelectionEndpoints } from '../grid/multi-selection.ts';
import type { WorkbookState } from '../grid/state.ts';
import { validateProfileResponse, type ProfileRequest } from './profile-model.ts';

export interface ProfileNumberFormatter {
  get(): AnalysisDisplayOptions;
  format(
    cell: { readonly exact: string; readonly display: string },
    settings?: AnalysisDisplayOptions,
  ): string;
}

export interface ProfileCenterOptions {
  readonly state: WorkbookState;
  readonly api: ApiClient;
  readonly toast: ToastFn;
  readonly openDialog: (id: string) => void;
  readonly navigateToCell: (
    sheet: string,
    ref: string,
    pushHistory?: boolean,
    guard?: () => boolean,
  ) => Promise<unknown>;
  readonly colName: (col: number) => string;
  readonly limits: ProfileLimits;
  readonly source: () => SourceCapabilities;
  readonly canRead?: () => boolean;
  readonly excludeThroughRow?: () => number;
  readonly numbers?: ProfileNumberFormatter;
}

interface ProfileBinding {
  readonly key: string;
  readonly index: IndexStartResponse | IndexStatusResponse | null;
}

interface ProfileSelectionState extends SelectionEndpoints {
  readonly filterId: string;
  readonly viewId: string;
  readonly excludeThroughRow: number;
  readonly epoch: number;
}

const DEFAULT_DISPLAY_OPTIONS: AnalysisDisplayOptions = {
  groupThousands: true,
  scientific: true,
  scientificExponent: 12,
};

const fallbackFormatter: ProfileNumberFormatter = {
  get: () => ({ ...DEFAULT_DISPLAY_OPTIONS }),
  format: (cell) => cell.display || '—',
};

function makeEl<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  cls = '',
  text = '',
): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag);
  node.className = cls;
  node.textContent = text;
  return node;
}

function isAbortError(error: unknown): boolean {
  return error instanceof Error && error.name === 'AbortError';
}

export class ProfileCenter {
  private readonly s: WorkbookState;
  private readonly api: ApiClient;
  private readonly toast: ToastFn;
  private readonly openDialog: (id: string) => void;
  private readonly navigate: ProfileCenterOptions['navigateToCell'];
  private readonly colName: (col: number) => string;
  private readonly limits: ProfileLimits;
  private readonly source: () => SourceCapabilities;
  private readonly canRead: () => boolean;
  private readonly excludeThroughRow: () => number;
  private readonly numbers: ProfileNumberFormatter;

  private pending: AbortController | null = null;
  private serial = 0;
  private selection: ProfileSelectionState | null = null;
  private bound: ProfileBinding | null = null;
  public page: ProfileResponse | null = null;
  private accepted: ProfileRequest | null = null;

  constructor(options: ProfileCenterOptions) {
    this.s = options.state;
    this.api = options.api;
    this.toast = options.toast;
    this.openDialog = options.openDialog;
    this.navigate = options.navigateToCell;
    this.colName = options.colName;
    this.limits = options.limits;
    this.source = options.source;
    this.canRead = options.canRead ?? (() => true);
    this.excludeThroughRow = options.excludeThroughRow ?? (() => 0);
    this.numbers = options.numbers ?? fallbackFormatter;

    requireButton('profileAnalyzeBtn').addEventListener('click', () => {
      void this.analyze();
    });
    requireButton('profileCancelBtn').addEventListener('click', () => {
      this.cancel();
      this.status('已取消分析');
    });
    const dialog = requireDialog('profileDialog');
    dialog.addEventListener('close', () => {
      if (!dialog.open) this.clear();
    });
    dialog.addEventListener('cancel', () => this.cancel());
    for (const id of ['profileStart', 'profileEnd', 'profileFiltered', 'profileDistribution']) {
      requireInput(id).addEventListener(
        id === 'profileStart' || id === 'profileEnd' ? 'input' : 'change',
        () => this.inputsChanged(),
      );
    }
    document.addEventListener('excellens:analysisnumberchange', () => {
      if (this.page && this.accepted && this.same(this.bound) && dialog.open) {
        this.renderValidated(this.page, this.accepted);
      }
    });
  }

  private binding(): ProfileBinding {
    return {
      key: JSON.stringify([
        this.s.workbookKey,
        this.s.book,
        this.s.sheet,
        this.s.sheetEpoch,
        this.s.rowView?.id ?? '',
        this.s.filtered ? this.s.filterId : '',
        this.s.indexReady,
        this.excludeThroughRow(),
      ]),
      index: this.s.index,
    };
  }

  private same(bound: ProfileBinding | null): boolean {
    const now = this.binding();
    return bound !== null && now.key === bound.key && now.index === bound.index;
  }

  changed(): void {
    if (this.bound && !this.same(this.bound)) this.resetBook();
  }

  cancel(): void {
    ++this.serial;
    this.pending?.abort();
    this.pending = null;
    requireButton('profileAnalyzeBtn').disabled = false;
    requireButton('profileCancelBtn').hidden = true;
    requireEl('profileDetail').setAttribute('aria-busy', 'false');
  }

  private clear(): void {
    this.cancel();
    this.bound = null;
    this.selection = null;
    this.page = null;
    this.accepted = null;
    for (const id of ['profileSummary', 'profileDetail']) {
      const node = requireEl(id);
      node.replaceChildren();
      node.hidden = true;
    }
    this.status('');
  }

  resetBook(): void {
    this.clear();
    const dialog = requireDialog('profileDialog');
    if (dialog.open) dialog.close();
  }

  private inputsChanged(): void {
    this.cancel();
    this.page = null;
    this.accepted = null;
    for (const id of ['profileSummary', 'profileDetail']) {
      const node = requireEl(id);
      node.replaceChildren();
      node.hidden = true;
    }
    this.status('');
  }

  private status(text: string, error = false): void {
    const pending = requireEl('profilePending');
    pending.textContent = text;
    pending.hidden = !text;
    pending.setAttribute('role', error ? 'alert' : 'status');
  }

  enabled(): boolean {
    return (
      this.s.opened &&
      Boolean(this.s.sheet) &&
      Boolean(this.s.book) &&
      Boolean(this.s.rowView?.id) &&
      this.s.indexReady &&
      !this.s.opening &&
      !this.s.sheetLoadingEpoch &&
      !this.s.rowViewFailure &&
      this.canRead()
    );
  }

  open(): void {
    if (!this.enabled()) {
      this.toast('请先完成工作表加载', 'warning');
      return;
    }
    if (this.s.multiRanges.length) {
      this.toast('详细分析请选择一个连续区域', 'warning');
      return;
    }
    this.clear();
    this.bound = this.binding();
    const r = this.s.range;
    if (r) {
      const points = selectionPoints(r, this.colName);
      requireInput('profileStart').value = points.start;
      requireInput('profileEnd').value = points.end;
      if (this.s.filtered || this.s.rowView) {
        this.selection = {
          ...points,
          filterId: this.s.filtered ? this.s.filterId : '',
          viewId: this.s.rowView?.id ?? '',
          excludeThroughRow: this.excludeThroughRow(),
          epoch: this.s.sheetEpoch,
        };
      }
    } else {
      const col = this.colName(this.s.selected?.col || 1);
      requireInput('profileStart').value = `${col}1`;
      requireInput('profileEnd').value = `${col}${this.s.usedMaxRow || 1}`;
    }
    const filteredBox = requireInput('profileFiltered');
    filteredBox.checked = Boolean(this.s.filtered && this.s.filterId);
    filteredBox.disabled = !this.s.filtered;
    this.openDialog('profileDialog');
  }

  public input(): ProfileRequest {
    const start = requireInput('profileStart').value.trim();
    const end = requireInput('profileEnd').value.trim();
    const filterId = requireInput('profileFiltered').checked ? this.s.filterId : '';
    const selected = this.selection;
    const matchSelection =
      selected !== null &&
      selected.epoch === this.s.sheetEpoch &&
      selected.start === start &&
      selected.end === end &&
      selected.filterId === filterId;
    return {
      workbookKey: this.s.workbookKey,
      book: this.s.book,
      sheet: this.s.sheet ?? '',
      bindingViewId: this.s.rowView?.id ?? '',
      bindingFilterId: this.s.filtered ? this.s.filterId : '',
      start,
      end,
      filterId,
      includeDistribution: requireInput('profileDistribution').checked,
      ...(matchSelection
        ? {
            visualStart: selected.visualStart,
            visualEnd: selected.visualEnd,
            excludeThroughRow: selected.excludeThroughRow,
            ...(selected.viewId ? { viewId: selected.viewId } : {}),
          }
        : {}),
    };
  }

  async analyze(): Promise<void> {
    this.changed();
    const dialog = requireDialog('profileDialog');
    if (!this.enabled() || !dialog.open || !this.bound) return;
    this.cancel();
    const ctrl = new AbortController();
    const ticket = this.serial;
    const bound = this.bound;
    const input = this.input();
    const inputKey = JSON.stringify(input);
    this.pending = ctrl;
    const current = (): boolean =>
      !ctrl.signal.aborted &&
      ticket === this.serial &&
      this.same(bound) &&
      dialog.open &&
      inputKey === JSON.stringify(this.input());
    requireButton('profileAnalyzeBtn').disabled = true;
    requireButton('profileCancelBtn').hidden = false;
    requireEl('profileDetail').setAttribute('aria-busy', 'true');
    this.status(input.includeDistribution ? '正在计算分布…' : '正在分析…');
    try {
      const body = JSON.stringify(input);
      if (new TextEncoder().encode(body).length > this.limits.requestBytes) {
        throw new Error('分析请求超过容量');
      }
      const data = await this.api('/api/profile', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body,
        signal: ctrl.signal,
      });
      if (current()) {
        this.render(data, input);
        this.status('');
      }
    } catch (error: unknown) {
      if (current() && !isAbortError(error)) {
        this.status(errorMessage(error), true);
      }
    } finally {
      if (this.pending === ctrl) {
        this.pending = null;
        requireButton('profileAnalyzeBtn').disabled = false;
        requireButton('profileCancelBtn').hidden = true;
        requireEl('profileDetail').setAttribute('aria-busy', 'false');
      }
    }
  }

  private format(exact: string): string {
    return exact === '' ? '—' : this.numbers.format({ exact, display: exact });
  }

  private render(data: unknown, request: ProfileRequest): void {
    const d = validateProfileResponse(data, request, this.limits, this.source());
    this.renderValidated(d, request);
  }

  private renderValidated(d: ProfileResponse, request: ProfileRequest): void {
    const summary = document.createDocumentFragment();
    const detail = document.createDocumentFragment();

    const fields: [string, number | string][] = [
      ['总格数', d.total],
      ['数值', d.number],
      ['空白', d.blank],
      ['不同值', `${d.uniqueTruncated ? '≥ ' : ''}${d.unique.toLocaleString('zh-CN')}`],
    ];
    const extras: readonly (readonly [string, number])[] = [
      ['其他值', d.text],
      ['公式', d.formulas],
      ['缺少公式结果', d.cacheMissing],
      ['错误', d.errors],
    ];
    for (const [label, value] of extras) {
      if (value) fields.push([label, value]);
    }
    for (const [label, value] of fields) {
      const box = makeEl('div', 'profile-stat');
      box.append(
        makeEl('div', 'profile-number', typeof value === 'number' ? value.toLocaleString('zh-CN') : value),
        makeEl('div', 'profile-caption', label),
      );
      summary.append(box);
    }

    const heading = (title: string): void => {
      detail.append(makeEl('h4', '', title));
    };
    const line = (label: string, value: string, numeric = false): void => {
      const node = makeEl('div', 'profile-data-line');
      const text = numeric ? this.format(value) : value;
      const code = makeEl('code', '', text);
      if (numeric) code.title = value;
      node.append(makeEl('span', '', label), code);
      detail.append(node);
    };

    heading('数值统计');
    const numericPairs: readonly (readonly [string, string])[] = [
      ['合计', d.sumExact],
      ['平均', d.averageExact],
      ['最小', d.minimumExact],
      ['最大', d.maximumExact],
    ];
    for (const [label, value] of numericPairs) line(label, value, true);

    if (d.distribution) {
      heading('分位数');
      if (!d.number) detail.append(makeEl('p', '', '无可用数值'));
      for (const q of d.distribution.quantiles) {
        line(q.percent === 50 ? '中位数' : `${q.percent}%分位`, q.value, true);
      }
      if (d.distribution.bins.length) {
        heading('数值分布');
        const bins = makeEl('div', 'profile-bins');
        for (const bin of d.distribution.bins) {
          let lower = this.format(bin.lower);
          let upper = this.format(bin.upper);
          if (lower === upper && bin.lower !== bin.upper) {
            const settings: AnalysisDisplayOptions = { ...this.numbers.get(), scientific: false };
            lower = this.numbers.format({ exact: bin.lower, display: bin.lower }, settings);
            upper = this.numbers.format({ exact: bin.upper, display: bin.upper }, settings);
          }
          const row = makeEl('div', 'profile-bin');
          const rangeText = `[${lower}, ${upper}${bin.upperInclusive ? ']' : ')'}`;
          const label = makeEl('span', 'profile-bin-range', rangeText);
          label.title = `[${bin.lower}, ${bin.upper}${bin.upperInclusive ? ']' : ')'}`;
          const meter = makeEl('meter');
          meter.min = 0;
          meter.max = d.number;
          meter.value = bin.count;
          meter.setAttribute('aria-label', rangeText);
          row.append(
            label,
            meter,
            makeEl('span', 'profile-bin-count', bin.count.toLocaleString('zh-CN')),
          );
          bins.append(row);
        }
        detail.append(bins);
      }
    }

    if (d.topValues.length) {
      heading(d.uniqueTruncated ? '常见值（部分）' : '常见值');
      for (const value of d.topValues) {
        line(value.value || '（空白）', `${value.count.toLocaleString('zh-CN')} 次`);
      }
    }

    const refs = (title: string, items: readonly string[]): void => {
      if (!items.length) return;
      heading(title);
      const holder = makeEl('div');
      for (const ref of items) {
        const button = makeEl('button', 'profile-ref', ref);
        button.type = 'button';
        button.addEventListener('click', () => {
          if (!this.same(this.bound)) return;
          const bound = this.bound;
          requireDialog('profileDialog').close();
          void this.navigate(d.sheet, ref, false, () => this.same(bound));
        });
        holder.append(button);
      }
      detail.append(holder);
    };
    refs('错误单元格', d.errorRefs);
    refs('缺少公式结果', d.missingRefs);
    if (d.warning) detail.append(makeEl('p', 'profile-warning', d.warning));

    this.page = d;
    this.accepted = request;
    const summaryEl = requireEl('profileSummary');
    const detailEl = requireEl('profileDetail');
    summaryEl.replaceChildren(summary);
    summaryEl.hidden = false;
    detailEl.replaceChildren(detail);
    detailEl.hidden = false;
  }
}
