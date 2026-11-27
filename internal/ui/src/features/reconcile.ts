/**
 * Two-table reconciliation workflow: source pairing, unique-header auto-matching,
 * exact/tolerance comparison, audit record rebinding, version/batch comparison,
 * and background job resumption.
 */
import type {
  ApiClient,
  ApiRequestOptions,
  ToastFn,
} from '../contracts/common.ts';
import type {
  ReconcileBookChoice,
  ReconcileCounts,
  ReconcileField,
  ReconcilePair,
  ReconcileQuery,
  ReconcileResult,
  ReconcileSourceInfo,
  ReconcileSourceRef,
} from '../contracts/features.ts';
import { AsyncJobs, type AsyncJobStatusEnvelope } from '../core/async-jobs.ts';
import { columnNumber } from '../core/cell-address.ts';
import { errorMessage } from '../core/display-format.ts';
import {
  requireButton,
  requireDialog,
  requireEl,
  requireInput,
  requireSelect,
} from '../core/dom.ts';
import type { WorkbookState } from '../grid/state.ts';
import {
  decodeCompositionNavigationResponse,
  decodeReconcileJobStatus,
  decodeReconcileSourceInfo,
  decodeReconcileUploadResponse,
  decodereconcileReconcileSourcesResponse,
} from '../core/http-decoders.generated.ts';

type ReconcileSide = 'left' | 'right';

export interface ReconcileResultWithMeta extends ReconcileResult {
  readonly recordError?: string;
  readonly exceptionGroupsSHA256?: string;
}

/** @httpResponse ReconcileJobStatus */
export interface ReconcileJobStatus extends AsyncJobStatusEnvelope<ReconcileResult> {
  readonly recordError?: string;
  readonly exceptionGroupsSHA256?: string;
}

export interface ReconcileRecordInput {
  readonly kind: 'workbook' | 'analysis';
  readonly name: string;
  readonly sheet: string;
  readonly stamp: string;
  readonly fileSHA256?: string;
  readonly fields: readonly ReconcileField[];
}

export interface ReconcileRecordFile {
  readonly kind: 'ExcelLens.Reconcile';
  readonly version: 1;
  readonly ruleVersion: 1 | 2;
  readonly scope: 'all-data-rows';
  readonly exceptionGroupsSHA256: string;
  readonly inputs: readonly [ReconcileRecordInput, ReconcileRecordInput];
  readonly query: ReconcileQuery;
  readonly counts: Partial<ReconcileCounts>;
  readonly detail: {
    readonly status: string;
    readonly sha256: string;
    readonly protectCSV: boolean;
  };
}

export interface ReconcileReboundQuery {
  readonly keys: readonly ReconcilePair[];
  readonly fields: readonly ReconcilePair[];
  readonly includeEqual: boolean;
}

export interface ReconcileExportExtra {
  readonly format?: string;
  readonly status?: string;
}

export interface ReconcileOptions {
  readonly state: WorkbookState;
  readonly api: ApiClient;
  readonly toast: ToastFn;
  readonly colName: (col: number) => string;
  readonly navigate: (sheet: string, ref: string) => Promise<unknown>;
  readonly navigateAnalysis?: (sheet: string, row: number, col: number) => Promise<void> | void;
  readonly closeAnalysis?: () => void;
  readonly onExport?: (
    jobId: string,
    protectCSV: boolean,
    label: string,
    options?: ReconcileExportExtra,
  ) => unknown;
  readonly onAuditExport?: (
    path: string,
    payload: Record<string, unknown> | FormData,
    label: string,
  ) => unknown;
  readonly canRun?: (label: string) => boolean;
  readonly recordBytes: number;
}

/** @httpResponse reconcileReconcileSourcesResponse */
export interface ReconcileSourcesResponse {
  readonly books?: readonly ReconcileBookChoice[];
  readonly currentBook: string;
}

/** @httpResponse ReconcileUploadResponse */
export interface ReconcileUploadResponse {
  readonly book: string;
  readonly name: string;
}

interface SideRuntimeState {
  serial: number;
  info: ReconcileSourceInfo | null;
  controller: AbortController | null;
}

interface ReconcileBaselineState {
  readonly record: ReconcileRecordFile;
  readonly targets: readonly [ReconcileSourceInfo, ReconcileSourceInfo];
  readonly query: ReconcileReboundQuery;
}

const SIDES: readonly [ReconcileSide, ReconcileSide] = ['left', 'right'];

const STATUS_LABELS: Readonly<Record<string, string>> = {
  equal: '一致',
  within_tolerance: '容差内',
  different: '差异',
  left_only: '仅左侧',
  right_only: '仅右侧',
  duplicate: '重复主键',
  review: '需核对',
  invalid_key: '无效主键',
};

const COUNT_LABEL_ENTRIES: readonly (readonly [keyof ReconcileCounts, string])[] = [
  ['equal', '一致主键'],
  ['withinTolerance', '容差内主键'],
  ['different', '差异主键'],
  ['leftOnly', '仅左侧主键'],
  ['rightOnly', '仅右侧主键'],
  ['duplicate', '重复主键'],
  ['review', '需核对主键'],
  ['invalidKeys', '无效主键记录'],
];

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function isUnknownArray(value: unknown): value is readonly unknown[] {
  return Array.isArray(value);
}

function isSha256(value: unknown): value is string {
  return typeof value === 'string' && /^[0-9a-f]{64}$/.test(value);
}

function decimalTolerance(text: string): boolean {
  return /^(?:\d+(?:\.\d*)?|\.\d+)$/.test(text);
}

function isAbortError(error: unknown): boolean {
  return error instanceof Error && error.name === 'AbortError';
}

function parseRecordInput(value: unknown): ReconcileRecordInput {
  if (!isRecord(value)) throw new Error('核查记录的字段、条件或计数无效');
  if (
    !isSha256(value.stamp) ||
    (value.kind !== 'analysis' && !(value.kind === 'workbook' && isSha256(value.fileSHA256)))
  ) {
    throw new Error('核查记录的字段、条件或计数无效');
  }
  const fieldsRaw = value.fields;
  const fields: ReconcileField[] = [];
  if (isUnknownArray(fieldsRaw)) {
    for (const item of fieldsRaw) {
      if (isRecord(item) && typeof item.column === 'number' && typeof item.name === 'string') {
        fields.push({
          column: item.column,
          name: item.name,
          rawName: typeof item.rawName === 'string' ? item.rawName : undefined,
          headerHash: typeof item.headerHash === 'string' ? item.headerHash : undefined,
          headerLoaded: item.headerLoaded === true,
          truncated: typeof item.truncated === 'boolean' ? item.truncated : undefined,
          uniqueHeader: typeof item.uniqueHeader === 'boolean' ? item.uniqueHeader : undefined,
        });
      }
    }
  }
  return {
    kind: value.kind,
    name: typeof value.name === 'string' ? value.name : '',
    sheet: typeof value.sheet === 'string' ? value.sheet : '',
    stamp: value.stamp,
    fileSHA256: typeof value.fileSHA256 === 'string' ? value.fileSHA256 : undefined,
    fields,
  };
}

