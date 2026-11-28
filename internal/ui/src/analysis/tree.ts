/**
 * Lazy branches own only bounded responses. Runtime job IDs never enter a
 * saved query, and a cancelled late start is cleared by its own job owner.
 */
import { interfaceLocale, t as localize } from '../core/i18n.ts';
import { decodeAnalysisTreeStatusResponse } from '../core/http-decoders.generated.ts';
import type {
  AnalysisCell,
  AnalysisMetric,
  AnalysisResult,
  AnalysisResultRow,
  AnalysisTreeStatusResponse,
} from '../contracts/analysis.ts';
import type { ApiClient } from '../contracts/common.ts';
import type { AnalysisTreeLimits } from '../contracts/formats.ts';
import { AsyncJobs } from '../core/async-jobs.ts';
import { errorMessage } from '../core/display-format.ts';
import type { SourceTaskContext } from '../core/source-tasks.ts';
import { comparisonTitle } from './comparisons.ts';
import { retainedBytes, type WorkspaceAnalysisQuery } from './results.ts';

export interface AnalysisTreeRoot {
  readonly bookKey: string;
  readonly book: string;
  readonly job: string;
  readonly query: WorkspaceAnalysisQuery;
  readonly result: AnalysisResult;
}

export type AnalysisTreeNodePhase = 'idle' | 'queued' | 'running' | 'done' | 'error';

export interface AnalysisTreeNode {
  readonly key: string;
  readonly path: readonly string[];
  readonly children: Map<string, AnalysisTreeNode>;
  expanded: boolean;
  result: AnalysisResult | null;
  pending: boolean;
  running: boolean;
  version: number;
  phase: AnalysisTreeNodePhase;
  error: string;
  progress: number;
  total: number;
  bytes: number;
  readonly baseBytes: number;
  used: number;
  ticket: number;
  jobs: AsyncJobs<AnalysisTreeStatusResponse | AnalysisResult | null, AnalysisTreeStatusResponse> | null;
}

export type AnalysisTreeRowItem =
  | {
      readonly kind: 'row';
      readonly row: AnalysisResultRow;
      readonly result: AnalysisResult;
      readonly node: AnalysisTreeNode | null;
    }
  | {
      readonly kind: 'truncated' | 'status';
      readonly node: AnalysisTreeNode;
    };

export interface AnalysisTreePage {
  readonly rows: readonly AnalysisTreeRowItem[];
  readonly more: boolean;
  readonly first: number;
  readonly last: number;
}

export interface AnalysisTreeChangeEvent {
  readonly type: 'change' | 'progress';
  readonly node: AnalysisTreeNode | null;
}

export interface AnalysisTreeDiagnostics {
  readonly nodes: number;
  readonly bytes: number;
  readonly active: number;
  readonly queued: number;
  readonly page: number;
}

export interface AnalysisTreeOptions {
  readonly api: ApiClient;
  readonly context: () => SourceTaskContext;
  readonly limits: AnalysisTreeLimits;
  readonly onChange?: (event: AnalysisTreeChangeEvent) => void;
  readonly onMessage?: (message: string) => void;
  readonly sleep?: (ms: number) => Promise<void>;
}

const pathKey = (path: readonly string[]): string => JSON.stringify(path);

function arraysEqual(a: readonly string[], b: readonly string[]): boolean {
  return a.length === b.length && a.every((v, i) => v === b[i]);
}

function isPrefix(a: readonly string[], b: readonly string[]): boolean {
  return a.length <= b.length && a.every((v, i) => v === b[i]);
}

function displayLabel(value: string): string {
  return value === '' ? localize('ui.common.empty') : value;
}

function formatCount(n: number): string {
  return n.toLocaleString(interfaceLocale());
}

function isAbortError(error: unknown): boolean {
  return error instanceof Error && error.name === 'AbortError';
}

function isTreeStatusResponse(
  value: AnalysisTreeStatusResponse | AnalysisResult | null,
): value is AnalysisTreeStatusResponse {
  return value !== null && 'rootJobId' in value && 'path' in value;
}

