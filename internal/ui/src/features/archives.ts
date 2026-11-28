/**
 * Archive member browser & streaming extraction dialog controller.
 */
import { t as localize } from '../core/i18n.ts';
import type { ActivityHandle, ApiClient, AsyncJobProgress } from '../contracts/common.ts';
import type { ArchiveListResponse, ArchiveMember } from '../contracts/features.ts';
import type { ArchiveSummary, WorkbookStatusResponse } from '../contracts/workbook.ts';
import { decodeOpenResult } from '../core/workbook-response.ts';
import { beginActivity } from '../core/activity.ts';
import { errorMessage, isComposing } from '../core/display-format.ts';
import { requireButton, requireDialog, requireEl, requireInput } from '../core/dom.ts';
import {
  decodeArchiveJobStatusResponse,
  decodeArchiveListApiResponse,
  decodeArchiveOpenStartResponse,
} from '../core/http-decoders.generated.ts';

export interface ArchiveDescriptor extends ArchiveSummary {
  readonly id?: string;
  readonly format?: string;
  readonly count?: number;
  readonly depth?: number;
}

/** @httpResponse ArchiveOpenStartResponse */
export interface ArchiveOpenStartResponse {
  readonly jobId: string;
}

/** @httpResponse ArchiveJobStatusResponse */
export interface ArchiveJobStatusResponse extends AsyncJobProgress {
  readonly status: 'queued' | 'running' | 'done' | 'error' | 'cancelled';
  readonly stage: string;
  readonly doneBytes: number;
  readonly totalBytes: number;
  readonly error?: string;
  readonly result: unknown;
}

/** @httpResponse ArchiveListApiResponse */
export interface ArchiveListApiResponse extends Omit<ArchiveListResponse, 'items' | 'archive'> {
  readonly archive?: ArchiveDescriptor;
  readonly items?: readonly ArchiveMember[] | null;
}

export interface ArchivesOptions {
  readonly api: ApiClient;
  readonly open: (result: WorkbookStatusResponse) => Promise<void> | void;
  readonly fmtBytes: (value: unknown) => string;
  readonly canOpen?: () => boolean;
}

const delay = (ms: number): Promise<void> => new Promise((resolve) => setTimeout(resolve, ms));

export class Archives {
  public archive: ArchiveDescriptor | null = null;
  public items: ArchiveMember[] = [];
  public offset = 0;
  public total = 0;
  public selected = -1;
  public ticket = 0;
  public operation = 0;
  public busy = false;
  public job = '';

  private readonly api: ApiClient;
  private readonly openWorkbook: (result: WorkbookStatusResponse) => Promise<void> | void;
  private readonly fmtBytes: (value: unknown) => string;
  private readonly canOpen: () => boolean;
  private readonly dialog: HTMLDialogElement;
  private searchTimer: ReturnType<typeof setTimeout> | undefined;
  private controller: AbortController | null = null;
  private activity: ActivityHandle | null = null;

