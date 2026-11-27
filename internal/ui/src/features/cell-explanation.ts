/**
 * Single-cell provenance, numeric eligibility, formula state, filter/sort context,
 * and presentation notice explanation dialog with strict response validation.
 */
import type { ApiClient, CellRef, ToastFn } from '../contracts/common.ts';
import type { CellExplanationResponse } from '../contracts/features.ts';
import type { CellExplanationLimits, SourceCapabilities } from '../contracts/formats.ts';
import type {
  CellData,
  IndexStartResponse,
  IndexStatusResponse,
} from '../contracts/workbook.ts';
import { errorMessage } from '../core/display-format.ts';
import { requireButton, requireDialog, requireEl } from '../core/dom.ts';
import {
  cellDisplayValue,
  cellFormulaText,
  cellSourceTypeLabel,
} from '../grid/cell-view.ts';
import type { WorkbookState } from '../grid/state.ts';

export interface CellExplanationRequest {
  readonly workbookKey: string;
  readonly book: string;
  readonly sheet: string;
  readonly viewId: string;
  readonly filterId: string;
  readonly ref: string;
  readonly pinnedThroughRow: number;
}

export interface CellExplanationOptions {
  readonly state: WorkbookState;
  readonly api: ApiClient;
  readonly toast: ToastFn;
  readonly limits: CellExplanationLimits;
  readonly source: () => SourceCapabilities;
  readonly parseRef: (value: string) => CellRef | null;
  readonly colName: (col: number) => string;
  readonly pinnedThroughRow: () => number;
  readonly canRead?: () => boolean;
  readonly notice?: (cell: CellData) => string;
}

interface CellExplanationBinding {
  readonly request: CellExplanationRequest;
  readonly index: IndexStartResponse | IndexStatusResponse | null;
  readonly key: string;
}

const REASONS = new Map<string, string>([
  ['number', '可用'],
  ['imported-amount', '可用（导入金额）'],
  ['source-text', '不参与（源为文本）'],
  ['text-or-identifier', '不参与（文本或编号）'],
  ['blank', '不参与（空白）'],
  ['missing-result', '不参与（公式无保存结果）'],
  ['invalid-number', '不参与（无效数值）'],
  ['error', '不参与（错误值）'],
  ['boolean', '不参与（布尔值）'],
  ['date', '不参与（日期文本）'],
  ['unsupported-type', '不参与（不支持的源类型）'],
]);

const ROW_STATES = new Map<string, string>([
  ['visible', '当前可见'],
  ['outside', '已用范围外'],
  ['hidden', '当前已隐藏'],
  ['header', '表头，不参与筛选'],
  ['pinned', '冻结保留行'],
  ['matched', '在筛选结果中'],
  ['filtered-out', '不在筛选结果中'],
]);

const OPERATORS = new Map<string, string>([
  ['contains', '包含'],
  ['notContains', '不包含'],
  ['equals', '等于'],
  ['notEquals', '不等于'],
  ['startsWith', '开头是'],
  ['endsWith', '结尾是'],
  ['gt', '大于'],
  ['gte', '大于等于'],
  ['lt', '小于'],
  ['lte', '小于等于'],
  ['blank', '为空'],
  ['notBlank', '非空'],
  ['in', '属于'],
]);

const SORT_MODE_LABELS: Readonly<Record<string, string>> = {
  number: '（数值）',
  text: '（文本）',
  natural: '（自然）',
};

function makeEl<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  cls = '',
  text = '',
): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag);
  node.className = cls;
  node.textContent = text;
  return node;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function isUnknownArray(value: unknown): value is readonly unknown[] {
  return Array.isArray(value);
}

function integer(n: unknown, min: number, max: number): n is number {
  return typeof n === 'number' && Number.isSafeInteger(n) && n >= min && n <= max;
}

function isAbortError(error: unknown): boolean {
  return error instanceof Error && error.name === 'AbortError';
}

function isValidCellObject(
  value: unknown,
  ref: string,
  col: number,
): value is CellData {
  if (!isRecord(value)) return false;
  if (
    value.ref !== ref ||
    value.col !== col ||
    typeof value.raw !== 'string' ||
    typeof value.display !== 'string' ||
    typeof value.hasFormula !== 'boolean' ||
    !integer(value.style, 0, Number.MAX_SAFE_INTEGER)
  ) {
    return false;
  }
  for (const key of ['formula', 'type'] as const) {
    if (value[key] !== undefined && typeof value[key] !== 'string') return false;
  }
  for (const key of ['cacheMissing', 'sharedFormula', 'presentationOnly'] as const) {
    if (value[key] !== undefined && typeof value[key] !== 'boolean') return false;
  }
  if (value.conditional !== undefined && value.conditional !== null) {
    if (!isRecord(value.conditional)) return false;
    if (
      value.conditional.display !== undefined &&
      value.conditional.display !== null &&
      typeof value.conditional.display !== 'string'
    ) {
      return false;
    }
  }
  return true;
}

