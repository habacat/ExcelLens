/**
 * Recent workbooks list backed by Go's user-local recent.json.
 * Never stores spreadsheet content, never accesses the network outside localhost,
 * and never assumes a missing file is safe to delete automatically.
 */
import { interfaceLocale, t as localize } from '../core/i18n.ts';
import type { ApiClient, ToastFn } from '../contracts/common.ts';
import type { RecentEntry } from '../contracts/features.ts';
import { errorMessage, formatBytes, icon } from '../core/display-format.ts';
import { requireEl, requireInput } from '../core/dom.ts';
import { decodeRecentListResponse } from '../core/http-decoders.generated.ts';

/** @httpResponse RecentListResponse */
export interface RecentListResponse {
  readonly items?: readonly RecentEntry[] | null;
}

export interface RecentWorkbooksOptions {
  readonly api: ApiClient;
  readonly open: (entry: RecentEntry) => Promise<unknown>;
  readonly toast: ToastFn;
}

export class RecentWorkbooks {
  public items: RecentEntry[] = [];
  public ticket = 0;
  public busy = false;

  private readonly api: ApiClient;
  private readonly open: (entry: RecentEntry) => Promise<unknown>;
  private readonly toast: ToastFn;
  private readonly list: HTMLElement;
  private readonly search: HTMLInputElement;
  private readonly ageTimer: ReturnType<typeof setInterval>;

  constructor(options: RecentWorkbooksOptions) {
    this.api = options.api;
    this.open = options.open;
    this.toast = options.toast;
    this.list = requireEl('recentList');
    this.search = requireInput('recentSearch');

    this.search.addEventListener('input', () => this.render());
    requireEl('recentRefreshBtn').addEventListener('click', () => {
      void this.load();
    });
    this.list.addEventListener('click', (event: MouseEvent) => {
      void this.handleClick(event);
    });
    this.list.addEventListener('keydown', (event: KeyboardEvent) => {
      if (
        (event.key === 'Enter' || event.key === ' ') &&
        event.target instanceof HTMLElement &&
        event.target.matches('.recent-item')
      ) {
        event.preventDefault();
        event.target.click();
      }
    });
    window.addEventListener('focus', () => this.refreshTimes());
    this.ageTimer = setInterval(() => this.refreshTimes(), 60000);
  }

  prettyDate(raw: string, now = Date.now()): string {
    const timestamp = Date.parse(raw);
    if (!Number.isFinite(timestamp)) return localize('ui.recent.timeUnknown');
    const age = Math.max(0, now - timestamp);
    const minute = 60000;
    const hour = 60 * minute;
    const day = 24 * hour;
    if (age < minute) return localize('ui.recent.justNow');
    if (age < hour) return localize('ui.recent.minutesAgo', { value: Math.max(1, Math.floor(age / minute)) });
    if (age < day) return localize('ui.recent.hoursAgo', { value: Math.max(1, Math.floor(age / hour)) });
    if (age < 30 * day) return localize('ui.recent.daysBefore', { value: Math.max(1, Math.floor(age / day)) });
    const date = new Date(timestamp);
    const today = new Date(now);
    const months =
      (today.getFullYear() - date.getFullYear()) * 12 + today.getMonth() - date.getMonth();
    if (months === 1) return localize('ui.recent.lastMonth');
    if (months >= 2 && months < 12 && age < 365 * day) return localize('ui.recent.monthsAgo', { months });
    if (age < 60 * day) return localize('ui.recent.lastMonth');
    if (age < 365 * day) return localize('ui.recent.monthsAgo758e818', { value: Math.max(2, Math.floor(age / (30 * day))) });
    return localize('ui.recent.longTimeAgo');
  }

  exactDate(raw: string): string {
    const date = new Date(raw);
    return Number.isNaN(date.getTime())
      ? localize('ui.recent.timeUnknown')
      : new Intl.DateTimeFormat(interfaceLocale(), {
          year: 'numeric',
          month: '2-digit',
          day: '2-digit',
          hour: '2-digit',
          minute: '2-digit',
          second: '2-digit',
          hour12: false,
        }).format(date);
  }

  formatSize(bytes: number | undefined): string {
    return formatBytes(bytes);
  }

  refreshTimes(): void {
    const now = Date.now();
    for (const node of this.list.querySelectorAll<HTMLElement>('.recent-date[data-opened-at]')) {
      node.textContent = this.prettyDate(node.dataset.openedAt ?? '', now);
    }
  }

  async load(): Promise<void> {
    const ticket = ++this.ticket;
    try {
      const data = await this.api('/api/recent', undefined, decodeRecentListResponse);
      if (ticket !== this.ticket) return;
      this.items = data.items ? [...data.items] : [];
      this.render();
    } catch (error: unknown) {
      if (ticket !== this.ticket) return;
      this.list.replaceChildren(this.empty(localize('ui.recent.failureReadLatestFile', { detail: errorMessage(error) })));
    }
  }

  empty(message: string): HTMLDivElement {
    const el = document.createElement('div');
    el.className = 'recent-empty';
    const detail = document.createElement('span');
    detail.textContent = message;
    el.append(detail);
    return el;
  }

