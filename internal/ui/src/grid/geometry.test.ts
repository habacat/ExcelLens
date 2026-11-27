import { test } from 'node:test';
import assert from 'node:assert/strict';
import { type Axis, Geometry } from './geometry.ts';

function slowAt(axis: Axis, pos: number): number {
  let lo = 1;
  let hi = axis.max;
  while (lo < hi) {
    const mid = Math.floor((lo + hi + 1) / 2);
    if (axis.offset(mid) <= pos) lo = mid;
    else hi = mid - 1;
  }
  return lo;
}

test('row header follows the largest rendered row number and aligns to device pixels', () => {
  const cases: readonly (readonly [number, number])[] = [
    [1, 26],
    [18, 26],
    [99, 26],
    [100, 30],
    [1000, 36],
    [9000, 36],
    [90000, 42],
    [1000000, 54],
    [50000000, 60],
    [1000000000, 72],
  ];
  for (const [row, expected] of cases) {
    assert.equal(Geometry.rowHeaderWidth(row), expected);
    for (const dpr of [1, 1.25, 1.5, 2]) {
      const width = Geometry.rowHeaderWidth(row, dpr);
      assert.ok(width >= expected);
      assert.ok(width < expected + 1 / dpr + 0.0001);
      assert.ok(Math.abs(width * dpr - Math.round(width * dpr)) < 0.00001);
    }
  }
});

test('sparse resize, inverse mapping, and million-row offsets remain exact', () => {
  const g = new Geometry(1_000_000_000, 16_384);
  assert.equal(g.rows.offset(4), 75);
  g.rows.set(2, 50);
  g.rows.set(4, 30);
  assert.equal(g.rows.offset(4), 100);
  assert.equal(g.rows.offset(5), 130);
  assert.equal(g.rows.at(20), 1);
  assert.equal(g.rows.at(25), 2);
  assert.equal(g.rows.at(74), 2);
  assert.equal(g.rows.at(75), 3);
  g.cols.set(3, 300);
  assert.equal(g.cols.offset(4), 132 * 2 + 300);
  g.load({ rows: { '1000000': 200 }, cols: { '16000': 350 } });
  assert.equal(g.rows.offset(1_000_000), 999_999 * 25);
  assert.equal(g.rows.offset(1_000_001), 999_999 * 25 + 200);
  assert.equal(g.cols.at(g.cols.offset(16_000)), 16_000);
  assert.equal(g.cols.at(g.cols.offset(16_001) - 1), 16_000);
});

test('billion-row geometry keeps sparse runs, exact tail positions and the injected ceiling', () => {
  const g = new Geometry(1_000_000_000, 16_384);
  g.rows.load({ '500000000': 40 }, [{ min: 1, max: 1_000_000_000, size: 17.5 }]);
  assert.equal(g.rows.source.length, 1);
  assert.equal(g.rows.values.size, 1);
  assert.equal(g.rows.total(1_000_000_000), 17_500_000_022.5);
  assert.equal(g.rows.at(g.rows.offset(1_000_000_000)), 1_000_000_000);
  assert.equal(g.rows.set(1_000_000_001, 30), false);
});

test('source row runs preserve fractional heights, local overrides and inverse positions', () => {
  const g = new Geometry(1_000_000_000, 16_384);
  const runs = [
    { min: 1, max: 2, size: 17.5 },
    { min: 4, max: 7, size: 31.25 },
    { min: 9, max: 10, size: 14.125 },
  ];
  const overrides: Record<string, number> = { '2': 44, '5': 18 };
  g.rows.load(overrides, runs);
  let top = 0;
  for (let i = 1; i <= 30; i++) {
    const size = overrides[String(i)] ?? runs.find((r) => r.min <= i && r.max >= i)?.size ?? 25;
    assert.equal(g.rows.measure(i), size);
    assert.equal(g.rows.offset(i), top);
    for (const y of [top, top + 0.01, top + size / 2, top + size - 0.01]) {
      assert.equal(g.rows.at(y), i, `row ${i} at ${y}`);
    }
    top += size;
  }
  assert.equal(g.rows.total(30), top);
  g.rows.set(2, 0);
  assert.equal(g.rows.measure(2), 17.5);
  g.rows.load({});
  assert.equal(g.rows.measure(1), 25);
  assert.equal(g.rows.offset(3), 50);
});

