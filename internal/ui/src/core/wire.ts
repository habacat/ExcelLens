/**
 * Compact viewport wire protocol decoder (`compact=1` responses from `/api/sheet`
 * and `/api/preview`). Reconstructs full `RowData` and `CellData` models.
 */
import { decodeCompactRowWire } from './http-decoders.generated.ts';
import type { CellData, RowData } from '../contracts/workbook.ts';
import { columnName } from './cell-address.ts';

export function unpackWireRows(rows: unknown): RowData[] {
  if (!Array.isArray(rows)) throw new Error('工作表响应缺少行列表');
  return rows.map((input: unknown) => {
    const row = decodeCompactRowWire(input);
    if (!Number.isSafeInteger(row.n) || row.n < 1 || !row.c) throw new Error('工作表行坐标或单元格列表无效');
    const suffix = String(row.n);
    const cells: CellData[] = row.c.map((value) => {
      if (!Number.isSafeInteger(value.c) || value.c < 1) throw new Error('工作表列坐标无效');
      const flags = value.k ?? 0;
      if (!Number.isInteger(flags) || flags < 0 || flags > 255) throw new Error('工作表单元格标志无效');
      const cell: CellData = {
        col: value.c,
        ref: value.r ?? `${columnName(value.c)}${suffix}`,
        raw: value.v,
        display: value.d ?? value.v,
        hasFormula: Boolean(flags & 1),
      };
      if (value.t) cell.type = value.t;
      if (value.s || flags & 8) cell.style = value.s ?? 0;
      if (value.f) cell.formula = value.f;
      if (flags & 2) cell.cacheMissing = true;
      if (flags & 4) cell.sharedFormula = true;
      if (value.st) cell.sourceType = value.st;
      if (value.cu) cell.currency = value.cu;
      if (value.o) cell.valueColor = value.o;
      if (value.h) {
        const kind = value.h.kind;
        if (kind !== 'internal' && kind !== 'external') throw new Error('工作表超链接类型无效');
        cell.hyperlink = { ...value.h, kind };
      }
      if (value.x) cell.richText = value.x;
      if (value.q) cell.conditional = value.q;
      if (flags & 16) cell.presentationOnly = true;
      return cell;
    });
    const out: RowData = { number: row.n, cells };
    if (row.s) out.style = row.s;
    if (row.p) out.presentationNotice = row.p;
    return out;
  });
}
