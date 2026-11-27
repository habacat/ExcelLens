/**
 * Print geometry validation and projection in exact integer nanopts.
 * Only final, bounded CSS pixels become JS Numbers; differences are computed
 * before any lossy conversion.
 */
import type { MergeRange } from '../contracts/common.ts';
import type {
  PrintCellBorders,
  PrintDimensions,
  PrintInterval,
  PrintMarginUnits,
  PrintOptions,
  PrintPage,
  PrintPageAxis,
  PrintPageCell,
  PrintPageContent,
  PrintPageRect,
  PrintPart,
  PrintSummary,
  SourcePrintProperty,
} from '../contracts/features.ts';
import type { PrintingLimits, SourceCapabilities } from '../contracts/formats.ts';
import type { CellData, VisualStyle } from '../contracts/workbook.ts';
import { columnName, columnNumber } from '../core/cell-address.ts';

export interface PrintBinding {
  readonly workbookKey: string;
  readonly book: string;
  readonly sheet: string;
}

export interface PrintFormInput {
  readonly defaults: boolean;
  readonly paper: string;
  readonly width: string;
  readonly height: string;
  readonly orientation: string;
  readonly scaleMode: string;
  readonly percent: string;
  readonly fitWidth: string;
  readonly fitHeight: string;
  readonly area: string;
  readonly repeatRows: string;
  readonly repeatColumns: string;
  readonly clearRepeatRows: boolean;
  readonly clearRepeatColumns: boolean;
  readonly marginMode: string;
  readonly left: string;
  readonly right: string;
  readonly top: string;
  readonly bottom: string;
  readonly header: string;
  readonly footer: string;
  readonly pageOrder: string;
  readonly firstPage: string;
  readonly rowHeight: string;
  readonly columnWidth: string;
  readonly ignoreBreaks: boolean;
}

export type SourcePrintProperties = Readonly<
  Record<string, Readonly<Record<string, SourcePrintProperty>>>
>;

export interface PrintSummaryResponse {
  readonly id: string;
  readonly workbookKey: string;
  readonly book: string;
  readonly sheet: string;
  readonly summary: PrintSummary;
  readonly sourceComplete: boolean;
  readonly sourceProperties: SourcePrintProperties;
  readonly sourceNotices?: readonly string[];
}

export interface PrintPageResponse {
  readonly id: string;
  readonly workbookKey: string;
  readonly book: string;
  readonly sheet: string;
  readonly page: number;
  readonly content: PrintPageContent;
}

export interface ProjectedPrintCell {
  readonly item: PrintPageCell;
  readonly x: number;
  readonly y: number;
  readonly width: number;
  readonly height: number;
  readonly innerX: number;
  readonly innerY: number;
  readonly innerWidth: number;
  readonly innerHeight: number;
}

export interface ProjectedPrintAxis {
  readonly item: PrintPageAxis;
  readonly start: number;
  readonly size: number;
}

export interface ProjectedPrintPage {
  readonly width: number;
  readonly height: number;
  readonly x: number;
  readonly y: number;
  readonly contentWidth: number;
  readonly contentHeight: number;
  readonly scale: number;
  readonly cells: readonly ProjectedPrintCell[];
  readonly rows: readonly ProjectedPrintAxis[];
  readonly columns: readonly ProjectedPrintAxis[];
}

export interface RenderedPrintPage {
  readonly node: HTMLDivElement;
  readonly width: number;
  readonly height: number;
  readonly warnings: readonly string[];
}

const MAX64 = 9223372036854775807n;

function isInteger(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value);
}

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function isUnknownArray(value: unknown): value is readonly unknown[] {
  return Array.isArray(value);
}

function fail(): never {
  throw new Error('预览返回的页面数据无效，请重新准备');
}

export function printUnits(value: unknown, negative = false): bigint {
  if (typeof value !== 'string' || !/^(?:0|-?[1-9]\d*)$/.test(value) || value.length > 20) fail();
  const n = BigInt(value);
  if (n > MAX64 || n < -MAX64 || (!negative && n < 0n)) fail();
  return n;
}

