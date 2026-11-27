import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { ApiRequestOptions } from '../contracts/common.ts';
import type { SourceFilterRule } from '../contracts/analysis.ts';
import type { TaskCheckpointInfo } from '../contracts/features.ts';
import { searchKey, SheetController } from '../grid/sheet-controller.ts';
import { createInitialWorkbookState } from '../grid/state.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import { makeDefaultSheetControllerDeps } from '../test-helpers/grid-fixture.ts';

function setupAppRecovery() {
  const dom = installDom({ fromIndexHtml: true });
  const S = createInitialWorkbookState(1_048_576, 16_384);
  S.sheet = '数据';
  S.sheetEpoch = 1;
  S.book = 'book';
  S.workbookKey = 'hash';
  S.searchRequestID = 0;
  S.searchJob = null;
  S.searchSession = null;

  const calls: { readonly url: string; readonly body: Record<string, unknown> }[] = [];
  const polled: [string, number, boolean][] = [];
  let shown = 0;
  let apiImpl: (url: string, opts?: ApiRequestOptions) => Promise<unknown> = async (url, opts) => {
    const body =
      typeof opts?.body === 'string' ? (JSON.parse(opts.body) as Record<string, unknown>) : {};
    calls.push({ url, body });
    return { jobId: 'search-job' };
  };

  const deps = makeDefaultSheetControllerDeps({
    S,
    api: async <T = unknown>(url: string, opts?: ApiRequestOptions): Promise<T> =>
      (await apiImpl(url, opts)) as T,
    showWorkspace: () => {
      shown++;
    },
    openDialog: () => undefined,
    panels: {
      searchPanel: {
        leavePages: () => undefined,
        buildHits: () => undefined,
        applyHighlightClasses: () => undefined,
        displaySummary: () => undefined,
        renderResults: () => undefined,
        syncMode: () => undefined,
        poll: async (jobID, requestID, alreadyCached) => {
          polled.push([jobID, requestID, alreadyCached]);
        },
      },
      exportCenter: {
        adoptRecovered: () => true,
      },
    },
  });
  const controller = new SheetController(deps);

  (document.getElementById('searchMode') as HTMLSelectElement).value = 'value';
  (document.getElementById('searchScope') as HTMLSelectElement).value = 'current';
  (document.getElementById('searchInput') as HTMLInputElement).value = '金额';
  (document.getElementById('searchFull') as HTMLInputElement).checked = false;
  (document.getElementById('searchRecoverable') as HTMLInputElement).checked = false;

  return {
    controller,
    S,
    calls,
    polled,
    getShown: () => shown,
    setApi: (fn: typeof apiImpl) => {
      apiImpl = fn;
    },
    restore: () => {
      dom.restore();
    },
  };
}

test('explicit recoverable search bypasses in-page cache and binds both source identities', async () => {
  const h = setupAppRecovery();
  try {
    const key = searchKey('value', 'current', '金额', '数据');
    h.S.searchCache.set(key, { matches: [], truncated: false });
    h.S.searchSession = {
      key,
      sheet: '数据',
      input: '金额',
      query: '金额',
      mode: 'value',
      scope: 'current',
      full: false,
      status: 'done',
      matches: [],
      truncated: false,
      summary: '',
      selected: -1,
    };
    (document.getElementById('searchRecoverable') as HTMLInputElement).checked = true;

    await h.controller.runSearch();
    assert.ok(h.calls[0]);
    assert.equal(h.calls[0].body.recoverable, true);
    assert.equal(h.calls[0].body.book, 'book');
    assert.equal(h.calls[0].body.workbookKey, 'hash');
    assert.equal(h.polled[0]?.[0], 'search-job');
  } finally {
    h.restore();
  }
});

