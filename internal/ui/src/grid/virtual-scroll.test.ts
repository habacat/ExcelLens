import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { installDom } from '../test-helpers/dom-mock.ts';
import {
  makeDefaultViewportPipelineDeps,
  makeTestFrozenPanes,
} from '../test-helpers/grid-fixture.ts';
import type { GridRowElement } from './state.ts';
import { ViewportPipeline } from './viewport-pipeline.ts';
import { VirtualScroll } from './virtual-scroll.ts';

const HERE = path.dirname(fileURLToPath(import.meta.url));

test('repeated wheel/trackpad down and back lands exactly at row one, at every supported zoom', () => {
  for (const count of [100, 1_048_576, 2_907_951, 50_000_000, 1_000_000_000]) {
    for (const zoom of [0.7, 1, 1.25]) {
      const s = new VirtualScroll();
      const height = 27 * zoom + count * 25 * zoom;
      const view = 763;
      const physicalHeight = s.configure(height, view);
      assert.ok(physicalHeight <= 15_000_000);
      let top = 0;
      for (let cycle = 0; cycle < 25; cycle++) {
        for (let i = 0; i < 400; i++) {
          top = s.wheel({ deltaY: 19.75, deltaX: 0, deltaMode: 0, shiftKey: false }, top, 25 * zoom, view).top;
        }
        assert.ok(Math.abs(s.logical(top) - 7900) < 0.0001 || top === s.physicalMax);
        for (let i = 0; i < 401; i++) {
          top = s.wheel({ deltaY: -19.75, deltaX: 0, deltaMode: 0, shiftKey: false }, top, 25 * zoom, view).top;
        }
        assert.equal(top, 0, `rows=${count} zoom=${zoom} cycle=${cycle}`);
        assert.equal(s.logical(top), 0);
      }
    }
  }
});

test('native scrollbar endpoints cover the complete source and logical positions round trip', () => {
  const s = new VirtualScroll();
  s.configure(25_000_000_027, 630);
  for (const value of [0, 0.01, 25, 50_000, 100_000_000, s.logicalMax]) {
    assert.ok(Math.abs(s.logical(s.physical(value)) - value) < 0.00001);
  }
  assert.equal(s.logical(s.physicalMax), s.logicalMax);
  assert.equal(s.physical(-100), 0);
  assert.equal(s.physical(Infinity), s.physicalMax);
  assert.equal(s.logical(-1), 0);
});

test('fractional touchpad input accumulates despite native subpixel quantization', () => {
  const s = new VirtualScroll();
  s.configure(1_250_000_027, 700);
  let top = 0;
  const step = (delta: number): void => {
    top = Math.round(s.wheel({ deltaY: delta, deltaX: 0, deltaMode: 0, shiftKey: false }, top, 25, 673).top * 64) / 64;
  };
  for (let i = 0; i < 10_000; i++) step(0.1);
  assert.ok(
    Math.abs(s.logical(top) - 1000) < 2,
    'tiny deltas disappeared at the physical scrollbar',
  );
  for (let i = 0; i < 10_001; i++) step(-0.1);
  assert.equal(top, 0);
  top = s.physical(10_000_000);
  step(-25);
  assert.ok(
    Math.abs(s.logical(top) - 9_999_975) < 2,
    'native thumb drag reused the old wheel origin',
  );
});

test('line/page wheels retain logical pixel distance and horizontal wheel does not jump rows', () => {
  const s = new VirtualScroll();
  s.configure(73_000_000, 700);
  const top = s.physical(10_000);
  assert.ok(Math.abs(s.logical(s.wheel({ deltaY: -3, deltaX: 0, deltaMode: 1, shiftKey: false }, top, 25, 673).top) - 9925) < 1e-8);
  assert.ok(Math.abs(s.logical(s.wheel({ deltaY: 1, deltaX: 0, deltaMode: 2, shiftKey: false }, top, 25, 673).top) - 10673) < 1e-8);
  const horizontal = s.wheel({ deltaY: 80, deltaX: 0, deltaMode: 0, shiftKey: true }, top, 25, 673);
  assert.equal(horizontal.top, top);
  assert.equal(horizontal.left, 80);
});

test('rowPosition keeps deep cached rows distinct without overlap at every zoom', () => {
  for (const filtered of [false, true]) {
    for (const zoom of [0.7, 1, 1.25]) {
      const s = new VirtualScroll();
      const height = 33 * zoom;
      const offset = filtered ? height * 3 : 0;
      s.configure(2_907_951 * height + 27 * zoom + offset, 763);
      const rowTop = (visual: number): number => (visual - 1) * height;
      for (let i = 0; i < 120; i++) {
        const physicalScroll = s.physical((2_907_900 + (i % 2 ? 0 : 1)) * height + (i % 7) * 0.125);
        let previous: number | undefined;
        for (let visual = 2_907_870; visual < 2_907_950; visual++) {
          const pos = s.rowPosition(rowTop(visual) + offset, physicalScroll);
          if (previous !== undefined) {
            assert.ok(
              Math.abs(pos - previous - height) < 1e-6,
              'adjacent rows overlapped or left a gap',
            );
          }
          previous = pos;
        }
      }
    }
  }
});

