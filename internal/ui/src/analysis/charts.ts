/**
 * Canvas/SVG chart renderer for pivot analysis, dashboards, and snapshot trends.
 * Axes use drawing approximations; data labels and tooltips use exact cells.
 */
import type {
  AnalysisCell,
  AnalysisDisplayOptions,
  AnalysisResult,
  AnalysisResultRow,
  ComparedValue,
} from '../contracts/analysis.ts';
import { comparisonTitle } from './comparisons.ts';
import { formatAnalysisNumber, getAnalysisNumberOptions } from './numbers.ts';

export type ChartMode =
  | 'bar'
  | 'horizontal'
  | 'line'
  | 'area'
  | 'donut'
  | 'pie'
  | 'scatter'
  | 'stacked'
  | 'heatmap';

export const CHART_MODES: readonly ChartMode[] = Object.freeze([
  'bar',
  'horizontal',
  'line',
  'area',
  'donut',
  'pie',
  'scatter',
  'stacked',
  'heatmap',
]);

export function isChartMode(value: string): value is ChartMode {
  return (CHART_MODES as readonly string[]).includes(value);
}

export interface ChartTheme {
  readonly themed: boolean;
  readonly paper: string;
  readonly ink: string;
  readonly muted: string;
  readonly grid: string;
  readonly error: string;
}

export interface ChartTextMetrics {
  readonly width: number;
}

export interface ChartDrawContext {
  fillStyle: string | CanvasGradient | CanvasPattern;
  strokeStyle: string | CanvasGradient | CanvasPattern;
  lineWidth: number;
  font: string;
  textAlign: CanvasTextAlign;
  beginPath(): void;
  moveTo(x: number, y: number): void;
  lineTo(x: number, y: number): void;
  closePath(): void;
  arc(
    x: number,
    y: number,
    radius: number,
    startAngle: number,
    endAngle: number,
    counterclockwise?: boolean,
  ): void;
  fill(): void;
  stroke(): void;
  fillRect(x: number, y: number, w: number, h: number): void;
  fillText(text: string, x: number, y: number, maxWidth?: number): void;
  measureText(text: string): ChartTextMetrics;
  save(): void;
  restore(): void;
  translate(x: number, y: number): void;
}

export interface ChartResultLike {
  readonly columnField?: string;
  readonly columnKeys: readonly string[];
  readonly rows: readonly Pick<AnalysisResultRow, 'keys' | 'values'>[];
  readonly truncated?: boolean;
  readonly tree?: AnalysisResult['tree'];
}

export interface ChartPieSliceDatum {
  readonly v: number;
  readonly i: number;
}

export interface ChartPlan {
  readonly allRows: readonly Pick<AnalysisResultRow, 'keys' | 'values'>[];
  readonly allSeries: readonly string[];
  readonly points: readonly Pick<AnalysisResultRow, 'keys' | 'values'>[];
  readonly series: readonly string[];
  readonly values: readonly (readonly (number | null)[])[];
  readonly percent: boolean;
  readonly compared: boolean;
  readonly positive: readonly ChartPieSliceDatum[];
  readonly pie: readonly ChartPieSliceDatum[];
  readonly mode: ChartMode;
}

export interface ChartDescription {
  readonly mode: ChartMode;
  readonly totalRows: number;
  readonly totalSeries: number;
  readonly consideredRows: number;
  readonly rowIndices: readonly number[];
  readonly seriesIndices: readonly number[];
  readonly drawableCells: number;
  readonly missingCells: number;
  readonly unrepresentable: boolean;
  readonly positiveOnly: boolean;
  readonly negativeExcluded: boolean;
  readonly resultTruncated: boolean;
  readonly firstTreeLevel: boolean;
  readonly percent: boolean;
}

export type ChartHitBox =
  | {
      readonly pie?: false;
      readonly x: number;
      readonly y: number;
      readonly w: number;
      readonly h: number;
      readonly row: number;
      readonly col: number;
    }
  | {
      readonly pie: true;
      readonly cx: number;
      readonly cy: number;
      readonly r: number;
      readonly inner: number;
      readonly from: number;
      readonly to: number;
      readonly row: number;
      readonly col: number;
    };

export type ChartPaintResult =
  | {
      readonly status: 'empty' | 'range';
      readonly hit: readonly ChartHitBox[];
      readonly points: readonly Pick<AnalysisResultRow, 'keys' | 'values'>[];
      readonly series: readonly string[];
    }
  | {
      readonly status: 'ok';
      readonly hit: readonly ChartHitBox[];
      readonly points: readonly Pick<AnalysisResultRow, 'keys' | 'values'>[];
      readonly series: readonly string[];
      readonly label: (row: Pick<AnalysisResultRow, 'keys'>) => string;
      readonly seriesLabel: (key: string) => string;
      readonly plan: ChartPlan;
    };

