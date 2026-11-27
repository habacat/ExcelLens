/**
 * Text clustering (near-duplicate candidate inspection, variant selection,
 * mapping transform creation, and CSV export) modal controller.
 */
import { decodeAsyncJobProgress } from '../core/http-decoders.generated.ts';
import type {
  AnalysisField,
  AnalysisTransform,
  ClusterDetailResponse,
  ClusterEnvelope,
  ClusterGroupsResponse,
  ClusterOptions,
  SourceScope,
} from '../contracts/analysis.ts';
import type { ApiClient, ToastFn } from '../contracts/common.ts';
import type { ClusteringLimits } from '../contracts/formats.ts';
import { AsyncJobs } from '../core/async-jobs.ts';
import { columnName } from '../core/cell-address.ts';
import { errorMessage } from '../core/display-format.ts';
import {
  requireButton,
  requireDialog,
  requireEl,
  requireInput,
  requireSelect,
} from '../core/dom.ts';
import {
  decodeClusterConfirmResponse,
  decodeClusterDetailPayload,
  decodeClusterGroupsResponse,
  decodeClusterResolveResponse,
} from '../core/http-decoders.generated.ts';

export interface ClusterContext {
  readonly bookKey: string;
  readonly book: string;
  readonly opened: boolean;
  readonly ready: boolean;
  readonly source: string;
  readonly headerRow: number;
  readonly sourceScope?: SourceScope | null;
  readonly sourceId: string;
}

/** @httpResponse ClusterDetailPayload */
export interface ClusterDetailPayload extends ClusterDetailResponse {
  readonly column?: number;
}

/** @httpResponse ClusterConfirmResponse */
export interface ClusterConfirmResponse extends ClusterEnvelope {
  readonly transform?: AnalysisTransform;
}

/** @httpResponse ClusterResolveResponse */
export interface ClusterResolveResponse extends ClusterEnvelope {
  readonly row: number;
  readonly column: number;
  readonly ref: string;
}

export interface ClusterSnapshot {
  readonly job: string;
  readonly identity: string;
  readonly context: ClusterContext;
  readonly field: AnalysisField;
}

export interface AnalysisClustersOptions {
  readonly api: ApiClient;
  readonly context: () => ClusterContext;
  readonly scopeKey?: (scope: SourceScope | null | undefined) => string;
  readonly onTransform: (step: AnalysisTransform, guard: () => boolean) => void | Promise<void>;
  readonly navigate: (
    data: ClusterResolveResponse,
    guard: () => boolean,
  ) => Promise<boolean> | boolean;
  readonly startExport: (jobId: string, protectCSV: boolean) => void;
  readonly toast: ToastFn;
  readonly limits: ClusteringLimits;
  readonly sleep?: (ms: number) => Promise<void>;
}

function makeAbortError(): Error {
  const err = new Error('疑似重复检查已取消');
  err.name = 'AbortError';
  return err;
}

function isAbortError(error: unknown): boolean {
  return error instanceof Error && error.name === 'AbortError';
}

function requireFieldSet(id: string): HTMLFieldSetElement {
  const node = requireEl(id);
  if (!(node instanceof HTMLFieldSetElement)) {
    throw new Error(`Expected HTMLFieldSetElement for #${id}`);
  }
  return node;
}

function parseSpaceOption(raw: string): ClusterOptions['space'] {
  if (raw === 'trim' || raw === 'collapse' || raw === 'remove') return raw;
  return '';
}

export function clusterIdentity(
  c: Pick<ClusterContext, 'bookKey' | 'book' | 'opened' | 'source' | 'headerRow' | 'sourceScope'>,
  scopeKey: (scope: SourceScope | null | undefined) => string,
): string {
  return JSON.stringify([c.bookKey, c.book, c.opened, c.source, c.headerRow, scopeKey(c.sourceScope)]);
}

export function validateClusterOwner(
  data: ClusterEnvelope | null | undefined,
  c: Pick<ClusterContext, 'bookKey' | 'book' | 'source' | 'headerRow' | 'sourceScope'>,
  job: string,
  field: string,
  scopeKey: (scope: SourceScope | null | undefined) => string,
): void {
  if (
    data?.id !== job ||
    data.workbookKey !== c.bookKey ||
    data.book !== c.book ||
    data.source !== c.source ||
    data.headerRow !== c.headerRow ||
    data.field !== field ||
    scopeKey(data.sourceScope) !== scopeKey(c.sourceScope)
  ) {
    throw new Error('候选响应与当前来源范围不一致');
  }
}

