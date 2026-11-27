import { test } from 'node:test';
import assert from 'node:assert/strict';
import type {
  AnalysisQueryStatusResponse,
  AnalysisResult,
  Recipe,
  RecipeField,
  SourceScope,
} from '../contracts/analysis.ts';
import type { ApiRequestOptions, HttpError } from '../contracts/common.ts';
import { AsyncJobs } from '../core/async-jobs.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import {
  RecipesController,
  RecipesModel,
  type RecipeContext,
  type RecipePreviewState,
} from './recipes.ts';

const tick = (): Promise<void> =>
  new Promise((resolve) => {
    setImmediate(resolve);
  });

function deferred<T>() {
  let resolve: (value: T | PromiseLike<T>) => void = () => undefined;
  let reject: (reason?: unknown) => void = () => undefined;
  const promise = new Promise<T>((a, b) => {
    resolve = a;
    reject = b;
  });
  return { promise, resolve, reject };
}

function makeField(overrides: Partial<RecipeField> & { readonly id: string; readonly mask: number }): RecipeField {
  return {
    column: 1,
    name: overrides.id,
    headerHash: '',
    headerLoaded: true,
    headerUnique: true,
    truncated: false,
    uncertain: false,
    ...overrides,
  };
}

const sampleRecipe: Recipe = {
  version: 1,
  id: 'plan',
  name: '月度核对',
  origin: {
    name: '原表',
    source: 'wb:数据',
    format: 'xlsx',
    fingerprint: 'one',
    headerRow: 1,
  },
  fields: [
    makeField({ id: 'C1', name: '项目', mask: 2 }),
    makeField({ id: 'C2', name: '金额', mask: 4, column: 2 }),
  ],
  steps: [],
  created: '2026-11-27T00:00:00Z',
  updated: '2026-11-27T00:00:00Z',
};

const preview: RecipePreviewState = {
  recipe: sampleRecipe,
  fields: [
    makeField({ id: 'C1', mask: 4 }),
    makeField({ id: 'C3', mask: 2, column: 3 }),
    makeField({ id: 'C4', mask: 0, column: 4 }),
    makeField({ id: 'C5', mask: 4, column: 5, uncertain: true }),
  ],
  matches: [],
  expectedTypes: {},
  targetTypes: {},
  sampled: 10,
  fieldsTruncated: false,
  ref: { book: 'book', source: 'wb:数据', headerRow: 1, stamp: 'target' },
  digest: 'digest',
  stamp: 'stamp',
  sourceId: '',
  target: { total: 10, stamp: 'target' },
};

interface MutableRecipeContext {
  bookKey: string;
  opened: boolean;
  intent: number;
  source: string;
  headerRow: number;
  sourceId: string;
  sourceScope?: SourceScope | null;
  resultJob: string;
  sources: { readonly value: string; readonly label: string }[];
}

function setup() {
  const dom = installDom({ fromIndexHtml: true });
  const c: MutableRecipeContext = {
    bookKey: 'one',
    opened: true,
    intent: 1,
    source: 'wb:数据',
    headerRow: 1,
    sourceId: '',
    resultJob: 'finished',
    sources: [{ value: 'wb:数据', label: '数据' }],
  };
  const adopted: unknown[][] = [];
  const calls: { readonly url: string; readonly data: Record<string, unknown> | undefined }[] = [];
  let apiImpl: (url: string, options?: ApiRequestOptions) => Promise<unknown> = async () => ({});

  const h = new RecipesController({
    api: async <T = unknown>(url: string, options?: ApiRequestOptions): Promise<T> => {
      const data =
        typeof options?.body === 'string'
          ? (JSON.parse(options.body) as Record<string, unknown>)
          : undefined;
      if (options?.method === 'POST') calls.push({ url, data });
      return (await apiImpl(url, options)) as T;
    },
    context: (): RecipeContext => c,
    capture: () => ({ jobId: c.resultJob, format: 'csv' }),
    adopt: async (...args) => {
      adopted.push(args);
      return true;
    },
    toast: () => undefined,
  });

  h.epoch = 1;
  h.opened = true;
  h.book = 'book';
  h.store = { version: 1, revision: 1, items: [] };
  h.dialog.showModal();
  const planName = document.getElementById('analysisPlanName');
  assert.ok(planName instanceof HTMLInputElement);
  planName.value = '月度核对';
  const origControls = h.controls.bind(h);
  h.controls = () => undefined;

  return {
    h,
    c,
    adopted,
    calls,
    origControls,
    setApi: (fn: typeof apiImpl) => {
      apiImpl = fn;
    },
    restore: () => dom.restore(),
  };
}

