/**
 * Shared cell presentation contract for both the scrolling virtual grid and
 * frozen panes: display formatting, rich-text budget planning, template cloning,
 * General number precision fitting, and text overflow/spill calculation.
 */
import { t as localize } from '../core/i18n.ts';
import type { MergeRange } from '../contracts/common.ts';
import type { RichTextLimits } from '../contracts/formats.ts';
import type { CellData, RichText, RowData, VisualStyle } from '../contracts/workbook.ts';
import {
  applyConditionalNotice,
  conditionalFont,
  conditionalNeedsInline,
  conditionalValue,
  conditionalVisual,
  type ConditionalCellLike,
} from './conditional.ts';
import { eachRichPart, inspectRichText, overlayRichFont, paintRichText } from './richtext.ts';
import type { GridCellElement, WorkbookState } from './state.ts';

export function cellDisplayValue(
  cell: ConditionalCellLike | null | undefined,
  formulas = false,
  styles = true,
): string {
  return conditionalValue(cell, formulas, styles);
}

export function isCellOccupied(cell: CellData | null | undefined): boolean {
  return Boolean(cell && (cell.hasFormula || cell.raw !== '' || cell.display !== ''));
}

const TEXT_TYPES = new Set(['s', 'str', 'inlineStr', 'text', 'string']);
const NUMERIC_RE = /^[+-]?(?:\d+\.?\d*|\.\d+)(?:[eE][+-]?\d+)?$/;

function fitGeneralNumber(text: string, fits: (candidate: string) => boolean): string {
  const n = Number(text);
  if (!Number.isFinite(n) || n === 0) return '';
  const x = Math.abs(n);
  const integerDigits = x < 1 ? 1 : Math.floor(Math.log10(x)) + 1;
  if (x < 1e11 && x >= 1e-9) {
    for (let places = Math.min(14, 15 - integerDigits); places >= 0; places--) {
      const candidate = n.toFixed(places).replace(/(\.\d*?[1-9])0+$|\.0+$/, '$1');
      if (Number(candidate) !== 0 && fits(candidate)) return candidate;
    }
  }
  for (let places = 6; places >= 0; places--) {
    const parts = n.toExponential(places).split('e');
    const mantissa = parts[0] ?? '';
    const exponent = parts[1] ?? '+0';
    const candidate =
      mantissa.replace(/(\.\d*?[1-9])0+$|\.0+$/, '$1') +
      'E' +
      (exponent[0] ?? '+') +
      exponent.slice(1).padStart(2, '0');
    if (fits(candidate)) return candidate;
  }
  return '';
}

const selectionFills = new Map<string, string>();

function parseRgb(value: string | undefined, fallback: readonly [number, number, number]): readonly [number, number, number] {
  const hex = /^#([\da-f]{6})$/i.exec(value ?? '');
  if (!hex?.[1]) return fallback;
  const h = hex[1];
  return [
    parseInt(h.slice(0, 2), 16),
    parseInt(h.slice(2, 4), 16),
    parseInt(h.slice(4, 6), 16),
  ];
}

function luminance(color: readonly [number, number, number]): number {
  const weights = [0.2126, 0.7152, 0.0722] as const;
  let sum = 0;
  for (let i = 0; i < 3; i++) {
    const v = (color[i] ?? 0) / 255;
    sum += (weights[i] ?? 0) * (v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4);
  }
  return sum;
}

function contrast(a: number, b: number): number {
  return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
}

export function selectionFill(background?: string, foreground?: string): string {
  const key = `${background ?? ''}|${foreground ?? ''}`;
  const cached = selectionFills.get(key);
  if (cached !== undefined) return cached;
  const base = parseRgb(background, [255, 255, 255]);
  const ink = luminance(parseRgb(foreground, [48, 67, 85]));
  const required = Math.min(4.5, contrast(ink, luminance(base)) * 0.9);
  const mean = (base[0] + base[1] + base[2]) / 3;
  const preferred = mean >= 128 ? 0 : 255;
  let result = 'rgba(0,0,0,0)';
  for (const gray of [preferred, 255 - preferred]) {
    const distance = Math.abs(gray - mean);
    if (distance < 20) continue;
    const alpha = Math.min(1, 25 / distance);
    const painted: [number, number, number] = [
      base[0] + (gray - base[0]) * alpha,
      base[1] + (gray - base[1]) * alpha,
      base[2] + (gray - base[2]) * alpha,
    ];
    if (contrast(ink, luminance(painted)) >= required) {
      result = `rgba(${gray},${gray},${gray},${alpha})`;
      break;
    }
  }
  if (selectionFills.size >= 256) {
    const oldest = selectionFills.keys().next().value;
    if (oldest !== undefined) selectionFills.delete(oldest);
  }
  selectionFills.set(key, result);
  return result;
}