function parseCellPoint(
  text: string,
  source: Pick<SourceCapabilities, 'maxRows' | 'maxColumns'>,
): { readonly row: number; readonly col: number } {
  const match = /^\$?([A-Za-z]+)\$?([1-9]\d*)$/.exec(text.trim());
  if (!match?.[1] || !match[2]) throw new Error(`区域地址无效：${text}`);
  const col = columnNumber(match[1]);
  const row = Number(match[2]);
  if (!isInteger(row) || !isInteger(col) || col < 1 || row > source.maxRows || col > source.maxColumns) {
    throw new Error(`区域地址超出此来源范围：${text}`);
  }
  return { row, col };
}

function parseList(text: string, maximum: number): string[] {
  const parts = text
    .trim()
    .split(/[,，;；]/)
    .map((s) => s.trim());
  if (parts.some((s) => !s) || parts.length > maximum) {
    throw new Error('区域数量超限或含空地址');
  }
  return parts;
}

export function parsePrintAreas(
  text: string,
  source: Pick<SourceCapabilities, 'maxRows' | 'maxColumns'>,
  limits: Pick<PrintingLimits, 'areas'>,
): MergeRange[] {
  return parseList(text, limits.areas).map((part) => {
    const pair = part.split(':');
    if (pair.length > 2 || !pair[0]) throw new Error(`区域地址无效：${part}`);
    const a = parseCellPoint(pair[0], source);
    const b = parseCellPoint(pair[1] || pair[0], source);
    if (b.row < a.row || b.col < a.col) throw new Error('区域终点不能在起点之前');
    return { row: a.row, col: a.col, endRow: b.row, endCol: b.col };
  });
}

export function parsePrintTitles(
  text: string,
  axis: 'row' | 'col',
  source: Pick<SourceCapabilities, 'maxRows' | 'maxColumns'>,
  limits: Pick<PrintingLimits, 'areas'>,
): PrintInterval[] {
  const maximum = axis === 'row' ? source.maxRows : source.maxColumns;
  return parseList(text, limits.areas).map((part) => {
    const pair = part
      .replaceAll('$', '')
      .split(':')
      .map((s) => s.trim());
    if (pair.length > 2 || !pair[0]) throw new Error('重复标题范围无效');
    const parseNum = (value: string): number =>
      axis === 'row' && /^[1-9]\d*$/.test(value)
        ? Number(value)
        : axis === 'col' && /^[a-z]+$/i.test(value)
          ? columnNumber(value)
          : NaN;
    const first = parseNum(pair[0]);
    const last = parseNum(pair[1] || pair[0]);
    if (!isInteger(first) || !isInteger(last) || first < 1 || last < first || last > maximum) {
      throw new Error('重复标题范围超出此来源或顺序无效');
    }
    return { first, last };
  });
}

const PAPERS: Readonly<Record<string, { readonly width: string; readonly height: string }>> = {
  a4: { width: '210mm', height: '297mm' },
  a3: { width: '297mm', height: '420mm' },
  letter: { width: '8.5in', height: '11in' },
  legal: { width: '8.5in', height: '14in' },
};

function parseDecimal(text: string, label: string, positive = true): string {
  const value = text.trim();
  if (
    value.length > 64 ||
    !/^(?:\d+(?:\.\d*)?|\.\d+)$/.test(value) ||
    !Number.isFinite(Number(value)) ||
    Number(value) < 0 ||
    (positive && Number(value) === 0)
  ) {
    throw new Error(`${label}请输入${positive ? '正数' : '不小于零的数'}`);
  }
  return value;
}

function parseCount(text: string, label: string, max: number): number {
  const value = text.trim();
  if (!/^\d+$/.test(value) || !isInteger(Number(value)) || Number(value) > max) {
    throw new Error(`${label}超出允许范围`);
  }
  return Number(value);
}

interface MutablePrintOptions {
  usePreviewDefaults: boolean;
  paper?: { readonly width: string; readonly height: string };
  orientation?: string;
  margins?: PrintMarginUnits;
  scalePercent?: string;
  fit?: { readonly width: number; readonly height: number };
  areas?: readonly MergeRange[];
  repeatRows?: readonly PrintInterval[];
  repeatColumns?: readonly PrintInterval[];
  missingRowHeight?: string;
  missingColumnWidth?: string;
  pageOrder?: string;
  firstPageNumber?: number;
  ignoreManualBreaks?: boolean;
}

