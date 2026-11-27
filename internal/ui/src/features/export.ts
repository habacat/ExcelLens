/**
 * ExcelLens Export Center. A bounded progress API controls a streaming Go job.
 * No workbook cell data is copied into the browser to build the export.
 */
import type { ActivityHandle, ApiClient, HttpError, ToastFn } from '../contracts/common.ts';
import type { SourceCapabilities } from '../contracts/formats.ts';
import { beginActivity } from '../core/activity.ts';
import { errorMessage, formatBytes } from '../core/display-format.ts';
import {
  requireAnchor,
  requireButton,
  requireDialog,
  requireEl,
  requireInput,
  requireSelect,
} from '../core/dom.ts';
import type { ClipboardSnapshot, ExportCenterClipboardBridge } from '../grid/grid-clipboard.ts';
import { MultiSelection, type SelectionEndpoints } from '../grid/multi-selection.ts';
import type { WorkbookState } from '../grid/state.ts';
import {
  decodeExportStartResponse,
  decodeExportStatusResponse,
  decodeReconcileReportOpenResponse,
} from '../core/http-decoders.generated.ts';

const CONTROL_IDS = [
  'exportScope',
  'exportFormat',
  'exportMode',
  'exportStyles',
  'exportBOM',
  'exportCSVGuard',
  'exportHidden',
  'exportRecoverable',
] as const;

type ExportControlId = (typeof CONTROL_IDS)[number];

interface GeneratedExportSpec {
  readonly path: string;
  readonly payload: Record<string, unknown> | FormData;
  readonly label: string;
}

interface ExportBeginRequest {
  readonly ticket: number;
  readonly book: string;
  readonly ctrl: AbortController;
}

/** @httpResponse ExportStartResponse */
export interface ExportStartResponse {
  readonly jobId: string;
}

/** @httpResponse ReconcileReportOpenResponse */
export interface ReconcileReportOpenResponse {
  readonly url: string;
}

export interface RecoveredExportStart {
  readonly jobId: string;
  readonly operation?: string;
}

export interface RecoveredExportItem {
  readonly book: string;
  readonly workbookKey: string;
  readonly file: string;
  readonly sheet?: string;
  readonly mode?: string;
  readonly scope?: string;
}

export interface ExportCenterTimers {
  setTimeout(fn: () => void, ms: number): ReturnType<typeof setTimeout> | number;
  clearTimeout(id: ReturnType<typeof setTimeout> | number): void;
}

export interface ExportCenterOptions {
  readonly state: WorkbookState;
  readonly api: ApiClient;
  readonly toast: ToastFn;
  readonly colName: (col: number) => string;
  readonly openDialog: (id: string) => void;
  readonly excludeThroughRow?: () => number;
  readonly sourceCapabilities?: () => Pick<SourceCapabilities, 'styled'>;
  readonly timers?: ExportCenterTimers;
}

function getHttpStatus(error: unknown): number | undefined {
  if (typeof error === 'object' && error !== null && 'status' in error) {
    const status = (error as HttpError).status;
    if (typeof status === 'number') return status;
  }
  return undefined;
}

function setControlDisabled(id: ExportControlId, disabled: boolean): void {
  const node = requireEl(id);
  if (node instanceof HTMLSelectElement || node instanceof HTMLInputElement) {
    node.disabled = disabled;
  }
}

export class ExportCenter implements ExportCenterClipboardBridge {
  public job = '';
  public running = false;
  public busy = false;
  public serial = 0;

  private readonly s: WorkbookState;
  private readonly api: ApiClient;
  private readonly toast: ToastFn;
  private readonly colName: (col: number) => string;
  private readonly openDialog: (id: string) => void;
  private readonly excludeThroughRow: () => number;
  private readonly sourceCapabilities: () => Pick<SourceCapabilities, 'styled'>;
  private readonly timers: ExportCenterTimers;

