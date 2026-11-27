import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { AnalysisCell } from '../contracts/analysis.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import {
  drawChart,
  drawSparkline,
  resetChartListeners,
  resizeCharts,
  type ChartResultLike,
} from './charts.ts';
import {
  DEFAULT_ANALYSIS_NUMBER_OPTIONS,
  setAnalysisNumberOptions,
} from './numbers.ts';

interface PaintedRect {
  readonly color: string;
  readonly x: number;
  readonly y: number;
  readonly w: number;
  readonly h: number;
}

interface PaintedText {
  readonly value: string;
  readonly color: string;
}

interface PaintedPath {
  readonly color: string;
  readonly points: readonly (readonly ['move' | 'line', number, number])[];
}

interface MockResizeObserverInstance {
  readonly callback: ResizeObserverCallback;
  readonly observed: Element[];
  observe(panel: Element): void;
  unobserve(): void;
  disconnect(): void;
}

function makeCell(exact: string, display: string = exact): AnalysisCell {
  return { exact, display, count: exact ? 1 : 0 };
}

function resultOf(values: readonly string[]): ChartResultLike {
  return {
    columnKeys: ['金额'],
    rows: values.map((exact, i) => ({
      keys: [String(i)],
      values: [{ exact, display: '0', count: exact ? 1 : 0 }],
    })),
  };
}