export function isTextCell(cell: CellData | null | undefined): boolean {
  if (!cell || cell.cacheMissing) return false;
  if (cell.type && TEXT_TYPES.has(cell.type)) return true;
  if (cell.type) return false;
  const raw = cell.raw || cell.display || '';
  return raw !== '' && !NUMERIC_RE.test(raw.trim());
}

export interface ColumnLayoutItem {
  readonly col: number;
  readonly width: number;
}

export interface PresenterOptions {
  readonly state: Pick<
    WorkbookState,
    'showStyles' | 'showFormulas' | 'fontOverride' | 'styles' | 'autoFilter' | 'indexReady'
  >;
  readonly applyStyle: (node: GridCellElement, cell: Partial<CellData>) => void;
  readonly colName: (col: number) => string;
  readonly scale: () => number;
  readonly fontStack: (name: string) => string;
  readonly merge?: (row: number, col: number) => MergeRange | null;
  readonly richLimits?: RichTextLimits | null;
  readonly numericPadding?: (node: HTMLElement) => number;
  readonly isHyperlinkVisited?: (ref: string) => boolean;
}

interface PaintStateCache {
  readonly styles: boolean;
  readonly formulas: boolean;
  readonly font: string;
  readonly scale: number;
  readonly key: string;
}

interface RichWidthCache {
  readonly font: string;
  readonly text: string;
  readonly width: number;
}

interface NumberMeasureCache {
  readonly text: string;
  readonly font: string;
  readonly width: number;
  readonly hash: number;
  fitWidth?: number;
  fitted?: string;
}

interface TemplateEntry {
  readonly node: GridCellElement;
  readonly paintKey: string | undefined;
}

export class CellPresenter {
  public readonly o: PresenterOptions;
  private measure: CanvasRenderingContext2D | null = null;
  private currentFont = '';
  private readonly widths = new Map<string, number>();
  private readonly richWidths = new WeakMap<RichText, RichWidthCache>();
  private readonly numberWidths = new WeakMap<CellData, NumberMeasureCache>();
  private readonly numberMeasures = new Map<string, NumberMeasureCache>();
  private readonly hashWidths = new Map<string, number>();
  private readonly templates = new Map<string, TemplateEntry>();
  private templatePalette: readonly VisualStyle[] | null = null;
  private richPlan: WeakMap<CellData, boolean> | null = null;
  private paintState: PaintStateCache | null = null;

  constructor(options: PresenterOptions) {
    this.o = options;
  }

  prepare(rows?: readonly RowData[] | null): void {
    this.richPlan = new WeakMap();
    let remaining = this.o.richLimits?.viewRuns ?? 0;
    for (const row of rows ?? []) {
      for (const cell of row.cells) {
        if (
          (!cell.richText && !conditionalNeedsInline(cell, this.style(cell, row.style))) ||
          this.richPlan.has(cell)
        ) {
          continue;
        }
        const count = cell.richText ? (cell.richText.runs?.length ?? 0) + 1 : 2;
        const allowed = count <= remaining;
        this.richPlan.set(cell, allowed);
        if (allowed) remaining -= count;
      }
    }
  }

  allowed(cell: CellData | null | undefined): boolean {
    if (!cell) return true;
    return (
      (!cell.richText && !conditionalNeedsInline(cell, this.style(cell))) ||
      !this.richPlan ||
      this.richPlan.get(cell) === true
    );
  }

