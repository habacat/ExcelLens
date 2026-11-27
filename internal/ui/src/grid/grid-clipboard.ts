/**
 * Grid clipboard orchestrator: text/HTML clipboard writes, bounded selection
 * snapshots, budget-aware progressive copying, cut/portable markup hooks, and
 * audit evidence/qualified-address copying.
 */
import type { ClipboardLimits } from '../contracts/formats.ts';
import type {
  CellData,
  ClipboardPayload,
  VisualStyle,
} from '../contracts/workbook.ts';
import { beginActivity } from '../core/activity.ts';
import { errorMessage } from '../core/display-format.ts';
import type { FileFormats } from '../core/formats.ts';
import type { ApiClient } from '../core/transport.ts';
import {
  buildClipboard,
  copyByBudget,
  utf8Length,
  type ClipboardBuildResult,
} from './clipboard.ts';
import type { FrozenPanes } from './freeze.ts';
import type { MergeCopyAnchor, MergedCells } from './merges.ts';
import { MultiSelection } from './multi-selection.ts';
import type { WorkbookState } from './state.ts';

export interface ClipboardReadOptions {
  readonly headerRow?: number;
  readonly richText?: boolean;
  readonly conditional?: boolean;
  readonly force?: boolean;
  readonly portable?: boolean;
}

export interface ClipboardSnapshot {
  readonly book: string;
  readonly workbookKey: string;
  readonly sheet: string;
  readonly styled: boolean;
  readonly styles: readonly VisualStyle[];
  readonly fontOverride: string;
  readonly current: () => boolean;
  readonly sourceCurrent: () => boolean;
  readonly mergeAt?: (row: number, col: number) => MergeCopyAnchor | null;
  readonly rows: number;
  readonly firstColumn: number;
  readonly lastColumn: number;
  readonly start: string;
  readonly end: string;
  readonly visualStart?: number;
  readonly visualEnd?: number;
  readonly maxRow: number;
  readonly headerRow: number;
  readonly label: string;
  readonly readTSV: () => Promise<string>;
  readonly read: (
    options?: ClipboardReadOptions,
    signal?: AbortSignal,
  ) => Promise<ClipboardPayload>;
}

export interface EditCopyClipboardSnapshot {
  marker: string;
  cut?: boolean;
  inactive?: boolean;
  text?: string;
  clip?: unknown;
}

export interface EditCopyRangesBridge {
  copyEpoch: number;
  copied: EditCopyClipboardSnapshot | null;
  readonly view: {
    mappedRows(): boolean;
  };
  copySnapshot(
    snapshot: ClipboardSnapshot,
    cut: boolean,
    rowNumbers?: readonly number[],
  ): Promise<EditCopyClipboardSnapshot | null>;
}

export interface EditCopyClipboardBridge {
  readonly pending?: unknown;
  readonly abort?: AbortController | null;
  readonly data?: {
    readonly document?: {
      readonly revision?: string | number;
    };
  } | null;
  readonly grid: {
    readonly active: boolean;
    readonly stale?: boolean;
    readonly ranges: EditCopyRangesBridge;
    readonly view: {
      redraw(): void;
    };
    projectRow(
      sheet: string,
      row: { readonly row: number; readonly cells: readonly CellData[] },
      col1: number,
      col2: number,
    ): CellData[];
  };
}

export interface ExportCenterClipboardBridge {
  readonly running: boolean;
  readonly busy: boolean;
  open(scope?: string, snapshot?: ClipboardSnapshot): void;
}

export interface GridClipboardDeps {
  readonly S: WorkbookState;
  readonly api: ApiClient;
  readonly colName: (col: number) => string;
  readonly fileFormats: FileFormats;
  readonly mergedCells: MergedCells;
  readonly sourceRowLimit: () => number;
  readonly toast: (message: string, tone?: 'info' | 'success' | 'warning' | 'error') => void;
  readonly getEditCopy?: () => EditCopyClipboardBridge | null | undefined;
  readonly getFrozenPanes: () => FrozenPanes;
  readonly getExportCenter?: () => ExportCenterClipboardBridge | null | undefined;
  readonly encodePasteMarkup?: (payload: unknown) => string;
}

