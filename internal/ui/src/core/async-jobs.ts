/**
 * Generic async server job runner owning start, polling, and cancellation
 * (including job IDs arriving after cancellation). No cancelled task may
 * publish progress or results.
 */
import type {
  ActivityService,
  ApiClient,
  AsyncJobProgress,
  AsyncJobStatusKind,
} from '../contracts/common.ts';
import { decodeAsyncJobStartResponse } from './http-decoders.generated.ts';

/** @httpResponse AsyncJobStartResponse */
export interface AsyncJobStartResponse {
  readonly jobId?: string;
}

export interface AsyncJobStatusEnvelope<TResult = unknown> extends AsyncJobProgress {
  readonly id?: string;
  readonly status?: AsyncJobStatusKind;
  readonly result?: TResult;
  readonly error?: string;
}

export interface AsyncJobCompleted<TResult, TStatus> {
  readonly result: TResult;
  readonly job: string;
  readonly status: TStatus;
  readonly current: () => boolean;
}

export interface AsyncJobsOptions<TStatus extends AsyncJobStatusEnvelope<TResult>, TResult> {
  readonly api: ApiClient;
  readonly identity: () => string;
  readonly decodeStatus: (value: unknown) => TStatus;
  readonly cancelEndpoint?: string;
  readonly requestIdentity?: () => string;
  readonly sleep: (ms: number) => Promise<void>;
  readonly onBusy?: (busy: boolean) => void;
  readonly onProgress?: (status: TStatus) => void;
  readonly endpoint?: string;
  readonly label?: string;
  readonly slot?: string | null;
  readonly maxPolls?: number;
  readonly activity?: ActivityService;
  readonly resultFromStatus: (status: TStatus) => TResult;
}

interface ActiveTask {
  readonly main: boolean;
  readonly serial: number;
  readonly book: string;
  readonly requestBook: string;
  id: string;
  cancelled: boolean;
  cancelSent: boolean;
  finished: boolean;
  readonly controller: AbortController;
}

const VALID_STATUSES = new Set<string>(['queued', 'running', 'done', 'error', 'cancelled']);

function makeAbortError(label: string): Error {
  const err = new Error(`${label}已取消`);
  err.name = 'AbortError';
  return err;
}

export class AsyncJobs<
  TResult = unknown,
  TStatus extends AsyncJobStatusEnvelope<TResult> = AsyncJobStatusEnvelope<TResult>,
