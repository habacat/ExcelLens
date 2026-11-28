/**
 * Per-document foreground activity queue and indicator UI.
 * A task can span multiple requests and polling intervals without sharing
 * clocks or progress across tabs.
 */
import { t as localize } from './i18n.ts';
import type {
  ActivityBeginOptions,
  ActivityHandle,
  ActivityService,
  ApiRequestOptions,
  AsyncJobProgress,
} from '../contracts/common.ts';

const DELAY_MS = 500;
const clamp = (n: number): number => Math.max(0, Math.min(100, n));

interface TaskState {
  label: string;
  priority: number;
  slot: string | null | undefined;
  start: number;
  phaseStart: number;
  from: number;
  to: number;
  value: number | null;
  detail: string;
  cancel: (() => void) | null;
  pausedAt?: number;
}

export interface ActivityPaintStatus {
  readonly owner: TaskState;
  readonly remaining: number;
  readonly estimated: boolean;
  readonly label: string;
  readonly value: string;
  readonly percent: number;
  readonly cancel: (() => void) | null;
}

export interface ActivityClock {
  now(): number;
}

export interface ActivityTimers {
  setTimeout(handler: () => void, timeout?: number): number;
  clearTimeout(id: number | undefined): void;
  requestAnimationFrame(callback: FrameRequestCallback): number;
  cancelAnimationFrame(id: number): void;
  readonly document?: { readonly visibilityState?: string };
}

export class ActivityQueue {
  private readonly paint: (status: ActivityPaintStatus | null) => void;
  private readonly clock: ActivityClock;
  private readonly timers: ActivityTimers;
  private readonly tasks = new Set<TaskState>();
  private timer: number | undefined;
  private visible: TaskState | null = null;

  constructor(
    paint: (status: ActivityPaintStatus | null) => void,
    clock: ActivityClock = performance,
    timers: ActivityTimers = window,
  ) {
    this.paint = paint;
    this.clock = clock;
    this.timers = timers;
  }

  begin(label: string, options: ActivityBeginOptions = {}): ActivityHandle {
    const { priority = 1, signal, slot } = options;
    const start = this.clock.now();
    const state: TaskState = {
      label,
      priority,
      slot,
      start,
      phaseStart: start,
      from: 0,
      to: 100,
      value: null,
      detail: '',
      cancel: null,
    };
    const end = (): void => {
      if (!this.tasks.delete(state)) return;
      signal?.removeEventListener('abort', end);
      this.refresh();
    };
    if (!signal?.aborted) {
      this.tasks.add(state);
      signal?.addEventListener('abort', end, { once: true });
      this.refresh();
    }

    const report = (
      done: number | undefined,
      total: number | undefined,
      name?: string,
      detail = '',
    ): void => {
      if (name && name !== state.label) {
        state.label = name;
        state.phaseStart = this.clock.now();
        state.value = null;
        state.detail = '';
      }
      if (
        done !== undefined &&
        total !== undefined &&
        Number.isFinite(done) &&
        Number.isFinite(total) &&
        total > 0
      ) {
        state.value = clamp((done / total) * 100);
        state.detail = detail;
      }
      if (this.visible === state) this.refresh();
    };

    const task: ActivityHandle = {
      pause: (): void => {
        if (!this.tasks.has(state) || state.pausedAt !== undefined) return;
        state.pausedAt = this.clock.now();
        this.refresh();
      },
      resume: (): void => {
        if (!this.tasks.has(state) || state.pausedAt === undefined) return;
        const now = this.clock.now();
        state.start = now;
        state.phaseStart += now - state.pausedAt;
        delete state.pausedAt;
        this.refresh();
      },
      stage: (name: string, from = 0, to = 100): void => {
        state.label = name;
        state.from = from;
        state.to = to;
        state.phaseStart = this.clock.now();
        state.value = null;
        state.detail = '';
        state.cancel = null;
        if (this.visible === state) this.refresh();
      },
      report,
      onCancel: (action: (() => void) | null): void => {
        state.cancel = action;
        if (this.visible === state) this.refresh();
      },
      paintIfDue: (): Promise<void> | undefined => {
        if (
          !this.tasks.has(state) ||
          state.pausedAt !== undefined ||
          this.clock.now() - state.start < DELAY_MS ||
          this.timers.document?.visibilityState !== 'visible'
        ) {
          return undefined;
        }
        this.refresh();
        return new Promise<void>((resolve) => {
          const win = this.timers;
          let first = 0;
          let second = 0;
          const done = (): void => {
            win.clearTimeout(fallback);
            win.cancelAnimationFrame(first);
            win.cancelAnimationFrame(second);
            resolve();
          };
          const fallback = win.setTimeout(done, 100);
          first = win.requestAnimationFrame(() => {
            second = win.requestAnimationFrame(done);
          });
        });
      },
      job: (result: AsyncJobProgress): void => {
        if (result.totalBytes !== undefined && result.totalBytes > 0) {
          report(result.doneBytes, result.totalBytes, result.stage);
        } else if (result.totalRows !== undefined && result.totalRows > 0) {
          report(result.doneRows, result.totalRows, result.stage);
        } else {
          report(result.scanned ?? result.done ?? result.progress, result.total, result.stage);
        }
      },
      end,
    };
    return task;
  }

