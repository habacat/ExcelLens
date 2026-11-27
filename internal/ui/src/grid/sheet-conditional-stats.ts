import type { ApiClient, ToastTone } from '../contracts/common.ts';
import type { CellData } from '../contracts/workbook.ts';
import { errorMessage } from '../core/display-format.ts';
import { byId, requireButton, requireEl } from '../core/dom.ts';
import type { FileFormatsRegistry } from '../core/formats.ts';
import { createStatsRequests, type ConditionalStatsRequestQueue } from './conditional.ts';
import type { FrozenPanes } from './freeze.ts';
import type { WorkbookState } from './state.ts';
import {
  decodeConditionalStatsCancelResponse,
  decodeConditionalStatsStartResponse,
  decodeConditionalStatsStatusResponse,
} from '../core/http-decoders.generated.ts';

export interface ConditionalStatsDeps {
  readonly S: WorkbookState;
  readonly api: ApiClient;
  readonly fileFormats: FileFormatsRegistry;
  readonly toast: (message: string, tone?: ToastTone) => void;
  readonly resetGridDOM: () => void;
  readonly renderGridFromCurrentWindow: () => void;
  readonly invalidateViewport: (options?: { readonly preserveDOM?: boolean }) => void;
  readonly loadViewport: (
    force?: boolean,
    expectedEpoch?: number,
    strict?: boolean,
  ) => Promise<void>;
  readonly getCell: (sheet: string, ref: string) => Promise<CellData>;
  readonly selectCell: (
    cell: CellData | null | undefined,
    rowNumber: number,
    colNumber: number,
    ref: string,
    visualIndex: number,
  ) => void;
  readonly getFrozenPanes: () => FrozenPanes;
}

export class SheetConditionalStats {
  private readonly d: ConditionalStatsDeps;
  private statsRequests: ConditionalStatsRequestQueue | null = null;

  constructor(deps: ConditionalStatsDeps) {
    this.d = deps;
  }

  private getConditionalStatsRequests(): ConditionalStatsRequestQueue {
    const post = (payload: { sheet: string } | { id: string }) => ({
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
      activity: false,
    });
    this.statsRequests ??= createStatsRequests(
      sheet => this.d.api('/api/conditional-stats/start', post({ sheet }), decodeConditionalStatsStartResponse),
      id => this.d.api('/api/conditional-stats/cancel', post({ id }), decodeConditionalStatsCancelResponse),
    );
    return this.statsRequests;
  }

  updateSourceAppearance(): void {
    const { S, fileFormats } = this.d;
    const workspace = requireEl('workspace');
    workspace.classList.toggle(
      'source-sheet-view',
      S.showStyles && fileFormats.source(S.source?.format).styled,
    );
    workspace.classList.toggle('source-no-gridlines', S.showStyles && !S.sourceGridLines);
    workspace.classList.toggle('source-html-view', S.showStyles && S.source?.format === 'xls-html');
  }

  toggleOriginalFormat(): void {
    const { S, resetGridDOM, renderGridFromCurrentWindow, getFrozenPanes } = this.d;
    S.showStyles = !S.showStyles;
    this.updateSourceAppearance();
    const formatBtn = requireButton('formatBtn');
    formatBtn.classList.toggle('format-active', S.showStyles);
    formatBtn.setAttribute('aria-pressed', String(S.showStyles));
    if (!S.showStyles) {
      if (S.conditionalStatsJob) this.cancelConditionalStats(true, false);
      else this.updateConditionalStatsUI('');
    }
    resetGridDOM();
    renderGridFromCurrentWindow();
    getFrozenPanes().schedule(true);
    if (S.showStyles && S.indexReady && S.sheet) {
      const skipped = S.tilesSkippedConditional;
      if (skipped) this.refreshViewportForConditionalStats(S.sheetEpoch);
      if (S.conditionalStatsState !== 'done' && S.conditionalStatsState !== 'not_needed') {
        void this.startConditionalStats(S.sheet, S.sheetEpoch, {
          refreshOnReady: !skipped && Boolean(S.viewportData?.data.conditionalStatsPending),
        });
      }
    }
  }