interface LegacyCopyDocument {
  execCommand?(commandId: string): boolean;
}

function readClipboard(
  nav: { readonly clipboard?: Partial<Clipboard> },
): Partial<Clipboard> | undefined {
  return nav.clipboard;
}

function execLegacyCopy(): boolean {
  const legacy: LegacyCopyDocument = document;
  return typeof legacy.execCommand === 'function' && legacy.execCommand('copy');
}

export class GridClipboard {
  private readonly d: GridClipboardDeps;

  constructor(deps: GridClipboardDeps) {
    this.d = deps;
  }

  async copyText(value: string | null | undefined, successMessage = '已复制'): Promise<boolean> {
    const { toast } = this.d;
    const text = value ?? '';
    try {
      const clipboard = readClipboard(navigator);
      if (typeof clipboard?.writeText === 'function') {
        await clipboard.writeText(text);
      } else {
        const helper = document.createElement('textarea');
        helper.value = text;
        helper.style.position = 'fixed';
        helper.style.opacity = '0';
        document.body.appendChild(helper);
        helper.select();
        try {
          if (!execLegacyCopy()) {
            throw new Error('浏览器未能写入剪贴板');
          }
        } finally {
          helper.remove();
        }
      }
      toast(successMessage, 'success');
      return true;
    } catch (error: unknown) {
      toast(`复制失败：${errorMessage(error)}`, 'error');
      return false;
    }
  }

  async writeClipboardPayload(
    content: Pick<ClipboardBuildResult, 'text' | 'html'>,
    cut = false,
  ): Promise<boolean> {
    const { toast } = this.d;
    if (typeof navigator.clipboard.write === 'function' && typeof ClipboardItem !== 'undefined') {
      try {
        await navigator.clipboard.write([
          new ClipboardItem({
            'text/plain': new Blob([content.text], { type: 'text/plain' }),
            'text/html': new Blob([content.html], { type: 'text/html' }),
          }),
        ]);
        if (!cut) toast('已复制', 'success');
        return true;
      } catch {
        // Some browsers only allow HTML during a native copy event.
      }
    }
    const copyState = { rich: false };
    const onCopy = (event: ClipboardEvent): void => {
      if (!event.clipboardData) return;
      event.preventDefault();
      event.stopImmediatePropagation();
      event.clipboardData.setData('text/plain', content.text);
      event.clipboardData.setData('text/html', content.html);
      copyState.rich = true;
    };
    document.addEventListener('copy', onCopy, true);
    try {
      if (execLegacyCopy() && copyState.rich) {
        if (!cut) toast('已复制', 'success');
        return true;
      }
    } catch {
      // A text-only clipboard can still accept the TSV.
    } finally {
      document.removeEventListener('copy', onCopy, true);
    }
    if (cut) throw new Error('浏览器未写入剪切区域，请重新剪切');
    const copied = await this.copyText(content.text);
    if (copied) toast('已复制为纯文本，当前浏览器未写入表格格式', 'warning');
    return copied;
  }

  clipboardIdentity(): string {
    const { S, getEditCopy } = this.d;
    const editCopy = getEditCopy?.();
    return JSON.stringify([
      S.book,
      S.workbookKey,
      S.sheet,
      S.sheetEpoch,
      S.indexReady,
      S.filtered,
      S.filterId,
      S.rowView?.id,
      S.selected?.ref,
      S.selected?.row,
      S.selected?.col,
      S.selectedVisualIndex,
      S.range,
      S.multiRanges,
      S.showStyles,
      S.fontOverride,
      S.freeze,
      editCopy?.grid.active ? editCopy.data?.document?.revision : false,
    ]);
  }

  selectionSourceIdentity(): string {
    const { S } = this.d;
    return JSON.stringify([
      S.book,
      S.workbookKey,
      S.sheet,
      S.sheetEpoch,
      S.indexReady,
      S.filtered,
      S.filterId,
      S.rowView?.id,
      S.freeze,
    ]);
  }

