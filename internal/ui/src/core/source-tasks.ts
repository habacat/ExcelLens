/**
 * Asynchronous source preparation task runner shared by composition and relation
 * workflows. Each preparation owns its own runtime token, including late starts.
 */
import type {
  ActivityHandle,
  ActivityService,
  ApiClient,
  AsyncJobProgress,
} from '../contracts/common.ts';

export interface SourceTaskContext {
  readonly bookKey: string;
  readonly book: string;
  readonly opened: boolean;
}

export interface SourceTasksOptions {
  readonly api: ApiClient;
  readonly context: () => SourceTaskContext;
  readonly sleep?: (ms: number) => Promise<void>;
  readonly endpoint?: string;
  readonly label?: string;
  readonly activity?: ActivityService;
}

export interface SourceTaskStartResponse {
  readonly jobId?: string;
}

export interface SourceTaskResultEnvelope {
  readonly id?: string;
  readonly workbookKey?: string;
  readonly book?: string;
}

interface PendingRecord {
  id: string;
  cancelled: boolean;
  activity: ActivityHandle | null;
}

const VALID_STATUSES = new Set<string>(['queued', 'running', 'done', 'error', 'cancelled']);

function makeAbortError(): Error {
  const err = new Error('联合来源准备已取消');
  err.name = 'AbortError';
  return err;
}

export class SourceTasks {
  private readonly api: ApiClient;
  private readonly context: () => SourceTaskContext;
  private readonly sleep: (ms: number) => Promise<void>;
  private readonly endpoint: string;
  private readonly label: string;
  private readonly activity?: ActivityService;
  public readonly pending = new Set<PendingRecord>();

  constructor(options: SourceTasksOptions) {
    this.api = options.api;
    this.context = options.context;
    this.sleep = options.sleep ?? ((ms) => new Promise<void>((r) => setTimeout(r, ms)));
    this.endpoint = options.endpoint ?? '/api/composition';
    this.label = options.label ?? '来源';
    this.activity = options.activity;
  }

  post<T = unknown>(path: string, data: unknown): Promise<T> {
    return this.api<T>(path, { method: 'POST', body: JSON.stringify(data) });
  }

  clear(id: string): Promise<unknown> {
    if (!id) return Promise.resolve();
    return this.post(`${this.endpoint}/clear`, { id }).catch(() => {
      // Best-effort server token cleanup.
    });
  }

  cancel(record: PendingRecord): void {
    record.cancelled = true;
    record.activity?.end();
    if (record.id) void this.clear(record.id);
  }

  reset(): void {
    for (const record of this.pending) this.cancel(record);
  }

  async run<TResult extends SourceTaskResultEnvelope>(
    request: Record<string, unknown>,
    guard: () => boolean = () => true,
  ): Promise<TResult & { readonly id: string }> {
    const c = this.context();
    const identity = JSON.stringify([c.bookKey, c.book]);
    const record: PendingRecord = {
      id: '',
      cancelled: false,
      activity: this.activity?.begin(`正在准备${this.label}`) ?? null,
    };
    this.pending.add(record);
    const current = (): boolean => {
      const now = this.context();
      return (
        !record.cancelled &&
        guard() &&
        now.opened &&
        JSON.stringify([now.bookKey, now.book]) === identity
      );
    };
    let accepted = false;
    try {
      if (!current()) throw makeAbortError();
      const start = await this.post<SourceTaskStartResponse>(`${this.endpoint}/start`, {
        ...request,
        workbookKey: c.bookKey,
        book: c.book,
      });
      if (typeof start.jobId !== 'string' || !/^[a-zA-Z0-9_-]+$/.test(start.jobId)) {
        record.id = '';
        throw new Error(`${this.label}任务未创建，请重试`);
      }
      record.id = start.jobId;
      if (!current()) throw makeAbortError();
      for (;;) {
        const status = await this.api<AsyncJobProgress>(
          `${this.endpoint}/status?id=${encodeURIComponent(record.id)}`,
        );
        if (!current()) throw makeAbortError();
        record.activity?.job(status);
        if (
          typeof status.status !== 'string' ||
          !VALID_STATUSES.has(status.status) ||
          (status.id !== undefined && status.id !== record.id) ||
          (status.jobId !== undefined && status.jobId !== record.id)
        ) {
          throw new Error(`${this.label}任务状态无效，请重试`);
        }
        if (status.status === 'done') break;
        if (status.status === 'error' || status.status === 'cancelled') {
          throw new Error(status.error || '来源准备未完成');
        }
        await this.sleep(250);
        if (!current()) throw makeAbortError();
      }
      const result = await this.api<TResult>(
        `${this.endpoint}/result?id=${encodeURIComponent(record.id)}`,
      );
      if (!current()) throw makeAbortError();
      if (result.id !== record.id || result.workbookKey !== c.bookKey || result.book !== c.book) {
        throw new Error('来源响应已变化，请重新准备');
      }
      accepted = true;
      return { ...result, id: record.id };
    } finally {
      record.activity?.end();
      this.pending.delete(record);
      if (!accepted) await this.clear(record.id);
    }
  }
}
