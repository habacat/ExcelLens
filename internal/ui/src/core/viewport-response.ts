import type {
  CellData,
  PreviewResponse,
  RowData,
  SheetViewportResponse,
} from '../contracts/workbook.ts';
import {
  decodePlainRowWire,
  decodePreviewHeaderWire,
  decodeViewportHeaderWire,
} from './http-decoders.generated.ts';
import { unpackWireRows } from './wire.ts';

export function decodeSampledRows(value: unknown, compact?: number): RowData[] {
  if (typeof value !== 'object' || value === null || !('rows' in value)) throw new Error('工作表响应缺少行列表');
  if (compact === 1) return unpackWireRows(value.rows);
  if (compact !== undefined && compact !== 0) throw new Error('不支持的工作表传输版本');
  if (value.rows === null) return []; // Plain Go slices can encode an empty selection as null.
  if (!Array.isArray(value.rows)) throw new Error('工作表响应的行列表无效');
  return value.rows.map((item: unknown) => {
    const row = decodePlainRowWire(item);
    if (!Number.isSafeInteger(row.number) || row.number < 1) throw new Error('工作表行坐标无效');
    const cells: CellData[] = (row.cells ?? []).map(cell => {
      if (!Number.isSafeInteger(cell.col) || cell.col < 1) throw new Error('工作表列坐标无效');
      const link = cell.hyperlink;
      if (!link) return { ...cell, hyperlink: undefined };
      const kind = link.kind;
      if (kind !== 'internal' && kind !== 'external') throw new Error('工作表超链接类型无效');
      return { ...cell, hyperlink: { ...link, kind } };
    });
    return { ...row, cells };
  });
}

export function decodeSheetViewport(value: unknown): SheetViewportResponse {
  const header = decodeViewportHeaderWire(value);
  if (!header.sheet || !Number.isSafeInteger(header.offset) || header.offset < 0 ||
      !Number.isSafeInteger(header.limit) || header.limit < 1 ||
      !Number.isSafeInteger(header.startCol) || header.startCol < 1 ||
      !Number.isSafeInteger(header.colCount) || header.colCount < 1) {
    throw new Error('工作表响应的视口坐标无效');
  }
  return { ...header, rows: decodeSampledRows(value, header.compact) };
}

export function decodeSheetPreview(value: unknown): PreviewResponse {
  if (typeof value === 'object' && value !== null && 'available' in value &&
      value.available === false && !('rows' in value) && 'partial' in value && value.partial === true) {
    return { available: false, partial: true };
  }
  const header = decodePreviewHeaderWire(value);
  return { ...header, rows: decodeSampledRows(value, header.compact) };
}
