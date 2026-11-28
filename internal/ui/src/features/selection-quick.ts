/**
 * Selection Quick Analysis dialog: per-column type distribution, paired
 * column difference, segmented trend chart, and source cell jump.
 */
import { interfaceLocale, t as localize, th as localizeHTML } from '../core/i18n.ts';
import type { ApiClient, SelectionArea, ToastFn } from '../contracts/common.ts';
import type { SelectionQuickLimits } from '../contracts/formats.ts';
import type { IndexStartResponse, IndexStatusResponse } from '../contracts/workbook.ts';
import { errorMessage } from '../core/display-format.ts';
import { byId, requireButton, requireEl, requireInput, requireSelect } from '../core/dom.ts';
import { MultiSelection } from '../grid/multi-selection.ts';
import type { WorkbookState } from '../grid/state.ts';
import {
  buildSelectionQuickChart,
  validateSelectionQuick,
  type SelectionQuickChartData,
  type SelectionQuickRequest,
  type SelectionQuickSeries,
  type SelectionQuickWireResponse,
} from './selection-quick-model.ts';

export interface SelectionQuickChartExportPayload {
  readonly kind: 'selection-quick';
  readonly canvas: HTMLCanvasElement;
  readonly expectedResult: SelectionQuickChartData;
  readonly mode: 'line';
  readonly seriesKey: string;
  readonly result: SelectionQuickWireResponse;
  readonly origin: {
    readonly workbookKey: string;
    readonly book: string;
    readonly sheet: string;
    readonly viewId: string | undefined;
  };
  readonly title: string;
  readonly source: string;
  readonly scope: string;
  readonly metric: string;
  readonly unit: string;
  readonly current: () => boolean;
  readonly notify: ToastFn;
}

export interface SelectionQuickChartExportBridge {
  revalidate(): void;
  open(payload: SelectionQuickChartExportPayload): void;
}

export interface SelectionQuickOptions {
  readonly state: WorkbookState;
  readonly api: ApiClient;
  readonly toast: ToastFn;
  readonly colName: (col: number) => string;
  readonly limits: SelectionQuickLimits;
  readonly pinnedThroughRow: () => number;
  readonly canRead?: () => boolean;
  readonly navigate: (
    sheet: string,
    ref: string,
    guard?: () => boolean,
  ) => Promise<unknown>;
  readonly formatNumber?: (value: { readonly exact: string; readonly display: string }) => string;
  readonly drawChart?: (
    canvas: HTMLCanvasElement,
    data: SelectionQuickChartData,
    mode: string,
    onSelect: (keys: readonly string[]) => void,
  ) => void;
  readonly getChartExport?: () => SelectionQuickChartExportBridge | null | undefined;
}

interface QuickBinding {
  readonly request: Omit<SelectionQuickRequest, 'skipFirstRow' | 'minuend' | 'subtrahend'>;
  readonly area: SelectionArea | null;
  readonly first: number;
  readonly last: number;
  readonly rows: number;
  readonly index: IndexStartResponse | IndexStatusResponse | null;
  readonly sourceKey: string;
  readonly key: string;
}

interface DrawnState {
  readonly plot: SelectionQuickChartData;
  readonly page: SelectionQuickWireResponse;
  readonly series: SelectionQuickSeries;
}

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

export class SelectionQuick {
  public readonly dialog: HTMLDialogElement;
  public busy = false;

  private readonly s: WorkbookState;
  private readonly api: ApiClient;
  private readonly toast: ToastFn;
  private readonly colName: (col: number) => string;
  private readonly limits: SelectionQuickLimits;
  private readonly pinnedThroughRow: () => number;
  private readonly canRead: () => boolean;
  private readonly navigate: (
    sheet: string,
    ref: string,
    guard?: () => boolean,
  ) => Promise<unknown>;
  private readonly formatNumber: (value: {
    readonly exact: string;
    readonly display: string;
  }) => string;
  private readonly drawChart?: (
    canvas: HTMLCanvasElement,
    data: SelectionQuickChartData,
    mode: string,
    onSelect: (keys: readonly string[]) => void,
  ) => void;
  private readonly getChartExport?: () => SelectionQuickChartExportBridge | null | undefined;

