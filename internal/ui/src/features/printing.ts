/**
 * Print preview dialog controller, page setup form, pagination navigation,
 * and printable HTML manuscript exporter.
 */
import type { ApiClient, ToastFn } from '../contracts/common.ts';
import type { PrintingLimits, RichTextLimits, SourceCapabilities } from '../contracts/formats.ts';
import { AsyncJobs, type AsyncJobStatusEnvelope } from '../core/async-jobs.ts';
import { errorMessage, isComposing } from '../core/display-format.ts';
import {
  byId,
  requireButton,
  requireEl,
  requireInput,
  requireSelect,
} from '../core/dom.ts';
import type { WorkbookState } from '../grid/state.ts';
import {
  buildPrintOptions,
  printUnits,
  validatePrintPage,
  validatePrintSummary,
  type PrintBinding,
  type PrintFormInput,
  type PrintPageResponse,
  type PrintSummaryResponse,
  type RenderedPrintPage,
} from './printing-model.ts';
import { parsePageRange, PrintOutput } from './printing-output.ts';
import { PrintingRenderer } from './printing-render.ts';

export interface PrintingOptions {
  readonly state: WorkbookState;
  readonly api: ApiClient;
  readonly toast: ToastFn;
  readonly source: () => SourceCapabilities;
  readonly limits: PrintingLimits;
  readonly richLimits?: RichTextLimits | null;
  readonly fontStack: (name: string) => string;
  readonly sleep?: (ms: number) => Promise<void>;
}

interface PrintingJobStatus extends AsyncJobStatusEnvelope<PrintingJobStatus> {
  readonly workbookKey?: string;
  readonly book?: string;
  readonly sheet?: string;
}

function makeEl<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  cls = '',
  text = '',
): HTMLElementTagNameMap[K] {
  const n = document.createElement(tag);
  n.className = cls;
  n.textContent = text;
  return n;
}

function requireForm(id: string): HTMLFormElement {
  const node = requireEl(id);
  if (!(node instanceof HTMLFormElement)) {
    throw new Error(`Expected HTMLFormElement for #${id}`);
  }
  return node;
}

function requireDetails(id: string): HTMLDetailsElement {
  const node = requireEl(id);
  if (!(node instanceof HTMLDetailsElement)) {
    throw new Error(`Expected HTMLDetailsElement for #${id}`);
  }
  return node;
}

function isAbortError(error: unknown): boolean {
  return error instanceof Error && error.name === 'AbortError';
}

const ORIGIN_LABELS: Readonly<Record<string, string>> = {
  source: '源设置',
  'preview-override': '本次覆盖',
  'preview-default': '预览缺省',
  'schema-default': '格式缺省',
  'indexed-data-range': '源数据范围',
  'office-compatible-source': '源手工分页',
};

const SETTING_LABELS: readonly (readonly [string, string])[] = [
  ['paper', '纸张'],
  ['orientation', '方向'],
  ['areas', '打印区域'],
  ['repeatRows', '重复行'],
  ['repeatColumns', '重复列'],
  ['firstPageNumber', '起始页码'],
];

const GROUP_LABELS: Readonly<Record<string, string>> = {
  pageSetup: '源页面设置',
  pageMargins: '源页边距',
  pageSetUpPr: '源适配设置',
  printOptions: '源打印选项',
  headerFooter: '源页眉页脚定义',
};

const MARGIN_NAMES: readonly (readonly [string, string])[] = [
  ['left', '左'],
  ['right', '右'],
  ['top', '上'],
  ['bottom', '下'],
  ['header', '页眉'],
  ['footer', '页脚'],
];

export class Printing {
  private readonly s: WorkbookState;
  private readonly api: ApiClient;
  private readonly toast: ToastFn;
  private readonly source: () => SourceCapabilities;
  private readonly limits: PrintingLimits;
  private readonly renderer: PrintingRenderer;
  private readonly jobs: AsyncJobs<PrintingJobStatus, PrintingJobStatus>;
  public readonly dialog: HTMLDialogElement;

  private epoch = 0;
  private request = 0;
  private pageRequest = 0;
  public job = '';
  public index = 0;
  public busy = false;
  private loading = false;
  public dirty = false;
  private bound = '';
  private noticeCount = 0;
  private summary: PrintSummaryResponse | null = null;
  public page: PrintPageResponse | null = null;
  public rendered: RenderedPrintPage | null = null;
  private controller: AbortController | null = null;
  private pageController: AbortController | null = null;
  private outputController: AbortController | null = null;
  private outputJobs: AsyncJobs<PrintingJobStatus, PrintingJobStatus> | null = null;
  public readonly resize?: ResizeObserver;