test('actual cached-row scroll follower keeps row screen positions correct without repaint or fetch', () => {
  const dom = installDom({ fromIndexHtml: true });
  try {
    const s = new VirtualScroll();
    s.configure(73_000_000, 700);
    const grid = document.getElementById('grid');
    assert.ok(grid);
    grid.scrollTop = 0;
    const deps = makeDefaultViewportPipelineDeps({ virtualScroll: s });
    deps.S.renderedScrollTop = 0;
    for (let i = 1; i <= 80; i++) {
      const row = document.createElement('div') as GridRowElement;
      row.style.top = `${(i - 1) * 25}px`;
      deps.S.renderedRows.set(i, row);
    }
    const pipeline = new ViewportPipeline(deps);
    for (const logical of [300, 350, 275, 5000, 10_000_000, 50, 0, 250, 0]) {
      grid.scrollTop = s.physical(logical);
      pipeline.followGridScroll();
      for (const [visual, row] of deps.S.renderedRows) {
        assert.ok(
          Math.abs(parseFloat(row.style.top) - grid.scrollTop - ((visual - 1) * 25 - logical)) <
            0.00001,
        );
      }
    }
    const firstRow = deps.S.renderedRows.get(1);
    assert.ok(firstRow);
    assert.ok(Math.abs(parseFloat(firstRow.style.top)) < 0.00001);
  } finally {
    dom.restore();
  }
});

test('deep cached rows retain distinct coordinates despite lossy CSSOM serialization', () => {
  const dom = installDom({ fromIndexHtml: true });
  try {
    const grid = document.getElementById('grid');
    assert.ok(grid);
    for (const filtered of [false, true]) {
      for (const zoom of [0.7, 1, 1.25]) {
        const s = new VirtualScroll();
        const height = 33 * zoom;
        const offset = filtered ? height * 3 : 0;
        s.configure(2_907_951 * height + 27 * zoom + offset, 763);
        grid.scrollTop = s.physical(2_907_900 * height);
        const actuals = new Map<number, number>();
        let sourceGeometryReads = 0;
        let frozenPanesRef: ReturnType<typeof makeTestFrozenPanes> | undefined;
        const deps = makeDefaultViewportPipelineDeps({
          virtualScroll: s,
          getFrozenPanes: () => {
            if (!frozenPanesRef) {
              frozenPanesRef = makeTestFrozenPanes(deps.S);
              frozenPanesRef.effective = () => ({ rows: 3, cols: 0 });
            }
            return frozenPanesRef;
          },
        });
        deps.S.filtered = filtered;
        deps.S.zoom = zoom * 100;
        deps.geometry.rows.setBase(33);
        deps.S.renderedScrollTop = grid.scrollTop;
        const pipeline = new ViewportPipeline(deps);
        pipeline.rowTop = (visual: number): number => (visual - 1) * height;
        pipeline.sourceRowTop = (row: number): number => {
          sourceGeometryReads++;
          assert.equal(row, 4);
          return offset;
        };
        for (let visual = 2_907_870; visual < 2_907_950; visual++) {
          let actual = s.rowPosition((visual - 1) * height + offset, grid.scrollTop);
          actuals.set(visual, actual);
          const row = document.createElement('div') as GridRowElement;
          const lossyStyle = {
            get top(): string {
              return `${Number(actual.toPrecision(6))}px`;
            },
            set top(value: string) {
              actual = parseFloat(value);
              actuals.set(visual, actual);
            },
          };
          Object.defineProperty(row, 'style', { configurable: true, value: lossyStyle });
          deps.S.renderedRows.set(visual, row);
        }
        for (let i = 0; i < 120; i++) {
          grid.scrollTop = s.physical((2_907_900 + (i % 2 ? 0 : 1)) * height + (i % 7) * 0.125);
          pipeline.followGridScroll();
          let previous: number | undefined;
          for (const visual of deps.S.renderedRows.keys()) {
            const actual = actuals.get(visual);
            assert.ok(actual !== undefined);
            const expected = s.rowPosition((visual - 1) * height + offset, grid.scrollTop);
            assert.ok(
              Math.abs(actual - expected) < 1e-6,
              `filtered=${filtered} zoom=${zoom} row=${visual} step=${i}: ${actual} vs ${expected}`,
            );
            if (previous !== undefined) {
              assert.ok(
                Math.abs(actual - previous - height) < 1e-6,
                'adjacent rows overlapped or left a gap',
              );
            }
            previous = actual;
          }
        }
        assert.equal(
          sourceGeometryReads,
          filtered ? 120 : 0,
          'frozen prefix geometry is read once per scroll, not per row',
        );
      }
    }
  } finally {
    dom.restore();
  }
});

test('native anchoring is disabled and overscan cannot extend the authoritative scroll range', () => {
  const css = fs.readFileSync(path.resolve(HERE, '../../web/style.css'), 'utf8');
  assert.match(css, /\.grid-scroll\s*\{[^}]*overflow-anchor:\s*none/);
  assert.match(css, /\.rows-layer\s*\{[^}]*bottom:\s*0[^}]*overflow:\s*clip/);
});
