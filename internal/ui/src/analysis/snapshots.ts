/**
 * Comparison Snapshots dialog controller: capture, browse, delete, and compare
 * two saved exact analysis snapshots side-by-side with sparkline trends.
 */
import { interfaceLocale, t as localize, th as localizeHTML } from '../core/i18n.ts';
import type {
  AnalysisCell,
  ComparisonSnapshot,
  ComparisonSnapshotCatalog,
  SnapshotDelta,
} from '../contracts/analysis.ts';
import type { ApiClient, HttpError, ToastFn } from '../contracts/common.ts';
import type { SnapshotLimits } from '../contracts/formats.ts';
import { errorMessage } from '../core/display-format.ts';
import { requireButton, requireEl, requireInput, requireSelect } from '../core/dom.ts';
import { drawSparkline } from './charts.ts';
import { formatAnalysisNumber } from './numbers.ts';
import {
  snapshotDescription,
  snapshotMetricLabel,
  snapshotSeriesCells,
  validateSnapshot,
  validateSnapshotCatalog,
  validateSnapshotComparison,
  type ValidatedSnapshotComparison,
} from './snapshot-model.ts';

export interface SnapshotCaptureContext {
  readonly job: string;
  readonly metric: number;
  readonly workbookKey: string;
  readonly book: string;
  readonly title: string;
  readonly current: () => boolean;
}

export interface SnapshotsOptions {
  readonly api: ApiClient;
  readonly toast: ToastFn;
  readonly limits: SnapshotLimits;
  readonly drawSparkline?: typeof drawSparkline;
}

interface AcceptedSnapshotsPair {
  readonly left: ComparisonSnapshot;
  readonly right: ComparisonSnapshot;
  readonly comparison: ValidatedSnapshotComparison | null;
}

interface DeleteIntent {
  readonly id: string;
  readonly side: 'Left' | 'Right';
}

const DELTA_STATES: Readonly<Record<string, string>> = {
  only_left: localize('ui.snapshots.onlyBaseline'),
  only_right: localize('ui.snapshots.onlyComparison'),
  empty: localize('ui.snapshots.noValidValues'),
  unavailable: localize('ui.snapshots.incomparable'),
  missing: localize('ui.snapshots.noneOnBothSides'),
  zero_base: localize('ui.snapshots.baseline'),
};

function makeNode<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  cls = '',
  text = '',
): HTMLElementTagNameMap[K] {
  const n = document.createElement(tag);
  n.className = cls;
  n.textContent = text;
  return n;
}

function requireCanvas(id: string): HTMLCanvasElement {
  const node = requireEl(id);
  if (!(node instanceof HTMLCanvasElement)) {
    throw new Error(`Expected HTMLCanvasElement for #${id}`);
  }
  return node;
}

function isAbortError(error: unknown): boolean {
  return error instanceof Error && error.name === 'AbortError';
}

function getHttpStatus(error: unknown): number | undefined {
  if (typeof error === 'object' && error !== null && 'status' in error) {
    const status = (error as HttpError).status;
    if (typeof status === 'number') return status;
  }
  return undefined;
}

export class SnapshotsController {
  private readonly api: ApiClient;
  private readonly limits: SnapshotLimits;
  private readonly sparklineFn: typeof drawSparkline;

  public readonly dialog: HTMLDialogElement;
  public epoch = 0;
  private readSerial = 0;
  private pairSerial = 0;
  public busy = false;
  public capture: SnapshotCaptureContext | null = null;
  public store: ComparisonSnapshotCatalog | null = null;
  public accepted: AcceptedSnapshotsPair | null = null;
  private deleteIntent: DeleteIntent | null = null;
  private page = 0;
  private readAbort: AbortController | null = null;
  private pairAbort: AbortController | null = null;
  private readonly observer: ResizeObserver | null = null;