  constructor(options: PrintingOptions) {
    this.s = options.state;
    this.api = options.api;
    this.toast = options.toast;
    this.source = options.source;
    this.limits = options.limits;
    if (!options.limits.pages || !options.limits.domPixels) {
      throw new Error('分页预览容量不可用');
    }
    const sleep = options.sleep ?? ((ms) => new Promise<void>((resolve) => setTimeout(resolve, ms)));
    this.renderer = new PrintingRenderer({
      limits: options.limits,
      richLimits: options.richLimits,
      fontStack: options.fontStack,
    });
    this.dialog = this.create();
    this.jobs = new AsyncJobs<PrintingJobStatus, PrintingJobStatus>({
      api: this.api,
      identity: () => this.identity(),
      requestIdentity: () => this.s.workbookKey,
      sleep,
      endpoint: '/api/printing',
      label: '分页预览',
      maxPolls: 0,
      resultFromStatus: (status) => status,
      onProgress: (status) => {
        this.validSource(status);
        this.status(status.stage || '正在准备预览');
      },
    });

    requireButton('printPreviewOpenBtn').addEventListener('click', () => {
      void this.open();
    });
    requireButton('printPreviewClose').addEventListener('click', () => this.close());
    this.dialog.addEventListener('cancel', (event: Event) => {
      event.preventDefault();
      this.close();
    });
    this.dialog.addEventListener('close', () => {
      if (!this.dialog.open) this.stop(true);
    });
    requireButton('printPreviewUpdate').addEventListener('click', () => {
      void this.refresh();
    });
    requireButton('printPreviewCancel').addEventListener('click', () => this.cancel());
    const form = requireForm('printSettingsForm');
    form.addEventListener('submit', (event: SubmitEvent) => {
      event.preventDefault();
      void this.refresh();
    });
    form.addEventListener('input', () => this.changed());
    form.addEventListener('change', () => this.changed());
    requireButton('printPreviewPrev').addEventListener('click', () => {
      void this.go(this.index - 1);
    });
    requireButton('printPreviewNext').addEventListener('click', () => {
      void this.go(this.index + 1);
    });
    const pageInput = requireInput('printPreviewPage');
    pageInput.addEventListener('change', () => {
      void this.goInput();
    });
    pageInput.addEventListener('keydown', (event: KeyboardEvent) => {
      if (!isComposing(event) && event.key === 'Enter') {
        event.preventDefault();
        void this.goInput();
      }
    });
    requireSelect('printOutputMode').addEventListener('change', () => this.controls());
    requireButton('printOutput').addEventListener('click', () => {
      void this.exportPaper();
    });
    requireSelect('printScreenZoom').addEventListener('change', () => this.zoom());

    this.dialog.addEventListener('keydown', (event: KeyboardEvent) => {
      if (
        (event.target instanceof Element && event.target.closest('input,textarea,select')) ||
        event.ctrlKey ||
        event.altKey ||
        event.metaKey
      ) {
        return;
      }
      const totalPages = this.summary?.summary.pages || 1;
      const targetPage =
        event.key === 'PageDown'
          ? this.index + 1
          : event.key === 'PageUp'
            ? this.index - 1
            : event.key === 'Home'
              ? 0
              : event.key === 'End'
                ? totalPages - 1
                : null;
      if (targetPage !== null) {
        event.preventDefault();
        void this.go(targetPage);
      }
    });

    document.addEventListener('keydown', (event: KeyboardEvent) => {
      if ((event.ctrlKey || event.metaKey) && !event.altKey && event.key.toLowerCase() === 'p' && this.s.opened) {
        event.preventDefault();
        void this.open();
      }
    });

    if (typeof ResizeObserver !== 'undefined') {
      this.resize = new ResizeObserver(() => this.zoom());
      this.resize.observe(requireEl('printPreviewCanvas'));
    }
    this.fields();
    this.controls();
  }

