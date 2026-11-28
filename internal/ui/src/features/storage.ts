/**
 * Local storage usage, backup/restore/migration jobs, conditional-stats cache budget,
 * and source-file relocation dialog controller.
 */
import { interfaceLocale, t as localize } from '../core/i18n.ts';
import type { ApiClient } from '../contracts/common.ts';
import { errorMessage, formatBytes } from '../core/display-format.ts';
import { byId, requireButton, requireDialog, requireEl, requireInput, requireSelect } from '../core/dom.ts';
import { confirmOverlay, type ConfirmOverlayOptions } from '../core/overlays.ts';
import {
  decodeStorageActionResponse,
  decodeStorageJobItem,
  decodeStorageJobsResponse,
  decodeStorageSourcesResponse,
  decodeStorageStagesResponse,
  decodeStorageUsageResponse,
} from '../core/http-decoders.generated.ts';

interface StorageJobProgress {
  readonly phase?: string;
  readonly files?: number;
  readonly items?: number;
  readonly bytes?: number;
  readonly total?: number;
}

/** @httpResponse StorageJobItem */
export interface StorageJobItem {
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

/** @httpResponse StorageJobsResponse */
export interface StorageJobsResponse {
  readonly jobs: readonly StorageJobItem[];
  readonly historyIssue: StorageHistoryIssue | null;
}

interface StorageStageItem {
  readonly name: string;
  readonly state: 'active' | 'orphan' | 'unknown';
}

/** @httpResponse StorageStagesResponse */
export interface StorageStagesResponse {
  readonly stages: readonly StorageStageItem[];
}

/** @httpResponse StorageActionResponse */
export interface StorageActionResponse {
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

/** @httpResponse StorageSourcesResponse */
export interface StorageSourcesResponse {
  readonly items?: readonly StorageSourceItem[];
}

export interface StorageDialogOptions {
  readonly api: ApiClient;
  readonly openDialog: (id: string) => void;
  readonly background: () => Promise<void> | void;
}

const CONFIRMATIONS: Readonly<Record<string, ConfirmOverlayOptions>> = {
  'clear-cache': {
    title: localize('ui.storage.clearHistoricalIndex'),
    message: localize('ui.storage.wantClearUnusedWorkbookIndexes'),
    confirmLabel: localize('ui.common.cleanUp'),
    danger: true,
  },
  'clear-conditional-stats': {
    title: localize('ui.storage.clearingConditionalStatisticsCache'),
    message: localize('ui.storage.cleanUpUnusedConditionalStatistics'),
    confirmLabel: localize('ui.common.cleanUp'),
    danger: true,
  },
  'clear-uploads': {
    title: localize('ui.storage.cleaningUpIdleUploadCopies'),
    message: localize('ui.storage.deleteUnusedUploadedCopiesFiles'),
    confirmLabel: localize('ui.common.delete'),
    danger: true,
  },
};

const COMMAND_LABELS: Readonly<Record<string, string>> = {
  backup: localize('ui.storage.backUp'),
  restore: localize('ui.storage.migrateBackup'),
  migrate: localize('ui.storage.migratingOldSettings'),
  'verify-source': localize('ui.storage.verificationSource'),
  'relocate-source': localize('ui.storage.updateSource'),
};

const STAGE_LABELS: Readonly<Record<StorageStageItem['state'], string>> = {
  active: localize('ui.common.use'),
  orphan: localize('ui.storage.notCompleted'),
  unknown: localize('ui.storage.missingTagsCheckTableContents'),
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
    const { roots, groups, conditionalStats } = await api('/api/storage', undefined, decodeStorageUsageResponse);
    if (roots) {
      requireEl('storageRoots').textContent = localize('ui.storage.userConfigurationLocalData', { config: roots.config, local: roots.local });
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
          ? localize('ui.storage.using', { protectedFiles: conditionalStats.protectedFiles })
          : '';
        summary.textContent = localize('ui.storage.currentlyUpperLimit', { used, limit, active });
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
      const values = [group.kind ?? '', group.error ? localize('ui.storage.unknown') : formatBytes(group.allocated)];
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

  const post = (url: string, payload: unknown): Promise<unknown> =>
    api(url, {
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
        p.files ? localize('ui.storage.items', { files: p.files, items: p.items || '…' }) : '',
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
      row.textContent = `${new Date(item.started).toLocaleString(interfaceLocale())}　${COMMAND_LABELS[item.action] ?? item.action}　${describe(item)}`;
      holder.append(row);
    }
    if (!jobs.length) holder.textContent = localize('ui.storage.noTransferRecords');
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
          ...(item.moves ?? []).map((x) => `${new Date(x.at).toLocaleString(interfaceLocale())}　${x.to}`),
        ].join('\n')
      : localize('ui.storage.noRelocationSourcesAvailable');
    requireButton('storageRelocate').disabled = !item;
  }

  async function loadSources(): Promise<void> {
    try {
      const { items } = await api('/api/storage/sources', undefined, decodeStorageSourcesResponse);
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
      const data = await api('/api/storage/jobs', undefined, decodeStorageJobsResponse);
      if (ticket !== serial) return;
      const jobs = data.jobs;
      historyIssue = data.historyIssue;
      const issueEl = requireEl('storageHistoryIssue');
      issueEl.hidden = !historyIssue;
      if (historyIssue) {
        requireEl('storageHistoryReason').textContent =
          localize('ui.storage.thisTransferRecordTemporarilyStored', { reason: historyIssue.reason });
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
          title: localize('ui.storage.rebuildTransferHistory'),
          message: localize('ui.storage.keepOriginalFileAsDiagnostic'),
          confirmLabel: localize('ui.storage.reconstruction'),
        }))
      ) {
        return;
      }
      if (isBusy() || !isDialogOpen() || revision !== historyIssue?.revision) return;
      const ticket = ++serial;
      working(true);
      try {
        const result = await post('/api/storage', {
          action,
          revision,
          confirm: true,
        }).then(decodeStorageActionResponse);
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
      show(localize('ui.storage.enterFullPathDirectory'), true);
      directoryInput.focus();
      return;
    }
    const passwordInput = requireInput('storagePassword');
    const password = passwordInput.value;
    if (
      action === 'backup' &&
      password &&
      !(await confirmOverlay({
        title: localize('ui.storage.encryptionBackup'),
        message: localize('ui.storage.useThisPasswordEncryptionBackup'),
        confirmLabel: localize('ui.storage.startBackup'),
      }))
    ) {
      return;
    }
    if (isBusy() || !isDialogOpen()) return;
    const ticket = ++serial;
    working(true);
    show(localize('ui.storage.processingProgress'));
    try {
      if (action === 'refresh') {
        await observe(ticket);
        return;
      }
      if (action === 'list-stages') {
        const { stages } = await post('/api/storage', { action, directory }).then(decodeStorageStagesResponse);
        const holder = requireEl('storageStageList');
        holder.replaceChildren();
        for (const stage of stages) {
          const row = document.createElement('div');
          row.textContent = `${stage.name}　${STAGE_LABELS[stage.state]}`;
          if (stage.state === 'orphan') {
            const remove = document.createElement('button');
            remove.type = 'button';
            remove.className = 'mini-button';
            remove.textContent = localize('ui.common.cleanUp');
            remove.onclick = async (): Promise<void> => {
              if (
                !(await confirmOverlay({
                  title: localize('ui.storage.cleanUpIncompleteBackups'),
                  message: stage.name,
                  confirmLabel: localize('ui.common.cleanUp'),
                  danger: true,
                }))
              ) {
                return;
              }
              try {
                await post('/api/storage', { action: 'clean-stage', directory, stage: stage.name });
                row.remove();
                show(localize('ui.storage.completedBackupsCleared'));
              } catch (error: unknown) {
                show(errorMessage(error), true);
              }
            };
            row.append(remove);
          }
          holder.append(row);
        }
        if (!stages.length) holder.textContent = localize('ui.storage.noUnfinishedBackups');
        working(false);
        show('');
        return;
      }
      if (action === 'set-conditional-stats-budget') {
        const budgetEl = byId('storageConditionalBudget');
        const mb = budgetEl instanceof HTMLInputElement ? Number(budgetEl.value) : Number.NaN;
        if (!Number.isFinite(mb) || mb < 64 || mb > 65536) {
          show(localize('ui.storage.enterUpperLimitBetweenMb'), true);
          if (budgetEl instanceof HTMLInputElement) budgetEl.focus();
          working(false);
          return;
        }
        const item = await post('/api/storage', {
          action,
          budgetBytes: Math.round(mb) * 1024 * 1024,
        }).then(decodeStorageActionResponse);
        await refresh();
        show(item.message ?? '');
        working(false);
        return;
      }
      passwordInput.value = '';
      const item = await post('/api/storage', {
        action,
        directory,
        password: action === 'backup' || action === 'restore' ? password : '',
      }).then(decodeStorageActionResponse);
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
        show(localize('ui.storage.cancelingProgress'));
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
    const started = await post('/api/storage/sources', request).then(decodeStorageActionResponse);
    if (!started.id) throw new Error(localize('ui.storage.sourceVerificationTaskFailedStart'));
    job = started.id;
    while (ticket === serial && isDialogOpen()) {
      const item = await api(
        `/api/storage/jobs?id=${encodeURIComponent(started.id)}`,
        undefined,
        decodeStorageJobItem,
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
        throw new Error(item.error || localize('ui.storage.sourceVerificationFailed'));
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
          title: localize('ui.storage.confirmUpdateSourceLocation'),
          message: localize('ui.storage.contentFingerprintsMatchConfirmUpdated', { path: item.path, detail: checked.candidate ?? '' }),
          confirmLabel: localize('ui.storage.confirmUpdate'),
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