export function validateClusterPage(
  data: {
    readonly offset: number;
    readonly next: number;
    readonly total: number;
    readonly items?: readonly unknown[];
    readonly variants?: readonly unknown[];
  } | null | undefined,
  offset: number,
  limit: number,
  key: 'items' | 'variants' = 'items',
): void {
  const items = data?.[key];
  if (
    !data ||
    !Number.isSafeInteger(offset) ||
    offset < 0 ||
    data.offset !== offset ||
    !Number.isSafeInteger(data.total) ||
    data.total < offset ||
    !items ||
    items.length > limit ||
    data.next !== offset + items.length ||
    data.next > data.total ||
    (data.next < data.total && items.length !== limit)
  ) {
    throw new Error('候选分页响应无效');
  }
}

export const ClusterModel = Object.freeze({
  identity: clusterIdentity,
  owner: validateClusterOwner,
  page: validateClusterPage,
});

export class AnalysisClusters {
  private readonly api: ApiClient;
  private readonly context: () => ClusterContext;
  private readonly scopeKey: (scope: SourceScope | null | undefined) => string;
  private readonly onTransform: (step: AnalysisTransform, guard: () => boolean) => void | Promise<void>;
  private readonly navigate: (
    data: ClusterResolveResponse,
    guard: () => boolean,
  ) => Promise<boolean> | boolean;
  private readonly startExport: (jobId: string, protectCSV: boolean) => void;
  private readonly toast: ToastFn;
  private readonly limits: ClusteringLimits;
  private readonly jobs: AsyncJobs;

  public readonly dialog: HTMLDialogElement;
  private serial = 0;
  private pageSerial = 0;
  private detailSerial = 0;
  private navSerial = 0;
  public snapshot: ClusterSnapshot | null = null;
  public page: ClusterGroupsResponse | null = null;
  public detail: ClusterDetailPayload | null = null;
  public choice: string | null = null;
  private readonly confirmed = new Map<number, string>();
  public opened = false;
  public busy = false;
  public loading = false;
  private detailLoading = false;
  private confirming = false;
  private field: AnalysisField | null = null;
  private pageController: AbortController | null = null;
  private detailController: AbortController | null = null;

  constructor(options: AnalysisClustersOptions) {
    this.api = options.api;
    this.context = options.context;
    this.scopeKey = options.scopeKey ?? ((scope) => JSON.stringify(scope ?? null));
    this.onTransform = options.onTransform;
    this.navigate = options.navigate;
    this.startExport = options.startExport;
    this.toast = options.toast;
    this.limits = options.limits;
    const sleep = options.sleep ?? ((ms) => new Promise<void>((resolve) => setTimeout(resolve, ms)));

    this.dialog = requireDialog('analysisClustersDialog');
    this.jobs = new AsyncJobs({
      decodeStatus: decodeAsyncJobProgress,
      api: this.api,
      identity: () => this.identity(),
      requestIdentity: () => this.context().bookKey,
      sleep,
      endpoint: '/api/analysis/clusters',
      label: '疑似重复检查',
      resultFromStatus: (s) => s,
      onBusy: (busy) => {
        this.busy = busy;
        this.controls();
      },
      onProgress: (s) => this.status(s.stage || '正在检查疑似重复…'),
    });

    requireButton('analysisClustersClose').addEventListener('click', () => this.close());
    requireButton('analysisClustersRun').addEventListener('click', () => {
      void this.run();
    });
    requireButton('analysisClustersCancel').addEventListener('click', () => {
      this.serial++;
      this.loading = false;
      this.jobs.cancelMain();
      this.controls();
      this.status('检查已取消；上次成功结果保留');
    });
    requireButton('analysisClustersPrev').addEventListener('click', () => {
      if (!this.page) return;
      void this.loadPage(Math.max(0, this.page.offset - this.limits.pageGroups));
    });
    requireButton('analysisClustersNext').addEventListener('click', () => {
      if (!this.page) return;
      void this.loadPage(this.page.next);
    });
    requireButton('analysisClusterVariantsPrev').addEventListener('click', () => {
      if (!this.detail) return;
      void this.select(
        this.detail.group,
        Math.max(0, this.detail.offset - this.limits.pageVariants),
        true,
      );
    });
    requireButton('analysisClusterVariantsNext').addEventListener('click', () => {
      if (!this.detail) return;
      void this.select(this.detail.group, this.detail.next, true);
    });
    requireButton('analysisClustersConfirm').addEventListener('click', () => {
      void this.confirm();
    });
    requireButton('analysisClustersExport').addEventListener('click', () => this.export());
    this.dialog.addEventListener('cancel', (event: Event) => {
      event.preventDefault();
      this.close();
    });
    this.dialog.addEventListener('close', () => {
      if (!this.dialog.open && this.opened) this.close();
    });
    this.controls();
  }

