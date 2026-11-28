/**
 * Shared read-only precedent/dependent tracing controller.
 * Results and origin survive minimization. Arrival rectangles live above the
 * grid layer, never inside grid layout geometry.
 */
import { t as localize } from '../core/i18n.ts';
import type { ActivityHandle, ApiClient, CellRef, MergeRange, ToastFn } from '../contracts/common.ts';
import type { CellData, SearchHit, SearchStatusResponse, SelectedCell } from '../contracts/workbook.ts';
import { beginActivity } from '../core/activity.ts';
import { errorMessage } from '../core/display-format.ts';
import { enterMotion, exitMotion, stopMotion } from '../core/motion.ts';
import type { TraceRefEntry, WorkbookState } from '../grid/state.ts';
import {
  decodeTraceStartResponse,
  decodeTraceStatusApiResponse,
} from '../core/http-decoders.generated.ts';

export type TraceKind = 'precedents' | 'dependents';

export interface TraceVisibleRect {
  readonly left: number;
  readonly top: number;
  readonly right: number;
  readonly bottom: number;
  readonly pane: string;
}

/** @httpResponse TraceStartResponse */
export interface TraceStartResponse {
  readonly jobId: string;
  readonly reused?: boolean;
}

/** @httpResponse TraceStatusApiResponse */
export interface TraceStatusApiResponse extends Omit<SearchStatusResponse, 'matches'> {
  readonly matches?: readonly SearchHit[] | null;
}

interface TraceJobHandle {
  readonly id: string;
  readonly owned: boolean;
}

interface TraceWaitHandle {
  readonly resolve: () => void;
  readonly timer: unknown;
}

interface ArrivalBox {
  readonly left: number;
  readonly top: number;
  readonly width: number;
  readonly height: number;
}

export interface TracingTimers {
  readonly now: () => number;
  readonly setTimeout: (fn: () => void, ms: number) => unknown;
  readonly clearTimeout: (id: unknown) => void;
  readonly requestAnimationFrame: (fn: FrameRequestCallback) => number;
  readonly cancelAnimationFrame: (id: number) => void;
}

export interface TracingMotion {
  readonly enter: typeof enterMotion;
  readonly exit: typeof exitMotion;
  readonly stop: typeof stopMotion;
}

export interface TracingOptions {
  readonly state: WorkbookState;
  readonly api: ApiClient;
  readonly panel: HTMLDialogElement;
  readonly dock: HTMLElement;
  readonly layer: HTMLElement;
  readonly grid: HTMLElement;
  readonly title: HTMLElement;
  readonly formula: HTMLElement;
  readonly summary: HTMLElement;
  readonly list: HTMLElement;
  readonly scopeRow: HTMLElement;
  readonly scope: HTMLSelectElement;
  readonly cancel: HTMLButtonElement;
  readonly minimize: HTMLButtonElement;
  readonly restore: HTMLButtonElement;
  readonly origin: HTMLButtonElement;
  readonly end: HTMLButtonElement;
  readonly parse: (formula: string, sheet: string) => TraceRefEntry[];
  readonly parseRef: (value: string | null | undefined) => CellRef | null;
  readonly getCell: (sheet: string, ref: string) => Promise<CellData>;
  readonly notify: ToastFn;
  readonly readScope?: () => string;
  readonly writeScope?: (value: string) => void;
  readonly navigate: (
    sheet: string,
    ref: string,
    guard?: () => boolean,
  ) => Promise<unknown>;
  readonly atTarget: (ref: TraceRefEntry) => boolean;
  readonly bounds: (row: number, col: number) => MergeRange;
  readonly visibleRect: (node: HTMLElement) => TraceVisibleRect | null;
  readonly setRefs: (refs: TraceRefEntry[]) => void;
  readonly timers?: TracingTimers;
  readonly motion?: TracingMotion;
}

