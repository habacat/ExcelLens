import { t as localize, th as localizeHTML } from '../core/i18n.ts';
import type { AsyncJobProgress } from '../contracts/common.ts';
import { decodePrintingJobStatus } from '../core/http-decoders.generated.ts';
/**
 * Print preview dialog controller, page setup form, pagination navigation,
 * and printable HTML manuscript exporter.
 */
import type { ApiClient, ToastFn } from '../contracts/common.ts';
import type { PrintingLimits, RichTextLimits, SourceCapabilities } from '../contracts/formats.ts';
import { AsyncJobs } from '../core/async-jobs.ts';
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

/** @httpResponse PrintingJobStatus */
export interface PrintingJobStatus extends AsyncJobProgress {
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
  source: localize('ui.printing.sourceSettings'),
  'preview-override': localize('ui.printing.thisCover'),
  'preview-default': localize('ui.printing.previewDefault'),
  'schema-default': localize('ui.printing.defaultFormat'),
  'indexed-data-range': localize('ui.printing.sourceDataRange'),
  'office-compatible-source': localize('ui.printing.sourceManualPagination'),
};

const SETTING_LABELS: readonly (readonly [string, string])[] = [
  ['paper', localize('ui.printing.paper')],
  ['orientation', localize('ui.printing.direction')],
  ['areas', localize('ui.printing.printArea')],
  ['repeatRows', localize('ui.printing.repeatingLines')],
  ['repeatColumns', localize('ui.printing.duplicateColumns')],
  ['firstPageNumber', localize('ui.printing.startingPageNumber')],
];

const GROUP_LABELS: Readonly<Record<string, string>> = {
  pageSetup: localize('ui.printing.sourcePageSettings'),
  pageMargins: localize('ui.printing.sourceMargin'),
  pageSetUpPr: localize('ui.printing.sourceAdaptationSettings'),
  printOptions: localize('ui.printing.sourcePrintingOptions'),
  headerFooter: localize('ui.printing.definitionSourcePageHeadersFooters'),
};