  private create(): HTMLDialogElement {
    const d = makeEl('dialog', 'float-dialog printing-dialog');
    d.id = 'printPreviewDialog';
    d.setAttribute('aria-labelledby', 'printPreviewTitle');
    d.innerHTML = `<div class="dialog-head"><div id="printPreviewTitle" class="dialog-title">分页预览</div><button id="printPreviewClose" class="dialog-close" type="button" aria-label="关闭分页预览">×</button></div>
        <p id="printPreviewSource" class="print-source"></p><div class="print-main"><details id="printSettings" class="print-settings" open><summary>页面设置</summary><form id="printSettingsForm">
        <label>纸张<select id="printPaper" class="field-control"><option value="source">按源设置</option><option value="a4">A4</option><option value="a3">A3</option><option value="letter">Letter</option><option value="legal">Legal</option><option value="custom">自定义尺寸</option></select></label>
        <div id="printCustomPaper" class="print-pair" hidden><label>宽（mm）<input id="printPaperWidth" class="field-control" value="210" inputmode="decimal"></label><label>高（mm）<input id="printPaperHeight" class="field-control" value="297" inputmode="decimal"></label></div>
        <label>方向<select id="printOrientation" class="field-control"><option value="">按源设置</option><option value="portrait">纵向</option><option value="landscape">横向</option></select></label>
        <label>打印缩放<select id="printScaleMode" class="field-control"><option value="source">按源设置</option><option value="percent">指定比例</option><option value="fit">适配页数</option></select></label>
        <label id="printPercentRow" hidden>比例（%）<input id="printPercent" class="field-control" value="100" inputmode="decimal"></label>
        <div id="printFitRow" class="print-pair" hidden><label>横向页数<input id="printFitWidth" class="field-control" value="1" inputmode="numeric"></label><label>纵向页数<input id="printFitHeight" class="field-control" value="0" inputmode="numeric"></label><small>0 表示该方向不限。</small></div>
        <label>打印区域<input id="printArea" class="field-control" placeholder="A1:H50, J1:L50（留空按原表）" autocomplete="off"></label>
        <details class="print-advanced"><summary>更多页面设置</summary><label>重复行<input id="printRepeatRows" class="field-control" placeholder="留空按源，如 1:2"></label><label class="print-toggle"><input id="printClearRepeatRows" type="checkbox">不重复行</label><label>重复列<input id="printRepeatColumns" class="field-control" placeholder="留空按源，如 A:B"></label><label class="print-toggle"><input id="printClearRepeatColumns" type="checkbox">不重复列</label>
        <label>页边距<select id="printMarginMode" class="field-control"><option value="source">按源设置</option><option value="custom">自定义（mm）</option></select></label><div id="printMargins" class="print-pair" hidden></div>
        <label>页序<select id="printPageOrder" class="field-control"><option value="">按源设置</option><option value="downThenOver">先向下，后向右</option><option value="overThenDown">先向右，后向下</option></select></label><label>起始页码<input id="printFirstPage" class="field-control" placeholder="留空按源" inputmode="numeric"></label>
        <div class="print-pair"><label>缺省行高（磅）<input id="printRowHeight" class="field-control" placeholder="留空按源" inputmode="decimal"></label><label>缺省列宽（磅）<input id="printColumnWidth" class="field-control" placeholder="留空按源" inputmode="decimal"></label></div><label class="print-toggle"><input id="printIgnoreBreaks" type="checkbox">忽略源手工分页</label></details>
        <label class="print-toggle"><input id="printUseDefaults" type="checkbox" checked>补足缺失的页面设置</label><button id="printPreviewUpdate" class="primary-button" type="button">更新预览</button><p id="printPendingNote" class="print-pending" hidden>设置已更改，点击更新预览。</p></form></details>
        <section class="print-view"><div class="print-page-tools"><button id="printPreviewPrev" class="mini-button" type="button" aria-label="上一页">上一页</button><label class="print-page-number">第<input id="printPreviewPage" class="field-control" type="number" min="1" value="1" aria-label="预览页序">页<span id="printPreviewCount">/ 0</span></label><button id="printPreviewNext" class="mini-button" type="button" aria-label="下一页">下一页</button><label class="print-screen-zoom">屏幕缩放<select id="printScreenZoom" class="field-control"><option value="fit">适合页面</option><option value="width">适合宽度</option><option value="50">50%</option><option value="75">75%</option><option value="100">100%</option><option value="125">125%</option><option value="150">150%</option><option value="200">200%</option></select></label></div><p id="printEffective" class="print-effective"></p><div id="printPreviewCanvas" class="print-canvas" tabindex="0" aria-label="纸张预览，PageUp和PageDown翻页"><div id="printPaperWrap" class="print-paper-wrap"></div><p id="printPreviewEmpty" class="print-empty">正在准备预览…</p></div><p id="printPageCaption" class="print-caption"></p></section></div>
        <details id="printSourceDetails" class="print-source-details"><summary>页面详情</summary><ul id="printWarnings"></ul><div id="printRawSettings"></div></details><div class="print-footer"><select id="printOutputMode" class="field-control" aria-label="底稿页段"><option value="current">当前页</option><option value="range">指定页段</option><option value="sheet">本表全部页</option><option value="book">整本可见表</option></select><input id="printOutputRange" class="field-control" placeholder="1-3,5" aria-label="底稿页段范围" hidden><button id="printOutput" class="mini-button" type="button">导出打印底稿</button><p id="printPreviewStatus" role="status"></p><button id="printPreviewCancel" class="mini-button" type="button" hidden>取消</button></div>`;
    document.body.append(d);
    const marginsHost = requireEl('printMargins');
    for (const [key, label] of MARGIN_NAMES) {
      const node = makeEl('label', '', `${label}（mm）`);
      const input = makeEl('input', 'field-control');
      input.id = `printMargin${key.slice(0, 1).toUpperCase()}${key.slice(1)}`;
      input.inputMode = 'decimal';
      const defaultVal = key === 'header' || key === 'footer' ? '8' : '20';
      input.value = defaultVal;
      input.defaultValue = defaultVal;
      node.append(input);
      marginsHost.append(node);
    }
    return d;
  }

