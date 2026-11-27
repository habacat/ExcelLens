/**
 * Bounded special clipboard representations (TSV, Markdown, version-1 Selection JSON)
 * and text-only paste plan generator for editable analysis sheets.
 */
import type { ClipboardLimits } from '../contracts/formats.ts';
import type { CellData, ClipboardPayload, ClipboardRow } from '../contracts/workbook.ts';
import { columnName } from '../core/cell-address.ts';
import {
  buildClipboard,
  characterCount,
  checkCopy,
  parseClipboardTSV,
  utf8Length,
  validateClipboardChanges,
  type PasteCellChange,
} from './clipboard.ts';
import type { MergeCopyAnchor } from './merges.ts';

export type CopySpecialFormat = 'tsv' | 'markdown' | 'json';
export type CopySpecialMode = 'display' | 'raw' | 'formula';

export interface CopySpecialOptions {
  readonly format?: CopySpecialFormat;
  readonly mode?: CopySpecialMode;
  readonly includeHeader?: boolean;
  readonly styled?: boolean;
  readonly sheet?: string;
  readonly mergeAt?: (row: number, col: number) => MergeCopyAnchor | null;
}

export interface CopySpecialResult {
  readonly text: string;
  readonly html?: string;
  readonly notices: readonly string[];
  readonly rows: number;
  readonly columns: number;
}

export interface PastePlanOptions {
  readonly format?: 'tsv' | 'json';
  readonly mode?: CopySpecialMode;
  readonly withHeader?: boolean;
  readonly skipFirstRow?: boolean;
  readonly transpose?: boolean;
  readonly skipBlank?: boolean;
  readonly startRow?: number;
  readonly startCol?: number;
  readonly rowLimit?: number;
  readonly columns?: number;
}

export interface PastePlanResult {
  readonly changes: readonly PasteCellChange[];
  readonly rows: number;
  readonly columns: number;
  readonly skipped: number;
  readonly preview: readonly (readonly string[])[];
}

function cellsOfRow(row: ClipboardRow, start: number, end: number): CellData[] {
  const cells = new Map<number, CellData>();
  for (const cell of row.cells) {
    if (!Number.isSafeInteger(cell.col) || cell.col < start || cell.col > end || cells.has(cell.col)) {
      throw new Error('复制单元格坐标无效或重复');
    }
    cells.set(cell.col, cell);
  }
  return Array.from({ length: end - start + 1 }, (_, i) => {
    const col = start + i;
    return (
      cells.get(col) ?? {
        col,
        ref: `${columnName(col)}${row.row}`,
        raw: '',
        display: '',
        hasFormula: false,
        present: false,
      }
    );
  });
}

interface CellValueLike {
  readonly ref?: string;
  readonly raw?: string;
  readonly display?: string;
  readonly hasFormula?: boolean;
  readonly formula?: string;
  readonly cacheMissing?: boolean;
  readonly conditional?: CellData['conditional'];
}

function valueOfCell(cell: CellValueLike, mode: CopySpecialMode = 'raw', styled = false): string {
  if (mode === 'formula' && cell.hasFormula) {
    if (typeof cell.formula !== 'string' || !cell.formula.startsWith('=')) {
      throw new Error(`${cell.ref || '此单元格'} 的公式无法还原，请使用保留状态的JSON`);
    }
    return cell.formula;
  }
  if (cell.cacheMissing) {
    throw new Error(`${cell.ref || '此单元格'} 的公式缺少缓存值，请选择公式文字或JSON`);
  }
  if (mode === 'display') {
    if (styled && cell.conditional?.display != null) return cell.conditional.display;
    return cell.display ?? cell.raw ?? '';
  }
  return cell.raw ?? cell.display ?? '';
}

function escapeMarkdown(value: string): string {
  return value
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/\\/g, '\\\\')
    .replace(/\|/g, '\\|')
    .replace(/\r\n?|\n/g, '<br>')
    .replace(/\t/g, '&#9;')
    .replace(/^ +| +$/g, (s) => '&#32;'.repeat(s.length));
}

