/**
 * Background task and detached-session monitor embedded inside the local storage dialog.
 */
import { t as localize } from '../core/i18n.ts';
import type {
  AnalysisQueryStatusResponse,
  SourceFilterSpec,
} from '../contracts/analysis.ts';
import type {
  ApiClient,
  AsyncJobStatusKind,
  SessionService,
  ToastFn,
} from '../contracts/common.ts';
import type { BackgroundItem, TaskCheckpointInfo } from '../contracts/features.ts';
import { errorMessage } from '../core/display-format.ts';
import { requireDialog, requireEl } from '../core/dom.ts';
import { confirmOverlay } from '../core/overlays.ts';
import type { ReconcileJobStatus } from './reconcile.ts';
import type { RecoveredTaskStart } from '../grid/sheet-controller.ts';
import {
  decodeAnalysisQueryStatusResponse,
  decodeBackgroundJobStatusPayload,
  decodeBackgroundStatusResponse,
  decodeReconcileJobStatus,
} from '../core/http-decoders.generated.ts';

const KIND_NAMES: Readonly<Record<string, string>> = {
  analysis: localize('ui.common.analysis'),
  reconcile: localize('ui.backgroundTasks.reconcilingAccounts'),
  export: localize('ui.backgroundTasks.export'),
  search: localize('ui.common.search'),
  filter: localize('ui.backgroundTasks.filter'),
};

function endpoint(kind: string): string {
  return `/api/${kind === 'analysis' ? 'analysis/query' : kind}`;
}

function requireDetails(id: string): HTMLDetailsElement {
  const node = requireEl(id);
  if (!(node instanceof HTMLDetailsElement)) {
    throw new Error(`Expected HTMLDetailsElement for #${id}`);
  }
  return node;
}

/** @httpResponse BackgroundJobStatusPayload */
export interface BackgroundJobStatusPayload {
  readonly status: AsyncJobStatusKind;
  readonly error?: string;
  readonly filter?: SourceFilterSpec;
  readonly mode?: string;
  readonly format?: string;
  readonly sheet?: string;
  readonly query?: string;
  readonly scope?: string;
  readonly full?: boolean;
  readonly [key: string]: unknown;
}

interface BackgroundActionPayload {
  readonly action: 'cancel' | 'discard' | 'release';
  readonly session: string;
  readonly kind?: string;
  readonly id?: string;
}

export interface BackgroundTasksOptions {
  readonly api: ApiClient;
  readonly session: SessionService;
  readonly state: { readonly book: string; readonly workbookKey: string };
  readonly toast: ToastFn;
  readonly adoptTask: (
    start: RecoveredTaskStart,
    item: TaskCheckpointInfo,
  ) => Promise<boolean>;
  readonly analysis: (jobId: string, status: AnalysisQueryStatusResponse) => unknown;
  readonly reconcile: (jobId: string, status: ReconcileJobStatus) => unknown;
  readonly workbookReady?: () => Promise<void>;
}