  render(): void {
    const q = this.search.value.trim().toLocaleLowerCase();
    const matching = this.items.filter(
      (x) => !q || `${x.name} ${x.path}`.toLocaleLowerCase().includes(q),
    );
    const frag = document.createDocumentFragment();
    const latest = this.items.reduce<RecentEntry | null>(
      (best, item) => (!best || Date.parse(item.openedAt) > Date.parse(best.openedAt) ? item : best),
      null,
    );
    const now = Date.now();
    for (const item of matching) {
      const btn = document.createElement('div');
      btn.className = `recent-item${item.available ? '' : ' is-missing'}`;
      btn.dataset.id = item.id;
      const fileIcon = document.createElement('div');
      fileIcon.className = 'recent-file-icon';
      fileIcon.textContent = (item.name.split('.').pop() || 'XL').slice(0, 2).toUpperCase();
      const content = document.createElement('div');
      content.className = 'recent-content';
      const title = document.createElement('div');
      title.className = 'recent-title-line';
      const name = document.createElement('div');
      name.className = 'recent-name';
      name.textContent = item.name;
      name.title = item.path;
      title.append(name);
      if (latest?.id === item.id) {
        const tag = document.createElement('span');
        tag.className = 'recent-last-tag';
        tag.textContent = localize('ui.recent.lastOpen');
        tag.title = localize('ui.recent.lastWorkbookOpened');
        title.append(tag);
      }
      const meta = document.createElement('div');
      meta.className = 'recent-meta';
      const one = document.createElement('span');
      one.textContent = item.available ? localize('ui.recent.table', { sheetCount: item.sheetCount }) : localize('ui.recent.fileMovedOrDeleted');
      const two = document.createElement('span');
      two.textContent = item.archiveName ? `${item.archiveName} / ${item.name}` : item.path;
      two.title = item.archivePath || item.path;
      meta.append(one, two);
      content.append(title, meta);

      const side = document.createElement('div');
      side.className = 'recent-side';
      const date = document.createElement('div');
      date.className = 'recent-date';
      date.dataset.openedAt = item.openedAt;
      date.textContent = this.prettyDate(item.openedAt, now);
      const exact = this.exactDate(item.openedAt);
      date.title = localize('ui.recent.openingTime', { exact });
      date.setAttribute('aria-label', localize('ui.recent.openingTime', { exact }));
      const size = document.createElement('span');
      size.className = 'recent-size';
      size.textContent = this.formatSize(item.fileSize);
      size.title = size.textContent === '—' ? localize('ui.recent.originalFileSizeUnknown') : localize('ui.recent.originalFileSize', { textContent: size.textContent });

      const buttons = document.createElement('div');
      buttons.className = 'recent-controls';
      const pin = document.createElement('button');
      pin.type = 'button';
      pin.className = `recent-action${item.pinned ? ' is-pinned' : ''}`;
      pin.dataset.action = 'pin';
      pin.innerHTML = icon('pin');
      pin.title = item.pinned ? localize('ui.recent.unpin') : localize('ui.recent.pinned');
      pin.setAttribute('aria-label', pin.title);
      const remove = document.createElement('button');
      remove.type = 'button';
      remove.className = 'recent-action';
      remove.dataset.action = 'remove';
      remove.innerHTML = icon('x');
      remove.title = localize('ui.recent.removedFromListFileNot');
      remove.setAttribute('aria-label', remove.title);
      buttons.append(pin, remove);
      side.append(date, size, buttons);
      btn.append(fileIcon, content, side);
      btn.setAttribute('role', 'button');
      btn.tabIndex = 0;
      btn.title = item.available ? '' : localize('ui.recent.fileNotOriginalLocationSelect');
      frag.appendChild(btn);
    }
    if (!matching.length) {
      frag.appendChild(this.empty(q ? localize('ui.recent.noMatchingFilesFound') : localize('ui.recent.filesThatHaveOpenedWill')));
    }
    this.list.replaceChildren(frag);
    requireEl('recentSummary').textContent = this.items.length ? localize('ui.recent.totalFiles', { count: this.items.length }) : '';
  }

  async handleClick(event: MouseEvent): Promise<void> {
    const target = event.target instanceof Element ? event.target : null;
    const itemNode = target?.closest<HTMLElement>('.recent-item');
    if (!itemNode || this.busy) return;
    const id = itemNode.dataset.id;
    const entry = this.items.find((x) => x.id === id);
    if (!entry) return;
    const action = target?.closest<HTMLElement>('[data-action]')?.dataset.action || 'open';
    if (action === 'open' && !entry.available) {
      this.toast(localize('ui.recent.fileMovedOrDeletedCan'), 'warning');
      return;
    }
    this.busy = true;
    itemNode.classList.add('recent-working');
    try {
      if (action === 'open') {
        await this.open(entry);
      } else {
        await this.api('/api/recent', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ action, id }),
        });
        await this.load();
      }
    } catch (err: unknown) {
      this.toast(errorMessage(err), 'error');
      await this.load();
    } finally {
      this.busy = false;
      itemNode.classList.remove('recent-working');
    }
  }

  dispose(): void {
    clearInterval(this.ageTimer);
  }
}
