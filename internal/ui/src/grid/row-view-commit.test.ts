import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { ApiRequestOptions } from '../contracts/common.ts';
import type { RowViewState } from '../contracts/workbook.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import {
  makeDefaultSheetControllerDeps,
  makeRowViewState,
  SheetController,
} from '../test-helpers/grid-fixture.ts';
import { RowVisibility } from './row-visibility.ts';
import { createInitialWorkbookState } from './state.ts';

const tick = (): Promise<void> =>
  new Promise((resolve) => {
    setImmediate(resolve);
  });

function deferred<T = void>() {
  let resolve: (value: T | PromiseLike<T>) => void = () => undefined;
  const promise = new Promise<T>((yes) => {
    resolve = yes;
  });
  return { promise, resolve };
}

function setupCommitHarness() {
  const dom = installDom({ fromIndexHtml: true });
  const calls: { readonly url: string; readonly body: unknown }[] = [];
  const cleared: string[] = [];
  const installed: string[] = [];
  const selected: string[] = [];
  const cardResets: (string | undefined)[] = [];
  const quickResets: (string | undefined)[] = [];
  const detailResets: (string | undefined)[] = [];

  const S = createInitialWorkbookState(1_048_576, 16_384);
  S.sheet = 'A';
  S.sheetEpoch = 1;
  S.workbookKey = 'book';
  S.filterRequest = 1;
  S.filterId = 'old';
  S.filterJob = 'query';
  S.filterTotal = 3;
  S.filtered = true;
  S.rowView = makeRowViewState({ id: 'old-view', filterId: 'old', totalRows: 3 });
  S.rowVisibility = new RowVisibility(100);
  S.filterTotalsBySheet.set('A', 3);
  S.filtersBySheet.set('A', 'old');
  S.pendingFilterSpec = {
    sheet: 'A',
    rules: [{ column: 1, operator: 'contains', value: 'pending' }],
    options: {},
  };

  let prepareFilterImpl: (
    filterId: string,
    options: { readonly guard?: () => boolean; readonly signal?: AbortSignal },
  ) => Promise<RowViewState> = async (filterId) => {
    rowViews.serial++;
    return makeRowViewState({ id: 'prepared', filterId, totalRows: 2 });
  };
  const rowViews = {
    busy: false,
    serial: 0,
    cache: new Map(),
    cancel: () => undefined,
    prepare: async () => makeRowViewState(),
    prepareFilter: (
      filterId: string,
      options: { readonly guard?: () => boolean; readonly signal?: AbortSignal },
    ) => prepareFilterImpl(filterId, options),
    build: async () => makeRowViewState(),
    validate: () => undefined,
    install: (data: RowViewState) => {
      assert.equal(data.filterId, S.filterId);
      S.rowView = data;
      installed.push(data.id);
    },
    cleanup: (id: string) => {
      cleared.push(id);
    },
  };

  let apiImpl: (url: string, options?: ApiRequestOptions) => Promise<unknown> = async (
    url,
    options,
  ) => {
    calls.push({
      url,
      body: typeof options?.body === 'string' ? JSON.parse(options.body) : undefined,
    });
    return url.startsWith('/api/filter/status')
      ? { status: 'done', filterId: 'new', matchedRows: 2 }
      : {};
  };
  let loadViewportImpl: () => Promise<void> = async () => undefined;

  const deps = makeDefaultSheetControllerDeps({
    S,
    api: async <T = unknown>(url: string, options?: ApiRequestOptions): Promise<T> =>
      (await apiImpl(url, options)) as T,
    loadViewport: () => loadViewportImpl(),
    selectInitialCell: () => {
      selected.push('initial');
    },
    navigateToCell: async () => {
      selected.push('navigation');
      return true;
    },
    panels: {
      searchPanel: {
        leavePages: () => undefined,
        buildHits: () => undefined,
        applyHighlightClasses: () => undefined,
        displaySummary: () => undefined,
        renderResults: () => undefined,
        syncMode: () => undefined,
        poll: async () => undefined,
      },
      exportCenter: { adoptRecovered: () => false },
      rowViews,
      recordCardCenter: {
        resetBook: () => cardResets.push(S.rowView?.id),
        controls: () => undefined,
      },
      selectionQuick: {
        resetBook: () => quickResets.push(S.rowView?.id),
        changed: () => undefined,
      },
      cellExplanation: {
        resetBook: () => detailResets.push(S.rowView?.id),
        changed: () => undefined,
      },
      transposeCenter: { resetBook: () => undefined, changed: () => undefined },
      profileCenter: { resetBook: () => undefined, changed: () => undefined },
    },
  });
  const controller = new SheetController(deps);
  controller.refreshAutoFilterHeaders = () => undefined;

  return {
    S,
    controller,
    rowViews,
    calls,
    cleared,
    installed,
    selected,
    cardResets,
    quickResets,
    detailResets,
    setPrepareFilter: (fn: typeof prepareFilterImpl) => {
      prepareFilterImpl = fn;
    },
    setApi: (fn: typeof apiImpl) => {
      apiImpl = fn;
    },
    setLoadViewport: (fn: () => Promise<void>) => {
      loadViewportImpl = fn;
    },
    restore: () => dom.restore(),
  };
}

