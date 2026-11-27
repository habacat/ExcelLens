/**
 * Canvas subset used by the shared chart core for PNG and vector-only SVG export.
 */
import type { ChartExportLimits } from '../contracts/formats.ts';
import {
  paintChart,
  type ChartDrawContext,
  type ChartTextMetrics,
  type ChartTheme,
} from './charts.ts';
import {
  chartExportCapacityError,
  resolveChartExportPlot,
  validateChartExportDimensions,
  type ChartExportPayload,
  type ChartExportSpec,
} from './chart-export-model.ts';

const encoder = new TextEncoder();

interface RasterTicket {
  readonly resolve: (release: () => void) => void;
  readonly reject: (reason: Error) => void;
  cleanup: () => void;
  abort: () => void;
  release: () => void;
}

// toBlob itself cannot be aborted. One owner retains its canvas until the
// real callback; one cancellable waiter permits an immediate UI retry
// without allowing cancelled encoders to accumulate large bitmaps.
let rasterOwner: RasterTicket | null = null;
let rasterWaiter: RasterTicket | null = null;

function makeRasterAbortError(): Error {
  const err = new Error('图片生成已取消');
  err.name = 'AbortError';
  return err;
}

function rasterLease(signal?: AbortSignal): Promise<() => void> {
  if (signal?.aborted) return Promise.reject(makeRasterAbortError());
  return new Promise<() => void>((resolve, reject) => {
    const ticket: RasterTicket = {
      resolve,
      reject,
      cleanup: () => {
        signal?.removeEventListener('abort', ticket.abort);
      },
      abort: () => {
        if (rasterWaiter === ticket) {
          rasterWaiter = null;
          ticket.cleanup();
          reject(makeRasterAbortError());
        }
      },
      release: () => {
        if (rasterOwner !== ticket) return;
        rasterOwner = null;
        ticket.cleanup();
        if (rasterWaiter) {
          const next = rasterWaiter;
          rasterWaiter = null;
          rasterOwner = next;
          next.resolve(next.release);
        }
      },
    };
    if (!rasterOwner) {
      rasterOwner = ticket;
      resolve(ticket.release);
    } else if (!rasterWaiter) {
      rasterWaiter = ticket;
      signal?.addEventListener('abort', ticket.abort, { once: true });
    } else {
      reject(new Error('已有图片正在等待编码，请取消后重试'));
    }
  });
}

function assertFiniteCoord(n: number): number {
  if (!Number.isFinite(n) || Math.abs(n) > 1e7) throw new Error('图表绘制坐标无效');
  return n;
}

function formatSvgNumber(n: number): string {
  assertFiniteCoord(n);
  return String(Number(n.toFixed(6)));
}

const XML_ENTITIES: Readonly<Record<string, string>> = {
  '&': '&amp;',
  '<': '&lt;',
  '>': '&gt;',
  '"': '&quot;',
  "'": '&apos;',
};

export function escapeXml(value: string): string {
  let out = '';
  for (let i = 0; i < value.length; i++) {
    const code = value.charCodeAt(i);
    if (
      (code >= 0x0000 && code <= 0x0008) ||
      code === 0x000b ||
      code === 0x000c ||
      (code >= 0x000e && code <= 0x001f) ||
      code === 0xfffe ||
      code === 0xffff
    ) {
      out += '\uFFFD';
    } else if (code >= 0xd800 && code <= 0xdbff) {
      const next = i + 1 < value.length ? value.charCodeAt(i + 1) : 0;
      if (next >= 0xdc00 && next <= 0xdfff) {
        out += value.slice(i, i + 2);
        i++;
      } else {
        out += '\uFFFD';
      }
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      out += '\uFFFD';
    } else {
      const ch = value[i] ?? '';
      out += XML_ENTITIES[ch] ?? ch;
    }
  }
  return out;
}