  updateConditionalStatsUI(
    state: string,
    text = '',
    options: { readonly canCancel?: boolean; readonly canRetry?: boolean } = {},
  ): void {
    const { canCancel = false, canRetry = false } = options;
    const { S } = this.d;
    const box = byId('conditionalStatsStatus');
    S.conditionalStatsState = state || '';
    if (!box) return;
    const visible = Boolean(
      S.opened && S.showStyles && state && state !== 'done' && state !== 'not_needed',
    );
    box.hidden = !visible;
    box.dataset.state = state || '';
    if (!visible) return;
    requireEl('conditionalStatsText').textContent = text;
    requireEl('conditionalStatsCancel').hidden = !canCancel;
    requireEl('conditionalStatsRetry').hidden = !canRetry;
  }

  refreshViewportForConditionalStats(epoch: number): void {
    const {
      S,
      invalidateViewport,
      loadViewport,
      toast,
      getFrozenPanes,
      getCell,
      selectCell,
    } = this.d;
    if (epoch !== S.sheetEpoch || !S.indexReady || !S.sheet || !S.showStyles) return;
    S.tilesSkippedConditional = false;
    const cached = S.sheetViews.get(S.sheet);
    if (cached) {
      S.sheetViewsBytes -= cached.bytes;
      S.sheetViews.delete(S.sheet);
    }
    invalidateViewport({ preserveDOM: true });
    const frozenPanes = getFrozenPanes();
    frozenPanes.reset({ preserveDOM: true });
    loadViewport(true, epoch).catch((error: unknown) => {
      if (epoch === S.sheetEpoch && !(error instanceof Error && error.name === 'AbortError')) {
        toast(errorMessage(error), 'error');
      }
    });
    frozenPanes.schedule(true);
    if (S.selected?.ref) {
      const ref = S.selected.ref;
      const row = S.selected.row;
      const col = S.selected.col;
      const visual = S.selectedVisualIndex;
      getCell(S.sheet, ref)
        .then((cell) => {
          if (epoch === S.sheetEpoch && S.selected?.ref === ref) {
            selectCell(cell, row, col, ref, visual);
          }
        })
        .catch(() => {
          // Ignore stale cell refresh errors.
        });
    }
  }

  cancelConditionalStats(sendCancel = true, showCancelled = false): void {
    const { S } = this.d;
    const epoch = S.sheetEpoch;
    ++S.conditionalStatsSeq;
    if (S.conditionalStatsTimer) {
      clearTimeout(S.conditionalStatsTimer);
      S.conditionalStatsTimer = 0;
    }
    const jobID =
      S.conditionalStatsJob && S.conditionalStatsJob !== 'starting'
        ? S.conditionalStatsJob
        : null;
    const hadActive = Boolean(S.conditionalStatsJob);
    S.conditionalStatsJob = null;
    let cancelPromise: Promise<unknown> = Promise.resolve();
    if (sendCancel && hadActive) {
      cancelPromise = this.getConditionalStatsRequests()
        .cancel(jobID)
        .catch(() => undefined);
    }
    if (showCancelled && S.showStyles && S.indexReady) {
      this.updateConditionalStatsUI('cancelled', '条件统计已取消', { canRetry: true });
      void cancelPromise.then(() => {
        if (epoch === S.sheetEpoch && S.showStyles && S.conditionalStatsState === 'cancelled') {
          this.refreshViewportForConditionalStats(epoch);
        }
      });
    } else {
      this.updateConditionalStatsUI('');
    }
  }