  identity(): string {
    return clusterIdentity(this.context(), this.scopeKey);
  }

  status(text: string, error = false): void {
    const el = requireEl('analysisClustersStatus');
    el.textContent = text;
    el.classList.toggle('is-error', error);
  }

  current(snapshot: ClusterSnapshot | null = this.snapshot): snapshot is ClusterSnapshot {
    return (
      this.opened &&
      snapshot !== null &&
      snapshot === this.snapshot &&
      snapshot.identity === this.identity()
    );
  }

  release(id: string | undefined): void {
    if (!id) return;
    void this.api('/api/analysis/clusters/clear', {
      method: 'POST',
      body: JSON.stringify({ id }),
    }).catch(() => {
      // Best-effort clear.
    });
  }

  reset(): void {
    this.close();
    this.release(this.snapshot?.job);
    this.snapshot = null;
    this.page = null;
    this.detail = null;
    this.choice = null;
    this.confirmed.clear();
    this.renderList();
    this.renderDetail();
    this.renderSummary();
    this.controls();
  }

  close(): void {
    this.opened = false;
    this.loading = false;
    this.serial++;
    this.pageSerial++;
    this.detailSerial++;
    this.jobs.reset();
    this.pageController?.abort();
    this.detailController?.abort();
    if (this.dialog.open) this.dialog.close();
  }

  open(field: AnalysisField): void {
    const context = this.context();
    if (!context.opened || !context.ready) return;
    if (
      this.snapshot &&
      (this.snapshot.identity !== this.identity() || this.snapshot.field.id !== field.id)
    ) {
      this.reset();
    }
    this.field = { ...field };
    this.opened = true;
    this.serial++;
    requireEl('analysisClustersTitle').textContent = `疑似重复 · ${field.name}`;
    this.status(
      this.snapshot
        ? '保留上次成功结果；可调整规则重新检查'
        : '选择比较规则后点击“检查”。',
    );
    this.controls();
    if (!this.dialog.open) this.dialog.showModal();
  }

  controls(): void {
    const busy = this.busy || this.loading;
    requireFieldSet('analysisClustersOptions').disabled = busy;
    requireButton('analysisClustersRun').disabled = busy || this.confirming;
    requireButton('analysisClustersCancel').hidden = !busy;
    requireButton('analysisClustersPrev').disabled = busy || !this.page || this.page.offset === 0;
    requireButton('analysisClustersNext').disabled =
      busy || !this.page || this.page.next >= this.page.total;
    requireButton('analysisClusterVariantsPrev').disabled =
      busy || !this.detail || this.detail.offset === 0;
    requireButton('analysisClusterVariantsNext').disabled =
      busy || !this.detail || this.detail.next >= this.detail.total;
    requireButton('analysisClustersConfirm').disabled =
      busy ||
      this.confirming ||
      this.detailLoading ||
      !this.current() ||
      this.detail?.kind !== 'similar' ||
      this.choice === null ||
      this.confirmed.get(this.detail.group) === this.choice;
    requireButton('analysisClustersExport').disabled = busy || !this.current();
  }

  options(): ClusterOptions {
    return {
      foldCase: requireInput('analysisClustersCase').checked,
      foldWidth: requireInput('analysisClustersWidth').checked,
      space: parseSpaceOption(requireSelect('analysisClustersSpace').value),
      ignorePunctuation: requireInput('analysisClustersPunctuation').checked,
    };
  }