  private timer: ReturnType<typeof setTimeout> | number | undefined;
  private failures = 0;
  public snapshot = '';
  public resultSource = '';
  private pending: AbortController | null = null;
  private copySelection: ClipboardSnapshot | null = null;
  private activity: ActivityHandle | null = null;
  public generated: GeneratedExportSpec | null = null;

  constructor(options: ExportCenterOptions) {
    this.s = options.state;
    this.api = options.api;
    this.toast = options.toast;
    this.colName = options.colName;
    this.openDialog = options.openDialog;
    this.excludeThroughRow = options.excludeThroughRow ?? (() => 0);
    this.sourceCapabilities = options.sourceCapabilities ?? (() => ({ styled: false }));
    this.timers = options.timers ?? {
      setTimeout: (fn, ms) => setTimeout(fn, ms),
      clearTimeout: (id) => clearTimeout(id),
    };

    for (const id of CONTROL_IDS) {
      requireEl(id).addEventListener('change', () => this.refresh());
    }
    requireButton('exportViewReport').addEventListener('click', () => {
      void this.openReport();
    });
    requireButton('exportStartBtn').addEventListener('click', () => {
      void this.start();
    });
    requireButton('exportCancelBtn').addEventListener('click', () => {
      void this.cancel();
    });
    requireButton('exportFolderBtn').addEventListener('click', () => {
      void this.folder();
    });
    requireButton('exportRetryBtn').addEventListener('click', () => {
      this.failures = 0;
      void this.poll();
    });
  }

  async openReport(): Promise<void> {
    const job = this.job;
    const serial = this.serial;
    const popup = window.open('about:blank', '_blank');
    if (!popup) {
      this.toast('请允许打开明细标签页', 'warning');
      return;
    }
    popup.opener = null;
    try {
      const data = await this.api('/api/reconcile/report/open', {
        method: 'POST',
        body: JSON.stringify({ id: job }),
      }, decodeReconcileReportOpenResponse);
      if (job !== this.job || serial !== this.serial) {
        popup.close();
        return;
      }
      popup.location.assign(data.url);
    } catch (error: unknown) {
      popup.close();
      this.toast(errorMessage(error), 'error');
    }
  }

  clearResult(): void {
    requireButton('exportViewReport').hidden = true;
    requireEl('exportResult').hidden = true;
    const download = requireAnchor('exportDownload');
    download.removeAttribute('href');
    download.removeAttribute('download');
    requireEl('exportFileDetails').textContent = '';
    requireEl('exportSnapshot').textContent = '';
  }

  resetBook(): void {
    this.copySelection = null;
    this.activity?.end();
    this.activity = null;
    if (this.job && this.running) void this.cancelOwned(this.job);
    ++this.serial;
    if (this.timer !== undefined) {
      this.timers.clearTimeout(this.timer);
      this.timer = undefined;
    }
    this.pending?.abort();
    this.pending = null;
    this.job = '';
    this.running = false;
    this.busy = false;
    this.snapshot = '';
    this.resultSource = '';
    this.failures = 0;
    this.generated = null;
    this.clearResult();
    requireEl('exportProgress').hidden = true;
    requireButton('exportCancelBtn').hidden = true;
    requireButton('exportRetryBtn').hidden = true;
    requireDialog('exportDialog').close();
  }

  range(): SelectionEndpoints {
    if (this.copySelection) {
      const { start, end, visualStart, visualEnd } = this.copySelection;
      return { start, end, visualStart, visualEnd };
    }
    const area = MultiSelection.areas(this.s, true).at(-1);
    return area ? MultiSelection.points(area, this.colName) : { start: 'A1', end: 'A1' };
  }

  open(scope = '', selection: ClipboardSnapshot | null = null): void {
    if (!this.s.opened || !this.s.sheet) {
      this.toast('请先打开工作簿', 'warning');
      return;
    }
    const hasRange =
      this.s.range !== null &&
      (this.s.range.row1 !== this.s.range.row2 || this.s.range.col1 !== this.s.range.col2);
    if (!this.running && !this.busy) {
      this.generated = null;
      this.copySelection = selection;
    }
    const scopeEl = requireSelect('exportScope');
    if (scope && !this.running && !this.busy) scopeEl.value = scope;
    else if (!this.running) scopeEl.value = hasRange ? 'selection' : 'sheet';
    this.refresh();
    this.openDialog('exportDialog');
  }

