import { decodeNamedViewFilterJobStatus } from '../core/http-decoders.generated.ts';
import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { ApiRequestOptions } from '../contracts/common.ts';
import type { NamedView } from '../contracts/features.ts';
import { AsyncJobs, type AsyncJobStatusEnvelope } from '../core/async-jobs.ts';
import { createInitialWorkbookState } from '../grid/state.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import {
  NamedViews,
  NamedViewsModel,
  type NamedViewFilterJobStatus,
} from './named-views.ts';

function deferred<T = unknown>() {
  let resolve: (value: T | PromiseLike<T>) => void = () => undefined;
  let reject: (reason?: unknown) => void = () => undefined;
  const promise = new Promise<T>((a, b) => {
    resolve = a;
    reject = b;
  });
  return { promise, resolve, reject };
}

const tick = (): Promise<void> =>
  new Promise((resolve) => {
    setImmediate(resolve);
  });

const sampleView: NamedView = {
  id: 'saved',
  name: '常用视图',
  ruleVersion: 1,
  created: '',
  updated: '',
  source: {
    fingerprint: 'one',
    format: 'xlsx',
    name: 'book.xlsx',
    sheet: '数据',
    sheetPath: '',
    headerRow: 1,
    columns: 2,
    fields: [],
  },
  query: { headerRow: 1, rules: [], sortKeys: [{ column: 1, descending: false, mode: 'natural' }] },
  config: {
    freeze: { rows: 1, cols: 0 },
    layout: { rows: {}, cols: {}, font: '', columnMode: 'original', columnVisibility: {} },
    table: null,
    zoom: 105,
    showStyles: true,
    showFormulas: false,
  },
};

function setupNamedViews(
  apiImpl: (url: string, options?: ApiRequestOptions) => Promise<unknown>,
) {
  const dom = installDom({ fromIndexHtml: true });
  const state = createInitialWorkbookState(1_048_576, 16_384);
  state.opened = true;
  state.indexReady = true;
  state.workbookKey = 'one';
  state.sheet = '数据';
  state.sheetEpoch = 1;

  const applied: unknown[][] = [];
  const instance = new NamedViews({
    state,
    api: async <T = unknown>(url: string, options?: ApiRequestOptions): Promise<T> =>
      (await apiImpl(url, options)) as T,
    toast: () => undefined,
    capture: () => ({
      sheet: '数据',
      filterId: '',
      headerRow: 1,
      config: sampleView.config,
    }),
    apply: async (prepared, data, current) => {
      applied.push([prepared, data, current]);
      return true;
    },
    canApply: () => true,
    supportsRowVisibility: () => false,
    sleep: async () => undefined,
  });

  const seedStore = async (items: readonly NamedView[] = [sampleView], revision = 1) => {
    const list = document.getElementById('namedViewList') as HTMLSelectElement;
    const prevApi = apiImpl;
    apiImpl = async () => ({ book: 'book', store: { version: 1, revision, items } });
    await instance.refresh();
    apiImpl = prevApi;
    if (items[0]) list.value = items[0].id;
  };

  return {
    instance,
    state,
    applied,
    seedStore,
    setApi: (fn: typeof apiImpl) => {
      apiImpl = fn;
    },
    restore: () => {
      instance.resetBook();
      dom.restore();
    },
  };
}

test('view summaries use bounded configuration facts without claiming cached data was restored', () => {
  assert.equal(NamedViewsModel.active(sampleView.query), true);
  assert.equal(NamedViewsModel.active({ headerRow: 1, rules: [], sortKeys: [] }), false);
  assert.match(NamedViewsModel.summary(sampleView), /1级排序.*105%/);
});

test('saved-view jobs use a sheet identity without placing Unicode sheet names in request headers', async () => {
  const calls: { readonly url: string; readonly opts: ApiRequestOptions | undefined }[] = [];
  let identity = '["one","数据",1]';
  const jobs = new AsyncJobs<
    NamedViewFilterJobStatus,
    AsyncJobStatusEnvelope<NamedViewFilterJobStatus> & NamedViewFilterJobStatus
  >({
      decodeStatus: decodeNamedViewFilterJobStatus,
    api: async <T = unknown>(url: string, opts?: ApiRequestOptions): Promise<T> => {
      calls.push({ url, opts });
      return (
        url.endsWith('/start')
          ? { jobId: 'filter' }
          : { status: 'done', filterId: 'filter', matchedRows: 3 }
      ) as T;
    },
    identity: () => identity,
    requestIdentity: () => 'one',
    sleep: async () => undefined,
    endpoint: '/api/filter',
    resultFromStatus: (status) => status,
  });
  const completed = await jobs.execute({ sortKeys: [{ column: 1 }] }, { main: true });
  const headers = calls[0]?.opts?.headers as Record<string, string> | undefined;
  assert.equal(headers?.['X-ExcelLens-Workbook'], 'one');
  assert.equal(completed.result.filterId, 'filter');
  assert.equal(completed.current(), true);
  identity = '["one","下一表",2]';
  assert.equal(completed.current(), false);
});