test('plan mapping keeps native text, amount text, empty samples and repeated targets distinct', () => {
  assert.equal(RecipesModel.compatible(makeField({ id: 'a', mask: 2 }), makeField({ id: 'b', mask: 4 })), false);
  assert.equal(RecipesModel.compatible(makeField({ id: 'a', mask: 1 }), makeField({ id: 'b', mask: 4 })), false);
  assert.equal(RecipesModel.compatible(makeField({ id: 'a', mask: 4 }), makeField({ id: 'b', mask: 0 })), true);
  assert.equal(
    RecipesModel.compatible(
      makeField({ id: 'a', mask: 4 }),
      makeField({ id: 'b', mask: 4, uncertain: true }),
    ),
    false,
  );
  assert.equal(RecipesModel.validateMapping(preview, { C1: 'C3', C2: 'C1' }), '');
  assert.match(RecipesModel.validateMapping(preview, { C1: 'C3' }), /逐项/);
  assert.match(RecipesModel.validateMapping(preview, { C1: 'C1', C2: 'C3' }), /不兼容/);
  assert.match(
    RecipesModel.validateMapping(
      {
        ...preview,
        recipe: {
          ...sampleRecipe,
          fields: [makeField({ id: 'C1', mask: 4 }), makeField({ id: 'C2', mask: 4 })],
        },
      },
      { C1: 'C1', C2: 'C1' },
    ),
    /一次/,
  );
});

test('catalog conflicts retain the full-input draft and retry only its commit', async () => {
  const { h, calls, setApi, restore } = setup();
  try {
    let captures = 0;
    let commits = 0;
    h.captureJobs.execute = async (request) => {
      captures++;
      const req = request as { jobId?: string; format?: string };
      assert.equal(req.jobId, 'finished');
      assert.equal(req.format, 'csv');
      return {
        job: 'cap-1',
        result: { captureId: 'validated' },
        sourceFields: undefined,
        query: undefined,
        current: () => true,
      };
    };
    setApi(async (_url, options) => {
      if (options?.method === 'POST') {
        if (!commits++) {
          const err: HttpError = new Error('目录已修改');
          err.status = 409;
          throw err;
        }
        return { id: 'plan', revision: 3 };
      }
      return { book: 'book', store: { version: 1, revision: 3, items: [] } };
    });

    await h.save();
    assert.equal(h.draft?.id, 'validated');
    assert.match(document.getElementById('analysisPlanStatus')?.textContent ?? '', /目录已修改/);
    assert.equal(h.busy, false);

    h.store = { version: 1, revision: 2, items: [] };
    const planName = document.getElementById('analysisPlanName');
    assert.ok(planName instanceof HTMLInputElement);
    planName.value = '新的名称';
    await h.save();
    assert.equal(captures, 1);
    assert.equal(calls.length, 2);
    assert.equal(calls[1]?.data?.id, 'validated');
    assert.equal(calls[1].data.expectedRevision, 2);
    assert.equal(calls[1].data.name, '新的名称');
    assert.equal(h.store.revision, 3);
    assert.equal(h.draft, null);
  } finally {
    restore();
  }
});