export type ChartPickHandler = (keys: readonly string[], colKey: string) => void;

export interface ChartRenderSnapshot {
  readonly result: ChartResultLike | null;
  readonly mode: ChartMode;
  readonly onPick?: ChartPickHandler;
  readonly theme: ChartTheme;
  readonly numberSettings: AnalysisDisplayOptions;
  hoverFrame: number;
}

const PALETTE: readonly string[] = [
  '#287b78',
  '#709cbe',
  '#d19a59',
  '#8b82b7',
  '#66a58e',
  '#c67e7f',
  '#d2b66e',
  '#777fa5',
  '#7bb0bd',
  '#ac9c87',
];

const rendered = new WeakMap<HTMLCanvasElement, ChartRenderSnapshot>();
let listenersBound = false;
let resizeFrame = 0;

/**
 * Approximate ONLY for drawing. Fractions must not fall back to a rounded
 * display string, and missing/overflowed values must never become zero.
 */
export function chartNumber(
  cell?: Partial<Pick<AnalysisCell, 'display' | 'exact' | 'comparison'>> | Partial<ComparedValue> | null,
): number | null {
  if (!cell) return null;
  const target: Partial<Pick<AnalysisCell, 'display' | 'exact'>> | Partial<ComparedValue> =
    'comparison' in cell && cell.comparison ? cell.comparison : cell;
  if ('status' in target && target.status) return null;
  const raw = (target.exact ?? target.display ?? '').trim();
  if (!raw) return null;
  if (raw.includes('/')) {
    const parts = raw.split('/');
    const numPart = parts[0];
    const denPart = parts[1];
    if (
      parts.length !== 2 ||
      numPart === undefined ||
      denPart === undefined ||
      !/^[+-]?\d+$/.test(numPart) ||
      !/^[+-]?\d+$/.test(denPart)
    ) {
      return Number.NaN;
    }
    const component = (s: string): { readonly mantissa: number; readonly exponent: number } => {
      const sign = s.startsWith('-') ? -1 : 1;
      const digits = s.replace(/^[+-]/, '').replace(/^0+/, '');
      return {
        mantissa: (sign * Number(digits.slice(0, 16) || '0')) / 10 ** (Math.min(digits.length, 16) - 1),
        exponent: Math.max(0, digits.length - 1),
      };
    };
    const n = component(numPart);
    const d = component(denPart);
    if (!d.mantissa) return Number.NaN;
    if (!n.mantissa) return 0;
    const value = Number(`${String(n.mantissa / d.mantissa)}e${n.exponent - d.exponent}`);
    return Number.isFinite(value) && value !== 0 ? value : Number.NaN;
  }
  if (!/^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?$/.test(raw)) return Number.NaN;
  const value = Number(raw);
  const mantissaPart = raw.split(/[eE]/)[0] ?? '';
  const nonzero = /[1-9]/.test(mantissaPart);
  return Number.isFinite(value) && (value !== 0 || !nonzero) ? value : Number.NaN;
}

export function prepareChart(
  result: ChartResultLike | null | undefined,
  mode = 'bar',
): ChartPlan {
  if (!isChartMode(mode)) throw new Error('图表类型无效');
  const allRows = result?.rows ?? [];
  const allSeries = result?.columnKeys ?? [];
  const single = mode === 'horizontal' || mode === 'pie' || mode === 'donut';
  const limit = mode === 'horizontal' ? 24 : mode === 'heatmap' ? 28 : 60;
  const points = allRows.slice(0, limit);
  const series = allSeries.slice(0, single ? 1 : 12);
  const percent = points.some((row) =>
    row.values.some((cell) => cell.comparison?.percent === true),
  );
  const compared = points.some((row) => row.values.some((cell) => Boolean(cell.comparison)));
  const values: (number | null)[][] = points.map((row) =>
    series.map((_, j) => {
      const cell = row.values[j];
      const value = chartNumber(cell);
      return value === null ? null : cell?.comparison?.percent ? value * 100 : value;
    }),
  );
  const positive: ChartPieSliceDatum[] = [];
  for (let i = 0; i < points.length; i++) {
    const v = values[i]?.[0];
    if (v !== null && v !== undefined && v > 0) {
      positive.push({ v, i });
    }
  }
  const pie = positive.slice(0, 16);
  return { allRows, allSeries, points, series, values, percent, compared, positive, pie, mode };
}

