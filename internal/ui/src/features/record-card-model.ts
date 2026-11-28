/**
 * Bounded presentation and strict response validation for single-record cards.
 * Never performs numeric or type conversion on source values.
 */
import { t as localize } from '../core/i18n.ts';
import type { RecordCardResponse } from '../contracts/features.ts';
import type { RecordCardLimits, SourceCapabilities } from '../contracts/formats.ts';
import type { CellData } from '../contracts/workbook.ts';
import {
  cellDisplayValue,
  cellFormulaText,
  cellSourceTypeLabel,
} from '../grid/cell-view.ts';

export type RecordCardValueMode = 'display' | 'raw';

export interface RecordCardRequest {
  readonly workbookKey: string;
  readonly book: string;
  readonly sheet: string;
  readonly viewId: string;
  readonly filterId: string;
  readonly row: number;
  readonly headerRow: number;
  readonly firstColumn: number;
  readonly columns: number;
  readonly pinnedThroughRow: number;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function isUnknownArray(value: unknown): value is readonly unknown[] {
  return Array.isArray(value);
}

function isIntegerInRange(value: unknown, first: number, last: number): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= first && value <= last;
}

function fail(): never {
  throw new Error(localize('ui.recordCardModel.responseFromRecordCardDoes'));
}

function isValidRecordCell(
  value: unknown,
  row: number,
  column: number,
  colName: (col: number) => string,
): value is CellData {
  if (!isRecord(value)) return false;
  if (
    value.ref !== `${colName(column)}${row}` ||
    value.col !== column ||
    typeof value.raw !== 'string' ||
    typeof value.display !== 'string' ||
    typeof value.hasFormula !== 'boolean' ||
    !isIntegerInRange(value.style, 0, Number.MAX_SAFE_INTEGER)
  ) {
    return false;
  }
  for (const key of ['formula', 'type', 'valueColor'] as const) {
    if (value[key] !== undefined && typeof value[key] !== 'string') return false;
  }
  for (const key of ['cacheMissing', 'sharedFormula', 'presentationOnly'] as const) {
    if (value[key] !== undefined && typeof value[key] !== 'boolean') return false;
  }
  const conditional = value.conditional;
  if (conditional !== undefined && conditional !== null) {
    if (!isRecord(conditional)) return false;
    if (
      conditional.display !== undefined &&
      conditional.display !== null &&
      typeof conditional.display !== 'string'
    ) {
      return false;
    }
  }
  return true;
}

function isValidRecordCardPayload(
  page: unknown,
  request: RecordCardRequest,
  limits: RecordCardLimits | null | undefined,
  source: Pick<SourceCapabilities, 'maxRows' | 'maxColumns'>,
  colName: (col: number) => string,
  totalColumns: number,
): page is RecordCardResponse {
  if (
    !isRecord(page) ||
    !limits ||
    !isIntegerInRange(limits.columns, 1, source.maxColumns) ||
    !isIntegerInRange(limits.bytes, 1, Number.MAX_SAFE_INTEGER)
  ) {
    return false;
  }
  if (
    page.workbookKey !== request.workbookKey ||
    page.book !== request.book ||
    page.sheet !== request.sheet ||
    page.viewId !== request.viewId ||
    page.row !== request.row ||
    page.headerRow !== request.headerRow ||
    page.firstColumn !== request.firstColumn ||
    page.pinnedThroughRow !== request.pinnedThroughRow
  ) {
    return false;
  }
  const pageFilterId = typeof page.filterId === 'string' ? page.filterId : '';
  if (pageFilterId !== request.filterId) return false;

  if (
    !isIntegerInRange(page.totalColumns, 1, source.maxColumns) ||
    page.totalColumns !== totalColumns ||
    !isIntegerInRange(request.columns, 1, limits.columns) ||
    !isIntegerInRange(page.firstColumn, 1, totalColumns)
  ) {
    return false;
  }
  const count = Math.min(request.columns, totalColumns - page.firstColumn + 1);
  if (
    page.columns !== count ||
    !isUnknownArray(page.fields) ||
    page.fields.length !== count ||
    !isIntegerInRange(page.row, 1, source.maxRows) ||
    !isIntegerInRange(page.headerRow, 0, source.maxRows)
  ) {
    return false;
  }
  if (
    !isIntegerInRange(page.totalRecords, 1, source.maxRows) ||
    !isIntegerInRange(page.position, 1, page.totalRecords) ||
    !isIntegerInRange(page.previousRow, 0, source.maxRows) ||
    !isIntegerInRange(page.nextRow, 0, source.maxRows)
  ) {
    return false;
  }
  if (
    (page.position === 1) !== (page.previousRow === 0) ||
    (page.position === page.totalRecords) !== (page.nextRow === 0) ||
    page.previousRow === page.row ||
    page.nextRow === page.row ||
    (page.previousRow !== 0 && page.previousRow === page.nextRow)
  ) {
    return false;
  }
  if (
    !isIntegerInRange(page.rowStyle, 0, Number.MAX_SAFE_INTEGER) ||
    !isIntegerInRange(page.headerStyle, 0, Number.MAX_SAFE_INTEGER) ||
    typeof page.rowIndexed !== 'boolean' ||
    !isUnknownArray(page.notices) ||
    page.notices.some((text) => typeof text !== 'string')
  ) {
    return false;
  }
  for (const [at, field] of page.fields.entries()) {
    const column = request.firstColumn + at;
    if (!isRecord(field) || field.column !== column) return false;
    if (!isValidRecordCell(field.cell, page.row, column, colName)) return false;
    if (page.headerRow) {
      if (!isValidRecordCell(field.header, page.headerRow, column, colName)) return false;
    } else if (field.header !== undefined && field.header !== null) {
      return false;
    }
  }
  return true;
}