  private serial = 0;
  private navigation = 0;
  private abort: AbortController | null = null;
  private bound: QuickBinding | null = null;
  public page: SelectionQuickWireResponse | null = null;
  private series: SelectionQuickSeries | null = null;
  private drawn: DrawnState | null = null;

  constructor(options: SelectionQuickOptions) {
    if (
      !options.limits.cells ||
      !options.limits.columns ||
      !options.limits.segments ||
      !options.limits.bytes ||
      !options.limits.requestBytes
    ) {
      throw new Error(localize('ui.selectionQuick.quickAnalysisCapacityConfigurationNot'));
    }
    this.s = options.state;
    this.api = options.api;
    this.toast = options.toast;
    this.colName = options.colName;
    this.limits = options.limits;
    this.pinnedThroughRow = options.pinnedThroughRow;
    this.canRead = options.canRead ?? (() => true);
    this.navigate = options.navigate;
    this.formatNumber = options.formatNumber ?? ((v) => v.display);
    this.drawChart = options.drawChart;
    this.getChartExport = options.getChartExport;

    this.dialog = this.create();
    requireButton('selectionQuickClose').addEventListener('click', () => this.close());
    this.dialog.addEventListener('cancel', (event: Event) => {
      event.preventDefault();
      this.close();
    });
    this.dialog.addEventListener('close', () => {
      if (!this.dialog.open) this.clear();
    });
    requireButton('selectionQuickRun').addEventListener('click', () => {
      void this.run();
    });
    requireButton('selectionQuickCancel').addEventListener('click', () => this.cancel());
    for (const id of ['selectionQuickSkip', 'selectionQuickMinuend', 'selectionQuickSubtrahend']) {
      requireEl(id).addEventListener('change', () => {
        if (this.page) this.status(localize('ui.selectionQuick.settingChangedRunProcessAgain'));
      });
    }
    for (const eventName of ['excellens:readingchange', 'excellens:analysisnumberchange']) {
      document.addEventListener(eventName, () => {
        if (this.page && this.dialog.open) this.renderSeries();
      });
    }
    window.addEventListener('resize', () => {
      if (this.page && this.dialog.open) this.draw();
    });
    for (const type of ['pointerdown', 'keydown'] as const) {
      document.addEventListener(
        type,
        () => {
          ++this.navigation;
        },
        true,
      );
    }
  }

  private create(): HTMLDialogElement {
    const d = makeEl('dialog', 'float-dialog selection-quick-dialog');
    d.id = 'selectionQuickDialog';
    d.setAttribute('aria-labelledby', 'selectionQuickTitle');
    // Static markup only; every source string below uses textContent.
    d.innerHTML = `<div class="dialog-head"><div id="selectionQuickTitle" class="dialog-title">${localizeHTML('ui.selectionQuick.quickAnalysisSelection')}</div><button id="selectionQuickClose" type="button" class="dialog-close" aria-label="${localizeHTML('ui.selectionQuick.disableQuickAnalysis')}">×</button></div>
        <p id="selectionQuickSource" class="selection-quick-source"></p><div class="selection-quick-tools"><label><input id="selectionQuickSkip" type="checkbox">${localizeHTML('ui.selectionQuick.skipFirstRow')}</label><label>${localizeHTML('ui.selectionQuick.subtractedColumns')}<select id="selectionQuickMinuend" class="field-control"></select></label><span aria-hidden="true">−</span><label>${localizeHTML('ui.selectionQuick.removeColumns')}<select id="selectionQuickSubtrahend" class="field-control"></select></label><button id="selectionQuickRun" type="button" class="mini-button">${localizeHTML('ui.selectionQuick.runAnalysis')}</button></div>
        <div id="selectionQuickBody" class="selection-quick-body"></div>
        <div class="selection-quick-footer"><p id="selectionQuickStatus" role="status" aria-live="polite"></p><button id="selectionQuickCancel" type="button" class="mini-button" hidden>${localizeHTML('ui.selectionQuick.disanalysis')}</button></div>`;
    document.body.append(d);
    return d;
  }