  paintKey(): string {
    const s = this.o.state;
    const scale = this.o.scale();
    let p = this.paintState;
    if (
      p?.styles !== s.showStyles ||
      p.formulas !== s.showFormulas ||
      p.font !== s.fontOverride ||
      p.scale !== scale
    ) {
      p = {
        styles: s.showStyles,
        formulas: s.showFormulas,
        font: s.fontOverride,
        scale,
        key: `${s.showStyles}|${s.showFormulas}|${s.fontOverride}|${scale}`,
      };
      this.paintState = p;
    }
    return p.key;
  }

  private hyperlinkVisited(node: GridCellElement, cell: CellData | null | undefined): boolean {
    if (!cell?.hyperlink || !this.o.isHyperlinkVisited) return false;
    const ref =
      cell.ref ||
      node.dataset.ref ||
      `${this.o.colName(Number(node.dataset.col))}${Number(node.dataset.row)}`;
    return Boolean(ref && this.o.isHyperlinkVisited(ref));
  }

  needsPaint(
    node: GridCellElement,
    cell: CellData | null | undefined,
    row?: Partial<RowData> | null,
  ): boolean {
    return (
      node._xlPaintKey !== this.paintKey() ||
      node._xlRichAllowed !== this.allowed(cell) ||
      node._xlRowPresentationNotice !== (row?.presentationNotice ?? '') ||
      Boolean(node._xlHyperlinkVisited) !== this.hyperlinkVisited(node, cell)
    );
  }

  value(cell: CellData | null | undefined): string {
    const s = this.o.state;
    const text = cellDisplayValue(cell, s.showFormulas, s.showStyles);
    if (!cell || (s.showFormulas && cell.hasFormula) || cell.conditional?.display != null) {
      return text;
    }
    const pattern =
      cell.displayNumFmt == null
        ? s.styles[cell.style ?? 0]?.shortDate
        : cell.shortDate;
    if (!pattern || (cell.type && cell.type !== 'n') || !NUMERIC_RE.test(cell.raw)) {
      return text;
    }
    const parts = /^(\d{4})-(\d\d)-(\d\d)(.*)$/.exec(text);
    if (!parts?.[1] || !parts[2] || !parts[3]) return text;
    const year = parts[1];
    const month = parts[2];
    const day = parts[3];
    const suffix = parts[4] ?? '';
    const fields: Readonly<Record<string, string>> = {
      y: year.slice(-2),
      yy: year.slice(-2),
      yyyy: year,
      m: String(Number(month)),
      mm: month,
      d: String(Number(day)),
      dd: day,
    };
    return pattern.replace(/y+|m+|d+/g, (t) => fields[t] ?? t) + suffix;
  }

  style(cell: Partial<CellData> | null | undefined, rowStyle = 0): VisualStyle | null {
    const s = this.o.state;
    return conditionalVisual(cell, s.styles, rowStyle, s.showStyles);
  }

  create(
    cell: CellData | null | undefined,
    row: Partial<RowData> | null | undefined,
    rowNumber: number,
    col: number,
  ): GridCellElement {
    const s = this.o.state;
    const filter = s.autoFilter;
    const isHeaderCell = filter !== null && rowNumber === filter.row && col >= filter.startCol && col <= filter.endCol;
    const ordinary =
      !cell?.richText &&
      !cell?.conditional &&
      !cell?.hyperlink &&
      !row?.presentationNotice &&
      !isHeaderCell;
    const isNumericLike =
      cell?.type === 'n' ||
      (!cell?.type && Boolean(cell?.raw) && Number.isFinite(Number(cell?.raw)));
    const key = ordinary
      ? `${this.paintKey()}|${cell?.style ?? row?.style ?? 0}|${cell ? 1 : 0}|${cell?.hasFormula ? 1 : 0}|${isNumericLike ? 1 : 0}|${cell?.valueColor ?? ''}`
      : null;

    if (this.templatePalette !== s.styles) {
      this.templates.clear();
      this.templatePalette = s.styles;
    }
    const saved = key ? this.templates.get(key) : undefined;
    if (saved) {
      const node = saved.node.cloneNode(false) as GridCellElement;
      node._xlPaintKey = saved.paintKey;
      node._xlRowStyle = row?.style ?? 0;
      node._xlModel = cell ?? null;
      node._xlRichAllowed = true;
      node._xlRowPresentationNotice = '';
      node._xlHyperlinkVisited = false;
      node.textContent = this.value(cell);
      return node;
    }

    const node = document.createElement('div') as GridCellElement;
    node.className = 'grid-cell';
    node.dataset.row = String(rowNumber);
    node.dataset.col = String(col);
    this.paint(node, cell, row);
    if (key && !node.childElementCount && !node.title && node._xlRichAllowed) {
      const template = node.cloneNode(false) as GridCellElement;
      if (this.templates.size >= 384) this.templates.clear();
      this.templates.set(key, { node: template, paintKey: node._xlPaintKey });
    }
    return node;
  }