const NAMES: Readonly<Record<TraceKind, string>> = {
  precedents: localize('ui.tracing.trackingReferences'),
  dependents: localize('ui.tracing.tracingSubordinateRelations'),
};

export class Tracing {
  public refs: TraceRefEntry[] = [];
  public source: SelectedCell | null = null;
  public kind: TraceKind = 'precedents';
  public minimized = false;

  private readonly o: TracingOptions;
  private readonly panel: HTMLDialogElement;
  private readonly dock: HTMLElement;
  private readonly layer: HTMLElement;
  private readonly timers: TracingTimers;
  private readonly motionOps: TracingMotion;

  private request = 0;
  private nav = 0;
  private motion = 0;
  private job: TraceJobHandle | null = null;
  private waiting: TraceWaitHandle | null = null;
  private activity: ActivityHandle | null = null;
  private muted = false;
  private didLocate = false;
  private book = '';
  private count = 100;
  private pulseRefs: readonly TraceRefEntry[] = [];
  public pulseLabel = '';
  public pulseUntil = 0;
  private pulseTicket = 0;
  public pulseTimer: unknown = undefined;
  private pulseFrame = 0;
  private ignoreClose = 0;

  constructor(options: TracingOptions) {
    this.o = options;
    this.panel = options.panel;
    this.dock = options.dock;
    this.layer = options.layer;
    this.timers = options.timers ?? {
      now: () => Date.now(),
      setTimeout: (fn, ms) => setTimeout(fn, ms),
      clearTimeout: (id) => {
        if (id !== undefined) clearTimeout(id as ReturnType<typeof setTimeout>);
      },
      requestAnimationFrame: (fn) => requestAnimationFrame(fn),
      cancelAnimationFrame: (id) => cancelAnimationFrame(id),
    };
    this.motionOps = options.motion ?? {
      enter: enterMotion,
      exit: exitMotion,
      stop: stopMotion,
    };

    options.minimize.addEventListener('click', () => {
      void this.minimize();
    });
    options.restore.addEventListener('click', () => this.restore());
    options.origin.addEventListener('click', () => {
      void this.goOrigin();
    });
    options.end.addEventListener('click', () => this.reset());
    options.cancel.addEventListener('click', () => {
      this.stopRun();
      options.summary.textContent = localize('ui.tracing.canceledFoundResultsCanStill');
      options.cancel.hidden = true;
    });
    options.scope.addEventListener('change', () => {
      options.writeScope?.(options.scope.value);
      if (this.source) void this.open(this.kind, this.source);
    });
    this.panel.addEventListener('close', () => {
      if (this.ignoreClose) {
        this.ignoreClose--;
        return;
      }
      if (this.source) this.reset();
    });
    this.panel.addEventListener('keydown', (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        event.stopPropagation();
        void this.minimize();
      }
    });
    options.grid.addEventListener('scroll', () => this.refreshPulse(), { passive: true });
    window.addEventListener('resize', () => this.refreshPulse());
  }

  stopRun(): void {
    this.activity?.end();
    ++this.request;
    if (this.waiting) {
      this.timers.clearTimeout(this.waiting.timer);
      this.waiting.resolve();
      this.waiting = null;
    }
    const job = this.job;
    this.job = null;
    if (job?.owned) {
      this.o
        .api('/api/search/cancel', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ id: job.id }),
        })
        .catch(() => {
          // Best-effort cancellation of dependent trace search.
        });
    }
  }

  reset(): void {
    this.stopRun();
    ++this.nav;
    ++this.motion;
    this.clearPulse();
    this.source = null;
    this.refs = [];
    this.muted = false;
    this.minimized = false;
    this.o.setRefs([]);
    this.dock.hidden = true;
    this.motionOps.stop(this.panel);
    if (this.panel.open) {
      this.ignoreClose++;
      this.panel.close();
    }
  }

  selectionChanged(): void {
    ++this.nav;
    this.clearPulse();
    if (this.source) {
      this.muted = true;
      this.o.setRefs([]);
    }
  }

  updateDock(): void {
    this.o.restore.textContent = `${NAMES[this.kind]} · ${this.refs.length}`;
    this.o.restore.title = this.source
      ? localize('ui.tracing.expandTrackingResults', { sheet: this.source.sheet, ref: this.source.ref })
      : '';
    this.o.origin.title = this.source
      ? localize('ui.tracing.returnStartPoint', { sheet: this.source.sheet, ref: this.source.ref })
      : '';
  }

  restore(): void {
    if (!this.source) return;
    ++this.motion;
    this.minimized = false;
    this.dock.hidden = true;
    if (!this.panel.open) this.panel.show();
    this.motionOps.enter(this.panel, { distance: 6 });
    this.o.summary.focus({ preventScroll: true });
  }

  private isMinimized(): boolean {
    return this.minimized;
  }

  private isPanelOpen(): boolean {
    return this.panel.open;
  }

  private isMuted(): boolean {
    return this.muted;
  }

  private hasLocated(): boolean {
    return this.didLocate;
  }

  async minimize(): Promise<void> {
    if (!this.source) return;
    const ticket = ++this.motion;
    this.minimized = true;
    this.updateDock();
    this.dock.hidden = false;
    this.motionOps.enter(this.dock, { distance: 3 });
    if (this.panel.open) {
      await this.motionOps.exit(this.panel);
      if (ticket === this.motion && this.isMinimized() && this.isPanelOpen()) {
        this.ignoreClose++;
        this.panel.close();
      }
    }
  }

  async open(kind: TraceKind, source: SelectedCell | null = this.o.state.selected): Promise<void> {
    if (!source || (kind === 'precedents' && (!source.hasFormula || !source.formula))) return;
    this.stopRun();
    ++this.nav;
    this.clearPulse();
    this.muted = false;
    this.didLocate = false;
    const sheet = source.sheet || this.o.state.sheet || '';
    this.source = { ...source, sheet };
    this.book = this.o.state.workbookKey;
    this.kind = kind;
    this.refs = [];
    this.o.setRefs([]);

    const ticket = this.request;
    this.count = 100;
    this.o.title.textContent = `${NAMES[kind]} · ${this.source.sheet}!${this.source.ref}`;
    this.o.formula.textContent =
      this.source.formula || `${this.source.sheet}!${this.source.ref}`;
    this.o.scopeRow.hidden = kind !== 'dependents';
    this.o.scope.value = this.o.readScope?.() || this.o.scope.value || 'current';
    this.o.list.replaceChildren();
    this.o.summary.textContent =
      kind === 'dependents' ? localize('ui.tracing.currentlyFindingFormulasThatDirectly') : localize('ui.tracing.failedReadDirectReferenceCurrent');
    this.o.cancel.hidden = kind !== 'dependents';
    this.restore();

    if (kind === 'precedents') {
      this.refs = this.o.parse(this.source.formula ?? '', this.source.sheet).map((ref) => ({
        ...ref,
        sheet:
          this.o.state.sheets.find(
            (s) => s.name.toLocaleLowerCase() === ref.sheet.toLocaleLowerCase(),
          )?.name ?? ref.sheet,
      }));
      this.o.setRefs(this.refs);
      this.render(ticket);
      this.o.summary.textContent = this.refs.length
        ? localize('ui.tracing.directReferencesClickLocateContinue', { count: this.refs.length })
        : localize('ui.tracing.a1ReferenceNotRecognizedNames');
      this.updateDock();
      this.pulse(this.refs, localize('ui.tracing.where', { value: NAMES[kind], count: this.refs.length }));
      return;
    }

    const scope = this.o.scope.value;
    const query =
      scope === 'all'
        ? `'${this.source.sheet.replaceAll("'", "''")}'!${this.source.ref}`
        : this.source.ref;
    const activity = beginActivity(localize('ui.tracing.trackingDependentFormulas'));
    this.activity = activity;
    try {
      const start = await this.o.api('/api/search/start', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          sheet: this.source.sheet,
          query,
          mode: 'reference',
          scope,
        }),
      }, decodeTraceStartResponse);
      if (ticket !== this.request) {
        if (!start.reused) {
          this.o
            .api('/api/search/cancel', {
              method: 'POST',
              headers: { 'Content-Type': 'application/json' },
              body: JSON.stringify({ id: start.jobId }),
            })
            .catch(() => {
              // Ignore cleanup failure for stale search job.
            });
        }
        return;
      }
      this.job = { id: start.jobId, owned: !start.reused };
      const sourceSheet = this.source.sheet;
      while (ticket === this.request) {
        const data = await this.o.api(
          `/api/search/status?id=${encodeURIComponent(start.jobId)}`,
          undefined,
          decodeTraceStatusApiResponse,
        );
        if (ticket !== this.request) return;
        activity?.report(data.progress, 100, data.stage);
        const refs: TraceRefEntry[] = [];
        for (const match of data.matches ?? []) {
          const p = this.o.parseRef(match.ref);
          if (!p) continue;
          const matchSheet = match.sheet || sourceSheet;
          refs.push({
            sheet: matchSheet,
            label: `${matchSheet}!${match.ref}`,
            start: p,
            end: p,
            formula: match.formula,
          });
        }
        const changed =
          refs.length !== this.refs.length ||
          refs.at(-1)?.label !== this.refs.at(-1)?.label ||
          data.status !== 'running';
        this.refs = refs;
        if (changed) {
          if (!this.isMuted()) this.o.setRefs(this.refs);
          this.render(ticket);
          this.updateDock();
        }
        const incomplete = data.truncated ? ` ${localize('ui.tracing.resultsHaveTruncatedUpItems')}` : '';
        if (data.status !== 'running') {
          this.job = null;
          this.o.cancel.hidden = true;
          this.o.summary.textContent =
            data.status === 'error'
              ? localize('ui.tracing.notCompletedFound', { detail: data.error || localize('ui.tracing.trackingFailed'), count: this.refs.length })
              : data.status === 'cancelled'
                ? localize('ui.tracing.canceledFound', { count: this.refs.length })
                : localize('ui.tracing.foundDirectDependentFormulasFrom', { count: this.refs.length, incomplete });
          if (data.status === 'done' && !this.isMuted() && !this.hasLocated()) {
            this.pulse(this.refs, localize('ui.tracing.where', { value: NAMES[kind], count: this.refs.length }));
          }
          return;
        }
        this.o.summary.textContent =
          localize('ui.tracing.location', { stage: data.stage || localize('ui.tracing.searching'), round: Math.round(data.progress || 0), count: this.refs.length });
        await new Promise<void>((resolve) => {
          this.waiting = {
            resolve,
            timer: this.timers.setTimeout(() => {
              this.waiting = null;
              resolve();
            }, 250),
          };
        });
      }
    } catch (error: unknown) {
      if (ticket === this.request) {
        this.stopRun();
        this.o.summary.textContent = localize('ui.tracing.trackingFailed0ff1996', { detail: errorMessage(error) });
        this.o.cancel.hidden = true;
      }
    } finally {
      activity?.end();
    }
  }

  render(ticket: number): void {
    const fragment = document.createDocumentFragment();
    for (const ref of this.refs.slice(0, this.count)) {
      const item = document.createElement('div');
      item.className = 'trace-item';
      const addressEl = document.createElement('div');
      addressEl.className = 'trace-ref';
      addressEl.textContent = ref.label ?? '';
      addressEl.title = ref.label ?? '';
      const value = document.createElement('div');
      value.className = 'trace-value';
      if (ref.formula) {
        value.textContent = ref.formula;
      } else if (ref.range) {
        value.textContent = localize('ui.tracing.regionReferencePositioningAtTop');
      } else if (ref.sheet !== this.o.state.sheet) {
        value.textContent = localize('ui.tracing.crossSheetReferencePositionAs');
      } else {
        value.textContent = localize('ui.tracing.reading');
        this.o
          .getCell(ref.sheet, ref.start.ref)
          .then((cell) => {
            if (ticket !== this.request) return;
            const shown = cell.display === '' ? localize('ui.common.blank') : cell.display;
            value.textContent = cell.cacheMissing ? localize('ui.common.noCachingAvailable') : shown;
            value.title = cell.formula || value.textContent;
          })
          .catch((error: unknown) => {
            if (ticket === this.request) value.textContent = errorMessage(error);
          });
      }
      const go = document.createElement('button');
      go.type = 'button';
      go.className = 'trace-go';
      go.textContent = localize('ui.tracing.positioning');
      go.addEventListener('click', () => {
        void this.locate(ref);
      });
      item.append(addressEl, value, go);
      fragment.append(item);
    }
    if (this.refs.length > this.count) {
      const more = document.createElement('button');
      more.type = 'button';
      more.className = 'trace-go trace-more';
      more.textContent = localize('ui.tracing.showMore', { count: this.count, count2: this.refs.length });
      more.addEventListener('click', () => {
        this.count += 100;
        this.render(ticket);
      });
      fragment.append(more);
    }
    const top = this.o.list.scrollTop;
    this.o.list.replaceChildren(fragment);
    this.o.list.scrollTop = top;
  }

  async locate(ref: TraceRefEntry, origin = false): Promise<void> {
    if (!this.source) return;
    const ticket = ++this.nav;
    const book = this.book;
    this.clearPulse();
    this.muted = false;
    this.didLocate = true;
    this.o.setRefs(this.refs);
    const current = (): boolean =>
      ticket === this.nav && book === this.o.state.workbookKey && Boolean(this.source);
    try {
      await Promise.all([this.minimize(), this.o.navigate(ref.sheet, ref.start.ref, current)]);
      if (!current()) return;
      if (!this.o.atTarget(ref)) {
        this.o.notify(localize('ui.tracing.targetNotDisplayedCheckFilters'), 'warning');
        return;
      }
      const label = `${origin ? localize('ui.tracing.trackingStartPoint') : NAMES[this.kind]} · ${ref.label ?? ''}`;
      this.pulse([ref], label, true, current);
    } catch (error: unknown) {
      if (current()) this.o.notify(errorMessage(error), 'error');
    }
  }

  goOrigin(): Promise<void> | undefined {
    if (!this.source) return undefined;
    const p = this.o.parseRef(this.source.ref);
    if (!p) return undefined;
    return this.locate(
      {
        sheet: this.source.sheet,
        label: `${this.source.sheet}!${this.source.ref}`,
        start: p,
        end: p,
      },
      true,
    );
  }

  clearPulse(): void {
    ++this.pulseTicket;
    if (this.pulseTimer !== undefined) {
      this.timers.clearTimeout(this.pulseTimer);
      this.pulseTimer = undefined;
    }
    this.timers.cancelAnimationFrame(this.pulseFrame);
    this.pulseFrame = 0;
    this.pulseRefs = [];
    this.layer.hidden = true;
    this.layer.replaceChildren();
  }

  pulse(
    refs: readonly TraceRefEntry[],
    label: string,
    wait = false,
    current: () => boolean = () => true,
  ): void {
    this.clearPulse();
    this.pulseTicket++;
    const ticket = this.pulseTicket;
    const deadline = this.timers.now() + 1200;
    this.pulseRefs = refs;
    this.pulseLabel = label;
    this.pulseUntil = 0;
    const arrive = (): void => {
      this.pulseFrame = 0;
      if (ticket !== this.pulseTicket || !current()) return;
      const boxes = this.measure();
      if (boxes.length) {
        this.pulseUntil = this.timers.now() + 2000;
        this.paint(boxes);
        this.pulseTimer = this.timers.setTimeout(() => {
          if (ticket === this.pulseTicket) this.clearPulse();
        }, 2000);
      } else if (wait && this.timers.now() < deadline) {
        this.pulseFrame = this.timers.requestAnimationFrame(arrive);
      } else {
        this.clearPulse();
      }
    };
    arrive();
  }

  measure(): ArrivalBox[] {
    const sheet = this.o.state.sheet ?? '';
    const region = this.layer.parentElement?.getBoundingClientRect();
    if (!region) return [];
    const cells: { readonly node: HTMLElement; readonly bounds: MergeRange }[] = [];
    for (const node of document.querySelectorAll<HTMLElement>(
      '.grid-region .grid-cell[data-row][data-col]',
    )) {
      const b = this.o.bounds(Number(node.dataset.row), Number(node.dataset.col));
      cells.push({ node, bounds: b });
    }
    const boxes: ArrivalBox[] = [];
    const measured = new Map<HTMLElement, TraceVisibleRect | null>();
    for (const ref of this.pulseRefs) {
      if (ref.sheet.toLocaleLowerCase() !== sheet.toLocaleLowerCase()) continue;
      const groups = new Map<
        string,
        { left: number; top: number; right: number; bottom: number }
      >();
      for (const { node, bounds: b } of cells) {
        if (
          b.row > ref.end.row ||
          b.endRow < ref.start.row ||
          b.col > ref.end.col ||
          b.endCol < ref.start.col
        ) {
          continue;
        }
        let r = measured.get(node);
        if (r === undefined) {
          r = this.o.visibleRect(node);
          measured.set(node, r);
        }
        if (!r) continue;
        const old = groups.get(r.pane);
        groups.set(
          r.pane,
          old
            ? {
                left: Math.min(old.left, r.left),
                top: Math.min(old.top, r.top),
                right: Math.max(old.right, r.right),
                bottom: Math.max(old.bottom, r.bottom),
              }
            : { left: r.left, top: r.top, right: r.right, bottom: r.bottom },
        );
      }
      for (const r of groups.values()) {
        boxes.push({
          left: r.left - region.left,
          top: r.top - region.top,
          width: r.right - r.left,
          height: r.bottom - r.top,
        });
      }
    }
    return boxes;
  }

  paint(boxes: readonly ArrivalBox[]): void {
    const first = boxes[0];
    if (!first) {
      this.layer.hidden = true;
      return;
    }
    this.layer.hidden = false;
    const nodes = [...this.layer.querySelectorAll<HTMLElement>('.trace-arrival-box')];
    while (nodes.length > boxes.length) nodes.pop()?.remove();
    for (let i = 0; i < boxes.length; i++) {
      let node = nodes[i];
      if (!node) {
        node = document.createElement('div');
        node.className = 'trace-arrival-box';
        this.layer.append(node);
      }
      const b = boxes[i];
      if (!b) continue;
      node.style.left = `${b.left}px`;
      node.style.top = `${b.top}px`;
      node.style.width = `${b.width}px`;
      node.style.height = `${b.height}px`;
    }
    let label = this.layer.querySelector<HTMLElement>('.trace-arrival-label');
    if (!label) {
      label = document.createElement('div');
      label.className = 'trace-arrival-label';
      label.setAttribute('role', 'status');
      this.layer.append(label);
    }
    label.textContent = this.pulseLabel;
    const width = this.layer.clientWidth;
    label.style.left = `${Math.max(8, Math.min(first.left, width - Math.min(380, width - 16) - 8))}px`;
    label.style.top = `${Math.max(4, first.top - 29)}px`;
  }

  refreshPulse(): void {
    if (!this.pulseRefs.length || !this.pulseUntil || this.pulseFrame) return;
    this.pulseFrame = this.timers.requestAnimationFrame(() => {
      this.pulseFrame = 0;
      if (this.timers.now() >= this.pulseUntil) this.clearPulse();
      else this.paint(this.measure());
    });
  }
}
