/**
 * A1 column/cell address conversion shared by rendering, editing, printing and analysis.
 * Source limits are supplied by the caller; the cache only limits retained memory.
 */
import type { CellRef } from '../contracts/common.ts';

const labels = new Map<number, string>();

export function columnName(column: number): string {
  if (!Number.isSafeInteger(column) || column < 1) return '';
  const cached = labels.get(column);
  if (cached !== undefined) return cached;
  let text = '';
  for (let n = column; n > 0; n = Math.floor((n - 1) / 26)) {
    text = String.fromCharCode(65 + ((n - 1) % 26)) + text;
  }
  if (labels.size < 16384) labels.set(column, text);
  return text;
}

export function columnNumber(letters: string): number {
  if (!/^[A-Z]+$/i.test(letters)) return 0;
  let value = 0;
  for (const ch of letters.toUpperCase()) {
    value = value * 26 + ch.charCodeAt(0) - 64;
  }
  return Number.isSafeInteger(value) ? value : 0;
}

export function parseColumn(value: string | number | null | undefined, maxColumns: number): number {
  const text = String(value ?? '').trim().replaceAll('$', '');
  const col = /^\d+$/.test(text) ? Number(text) : columnNumber(text);
  return Number.isSafeInteger(col) && col > 0 && col <= maxColumns ? col : 0;
}

export function parseRef(
  value: string | null | undefined,
  maxRows: number,
  maxColumns: number,
): CellRef | null {
  const text = (value ?? '').trim().replaceAll('$', '');
  const match = /^([A-Za-z]+)([1-9]\d*)$/.exec(text);
  if (!match?.[1] || !match[2]) return null;
  const col = parseColumn(match[1], maxColumns);
  const row = Number(match[2]);
  if (!col || !Number.isSafeInteger(row) || row > maxRows) return null;
  return { col, row, ref: `${columnName(col)}${row}` };
}