function parseReconcileRecordFile(record: unknown): {
  readonly parsed: ReconcileRecordFile;
  readonly rawInputs: readonly [Record<string, unknown>, Record<string, unknown>];
  readonly rawKeys: readonly unknown[];
  readonly rawFields: readonly unknown[];
  readonly includeEqual: boolean;
} {
  if (
    !isRecord(record) ||
    record.kind !== 'ExcelLens.Reconcile' ||
    record.version !== 1 ||
    (record.ruleVersion !== 1 && record.ruleVersion !== 2) ||
    record.scope !== 'all-data-rows'
  ) {
    throw new Error('核查记录或规则版本不受支持');
  }
  const query = record.query;
  const inputs = record.inputs;
  const counts = record.counts;
  const detail = record.detail;
  const filter = isRecord(detail) ? detail.status : undefined;

  if (
    !isUnknownArray(inputs) ||
    inputs.length !== 2 ||
    !isRecord(inputs[0]) ||
    !isRecord(inputs[1]) ||
    !isRecord(query) ||
    !isRecord(detail) ||
    !isRecord(counts) ||
    !isSha256(record.exceptionGroupsSHA256) ||
    !isSha256(detail.sha256) ||
    !isUnknownArray(query.keys) ||
    !query.keys.length ||
    query.keys.length > 4 ||
    !(query.fields === null || (isUnknownArray(query.fields) && query.fields.length <= 8)) ||
    typeof filter !== 'string' ||
    (filter !== '' && !Object.hasOwn(STATUS_LABELS, filter)) ||
    (query.includeEqual !== undefined && typeof query.includeEqual !== 'boolean') ||
    typeof detail.protectCSV !== 'boolean'
  ) {
    throw new Error('核查记录的字段、条件或计数无效');
  }

  const parsedInputs: [ReconcileRecordInput, ReconcileRecordInput] = [
    parseRecordInput(inputs[0]),
    parseRecordInput(inputs[1]),
  ];

  const parsedCounts: Partial<Record<keyof ReconcileCounts, number>> = {};
  for (const [key] of COUNT_LABEL_ENTRIES) {
    const val = counts[key];
    if (key === 'withinTolerance' && record.ruleVersion === 1 && val === undefined) continue;
    if (typeof val !== 'number' || !Number.isSafeInteger(val) || val < 0) {
      throw new Error('核查记录的字段、条件或计数无效');
    }
    parsedCounts[key] = val;
  }

  const rawFields = query.fields === null ? [] : query.fields;
  const parsed: ReconcileRecordFile = {
    kind: 'ExcelLens.Reconcile',
    version: 1,
    ruleVersion: record.ruleVersion,
    scope: 'all-data-rows',
    exceptionGroupsSHA256: record.exceptionGroupsSHA256,
    inputs: parsedInputs,
    query: {
      keys: [],
      fields: [],
      includeEqual: Boolean(query.includeEqual),
    },
    counts: parsedCounts,
    detail: {
      status: filter,
      sha256: detail.sha256,
      protectCSV: detail.protectCSV,
    },
  };
  return {
    parsed,
    rawInputs: [inputs[0], inputs[1]],
    rawKeys: query.keys,
    rawFields,
    includeEqual: Boolean(query.includeEqual),
  };
}

export function rebindReconcileRecord(
  record: unknown,
  targets: readonly [ReconcileSourceInfo, ReconcileSourceInfo],
): ReconcileReboundQuery {
  const { parsed, rawInputs, rawKeys, rawFields, includeEqual } = parseReconcileRecordFile(record);

  const bindColumn = (side: 0 | 1, column: unknown): number => {
    const rawInput = rawInputs[side];
    const input = parsed.inputs[side];
    const target = targets[side];
    if (
      typeof column !== 'number' ||
      !Number.isSafeInteger(column) ||
      column < 1 ||
      !isUnknownArray(rawInput.fields) ||
      typeof rawInput.name !== 'string' ||
      !input.stamp
    ) {
      throw new Error('核查记录的来源字段无效');
    }
    const saved = input.fields.find((field) => field.column === column);
    if (!saved) throw new Error('核查记录缺少已用字段');
    if (
      input.kind === target.kind &&
      input.stamp === target.stamp &&
      target.fields.some((field) => field.column === column)
    ) {
      return column;
    }
    const matches =
      saved.uniqueHeader && saved.headerHash
        ? target.fields.filter(
            (field) => field.uniqueHeader && field.headerHash === saved.headerHash,
          )
        : [];
    const matched = matches[0];
    if (matches.length !== 1 || !matched) {
      throw new Error(
        `${side ? '右' : '左'}侧「${saved.name}」无法按唯一表头对应，请核对来源和表头`,
      );
    }
    return matched.column;
  };

  const bindPair = (pair: unknown, isField: boolean): ReconcilePair => {
    if (!isRecord(pair)) throw new Error('核对方式无效');
    const mode = pair.mode === undefined ? '' : pair.mode;
    if (
      typeof mode !== 'string' ||
      !['', 'text', 'number'].includes(mode) ||
      (pair.trim !== undefined && typeof pair.trim !== 'boolean') ||
      (pair.ignoreCase !== undefined && typeof pair.ignoreCase !== 'boolean')
    ) {
      throw new Error('核对方式无效');
    }
    const mapping: ReconcilePair = {
      left: bindColumn(0, pair.left),
      right: bindColumn(1, pair.right),
      mode,
      trim: Boolean(pair.trim),
      ignoreCase: Boolean(pair.ignoreCase),
    };
    if (pair.tolerance !== undefined) {
      if (
        !isField ||
        mode !== 'number' ||
        parsed.ruleVersion === 1 ||
        typeof pair.tolerance !== 'string' ||
        !decimalTolerance(pair.tolerance)
      ) {
        throw new Error('核查记录包含无效金额容差');
      }
      mapping.tolerance = pair.tolerance;
    }
    return mapping;
  };

  const bindPairs = (pairs: readonly unknown[], isField: boolean): ReconcilePair[] => {
    const mapped = pairs.map((pair) => bindPair(pair, isField));
    if (
      new Set(mapped.map((pair) => pair.left)).size !== mapped.length ||
      new Set(mapped.map((pair) => pair.right)).size !== mapped.length
    ) {
      throw new Error('核查记录包含重复映射');
    }
    return mapped;
  };

  return {
    keys: bindPairs(rawKeys, false),
    fields: bindPairs(rawFields, true),
    includeEqual,
  };
}

export function suggestReconcilePairs(
  left: readonly ReconcileField[],
  right: readonly ReconcileField[],
): { readonly keys: ReconcilePair[]; readonly fields: ReconcilePair[] } {
  const unique = (fields: readonly ReconcileField[]): Map<string, ReconcileField | null> => {
    const map = new Map<string, ReconcileField | null>();
    for (const field of fields) {
      if (!field.headerLoaded || field.truncated || !field.rawName || !field.headerHash) continue;
      const key = field.headerHash;
      map.set(key, map.has(key) ? null : field);
    }
    return map;
  };
  const a = unique(left);
  const b = unique(right);
  const pairs: ReconcilePair[] = [];
  for (const field of left) {
    const key = field.headerHash;
    const match = key ? b.get(key) : null;
    if (key && a.get(key) === field && match) {
      pairs.push({ left: field.column, right: match.column, mode: '' });
    }
  }
  return { keys: pairs.slice(0, 1), fields: pairs.slice(1, 9) };
}