function setup({ observe = false }: { readonly observe?: boolean } = {}) {
  const dom = installDom();
  resetChartListeners();
  setAnalysisNumberOptions(DEFAULT_ANALYSIS_NUMBER_OPTIONS);

  let themed = false;
  const paint: string[] = [];
  const text: PaintedText[] = [];
  const lines: string[] = [];
  const variables: Record<string, string> = {};
  const rects: PaintedRect[] = [];
  const paths: PaintedPath[] = [];
  const arcs: (readonly number[])[] = [];
  let current: (readonly ['move' | 'line', number, number])[] = [];

  const context = {
    fillStyle: '',
    strokeStyle: '',
    lineWidth: 1,
    font: '11px "Segoe UI"',
    textAlign: 'left' as CanvasTextAlign,
    setTransform() {},
    clearRect() {},
    save() {},
    restore() {},
    translate() {},
    fillRect(x: number, y: number, w: number, h: number) {
      paint.push(this.fillStyle);
      rects.push({ color: this.fillStyle, x, y, w, h });
    },
    fillText(value: string) {
      text.push({ value, color: this.fillStyle });
    },
    measureText(value: string) {
      return { width: value.length * 7 };
    },
    beginPath() {
      current = [];
    },
    moveTo(x: number, y: number) {
      current.push(['move', x, y]);
    },
    lineTo(x: number, y: number) {
      current.push(['line', x, y]);
    },
    stroke() {
      lines.push(this.strokeStyle);
      paths.push({ color: this.strokeStyle, points: current });
    },
    arc(x: number, y: number, r: number, start: number, end: number) {
      arcs.push([x, y, r, start, end]);
    },
    closePath() {},
    fill() {},
  };

  const root = document.createElement('div');
  root.id = 'analysisRoot';
  const panel = document.createElement('div');
  panel.className = 'analysis-panel';
  const wrap = document.createElement('div');
  const tip = document.createElement('div');
  tip.className = 'analysis-chart-tooltip';
  tip.hidden = true;
  tip.getBoundingClientRect = () =>
    ({
      x: 0,
      y: 0,
      left: 0,
      top: 0,
      right: 220,
      bottom: 40,
      width: 220,
      height: 40,
      toJSON() {
        return this;
      },
    }) satisfies DOMRect;
  const canvas = document.createElement('canvas');
  wrap.append(canvas, tip);
  panel.append(wrap);
  root.append(panel);
  document.body.append(root);

  const defaultRect = (): DOMRect => ({
    x: 0,
    y: 0,
    left: 0,
    top: 0,
    right: 640,
    bottom: 300,
    width: 640,
    height: 300,
    toJSON() {
      return this;
    },
  });
  wrap.getBoundingClientRect = defaultRect;
  canvas.getBoundingClientRect = defaultRect;
  let visibleRects = true;
  Object.defineProperty(canvas, 'getClientRects', {
    value: () => (visibleRects ? [defaultRect()] : []),
    configurable: true,
    writable: true,
  });
  Object.defineProperty(canvas, 'getContext', {
    value: () => context,
    configurable: true,
    writable: true,
  });

  const frames: FrameRequestCallback[] = [];
  const observers: MockResizeObserverInstance[] = [];

  Object.defineProperty(globalThis, 'requestAnimationFrame', {
    value: (fn: FrameRequestCallback): number => {
      frames.push(fn);
      return frames.length;
    },
    configurable: true,
    writable: true,
  });
  Object.defineProperty(window, 'requestAnimationFrame', {
    value: globalThis.requestAnimationFrame,
    configurable: true,
    writable: true,
  });
  Object.defineProperty(globalThis, 'getComputedStyle', {
    value: () => ({
      getPropertyValue: (name: string) => variables[name] ?? '',
    }),
    configurable: true,
    writable: true,
  });
  Object.defineProperty(window, 'getComputedStyle', {
    value: globalThis.getComputedStyle,
    configurable: true,
    writable: true,
  });

  if (observe) {
    class MockObserver implements MockResizeObserverInstance {
      readonly callback: ResizeObserverCallback;
      readonly observed: Element[] = [];
      constructor(cb: ResizeObserverCallback) {
        this.callback = cb;
        observers.push(this);
      }
      observe(el: Element): void {
        this.observed.push(el);
      }
      unobserve(): void {}
      disconnect(): void {}
    }
    Object.defineProperty(globalThis, 'ResizeObserver', {
      value: MockObserver,
      configurable: true,
      writable: true,
    });
    Object.defineProperty(window, 'ResizeObserver', {
      value: MockObserver,
      configurable: true,
      writable: true,
    });
  } else {
    Object.defineProperty(globalThis, 'ResizeObserver', {
      value: undefined,
      configurable: true,
      writable: true,
    });
    Object.defineProperty(window, 'ResizeObserver', {
      value: undefined,
      configurable: true,
      writable: true,
    });
  }

  const origHasAttribute = document.documentElement.hasAttribute.bind(document.documentElement);
  document.documentElement.hasAttribute = (name: string): boolean => {
    if (name === 'data-reading-preset') return themed;
    return origHasAttribute(name);
  };

  const result: ChartResultLike = {
    columnKeys: ['金额'],
    rows: [{ keys: ['样本'], values: [makeCell('10')] }],
  };
  const original = JSON.stringify(result);

  return {
    canvas,
    panel,
    result,
    original,
    paint,
    text,
    lines,
    variables,
    rects,
    paths,
    arcs,
    tip,
    frames,
    observers,
    setVisibleRects: (v: boolean) => {
      visibleRects = v;
    },
    changeTheme: (value: boolean) => {
      themed = value;
      document.dispatchEvent(new Event('excellens:readingchange'));
    },
    changeNumbers: (options: typeof DEFAULT_ANALYSIS_NUMBER_OPTIONS) => {
      setAnalysisNumberOptions(options);
      document.dispatchEvent(new Event('excellens:analysisnumberchange'));
    },
    restore: () => {
      resetChartListeners();
      setAnalysisNumberOptions(DEFAULT_ANALYSIS_NUMBER_OPTIONS);
      dom.restore();
    },
  };
}

test('snapshot mini trends preserve gaps and use a shared normalized range without overflowing the canvas', () => {
  const h = setup();
  try {
    const domain = [makeCell('-1e308'), makeCell('1e308')];
    drawSparkline(h.canvas, [makeCell('-1e308'), null, makeCell('1e308')], domain);
    const path = h.paths.at(-1);
    assert.ok(path);
    assert.equal(path.points.length, 2);
    assert.ok(path.points.every((p) => p[0] === 'move'));
    assert.ok(path.points.every((p) => Number.isFinite(p[1]) && Number.isFinite(p[2])));
    assert.equal(h.arcs.length, 2, 'isolated finite points must remain visible across gaps');

    drawSparkline(h.canvas, [makeCell('1/3')], [makeCell('1/3'), makeCell('2/3')]);
    const arcsAfter: readonly (readonly number[])[] = h.arcs;
    assert.ok(arcsAfter.length > 0);
    assert.equal(h.text.length, 0);
  } finally {
    h.restore();
  }
});