export function describeChart(
  result: ChartResultLike | null | undefined,
  mode = 'bar',
): ChartDescription {
  const p = prepareChart(result, mode);
  const isPie = p.mode === 'pie' || p.mode === 'donut';
  const rowIndices = isPie ? p.pie.map((x) => x.i) : p.points.map((_, i) => i);
  const seriesIndices = p.series.map((_, i) => i);
  const drawableCells = rowIndices.reduce(
    (n, i) => n + (p.values[i]?.filter((v): v is number => v !== null && Number.isFinite(v)).length ?? 0),
    0,
  );
  const missingCells = rowIndices.reduce(
    (n, i) => n + (p.values[i]?.filter((v) => v === null).length ?? 0),
    0,
  );
  return {
    mode: p.mode,
    totalRows: p.allRows.length,
    totalSeries: p.allSeries.length,
    consideredRows: p.points.length,
    rowIndices,
    seriesIndices,
    drawableCells,
    missingCells,
    unrepresentable: p.values.some((row) => row.some((v) => v !== null && Number.isNaN(v))),
    positiveOnly: isPie,
    negativeExcluded:
      isPie &&
      p.values.some((row) => {
        const first = row[0];
        return first !== null && first !== undefined && first < 0;
      }),
    resultTruncated: Boolean(result?.truncated),
    firstTreeLevel: Boolean(result?.tree),
    percent: p.percent,
  };
}

export function chartThemeFor(canvas: Element): ChartTheme {
  const style = getComputedStyle(canvas);
  const get = (key: string, fallback: string): string => style.getPropertyValue(key).trim() || fallback;
  return {
    themed: document.documentElement.hasAttribute('data-reading-preset'),
    paper: get('--surface', '#fff'),
    ink: get('--ink', '#2c4852'),
    muted: get('--muted', '#768a97'),
    grid: get('--line-soft', '#e8eef1'),
    error: get('--danger', '#b16f72'),
  };
}

