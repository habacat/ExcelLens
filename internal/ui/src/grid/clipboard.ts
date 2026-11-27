/**
 * Excel-compatible HTML/TSV clipboard builder and strict bounded TSV parser.
 * Pure string construction: never mutates the live grid, evaluates formulas,
 * or executes workbook HTML.
 */
import type { HttpError } from '../contracts/common.ts';
import type { ClipboardLimits, RichTextLimits } from '../contracts/formats.ts';
import type {
  CellData,
  ClipboardPayload,
  ClipboardRow,
  VisualStyle,
} from '../contracts/workbook.ts';
import { escapeHtml } from '../core/display-format.ts';
import { conditionalColor, conditionalValue, conditionalVisual } from './conditional.ts';
import type { MergeCopyAnchor } from './merges.ts';
import { richTextHtml } from './richtext.ts';

let configured: Readonly<ClipboardLimits> | null = null;

export interface ClipboardBuildResult {
  text: string;
  html: string;
  notices: string[];
}

export interface ClipboardSelectionSnapshot {
  readonly rows: number;
  readonly firstColumn: number;
  readonly lastColumn: number;
  current(): boolean;
}

export interface PasteParseOptions {
  readonly maxRows?: number;
  readonly maxCols?: number;
  readonly maxCells?: number;
  readonly maxCellLength?: number;
  readonly maxTextLength?: number;
}

export interface PasteCellChange {
  readonly row: number;
  readonly col: number;
  readonly value: string;
}

export function configureClipboard(value: ClipboardLimits): void {
  const required = [
    value.rows,
    value.columns,
    value.cells,
    value.textBytes,
    value.htmlBytes,
  ];
  if (required.some((n) => !Number.isSafeInteger(n) || n < 1)) {
    throw new Error('复制容量配置无效');
  }
  configured = Object.freeze({ ...value });
}

export function copyLimits(override?: Partial<ClipboardLimits> | null): ClipboardLimits {
  const limits: Partial<ClipboardLimits> = { ...configured, ...override };
  if (
    !Number.isSafeInteger(limits.rows) ||
    !Number.isSafeInteger(limits.columns) ||
    !Number.isSafeInteger(limits.cells) ||
    !Number.isSafeInteger(limits.textBytes) ||
    !Number.isSafeInteger(limits.htmlBytes) ||
    (limits.rows ?? 0) < 1 ||
    (limits.columns ?? 0) < 1 ||
    (limits.cells ?? 0) < 1 ||
    (limits.textBytes ?? 0) < 1 ||
    (limits.htmlBytes ?? 0) < 1
  ) {
    throw new Error('复制容量配置尚未载入');
  }
  return {
    rows: limits.rows ?? 0,
    columns: limits.columns ?? 0,
    cells: limits.cells ?? 0,
    textBytes: limits.textBytes ?? 0,
    htmlBytes: limits.htmlBytes ?? 0,
    tsvRows: limits.tsvRows ?? 2000,
    tsvColumns: limits.tsvColumns ?? 200,
    tsvCells: limits.tsvCells ?? 50000,
  };
}

export function checkCopy(
  payload: Pick<ClipboardPayload, 'startCol' | 'endCol' | 'rows'>,
  override?: Partial<ClipboardLimits> | null,
): { readonly start: number; readonly end: number; readonly limits: ClipboardLimits } {
  const limits = copyLimits(override);
  const start = payload.startCol;
  const end = payload.endCol;
  if (
    !Number.isSafeInteger(start) ||
    !Number.isSafeInteger(end) ||
    start < 1 ||
    end < start ||
    end - start + 1 > limits.columns ||
    !Array.isArray(payload.rows) ||
    payload.rows.length > limits.rows ||
    payload.rows.length * (end - start + 1) > limits.cells
  ) {
    throw new Error('复制区域超出安全上限');
  }
  return { start, end, limits };
}

function capacityError(message: string): HttpError {
  const err: HttpError = new Error(message);
  err.status = 413;
  return err;
}

function getHttpStatus(error: unknown): number | undefined {
  if (typeof error === 'object' && error !== null && 'status' in error) {
    const status = (error as HttpError).status;
    if (typeof status === 'number') return status;
  }
  return undefined;
}

export async function copyByBudget<TResult>(
  snapshot: ClipboardSelectionSnapshot,
  limits: ClipboardLimits,
  plain: boolean,
  richCopy: () => Promise<TResult>,
  textCopy: () => Promise<TResult>,
  openExport: () => void,
): Promise<TResult | false> {
  const rows = snapshot.rows;
  const columns = snapshot.lastColumn - snapshot.firstColumn + 1;
  const cells = rows * columns;
  if (!plain && rows <= limits.rows && columns <= limits.columns && cells <= limits.cells) {
    try {
      return await richCopy();
    } catch (error: unknown) {
      if (getHttpStatus(error) !== 413) throw error;
    }
  }
  if (!snapshot.current()) return false;
  if (rows <= limits.tsvRows && columns <= limits.tsvColumns && cells <= limits.tsvCells) {
    try {
      return await textCopy();
    } catch (error: unknown) {
      if (getHttpStatus(error) !== 413) throw error;
    }
  }
  if (snapshot.current()) openExport();
  return false;
}