  private binding(): QuickBinding {
    const areas = MultiSelection.areas(this.s, true);
    const area = areas.length === 1 ? (areas[0] ?? null) : null;
    const points = area ? MultiSelection.points(area, this.colName) : null;
    const through = this.pinnedThroughRow();
    const request: Omit<SelectionQuickRequest, 'skipFirstRow' | 'minuend' | 'subtrahend'> = {
      workbookKey: this.s.workbookKey,
      book: this.s.book,
      sheet: this.s.sheet ?? '',
      viewId: this.s.rowView?.id ?? '',
      filterId: this.s.filtered ? this.s.filterId : '',
      excludeThroughRow: through,
      start: points?.start ?? '',
      end: points?.end ?? '',
      visualStart: points?.visualStart,
      visualEnd: points?.visualEnd,
    };
    const first = area ? Math.min(area.col1, area.col2) : 0;
    const last = area ? Math.max(area.col1, area.col2) : 0;
    const rows = area
      ? this.s.rowVisibility
        ? this.s.rowVisibility.selectionCount(area, this.s.filtered, through)
        : MultiSelection.rowCount(area, this.s.filtered, through)
      : 0;
    const sourceKey = JSON.stringify([
      request.workbookKey,
      request.book,
      request.sheet,
      request.viewId,
      request.filterId,
      through,
      this.s.sheetEpoch,
      this.s.opened,
      this.s.indexReady,
      this.s.filtered,
    ]);
    return {
      request,
      area,
      first,
      last,
      rows,
      index: this.s.index,
      sourceKey,
      key: JSON.stringify([
        request,
        this.s.sheetEpoch,
        this.s.opened,
        this.s.indexReady,
        this.s.filtered,
        areas,
      ]),
    };
  }

  private same(bound: QuickBinding | null): boolean {
    const now = this.binding();
    return bound?.key === now.key && bound.index === now.index;
  }

  private ready(): boolean {
    return (
      this.s.opened &&
      this.s.indexReady &&
      !this.s.opening &&
      !this.s.sheetLoadingEpoch &&
      !this.s.rowViewFailure &&
      Boolean(this.s.rowView?.id) &&
      this.canRead()
    );
  }

  changed(): void {
    if (this.bound && (!this.same(this.bound) || !this.ready())) this.close();
    this.getChartExport?.()?.revalidate();
  }

  status(text: string, error = false): void {
    const p = requireEl('selectionQuickStatus');
    p.textContent = text;
    p.classList.toggle('is-error', error);
    p.setAttribute('role', error ? 'alert' : 'status');
  }

  controls(): void {
    requireButton('selectionQuickCancel').hidden = !this.busy;
    requireButton('selectionQuickRun').disabled = this.busy;
    requireInput('selectionQuickSkip').disabled = this.busy;
    requireSelect('selectionQuickMinuend').disabled = this.busy;
    requireSelect('selectionQuickSubtrahend').disabled = this.busy;
    requireEl('selectionQuickBody').setAttribute('aria-busy', String(this.busy));
    const imageBtn = byId('selectionQuickImage');
    if (imageBtn instanceof HTMLButtonElement) {
      imageBtn.disabled = this.busy || !this.page;
    }
  }