function isValidExplanationPayload(
  page: unknown,
  request: CellExplanationRequest,
  limits: CellExplanationLimits | null | undefined,
  source: Pick<SourceCapabilities, 'maxRows' | 'maxColumns' | 'textAmounts'> | null | undefined,
  parseRef: (value: string) => CellRef | null,
): page is CellExplanationResponse {
  if (!isRecord(page) || !limits?.bytes || !source) return false;
  if (
    page.workbookKey !== request.workbookKey ||
    page.book !== request.book ||
    page.sheet !== request.sheet ||
    page.viewId !== request.viewId ||
    page.ref !== request.ref ||
    page.pinnedThroughRow !== request.pinnedThroughRow
  ) {
    return false;
  }
  const pageFilterId = typeof page.filterId === 'string' ? page.filterId : '';
  if (pageFilterId !== request.filterId) return false;

  const ref = parseRef(request.ref);
  if (
    !ref ||
    !integer(ref.row, 1, source.maxRows) ||
    !integer(ref.col, 1, source.maxColumns) ||
    !isValidCellObject(page.cell, request.ref, ref.col)
  ) {
    return false;
  }
  const cell = page.cell;
  const numeric = page.numeric;
  if (
    !isRecord(numeric) ||
    typeof numeric.eligible !== 'boolean' ||
    typeof numeric.reason !== 'string' ||
    !REASONS.has(numeric.reason) ||
    numeric.eligible !== (numeric.reason === 'number' || numeric.reason === 'imported-amount')
  ) {
    return false;
  }
  if (numeric.eligible) {
    const typeVal = cell.type;
    if (
      cell.cacheMissing ||
      cell.raw === '' ||
      typeof numeric.exact !== 'string' ||
      !numeric.exact ||
      (numeric.reason === 'number' && typeVal !== 'n' && typeVal !== '' && typeVal !== undefined) ||
      (numeric.reason === 'imported-amount' &&
        (!source.textAmounts ||
          (typeVal !== 's' && typeVal !== 'str' && typeVal !== 'inlineStr')))
    ) {
      return false;
    }
  } else if (numeric.exact !== undefined) {
    return false;
  }

  const expectedFormulaState = cell.hasFormula
    ? cell.cacheMissing
      ? 'missing'
      : 'cached'
    : 'none';
  if (
    page.formulaState !== expectedFormulaState ||
    page.formulaAvailable !== Boolean(cellFormulaText(cell)) ||
    typeof page.rowState !== 'string' ||
    !ROW_STATES.has(page.rowState)
  ) {
    return false;
  }
  if (page.presentationNotice !== undefined && typeof page.presentationNotice !== 'string') {
    return false;
  }

  if (page.filter !== undefined && page.filter !== null) {
    const filter = page.filter;
    if (
      !isRecord(filter) ||
      !request.filterId ||
      !integer(filter.headerRow, 0, source.maxRows) ||
      !isUnknownArray(filter.rules) ||
      !isUnknownArray(filter.sortKeys)
    ) {
      return false;
    }
    for (const rule of filter.rules) {
      if (
        !isRecord(rule) ||
        !integer(rule.column, 1, source.maxColumns) ||
        typeof rule.operator !== 'string' ||
        !OPERATORS.has(rule.operator) ||
        typeof rule.value !== 'string' ||
        (rule.values !== undefined &&
          (!isUnknownArray(rule.values) || rule.values.some((v) => typeof v !== 'string')))
      ) {
        return false;
      }
    }
    if (filter.sortColumn !== undefined && !integer(filter.sortColumn, 0, source.maxColumns)) {
      return false;
    }
    if (filter.descending !== undefined && typeof filter.descending !== 'boolean') return false;
    for (const sort of filter.sortKeys) {
      if (
        !isRecord(sort) ||
        !integer(sort.column, 1, source.maxColumns) ||
        typeof sort.descending !== 'boolean' ||
        (sort.mode !== undefined &&
          (typeof sort.mode !== 'string' ||
            !['', 'source', 'number', 'text', 'natural'].includes(sort.mode)))
      ) {
        return false;
      }
    }
  }
  return true;
}