  private identity(): string {
    return JSON.stringify([
      this.s.workbookKey,
      this.s.book,
      this.s.sheet,
      this.s.sheetEpoch,
      this.epoch,
    ]);
  }

  private binding(): PrintBinding {
    return {
      workbookKey: this.s.workbookKey,
      book: this.s.book,
      sheet: this.s.sheet ?? '',
    };
  }

  enabled(): boolean {
    return (
      this.s.opened &&
      this.s.indexReady &&
      Boolean(this.s.sheet) &&
      !this.s.opening &&
      !this.s.sheetLoadingEpoch &&
      this.source().printMetadata
    );
  }

  private validSource(value: Pick<PrintingJobStatus, 'workbookKey' | 'book' | 'sheet'>): void {
    if (
      value.workbookKey !== this.s.workbookKey ||
      value.book !== this.s.book ||
      value.sheet !== this.s.sheet
    ) {
      throw new Error('预览来源已切换，请重新准备');
    }
  }

  private release(id: string): void {
    if (!id) return;
    this.api('/api/printing/clear', {
      method: 'POST',
      body: JSON.stringify({ id }),
    }).catch(() => {
      // Best-effort cleanup of abandoned preview job.
    });
  }

  private status(text: string, error = false): void {
    const el = requireEl('printPreviewStatus');
    el.textContent = text;
    el.classList.toggle('is-error', error);
    el.setAttribute('role', error ? 'alert' : 'status');
  }

  controls(): void {
    const enabled = this.enabled();
    const pending = this.busy || this.loading || this.outputController !== null;
    const pages = this.summary?.summary.pages ?? 0;
    requireButton('printPreviewOpenBtn').disabled = !enabled;
    const menuBtn = byId('menuPrintPreview');
    if (menuBtn instanceof HTMLButtonElement) menuBtn.disabled = !enabled;
    requireButton('printPreviewUpdate').disabled = !enabled || this.busy;
    requireButton('printPreviewCancel').hidden = !pending;
    requireButton('printPreviewPrev').disabled = pending || !this.job || this.index === 0;
    requireButton('printPreviewNext').disabled = pending || !this.job || this.index + 1 >= pages;
    const pageInput = requireInput('printPreviewPage');
    pageInput.disabled = this.busy || !pages;
    pageInput.max = String(pages);
    requireEl('printPreviewCount').textContent = `/ ${pages}`;
    requireButton('printOutput').disabled = pending || this.dirty || !this.job || !pages;
    const outputMode = requireSelect('printOutputMode');
    const outputRange = requireInput('printOutputRange');
    outputRange.hidden = outputMode.value !== 'range';
    outputMode.disabled = this.outputController !== null;
    outputRange.disabled = this.outputController !== null;
    requireEl('printPendingNote').hidden = !this.dirty;
    requireSelect('printScreenZoom').disabled = this.rendered === null;
  }