export function buildSpecialSelection(
  payload: ClipboardPayload,
  options: CopySpecialOptions = {},
  limits?: Partial<ClipboardLimits> | null,
): CopySpecialResult {
  const {
    format = 'tsv',
    mode = 'display',
    includeHeader = false,
    styled = false,
    sheet = '',
    mergeAt,
  } = options;
  if (
    !['tsv', 'markdown', 'json'].includes(format) ||
    !['display', 'raw', 'formula'].includes(mode)
  ) {
    throw new Error('复制方式无效');
  }
  const rows = includeHeader
    ? payload.rows.filter((row) => row.row !== payload.header?.row)
    : payload.rows;
  if (includeHeader && !payload.header) throw new Error('请先读取所选表头行');
  const all = includeHeader && payload.header ? [payload.header, ...rows] : rows;
  const { start, end, limits: budget } = checkCopy({ ...payload, rows: all }, limits);
  const notices = new Set<string>();
  const inspect = (row: ClipboardRow): CellData[] => {
    if (row.presentationNotice) notices.add(row.presentationNotice);
    return cellsOfRow(row, start, end);
  };

  let text: string;
  let html: string | undefined;

  if (format === 'json') {
    const encode = (row: ClipboardRow) => ({
      row: row.row,
      cells: inspect(row).map((cell) => ({
        col: cell.col,
        ref: cell.ref || `${columnName(cell.col)}${row.row}`,
        raw: cell.raw,
        display: cell.display,
        type: cell.type ?? '',
        hasFormula: cell.hasFormula,
        ...(cell.hasFormula ? { formula: cell.formula ?? '' } : {}),
        ...(cell.cacheMissing ? { cacheMissing: true } : {}),
        ...(cell.sharedFormula ? { sharedFormula: true } : {}),
        ...(cell.presentationOnly ? { presentationOnly: true } : {}),
        ...(cell.present === false ? { present: false } : {}),
        ...(cell.conditional?.display != null
          ? { presentationDisplay: cell.conditional.display }
          : {}),
      })),
    });
    const header = includeHeader && payload.header ? encode(payload.header) : null;
    const doc = {
      format: 'ExcelLens.Selection',
      version: 1,
      sheet,
      columns: Array.from({ length: end - start + 1 }, (_, i) => ({
        col: start + i,
        ...(header ? { header: header.cells[i]?.display ?? '' } : {}),
      })),
      ...(header ? { header } : {}),
      rows: rows.map(encode),
    };
    text = JSON.stringify(doc, null, 2);
  } else {
    const values = all.map((row) =>
      inspect(row).map((cell) => {
        if (styled && cell.conditional?.notice) notices.add(cell.conditional.notice);
        return valueOfCell(cell, mode, styled);
      }),
    );
    if (format === 'tsv') {
      const literal: ClipboardPayload = {
        startCol: start,
        endCol: end,
        rows: values.map((rowVals, i) => ({
          row: all[i]?.row ?? i + 1,
          cells: rowVals.map((val, j) => ({
            col: start + j,
            ref: '',
            raw: val,
            display: val,
            type: 'inlineStr',
            hasFormula: false,
          })),
        })),
      };
      const result = buildClipboard(literal, [], false, '', null, budget, mergeAt);
      text = result.text;
      html = result.html;
    } else {
      const header =
        (includeHeader ? values.shift() : undefined) ??
        Array.from({ length: end - start + 1 }, (_, i) => columnName(start + i));
      text = [
        header.map(escapeMarkdown),
        header.map(() => '---'),
        ...values.map((rowVals) => rowVals.map(escapeMarkdown)),
      ]
        .map((rowVals) => `| ${rowVals.join(' | ')} |`)
        .join('\n');
    }
  }

  if (utf8Length(text) > budget.textBytes) {
    throw new Error('复制结果超过本次文字容量，请缩小选区或使用导出');
  }
  return {
    text,
    ...(html ? { html } : {}),
    notices: [...notices],
    rows: rows.length,
    columns: end - start + 1,
  };
}

function isPlainRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function isUnknownArray(value: unknown): value is readonly unknown[] {
  return Array.isArray(value);
}