test('cancelling a filter while its visible projection is pending retains the confirmed view and rules', async () => {
  const h = setupCommitHarness();
  try {
    const pending = deferred<RowViewState>();
    let signal: AbortSignal | undefined;
    h.setPrepareFilter(async (_filterId, options) => {
      h.rowViews.serial++;
      signal = options.signal;
      return pending.promise;
    });
    const poll = h.controller.pollFilter('query');
    await tick();
    await h.controller.cancelFilter('query');
    assert.equal(signal?.aborted, true);
    pending.resolve(makeRowViewState({ id: 'late', filterId: 'new', totalRows: 2 }));
    assert.equal(await poll, false);
    assert.equal(h.S.filterId, 'old');
    assert.equal(h.S.rowView?.id, 'old-view');
    assert.equal(h.S.lastRulesBySheet.size, 0);
    assert.deepEqual(h.installed, []);
    assert.deepEqual(h.cleared, ['late']);
    assert.deepEqual(h.cardResets, []);
  } finally {
    h.restore();
  }
});

test('an obsolete filter intention cannot adopt a response even if the projection transport ignores cancellation', async () => {
  for (const reason of ['request', 'source', 'projection'] as const) {
    const h = setupCommitHarness();
    try {
      const pending = deferred<RowViewState>();
      h.setPrepareFilter(async () => {
        h.rowViews.serial++;
        return pending.promise;
      });
      const applying = h.controller.adoptFilterResult({ filterId: 'new', matchedRows: 2 });
      await tick();
      if (reason === 'request') h.S.filterRequest++;
      else if (reason === 'source') h.S.sheetEpoch++;
      else h.rowViews.serial++;
      pending.resolve(makeRowViewState({ id: 'late', filterId: 'new', totalRows: 2 }));
      assert.equal(await applying, false);
      assert.equal(h.S.filterId, 'old');
      assert.deepEqual(h.installed, []);
      assert.deepEqual(h.cleared, ['late']);
      assert.equal(
        h.calls.some((x) => x.url === '/api/filter/clear'),
        false,
      );
    } finally {
      h.restore();
    }
  }
});

test('a delayed clear cannot remove a newly selected filter or overwrite its selection', async () => {
  const h = setupCommitHarness();
  try {
    const pending = deferred<Record<string, unknown>>();
    h.S.filterJob = null;
    h.setApi(async (url) => (url === '/api/filter/clear' ? pending.promise : {}));
    const clearing = h.controller.clearFilter();
    await tick();
    h.S.filterRequest++;
    h.S.filterId = 'latest';
    h.S.filtersBySheet.set('A', 'latest');
    h.S.rowView = makeRowViewState({ id: 'latest-view', filterId: 'latest' });
    pending.resolve({});
    await clearing;
    assert.equal(h.S.filterId, 'latest');
    assert.equal(h.S.filtersBySheet.get('A'), 'latest');
    assert.equal(h.S.rowView.id, 'latest-view');
    assert.deepEqual(h.selected, []);
    assert.deepEqual(h.cleared, ['prepared']);
  } finally {
    h.restore();
  }
});

test('a row-view commit that finishes loading late cannot select or navigate over a newer view', async () => {
  const h = setupCommitHarness();
  try {
    const pending = deferred();
    h.S.filtered = false;
    h.S.filterId = '';
    h.S.selected = {
      sheet: 'A',
      row: 1,
      col: 1,
      ref: 'A1',
      raw: '',
      display: '',
      hasFormula: false,
    };
    h.setLoadViewport(() => pending.promise);
    const committing = h.controller.commitRowView(makeRowViewState({ id: 'commit', filterId: '' }));
    await tick();
    h.S.rowView = makeRowViewState({ id: 'latest', filterId: '' });
    pending.resolve();
    assert.equal(await committing, false);
    assert.deepEqual(h.selected, []);
    assert.deepEqual(h.cardResets, ['old-view']);
    assert.deepEqual(h.quickResets, ['old-view']);
    assert.deepEqual(h.detailResets, ['old-view']);
  } finally {
    h.restore();
  }
});

test('a successful adoption updates the view before selecting while later viewport responses stay guarded', async () => {
  const h = setupCommitHarness();
  try {
    h.S.filterJob = null;
    assert.equal(await h.controller.adoptFilterResult({ filterId: 'new', matchedRows: 2 }), true);
    assert.equal(h.S.filterId, 'new');
    assert.equal(h.S.rowView?.id, 'prepared');
    assert.equal(h.S.filterTotal, 2);
    assert.deepEqual(h.selected, ['initial']);
    assert.deepEqual(h.cleared, []);
    assert.deepEqual(h.cardResets, ['old-view']);
    assert.deepEqual(h.quickResets, ['old-view']);
    assert.deepEqual(h.detailResets, ['old-view']);
  } finally {
    h.restore();
  }
});
