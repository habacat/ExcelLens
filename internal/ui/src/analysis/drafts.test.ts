import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { AnalysisCellChange } from '../contracts/analysis.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import { AnalysisDrafts } from './drafts.ts';
import { AnalysisResultCache } from './results.ts';
import { AnalysisSheetsController, type AnalysisSheetsState } from './sheets.ts';

const tick = (): Promise<void> =>
  new Promise((resolve) => {
    setImmediate(resolve);
  });

interface PostCall {
  readonly path: string;
  readonly body: Record<string, unknown>;
}

function setup(override?: (path: string, body: Record<string, unknown>) => unknown) {
  const dom = installDom({ fromIndexHtml: true });
  const drafts = new AnalysisDrafts();
  const state: AnalysisSheetsState = {
    opened: true,
    tab: 'sheets',
    fingerprint: 'book',
    book: 'book-path',
    loadedSheet: 'first',
    sheetPage: 0,
    sheetLoadSeq: 0,
    sheetAbortController: null,
    sheetRevisions: new Map([['first', 0]]),
    undoHistory: new Map(),
    gridData: null,
    sheetCell: null,
    reportCache: new AnalysisResultCache(),
    workspace: {
      sheets: [
        {
          id: 'first',
          name: '第一张表',
          columns: ['x'],
          rows: 10,
          updated: '2026-11-27T00:00:00Z',
          revision: 0,
        },
      ],
    },
  };
  const calls: PostCall[] = [];
  const messages: string[] = [];

  const sheets = new AnalysisSheetsController({
    state,
    drafts,
    api: async <T = unknown>() => {
      const empty: unknown = {};
      return empty as T;
    },
    post: async (path: string, body: unknown): Promise<unknown> => {
      const payload = (typeof body === 'object' && body !== null
        ? body
        : {}) as Record<string, unknown>;
      calls.push({ path, body: payload });
      const res = override ? await override(path, payload) : { revision: 1, previous: [] };
      return res;
    },
    toast: (message) => {
      messages.push(message);
    },
    askText: async () => null,
    setText: (id, text) => {
      const el = document.getElementById(id);
      if (el) el.textContent = text;
    },
    fmtNum: (n) => String(n),
    refreshWorkspace: async () => true,
    setTab: () => undefined,
    open: async () => undefined,
  });
  sheets.loadSheet = async () => undefined;
  sheets.renderSheets = async () => undefined;

  return {
    sheets,
    state,
    drafts,
    calls,
    messages,
    restore: () => dom.restore(),
  };
}

test('restored drafts wait for a deliberate retry and validate the whole batch before adding cells', () => {
  const h = setup();
  try {
    const draft = { book: 'book', id: 'first', row: 0, col: 0, value: '恢复输入' };
    const key = h.drafts.key('book', 'first', 0, 0);
    h.drafts.restore({ [key]: draft });
    assert.equal(h.calls.length, 0);
    assert.equal(h.drafts.get('book', 'first', 0, 0)?.value, '恢复输入');
    assert.equal(h.sheets.hasUnsaved(), true);

    h.drafts.stage('book', 'first', [{ row: 0, col: 0, value: '本页新输入' }]);
    h.drafts.restore({ [key]: draft });
    assert.equal(h.drafts.get('book', 'first', 0, 0)?.value, '本页新输入');

    const other = h.drafts.key('book', 'first', 1, 0);
    assert.throws(
      () => h.drafts.restore({ [other]: { ...draft, row: 1 }, bad: { ...draft, row: 10000 } }),
      /无效/,
    );
    assert.equal(h.drafts.get('book', 'first', 1, 0), undefined);
  } finally {
    h.restore();
  }
});

test('a recovered draft on the home page does not prevent opening its source or an unrelated workbook', () => {
  const h = setup();
  try {
    h.drafts.stage('book', 'first', [{ row: 0, col: 0, value: '恢复草稿' }]);
    h.state.currentBook = '';
    assert.equal(h.sheets.hasUnsaved(), false);
    h.state.currentBook = 'other';
    assert.equal(h.sheets.hasUnsaved(), false);
    assert.equal(h.drafts.size, 1);
    h.state.currentBook = 'book';
    assert.equal(h.sheets.hasUnsaved(), true);
  } finally {
    h.restore();
  }
});