export function resolveReconcileColumn(
  rawInput: string | number,
  fields: readonly ReconcileField[],
): number {
  const raw = String(rawInput).trim();
  let column = 0;
  if (/^\d+$/.test(raw)) column = Number(raw);
  else if (/^[A-Za-z]{1,5}$/.test(raw)) column = columnNumber(raw);
  const exists = fields.some((field) => field.column === column);
  const named = fields.filter(
    (field) => field.headerLoaded && !field.truncated && (field.rawName === raw || field.name === raw),
  );
  if (exists && named.some((field) => field.column !== column) && !/^\d+$/.test(raw)) {
    throw new Error('字段名与列标识有歧义，请输入列序号');
  }
  if (exists) return column;
  const firstNamed = named[0];
  if (named.length === 1 && firstNamed) return firstNamed.column;
  throw new Error(named.length > 1 ? '字段名重复，请输入列字母或序号' : `字段「${raw}」无效`);
}

export const ReconcileModel = Object.freeze({
  suggest: suggestReconcilePairs,
  resolveColumn: resolveReconcileColumn,
  rebindRecord: rebindReconcileRecord,
});

export class Reconcile {
  private readonly s: WorkbookState;
  private readonly api: ApiClient;
  private readonly toast: ToastFn;
  private readonly colName: (col: number) => string;
  private readonly navigate: ReconcileOptions['navigate'];
  private readonly navigateAnalysis?: ReconcileOptions['navigateAnalysis'];
  private readonly closeAnalysis?: ReconcileOptions['closeAnalysis'];
  private readonly onExport?: ReconcileOptions['onExport'];
  private readonly onAuditExport?: ReconcileOptions['onAuditExport'];
  private readonly canRun: (label: string) => boolean;
  private readonly recordBytes: number;
  private readonly jobs: AsyncJobs<ReconcileResultWithMeta, ReconcileJobStatus>;

  public readonly dialog: HTMLDialogElement;
  public epoch = 0;
  private books: ReconcileBookChoice[] = [];
  public primaryBook = '';
  public result: ReconcileResultWithMeta | null = null;
  private resultFields: {
    left: Map<number, ReconcileField>;
    right: Map<number, ReconcileField>;
  } = { left: new Map(), right: new Map() };
  public job = '';
  public busy = false;
  private pendingUpload: AbortController | null = null;
  private catalogController: AbortController | null = null;
  private catalogSerial = 0;
  private mapped = false;
  public configChanged = false;
  private recordIntent = 0;
  private baseline: ReconcileBaselineState | null = null;
  private resultBaseline: ReconcileRecordFile | null = null;
  public readonly sides: Record<ReconcileSide, SideRuntimeState> = {
    left: { serial: 0, info: null, controller: null },
    right: { serial: 0, info: null, controller: null },
  };

  constructor(options: ReconcileOptions) {
    this.s = options.state;
    this.api = options.api;
    this.toast = options.toast;
    this.colName = options.colName;
    this.navigate = options.navigate;
    this.navigateAnalysis = options.navigateAnalysis;
    this.closeAnalysis = options.closeAnalysis;
    this.onExport = options.onExport;
    this.onAuditExport = options.onAuditExport;
    this.canRun = options.canRun ?? (() => true);
    this.recordBytes = options.recordBytes;

    this.dialog = requireDialog('reconcileDialog');
    this.jobs = new AsyncJobs<ReconcileResultWithMeta, ReconcileJobStatus>({
      decodeStatus: decodeReconcileJobStatus,
      api: this.api,
      identity: () => this.s.workbookKey,
      sleep: (ms) => new Promise<void>((resolve) => setTimeout(resolve, ms)),
      endpoint: '/api/reconcile',
      label: '核对',
      maxPolls: 0,
      resultFromStatus: (status) => {
        if (!status.result) throw new Error('核对结果缺失，请重试');
        return {
          ...status.result,
          recordError: status.recordError,
          exceptionGroupsSHA256: status.exceptionGroupsSHA256,
        };
      },
      onBusy: (value) => {
        this.busy = value;
        this.controls();
      },
      onProgress: (status) =>
        this.progress(
          `${status.stage || '核对中'}${status.total ? `　${(status.done || 0).toLocaleString()} / ${status.total.toLocaleString()} 条` : ''}`,
        ),
    });
    this.bind();
  }

  private sidePrefix(side: ReconcileSide): string {
    return `reconcile${side === 'left' ? 'Left' : 'Right'}`;
  }

  private bookSelect(side: ReconcileSide): HTMLSelectElement {
    return requireSelect(`${this.sidePrefix(side)}Book`);
  }

  private sheetSelect(side: ReconcileSide): HTMLSelectElement {
    return requireSelect(`${this.sidePrefix(side)}Sheet`);
  }

  private headerInput(side: ReconcileSide): HTMLInputElement {
    return requireInput(`${this.sidePrefix(side)}Header`);
  }

  private removeButton(side: ReconcileSide): HTMLButtonElement {
    return requireButton(`${this.sidePrefix(side)}Remove`);
  }

  private infoEl(side: ReconcileSide): HTMLElement {
    return requireEl(`${this.sidePrefix(side)}Info`);
  }

  private post(
    path: string,
    value: unknown,
    options: ApiRequestOptions = {},
  ): Promise<unknown> {
    return this.api(path, { method: 'POST', body: JSON.stringify(value), ...options });
  }

  private bind(): void {
    requireButton('reconcileOpenBtn').addEventListener('click', () => {
      void this.open();
    });
    requireButton('reconcileRefresh').addEventListener('click', () => {
      void this.refresh();
    });
    requireButton('reconcileAddFile').addEventListener('click', () => {
      if (!this.busy) requireInput('reconcileFile').click();
    });
    const fileInput = requireInput('reconcileFile');
    fileInput.addEventListener('change', () => {
      const file = fileInput.files?.[0];
      void this.upload(file).finally(() => {
        fileInput.value = '';
      });
    });
    requireButton('reconcileSwap').addEventListener('click', () => this.swap());
    requireButton('reconcileLoadFields').addEventListener('click', () => {
      void this.loadFields();
    });
    requireButton('reconcileMatchFields').addEventListener('click', () => this.suggestMappings());
    requireButton('reconcileAddKey').addEventListener('click', () => this.addMapping('keys'));
    requireButton('reconcileAddField').addEventListener('click', () => this.addMapping('fields'));
    requireButton('reconcileRun').addEventListener('click', () => {
      void this.run();
    });
    requireButton('reconcileCancel').addEventListener('click', () => {
      this.jobs.cancelMain();
      this.progress('已请求取消核对');
    });
    requireInput('reconcileIncludeEqual').addEventListener('change', () => this.changed());
    requireSelect('reconcileResultFilter').addEventListener('change', () => this.renderSamples());
    requireButton('reconcileClear').addEventListener('click', () => {
      void this.clear();
    });
    requireButton('reconcileExport').addEventListener('click', () => {
      void this.export(false);
    });
    requireButton('batchCompare').addEventListener('click', () => {
      void this.compareBatches();
    });
    requireButton('versionCompare').addEventListener('click', () => {
      void this.compareVersions();
    });
    requireButton('reconcileBundle').addEventListener('click', () => {
      void this.export(true);
    });
    requireButton('reconcileImport').addEventListener('click', () =>
      requireInput('reconcileRecordFile').click(),
    );
    const recordFileInput = requireInput('reconcileRecordFile');
    recordFileInput.addEventListener('change', () => {
      const file = recordFileInput.files?.[0];
      recordFileInput.value = '';
      if (file) void this.loadRecord(file);
    });

    for (const side of SIDES) {
      this.bookSelect(side).addEventListener('change', () => {
        this.invalidate(side);
        this.fillSheets(side);
        void this.loadScratchChoices(this.bookSelect(side).value);
      });
      this.sheetSelect(side).addEventListener('change', () => {
        this.invalidate(side);
        const scratch = this.sheetSelect(side).value.startsWith('scratch:');
        const header = this.headerInput(side);
        header.disabled = scratch;
        header.value = scratch ? '0' : '1';
      });
      this.headerInput(side).addEventListener('change', () => this.invalidate(side));
      this.removeButton(side).addEventListener('click', () => {
        void this.removeSource(side);
      });
    }
  }

