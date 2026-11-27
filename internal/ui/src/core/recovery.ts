/**
 * Per-page persistent draft recovery journal (separate from source files and
 * saved analysis data). Only a synced server receipt clears matching pending input.
 */
import type { ApiClient, HttpError, SessionService } from '../contracts/common.ts';
import { errorMessage } from './display-format.ts';
import { decodeRecoveryListResponse, decodeRecoverySaveResponse } from './http-decoders.generated.ts';

export type RecoveryKind = 'calculator' | 'analysis';

export interface RecoverySnapshotResponse<TRecord = unknown> {
  readonly revision: number;
  readonly records: Readonly<Record<string, TRecord>>;
  readonly recovered?: boolean;
}

/** @httpResponse RecoverySaveResponse */
export interface RecoverySaveResponse {
  readonly revision: number;
}

export interface RecoveryListEntry {
  readonly owner: string;
  readonly updated: string;
  readonly bytes: number;
  readonly backup?: boolean;
}

/** @httpResponse RecoveryListResponse */
export interface RecoveryListResponse {
  readonly entries?: readonly RecoveryListEntry[];
}

export interface RecoveryJournalState {
  readonly dirty: boolean;
  readonly loading: boolean;
  readonly saving: boolean;
  readonly error: string;
  readonly blocked: boolean;
  readonly count: number;
  readonly revision: number;
}

export interface RecoveryRestoreContext {
  readonly automatic: boolean;
  readonly recovered: boolean;
}

export interface RecoveryJournalOptions<TRecord> {
  readonly kind: RecoveryKind;
  readonly api: ApiClient;
  readonly session?: SessionService;
  readonly onRestore?: (
    records: Record<string, TRecord>,
    context: RecoveryRestoreContext,
  ) => void;
  readonly onStatus?: (state: RecoveryJournalState) => void;
  readonly prepareCopy?: (
    incoming: Readonly<Record<string, TRecord>>,
    existing: ReadonlyMap<string, TRecord>,
  ) => Record<string, TRecord>;
}

interface PendingChange<TRecord> {
  readonly key: string;
  readonly data: TRecord | null;
}

interface InflightBatch<TRecord> {
  readonly entries: readonly PendingChange<TRecord>[];
  readonly request: {
    readonly owner: string;
    readonly kind: RecoveryKind;
    readonly revision: number;
    readonly requestId: string;
    readonly changes: readonly { readonly key: string; readonly data: TRecord | null }[];
  };
}

export interface RecoveryImportResult {
  readonly count: number;
  readonly skipped: number;
  readonly saved: boolean;
}

const utf8Bytes = (text: string): number => new TextEncoder().encode(text).length;

function makeID(): string {
  if (typeof crypto.randomUUID === 'function') return crypto.randomUUID();
  return Array.from(crypto.getRandomValues(new Uint8Array(20)), (n) =>
    n.toString(16).padStart(2, '0'),
  ).join('');
}

function validID(id: string | null | undefined): id is string {
  return typeof id === 'string' && /^[A-Za-z0-9_-]{20,64}$/.test(id);
}

function getHttpStatus(error: unknown): number | undefined {
  if (typeof error === 'object' && error !== null && 'status' in error) {
    const status = (error as HttpError).status;
    if (typeof status === 'number') return status;
  }
  return undefined;
}

function isPlainRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

export class RecoveryJournal<TRecord = Record<string, unknown>> {
  public readonly kind: RecoveryKind;
  public readonly api: ApiClient;
  private readonly session?: SessionService;
  private readonly onRestore: (
    records: Record<string, TRecord>,
    context: RecoveryRestoreContext,
  ) => void;
  private readonly onStatus: (state: RecoveryJournalState) => void;
  private readonly prepareCopy: (
    incoming: Readonly<Record<string, TRecord>>,
    existing: ReadonlyMap<string, TRecord>,
  ) => Record<string, TRecord>;