export class AnalysisTree {
  private readonly api: ApiClient;
  private readonly context: () => SourceTaskContext;
  private readonly limits: AnalysisTreeLimits;
  private readonly onChange: (event: AnalysisTreeChangeEvent) => void;
  private readonly onMessage: (message: string) => void;
  private readonly sleep: (ms: number) => Promise<void>;

  public readonly nodes = new Map<string, AnalysisTreeNode>();
  public readonly index = new Map<string, AnalysisTreeNode>();
  public serial = 0;
  public clock = 0;
  public active = 0;
  public bytes = 0;
  public page = 0;
  public blocked = false;
  public root: AnalysisTreeRoot | null = null;

  constructor(options: AnalysisTreeOptions) {
    const { limits } = options;
    if (
      !Number.isSafeInteger(limits.nodes) ||
      limits.nodes <= 0 ||
      !Number.isSafeInteger(limits.bytes) ||
      limits.bytes <= 0 ||
      !Number.isSafeInteger(limits.pageRows) ||
      limits.pageRows <= 0 ||
      !Number.isSafeInteger(limits.active) ||
      limits.active <= 0
    ) {
      throw new Error(localize('ui.tree.hierarchicalViewConfigurationInvalid'));
    }
    this.api = options.api;
    this.context = options.context;
    this.limits = limits;
    this.onChange =
      options.onChange ??
      (() => {
        // default no-op
      });
    this.onMessage =
      options.onMessage ??
      (() => {
        // default no-op
      });
    this.sleep = options.sleep ?? ((ms) => new Promise<void>((r) => setTimeout(r, ms)));
  }

  identity(): string {
    const c = this.context();
    return JSON.stringify([c.bookKey, c.book, c.opened, this.serial, this.root?.job]);
  }

  current(root: AnalysisTreeRoot | null = this.root): boolean {
    const c = this.context();
    return (
      root !== null &&
      root === this.root &&
      c.opened &&
      root.bookKey === c.bookKey &&
      root.book === c.book
    );
  }

  notify(type: 'change' | 'progress' = 'change', node: AnalysisTreeNode | null = null): void {
    this.onChange({ type, node });
  }

  setRoot(root: AnalysisTreeRoot | null | undefined): void {
    if (!root?.result.tree) {
      this.reset();
      return;
    }
    if (
      this.root?.job === root.job &&
      this.root.book === root.book &&
      this.root.bookKey === root.bookKey &&
      this.root.result === root.result
    ) {
      return;
    }
    this.reset();
    this.root = {
      ...root,
      query: JSON.parse(JSON.stringify(root.query)) as WorkspaceAnalysisQuery,
    };
  }

  reset(): void {
    ++this.serial;
    for (const node of this.nodes.values()) this.cancelNode(node);
    this.nodes.clear();
    this.index.clear();
    this.bytes = 0;
    this.page = 0;
    this.root = null;
  }

  get(path: readonly string[]): AnalysisTreeNode | null {
    let map = this.index;
    let node: AnalysisTreeNode | undefined;
    for (const value of path) {
      node = map.get(value);
      if (!node) return null;
      map = node.children;
    }
    return node ?? null;
  }

  remove(node: AnalysisTreeNode): void {
    const victims = [...this.nodes.values()].filter((other) => isPrefix(node.path, other.path));
    for (const old of victims) {
      this.cancelNode(old);
      this.nodes.delete(old.key);
      this.bytes -= old.bytes;
    }
    const parent =
      node.path.length === 1 ? this.index : this.get(node.path.slice(0, -1))?.children;
    const leaf = node.path.at(-1);
    if (leaf !== undefined) parent?.delete(leaf);
  }

  fit(extraBytes: number, extraNodes: number, protect: readonly string[]): boolean {
    while (
      this.bytes + extraBytes > this.limits.bytes ||
      this.nodes.size + extraNodes > this.limits.nodes
    ) {
      const candidates = [...this.nodes.values()].filter(
        (n) =>
          !n.pending &&
          !isPrefix(n.path, protect) &&
          ![...this.nodes.values()].some((active) => active.pending && isPrefix(n.path, active.path)),
      );
      candidates.sort((a, b) => Number(a.expanded) - Number(b.expanded) || a.used - b.used);
      const victim = candidates[0];
      if (!victim) return false;
      this.remove(victim);
    }
    return true;
  }