  constructor(options: SnapshotsOptions) {
    this.api = options.api;
    this.limits = options.limits;
    this.sparklineFn = options.drawSparkline ?? drawSparkline;
    this.dialog = this.createDialog();

    requireButton('snapshotsClose').addEventListener('click', () => this.close());
    this.dialog.addEventListener('cancel', (e: Event) => {
      e.preventDefault();
      this.close();
    });
    this.dialog.addEventListener('close', () => {
      if (!this.dialog.open) this.reset();
    });
    requireButton('snapshotsRefresh').addEventListener('click', () => {
      void this.refresh();
    });
    requireButton('snapshotsSave').addEventListener('click', () => {
      void this.save();
    });
    for (const side of ['Left', 'Right'] as const) {
      requireSelect(`snapshots${side}`).addEventListener('change', () => {
        void this.loadPair();
      });
      requireButton(`snapshotsDelete${side}`).addEventListener('click', () => {
        void this.remove(side);
      });
    }
    requireSelect('snapshotsSeries').addEventListener('change', () => {
      this.page = 0;
      this.renderRows();
      this.draw();
    });
    requireButton('snapshotsPrevious').addEventListener('click', () => {
      this.page = Math.max(0, this.page - 1);
      this.renderRows();
    });
    requireButton('snapshotsNext').addEventListener('click', () => {
      this.page++;
      this.renderRows();
    });
    if (typeof ResizeObserver !== 'undefined') {
      this.observer = new ResizeObserver(() => this.draw());
      this.observer.observe(requireEl('snapshotsSides'));
    }
    for (const event of ['excellens:readingchange', 'excellens:analysisnumberchange'] as const) {
      document.addEventListener(event, () => {
        if (this.accepted && this.dialog.open) {
          this.renderSides();
          this.renderRows();
          this.draw();
        }
      });
    }
  }

  private createDialog(): HTMLDialogElement {
    const d = makeNode('dialog', 'float-dialog snapshots-dialog');
    d.id = 'snapshotsDialog';
    d.setAttribute('aria-labelledby', 'snapshotsTitle');
    d.innerHTML = `<div class="dialog-head"><div id="snapshotsTitle" class="dialog-title">${localizeHTML('ui.snapshots.compareSnapshots')}</div><button id="snapshotsClose" type="button" class="dialog-close" aria-label="${localizeHTML('ui.snapshots.disableComparisonSnapshots')}">×</button></div>
      <form id="snapshotsCapture" class="snapshots-tools"><input id="snapshotsName" class="field-control" aria-label="${localizeHTML('ui.snapshots.newSnapshotName')}" maxlength="80" placeholder="${localizeHTML('ui.snapshots.snapshotName')}"><button id="snapshotsSave" type="button" class="mini-button">${localizeHTML('ui.snapshots.saveCurrentResults')}</button></form>
      <div class="snapshots-scroll"><div id="snapshotsSides" class="snapshots-sides">${(['Left', 'Right'] as const)
        .map(
          (side, i) =>
            `<section><div class="snapshots-picker"><label for="snapshots${side}">${i ? localize('ui.common.comparison') : localize('ui.common.baselines')}</label><select id="snapshots${side}" class="field-control"></select><button id="snapshotsDelete${side}" type="button" class="mini-button" aria-label="${localizeHTML('ui.snapshots.deleteSnapshot', { detail: i ? localize('ui.common.comparison') : localize('ui.common.baselines') })}">${localizeHTML('ui.snapshots.delete')}</button></div><div id="snapshots${side}Content" class="snapshots-content"></div></section>`,
        )
        .join('')}</div>
      <div id="snapshotsComparison" hidden><div class="snapshots-table-tools"><label>${localizeHTML('ui.snapshots.series')} <select id="snapshotsSeries" class="field-control"></select></label><span id="snapshotsCount"></span></div><div id="snapshotsTable" class="snapshots-table"></div><div class="snapshots-pager"><button id="snapshotsPrevious" class="mini-button" type="button">${localizeHTML('ui.snapshots.previousPage')}</button><span id="snapshotsPage"></span><button id="snapshotsNext" class="mini-button" type="button">${localizeHTML('ui.snapshots.nextPage')}</button></div></div></div>
      <div class="snapshots-footer"><span id="snapshotsStatus" role="status" aria-live="polite"></span><button id="snapshotsRefresh" type="button" class="mini-button">${localizeHTML('ui.snapshots.refreshCatalog')}</button></div>`;
    d.querySelector('form')?.addEventListener('submit', (e: SubmitEvent) => {
      e.preventDefault();
      void this.save();
    });
    document.body.append(d);
    return d;
  }

  status(text: string, error = false): void {
    const node = requireEl('snapshotsStatus');
    node.textContent = text;
    node.classList.toggle('is-error', error);
    node.setAttribute('role', error ? 'alert' : 'status');
  }

  controls(): void {
    const available = Boolean(this.capture?.current());
    requireEl('snapshotsCapture').hidden = !available;
    requireButton('snapshotsSave').disabled = this.busy || !this.store || !available;
    requireInput('snapshotsName').disabled = this.busy;
    requireButton('snapshotsRefresh').disabled = this.busy;
    for (const side of ['Left', 'Right'] as const) {
      const sel = requireSelect(`snapshots${side}`);
      sel.disabled = this.busy;
      requireButton(`snapshotsDelete${side}`).disabled = this.busy || !sel.value;
    }
  }

