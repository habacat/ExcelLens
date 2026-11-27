/**
 * Background task and detached-session monitor embedded inside the local storage dialog.
 */
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
import type {
  BackgroundItem,
  BackgroundStatusResponse,
  TaskCheckpointInfo,
} from '../contracts/features.ts';
import { errorMessage } from '../core/display-format.ts';
import { requireDialog, requireEl } from '../core/dom.ts';
import { confirmOverlay } from '../core/overlays.ts';
import type { ReconcileJobStatus } from './reconcile.ts';
import type { RecoveredTaskStart } from '../grid/sheet-controller.ts';

const KIND_NAMES: Readonly<Record<string, string>> = {
  analysis: '分析',
  reconcile: '对账',
  export: '导出',
  search: '搜索',
  filter: '筛选',
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

interface BackgroundJobStatusPayload {
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
    if (!item.canAttach) throw new Error('任务仍在原标签页中');
    if (item.session !== session.id) {
      const url = new URL(window.location.pathname, window.location.href);
      url.search = new URLSearchParams({ session: item.session, job: item.id }).toString();
      window.open(url.href, '_blank', 'noopener');
      return;
    }
    if (item.state !== 'done') throw new Error(item.error || item.stage || '任务仍在运行');
    const statusUrl = `${endpoint(item.kind)}/status?id=${encodeURIComponent(item.id)}`;
    if (item.kind === 'analysis') {
      const status = await api<AnalysisQueryStatusResponse>(statusUrl);
      if (status.status !== 'done') throw new Error(status.error || '任务尚未完成');
      requireDialog('storageDialog').close();
      await analysis(item.id, status);
      return;
    }
    if (item.kind === 'reconcile') {
      const status = await api<ReconcileJobStatus>(statusUrl);
      if (status.status !== 'done') throw new Error(status.error || '任务尚未完成');
      requireDialog('storageDialog').close();
      await reconcile(item.id, status);
      return;
    }
    const status = await api<BackgroundJobStatusPayload>(statusUrl);
    if (status.status !== 'done') throw new Error(status.error || '任务尚未完成');
    requireDialog('storageDialog').close();
    if (item.kind !== 'export' && item.kind !== 'search' && item.kind !== 'filter') {
      throw new Error('不支持的后台任务类型');
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
    if (!ok) throw new Error('当前视图正在使用，请结束当前操作后再打开结果');
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
          title: '释放会话',
          message: '释放此工作簿的后台结果？已保存的文件仍可使用。',
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
      const { items, sessions } = await api<BackgroundStatusResponse>('/api/background');
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
          button.textContent = '查看结果';
          button.onclick = (): void => {
            openResult(item).catch((error: unknown) => toast(errorMessage(error), 'error'));
          };
          row.append(button);
        }
        const live = item.state === 'running' || item.state === 'queued';
        if (item.protected && !live) {
          const note = document.createElement('span');
          note.textContent = '活动页面使用中';
          row.append(note);
        } else {
          addAction(row, live ? '取消任务' : '丢弃结果', {
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
        addAction(row, '释放会话', { action: 'release', session: entry.session });
        holder.append(row);
      }
      if (!itemList.length && !sessionList.length) holder.textContent = '没有后台任务';
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
            const { items } = await api<BackgroundStatusResponse>('/api/background');
            const item = items?.find((x) => x.id === job && x.session === session.id);
            if (!item) throw new Error('后台结果已到期，请查看已保存的可恢复任务');
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