  public readonly records = new Map<string, TRecord>();
  private readonly encoded = new Map<string, string>();
  public pending = new Map<string, PendingChange<TRecord>>();
  public revision = 0;
  public owner = '';
  public ownerKey = '';
  private inflight: InflightBatch<TRecord> | null = null;
  private running: Promise<boolean> | null = null;
  private timer: ReturnType<typeof setTimeout> | undefined;
  public error = '';
  public blocked = false;
  private capacityBlocked = false;
  public initialized = false;
  public loading = true;
  private failures = 0;
  public ready: Promise<boolean>;

  constructor(options: RecoveryJournalOptions<TRecord>) {
    this.kind = options.kind;
    this.api = options.api;
    this.session = options.session;
    this.onRestore =
      options.onRestore ??
      (() => {
        // default no-op
      });
    this.onStatus =
      options.onStatus ??
      (() => {
        // default no-op
      });
    this.prepareCopy =
      options.prepareCopy ?? ((records) => ({ ...records }));
    this.ready = this.initialize();

    window.addEventListener('beforeunload', (event: BeforeUnloadEvent) => {
      if (!this.dirty) return;
      if (!this.session?.cacheCleared) {
        event.preventDefault();
      }
      void this.flush(true);
    });
    window.addEventListener('pagehide', () => {
      void this.flush(true);
    });
    window.addEventListener('online', () => {
      if (this.dirty && !this.blocked) void this.flush();
    });
  }

  get dirty(): boolean {
    return this.pending.size > 0 || this.inflight !== null;
  }

  state(): RecoveryJournalState {
    return {
      dirty: this.dirty,
      loading: this.loading,
      saving: this.running !== null,
      error: this.error,
      blocked: this.blocked,
      count: this.records.size,
      revision: this.revision,
    };
  }

  notify(): void {
    this.onStatus(this.state());
  }

  rememberOwner(): void {
    try {
      sessionStorage.setItem(this.ownerKey, this.owner);
    } catch {
      // Ignore storage failures.
    }
  }

  async initialize(): Promise<boolean> {
    try {
      await this.session?.ready;
      const sessionId = this.session?.id || makeID();
      this.ownerKey = `ExcelLens.recovery.${this.kind}.${sessionId}`;
      let saved = '';
      try {
        saved = sessionStorage.getItem(this.ownerKey) ?? '';
      } catch {
        // Ignore storage failures.
      }
      this.owner = validID(saved) ? saved : sessionId;
      const snapshot = await this.api(
        `/api/recovery?kind=${this.kind}&owner=${encodeURIComponent(this.owner)}`,
      );
      this.validateSnapshot(snapshot);
      const restored: Record<string, TRecord> = {};
      for (const [key, data] of Object.entries(snapshot.records)) {
        if (!this.pending.has(key)) {
          this.records.set(key, data);
          this.encoded.set(key, JSON.stringify(data));
          restored[key] = data;
        }
      }
      this.onRestore(restored, { automatic: true, recovered: Boolean(snapshot.recovered) });
      this.revision = snapshot.revision;
      this.initialized = true;
      this.loading = false;
      this.notify();
      if (this.dirty) this.schedule();
      return true;
    } catch (error: unknown) {
      this.loading = false;
      this.blocked = true;
      this.error = `恢复副本未载入：${errorMessage(error)}`;
      this.notify();
      return false;
    }
  }

  validateSnapshot(snapshot: unknown): asserts snapshot is RecoverySnapshotResponse<TRecord> {
    if (
      !isPlainRecord(snapshot) ||
      typeof snapshot.revision !== 'number' ||
      !Number.isSafeInteger(snapshot.revision) ||
      snapshot.revision < 0 ||
      !isPlainRecord(snapshot.records) ||
      Object.keys(snapshot.records).length > 20000
    ) {
      throw new Error('恢复副本结构无效');
    }
  }

