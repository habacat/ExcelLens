import { test } from 'node:test';
import assert from 'node:assert/strict';
import type {
  AnalysisField,
  AnalysisQuery,
  AnalysisReport,
  AnalysisResult,
  AnalysisWorkspaceResponse,
  CompositionDefinition,
} from '../contracts/analysis.ts';
import type { ApiRequestOptions } from '../contracts/common.ts';
import { createFileFormats } from '../core/formats.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import { makeTestFileFormatsConfig } from '../test-helpers/grid-fixture.ts';
import { restoreReportQuery, type EditableAnalysisQuery } from './pivot-view.ts';
import { AnalysisResultCache } from './results.ts';
import { AnalysisWorkspace } from './workspace.ts';

function makeResult(grandDisplay = '0'): AnalysisResult {
  return {
    rowFields: [],
    columnKeys: [],
    rows: [],
    totals: [],
    grand: { display: grandDisplay, exact: grandDisplay, count: 1 },
    scanned: 1,
    included: 1,
    invalidNumbers: 0,
    missingFormulaCaches: 0,
    aggregation: 'count',
  };
}

function setup() {
  const dom = installDom({ fromIndexHtml: true });
  const root = document.getElementById('analysisRoot');
  assert.ok(root);

  const exportFormat = document.getElementById('analysisExportFormat');
  assert.ok(exportFormat instanceof HTMLSelectElement);
  exportFormat.value = 'csv';

  const exports: string[] = [];
  const materialized: unknown[] = [];
  const shown: AnalysisResult[] = [];
  const messages: string[] = [];
  const postRequests: { readonly path: string; readonly body: unknown }[] = [];

  let apiImpl: (
    url: string,
    options?: ApiRequestOptions,
    mode?: string,
  ) => Promise<unknown> = async (url, _opts, mode) => {
    if (mode === 'response') {
      exports.push(url);
      return { ok: true, status: 200, blob: async () => new Blob(['csv']) };
    }
    return {};
  };
  let postImpl: (path: string, body: unknown) => Promise<unknown> = async (path, body) => {
    postRequests.push({ path, body });
    const rec = body as { jobId?: string };
    if (path === '/api/analysis/materialize') materialized.push(rec.jobId);
    return { id: 'saved' };
  };
  let askTextImpl: (title: string, initial?: string, hint?: string) => Promise<string | null> =
    async () => '快照';

  const ws = new AnalysisWorkspace(root, {
    api: async <T = unknown>(
      url: string,
      options?: ApiRequestOptions,
      mode?: 'json' | 'text' | 'blob' | 'response' | ((value: unknown) => unknown),
    ): Promise<T> => {
      if (url.startsWith('/api/recovery')) {
        const snap: unknown = { revision: 0, records: {} };
        return snap as T;
      }
      const value = await apiImpl(url, options, typeof mode === 'function' ? undefined : mode);
      return (typeof mode === 'function' ? mode(value) : value) as T;
    },
    post: async (path: string, body: unknown): Promise<unknown> =>
      postImpl(path, body),
    formats: createFileFormats(makeTestFileFormatsConfig()),
    getSourceContext: () => ({
      workbookKey: 'book',
      book: 'path',
      source: 'wb:test',
      filterId: '',
      viewId: 'view',
      excludeThroughRow: 0,
    }),
    askText: (title, initial, hint) => askTextImpl(title, initial, hint),
    downloadBlob: () => undefined,
    onToast: (message) => {
      messages.push(message);
    },
  });

  ws.state.fingerprint = 'book';
  ws.state.book = 'path';
  ws.state.opened = true;
  ws.state.openSerial = 1;
  ws.state.jobID = 'previous-job';
  ws.state.workspace = {
    sheets: [],
    reports: [],
    dashboardOrder: null,
    sourceSheets: [{ name: 'test' }],
    workbook: 'test.xlsx',
    fingerprint: 'book',
  };

  ws.loadFields = async () => true;
  ws.refreshWorkspace = async () => true;
  ws.pivotView.renderResult = (res) => {
    shown.push(res);
  };

  return {
    ws,
    cache: ws.state.reportCache,
    exports,
    materialized,
    shown,
    messages,
    postRequests,
    setApi: (fn: typeof apiImpl) => {
      apiImpl = fn;
    },
    setPost: (fn: typeof postImpl) => {
      postImpl = fn;
    },
    setAskText: (fn: typeof askTextImpl) => {
      askTextImpl = fn;
    },
    restore: () => {
      ws.close();
      dom.restore();
    },
  };
}

