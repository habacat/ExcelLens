/**
 * Foreground & speculative prefetch viewport tile scheduler with cancellation
 * and slot reservation for sudden thumb jumps.
 */

export interface SchedulerItem {
  readonly key: string;
}

export interface SchedulerStats {
  started: number;
  cancelled: number;
  completed: number;
  peak: number;
}

type ActiveSchedulerJob<TItem extends SchedulerItem, TResult> = TItem & {
  foreground: boolean;
  controller?: AbortController;
  readonly promise: Promise<TResult | undefined>;
  readonly resolve: (value?: TResult) => void;
  readonly reject: (reason?: unknown) => void;
};

export class ViewportScheduler<TItem extends SchedulerItem, TResult> {
  private readonly read: (item: TItem, signal: AbortSignal) => Promise<TResult>;
  private readonly limit: number;
  public readonly active = new Map<string, ActiveSchedulerJob<TItem, TResult>>();
  public wanted = new Map<string, ActiveSchedulerJob<TItem, TResult>>();
  public readonly stats: SchedulerStats = { started: 0, cancelled: 0, completed: 0, peak: 0 };

  constructor(read: (item: TItem, signal: AbortSignal) => Promise<TResult>, limit = 3) {
    this.read = read;
    this.limit = limit;
  }

  reset(): void {
    for (const job of this.active.values()) this.cancel(job);
    for (const job of this.wanted.values()) job.resolve(undefined);
    this.wanted.clear();
  }

  private cancel(job: ActiveSchedulerJob<TItem, TResult>): void {
    if (job.controller && !job.controller.signal.aborted) {
      this.stats.cancelled++;
      job.controller.abort();
    }
  }

  plan(current: readonly TItem[], predicted: readonly TItem[]): Promise<(TResult | undefined)[]> {
    const wanted = new Map<string, ActiveSchedulerJob<TItem, TResult>>();
    const required: Promise<TResult | undefined>[] = [];
    const add = (item: TItem, foreground: boolean): void => {
      if (wanted.has(item.key)) return;
      let job = this.active.get(item.key);
      if (job?.controller?.signal.aborted) job = undefined;
      job ??= this.wanted.get(item.key);
      if (!job || job.controller?.signal.aborted) {
        let resolveFn: (value?: TResult) => void = () => {
          // initialized synchronously by Promise executor
        };
        let rejectFn: (reason?: unknown) => void = () => {
          // initialized synchronously by Promise executor
        };
        const promise = new Promise<TResult | undefined>((ok, fail) => {
          resolveFn = ok;
          rejectFn = fail;
        });
        job = {
          ...item,
          foreground,
          promise,
          resolve: resolveFn,
          reject: rejectFn,
        };
        promise.catch(() => {
          // Speculative errors are retried if the tile later becomes current.
        });
      }
      job.foreground = foreground;
      wanted.set(item.key, job);
      if (foreground) required.push(job.promise);
    };

    for (const item of current) add(item, true);
    for (const item of predicted) add(item, false);
    for (const old of this.wanted.values()) {
      if (!wanted.has(old.key) && !old.controller) old.resolve(undefined);
    }
    this.wanted = wanted;
    this.pump();
    return Promise.all(required);
  }

  private pump(): void {
    for (const job of this.wanted.values()) {
      if (job.controller || this.active.has(job.key)) continue;
      if (this.active.size >= this.limit) {
        if (
          job.foreground &&
          ![...this.active.values()].some((j) => j.controller?.signal.aborted)
        ) {
          const obsolete = [...this.active.values()].find((j) => !this.wanted.has(j.key));
          if (obsolete) this.cancel(obsolete);
        }
        break;
      }
      if (!job.foreground && this.active.size >= this.limit - 1) break;
      const controller = new AbortController();
      job.controller = controller;
      this.active.set(job.key, job);
      this.stats.started++;
      this.stats.peak = Math.max(this.stats.peak, this.active.size);
      Promise.resolve()
        .then(() => this.read(job, controller.signal))
        .then(
          (result) => {
            if (!controller.signal.aborted) this.stats.completed++;
            job.resolve(result);
          },
          (err: unknown) => {
            if (controller.signal.aborted) job.resolve(undefined);
            else job.reject(err);
          },
        )
        .finally(() => {
          this.active.delete(job.key);
          if (this.wanted.get(job.key) === job) this.wanted.delete(job.key);
          this.pump();
        });
    }
  }
}