  paint(
    node: GridCellElement,
    cell: CellData | null | undefined,
    row?: Partial<RowData> | null,
  ): void {
    const fresh = node._xlPaintKey === undefined;
    if (node._xlNumericOriginal) node.title = node._xlNumericOriginalTitle ?? '';
    node._xlNumericOriginal = null;
    node._xlNumericSpan = null;
    const s = this.o.state;
    node._xlModel = cell ?? null;
    node._xlRowStyle = row?.style ?? 0;
    node._xlPaintKey = this.paintKey();
    node._xlRichAllowed = this.allowed(cell);
    node._xlRowPresentationNotice = row?.presentationNotice ?? '';
    if (node._xlRichNotice) {
      node.title = '';
      node._xlRichNotice = '';
    }
    if (node._xlPresentationNotice) {
      node.title = '';
      node._xlPresentationNotice = '';
    }
    node._xlRichNode = null;
    if (!fresh) node.classList.remove('rich-text-cell', 'rich-text-notice');
    if (node._xlOverflowSpan) {
      node._xlOverflowSpan = null;
      node.classList.remove('cell-overflow');
      node.style.removeProperty('--overflow-width');
    }
    if (!fresh) {
      node.classList.remove(
        'formula-cell',
        'hyperlink-cell',
        'hyperlink-visited',
        'formula-view-cell',
        'auto-filter-header',
      );
    }
    const visited = this.hyperlinkVisited(node, cell);
    node._xlHyperlinkVisited = visited;
    node.textContent = this.value(cell);
    if (cell) {
      const styled = cell.style == null && row?.style ? { ...cell, style: row.style } : cell;
      this.o.applyStyle(node, styled);
      if (cell.hasFormula) node.classList.add('formula-cell');
      if (cell.hyperlink) {
        node.classList.add('hyperlink-cell');
        if (visited) node.classList.add('hyperlink-visited');
      }
      if (s.showFormulas && cell.hasFormula) node.classList.add('formula-view-cell');
    } else if (row?.style) {
      this.o.applyStyle(node, { style: row.style });
    }
    const base = this.style(cell, row?.style) ?? {};
    if (
      s.showStyles &&
      (cell?.richText || conditionalNeedsInline(cell, base)) &&
      !(s.showFormulas && cell?.hasFormula)
    ) {
      paintRichText(node, cell, base, {
        limits: this.o.richLimits,
        allowed: this.allowed(cell),
        fontOverride: s.fontOverride,
        fontStack: this.o.fontStack,
        scale: this.o.scale(),
      });
    }
    if (
      !fresh ||
      cell?.conditional ||
      row?.presentationNotice ||
      (base.underlineStyle ?? '').endsWith('Accounting')
    ) {
      applyConditionalNotice(node, cell, row, s.showStyles, base);
    }
    const r = Number(node.dataset.row);
    const c = Number(node.dataset.col);
    const filter = s.autoFilter;
    if (filter !== null && r === filter.row && c >= filter.startCol && c <= filter.endCol) {
      node.classList.add('auto-filter-header');
      const button = document.createElement('button');
      button.type = 'button';
      button.className = `auto-filter-button${filter.rules.has(c) ? ' is-filtered' : ''}`;
      button.dataset.column = String(c);
      button.textContent = '▾';
      button.title = localize('ui.cellView.columnFilterMenu', { colName: this.o.colName(c) });
      button.setAttribute('aria-label', button.title);
      button.disabled = !s.indexReady;
      node.appendChild(button);
    }
  }

