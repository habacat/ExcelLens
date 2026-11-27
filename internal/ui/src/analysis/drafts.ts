/**
 * Unsaved analysis edits stay separate from the last durable server snapshot.
 * Acknowledging an older request must never erase a newer draft.
 */
import type { AnalysisCellChange } from '../contracts/analysis.ts';

export interface AnalysisDraftRecord {
  readonly book: string;
  readonly id: string;
  readonly row: number;
  readonly col: number;
  readonly value: string;
}

export interface AnalysisDraftEntry extends AnalysisDraftRecord {
  readonly key: string;
}

function isPlainRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

export class AnalysisDrafts {
  public readonly items = new Map<string, AnalysisDraftEntry>();
  public onChange: ((key: string, data: AnalysisDraftRecord | null) => void) | null = null;

  key(book: string, id: string, row: number, col: number): string {
    return JSON.stringify([book, id, row, col]);
  }

  stage(book: string, id: string, changes: readonly AnalysisCellChange[]): AnalysisDraftEntry[] {
    return changes.map((change) => {
      const key = this.key(book, id, change.row, change.col);
      const entry: AnalysisDraftEntry = {
        book,
        id,
        row: change.row,
        col: change.col,
        value: change.value,
        key,
      };
      this.items.set(key, entry);
      this.onChange?.(key, {
        book,
        id,
        row: entry.row,
        col: entry.col,
        value: entry.value,
      });
      return entry;
    });
  }

  acknowledge(entries: readonly AnalysisDraftEntry[]): void {
    for (const entry of entries) {
      if (this.items.get(entry.key) === entry) {
        this.items.delete(entry.key);
        this.onChange?.(entry.key, null);
      }
    }
  }

  restore(records: Readonly<Record<string, unknown>>): void {
    const items = Object.entries(records);
    const validated: (readonly [string, AnalysisDraftRecord])[] = [];
    for (const [key, raw] of items) {
      if (
        !isPlainRecord(raw) ||
        typeof raw.book !== 'string' ||
        typeof raw.id !== 'string' ||
        typeof raw.row !== 'number' ||
        !Number.isSafeInteger(raw.row) ||
        raw.row < 0 ||
        raw.row >= 10000 ||
        typeof raw.col !== 'number' ||
        !Number.isSafeInteger(raw.col) ||
        raw.col < 0 ||
        raw.col >= 80 ||
        typeof raw.value !== 'string' ||
        Array.from(raw.value).length > 4000 ||
        this.key(raw.book, raw.id, raw.row, raw.col) !== key
      ) {
        throw new Error('分析草稿恢复内容无效');
      }
      validated.push([
        key,
        {
          book: raw.book,
          id: raw.id,
          row: raw.row,
          col: raw.col,
          value: raw.value,
        },
      ]);
    }
    for (const [key, value] of validated) {
      if (!this.items.has(key)) {
        this.items.set(key, { ...value, key });
      }
    }
  }

  get(book: string, id: string, row: number, col: number): AnalysisDraftEntry | undefined {
    return this.items.get(this.key(book, id, row, col));
  }

  entries(book: string): AnalysisDraftEntry[] {
    return [...this.items.values()].filter((entry) => entry.book === book);
  }

  get size(): number {
    return this.items.size;
  }
}