  open(): void {
    if (!this.ready()) {
      this.toast(localize('ui.selectionQuick.selectConsecutiveAreasWorksheetsThat'), 'warning');
      return;
    }
    if (this.dialog.open) return;
    const bound = this.binding();
    const { first, last, rows } = bound;
    const indexMaxCol = this.s.index?.maxCol ?? 0;
    if (!bound.area || first < 1 || last > indexMaxCol || rows < 1) {
      this.toast(localize('ui.selectionQuick.quickAnalysisSelectContinuousArea'), 'warning');
      return;
    }
    if (last - first + 1 > this.limits.columns || rows * (last - first + 1) > this.limits.cells) {
      this.toast(
        localize('ui.selectionQuick.quickAnalysisThereMaximumColumns', { columns: this.limits.columns, cells: this.limits.cells.toLocaleString(interfaceLocale()) }),
        'warning',
      );
      return;
    }
    this.clear();
    ++this.navigation;
    this.bound = bound;
    // The column span is read from the source, so columns hidden on screen count too. Say so when it applies.
    const hidden: string[] = [];
    for (let col = first; col <= last; col++) {
      if (this.s.hiddenColumns.has(col)) hidden.push(this.colName(col));
    }
    const hiddenNote = hidden.length
      ? ` ${localize('ui.selectionQuick.includesHiddenColumns', { slice: hidden.slice(0, 6).join('、'), detail: hidden.length > 6 ? ` ${localize('ui.selectionQuick.waitColumn', { count: hidden.length })}` : '' })}`
      : '';
    requireEl('selectionQuickSource').textContent =
      localize('ui.selectionQuick.rowsa30527c', { detail: this.s.sheet ?? '', start: bound.request.start, end: bound.request.end, rows: rows.toLocaleString(interfaceLocale()), hiddenNote });
    requireInput('selectionQuickSkip').checked = false;
    const left = requireSelect('selectionQuickMinuend');
    const right = requireSelect('selectionQuickSubtrahend');
    left.replaceChildren(new Option(localize('ui.selectionQuick.noComparison'), '0'));
    right.replaceChildren();
    for (let col = first; col <= last; col++) {
      left.append(new Option(this.colName(col), String(col)));
      right.append(new Option(this.colName(col), String(col)));
    }
    left.value = '0';
    right.value = String(first);
    this.controls();
    this.status('');
    this.dialog.showModal();
    requireButton('selectionQuickRun').focus();
  }

  private stop(): void {
    ++this.serial;
    this.abort?.abort();
    this.abort = null;
    this.busy = false;
  }

  clear(): void {
    this.stop();
    this.bound = null;
    this.page = null;
    this.series = null;
    this.drawn = null;
    this.getChartExport?.()?.revalidate();
    requireEl('selectionQuickBody').replaceChildren();
    this.status('');
  }

  close(): void {
    this.clear();
    if (this.dialog.open) this.dialog.close();
  }

  resetBook(): void {
    this.close();
  }

  cancel(): void {
    this.stop();
    this.status(this.page ? localize('ui.selectionQuick.cancelledRetainingPreviousResults') : localize('ui.common.canceled'));
    this.controls();
  }

  async run(): Promise<void> {
    this.changed();
    if (!this.dialog.open || !this.bound || this.busy || !this.ready()) return;
    const bound = this.bound;
    const minuend = Number(requireSelect('selectionQuickMinuend').value);
    const subtrahend = minuend ? Number(requireSelect('selectionQuickSubtrahend').value) : 0;
    if (
      minuend &&
      (minuend === subtrahend ||
        minuend < bound.first ||
        minuend > bound.last ||
        subtrahend < bound.first ||
        subtrahend > bound.last)
    ) {
      this.status(localize('ui.selectionQuick.differenceSelectTwoDifferentSource'), true);
      return;
    }
    const request: SelectionQuickRequest = {
      ...bound.request,
      skipFirstRow: requireInput('selectionQuickSkip').checked,
      minuend,
      subtrahend,
    };
    this.stop();
    const serial = this.serial;
    const ctrl = new AbortController();
    this.abort = ctrl;
    this.busy = true;
    this.controls();
    this.status(localize('ui.common.analyzing'));
    const current = (): boolean =>
      this.serial === serial &&
      !ctrl.signal.aborted &&
      this.dialog.open &&
      this.same(bound) &&
      this.ready();
    try {
      const body = JSON.stringify(request);
      if (new TextEncoder().encode(body).length > this.limits.requestBytes) {
        throw new Error(localize('ui.selectionQuick.requestExceedsQuickAnalysisCapacity'));
      }
      const rawPage = await this.api('/api/selection/quick', {
        method: 'POST',
        body,
        signal: ctrl.signal,
      });
      if (!current()) return;
      const page = validateSelectionQuick(
        rawPage,
        request,
        this.limits,
        bound.rows,
        bound.first,
        bound.last,
        bound.index?.maxRow ?? 0,
      );
      const view = this.build(page);
      if (!current()) return;
      this.page = page;
      requireEl('selectionQuickBody').replaceChildren(view);
      this.status(
        localize('ui.selectionQuick.rowsColumns', { rows: page.rows.toLocaleString(interfaceLocale()), count: page.columns.length, detail: page.skipFirstRow ? localize('ui.selectionQuick.skipFirstLine') : '' }),
      );
      this.renderSeries();
    } catch (error: unknown) {
      if (current() && !(error instanceof Error && error.name === 'AbortError')) {
        this.status(`${errorMessage(error)}${this.page ? localize('ui.common.keepPreviousResult') : ''}`, true);
      }
    } finally {
      if (this.serial === serial) {
        this.abort = null;
        this.busy = false;
        this.controls();
      }
    }
  }