  private ensureMeasure(): CanvasRenderingContext2D | null {
    if (!this.measure && typeof document !== 'undefined') {
      this.measure = document.createElement('canvas').getContext('2d');
    }
    return this.measure;
  }

  private measureFont(ctx: CanvasRenderingContext2D, font: string): void {
    if (this.currentFont !== font) {
      ctx.font = font;
      this.currentFont = font;
    }
  }

  spillWidth(
    cell: CellData | null | undefined,
    values: Readonly<Record<number, CellData | undefined>> | null | undefined,
    layouts: readonly ColumnLayoutItem[],
    position: number,
    rowStyle = 0,
    row = 0,
  ): number {
    const s = this.o.state;
    if (!cell || !isTextCell(cell) || s.showFormulas) return 0;
    const text = cellDisplayValue(cell, false, s.showStyles);
    if (!text || /[\r\n]/.test(text)) return 0;
    const style = this.style(cell, rowStyle);
    const hAlign = style?.horizontal ?? '';
    if (
      style?.wrap ||
      ['center', 'right', 'justify', 'distributed', 'centerContinuous', 'fill'].includes(hAlign)
    ) {
      return 0;
    }
    const current = layouts[position];
    if (!current || !(current.width > 0)) return 0;
    const ctx = this.ensureMeasure();
    if (!ctx) return 0;
    const vertical = cell.conditional?.style?.font.vertical || style?.fontVertical || '';
    const raised = vertical === 'superscript' || vertical === 'subscript';
    const font =
      (style?.italic ? 'italic ' : '') +
      (style?.bold ? '700 ' : '') +
      `${(((style?.fontSize || 8.25) * (raised ? 0.65 : 1) * 4) / 3) * this.o.scale()}px ` +
      this.o.fontStack(s.fontOverride || style?.fontName || 'Segoe UI');

    const prefix = text.slice(0, 2048);
    const key = `${font}\0${prefix}\0${text.length}`;
    let needed = this.widths.get(key);
    const rich = s.showStyles && this.allowed(cell) ? inspectRichText(cell, this.o.richLimits) : null;
    if (rich?.runs.length && cell.richText) {
      const overlay = conditionalFont(cell);
      const fontKey = `${font}\0${JSON.stringify(overlay)}`;
      const cached = this.richWidths.get(cell.richText);
      if (cached?.font === fontKey && cached.text === text) {
        needed = cached.width;
      } else {
        needed = text.length > 2048 ? Infinity : 14;
        if (Number.isFinite(needed)) {
          let totalWidth = needed;
          eachRichPart(rich, (part, rawRun) => {
            const run = overlayRichFont(rawRun, overlay);
            const runVertical = run.vertical || style?.fontVertical || '';
            const runRaised = runVertical === 'superscript' || runVertical === 'subscript';
            const size = (run.size || style?.fontSize || 8.25) * (runRaised ? 0.65 : 1);
            this.measureFont(
              ctx,
              ((run.italic ?? style?.italic) ? 'italic ' : '') +
                ((run.bold ?? style?.bold) ? '700 ' : '') +
                `${((size * 4) / 3) * this.o.scale()}px ` +
                this.o.fontStack(s.fontOverride || run.name || style?.fontName || 'Segoe UI'),
            );
            totalWidth += ctx.measureText(part).width;
          });
          needed = totalWidth;
        }
        this.richWidths.set(cell.richText, { font: fontKey, text, width: needed });
      }
    } else if (needed === undefined) {
      this.measureFont(ctx, font);
      needed = text.length > 2048 ? Infinity : ctx.measureText(prefix).width + 14;
      this.widths.set(key, needed);
      if (this.widths.size > 2048) {
        const oldest = this.widths.keys().next().value;
        if (oldest !== undefined) this.widths.delete(oldest);
      }
    }
    if (needed <= current.width - 2) return 0;
    let available = current.width;
    for (let i = position + 1; i < layouts.length; i++) {
      const nextLayout = layouts[i];
      if (!nextLayout) break;
      if (isCellOccupied(values?.[nextLayout.col]) || (row && this.o.merge?.(row, nextLayout.col))) {
        break;
      }
      available += nextLayout.width;
      if (available >= needed) break;
    }
    return available > current.width + 1 ? Math.max(current.width, Math.min(available - 6, needed)) : 0;
  }

