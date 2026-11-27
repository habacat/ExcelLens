/**
 * Bounded LRU viewport tile cache, coverage predicates, tile projection across
 * adjacent row/column windows, and cached hit-testing.
 */
import type { MergeRange } from '../contracts/common.ts';
import type { CellData, RowData, SheetViewportResponse } from '../contracts/workbook.ts';
import { estimateConditionalBytes } from './conditional.ts';
import { estimateRichTextBytes } from './richtext.ts';
import type { ViewportBounds, ViewportEntry, ViewportRequest } from './state.ts';

export type TileCacheEntry = ViewportEntry;

export interface TileProjectedResult {
  readonly request: ViewportRequest;
  readonly data: SheetViewportResponse;
  readonly bounds: ViewportBounds;
}

export interface SourcePointHit {
  readonly cell: CellData | undefined;
  readonly row: number;
  readonly col: number;
  readonly visual: number;
  readonly crosshair: { readonly row: number; readonly col: number } | null;
  ref?: string;
}

export function viewportBounds(
  data: Pick<SheetViewportResponse, 'columns' | 'rows' | 'startCol' | 'colCount'>,
  request: ViewportRequest,
): ViewportBounds {
  const columns = data.columns?.length ? data.columns : null;
  return {
    context: request.context,
    filtered: request.filtered,
    filterId: request.filterId,
    viewId: request.viewId || '',
    excludeThroughRow: request.excludeThroughRow,
    displayStart: request.displayStart,
    displayEnd: request.filtered ? request.displayStart + data.rows.length - 1 : request.displayEnd,
    startCol: data.startCol,
    endCol: columns ? (columns[columns.length - 1] ?? data.startCol) : data.startCol + data.colCount - 1,
    columns,
  };
}

function coversColumnRange(bounds: ViewportBounds, first: number, last: number): boolean {
  if (bounds.startCol > first || bounds.endCol < last) return false;
  if (!bounds.columns) return true;
  const available = (bounds.columnSet ??= new Set(bounds.columns));
  if (last - first + 1 > available.size) return false;
  for (let col = first; col <= last; col++) {
    if (!available.has(col)) return false;
  }
  return true;
}

function hasRequestedColumns(
  bounds: ViewportBounds,
  request: ViewportRequest,
  visibleOnly: boolean,
): boolean {
  if (request.visibleColEnd < request.visibleColStart) return true;
  if (!request.selectedCols) {
    const first = visibleOnly ? request.visibleColStart : request.startCol;
    const last = visibleOnly ? request.visibleColEnd : request.startCol + request.cols - 1;
    return coversColumnRange(bounds, first, last);
  }
  const available = bounds.columns ? (bounds.columnSet ??= new Set(bounds.columns)) : null;
  for (const col of request.selectedCols) {
    if (visibleOnly && (col < request.visibleColStart || col > request.visibleColEnd)) continue;
    if (col < bounds.startCol || col > bounds.endCol || (available && !available.has(col))) {
      return false;
    }
  }
  return true;
}

export function sameViewportRows(
  bounds: ViewportBounds | null | undefined,
  request: ViewportRequest,
): boolean {
  return (
    Boolean(bounds) &&
    bounds?.filtered === request.filtered &&
    bounds.filterId === request.filterId &&
    (bounds.viewId || '') === (request.viewId || '') &&
    bounds.excludeThroughRow === request.excludeThroughRow &&
    (bounds.context || '') === (request.context || '')
  );
}

function containsCol(bounds: ViewportBounds, col: number): boolean {
  return (
    col >= bounds.startCol &&
    col <= bounds.endCol &&
    (!bounds.columns || (bounds.columnSet ??= new Set(bounds.columns)).has(col))
  );
}

export function viewportCoversVisible(
  bounds: ViewportBounds | null | undefined,
  request: ViewportRequest,
): boolean {
  if (!bounds || !sameViewportRows(bounds, request)) return false;
  if (request.visibleEnd < request.visibleStart) return true;
  return (
    bounds.displayStart <= request.visibleStart &&
    bounds.displayEnd >= request.visibleEnd &&
    hasRequestedColumns(bounds, request, true)
  );
}

export function viewportHasHeadroom(
  bounds: ViewportBounds | null | undefined,
  request: ViewportRequest,
): boolean {
  if (!bounds || !viewportCoversVisible(bounds, request)) return false;
  const row = request.leadRow;
  const col = request.leadCol;
  return row >= bounds.displayStart && row <= bounds.displayEnd && containsCol(bounds, col);
}

export function viewportCoversRequest(
  bounds: ViewportBounds | null | undefined,
  request: ViewportRequest,
): boolean {
  if (!bounds || !sameViewportRows(bounds, request)) return false;
  return (
    bounds.displayStart <= request.displayStart &&
    bounds.displayEnd >= request.displayEnd &&
    hasRequestedColumns(bounds, request, false)
  );
}