  private fields(): void {
    requireEl('printCustomPaper').hidden = requireSelect('printPaper').value !== 'custom';
    const scaleMode = requireSelect('printScaleMode').value;
    requireEl('printPercentRow').hidden = scaleMode !== 'percent';
    requireEl('printFitRow').hidden = scaleMode !== 'fit';
    requireEl('printMargins').hidden = requireSelect('printMarginMode').value !== 'custom';
    requireInput('printRepeatRows').disabled = requireInput('printClearRepeatRows').checked;
    requireInput('printRepeatColumns').disabled = requireInput('printClearRepeatColumns').checked;
  }

  private input(): PrintFormInput {
    return {
      defaults: requireInput('printUseDefaults').checked,
      paper: requireSelect('printPaper').value,
      width: requireInput('printPaperWidth').value,
      height: requireInput('printPaperHeight').value,
      orientation: requireSelect('printOrientation').value,
      scaleMode: requireSelect('printScaleMode').value,
      percent: requireInput('printPercent').value,
      fitWidth: requireInput('printFitWidth').value,
      fitHeight: requireInput('printFitHeight').value,
      area: requireInput('printArea').value,
      repeatRows: requireInput('printRepeatRows').value,
      repeatColumns: requireInput('printRepeatColumns').value,
      clearRepeatRows: requireInput('printClearRepeatRows').checked,
      clearRepeatColumns: requireInput('printClearRepeatColumns').checked,
      marginMode: requireSelect('printMarginMode').value,
      left: requireInput('printMarginLeft').value,
      right: requireInput('printMarginRight').value,
      top: requireInput('printMarginTop').value,
      bottom: requireInput('printMarginBottom').value,
      header: requireInput('printMarginHeader').value,
      footer: requireInput('printMarginFooter').value,
      pageOrder: requireSelect('printPageOrder').value,
      firstPage: requireInput('printFirstPage').value,
      rowHeight: requireInput('printRowHeight').value,
      columnWidth: requireInput('printColumnWidth').value,
      ignoreBreaks: requireInput('printIgnoreBreaks').checked,
    };
  }

  changed(): void {
    this.stop(false);
    this.dirty = true;
    this.fields();
    this.controls();
    this.status('设置已更改，点击更新预览。');
  }

  stop(clear = false): void {
    this.outputController?.abort();
    this.outputJobs?.reset();
    this.outputController = null;
    this.outputJobs = null;
    ++this.epoch;
    ++this.request;
    ++this.pageRequest;
    this.jobs.reset();
    this.controller?.abort();
    this.pageController?.abort();
    this.busy = false;
    this.loading = false;
    if (clear) {
      this.release(this.job);
      this.job = '';
      this.summary = null;
      this.page = null;
      this.rendered = null;
      requireEl('printPaperWrap').replaceChildren();
      requireEl('printPageCaption').textContent = '';
      requireEl('printEffective').textContent = '';
      requireEl('printWarnings').replaceChildren();
      requireEl('printRawSettings').replaceChildren();
      const empty = requireEl('printPreviewEmpty');
      empty.hidden = false;
      empty.textContent = '点击更新预览，准备本表分页。';
    }
    this.controls();
  }

  close(): void {
    this.stop(true);
    if (this.dialog.open) this.dialog.close();
  }

  resetBook(): void {
    this.stop(true);
    this.bound = '';
    this.dirty = false;
    requireForm('printSettingsForm').reset();
    requireSelect('printScreenZoom').value = 'fit';
    this.fields();
    this.status('');
    if (this.dialog.open) this.dialog.close();
    this.controls();
  }

  cancel(): void {
    this.stop(false);
    this.status(this.job ? '已取消更新，保留上一次预览。' : '已取消，可重新准备预览。');
  }

  async open(): Promise<void> {
    if (!this.enabled()) {
      this.toast('请先打开并完成支持打印设置的工作表加载', 'warning');
      return;
    }
    const bound = JSON.stringify(this.binding());
    if (this.bound && this.bound !== bound) this.resetBook();
    this.bound = bound;
    requireEl('printPreviewSource').textContent = `${this.s.fileName || '当前工作簿'} / ${this.s.sheet ?? ''}`;
    if (!this.dialog.open) {
      requireDetails('printSettings').open =
        typeof matchMedia === 'undefined' || !matchMedia('(max-width:680px)').matches;
      this.dialog.showModal();
    }
    this.controls();
    if (!this.job && !this.busy) await this.refresh();
    if (this.dialog.open) requireEl('printPreviewCanvas').focus({ preventScroll: true });
  }