  row(path: readonly string[]): AnalysisResultRow | undefined {
    const parent =
      path.length === 1 ? this.root?.result : this.get(path.slice(0, -1))?.result;
    return parent?.rows.find((row) => arraysEqual(row.keys, path));
  }

  expand(path: readonly string[]): AnalysisTreeNode | null {
    if (!this.current() || this.blocked || !this.root) return null;
    if (!path.length || path.length >= this.root.query.rows.length || !this.row(path)) return null;
    let node = this.get(path);
    if (!node) {
      const id = pathKey(path);
      const bytes = 512 + retainedBytes({ path, id }, this.limits.bytes);
      if (!this.fit(bytes, 1, path)) {
        this.onMessage(localize('ui.tree.expandedContentReachedCacheCapacity'));
        return null;
      }
      const parentMap =
        path.length === 1 ? this.index : this.get(path.slice(0, -1))?.children;
      const leaf = path.at(-1);
      if (!parentMap || leaf === undefined) return null;
      node = {
        key: id,
        path: [...path],
        children: new Map(),
        expanded: false,
        result: null,
        pending: false,
        running: false,
        version: 0,
        phase: 'idle',
        error: '',
        progress: 0,
        total: 0,
        bytes,
        baseBytes: bytes,
        used: ++this.clock,
        ticket: 0,
        jobs: null,
      };
      parentMap.set(leaf, node);
      this.nodes.set(id, node);
      this.bytes += bytes;
    }
    node.expanded = true;
    node.used = ++this.clock;
    this.page = 0;
    if (!node.result && !node.pending) {
      this.bytes += node.baseBytes - node.bytes;
      node.bytes = node.baseBytes;
      node.pending = true;
      node.phase = 'queued';
      node.error = '';
      node.ticket = ++this.clock;
    }
    this.notify();
    this.pump();
    return node;
  }

  cancelNode(node: AnalysisTreeNode): void {
    ++node.version;
    node.jobs?.cancelMain();
    node.pending = false;
    if (!node.result) {
      this.bytes += node.baseBytes - node.bytes;
      node.bytes = node.baseBytes;
      node.phase = 'idle';
      node.error = '';
    }
  }

  cancel(path: readonly string[]): void {
    const node = this.get(path);
    if (!node) return;
    this.cancelNode(node);
    this.notify();
    this.pump();
  }

  collapse(path: readonly string[]): void {
    const node = this.get(path);
    if (!node) return;
    node.expanded = false;
    for (const other of this.nodes.values()) {
      if (isPrefix(path, other.path) && other.pending) this.cancelNode(other);
    }
    this.page = 0;
    this.notify();
    this.pump();
  }

  collapseAll(): void {
    for (const node of this.nodes.values()) {
      node.expanded = false;
      if (node.pending) this.cancelNode(node);
    }
    this.page = 0;
    this.notify();
    this.pump();
  }

  setBlocked(blocked: boolean): void {
    if (this.blocked === blocked) return;
    this.blocked = blocked;
    if (blocked) {
      for (const node of this.nodes.values()) {
        if (node.pending) this.cancelNode(node);
      }
    }
    this.notify();
    if (!blocked) this.pump();
  }

  pump(): void {
    if (!this.current() || this.blocked) return;
    const queued = [...this.nodes.values()]
      .filter((n) => n.pending && !n.running)
      .sort((a, b) => a.ticket - b.ticket);
    for (const node of queued) {
      if (this.active >= this.limits.active) break;
      node.running = true;
      ++this.active;
      void this.execute(node);
    }
  }

