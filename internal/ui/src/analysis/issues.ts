/**
 * Analysis issues inspection, extraction, and CSV export modal controller.
 */
import { interfaceLocale, t as localize } from '../core/i18n.ts';
import type {
  AnalysisIssue,
  AnalysisQuery,
  AnalysisResult,
} from '../contracts/analysis.ts';
import type { ApiClient, ToastFn } from '../contracts/common.ts';
import { columnName } from '../core/cell-address.ts';
import { errorMessage } from '../core/display-format.ts';
import { requireButton, requireDialog, requireEl, requireInput } from '../core/dom.ts';
import { decodeAnalysisIssuesMaterializeResponse } from '../core/http-decoders.generated.ts';

export interface AnalysisIssuesSnapshot {
  readonly book: string;
  readonly job: string;
  readonly query: AnalysisQuery;
  readonly result: AnalysisResult;
}

/** @httpResponse AnalysisIssuesMaterializeResponse */
export interface AnalysisIssuesMaterializeResponse {
  readonly id: string;
  readonly rows: number;
  readonly total: number;
  readonly truncated?: boolean;
}

export interface AnalysisIssuesOptions {
  readonly api: ApiClient;
  readonly identity: () => string;
  readonly toast: ToastFn;
  readonly navigate: (query: AnalysisQuery, issue: AnalysisIssue, ref: string) => void | Promise<void>;
  readonly askName: (title: string, initial?: string, hint?: string) => Promise<string | null>;
  readonly onMaterialized: (data: AnalysisIssuesMaterializeResponse) => Promise<void>;
  readonly startExport: (jobId: string, protectCSV: boolean) => void;
}

const KIND_LABELS: Readonly<Record<string, string>> = {
  conversion: localize('ui.issues.conversionFailure'),
  number: localize('ui.issues.notIncludedNumericalAggregations'),
  missing_cache: localize('ui.issues.formulaNotCached'),
};

export class AnalysisIssues {
  private readonly api: ApiClient;
  private readonly identity: () => string;
  private readonly toast: ToastFn;
  private readonly navigate: (
    query: AnalysisQuery,
    issue: AnalysisIssue,
    ref: string,
  ) => void | Promise<void>;
  private readonly askName: (
    title: string,
    initial?: string,
    hint?: string,
  ) => Promise<string | null>;
  private readonly onMaterialized: (data: AnalysisIssuesMaterializeResponse) => Promise<void>;
  private readonly startExport: (jobId: string, protectCSV: boolean) => void;

  public snapshot: AnalysisIssuesSnapshot | null = null;
  public readonly dialog: HTMLDialogElement;
  private busy = false;

  constructor(options: AnalysisIssuesOptions) {
    this.api = options.api;
    this.identity = options.identity;
    this.toast = options.toast;
    this.navigate = options.navigate;
    this.askName = options.askName;
    this.onMaterialized = options.onMaterialized;
    this.startExport = options.startExport;
    this.dialog = requireDialog('analysisIssuesDialog');

    requireButton('analysisIssuesOpen').addEventListener('click', () => this.open());
    requireButton('analysisIssuesClose').addEventListener('click', () => this.close());
    this.dialog.addEventListener('keydown', (event: KeyboardEvent) => {
      if (event.key === 'Escape') event.stopPropagation();
    });
    requireButton('analysisIssuesExtract').addEventListener('click', () => {
      void this.extract();
    });
    requireButton('analysisIssuesExport').addEventListener('click', () => {
      const snapshot = this.snapshot;
      if (!this.current(snapshot)) return;
      const protect = requireInput('analysisIssuesCSVGuard').checked;
      this.close();
      this.startExport(snapshot.job, protect);
    });
  }

  current(snapshot: AnalysisIssuesSnapshot | null): snapshot is AnalysisIssuesSnapshot {
    return snapshot !== null && snapshot === this.snapshot && snapshot.book === this.identity();
  }

  close(): void {
    if (this.dialog.open) this.dialog.close();
  }

  reset(): void {
    this.close();
    this.snapshot = null;
    requireButton('analysisIssuesOpen').hidden = true;
  }

  setSnapshot(snapshot: AnalysisIssuesSnapshot | null): void {
    if (snapshot?.job !== this.snapshot?.job) this.close();
    this.snapshot = snapshot;
    const total = this.total(snapshot?.result);
    const button = requireButton('analysisIssuesOpen');
    button.hidden = !total;
    button.textContent = localize('ui.issues.verificationItem', { total: total.toLocaleString(interfaceLocale()) });
    button.disabled = !total;
  }