test('snapshot mini trends show a range failure for underflow or overflow instead of fabricating zero', () => {
  const h = setup();
  try {
    drawSparkline(h.canvas, [{ exact: '1e999', display: '99', count: 1 }]);
    assert.ok(h.text.some((t) => t.value.includes('绘图范围')));
    assert.equal(h.paths.length, 0);
  } finally {
    h.restore();
  }
});

test('panel and window resizing coalesce one repaint, adapt backing pixels and preserve exact results and drill keys', () => {
  const h = setup({ observe: true });
  try {
    const picked: (readonly string[])[] = [];
    drawChart(h.canvas, h.result, 'bar', (keys) => picked.push(keys));
    assert.equal(h.observers.length, 1);
    assert.deepEqual(h.observers[0]?.observed, [h.panel]);

    h.paint.length = 0;
    h.rects.length = 0;
    Object.defineProperty(window, 'devicePixelRatio', { value: 2, configurable: true, writable: true });
    h.canvas.getBoundingClientRect = () => ({
      x: 0,
      y: 0,
      left: 0,
      top: 0,
      right: 320,
      bottom: 200,
      width: 320,
      height: 200,
      toJSON() {
        return this;
      },
    });
    const observer = h.observers[0];
    assert.ok(observer);
    for (let i = 0; i < 20; i++) {
      resizeCharts();
      observer.callback([], observer);
    }
    assert.equal(h.frames.length, 1);
    assert.equal(h.paint.length, 0);
    h.frames.shift()?.(0);
    assert.equal(h.canvas.width, 640);
    assert.equal(h.canvas.height, 400);
    assert.equal(h.paint.length, 2);

    const bar = h.rects.find((r) => r.color === '#287b78');
    assert.ok(bar);
    const clickEv = new Event('click') as PointerEvent;
    Object.defineProperty(clickEv, 'clientX', { value: bar.x + bar.w / 2 });
    Object.defineProperty(clickEv, 'clientY', { value: bar.y + bar.h / 2 });
    h.canvas.onclick?.(clickEv);
    assert.deepEqual(picked, [['样本']]);
    assert.equal(JSON.stringify(h.result), h.original);

    h.setVisibleRects(false);
    h.paint.length = 0;
    observer.callback([], observer);
    h.frames.shift()?.(0);
    assert.equal(h.paint.length, 0);
    assert.equal(h.canvas.width, 640);
  } finally {
    h.restore();
  }
});

test('comparison charts plot exact derived ratios with percentage axes and never turn undefined ratios into zero', () => {
  const h = setup();
  try {
    const selected: (readonly string[])[] = [];
    const result: ChartResultLike = {
      columnKeys: ['金额'],
      rows: [
        {
          keys: ['A'],
          values: [
            {
              exact: '9007199254740993.31',
              display: '9007199254740993.31',
              count: 1,
              comparison: {
                mode: 'percent_grand',
                exact: '1/4',
                display: '25.00%',
                percent: true,
                numerator: '1',
                denominator: '4',
              },
            },
          ],
        },
        {
          keys: ['B'],
          values: [
            {
              exact: '0',
              display: '0.00',
              count: 1,
              comparison: {
                mode: 'percent_grand',
                exact: '',
                display: '分母为0',
                status: 'zero_denominator',
                percent: true,
              },
            },
          ],
        },
        {
          keys: ['C'],
          values: [
            {
              exact: '9',
              display: '9.00',
              count: 1,
              comparison: {
                mode: 'percent_grand',
                exact: '3/4',
                display: '75.00%',
                percent: true,
                numerator: '3',
                denominator: '4',
              },
            },
          ],
        },
      ],
    };
    const original = JSON.stringify(result);
    drawChart(h.canvas, result, 'line', (keys) => selected.push(keys));
    assert.equal(h.arcs.length, 2);
    assert.ok(h.text.some((item) => item.value.endsWith("%")));
    assert.ok(!h.text.some((item) => item.value.includes('9.01e+15')));

    const firstArc = h.arcs[0];
    assert.ok(firstArc);
    const ev = new Event('mousemove') as PointerEvent;
    Object.defineProperty(ev, 'clientX', { value: firstArc[0] });
    Object.defineProperty(ev, 'clientY', { value: firstArc[1] });
    h.canvas.onmousemove?.(ev);
    assert.match(h.tip.textContent, /25.00%/);
    assert.match(h.tip.textContent, /精确比值：1\/4/);
    assert.match(h.tip.textContent, /本分组原始聚合：9007199254740993.31/);
    h.canvas.onclick?.(ev);
    assert.equal(selected[0]?.[0], 'A');
    assert.equal(JSON.stringify(result), original);
  } finally {
    h.restore();
  }
});