> {
  private readonly api: ApiClient;
  private readonly identity: () => string;
  private readonly decodeStatus: (value: unknown) => TStatus;
  private readonly cancelEndpoint: string;
  private readonly requestIdentity: () => string;
  private readonly sleep: (ms: number) => Promise<void>;
  private readonly onBusy: (busy: boolean) => void;
  private readonly onProgress: (status: TStatus) => void;
  private readonly endpoint: string;
  private readonly label: string;
  private readonly slot: string | null;
  private readonly maxPolls: number;
  private readonly activity?: ActivityService;
  private readonly resultFromStatus: (status: TStatus) => TResult;

  public serial = 0;
  public main: ActiveTask | null = null;
  public readonly tasks = new Set<ActiveTask>();

  constructor(options: AsyncJobsOptions<TStatus, TResult>) {
    this.api = options.api;
    this.decodeStatus = options.decodeStatus;
    this.identity = options.identity;
    this.requestIdentity = options.requestIdentity ?? options.identity;
    this.sleep = options.sleep;
    this.onBusy =
      options.onBusy ??
      (() => {
        // default no-op
      });
    this.onProgress =
      options.onProgress ??
      (() => {
        // default no-op
      });
    this.endpoint = options.endpoint ?? '/api/analysis/query';
    this.cancelEndpoint = options.cancelEndpoint ?? `${this.endpoint}/cancel`;
    this.label = options.label ?? '分析';
    this.slot = options.slot ?? null;
    this.maxPolls = options.maxPolls ?? 1800;
    this.activity = options.activity;
    this.resultFromStatus = options.resultFromStatus;
  }

  current(task: ActiveTask): boolean {
    return (
      !task.cancelled &&
      task.book === this.identity() &&
      (!task.main || task.serial === this.serial)
    );
  }

  cancelServer(task: ActiveTask): void {
    if (!task.id || task.cancelSent || task.finished) return;
    task.cancelSent = true;
    this.api(this.cancelEndpoint, {
      method: 'POST',
      body: JSON.stringify({ id: task.id }),
    }).catch(() => {
      // Best-effort cancellation on the server.
    });
  }

  cancelTask(task: ActiveTask): void {
    task.cancelled = true;
    task.controller.abort();
    this.cancelServer(task);
  }

  cancelMain(): boolean {
    ++this.serial;
    if (!this.main) return false;
    const task = this.main;
    this.main = null;
    this.cancelTask(task);
    this.onBusy(false);
    return true;
  }

  reset(): void {
    this.cancelMain();
    for (const task of this.tasks) this.cancelTask(task);
  }

  async execute(
    query: unknown,
    options: { readonly main?: boolean } = {},
  ): Promise<AsyncJobCompleted<TResult, TStatus>> {
    const isMain = options.main ?? false;
    if (isMain) this.cancelMain();
    const task: ActiveTask = {
      main: isMain,
      serial: this.serial,
      book: this.identity(),
      requestBook: this.requestIdentity(),
      id: '',
      cancelled: false,
      cancelSent: false,
      finished: false,
      controller: new AbortController(),
    };
    const activityHandle = this.activity?.begin(`正在处理${this.label}`, {
      signal: task.controller.signal,
      slot: isMain ? this.slot : null,
    });
    this.tasks.add(task);
    if (isMain) {
      this.main = task;
      this.onBusy(true);
    }
    try {
      const started = await this.api(`${this.endpoint}/start`, {
        method: 'POST',
        headers: { 'X-ExcelLens-Workbook': task.requestBook },
        body: JSON.stringify(query),
      }, decodeAsyncJobStartResponse);
      if (typeof started.jobId !== 'string' || !/^[a-zA-Z0-9_-]+$/.test(started.jobId)) {
        throw new Error(`${this.label}任务标识无效，请重试`);
      }
      task.id = started.jobId;
      if (!this.current(task)) throw makeAbortError(this.label);
      for (let tick = 0; this.maxPolls === 0 || tick < this.maxPolls; tick++) {
        await this.sleep(tick < 12 ? 240 : 420);
        if (!this.current(task)) throw makeAbortError(this.label);
        const status = await this.api(
          `${this.endpoint}/status?id=${encodeURIComponent(task.id)}`,
          { signal: task.controller.signal },
          this.decodeStatus,
        );
        if (!this.current(task)) throw makeAbortError(this.label);
        if (
          typeof status.status !== 'string' ||
          !VALID_STATUSES.has(status.status) ||
          (status.id !== undefined && status.id !== task.id)
        ) {
          throw new Error(`${this.label}任务状态响应无效，请重试`);
        }
        activityHandle?.job(status);
        if (isMain) this.onProgress(status);
        if (status.status === 'done') {
          task.finished = true;
          return {
            result: this.resultFromStatus(status),
            job: task.id,
            status,
            current: () => this.current(task),
          };
        }
        if (status.status === 'error' || status.status === 'cancelled') {
          throw new Error(status.error || `${this.label}已取消`);
        }
      }
      throw new Error(`${this.label}等待超时，请缩小范围后重试`);
    } catch (error: unknown) {
      if (!this.current(task)) throw makeAbortError(this.label);
      throw error;
    } finally {
      activityHandle?.end();
      this.tasks.delete(task);
      if (!task.finished) this.cancelServer(task);
      if (this.main === task) {
        this.main = null;
        this.onBusy(false);
      }
    }
  }
}