  set(key: string | number, data: TRecord | null | undefined): void {
    const id = String(key);
    const encoded = data == null ? null : JSON.stringify(data);
    if (encoded === (this.encoded.get(id) ?? null)) return;
    const copy: TRecord | null = encoded === null ? null : (JSON.parse(encoded) as TRecord);
    if (copy === null) {
      this.records.delete(id);
      this.encoded.delete(id);
    } else {
      this.records.set(id, copy);
      this.encoded.set(id, encoded ?? '');
    }
    this.pending.set(id, { key: id, data: copy });
    this.notify();
    this.schedule();
    if (this.capacityBlocked) {
      this.capacityBlocked = false;
      this.blocked = false;
      this.error = '';
      this.notify();
      this.schedule();
    }
  }

  schedule(delayMs = 400): void {
    if (this.timer !== undefined) {
      clearTimeout(this.timer);
      this.timer = undefined;
    }
    if (!this.initialized || this.blocked || this.running || !this.dirty) return;
    this.timer = setTimeout(() => {
      this.timer = undefined;
      void this.flush();
    }, delayMs);
  }

  dispose(): void {
    if (this.timer !== undefined) {
      clearTimeout(this.timer);
      this.timer = undefined;
    }
  }

  batch(): InflightBatch<TRecord> {
    const entries: PendingChange<TRecord>[] = [];
    let size = 512;
    const ordered = function* (
      pending: Map<string, PendingChange<TRecord>>,
    ): Generator<PendingChange<TRecord>> {
      for (const item of pending.values()) if (item.data === null) yield item;
      for (const item of pending.values()) if (item.data !== null) yield item;
    };
    for (const entry of ordered(this.pending)) {
      const n = utf8Bytes(JSON.stringify(entry)) + 1;
      if (entries.length && (size + n > 48 * 1024 || entries.length >= 128)) break;
      if (n > 64 * 1024) {
        const error: HttpError = new Error('单条恢复内容过长，请缩短公式或备注');
        error.status = 413;
        throw error;
      }
      entries.push(entry);
      size += n;
    }
    return {
      entries,
      request: {
        owner: this.owner,
        kind: this.kind,
        revision: this.revision,
        requestId: makeID(),
        changes: entries.map(({ key, data }) => ({ key, data })),
      },
    };
  }

  flush(keepalive = false): Promise<boolean> {
    if (this.timer !== undefined) {
      clearTimeout(this.timer);
      this.timer = undefined;
    }
    if (this.running) return this.running;
    const work = async (): Promise<boolean> => {
      await this.ready;
      if (!this.initialized || this.blocked) return false;
      try {
        while (this.pending.size || this.inflight) {
          this.inflight ??= this.batch();
          const sent = this.inflight;
          const body = JSON.stringify(sent.request);
          if (keepalive && utf8Bytes(body) > 60 * 1024) return false;
          const controller = new AbortController();
          const timeout = setTimeout(() => controller.abort(), 15000);
          let meta: RecoverySaveResponse;
          try {
            meta = await this.api('/api/recovery', {
              method: 'POST',
              headers: { 'Content-Type': 'application/json' },
              body,
              keepalive,
              signal: controller.signal,
            }, decodeRecoverySaveResponse);
          } finally {
            clearTimeout(timeout);
          }
          if (!Number.isSafeInteger(meta.revision) || meta.revision !== sent.request.revision + 1) {
            throw new Error('恢复保存确认版本无效');
          }
          this.revision = meta.revision;
          this.failures = 0;
          this.error = '';
          for (const entry of sent.entries) {
            if (this.pending.get(entry.key) === entry) this.pending.delete(entry.key);
          }
          this.inflight = null;
          this.notify();
          if (keepalive) break;
        }
        return !this.dirty;
      } catch (error: unknown) {
        this.error = `恢复未保存：${errorMessage(error)}`;
        this.failures++;
        const status = getHttpStatus(error);
        this.blocked = status !== undefined && [400, 409, 413, 422].includes(status);
        if (status === 413) {
          this.inflight = null;
          this.capacityBlocked = true;
        }
        return false;
      }
    };
    this.running = work().finally(() => {
      this.running = null;
      this.notify();
      if (this.dirty && !this.blocked) {
        this.schedule(Math.min(30000, 1000 * 2 ** Math.min(this.failures, 5)));
      }
    });
    this.notify();
    return this.running;
  }