  open(capture: SnapshotCaptureContext | null = null): void {
    if (this.dialog.open) {
      this.capture = capture?.current() ? capture : null;
      this.controls();
      return;
    }
    this.reset();
    this.capture = capture?.current() ? capture : null;
    requireInput('snapshotsName').value = capture?.title || localize('ui.snapshots.analyzeSnapshots');
    this.controls();
    this.dialog.showModal();
    void this.refresh();
  }

  reset(): void {
    ++this.epoch;
    ++this.readSerial;
    ++this.pairSerial;
    this.readAbort?.abort();
    this.pairAbort?.abort();
    this.readAbort = null;
    this.pairAbort = null;
    this.busy = false;
    this.capture = null;
    this.store = null;
    this.accepted = null;
    this.deleteIntent = null;
    this.page = 0;
    for (const side of ['Left', 'Right'] as const) {
      requireSelect(`snapshots${side}`).replaceChildren();
      requireEl(`snapshots${side}Content`).replaceChildren();
    }
    requireEl('snapshotsComparison').hidden = true;
    requireEl('snapshotsTable').replaceChildren();
    this.status('');
  }

  close(): void {
    this.reset();
    if (this.dialog.open) this.dialog.close();
  }

  changed(): void {
    if (this.capture && !this.capture.current()) {
      this.capture = null;
      this.controls();
    }
  }

  async refresh(): Promise<void> {
    if (this.busy || !this.dialog.open) return;
    const epoch = this.epoch;
    const serial = ++this.readSerial;
    this.readAbort?.abort();
    const ctrl = new AbortController();
    this.readAbort = ctrl;
    this.status(localize('ui.common.reading'));
    try {
      const raw = await this.api('/api/analysis/snapshots', { signal: ctrl.signal });
      const data = validateSnapshotCatalog(raw, this.limits);
      const isOpen = (): boolean => this.dialog.open;
      if (this.epoch !== epoch || serial !== this.readSerial || !isOpen()) return;
      this.adoptCatalog(data);
      await this.loadPair();
    } catch (e: unknown) {
      if (this.epoch === epoch && serial === this.readSerial && !isAbortError(e)) {
        this.status(errorMessage(e), true);
      }
    }
  }

  adoptCatalog(data: ComparisonSnapshotCatalog, newID = ''): void {
    this.store = data;
    const items = data.items;
    const ids = new Set(items.map((i) => i.id));
    for (const [i, side] of (['Left', 'Right'] as const).entries()) {
      const node = requireSelect(`snapshots${side}`);
      const old = node.value;
      node.replaceChildren(new Option(localize('ui.snapshots.chooseSnapshot'), ''));
      for (const item of items) node.append(new Option(`${item.name} — ${item.file}`, item.id));
      const fallback = items[Math.max(0, items.length - (i ? 1 : 2))]?.id ?? '';
      node.value = side === 'Right' && newID ? newID : ids.has(old) ? old : fallback;
    }
    this.controls();
  }

  async save(): Promise<void> {
    if (this.busy || !this.store || !this.capture?.current()) return;
    const capture = this.capture;
    const name = requireInput('snapshotsName').value.trim();
    if (!name) {
      this.status(localize('ui.snapshots.enterSnapshotName'), true);
      return;
    }
    await this.write(
      {
        op: 'capture',
        name,
        jobId: capture.job,
        metricIndex: capture.metric,
        workbookKey: capture.workbookKey,
        book: capture.book,
      },
      true,
    );
  }

  async remove(side: 'Left' | 'Right'): Promise<void> {
    if (this.busy || !this.store) return;
    const id = requireSelect(`snapshots${side}`).value;
    if (!id) return;
    if (this.deleteIntent?.id !== id || this.deleteIntent.side !== side) {
      this.deleteIntent = { id, side };
      for (const key of ['Left', 'Right'] as const) {
        requireButton(`snapshotsDelete${key}`).textContent = key === side ? localize('ui.snapshots.confirmDeletion') : localize('ui.common.delete');
      }
      return;
    }
    await this.write({ op: 'delete', id });
  }