export function buildPrintOptions(
  input: PrintFormInput,
  source: Pick<SourceCapabilities, 'maxRows' | 'maxColumns'> | null | undefined,
  limits: PrintingLimits,
): Partial<PrintOptions> & { readonly usePreviewDefaults: boolean } {
  if (!source || !isInteger(source.maxRows) || !isInteger(source.maxColumns)) {
    throw new Error('来源容量不可用，请重新打开工作表');
  }
  const out: MutablePrintOptions = { usePreviewDefaults: input.defaults };
  const presetPaper = Object.hasOwn(PAPERS, input.paper) ? PAPERS[input.paper] : undefined;
  if (input.paper === 'custom') {
    out.paper = {
      width: `${parseDecimal(input.width, '纸张宽度')}mm`,
      height: `${parseDecimal(input.height, '纸张高度')}mm`,
    };
  } else if (presetPaper) {
    out.paper = { ...presetPaper };
  } else if (input.paper !== 'source') {
    throw new Error('请选择有效纸张');
  }

  if (input.orientation) {
    if (input.orientation !== 'portrait' && input.orientation !== 'landscape') {
      throw new Error('请选择有效纸张方向');
    }
    out.orientation = input.orientation;
  }

  if (input.scaleMode === 'percent') {
    out.scalePercent = parseDecimal(input.percent, '打印缩放');
    if (Number(out.scalePercent) < 1 || Number(out.scalePercent) > 400) {
      throw new Error('打印缩放须在1%到400%之间');
    }
  } else if (input.scaleMode === 'fit') {
    out.fit = {
      width: parseCount(input.fitWidth, '横向页数', limits.pages),
      height: parseCount(input.fitHeight, '纵向页数', limits.pages),
    };
    if (!out.fit.width && !out.fit.height) {
      throw new Error('适配页数至少指定一个方向，0表示该方向不限');
    }
  } else if (input.scaleMode !== 'source') {
    throw new Error('请选择有效打印缩放方式');
  }

  if (input.area.trim()) {
    out.areas = parsePrintAreas(input.area, source, limits);
  }
  if (input.clearRepeatRows) {
    out.repeatRows = [];
  } else if (input.repeatRows.trim()) {
    out.repeatRows = parsePrintTitles(input.repeatRows, 'row', source, limits);
  }
  if (input.clearRepeatColumns) {
    out.repeatColumns = [];
  } else if (input.repeatColumns.trim()) {
    out.repeatColumns = parsePrintTitles(input.repeatColumns, 'col', source, limits);
  }

  if (input.marginMode === 'custom') {
    out.margins = {
      left: `${parseDecimal(input.left, '页边距', false)}mm`,
      right: `${parseDecimal(input.right, '页边距', false)}mm`,
      top: `${parseDecimal(input.top, '页边距', false)}mm`,
      bottom: `${parseDecimal(input.bottom, '页边距', false)}mm`,
      header: `${parseDecimal(input.header, '页边距', false)}mm`,
      footer: `${parseDecimal(input.footer, '页边距', false)}mm`,
    };
  } else if (input.marginMode !== 'source') {
    throw new Error('请选择有效页边距设置');
  }

  if (input.pageOrder) {
    if (input.pageOrder !== 'downThenOver' && input.pageOrder !== 'overThenDown') {
      throw new Error('请选择有效页序');
    }
    out.pageOrder = input.pageOrder;
  }

  if (input.firstPage.trim()) {
    if (!/^-?\d+$/.test(input.firstPage.trim())) throw new Error('起始页码请输入整数');
    out.firstPageNumber = Number(input.firstPage);
    if (
      !isInteger(out.firstPageNumber) ||
      out.firstPageNumber < limits.firstPageMin ||
      out.firstPageNumber > limits.firstPageMax
    ) {
      throw new Error('起始页码超出源格式范围');
    }
  }

  if (input.rowHeight.trim()) out.missingRowHeight = parseDecimal(input.rowHeight, '缺省行高');
  if (input.columnWidth.trim()) out.missingColumnWidth = parseDecimal(input.columnWidth, '缺省列宽');
  if (input.ignoreBreaks) out.ignoreManualBreaks = true;
  if (JSON.stringify(out).length > limits.requestBytes) throw new Error('页面设置超出本次容量');
  return out;
}

