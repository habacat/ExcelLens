/**
 * Conditional formatting presentation helpers and serialized stats request queue.
 * Derived presentation never replaces source values, source types, or style IDs.
 */
import { t as localize } from '../core/i18n.ts';
import type {
  CellData,
  ConditionalPresentation,
  ConditionalStatsStartResponse,
  RichFont,
  RowData,
  VisualStyle,
} from '../contracts/workbook.ts';

export interface PresentationNoticeMeta {
  _xlRichNotice?: string;
  _xlPresentationNotice?: string;
}

export type PresentationNoticeElement = HTMLElement & PresentationNoticeMeta;

export interface ConditionalCellLike {
  readonly hasFormula?: boolean;
  readonly formula?: string;
  readonly conditional?: ConditionalPresentation;
  readonly display?: string;
  readonly raw?: string;
  readonly style?: number;
  readonly valueColor?: string;
}

export function conditionalValue(
  cell: ConditionalCellLike | null | undefined,
  formulas = false,
  styles = true,
): string {
  if (!cell) return '';
  if (formulas && cell.hasFormula && cell.formula) return cell.formula;
  if (styles && cell.conditional?.display != null) return cell.conditional.display;
  return cell.display ?? cell.raw ?? '';
}

export function conditionalVisual(
  cell: Pick<ConditionalCellLike, 'conditional' | 'style'> | null | undefined,
  palette: readonly VisualStyle[] | null | undefined,
  rowStyle = 0,
  enabled = true,
): VisualStyle | null {
  if (!enabled) return null;
  if (cell?.conditional?.visual) return cell.conditional.visual;
  const idx = (cell?.style ?? rowStyle) || 0;
  return palette?.[idx] ?? {};
}

export function conditionalColor(
  cell: Pick<CellData, 'conditional' | 'valueColor'> | null | undefined,
  style: VisualStyle | null | undefined,
): string {
  if (cell?.conditional?.valueColor != null) {
    return cell.conditional.valueColor || style?.color || '';
  }
  return cell?.valueColor || style?.color || '';
}

export function conditionalFont(
  cell: Pick<CellData, 'conditional'> | null | undefined,
): RichFont {
  return cell?.conditional?.style?.font ?? {};
}

export function conditionalNeedsInline(
  cell: Pick<CellData, 'conditional'> | null | undefined,
  base: VisualStyle | null | undefined = {},
): boolean {
  const vertical = conditionalFont(cell).vertical || base?.fontVertical || '';
  return vertical === 'superscript' || vertical === 'subscript';
}

export function applyConditionalNotice(
  node: PresentationNoticeElement,
  cell: Pick<CellData, 'conditional'> | null | undefined,
  row: Pick<RowData, 'presentationNotice'> | null | undefined,
  enabled: boolean,
  base: VisualStyle | null | undefined = {},
): void {
  const notices: string[] = [];
  const pending = Boolean(enabled && cell?.conditional?.pending);
  if (enabled) {
    if (cell?.conditional?.notice) notices.push(cell.conditional.notice);
    if (row?.presentationNotice) notices.push(row.presentationNotice);
    if (cell?.conditional?.style?.rotation) notices.push(localize('ui.conditional.conditionalFormattingTextRotationNot'));
    if ((conditionalFont(cell).underline || base?.underlineStyle || '').endsWith('Accounting')) {
      notices.push(localize('ui.conditional.accountingUnderlineDisplayedAsText'));
    }
  }
  const text = [...new Set(notices)].join('；');
  node._xlPresentationNotice = text;
  if (pending) node.classList.add('cell-conditional-pending');
  else node.classList.remove('cell-conditional-pending');
  if (text) {
    node.classList.add('cell-presentation-notice');
    node.title = [node._xlRichNotice, text].filter(Boolean).join('；');
  } else {
    node.classList.remove('cell-presentation-notice');
  }
}

export function sameConditional(
  a: ConditionalPresentation | null | undefined,
  b: ConditionalPresentation | null | undefined,
): boolean {
  return a === b || (Boolean(a) && Boolean(b) && JSON.stringify(a) === JSON.stringify(b));
}

export function estimateConditionalBytes(p: ConditionalPresentation | null | undefined): number {
  return p ? 512 + 4 * JSON.stringify(p).length : 0;
}

export interface ConditionalStatsRequestQueue {
  start(sheet: string): Promise<ConditionalStatsStartResponse>;
  cancel(id?: string | null): Promise<void>;
}

export function createStatsRequests(
  start: (sheet: string) => Promise<ConditionalStatsStartResponse>,
  cancel: (id: string) => Promise<unknown>,
): ConditionalStatsRequestQueue {
  let tail: Promise<unknown> = Promise.resolve();
  let started: Promise<ConditionalStatsStartResponse> | null = null;

  function enqueue<T>(action: () => Promise<T>): Promise<T> {
    const result = tail.then(action);
    tail = result.catch(() => {
      // Keep the serial queue alive after an earlier error.
    });
    return result;
  }

  return {
    start(sheet: string): Promise<ConditionalStatsStartResponse> {
      started = enqueue(() => start(sheet));
      return started;
    },
    cancel(id?: string | null): Promise<void> {
      const prior = started;
      started = null;
      return enqueue(async () => {
        const target = id || (await prior?.catch(() => null))?.jobId;
        if (target) await cancel(target);
      });
    },
  };
}

export const ConditionalModel = Object.freeze({
  value: conditionalValue,
  visual: conditionalVisual,
  color: conditionalColor,
  font: conditionalFont,
  needsInline: conditionalNeedsInline,
  notice: applyConditionalNotice,
  same: sameConditional,
  estimate: estimateConditionalBytes,
  createStatsRequests,
});