  total(result: AnalysisResult | null | undefined): number {
    return (
      (result?.invalidNumbers ?? 0) +
      (result?.invalidConversions ?? 0) +
      (result?.missingFormulaCaches ?? 0)
    );
  }

  open(): void {
    const snapshot = this.snapshot;
    if (!this.current(snapshot)) return;
    const result = snapshot.result;
    const issues = result.issues ?? [];
    const total = this.total(result);
    requireEl('analysisIssuesSummary').textContent =
      localize('ui.issues.totalItemsDisplayedItemsShown', { total: total.toLocaleString(interfaceLocale()), count: issues.length, detail: result.issuesTruncated ? localize('ui.issues.example') : '' });
    requireEl('analysisIssuesTextNote').textContent =
      localize('ui.issues.originalTextCanDisplayUp');

    const table = requireEl('analysisIssuesTable');
    table.replaceChildren();
    const head = document.createElement('thead');
    const tr = document.createElement('tr');
    for (const label of [localize('ui.issues.sourceLocation'), localize('ui.issues.participatingFields'), localize('ui.common.categories'), localize('ui.issues.originalFragment'), localize('ui.issues.stepInput'), localize('ui.issues.currentValue'), localize('ui.issues.reason')]) {
      const th = document.createElement('th');
      th.textContent = label;
      tr.append(th);
    }
    head.append(tr);
    table.append(head);

    const body = document.createElement('tbody');
    for (const issue of issues) {
      const row = document.createElement('tr');
      const position = document.createElement('td');
      const jump = document.createElement('button');
      jump.type = 'button';
      jump.className = 'analysis-drill-link';
      jump.textContent = (issue.sourceColumn ? columnName(issue.sourceColumn) : `${localize('ui.issues.row')} `) + String(issue.row);
      jump.title = localize('ui.issues.goSourceLocation');
      jump.onclick = (): void => {
        if (this.current(snapshot)) {
          this.close();
          void this.navigate(
            snapshot.query,
            issue,
            `${columnName(issue.sourceColumn || 1)}${issue.row}`,
          );
        }
      };
      if (issue.origin) {
        const source = document.createElement('div');
        source.className = 'composition-origin';
        source.textContent = `${issue.origin.file} / ${issue.origin.sheet}`;
        position.append(source);
      }
      position.append(jump);
      row.append(position);

      const cells: readonly string[] = [
        issue.fieldName || issue.field,
        KIND_LABELS[issue.kind] ?? issue.kind,
        issue.original,
        issue.input,
        issue.value,
        issue.reason + (issue.textTruncated ? localize('ui.issues.contentTruncated') : ''),
      ];
      for (const text of cells) {
        const td = document.createElement('td');
        td.textContent = text;
        row.append(td);
      }
      body.append(row);
    }
    table.append(body);
    requireButton('analysisIssuesExtract').disabled = this.busy;
    requireButton('analysisIssuesExport').disabled = this.busy;
    if (!this.dialog.open) this.dialog.showModal();
  }

  async extract(): Promise<void> {
    const snapshot = this.snapshot;
    if (this.busy || !this.current(snapshot)) return;
    // Close the native modal before opening the app's naming prompt.
    this.close();
    const name = await this.askName(localize('ui.issues.extractExceptionVerificationForm'), localize('ui.issues.exceptionalCheckOn', { value: new Date().toLocaleDateString('zh-CN') }));
    if (!name || !this.current(snapshot)) return;
    this.busy = true;
    try {
      const data = await this.api('/api/analysis/issues/materialize', {
        method: 'POST',
        body: JSON.stringify({ jobId: snapshot.job, name, limit: 10000 }),
      }, decodeAnalysisIssuesMaterializeResponse);
      if (!this.current(snapshot)) return;
      await this.onMaterialized(data);
      this.toast(
        localize('ui.issues.itemsHaveExtractedFrom', { rows: data.rows.toLocaleString(interfaceLocale()), total: data.total.toLocaleString(interfaceLocale()), detail: data.truncated ? localize('ui.issues.ifCapacityLimitReachedExport') : '' }),
        data.truncated ? 'warning' : 'success',
      );
    } catch (error: unknown) {
      if (this.current(snapshot)) this.toast(errorMessage(error), 'error');
    } finally {
      this.busy = false;
    }
  }
}