  refresh(): void {
    const scopeEl = requireSelect('exportScope');
    const formatEl = requireSelect('exportFormat');
    const modeEl = requireSelect('exportMode');
    const stylesEl = requireInput('exportStyles');
    const bomEl = requireInput('exportBOM');
    const csvGuardEl = requireInput('exportCSVGuard');
    const hiddenEl = requireInput('exportHidden');

    const scope = scopeEl.value;
    const filtered = Boolean(this.s.filtered && this.s.filterId);
    const filteredOption = scopeEl.querySelector('[value="filtered"]');
    const visibleOption = scopeEl.querySelector('[value="visible"]');
    if (visibleOption instanceof HTMLOptionElement) visibleOption.disabled = !this.s.rowView;
    if (filteredOption instanceof HTMLOptionElement) filteredOption.disabled = !filtered;
    if (scope === 'filtered' && !filtered) scopeEl.value = 'sheet';
    if (scope === 'visible' && !this.s.rowView) scopeEl.value = 'sheet';

    const activeScope = scopeEl.value;
    if (activeScope === 'all' && !['xlsx', 'zipcsv'].includes(formatEl.value)) {
      formatEl.value = 'zipcsv';
    }
    if (!this.generated && activeScope !== 'all' && formatEl.value === 'zipcsv') {
      formatEl.value = 'csv';
    }
    const fmt = formatEl.value;
    const range = this.range();
    const sheet = this.s.sheet ?? '';
    const filteredRows = this.s.filterTotalsBySheet.get(sheet) ?? this.s.filterTotal;
    const rangeLabel = requireEl('exportRangeLabel');
    rangeLabel.textContent =
      activeScope === 'all'
        ? `${this.s.sheets.length} 个 Sheet`
        : activeScope === 'filtered'
          ? `已筛选：${filteredRows.toLocaleString()} 行`
          : activeScope === 'visible'
            ? `${sheet} · 当前可见行`
            : activeScope === 'selection'
              ? `${sheet} · ${range.start}:${range.end}`
              : `${sheet} · ${this.s.dimension || '已使用区域'}`;

    const styleOK = (fmt === 'xlsx' || fmt === 'html') && this.sourceCapabilities().styled;
    stylesEl.disabled = !styleOK;
    if (!styleOK) stylesEl.checked = false;
    bomEl.disabled = !['csv', 'zipcsv'].includes(fmt);
    csvGuardEl.disabled = !['csv', 'tsv', 'zipcsv'].includes(fmt);
    hiddenEl.disabled = activeScope !== 'all';

    const formulaUnsafe =
      fmt === 'xlsx' && ['selection', 'filtered', 'visible'].includes(activeScope);
    const formulaOption = modeEl.querySelector('[value="formula"]');
    if (formulaOption instanceof HTMLOptionElement) formulaOption.disabled = formulaUnsafe;
    if (formulaUnsafe && modeEl.value === 'formula') modeEl.value = 'values';

    let advisory = '';
    if (activeScope === 'all') {
      advisory = '导出整本工作簿时，尚未建立索引的 Sheet 会先建立索引；大表可能产生较大的文件。';
    } else if (activeScope === 'filtered') {
      advisory = '导出全部筛选匹配行，包含手动隐藏的行，保持排序。';
    } else if (activeScope === 'visible') {
      advisory = '导出当前可见行，包含可见的冻结标题，保持排序。';
    } else if (activeScope === 'selection') {
      advisory = '选区按左上角开始重新排布，不会改变原始工作簿。';
    }
    if (formulaUnsafe) {
      advisory += ' 为避免公式引用错位，该范围的 XLSX 只允许导出缓存值。';
    }
    if (fmt === 'html') {
      advisory += ' HTML 包含单元格颜色等基础样式，限制15万格；完整格式建议使用 XLSX。';
    }
    if (fmt === 'jsonl') {
      advisory += ' JSONL 每行保留来源行号、单元格地址、公式、缓存值及类型，适合程序审计。';
    }
    if (modeEl.value === 'formula' && fmt === 'xlsx') {
      advisory += ' 仅导出指定 Sheet 时，跨表引用可能指向未导出的 Sheet；如需保留依赖，请选择整个工作簿。';
    }
    requireEl('exportAdvisory').textContent = this.generated ? '' : advisory;

    const locked = this.running || this.busy;
    for (const id of CONTROL_IDS) {
      const disabled =
        locked ||
        Boolean(this.generated) ||
        (id === 'exportStyles' && !styleOK) ||
        (id === 'exportBOM' && !['csv', 'zipcsv'].includes(fmt)) ||
        (id === 'exportCSVGuard' && !['csv', 'tsv', 'zipcsv'].includes(fmt)) ||
        (id === 'exportHidden' && activeScope !== 'all');
      setControlDisabled(id, disabled);
    }
    if ((locked || this.generated) && this.snapshot) {
      rangeLabel.textContent = this.snapshot;
    }
    const startBtn = requireButton('exportStartBtn');
    startBtn.disabled = locked;
    startBtn.textContent = this.generated ? '重新导出' : '开始导出';
  }