  captureClipboardSelection(
    options: {
      readonly purpose?: string;
      readonly limits?: Pick<ClipboardLimits, 'rows' | 'columns' | 'cells'> | null;
      readonly withMerges?: boolean;
    } = {},
  ): ClipboardSnapshot {
    const {
      purpose = '复制',
      limits = this.d.fileFormats.clipboard,
      withMerges = false,
    } = options;
    const { S, api, colName, mergedCells, sourceRowLimit, getEditCopy, getFrozenPanes } = this.d;
    const editCopy = getEditCopy?.();
    const frozenPanes = getFrozenPanes();
    if (!S.indexReady || !S.selected || !S.sheet || S.sheetLoadingEpoch) {
      throw new Error('请先选择一个已加载的单元格或区域');
    }
    if (editCopy?.grid.active && (editCopy.grid.stale || editCopy.pending || editCopy.abort)) {
      throw new Error('请先确认当前编辑修订');
    }
    if (S.multiRanges.length) {
      throw new Error(`请选择一个连续区域进行${purpose}`);
    }
    const visibility = S.rowVisibility;
    let area = MultiSelection.areas(S, true).at(-1);
    if (!area) throw new Error('请先选择一个已加载的单元格或区域');
    if (withMerges && !S.filtered) area = mergedCells.expand(area, visibility);
    if (withMerges && S.filtered && !S.range) {
      const m = mergedCells.at(S.selected.row, S.selected.col);
      if (m?.row === S.selected.row) area = { ...area, col1: m.col, col2: m.endCol };
    }
    const points = MultiSelection.points(area, colName);
    const col1 = Math.min(area.col1, area.col2);
    const col2 = Math.max(area.col1, area.col2);
    const excluded = frozenPanes.effective().rows;
    const rowCount = visibility
      ? visibility.selectionCount(area, S.filtered, excluded)
      : MultiSelection.rowCount(area, S.filtered, excluded);
    const colCount = col2 - col1 + 1;
    if (rowCount < 1) throw new Error('选区没有可见行');
    if (
      limits &&
      (rowCount > limits.rows || colCount > limits.columns || rowCount * colCount > limits.cells)
    ) {
      throw new Error(
        `${purpose}最多${limits.rows}行、${limits.columns}列、${limits.cells}格，请缩小选区`,
      );
    }
    const sheet = S.sheet;
    const identity = this.clipboardIdentity();
    const selected = { ...S.selected };
    const viewId = S.rowView?.id ?? '';
    const filterId = S.filtered ? S.filterId : '';
    const book = S.book;
    const workbookKey = S.workbookKey;
    const styled = S.showStyles;
    const styles = S.styles;
    const fontOverride = S.fontOverride;
    const current = (): boolean => identity === this.clipboardIdentity();
    const sourceIdentity = this.selectionSourceIdentity();
    const sourceCurrent = (): boolean => sourceIdentity === this.selectionSourceIdentity();
    const mergeAt =
      withMerges && mergedCells.root
        ? (row: number, col: number): MergeCopyAnchor | null =>
            mergedCells.copyAnchor(row, col, visibility)
        : undefined;

    const rangeParams = (): URLSearchParams => {
      const params = new URLSearchParams({
        sheet,
        start: points.start,
        end: points.end,
        book,
        workbookKey,
      });
      if (viewId) params.set('viewId', viewId);
      if (filterId) {
        params.set('filterId', filterId);
        params.set('visualStart', String(points.visualStart));
        params.set('visualEnd', String(points.visualEnd));
      }
      if (viewId || filterId) params.set('excludeThroughRow', String(excluded));
      return params;
    };

    return {
      book,
      workbookKey,
      sheet,
      styled,
      styles,
      fontOverride,
      current,
      sourceCurrent,
      mergeAt,
      rows: rowCount,
      firstColumn: col1,
      lastColumn: col2,
      ...points,
      maxRow: sourceRowLimit(),
      headerRow:
        S.autoFilter?.row ||
        S.sortHeaderBySheet.get(sheet) ||
        S.source?.tableHeader?.row ||
        1,
      label: `${sheet} · ${points.start}:${points.end} · ${rowCount}行 × ${colCount}列`,
      readTSV: (): Promise<string> => {
        const params = rangeParams();
        params.set('format', 'tsv');
        return api(`/api/range?${params.toString()}`, {}, 'text');
      },
      read: async (
        readOpts: ClipboardReadOptions = {},
        signal?: AbortSignal,
      ): Promise<ClipboardPayload> => {
        const {
          headerRow = 0,
          richText = true,
          conditional = styled,
          force = false,
          portable = false,
        } = readOpts;
        if (!current()) throw new Error('来源或选区已变化，请重新选择');
        if (rowCount === 1 && colCount === 1 && !headerRow && !force && !portable) {
          const cell: CellData = { ...selected };
          if (!richText) delete cell.richText;
          if (!conditional) delete cell.conditional;
          return {
            book,
            workbookKey,
            sheet,
            startCol: col1,
            endCol: col2,
            rows: [{ row: selected.row, cells: [cell] }],
          };
        }
        const params = rangeParams();
        params.set('format', 'json');
        params.set('richText', richText ? '1' : '0');
        params.set('conditional', conditional ? '1' : '0');
        if (portable) params.set('portable', '1');
        if (headerRow) params.set('headerRow', String(headerRow));
        const payload = await api<ClipboardPayload>(`/api/range?${params.toString()}`, { signal });
        if (!current()) {
          const err = new Error('复制已取消，来源或选区已变化');
          err.name = 'AbortError';
          throw err;
        }
        const activeEdit = getEditCopy?.();
        if (activeEdit?.grid.active) {
          const rows = payload.rows.map((row) => ({
            ...row,
            cells: activeEdit.grid.projectRow(sheet, row, col1, col2),
          }));
          const header = payload.header
            ? {
                ...payload.header,
                cells: activeEdit.grid.projectRow(sheet, payload.header, col1, col2),
              }
            : undefined;
          return { ...payload, rows, header };
        }
        return payload;
      },
    };
  }