  async refresh(): Promise<void> {
    if (this.busy || !this.enabled() || !this.dialog.open) return;
    let options: ReturnType<typeof buildPrintOptions>;
    try {
      options = buildPrintOptions(this.input(), this.source(), this.limits);
    } catch (error: unknown) {
      this.status(errorMessage(error), true);
      return;
    }
    this.stop(false);
    const request = ++this.request;
    const identity = this.identity();
    const binding = this.binding();
    this.busy = true;
    this.controls();
    this.status('正在准备纸张分页…');
    let owned = '';
    let adopted = false;
    const current = (): boolean =>
      request === this.request && identity === this.identity() && this.dialog.open;
    try {
      const done = await this.jobs.execute({ ...binding, options }, { main: true });
      owned = done.job;
      if (!current() || !done.current()) return;
      const controller = new AbortController();
      this.controller = controller;
      const aborted = (): boolean => controller.signal.aborted;
      const rawSummary = await this.api(
        `/api/printing/summary?${new URLSearchParams({ id: owned }).toString()}`,
        { signal: controller.signal },
      );
      if (!current() || aborted()) return;
      const summary = validatePrintSummary(rawSummary, binding, owned, this.limits, this.source());
      let page: PrintPageResponse | null = null;
      let rendered: RenderedPrintPage | null = null;
      if (summary.summary.pages) {
        page = await this.readPage(owned, 0, summary, binding, controller.signal);
        if (!current() || aborted()) return;
        rendered = this.renderer.build(summary, page);
      }
      if (!current()) return;
      const old = this.job;
      this.job = owned;
      this.summary = summary;
      this.index = 0;
      this.page = page;
      this.rendered = rendered;
      adopted = true;
      this.dirty = false;
      this.commit();
      this.release(old);
    } catch (error: unknown) {
      if (current() && !isAbortError(error)) {
        this.status(`${this.job ? '保留上一次预览。' : ''}${errorMessage(error)}`, true);
      }
    } finally {
      if (owned && !adopted) this.release(owned);
      if (current()) {
        this.busy = false;
        this.controls();
      }
    }
  }

  private async readPage(
    job: string,
    index: number,
    summary: PrintSummaryResponse,
    binding: PrintBinding,
    signal: AbortSignal,
  ): Promise<PrintPageResponse> {
    const value = await this.api(
      `/api/printing/page?${new URLSearchParams({ id: job, page: String(index) }).toString()}`,
      { signal },
    );
    return validatePrintPage(value, binding, job, index, summary, this.limits, this.source());
  }

  async go(index: number): Promise<void> {
    const summary = this.summary;
    const pages = summary?.summary.pages ?? 0;
    if (
      this.busy ||
      !this.job ||
      !summary ||
      !this.dialog.open ||
      !Number.isSafeInteger(index) ||
      index < 0 ||
      index >= pages ||
      (!this.loading && index === this.index)
    ) {
      return;
    }
    const serial = ++this.pageRequest;
    const identity = this.identity();
    const job = this.job;
    const binding = this.binding();
    this.pageController?.abort();
    const controller = new AbortController();
    this.pageController = controller;
    this.loading = true;
    this.controls();
    this.status(`正在读取第 ${index + 1} 页…`);
    const current = (): boolean =>
      serial === this.pageRequest &&
      identity === this.identity() &&
      job === this.job &&
      this.dialog.open;
    try {
      const page = await this.readPage(job, index, summary, binding, controller.signal);
      if (!current() || controller.signal.aborted) return;
      const rendered = this.renderer.build(summary, page);
      if (!current()) return;
      this.page = page;
      this.index = index;
      this.rendered = rendered;
      this.commit();
    } catch (error: unknown) {
      if (current() && !isAbortError(error)) {
        this.status(`保留上一次预览。${errorMessage(error)}`, true);
        requireInput('printPreviewPage').value = String(this.index + 1);
      }
    } finally {
      if (current()) {
        this.loading = false;
        this.controls();
      }
    }
  }