export function paintChart(
  ctx: ChartDrawContext,
  w: number,
  h: number,
  result: ChartResultLike | null | undefined,
  mode = 'bar',
  theme: Partial<ChartTheme> = {},
  numberSettings: AnalysisDisplayOptions = getAnalysisNumberOptions(),
): ChartPaintResult {
  const themed = theme.themed ?? false;
  const paper = theme.paper ?? '#fff';
  const ink = theme.ink ?? '#2c4852';
  const muted = theme.muted ?? '#768a97';
  const grid = theme.grid ?? '#e8eef1';
  const errorColor = theme.error ?? '#b16f72';

  const plan = prepareChart(result, mode);
  ctx.fillStyle = paper;
  ctx.fillRect(0, 0, w, h);

  const fmt = (v: number): string => {
    if (!Number.isFinite(v)) return '—';
    const n = Math.abs(v);
    if (n !== 0 && n < 0.01) return v.toExponential(2);
    return formatAnalysisNumber(
      {
        exact: String(v),
        display: v.toLocaleString('en-US', { useGrouping: false, maximumFractionDigits: 2 }),
      },
      numberSettings,
    );
  };

  const { allRows, allSeries, points, series, values, percent, compared } = plan;
  if (!points.length) return { status: 'empty', hit: [], points, series };

  const unitFmt = (v: number): string => fmt(v) + (percent ? '%' : '');
  const plot = { left: 62, right: w - 21, top: 22, bottom: h - 49 };
  const pw = Math.max(10, w - 84);
  const ph = Math.max(10, h - 71);
  const hit: ChartHitBox[] = [];

  const text = (
    x: number,
    y: number,
    str: string,
    color = '#768a97',
    align: CanvasTextAlign = 'center',
    font = '11px "Segoe UI"',
    maxWidth?: number,
  ): void => {
    ctx.font = font;
    ctx.textAlign = align;
    ctx.fillStyle = themed
      ? color === '#2c4852'
        ? ink
        : color === '#b16f72'
          ? errorColor
          : muted
      : color;
    if (maxWidth !== undefined && maxWidth > 0) ctx.fillText(str, x, y, maxWidth);
    else ctx.fillText(str, x, y);
  };

  const line = (a: number, b: number, c: number, d: number, color = '#e8eef1'): void => {
    ctx.beginPath();
    ctx.moveTo(a, b);
    ctx.lineTo(c, d);
    ctx.strokeStyle = themed ? grid : color;
    ctx.lineWidth = 1;
    ctx.stroke();
  };

  const vRange = (): { readonly low: number; readonly high: number } => {
    let low = 0;
    let high = 0;
    for (const row of values) {
      if (plan.mode === 'stacked' && series.length > 1) {
        let positive = 0;
        let negative = 0;
        for (const v of row) {
          if (v !== null && Number.isFinite(v)) {
            if (v >= 0) positive += v;
            else negative += v;
          }
        }
        low = Math.min(low, negative);
        high = Math.max(high, positive);
      } else {
        for (const v of row) {
          if (v !== null && Number.isFinite(v)) {
            low = Math.min(low, v);
            high = Math.max(high, v);
          }
        }
      }
    }
    if (high === low) high = low + 1;
    const gap = (high - low) * 0.08;
    // Preserve a true zero baseline whenever all values share a sign.
    // Otherwise positive-only charts appear to cross a negative y-axis.
    return { low: low === 0 ? 0 : low - gap, high: high === 0 ? 0 : high + gap };
  };

  const label = (row: Pick<AnalysisResultRow, 'keys'>): string =>
    row.keys.length ? row.keys.map((key) => (key === '' ? '空白值' : key)).join(' / ') : '总计';
  const seriesLabel = (key: string): string =>
    key === '' ? (result?.columnField || allSeries.length > 1 ? '空白列' : '汇总') : key;
  const interactive = (
    x: number,
    y: number,
    rwidth: number,
    rheight: number,
    rowIndex: number,
    colIndex: number,
  ): void => {
    hit.push({ x, y, w: rwidth, h: rheight, row: rowIndex, col: colIndex });
  };
  const color = (j: number): string => PALETTE[j % PALETTE.length] ?? '#287b78';

  const range = vRange();
  if (
    values.some((row) => row.some((v) => v !== null && Number.isNaN(v))) ||
    !Number.isFinite(range.high - range.low) ||
    range.high === range.low
  ) {
    text(w / 2, h / 2, '数值超出图表范围，请查看透视表', '#b16f72');
    return { status: 'range', hit: [], points, series };
  }

  let scope =
    points.length < allRows.length || series.length < allSeries.length
      ? `显示 ${points.length}/${allRows.length} 个分类 · ${series.length}/${allSeries.length} 个系列`
      : '';
  if (result?.truncated) scope += `${scope ? '；' : ''}分类已截取，完整合计见透视表`;

  if (plan.mode === 'donut' || plan.mode === 'pie') {
    const positive = plan.positive;
    const data = plan.pie;
    const total = data.reduce((s, x) => s + x.v, 0);
    const cx = w * 0.42;
    const cy = h * 0.49;
    const r = Math.min(w * 0.26, h * 0.38);
    const inner = plan.mode === 'donut' ? r * 0.59 : 0;
    if (!Number.isFinite(total)) {
      text(w / 2, h / 2, '数值超出图表范围，请查看透视表', '#b16f72');
      return { status: 'range', hit: [], points, series };
    }
    let angle = -Math.PI / 2;
    for (let j = 0; j < data.length; j++) {
      const obj = data[j];
      if (!obj) continue;
      const end = angle + (obj.v / total) * 2 * Math.PI;
      ctx.beginPath();
      ctx.moveTo(cx + Math.cos(angle) * inner, cy + Math.sin(angle) * inner);
      ctx.arc(cx, cy, r, angle, end);
      if (inner > 0) ctx.arc(cx, cy, inner, end, angle, true);
      else ctx.lineTo(cx, cy);
      ctx.closePath();
      ctx.fillStyle = color(j);
      ctx.fill();
      hit.push({ pie: true, cx, cy, r, inner, from: angle, to: end, row: obj.i, col: 0 });
      angle = end;
    }
    if (inner > 0) {
      text(cx, cy - 3, unitFmt(total), '#2c4852', 'center', 'bold 20px "Segoe UI"', inner * 1.7);
      text(cx, cy + 14, compared ? '已绘制数值之和' : '已绘制合计', '#a1adb6');
    }
    data.slice(0, 9).forEach((obj, j) => {
      const point = points[obj.i];
      if (!point) return;
      ctx.fillStyle = color(j);
      ctx.fillRect(w * 0.76, 35 + j * 28, 9, 9);
      text(w * 0.76 + 15, 43 + j * 28, label(point).slice(0, 17), '#516675', 'left');
    });
    if (!data.length) text(w / 2, h / 2, '当前分类没有可绘制的正数', '#a9b5be');
    if (positive.length > data.length || allRows.length > points.length) {
      scope =
        `绘制 ${data.length} 个正值分类（共 ${allRows.length} 个分类）` +
        (allSeries.length > 1 ? ' · 第1系列' : '') +
        (result?.truncated ? '；分类已截取' : '');
    }
    if (
      points.some((_, i) => {
        const first = values[i]?.[0];
        return first !== null && first !== undefined && first < 0;
      })
    ) {
      text(w * 0.49, h - 27, '含负数：扇形仅表示正值', '#b16f72');
    }
  } else if (plan.mode === 'heatmap' && series.length > 1) {
    const rows = Math.min(points.length, 28);
    const cols = series.length;
    const cellW = pw / cols;
    const cellH = ph / rows;
    const finiteAbs = values
      .flat()
      .filter((v): v is number => v !== null && Number.isFinite(v))
      .map(Math.abs);
    const max = Math.max(0, ...finiteAbs) || 1;
    points.slice(0, rows).forEach((row, i) => {
      text(
        plot.left - 9,
        plot.top + (i + 0.65) * cellH,
        label(row).slice(0, 9),
        '#758591',
        'right',
        '10px "Segoe UI"',
      );
      series.forEach((_, j) => {
        const v = values[i]?.[j];
        if (v === null || v === undefined) return;
        const alpha = Math.max(0.07, Math.min(0.94, Math.abs(v) / max));
        ctx.fillStyle = v < 0 ? `rgba(194,95,94,${alpha})` : `rgba(35,128,124,${alpha})`;
        ctx.fillRect(
          plot.left + j * cellW + 1,
          plot.top + i * cellH + 1,
          Math.max(1, cellW - 2),
          Math.max(1, cellH - 2),
        );
        interactive(plot.left + j * cellW, plot.top + i * cellH, cellW, cellH, i, j);
      });
    });
    series.forEach((s, j) => text(plot.left + (j + 0.5) * cellW, h - 20, seriesLabel(s).slice(0, 9)));
  } else if (plan.mode === 'horizontal') {
    const count = points.length;
    const rowH = ph / Math.max(1, count);
    points.slice(0, count).forEach((row, i) => {
      const left = plot.left + 45;
      const usable = Math.max(35, plot.right - left - 25);
      text(
        left - 9,
        plot.top + (i + 0.65) * rowH,
        label(row).slice(0, 13),
        '#758591',
        'right',
        '10px "Segoe UI"',
      );
      const y = plot.top + (i + 0.24) * rowH;
      const val = values[i]?.[0];
      if (val === null || val === undefined) return;
      const project = (v: number): number => left + ((v - range.low) / (range.high - range.low)) * usable;
      const x0 = project(0);
      const x1 = project(val);
      const start = Math.min(x0, x1);
      const bar = Math.abs(x1 - x0);
      ctx.fillStyle = val < 0 ? '#c67e7f' : color(i % 4);
      ctx.fillRect(start, y, bar, Math.max(2, rowH * 0.55));
      interactive(start, y, Math.max(5, bar), Math.max(4, rowH * 0.55), i, 0);
      if (w > 500) {
        text(
          x1 + (val < 0 ? -5 : 5),
          y + rowH * 0.44,
          formatAnalysisNumber(row.values[0], numberSettings),
          '#667985',
          val < 0 ? 'right' : 'left',
          '10px "Segoe UI"',
          Math.max(25, val < 0 ? x1 - 10 : w - x1 - 10),
        );
      }
    });
  } else {
    const project = (v: number): number =>
      plot.bottom - ((v - range.low) / (range.high - range.low)) * ph;
    const zero = project(0);
    for (let i = 0; i <= 4; i++) {
      const vv = range.low + ((range.high - range.low) * i) / 4;
      const y = project(vv);
      line(plot.left, y, plot.right, y);
      text(plot.left - 8, y + 3, unitFmt(vv), '#92a0ac', 'right', '11px "Segoe UI"', plot.left - 12);
    }
    const count = points.length;
    const slot = pw / count;
    const step = Math.max(1, Math.ceil(count / 11));
    points.forEach((row, i) => {
      if (i % step === 0) text(plot.left + (i + 0.5) * slot, h - 21, label(row).slice(0, 11), '#82939f');
    });
    if (plan.mode === 'line' || plan.mode === 'area' || plan.mode === 'scatter') {
      series.forEach((_, j) => {
        const segments: number[][] = [];
        let segment: number[] = [];
        points.forEach((_, i) => {
          const val = values[i]?.[j];
          if (val === null || val === undefined) {
            if (segment.length) segments.push(segment);
            segment = [];
          } else {
            segment.push(i);
          }
        });
        if (segment.length) segments.push(segment);
        for (const indices of segments) {
          const firstIdx = indices[0];
          const lastIdx = indices.at(-1);
          if (firstIdx === undefined || lastIdx === undefined) continue;
          if (plan.mode === 'area') {
            ctx.beginPath();
            ctx.moveTo(plot.left + (firstIdx + 0.5) * slot, zero);
            for (const i of indices) {
              const val = values[i]?.[j];
              if (val !== null && val !== undefined) {
                ctx.lineTo(plot.left + (i + 0.5) * slot, project(val));
              }
            }
            ctx.lineTo(plot.left + (lastIdx + 0.5) * slot, zero);
            ctx.closePath();
            ctx.fillStyle = 'rgba(47,133,128,.16)';
            ctx.fill();
          }
          if (plan.mode !== 'scatter') {
            ctx.beginPath();
            indices.forEach((i, k) => {
              const val = values[i]?.[j];
              if (val === null || val === undefined) return;
              const x = plot.left + (i + 0.5) * slot;
              const y = project(val);
              if (k === 0) ctx.moveTo(x, y);
              else ctx.lineTo(x, y);
            });
            ctx.strokeStyle = color(j);
            ctx.lineWidth = 2;
            ctx.stroke();
          }
          for (const i of indices) {
            const val = values[i]?.[j];
            if (val === null || val === undefined) continue;
            const x = plot.left + (i + 0.5) * slot;
            const y = project(val);
            ctx.beginPath();
            ctx.arc(x, y, Math.max(2.5, Math.min(4, slot * 0.15)), 0, Math.PI * 2);
            ctx.fillStyle = color(j);
            ctx.fill();
            interactive(x - 8, y - 8, 16, 16, i, j);
          }
        }
      });
    } else {
      points.forEach((_, i) => {
        if (plan.mode === 'stacked' && series.length > 1) {
          let positive = 0;
          let negative = 0;
          series.forEach((_, j) => {
            const val = values[i]?.[j];
            if (val === null || val === undefined) return;
            const baseValue = val >= 0 ? positive : negative;
            const y = project(baseValue + val);
            const base = project(baseValue);
            const x = plot.left + (i + 0.17) * slot;
            ctx.fillStyle = color(j);
            ctx.fillRect(x, Math.min(y, base), slot * 0.67, Math.max(1, Math.abs(base - y)));
            interactive(x, Math.min(y, base), slot * 0.67, Math.max(3, Math.abs(base - y)), i, j);
            if (val >= 0) positive += val;
            else negative += val;
          });
        } else {
          series.forEach((_, j) => {
            const val = values[i]?.[j];
            if (val === null || val === undefined) return;
            const x = plot.left + (i + 0.1 + (j * 0.78) / series.length) * slot;
            const bw = (slot * 0.78) / series.length;
            const y = project(val);
            ctx.fillStyle = color(j);
            ctx.fillRect(x, Math.min(y, zero), Math.max(2, bw), Math.max(1, Math.abs(zero - y)));
            interactive(x, Math.min(y, zero), Math.max(2, bw), Math.max(4, Math.abs(zero - y)), i, j);
          });
        }
      });
    }
    if (series.length > 1) {
      series.slice(0, 7).forEach((s, j) => {
        const start = w - 145 - (j % 2) * 120;
        ctx.fillStyle = color(j);
        ctx.fillRect(Math.max(10, start), 8 + Math.floor(j / 2) * 15, 7, 7);
        text(
          Math.max(20, start + 12),
          15 + Math.floor(j / 2) * 15,
          seriesLabel(s).slice(0, 13),
          '#758997',
          'left',
          '10px "Segoe UI"',
        );
      });
    }
  }
  if (scope) text(w / 2, h - 5, scope, '#768a97', 'center', '10px "Segoe UI"');
  return { status: 'ok', hit, points, series, label, seriesLabel, plan };
}