test('cached report open, export and result sheet use the same result task after visiting another report', async () => {
  const h = setup();
  try {
    const a: AnalysisReport = {
      id: 'a',
      name: 'A',
      updated: '',
      query: { source: 'wb:test', rows: ['C1'], aggregate: 'count' },
    };
    const b: AnalysisReport = {
      id: 'b',
      name: 'B',
      updated: '',
      query: { source: 'wb:test', rows: ['C2'], aggregate: 'count' },
    };
    h.cache.set('book', a.query, { job: 'task-a', result: makeResult('11') });
    h.cache.set('book', b.query, { job: 'task-b', result: makeResult('99') });

    await h.ws.openReport(b);
    await h.ws.openReport(a);
    await h.ws.exportCSV();
    await h.ws.materialize();

    assert.equal(h.ws.state.activeResult?.grand.display, '11');
    assert.equal(h.ws.state.jobID, 'task-a');
    assert.ok(h.exports[0]?.includes('id=task-a&'));
    assert.deepEqual(h.materialized, ['task-a']);

    h.setApi(async () => ({ ok: false, status: 404 }));
    await h.ws.exportCSV();
    assert.ok(h.messages.at(-1)?.includes('重新运行'));
    assert.equal(h.ws.state.jobID, 'task-a');
  } finally {
    h.restore();
  }
});

test('displayed result captions remain bound to the completed source after builder fields change', () => {
  const h = setup();
  try {
    const query: AnalysisQuery = {
      source: 'wb:original',
      rows: ['C1'],
      aggregate: 'sum',
      transforms: [{ id: 'D_amount', name: '原转换金额', source: 'C2', op: 'number' }],
    };
    const fields: AnalysisField[] = [
      { id: 'C1', name: '原分类', column: 1 },
      { id: 'C2', name: '原金额', column: 2 },
    ];
    const snapshot = h.cache.set('book', query, {
      job: 'done',
      result: makeResult(),
      sourceFields: fields,
    });
    (fields[0] as { name: string }).name = 'changed';
    h.ws.activateResult(snapshot);
    h.ws.state.fields = [{ id: 'C1', name: '新来源分类', column: 1 }];
    h.ws.state.query = {
      ...restoreReportQuery(query),
      transforms: [{ id: 'D_amount', name: '新转换', source: 'C2', op: 'number' }],
    };
    assert.equal(h.ws.pivotView.resultFieldName('C1'), '原分类');
    assert.equal(h.ws.pivotView.resultFieldName('D_amount'), '原转换金额');
    assert.equal(h.ws.state.activeResultFields[1]?.name, '原金额');
  } finally {
    h.restore();
  }
});

test('a delayed older report open cannot replace the latest report or its task', async () => {
  const h = setup();
  try {
    const a: AnalysisReport = {
      id: 'a',
      name: 'A',
      updated: '',
      query: { source: 'wb:a', rows: [], aggregate: 'count' },
    };
    const b: AnalysisReport = {
      id: 'b',
      name: 'B',
      updated: '',
      query: { source: 'wb:b', rows: [], aggregate: 'count' },
    };
    h.cache.set('book', a.query, { job: 'a', result: makeResult('a') });
    h.cache.set('book', b.query, { job: 'b', result: makeResult('b') });

    let release: (v: boolean) => void = () => undefined;
    h.ws.loadFields = () =>
      new Promise<boolean>((resolve) => {
        release = resolve;
      });
    const earlier = h.ws.openReport(a);
    h.ws.loadFields = async () => true;
    await h.ws.openReport(b);
    release(true);
    await earlier;
    assert.equal(h.ws.state.jobID, 'b');
    assert.deepEqual(
      h.shown.map((x) => x.grand.display),
      ['b'],
    );
  } finally {
    h.restore();
  }
});