test('existing charts repaint paper and labels on preset changes and restore the original palette without changing results', () => {
  const h = setup();
  try {
    drawChart(h.canvas, h.result);
    assert.equal(h.paint[0], '#fff');
    assert.ok(h.text.some((t) => t.color === '#92a0ac'));
    assert.ok(h.lines.includes('#e8eef1'));

    Object.assign(h.variables, {
      '--surface': '#2e333a',
      '--ink': '#dce2e8',
      '--muted': '#abb5bf',
      '--line-soft': '#39424c',
    });
    h.paint.length = 0;
    h.text.length = 0;
    h.lines.length = 0;
    h.changeTheme(true);
    assert.equal(h.paint[0], '#2e333a');
    assert.ok(h.text.every((t) => t.color === '#abb5bf'));
    assert.ok(h.lines.every((c) => c === '#39424c'));
    assert.ok(h.paint.includes('#287b78'), 'series colors must remain distinct from page chrome');

    h.paint.length = 0;
    h.text.length = 0;
    h.lines.length = 0;
    for (const k of Object.keys(h.variables)) Reflect.deleteProperty(h.variables, k);
    h.changeTheme(false);
    assert.equal(h.paint[0], '#fff');
    assert.ok(h.text.some((t) => t.color === '#92a0ac'));
    assert.equal(JSON.stringify(h.result), h.original);
  } finally {
    h.restore();
  }
});

test('a hidden chart retains its drawable data but theme changes do not resize it', () => {
  const h = setup();
  try {
    drawChart(h.canvas, h.result);
    h.setVisibleRects(false);
    h.paint.length = 0;
    h.changeTheme(true);
    assert.deepEqual(h.paint, []);
    assert.equal(h.canvas.width, 640);
    assert.equal(h.canvas.height, 300);
  } finally {
    h.restore();
  }
});

test('fractions draw from exact values; missing data leaves holes in bars, points and line segments', () => {
  const h = setup();
  try {
    const result = resultOf(['1/3', '', '2/3']);
    drawChart(h.canvas, result, 'bar');
    const bars = h.rects.filter((x) => x.color === '#287b78');
    assert.equal(bars.length, 2);
    assert.ok(bars[0] && bars[1] && Math.abs(bars[1].h / bars[0].h - 2) < 1e-12);

    drawChart(h.canvas, result, 'line');
    assert.equal(h.arcs.length, 2);
    const strokes = h.paths.filter((x) => x.color === '#287b78');
    assert.equal(strokes.length, 2);
    assert.ok(strokes.every((p) => p.points.length === 1 && p.points[0]?.[0] === 'move'));

    const pick = drawChart(h.canvas, result, 'scatter');
    assert.ok(pick);
    const ev = new Event('mousemove') as MouseEvent;
    Object.defineProperty(ev, 'clientX', { value: 340 });
    Object.defineProperty(ev, 'clientY', { value: 150 });
    assert.equal(pick(ev), null);
    assert.equal(result.rows[0]?.values[0]?.exact, '1/3');
  } finally {
    h.restore();
  }
});