test('cancelled type capture reclaims a late job without committing a catalog entry', async () => {
  const { h, calls, restore } = setup();
  try {
    const gate = deferred<{ jobId: string }>();
    const cancelled: string[] = [];
    h.captureJobs = new AsyncJobs({
      api: async <T = unknown>(url: string, options?: ApiRequestOptions): Promise<T> => {
        if (url.endsWith('/start')) return (await gate.promise) as T;
        if (url.endsWith('/cancel')) {
          const body =
            typeof options?.body === 'string'
              ? (JSON.parse(options.body) as { id?: string })
              : {};
          if (body.id) cancelled.push(body.id);
          const empty: unknown = {};
          return empty as T;
        }
        throw new Error('must not poll cancelled job');
      },
      identity: () => h.identity(),
      requestIdentity: () => 'one',
      sleep: async () => undefined,
      endpoint: '/api/recipes/capture',
    });

    const pending = h.save();
    await tick();
    h.cancel();
    gate.resolve({ jobId: 'late-capture' });
    await pending;
    await tick();
    assert.deepEqual(cancelled, ['late-capture']);
    assert.equal(calls.length, 0);
    assert.equal(h.draft, null);
    assert.equal(h.busy, false);
  } finally {
    restore();
  }
});

test('a stale catalog response cannot overwrite a successfully saved plan', async () => {
  const { h, setApi, restore } = setup();
  try {
    const gate = deferred<{ book: string; store: { version: number; revision: number; items: [] } }>();
    let reads = 0;
    setApi(async (_url, options) => {
      if (options?.method === 'POST') return { id: 'new', revision: 2 };
      return ++reads === 1
        ? gate.promise
        : { book: 'book', store: { version: 1, revision: 2, items: [] } };
    });
    h.captureJobs.execute = async () => ({
      job: 'cap',
      result: { captureId: 'draft' },
      sourceFields: undefined,
      query: undefined,
      current: () => true,
    });
    const stale = h.refresh();
    await h.save();
    gate.resolve({ book: 'book', store: { version: 1, revision: 1, items: [] } });
    await stale;
    assert.equal(h.store?.revision, 2);
  } finally {
    restore();
  }
});

test('a preview arriving after workbook replacement is ignored', async () => {
  const { h, c, setApi, restore } = setup();
  try {
    const gate = deferred<RecipePreviewState>();
    h.selected = () => sampleRecipe;
    h.clearPreview = () => {
      h.preview = null;
    };
    h.target = () => preview.ref;
    setApi(() => gate.promise);
    h.renderPreview = () => {
      throw new Error('obsolete schema rendered');
    };
    const pending = h.readPreview();
    c.bookKey = 'two';
    gate.resolve(preview);
    await pending;
    assert.equal(h.preview, null);
  } finally {
    restore();
  }
});

test('replay failures and cancelled late completions preserve the active result', async () => {
  const { h, adopted, restore } = setup();
  try {
    h.preview = preview;
    h.mapping = () => ({ C1: 'C3', C2: 'C1' });
    h.runJobs.execute = async () => {
      throw new Error('B11的读取类型不兼容');
    };
    await h.run();
    assert.equal(adopted.length, 0);
    assert.match(document.getElementById('analysisPlanStatus')?.textContent ?? '', /B11/);
    assert.equal(h.busy, false);

    const gate = deferred<{ jobId: string }>();
    const cancelled: string[] = [];
    h.runJobs = new AsyncJobs<
      AnalysisQueryStatusResponse | AnalysisResult,
      AnalysisQueryStatusResponse
    >({
      api: async <T = unknown>(url: string, options?: ApiRequestOptions): Promise<T> => {
        if (url.endsWith('/start')) return (await gate.promise) as T;
        if (url.endsWith('/cancel')) {
          const body =
            typeof options?.body === 'string'
              ? (JSON.parse(options.body) as { id?: string })
              : {};
          if (body.id) cancelled.push(body.id);
          const empty: unknown = {};
          return empty as T;
        }
        throw new Error('late replay must not poll');
      },
      identity: () => h.identity(),
      requestIdentity: () => 'one',
      sleep: async () => undefined,
      endpoint: '/api/recipes/run',
      resultFromStatus: (status) => status,
    });
    const pending = h.run();
    await tick();
    h.cancel();
    gate.resolve({ jobId: 'late-run' });
    await pending;
    await tick();
    assert.deepEqual(cancelled, ['late-run']);
    assert.equal(adopted.length, 0);
  } finally {
    restore();
  }
});