test('result cache keeps workbook identity, query snapshots, LRU and retained byte limits', () => {
  const cache = new AnalysisResultCache({ maxEntries: 2, maxBytes: 4000 });
  const q: AnalysisQuery = { source: 'wb:x', rows: [], aggregate: 'count' };
  const snapshot = cache.set('one', q, { job: '1', result: makeResult('same name') });
  q.source = 'changed';
  assert.equal(snapshot.query.source, 'wb:x');
  assert.equal(cache.get('two', { source: 'wb:x', rows: [], aggregate: 'count' }), null);

  cache.set('two', { source: 'wb:x', rows: [], aggregate: 'count' }, { job: '2', result: makeResult() });
  assert.equal(cache.get('one', { source: 'wb:x', rows: [], aggregate: 'count' })?.job, '1');

  cache.set('three', { source: 'wb:z', rows: [], aggregate: 'count' }, { job: '3', result: makeResult() });
  assert.equal(cache.get('two', { source: 'wb:x', rows: [], aggregate: 'count' }), null);

  const huge = cache.set(
    'huge',
    { source: 'wb:h', rows: [], aggregate: 'count' },
    { job: 'huge', result: makeResult('文'.repeat(4000)) },
  );
  assert.equal(huge.job, 'huge');
  assert.equal(cache.get('huge', { source: 'wb:h', rows: [], aggregate: 'count' }), null);
  assert.ok(cache.bytes <= 4000);

  cache.clear();
  assert.equal(cache.bytes, 0);
  assert.equal(cache.size, 0);
});

test('saved reports restore omitted arrays and zero-valued display settings without changing stored queries', () => {
  const h = setup();
  try {
    const saved = JSON.parse(
      '{"source":"wb:test","rows":null,"aggregate":"sum","value":"C1"}',
    ) as AnalysisQuery;
    const q = restoreReportQuery(saved);
    assert.ok(Array.isArray(q.rows));
    assert.ok(Array.isArray(q.columns));
    assert.ok(Array.isArray(q.metrics));
    assert.ok(Array.isArray(q.filters));
    assert.ok(Array.isArray(q.transforms));
    assert.equal(q.top, 0);
    assert.equal(q.precision, 0);
    assert.equal(q.aggregate, 'sum');
    assert.equal(q.value, 'C1');
    assert.equal(Reflect.get(saved, 'rows'), null);
    assert.ok(!('precision' in saved));

    h.ws.activateResult(h.cache.set('book', saved, { job: 'zero', result: makeResult() }));
    assert.equal(h.ws.state.activeResultQuery?.rows.length, 0);
    assert.equal(h.ws.state.activeResultQuery.precision, 0);
  } finally {
    h.restore();
  }
});

test('composition reports preserve the header-zero boundary and their complete immutable descriptor', () => {
  const definition: CompositionDefinition = {
    version: 1,
    name: '联合来源',
    fields: ['金额'],
    inputs: [
      { ref: { book: 'first', fingerprint: 'a', source: 'wb:1', headerRow: 1 }, columns: [2] },
      { ref: { book: 'second', fingerprint: 'b', source: 'wb:2', headerRow: 1 }, columns: [1] },
    ],
  };
  const saved: AnalysisQuery = {
    source: 'append:descriptor',
    composition: definition,
    rows: [],
    aggregate: 'sum',
    value: 'C1',
  };
  const restored = restoreReportQuery(saved);
  assert.equal(restored.headerRow, 0);
  assert.equal(restored.composition?.inputs[1]?.columns[0], 1);
  const mutableCols = restored.composition.inputs[1].columns as number[];
  mutableCols[0] = 4;
  assert.equal(definition.inputs[1]?.columns[0], 1);
});

test('plan adoption preserves the visible result when schema identity changed after completion', async () => {
  const h = setup();
  try {
    const previous = makeResult('previous');
    h.ws.state.activeResult = previous;
    h.ws.state.source = 'wb:old';
    h.setApi(async () => ({ sourceStamp: 'edited', fields: [], preview: [], totalRows: 0, headerRow: 1, truncatedFields: false }));
    const completed = {
      job: 'plan',
      result: {
        status: 'done' as const,
        stage: '',
        progress: 1,
        total: 1,
        error: '',
        query: { source: 'scratch:target', rows: ['C1'], aggregate: 'count' as const },
        result: makeResult('new'),
      },
      current: () => true,
    };
    await assert.rejects(
      h.ws.adoptAnalysisPlan(completed, { ref: { source: 'scratch:target' }, stamp: 'completed' }, () => true),
      /发生变化/,
    );
    assert.equal(h.ws.state.activeResult, previous);
    assert.equal(h.ws.state.source, 'wb:old');
    assert.equal(h.ws.state.jobID, 'previous-job');
  } finally {
    h.restore();
  }
});