function cellHTML(value: string): string {
  return escapeHtml(value).replace(/\r\n?|\n/g, '<br style="mso-data-placement:same-cell">');
}

export function tsvCell(value: unknown): string {
  const text =
    typeof value === 'string'
      ? value
      : typeof value === 'number' || typeof value === 'boolean'
        ? String(value)
        : '';
  return /[\t\r\n"]/.test(text) ? `"${text.replace(/"/g, '""')}"` : text;
}

function safeColor(value: string | undefined): string {
  return /^(#[0-9a-f]{3}(?:[0-9a-f]{3})?|rgba?\([\d\s,.%]+\))$/i.test(value ?? '') ? (value ?? '') : '';
}

function safeBorder(value: string | undefined): string {
  return /^(?:\d+(?:\.\d+)?px\s+)?(?:solid|dashed|dotted|double)\s+(?:#[\da-f]{3}(?:[\da-f]{3})?|rgba?\([\d\s,.%]+\))$/i.test(
    value ?? '',
  )
    ? (value ?? '')
    : '';
}

export function styleOfCell(
  cell: Partial<CellData> | null | undefined,
  palette: readonly VisualStyle[] | undefined,
  enabled: boolean,
  fontOverride = '',
  rich = false,
): string {
  const source = conditionalVisual(cell, palette, 0, enabled);
  const parts = ['padding:2px 7px', 'border:1px solid #e3e7eb', 'vertical-align:middle'];
  if (source) {
    const fg = safeColor(conditionalColor(cell, source));
    const bg = safeColor(source.background);
    if (fg) parts.push(`color:${fg}`);
    if (bg) parts.push(`background-color:${bg}`);
    if (source.bold) parts.push('font-weight:bold');
    if (source.italic) parts.push('font-style:italic');
    if (!rich && (source.underline || source.strike)) {
      parts.push(
        `text-decoration:${[source.underline && 'underline', source.strike && 'line-through'].filter(Boolean).join(' ')}`,
      );
    }
    const underline = cell?.conditional?.style?.font.underline || source.underlineStyle;
    if (!rich && (underline === 'double' || underline === 'doubleAccounting')) {
      parts.push('text-decoration-style:double');
    }
    if (source.fontSize !== undefined && source.fontSize > 0 && source.fontSize <= 409) {
      parts.push(`font-size:${source.fontSize}pt`);
    }
    if (source.horizontal && ['left', 'center', 'right', 'justify'].includes(source.horizontal)) {
      parts.push(`text-align:${source.horizontal}`);
    }
    if (source.vertical && ['top', 'center', 'bottom'].includes(source.vertical)) {
      parts.push(`vertical-align:${source.vertical === 'center' ? 'middle' : source.vertical}`);
    }
    if (source.wrap) parts.push('white-space:pre-wrap');
    const borderPairs: readonly (readonly [string, string | undefined])[] = [
      ['top', source.borderTop],
      ['bottom', source.borderBottom],
      ['left', source.borderLeft],
      ['right', source.borderRight],
    ];
    for (const [side, rawBorder] of borderPairs) {
      const safe = safeBorder(rawBorder);
      if (safe) parts.push(`border-${side}:${safe}`);
    }
  }
  const font = (fontOverride || source?.fontName || '').replace(/["'<>;\\\r\n]/g, '').slice(0, 80);
  if (font) parts.push(`font-family:"${font}"`);
  if (
    cell?.type === 's' ||
    cell?.type === 'inlineStr' ||
    cell?.type === 'str' ||
    cell?.type === 'e' ||
    (!cell?.type &&
      /^(=|\+|-|@)/.test(cell?.display ?? '') &&
      !/^-[\d,.]+$/.test(cell?.display ?? ''))
  ) {
    parts.push('mso-number-format:"\\@"');
  }
  return parts.join(';');
}

function mergeHeight(rows: readonly ClipboardRow[], index: number, merge: MergeCopyAnchor): number {
  const height = merge.visibleRows;
  if (index + height > rows.length) return 1;
  for (let offset = 1; offset < height; offset++) {
    const cur = rows[index + offset];
    const prev = rows[index + offset - 1];
    if (!cur || !prev || cur.row <= prev.row || cur.row > merge.endRow) return 1;
  }
  return height;
}

export function characterCount(value: string | null | undefined): number {
  let count = 0;
  for (const _ of value ?? '') count++;
  return count;
}

export function utf8Length(value: string | null | undefined): number {
  let bytes = 0;
  for (const ch of value ?? '') {
    const n = ch.codePointAt(0) ?? 0;
    bytes += n <= 0x7f ? 1 : n <= 0x7ff ? 2 : n <= 0xffff ? 3 : 4;
  }
  return bytes;
}

export function buildClipboard(
  payload: Pick<ClipboardPayload, 'startCol' | 'endCol' | 'rows'>,
  styles: readonly VisualStyle[] = [],
  showStyles = true,
  fontOverride = '',
  richLimits?: RichTextLimits | null,
  overrideLimits?: Partial<ClipboardLimits> | null,
  mergeAt?: (row: number, col: number) => MergeCopyAnchor | null,
): ClipboardBuildResult {
  const { start, end, limits } = checkCopy(payload, overrideLimits);
  const plain: string[] = [];
  const notices = new Set<string>();
  const html: string[] = [];
  let sourceBytes = 0;
  let htmlBytes = 0;

  const append = (...parts: string[]): void => {
    for (const part of parts) {
      htmlBytes += utf8Length(part);
      if (htmlBytes > limits.htmlBytes) {
        throw capacityError('复制HTML超过本次容量，请缩小选区或使用导出');
      }
      html.push(part);
    }
  };

  append(
    '<html><body><!--StartFragment--><table border="0" cellspacing="0" cellpadding="0" style="border-collapse:collapse">',
  );
  const coveredUntil = new Uint32Array(end - start + 1);

  for (const [index, row] of payload.rows.entries()) {
    if (showStyles && row.presentationNotice) notices.add(row.presentationNotice);
    const cells = new Map(row.cells.map((x) => [x.col, x]));
    const values: string[] = [];
    append('<tr>');
    for (let col = start; col <= end; col++) {
      if ((coveredUntil[col - start] ?? 0) > index) {
        values.push('');
        continue;
      }
      let span = '';
      const merge = mergeAt?.(row.row, col);
      if (merge) {
        const width = Math.min(end, merge.endCol) - col + 1;
        const height = mergeHeight(payload.rows, index, merge);
        if (width > 1) span += ` colspan="${width}"`;
        if (height > 1) span += ` rowspan="${height}"`;
        coveredUntil.fill(index + height, col - start, col - start + width);
      }
      const cell: CellData = cells.get(col) ?? {
        col,
        ref: '',
        display: '',
        raw: '',
        hasFormula: false,
        style: 0,
      };
      sourceBytes += utf8Length(cell.raw) + utf8Length(cell.display) + utf8Length(cell.formula);
      if (showStyles && cell.conditional?.display != null) {
        sourceBytes += utf8Length(cell.conditional.display);
      }
      if (sourceBytes > limits.textBytes) {
        throw capacityError('复制文字超过本次容量，请缩小选区或使用导出');
      }
      const value = conditionalValue(cell, false, showStyles);
      values.push(tsvCell(value));
      const base =
        conditionalVisual(cell, styles, 0, true) ?? styles[Number(cell.style) || 0] ?? {};
      const rich = showStyles
        ? richTextHtml(cell, base, { limits: richLimits, fontOverride }, cellHTML)
        : null;
      if (rich?.notice) notices.add(rich.notice);
      const notice = [rich?.notice, showStyles ? cell.conditional?.notice : '']
        .filter(Boolean)
        .join('；');
      if (notice) notices.add(notice);
      append(
        '<td',
        span,
        ' style="',
        escapeHtml(styleOfCell(cell, styles, showStyles, fontOverride, Boolean(rich?.rich))),
        '"',
        notice ? ` title="${escapeHtml(notice)}"` : '',
        '>',
        rich?.html ?? cellHTML(value),
        '</td>',
      );
    }
    plain.push(values.join('\t'));
    append('</tr>');
  }
  append('</table><!--EndFragment--></body></html>');
  const text = plain.join('\n');
  if (utf8Length(text) > limits.textBytes) {
    throw capacityError('复制文字超过本次容量，请缩小选区或使用导出');
  }
  return { text, html: html.join(''), notices: [...notices] };
}

const REQUEST_LIMIT = 2 * 1024 * 1024;
const REQUEST_RESERVE = 1024;
const MAX_BATCH_CELLS = 20000;

function changeBytes(change: PasteCellChange): number {
  if (characterCount(change.value) > 4000) {
    throw new Error('单元格内容超过4000字符，请缩短后重试');
  }
  return utf8Length(JSON.stringify({ row: change.row, col: change.col, value: change.value })) + 1;
}

export function validateClipboardChanges<T extends PasteCellChange>(changes: readonly T[]): readonly T[] {
  if (changes.length > MAX_BATCH_CELLS) throw new Error('一次最多粘贴20,000个单元格');
  let bytes = REQUEST_RESERVE;
  for (const change of changes) {
    bytes += changeBytes(change);
    if (bytes > REQUEST_LIMIT) throw new Error('编辑请求超过2 MiB，请分批粘贴');
  }
  return changes;
}

export function* clipboardBatches<T extends PasteCellChange>(
  changes: readonly T[],
): Generator<T[]> {
  let batch: T[] = [];
  let bytes = REQUEST_RESERVE;
  for (const change of changes) {
    const size = changeBytes(change);
    if (batch.length && (batch.length === MAX_BATCH_CELLS || bytes + size > REQUEST_LIMIT)) {
      yield batch;
      batch = [];
      bytes = REQUEST_RESERVE;
    }
    batch.push(change);
    bytes += size;
  }
  if (batch.length) yield batch;
}

export function parseClipboardTSV(
  rawText: string | null | undefined,
  options: PasteParseOptions = {},
): string[][] {
  const {
    maxRows = 2000,
    maxCols = 80,
    maxCells = 20000,
    maxCellLength = 4000,
    maxTextLength = 2 * 1024 * 1024,
  } = options;
  const text = rawText ?? '';
  if (text.length > maxTextLength || utf8Length(text) > maxTextLength) {
    throw new Error('剪贴板内容超过2 MiB，请分批粘贴');
  }
  const rows: string[][] = [];
  let row: string[] = [];
  let cell = '';
  let quoted = false;
  let closed = false;
  let endedRow = false;
  let cells = 0;

  const pushCell = (): void => {
    if (characterCount(cell) > maxCellLength) {
      throw new Error(`单元格内容超过${maxCellLength}字符，未粘贴`);
    }
    if (row.length >= maxCols) throw new Error(`一次最多粘贴${maxCols}列，未粘贴`);
    if (++cells > maxCells) throw new Error(`一次最多粘贴${maxCells.toLocaleString()}个单元格`);
    row.push(cell);
    cell = '';
    closed = false;
  };
  const pushRow = (): void => {
    pushCell();
    if (rows.length >= maxRows) throw new Error(`一次最多粘贴${maxRows}行`);
    rows.push(row);
    row = [];
  };

  for (let i = 0; i < text.length; i++) {
    const ch = text[i] ?? '';
    endedRow = false;
    if (quoted) {
      if (ch === '"') {
        if (text[i + 1] === '"') {
          cell += '"';
          i++;
        } else {
          quoted = false;
          closed = true;
        }
      } else {
        cell += ch;
      }
    } else if (ch === '\t') {
      pushCell();
    } else if (ch === '\r' || ch === '\n') {
      if (ch === '\r' && text[i + 1] === '\n') i++;
      pushRow();
      endedRow = true;
    } else if (closed) {
      throw new Error('剪贴板引号格式无效，未粘贴');
    } else if (ch === '"' && cell === '') {
      quoted = true;
    } else {
      cell += ch;
    }
    if (cell.length > maxCellLength * 2) {
      throw new Error(`单元格内容超过${maxCellLength}字符，未粘贴`);
    }
  }
  if (quoted) throw new Error('剪贴板引号未闭合，未粘贴');
  if (!endedRow) pushRow();
  return rows;
}

export function pasteChangesFromTSV(
  text: string,
  options: {
    readonly startRow: number;
    readonly startCol: number;
    readonly rowLimit?: number;
    readonly columns: number;
  },
): PasteCellChange[] {
  const { startRow, startCol, rowLimit = 10000, columns } = options;
  const rows = parseClipboardTSV(text);
  if (
    !Number.isSafeInteger(startRow) ||
    !Number.isSafeInteger(startCol) ||
    startRow < 0 ||
    startCol < 0 ||
    startRow + rows.length > rowLimit ||
    rows.some((r) => startCol + r.length > columns)
  ) {
    throw new Error('粘贴范围超出编辑表边界，未粘贴；请增加行列或缩小范围');
  }
  const changes: PasteCellChange[] = [];
  rows.forEach((r, i) => {
    r.forEach((value, j) => {
      changes.push({ row: startRow + i, col: startCol + j, value });
    });
  });
  validateClipboardChanges(changes);
  return changes;
}

export const ClipboardCodec = Object.freeze({
  configure: configureClipboard,
  copyLimits,
  checkCopy,
  copyByBudget,
  build: buildClipboard,
  styleOf: styleOfCell,
  tsvCell,
  parse: parseClipboardTSV,
  pasteChanges: pasteChangesFromTSV,
  characterCount,
  utf8Length,
  validateChanges: validateClipboardChanges,
  batches: clipboardBatches,
});
