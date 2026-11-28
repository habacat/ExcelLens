/**
 * Validated UTF-16 rich-text span inspection, CSS style synthesis, DOM painting,
 * and HTML export serialization. Source text remains immutable.
 */
import { t as localize } from '../core/i18n.ts';
import type { RichTextLimits } from '../contracts/formats.ts';
import type {
  CellData,
  RichFont,
  RichText,
  RichTextRun,
  VisualStyle,
} from '../contracts/workbook.ts';
import { escapeHtml } from '../core/display-format.ts';

const own = (value: object, key: string): boolean =>
  Object.prototype.hasOwnProperty.call(value, key);

const isHexColor = (value: string | undefined): boolean => /^#[\da-f]{6}$/i.test(value ?? '');

const UNDERLINES = new Set(['', 'none', 'single', 'double', 'singleAccounting', 'doubleAccounting']);
const VERTICALS = new Set(['', 'baseline', 'superscript', 'subscript']);
const FONT_KEYS = [
  'name',
  'size',
  'color',
  'autoColor',
  'bold',
  'italic',
  'strike',
  'underline',
  'vertical',
] as const;

export interface RichTextInfo {
  readonly text: string;
  readonly runs: readonly RichTextRun[];
  readonly notice: string;
}

export interface RichTextOptions {
  readonly limits?: RichTextLimits | null;
  readonly allowed?: boolean;
  readonly fontOverride?: string;
  readonly fontStack?: (name: string) => string;
  readonly scale?: number;
  readonly dom?: boolean;
  readonly fontOverlay?: RichFont;
}

export interface RichTextHtmlResult {
  readonly html: string;
  readonly notice: string;
  readonly rich: boolean;
}

export interface RichTextCellMeta {
  _xlRichNotice?: string;
  _xlRichNode?: HTMLSpanElement | null;
}

export type RichTextCellElement = HTMLElement & RichTextCellMeta;

export function overlayRichFont(font: RichFont, overlay?: RichFont | null): RichFont {
  if (!overlay || !Object.keys(overlay).length) return font;
  const out: RichFont = { ...font };
  if (overlay.name !== undefined && overlay.name !== '') out.name = overlay.name;
  if (overlay.size !== undefined && overlay.size !== 0) out.size = overlay.size;
  if (overlay.color !== undefined && overlay.color !== '') out.color = overlay.color;
  if (overlay.autoColor !== undefined) out.autoColor = overlay.autoColor;
  if (overlay.bold !== undefined) out.bold = overlay.bold;
  if (overlay.italic !== undefined) out.italic = overlay.italic;
  if (overlay.strike !== undefined) out.strike = overlay.strike;
  if (overlay.underline !== undefined && overlay.underline !== '') out.underline = overlay.underline;
  if (overlay.vertical !== undefined && overlay.vertical !== '') out.vertical = overlay.vertical;
  if (overlay.autoColor) out.color = '';
  if (overlay.color) out.autoColor = false;
  return out;
}

function hasForbiddenFontChar(name: string): boolean {
  for (let i = 0; i < name.length; i++) {
    const code = name.charCodeAt(i);
    if (code === 0 || code === 10 || code === 13) return true;
  }
  return false;
}

function quoteFont(name: string): string {
  let out = '"';
  for (let i = 0; i < name.length; i++) {
    const ch = name[i] ?? '';
    const code = name.charCodeAt(i);
    if (ch === '\\' || ch === '"') {
      out += `\\${ch}`;
    } else if (code <= 0x1f || code === 0x7f || ch === '<' || ch === '>' || ch === '&' || ch === ';') {
      out += `\\${code.toString(16)} `;
    } else {
      out += ch;
    }
  }
  return `${out}"`;
}

function isValidFont(font: unknown): font is RichFont {
  if (!font || typeof font !== 'object' || Array.isArray(font)) return false;
  const rec = font as Record<string, unknown>;
  const allowedKeys: readonly string[] = FONT_KEYS;
  if (Object.keys(rec).some((key) => !allowedKeys.includes(key))) return false;
  if (rec.name !== undefined) {
    if (
      typeof rec.name !== 'string' ||
      rec.name.length > 160 ||
      Array.from(rec.name).length > 80 ||
      hasForbiddenFontChar(rec.name)
    ) {
      return false;
    }
  }
  if (rec.size !== undefined && rec.size !== 0) {
    if (typeof rec.size !== 'number' || !Number.isFinite(rec.size) || rec.size < 1 || rec.size > 409) {
      return false;
    }
  }
  if (rec.color !== undefined && rec.color !== '') {
    if (typeof rec.color !== 'string' || !isHexColor(rec.color)) return false;
  }
  if (rec.autoColor !== undefined && typeof rec.autoColor !== 'boolean') return false;
  for (const key of ['bold', 'italic', 'strike'] as const) {
    if (own(rec, key) && typeof rec[key] !== 'boolean') return false;
  }
  const underline = rec.underline === undefined ? '' : rec.underline;
  const vertical = rec.vertical === undefined ? '' : rec.vertical;
  return (
    typeof underline === 'string' &&
    UNDERLINES.has(underline) &&
    typeof vertical === 'string' &&
    VERTICALS.has(vertical)
  );
}