  private table(
    headers: readonly string[],
    rows: readonly (readonly (string | number | Node)[])[],
    cls = '',
  ): HTMLDivElement {
    const wrap = makeEl('div', `selection-quick-table-wrap ${cls}`);
    const table = makeEl('table');
    const head = makeEl('thead');
    const tr = makeEl('tr');
    for (const label of headers) {
      const th = makeEl('th', '', label);
      th.scope = 'col';
      tr.append(th);
    }
    head.append(tr);
    table.append(head);
    const body = makeEl('tbody');
    for (const row of rows) {
      const line = makeEl('tr');
      for (const value of row) {
        const td = makeEl('td');
        if (value instanceof Node) td.append(value);
        else td.textContent = String(value);
        line.append(td);
      }
      body.append(line);
    }
    table.append(body);
    wrap.append(table);
    return wrap;
  }

  private build(page: SelectionQuickWireResponse): DocumentFragment {
    const fragment = document.createDocumentFragment();
    fragment.append(makeEl('h4', '', localize('ui.selectionQuick.columnColumnDistribution')));
    fragment.append(
      this.table(
        [localize('ui.common.column'), localize('ui.selectionQuick.negativeNumbers'), localize('ui.selectionQuick.pages'), localize('ui.selectionQuick.positiveNumbers'), localize('ui.common.blank'), localize('ui.selectionQuick.otherValues'), localize('ui.common.errors'), localize('ui.selectionQuick.formulaNoResult'), localize('ui.selectionQuick.invalidValues')],
        page.columns.map((c) => [
          this.colName(c.column),
          c.counts.negative,
          c.counts.zero,
          c.counts.positive,
          c.counts.blank,
          c.counts.other,
          c.counts.errors,
          c.counts.missing,
          c.counts.invalid,
        ]),
      ),
    );
    const tools = makeEl('div', 'selection-quick-series-tools');
    const label = makeEl('label', '', `${localize('ui.selectionQuick.viewStatisticsTrends')} `);
    const select = makeEl('select', 'field-control');
    select.id = 'selectionQuickSeries';
    for (const col of page.columns) {
      select.append(new Option(localize('ui.selectionQuick.sourceColumn', { colName: this.colName(col.column) }), String(col.column)));
    }
    if (page.difference) {
      select.append(
        new Option(localize('ui.selectionQuick.difference', { colName: this.colName(page.minuend), colName2: this.colName(page.subtrahend) }), 'difference'),
      );
    }
    select.value = page.difference ? 'difference' : String(page.firstColumn);
    select.addEventListener('change', () => this.renderSeries());
    label.append(select);
    tools.append(label);

    const image = makeEl('button', 'mini-button', localize('ui.selectionQuick.exportTrendImages'));
    image.id = 'selectionQuickImage';
    image.type = 'button';
    image.addEventListener('click', () => this.exportImage());
    tools.append(image);
    fragment.append(tools);

    const summary = makeEl('div', 'selection-quick-summary');
    summary.id = 'selectionQuickSummary';
    fragment.append(summary);

    const note = makeEl('p', 'selection-quick-note');
    note.id = 'selectionQuickPairing';
    fragment.append(note);

    fragment.append(makeEl('h4', '', localize('ui.selectionQuick.segmentedAverageTrend')));
    const chart = makeEl('div', 'selection-quick-chart');
    const canvas = makeEl('canvas');
    canvas.id = 'selectionQuickChart';
    canvas.setAttribute('role', 'img');
    canvas.setAttribute('aria-label', localize('ui.selectionQuick.segmentedAverageTrend'));
    chart.append(canvas, makeEl('div', 'analysis-chart-tooltip'));
    fragment.append(chart);

    const details = makeEl('details');
    details.id = 'selectionQuickSegments';
    details.append(makeEl('summary', '', localize('ui.selectionQuick.segmentDetails')));
    const holder = makeEl('div');
    holder.id = 'selectionQuickSegmentTable';
    details.append(holder);
    fragment.append(details);
    return fragment;
  }