  async retry(): Promise<boolean> {
    if (!this.initialized) {
      this.blocked = false;
      this.loading = true;
      this.ready = this.initialize();
      return this.ready;
    }
    if (this.blocked) return false;
    return this.flush();
  }

  async fork(): Promise<boolean> {
    await this.ready;
    if (this.running) await this.running;
    this.owner = makeID();
    this.revision = 0;
    this.inflight = null;
    this.initialized = true;
    this.loading = false;
    this.blocked = false;
    this.capacityBlocked = false;
    this.error = '';
    this.failures = 0;
    this.pending = new Map([...this.records].map(([key, data]) => [key, { key, data }]));
    this.rememberOwner();
    this.notify();
    return this.flush();
  }

  async importCopy(entry: RecoveryListEntry): Promise<RecoveryImportResult> {
    await this.ready;
    if (this.running) await this.running;
    const snapshot = await this.api(
      `/api/recovery?kind=${this.kind}&owner=${encodeURIComponent(entry.owner)}${entry.backup ? '&backup=1' : ''}`,
    );
    this.validateSnapshot(snapshot);
    const incoming = this.prepareCopy(snapshot.records, this.records);
    const added: Record<string, TRecord> = {};
    let skipped = 0;
    for (const [key, data] of Object.entries(incoming)) {
      if (this.records.has(key)) {
        skipped++;
        continue;
      }
      added[key] = data;
    }
    this.onRestore(added, { automatic: false, recovered: Boolean(snapshot.recovered) });
    for (const [key, data] of Object.entries(added)) {
      this.records.set(key, data);
      this.encoded.set(key, JSON.stringify(data));
    }
    const saved = await this.fork();
    return { count: Object.keys(added).length, skipped, saved };
  }
}

function previewLine(kind: RecoveryKind, key: string, value: unknown): string {
  const rec = isPlainRecord(value) ? value : {};
  const str = (field: string): string => (typeof rec[field] === 'string' ? rec[field] : '');
  const num = (field: string): number => (typeof rec[field] === 'number' ? rec[field] : Number(rec[field] ?? 0));
  if (kind === 'calculator') {
    const note = str('note');
    return `第${Number(key) + 1}行　${str('text')}${note ? `　备注：${note}` : ''}`;
  }
  return `${str('book')} / ${str('id')} / ${num('row') + 1},${num('col') + 1}　${str('value')}`;
}

let activeDialog: HTMLDialogElement | null = null;