function repaintActiveCharts(): void {
  if (typeof document === 'undefined') return;
  for (const canvas of document.querySelectorAll<HTMLCanvasElement>('#analysisRoot canvas')) {
    const saved = rendered.get(canvas);
    if (saved && canvas.getClientRects().length) {
      drawChart(canvas, saved.result, saved.mode, saved.onPick);
    }
  }
}

export function resizeCharts(): void {
  if (resizeFrame) return;
  resizeFrame = requestAnimationFrame(() => {
    resizeFrame = 0;
    repaintActiveCharts();
  });
}

export function resetChartListeners(): void {
  listenersBound = false;
  resizeFrame = 0;
}

/**
 * Bind ResizeObserver and theme/number-format listeners once in browser environments.
 */
export function initChartsListeners(): void {
  if (listenersBound || typeof document === 'undefined') return;
  listenersBound = true;
  if (typeof ResizeObserver !== 'undefined') {
    const observer = new ResizeObserver(resizeCharts);
    for (const panel of document.querySelectorAll('#analysisRoot .analysis-panel')) {
      observer.observe(panel);
    }
  }
  document.addEventListener('excellens:readingchange', repaintActiveCharts);
  document.addEventListener('excellens:analysisnumberchange', repaintActiveCharts);
}

export function drawChart(
  canvas: HTMLCanvasElement | null | undefined,
  result: ChartResultLike | null | undefined,
  mode = 'bar',
  onPick?: ChartPickHandler,
): (e: MouseEvent) => ChartHitBox | null {
  if (!canvas) return () => null;
  initChartsListeners();
  const validMode: ChartMode = isChartMode(mode) ? mode : 'bar';
  const theme = chartThemeFor(canvas);
  const numberSettings = getAnalysisNumberOptions();
  const prevHover = rendered.get(canvas)?.hoverFrame;
  if (prevHover && typeof cancelAnimationFrame === 'function') {
    cancelAnimationFrame(prevHover);
  }
  const entry: ChartRenderSnapshot = {
    result: result ?? null,
    mode: validMode,
    onPick,
    theme,
    numberSettings,
    hoverFrame: 0,
  };
  rendered.set(canvas, entry);

  const dpr = Math.min(window.devicePixelRatio || 1, 2);
  const bounds = canvas.getBoundingClientRect();
  const w = Math.max(140, Math.floor(bounds.width));
  const h = Math.max(140, Math.floor(bounds.height));
  canvas.width = w * dpr;
  canvas.height = h * dpr;
  const ctx = canvas.getContext('2d', { alpha: false });
  if (!ctx) return () => null;
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  canvas.onclick = null;
  canvas.onmousemove = null;
  canvas.onmouseleave = null;
  canvas.style.cursor = 'crosshair';
  const tip = canvas.parentElement?.querySelector<HTMLElement>('.analysis-chart-tooltip') ?? null;
  if (tip) tip.hidden = true;

  const drawing = paintChart(ctx, w, h, result, mode, theme, numberSettings);
  if (drawing.status !== 'ok') return () => null;
  const { hit, points, series, label, seriesLabel } = drawing;

  function pick(e: MouseEvent): ChartHitBox | null {
    if (!canvas) return null;
    const rect = canvas.getBoundingClientRect();
    const x = ((e.clientX - rect.left) * w) / rect.width;
    const y = ((e.clientY - rect.top) * h) / rect.height;
    for (let i = hit.length - 1; i >= 0; i--) {
      const box = hit[i];
      if (!box) continue;
      if (box.pie) {
        const dx = x - box.cx;
        const dy = y - box.cy;
        const rad = Math.hypot(dx, dy);
        const angle = Math.atan2(dy, dx);
        const aa = angle < box.from ? angle + Math.PI * 2 : angle;
        if (rad >= box.inner && rad <= box.r && aa >= box.from && aa <= box.to) return box;
      } else if (x >= box.x && x <= box.x + box.w && y >= box.y && y <= box.y + box.h) {
        return box;
      }
    }
    return null;
  }

  let lastKey = '';
  let lastEvent: MouseEvent | null = null;
  let tipWidth = 0;
  let pending = false;

  const applyHover = (e: MouseEvent): void => {
    const p = pick(e);
    const cursor = p ? 'pointer' : 'crosshair';
    if (canvas.style.cursor !== cursor) canvas.style.cursor = cursor;
    if (!tip) return;
    if (!p) {
      if (!tip.hidden) tip.hidden = true;
      lastKey = '';
      return;
    }
    const key = `${p.row}:${p.col}`;
    const point = points[p.row];
    const seriesName = series[p.col];
    if (!point || seriesName === undefined) return;
    if (key !== lastKey || tip.hidden) {
      lastKey = key;
      const cell = point.values[p.col];
      const display = formatAnalysisNumber(cell);
      const extra = cell?.comparison
        ? `（${comparisonTitle(cell)}）`
        : cell?.exact && cell.exact !== display
          ? `（精确值 ${cell.exact}）`
          : '';
      tip.textContent = `${label(point)} · ${seriesLabel(seriesName)}：${display}${extra}`;
      tip.hidden = false;
      tipWidth = tip.offsetWidth || tip.getBoundingClientRect().width || 0;
    }
    const rect = canvas.parentElement?.getBoundingClientRect();
    if (!rect) return;
    tip.style.left = `${Math.max(6, Math.min(rect.width - tipWidth - 6, e.clientX - rect.left + 12))}px`;
    tip.style.top = `${Math.max(6, e.clientY - rect.top - 28)}px`;
  };

  canvas.onclick = (e: MouseEvent): void => {
    const p = pick(e);
    const point = p ? points[p.row] : undefined;
    const seriesName = p ? series[p.col] : undefined;
    if (p && point && seriesName !== undefined && onPick) {
      onPick(point.keys, seriesName);
    }
  };
  canvas.onmousemove = (e: MouseEvent): void => {
    lastEvent = e;
    if (!entry.hoverFrame) {
      applyHover(e);
      pending = false;
      entry.hoverFrame = requestAnimationFrame(() => {
        entry.hoverFrame = 0;
        if (pending && lastEvent) {
          pending = false;
          applyHover(lastEvent);
        }
      });
    } else {
      pending = true;
    }
  };
  canvas.onmouseleave = (): void => {
    if (entry.hoverFrame && typeof cancelAnimationFrame === 'function') {
      cancelAnimationFrame(entry.hoverFrame);
    }
    entry.hoverFrame = 0;
    pending = false;
    lastEvent = null;
    lastKey = '';
    if (tip) tip.hidden = true;
  };
  return pick;
}

