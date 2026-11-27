/**
 * Portable clipboard markup codec (`data-excellens-range`), external HTML table
 * detection, SHA-256 native paste lookup, filtered/hidden-row paste destination
 * resolution, and portable range paste execution.
 */
import type { ActivityHandle } from '../contracts/common.ts';
import type {
  EditAddress,
  EditChange,
  EditClipboard,
  EditSheetMeta,
  EditStructure,
  PortableClipboard,
} from '../contracts/edit.ts';
import type { EditLimits } from '../contracts/formats.ts';
import { requireButton, requireDialog } from '../core/dom.ts';
import type { EditCopyClipboardSnapshot } from '../grid/grid-clipboard.ts';
import {
  decodeEditBatchPreviewResponse,
  decodeOptionalPortableClipboard,
} from '../core/http-decoders.generated.ts';

/** @httpResponse OptionalPortableClipboard */
export type OptionalPortableClipboard = PortableClipboard | null;

export interface GridPasteRect {
  readonly top: number;
  readonly bottom: number;
  readonly left: number;
  readonly right: number;
}

export interface GridPasteTarget {
  readonly document: { readonly id: string; readonly binding: string };
  readonly revision: string;
  readonly sheet: string;
  readonly selection?: string;
}

export interface EditCopiedSnapshot extends EditCopyClipboardSnapshot {
  readonly merged?: boolean;
  readonly first?: EditAddress;
  readonly last?: EditAddress;
  readonly sheet?: string;
  readonly start?: string;
  readonly end?: string;
  readonly id?: string;
  readonly binding?: string;
  readonly revision?: string;
  structural?: boolean;
  acceptedRevision?: string;
}

export type IdentifiedClipboard =
  | EditCopiedSnapshot
  | { readonly portable: PortableClipboard }
  | { readonly external: true };

export interface GridPasteOwnerHost {
  readonly limits: EditLimits;
  colName(col: number): string;
  post(command: string, body: unknown, signal?: AbortSignal): Promise<unknown>;
  commit(
    action: 'change' | 'structure',
    changes: readonly EditChange[] | undefined,
    options?: {
      readonly proof?: string;
      readonly direct?: boolean;
      readonly expandTable?: boolean;
      readonly structure?: EditStructure;
    },
  ): Promise<boolean | undefined>;
}

export interface GridPasteRangesHost {
  readonly owner: GridPasteOwnerHost;
  readonly grid: { readonly shape: EditSheetMeta | undefined };
  readonly view: {
    mappedRows(): boolean;
    visibleSelection(): { readonly rows: number };
    pasteRows(count: number): Promise<readonly number[]>;
  };
  current(target: GridPasteTarget): void;
}

