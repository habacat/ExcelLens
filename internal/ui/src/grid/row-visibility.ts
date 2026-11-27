/**
 * Compressed source-row visibility interval index supporting O(log n)
 * rank/select queries without allocating per-row bitmaps.
 */
import type { AxisRange, SelectionArea } from '../contracts/common.ts';

function lowerBound<T>(values: readonly T[], test: (item: T) => boolean): number {
  let lo = 0;
  let hi = values.length;
  while (lo < hi) {
    const mid = (lo + hi) >>> 1;
    const item = values[mid];
    if (item !== undefined && test(item)) hi = mid;
    else lo = mid + 1;
  }
  return lo;
}

export class RowVisibility {
  public readonly maximum: number;
  public readonly hidden: readonly AxisRange[];
  public readonly prefix: readonly number[];
  public readonly length: number;

  constructor(maximum: number, ranges?: readonly AxisRange[] | null) {
    if (!Number.isSafeInteger(maximum) || maximum < 1) {
      throw new Error('源行容量无效');
    }
    this.maximum = maximum;
    const hidden: AxisRange[] = [];
    const prefix: number[] = [];
    let previous = 0;
    let total = 0;
    for (const r of ranges ?? []) {
      if (
        !Number.isSafeInteger(r.min) ||
        !Number.isSafeInteger(r.max) ||
        r.min <= previous ||
        r.max < r.min ||
        r.max > maximum
      ) {
        throw new Error('行显隐区间无效');
      }
      hidden.push(Object.freeze({ min: r.min, max: r.max }));
      total += r.max - r.min + 1;
      prefix.push(total);
      previous = r.max;
    }
    this.hidden = Object.freeze(hidden);
    this.prefix = Object.freeze(prefix);
    this.length = maximum - total;
    Object.freeze(this);
  }

  before(index: number): number {
    const row = Math.max(1, Math.min(this.maximum + 1, index));
    const i = lowerBound(this.hidden, (r) => r.max >= row);
    const r = this.hidden[i];
    const prior = i > 0 ? (this.prefix[i - 1] ?? 0) : 0;
    return row - 1 - prior - (r && r.min < row ? row - r.min : 0);
  }

  visible(row: number): boolean {
    if (!Number.isInteger(row) || row < 1 || row > this.maximum) return false;
    const r = this.hidden[lowerBound(this.hidden, (item) => item.max >= row)];
    return !r || r.min > row;
  }

  at(position: number): number {
    if (!Number.isInteger(position) || position < 0 || position >= this.length) return 0;
    let lo = 0;
    let hi = this.hidden.length;
    while (lo < hi) {
      const mid = (lo + hi) >>> 1;
      const entry = this.hidden[mid];
      const prior = mid > 0 ? (this.prefix[mid - 1] ?? 0) : 0;
      if (entry && entry.min - 1 - prior > position) hi = mid;
      else lo = mid + 1;
    }
    return position + 1 + (lo > 0 ? (this.prefix[lo - 1] ?? 0) : 0);
  }

  next(row: number): number {
    const rank = this.before(Math.max(1, row));
    return this.at(rank);
  }

  previous(row: number): number {
    const last = Math.min(this.maximum, row);
    return this.at(this.before(last + 1) - 1);
  }

  advance(row: number, delta: number): number {
    return this.at(this.before(row) + delta - (!this.visible(row) && delta > 0 ? 1 : 0));
  }

  count(first: number, last: number): number {
    return last < first ? 0 : this.before(last + 1) - this.before(first);
  }

  selectionCount(area: SelectionArea, filtered: boolean, through = 0): number {
    const a = area.row1;
    const b = area.row2;
    if (!filtered) return this.count(Math.min(a, b), Math.max(a, b));
    if (a <= through && b <= through) return this.count(Math.min(a, b), Math.max(a, b));
    if (a <= through) return this.count(a, through) + area.visual2;
    if (b <= through) return this.count(b, through) + area.visual1;
    return Math.abs(area.visual1 - area.visual2) + 1;
  }
}