  numberMetrics(cell: CellData | null | undefined, rowStyle = 0): NumberMeasureCache | null {
    const s = this.o.state;
    if (
      !cell ||
      cell.cacheMissing ||
      (s.showFormulas && cell.hasFormula) ||
      !(((!cell.type || cell.type === 'n') && NUMERIC_RE.test(cell.raw.trim())) || cell.type === 'd')
    ) {
      return null;
    }
    const text = this.value(cell);
    const style = this.style(cell, rowStyle) ?? {};
    if (!text || style.wrap) return null;
    const ctx = this.ensureMeasure();
    if (!ctx) return null;
    const vertical = cell.conditional?.style?.font.vertical || style.fontVertical || '';
    const raised = vertical === 'superscript' || vertical === 'subscript';
    const font =
      (style.italic ? 'italic ' : '') +
      (style.bold ? '700 ' : '') +
      `${(((style.fontSize || 8.25) * (raised ? 0.65 : 1) * 4) / 3) * this.o.scale()}px ` +
      this.o.fontStack(s.fontOverride || style.fontName || 'Segoe UI');

    let measured = this.numberWidths.get(cell);
    if (measured?.text !== text || measured.font !== font) {
      const key = text.length <= 256 ? `${font}\0${text}` : null;
      measured = key ? this.numberMeasures.get(key) : undefined;
      if (!measured) {
        this.measureFont(ctx, font);
        let hash = this.hashWidths.get(font);
        if (hash === undefined) {
          hash = ctx.measureText('#').width;
          if (this.hashWidths.size >= 128) this.hashWidths.clear();
          this.hashWidths.set(font, hash);
        }
        measured = { text, font, width: ctx.measureText(text).width, hash };
        if (key) {
          if (this.numberMeasures.size >= 512) {
            const oldest = this.numberMeasures.keys().next().value;
            if (oldest !== undefined) this.numberMeasures.delete(oldest);
          }
          this.numberMeasures.set(key, measured);
        }
      }
      this.numberWidths.set(cell, measured);
    }
    return measured;
  }

  fitNumber(
    node: GridCellElement,
    cell: CellData | null | undefined,
    width: number,
    paddingOverride?: number,
  ): boolean {
    const restore = (): boolean => {
      if (node._xlNumericSpan) {
        if (node._xlNumericOriginal) node._xlNumericSpan.replaceWith(node._xlNumericOriginal);
        node.title = node._xlNumericOriginalTitle ?? '';
        node._xlNumericOriginal = null;
        node._xlNumericSpan = null;
      }
      return false;
    };
    const measured = this.numberMetrics(cell, node._xlRowStyle);
    if (!measured || !cell) return restore();
    const { text, font } = measured;
    const s = this.o.state;
    const padding =
      paddingOverride ??
      (this.o.numericPadding
        ? this.o.numericPadding(node)
        : node.classList.contains('auto-filter-header')
          ? 32
          : 14);
    const available = Math.max(0, width - padding);
    if (!(width > 0) || !Number.isFinite(measured.width) || measured.width <= available + 0.25) {
      return restore();
    }
    if (!node._xlNumericSpan) {
      const original =
        node._xlRichNode ?? [...node.childNodes].find((n) => n.nodeType === 3) ?? null;
      if (!original) return false;
      const span = document.createElement('span');
      span.className = 'cell-number-overflow';
      node._xlNumericOriginal = original;
      node._xlNumericOriginalTitle = node.title || '';
      node._xlNumericSpan = span;
      original.replaceWith(span);
    }
    let fitted = '';
    const ctx = this.ensureMeasure();
    if (
      ctx &&
      text === cell.raw &&
      !cell.conditional?.display &&
      !(cell.displayNumFmt ?? s.styles[cell.style ?? 0]?.numFmtId)
    ) {
      if (measured.fitWidth !== available) {
        this.measureFont(ctx, font);
        measured.fitted = fitGeneralNumber(
          text,
          (candidate) => ctx.measureText(candidate).width <= available + 0.25,
        );
        measured.fitWidth = available;
      }
      fitted = measured.fitted ?? '';
    }
    node._xlNumericSpan.textContent =
      fitted || '#'.repeat(Math.min(128, Math.max(1, Math.floor(available / Math.max(1, measured.hash)))));
    node.title = [node._xlNumericOriginalTitle, localize('ui.cellView.insufficientWidthFullDisplayValue', { text })]
      .filter(Boolean)
      .join('；');
    return true;
  }