  constructor(options: ArchivesOptions) {
    this.api = options.api;
    this.openWorkbook = options.open;
    this.fmtBytes = options.fmtBytes;
    this.canOpen = options.canOpen ?? (() => true);
    this.dialog = requireDialog('archiveDialog');

    requireEl('archiveBtn').addEventListener('click', () => this.present(this.archive));
    const search = requireInput('archiveSearch');
    search.addEventListener('input', () => {
      if (this.searchTimer !== undefined) clearTimeout(this.searchTimer);
      this.searchTimer = setTimeout(() => {
        this.offset = 0;
        void this.load();
      }, 120);
    });
    search.addEventListener('keydown', (event: KeyboardEvent) => {
      if (isComposing(event)) return;
      const first = this.items[0];
      if (event.key === 'ArrowDown' && first) {
        event.preventDefault();
        requireEl('archiveList').focus();
        this.select(this.selected >= 0 ? this.selected : first.id);
      }
      if (event.key === 'Enter' && this.selected >= 0) {
        event.preventDefault();
        void this.openSelected();
      }
    });
    requireButton('archivePrev').addEventListener('click', () => {
      this.offset = Math.max(0, this.offset - 100);
      void this.load();
    });
    requireButton('archiveNext').addEventListener('click', () => {
      this.offset += 100;
      void this.load();
    });
    requireButton('archiveOpen').addEventListener('click', () => {
      void this.openSelected();
    });
    requireInput('archiveAllowPartial').addEventListener('change', () => this.controls());
    requireButton('archiveCancel').addEventListener('click', () => {
      void this.cancel();
    });

    const list = requireEl('archiveList');
    list.addEventListener('click', (event: MouseEvent) => {
      const target = event.target instanceof Element ? event.target : null;
      const row = target?.closest<HTMLElement>('[data-member]');
      if (row && !this.busy) this.select(Number(row.dataset.member));
    });
    list.addEventListener('dblclick', (event: MouseEvent) => {
      const target = event.target instanceof Element ? event.target : null;
      const row = target?.closest<HTMLElement>('[data-member]');
      if (row && !this.busy) {
        this.select(Number(row.dataset.member));
        void this.openSelected();
      }
    });
    list.addEventListener('keydown', (event: KeyboardEvent) => this.keydown(event));
    this.dialog.addEventListener('close', () => {
      if (this.busy) void this.cancel();
    });
  }

  use(summary: ArchiveDescriptor | null | undefined): void {
    if (this.archive?.id !== summary?.id) {
      this.activity?.end();
      ++this.ticket;
      ++this.operation;
      this.controller?.abort();
      const previousJob = this.job;
      this.job = '';
      this.busy = false;
      if (previousJob) {
        this.api(`/api/archive/job?id=${encodeURIComponent(previousJob)}`, {
          method: 'POST',
        }).catch(() => {
          // Best-effort cancellation of superseded archive extraction job.
        });
      }
      this.items = [];
      this.offset = 0;
      this.total = 0;
      this.selected = -1;
      const search = requireInput('archiveSearch');
      search.value = '';
      search.disabled = false;
      requireInput('archiveAllowPartial').checked = false;
      requireEl('archivePartialChoice').hidden = true;
      requireEl('archiveProgress').hidden = true;
    }
    this.archive = summary ?? null;
    const notice = requireEl('archiveNotice');
    notice.textContent = summary?.notice ?? '';
    notice.hidden = !summary?.notice;
    const btn = requireEl('archiveBtn');
    btn.hidden = !summary;
    btn.title = summary ? localize('ui.archives.browsing', { detail: summary.name ?? '' }) : localize('ui.archives.viewCurrentCompressedPackage');
  }

  present(summary: ArchiveDescriptor | null | undefined): void {
    if (!summary) return;
    this.use(summary);
    requireEl('archiveTitle').textContent = summary.name ?? '';
    if (!this.dialog.open) this.dialog.showModal();
    void this.load();
    requireInput('archiveSearch').focus();
  }

  error(message: string): void {
    const el = requireEl('archiveError');
    el.textContent = message;
    el.hidden = !message;
  }

  async load(): Promise<void> {
    if (!this.archive?.id || this.busy) return;
    const ticket = ++this.ticket;
    this.controller?.abort();
    const controller = new AbortController();
    this.controller = controller;
    this.error('');
    requireEl('archiveSummary').textContent = localize('ui.archives.readingFileDirectory');
    try {
      const params = new URLSearchParams({
        id: this.archive.id,
        q: requireInput('archiveSearch').value,
        offset: String(this.offset),
        limit: '100',
      });
      const data = await this.api(`/api/archive/list?${params.toString()}`, {
        signal: controller.signal,
      }, decodeArchiveListApiResponse);
      if (ticket !== this.ticket) return;
      const previous = this.selected;
      this.items = data.items ? [...data.items] : [];
      this.total = data.total;
      if (data.archive) {
        this.archive = data.archive;
        const notice = requireEl('archiveNotice');
        notice.textContent = data.archive.notice ?? '';
        notice.hidden = !data.archive.notice;
      }
      this.selected = this.items.some((item) => item.id === previous)
        ? previous
        : (this.items[0]?.id ?? -1);
      if (this.selected !== previous) {
        requireInput('archiveAllowPartial').checked = false;
      }
      this.render();
    } catch (error: unknown) {
      if (!(error instanceof Error && error.name === 'AbortError') && ticket === this.ticket) {
        this.error(errorMessage(error));
      }
    }
  }