function validateColor(value: unknown): string {
  if (
    typeof value !== 'string' ||
    !/^(?:#(?:[\da-f]{3}|[\da-f]{4}|[\da-f]{6}|[\da-f]{8})|rgba?\([\d.,\s%]+\))$/i.test(value)
  ) {
    throw new Error('图表颜色无法导出');
  }
  if (value.startsWith('rgb')) {
    const parts = value
      .slice(value.indexOf('(') + 1, -1)
      .split(',')
      .map((s) => s.trim());
    if (
      parts.length !== (value.startsWith('rgba') ? 4 : 3) ||
      parts.some(
        (s, i) =>
          !/^\d*\.?\d+%?$/.test(s) ||
          Number.parseFloat(s) < 0 ||
          Number.parseFloat(s) > (s.endsWith('%') ? 100 : i === 3 ? 1 : 255),
      )
    ) {
      throw new Error('图表颜色无效');
    }
  }
  return value;
}

interface SVGContextState {
  readonly tx: number;
  readonly ty: number;
  readonly font: string;
  readonly textAlign: CanvasTextAlign;
  readonly fillStyle: string | CanvasGradient | CanvasPattern;
  readonly strokeStyle: string | CanvasGradient | CanvasPattern;
  readonly lineWidth: number;
}

export interface SVGTextMeasurer {
  font: string;
  measureText(text: string): ChartTextMetrics;
}

export class SVGContext implements ChartDrawContext {
  private readonly measure: SVGTextMeasurer;
  private readonly limits: Pick<ChartExportLimits, 'svgNodes' | 'svgBytes'>;
  private readonly nodes: string[] = [];
  private bytes = 0;
  private readonly stack: SVGContextState[] = [];
  private tx = 0;
  private ty = 0;
  private path = '';
  private current: readonly [number, number] | null = null;
  private start: readonly [number, number] | null = null;

  public font = '11px "Segoe UI"';
  public textAlign: CanvasTextAlign = 'start';
  public fillStyle: string | CanvasGradient | CanvasPattern = '#000';
  public strokeStyle: string | CanvasGradient | CanvasPattern = '#000';
  public lineWidth = 1;

  constructor(
    measure: SVGTextMeasurer,
    limits: Pick<ChartExportLimits, 'svgNodes' | 'svgBytes'>,
  ) {
    this.measure = measure;
    this.limits = limits;
    this.beginPath();
  }

  private emit(node: string): void {
    if (this.nodes.length >= this.limits.svgNodes) throw chartExportCapacityError();
    this.bytes += encoder.encode(node).length;
    if (this.bytes > this.limits.svgBytes) throw chartExportCapacityError();
    this.nodes.push(node);
  }

  private point(x: number, y: number): readonly [number, number] {
    return [assertFiniteCoord(x + this.tx), assertFiniteCoord(y + this.ty)];
  }

  private command(text: string): void {
    if (this.path.length + text.length > this.limits.svgBytes) throw chartExportCapacityError();
    this.path += text;
  }

  beginPath(): void {
    this.path = '';
    this.current = null;
    this.start = null;
  }

  moveTo(x: number, y: number): void {
    const p = this.point(x, y);
    this.command(`M${formatSvgNumber(p[0])} ${formatSvgNumber(p[1])}`);
    this.current = p;
    this.start = p;
  }

  lineTo(x: number, y: number): void {
    if (!this.current) {
      this.moveTo(x, y);
      return;
    }
    const p = this.point(x, y);
    this.command(`L${formatSvgNumber(p[0])} ${formatSvgNumber(p[1])}`);
    this.current = p;
  }

  closePath(): void {
    if (this.current) {
      this.command('Z');
      this.current = this.start;
    }
  }

  arc(x: number, y: number, r: number, start: number, end: number, ccw = false): void {
    assertFiniteCoord(x);
    assertFiniteCoord(y);
    assertFiniteCoord(r);
    assertFiniteCoord(start);
    assertFiniteCoord(end);
    if (r < 0) throw new Error('图表圆弧半径无效');
    const tau = Math.PI * 2;
    const raw = ccw ? start - end : end - start;
    const span = raw >= tau ? tau : ((raw % tau) + tau) % tau;
    const firstX = x + Math.cos(start) * r;
    const firstY = y + Math.sin(start) * r;
    const position = this.point(firstX, firstY);
    if (!this.current) {
      this.moveTo(firstX, firstY);
    } else if (Math.hypot(this.current[0] - position[0], this.current[1] - position[1]) > 1e-8) {
      this.lineTo(firstX, firstY);
    }
    if (!r || !span) return;
    const count = Math.ceil(span / Math.PI);
    const step = (span / count) * (ccw ? -1 : 1);
    for (let i = 1; i <= count; i++) {
      const angle = start + step * i;
      const p = this.point(x + Math.cos(angle) * r, y + Math.sin(angle) * r);
      this.command(
        `A${formatSvgNumber(r)} ${formatSvgNumber(r)} 0 0 ${ccw ? '0' : '1'} ${formatSvgNumber(p[0])} ${formatSvgNumber(p[1])}`,
      );
      this.current = p;
    }
  }

  fill(): void {
    if (this.path) {
      this.emit(`<path d="${this.path}" fill="${validateColor(this.fillStyle)}"/>`);
    }
  }

  stroke(): void {
    if (this.path) {
      if (!Number.isFinite(this.lineWidth) || this.lineWidth <= 0) {
        throw new Error('图表线宽无效');
      }
      this.emit(
        `<path d="${this.path}" fill="none" stroke="${validateColor(this.strokeStyle)}" stroke-width="${formatSvgNumber(this.lineWidth)}"/>`,
      );
    }
  }

  fillRect(x: number, y: number, width: number, height: number): void {
    const p = this.point(x, y);
    assertFiniteCoord(width);
    assertFiniteCoord(height);
    const left = width < 0 ? p[0] + width : p[0];
    const top = height < 0 ? p[1] + height : p[1];
    this.emit(
      `<rect x="${formatSvgNumber(left)}" y="${formatSvgNumber(top)}" width="${formatSvgNumber(Math.abs(width))}" height="${formatSvgNumber(Math.abs(height))}" fill="${validateColor(this.fillStyle)}"/>`,
    );
  }

  measureText(text: string): ChartTextMetrics {
    this.measure.font = this.font;
    return this.measure.measureText(text);
  }

  fillText(value: string, x: number, y: number, maxWidth?: number): void {
    const p = this.point(x, y);
    const font = /^(?:(bold|normal)\s+)?(\d+(?:\.\d+)?)px\s+"?([\w\s]+)"?$/.exec(this.font);
    const fontFamily = font?.[3]?.trim();
    const fontSize = font?.[2];
    if (!font || !fontFamily || !fontSize || !['Segoe UI', 'Microsoft YaHei'].includes(fontFamily)) {
      throw new Error('图表字体无法导出');
    }
    const measured = this.measureText(value).width;
    const squeeze =
      maxWidth !== undefined && maxWidth > 0 && measured > maxWidth
        ? ` textLength="${formatSvgNumber(maxWidth)}" lengthAdjust="spacingAndGlyphs"`
        : '';
    const anchor =
      this.textAlign === 'center'
        ? 'middle'
        : this.textAlign === 'right' || this.textAlign === 'end'
          ? 'end'
          : 'start';
    this.emit(
      `<text x="${formatSvgNumber(p[0])}" y="${formatSvgNumber(p[1])}" fill="${validateColor(this.fillStyle)}" font-family="${escapeXml(fontFamily)}" font-size="${fontSize}" font-weight="${font[1] === 'bold' ? '700' : '400'}" text-anchor="${anchor}" xml:space="preserve"${squeeze}>${escapeXml(value)}</text>`,
    );
  }

  save(): void {
    if (this.stack.length >= 32) throw chartExportCapacityError();
    this.stack.push({
      tx: this.tx,
      ty: this.ty,
      font: this.font,
      textAlign: this.textAlign,
      fillStyle: this.fillStyle,
      strokeStyle: this.strokeStyle,
      lineWidth: this.lineWidth,
    });
  }

  restore(): void {
    const state = this.stack.pop();
    if (!state) throw new Error('SVG绘图状态无效');
    this.tx = state.tx;
    this.ty = state.ty;
    this.font = state.font;
    this.textAlign = state.textAlign;
    this.fillStyle = state.fillStyle;
    this.strokeStyle = state.strokeStyle;
    this.lineWidth = state.lineWidth;
  }

  translate(x: number, y: number): void {
    this.tx = assertFiniteCoord(this.tx + x);
    this.ty = assertFiniteCoord(this.ty + y);
  }

  serialize(width: number, height: number, title: string): string {
    if (this.stack.length) throw new Error('SVG绘图状态未结束');
    const w = formatSvgNumber(width);
    const h = formatSvgNumber(height);
    const text = `<?xml version="1.0" encoding="UTF-8"?><svg xmlns="http://www.w3.org/2000/svg" width="${w}" height="${h}" viewBox="0 0 ${w} ${h}" role="img" aria-labelledby="chart-title"><title id="chart-title">${escapeXml(title)}</title><desc>已接受结果快照；精确值与完整说明见同包 exact-data.json 及 README.txt。</desc><defs><clipPath id="chart-bounds"><rect width="${w}" height="${h}"/></clipPath></defs><g clip-path="url(#chart-bounds)">${this.nodes.join('')}</g></svg>`;
    if (encoder.encode(text).length > this.limits.svgBytes) throw chartExportCapacityError();
    return text;
  }
}

export function fitSvgLine(
  ctx: Pick<ChartDrawContext, 'measureText'>,
  value: string,
  width: number,
): string {
  const chars: string[] = [];
  let shortened = false;
  for (const c of value) {
    if (chars.length === 512) {
      shortened = true;
      break;
    }
    chars.push(c);
  }
  const text = chars.join('');
  if (ctx.measureText(text).width <= width && !shortened) return text;
  let lo = 0;
  let hi = chars.length;
  while (lo < hi) {
    const mid = Math.ceil((lo + hi) / 2);
    if (ctx.measureText(`${chars.slice(0, mid).join('')}…`).width <= width) lo = mid;
    else hi = mid - 1;
  }
  return `${chars.slice(0, lo).join('')}…`;
}

export function chartExportDescriptions(payload: ChartExportPayload): readonly string[] {
  const c = payload.chart;
  const d = c.drawing;
  const flags: string[] = [];
  if (d.resultTruncated) flags.push('结果已按前 N 项截取，未显示分类不在附件行中');
  if (d.firstTreeLevel) flags.push('层级透视首层，未导出展开后代');
  if (d.positiveOnly) {
    flags.push(
      `仅从前${d.consideredRows}分类绘制前${d.rowIndices.length}个正值及第1系列，负数/零不画`,
    );
  }
  if (d.missingCells) flags.push(`${d.missingCells}个缺值不补0`);
  const display = c.numberSettings;
  return [
    `绘制 ${d.rowIndices.length}/${d.totalRows} 个分类 · ${d.seriesIndices.length}/${d.totalSeries} 个系列；范围：${c.scope}`,
    flags.join('；') || '本图保留所绘分类顺序；附件保留全部已接受结果。',
    '绘图坐标使用近似值；原始/派生精确值、分数及状态以 exact-data.json 为准。',
    `快照 ${c.capturedAt} · 千分位${display.groupThousands ? '开启' : '关闭'} · 科学计数法${display.scientific ? `10^${display.scientificExponent}起` : '关闭'}`,
    '长标签/说明在图片中可能截短，完整原文见附件。',
    '数据附件为已接受结果快照，不代表未截取的整个源表；源文件未修改。',
  ];
}

const WHITE_THEME: ChartTheme = Object.freeze({
  themed: false,
  paper: '#fff',
  ink: '#2c4852',
  muted: '#768a97',
  grid: '#e8eef1',
  error: '#b16f72',
});

export function paintChartExportDocument(
  ctx: ChartDrawContext,
  payload: ChartExportPayload,
  spec: ChartExportSpec,
): void {
  const c = payload.chart;
  const theme: ChartTheme = spec.background === 'white' ? WHITE_THEME : c.theme;
  ctx.fillStyle = validateColor(theme.paper);
  ctx.fillRect(0, 0, spec.width, spec.height);
  ctx.textAlign = 'left';

  const text = (value: string, y: number, size = 12, bold = false): void => {
    ctx.font = `${bold ? 'bold ' : ''}${size}px "Segoe UI"`;
    ctx.fillStyle = validateColor(bold ? theme.ink : theme.muted);
    ctx.fillText(fitSvgLine(ctx, value, spec.width - 48), 24, y);
  };

  text(c.title || 'ExcelLens 图表', 32, 18, true);
  text(`来源：${c.source}`, 56);
  text(`指标：${c.metric} · 单位：${c.unit}`, 78);

  ctx.save();
  ctx.translate(0, 100);
  const drawn = paintChart(
    ctx,
    spec.width,
    spec.height - 206,
    resolveChartExportPlot(payload),
    c.mode,
    theme,
    c.numberSettings,
  );
  ctx.restore();
  if (drawn.status !== 'ok') throw new Error('数值超出可绘制范围，未生成图片');

  const description = chartExportDescriptions(payload);
  const d = c.drawing;
  const notes: string[] = [description[0] ?? ''];
  if (d.resultTruncated || d.firstTreeLevel || d.positiveOnly || d.missingCells) {
    notes.push(description[1] ?? '');
  }
  notes.push('精确数据见 exact-data.json');
  notes.forEach((line, i) => text(line, spec.height - 74 + i * 18, 11));
}

export async function renderChartExportImage(
  payload: ChartExportPayload,
  spec: ChartExportSpec,
  limits: ChartExportLimits,
  checkpoint: () => Promise<void>,
  signal?: AbortSignal,
): Promise<Blob> {
  const dimensions = validateChartExportDimensions(spec, limits);
  await checkpoint();
  const release =
    spec.type === 'png'
      ? await rasterLease(signal)
      : (): void => {
          // SVG does not require a raster lease.
        };
  let canvas: HTMLCanvasElement | undefined;
  try {
    await checkpoint();
    canvas = document.createElement('canvas');
    if (spec.type === 'svg') {
      const measure = canvas.getContext('2d');
      if (!measure) throw new Error('当前环境无法测量图表字体');
      const ctx = new SVGContext(measure, limits);
      paintChartExportDocument(ctx, payload, spec);
      return new Blob(
        [ctx.serialize(spec.width, spec.height, fitSvgLine(measure, payload.chart.title, spec.width))],
        { type: 'image/svg+xml;charset=utf-8' },
      );
    }
    canvas.width = dimensions.width;
    canvas.height = dimensions.height;
    const ctx = canvas.getContext('2d', { alpha: false });
    if (!ctx) throw new Error('当前环境无法创建图表图片');
    ctx.setTransform(spec.scale, 0, 0, spec.scale, 0, 0);
    paintChartExportDocument(ctx, payload, spec);
    const rasterCanvas = canvas;
    const blob = await new Promise<Blob>((resolve, reject) => {
      try {
        rasterCanvas.toBlob(
          (value) => (value ? resolve(value) : reject(new Error('PNG编码失败，请重试'))),
          'image/png',
        );
      } catch (error: unknown) {
        reject(error instanceof Error ? error : new Error('PNG编码失败，请重试'));
      }
    });
    await checkpoint();
    if (blob.type !== 'image/png' || blob.size > limits.imageBytes) {
      throw chartExportCapacityError();
    }
    return blob;
  } finally {
    if (canvas) canvas.width = canvas.height = 0;
    release();
  }
}

export const ChartExportSurface = Object.freeze({
  SVGContext,
  xml: escapeXml,
  fitLine: fitSvgLine,
  descriptions: chartExportDescriptions,
  paintDocument: paintChartExportDocument,
  image: renderChartExportImage,
});