  refresh(): void {
    this.timers.clearTimeout(this.timer);
    this.timer = undefined;
    const now = this.clock.now();
    let chosen: TaskState | null = null;
    let preparing: TaskState | null = null;
    let next = Infinity;
    for (const task of this.tasks) {
      if (task.pausedAt !== undefined) continue;
      const remaining = task.start + DELAY_MS - now;
      if (remaining > 0) {
        next = Math.min(next, remaining);
        if (task.priority >= 2) preparing = task;
      } else if (!chosen || task.priority >= chosen.priority) {
        chosen = task;
      }
    }
    chosen ??= preparing;
    if (chosen) {
      const remaining = Math.max(0, chosen.start + DELAY_MS - now);
      const estimated = chosen.value === null;
      const elapsed = now + remaining - chosen.phaseStart;
      const fraction = estimated
        ? 0.94 * (1 - Math.exp(-elapsed / 6500))
        : (chosen.value ?? 0) / 100;
      const percent = Math.floor(chosen.from + (chosen.to - chosen.from) * fraction);
      const approximate = estimated || chosen.from !== 0 || chosen.to !== 100;
      const value = chosen.detail || `${approximate ? `${localize('ui.activity.zero')} ` : ''}${percent}%`;
      this.paint({
        owner: chosen,
        remaining,
        estimated,
        label: chosen.label,
        value,
        percent,
        cancel: chosen.cancel,
      });
      if (estimated && !remaining) next = Math.min(next, 250);
    } else if (this.visible) {
      this.paint(null);
    }
    this.visible = chosen;
    if (Number.isFinite(next)) {
      this.timer = this.timers.setTimeout(() => this.refresh(), next);
    }
  }
}

const QUIET_ROUTES = new Set([
  'status',
  'recent',
  'tasks',
  'recovery',
  'bookmarks',
  'images',
  'version',
  'formats',
  'search/history',
  'heartbeat',
  'analysis/workspace',
  'archive/job',
  'preview',
  'styles',
  'view',
]);