function isSafeUtf16Boundary(text: string, end: number): boolean {
  if (end <= 0 || end >= text.length) return true;
  const high = text.charCodeAt(end - 1);
  const low = text.charCodeAt(end);
  return !(high >= 0xd800 && high <= 0xdbff && low >= 0xdc00 && low <= 0xdfff);
}

export interface RichTextCellLike {
  readonly raw?: string;
  readonly display?: string;
  readonly richText?: RichText;
}

export function inspectRichText(
  cell: RichTextCellLike | null | undefined,
  limits?: RichTextLimits | null,
): RichTextInfo | null {
  const rich = cell?.richText;
  const text = cell?.display ?? cell?.raw ?? '';
  if (!rich) return null;
  const invalid = (message: string): RichTextInfo => ({ text, runs: [], notice: message });
  if (text !== (cell.raw ?? '')) return invalid(localize('ui.richtext.displayedValueDiffersFromOriginal'));
  if (!limits || !Number.isSafeInteger(limits.cellRuns) || limits.cellRuns < 1) {
    return invalid(localize('ui.richtext.richTextDisplayCapacityUnavailable'));
  }
  const runs: readonly RichTextRun[] = rich.runs ?? [];
  const notice = rich.notice ?? '';
  if (runs.length > limits.cellRuns || notice.length > 1024) {
    return invalid(localize('ui.richtext.richTextStyleInvalidOr'));
  }
  let previous = 0;
  for (const run of runs) {
    if (
      !Number.isSafeInteger(run.end) ||
      run.end <= previous ||
      run.end > text.length ||
      !isSafeUtf16Boundary(text, run.end) ||
      !isValidFont(run.font)
    ) {
      return invalid(localize('ui.richtext.richTextStyleInvalidOriginal'));
    }
    previous = run.end;
  }
  if (!notice && previous !== text.length) {
    return invalid(localize('ui.richtext.richTextStyleDoesNot'));
  }
  const extra = runs.some((run) => (run.font.underline ?? '').endsWith('Accounting'))
    ? localize('ui.richtext.accountingUnderlineDisplayedAsSegments')
    : '';
  return { text, runs, notice: [notice, extra].filter(Boolean).join('；') };
}

export function richFontProperties(
  rawFont: RichFont,
  base: VisualStyle = {},
  options: RichTextOptions = {},
): Record<string, string> {
  let font = overlayRichFont(rawFont, options.fontOverlay);
  if (!font.vertical && base.fontVertical) font = { ...font, vertical: base.fontVertical };
  const p: Record<string, string> = {};
  const name = options.fontOverride || font.name;
  if (name) p['font-family'] = options.fontStack ? options.fontStack(name) : quoteFont(name);
  if (own(font, 'bold')) p['font-weight'] = font.bold ? '700' : '400';
  if (own(font, 'italic')) p['font-style'] = font.italic ? 'italic' : 'normal';
  const u = font.underline || base.underlineStyle || (base.underline ? 'single' : 'none');
  const strike = font.strike ?? Boolean(base.strike);
  p['text-decoration-line'] =
    [u !== 'none' && 'underline', strike && 'line-through'].filter(Boolean).join(' ') || 'none';
  if (u === 'double' || u === 'doubleAccounting') p['text-decoration-style'] = 'double';
  if (u.endsWith('Accounting')) p['text-underline-position'] = 'under';
  if (font.color) p.color = font.color;
  else if (font.autoColor) p.color = options.dom ? 'var(--rich-auto-color,#344454)' : '#000000';
  if (options.dom && p.color) {
    p['--rich-color'] = p.color;
    p.color = 'var(--rich-ink,var(--rich-color))';
  }
  const raised = font.vertical === 'superscript' || font.vertical === 'subscript';
  if (font.size || raised) {
    const size = (font.size || base.fontSize || 8.25) * (raised ? 0.65 : 1);
    p['font-size'] = options.dom ? `${((size * 4) / 3) * (options.scale || 1)}px` : `${size}pt`;
  }
  if (font.vertical) {
    p['vertical-align'] = raised
      ? font.vertical === 'superscript'
        ? 'super'
        : 'sub'
      : 'baseline';
  }
  return p;
}