test('an earlier save acknowledgement cannot erase a newer typed draft', async () => {
  let finish: (value: unknown) => void = () => undefined;
  const h = setup(
    () =>
      new Promise((resolve) => {
        finish = resolve;
      }),
  );
  try {
    const saving = h.sheets.commitBatch('first', [{ row: 0, col: 0, value: 'sent' }]);
    await tick();
    h.drafts.stage('book', 'first', [{ row: 0, col: 0, value: 'new text' }]);
    finish({ revision: 1, previous: [] });
    await saving;
    assert.equal(h.drafts.get('book', 'first', 0, 0)?.value, 'new text');
    assert.equal(h.sheets.hasUnsaved(), true);
    assert.equal(document.getElementById('analysisAutoSave')?.textContent, '1格未保存');
  } finally {
    h.restore();
  }
});

test('failed saves preserve all pasted cells and a deliberate retry clears only acknowledged drafts', async () => {
  let fail = true;
  const h = setup(() => {
    if (fail) throw new Error('disk unavailable');
    return { revision: 1, previous: [] };
  });
  try {
    await assert.rejects(
      h.sheets.commitBatch('first', [
        { row: 9, col: 0, value: '0001' },
        { row: 10, col: 0, value: 'line\r\nnext' },
      ]),
      /unavailable/,
    );
    assert.equal(h.drafts.size, 2);
    assert.equal(document.getElementById('analysisRetrySave')?.hidden, false);
    assert.equal(h.sheets.hasUnsaved(), true);

    fail = false;
    await h.sheets.retryDrafts();
    assert.equal(h.drafts.size, 0);
    assert.equal(h.sheets.hasUnsaved(), false);
    assert.equal(document.getElementById('analysisAutoSave')?.textContent, '已保存');
    const secondChanges = (h.calls[1]?.body.changes ?? []) as readonly AnalysisCellChange[];
    assert.deepEqual(
      secondChanges.map((x) => x.value),
      ['0001', 'line\r\nnext'],
    );
    assert.equal(h.calls[1]?.body.workbookKey, 'book');
  } finally {
    h.restore();
  }
});

test('queued append captures its original sheet and uses the revision after prior saves', async () => {
  let release: (value: unknown) => void = () => undefined;
  const h = setup((path) =>
    path.endsWith('/batch')
      ? new Promise((resolve) => {
          release = resolve;
        })
      : { revision: 5, total: 30 },
  );
  try {
    const saving = h.sheets.commitBatch('first', [{ row: 19, col: 0, value: 'keep' }]);
    await tick();
    const adding = h.sheets.addRows();
    h.state.loadedSheet = 'other';
    release({ revision: 4, previous: [] });
    await saving;
    await adding;
    assert.equal(h.calls[1]?.path, '/api/analysis/sheet/rows');
    assert.equal(h.calls[1].body.id, 'first');
    assert.equal(h.calls[1].body.expectedRevision, 4);
    assert.equal(h.calls[1].body.count, 10);
    assert.equal(h.calls[1].body.row, undefined);
  } finally {
    h.restore();
  }
});

test('typing while a save fails is retained and workbook identities keep drafts separate', async () => {
  let rejectSave: (reason?: unknown) => void = () => undefined;
  const h = setup(
    () =>
      new Promise((_resolve, reject) => {
        rejectSave = reject;
      }),
  );
  try {
    const saving = h.sheets.commitBatch('first', [{ row: 0, col: 0, value: 'sent' }]);
    await tick();
    h.drafts.stage('book', 'first', [{ row: 0, col: 0, value: 'latest' }]);
    rejectSave(new Error('offline'));
    await assert.rejects(saving);
    h.drafts.stage('other', 'first', [{ row: 0, col: 0, value: 'different book' }]);
    assert.equal(h.drafts.get('book', 'first', 0, 0)?.value, 'latest');
    assert.equal(h.drafts.get('other', 'first', 0, 0)?.value, 'different book');
  } finally {
    h.restore();
  }
});

test('Unicode drafts retry within encoded request budgets, while invalid typed values stay recoverable', async () => {
  const h = setup();
  try {
    const value = '文'.repeat(4000);
    h.drafts.stage(
      'book',
      'first',
      Array.from({ length: 200 }, (_, row) => ({ row, col: 0, value })),
    );
    await h.sheets.retryDrafts();
    assert.equal(h.calls.length, 2);
    assert.equal(h.drafts.size, 0);
    for (const call of h.calls) {
      assert.ok(Buffer.byteLength(JSON.stringify(call.body), 'utf8') <= 2 * 1024 * 1024);
    }
    const typed = '😀'.repeat(4001);
    await assert.rejects(
      h.sheets.commitBatch('first', [{ row: 0, col: 0, value: typed }]),
      /4000/,
    );
    assert.equal(h.calls.length, 2);
    assert.equal(h.drafts.get('book', 'first', 0, 0)?.value, typed);
    assert.equal(h.sheets.hasUnsaved(), true);
  } finally {
    h.restore();
  }
});
