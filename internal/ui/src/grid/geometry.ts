/**
 * Sparse per-sheet spreadsheet geometry with O(log n) pixel/index lookup.
 * Overrides are in unzoomed CSS pixels stored in a compact prefix-delta index
 * so million-row worksheets never allocate per-row layout arrays.
 */
import { t as localize } from '../core/i18n.ts';
import type { AxisRange, AxisSizeRange } from '../contracts/common.ts';

export class Axis {
  public base: number;
  public readonly max: number;
  public readonly values = new Map<number, number>();
  public positions: number[] = [];
  public deltas: number[] = [];
  public ends: number[] = [];
  public source: AxisSizeRange[] = [];
  public sourceDeltas: number[] = [];
  public hidden: AxisRange[] = [];
  public hiddenPixels: number[] = [];
  public hiddenStarts: number[] = [];
  public version = 0;

  constructor(base: number, max: number) {
    this.base = base;
    this.max = max;
  }

  setHidden(ranges?: readonly AxisRange[] | null): void {
    const clean: AxisRange[] = [];
    let previous = 0;
    for (const r of ranges ?? []) {
      if (
        !Number.isSafeInteger(r.min) ||
        !Number.isSafeInteger(r.max) ||
        r.min <= previous ||
        r.max < r.min ||
        r.max > this.max
      ) {
        throw new Error(localize('ui.geometry.rowVisibilityRangeInvalid'));
      }
      const last = clean.at(-1);
      if (last && last.max + 1 === r.min) last.max = r.max;
      else clean.push({ min: r.min, max: r.max });
      previous = r.max;
    }
    this.hidden = clean;
    this.reindexHidden();
    this.version++;
  }

  hiddenAt(index: number): number {
    let lo = 0;
    let hi = this.hidden.length;
    while (lo < hi) {
      const mid = (lo + hi) >>> 1;
      const entry = this.hidden[mid];
      if (entry && entry.max < index) lo = mid + 1;
      else hi = mid;
    }
    return lo;
  }

  visible(index: number): boolean {
    const r = this.hidden[this.hiddenAt(index)];
    return !r || r.min > index;
  }

  reindexHidden(): void {
    let start = 0;
    let sourceEnd = 0;
    this.hiddenPixels = [];
    this.hiddenStarts = [];
    for (const r of this.hidden) {
      start += this.rawOffset(r.min) - sourceEnd;
      this.hiddenStarts.push(start);
      sourceEnd = this.rawOffset(r.max + 1);
      this.hiddenPixels.push(sourceEnd - start);
    }
  }

  setBase(base: number): void {
    if (this.base !== base) {
      this.base = base;
      this.reindex();
    }
  }

  set(index: number, value: number | null | undefined): boolean {
    if (!Number.isInteger(index) || index < 1 || index > this.max) return false;
    if (value === 0 || value === null || value === undefined) {
      this.values.delete(index);
    } else if (!Number.isFinite(value) || value < 10 || value > 1200) {
      return false;
    } else {
      this.values.set(index, value);
    }
    this.reindex();
    return true;
  }

  load(
    values?: Readonly<Record<string, number>> | null,
    ranges?: readonly AxisSizeRange[] | null,
  ): void {
    this.source = [];
    let last = 0;
    for (const r of ranges ?? []) {
      if (
        Number.isInteger(r.min) &&
        Number.isInteger(r.max) &&
        r.min > last &&
        r.max >= r.min &&
        r.max <= this.max &&
        Number.isFinite(r.size) &&
        r.size >= 10 &&
        r.size <= 1200
      ) {
        this.source.push(r);
        last = r.max;
      }
    }
    this.values.clear();
    for (const [key, v] of Object.entries(values ?? {})) {
      const i = Number(key);
      const n = v;
      if (
        Number.isInteger(i) &&
        i >= 1 &&
        i <= this.max &&
        Number.isFinite(n) &&
        (n === 0 || (n >= 10 && n <= 1200))
      ) {
        this.values.set(i, n);
      }
    }
    this.reindex();
  }

  sourceMeasure(index: number): number {
    let lo = 0;
    let hi = this.source.length;
    while (lo < hi) {
      const mid = (lo + hi) >>> 1;
      const entry = this.source[mid];
      if (entry && entry.max < index) lo = mid + 1;
      else hi = mid;
    }
    const r = this.source[lo];
    return r && r.min <= index ? r.size : this.base;
  }

  sourceOffset(index: number): number {
    let lo = 0;
    let hi = this.source.length;
    while (lo < hi) {
      const mid = (lo + hi) >>> 1;
      const entry = this.source[mid];
      if (entry && entry.max < index) lo = mid + 1;
      else hi = mid;
    }
    const r = this.source[lo];
    const prior = lo > 0 ? (this.sourceDeltas[lo - 1] ?? 0) : 0;
    return (
      (index - 1) * this.base +
      prior +
      (r && r.min < index ? (index - r.min) * (r.size - this.base) : 0)
    );
  }

  reindex(): void {
    let sourceDelta = 0;
    this.sourceDeltas = this.source.map((r) => {
      sourceDelta += (r.max - r.min + 1) * (r.size - this.base);
      return sourceDelta;
    });
    this.positions = [...this.values.keys()].sort((a, b) => a - b);
    this.deltas = [];
    this.ends = [];
    let total = 0;
    for (const p of this.positions) {
      total += (this.values.get(p) ?? 0) - this.sourceMeasure(p);
      this.deltas.push(total);
      this.ends.push(this.sourceOffset(p + 1) + total);
    }
    this.reindexHidden();
    this.version++;
  }

  measure(index: number): number {
    return this.visible(index) ? (this.values.get(index) ?? this.sourceMeasure(index)) : 0;
  }

