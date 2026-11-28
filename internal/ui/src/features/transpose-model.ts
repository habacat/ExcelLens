/**
 * Axis-exchanged read-only view model. Every stored CellData retains its
 * source ref, type, raw value, and formula state; sparse blanks are not
 * manufactured into synthetic cells.
 */
import { t as localize } from '../core/i18n.ts';
import type { TransposeLimits } from '../contracts/formats.ts';
import type { CellData, ClipboardPayload } from '../contracts/workbook.ts';
import { cellDisplayValue, cellFormulaText } from '../grid/cell-view.ts';

export type TransposeViewMode = 'display' | 'raw' | 'formula';

export interface TransposeSnapshotLike {
  readonly book: string;
  readonly workbookKey: string;
  readonly sheet: string;
  readonly firstColumn: number;
  readonly lastColumn: number;
  readonly rows: number;
  readonly maxRow: number;
}

export interface TransposedRow {
  readonly number: number;
  readonly style: number;
  readonly notice: string;
  readonly cells: ReadonlyMap<number, CellData>;
}

export interface TransposedModelData {
  readonly payload: ClipboardPayload;
  readonly rows: readonly TransposedRow[];
  readonly firstColumn: number;
  readonly lastColumn: number;
}

export interface TransposedItem {
  readonly row: number;
  readonly column: number;
  readonly ref: string;
  readonly cell: CellData | null;
  readonly style: number;
  readonly notice: string;
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

function fail(): never {
  throw new Error(localize('ui.transposeModel.transposedContentDoesNotMatch'));
}

function isValidTransposeCell(
  cell: unknown,
  rowNumber: number,
  startCol: number,
  endCol: number,
  colName: (col: number) => string,
): cell is CellData {
  if (
    !isRecord(cell) ||
    !integer(cell.col, startCol, endCol) ||
    cell.ref !== `${colName(cell.col)}${rowNumber}` ||
    typeof cell.raw !== 'string' ||
    typeof cell.display !== 'string' ||
    typeof cell.hasFormula !== 'boolean' ||
    !integer(cell.style, 0, Number.MAX_SAFE_INTEGER)
  ) {
    return false;
  }
  for (const key of ['formula', 'type'] as const) {
    if (cell[key] !== undefined && typeof cell[key] !== 'string') return false;
  }
  for (const key of ['cacheMissing', 'presentationOnly', 'sharedFormula'] as const) {
    if (cell[key] !== undefined && typeof cell[key] !== 'boolean') return false;
  }
  if (cell.conditional !== undefined && cell.conditional !== null) {
    if (!isRecord(cell.conditional)) return false;
    if (cell.conditional.display !== undefined && typeof cell.conditional.display !== 'string') {
      return false;
    }
  }
  return true;
}

export function validateTranspose(
  payload: unknown,
  snapshot: TransposeSnapshotLike,
  limits: TransposeLimits,
  colName: (col: number) => string,
): TransposedModelData {
  if (!isRecord(payload)) fail();
  if (
    payload.book !== snapshot.book ||
    payload.workbookKey !== snapshot.workbookKey ||
    payload.sheet !== snapshot.sheet
  ) {
    fail();
  }
  if (
    payload.startCol !== snapshot.firstColumn ||
    payload.endCol !== snapshot.lastColumn ||
    !isUnknownArray(payload.rows) ||
    payload.rows.length !== snapshot.rows ||
    !integer(payload.rows.length, 1, limits.rows)
  ) {
    fail();
  }
  const startCol = payload.startCol;
  const endCol = payload.endCol;
  const columns = endCol - startCol + 1;
  if (
    !integer(columns, 1, limits.columns) ||
    columns * payload.rows.length > limits.cells ||
    new TextEncoder().encode(JSON.stringify(payload)).length > limits.bytes
  ) {
    fail();
  }

  const seen = new Set<number>();
  const rows: TransposedRow[] = [];
  const validatedPayloadRows: ClipboardPayload['rows'] = [];
  for (const row of payload.rows) {
    if (
      !isRecord(row) ||
      !integer(row.row, 1, snapshot.maxRow) ||
      seen.has(row.row) ||
      (row.cells !== null && !isUnknownArray(row.cells)) ||
      (row.style !== undefined && !integer(row.style, 0, Number.MAX_SAFE_INTEGER)) ||
      (row.presentationNotice !== undefined && typeof row.presentationNotice !== 'string')
    ) {
      fail();
    }
    const rowCells = row.cells ?? [];
    if (rowCells.length > columns) fail();
    seen.add(row.row);
    const cells = new Map<number, CellData>();
    const validatedRowCells: CellData[] = [];
    for (const cell of rowCells) {
      if (!isValidTransposeCell(cell, row.row, startCol, endCol, colName) || cells.has(cell.col)) {
        fail();
      }
      cells.set(cell.col, cell);
      validatedRowCells.push(cell);
    }
    rows.push({
      number: row.row,
      style: row.style ?? 0,
      notice: row.presentationNotice ?? '',
      cells,
    });
    validatedPayloadRows.push({
      row: row.row,
      style: row.style,
      cells: validatedRowCells,
      presentationNotice: row.presentationNotice,
    });
  }
  const validatedPayload: ClipboardPayload = {
    workbookKey: snapshot.workbookKey,
    book: snapshot.book,
    sheet: snapshot.sheet,
    startCol,
    endCol,
    rows: validatedPayloadRows,
  };
  return {
    payload: validatedPayload,
    rows,
    firstColumn: startCol,
    lastColumn: endCol,
  };
}

export function transposeAt(
  model: TransposedModelData,
  sourceColumn: number,
  index: number,
  colName: (col: number) => string,
): TransposedItem | null {
  const row = model.rows[index];
  if (!row || !integer(sourceColumn, model.firstColumn, model.lastColumn)) return null;
  return {
    row: row.number,
    column: sourceColumn,
    ref: `${colName(sourceColumn)}${row.number}`,
    cell: row.cells.get(sourceColumn) ?? null,
    style: row.style,
    notice: row.notice,
  };
}

export function transposeValue(
  item: TransposedItem | null | undefined,
  mode: TransposeViewMode,
  styled: boolean,
): string | null {
  const cell = item?.cell;
  if (!cell) return '';
  if (mode === 'formula' && cell.hasFormula) return cellFormulaText(cell);
  if (cell.cacheMissing) return null;
  return mode === 'display' ? cellDisplayValue(cell, false, styled) : cell.raw;
}

export function transposePageSize(width: number, maximum: number): number {
  return Math.max(1, Math.min(maximum, Math.floor((Math.max(0, width) - 58) / 140)));
}

export const TransposeModel = Object.freeze({
  validate: validateTranspose,
  at: transposeAt,
  value: transposeValue,
  pageSize: transposePageSize,
});