  async run(): Promise<void> {
    if (!this.opened || this.busy || this.loading || !this.field) return;
    const c: ClusterContext = { ...this.context() };
    const field: AnalysisField = { ...this.field };
    const identity = this.identity();
    const ticket = ++this.serial;
    if (c.sourceScope && !c.sourceId) {
      this.status('来源范围已释放，请重新读取分析字段', true);
      return;
    }
    this.pageController?.abort();
    this.detailController?.abort();
    this.pageSerial++;
    this.detailSerial++;
    this.loading = true;
    this.controls();
    let candidate = '';
    let adopted = false;
    const isOpened = (): boolean => this.opened;
    try {
      const completed = await this.jobs.execute(
        {
          workbookKey: c.bookKey,
          book: c.book,
          source: c.source,
          sourceId: c.sourceId || '',
          headerRow: c.headerRow,
          field: field.id,
          options: this.options(),
        },
        { main: true },
      );
      candidate = completed.job;
      if (!isOpened() || ticket !== this.serial || identity !== this.identity()) {
        throw makeAbortError();
      }
      const page = await this.api(
        `/api/analysis/clusters/groups?${new URLSearchParams({ id: candidate, offset: '0' }).toString()}`,
        undefined,
        decodeClusterGroupsResponse,
      );
      if (!isOpened() || ticket !== this.serial || identity !== this.identity()) {
        throw makeAbortError();
      }
      validateClusterOwner(page, c, candidate, field.id, this.scopeKey);
      validateClusterPage(page, 0, this.limits.pageGroups);
      const previous = this.snapshot?.job;
      this.snapshot = { job: candidate, identity, context: c, field };
      this.page = page;
      this.detail = null;
      this.choice = null;
      this.confirmed.clear();
      adopted = true;
      this.loading = false;
      this.renderList();
      this.renderDetail();
      this.renderSummary();
      this.controls();
      if (previous && previous !== candidate) this.release(previous);
      this.status('已检查完整分析范围。疑似相同仅供核对，需逐组选择代表原文。');
      const firstItem = page.items[0];
      if (firstItem) await this.select(firstItem.id);
    } catch (error: unknown) {
      if (
        isOpened() &&
        ticket === this.serial &&
        identity === this.identity() &&
        !isAbortError(error)
      ) {
        this.status(`${errorMessage(error)}；上次成功结果保留`, true);
      }
    } finally {
      if (candidate && !adopted) this.release(candidate);
      if (ticket === this.serial) this.loading = false;
      this.controls();
    }
  }

  async loadPage(offset: number): Promise<void> {
    const snapshot = this.snapshot;
    if (!this.current(snapshot) || this.busy) return;
    const ticket = ++this.pageSerial;
    const controller = new AbortController();
    this.pageController?.abort();
    this.pageController = controller;
    try {
      const data = await this.api(
        `/api/analysis/clusters/groups?${new URLSearchParams({ id: snapshot.job, offset: String(offset) }).toString()}`,
        { signal: controller.signal },
        decodeClusterGroupsResponse,
      );
      if (!this.current(snapshot) || ticket !== this.pageSerial || controller.signal.aborted) return;
      validateClusterOwner(data, snapshot.context, snapshot.job, snapshot.field.id, this.scopeKey);
      validateClusterPage(data, offset, this.limits.pageGroups);
      this.page = data;
      this.detail = null;
      this.choice = null;
      this.renderList();
      this.renderDetail();
      this.controls();
      const firstItem = data.items[0];
      if (firstItem) await this.select(firstItem.id);
    } catch (error: unknown) {
      if (this.current(snapshot) && ticket === this.pageSerial && !isAbortError(error)) {
        this.status(errorMessage(error), true);
      }
    }
  }