export function viewportKey(epoch: number, request: ViewportRequest): string {
  return [
    epoch,
    request.context || '',
    request.filtered,
    request.filterId,
    request.viewId || '',
    request.displayStart,
    request.limit,
    request.startCol,
    request.cols,
    request.selectedCols?.join(',') ?? '',
    request.excludeThroughRow,
  ].join('|');
}

export function estimateViewportBytes(
  data: Pick<SheetViewportResponse, 'rows'> | null | undefined,
): number {
  let size = 256;
  for (const row of data?.rows ?? []) {
    size += 64 + 2 * (row.presentationNotice?.length ?? 0);
    for (const cell of row.cells) {
      size +=
        176 +
        2 *
          (cell.raw.length +
            cell.display.length +
            (cell.formula?.length ?? 0) +
            cell.ref.length +
            (cell.hyperlink?.url?.length ?? 0) +
            (cell.hyperlink?.sheet?.length ?? 0) +
            (cell.hyperlink?.ref?.length ?? 0)) +
        estimateRichTextBytes(cell.richText) +
        estimateConditionalBytes(cell.conditional);
    }
  }
  return size;
}

const columnIndex = new WeakMap<RowData, Record<number, CellData | undefined>>();

export function rowColumns(
  row: RowData | null | undefined,
): Record<number, CellData | undefined> | null {
  if (!row) return null;
  let columns = columnIndex.get(row);
  if (!columns) {
    columns = Object.create(null) as Record<number, CellData | undefined>;
    for (const cell of row.cells) columns[cell.col] = cell;
    columnIndex.set(row, columns);
  }
  return columns;
}

function tileRow(entry: TileCacheEntry, visual: number): RowData | undefined {
  if (entry.bounds.filtered) return entry.data.rows[visual - entry.bounds.displayStart];
  entry.rowMap ??= new Map(entry.data.rows.map((row) => [row.number, row]));
  return entry.rowMap.get(visual);
}

function indexAtPixel(pixel: number, last: number, edge: (idx: number) => number): number {
  if (last < 1 || pixel < 0 || pixel >= edge(last + 1)) return 0;
  let low = 1;
  let high = last;
  while (low < high) {
    const mid = Math.floor((low + high) / 2);
    if (edge(mid + 1) <= pixel) low = mid + 1;
    else high = mid;
  }
  return low;
}

export class TileCache {
  public readonly budget: number;
  public readonly maxTiles: number;
  public readonly entries = new Map<string, TileCacheEntry>();
  public bytes = 0;
  public revision = 0;

  constructor(budget: number, maxTiles = 8) {
    this.budget = budget;
    this.maxTiles = maxTiles;
  }

  get size(): number {
    return this.entries.size;
  }

  clear(): void {
    this.revision++;
    this.entries.clear();
    this.bytes = 0;
  }

  store(key: string, data: SheetViewportResponse, request: ViewportRequest): TileCacheEntry {
    this.revision++;
    const old = this.entries.get(key);
    if (old) {
      this.entries.delete(key);
      this.bytes -= old.bytes;
    }
    const entry: TileCacheEntry = {
      data,
      request,
      bounds: viewportBounds(data, request),
      bytes: estimateViewportBytes(data),
    };
    if (entry.bytes > this.budget) return entry;
    this.entries.set(key, entry);
    this.bytes += entry.bytes;
    while (this.size > 1 && (this.size > this.maxTiles || this.bytes > this.budget)) {
      const oldest = this.entries.keys().next().value;
      if (oldest === undefined) break;
      this.bytes -= this.entries.get(oldest)?.bytes ?? 0;
      this.entries.delete(oldest);
    }
    return entry;
  }

  reader(
    request: ViewportRequest,
    received: readonly TileCacheEntry[] = [],
  ): (visual: number, col: number) => { readonly row: RowData | undefined; readonly cell: CellData | undefined } | null {
    const entries = [...received, ...[...this.entries.values()].reverse()].filter((entry) =>
      sameViewportRows(entry.bounds, request),
    );
    return (visual: number, col: number) => {
      for (const entry of entries) {
        const b = entry.bounds;
        if (visual < b.displayStart || visual > b.displayEnd || !containsCol(b, col)) continue;
        const row = tileRow(entry, visual);
        return { row, cell: rowColumns(row)?.[col] };
      }
      return null;
    };
  }

