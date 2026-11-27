/**
 * Compact viewport wire protocol decoder (`compact=1` responses from `/api/sheet`
 * and `/api/preview`). Reconstructs full `RowData` and `CellData` models.
 */
import type { CellData, RowData, WireRow } from '../contracts/workbook.ts';
import { columnName } from './cell-address.ts';

const own = (value: object, key: string): boolean =>
  Object.prototype.hasOwnProperty.call(value, key);

export function unpackWireRows(rows: readonly WireRow[]): RowData[] {
  return rows.map((row) => {
    const suffix = String(row.n);
    const cells: CellData[] = row.c.map((value) => {
      const flags = value.k ?? 0;
      const cell: CellData = {
        col: value.c,
        ref: own(value, 'r') && value.r !== undefined ? value.r : `${columnName(value.c)}${suffix}`,
        raw: value.v,
        display: own(value, 'd') && value.d !== undefined ? value.d : value.v,
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
      if (value.h) cell.hyperlink = value.h;
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