  async start(): Promise<void> {
    if (this.running || this.busy) return;
    if (this.generated) {
      await this.begin(this.generated.path, this.generated.payload, this.generated.label);
      return;
    }
    const scope = requireSelect('exportScope').value;
    if (scope === 'selection' && this.copySelection && !this.copySelection.current()) {
      this.toast('选区已变化，请重新选择导出范围', 'warning');
      return;
    }
    if (scope === 'selection' && this.s.multiRanges.length) {
      this.toast('导出选区请保留一个连续区域', 'warning');
      return;
    }
    const range = this.range();
    const payload: Record<string, unknown> = {
      scope,
      sheet: this.s.sheet,
      workbookKey: this.s.workbookKey,
      ...range,
      filterId: this.s.filtered ? this.s.filterId : '',
      excludeThroughRow: this.s.filtered || this.s.rowView ? this.excludeThroughRow() : 0,
      format: requireSelect('exportFormat').value,
      mode: requireSelect('exportMode').value,
      styles: requireInput('exportStyles').checked,
      recoverable: requireInput('exportRecoverable').checked,
      bom: requireInput('exportBOM').checked,
      protectCSV: requireInput('exportCSVGuard').checked,
      includeHidden: requireInput('exportHidden').checked,
    };
    if (this.s.rowView && ['selection', 'filtered', 'visible'].includes(scope)) {
      payload.viewId = this.s.rowView.id;
    }
    if (this.s.book) payload.book = this.s.book;
    const ticket = ++this.serial;
    const book = this.s.workbookKey;
    const ctrl = new AbortController();
    this.pending = ctrl;
    await this.begin(
      '/api/export/start',
      payload,
      requireEl('exportRangeLabel').textContent,
      { ticket, book, ctrl },
    );
  }

  async startGenerated(
    path: string,
    payload: Record<string, unknown> | FormData,
    label: string,
  ): Promise<boolean> {
    if (this.running || this.busy) {
      this.toast('已有导出正在处理，请完成或取消后重试', 'warning');
      this.openDialog('exportDialog');
      return false;
    }
    this.generated = { path, payload, label };
    const formatValue =
      !(payload instanceof FormData) && typeof payload.format === 'string' && payload.format
        ? payload.format
        : 'csv';
    requireSelect('exportFormat').value = formatValue;
    requireSelect('exportScope').value = 'sheet';
    requireSelect('exportMode').value = 'values';
    this.snapshot = label;
    this.refresh();
    this.openDialog('exportDialog');
    await this.begin(path, payload, label);
    return true;
  }