export function checkPrintEnvelope(
  value: unknown,
  binding: PrintBinding,
  id: string,
): Record<string, unknown> {
  if (
    !isObject(value) ||
    value.id !== id ||
    value.workbookKey !== binding.workbookKey ||
    value.book !== binding.book ||
    value.sheet !== binding.sheet
  ) {
    throw new Error('预览来源或任务已变化，请重新准备');
  }
  return value;
}

function validateNotices(value: unknown, limits: Pick<PrintingLimits, 'notices'>): string[] {
  if (value === undefined || value === null) return [];
  if (!isUnknownArray(value) || value.length > limits.notices || value.some((x) => typeof x !== 'string')) {
    fail();
  }
  return value.map((item) => item as string);
}

interface ParsedRect {
  readonly x: bigint;
  readonly y: bigint;
  readonly width: bigint;
  readonly height: bigint;
}

function parseRectangle(value: unknown, negative = false): ParsedRect {
  if (!isObject(value)) fail();
  return {
    x: printUnits(value.x, negative),
    y: printUnits(value.y, negative),
    width: printUnits(value.width),
    height: printUnits(value.height),
  };
}

function validateDimensions(value: unknown): PrintDimensions {
  if (!isObject(value)) fail();
  printUnits(value.width);
  printUnits(value.height);
  return { width: value.width as string, height: value.height as string };
}

function validateRange(
  value: unknown,
  source: Pick<SourceCapabilities, 'maxRows' | 'maxColumns'>,
): MergeRange {
  if (
    !isObject(value) ||
    !isInteger(value.row) ||
    !isInteger(value.col) ||
    !isInteger(value.endRow) ||
    !isInteger(value.endCol) ||
    value.row < 1 ||
    value.col < 1 ||
    value.endRow < value.row ||
    value.endCol < value.col ||
    value.endRow > source.maxRows ||
    value.endCol > source.maxColumns
  ) {
    fail();
  }
  return {
    row: value.row,
    col: value.col,
    endRow: value.endRow,
    endCol: value.endCol,
  };
}

function validateSourceProperties(raw: unknown): SourcePrintProperties {
  if (!isObject(raw)) fail();
  const result: Record<string, Record<string, SourcePrintProperty>> = {};
  for (const [group, fields] of Object.entries(raw)) {
    if (!isObject(fields)) fail();
    const groupFields: Record<string, SourcePrintProperty> = {};
    for (const [name, entry] of Object.entries(fields)) {
      if (!isObject(entry)) fail();
      groupFields[name] = {
        type: typeof entry.type === 'string' ? entry.type : '',
        value: typeof entry.value === 'string' ? entry.value : '',
        status: typeof entry.status === 'string' ? entry.status : '',
        reason: typeof entry.reason === 'string' ? entry.reason : undefined,
      };
    }
    result[group] = groupFields;
  }
  return result;
}

function validateIntervals(raw: unknown): PrintInterval[] {
  if (!isUnknownArray(raw)) return [];
  const out: PrintInterval[] = [];
  for (const item of raw) {
    if (isObject(item) && isInteger(item.first) && isInteger(item.last)) {
      out.push({ first: item.first, last: item.last });
    }
  }
  return out;
}