export function showRecoveryDialog<TRecord>(
  journal: RecoveryJournal<TRecord>,
  options: { readonly title: string; readonly explanation: string },
): void {
  if (activeDialog?.open) {
    activeDialog.focus();
    return;
  }
  const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
  const el = document.createElement('dialog');
  activeDialog = el;
  el.className = 'recovery-dialog';
  el.setAttribute('aria-label', options.title);

  const head = document.createElement('div');
  head.className = 'recovery-head';
  const heading = document.createElement('strong');
  heading.textContent = options.title;
  const close = document.createElement('button');
  close.type = 'button';
  close.textContent = '关闭';
  close.onclick = (): void => el.close();
  head.append(heading, close);

  const info = document.createElement('p');
  info.textContent = options.explanation;
  const status = document.createElement('p');
  status.className = 'recovery-status';
  status.setAttribute('role', 'status');

  const tools = document.createElement('div');
  tools.className = 'recovery-tools';
  const retry = document.createElement('button');
  retry.type = 'button';
  retry.textContent = '重试保存';
  const fork = document.createElement('button');
  fork.type = 'button';
  fork.textContent = '另存本页副本';
  tools.append(retry, fork);

  const list = document.createElement('div');
  list.className = 'recovery-list';
  list.textContent = '正在读取恢复副本…';
  const preview = document.createElement('pre');
  preview.className = 'recovery-preview';
  preview.hidden = true;
  const load = document.createElement('button');
  load.type = 'button';
  load.className = 'recovery-load';
  load.textContent = '载入副本';
  load.hidden = true;

  let selected: RecoveryListEntry | null = null;
  let busy = false;
  let serial = 0;

  const message = (): void => {
    status.textContent =
      journal.error ||
      (journal.dirty
        ? '本页还有未确认输入'
        : journal.records.size
          ? '本页恢复副本已保存'
          : '本页暂无输入');
    retry.disabled = busy;
    fork.disabled = busy || !journal.records.size;
    load.disabled = busy || !selected;
  };

  const run = async (fn: () => Promise<void>): Promise<void> => {
    if (busy) return;
    busy = true;
    message();
    try {
      await fn();
    } catch (error: unknown) {
      status.textContent = errorMessage(error);
    } finally {
      busy = false;
      retry.disabled = false;
      fork.disabled = !journal.records.size;
      load.disabled = !selected;
    }
  };

  retry.onclick = (): void => {
    void run(async () => {
      await journal.retry();
      message();
    });
  };
  fork.onclick = (): void => {
    void run(async () => {
      await journal.fork();
      message();
    });
  };
  load.onclick = (): void => {
    void run(async () => {
      if (!selected) return;
      const result = await journal.importCopy(selected);
      status.textContent =
        `已载入${result.count}条` +
        (result.skipped ? `；保留本页同格草稿${result.skipped}条` : '') +
        (result.saved ? '；恢复副本已保存' : `；${journal.error}`);
    });
  };

  el.append(head, info, status, tools, list, preview, load);
  document.body.append(el);
  el.addEventListener(
    'close',
    () => {
      ++serial;
      el.remove();
      if (activeDialog === el) activeDialog = null;
      previous?.focus({ preventScroll: true });
    },
    { once: true },
  );
  el.showModal();
  message();

  void journal.ready
    .then(() => journal.api(`/api/recovery/list?kind=${journal.kind}`, undefined, decodeRecoveryListResponse))
    .then((data) => {
      if (!el.open) return;
      list.replaceChildren();
      const entries = data.entries ?? [];
      if (!entries.length) {
        list.textContent = '暂无恢复副本';
        return;
      }
      for (const entry of entries) {
        const button = document.createElement('button');
        button.type = 'button';
        button.className = 'recovery-entry';
        button.textContent =
          new Date(entry.updated).toLocaleString() +
          (entry.backup ? '　备份' : '') +
          (entry.owner === journal.owner ? '　本页' : '') +
          `　${Math.ceil(entry.bytes / 1024)} KiB`;
        button.onclick = (): void => {
          void run(async () => {
            const ticket = ++serial;
            selected = null;
            load.hidden = true;
            preview.hidden = false;
            preview.textContent = '正在读取…';
            const copy = await journal.api(
              `/api/recovery?kind=${journal.kind}&owner=${encodeURIComponent(entry.owner)}${entry.backup ? '&backup=1' : ''}`,
            );
            if (ticket !== serial || !el.open) return;
            journal.validateSnapshot(copy);
            selected = entry;
            const items = Object.entries(copy.records);
            preview.textContent = (
              `${items.length}条输入${copy.recovered ? '（已找回中断前的完整记录）' : ''}\n` +
              items
                .slice(0, 5)
                .map(([key, v]) => previewLine(journal.kind, key, v))
                .join('\n')
            ).slice(0, 2400);
            load.hidden = false;
            load.disabled = false;
            message();
          });
        };
        list.append(button);
      }
    })
    .catch((error: unknown) => {
      if (el.open) list.textContent = errorMessage(error);
    });
}
