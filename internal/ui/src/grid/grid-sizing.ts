import { t as localize } from '../core/i18n.ts';
import type { ApiClient, ToastTone } from '../contracts/common.ts';
import type { VisualStyle } from '../contracts/workbook.ts';
import { errorMessage } from '../core/display-format.ts';
import { requireEl } from '../core/dom.ts';
import { askOverlay } from '../core/overlays.ts';
import type { Geometry } from './geometry.ts';
import type { WorkbookState } from './state.ts';
import { BASE_ROW } from './geometry.ts';
import { fontStack } from './display-font.ts';
import { decodeSheetLayoutResponse } from '../core/http-decoders.generated.ts';
import { decodeSampledRows } from '../core/viewport-response.ts';

interface GridSizingDeps {
  readonly S: WorkbookState;
  readonly api: ApiClient;
  readonly geometry: Geometry;
  readonly toast: (message: string, tone?: ToastTone) => void;
  readonly colName: (col: number) => string;
  readonly sourceColLimit: () => number;
  readonly scale: () => number;
  readonly rowHeight: (row: number) => number;
  readonly colWidth: (col: number) => number;
  readonly relayoutGrid: () => void;
}

export class GridSizing {
  private readonly d: GridSizingDeps;
  private readonly autoFitCtx: CanvasRenderingContext2D | null;

  private saveQueue: Promise<unknown> = Promise.resolve();

  constructor(deps: GridSizingDeps) {
    this.d = deps;
    this.autoFitCtx = document.createElement('canvas').getContext('2d');
  }