export function validatePrintSummary(
  value: unknown,
  binding: PrintBinding,
  id: string,
  limits: PrintingLimits,
  source: Pick<SourceCapabilities, 'maxRows' | 'maxColumns'>,
): PrintSummaryResponse {
  const envelopeObj = checkPrintEnvelope(value, binding, id);
  const s = envelopeObj.summary;
  if (
    !isObject(s) ||
    !isInteger(s.pages) ||
    s.pages < 0 ||
    s.pages > limits.pages ||
    s.sheet !== binding.sheet ||
    typeof s.sourceId !== 'string' ||
    !/^[a-f0-9]{64}$/.test(s.sourceId) ||
    typeof envelopeObj.sourceComplete !== 'boolean' ||
    !isObject(envelopeObj.sourceProperties)
  ) {
    fail();
  }
  validateNotices(s.notices, limits);
  validateNotices(envelopeObj.sourceNotices, limits);
  const paper = validateDimensions(s.paper);
  validateDimensions(s.headings);

  if (!isObject(s.origins) || Object.values(s.origins).some((x) => typeof x !== 'string')) fail();

  if (s.areas !== undefined && s.areas !== null && !isUnknownArray(s.areas)) fail();
  const rawAreas = isUnknownArray(s.areas) ? s.areas : [];
  if (rawAreas.length > limits.areas) fail();
  const areas = rawAreas.map((a) => validateRange(a, source));

  if (s.pages) {
    const rawScale = s.scale;
    if (
      !isInteger(s.firstPageNumber) ||
      s.firstPageNumber < limits.firstPageMin ||
      s.firstPageNumber > limits.firstPageMax ||
      !isObject(rawScale) ||
      typeof rawScale.numerator !== 'string' ||
      typeof rawScale.denominator !== 'string' ||
      !/^[1-9]\d{0,127}$/.test(rawScale.numerator) ||
      !/^[1-9]\d{0,127}$/.test(rawScale.denominator)
    ) {
      fail();
    }
    const n = BigInt(rawScale.numerator);
    const d = BigInt(rawScale.denominator);
    if (
      n > 4n * d ||
      printUnits(paper.width) === 0n ||
      printUnits(paper.height) === 0n ||
      !areas.length
    ) {
      fail();
    }
  }

  if (!isObject(s.margins)) fail();
  const marginKeys = ['left', 'right', 'top', 'bottom', 'header', 'footer'] as const;
  for (const k of marginKeys) printUnits(s.margins[k]);

  validateIntervals(s.repeatRows);
  validateIntervals(s.repeatColumns);
  validateSourceProperties(envelopeObj.sourceProperties);

  return value as PrintSummaryResponse;
}

function validateAxis(
  items: unknown,
  parts: unknown,
  kind: 'row' | 'col',
  source: Pick<SourceCapabilities, 'maxRows' | 'maxColumns'>,
  limits: PrintingLimits,
): { readonly total: bigint; readonly items: PrintPageAxis[]; readonly parts: PrintPart[] } {
  const maximum = kind === 'row' ? source.maxRows : source.maxColumns;
  const cap = kind === 'row' ? limits.rows : limits.columns;
  if (
    !isUnknownArray(items) ||
    !items.length ||
    items.length > cap ||
    !isUnknownArray(parts) ||
    !parts.length ||
    parts.length > limits.parts
  ) {
    fail();
  }
  let total = 0n;
  const offsets: bigint[] = [];
  const validatedParts: PrintPart[] = [];
  for (const p of parts) {
    if (
      !isObject(p) ||
      !isInteger(p.first) ||
      !isInteger(p.last) ||
      p.first < 1 ||
      p.last < p.first ||
      p.last > maximum ||
      typeof p.repeated !== 'boolean'
    ) {
      fail();
    }
    offsets.push(total);
    printUnits(p.offset);
    const length = printUnits(p.length);
    if (length === 0n) fail();
    total += length;
    if (total > MAX64) fail();
    validatedParts.push({
      first: p.first,
      last: p.last,
      offset: p.offset as string,
      length: p.length as string,
      repeated: p.repeated,
    });
  }
  let end = 0n;
  let lastPart = -1;
  let lastNumber = 0;
  const validatedItems: PrintPageAxis[] = [];
  for (const i of items) {
    if (
      !isObject(i) ||
      !isInteger(i.number) ||
      i.number < 1 ||
      i.number > maximum ||
      !isInteger(i.part) ||
      i.part < 0 ||
      i.part >= validatedParts.length ||
      typeof i.repeated !== 'boolean'
    ) {
      fail();
    }
    const partObj = validatedParts[i.part];
    const partOffset = offsets[i.part];
    if (
      !partObj ||
      partOffset === undefined ||
      i.repeated !== partObj.repeated ||
      i.number < partObj.first ||
      i.number > partObj.last ||
      i.part < lastPart ||
      (i.part === lastPart && i.number <= lastNumber)
    ) {
      fail();
    }
    const start = printUnits(i.start, true);
    const size = printUnits(i.size);
    const clip = printUnits(i.clipStart);
    const length = printUnits(i.clipSize);
    if (
      size === 0n ||
      length === 0n ||
      clip !== end ||
      clip < start ||
      clip + length > start + size ||
      clip < partOffset ||
      clip + length > partOffset + printUnits(partObj.length)
    ) {
      fail();
    }
    end = clip + length;
    lastPart = i.part;
    lastNumber = i.number;
    validatedItems.push({
      number: i.number,
      part: i.part,
      start: i.start as string,
      size: i.size as string,
      clipStart: i.clipStart as string,
      clipSize: i.clipSize as string,
      repeated: i.repeated,
    });
  }
  if (end !== total) fail();
  return { total, items: validatedItems, parts: validatedParts };
}