  resetBook(): void {
    this.baseline = null;
    this.resultBaseline = null;
    ++this.epoch;
    this.jobs.reset();
    this.catalogController?.abort();
    this.pendingUpload?.abort();
    this.pendingUpload = null;
    for (const side of SIDES) {
      this.sides[side].controller?.abort();
      this.sides[side] = { serial: 0, info: null, controller: null };
      this.bookSelect(side).replaceChildren();
      this.sheetSelect(side).replaceChildren();
      this.infoEl(side).textContent = '选择来源后读取字段';
    }
    this.books = [];
    this.primaryBook = '';
    this.result = null;
    this.job = '';
    this.mapped = false;
    this.configChanged = false;
    requireEl('reconcileKeys').replaceChildren();
    requireEl('reconcileFields').replaceChildren();
    requireEl('reconcileResults').hidden = true;
    requireEl('reconcileProgress').hidden = true;
    if (this.dialog.open) this.dialog.close();
    this.controls();
  }

  async open(): Promise<void> {
    if (!this.s.opened) {
      this.toast('请先打开一个数据文件', 'warning');
      return;
    }
    if (!this.dialog.open) this.dialog.showModal();
    if (!this.primaryBook) await this.refresh();
  }

  async refresh(focus = ''): Promise<void> {
    if (this.busy) return;
    const epoch = this.epoch;
    const serial = ++this.catalogSerial;
    this.catalogController?.abort();
    const controller = new AbortController();
    this.catalogController = controller;
    try {
      requireEl('reconcileSourceNotice').textContent = '正在读取来源…';
      const data = await this.api(
        `/api/reconcile/sources${focus ? `?book=${encodeURIComponent(focus)}` : ''}`,
        { signal: controller.signal },
        decodereconcileReconcileSourcesResponse,
      );
      if (epoch !== this.epoch || serial !== this.catalogSerial) return;
      const previous = new Map(this.books.map((book) => [book.id, book]));
      this.books = (data.books ?? []).map((book) => {
        const old = previous.get(book.id);
        return !book.scratchLoaded && old?.scratchLoaded
          ? { ...book, scratch: old.scratch, scratchLoaded: true }
          : book;
      });
      this.primaryBook = data.currentBook;
      for (const side of SIDES) {
        const node = this.bookSelect(side);
        const selected = node.value;
        const sheet = this.sheetSelect(side).value;
        node.replaceChildren();
        for (const book of this.books) {
          node.append(new Option(book.name, book.id));
        }
        node.value = this.books.some((book) => book.id === selected)
          ? selected
          : side === 'left'
            ? this.primaryBook
            : (this.books.find((book) => book.id !== this.primaryBook)?.id ?? this.primaryBook);
        this.fillSheets(side, sheet);
        if (selected && (selected !== node.value || sheet !== this.sheetSelect(side).value)) {
          this.invalidate(side);
        }
      }
      requireEl('reconcileSourceNotice').textContent = '';
      this.controls();
    } catch (error: unknown) {
      if (epoch === this.epoch && serial === this.catalogSerial && !isAbortError(error)) {
        const msg = errorMessage(error);
        requireEl('reconcileSourceNotice').textContent = msg;
        this.toast(msg, 'error');
      }
    } finally {
      if (this.catalogController === controller) this.catalogController = null;
    }
  }

  private async loadScratchChoices(bookID: string): Promise<void> {
    const book = this.books.find((item) => item.id === bookID);
    if (book && !book.scratchLoaded) await this.refresh(bookID);
  }

  private fillSheets(side: ReconcileSide, previous = ''): void {
    const book = this.books.find((item) => item.id === this.bookSelect(side).value);
    const node = this.sheetSelect(side);
    const selected = previous || node.value;
    node.replaceChildren();
    for (const sheet of book?.sheets ?? []) {
      node.append(new Option(sheet.name, `wb:${sheet.name}`));
    }
    for (const sheet of book?.scratch ?? []) {
      node.append(new Option(`${sheet.name}（编辑表，已保存）`, `scratch:${sheet.id}`));
    }
    if ([...node.options].some((option) => option.value === selected)) node.value = selected;
    const scratch = node.value.startsWith('scratch:');
    const header = this.headerInput(side);
    header.disabled = scratch || this.busy;
    if (scratch) header.value = '0';
    this.removeButton(side).hidden = !book?.attached;
    this.controls();
  }

  private invalidate(side: ReconcileSide): void {
    this.sides[side].controller?.abort();
    this.sides[side].serial++;
    this.sides[side].info = null;
    this.mapped = false;
    this.infoEl(side).textContent = '字段需重新读取';
    this.changed();
    this.controls();
  }

  private changed(): void {
    this.recordIntent++;
    if (this.result) {
      this.configChanged = true;
      this.renderSource();
    }
  }

  private async loadRecord(file: File): Promise<void> {
    const leftInfo = this.sides.left.info;
    const rightInfo = this.sides.right.info;
    if (!leftInfo || !rightInfo || this.busy) return;
    const targets: readonly [ReconcileSourceInfo, ReconcileSourceInfo] = [leftInfo, rightInfo];
    const epoch = this.epoch;
    const intent = ++this.recordIntent;
    const current = (): boolean =>
      epoch === this.epoch &&
      intent === this.recordIntent &&
      !this.busy &&
      this.sides.left.info === targets[0] &&
      this.sides.right.info === targets[1];
    try {
      if (file.size > this.recordBytes) throw new Error('核查记录超过容量');
      const rawRecord: unknown = JSON.parse(await file.text());
      if (!current()) return;
      const { parsed } = parseReconcileRecordFile(rawRecord);
      const query = rebindReconcileRecord(rawRecord, targets);
      requireEl('reconcileKeys').replaceChildren();
      for (const pair of query.keys) this.addMapping('keys', pair);
      requireEl('reconcileFields').replaceChildren();
      for (const pair of query.fields) this.addMapping('fields', pair);
      requireInput('reconcileIncludeEqual').checked = query.includeEqual;
      this.mapped = true;
      this.baseline = { record: parsed, targets, query };
      this.changed();
      this.progress('已载入核查规则，核对字段后点击“开始核对”');
      this.controls();
    } catch (error: unknown) {
      if (current()) this.progress(errorMessage(error), true);
    }
  }