test('tiny negative values keep their scale; long rational components are approximated without Infinity divided by Infinity', () => {
  const h = setup();
  try {
    drawChart(h.canvas, resultOf(['-0.0000000000001']), 'bar');
    const bar = h.rects.find((x) => x.color === '#287b78');
    assert.ok(bar && bar.h > 200);
    assert.ok(h.text.some((t) => t.value.includes('e-13')));

    h.rects.length = 0;
    drawChart(h.canvas, resultOf([`1${'0'.repeat(400)}/3${'0'.repeat(400)}`]), 'bar');
    assert.ok(h.rects.some((x) => x.color === '#287b78' && x.h > 200));
  } finally {
    h.restore();
  }
});

test('unrepresentable values refuse drawing and remove old interactions; missing heatmap cells remain blank', () => {
  const h = setup();
  try {
    drawChart(h.canvas, h.result);
    h.rects.length = 0;
    drawChart(h.canvas, resultOf(['1e400']));
    assert.equal(h.canvas.onclick, null);
    assert.ok(h.text.some((t) => t.value.includes('超出图表范围')));
    assert.equal(h.rects.length, 1);

    h.rects.length = 0;
    drawChart(
      h.canvas,
      {
        columnKeys: ['a', 'b'],
        rows: [{ keys: ['一'], values: [makeCell(''), makeCell('0')] }],
      },
      'heatmap',
    );
    assert.equal(h.rects.length, 2, 'only background and the real zero are painted');
  } finally {
    h.restore();
  }
});

test('limited donut totals and chart series show their actual plotted scope', () => {
  const h = setup();
  try {
    const result: ChartResultLike = {
      ...resultOf(Array<string>(25).fill('1')),
      columnKeys: ['金额', '数量'],
    };
    drawChart(h.canvas, result, 'donut');
    assert.ok(h.text.some((t) => t.value === '已绘制合计'));
    assert.ok(h.text.some((t) => t.value.includes('16 个正值分类') && t.value.includes('第1系列')));
    assert.ok(!h.text.some((t) => t.value === '汇总合计'));

    h.text.length = 0;
    drawChart(h.canvas, result, 'horizontal');
    assert.ok(h.text.some((t) => t.value.includes('24/25') && t.value.includes('1/2')));
  } finally {
    h.restore();
  }
});

test('empty groups, literal labels and totals stay distinct; exact tooltips repaint preferences without changing drill keys', () => {
  const h = setup();
  try {
    const value = makeCell('9007199254741015.11');
    const result: ChartResultLike = {
      columnField: 'C2',
      columnKeys: ['', '（空白）'],
      rows: [[''], ['（空白）'], []].map((keys) => ({
        keys,
        values: [value, makeCell('1', '1.00')],
      })),
    };
    const before = JSON.stringify(result);
    const picked: { readonly keys: readonly string[]; readonly column: string }[] = [];
    drawChart(h.canvas, result, 'bar', (keys, column) => picked.push({ keys, column }));
    for (const label of ['空白值', '（空白）', '总计', '空白列']) {
      assert.ok(
        h.text.some((t) => t.value === label),
        label,
      );
    }

    const bar = h.rects.find((rect) => rect.color === '#287b78');
    assert.ok(bar);
    const ev = new Event('mousemove') as PointerEvent;
    Object.defineProperty(ev, 'clientX', { value: bar.x + bar.w / 2 });
    Object.defineProperty(ev, 'clientY', { value: bar.y + bar.h / 2 });
    h.canvas.onmousemove?.(ev);
    assert.ok(h.tip.textContent.startsWith('空白值 · 空白列：9.01e+15'));
    assert.ok(h.tip.textContent.includes(value.exact));
    h.canvas.onclick?.(ev);
    assert.deepEqual(picked, [{ keys: [''], column: '' }]);

    h.changeNumbers({ ...DEFAULT_ANALYSIS_NUMBER_OPTIONS, scientific: false });
    h.canvas.onmousemove?.(ev);
    assert.ok(h.tip.textContent.includes('9,007,199,254,741,015.11'));
    assert.equal(JSON.stringify(result), before);
    h.canvas.onclick?.(ev);
    assert.deepEqual(picked[1], { keys: [''], column: '' });
  } finally {
    h.restore();
  }
});