test('search startup delivered after source change is cancelled without taking the new session', async () => {
  const h = setupAppRecovery();
  try {
    let release: ((value: { readonly jobId: string }) => void) | undefined;
    h.setApi((url, opts) => {
      if (url === '/api/search/cancel') {
        const body =
          typeof opts?.body === 'string' ? (JSON.parse(opts.body) as Record<string, unknown>) : {};
        h.calls.push({ url, body });
        return Promise.resolve({});
      }
      return new Promise<{ readonly jobId: string }>((resolve) => {
        release = resolve;
      });
    });

    const pending = h.controller.runSearch();
    h.S.book = 'new';
    h.S.searchJob = 'new-query';
    h.S.searchSession = {
      key: 'k',
      sheet: '数据',
      input: 'new',
      query: 'new',
      mode: 'value',
      scope: 'current',
      full: false,
      status: 'running',
      matches: [],
      truncated: false,
      summary: '',
      selected: -1,
    };
    assert.ok(release);
    release({ jobId: 'late-query' });
    await pending;
    assert.equal(h.S.searchJob, 'new-query');
    assert.equal(h.S.searchSession.query, 'new');
    assert.deepEqual(
      h.calls.map((c) => c.body),
      [{ id: 'late-query' }],
    );
    assert.equal(h.polled.length, 0);
  } finally {
    h.restore();
  }
});

test('late cancellation does not invalidate a newer running search', async () => {
  const h = setupAppRecovery();
  try {
    h.S.searchJob = 'old';
    let release: ((value: unknown) => void) | undefined;
    h.setApi(
      () =>
        new Promise((resolve) => {
          release = resolve;
        }),
    );
    const pending = h.controller.cancelSearch('old');
    h.S.searchJob = 'new';
    h.S.searchSession = {
      key: 'k',
      sheet: '数据',
      input: '金额',
      query: '金额',
      mode: 'value',
      scope: 'current',
      full: false,
      status: 'running',
      matches: [],
      truncated: false,
      summary: '',
      selected: -1,
    };
    const request = h.S.searchRequestID;
    assert.ok(release);
    release({});
    await pending;
    assert.equal(h.S.searchJob, 'new');
    assert.equal(h.S.searchSession.status, 'running');
    assert.equal(h.S.searchRequestID, request);
  } finally {
    h.restore();
  }
});

test('sorting preserves the full committed advanced predicates including repeated columns', () => {
  const h = setupAppRecovery();
  try {
    const rules: SourceFilterRule[] = [
      { column: 2, operator: 'gt', value: '10' },
      { column: 2, operator: 'lt', value: '20' },
      { column: 3, operator: 'equals', value: 'keep' },
    ];
    h.S.lastRulesBySheet = new Map([['数据', rules]]);
    h.S.autoFilter = {
      row: 1,
      startCol: 1,
      endCol: 5,
      rules: new Map(),
      sortColumn: 0,
      descending: false,
    };
    h.S.filtered = true;
    assert.equal(h.controller.currentQueryRules(), rules);
    assert.equal(h.controller.currentQueryRules().length, 3);
    h.S.filtered = false;
    assert.equal(h.controller.currentQueryRules().length, 0);
  } finally {
    h.restore();
  }
});

test('resuming a query from the home screen reveals the workspace before taking the task', async () => {
  const h = setupAppRecovery();
  try {
    const item: TaskCheckpointInfo = {
      id: 'a'.repeat(32),
      revision: '1',
      kind: 'query',
      driver: 'source-search',
      title: '搜索',
      file: '账目.csv',
      path: 'C:/source/账目.csv',
      book: 'book',
      workbookKey: 'hash',
      sheet: '数据',
      query: '金额',
      mode: 'value',
      scope: 'current',
      updated: '2026-11-17T00:00:00Z',
      complete: false,
      busy: false,
      backup: false,
      error: '',
    };
    const adopted = await h.controller.adoptRecoveredTask(
      { jobId: 'restored', operation: 'search' },
      item,
    );
    assert.equal(adopted, true);
    assert.equal(h.getShown(), 1);
    assert.equal(h.S.searchJob, 'restored');
    assert.equal(h.polled[0]?.[0], 'restored');
  } finally {
    h.restore();
  }
});
