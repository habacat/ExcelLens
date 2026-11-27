import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { AnalysisIssue, AnalysisQuery } from '../contracts/analysis.ts';
import type { ApiRequestOptions } from '../contracts/common.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import {
  AnalysisIssues,
  type AnalysisIssuesMaterializeResponse,
  type AnalysisIssuesSnapshot,
} from './issues.ts';

function setup() {
  const dom = installDom({ fromIndexHtml: true });
  let book = 'one';
  const navigations: (readonly [AnalysisQuery, AnalysisIssue, string])[] = [];
  const calls: { readonly path: string; readonly body: Record<string, unknown> }[] = [];
  const messages: (readonly [string, string | undefined])[] = [];
  const exports: (readonly [string, boolean])[] = [];
  const saved: AnalysisIssuesMaterializeResponse[] = [];
  let askNameImpl: (title: string, initial?: string, hint?: string) => Promise<string | null> =
    async () => '核对表';

  const panel = new AnalysisIssues({
    identity: () => book,
    api: async <T = unknown>(path: string, opts?: ApiRequestOptions): Promise<T> => {
      const body =
        typeof opts?.body === 'string'
          ? (JSON.parse(opts.body) as Record<string, unknown>)
          : {};
      calls.push({ path, body });
      const res: AnalysisIssuesMaterializeResponse = {
        id: 'new',
        rows: 10000,
        total: 15000,
        truncated: true,
      };
      return res as T;
    },
    toast: (msg, tone) => {
      messages.push([msg, tone]);
    },
    navigate: (query, issue, ref) => {
      navigations.push([query, issue, ref]);
    },
    askName: (title, initial, hint) => askNameImpl(title, initial, hint),
    onMaterialized: async (data) => {
      saved.push(data);
    },
    startExport: (jobId, protectCSV) => {
      exports.push([jobId, protectCSV]);
    },
  });

  const snapshot: AnalysisIssuesSnapshot = {
    book,
    job: 'job',
    query: { source: 'wb:数据', rows: [], aggregate: 'sum' },
    result: {
      rowFields: [],
      columnKeys: [],
      rows: [],
      totals: [],
      grand: { display: '0', exact: '0', count: 0 },
      scanned: 15000,
      included: 0,
      invalidNumbers: 15000,
      missingFormulaCaches: 0,
      aggregation: 'sum',
      issuesTruncated: true,
      issues: [
        {
          row: 103,
          sourceColumn: 28,
          sourceField: 'C28',
          field: 'D_x',
          fieldName: '转换金额',
          kind: 'number',
          original: '0001',
          input: '0001',
          value: '0001',
          reason: '源值为文本',
        },
      ],
    },
  };
  panel.setSnapshot(snapshot);

  return {
    panel,
    snapshot,
    navigations,
    calls,
    messages,
    exports,
    saved,
    setBook: (value: string) => {
      book = value;
    },
    setAskName: (fn: typeof askNameImpl) => {
      askNameImpl = fn;
    },
    restore: () => dom.restore(),
  };
}

test('issue samples retain source coordinates, show incomplete counts and do not start a full scan', () => {
  const h = setup();
  try {
    h.panel.open();
    const dialog = document.getElementById('analysisIssuesDialog');
    assert.ok(dialog instanceof HTMLDialogElement);
    assert.equal(dialog.open, true);
    assert.match(document.getElementById('analysisIssuesSummary')?.textContent ?? '', /15,000.*1.*样例/);
    assert.equal(h.calls.length, 0);

    const table = document.getElementById('analysisIssuesTable');
    assert.ok(table);
    const jump = table.querySelector('tbody tr td button');
    assert.ok(jump instanceof HTMLButtonElement);
    assert.equal(jump.textContent, 'AB103');
    jump.click();
    assert.equal(h.navigations[0]?.[2], 'AB103');
    assert.equal(dialog.open, false);
  } finally {
    h.restore();
  }
});

test('complete issue export uses the shared export center with an explicit CSV text choice', () => {
  const h = setup();
  try {
    h.panel.open();
    const guard = document.getElementById('analysisIssuesCSVGuard');
    const exportBtn = document.getElementById('analysisIssuesExport');
    const dialog = document.getElementById('analysisIssuesDialog');
    assert.ok(
      guard instanceof HTMLInputElement &&
        exportBtn instanceof HTMLButtonElement &&
        dialog instanceof HTMLDialogElement,
    );
    guard.checked = false;
    exportBtn.click();
    assert.deepEqual(h.exports, [['job', false]]);
    assert.equal(h.calls.length, 0);
    assert.equal(dialog.open, false);
  } finally {
    h.restore();
  }
});

test('bounded issue extraction reports truncation and never installs a different workbook result', async () => {
  const h = setup();
  try {
    await h.panel.extract();
    assert.equal(h.calls[0]?.body.jobId, 'job');
    assert.equal(h.calls[0].body.limit, 10000);
    assert.equal(h.saved[0]?.id, 'new');
    assert.match(h.messages[0]?.[0] ?? '', /容量上限/);

    let release: (value: string | null) => void = () => undefined;
    h.setAskName(
      () =>
        new Promise<string | null>((resolve) => {
          release = resolve;
        }),
    );
    const waiting = h.panel.extract();
    h.setBook('two');
    release('new');
    await waiting;
    assert.equal(h.calls.length, 1);
  } finally {
    h.restore();
  }
});