  async copySelection(plain = false, cut = false): Promise<boolean | undefined> {
    const { fileFormats, toast, getEditCopy, getExportCenter } = this.d;
    const activity = beginActivity(cut ? '正在剪切选区' : '正在复制选区');
    const editCopy = getEditCopy?.();
    const ranges = editCopy?.grid.active ? editCopy.grid.ranges : null;
    const epoch = ranges ? (ranges.copyEpoch = (ranges.copyEpoch || 0) + 1) : 0;
    try {
      const progressive = !ranges && !cut;
      const snapshot = this.captureClipboardSelection({
        withMerges: !plain,
        limits: progressive ? null : fileFormats.clipboard,
      });
      if (!progressive) {
        return await this.copyGridPayload(snapshot, plain, cut, ranges, epoch);
      }
      return await copyByBudget(
        snapshot,
        fileFormats.clipboard,
        plain,
        () => this.copyGridPayload(snapshot, false, false, null, 0),
        async () => {
          const text = await snapshot.readTSV();
          return snapshot.current() ? this.copyText(text, '已复制为纯文本') : false;
        },
        () => {
          const exportCenter = getExportCenter?.();
          if (!exportCenter) return;
          if (exportCenter.running || exportCenter.busy) {
            toast('导出中心正在处理任务，请在完成后导出选区', 'warning');
            return;
          }
          exportCenter.open('selection', snapshot);
          toast('选区超过复制容量，可导出为文件', 'warning');
        },
      );
    } catch (err: unknown) {
      if (!(err instanceof Error && err.name === 'AbortError')) {
        toast(errorMessage(err), 'error');
      }
      return undefined;
    } finally {
      activity?.end();
    }
  }