function validateCellRect(raw: unknown): PrintPageRect {
  if (!isObject(raw)) fail();
  return {
    x: raw.x as string,
    y: raw.y as string,
    width: raw.width as string,
    height: raw.height as string,
  };
}

function validateBorders(raw: unknown): PrintCellBorders | undefined {
  if (raw === undefined || raw === null) return undefined;
  if (!isObject(raw)) fail();
  return {
    borderTop: typeof raw.borderTop === 'string' ? raw.borderTop : undefined,
    borderRight: typeof raw.borderRight === 'string' ? raw.borderRight : undefined,
    borderBottom: typeof raw.borderBottom === 'string' ? raw.borderBottom : undefined,
    borderLeft: typeof raw.borderLeft === 'string' ? raw.borderLeft : undefined,
  };
}

function isValidPrintCellData(value: unknown, ref: string, col: number): value is CellData {
  return (
    isObject(value) &&
    value.ref === ref &&
    value.col === col &&
    typeof value.raw === 'string' &&
    typeof value.display === 'string' &&
    typeof value.hasFormula === 'boolean' &&
    isInteger(value.style)
  );
}

export function validatePrintPage(
  value: unknown,
  binding: PrintBinding,
  id: string,
  index: number,
  summaryValue: PrintSummaryResponse,
  limits: PrintingLimits,
  source: Pick<SourceCapabilities, 'maxRows' | 'maxColumns'>,
): PrintPageResponse {
  const envelopeObj = checkPrintEnvelope(value, binding, id);
  if (
    envelopeObj.page !== index ||
    !isInteger(index) ||
    index < 0 ||
    index >= summaryValue.summary.pages
  ) {
    fail();
  }
  const content = envelopeObj.content;
  const s = summaryValue.summary;
  if (!isObject(content) || !isObject(content.page)) fail();
  const p = content.page;
  const areaCount = s.areas?.length ?? 0;
  if (
    p.number !== s.firstPageNumber + index ||
    !isInteger(p.area) ||
    p.area < 1 ||
    p.area > areaCount ||
    !isUnknownArray(content.cells) ||
    content.cells.length > limits.cells ||
    !isObject(content.styles)
  ) {
    fail();
  }
  const pageNotices = validateNotices(content.notices, limits);
  const contentDims = validateDimensions(p.content);
  const bodyDims = validateDimensions(p.body);
  printUnits(p.offsetX);
  printUnits(p.offsetY);

  const rowAxis = validateAxis(content.rows, p.rows, 'row', source, limits);
  const colAxis = validateAxis(content.columns, p.columns, 'col', source, limits);
  const height = rowAxis.total;
  const width = colAxis.total;

  const validatedCells: PrintPageCell[] = [];
  for (const i of content.cells) {
    if (
      !isObject(i) ||
      !isInteger(i.row) ||
      !isInteger(i.col) ||
      i.row < 1 ||
      i.col < 1 ||
      i.row > source.maxRows ||
      i.col > source.maxColumns ||
      i.ref !== `${columnName(i.col)}${i.row}` ||
      !isInteger(i.style) ||
      i.style < 0
    ) {
      fail();
    }
    const b = parseRectangle(i.box, true);
    const c = parseRectangle(i.clip);
    if (
      b.width === 0n ||
      b.height === 0n ||
      c.width === 0n ||
      c.height === 0n ||
      c.x < b.x ||
      c.y < b.y ||
      c.x + c.width > b.x + b.width ||
      c.y + c.height > b.y + b.height ||
      c.x + c.width > width ||
      c.y + c.height > height
    ) {
      fail();
    }
    let merge: MergeRange | undefined;
    if (i.merge !== undefined && i.merge !== null) {
      merge = validateRange(i.merge, source);
      if (merge.row !== i.row || merge.col !== i.col) fail();
    }
    if (
      i.hiddenAnchor !== undefined &&
      i.hiddenAnchor !== null &&
      (typeof i.hiddenAnchor !== 'boolean' || (i.hiddenAnchor && !merge))
    ) {
      fail();
    }
    let cell: CellData | undefined;
    if (i.cell !== undefined && i.cell !== null) {
      if (!isValidPrintCellData(i.cell, i.ref, i.col)) {
        fail();
      }
      cell = i.cell;
    }
    validatedCells.push({
      row: i.row,
      col: i.col,
      ref: i.ref,
      cell,
      style: i.style,
      box: validateCellRect(i.box),
      clip: validateCellRect(i.clip),
      merge,
      hiddenAnchor: typeof i.hiddenAnchor === 'boolean' ? i.hiddenAnchor : undefined,
      borders: validateBorders(i.borders),
    });
  }

  const pageObj: PrintPage = {
    number: p.number,
    area: p.area,
    rows: rowAxis.parts,
    columns: colAxis.parts,
    body: bodyDims,
    content: contentDims,
    offsetX: p.offsetX as string,
    offsetY: p.offsetY as string,
    oversize: p.oversize === true,
  };
  const styles = content.styles as Readonly<Record<string, VisualStyle>>;
  const pageContent: PrintPageContent = {
    page: pageObj,
    rows: rowAxis.items,
    columns: colAxis.items,
    cells: validatedCells,
    styles,
    notices: pageNotices,
  };
  return {
    id,
    workbookKey: binding.workbookKey,
    book: binding.book,
    sheet: binding.sheet,
    page: index,
    content: pageContent,
  };
}