  validResult(status: AnalysisTreeStatusResponse, node: AnalysisTreeNode): boolean {
    if (!this.root) return false;
    const result = status.result;
    const q = this.root.query;
    if (!result?.tree) return false;
    return (
      status.rootJobId === this.root.job &&
      arraysEqual(status.path, node.path) &&
      arraysEqual(result.tree.path, node.path) &&
      result.tree.levels === q.rows.length &&
      result.tree.hasChildren === (node.path.length + 1 < q.rows.length) &&
      arraysEqual(result.rowFields ?? [], q.rows.slice(0, node.path.length + 1)) &&
      result.columnKeys.every((col) => this.root?.result.columnKeys.includes(col)) &&
      result.rows.every(
        (row) => row.keys.length === node.path.length + 1 && isPrefix(node.path, row.keys),
      )
    );
  }

  async execute(node: AnalysisTreeNode): Promise<void> {
    const root = this.root;
    if (!root) {
      node.running = false;
      --this.active;
      return;
    }
    const identity = this.identity();
    const version = node.version;
    const current = (): boolean =>
      this.current(root) &&
      identity === this.identity() &&
      version === node.version &&
      this.get(node.path) === node &&
      node.pending;

    // Cancel also forgets any completed response. AsyncJobs observes a
    // start response even after cancellation, so its late ID is not lost.

    const jobs = new AsyncJobs<
      AnalysisTreeStatusResponse | AnalysisResult | null,
      AnalysisTreeStatusResponse
    >({
      api: this.api,
      decodeStatus: decodeAnalysisTreeStatusResponse,
      cancelEndpoint: '/api/analysis/tree/clear',
      identity: () => `${this.identity()}:${node.version}`,
      requestIdentity: () => root.bookKey,
      sleep: this.sleep,
      endpoint: '/api/analysis/tree',
      label: localize('ui.tree.expandHierarchy'),
      maxPolls: 0,
      resultFromStatus: (status) => status,
      onProgress: (status) => {
        if (current()) {
          node.progress = status.progress || 0;
          node.total = status.total || 0;
          node.phase = 'running';
          this.notify('progress', node);
        }
      },
    });
    node.jobs = jobs;
    node.phase = 'running';
    this.notify('progress', node);
    let completedJob = '';
    try {
      const completed = await jobs.execute(
        { jobId: root.job, workbookKey: root.bookKey, book: root.book, path: node.path },
        { main: true },
      );
      completedJob = completed.job;
      if (!current() || !completed.current()) return;
      if (!isTreeStatusResponse(completed.result) || !this.validResult(completed.result, node)) {
        throw new Error(localize('ui.tree.hierarchicalResultsDoNotMatch'));
      }
      const result = completed.result.result;
      if (!result) throw new Error(localize('ui.tree.hierarchicalResultsDoNotMatch'));
      const bytes = node.baseBytes + retainedBytes(result, this.limits.bytes);
      if (bytes > this.limits.bytes || !this.fit(bytes - node.bytes, 0, node.path)) {
        throw new Error(localize('ui.tree.thisGroupResultExceedsCache'));
      }
      this.bytes += bytes - node.bytes;
      node.bytes = bytes;
      node.result = result;
      node.phase = 'done';
      node.used = ++this.clock;
    } catch (error: unknown) {
      if (current() && !isAbortError(error)) {
        let text = errorMessage(error) || localize('ui.tree.expansionFailed');
        if (text.length > 2048) {
          text = `${text.slice(0, 2047).replace(/[\uD800-\uDBFF]$/, '')}…`;
        }
        const bytes = node.baseBytes + retainedBytes({ error: text }, this.limits.bytes);
        if (bytes <= this.limits.bytes && this.fit(bytes - node.bytes, 0, node.path)) {
          this.bytes += bytes - node.bytes;
          node.bytes = bytes;
          node.error = text;
        } else {
          node.error = localize('ui.tree.expansionFailedNarrowScopeTry');
        }
        node.phase = 'error';
      }
    } finally {
      if (completedJob) {
        void this.api('/api/analysis/tree/clear', {
          method: 'POST',
          body: JSON.stringify({ id: completedJob }),
        }).catch(() => {
          // Best-effort clear.
        });
      }
      if (node.jobs === jobs) {
        node.jobs = null;
        node.running = false;
        if (version === node.version) node.pending = false;
      }
      --this.active;
      if (this.current(root)) this.notify();
      this.pump();
    }
  }

