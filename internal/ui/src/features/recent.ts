/**
 * Recent workbooks list backed by Go's user-local recent.json.
 * Never stores spreadsheet content, never accesses the network outside localhost,
 * and never assumes a missing file is safe to delete automatically.
 */
import type { ApiClient, ToastFn } from '../contracts/common.ts';
import type { RecentEntry } from '../contracts/features.ts';
import { errorMessage, formatBytes, icon } from '../core/display-format.ts';
import { requireEl, requireInput } from '../core/dom.ts';

interface RecentListResponse {
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
    if (!Number.isFinite(timestamp)) return '时间未知';
    const age = Math.max(0, now - timestamp);
    const minute = 60000;
    const hour = 60 * minute;
    const day = 24 * hour;
    if (age < minute) return '刚刚';
    if (age < hour) return `${Math.max(1, Math.floor(age / minute))}分钟前`;
    if (age < day) return `${Math.max(1, Math.floor(age / hour))}小时前`;
    if (age < 30 * day) return `${Math.max(1, Math.floor(age / day))}天前`;
    const date = new Date(timestamp);
    const today = new Date(now);
    const months =
      (today.getFullYear() - date.getFullYear()) * 12 + today.getMonth() - date.getMonth();
    if (months === 1) return '上个月';
    if (months >= 2 && months < 12 && age < 365 * day) return `${months}个月前`;
    if (age < 60 * day) return '上个月';
    if (age < 365 * day) return `${Math.max(2, Math.floor(age / (30 * day)))}个月前`;
    return '很久以前';
  }

  exactDate(raw: string): string {
    const date = new Date(raw);
    return Number.isNaN(date.getTime())
      ? '时间未知'
      : new Intl.DateTimeFormat('zh-CN', {
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
      const data = await this.api<RecentListResponse>('/api/recent');
      if (ticket !== this.ticket) return;
      this.items = data.items ? [...data.items] : [];
      this.render();
    } catch (error: unknown) {
      if (ticket !== this.ticket) return;
      this.list.replaceChildren(this.empty(`最近文件读取失败：${errorMessage(error)}`));
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
        tag.textContent = '上次打开';
        tag.title = '最近一次打开的工作簿';
        title.append(tag);
      }
      const meta = document.createElement('div');
      meta.className = 'recent-meta';
      const one = document.createElement('span');
      one.textContent = item.available ? `${item.sheetCount} 张表` : '文件已移动或删除';
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
      date.title = `打开时间：${exact}`;
      date.setAttribute('aria-label', `打开时间：${exact}`);
      const size = document.createElement('span');
      size.className = 'recent-size';
      size.textContent = this.formatSize(item.fileSize);
      size.title = size.textContent === '—' ? '原文件大小未知' : `原文件大小：${size.textContent}`;

      const buttons = document.createElement('div');
      buttons.className = 'recent-controls';
      const pin = document.createElement('button');
      pin.type = 'button';
      pin.className = `recent-action${item.pinned ? ' is-pinned' : ''}`;
      pin.dataset.action = 'pin';
      pin.innerHTML = icon('pin');
      pin.title = item.pinned ? '取消置顶' : '置顶';
      pin.setAttribute('aria-label', pin.title);
      const remove = document.createElement('button');
      remove.type = 'button';
      remove.className = 'recent-action';
      remove.dataset.action = 'remove';
      remove.innerHTML = icon('x');
      remove.title = '从列表移除（不删除文件）';
      remove.setAttribute('aria-label', remove.title);
      buttons.append(pin, remove);
      side.append(date, size, buttons);
      btn.append(fileIcon, content, side);
      btn.setAttribute('role', 'button');
      btn.tabIndex = 0;
      btn.title = item.available ? '点击直接打开' : '文件不在原位置，请重新选择文件';
      frag.appendChild(btn);
    }
    if (!matching.length) {
      frag.appendChild(this.empty(q ? '没有匹配的文件' : '打开过的文件会列在这里'));
    }
    this.list.replaceChildren(frag);
    requireEl('recentSummary').textContent = this.items.length ? `共 ${this.items.length} 个文件` : '';
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
      this.toast('文件已移动或删除，可移除该记录后重新选择文件', 'warning');
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