  private ref(side: ReconcileSide): ReconcileSourceRef {
    const source = this.sheetSelect(side).value;
    const header = source.startsWith('scratch:') ? 0 : Number(this.headerInput(side).value);
    if (!Number.isInteger(header) || header < 0) {
      throw new Error('表头行号无效，0表示无表头');
    }
    const info = this.sides[side].info;
    return {
      book: this.bookSelect(side).value,
      source,
      headerRow: header,
      ...(info ? { stamp: info.stamp } : {}),
    };
  }

  private async loadFields(): Promise<void> {
    if (this.busy) return;
    await Promise.all(SIDES.map((side) => this.loadSide(side)));
  }

  async loadSide(side: ReconcileSide): Promise<void> {
    const state = this.sides[side];
    const serial = ++state.serial;
    const epoch = this.epoch;
    state.controller?.abort();
    const controller = new AbortController();
    state.controller = controller;
    state.info = null;
    this.mapped = false;
    this.controls();
    try {
      const ref = this.ref(side);
      if (!ref.book || !ref.source) throw new Error('请先选择文件和工作表');
      this.infoEl(side).textContent = '正在读取字段…';
      const info = await this.post('/api/reconcile/fields', ref, {
        signal: controller.signal,
      }).then(decodeReconcileSourceInfo);
      if (epoch !== this.epoch || serial !== this.sides[side].serial) return;
      state.info = info;
      this.infoEl(side).textContent =
        `${info.total.toLocaleString()} 条数据，${info.fields.length.toLocaleString()} 列${info.notice ? `；${info.notice}` : ''}`;
      this.options(side, info);
      if (this.ready() && !this.isMapped()) this.suggestMappings();
    } catch (error: unknown) {
      if (epoch === this.epoch && serial === this.sides[side].serial && !isAbortError(error)) {
        this.infoEl(side).textContent = errorMessage(error);
      }
    } finally {
      if (state.controller === controller) state.controller = null;
      this.controls();
    }
  }

  private isMapped(): boolean {
    return this.mapped;
  }

  private ready(): boolean {
    return this.sides.left.info !== null && this.sides.right.info !== null;
  }

  private options(side: ReconcileSide, info: ReconcileSourceInfo | null): void {
    const list = requireEl(side === 'left' ? 'reconcileLeftOptions' : 'reconcileRightOptions');
    list.replaceChildren();
    for (const field of info?.fields.slice(0, 512) ?? []) {
      const option = document.createElement('option');
      option.value = this.colName(field.column);
      option.label = field.name + (field.truncated ? '（字段名截取）' : '');
      list.append(option);
    }
  }

  private suggestMappings(): void {
    const leftInfo = this.sides.left.info;
    const rightInfo = this.sides.right.info;
    if (!leftInfo || !rightInfo) return;
    const pairs = suggestReconcilePairs(leftInfo.fields, rightInfo.fields);
    requireEl('reconcileKeys').replaceChildren();
    requireEl('reconcileFields').replaceChildren();
    for (const pair of pairs.keys.length ? pairs.keys : [{}]) this.addMapping('keys', pair);
    for (const pair of pairs.fields) this.addMapping('fields', pair);
    this.mapped = true;
    this.changed();
    this.controls();
    requireEl('reconcileSourceNotice').textContent = pairs.keys.length
      ? '已按唯一同名字段填入，请核对主键及核对方式。'
      : '没有可唯一匹配的字段，请输入列字母、序号或字段名。';
  }

  private addMapping(kind: 'keys' | 'fields', pair: Partial<ReconcilePair> = {}): void {
    const holder = requireEl(kind === 'keys' ? 'reconcileKeys' : 'reconcileFields');
    const limit = kind === 'keys' ? 4 : 8;
    if (holder.children.length >= limit) {
      this.toast(`最多${limit}组${kind === 'keys' ? '主键' : '比较字段'}`, 'warning');
      return;
    }
    const row = document.createElement('div');
    row.className = 'reconcile-mapping';
    for (const side of SIDES) {
      const input = document.createElement('input');
      input.type = 'text';
      input.className = `field-control reconcile-column-${side}`;
      input.setAttribute('list', side === 'left' ? 'reconcileLeftOptions' : 'reconcileRightOptions');
      input.setAttribute(
        'aria-label',
        `${side === 'left' ? '左侧' : '右侧'}${kind === 'keys' ? '主键字段' : '比较字段'}`,
      );
      input.placeholder = side === 'left' ? '左侧列或字段名' : '右侧列或字段名';
      const colVal = side === 'left' ? pair.left : pair.right;
      input.value = colVal ? this.colName(colVal) : '';
      input.addEventListener('input', () => this.changed());
      row.append(input);
    }

    const mode = document.createElement('select');
    mode.className = 'field-control reconcile-mode';
    mode.setAttribute('aria-label', '核对方式');
    const modeOptions: readonly (readonly [string, string])[] = [
      ['', '保留源类型'],
      ['text', '按显示文本'],
      ['number', '按精确数值'],
    ];
    for (const [value, label] of modeOptions) mode.append(new Option(label, value));
    mode.value = pair.mode ?? '';
    row.append(mode);

    const flagEntries: readonly (readonly ['trim' | 'ignoreCase', string])[] = [
      ['trim', '去首尾空格'],
      ['ignoreCase', '忽略大小写'],
    ];
    for (const [key, label] of flagEntries) {
      const wrap = document.createElement('label');
      const input = document.createElement('input');
      input.type = 'checkbox';
      input.className = `reconcile-${key}`;
      input.checked = Boolean(pair[key]);
      input.addEventListener('change', () => this.changed());
      wrap.append(input, document.createTextNode(label));
      row.append(wrap);
    }

    let threshold: HTMLLabelElement | null = null;
    if (kind === 'fields') {
      threshold = document.createElement('label');
      threshold.className = 'reconcile-threshold';
      const input = document.createElement('input');
      input.className = 'field-control reconcile-tolerance';
      input.type = 'text';
      input.inputMode = 'decimal';
      input.placeholder = '0';
      input.value = pair.tolerance ?? '';
      input.addEventListener('input', () => this.changed());
      threshold.append(document.createTextNode('绝对容差'), input);
      row.append(threshold);
    }

    const updateMode = (): void => {
      const numeric = mode.value === 'number';
      for (const checkbox of row.querySelectorAll<HTMLInputElement>('input[type=checkbox]')) {
        if (checkbox.parentElement) checkbox.parentElement.hidden = numeric;
        checkbox.disabled = numeric || this.busy;
        if (numeric) checkbox.checked = false;
      }
      if (threshold) threshold.hidden = !numeric;
    };
    updateMode();
    mode.addEventListener('change', () => {
      updateMode();
      this.changed();
    });

    const remove = document.createElement('button');
    remove.type = 'button';
    remove.textContent = '×';
    remove.title = '移除此组字段';
    remove.addEventListener('click', () => {
      if (kind === 'keys' && holder.children.length === 1) {
        this.toast('至少保留一组主键', 'warning');
        return;
      }
      row.remove();
      this.changed();
      this.controls();
    });
    row.append(remove);
    holder.append(row);
    this.controls();
  }