/**
 * Bounded snapshot trends use the same exact-to-plot conversion. Missing
 * values break the path; unrepresentable exact values never become zero.
 */
export function drawSparkline(
  canvas: HTMLCanvasElement,
  cells: readonly (AnalysisCell | null | undefined)[],
  domain: readonly (AnalysisCell | null | undefined)[] = cells,
): void {
  const number = (c: AnalysisCell | null | undefined): number | null => (c ? chartNumber(c) : null);
  const values = cells.map(number);
  const range = domain.map(number);
  const theme = chartThemeFor(canvas);
  const box = canvas.getBoundingClientRect();
  const w = Math.max(120, Math.floor(box.width));
  const h = Math.max(32, Math.floor(box.height));
  const dpr = Math.min(window.devicePixelRatio || 1, 2);
  canvas.width = w * dpr;
  canvas.height = h * dpr;
  const ctx = canvas.getContext('2d');
  if (!ctx) return;
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  ctx.clearRect(0, 0, w, h);

  const finite = range.filter((v): v is number => v !== null && Number.isFinite(v));
  const invalid =
    values.some((v) => v !== null && Number.isNaN(v)) ||
    range.some((v) => v !== null && Number.isNaN(v));
  if (invalid || !finite.length) {
    ctx.fillStyle = theme.muted;
    ctx.font = '12px "Segoe UI"';
    ctx.textAlign = 'center';
    ctx.fillText(invalid ? '数值超出绘图范围' : '无可绘制数值', w / 2, h / 2 + 4);
    return;
  }

  const unit = Math.max(...finite.map(Math.abs)) || 1;
  const low = Math.min(...finite.map((v) => v / unit));
  const high = Math.max(...finite.map((v) => v / unit));
  const strokeColor = PALETTE[0] ?? '#287b78';
  ctx.strokeStyle = strokeColor;
  ctx.fillStyle = strokeColor;
  ctx.lineWidth = 1.8;
  ctx.beginPath();
  let connected = false;
  values.forEach((v, i) => {
    if (v === null) {
      connected = false;
      return;
    }
    const x = 5 + (i * (w - 10)) / Math.max(1, values.length - 1);
    const y = high === low ? h / 2 : h - 5 - ((v / unit - low) / (high - low)) * (h - 10);
    if (connected) ctx.lineTo(x, y);
    else ctx.moveTo(x, y);
    connected = true;
  });
  ctx.stroke();

  // A run consisting of one point has no stroked length. Mark each valid
  // point so alternating missing series remain visible without joining gaps.
  values.forEach((v, i) => {
    if (v === null) return;
    const x = 5 + (i * (w - 10)) / Math.max(1, values.length - 1);
    const y = high === low ? h / 2 : h - 5 - ((v / unit - low) / (high - low)) * (h - 10);
    ctx.beginPath();
    ctx.arc(x, y, 1.6, 0, Math.PI * 2);
    ctx.fill();
  });
}

export function chartSnapshot(canvas: HTMLCanvasElement): ChartRenderSnapshot | null {
  return rendered.get(canvas) ?? null;
}

export const AnalyticsCharts = Object.freeze({
  draw: drawChart,
  paint: paintChart,
  prepare: prepareChart,
  describe: describeChart,
  themeFor: chartThemeFor,
  modes: CHART_MODES,
  sparkline: drawSparkline,
  snapshot: chartSnapshot,
  resize: resizeCharts,
});
