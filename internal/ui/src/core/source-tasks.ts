/**
 * Asynchronous source preparation task runner shared by composition and relation
 * workflows. Each preparation owns its own runtime token, including late starts.
 */
import type { ActivityHandle, ActivityService, ApiClient } from '../contracts/common.ts';
import type { AsyncJobProgress } from '../contracts/common.ts';
import { decodeSourceTaskProgress, decodeSourceTaskStartResponse } from './http-decoders.generated.ts';

export interface SourceTaskContext {
  readonly bookKey: string;
  readonly book: string;
  readonly opened: boolean;
}

/** @httpResponse SourceTaskProgress */
export interface SourceTaskProgress extends Omit<AsyncJobProgress, 'done' | 'total'> {
  readonly done?: string | number;
  readonly total?: string | number;
}

function progressCount(value: number | string | undefined): number | undefined {
  if (value === undefined) return undefined;
  if (typeof value === 'string' && !/^(0|[1-9]\d*)$/.test(value)) throw new Error('来源任务的进度计数无效');
  const count = Number(value);
  if (count < 0) throw new Error('来源任务的进度计数无效');
  return Number.isSafeInteger(count) ? count : undefined;
}

export interface SourceTasksOptions {
  readonly api: ApiClient;
  readonly context: () => SourceTaskContext;
  readonly sleep?: (ms: number) => Promise<void>;
  readonly endpoint?: string;
  readonly label?: string;
  readonly activity?: ActivityService;
}

/** @httpResponse SourceTaskStartResponse */
export interface SourceTaskStartResponse {
  readonly jobId?: string;
}

/** @httpResponse SourceTaskResultEnvelope */
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

  post(path: string, data: unknown): Promise<unknown> {
    return this.api(path, { method: 'POST', body: JSON.stringify(data) });
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
    decode: (value: unknown) => TResult,
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
      const start = await this.post(`${this.endpoint}/start`, {
        ...request,
        workbookKey: c.bookKey,
        book: c.book,
      }).then(decodeSourceTaskStartResponse);
      if (typeof start.jobId !== 'string' || !/^[a-zA-Z0-9_-]+$/.test(start.jobId)) {
        record.id = '';
        throw new Error(`${this.label}任务未创建，请重试`);
      }
      record.id = start.jobId;
      if (!current()) throw makeAbortError();
      for (;;) {
        const status = await this.api(
          `${this.endpoint}/status?id=${encodeURIComponent(record.id)}`,
          undefined,
          decodeSourceTaskProgress,
        );
        if (!current()) throw makeAbortError();
        record.activity?.job({ ...status, done: progressCount(status.done), total: progressCount(status.total) });
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
      const result = await this.api(
        `${this.endpoint}/result?id=${encodeURIComponent(record.id)}`,
        undefined, decode,
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