  render(): void {
    const list = requireEl('archiveList');
    const fragment = document.createDocumentFragment();
    for (const item of this.items) {
      const row = document.createElement('div');
      row.className = 'archive-member';
      row.dataset.member = String(item.id);
      row.id = `archive-member-${item.id}`;
      row.setAttribute('role', 'option');
      row.classList.toggle('incomplete', Boolean(item.incomplete));
      const iconEl = document.createElement('span');
      iconEl.className = 'archive-file-kind';
      iconEl.textContent = item.archive
        ? localize('ui.archives.compressedPackage')
        : (item.name.split('.').pop() ?? '').toUpperCase();
      const content = document.createElement('span');
      content.className = 'archive-file-label';
      const slash = item.name.lastIndexOf('/');
      const name = document.createElement('strong');
      name.textContent = item.name.slice(slash + 1);
      if (item.incomplete) name.textContent += localize('ui.archives.incompleteFragment');
      const folder = document.createElement('span');
      folder.textContent = slash < 0 ? localize('ui.archives.rootDirectory') : item.name.slice(0, slash);
      folder.title = item.name;
      content.append(name, folder);
      const size = document.createElement('span');
      size.className = 'archive-file-size';
      size.textContent = item.size < 0 ? localize('ui.archives.readAsNeeded') : this.fmtBytes(item.size);
      if (item.incomplete) {
        size.title = localize('ui.archives.existingOriginalStatement', { fmtBytes: this.fmtBytes(item.size), fmtBytes2: this.fmtBytes(item.declaredSize) });
      }
      row.append(iconEl, content, size);
      fragment.append(row);
    }
    if (!this.items.length) {
      const empty = document.createElement('div');
      empty.className = 'archive-empty';
      empty.textContent = localize('ui.archives.noMatchingFilesFoundAdjust');
      fragment.append(empty);
    }
    list.replaceChildren(fragment);
    list.scrollTop = 0;
    this.select(this.selected, false);
    this.controls();
  }

  controls(): void {
    const partial = Boolean(this.items.find((item) => item.id === this.selected)?.incomplete);
    requireEl('archivePartialChoice').hidden = !partial;
    const allowPartial = requireInput('archiveAllowPartial');
    allowPartial.disabled = this.busy;
    const openBtn = requireButton('archiveOpen');
    openBtn.textContent = partial ? localize('ui.archives.viewTxtSnippet') : localize('ui.archives.openFile');
    openBtn.disabled = this.busy || this.selected < 0 || (partial && !allowPartial.checked);
    requireInput('archiveSearch').disabled = this.busy;
    requireButton('archivePrev').disabled = this.busy || this.offset === 0;
    requireButton('archiveNext').disabled = this.busy || this.offset + this.items.length >= this.total;
    requireButton('archiveCancel').disabled = !this.busy;
    requireEl('archiveList').setAttribute('aria-busy', String(this.busy));
    requireEl('archiveSummary').textContent = this.total
      ? localize('ui.archives.files', { offset: this.offset + 1, count: this.offset + this.items.length, total: this.total })
      : localize('ui.archives.matchingFiles');
  }

  select(id: number, scroll = true): void {
    if (id !== this.selected) requireInput('archiveAllowPartial').checked = false;
    this.selected = id;
    const list = requireEl('archiveList');
    for (const row of list.children) {
      if (!(row instanceof HTMLElement)) continue;
      const active = Number(row.dataset.member) === id;
      row.classList.toggle('selected', active);
      row.setAttribute('aria-selected', String(active));
      if (active && scroll) row.scrollIntoView({ block: 'nearest' });
    }
    list.setAttribute('aria-activedescendant', id >= 0 ? `archive-member-${id}` : '');
    this.controls();
  }