export interface PasteDestination {
  readonly rows: readonly number[];
  readonly width: number;
  readonly left: number;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function isPortableRange(value: unknown): value is NonNullable<PortableClipboard['range']> {
  return (
    isRecord(value) &&
    isRecord(value.start) &&
    typeof value.rows === 'number' &&
    typeof value.cols === 'number' &&
    Array.isArray(value.cells) &&
    Array.isArray(value.styles)
  );
}

export function isEditClipboard(value: unknown): value is EditClipboard {
  return (
    isRecord(value) &&
    value.format === 'ExcelLens.EditSelection' &&
    value.version === 2 &&
    isRecord(value.start) &&
    typeof value.rows === 'number' &&
    typeof value.cols === 'number' &&
    Array.isArray(value.cells)
  );
}

export function readPortableFromClip(clip: unknown): PortableClipboard | undefined {
  if (!isRecord(clip) || !isRecord(clip.portable)) return undefined;
  const portable = clip.portable;
  if (typeof portable.proof !== 'string' || !('range' in portable)) return undefined;
  return {
    range: isPortableRange(portable.range) ? portable.range : null,
    proof: portable.proof,
  };
}

function isPortableClipboard(value: unknown): value is PortableClipboard {
  return (
    isRecord(value) &&
    typeof value.proof === 'string' &&
    'range' in value &&
    (value.range === null || isPortableRange(value.range))
  );
}

/**
 * A clipboard can outlive a source tab. Embed only the copied rectangle.
 * The backend authenticates its portable style components before importing.
 * Accepts `unknown` so `app/main.ts` can inject directly into `GridClipboard`.
 */
export function encodePasteMarkup(typed: unknown): string {
  const portable = isRecord(typed) ? readPortableFromClip(typed.clip) : undefined;
  if (!portable) return '';
  const bytes = new TextEncoder().encode(JSON.stringify(portable));
  let binary = '';
  for (let i = 0; i < bytes.length; i += 8192) {
    binary += String.fromCharCode(...bytes.subarray(i, i + 8192));
  }
  return ` data-excellens-range="${btoa(binary)}"`;
}

export function identifyPasteClipboard(
  raw: string,
  html: string,
  copied: EditCopiedSnapshot | null | undefined,
  limit: number,
): IdentifiedClipboard | null {
  if (
    copied?.text === raw.replaceAll('\r\n', '\n') &&
    (html.includes(`id="${copied.marker}"`) || html.includes(`id='${copied.marker}'`))
  ) {
    return copied;
  }
  const attr = /\bdata-excellens-range=["']([a-zA-Z0-9+/=]+)["']/.exec(html);
  const encoded = attr?.[1];
  if (encoded) {
    if (encoded.length > 2 * limit) throw new Error('复制区域超过编辑容量');
    const text = new TextDecoder().decode(Uint8Array.from(atob(encoded), (c) => c.charCodeAt(0)));
    const parsed: unknown = JSON.parse(text);
    if (isPortableClipboard(parsed)) return { portable: parsed };
  }
  return /<(?:table|tr|td)\b/i.test(html) ? { external: true } : null;
}

export async function fetchNativePaste(
  owner: Pick<GridPasteOwnerHost, 'post'>,
  target: GridPasteTarget,
  raw: string,
): Promise<PortableClipboard | null> {
  const bytes = new TextEncoder().encode(raw.replaceAll('\r\n', '\n').replace(/\n$/, ''));
  const hash = new Uint8Array(await crypto.subtle.digest('SHA-256', bytes));
  const proof = Array.from(hash, (b) => b.toString(16).padStart(2, '0')).join('');
  return owner.post('native-paste', { ...target, proof }).then(decodeOptionalPortableClipboard);
}

export async function resolvePasteDestination(
  ranges: GridPasteRangesHost,
  rect: GridPasteRect,
  height: number,
  width: number,
): Promise<PasteDestination> {
  const { top, bottom, left, right } = rect;
  const filter = ranges.view.mappedRows();
  const size = filter ? ranges.view.visibleSelection().rows : bottom - top + 1;
  const selected = size > 1 || right > left;
  if (selected && (size % height || (right - left + 1) % width)) {
    throw new Error('请选择一个粘贴起点或与复制区域匹配的矩形');
  }
  const h = selected ? size : height;
  const w = selected ? right - left + 1 : width;
  if (h * w > ranges.owner.limits.batchCells) {
    throw new Error('区域超出一次编辑容量，请缩小范围');
  }
  const rows = filter
    ? await ranges.view.pasteRows(h)
    : Array.from({ length: h }, (_, i) => top + i);
  if (rows.length !== h || new Set(rows).size !== h) {
    throw new Error('筛选后的可见行不足，无法粘贴整个区域');
  }
  const shape = ranges.grid.shape;
  if (
    !shape ||
    rows.some((r) => !Number.isInteger(r) || r < shape.firstRow || r > shape.rows) ||
    left < shape.firstCol ||
    left + w - 1 > shape.cols
  ) {
    throw new Error('选区超出可编辑区域');
  }
  return { rows, width: w, left };
}

async function askVisibleOnly(
  clip: PortableClipboard,
  activity?: ActivityHandle | null,
): Promise<boolean | null> {
  const r = clip.range;
  if (!r) return false;
  const rows = r.visibleSourceRows?.length ?? 0;
  const cols = r.visibleSourceCols?.length ?? 0;
  if (!rows && !cols) return false;
  const dialog = requireDialog('editPasteHiddenRows');
  const size = (h: number, w: number): string =>
    rows && cols ? `${h} 行 × ${w} 列` : rows ? `${h} 行` : `${w} 列`;
  requireButton('editPasteVisible').textContent = `仅粘贴可见的 ${size(rows, cols)}`;
  requireButton('editPasteAllRows').textContent = `粘贴全部 ${size(r.rows, r.cols)}`;
  dialog.returnValue = 'cancel';
  activity?.pause();
  try {
    dialog.showModal();
    const value = await new Promise<string>((resolve) =>
      dialog.addEventListener('close', () => resolve(dialog.returnValue), { once: true }),
    );
    return value === 'visible' ? true : value === 'all' ? false : null;
  } finally {
    activity?.resume();
  }
}

export async function pastePortableRange(
  ranges: GridPasteRangesHost,
  target: GridPasteTarget,
  rect: GridPasteRect,
  clip: PortableClipboard,
  activity?: ActivityHandle | null,
): Promise<boolean | undefined> {
  const selection = clip.range;
  if (!selection) return false;
  const visible = await askVisibleOnly(clip, activity);
  if (visible === null) return true;
  ranges.current(target);
  const height = (visible && selection.visibleSourceRows?.length) || selection.rows;
  const widthToPaste = (visible && selection.visibleSourceCols?.length) || selection.cols;
  const dest = await resolvePasteDestination(ranges, rect, height, widthToPaste);
  ranges.current(target);
  const { rows, left, width } = dest;
  const firstRow = rows[0];
  const lastRow = rows.at(-1);
  if (firstRow === undefined || lastRow === undefined) {
    throw new Error('筛选后的可见行不足，无法粘贴整个区域');
  }
  const cell = (r: number, c: number): string => `${ranges.owner.colName(c)}${r}`;
  const range = `${cell(firstRow, left)}:${cell(ranges.view.mappedRows() ? firstRow : lastRow, left + width - 1)}`;
  const structure: EditStructure = {
    kind: 'pasteRange',
    sheet: target.sheet,
    range,
    paste: selection,
    ...(visible ? { pasteVisibleOnly: true } : {}),
    ...(ranges.view.mappedRows() ? { targetRows: rows } : {}),
  };
  if (!selection.merges?.length) {
    const preview = await ranges.owner.post('preview', {
      ...target,
      mode: 'portable',
      proof: clip.proof,
      operation: { structure },
    }).then(decodeEditBatchPreviewResponse);
    ranges.current(target);
    if (!preview.structure) {
      return ranges.owner.commit('change', preview.changes, {
        proof: preview.proof,
        direct: true,
        expandTable: !ranges.view.mappedRows(),
      });
    }
  }
  return ranges.owner.commit('structure', undefined, {
    structure,
    proof: clip.proof,
    direct: true,
  });
}

export const GridPasteModel = Object.freeze({
  markup: encodePasteMarkup,
  identify: identifyPasteClipboard,
  native: fetchNativePaste,
  destination: resolvePasteDestination,
  pasteRange: pastePortableRange,
});