const GROUP_LABELS: Readonly<Record<string, string>> = {
  edits: localize('ui.activity.processingEditingCopy'),
  analysis: localize('ui.activity.readingAnalysisData'),
  export: localize('ui.common.generatingExportFileProgress'),
  archive: localize('ui.activity.compressedFilesBeingRead'),
  sheet: localize('ui.activity.cellsBeingRead'),
  cell: localize('ui.activity.cellsBeingRead'),
  range: localize('ui.activity.readingSelectionArea'),
  filter: localize('ui.common.filteringDataProgress'),
  search: localize('ui.common.searching'),
  profile: localize('ui.activity.collectingStatistics'),
  inspect: localize('ui.activity.checkingData'),
  calculator: localize('ui.activity.calculating'),
  clipboard: localize('ui.activity.contentPreparationProgress'),
  print: localize('ui.activity.printingProgress'),
  printing: localize('ui.activity.printingProgress'),
  layout: localize('ui.activity.readingWorksheetLayoutProgress'),
  navigate: localize('ui.activity.locatingCells'),
  catalogue: localize('ui.activity.readingWorksheetDirectory'),
  reconcile: localize('ui.activity.verifyingData'),
  autofit: localize('ui.activity.measuringColumnWidth'),
  'data-rules': localize('ui.activity.readingDataRules'),
  'row-view': localize('ui.activity.visibleRowsBeingUpdated'),
  recipes: localize('ui.activity.processingAnalysisPlan'),
};

const EDIT_LABELS: Readonly<Record<string, string>> = {
  start: localize('ui.activity.copyPreparationProgress'),
  save: localize('ui.activity.generatingCopy'),
  commit: localize('ui.activity.applyingModifications'),
  open: localize('ui.activity.openingCopy'),
  download: localize('ui.activity.readingCopy'),
};