  private mappings(kind: 'keys' | 'fields'): ReconcilePair[] {
    const leftInfo = this.sides.left.info;
    const rightInfo = this.sides.right.info;
    if (!leftInfo || !rightInfo) throw new Error('请先读取两侧字段');
    const rows = Array.from(requireEl(kind === 'keys' ? 'reconcileKeys' : 'reconcileFields').children);
    const seenLeft = new Set<number>();
    const seenRight = new Set<number>();
    return rows.map((row) => {
      const leftInput = row.querySelector('.reconcile-column-left');
      const rightInput = row.querySelector('.reconcile-column-right');
      const modeSelect = row.querySelector('.reconcile-mode');
      const trimCheck = row.querySelector('.reconcile-trim');
      const caseCheck = row.querySelector('.reconcile-ignoreCase');
      const leftVal = leftInput instanceof HTMLInputElement ? leftInput.value : '';
      const rightVal = rightInput instanceof HTMLInputElement ? rightInput.value : '';
      const left = resolveReconcileColumn(leftVal, leftInfo.fields);
      const right = resolveReconcileColumn(rightVal, rightInfo.fields);
      if (seenLeft.has(left) || seenRight.has(right)) {
        throw new Error('主键或比较字段重复，请移除重复项');
      }
      seenLeft.add(left);
      seenRight.add(right);
      const pair: ReconcilePair = {
        left,
        right,
        mode: modeSelect instanceof HTMLSelectElement ? modeSelect.value : '',
        trim: trimCheck instanceof HTMLInputElement ? trimCheck.checked : false,
        ignoreCase: caseCheck instanceof HTMLInputElement ? caseCheck.checked : false,
      };
      if (kind === 'fields' && pair.mode === 'number') {
        const tolInput = row.querySelector('.reconcile-tolerance');
        const text = tolInput instanceof HTMLInputElement ? tolInput.value.trim() : '';
        if (text && !decimalTolerance(text)) throw new Error('金额容差须填写非负数字');
        if (text) pair.tolerance = text;
      }
      return pair;
    });
  }

  controls(): void {
    const uploading = this.pendingUpload !== null;
    const locked = this.busy || uploading;
    const ready = this.ready();
    for (const id of ['reconcileRefresh', 'reconcileAddFile', 'reconcileSwap', 'reconcileLoadFields']) {
      requireButton(id).disabled = locked;
    }
    requireInput('reconcileIncludeEqual').disabled = locked;
    requireButton('reconcileMatchFields').disabled = locked || !ready;
    requireButton('reconcileRun').disabled = locked || !ready;
    requireButton('reconcileImport').disabled = locked || !ready;
    requireButton('reconcileBundle').disabled = locked || !this.job || Boolean(this.result?.recordError);
    requireButton('reconcileCancel').hidden = !this.busy;
    requireButton('reconcileClear').disabled = locked;
    requireButton('reconcileExport').disabled = locked || !this.job;

    for (const side of SIDES) {
      this.bookSelect(side).disabled = locked;
      this.sheetSelect(side).disabled = locked;
      this.removeButton(side).disabled = locked;
      this.headerInput(side).disabled = locked || this.sheetSelect(side).value.startsWith('scratch:');
    }
    for (const kind of ['keys', 'fields'] as const) {
      const holder = requireEl(kind === 'keys' ? 'reconcileKeys' : 'reconcileFields');
      for (const node of holder.querySelectorAll<HTMLInputElement | HTMLSelectElement | HTMLButtonElement>(
        'input,select,button',
      )) {
        node.disabled = locked;
      }
      for (const row of Array.from(holder.children)) {
        const modeSelect = row.querySelector('.reconcile-mode');
        if (modeSelect instanceof HTMLSelectElement && modeSelect.value === 'number') {
          for (const box of row.querySelectorAll<HTMLInputElement>('input[type=checkbox]')) {
            box.disabled = true;
          }
        }
      }
      requireButton(kind === 'keys' ? 'reconcileAddKey' : 'reconcileAddField').disabled =
        locked || !ready || holder.children.length >= (kind === 'keys' ? 4 : 8);
    }
  }

  private progress(text: string, error = false): void {
    const el = requireEl('reconcileProgress');
    el.hidden = false;
    el.classList.toggle('is-error', error);
    el.textContent = text;
  }

  async run(): Promise<void> {
    if (this.busy || !this.ready() || !this.canRun('核对')) return;
    let request: {
      workbookKey: string;
      primaryBook: string;
      left: ReconcileSourceRef;
      right: ReconcileSourceRef;
      query: {
        ruleVersion?: number;
        keys: ReconcilePair[];
        fields: ReconcilePair[];
        includeEqual: boolean;
      };
    };
    try {
      request = {
        workbookKey: this.s.workbookKey,
        primaryBook: this.primaryBook,
        left: this.ref('left'),
        right: this.ref('right'),
        query: {
          keys: this.mappings('keys'),
          fields: this.mappings('fields'),
          includeEqual: requireInput('reconcileIncludeEqual').checked,
        },
      };
      if (!request.query.keys.length) throw new Error('请选择主键');
    } catch (error: unknown) {
      this.toast(errorMessage(error), 'warning');
      return;
    }

    const epoch = this.epoch;
    this.progress('正在开始核对…');
    this.changed();
    const base = this.baseline;
    const comparable =
      base !== null &&
      this.sides.left.info === base.targets[0] &&
      this.sides.right.info === base.targets[1] &&
      JSON.stringify(request.query) === JSON.stringify(base.query);
    if (comparable) request.query.ruleVersion = base.record.ruleVersion;
    try {
      const completed = await this.jobs.execute(request, { main: true });
      if (!completed.current() || epoch !== this.epoch) return;
      this.resultBaseline = comparable ? base.record : null;
      if (comparable) {
        requireSelect('reconcileResultFilter').value = base.record.detail.status;
        requireInput('reconcileProtectCSV').checked = base.record.detail.protectCSV;
      }
      const previous = this.job;
      this.job = completed.job;
      this.result = completed.result;
      this.configChanged = false;
      requireEl('reconcileResults').hidden = false;
      this.progress('核对完成');
      this.renderResult();
      if (previous && previous !== this.job) {
        this.post('/api/reconcile/clear', { id: previous }).catch(() => {
          // Best-effort cleanup of superseded job.
        });
      }
    } catch (error: unknown) {
      if (epoch === this.epoch && !isAbortError(error)) this.progress(errorMessage(error), true);
    } finally {
      this.controls();
    }
  }

  async reopenJob(job: string, status: ReconcileJobStatus): Promise<void> {
    await this.open();
    if (!status.result) return;
    this.job = job;
    this.result = {
      ...status.result,
      recordError: status.recordError,
      exceptionGroupsSHA256: status.exceptionGroupsSHA256,
    };
    this.resultBaseline = null;
    this.configChanged = false;
    requireEl('reconcileResults').hidden = false;
    this.progress('核对完成');
    this.renderResult();
    this.controls();
  }

  private async upload(file: File | undefined): Promise<void> {
    if (!file || this.busy || this.pendingUpload) return;
    const epoch = this.epoch;
    const controller = new AbortController();
    this.pendingUpload = controller;
    this.controls();
    try {
      const form = new FormData();
      form.append('file', file, file.name);
      form.append('reconcileSource', '1');
      form.append('workbookKey', this.s.workbookKey);
      form.append('primaryBook', this.primaryBook);
      requireEl('reconcileSourceNotice').textContent = '正在添加核对文件…';
      const result = await this.api('/api/open', {
        method: 'POST',
        body: form,
        signal: controller.signal,
      }, decodeReconcileUploadResponse);
      if (epoch !== this.epoch) return;
      await this.refresh(result.book);
      if (epoch !== this.epoch) return;
      this.bookSelect('right').value = result.book;
      this.fillSheets('right');
      this.invalidate('right');
      requireEl('reconcileSourceNotice').textContent = `已添加 ${result.name}，请读取字段。`;
    } catch (error: unknown) {
      if (epoch === this.epoch && !isAbortError(error)) {
        const msg = errorMessage(error);
        requireEl('reconcileSourceNotice').textContent = msg;
        this.toast(msg, 'error');
      }
    } finally {
      if (this.pendingUpload === controller) this.pendingUpload = null;
      this.controls();
    }
  }