function parseSelectionJSON(
  text: string,
  options: { readonly mode?: CopySpecialMode; readonly withHeader?: boolean } = {},
): string[][] {
  const { mode = 'raw', withHeader = false } = options;
  let doc: unknown;
  try {
    doc = JSON.parse(text);
  } catch {
    throw new Error('JSON格式无效，未粘贴');
  }
  const asString = (value: unknown): string => {
    if (value === null) return '';
    if (typeof value !== 'string') {
      throw new Error('JSON单元格必须为字符串，数字请加引号以保留精度');
    }
    return value;
  };
  if (isUnknownArray(doc)) {
    if (
      doc.length > 2000 ||
      !doc.every((row) => isUnknownArray(row) && row.length <= 80) ||
      doc.reduce<number>((sum, row) => sum + (isUnknownArray(row) ? row.length : 0), 0) > 20000
    ) {
      throw new Error('JSON应为容量以内的二维文字数组');
    }
    return doc.map((row) => (isUnknownArray(row) ? row.map(asString) : []));
  }
  if (
    !isPlainRecord(doc) ||
    doc.format !== 'ExcelLens.Selection' ||
    doc.version !== 1 ||
    !isUnknownArray(doc.columns) ||
    !doc.columns.length ||
    doc.columns.length > 80 ||
    !isUnknownArray(doc.rows) ||
    doc.rows.length > 2000
  ) {
    throw new Error('不支持此JSON结构、版本或容量');
  }
  const columns = doc.columns.map((column: unknown) =>
    isPlainRecord(column) && typeof column.col === 'number' ? column.col : -1,
  );
  if (
    columns.some(
      (col, i) => !Number.isSafeInteger(col) || col < 1 || (i > 0 && col !== (columns[i - 1] ?? 0) + 1),
    )
  ) {
    throw new Error('JSON列坐标无效');
  }
  if (withHeader && !doc.header) throw new Error('JSON中没有附带表头');
  const rawRows: readonly unknown[] = withHeader ? [doc.header, ...doc.rows] : doc.rows;
  return rawRows.map((rawRow) => {
    if (
      !isPlainRecord(rawRow) ||
      typeof rawRow.row !== 'number' ||
      !Number.isSafeInteger(rawRow.row) ||
      rawRow.row < 1 ||
      !isUnknownArray(rawRow.cells) ||
      rawRow.cells.length !== columns.length
    ) {
      throw new Error('JSON行内容无效');
    }
    const cells = new Map<number, CellData>();
    for (const rawCell of rawRow.cells) {
      if (
        !isPlainRecord(rawCell) ||
        typeof rawCell.col !== 'number' ||
        !columns.includes(rawCell.col) ||
        cells.has(rawCell.col) ||
        typeof rawCell.raw !== 'string' ||
        typeof rawCell.display !== 'string' ||
        typeof rawCell.type !== 'string' ||
        typeof rawCell.hasFormula !== 'boolean' ||
        (rawCell.hasFormula && typeof rawCell.formula !== 'string')
      ) {
        throw new Error('JSON单元格值、类型或坐标无效');
      }
      cells.set(rawCell.col, {
        col: rawCell.col,
        ref: typeof rawCell.ref === 'string' ? rawCell.ref : '',
        raw: rawCell.raw,
        display: rawCell.display,
        type: rawCell.type,
        hasFormula: rawCell.hasFormula,
        formula: typeof rawCell.formula === 'string' ? rawCell.formula : undefined,
        cacheMissing: rawCell.cacheMissing === true,
      });
    }
    if (cells.size !== columns.length) throw new Error('JSON行缺少单元格，未粘贴');
    return columns.map((col) => {
      const cell = cells.get(col);
      if (!cell) throw new Error('JSON行缺少单元格，未粘贴');
      return valueOfCell(cell, mode);
    });
  });
}

export function buildPastePlan(
  rawText: string | null | undefined,
  options: PastePlanOptions = {},
): PastePlanResult {
  const {
    format = 'tsv',
    mode = 'raw',
    withHeader = false,
    skipFirstRow = false,
    transpose = false,
    skipBlank = false,
    startRow,
    startCol,
    rowLimit = 10000,
    columns,
  } = options;
  const text = rawText ?? '';
  if (text.length > 2 * 1024 * 1024 || utf8Length(text) > 2 * 1024 * 1024) {
    throw new Error('剪贴板内容超过2 MiB，请分批粘贴');
  }
  let rows =
    format === 'tsv'
      ? parseClipboardTSV(text)
      : parseSelectionJSON(text, { mode, withHeader });
  if (!rows.length) throw new Error('没有可粘贴的数据');
  let width = Math.max(0, ...rows.map((row) => row.length));
  if (
    !width ||
    rows.length > 2000 ||
    width > 80 ||
    rows.reduce((sum, row) => sum + row.length, 0) > 20000
  ) {
    throw new Error('粘贴数据超过本次行列或单元格容量');
  }
  for (const row of rows) {
    for (const value of row) {
      if (characterCount(value) > 4000) throw new Error('单元格内容超过4000字符，未粘贴');
    }
  }
  if (skipFirstRow) rows = rows.slice(1);
  if (!rows.length) throw new Error('跳过表头后没有可粘贴的数据');
  width = Math.max(0, ...rows.map((row) => row.length));
  if (!width) throw new Error('没有可粘贴的数据');
  if (transpose) {
    const sourceRows = rows;
    rows = Array.from({ length: width }, (_, col) => sourceRows.map((row) => row[col] ?? ''));
  }
  const resultWidth = transpose ? (rows[0]?.length ?? 0) : width;
  if (
    startRow === undefined ||
    startCol === undefined ||
    columns === undefined ||
    !Number.isSafeInteger(startRow) ||
    !Number.isSafeInteger(startCol) ||
    startRow < 0 ||
    startCol < 0 ||
    !Number.isSafeInteger(columns) ||
    columns < 1 ||
    startRow + rows.length > rowLimit ||
    startCol + resultWidth > columns
  ) {
    throw new Error('粘贴范围超出编辑表边界，未粘贴；请增加行列或缩小范围');
  }
  const changes: PasteCellChange[] = [];
  let skipped = 0;
  rows.forEach((row, i) => {
    row.forEach((value, j) => {
      if (skipBlank && value === '') skipped++;
      else changes.push({ row: startRow + i, col: startCol + j, value });
    });
  });
  validateClipboardChanges(changes);
  return {
    changes,
    rows: rows.length,
    columns: resultWidth,
    skipped,
    preview: rows.slice(0, 6).map((row) => row.slice(0, 8)),
  };
}

export const ClipboardSpecial = Object.freeze({
  selection: buildSpecialSelection,
  pastePlan: buildPastePlan,
});