  overflow(
    node: GridCellElement,
    cell: CellData | null | undefined,
    values: Readonly<Record<number, CellData | undefined>> | null | undefined,
    layouts: readonly ColumnLayoutItem[],
    position: number,
  ): void {
    if (this.fitNumber(node, cell, parseFloat(node.style.width) || layouts[position]?.width || 0)) {
      return;
    }
    const merged = this.o.merge?.(Number(node.dataset.row), Number(node.dataset.col));
    const width = merged
      ? 0
      : this.spillWidth(cell, values, layouts, position, node._xlRowStyle, Number(node.dataset.row));
    const previous = node._xlOverflowSpan;
    if (!width) {
      if (previous) {
        previous.replaceWith(node._xlRichNode ?? document.createTextNode(this.value(cell)));
        node._xlOverflowSpan = null;
        node.classList.remove('cell-overflow');
        node.style.removeProperty('--overflow-width');
      }
      return;
    }
    if (!previous) {
      const span = document.createElement('span');
      span.className = 'cell-overflow-content';
      const old = node._xlRichNode ?? [...node.childNodes].find((n) => n.nodeType === 3) ?? null;
      if (old) {
        old.replaceWith(span);
        span.appendChild(old);
      } else {
        span.textContent = this.value(cell);
        node.prepend(span);
      }
      node._xlOverflowSpan = span;
    }
    node.classList.add('cell-overflow');
    const css = `${width}px`;
    if (node.style.getPropertyValue('--overflow-width') !== css) {
      node.style.setProperty('--overflow-width', css);
    }
  }
}

const CELL_TYPE_LABELS = new Map<string, string>([
  ['s', localize('ui.common.text')],
  ['str', localize('ui.common.text')],
  ['inlineStr', localize('ui.common.text')],
  ['text', localize('ui.common.text')],
  ['string', localize('ui.common.text')],
  ['b', localize('ui.cellView.boolean')],
  ['e', localize('ui.common.errors')],
  ['d', localize('ui.common.date')],
  ['n', localize('ui.common.numericalValues')],
]);

const ODS_TYPE_LABELS = new Map<string, string>([
  ['float', localize('ui.common.numericalValues')],
  ['currency', localize('ui.cellView.currency')],
  ['percentage', localize('ui.cellView.percentage')],
  ['date', localize('ui.common.date')],
  ['time', localize('ui.common.time')],
  ['boolean', localize('ui.cellView.boolean')],
  ['string', localize('ui.common.text')],
]);

export function cellSourceTypeLabel(cell: CellData): string {
  if (cell.cacheMissing) return localize('ui.cellView.formulasHaveNoSavedResults');
  if (cell.presentationOnly) return localize('ui.cellView.sourceAsBlank');
  if (cell.sourceType) {
    const label = ODS_TYPE_LABELS.get(cell.sourceType) ?? cell.sourceType;
    return `ODS · ${label}${cell.currency ? ` ${cell.currency}` : ''}`;
  }
  if (cell.raw === '' && !cell.hasFormula) return localize('ui.common.blank');
  if (cell.type) return CELL_TYPE_LABELS.get(cell.type) ?? localize('ui.cellView.sourceType', { type: cell.type });
  return localize('ui.common.numericalValues');
}

export function cellFormulaText(cell: CellData): string | null {
  return cell.hasFormula && typeof cell.formula === 'string' && cell.formula.startsWith('=') && cell.formula.length > 1
    ? cell.formula
    : null;
}

export const CellView = Object.freeze({
  value: cellDisplayValue,
  occupied: isCellOccupied,
  isText: isTextCell,
  Presenter: CellPresenter,
  selectionFill,
  sourceType: cellSourceTypeLabel,
  formula: cellFormulaText,
});