const MARGIN_NAMES: readonly (readonly [string, string])[] = [
  ['left', localize('ui.common.left')],
  ['right', localize('ui.common.right')],
  ['top', localize('ui.printing.above')],
  ['bottom', localize('ui.printing.below')],
  ['header', localize('ui.printing.header')],
  ['footer', localize('ui.printing.footer')],
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
      throw new Error(localize('ui.printing.paginationPreviewCapacityUnavailable'));
    }
    const sleep = options.sleep ?? ((ms) => new Promise<void>((resolve) => setTimeout(resolve, ms)));
    this.renderer = new PrintingRenderer({
      limits: options.limits,
      richLimits: options.richLimits,
      fontStack: options.fontStack,
    });
    this.dialog = this.create();
    this.jobs = new AsyncJobs<PrintingJobStatus, PrintingJobStatus>({
      decodeStatus: decodePrintingJobStatus,
      api: this.api,
      identity: () => this.identity(),
      requestIdentity: () => this.s.workbookKey,
      sleep,
      endpoint: '/api/printing',
      label: localize('ui.printing.pageBreakPreview'),
      maxPolls: 0,
      resultFromStatus: (status) => status,
      onProgress: (status) => {
        this.validSource(status);
        this.status(status.stage || localize('ui.printing.preparingPreview947975f'));
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
    d.innerHTML = `<div class="dialog-head"><div id="printPreviewTitle" class="dialog-title">${localizeHTML('ui.printing.pageBreakPreview')}</div><button id="printPreviewClose" class="dialog-close" type="button" aria-label="${localizeHTML('ui.printing.closePaginationPreview')}">×</button></div>
        <p id="printPreviewSource" class="print-source"></p><div class="print-main"><details id="printSettings" class="print-settings" open><summary>${localizeHTML('ui.printing.pageSettings')}</summary><form id="printSettingsForm">
        <label>${localizeHTML('ui.printing.paper')}<select id="printPaper" class="field-control"><option value="source">${localizeHTML('ui.printing.setAccordingOriginalTableConfiguration')}</option><option value="a4">A4</option><option value="a3">A3</option><option value="letter">Letter</option><option value="legal">Legal</option><option value="custom">${localizeHTML('ui.printing.customSize')}</option></select></label>
        <div id="printCustomPaper" class="print-pair" hidden><label>${localizeHTML('ui.printing.widthMm')}<input id="printPaperWidth" class="field-control" value="210" inputmode="decimal"></label><label>${localizeHTML('ui.printing.heightMm')}<input id="printPaperHeight" class="field-control" value="297" inputmode="decimal"></label></div>
        <label>${localizeHTML('ui.printing.direction')}<select id="printOrientation" class="field-control"><option value="">${localizeHTML('ui.printing.setAccordingOriginalTableConfiguration')}</option><option value="portrait">${localizeHTML('ui.printing.vertical')}</option><option value="landscape">${localizeHTML('ui.printing.horizontal')}</option></select></label>
        <label>${localizeHTML('ui.printing.printScaling')}<select id="printScaleMode" class="field-control"><option value="source">${localizeHTML('ui.printing.setAccordingOriginalTableConfiguration')}</option><option value="percent">${localizeHTML('ui.printing.specifyRatio')}</option><option value="fit">${localizeHTML('ui.printing.adaptNumberPages')}</option></select></label>
        <label id="printPercentRow" hidden>${localizeHTML('ui.printing.percentage')}<input id="printPercent" class="field-control" value="100" inputmode="decimal"></label>
        <div id="printFitRow" class="print-pair" hidden><label>${localizeHTML('ui.printing.horizontalPages')}<input id="printFitWidth" class="field-control" value="1" inputmode="numeric"></label><label>${localizeHTML('ui.printing.verticalPages')}<input id="printFitHeight" class="field-control" value="0" inputmode="numeric"></label><small>${localizeHTML('ui.printing.indicatesNoLimitThisDirection')}</small></div>
        <label>${localizeHTML('ui.printing.printArea')}<input id="printArea" class="field-control" placeholder="${localizeHTML('ui.printing.a1H50J1L50Leave')}" autocomplete="off"></label>
        <details class="print-advanced"><summary>${localizeHTML('ui.printing.morePageSettings')}</summary><label>${localizeHTML('ui.printing.repeatingLines')}<input id="printRepeatRows" class="field-control" placeholder="${localizeHTML('ui.printing.leaveBlankAsOriginalTable82521ed')}"></label><label class="print-toggle"><input id="printClearRepeatRows" type="checkbox">${localizeHTML('ui.printing.noDuplicationRows')}</label><label>${localizeHTML('ui.printing.duplicateColumns')}<input id="printRepeatColumns" class="field-control" placeholder="${localizeHTML('ui.printing.leaveBlankAsOriginalTable')}"></label><label class="print-toggle"><input id="printClearRepeatColumns" type="checkbox">${localizeHTML('ui.printing.noDuplicationColumns')}</label>
        <label>${localizeHTML('ui.printing.margins')}<select id="printMarginMode" class="field-control"><option value="source">${localizeHTML('ui.printing.setAccordingOriginalTableConfiguration')}</option><option value="custom">${localizeHTML('ui.printing.customMm')}</option></select></label><div id="printMargins" class="print-pair" hidden></div>
        <label>${localizeHTML('ui.printing.pageOrder')}<select id="printPageOrder" class="field-control"><option value="">${localizeHTML('ui.printing.setAccordingOriginalTableConfiguration')}</option><option value="downThenOver">${localizeHTML('ui.printing.downFirstThenRight')}</option><option value="overThenDown">${localizeHTML('ui.printing.firstMoveRightThenDown')}</option></select></label><label>${localizeHTML('ui.printing.startingPageNumber')}<input id="printFirstPage" class="field-control" placeholder="${localizeHTML('ui.printing.leaveFieldsEmptyAsThey')}" inputmode="numeric"></label>
        <div class="print-pair"><label>${localizeHTML('ui.printing.defaultRowHeightPounds')}<input id="printRowHeight" class="field-control" placeholder="${localizeHTML('ui.printing.leaveFieldsEmptyAsThey')}" inputmode="decimal"></label><label>${localizeHTML('ui.printing.defaultColumnWidthPounds')}<input id="printColumnWidth" class="field-control" placeholder="${localizeHTML('ui.printing.leaveFieldsEmptyAsThey')}" inputmode="decimal"></label></div><label class="print-toggle"><input id="printIgnoreBreaks" type="checkbox">${localizeHTML('ui.printing.ignoringManualPaginationOriginalTable')}</label></details>
        <label class="print-toggle"><input id="printUseDefaults" type="checkbox" checked>${localizeHTML('ui.printing.fillMissingPageSettings')}</label><button id="printPreviewUpdate" class="primary-button" type="button">${localizeHTML('ui.printing.updatePreview')}</button><p id="printPendingNote" class="print-pending" hidden>${localizeHTML('ui.printing.settingsHaveChangedClickUpdate')}</p></form></details>
        <section class="print-view"><div class="print-page-tools"><button id="printPreviewPrev" class="mini-button" type="button" aria-label="${localizeHTML('ui.printing.previousPage')}">${localizeHTML('ui.printing.previousPage')}</button><label class="print-page-number">${localizeHTML('ui.printing.weeks')}<input id="printPreviewPage" class="field-control" type="number" min="1" value="1" aria-label="${localizeHTML('ui.printing.previewPageOrder')}">${localizeHTML('ui.printing.trillions')}<span id="printPreviewCount">/ 0</span></label><button id="printPreviewNext" class="mini-button" type="button" aria-label="${localizeHTML('ui.printing.nextPage')}">${localizeHTML('ui.printing.nextPage')}</button><label class="print-screen-zoom">${localizeHTML('ui.printing.screenZoom')}<select id="printScreenZoom" class="field-control"><option value="fit">${localizeHTML('ui.printing.suitablePage')}</option><option value="width">${localizeHTML('ui.printing.suitableWidth')}</option><option value="50">50%</option><option value="75">75%</option><option value="100">100%</option><option value="125">125%</option><option value="150">150%</option><option value="200">200%</option></select></label></div><p id="printEffective" class="print-effective"></p><div id="printPreviewCanvas" class="print-canvas" tabindex="0" aria-label="${localizeHTML('ui.printing.paperPreviewPageupPagedownPage')}"><div id="printPaperWrap" class="print-paper-wrap"></div><p id="printPreviewEmpty" class="print-empty">${localizeHTML('ui.printing.preparingPreview')}</p></div><p id="printPageCaption" class="print-caption"></p></section></div>
        <details id="printSourceDetails" class="print-source-details"><summary>${localizeHTML('ui.printing.pageDetails')}</summary><ul id="printWarnings"></ul><div id="printRawSettings"></div></details><div class="print-footer"><select id="printOutputMode" class="field-control" aria-label="${localizeHTML('ui.printing.draftParagraphs')}"><option value="current">${localizeHTML('ui.printing.currentPage')}</option><option value="range">${localizeHTML('ui.printing.specifyPageSegment')}</option><option value="sheet">${localizeHTML('ui.printing.allPagesThisTable')}</option><option value="book">${localizeHTML('ui.printing.tablesVisibleAcrossBook')}</option></select><input id="printOutputRange" class="field-control" placeholder="1-3,5" aria-label="${localizeHTML('ui.printing.draftParagraphRange')}" hidden><button id="printOutput" class="mini-button" type="button">${localizeHTML('ui.printing.exportPrintout')}</button><p id="printPreviewStatus" role="status"></p><button id="printPreviewCancel" class="mini-button" type="button" hidden>${localizeHTML('ui.printing.cancel')}</button></div>`;
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
      throw new Error(localize('ui.printing.previewSourceSwitchedPrepareAgain'));
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
    this.status(localize('ui.printing.settingsHaveChangedClickUpdate'));
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
      empty.textContent = localize('ui.printing.clickUpdatePreviewPrepareTable');
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
    this.status(this.job ? localize('ui.printing.updateCanceledRetainingLastPreview') : localize('ui.printing.cancelledReadyRePreviewing'));
  }

  async open(): Promise<void> {
    if (!this.enabled()) {
      this.toast(localize('ui.printing.openCompleteWorksheetLoadingPrinting'), 'warning');
      return;
    }
    const bound = JSON.stringify(this.binding());
    if (this.bound && this.bound !== bound) this.resetBook();
    this.bound = bound;
    requireEl('printPreviewSource').textContent = `${this.s.fileName || localize('ui.common.currentWorkbook')} / ${this.s.sheet ?? ''}`;
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
    this.status(localize('ui.printing.preparingPaperPagination'));
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
        this.status(`${this.job ? localize('ui.printing.lastPreviewRetained') : ''}${errorMessage(error)}`, true);
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
    this.status(localize('ui.printing.readingPage', { index: index + 1 }));
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
        this.status(localize('ui.printing.retainLastPreview', { detail: errorMessage(error) }), true);
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
      decodeStatus: decodePrintingJobStatus,
      api: this.api,
      identity: () => this.identity(),
      requestIdentity: () => binding.workbookKey,
      sleep: (ms) => new Promise<void>((resolve) => setTimeout(resolve, ms)),
      endpoint: '/api/printing',
      label: localize('ui.printing.draftPagination'),
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
      const title = `${this.s.fileName} · ${mode === 'book' ? localize('ui.printing.visibleWorksheets') : binding.sheet}`;
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
            this.status(localize('ui.printing.currentlyOutputtingPage', { sheet, index: index + 1 }));
            const page = await this.readPage(id, index, summary, sourceBinding, controller.signal);
            if (!current()) return;
            const rendered = this.renderer.build(summary, page);
            const notes = [...summary.summary.notices, ...(summary.sourceNotices ?? [])];
            if (!summary.sourceComplete || summary.summary.approximate) {
              notes.push(localize('ui.printing.somePageSettingsUseDefault'));
            }
            if (
              Object.values(summary.sourceProperties.headerFooter ?? {}).some(
                (v) => v.type === 'text' && Boolean(v.value),
              )
            ) {
              notes.push(localize('ui.printing.sourcePageHeaderFooterFields'));
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
      link.download = localize('ui.printing.paginationDraftHtml', { detail: title.replace(/[\\/:*?"<>|]/g, '_') });
      link.click();
      setTimeout(() => URL.revokeObjectURL(url), 60000);
      this.status(localize('ui.printing.pageExportedCanPrintDraft', { count: output.parts.length }));
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
      this.status(localize('ui.printing.enterIntegerBetweenPageOrder', { pages }), true);
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
    empty.textContent = localize('ui.printing.thereNoVisibleContentCurrent');
    requireInput('printPreviewPage').value = String(this.index + 1);

    const s = summaryResp.summary;
    const mm = (value: string): string =>
      (((Number(printUnits(value)) / this.limits.unitsPerPoint) * 25.4) / 72).toFixed(1);
    const percent = s.pages ? (Number(s.scale.numerator) / Number(s.scale.denominator)) * 100 : 0;
    const percentText = percent < 0.0001 ? percent.toExponential(3) : String(Number(percent.toPrecision(6)));
    requireEl('printEffective').textContent = s.pages
      ? localize('ui.printing.mmPrintingScale', { mm: mm(s.paper.width), mm2: mm(s.paper.height), percentText })
      : localize('ui.printing.noPagesPreview');
    requireEl('printPageCaption').textContent = this.page
      ? localize('ui.printing.page', { index: this.index + 1, pages: s.pages }) +
        (this.page.content.page.number === this.index + 1 ? '' : ` ${localize('ui.printing.sourcePageNumber', { number: this.page.content.page.number })}`) +
        ` ${localize('ui.printing.area', { area: this.page.content.page.area })}`
      : '';
    this.explain();
    this.zoom();
    this.status(
      this.dirty
        ? localize('ui.printing.settingChangedUpdatePreview')
        : s.pages
          ? this.noticeCount
            ? localize('ui.printing.tipsSeePageDetails', { noticeCount: this.noticeCount })
            : ''
          : localize('ui.printing.noPagesPreview'),
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
    if (!s.sourceComplete) notes.add(localize('ui.printing.sourcePrintingSettingsHaveIncomplete'));
    if (s.summary.approximate) notes.add(localize('ui.printing.thisPreviewIncludesDerivedOr'));
    if (
      Object.values(s.sourceProperties.headerFooter ?? {}).some(
        (value) => value.type === 'text' && Boolean(value.value),
      )
    ) {
      notes.add(localize('ui.printing.headerOrFooterSavedDocument'));
    }
    this.noticeCount = notes.size;
    notes.add(localize('ui.printing.thisViewDisplaysCellsPagination'));
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
        if (value.status !== 'stored') line.append(makeEl('small', '', value.reason || localize('ui.printing.unconfirmed')));
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