export function createBackgroundMonitor(options: BackgroundTasksOptions): () => Promise<void> {
  const { api, session, state, toast, adoptTask, analysis, reconcile, workbookReady } = options;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let serial = 0;
  const box = requireDetails('storageBackground');
  const holder = requireEl('storageWork');

  async function openResult(item: BackgroundItem): Promise<void> {
    if (!item.canAttach) throw new Error(localize('ui.backgroundTasks.taskStillOriginalTab'));
    if (item.session !== session.id) {
      const url = new URL(window.location.pathname, window.location.href);
      url.search = new URLSearchParams({ session: item.session, job: item.id }).toString();
      window.open(url.href, '_blank', 'noopener');
      return;
    }
    if (item.state !== 'done') throw new Error(item.error || item.stage || localize('ui.backgroundTasks.taskStillRunning'));
    const statusUrl = `${endpoint(item.kind)}/status?id=${encodeURIComponent(item.id)}`;
    if (item.kind === 'analysis') {
      const status = await api(statusUrl, undefined, decodeAnalysisQueryStatusResponse);
      if (status.status !== 'done') throw new Error(status.error || localize('ui.common.taskNotCompletedYet'));
      requireDialog('storageDialog').close();
      await analysis(item.id, status);
      return;
    }
    if (item.kind === 'reconcile') {
      const status = await api(statusUrl, undefined, decodeReconcileJobStatus);
      if (status.status !== 'done') throw new Error(status.error || localize('ui.common.taskNotCompletedYet'));
      requireDialog('storageDialog').close();
      await reconcile(item.id, status);
      return;
    }
    const status = await api(statusUrl, undefined, decodeBackgroundJobStatusPayload);
    if (status.status !== 'done') throw new Error(status.error || localize('ui.common.taskNotCompletedYet'));
    requireDialog('storageDialog').close();
    if (item.kind !== 'export' && item.kind !== 'search' && item.kind !== 'filter') {
      throw new Error(localize('ui.backgroundTasks.unsupportedBackgroundTaskTypes'));
    }
    const checkpoint: TaskCheckpointInfo = {
      id: item.id,
      revision: '1',
      kind: item.kind,
      driver: `source-${item.kind}`,
      title: item.file,
      file: item.file,
      path: '',
      book: state.book,
      workbookKey: state.workbookKey,
      sheet: status.sheet ?? '',
      query: status.query ?? '',
      mode: status.mode ?? status.format ?? '',
      scope: status.scope ?? '',
      full: status.full,
      updated: item.created,
      complete: true,
      busy: false,
      backup: false,
      error: '',
    };
    const ok = await adoptTask(
      { operation: item.kind, jobId: item.id, filter: status.filter },
      checkpoint,
    );
    if (!ok) throw new Error(localize('ui.backgroundTasks.currentViewUseEndCurrent'));
  }

  function addAction(row: HTMLElement, label: string, payload: BackgroundActionPayload): void {
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'mini-button';
    button.textContent = label;
    button.onclick = async (): Promise<void> => {
      if (
        payload.action === 'release' &&
        !(await confirmOverlay({
          title: localize('ui.backgroundTasks.releaseSession'),
          message: localize('ui.backgroundTasks.releaseBackgroundResultsThisWorkbook'),
          confirmLabel: label,
        }))
      ) {
        return;
      }
      button.disabled = true;
      try {
        await api('/api/background', { method: 'POST', body: JSON.stringify(payload) });
        await refresh();
      } catch (error: unknown) {
        button.disabled = false;
        toast(errorMessage(error), 'error');
      }
    };
    row.append(button);
  }

  async function refresh(): Promise<void> {
    clearTimeout(timer);
    const ticket = ++serial;
    try {
      const { items, sessions } = await api('/api/background', undefined, decodeBackgroundStatusResponse);
      if (ticket !== serial || !box.open || !requireDialog('storageDialog').open) return;
      holder.replaceChildren();
      const itemList = items ?? [];
      const sessionList = sessions ?? [];
      for (const item of itemList) {
        const row = document.createElement('div');
        const label = document.createElement('span');
        label.textContent = [
          KIND_NAMES[item.kind] ?? item.kind,
          item.file,
          item.error || item.stage || item.state,
        ].join(' · ');
        row.append(label);
        if (item.state === 'done' && item.canAttach) {
          const button = document.createElement('button');
          button.type = 'button';
          button.className = 'mini-button';
          button.textContent = localize('ui.backgroundTasks.viewResults');
          button.onclick = (): void => {
            openResult(item).catch((error: unknown) => toast(errorMessage(error), 'error'));
          };
          row.append(button);
        }
        const live = item.state === 'running' || item.state === 'queued';
        if (item.protected && !live) {
          const note = document.createElement('span');
          note.textContent = localize('ui.backgroundTasks.activePageUse');
          row.append(note);
        } else {
          addAction(row, live ? localize('ui.backgroundTasks.cancelTask') : localize('ui.backgroundTasks.discardResults'), {
            action: live ? 'cancel' : 'discard',
            session: item.session,
            kind: item.kind,
            id: item.id,
          });
        }
        holder.append(row);
      }
      for (const entry of sessionList) {
        const row = document.createElement('div');
        row.append(entry.file);
        addAction(row, localize('ui.backgroundTasks.releaseSession'), { action: 'release', session: entry.session });
        holder.append(row);
      }
      if (!itemList.length && !sessionList.length) holder.textContent = localize('ui.backgroundTasks.noBackgroundTasks');
      timer = setTimeout(() => {
        void refresh();
      }, 1500);
    } catch (error: unknown) {
      if (ticket === serial) holder.textContent = errorMessage(error);
    }
  }

  box.addEventListener('toggle', () => {
    if (box.open) {
      void refresh();
    } else {
      ++serial;
      clearTimeout(timer);
    }
  });

  const job = new URLSearchParams(window.location.search).get('job');
  if (job) {
    window.addEventListener(
      'load',
      () => {
        void (async () => {
          try {
            await workbookReady?.();
            const { items } = await api('/api/background', undefined, decodeBackgroundStatusResponse);
            const item = items?.find((x) => x.id === job && x.session === session.id);
            if (!item) throw new Error(localize('ui.backgroundTasks.backgroundResultsHaveExpiredView'));
            await openResult(item);
          } catch (error: unknown) {
            toast(errorMessage(error), 'error');
          } finally {
            const url = new URL(window.location.href);
            url.searchParams.delete('job');
            history.replaceState(history.state, '', url.pathname + url.search + url.hash);
          }
        })();
      },
      { once: true },
    );
  }

  return refresh;
}