export function validateCellExplanation(
  page: unknown,
  request: CellExplanationRequest,
  limits: CellExplanationLimits | null | undefined,
  source: Pick<SourceCapabilities, 'maxRows' | 'maxColumns' | 'textAmounts'> | null | undefined,
  parseRef: (value: string) => CellRef | null,
): CellExplanationResponse {
  if (!limits || !isValidExplanationPayload(page, request, limits, source, parseRef)) {
    throw new Error('单元格信息与当前来源不一致，请重试');
  }
  if (new TextEncoder().encode(JSON.stringify(page)).length > limits.bytes) {
    throw new Error('单元格信息超过显示容量，请使用导出');
  }
  return page;
}

export const CellExplanationModel = Object.freeze({
  validate: validateCellExplanation,
});

export class CellExplanation {
  private readonly s: WorkbookState;
  private readonly api: ApiClient;
  private readonly toast: ToastFn;
  private readonly limits: CellExplanationLimits;
  private readonly source: () => SourceCapabilities;
  private readonly parseRef: (value: string) => CellRef | null;
  private readonly colName: (col: number) => string;
  private readonly pinnedThroughRow: () => number;
  private readonly canRead: () => boolean;
  private readonly notice: (cell: CellData) => string;

  public readonly dialog: HTMLDialogElement;
  private serial = 0;
  private bound: CellExplanationBinding | null = null;
  public page: CellExplanationResponse | null = null;
  private abort: AbortController | null = null;

  constructor(options: CellExplanationOptions) {
    this.s = options.state;
    this.api = options.api;
    this.toast = options.toast;
    this.limits = options.limits;
    this.source = options.source;
    this.parseRef = options.parseRef;
    this.colName = options.colName;
    this.pinnedThroughRow = options.pinnedThroughRow;
    this.canRead = options.canRead ?? (() => true);
    this.notice = options.notice ?? (() => '');

    this.dialog = requireDialog('detailDialog');
    this.dialog.addEventListener('cancel', (event: Event) => {
      event.preventDefault();
      this.resetBook();
    });
    this.dialog.addEventListener('close', () => {
      if (!this.dialog.open) this.clear();
    });
    requireButton('detailRetry').addEventListener('click', () => {
      void this.load();
    });
  }

  public binding(): CellExplanationBinding {
    const req: CellExplanationRequest = {
      workbookKey: this.s.workbookKey,
      book: this.s.book,
      sheet: this.s.sheet ?? '',
      viewId: this.s.rowView?.id ?? '',
      filterId: this.s.filtered ? this.s.filterId : '',
      ref: this.s.selected?.ref ?? '',
      pinnedThroughRow: this.pinnedThroughRow(),
    };
    return {
      request: req,
      index: this.s.index,
      key: JSON.stringify([req, this.s.sheetEpoch, this.s.opened, this.s.indexReady]),
    };
  }

  private same(bound: CellExplanationBinding | null): boolean {
    const now = this.binding();
    return bound !== null && bound.key === now.key && bound.index === now.index;
  }

  enabled(): boolean {
    return (
      this.s.opened &&
      this.s.indexReady &&
      this.s.selected !== null &&
      Boolean(this.s.book) &&
      Boolean(this.s.rowView?.id) &&
      !this.s.opening &&
      !this.s.sheetLoadingEpoch &&
      !this.s.rowViewFailure &&
      this.canRead()
    );
  }

  private stop(): void {
    ++this.serial;
    this.abort?.abort();
    this.abort = null;
  }

  private clear(): void {
    this.stop();
    this.bound = null;
    this.page = null;
    const body = requireEl('detailBody');
    body.replaceChildren();
    requireEl('detailStatus').textContent = '';
    requireButton('detailRetry').hidden = true;
    body.setAttribute('aria-busy', 'false');
  }

  resetBook(): void {
    this.clear();
    if (this.dialog.open) this.dialog.close();
  }

  changed(): void {
    if (this.bound && !this.same(this.bound)) this.resetBook();
  }

  async open(): Promise<void> {
    if (!this.enabled()) {
      this.toast('请先完成工作表加载并选择单元格', 'warning');
      return;
    }
    this.clear();
    this.bound = this.binding();
    if (!this.dialog.open) this.dialog.showModal();
    await this.load();
  }