test('a delayed plan schema response cannot change a newer result intent', async () => {
  const h = setup();
  try {
    const previous = makeResult('previous');
    h.ws.state.activeResult = previous;
    let release: (v: unknown) => void = () => undefined;
    h.setApi(() => new Promise((resolve) => {
      release = resolve;
    }));
    const pending = h.ws.adoptAnalysisPlan(
      {
        job: 'plan',
        result: {
          status: 'done',
          stage: '',
          progress: 1,
          total: 1,
          error: '',
          query: { source: 'wb:new', rows: [], aggregate: 'count' },
          result: makeResult(),
        },
        current: () => true,
      },
      { ref: { source: 'wb:new' }, stamp: 'stamp' },
      () => true,
    );
    await Promise.resolve();
    h.ws.state.reportOpenSerial++;
    release({ sourceStamp: 'stamp', fields: [], preview: [], totalRows: 0, headerRow: 1, truncatedFields: false });
    assert.equal(await pending, false);
    assert.equal(h.ws.state.activeResult, previous);
    assert.equal(h.ws.state.jobID, 'previous-job');
  } finally {
    h.restore();
  }
});

test('failed scoped report restoration keeps the completed result instead of querying the full source', async () => {
  const h = setup();
  try {
    const previous = makeResult('old');
    const scope = {
      version: 1,
      mode: 'selection' as const,
      fingerprint: 'book',
      selection: { startRow: 2, endRow: 3, startColumn: 2, endColumn: 2 },
    };
    h.ws.state.activeResult = previous;
    h.ws.loadFields = async () => false;
    await h.ws.openReport({
      id: 'scoped',
      name: 'Scoped',
      updated: '',
      query: { source: 'wb:test', rows: [], aggregate: 'count', sourceScope: scope },
    });
    assert.equal(h.ws.state.activeResult, previous);
    assert.equal(h.ws.state.jobID, 'previous-job');
    assert.equal(h.ws.state.query?.sourceScope?.mode, 'selection');
    assert.equal(h.shown.length, 0);
  } finally {
    h.restore();
  }
});

test('reports using different scopes never share a cached result', () => {
  const cache = new AnalysisResultCache();
  const full: AnalysisQuery = { source: 'wb:test', rows: [], aggregate: 'count' };
  const visible: AnalysisQuery = {
    ...full,
    sourceScope: { version: 1, mode: 'visible', fingerprint: 'book' },
  };
  cache.set('book', full, { job: 'full', result: makeResult() });
  cache.set('book', visible, { job: 'visible', result: makeResult() });
  assert.equal(cache.get('book', full)?.job, 'full');
  assert.equal(cache.get('book', visible)?.job, 'visible');
});

function savingHarness() {
  const h = setup();
  // Restore real refreshWorkspace (setup stubbed it)
  h.ws.refreshWorkspace = AnalysisWorkspace.prototype.refreshWorkspace.bind(h.ws);
  h.ws.state.source = 'wb:test';
  h.ws.state.tab = 'pivot';
  h.ws.state.activeResultQuery = restoreReportQuery({
    source: 'wb:test',
    rows: [],
    aggregate: 'count',
  });
  h.ws.state.activeResult = makeResult('17');
  h.ws.state.activeReportID = '';

  const renders: (readonly string[])[] = [];
  h.ws.dashboardController.renderDashboard = () => {
    assert.ok(
      h.ws.state.activeResultQuery &&
        h.ws.state.reportCache.get('book', h.ws.state.activeResultQuery),
    );
    renders.push((h.ws.state.workspace?.reports ?? []).map((r) => r.id));
  };

  const workspace = (ids: readonly string[]): AnalysisWorkspaceResponse => ({
    fingerprint: 'book',
    workbook: 'test.xlsx',
    dashboardOrder: null,
    sourceSheets: [{ name: 'test' }],
    sheets: [],
    reports: ids.map((id) => ({
      id,
      name: id,
      updated: '',
      query: h.ws.state.activeResultQuery as EditableAnalysisQuery,
    })),
  });

  return { ...h, renders, workspace };
}