  async exportPaper(): Promise<void> {
    if (this.dirty || this.busy || this.loading || this.outputController || !this.summary) return;
    const controller = new AbortController();
    const identity = this.identity();
    const binding = this.binding();
    const base = this.job;
    const baseSummary = this.summary;
    const current = (): boolean =>
      this.outputController === controller &&
      !controller.signal.aborted &&
      this.dialog.open &&
      this.identity() === identity &&
      this.job === base;
    const jobs = new AsyncJobs<PrintingJobStatus, PrintingJobStatus>({
      api: this.api,
      identity: () => this.identity(),
      requestIdentity: () => binding.workbookKey,
      sleep: (ms) => new Promise<void>((resolve) => setTimeout(resolve, ms)),
      endpoint: '/api/printing',
      label: '底稿分页',
      maxPolls: 0,
      resultFromStatus: (status) => status,
    });
    this.outputController = controller;
    this.outputJobs = jobs;
    this.controls();
    try {
      const mode = requireSelect('printOutputMode').value;
      const sheets =
        mode === 'book'
          ? this.s.sheets.filter((s) => !s.state || s.state === 'visible').map((s) => s.name)
          : [binding.sheet];
      const title = `${this.s.fileName} · ${mode === 'book' ? '可见工作表' : binding.sheet}`;
      const output = new PrintOutput(title, this.limits);
      for (const sheet of sheets) {
        const sourceBinding: PrintBinding = { ...binding, sheet };
        let id = base;
        let summary = baseSummary;
        let temporary = '';
        try {
          if (sheet !== binding.sheet) {
            const task = await jobs.execute(
              {
                ...sourceBinding,
                indexIfNeeded: true,
                options: { usePreviewDefaults: requireInput('printUseDefaults').checked },
              },
              { main: true },
            );
            id = task.job;
            temporary = task.job;
            if (!current()) return;
            const rawSummary = await this.api(
              `/api/printing/summary?${new URLSearchParams({ id }).toString()}`,
              { signal: controller.signal },
            );
            summary = validatePrintSummary(rawSummary, sourceBinding, id, this.limits, this.source());
          }
          if (!current()) return;
          const total = summary.summary.pages;
          if (!total) continue;
          const pages =
            mode === 'current'
              ? [this.index]
              : parsePageRange(
                  mode === 'range' ? requireInput('printOutputRange').value : `1-${total}`,
                  total,
                  this.limits.outputPages - output.parts.length,
                );
          for (const index of pages) {
            this.status(`${sheet} · 正在输出第 ${index + 1} 页`);
            const page = await this.readPage(id, index, summary, sourceBinding, controller.signal);
            if (!current()) return;
            const rendered = this.renderer.build(summary, page);
            const notes = [...summary.summary.notices, ...(summary.sourceNotices ?? [])];
            if (!summary.sourceComplete || summary.summary.approximate) {
              notes.push('部分页面设置采用缺省或近似值。');
            }
            if (
              Object.values(summary.sourceProperties.headerFooter ?? {}).some(
                (v) => v.type === 'text' && Boolean(v.value),
              )
            ) {
              notes.push('源页眉页脚字段未绘制。');
            }
            output.add(rendered, sheet, index + 1, notes);
          }
        } finally {
          if (temporary) {
            await this.api('/api/printing/clear', {
              method: 'POST',
              body: JSON.stringify({ id: temporary }),
            }).catch(() => {
              // Best-effort cleanup of temporary print job.
            });
          }
        }
      }
      if (!current()) return;
      const url = URL.createObjectURL(output.file());
      const link = document.createElement('a');
      link.href = url;
      link.download = `${title.replace(/[\\/:*?"<>|]/g, '_')}_分页底稿.html`;
      link.click();
      setTimeout(() => URL.revokeObjectURL(url), 60000);
      this.status(`已导出 ${output.parts.length} 页，可打开底稿打印或另存为 PDF`);
    } catch (error: unknown) {
      if (current()) this.status(errorMessage(error), true);
    } finally {
      if (this.outputController === controller) {
        this.outputController = null;
        this.outputJobs = null;
        this.controls();
      }
    }
  }

  private goInput(): Promise<void> | undefined {
    const value = Number(requireInput('printPreviewPage').value);
    const pages = this.summary?.summary.pages ?? 0;
    if (!Number.isSafeInteger(value) || value < 1 || value > pages) {
      this.status(`页序请输入1到${pages}之间的整数`, true);
      requireInput('printPreviewPage').value = String(this.index + 1);
      return undefined;
    }
    return this.go(value - 1);
  }