export function eachRichPart(
  info: RichTextInfo,
  visit: (text: string, font: RichFont) => void,
): void {
  let start = 0;
  for (const run of info.runs) {
    visit(info.text.slice(start, run.end), run.font);
    start = run.end;
  }
  if (start < info.text.length) visit(info.text.slice(start), {});
}

function presentationInfo(
  cell: Pick<CellData, 'raw' | 'display' | 'richText' | 'conditional'> | null | undefined,
  options: RichTextOptions,
  base: VisualStyle = {},
): RichTextInfo | null {
  const display = cell?.conditional?.display;
  const source = cell && display != null ? { ...cell, display } : cell;
  let info = inspectRichText(source, options.limits);
  const vertical = options.fontOverlay?.vertical || base.fontVertical || '';
  if (!info && (vertical === 'superscript' || vertical === 'subscript')) {
    const text = source?.display ?? source?.raw ?? '';
    info = { text, runs: text ? [{ end: text.length, font: {} }] : [], notice: '' };
  }
  return info;
}

export function paintRichText(
  node: RichTextCellElement,
  cell: Pick<CellData, 'raw' | 'display' | 'richText' | 'conditional'> | null | undefined,
  base: VisualStyle = {},
  rawOptions: RichTextOptions = {},
): boolean {
  const options: RichTextOptions = {
    ...rawOptions,
    fontOverlay: cell?.conditional?.style?.font,
  };
  const info = presentationInfo(cell, options, base);
  if (!info) return false;
  const notice =
    options.allowed === false ? localize('ui.richtext.richTextStyleExceedsCurrent') : info.notice;
  if (notice) {
    node.classList.add('rich-text-notice');
    node.title = notice;
    node._xlRichNotice = notice;
  }
  if (!info.runs.length || options.allowed === false) return false;
  const root = document.createElement('span');
  root.className = 'cell-rich-text';
  eachRichPart(info, (text, font) => {
    const span = document.createElement('span');
    span.textContent = text;
    for (const [key, value] of Object.entries(
      richFontProperties(font, base, { ...options, dom: true }),
    )) {
      span.style.setProperty(key, value);
    }
    root.appendChild(span);
  });
  node.textContent = '';
  node.appendChild(root);
  node.classList.add('rich-text-cell');
  node.style.textDecoration = 'none';
  node._xlRichNode = root;
  return true;
}

export function richTextHtml(
  cell: Pick<CellData, 'raw' | 'display' | 'richText' | 'conditional'> | null | undefined,
  base: VisualStyle = {},
  rawOptions: RichTextOptions = {},
  encoded: (text: string) => string = (text) => escapeHtml(text).replace(/\r\n?|\n/g, '<br>'),
): RichTextHtmlResult | null {
  const options: RichTextOptions = {
    ...rawOptions,
    fontOverlay: cell?.conditional?.style?.font,
  };
  const info = presentationInfo(cell, options, base);
  if (!info) return null;
  if (!info.runs.length) return { html: encoded(info.text), notice: info.notice, rich: false };
  const parts: string[] = [];
  eachRichPart(info, (text, font) => {
    const css = Object.entries(richFontProperties(font, base, options))
      .map(([k, v]) => `${k}:${v}`)
      .join(';');
    parts.push(`<span style="${escapeHtml(css)}">${encoded(text)}</span>`);
  });
  return { html: parts.join(''), notice: info.notice, rich: true };
}

export function estimateRichTextBytes(value: RichText | null | undefined): number {
  if (!value) return 0;
  let bytes = 96 + 2 * (value.notice?.length ?? 0);
  for (const run of value.runs ?? []) {
    bytes += 288;
    const f = run.font;
    if (f.name) bytes += 2 * f.name.length;
    if (f.color) bytes += 2 * f.color.length;
    if (f.underline) bytes += 2 * f.underline.length;
    if (f.vertical) bytes += 2 * f.vertical.length;
  }
  return bytes;
}

export function sameRichText(
  a: RichText | null | undefined,
  b: RichText | null | undefined,
): boolean {
  if (a === b) return true;
  if (!a || !b) return false;
  const runsA = a.runs ?? [];
  const runsB = b.runs ?? [];
  if (a.notice !== b.notice || runsA.length !== runsB.length) return false;
  return runsA.every((run, i) => {
    const other = runsB[i];
    if (other?.end !== run.end) return false;
    return FONT_KEYS.every((key) => run.font[key] === other.font[key]);
  });
}

export const RichTextModel = Object.freeze({
  inspect: inspectRichText,
  properties: richFontProperties,
  eachPart: eachRichPart,
  paint: paintRichText,
  html: richTextHtml,
  estimate: estimateRichTextBytes,
  same: sameRichText,
  overlayFont: overlayRichFont,
});