  async copyGridPayload(
    snapshot: ClipboardSnapshot,
    plain: boolean,
    cut: boolean,
    ranges: EditCopyRangesBridge | null,
    epoch: number,
  ): Promise<boolean> {
    const { fileFormats, toast, getEditCopy, encodePasteMarkup } = this.d;
    const editCopy = getEditCopy?.();
    const [payload, initial] = await Promise.all([
      snapshot.read({
        richText: !plain && snapshot.styled,
        conditional: !plain && snapshot.styled,
        portable: !plain && !ranges,
      }),
      !plain && editCopy?.grid.active && ranges && !ranges.view.mappedRows()
        ? ranges.copySnapshot(snapshot, cut)
        : Promise.resolve(null),
    ]);
    const typed =
      !plain && ranges && ranges.view.mappedRows() && snapshot.current()
        ? await ranges.copySnapshot(
            snapshot,
            cut,
            payload.rows.map((r) => r.row),
          )
        : initial;
    if (!snapshot.current() || (ranges && ranges.copyEpoch !== epoch)) return false;
    if (cut && !typed) {
      throw new Error('剪切需要一个连续、完整可见且未超出编辑容量的工作表区域');
    }
    const content = buildClipboard(
      payload,
      snapshot.styles,
      snapshot.styled && !plain,
      snapshot.fontOverride,
      fileFormats.richText,
      fileFormats.clipboard,
      snapshot.mergeAt,
    );
    let html = content.html;
    if (typed) {
      typed.text = content.text.replaceAll('\r\n', '\n');
      const markup = encodePasteMarkup ? encodePasteMarkup(typed) : '';
      html = html.replace('<table ', `<table id="${typed.marker}"${markup} `);
    }
    if (!typed && payload.portable && encodePasteMarkup) {
      html = html.replace(
        '<table ',
        `<table${encodePasteMarkup({ clip: { portable: payload.portable } })} `,
      );
    }
    if ((typed || payload.portable) && utf8Length(html) > fileFormats.clipboard.htmlBytes) {
      throw Object.assign(new Error('复制内容超过本次容量'), { status: 413 });
    }
    const notices = [...content.notices];
    if (payload.portableNotice) notices.push(payload.portableNotice);
    const copied = plain
      ? await this.copyText(content.text)
      : await this.writeClipboardPayload({ text: content.text, html }, cut);
    if (cut && !snapshot.current()) return false;
    if (copied && snapshot.current() && editCopy?.grid.active && ranges) {
      if (ranges.copyEpoch !== epoch) {
        if (cut && typed && !ranges.copied) ranges.copied = { ...typed, inactive: true };
        return false;
      }
      const repaintCut = Boolean(typed?.cut) || Boolean(ranges.copied?.cut && !ranges.copied.inactive);
      ranges.copied = typed;
      if (repaintCut) editCopy.grid.view.redraw();
    }
    if (!plain && notices.length) toast(notices.join('；'), 'warning');
    return copied;
  }

  copyCellEvidence(): Promise<boolean> | undefined {
    const { S, toast } = this.d;
    const c = S.selected;
    if (!c) {
      toast('请先选中一个单元格', 'warning');
      return undefined;
    }
    const text = [
      `工作簿：${S.fileName}`,
      `工作表：${S.sheet ?? ''}`,
      `单元格：${c.ref}`,
      `显示值：${c.display}`,
      `缓存原始值：${c.raw}`,
      `公式：${c.formula ?? '（非公式单元格）'}`,
      `标识：${c.cacheMissing ? '公式缺少缓存值' : '只读读取原文件保存的值（未重算）'}`,
    ].join('\n');
    return this.copyText(text);
  }

  copyQualifiedAddress(): Promise<boolean> | undefined {
    const { S, toast } = this.d;
    if (!S.selected) {
      toast('请先选择单元格', 'warning');
      return undefined;
    }
    const sheet = `'${(S.sheet ?? '').replaceAll("'", "''")}'`;
    return this.copyText(`${sheet}!${S.selected.ref}`);
  }
}