  *rows(result: AnalysisResult | null | undefined = this.root?.result): Generator<AnalysisTreeRowItem> {
    if (!result) return;
    for (const row of result.rows) {
      const node = this.get(row.keys);
      yield { kind: 'row', row, result, node };
      if (!node?.expanded) continue;
      if (node.result) {
        yield* this.rows(node.result);
        if (node.result.truncated) yield { kind: 'truncated', node };
      } else {
        yield { kind: 'status', node };
      }
    }
  }

  pageRows(): AnalysisTreePage {
    const rows: AnalysisTreeRowItem[] = [];
    const skip = this.page * this.limits.pageRows;
    let i = 0;
    let more = false;
    for (const item of this.rows()) {
      if (i++ < skip) continue;
      if (rows.length >= this.limits.pageRows) {
        more = true;
        break;
      }
      rows.push(item);
    }
    if (!rows.length && this.page > 0) {
      this.page = 0;
      return this.pageRows();
    }
    return { rows, more, first: rows.length ? skip + 1 : 0, last: skip + rows.length };
  }

  diagnostics(): AnalysisTreeDiagnostics {
    return {
      nodes: this.nodes.size,
      bytes: this.bytes,
      active: this.active,
      queued: [...this.nodes.values()].filter((n) => n.pending && !n.running).length,
      page: this.page,
    };
  }
}

export interface AnalysisTreeRenderDeps {
  readonly fieldName: (id: string) => string;
  readonly metricName: (metric: AnalysisMetric) => string;
  readonly metricCell: (
    result: AnalysisResult,
    row: AnalysisResultRow | null,
    columnIndex: number,
    metricIndex: number,
    total?: boolean,
  ) => AnalysisCell;
  readonly drill: (
    keys: readonly string[],
    colKey: string,
    offset?: number,
    columnSelected?: boolean,
  ) => void;
  readonly formatNumber?: (cell: AnalysisCell | null | undefined) => string;
}

export class AnalysisTreeRenderer {
  private readonly statusNodes = new Map<string, HTMLElement>();

  status(node: AnalysisTreeNode): string {
    if (node.pending) {
      return node.phase === 'queued'
        ? localize('ui.tree.waitingExpand')
        : localize('ui.tree.expanding', { detail: node.total ? ` · ${formatCount(node.progress)} / ${formatCount(node.total)}` : '…' });
    }
    return node.error ? localize('ui.tree.expansionFaileddfedc24', { detail: node.error }) : localize('ui.tree.exportCanceled');
  }

  progress(node: AnalysisTreeNode | null): void {
    if (!node) return;
    const text = this.statusNodes.get(node.key);
    if (text) text.textContent = this.status(node);
  }