  sourceAtPoint(
    request: ViewportRequest,
    params: {
      readonly x: number;
      readonly y: number;
      readonly lastRow: number;
      readonly lastCol: number;
      readonly rowTop: (row: number) => number;
      readonly colLeft: (col: number) => number;
      readonly mergeAt: (row: number, col: number) => MergeRange | null;
    },
  ): SourcePointHit | null {
    const col = indexAtPixel(params.x, params.lastCol, params.colLeft);
    const visual = indexAtPixel(params.y, params.lastRow, params.rowTop);
    if (!col || !visual) return null;
    const merge = request.filtered ? null : params.mergeAt(visual, col);
    const anchor = merge?.row ?? visual;
    const sourceCol = merge?.col ?? col;
    const record = this.reader(request)(anchor, sourceCol);
    if (!record || (request.filtered && !record.row)) return null;
    return {
      cell: record.cell,
      row: record.row?.number ?? anchor,
      col: sourceCol,
      visual: anchor,
      crosshair: merge ? { row: visual, col } : null,
    };
  }

  project(
    request: ViewportRequest,
    visuals: readonly number[],
    received: readonly TileCacheEntry[] = [],
  ): TileProjectedResult | null {
    const columns =
      request.selectedCols ?? Array.from({ length: request.cols }, (_, i) => request.startCol + i);
    const firstCol = columns[0];
    const lastCol = columns.at(-1);
    if (firstCol === undefined || lastCol === undefined) return null;
    const entries = [...new Set([...received, ...[...this.entries.values()].reverse()])].filter(
      ({ bounds: b }) =>
        sameViewportRows(b, request) &&
        b.displayStart <= Math.max(request.displayStart, request.displayEnd) &&
        b.displayEnd >= request.displayStart - 1 &&
        b.startCol <= lastCol &&
        b.endCol >= firstCol,
    );
    const firstEntry = entries[0];
    if (!firstEntry) return null;
    const meta = firstEntry.data;
    const rows: RowData[] = [];
    let owners: (TileCacheEntry | undefined)[] = [];
    let until = -1;
    let linkError = false;

    for (const visual of visuals) {
      if (request.filtered && visual > meta.totalRows) break;
      if (visual > until) {
        const available = entries.filter(
          ({ bounds: b }) => b.displayStart <= visual && b.displayEnd >= visual,
        );
        owners = columns.map((col) => available.find((e) => containsCol(e.bounds, col)));
        if (owners.some((owner) => !owner)) return null;
        until = Math.min(
          ...owners.map((e) => e?.bounds.displayEnd ?? 0),
          ...entries
            .filter((e) => e.bounds.displayStart > visual)
            .map((e) => e.bounds.displayStart - 1),
        );
      }
      let model: RowData | undefined;
      let owner: TileCacheEntry | undefined;
      let byCol: Record<number, CellData | undefined> | null = null;
      let notice = '';
      const cells: CellData[] = [];
      for (let i = 0; i < columns.length; i++) {
        const nextOwner = owners[i];
        if (!nextOwner) return null;
        if (nextOwner !== owner) {
          owner = nextOwner;
          const source = tileRow(owner, visual);
          if (request.filtered && (!source || (model && model.number !== source.number))) {
            return null;
          }
          model ??= source;
          byCol = rowColumns(source);
          if (source?.presentationNotice && !notice.includes(source.presentationNotice)) {
            notice += (notice ? '；' : '') + source.presentationNotice;
          }
          linkError ||= Boolean(owner.data.hyperlinkError);
        }
        const col = columns[i];
        const cell = col !== undefined ? byCol?.[col] : undefined;
        if (cell) cells.push(cell);
      }
      if (model) {
        rows.push(notice ? { ...model, cells, presentationNotice: notice } : { ...model, cells });
      }
    }
    const data: SheetViewportResponse = {
      ...meta,
      hyperlinkError: linkError ? '1' : '',
      rows,
      startCol: request.startCol,
      colCount: columns.length,
      columns: request.selectedCols,
    };
    return { request, data, bounds: viewportBounds(data, request) };
  }

  find(key: string, request: ViewportRequest): TileCacheEntry | null {
    let foundKey = key;
    let entry: TileCacheEntry | null = this.entries.get(key) ?? null;
    if (entry && !viewportCoversRequest(entry.bounds, request)) entry = null;
    if (!entry) {
      for (const [candidateKey, candidate] of this.entries) {
        if (viewportCoversRequest(candidate.bounds, request)) {
          foundKey = candidateKey;
          entry = candidate;
        }
      }
    }
    if (!entry) return null;
    this.entries.delete(foundKey);
    this.entries.set(foundKey, entry);
    return entry;
  }
}

export const ViewportModel = Object.freeze({
  TileCache,
  viewportBounds,
  sameViewportRows,
  viewportCoversVisible,
  viewportCoversRequest,
  viewportHasHeadroom,
  viewportKey,
  estimateViewportBytes,
  rowColumns,
});