  private format(exact: string): string {
    return exact === '' ? '—' : this.formatNumber({ exact, display: exact });
  }

  private numeric(exact: string): HTMLSpanElement {
    const span = makeEl('span', 'selection-quick-value', this.format(exact));
    span.title = exact === '' ? localize('ui.selectionQuick.noValidValues') : localize('ui.selectionQuick.exactValue', { exact });
    return span;
  }

  private sourceButton(row: number, col: number): HTMLButtonElement {
    const ref = `${this.colName(col)}${row}`;
    const button = makeEl('button', 'selection-quick-ref', ref);
    button.type = 'button';
    button.title = localize('ui.selectionQuick.locateSourceCell', { ref });
    button.addEventListener('click', () => {
      void this.locate(ref);
    });
    return button;
  }

  renderSeries(): void {
    this.changed();
    if (!this.page || !this.dialog.open) return;
    const page = this.page;
    const key = requireSelect('selectionQuickSeries').value;
    const delta = key === 'difference';
    const diffSeries = delta ? page.difference : undefined;
    const colSeries = delta ? undefined : page.columns.find((c) => String(c.column) === key);
    const series: SelectionQuickSeries | undefined = delta ? diffSeries : colSeries;
    if (!series) return;
    this.series = series;
    const summary = series.summary;
    const holder = requireEl('selectionQuickSummary');
    holder.replaceChildren();
    const col = delta ? page.minuend : Number(key);
    const cards: readonly (readonly [string, string, number?])[] = [
      [localize('ui.selectionQuick.numberValues'), String(summary.count)],
      [localize('ui.common.total'), summary.sum],
      [localize('ui.common.average'), summary.average],
      [localize('ui.common.minimum'), summary.minimum, summary.minimumRow],
      [localize('ui.common.maximum'), summary.maximum, summary.maximumRow],
    ];
    for (const [title, exact, row] of cards) {
      const card = makeEl('div');
      card.append(makeEl('small', '', title), this.numeric(exact));
      if (row) card.append(this.sourceButton(row, col));
      holder.append(card);
    }
    requireEl('selectionQuickPairing').textContent =
      delta && diffSeries
        ? localize('ui.selectionQuick.pairingRow', { paired: diffSeries.paired.toLocaleString(interfaceLocale()), detail: diffSeries.unpaired ? ` ${localize('ui.selectionQuick.unpairedRowsMarked', { unpaired: diffSeries.unpaired.toLocaleString(interfaceLocale()) })}` : '' })
        : '';
    requireEl('selectionQuickSegmentTable').replaceChildren(
      this.table(
        [localize('ui.selectionQuick.minutes'), localize('ui.selectionQuick.selectRowOrder'), localize('ui.selectionQuick.sourceFirstRowLastRow'), localize('ui.selectionQuick.numericalValue'), localize('ui.common.total'), localize('ui.common.average'), localize('ui.common.minimum'), localize('ui.common.maximum')],
        series.segments.map((p, i) => [
          i + 1,
          `${p.first}–${p.last}`,
          `${p.firstRow} → ${p.lastRow}`,
          p.summary.count,
          this.numeric(p.summary.sum),
          this.numeric(p.summary.average),
          this.numeric(p.summary.minimum),
          this.numeric(p.summary.maximum),
        ]),
      ),
    );
    this.draw();
  }