  async begin(
    path: string,
    payload: Record<string, unknown> | FormData,
    label: string,
    request?: ExportBeginRequest,
  ): Promise<void> {
    const { ticket, book, ctrl } = request ?? {
      ticket: ++this.serial,
      book: this.s.workbookKey,
      ctrl: new AbortController(),
    };
    this.pending = ctrl;
    const current = (): boolean =>
      ticket === this.serial && book === this.s.workbookKey && !ctrl.signal.aborted;
    const activity = beginActivity('正在生成导出文件', { slot: 'exportActivity' });
    this.activity = activity;
    this.snapshot = label;
    this.failures = 0;
    this.busy = true;
    this.clearResult();
    this.refresh();
    const payloadSheet =
      !(payload instanceof FormData) && typeof payload.sheet === 'string' ? payload.sheet : '';
    this.resultSource = [this.s.fileName, payloadSheet, label].filter(Boolean).join(' · ');
    try {
      const upload = payload instanceof FormData;
      const started = await this.api(
        path,
        upload
          ? { method: 'POST', body: payload, signal: ctrl.signal }
          : {
              method: 'POST',
              headers: { 'Content-Type': 'application/json' },
              body: JSON.stringify(payload),
            },
        decodeExportStartResponse,
      );
      if (!current()) {
        void this.cancelOwned(started.jobId);
        return;
      }
      this.job = started.jobId;
      this.running = true;
      requireEl('exportProgress').hidden = false;
      requireButton('exportCancelBtn').hidden = false;
      await this.poll();
    } catch (e: unknown) {
      if (current()) {
        const msg = errorMessage(e);
        this.toast(`导出失败：${msg}`, 'error');
        requireEl('exportStage').textContent = `导出失败：${msg}`;
      }
    } finally {
      if (!this.running || ticket !== this.serial) activity?.end();
      if (ticket === this.serial) {
        this.busy = false;
        this.refresh();
        if (this.pending === ctrl) this.pending = null;
      }
    }
  }