test('metadata arriving after sheet replacement cannot apply an old view', async () => {
  const gate = deferred();
  const h = setupNamedViews(() => gate.promise);
  try {
    await h.seedStore();
    const pending = h.instance.apply();
    h.state.sheet = '下一表';
    h.state.sheetEpoch++;
    gate.resolve({ view: sampleView });
    await pending;
    assert.equal(h.applied.length, 0);
    assert.equal(h.instance.busy, false);
  } finally {
    h.restore();
  }
});

test('cancelling an in-flight start cleans up its late job and preserves the current view', async () => {
  const gate = deferred();
  const cancelled: string[] = [];
  const h = setupNamedViews(async (url, options) => {
    if (url === '/api/named-views/resolve') return { view: sampleView };
    if (url === '/api/filter/start') return gate.promise;
    if (url === '/api/filter/cancel') {
      const body =
        typeof options?.body === 'string' ? (JSON.parse(options.body) as { id?: string }) : {};
      if (body.id) cancelled.push(body.id);
      return {};
    }
    throw new Error('obsolete job must not poll');
  });
  try {
    await h.seedStore();
    const pending = h.instance.apply();
    await tick();
    h.instance.cancel();
    gate.resolve({ jobId: 'late-view' });
    await pending;
    await tick();
    assert.deepEqual(cancelled, ['late-view']);
    assert.equal(h.applied.length, 0);
    assert.equal(h.instance.busy, false);
  } finally {
    h.restore();
  }
});

test('query failures leave the existing view and its saved configuration intact', async () => {
  const h = setupNamedViews(async (url) => {
    if (url === '/api/named-views') return { view: sampleView };
    throw new Error('sort disk budget exhausted');
  });
  try {
    await h.seedStore();
    await h.instance.apply();
    assert.equal(h.applied.length, 0);
    assert.match(document.getElementById('namedViewStatus')?.textContent ?? '', /disk budget/);
    assert.equal(h.instance.store?.revision, 1);
  } finally {
    h.restore();
  }
});

test('a view requiring row visibility never partly applies through a client without the capability', async () => {
  let queries = 0;
  const h = setupNamedViews(async (url) => {
    if (url === '/api/named-views/resolve') {
      return {
        view: {
          ...sampleView,
          ruleVersion: 2,
          config: { ...sampleView.config, rowVisibility: { mode: 'hide-all' } },
        },
      };
    }
    queries++;
    throw new Error('unsupported view must not run a query');
  });
  try {
    await h.seedStore();
    await h.instance.apply();
    assert.equal(queries, 0);
    assert.equal(h.applied.length, 0);
    assert.match(document.getElementById('namedViewStatus')?.textContent ?? '', /行显隐.*未应用/);
    assert.equal(h.instance.busy, false);
  } finally {
    h.restore();
  }
});

test('a successful save invalidates an older catalog response even if abort is ignored', async () => {
  const gate = deferred();
  const h = setupNamedViews(async (_url, options) => {
    if (options?.method === 'POST') {
      return { book: 'book', store: { version: 1, revision: 2, fingerprint: 'one', items: [sampleView] }, id: sampleView.id };
    }
    return gate.promise;
  });
  try {
    await h.seedStore([], 1);
    (document.getElementById('namedViewName') as HTMLInputElement).value = '新视图';
    const old = h.instance.refresh();
    await h.instance.save(false);
    gate.resolve({ book: 'book', store: { version: 1, revision: 1, fingerprint: 'one', items: [] } });
    await old;
    assert.ok(h.instance.store);
    assert.equal(h.instance.store.revision, 2);
    assert.equal(h.instance.store.items.length, 1);
  } finally {
    h.restore();
  }
});