  async select(group: number, offset = 0, keepChoice = false): Promise<void> {
    const snapshot = this.snapshot;
    if (!this.current(snapshot) || this.busy) return;
    const ticket = ++this.detailSerial;
    const controller = new AbortController();
    this.detailController?.abort();
    this.detailController = controller;
    if (!keepChoice) this.choice = null;
    this.detailLoading = true;
    this.renderDetail();
    this.controls();
    try {
      const data = await this.api(
        `/api/analysis/clusters/detail?${new URLSearchParams({
          id: snapshot.job,
          group: String(group),
          offset: String(offset),
        }).toString()}`,
        { signal: controller.signal },
        decodeClusterDetailPayload,
      );
      if (!this.current(snapshot) || ticket !== this.detailSerial || controller.signal.aborted) {
        return;
      }
      validateClusterOwner(data, snapshot.context, snapshot.job, snapshot.field.id, this.scopeKey);
      validateClusterPage(data, offset, this.limits.pageVariants, 'variants');
      const kind: string = data.kind;
      if (
        data.group !== group ||
        (kind !== 'exact' && kind !== 'similar') ||
        data.variants.some(
          (v) =>
            typeof v.raw !== 'string' ||
            !Number.isSafeInteger(v.count) ||
            v.count < 1 ||
            !Array.isArray(v.rows),
        )
      ) {
        throw new Error('候选原文响应无效');
      }
      this.detail = data;
      this.renderList();
      this.renderDetail();
      this.controls();
    } catch (error: unknown) {
      if (this.current(snapshot) && ticket === this.detailSerial && !isAbortError(error)) {
        this.status(errorMessage(error), true);
      }
    } finally {
      if (ticket === this.detailSerial) {
        this.detailLoading = false;
        this.renderDetail();
        this.controls();
      }
    }
  }

  renderSummary(): void {
    const counts = this.page?.counts;
    requireEl('analysisClustersSummary').textContent = counts
      ? `范围 ${counts.scanned.toLocaleString()} 行；参与 ${counts.eligible.toLocaleString()} 项，疑似重复 ${counts.similarGroups.toLocaleString()} 组，原文相同 ${counts.exactGroups.toLocaleString()} 组。排除：空值 ${counts.blank}、数值 ${counts.numeric}、其他类型 ${counts.otherTypes}、缺缓存 ${counts.missingCache}、空比较键 ${counts.emptyKey}。`
      : '';
  }

  renderList(): void {
    const host = requireEl('analysisClustersGroups');
    host.replaceChildren();
    for (const item of this.page?.items ?? []) {
      const button = document.createElement('button');
      button.type = 'button';
      button.className = 'analysis-cluster-group';
      button.dataset.group = String(item.id);
      button.setAttribute('aria-pressed', String(this.detail?.group === item.id));
      const title = document.createElement('strong');
      title.textContent = item.key;
      title.title = item.key;
      const meta = document.createElement('span');
      meta.textContent = `${item.kind === 'similar' ? '疑似重复' : '原文相同'} · ${item.variants} 项原文与类型 · ${item.count.toLocaleString()} 次`;
      button.append(title, meta);
      button.addEventListener('click', () => {
        void this.select(item.id);
      });
      host.append(button);
    }
    if (this.page && !this.page.items.length) {
      const note = document.createElement('p');
      note.textContent = '此范围和规则下没有重复候选。';
      host.append(note);
    }
    requireEl('analysisClustersPage').textContent = this.page
      ? `${this.page.total ? this.page.offset + 1 : 0}–${this.page.next} / ${this.page.total}`
      : '尚未检查';
  }

  renderDetail(): void {
    const detail = this.detail;
    const host = requireEl('analysisClusterVariants');
    host.replaceChildren();
    requireEl('analysisClusterDetailTitle').textContent = detail
      ? detail.kind === 'similar'
        ? '选择代表原文'
        : '原文相同，无需映射'
      : '选择候选组查看原文';
    requireEl('analysisClusterDetailKey').textContent = detail?.key ?? '';
    if (detail) {
      for (const [i, value] of detail.variants.entries()) {
        const item = document.createElement('div');
        item.className = 'analysis-cluster-variant';
        const label = document.createElement('label');
        const radio = document.createElement('input');
        radio.type = 'radio';
        radio.name = 'analysisClusterRepresentative';
        radio.disabled = this.detailLoading || detail.kind !== 'similar';
        radio.checked = this.choice === value.raw;
        radio.addEventListener('change', () => {
          if (this.detail !== detail || this.detailLoading) return;
          this.choice = value.raw;
          this.renderDetail();
          this.controls();
        });
        const raw = document.createElement('pre');
        raw.textContent = value.raw;
        label.append(radio, raw);
        item.append(label);

        const meta = document.createElement('div');
        meta.className = 'analysis-cluster-variant-meta';
        const count = document.createElement('span');
        count.textContent =
          `文本 · ${value.count.toLocaleString()} 次` +
          (value.formulaCount ? ` · 其中公式缓存 ${value.formulaCount} 次` : '');
        count.title = `数据类型：${value.type}`;
        meta.append(count);
        const colIdx = detail.column || Number(this.snapshot?.field.id.slice(1));
        for (const [sample, row] of value.rows.entries()) {
          const link = document.createElement('button');
          link.type = 'button';
          link.className = 'analysis-drill-link';
          link.textContent = `${columnName(colIdx)}${row}`;
          link.addEventListener('click', () => {
            if (this.detail === detail) void this.locate(detail.offset + i, sample);
          });
          meta.append(link);
        }
        item.append(meta);
        host.append(item);
      }
    }
    requireEl('analysisClusterVariantsPage').textContent = detail
      ? `${detail.offset + 1}–${detail.next} / ${detail.total} 项原文与类型`
      : '原文按读取类型分别列出；位置为有限样例，次数覆盖完整范围';
  }