  async startConditionalStats(
    sheetName: string,
    epoch: number,
    options: { readonly refreshOnReady?: boolean } = {},
  ): Promise<void> {
    const { refreshOnReady = false } = options;
    const { S, api } = this.d;
    if (!S.showStyles || !S.indexReady || !sheetName || epoch !== S.sheetEpoch || S.conditionalStatsJob) {
      return;
    }
    const seq = ++S.conditionalStatsSeq;
    S.conditionalStatsJob = 'starting';
    try {
      const start = await this.getConditionalStatsRequests().start(sheetName);
      if (seq !== S.conditionalStatsSeq || epoch !== S.sheetEpoch || !this.d.S.showStyles) return;
      if (!start.needed) {
        S.conditionalStatsJob = null;
        this.updateConditionalStatsUI('not_needed');
        if (refreshOnReady) this.refreshViewportForConditionalStats(epoch);
        return;
      }
      if (start.ready) {
        S.conditionalStatsJob = null;
        this.updateConditionalStatsUI('done');
        if (refreshOnReady) this.refreshViewportForConditionalStats(epoch);
        return;
      }
      if (start.status === 'error' || !start.jobId) {
        S.conditionalStatsJob = null;
        this.updateConditionalStatsUI(
          'error',
          start.error ? `条件统计失败：${start.error}` : '条件统计失败',
          { canRetry: true },
        );
        return;
      }
      const jobId = start.jobId;
      S.conditionalStatsJob = jobId;
      const baseLabel = start.damaged ? '条件统计重建中' : '条件统计计算中';
      this.updateConditionalStatsUI(start.status || 'running', baseLabel, { canCancel: true });
      const poll = async (): Promise<void> => {
        if (seq !== S.conditionalStatsSeq || epoch !== S.sheetEpoch || !S.showStyles) return;
        try {
          const st = await api(
            `/api/conditional-stats/status?id=${encodeURIComponent(jobId)}`,
            { activity: false },
            decodeConditionalStatsStatusResponse,
          );
          if (seq !== S.conditionalStatsSeq || epoch !== S.sheetEpoch || !this.d.S.showStyles) return;
          if (st.status === 'done') {
            S.conditionalStatsJob = null;
            this.updateConditionalStatsUI('done');
            this.refreshViewportForConditionalStats(epoch);
            return;
          }
          if (st.status === 'cancelled') {
            S.conditionalStatsJob = null;
            this.updateConditionalStatsUI('cancelled', '条件统计已取消', { canRetry: true });
            this.refreshViewportForConditionalStats(epoch);
            return;
          }
          if (st.status === 'error') {
            S.conditionalStatsJob = null;
            this.updateConditionalStatsUI(
              'error',
              st.error ? `条件统计失败：${st.error}` : '条件统计失败',
              { canRetry: true },
            );
            this.refreshViewportForConditionalStats(epoch);
            return;
          }
          const label = st.damaged ? '条件统计重建中' : '条件统计计算中';
          const pct =
            st.totalRows > 0
              ? ` ${Math.min(99, Math.floor((st.doneRows * 100) / st.totalRows))}%`
              : '';
          const rowsText =
            st.totalRows > 0
              ? `（${st.doneRows.toLocaleString()} / ${st.totalRows.toLocaleString()} 行）`
              : '';
          this.updateConditionalStatsUI(st.status || 'running', `${label}${pct}${rowsText}`, {
            canCancel: true,
          });
          S.conditionalStatsTimer = window.setTimeout(() => {
            void poll();
          }, 280);
        } catch (error: unknown) {
          if (seq !== S.conditionalStatsSeq || epoch !== S.sheetEpoch || !this.d.S.showStyles) return;
          S.conditionalStatsJob = null;
          this.updateConditionalStatsUI(
            'error',
            `条件统计状态读取失败：${errorMessage(error)}`,
            { canRetry: true },
          );
        }
      };
      S.conditionalStatsTimer = window.setTimeout(() => {
        void poll();
      }, 120);
    } catch (error: unknown) {
      if (seq !== S.conditionalStatsSeq || epoch !== S.sheetEpoch) return;
      S.conditionalStatsJob = null;
      if (this.d.S.showStyles) {
        this.updateConditionalStatsUI(
          'error',
          `条件统计启动失败：${errorMessage(error)}`,
          { canRetry: true },
        );
      }
    }
  }
}