test('saving then opening dashboard during a delayed catalogue refresh renders the saved result after adoption', async () => {
  const h = savingHarness();
  try {
    let release: (v: AnalysisWorkspaceResponse) => void = () => undefined;
    let entered: () => void = () => undefined;
    const waiting = new Promise<void>((resolve) => {
      entered = resolve;
    });
    h.setApi(() => {
      entered();
      return new Promise<AnalysisWorkspaceResponse>((resolve) => {
        release = resolve;
      });
    });
    h.setPost(async () => ({ id: 'saved-report' }));

    const pending = h.ws.saveReport();
    await waiting;
    h.ws.state.tab = 'dashboard';
    assert.ok(h.ws.state.activeResultQuery);
    assert.equal(
      h.ws.state.reportCache.get('book', h.ws.state.activeResultQuery)?.job,
      'previous-job',
    );
    release(h.workspace(['saved-report']));
    await pending;
    assert.deepEqual(h.renders, [['saved-report']]);
    assert.equal(h.ws.state.activeReportID, 'saved-report');
  } finally {
    h.restore();
  }
});

test('older workspace refreshes and responses from a previous opening cannot replace the latest report catalogue', async () => {
  const h = savingHarness();
  try {
    const releases: ((v: AnalysisWorkspaceResponse) => void)[] = [];
    h.setApi(
      () =>
        new Promise<AnalysisWorkspaceResponse>((resolve) => {
          releases.push(resolve);
        }),
    );
    h.ws.state.tab = 'dashboard';
    assert.ok(h.ws.state.activeResultQuery);
    h.cache.set('book', h.ws.state.activeResultQuery, { job: 'cached', result: makeResult() });

    const earlier = h.ws.refreshWorkspace();
    const latest = h.ws.refreshWorkspace();
    releases[1]?.(h.workspace(['new']));
    await latest;
    releases[0]?.(h.workspace(['old']));
    assert.equal(await earlier, false);
    assert.equal(h.ws.state.workspace?.reports[0]?.id, 'new');
    assert.deepEqual(h.renders, [['new']]);

    const reopened = h.ws.refreshWorkspace();
    h.ws.state.openSerial++;
    releases[2]?.(h.workspace(['obsolete-opening']));
    assert.equal(await reopened, false);
    assert.equal(h.ws.state.workspace.reports[0].id, 'new');
  } finally {
    h.restore();
  }
});

test('save captures the original report and source identities and never relabels a newer result', async () => {
  const h = savingHarness();
  try {
    const requests: Record<string, unknown>[] = [];
    let answer: (v: string | null) => void = () => undefined;
    h.ws.state.activeReportID = 'original';
    h.setAskText(
      () =>
        new Promise<string | null>((resolve) => {
          answer = resolve;
        }),
    );
    h.setPost(async (_path, body) => {
      requests.push(body as Record<string, unknown>);
      return { id: 'original' };
    });
    h.setApi(async () => h.workspace(['original']));

    const pending = h.ws.saveReport();
    h.ws.state.activeReportID = 'another';
    h.ws.state.activeResult = makeResult('99');
    h.ws.state.jobID = 'another-task';
    answer('原报表');
    await pending;
    assert.equal(requests[0]?.id, 'original');
    assert.equal(requests[0].workbookKey, 'book');
    assert.equal(requests[0].book, 'path');
    assert.equal(h.ws.state.activeReportID, 'another');
    assert.equal(h.ws.state.jobID, 'another-task');
  } finally {
    h.restore();
  }
});

test('save acknowledgements from a replaced source do not update the report catalogue or current UI', async () => {
  const h = savingHarness();
  try {
    let release: (v: unknown) => void = () => undefined;
    let started: () => void = () => undefined;
    const posted = new Promise<void>((resolve) => {
      started = resolve;
    });
    h.setPost(() => {
      started();
      return new Promise((resolve) => {
        release = resolve;
      });
    });
    h.setApi(async () => {
      throw new Error('obsolete save must not refresh');
    });
    const pending = h.ws.saveReport();
    await posted;
    h.ws.state.book = 'another-path';
    release({ id: 'old' });
    await pending;
    assert.equal(h.ws.state.activeReportID, '');
    assert.equal(h.cache.size, 0);
    assert.equal(h.messages.length, 0);
  } finally {
    h.restore();
  }
});