  private async removeSource(side: ReconcileSide): Promise<void> {
    if (this.busy) return;
    const id = this.bookSelect(side).value;
    try {
      await this.post('/api/reconcile/source/remove', { book: id });
      this.invalidate(side);
      await this.refresh();
    } catch (error: unknown) {
      this.toast(errorMessage(error), 'error');
    }
  }

  private swap(): void {
    if (this.busy) return;
    let pairs: { readonly keys: ReconcilePair[]; readonly fields: ReconcilePair[] } | null;
    try {
      pairs = this.ready()
        ? { keys: this.mappings('keys'), fields: this.mappings('fields') }
        : null;
    } catch (error: unknown) {
      this.toast(errorMessage(error), 'warning');
      return;
    }
    const values = SIDES.map((side) => ({
      book: this.bookSelect(side).value,
      sheet: this.sheetSelect(side).value,
      header: this.headerInput(side).value,
      info: this.sides[side].info,
    }));
    for (let i = 0; i < 2; i++) {
      const side = SIDES[i];
      const value = values[1 - i];
      if (!side || !value) continue;
      this.sides[side].controller?.abort();
      this.sides[side].serial++;
      this.sides[side].info = value.info;
      this.bookSelect(side).value = value.book;
      this.fillSheets(side, value.sheet);
      this.headerInput(side).value = value.header;
      this.infoEl(side).textContent = value.info
        ? `${value.info.total.toLocaleString()} 条数据，${value.info.fields.length} 列`
        : '字段需重新读取';
      this.options(side, value.info);
    }
    if (pairs) {
      requireEl('reconcileKeys').replaceChildren();
      requireEl('reconcileFields').replaceChildren();
      for (const pair of pairs.keys) {
        this.addMapping('keys', { ...pair, left: pair.right, right: pair.left });
      }
      for (const pair of pairs.fields) {
        this.addMapping('fields', { ...pair, left: pair.right, right: pair.left });
      }
    }
    this.changed();
    this.controls();
  }

  private renderSource(): void {
    if (!this.result) return;
    const r = this.result;
    const parts = [`左：${r.left.name} / ${r.left.sheet}　右：${r.right.name} / ${r.right.sheet}`];
    if (this.resultBaseline) {
      const base = this.resultBaseline;
      parts.push(`基线：${base.inputs.map((input) => `${input.name} / ${input.sheet}`).join('、')}`);
      if (r.exceptionGroupsSHA256) {
        parts.push(
          `有效异常主键、分类及组内笔数${r.exceptionGroupsSHA256 === base.exceptionGroupsSHA256 ? '相同' : '有变化'}`,
        );
      }
    }
    if (r.recordError) parts.push(`核查记录不可用：${r.recordError}`);
    if (this.configChanged) parts.push('配置已更改，以下仍为上次结果。');
    requireEl('reconcileResultSource').textContent = parts.join('　');
  }

  private renderResult(): void {
    if (!this.result) return;
    this.renderSource();
    this.resultFields = {
      left: new Map(this.result.left.fields.map((field) => [field.column, field])),
      right: new Map(this.result.right.fields.map((field) => [field.column, field])),
    };
    const holder = requireEl('reconcileCounts');
    holder.replaceChildren();
    const counts = this.result.counts;
    const hasTolerance = this.result.query.fields.some((pair) => Boolean(pair.tolerance));
    for (const [key, label] of COUNT_LABEL_ENTRIES) {
      if (key === 'withinTolerance' && !hasTolerance) continue;
      const span = document.createElement('span');
      const value = document.createElement('b');
      span.textContent = label;
      const countVal = counts[key] || 0;
      value.textContent = countVal.toLocaleString();
      span.append(value);
      if (this.resultBaseline) {
        const delta = countVal - (this.resultBaseline.counts[key] ?? 0);
        span.append(document.createTextNode(`（较基线 ${delta > 0 ? '+' : ''}${delta}）`));
      }
      holder.append(span);
    }
    requireEl('reconcileSampleNote').textContent =
      `显示${this.result.samples.length}组样例${this.result.samplesTruncated ? '，完整结果请导出CSV' : ''}；长文本仅截取显示。`;
    this.renderSamples();
  }

  private sourceButton(
    side: ReconcileSide,
    row: number,
    column: number,
    text: string,
  ): HTMLButtonElement {
    const button = document.createElement('button');
    button.type = 'button';
    button.textContent = text;
    button.title = `定位${side === 'left' ? '左' : '右'}侧 ${this.colName(column)}${row}`;
    button.addEventListener('click', () => {
      void this.goSource(side, row, column);
    });
    return button;
  }