  rawOffset(index: number): number {
    const row = Math.max(1, Math.min(this.max + 1, Math.floor(index)));
    let lo = 0;
    let hi = this.positions.length;
    while (lo < hi) {
      const mid = (lo + hi) >> 1;
      const pos = this.positions[mid];
      if (pos !== undefined && pos < row) lo = mid + 1;
      else hi = mid;
    }
    return this.sourceOffset(row) + (lo > 0 ? (this.deltas[lo - 1] ?? 0) : 0);
  }

  offset(index: number): number {
    const row = Math.max(1, Math.min(this.max + 1, Math.floor(index)));
    const i = this.hiddenAt(row);
    const r = this.hidden[i];
    if (r && r.min <= row) return this.hiddenStarts[i] ?? 0;
    if (i > 0) {
      const prev = this.hidden[i - 1];
      const prevStart = this.hiddenStarts[i - 1] ?? 0;
      if (prev) return prevStart + (this.rawOffset(row) - this.rawOffset(prev.max + 1));
    }
    return this.rawOffset(row);
  }

  total(count: number): number {
    return this.offset(Math.min(this.max + 1, count + 1));
  }

  paintOffset(index: number, factor = 1, ratio = 1): number {
    return Math.round(this.offset(index) * factor * ratio) / ratio;
  }

  paintSize(index: number, factor = 1, ratio = 1): number {
    return this.paintOffset(index + 1, factor, ratio) - this.paintOffset(index, factor, ratio);
  }

  at(pixel: number): number {
    if (!this.hidden.length) return this.rawAt(pixel);
    const pos = Math.max(0, pixel);
    if (Number.isNaN(pos)) return 1;
    let lo = 0;
    let hi = this.hiddenStarts.length;
    while (lo < hi) {
      const mid = (lo + hi) >>> 1;
      const start = this.hiddenStarts[mid];
      if (start !== undefined && start <= pos) lo = mid + 1;
      else hi = mid;
    }
    let index = this.rawAt(pos + (lo > 0 ? (this.hiddenPixels[lo - 1] ?? 0) : 0));
    if (!this.visible(index)) {
      const r = this.hidden[this.hiddenAt(index)];
      if (r) index = r.max < this.max ? r.max + 1 : Math.max(1, r.min - 1);
    }
    if (index > 1 && this.offset(index) > pos) {
      const previous = index - 1;
      const r = this.hidden[this.hiddenAt(previous)];
      index = r && r.min <= previous ? Math.max(1, r.min - 1) : previous;
    } else if (index < this.max && this.offset(index + 1) <= pos) {
      const next = index + 1;
      const r = this.hidden[this.hiddenAt(next)];
      const candidate = r && r.min <= next ? r.max + 1 : next;
      if (candidate <= this.max) index = candidate;
    }
    return index;
  }

  rawAt(pixel: number): number {
    const pos = Math.max(0, pixel);
    if (Number.isNaN(pos)) return 1;
    if (this.source.length) {
      let low = 1;
      let high = this.max;
      while (low < high) {
        const mid = Math.floor((low + high) / 2);
        if (this.rawOffset(mid + 1) <= pos) low = mid + 1;
        else high = mid;
      }
      return low;
    }
    let lo = 0;
    let hi = this.ends.length;
    while (lo < hi) {
      const mid = (lo + hi) >>> 1;
      const end = this.ends[mid];
      if (end !== undefined && end <= pos) lo = mid + 1;
      else hi = mid;
    }
    const delta = lo > 0 ? (this.deltas[lo - 1] ?? 0) : 0;
    let index = Math.floor((pos - delta) / this.base) + 1;
    const atLo = this.positions[lo];
    if (atLo !== undefined && index > atLo) index = atLo;
    const prevPos = lo > 0 ? this.positions[lo - 1] : undefined;
    if (prevPos !== undefined && index <= prevPos) index = prevPos + 1;
    index = Math.max(1, Math.min(this.max, index));
    const nextCustom = atLo ?? this.max + 1;
    const start = (index - 1) * this.base + delta;
    const end = index * this.base + (index === nextCustom ? (this.deltas[lo] ?? delta) : delta);
    if (index > 1 && start > pos) index--;
    else if (index < this.max && end <= pos) index++;
    return index;
  }
}

export interface GeometryLoadData {
  readonly rows?: Readonly<Record<string, number>> | null;
  readonly cols?: Readonly<Record<string, number>> | null;
}

export class Geometry {
  public readonly rows: Axis;
  public readonly cols: Axis;

  static rowHeaderWidth(maxRow: number, ratio = 1): number {
    const value = maxRow;
    const digits = String(Number.isFinite(value) ? Math.max(1, Math.floor(value)) : 1).length;
    const dpr = Number.isFinite(ratio) && ratio > 0 ? ratio : 1;
    return Math.ceil(Math.max(26, 12 + 6 * digits) * dpr) / dpr;
  }

  constructor(maxRows: number, maxColumns: number) {
    if (
      !Number.isSafeInteger(maxRows) ||
      maxRows < 1 ||
      !Number.isSafeInteger(maxColumns) ||
      maxColumns < 1
    ) {
      throw new Error('Geometry requires injected source limits');
    }
    this.rows = new Axis(25, maxRows);
    this.cols = new Axis(132, maxColumns);
  }

  get version(): string {
    return `${this.rows.version}:${this.cols.version}`;
  }

  load(data?: GeometryLoadData | null): void {
    this.rows.load(data?.rows);
    this.cols.load(data?.cols);
  }

  set(kind: 'row' | 'col', index: number, size: number | null | undefined): boolean {
    return (kind === 'row' ? this.rows : this.cols).set(index, size);
  }
}

export const BASE_ROW = 25;
export const BASE_COL = 132;
