/**
 * A displayed result and its server task are one snapshot. Bound dashboard
 * retention without serializing and duplicating large result strings.
 */
import type {
  AnalysisCell,
  AnalysisField,
  AnalysisQuery,
  AnalysisResult,
  AnalysisResultRow,
} from '../contracts/analysis.ts';

export interface WorkspaceAnalysisQuery extends AnalysisQuery {
  title?: string;
  sourceId?: string;
}

export interface AnalysisResultSnapshot {
  readonly book: string;
  readonly query: WorkspaceAnalysisQuery;
  readonly job: string;
  readonly result: AnalysisResult;
  readonly sourceFields?: readonly AnalysisField[];
}

export interface AnalysisResultCacheInput {
  readonly job: string;
  readonly result: AnalysisResult;
  readonly sourceFields?: readonly AnalysisField[];
}

interface CacheEntry {
  readonly snapshot: AnalysisResultSnapshot;
  readonly bytes: number;
}

const EMPTY_CELL: AnalysisCell = Object.freeze({
  display: '',
  exact: '',
  count: 0,
});

function isUnknownArray(value: unknown): value is readonly unknown[] {
  return Array.isArray(value);
}

export function retainedBytes(value: unknown, limit: number): number {
  let size = 0;
  const seen = new WeakSet<object>();
  function visit(item: unknown): void {
    if (size > limit) return;
    if (typeof item === 'string') {
      size += 16 + item.length * 2;
      return;
    }
    if (!item || typeof item !== 'object') {
      size += 8;
      return;
    }
    if (seen.has(item)) return;
    seen.add(item);
    size += 64;
    if (isUnknownArray(item)) {
      size += item.length * 8;
      for (const child of item) {
        visit(child);
        if (size > limit) break;
      }
    } else {
      const record = item as Record<string, unknown>;
      for (const key of Object.keys(record)) {
        size += 16 + key.length * 2;
        visit(record[key]);
        if (size > limit) break;
      }
    }
  }
  visit(value);
  return size;
}

/**
 * Screen and image exports must select the same completed metric. Never
 * infer from builder fields that may already belong to another source.
 */
export function metricCell(
  result: AnalysisResult,
  row: AnalysisResultRow | null,
  columnIndex: number,
  metricIndex: number,
  total = false,
): AnalysisCell {
  if (row) {
    if (total) {
      return row.metricTotals?.[metricIndex] ?? (metricIndex === 0 ? row.total : EMPTY_CELL);
    }
    return (
      row.metricValues?.[columnIndex]?.[metricIndex] ??
      (metricIndex === 0 ? row.values[columnIndex] : undefined) ??
      EMPTY_CELL
    );
  }
  if (total) {
    return result.metricGrand?.[metricIndex] ?? (metricIndex === 0 ? result.grand : EMPTY_CELL);
  }
  return (
    result.metricColumnTotals?.[columnIndex]?.[metricIndex] ??
    (metricIndex === 0 ? result.totals[columnIndex] : undefined) ??
    EMPTY_CELL
  );
}

export function projectMetric(result: AnalysisResult, index: number): AnalysisResult {
  if (index === 0) return result;
  return {
    ...result,
    grand: metricCell(result, null, 0, index, true),
    totals: result.columnKeys.map((_, j) => metricCell(result, null, j, index)),
    rows: result.rows.map((row) => ({
      ...row,
      total: metricCell(result, row, 0, index, true),
      values: result.columnKeys.map((_, j) => metricCell(result, row, j, index)),
    })),
  };
}

export class AnalysisResultCache {
  private readonly maxEntries: number;
  private readonly maxBytes: number;
  private readonly entries = new Map<string, CacheEntry>();
  public bytes = 0;

  constructor(options: { readonly maxEntries?: number; readonly maxBytes?: number } = {}) {
    this.maxEntries = options.maxEntries ?? 8;
    this.maxBytes = options.maxBytes ?? 24 * 1024 * 1024;
  }

  get size(): number {
    return this.entries.size;
  }

  key(book: string, query: AnalysisQuery): string {
    return JSON.stringify([book, query]);
  }

  clear(): void {
    this.entries.clear();
    this.bytes = 0;
  }

  remove(key: string): void {
    const old = this.entries.get(key);
    if (old) {
      this.bytes -= old.bytes;
      this.entries.delete(key);
    }
  }

  get(book: string, query: AnalysisQuery): AnalysisResultSnapshot | null {
    const key = this.key(book, query);
    const entry = this.entries.get(key);
    if (!entry) return null;
    this.entries.delete(key);
    this.entries.set(key, entry);
    return entry.snapshot;
  }

  set(book: string, query: WorkspaceAnalysisQuery, input: AnalysisResultCacheInput): AnalysisResultSnapshot {
    const key = this.key(book, query);
    const clonedQuery = JSON.parse(JSON.stringify(query)) as WorkspaceAnalysisQuery;
    const snapshot: AnalysisResultSnapshot = {
      book,
      query: clonedQuery,
      job: input.job,
      result: input.result,
      ...(input.sourceFields ? { sourceFields: input.sourceFields.map((field) => ({ ...field })) } : {}),
    };
    this.remove(key);
    const bytes = retainedBytes(snapshot, this.maxBytes) + key.length * 2 + 64;
    if (bytes > this.maxBytes || this.maxEntries < 1) return snapshot;
    while (this.entries.size >= this.maxEntries || this.bytes + bytes > this.maxBytes) {
      const oldest = this.entries.keys().next();
      if (oldest.done) break;
      this.remove(oldest.value);
    }
    this.entries.set(key, { snapshot, bytes });
    this.bytes += bytes;
    return snapshot;
  }
}
