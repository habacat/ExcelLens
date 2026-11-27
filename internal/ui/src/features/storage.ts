/**
 * Local storage usage, backup/restore/migration jobs, conditional-stats cache budget,
 * and source-file relocation dialog controller.
 */
import type { ApiClient } from '../contracts/common.ts';
import type { StorageUsageResponse } from '../contracts/features.ts';
import { errorMessage, formatBytes } from '../core/display-format.ts';
import { byId, requireButton, requireDialog, requireEl, requireInput, requireSelect } from '../core/dom.ts';
import { confirmOverlay, type ConfirmOverlayOptions } from '../core/overlays.ts';

interface StorageJobProgress {
  readonly phase?: string;
  readonly files?: number;
  readonly items?: number;
  readonly bytes?: number;
  readonly total?: number;
}

interface StorageJobItem {
  readonly id: string;
  readonly action: string;
  readonly state: string;
  readonly transaction?: string;
  readonly started: string;
  readonly message?: string;
  readonly error?: string;
  readonly candidate?: string;
  readonly progress?: StorageJobProgress;
}

interface StorageHistoryIssue {
  readonly reason: string;
  readonly revision?: string;
}

interface StorageJobsResponse {
  readonly jobs: readonly StorageJobItem[];
  readonly historyIssue: StorageHistoryIssue | null;
}

interface StorageStageItem {
  readonly name: string;
  readonly state: 'active' | 'orphan' | 'unknown';
}

interface StorageStagesResponse {
  readonly stages: readonly StorageStageItem[];
}

interface StorageActionResponse {
  readonly id?: string;
  readonly message?: string;
}

interface StorageSourceMove {
  readonly at: string;
  readonly to: string;
}

interface StorageSourceItem {
  readonly book: string;
  readonly path: string;
  readonly original: string;
  readonly candidate?: string;
  readonly moves?: readonly StorageSourceMove[];
}

interface StorageSourcesResponse {
  readonly items?: readonly StorageSourceItem[];
}

export interface StorageDialogOptions {
  readonly api: ApiClient;
  readonly openDialog: (id: string) => void;
  readonly background: () => Promise<void> | void;
}

const CONFIRMATIONS: Readonly<Record<string, ConfirmOverlayOptions>> = {
  'clear-cache': {
    title: '清理历史索引',
    message: '清理未使用的工作簿索引？',
    confirmLabel: '清理',
    danger: true,
  },
  'clear-conditional-stats': {
    title: '清理条件统计缓存',
    message: '清理未使用的条件统计索引？正在使用的索引会保留。',
    confirmLabel: '清理',
    danger: true,
  },
  'clear-uploads': {
    title: '清理闲置上传副本',
    message: '删除未使用的上传副本？仅存于此处的文件将无法从这里找回。',
    confirmLabel: '删除',
    danger: true,
  },
};

const COMMAND_LABELS: Readonly<Record<string, string>> = {
  backup: '备份',
  restore: '迁入备份',
  migrate: '迁入旧设置',
  'verify-source': '核验来源',
  'relocate-source': '更新来源',
};

const STAGE_LABELS: Readonly<Record<StorageStageItem['state'], string>> = {
  active: '正在使用',
  orphan: '未完成',
  unknown: '缺少标记，请在目录中核对',
};

function requireDetails(id: string): HTMLDetailsElement {
  const node = requireEl(id);
  if (!(node instanceof HTMLDetailsElement)) {
    throw new Error(`Expected HTMLDetailsElement for #${id}`);
  }
  return node;
}