  async confirm(): Promise<void> {
    const snapshot = this.snapshot;
    const detail = this.detail;
    const choice = this.choice;
    const ticket = this.serial;
    if (
      !this.current(snapshot) ||
      this.busy ||
      this.loading ||
      this.detailLoading ||
      this.confirming ||
      choice === null ||
      detail?.kind !== 'similar' ||
      this.confirmed.get(detail.group) === choice
    ) {
      return;
    }
    this.confirming = true;
    this.controls();
    try {
      const data = await this.api('/api/analysis/clusters/confirm', {
        method: 'POST',
        body: JSON.stringify({
          id: snapshot.job,
          workbookKey: snapshot.context.bookKey,
          book: snapshot.context.book,
          choices: [{ group: detail.group, representative: choice }],
        }),
      }, decodeClusterConfirmResponse);
      if (
        !this.current(snapshot) ||
        ticket !== this.serial ||
        this.detail !== detail ||
        this.choice !== choice
      ) {
        return;
      }
      validateClusterOwner(data, snapshot.context, snapshot.job, snapshot.field.id, this.scopeKey);
      if (
        data.transform?.op !== 'map_text' ||
        data.transform.source !== snapshot.field.id ||
        !data.transform.mapping?.length
      ) {
        throw new Error('文本映射响应无效');
      }
      await this.onTransform(data.transform, () => this.current(snapshot) && ticket === this.serial);
      this.confirmed.set(detail.group, choice);
      this.status('已添加独立文本映射列；将新列用于分类并运行分析后生效。');
      this.toast('已添加文本核对列，运行分析后生效', 'success');
    } catch (error: unknown) {
      if (this.current(snapshot) && ticket === this.serial) {
        this.status(errorMessage(error), true);
      }
    } finally {
      this.confirming = false;
      this.controls();
    }
  }

  async locate(variant: number, sample: number): Promise<void> {
    const snapshot = this.snapshot;
    const detail = this.detail;
    const ticket = ++this.navSerial;
    if (!this.current(snapshot) || !detail) return;
    const serial = this.serial;
    try {
      const data = await this.api(
        `/api/analysis/clusters/resolve?${new URLSearchParams({
          id: snapshot.job,
          group: String(detail.group),
          variant: String(variant),
          sample: String(sample),
          workbookKey: snapshot.context.bookKey,
          book: snapshot.context.book,
        }).toString()}`,
        undefined,
        decodeClusterResolveResponse,
      );
      if (!this.current(snapshot) || serial !== this.serial || ticket !== this.navSerial) return;
      validateClusterOwner(data, snapshot.context, snapshot.job, snapshot.field.id, this.scopeKey);
      this.close();
      await this.navigate(
        data,
        () =>
          snapshot.context.bookKey === this.context().bookKey &&
          snapshot.context.book === this.context().book &&
          ticket === this.navSerial,
      );
    } catch (error: unknown) {
      if (this.current(snapshot) && serial === this.serial) {
        this.status(errorMessage(error), true);
      }
    }
  }

  export(): void {
    const snapshot = this.snapshot;
    if (!this.current(snapshot) || this.busy) return;
    const protect = requireInput('analysisClustersCSVGuard').checked;
    this.close();
    this.startExport(snapshot.job, protect);
  }
}