  draw(): void {
    if (!this.dialog.open || !this.page || !this.series) return;
    const series = this.series;
    const plot = buildSelectionQuickChart(series);
    const canvas = byId('selectionQuickChart');
    if (canvas instanceof HTMLCanvasElement && this.drawChart) {
      this.drawChart(canvas, plot, 'line', (keys) => {
        const at = Number(keys[0]) - 1;
        const p = series.segments[at];
        if (Number.isInteger(at) && p) {
          this.status(
            localize('ui.selectionQuick.paragraphSelectedRowsPreciseAveraging', { at: at + 1, first: p.first, last: p.last, average: p.summary.average, firstRow: p.firstRow, lastRow: p.lastRow }),
          );
        }
      });
    }
    this.drawn = { plot, page: this.page, series };
    this.getChartExport?.()?.revalidate();
  }

  exportImage(): void {
    if (!this.page || !this.drawn || !this.series || this.busy || !this.ready() || !this.same(this.bound)) {
      return;
    }
    const canvas = byId('selectionQuickChart');
    if (!(canvas instanceof HTMLCanvasElement)) return;
    const page = this.page;
    const bound = this.bound;
    const series = this.series;
    const key = requireSelect('selectionQuickSeries').value;
    const expectedResult = this.drawn.plot;
    const current = (): boolean =>
      this.dialog.open &&
      this.page === page &&
      this.series === series &&
      !this.busy &&
      this.ready() &&
      this.same(bound) &&
      requireSelect('selectionQuickSeries').value === key;
    this.getChartExport?.()?.open({
      kind: 'selection-quick',
      canvas,
      expectedResult,
      mode: 'line',
      seriesKey: key,
      result: page,
      origin: {
        workbookKey: page.workbookKey,
        book: page.book,
        sheet: page.sheet,
        viewId: page.viewId,
      },
      title: localize('ui.selectionQuick.segmentTrendSelectedArea'),
      source: `${this.s.fileName || localize('ui.common.currentWorkbook')} / ${page.sheet}`,
      scope: localize('ui.selectionQuick.rows', { start: page.start, end: page.end, rows: page.rows, detail: page.skipFirstRow ? localize('ui.selectionQuick.skipFirstLine') : '' }),
      metric:
        key === 'difference'
          ? localize('ui.selectionQuick.differenceSegmentedAverage', { colName: this.colName(page.minuend), colName2: this.colName(page.subtrahend) })
          : localize('ui.selectionQuick.sourceColumnSegmentedAverage', { colName: this.colName(Number(key)) }),
      unit: localize('ui.selectionQuick.originalUnits'),
      current,
      notify: this.toast,
    });
  }

  async locate(ref: string): Promise<void> {
    if (!this.page || !this.bound || !this.same(this.bound) || this.busy || !this.ready()) return;
    const bound = this.bound;
    const sheet = this.page.sheet;
    this.close();
    const ticket = ++this.navigation;
    const guard = (): boolean => {
      const now = this.binding();
      return (
        !this.dialog.open &&
        this.navigation === ticket &&
        now.sourceKey === bound.sourceKey &&
        now.index === bound.index &&
        this.ready()
      );
    };
    try {
      await this.navigate(sheet, ref, guard);
    } catch (error: unknown) {
      if (guard()) this.toast(errorMessage(error), 'error');
    }
  }
}