  async load(): Promise<void> {
    if (!this.dialog.open || !this.bound || !this.same(this.bound) || !this.enabled()) return;
    this.stop();
    const ticket = this.serial;
    const bound = this.bound;
    const controller = new AbortController();
    this.abort = controller;
    const current = (): boolean =>
      ticket === this.serial &&
      !controller.signal.aborted &&
      this.dialog.open &&
      this.same(bound);
    const status = requireEl('detailStatus');
    const retry = requireButton('detailRetry');
    const detailBody = requireEl('detailBody');
    status.textContent = '正在读取…';
    status.setAttribute('role', 'status');
    retry.hidden = true;
    detailBody.setAttribute('aria-busy', 'true');
    try {
      const body = JSON.stringify(bound.request);
      if (new TextEncoder().encode(body).length > this.limits.requestBytes) {
        throw new Error('单元格信息请求超过容量');
      }
      const response = await this.api('/api/cell/explain', {
        method: 'POST',
        body,
        signal: controller.signal,
      });
      if (!current()) return;
      const page = validateCellExplanation(
        response,
        bound.request,
        this.limits,
        this.source(),
        this.parseRef,
      );
      const fragment = this.build(page);
      if (!current()) return;
      this.page = page;
      detailBody.replaceChildren(fragment);
      status.textContent = '';
    } catch (error: unknown) {
      if (current() && !isAbortError(error)) {
        status.textContent = errorMessage(error);
        status.setAttribute('role', 'alert');
        retry.hidden = false;
      }
    } finally {
      if (ticket === this.serial) {
        this.abort = null;
        detailBody.setAttribute('aria-busy', 'false');
      }
    }
  }

  private build(page: CellExplanationResponse): DocumentFragment {
    const fragment = document.createDocumentFragment();
    const cell = page.cell;
    const add = (label: string, content: string | Node): void => {
      const value = makeEl('div', 'detail-value');
      if (typeof content === 'string') value.textContent = content;
      else value.append(content);
      fragment.append(makeEl('div', 'detail-label', label), value);
    };
    const displayText = cellDisplayValue(cell, false, this.s.showStyles);
    add('位置', `${page.sheet}!${page.ref}`);
    add(
      '显示值',
      cell.cacheMissing
        ? '未保存计算结果'
        : displayText !== ''
          ? displayText
          : cell.hasFormula
            ? '（空字符串）'
            : '（空白）',
    );
    add('原始值', cell.raw === '' ? '（空白）' : cell.raw);
    add('源类型', cellSourceTypeLabel(cell));
    add('数值统计', REASONS.get(page.numeric.reason) ?? '');
    if (page.numeric.eligible && page.numeric.exact && page.numeric.exact !== cell.raw) {
      add('统计数值', page.numeric.exact);
    }
    if (page.formulaState !== 'none') {
      add('公式', page.formulaAvailable ? (cellFormulaText(cell) ?? '原文无法还原') : '原文无法还原');
      add('计算结果', page.formulaState === 'missing' ? '未保存' : '已保存');
    }
    if (page.filter) {
      const filterRules = page.filter.rules ?? [];
      if (filterRules.length) {
        const rules = makeEl('div', 'cell-explanation-rules');
        for (const rule of filterRules) {
          const column = `${this.colName(rule.column)}列 `;
          const op = OPERATORS.get(rule.operator) ?? rule.operator;
          if (rule.operator === 'in') {
            const values = rule.values ?? [];
            const details = makeEl('details');
            const summary = makeEl('summary', '', `${column}${op} ${values.length.toLocaleString()} 项`);
            details.append(summary);
            details.addEventListener('toggle', () => {
              if (details.open && details.children.length === 1) {
                details.append(makeEl('pre', '', values.map((v) => JSON.stringify(v)).join('\n')));
              }
            });
            rules.append(details);
          } else {
            const suffix =
              rule.operator === 'blank' || rule.operator === 'notBlank'
                ? ''
                : ` ${JSON.stringify(rule.value)}`;
            rules.append(makeEl('div', '', `${column}${op}${suffix}`));
          }
        }
        add('已应用筛选', rules);
        add('当前行', ROW_STATES.get(page.rowState) ?? '');
      }
      const sortKeys = page.filter.sortKeys ?? [];
      const sorts = sortKeys.length
        ? sortKeys
        : page.filter.sortColumn
          ? [{ column: page.filter.sortColumn, descending: Boolean(page.filter.descending), mode: undefined }]
          : [];
      if (sorts.length) {
        add(
          '已应用排序',
          sorts
            .map(
              (sort) =>
                `${this.colName(sort.column)}列 ${sort.descending ? '降序' : '升序'}${sort.mode ? (SORT_MODE_LABELS[sort.mode] ?? '') : ''}`,
            )
            .join('\n'),
        );
      }
    }
    const hasFilterRules = Boolean(page.filter?.rules?.length);
    if (!hasFilterRules && (page.rowState === 'hidden' || page.rowState === 'outside')) {
      add('当前行', ROW_STATES.get(page.rowState) ?? '');
    }
    if (cell.presentationOnly) add('源单元格', '源为空白，仅显示条件格式');
    if (cell.conditional?.display != null && cell.conditional.display !== cell.display) {
      add('源显示值', cell.display);
    }
    const notice =
      this.notice(cell) ||
      page.presentationNotice ||
      cell.conditional?.notice ||
      cell.richText?.notice;
    if (notice) add('呈现状态', notice);
    return fragment;
  }
}