  render(
    table: HTMLElement,
    model: AnalysisTree,
    deps: AnalysisTreeRenderDeps,
  ): AnalysisTreePage {
    const {
      fieldName,
      metricName,
      metricCell,
      drill,
      formatNumber = (cell: AnalysisCell | null | undefined): string => cell?.display || '—',
    } = deps;
    const root = model.root;
    if (!root) return { rows: [], more: false, first: 0, last: 0 };

    const activeEl = document.activeElement;
    const previous = activeEl instanceof HTMLElement ? activeEl.dataset.treeFocus : undefined;
    const scroll = table.parentElement?.scrollTop ?? 0;

    table.replaceChildren();
    table.setAttribute('role', 'treegrid');
    table.setAttribute('aria-label', localize('ui.tree.hierarchicalPerspective'));
    this.statusNodes.clear();

    const q = root.query;
    const result = root.result;
    const metrics: readonly AnalysisMetric[] = q.metrics?.length
      ? q.metrics
      : [{ field: q.value, aggregate: q.aggregate }];
    const columns: readonly string[] = q.columns?.length ? result.columnKeys : [];
    const headers: string[] = [q.rows.map(fieldName).join(' / ')];
    for (const col of columns) {
      for (const metric of metrics) {
        headers.push(`${col === '' ? localize('ui.tree.blankColumn') : col} · ${metricName(metric)}`);
      }
    }
    for (const metric of metrics) {
      headers.push(`${columns.length ? `${localize('ui.tree.subtotal')} ` : ''}${metricName(metric)}`);
    }

    const head = document.createElement('thead');
    const hr = document.createElement('tr');
    for (const title of headers) {
      const th = document.createElement('th');
      th.textContent = title;
      th.scope = 'col';
      hr.append(th);
    }
    head.append(hr);
    table.append(head);

    const body = document.createElement('tbody');
    const page = model.pageRows();

    const makeButton = (
      text: string,
      action: () => void,
      focus?: string,
    ): HTMLButtonElement => {
      const b = document.createElement('button');
      b.type = 'button';
      b.textContent = text;
      b.onclick = action;
      if (focus) b.dataset.treeFocus = focus;
      return b;
    };

    const appendValueCell = (
      tr: HTMLTableRowElement,
      cell: AnalysisCell | null,
      path: readonly string[],
      col: string,
      selected: boolean,
    ): void => {
      const td = document.createElement('td');
      td.className = 'analysis-number';
      td.title = comparisonTitle(cell) || cell?.exact || '';
      if (cell && cell.exact !== '') {
        const b = makeButton(formatNumber(cell), () => drill(path, col, 0, selected));
        b.className = 'analysis-drill-link';
        b.title = localize('ui.tree.viewCurrentGroupSourceDetails', { title: td.title });
        b.disabled = model.blocked;
        td.append(b);
      } else {
        td.textContent = formatNumber(cell);
      }
      tr.append(td);
    };

    for (const item of page.rows) {
      const tr = document.createElement('tr');
      if (item.kind !== 'row') {
        const td = document.createElement('td');
        td.colSpan = headers.length;
        td.className = 'analysis-tree-status';
        td.style.paddingInlineStart = `${18 + item.node.path.length * 16}px`;
        const text = document.createElement('span');
        text.setAttribute('role', 'status');
        text.textContent =
          item.kind === 'truncated'
            ? localize('ui.tree.thisLayerOnlyDisplaysFirst')
            : this.status(item.node);
        td.append(text);
        if (item.kind === 'status') {
          this.statusNodes.set(item.node.key, text);
          const b = makeButton(
            item.node.pending ? localize('ui.common.cancel') : localize('ui.tree.retry'),
            () => (item.node.pending ? model.cancel(item.node.path) : model.expand(item.node.path)),
            JSON.stringify(['status', item.node.path]),
          );
          b.className = 'analysis-quiet-button';
          b.disabled = model.blocked;
          td.append(b);
        }
        tr.append(td);
        body.append(tr);
        continue;
      }

      const { row, node } = item;
      const path = row.keys;
      const hasChildren = path.length < q.rows.length;
      tr.className = 'analysis-tree-row';
      tr.dataset.treePath = pathKey(path);
      tr.setAttribute('aria-level', String(path.length));
      if (hasChildren) tr.setAttribute('aria-expanded', String(Boolean(node?.expanded)));
      if (node?.pending) tr.setAttribute('aria-busy', 'true');

      const td = document.createElement('td');
      td.className = 'analysis-tree-label';
      td.style.paddingInlineStart = `${10 + (path.length - 1) * 16}px`;
      const b = makeButton(
        '',
        () => {
          if (hasChildren) {
            if (node?.expanded) model.collapse(path);
            else model.expand(path);
          } else {
            drill(path, '', 0, false);
          }
        },
        JSON.stringify(['row', path]),
      );
      b.className = 'analysis-tree-toggle';
      b.disabled = model.blocked;
      if (hasChildren) b.setAttribute('aria-expanded', String(Boolean(node?.expanded)));
      b.setAttribute(
        'aria-label',
        (hasChildren ? (node?.expanded ? `${localize('ui.tree.putAway')} ` : `${localize('ui.tree.expand')} `) : `${localize('ui.tree.viewDetails')} `) +
          path
            .map((v, i) => `${fieldName(q.rows[i] ?? '')}：${v === '' ? localize('ui.tree.blankValues') : v}`)
            .join(' / '),
      );

      const arrow = document.createElement('span');
      arrow.className = 'analysis-tree-arrow';
      arrow.setAttribute('aria-hidden', 'true');
      arrow.textContent = hasChildren ? (node?.expanded ? '▾' : '▸') : '·';
      const leafVal = path.at(-1) ?? '';
      const name = document.createElement('span');
      name.textContent = leafVal === '' ? localize('ui.common.blank') : leafVal;
      if (leafVal === '') name.className = 'analysis-tree-empty';
      b.append(arrow, name);
      td.append(b);
      td.title = path.map(displayLabel).join(' / ');
      if (path.length > 1) {
        const context = document.createElement('small');
        context.className = 'analysis-tree-context';
        context.textContent = path.slice(0, -1).map(displayLabel).join(' / ');
        td.append(context);
      }

      b.onkeydown = (event: KeyboardEvent): void => {
        if (event.key === 'ArrowRight' && hasChildren) {
          event.preventDefault();
          if (!node?.expanded) {
            model.expand(path);
          } else {
            const next = [...table.querySelectorAll<HTMLElement>('.analysis-tree-row')].find((n) => {
              const rawPath = n.dataset.treePath;
              if (!rawPath) return false;
              const p = JSON.parse(rawPath) as string[];
              return p.length === path.length + 1 && isPrefix(path, p);
            });
            next?.querySelector<HTMLElement>('.analysis-tree-toggle')?.focus();
          }
        } else if (event.key === 'ArrowLeft') {
          event.preventDefault();
          if (node?.expanded) {
            model.collapse(path);
          } else {
            const parent = [...table.querySelectorAll<HTMLElement>('.analysis-tree-row')].find((n) => {
              const rawPath = n.dataset.treePath;
              if (!rawPath) return false;
              return arraysEqual(JSON.parse(rawPath) as string[], path.slice(0, -1));
            });
            parent?.querySelector<HTMLElement>('.analysis-tree-toggle')?.focus();
          }
        } else if (['ArrowUp', 'ArrowDown', 'Home', 'End'].includes(event.key)) {
          event.preventDefault();
          const buttons = [...table.querySelectorAll<HTMLElement>('.analysis-tree-toggle')];
          const idx = buttons.indexOf(b);
          const at =
            event.key === 'Home'
              ? 0
              : event.key === 'End'
                ? buttons.length - 1
                : Math.max(0, Math.min(buttons.length - 1, idx + (event.key === 'ArrowDown' ? 1 : -1)));
          buttons[at]?.focus();
        }
      };

      tr.append(td);
      for (const col of columns) {
        const index = item.result.columnKeys.indexOf(col);
        for (let m = 0; m < metrics.length; m++) {
          appendValueCell(
            tr,
            index < 0 ? null : metricCell(item.result, row, index, m),
            path,
            col,
            true,
          );
        }
      }
      for (let m = 0; m < metrics.length; m++) {
        appendValueCell(tr, metricCell(item.result, row, 0, m, true), path, '', false);
      }
      body.append(tr);
    }

    table.append(body);
    const foot = document.createElement('tfoot');
    const total = document.createElement('tr');
    const caption = document.createElement('td');
    caption.textContent = result.truncated ? localize('ui.tree.totalAllItemsIncludingUnshown') : localize('ui.tree.rangeTotal');
    total.append(caption);
    for (let c = 0; c < columns.length; c++) {
      const colKey = columns[c] ?? '';
      for (let m = 0; m < metrics.length; m++) {
        appendValueCell(total, metricCell(result, null, c, m), [], colKey, true);
      }
    }
    for (let m = 0; m < metrics.length; m++) {
      appendValueCell(total, metricCell(result, null, 0, m, true), [], '', false);
    }
    foot.append(total);
    table.append(foot);

    if (previous) {
      [...table.querySelectorAll<HTMLElement>('[data-tree-focus]')]
        .find((n) => n.dataset.treeFocus === previous)
        ?.focus({ preventScroll: true });
    }
    if (table.parentElement) table.parentElement.scrollTop = scroll;
    return page;
  }
}
