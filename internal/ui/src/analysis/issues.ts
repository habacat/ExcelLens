/**
 * Analysis issues inspection, extraction, and CSV export modal controller.
 */
import type {
  AnalysisIssue,
  AnalysisQuery,
  AnalysisResult,
} from '../contracts/analysis.ts';
import type { ApiClient, ToastFn } from '../contracts/common.ts';
import { columnName } from '../core/cell-address.ts';
import { errorMessage } from '../core/display-format.ts';
import { requireButton, requireDialog, requireEl, requireInput } from '../core/dom.ts';

export interface AnalysisIssuesSnapshot {
  readonly book: string;
  readonly job: string;
  readonly query: AnalysisQuery;
  readonly result: AnalysisResult;
}

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
  conversion: '转换失败',
  number: '未计入数值聚合',
  missing_cache: '公式缺缓存',
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
    button.textContent = `核对项目 ${total.toLocaleString()}`;
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
      `共 ${total.toLocaleString()} 项；显示 ${issues.length} 项${result.issuesTruncated ? '（样例）' : ''}。同一源行可能有多项。`;
    requireEl('analysisIssuesTextNote').textContent =
      '原文最多显示512字符；编辑表最多10,000项，完整原文可导出CSV。';

    const table = requireEl('analysisIssuesTable');
    table.replaceChildren();
    const head = document.createElement('thead');
    const tr = document.createElement('tr');
    for (const label of ['源位置', '参与字段', '类别', '原文片段', '步骤输入', '当前值', '原因']) {
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
      jump.textContent = (issue.sourceColumn ? columnName(issue.sourceColumn) : '行 ') + String(issue.row);
      jump.title = '转到来源位置';
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
        issue.reason + (issue.textTruncated ? '（内容已截取）' : ''),
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
    const name = await this.askName('提取异常核对表', `异常核对 ${new Date().toLocaleDateString('zh-CN')}`);
    if (!name || !this.current(snapshot)) return;
    this.busy = true;
    try {
      const data = await this.api<AnalysisIssuesMaterializeResponse>('/api/analysis/issues/materialize', {
        method: 'POST',
        body: JSON.stringify({ jobId: snapshot.job, name, limit: 10000 }),
      });
      if (!this.current(snapshot)) return;
      await this.onMaterialized(data);
      this.toast(
        `已提取 ${data.rows.toLocaleString()} / ${data.total.toLocaleString()} 项${data.truncated ? '；达到容量上限，完整清单请导出CSV' : ''}`,
        data.truncated ? 'warning' : 'success',
      );
    } catch (error: unknown) {
      if (this.current(snapshot)) this.toast(errorMessage(error), 'error');
    } finally {
      this.busy = false;
    }
  }
}