test('fractional source columns share the same device-pixel edge at every zoom', () => {
  const axis = new Geometry(1_000_000_000, 16_384).cols;
  axis.load({
    '1': 47.263,
    '2': 85.057,
    '3': 151.189,
    '4': 160.658,
    '5': 56.732,
    '6': 103.927,
    '7': 103.927,
    '8': 94.526,
    '9': 94.457,
  });
  for (const ratio of [1, 1.25, 1.5, 2]) {
    for (const scale of [0.8, 1, 1.1, 1.25, 1.5]) {
      for (let col = 1; col <= 12; col++) {
        const left = axis.paintOffset(col, scale, ratio);
        const width = axis.paintSize(col, scale, ratio);
        const right = axis.paintOffset(col + 1, scale, ratio);
        assert.ok(Math.abs(left + width - right) < 1e-8);
        assert.ok(Math.abs(right * ratio - Math.round(right * ratio)) < 1e-8);
      }
    }
  }
});

test('fifty million source rows require one size run and sparse user overrides', () => {
  const g = new Geometry(1_000_000_000, 16_384);
  g.rows.load({ '25000000': 40 }, [{ min: 1, max: 50_000_000, size: 17.5 }]);
  assert.equal(g.rows.source.length, 1);
  assert.equal(g.rows.values.size, 1);
  assert.equal(g.rows.offset(50_000_000), 49_999_999 * 17.5 + 22.5);
  assert.equal(g.rows.at(g.rows.offset(50_000_000)), 50_000_000);
  assert.equal(g.rows.at(g.rows.offset(25_000_000) + 39), 25_000_000);
  assert.equal(g.rows.at(g.rows.offset(25_000_001)), 25_000_001);
  g.rows.setBase(30);
  assert.equal(g.rows.measure(1), 17.5);
  assert.equal(g.rows.total(50_000_000), 50_000_000 * 17.5 + 22.5);
});

test('geometry inverse lookup matches binary search oracle across dense overrides and fractional bases', () => {
  const g = new Geometry(1_000_000_000, 16_384);
  const changes: Record<string, number> = {};
  for (let i = 13; i < 1_048_576; i += 83) changes[String(i)] = 10 + (i % 90);
  g.rows.load(changes);
  const pixels = Array.from({ length: 3500 }, (_, i) => (i * 389419) % 25_000_000);
  for (const pixel of pixels) {
    assert.equal(g.rows.at(pixel), slowAt(g.rows, pixel), `pixel ${pixel}`);
  }
  for (let i = 100; i < 1_048_576; i += 787) {
    const start = g.rows.offset(i);
    const end = g.rows.offset(i + 1);
    for (const p of [start, Math.max(0, end - 1e-4), end]) {
      assert.equal(g.rows.at(p), slowAt(g.rows, p), `row ${i} pixel ${p}`);
    }
  }

  let seed = 37;
  const rand = (): number => {
    seed = (seed * 1664525 + 1013904223) >>> 0;
    return seed / 4294967296;
  };
  for (let batch = 0; batch < 24; batch++) {
    const axis = batch % 2 ? g.cols : g.rows;
    axis.setBase(12.5 + rand() * 13.4);
    const custom: Record<string, number> = {};
    for (let i = 0; i < 240; i++) {
      custom[String(Math.floor(1 + rand() * axis.max))] = 10 + rand() * 590;
    }
    axis.load(custom);
    for (let i = 0; i < 240; i++) {
      const cell = Math.floor(1 + rand() * axis.max);
      const candidates = [
        axis.offset(cell),
        axis.offset(cell + 1),
        axis.offset(cell) + rand() * axis.measure(cell),
        rand() * axis.total(axis.max),
        Math.max(0, axis.offset(cell + 1) - 1e-8),
      ];
      const pixel = candidates[i % 5] ?? 0;
      assert.equal(
        axis.at(pixel),
        slowAt(axis, pixel),
        `fractional batch=${batch} cell=${cell} pixel=${pixel}`,
      );
    }
  }
});