  async write(request: Record<string, unknown>, capture = false): Promise<void> {
    if (!this.store) return;
    const epoch = this.epoch;
    const revision = this.store.revision;
    const oldIDs = new Set(this.store.items.map((i) => i.id));
    this.busy = true;
    ++this.readSerial;
    this.readAbort?.abort();
    ++this.pairSerial;
    this.pairAbort?.abort();
    this.controls();
    this.status(localize('ui.snapshots.savingCurrently'));
    try {
      const raw = await this.api('/api/analysis/snapshots', {
        method: 'POST',
        body: JSON.stringify({ ...request, expectedRevision: revision }),
      });
      const data = validateSnapshotCatalog(raw, this.limits);
      if (this.epoch !== epoch || !this.dialog.open) return;
      this.busy = false;
      const id = capture ? (data.items.find((i) => !oldIDs.has(i.id))?.id ?? '') : '';
      this.adoptCatalog(data, id);
      await this.loadPair();
    } catch (e: unknown) {
      if (this.epoch === epoch && this.dialog.open) this.status(errorMessage(e), true);
    } finally {
      if (this.epoch === epoch) {
        this.busy = false;
        this.controls();
      }
    }
  }

  async loadPair(): Promise<void> {
    if (this.busy || !this.dialog.open) return;
    const leftID = requireSelect('snapshotsLeft').value;
    const rightID = requireSelect('snapshotsRight').value;
    const serial = ++this.pairSerial;
    const epoch = this.epoch;
    this.deleteIntent = null;
    for (const side of ['Left', 'Right'] as const) {
      requireButton(`snapshotsDelete${side}`).textContent = localize('ui.common.delete');
    }
    this.pairAbort?.abort();
    const ctrl = new AbortController();
    this.pairAbort = ctrl;
    this.controls();
    if (!leftID || !rightID) {
      this.accepted = null;
      for (const side of ['Left', 'Right'] as const) {
        requireEl(`snapshots${side}Content`).replaceChildren();
      }
      requireEl('snapshotsTable').replaceChildren();
      requireEl('snapshotsComparison').hidden = true;
      this.status(this.store?.items.length ? localize('ui.snapshots.selectTwoSnapshots') : localize('ui.snapshots.noSnapshotsYet'));
      return;
    }
    const current = (): boolean =>
      this.dialog.open &&
      epoch === this.epoch &&
      serial === this.pairSerial &&
      !ctrl.signal.aborted;
    this.status(localize('ui.snapshots.comparing'));
    try {
      const fetchSnap = async (id: string): Promise<ComparisonSnapshot> => {
        const raw = await this.api(`/api/analysis/snapshots?id=${encodeURIComponent(id)}`, {
          signal: ctrl.signal,
        });
        return validateSnapshot(raw, id, this.limits);
      };
      const [left, right] = await Promise.all([fetchSnap(leftID), fetchSnap(rightID)]);
      if (!current()) return;
      let comparison: ValidatedSnapshotComparison | null = null;
      let message = '';
      try {
        const rawCmp = await this.api(
          `/api/analysis/snapshots/compare?${new URLSearchParams({ left: leftID, right: rightID }).toString()}`,
          { signal: ctrl.signal },
        );
        comparison = validateSnapshotComparison(rawCmp, left, right, this.limits);
      } catch (e: unknown) {
        if (getHttpStatus(e) !== 422) throw e;
        message = errorMessage(e);
      }
      if (!current()) return;
      this.accepted = { left, right, comparison };
      this.page = 0;
      this.renderSides();
      const select = requireSelect('snapshotsSeries');
      select.replaceChildren(new Option(localize('ui.snapshots.groupSummary'), '-1'));
      for (const [i, key] of (comparison?.columnKeys ?? []).entries()) {
        select.append(new Option(key || localize('ui.snapshots.blankColumns'), String(i)));
      }
      select.value = '-1';
      this.renderRows();
      this.draw();
      this.status(message, Boolean(message));
    } catch (e: unknown) {
      if (current() && !isAbortError(e)) {
        this.status(`${errorMessage(e)}${this.accepted ? localize('ui.common.keepPreviousResult') : ''}`, true);
      }
    }
  }

  renderSides(): void {
    if (!this.accepted) return;
    const sides: readonly (readonly ['Left' | 'Right', ComparisonSnapshot])[] = [
      ['Left', this.accepted.left],
      ['Right', this.accepted.right],
    ];
    for (const [side, s] of sides) {
      const r = s.result;
      const host = requireEl(`snapshots${side}Content`);
      host.replaceChildren();
      const meta = makeNode('p', 'snapshots-meta', `${s.file}  ${new Date(s.created).toLocaleString(interfaceLocale())}`);
      const metric = makeNode('div', 'snapshots-metric', snapshotMetricLabel(s));
      const value = makeNode('strong', 'snapshots-grand', formatAnalysisNumber(r.grand));
      value.title = r.grand.comparison?.exact || r.grand.exact;
      const canvas = makeNode('canvas', 'snapshots-trend');
      canvas.id = `snapshotsTrend${side}`;
      canvas.setAttribute('role', 'img');
      canvas.setAttribute('aria-label', localize('ui.snapshots.categoryTrends', { detail: side === 'Left' ? localize('ui.common.baselines') : localize('ui.common.comparison') }));
      host.append(makeNode('h3', '', s.name), meta, metric, value, canvas);
      if (r.truncated) host.append(makeNode('p', 'snapshots-limit', localize('ui.snapshots.onlyContainsFirstCategories', { count: r.rows.length })));
      if (r.tree) host.append(makeNode('p', 'snapshots-limit', localize('ui.snapshots.firstLevelClassification')));
      const details = makeNode('details');
      const summary = makeNode('summary', '', localize('ui.snapshots.sourceRules'));
      const pre = makeNode('pre');
      details.append(summary, pre);
      details.addEventListener('toggle', () => {
        if (details.open) pre.textContent = snapshotDescription(s);
      });
      host.append(details);
    }
  }