export function validateRecordCard(
  page: unknown,
  request: RecordCardRequest,
  limits: RecordCardLimits | null | undefined,
  source: Pick<SourceCapabilities, 'maxRows' | 'maxColumns'>,
  colName: (col: number) => string,
  totalColumns: number,
): RecordCardResponse {
  if (!limits || !isValidRecordCardPayload(page, request, limits, source, colName, totalColumns)) {
    fail();
  }
  if (new TextEncoder().encode(JSON.stringify(page)).length > limits.bytes) {
    throw new Error(localize('ui.recordCardModel.recordCardResponseExceedsDisplay'));
  }
  return page;
}

export function recordTextOffsets(text: string, units: number): number[] {
  if (!isIntegerInRange(units, 2, Number.MAX_SAFE_INTEGER)) {
    throw new Error(localize('ui.recordCardModel.textCapacityRecordCardInvalid'));
  }
  const result: number[] = [0];
  let at = 0;
  while (at < text.length) {
    let end = Math.min(text.length, at + units);
    if (
      end < text.length &&
      text.charCodeAt(end - 1) >= 0xd800 &&
      text.charCodeAt(end - 1) <= 0xdbff &&
      text.charCodeAt(end) >= 0xdc00 &&
      text.charCodeAt(end) <= 0xdfff
    ) {
      --end;
    }
    result.push(end);
    at = end;
  }
  if (result.length === 1) result.push(0);
  return result;
}

export function previewText(text: string, units: number): string {
  if (text.length <= units) return text;
  let end = units;
  if (
    text.charCodeAt(end - 1) >= 0xd800 &&
    text.charCodeAt(end - 1) <= 0xdbff &&
    text.charCodeAt(end) >= 0xdc00 &&
    text.charCodeAt(end) <= 0xdfff
  ) {
    --end;
  }
  return text.slice(0, end);
}

export function recordCellValue(cell: CellData, mode: RecordCardValueMode): string | null {
  return cell.cacheMissing ? null : mode === 'raw' ? cell.raw : cellDisplayValue(cell, false, true);
}

export function recordCellFormula(cell: CellData): string | null {
  return cellFormulaText(cell);
}

export function recordCellType(cell: CellData): string {
  return cellSourceTypeLabel(cell);
}

export const RecordCardModel = Object.freeze({
  validate: validateRecordCard,
  offsets: recordTextOffsets,
  preview: previewText,
  value: recordCellValue,
  formula: recordCellFormula,
  type: recordCellType,
});