  async cancelOwned(id: string): Promise<void> {
    if (!id) return;
    try {
      await this.api('/api/export/cancel', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ id }),
      });
    } catch {
      // The task may have completed or been removed by a source switch.
    }
  }

  adoptRecovered(start: RecoveredExportStart, item: RecoveredExportItem): boolean {
    if (
      this.running ||
      this.busy ||
      item.book !== this.s.book ||
      item.workbookKey !== this.s.workbookKey
    ) {
      return false;
    }
    ++this.serial;
    this.pending?.abort();
    if (this.timer !== undefined) {
      this.timers.clearTimeout(this.timer);
      this.timer = undefined;
    }
    this.generated = null;
    this.failures = 0;
    this.job = start.jobId;
    this.running = true;
    this.snapshot = `${item.file}${item.sheet ? ` · ${item.sheet}` : ''}`;
    this.resultSource = this.snapshot;
    this.activity = beginActivity('正在生成导出文件', { slot: 'exportActivity' });
    if (item.mode) requireSelect('exportFormat').value = item.mode;
    if (item.scope) requireSelect('exportScope').value = item.scope;
    requireInput('exportRecoverable').checked = true;
    this.clearResult();
    requireEl('exportProgress').hidden = false;
    requireButton('exportCancelBtn').hidden = false;
    this.refresh();
    this.openDialog('exportDialog');
    void this.poll();
    return true;
  }

  async poll(): Promise<void> {
    if (!this.job || !this.running) return;
    if (this.timer !== undefined) {
      this.timers.clearTimeout(this.timer);
      this.timer = undefined;
    }
    this.pending?.abort();
    const ctrl = new AbortController();
    this.pending = ctrl;
    const ticket = this.serial;
    const job = this.job;
    const book = this.s.workbookKey;
    const current = (): boolean =>
      ticket === this.serial &&
      job === this.job &&
      book === this.s.workbookKey &&
      !ctrl.signal.aborted;
    try {
      const data = await this.api(
        `/api/export/status?id=${encodeURIComponent(job)}`,
        { signal: ctrl.signal },
        decodeExportStatusResponse,
      );
      if (!current()) return;
      this.failures = 0;
      requireButton('exportRetryBtn').hidden = true;
      this.activity ??= beginActivity('正在生成导出文件', { slot: 'exportActivity' });
      this.activity?.job({
        doneRows: data.doneRows,
        totalRows: data.totalRows,
        progress: data.progress,
        stage: data.stage,
      });
      const done = data.doneRows || 0;
      const total = data.totalRows || 0;
      const pct = Math.max(0, Math.min(100, data.progress || 0));
      requireEl('exportBar').style.width = `${pct}%`;
      requireEl('exportStage').textContent = data.stage || '处理中';
      requireEl('exportPercent').textContent = `${pct.toFixed(1)}%`;
      requireEl('exportDetails').textContent = `${done.toLocaleString()} / ${total.toLocaleString()} 行 · 已用 ${((data.elapsedMs || 0) / 1000).toFixed(1)} 秒`;
      if (data.status === 'done') {
        this.running = false;
        requireButton('exportCancelBtn').hidden = true;
        requireEl('exportResult').hidden = false;
        requireButton('exportViewReport').hidden = !data.auditReport;
        const download = requireAnchor('exportDownload');
        if (data.downloadUrl) download.href = data.downloadUrl;
        download.setAttribute('download', data.fileName || 'ExcelLens-export');
        requireEl('exportFileDetails').textContent =
          `${data.fileName} · ${this.formatBytes(data.size)}`;
        requireEl('exportSnapshot').textContent = this.resultSource
          ? `来源：${this.resultSource}`
          : '';
        this.refresh();
        return;
      }
      if (data.status === 'failed' || data.status === 'cancelled') {
        this.running = false;
        requireButton('exportCancelBtn').hidden = true;
        requireEl('exportStage').textContent =
          data.status === 'cancelled' ? '导出已取消' : `导出失败：${data.error || '未知错误'}`;
        this.refresh();
        return;
      }
      this.timer = this.timers.setTimeout(() => {
        void this.poll();
      }, 340);
    } catch (err: unknown) {
      if (!current()) return;
      if (getHttpStatus(err) === 404) {
        this.running = false;
        requireButton('exportCancelBtn').hidden = true;
        requireButton('exportRetryBtn').hidden = true;
        requireEl('exportStage').textContent = '导出任务已失效，请重新导出';
        this.refresh();
        return;
      }
      requireEl('exportStage').textContent = `状态读取失败：${errorMessage(err)}`;
      requireButton('exportRetryBtn').hidden = false;
      // A transport failure says nothing about the server job's completion.
      // Keep cancellation and the task identity, with bounded automatic retries.
      if (++this.failures <= 3) {
        this.timer = this.timers.setTimeout(
          () => {
            void this.poll();
          },
          Math.min(8000, 1000 * 2 ** (this.failures - 1)),
        );
      }
      this.refresh();
    } finally {
      if (ticket === this.serial && (!this.running || this.failures > 3)) {
        this.activity?.end();
        this.activity = null;
      }
      if (this.pending === ctrl) this.pending = null;
    }
  }

  formatBytes(value: unknown): string {
    return formatBytes(value);
  }

  async cancel(): Promise<void> {
    if (!this.job || !this.running) return;
    const ticket = this.serial;
    const job = this.job;
    try {
      await this.api('/api/export/cancel', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ id: job }),
      });
      if (ticket === this.serial && job === this.job) {
        requireEl('exportStage').textContent = '正在取消...';
        this.failures = 0;
        void this.poll();
      }
    } catch (e: unknown) {
      if (ticket === this.serial && job === this.job) {
        this.toast(errorMessage(e), 'error');
      }
    }
  }

  async folder(): Promise<void> {
    try {
      await this.api('/api/export/folder', { method: 'POST' });
    } catch (e: unknown) {
      this.toast(errorMessage(e), 'error');
    }
  }
}