  commitResize(kind: 'row' | 'col', index: number, size: number): Promise<boolean> {
    const { S, geometry, api, toast } = this.d;
    const sheet = S.sheet;
    const epoch = S.sheetEpoch;
    const workbook = S.workbookKey;
    const current = () => S.sheet === sheet && S.sheetEpoch === epoch && S.workbookKey === workbook;
    const save = async (): Promise<boolean> => {
      if (!current() || !S.indexReady || !sheet) return false;
      if (kind === 'row' && S.filtered) {
        toast(localize('ui.gridSizing.singleRowHeightFilteredView'), 'warning');
        return false;
      }
      if (kind === 'col' && S.hiddenColumns.has(index)) {
        toast(localize('ui.gridSizing.displayColumnFirstThenAdjust'), 'warning');
        return false;
      }
      const sizes = kind === 'col' ? S.columnWidths : S.rowSizes;
      if ((sizes[String(index)] ?? 0) === size) return true;
      try {
        const layout = await api('/api/layout', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json', 'X-ExcelLens-Workbook': workbook },
          body: JSON.stringify({ sheet, kind, index, size }),
        }, decodeSheetLayoutResponse);
        if (!current()) return false;
        const confirmed = kind === 'col' ? layout.cols : layout.rows;
        if (!confirmed || (confirmed[String(index)] ?? 0) !== size) {
          throw new Error(localize('ui.gridSizing.serviceNotConfirmedNewDimensions'));
        }
        if (size) sizes[String(index)] = size;
        else Reflect.deleteProperty(sizes, String(index));
        const effective = size || (kind === 'col' ? (S.sourceColumnWidths[String(index)] ?? 0) : 0);
        geometry.set(kind, index, effective);
        this.d.relayoutGrid();
        return true;
      } catch (error: unknown) {
        if (current()) toast(localize('ui.gridSizing.failedSaveDimensions', { detail: errorMessage(error) }), 'error');
        return false;
      }
    };
    // Apply only acknowledged edits, in the order the user made them.
    const pending = this.saveQueue.then(save, save);
    this.saveQueue = pending;
    return pending;
  }

  async promptResize(kind: 'row' | 'col', explicitIndex?: number): Promise<void> {
    const { S, colName, toast } = this.d;
    if (kind === 'row' && S.filtered) {
      toast(localize('ui.gridSizing.clearFilterFirstThenAdjust'), 'warning');
      return;
    }
    const index = explicitIndex ?? (kind === 'row' ? (S.selected?.row ?? 1) : (S.selected?.col ?? 1));
    const oldValue = kind === 'row' ? this.d.rowHeight(index) : this.d.colWidth(index);
    const low = kind === 'row' ? 14 : 32;
    const high = kind === 'row' ? 500 : 800;
    const sheet = S.sheet;
    const epoch = S.sheetEpoch;
    const valid = (text: string): boolean =>
      /^\d+$/.test(text) && (Number(text) === 0 || (Number(text) >= low && Number(text) <= high));
    const answer = await askOverlay({
      title: kind === 'row' ? localize('ui.gridSizing.rowHeightRow', { index }) : localize('ui.gridSizing.columnWidth', { colName: colName(index) }),
      label: localize('ui.gridSizing.pixel', { low, high }),
      value: String(Math.round(oldValue / this.d.scale())),
      inputMode: 'numeric',
      alternative: localize('ui.gridSizing.restoreDefault'),
      validate: (text) => (valid(text) ? '' : localize('ui.gridSizing.enterIntegerBetween', { low, high })),
    });
    if (!answer || S.sheet !== sheet || S.sheetEpoch !== epoch) return;
    await this.commitResize(kind, index, answer.alternative ? 0 : Number(answer.value));
  }

  bindResizeGrip(grip: HTMLElement, kind: 'row' | 'col', index: number): void {
    const { S, colName, toast } = this.d;
    grip.title = kind === 'row' ? localize('ui.gridSizing.adjustHeightRow', { index }) : localize('ui.gridSizing.adjustWidthColumn', { colName: colName(index) });
    grip.addEventListener('pointerdown', (event: PointerEvent) => {
      if (event.button !== 0) return;
      if (kind === 'row' && S.filtered) {
        event.preventDefault();
        event.stopPropagation();
        toast(localize('ui.gridSizing.clearFilterFirstThenAdjust'), 'warning');
        return;
      }
      event.preventDefault();
      event.stopPropagation();
      const epoch = S.sheetEpoch;
      const sheet = S.sheet;
      const pointer = event.pointerId;
      const start = kind === 'row' ? event.clientY : event.clientX;
      const previous = kind === 'row' ? this.d.rowHeight(index) : this.d.colWidth(index);
      const guide = requireEl('resizeGuide');
      let next = previous / this.d.scale();
      const host = requireEl('grid').getBoundingClientRect();
      const update = (delta: number): void => {
        next = Math.max(
          kind === 'row' ? 14 : 32,
          Math.min(kind === 'row' ? 500 : 800, (previous + delta) / this.d.scale()),
        );
        guide.hidden = false;
        guide.className = `resize-guide ${kind === 'row' ? 'horizontal' : 'vertical'}`;
        if (kind === 'row') {
          guide.style.top = `${Math.min(host.height - 1, Math.max(0, event.clientY - host.top + delta))}px`;
          guide.style.left = '0';
          guide.style.width = '100%';
        } else {
          guide.style.left = `${Math.min(host.width - 1, Math.max(0, event.clientX - host.left + delta))}px`;
          guide.style.top = '0';
          guide.style.height = '100%';
        }
      };
      const move = (e: PointerEvent): void => {
        if (e.pointerId !== pointer) return;
        e.preventDefault();
        update((kind === 'row' ? e.clientY : e.clientX) - start);
      };
      const cleanup = (): void => {
        document.removeEventListener('pointermove', move, true);
        document.removeEventListener('pointerup', end, true);
        document.removeEventListener('pointercancel', cancel, true);
        window.removeEventListener('blur', cleanup);
        guide.hidden = true;
      };
      const end = (e: PointerEvent): void => {
        if (e.pointerId !== pointer) return;
        cleanup();
        if (epoch !== S.sheetEpoch || sheet !== S.sheet) return;
        void this.commitResize(kind, index, Math.round(next));
      };
      const cancel = (e: PointerEvent): void => {
        if (e.pointerId === pointer) cleanup();
      };
      document.addEventListener('pointermove', move, true);
      document.addEventListener('pointerup', end, true);
      document.addEventListener('pointercancel', cancel, true);
      window.addEventListener('blur', cleanup);
      update(0);
    });
    grip.addEventListener('click', (event: MouseEvent) => {
      event.preventDefault();
      event.stopPropagation();
    });
    grip.addEventListener('dblclick', (event: MouseEvent) => {
      event.preventDefault();
      event.stopPropagation();
      void this.autoFitSize(kind, index);
    });
  }

  private measureAutoFit(
    text: string,
    visual: VisualStyle | null | undefined,
  ): { readonly size: number; readonly width: number; readonly lines: readonly string[] } {
    const { S } = this.d;
    const size = S.showStyles && visual?.fontSize && visual.fontSize > 0
      ? Math.min(80, (visual.fontSize * 4) / 3)
      : 11;
    if (this.autoFitCtx) {
      const name = S.fontOverride || (S.showStyles ? visual?.fontName : '') || 'Segoe UI';
      this.autoFitCtx.font = `${visual?.italic ? 'italic ' : ''}${visual?.bold ? 'bold ' : ''}${size}px ${fontStack(name)}`;
    }
    const lines = text.slice(0, 2048).split(/\r?\n/);
    const widths = lines.map((line) =>
      this.autoFitCtx ? this.autoFitCtx.measureText(line).width : line.length * size * 0.8,
    );
    return { size, width: Math.max(0, ...widths), lines };
  }

  async autoFitSize(kind: 'row' | 'col', index: number): Promise<void> {
    const { S, api, geometry, colName, sourceColLimit, toast } = this.d;
    if (!S.indexReady || !S.sheet) return;
    if (kind === 'row' && S.filtered) {
      toast(localize('ui.gridSizing.clearFilterBeforeAdjustingRow'), 'warning');
      return;
    }
    const sheet = S.sheet;
    const epoch = S.sheetEpoch;
    const params = new URLSearchParams({ sheet, kind, index: String(index) });
    if (kind === 'row') {
      const first = S.window?.startCol ?? 1;
      params.set('firstCol', String(first));
      params.set('lastCol', String(Math.min(sourceColLimit(), first + 79)));
    }
    try {
      const rows = await api(`/api/autofit?${params.toString()}`, undefined, decodeSampledRows);
      if (epoch !== S.sheetEpoch || sheet !== S.sheet) return;
      let result = kind === 'col' ? 65 : BASE_ROW;
      if (kind === 'col') {
        for (const row of rows) {
          for (const cell of row.cells) {
            const text = cell.display || cell.raw;
            if (!text) continue;
            const width = this.measureAutoFit(text, S.styles[cell.style ?? 0]).width;
            result = Math.max(result, Math.min(800, Math.ceil(width + 22)));
          }
        }
        result = Math.max(32, Math.min(800, result));
      } else {
        for (const row of rows) {
          for (const cell of row.cells) {
            const visual = S.showStyles ? S.styles[cell.style ?? 0] : null;
            const text = cell.display || cell.raw;
            if (!text) continue;
            const measured = this.measureAutoFit(text, visual);
            let lines = measured.lines.length;
            if (visual?.wrap) {
              const width = Math.max(24, geometry.cols.measure(cell.col) - 16);
              lines = measured.lines.reduce(
                (n, line) =>
                  n +
                  Math.max(
                    1,
                    Math.ceil(
                      (this.autoFitCtx
                        ? this.autoFitCtx.measureText(line).width
                        : line.length * measured.size * 0.8) / width,
                    ),
                  ),
                0,
              );
            }
            result = Math.max(result, Math.ceil(lines * measured.size * 1.35 + 8));
          }
        }
        result = Math.max(14, Math.min(500, result));
      }
      const saved = await this.commitResize(kind, index, result);
      if (saved && epoch === S.sheetEpoch) {
        toast(
          localize('ui.gridSizing.adjustedAutomaticallyBasedOn', { detail: kind === 'col' ? localize('ui.gridSizing.column', { colName: colName(index) }) : localize('ui.gridSizing.row', { index }), detail2: kind === 'col' ? localize('ui.gridSizing.samplingContent') : localize('ui.gridSizing.currentViewedColumn') }),
          'success',
        );
      }
    } catch (err: unknown) {
      if (epoch === S.sheetEpoch) toast(localize('ui.gridSizing.automaticAdjustmentFailed', { detail: errorMessage(err) }), 'error');
    }
  }
}