test('completed replay cannot adopt into a newer analysis intent', async () => {
  const { h, c, adopted, restore } = setup();
  try {
    const gate = deferred<{
      job: string;
      result: AnalysisQueryStatusResponse;
      sourceFields: unknown;
      query: unknown;
      current: () => boolean;
    }>();
    h.preview = preview;
    h.mapping = () => ({ C1: 'C3', C2: 'C1' });
    h.runJobs.execute = () => gate.promise;
    const pending = h.run();
    c.intent++;
    const status: AnalysisQueryStatusResponse = {
      status: 'done',
      stage: '',
      progress: 1,
      total: 1,
      error: '',
      query: { source: 'wb:数据', rows: [], aggregate: 'count' },
    };
    gate.resolve({
      job: 'run-1',
      result: status,
      sourceFields: undefined,
      query: status.query,
      current: () => true,
    });
    await pending;
    assert.equal(adopted.length, 0);
    assert.equal(h.busy, false);
  } finally {
    restore();
  }
});

test('catalog loading disables stale source actions until the fresh workbook identity is installed', async () => {
  const { h, origControls, setApi, restore } = setup();
  try {
    const gate = deferred<{ book: string; store: { version: number; revision: number; items: [] } }>();
    h.controls = origControls;
    h.selected = () => sampleRecipe;
    const targetSel = document.getElementById('analysisPlanTarget');
    assert.ok(targetSel instanceof HTMLSelectElement);
    targetSel.value = 'wb:Data';
    setApi(() => gate.promise);

    const pending = h.refresh();
    const listSel = document.getElementById('analysisPlanList');
    const previewBtn = document.getElementById('analysisPlanPreview');
    const saveBtn = document.getElementById('analysisPlanSave');
    assert.ok(
      listSel instanceof HTMLSelectElement &&
        previewBtn instanceof HTMLButtonElement &&
        saveBtn instanceof HTMLButtonElement,
    );
    assert.equal(listSel.disabled, true);
    assert.equal(previewBtn.disabled, true);
    assert.equal(saveBtn.disabled, true);

    gate.resolve({ book: 'fresh-book', store: { version: 1, revision: 2, items: [] } });
    await pending;
    assert.equal(h.book, 'fresh-book');
    assert.equal(previewBtn.disabled, false);
    assert.equal(saveBtn.disabled, false);
  } finally {
    restore();
  }
});

test('a native dialog close blocks capture publication before its queued close event runs', async () => {
  const { h, calls, restore } = setup();
  try {
    const gate = deferred<{
      job: string;
      result: { captureId: string };
      sourceFields: unknown;
      query: unknown;
      current: () => boolean;
    }>();
    h.captureJobs.execute = () => gate.promise;
    const pending = h.save();
    h.dialog.open = false;
    gate.resolve({
      job: 'cap',
      result: { captureId: 'late-draft' },
      sourceFields: undefined,
      query: undefined,
      current: () => true,
    });
    await pending;
    assert.equal(calls.length, 0);
    assert.equal(h.draft, null);
  } finally {
    restore();
  }
});

test('recipe preview and replay explicitly bind the chosen target range, never the origin range', async () => {
  const { h, c, calls, setApi, restore } = setup();
  try {
    c.source = 'wb:数据';
    c.sourceId = 'range-owner';
    c.sourceScope = { version: 1, mode: 'selection', fingerprint: 'one' };
    const scopeSel = document.getElementById('analysisPlanScope');
    assert.ok(scopeSel instanceof HTMLSelectElement);
    scopeSel.value = 'current';
    h.target = () => preview.ref;
    h.selected = () => sampleRecipe;
    h.renderPreview = () => undefined;
    setApi(async () => ({
      ...preview,
      target: { total: 10, stamp: 'target' },
      sourceScope: c.sourceScope,
    }));

    await h.readPreview();
    assert.equal(calls[0]?.data?.sourceId, 'range-owner');
    assert.equal(h.preview?.sourceId, 'range-owner');

    h.mapping = () => ({ C1: 'C3', C2: 'C1' });
    h.runJobs.execute = async (request) => {
      const req = request as { sourceId?: string };
      assert.equal(req.sourceId, 'range-owner');
      throw new Error('test stop before adoption');
    };
    await h.run();

    scopeSel.value = 'full';
    await h.readPreview();
    assert.equal(calls.at(-1)?.data?.sourceId, undefined);
  } finally {
    restore();
  }
});
