/**
 * Single narrow window runtime bridge permitted by architecture rules
 * (`tools/frontend/check-arch.mjs`). Exposes ONLY the read-only hooks required
 * by external headless Chromium QA and benchmark harnesses (`tools/qa/*`,
 * `tools/bench/release.cjs`). All internal application wiring uses ES imports.
 */
import type { SessionService } from '../contracts/common.ts';
import type { SchedulerStats } from '../grid/viewport-scheduler.ts';

export interface PerformanceSnapshot {
  readonly commits: number;
  readonly reusedRows: number;
  readonly createdRows: number;
  readonly createdCells: number;
  readonly lastMs: number;
  readonly virtualScrollScale: number;
  readonly currentRows: number;
  readonly visibleNodes: number;
  readonly bufferedTiles: number;
  readonly bufferedApproxBytes: number;
  readonly cachedSheetViews: number;
  readonly cachedSheetBytes: number;
  readonly sheetLoading: boolean;
  readonly conditionalStatsState: string;
  readonly conditionalStatsRunning: boolean;
  readonly hasPendingRequest: boolean;
  readonly foregroundHits: number;
  readonly foregroundLookups: number;
  readonly requests: SchedulerStats;
  readonly hasVisibleCoverage: boolean;
}

export interface AnalyticsDiagnosticsBridge {
  diagnostics(): Readonly<Record<string, unknown>>;
  refresh?(): Promise<boolean | undefined>;
}

declare global {
  interface Window {
    ExcelLensSession?: SessionService;
    ExcelLensPerformance?: () => PerformanceSnapshot;
    ExcelLensWorkbookReady?: Promise<void>;
    ExcelLensAnalytics?: AnalyticsDiagnosticsBridge;
  }
}

export function exposeSessionBridge(session: SessionService): void {
  window.ExcelLensSession = session;
}

export function exposePerformanceBridge(reader: () => PerformanceSnapshot): void {
  window.ExcelLensPerformance = reader;
}

export function exposeWorkbookReadyBridge(ready: Promise<void>): void {
  window.ExcelLensWorkbookReady = ready;
}

export function exposeAnalyticsDiagnosticsBridge(bridge: AnalyticsDiagnosticsBridge): void {
  window.ExcelLensAnalytics = bridge;
}