  private renderValueCell(
    cell: AnalysisCell | SnapshotDelta['change'] | null | undefined,
  ): HTMLTableCellElement {
    const n = makeNode('td', 'snapshots-number', cell ? formatAnalysisNumber(cell) : '—');
    if (cell) {
      n.title = ('comparison' in cell && cell.comparison?.exact) || cell.exact;
    }
    return n;
  }

  renderRows(): void {
    const c = this.accepted?.comparison;
    requireEl('snapshotsComparison').hidden = !c;
    if (!c) return;
    const table = makeNode('table');
    const head = makeNode('thead');
    const tr = makeNode('tr');
    for (const title of [localize('ui.common.classification'), localize('ui.common.baselines'), localize('ui.common.comparison'), localize('ui.snapshots.difference'), localize('ui.snapshots.changes')]) {
      const th = makeNode('th', '', title);
      th.scope = 'col';
      tr.append(th);
    }
    head.append(tr);
    table.append(head);

    const col = Number(requireSelect('snapshotsSeries').value);
    const body = makeNode('tbody');
    const pages = Math.max(1, Math.ceil(c.rows.length / this.limits.pageRows));
    this.page = Math.min(Math.max(0, this.page), pages - 1);

    const add = (
      host: HTMLTableSectionElement,
      keys: readonly string[],
      d: SnapshotDelta | undefined,
    ): void => {
      if (!d) return;
      const row = makeNode('tr');
      const label = makeNode('td', '', keys.map((k) => k || localize('ui.common.empty')).join(' / ') || localize('ui.common.all'));
      label.title = JSON.stringify(keys);
      row.append(label, this.renderValueCell(d.left), this.renderValueCell(d.right));
      row.append(
        d.status
          ? makeNode('td', 'snapshots-state', DELTA_STATES[d.status] ?? localize('ui.snapshots.incomparable'))
          : this.renderValueCell(d.difference),
      );
      row.append(
        d.change.status
          ? makeNode('td', 'snapshots-state', DELTA_STATES[d.change.status] ?? localize('ui.snapshots.unableCalculate'))
          : this.renderValueCell(d.change),
      );
      host.append(row);
    };

    for (const r of c.rows.slice(
      this.page * this.limits.pageRows,
      (this.page + 1) * this.limits.pageRows,
    )) {
      add(body, r.keys, col < 0 ? r.total : r.values[col]);
    }
    table.append(body);
    const foot = makeNode('tfoot');
    add(foot, [localize('ui.snapshots.totalAmount')], col < 0 ? c.grand : c.totals[col]);
    table.append(foot);
    requireEl('snapshotsTable').replaceChildren(table);
    requireButton('snapshotsPrevious').disabled = this.page === 0;
    requireButton('snapshotsNext').disabled = this.page >= pages - 1;
    requireEl('snapshotsPage').textContent = `${this.page + 1} / ${pages}`;
    requireEl('snapshotsCount').textContent = localize('ui.snapshots.categories', { count: c.rows.length });
  }

  draw(): void {
    if (!this.dialog.open || !this.accepted) return;
    const a = this.accepted;
    const c = a.comparison;
    const j = Number(requireSelect('snapshotsSeries').value);
    const left: (AnalysisCell | null)[] = c
      ? c.rows.map((r) => (j < 0 ? r.total : r.values[j])?.left ?? null)
      : snapshotSeriesCells(a.left, null);
    const right: (AnalysisCell | null)[] = c
      ? c.rows.map((r) => (j < 0 ? r.total : r.values[j])?.right ?? null)
      : snapshotSeriesCells(a.right, null);
    const domain = [...left, ...right];
    this.sparklineFn(requireCanvas('snapshotsTrendLeft'), left, domain);
    this.sparklineFn(requireCanvas('snapshotsTrendRight'), right, domain);
  }
}