  private commit(): void {
    const summaryResp = this.summary;
    if (!summaryResp) return;
    requireEl('printPaperWrap').replaceChildren(...(this.rendered ? [this.rendered.node] : []));
    const empty = requireEl('printPreviewEmpty');
    empty.hidden = this.rendered !== null;
    empty.textContent = '当前打印范围没有可见内容，可调整区域或重复标题。';
    requireInput('printPreviewPage').value = String(this.index + 1);

    const s = summaryResp.summary;
    const mm = (value: string): string =>
      (((Number(printUnits(value)) / this.limits.unitsPerPoint) * 25.4) / 72).toFixed(1);
    const percent = s.pages ? (Number(s.scale.numerator) / Number(s.scale.denominator)) * 100 : 0;
    const percentText = percent < 0.0001 ? percent.toExponential(3) : String(Number(percent.toPrecision(6)));
    requireEl('printEffective').textContent = s.pages
      ? `${mm(s.paper.width)} × ${mm(s.paper.height)} mm · 打印缩放 ${percentText}%`
      : '没有可预览的页面';
    requireEl('printPageCaption').textContent = this.page
      ? `第 ${this.index + 1} / ${s.pages} 页` +
        (this.page.content.page.number === this.index + 1 ? '' : ` · 源页码 ${this.page.content.page.number}`) +
        ` · 区域 ${this.page.content.page.area}`
      : '';
    this.explain();
    this.zoom();
    this.status(
      this.dirty
        ? '设置已更改，请更新预览。'
        : s.pages
          ? this.noticeCount
            ? `${this.noticeCount} 项提示，见页面详情`
            : ''
          : '没有可预览的页面',
    );
    this.controls();
  }

  private explain(): void {
    const s = this.summary;
    if (!s) return;
    const notes = new Set<string>([
      ...s.summary.notices,
      ...(s.sourceNotices ?? []),
      ...(this.rendered?.warnings ?? []),
    ]);
    if (!s.sourceComplete) notes.add('源打印设置有未完整解释的项目，原始属性保留。');
    if (s.summary.approximate) notes.add('此预览包含推导或近似设置，请核对源文件的实际分页。');
    if (
      Object.values(s.sourceProperties.headerFooter ?? {}).some(
        (value) => value.type === 'text' && Boolean(value.value),
      )
    ) {
      notes.add('保存的页眉或页脚含文字/字段，当前纸张中未绘制，可在源定义中核对。');
    }
    this.noticeCount = notes.size;
    notes.add('本视图显示单元格分页；图形、页眉页脚字段和打印机效果未绘制，原定义在下方列示。');
    const list = requireEl('printWarnings');
    list.replaceChildren();
    for (const text of notes) list.append(makeEl('li', '', text));

    const raw = requireEl('printRawSettings');
    raw.replaceChildren();
    for (const [key, label] of SETTING_LABELS) {
      const originKey = s.summary.origins[key];
      if (originKey) {
        raw.append(makeEl('p', 'print-setting-origin', `${label}：${ORIGIN_LABELS[originKey] ?? originKey}`));
      }
    }
    for (const [group, fields] of Object.entries(s.sourceProperties)) {
      const detail = makeEl('details', 'print-source-group');
      detail.append(makeEl('summary', '', GROUP_LABELS[group] ?? group));
      for (const [name, value] of Object.entries(fields)) {
        const line = makeEl('p', '', `${name}：`);
        const definition = makeEl('span', '', value.value);
        line.append(definition);
        if (value.status !== 'stored') line.append(makeEl('small', '', value.reason || '未确认'));
        detail.append(line);
      }
      raw.append(detail);
    }
  }

  zoom(): void {
    if (!this.rendered || !this.dialog.open) return;
    const canvas = requireEl('printPreviewCanvas');
    const mode = requireSelect('printScreenZoom').value;
    const w = this.rendered.width;
    const h = this.rendered.height;
    let scale =
      mode === 'fit'
        ? Math.min((canvas.clientWidth - 36) / w, (canvas.clientHeight - 36) / h, 1.5)
        : mode === 'width'
          ? (canvas.clientWidth - 36) / w
          : Number(mode) / 100;
    if (!Number.isFinite(scale) || scale <= 0) return;
    scale = Math.min(4, scale);
    this.rendered.node.style.transform = `scale(${scale})`;
    const wrap = requireEl('printPaperWrap');
    wrap.style.width = `${w * scale}px`;
    wrap.style.height = `${h * scale}px`;
  }
}