export function printPixels(
  value: string | bigint,
  limits: Pick<PrintingLimits, 'unitsPerPoint' | 'domPixels'>,
  n = 1n,
  d = 1n,
): number {
  const amount = typeof value === 'bigint' ? value : printUnits(value, true);
  const q = (amount * n * 4n * 1000000n) / (d * 3n * BigInt(limits.unitsPerPoint));
  const limitScaled = BigInt(limits.domPixels) * 1000000n;
  if (q > limitScaled || q < -limitScaled) {
    throw new Error('本页物理尺寸超出浏览器预览容量，请调整纸张、缩放或范围');
  }
  return Number(q) / 1000000;
}

export function projectPrintPage(
  summaryValue: PrintSummaryResponse,
  pageValue: PrintPageResponse,
  limits: Pick<PrintingLimits, 'unitsPerPoint' | 'domPixels'>,
): ProjectedPrintPage {
  const s = summaryValue.summary;
  const c = pageValue.content;
  const n = BigInt(s.scale.numerator);
  const d = BigInt(s.scale.denominator);
  const px = (v: string | bigint): number => printPixels(v, limits, n, d);
  const plain = (v: string | bigint): number => printPixels(v, limits);
  return {
    width: plain(s.paper.width),
    height: plain(s.paper.height),
    x: plain(c.page.offsetX),
    y: plain(c.page.offsetY),
    contentWidth: plain(c.page.content.width),
    contentHeight: plain(c.page.content.height),
    scale: Number(n) / Number(d),
    cells: c.cells.map((i) => {
      const b = parseRectangle(i.box, true);
      const clip = parseRectangle(i.clip);
      return {
        item: i,
        x: px(clip.x),
        y: px(clip.y),
        width: px(clip.width),
        height: px(clip.height),
        innerX: px(b.x - clip.x),
        innerY: px(b.y - clip.y),
        innerWidth: px(b.width),
        innerHeight: px(b.height),
      };
    }),
    rows: c.rows.map((i) => ({ item: i, start: px(i.clipStart), size: px(i.clipSize) })),
    columns: c.columns.map((i) => ({ item: i, start: px(i.clipStart), size: px(i.clipSize) })),
  };
}

export function printSourceFlag(
  properties: SourcePrintProperties | null | undefined,
  group: string,
  name: string,
): boolean {
  const entry = properties?.[group]?.[name];
  return entry?.status === 'stored' && (entry.value === 'true' || entry.value === '1');
}

export const PrintingModel = Object.freeze({
  units: printUnits,
  columnName,
  columnNumber,
  areas: parsePrintAreas,
  titles: parsePrintTitles,
  options: buildPrintOptions,
  envelope: checkPrintEnvelope,
  summary: validatePrintSummary,
  page: validatePrintPage,
  pixels: printPixels,
  project: projectPrintPage,
  flag: printSourceFlag,
});