export function initStorageDialog(options: StorageDialogOptions): void {
  const { api, openDialog, background } = options;
  const dialog = requireDialog('storageDialog');
  const status = requireEl('storageStatus');
  const body = requireEl('storageRows');
  const buttons = Array.from(dialog.querySelectorAll<HTMLButtonElement>('[data-storage]'));

  let historyIssue: StorageHistoryIssue | null = null;
  let busy = false;
  let job: string | null = null;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let serial = 0;
  let sources: readonly StorageSourceItem[] = [];

  const isBusy = (): boolean => busy;
  const isDialogOpen = (): boolean => dialog.open;

  function working(value: boolean): void {
    busy = value;
    for (const button of buttons) button.disabled = value;
    requireInput('storageDirectory').disabled = value;
    requireInput('storagePassword').disabled = value;
    const budgetEl = byId('storageConditionalBudget');
    if (budgetEl instanceof HTMLInputElement) budgetEl.disabled = value;
    dialog.setAttribute('aria-busy', String(value));
  }

  function show(message: string, failed = false): void {
    status.textContent = message;
    status.classList.toggle('inline-error', failed);
  }

  async function refresh(): Promise<void> {
    const { roots, groups, conditionalStats } = await api<StorageUsageResponse>('/api/storage');
    if (roots) {
      requireEl('storageRoots').textContent = `用户配置　${roots.config}\n本机数据　${roots.local}`;
    }
    const budgetInput = byId('storageConditionalBudget');
    if (conditionalStats && budgetInput instanceof HTMLInputElement) {
      const budgetMB = Math.round((conditionalStats.budgetBytes || 0) / (1024 * 1024));
      if (document.activeElement !== budgetInput) {
        budgetInput.value = String(budgetMB);
      }
      const summary = byId('storageConditionalSummary');
      if (summary) {
        const used = formatBytes(conditionalStats.usedBytes ?? conditionalStats.bytes ?? 0);
        const limit = formatBytes(conditionalStats.budgetBytes || 0);
        const active = conditionalStats.protectedFiles
          ? `（在用 ${conditionalStats.protectedFiles} 个）`
          : '';
        summary.textContent = `当前 ${used} / 上限 ${limit}${active}`;
      }
    }
    const fragment = document.createDocumentFragment();
    for (const group of groups ?? []) {
      const row = document.createElement('tr');
      const name = document.createElement('td');
      const details = document.createElement('details');
      const summary = document.createElement('summary');
      const paths = document.createElement('div');
      summary.textContent = group.name;
      paths.className = 'storage-paths';
      paths.textContent = (group.paths ?? []).join('\n');
      details.append(summary, paths);
      name.append(details);
      row.append(name);
      const values = [group.kind ?? '', group.error ? '未知' : formatBytes(group.allocated)];
      for (const value of values) {
        const cell = document.createElement('td');
        cell.textContent = value;
        row.append(cell);
      }
      fragment.append(row);
      if (group.error) {
        const errorRow = document.createElement('tr');
        const cell = document.createElement('td');
        cell.colSpan = 3;
        cell.className = 'inline-error';
        cell.textContent = group.error;
        errorRow.append(cell);
        fragment.append(errorRow);
      }
    }
    body.replaceChildren(fragment);
  }

  const post = <T>(url: string, payload: unknown): Promise<T> =>
    api<T>(url, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    });

  function describe(item: StorageJobItem): string {
    const p = item.progress ?? {};
    return (
      [item.message, item.error].filter(Boolean).join('。') ||
      [
        p.phase,
        p.files ? `${p.files} / ${p.items || '…'} 项` : '',
        p.bytes ? `${formatBytes(p.bytes)}${p.total ? ` / ${formatBytes(p.total)}` : ''}` : '',
      ]
        .filter(Boolean)
        .join(' · ')
    );
  }

  function renderHistory(jobs: readonly StorageJobItem[]): void {
    const holder = requireEl('storageJobs');
    holder.replaceChildren();
    for (const item of jobs) {
      const row = document.createElement('div');
      row.textContent = `${new Date(item.started).toLocaleString()}　${COMMAND_LABELS[item.action] ?? item.action}　${describe(item)}`;
      holder.append(row);
    }
    if (!jobs.length) holder.textContent = '没有传输记录';
  }

  function schedule(ticket: number): void {
    clearTimeout(timer);
    if (dialog.open) {
      timer = setTimeout(() => {
        void observe(ticket);
      }, 500);
    }
  }

  function selectSource(): void {
    const selectedBook = requireSelect('storageSource').value;
    const item = sources.find((x) => x.book === selectedBook);
    requireInput('storageCandidate').value = item?.candidate ?? '';
    requireEl('storageMoves').textContent = item
      ? [
          item.original,
          ...(item.moves ?? []).map((x) => `${new Date(x.at).toLocaleString()}　${x.to}`),
        ].join('\n')
      : '没有可重定位的来源';
    requireButton('storageRelocate').disabled = !item;
  }

  async function loadSources(): Promise<void> {
    try {
      const { items } = await api<StorageSourcesResponse>('/api/storage/sources');
      sources = items ?? [];
      const select = requireSelect('storageSource');
      const previous = select.value;
      select.replaceChildren();
      for (const x of sources) {
        const option = document.createElement('option');
        option.value = x.book;
        option.textContent = x.path;
        select.append(option);
      }
      if (sources.some((x) => x.book === previous)) select.value = previous;
      selectSource();
    } catch (error: unknown) {
      show(errorMessage(error), true);
    }
  }

  async function observe(ticket: number): Promise<void> {
    try {
      const data = await api<StorageJobsResponse>('/api/storage/jobs');
      if (ticket !== serial) return;
      const jobs = data.jobs;
      historyIssue = data.historyIssue;
      const issueEl = requireEl('storageHistoryIssue');
      issueEl.hidden = !historyIssue;
      if (historyIssue) {
        requireEl('storageHistoryReason').textContent =
          `${historyIssue.reason}。本次传输记录暂存于内存。`;
        const repairBtn = issueEl.querySelector('button');
        if (repairBtn) repairBtn.hidden = !historyIssue.revision;
      }
      renderHistory(jobs);
      const item = jobs.find((x) => x.id === job) ?? jobs[0];
      if (item?.state === 'running') {
        job = item.id;
        working(true);
        show(describe(item));
        requireButton('storageCancel').hidden =
          item.transaction === 'committing' || item.transaction === 'cancelling';
        schedule(ticket);
        return;
      }
      job = null;
      requireButton('storageCancel').hidden = true;
      if (dialog.open) await refresh();
      if (ticket !== serial) return;
      show(item ? describe(item) : '', Boolean(item?.error));
      if (requireDetails('storageSources').open) await loadSources();
      if (ticket === serial) working(false);
    } catch (error: unknown) {
      if (ticket === serial) {
        show(errorMessage(error), true);
        if (job) schedule(ticket);
        else working(false);
      }
    }
  }

  async function run(action: string | undefined): Promise<void> {
    if (!action || isBusy()) return;
    if (action === 'repair-history') {
      const revision = historyIssue?.revision;
      if (
        !revision ||
        !(await confirmOverlay({
          title: '重建传输历史',
          message: '保留原文件作为诊断副本，并使用本次运行的记录重建传输历史？',
          confirmLabel: '重建',
        }))
      ) {
        return;
      }
      if (isBusy() || !isDialogOpen() || revision !== historyIssue?.revision) return;
      const ticket = ++serial;
      working(true);
      try {
        const result = await post<StorageActionResponse>('/api/storage', {
          action,
          revision,
          confirm: true,
        });
        await observe(ticket);
        if (ticket === serial) show(result.message ?? '');
      } catch (error: unknown) {
        if (ticket === serial) {
          await observe(ticket);
          show(errorMessage(error), true);
        }
      }
      return;
    }
    const confirmation = CONFIRMATIONS[action];
    if (confirmation && !(await confirmOverlay(confirmation))) return;
    if (isBusy() || !isDialogOpen()) return;
    const directoryInput = requireInput('storageDirectory');
    const directory = directoryInput.value.trim();
    if ((action === 'backup' || action === 'restore') && !directory) {
      show('请输入目录的完整路径', true);
      directoryInput.focus();
      return;
    }
    const passwordInput = requireInput('storagePassword');
    const password = passwordInput.value;
    if (
      action === 'backup' &&
      password &&
      !(await confirmOverlay({
        title: '加密备份',
        message: '使用此口令加密备份？恢复时需要同一口令。',
        confirmLabel: '开始备份',
      }))
    ) {
      return;
    }
    if (isBusy() || !isDialogOpen()) return;
    const ticket = ++serial;
    working(true);
    show('正在处理…');
    try {
      if (action === 'refresh') {
        await observe(ticket);
        return;
      }
      if (action === 'list-stages') {
        const { stages } = await post<StorageStagesResponse>('/api/storage', { action, directory });
        const holder = requireEl('storageStageList');
        holder.replaceChildren();
        for (const stage of stages) {
          const row = document.createElement('div');
          row.textContent = `${stage.name}　${STAGE_LABELS[stage.state]}`;
          if (stage.state === 'orphan') {
            const remove = document.createElement('button');
            remove.type = 'button';
            remove.className = 'mini-button';
            remove.textContent = '清理';
            remove.onclick = async (): Promise<void> => {
              if (
                !(await confirmOverlay({
                  title: '清理未完成备份',
                  message: stage.name,
                  confirmLabel: '清理',
                  danger: true,
                }))
              ) {
                return;
              }
              try {
                await post('/api/storage', { action: 'clean-stage', directory, stage: stage.name });
                row.remove();
                show('已清理未完成备份');
              } catch (error: unknown) {
                show(errorMessage(error), true);
              }
            };
            row.append(remove);
          }
          holder.append(row);
        }
        if (!stages.length) holder.textContent = '没有未完成的备份';
        working(false);
        show('');
        return;
      }
      if (action === 'set-conditional-stats-budget') {
        const budgetEl = byId('storageConditionalBudget');
        const mb = budgetEl instanceof HTMLInputElement ? Number(budgetEl.value) : Number.NaN;
        if (!Number.isFinite(mb) || mb < 64 || mb > 65536) {
          show('请输入 64 到 65536 MB 之间的上限', true);
          if (budgetEl instanceof HTMLInputElement) budgetEl.focus();
          working(false);
          return;
        }
        const item = await post<StorageActionResponse>('/api/storage', {
          action,
          budgetBytes: Math.round(mb) * 1024 * 1024,
        });
        await refresh();
        show(item.message ?? '');
        working(false);
        return;
      }
      passwordInput.value = '';
      const item = await post<StorageActionResponse>('/api/storage', {
        action,
        directory,
        password: action === 'backup' || action === 'restore' ? password : '',
      });
      if (Object.hasOwn(COMMAND_LABELS, action) && item.id) {
        job = item.id;
        await observe(ticket);
        return;
      }
      await refresh();
      show(item.message ?? '');
      working(false);
    } catch (error: unknown) {
      show(errorMessage(error), true);
      working(false);
    }
  }

  requireButton('storageCancel').addEventListener('click', () => {
    const activeJob = job;
    if (!activeJob) return;
    void (async () => {
      try {
        await post(`/api/storage/jobs?id=${encodeURIComponent(activeJob)}`, {});
        show('正在取消…');
      } catch (error: unknown) {
        show(errorMessage(error), true);
      }
    })();
  });

  const sourcesDetails = requireDetails('storageSources');
  sourcesDetails.addEventListener('toggle', () => {
    if (sourcesDetails.open && !isBusy()) void loadSources();
  });
  requireSelect('storageSource').addEventListener('change', selectSource);

  async function locate(
    request: Readonly<Record<string, unknown>>,
    ticket: number,
  ): Promise<StorageJobItem | null> {
    const started = await post<StorageActionResponse>('/api/storage/sources', request);
    if (!started.id) throw new Error('来源核验任务启动失败');
    job = started.id;
    while (ticket === serial && isDialogOpen()) {
      const item = await api<StorageJobItem>(
        `/api/storage/jobs?id=${encodeURIComponent(started.id)}`,
      );
      if (ticket !== serial || !isDialogOpen()) return null;
      show(describe(item), Boolean(item.error));
      requireButton('storageCancel').hidden =
        item.state !== 'running' ||
        item.transaction === 'committing' ||
        item.transaction === 'cancelling';
      if (item.state === 'done') {
        job = null;
        return item;
      }
      if (item.state !== 'running') {
        job = null;
        throw new Error(item.error || '来源核验失败');
      }
      await new Promise((resolve) => setTimeout(resolve, 350));
    }
    return null;
  }

  requireButton('storageRelocate').addEventListener('click', () => {
    const selectedBook = requireSelect('storageSource').value;
    const item = sources.find((x) => x.book === selectedBook);
    if (!item || isBusy()) return;
    const request = {
      book: item.book,
      previous: item.path,
      path: requireInput('storageCandidate').value.trim(),
    };
    const ticket = ++serial;
    working(true);
    void (async () => {
      try {
        const checked = await locate(request, ticket);
        if (!checked || ticket !== serial || !isDialogOpen()) return;
        const accepted = await confirmOverlay({
          title: '确认更新来源位置',
          message: `内容指纹相符。确认更新来源位置？\n${item.path}\n→ ${checked.candidate ?? ''}`,
          confirmLabel: '确认更新',
        });
        if (!accepted || ticket !== serial || !isDialogOpen()) return;
        if (await locate({ ...request, path: checked.candidate, confirm: true }, ticket)) {
          await loadSources();
        }
      } catch (error: unknown) {
        if (ticket === serial) show(errorMessage(error), true);
      } finally {
        if (ticket === serial) {
          requireButton('storageCancel').hidden = true;
          working(false);
        }
      }
    })();
  });

  for (const button of buttons) {
    button.addEventListener('click', () => {
      void run(button.dataset.storage);
    });
  }
  dialog.addEventListener('close', () => {
    ++serial;
    clearTimeout(timer);
    requireInput('storagePassword').value = '';
  });
  for (const id of ['storageHome', 'storageOpenBtn']) {
    requireEl(id).addEventListener('click', () => {
      working(true);
      openDialog('storageDialog');
      void observe(++serial);
      if (requireDetails('storageBackground').open) void background();
    });
  }
}