export function requestLabel(url: string, method = 'GET'): string {
  const basePath = (url.split('?')[0] ?? '').replace(/^\/api\//, '');
  if (
    basePath.endsWith('/status') ||
    basePath.endsWith('/cancel') ||
    basePath.endsWith('/clear') ||
    (basePath.endsWith('/start') && basePath !== 'edits/start') ||
    basePath === 'archive/job' ||
    (method === 'GET' && QUIET_ROUTES.has(basePath))
  ) {
    return '';
  }
  const [group = '', action = ''] = basePath.split('/');
  return (group === 'edits' ? EDIT_LABELS[action] : undefined) || GROUP_LABELS[group] || localize('ui.activity.processingRequest');
}

export function createActivityService(
  clock: ActivityClock = performance,
  timers: ActivityTimers = window,
): ActivityService {
  const host = document.getElementById('tabActivity');
  if (!host) {
    const fallbackQueue = new ActivityQueue(() => {
      // Headless fallback when no #tabActivity node exists.
    }, clock, timers);
    return {
      begin: fallbackQueue.begin.bind(fallbackQueue),
      request(url: string, options: ApiRequestOptions = {}): ActivityHandle | null {
        if (options.activity === false) return null;
        const name = requestLabel(url, options.method);
        return name ? fallbackQueue.begin(name, { priority: 0, signal: options.signal ?? undefined }) : null;
      },
    };
  }

  if (typeof host.animate !== 'function') {
    host.classList.add('activity-static');
  }
  const label = host.querySelector('.activity-label');
  const value = host.querySelector('.activity-value');
  const cancel = host.querySelector('button');
  let cancelCurrent: (() => void) | null = null;
  let owner: TaskState | null = null;
  let reveal: Animation | null = null;
  const dialogs: HTMLDialogElement[] = [];

  function mount(): void {
    if (!host) return;
    const modal =
      dialogs.filter((node) => node.matches(':modal')).at(-1) ??
      document.querySelector('dialog:modal');
    const home = !modal && !document.fullscreenElement && document.body.classList.contains('home-view');
    const slot = owner?.slot ? document.getElementById(owner.slot) : null;
    const local =
      slot &&
      !slot.closest('[hidden],dialog:not([open])') &&
      (!modal || modal.contains(slot));
    const anchor = local
      ? slot
      : home
        ? (document.querySelector('.recent-working .recent-meta') ??
          document.getElementById('homeActivitySlot'))
        : null;
    if (anchor) {
      if (typeof host.hidePopover === 'function' && host.matches(':popover-open')) {
        host.hidePopover();
      }
      host.removeAttribute('popover');
      if (host.parentElement !== anchor) anchor.append(host);
      return;
    }
    const parent = modal ?? document.fullscreenElement ?? document.body;
    host.setAttribute('popover', 'manual');
    if (host.parentElement !== parent) parent.append(host);
    if (typeof host.showPopover === 'function' && !host.matches(':popover-open')) {
      host.showPopover();
    }
  }

  const queue = new ActivityQueue(
    (status) => {
      if (!status) {
        if (typeof host.hidePopover === 'function' && host.matches(':popover-open')) {
          host.hidePopover();
        }
        host.hidden = true;
        if (host.parentElement !== document.body) document.body.append(host);
        cancelCurrent = null;
        owner = null;
        reveal?.cancel();
        reveal = null;
        return;
      }
      const changed = owner !== status.owner;
      const entering = changed && (!owner || status.remaining > 0);
      owner = status.owner;
      if (status.estimated) host.setAttribute('data-estimate', '');
      else host.removeAttribute('data-estimate');
      if (status.remaining) host.setAttribute('aria-hidden', 'true');
      else host.removeAttribute('aria-hidden');
      if (label && label.textContent !== status.label) label.textContent = status.label;
      if (value) {
        if (value.textContent !== status.value) {
          value.textContent = status.value;
          value.setAttribute('aria-valuetext', status.value);
        }
        value.setAttribute('aria-valuenow', String(status.percent));
      }
      cancelCurrent = status.cancel;
      if (cancel instanceof HTMLElement) cancel.hidden = !cancelCurrent;
      if (host.hidden || changed || owner.slot) {
        host.hidden = false;
        mount();
      }
      if (!status.remaining && reveal?.effect) {
        const timingObj = reveal.effect.getTiming();
        const threshold = typeof timingObj.delay === 'number' ? timingObj.delay : 0;
        const currentTime = typeof reveal.currentTime === 'number' ? reveal.currentTime : 0;
        if (currentTime < threshold) reveal.currentTime = threshold;
      }
      if (changed) {
        reveal?.cancel();
        reveal = null;
        const prefersReduced =
          typeof window.matchMedia === 'function' &&
          window.matchMedia('(prefers-reduced-motion: reduce)').matches;
        if (entering && typeof host.animate === 'function' && !prefersReduced) {
          reveal = host.animate([{ opacity: 0 }, { opacity: 1 }], {
            delay: status.remaining,
            duration: 120,
            easing: 'ease-out',
            fill: 'backwards',
          });
        }
      }
    },
    clock,
    timers,
  );

  cancel?.addEventListener('click', () => cancelCurrent?.());
  document.addEventListener(
    'toggle',
    (event: Event) => {
      const node = event.target;
      if (!(node instanceof HTMLDialogElement)) return;
      const old = dialogs.indexOf(node);
      if (old >= 0) dialogs.splice(old, 1);
      if (node.open) dialogs.push(node);
      if (!host.hidden) mount();
    },
    true,
  );
  document.addEventListener('fullscreenchange', () => {
    if (!host.hidden) mount();
  });
  if (typeof MutationObserver !== 'undefined') {
    const placement = new MutationObserver(() => {
      if (!host.hidden) mount();
    });
    placement.observe(document.body, { attributes: true, attributeFilter: ['class'] });
    const recentList = document.getElementById('recentList');
    if (recentList) placement.observe(recentList, { childList: true });
  }
  document.addEventListener('visibilitychange', () => {
    if (!document.hidden) queue.refresh();
  });

  return {
    begin: queue.begin.bind(queue),
    request(url: string, options: ApiRequestOptions = {}): ActivityHandle | null {
      if (options.activity === false) return null;
      const name = requestLabel(url, options.method);
      return name ? queue.begin(name, { priority: 0, signal: options.signal ?? undefined }) : null;
    },
  };
}

let defaultActivityService: ActivityService | null = null;

export function setActivityService(service: ActivityService | null): void {
  defaultActivityService = service;
}

export function getActivityService(): ActivityService | null {
  return defaultActivityService;
}

export function beginActivity(
  label: string,
  options?: ActivityBeginOptions,
): ActivityHandle | null {
  return defaultActivityService ? defaultActivityService.begin(label, options) : null;
}