  private renderSamples(): void {
    if (!this.result) return;
    const table = requireEl('reconcileTable');
    table.replaceChildren();
    const head = document.createElement('thead');
    const hr = document.createElement('tr');
    for (const label of ['结果', '主键', '左源行', '右源行', '字段与读取值', '差额 / 说明']) {
      const th = document.createElement('th');
      th.textContent = label;
      hr.append(th);
    }
    head.append(hr);
    table.append(head);

    const body = document.createElement('tbody');
    const filter = requireSelect('reconcileResultFilter').value;
    const groups = this.result.samples.filter((group) => !filter || group.status === filter);
    const query = this.result.query;
    const firstKey = query.keys[0];

    for (const group of groups) {
      const tr = document.createElement('tr');
      tr.dataset.status = group.status;
      const c0 = document.createElement('td');
      const c1 = document.createElement('td');
      const c2 = document.createElement('td');
      const c3 = document.createElement('td');
      const c4 = document.createElement('td');
      const c5 = document.createElement('td');

      const status = document.createElement('span');
      status.className = 'reconcile-status';
      status.dataset.status = group.status;
      status.textContent = STATUS_LABELS[group.status] ?? group.status;
      c0.append(status);
      c1.textContent = group.key.join(' / ');

      const sideCells: readonly [HTMLTableCellElement, ReconcileSide][] = [
        [c2, 'left'],
        [c3, 'right'],
      ];
      for (const [cell, side] of sideCells) {
        const rows = (side === 'left' ? group.leftRows : group.rightRows) ?? [];
        const count = side === 'left' ? group.leftCount : group.rightCount;
        const keyCol = firstKey ? firstKey[side] : 1;
        for (const row of rows) {
          cell.append(this.sourceButton(side, row, keyCol, String(row)), document.createTextNode(' '));
        }
        if (count > rows.length) {
          cell.append(document.createTextNode(`等${count.toLocaleString()}条`));
        }
      }

      if (group.status === 'duplicate') {
        c4.textContent = `左${group.leftCount}条，右${group.rightCount}条；未自动配对。`;
        c5.textContent = group.reason ?? '';
      } else {
        const pairs = query.fields.length ? query.fields : query.keys.slice(0, 1);
        const reasons: string[] = [];
        pairs.forEach((pair, i) => {
          const div = document.createElement('div');
          div.className = 'reconcile-value-pair';
          const title = document.createElement('strong');
          const lf = this.resultFields.left.get(pair.left);
          const rf = this.resultFields.right.get(pair.right);
          title.textContent = `${lf?.name || this.colName(pair.left)} / ${rf?.name || this.colName(pair.right)}`;
          div.append(title);
          for (const side of SIDES) {
            const record = side === 'left' ? group.left : group.right;
            if (!record) continue;
            const col = pair[side];
            const value = record.values[String(col)];
            const text = value?.missingCache
              ? '（公式缺少缓存）'
              : !value?.exists
                ? '（缺失单元格）'
                : value.raw === ''
                  ? '（空值）'
                  : value.raw;
            const button = this.sourceButton(
              side,
              record.number,
              col,
              `${side === 'left' ? '左：' : '右：'}${text}${value?.truncated ? '…' : ''}`,
            );
            button.title += `；读取类型：${value?.type || '隐式/空白'}${value?.typeMissing ? '（源类型未保留）' : ''}`;
            div.append(button, document.createElement('br'));
          }
          c4.append(div);
          const comparison = group.comparisons?.[i];
          if (comparison?.delta && comparison.delta !== '0') {
            reasons.push(
              `${title.textContent}：${comparison.delta}${comparison.withinTolerance ? '（容差内）' : ''}`,
            );
          }
          if (comparison?.reason) reasons.push(comparison.reason);
        });
        c5.textContent = [
          group.reason,
          ...reasons,
          group.textTruncated ? '样例文字已截取；CSV保留完整读取值。' : '',
        ]
          .filter(Boolean)
          .join('\n');
      }
      tr.append(c0, c1, c2, c3, c4, c5);
      body.append(tr);
    }

    if (!groups.length) {
      const row = document.createElement('tr');
      const cell = document.createElement('td');
      cell.colSpan = 6;
      cell.className = 'reconcile-empty';
      cell.textContent = filter ? '当前样例中没有此类记录。' : '没有需要显示的差异样例。';
      row.append(cell);
      body.append(row);
    }
    table.append(body);
  }

  async goSource(side: ReconcileSide, row: number, column: number): Promise<void> {
    if (!this.job) return;
    const epoch = this.epoch;
    const job = this.job;
    const sideInfo = this.result?.[side];
    const foreign = sideInfo?.id.split('/')[0] !== this.primaryBook;
    const popup = foreign ? window.open('about:blank', '_blank') : null;
    if (popup) popup.opener = null;
    let popupHandled = false;
    try {
      const target = await this.post('/api/reconcile/navigate', {
        jobId: job,
        side,
        row,
        col: column,
      }).then(decodeCompositionNavigationResponse);
      if (epoch !== this.epoch || job !== this.job) return;
      if (target.sameBook) {
        popup?.close();
        popupHandled = true;
        this.dialog.close();
        if (target.kind === 'analysis') {
          await this.navigateAnalysis?.(target.analysisSheet, row, column);
        } else {
          this.closeAnalysis?.();
          await this.navigate(target.sheet, `${this.colName(column)}${row}`);
        }
      } else if (popup) {
        popup.location.assign(target.url);
        popupHandled = true;
      } else {
        this.toast('浏览器阻止了新窗口，请允许弹出窗口后重试', 'warning');
      }
    } catch (error: unknown) {
      if (epoch === this.epoch) this.toast(errorMessage(error), 'error');
    } finally {
      if (popup && !popupHandled) popup.close();
    }
  }

  async clear(): Promise<void> {
    if (this.busy || !this.job) return;
    try {
      await this.post('/api/reconcile/clear', { id: this.job });
      this.job = '';
      this.result = null;
      requireEl('reconcileResults').hidden = true;
      this.controls();
    } catch (error: unknown) {
      this.toast(errorMessage(error), 'error');
    }
  }

  private async compareBatches(): Promise<void> {
    if (this.busy || this.pendingUpload) return;
    const before = requireInput('batchBaseline').files?.[0];
    const after = requireInput('batchCurrent').files?.[0];
    if (!before || !after) {
      this.progress('请选择基线和当期的完整核查包', true);
      return;
    }
    const files = new FormData();
    files.append('baseline', before);
    files.append('current', after);
    this.dialog.close();
    await this.onAuditExport?.(
      `/api/reconcile/batches?book=${encodeURIComponent(this.primaryBook)}`,
      files,
      `${before.name} / ${after.name}`,
    );
  }

  private async compareVersions(): Promise<void> {
    if (this.busy || this.pendingUpload) return;
    const left = { book: this.bookSelect('left').value, source: this.sheetSelect('left').value };
    const right = { book: this.bookSelect('right').value, source: this.sheetSelect('right').value };
    if (!left.book || !right.book) {
      this.progress('请选择两个来源文件', true);
      return;
    }
    const scope = requireSelect('versionScope').value;
    const pair = scope === 'pair' || scope === 'keyed';
    if (pair && (!left.source.startsWith('wb:') || !right.source.startsWith('wb:'))) {
      this.progress('请选择两侧的源工作表', true);
      return;
    }
    const request: {
      primaryBook: string;
      workbookKey: string;
      leftBook: string;
      rightBook: string;
      leftSheet: string;
      rightSheet: string;
      alignment?: {
        left: ReconcileSourceRef;
        right: ReconcileSourceRef;
        keys: ReconcilePair[];
      };
    } = {
      primaryBook: this.primaryBook,
      workbookKey: this.s.workbookKey,
      leftBook: left.book,
      rightBook: right.book,
      leftSheet: pair ? left.source.slice(3) : '',
      rightSheet: pair ? right.source.slice(3) : '',
    };
    if (scope === 'keyed') {
      if (!this.ready()) {
        this.progress('请先读取两侧字段并确认业务主键', true);
        return;
      }
      try {
        const keys = this.mappings('keys');
        if (!keys.length) throw new Error('请选择业务主键');
        request.alignment = { left: this.ref('left'), right: this.ref('right'), keys };
      } catch (error: unknown) {
        this.progress(errorMessage(error), true);
        return;
      }
    }
    const label = SIDES.map((side) => this.bookSelect(side).selectedOptions[0]?.textContent ?? '').join(' / ');
    this.dialog.close();
    await this.onAuditExport?.('/api/reconcile/versions', request, `版本比较 · ${label}`);
  }

  async export(bundle = false): Promise<void> {
    if (!this.job || !this.result || this.busy) return;
    const r = this.result;
    const status = bundle ? requireSelect('reconcileResultFilter').value : '';
    if (status === 'equal' && !r.query.includeEqual) {
      this.progress('请勾选“包含一致记录”后重新核对', true);
      return;
    }
    this.dialog.close();
    const label = `${r.left.name} / ${r.left.sheet} 与 ${r.right.name} / ${r.right.sheet}（${bundle ? `核查包 · ${STATUS_LABELS[status] ?? '全部类型'}` : '完整核对'}）`;
    await this.onExport?.(
      this.job,
      requireInput('reconcileProtectCSV').checked,
      label,
      bundle ? { format: 'zipcsv', status } : {},
    );
  }
}