  keydown(event: KeyboardEvent): void {
    if (this.busy || !this.items.length || isComposing(event)) return;
    let index = this.items.findIndex((x) => x.id === this.selected);
    if (event.key === 'Enter') {
      event.preventDefault();
      void this.openSelected();
      return;
    }
    if (event.key === 'ArrowDown') index++;
    else if (event.key === 'ArrowUp') index--;
    else if (event.key === 'Home') index = 0;
    else if (event.key === 'End') index = this.items.length - 1;
    else if (event.key === 'PageDown') index += 10;
    else if (event.key === 'PageUp') index -= 10;
    else return;
    event.preventDefault();
    const target = this.items[Math.max(0, Math.min(this.items.length - 1, index))];
    if (target) this.select(target.id);
  }

  async cancel(): Promise<void> {
    if (!this.job) return;
    requireButton('archiveCancel').disabled = true;
    requireEl('archiveStage').textContent = localize('ui.archives.cancelingRead');
    try {
      await this.api(`/api/archive/job?id=${encodeURIComponent(this.job)}`, { method: 'POST' });
    } catch (error: unknown) {
      this.error(errorMessage(error));
    }
  }

  async openSelected(): Promise<void> {
    if (this.busy || this.selected < 0 || !this.archive?.id) return;
    if (!this.canOpen()) return;
    const partial = Boolean(this.items.find((item) => item.id === this.selected)?.incomplete);
    const allowPartial = requireInput('archiveAllowPartial');
    if (partial && !allowPartial.checked) {
      this.error(localize('ui.archives.thisTxtIncompleteFirstCheck'));
      return;
    }
    const operation = ++this.operation;
    const activity = beginActivity(localize('ui.archives.compressedFilesBeingRead'), { slot: 'archiveActivity' });
    this.activity = activity;
    this.busy = true;
    this.error('');
    this.controls();
    requireEl('archiveProgress').hidden = false;
    requireEl('archiveStage').textContent = localize('ui.archives.preparingRead');
    const bar = requireEl('archiveBar');
    bar.removeAttribute('value');
    try {
      const started = await this.api('/api/archive/open', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          archive: this.archive.id,
          member: this.selected,
          allowPartial: partial && allowPartial.checked,
        }),
      }, decodeArchiveOpenStartResponse);
      if (operation !== this.operation) {
        this.api(`/api/archive/job?id=${encodeURIComponent(started.jobId)}`, {
          method: 'POST',
        }).catch(() => {
          // Best-effort cancellation of stale extraction job.
        });
        return;
      }
      this.job = started.jobId;
      for (;;) {
        const state = await this.api(
          `/api/archive/job?id=${encodeURIComponent(started.jobId)}`,
          undefined,
          decodeArchiveJobStatusResponse,
        );
        if (operation === this.operation) activity?.job(state);
        if (operation !== this.operation) return;
        requireEl('archiveStage').textContent = state.stage;
        if (state.totalBytes > 0) {
          if (bar instanceof HTMLProgressElement) {
            bar.value = Math.min(100, (state.doneBytes / state.totalBytes) * 100);
          }
        } else {
          bar.removeAttribute('value');
        }
        if (state.status === 'error') throw new Error(state.error || localize('ui.archives.readingFailure'));
        if (state.status === 'cancelled') {
          this.error(localize('ui.archives.readingCanceledCanChooseAnother'));
          break;
        }
        if (state.status === 'done') {
          const result = decodeOpenResult(state.result);
          this.busy = false;
          this.job = '';
          this.dialog.close();
          await this.openWorkbook(result);
          break;
        }
        await delay(240);
      }
    } catch (error: unknown) {
      if (operation === this.operation) this.error(errorMessage(error));
    } finally {
      activity?.end();
      if (operation === this.operation) {
        this.activity = null;
        this.busy = false;
        this.job = '';
        requireEl('archiveProgress').hidden = true;
        this.controls();
      }
    }
  }
}
