import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { AxisViewSettings } from '../contracts/analysis.ts';
import type { ApiRequestOptions } from '../contracts/common.ts';
import type { RowViewState } from '../contracts/workbook.ts';
import { createInitialWorkbookState } from '../grid/state.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import { makeRowViewState } from '../test-helpers/grid-fixture.ts';
import { RowViews } from './row-views.ts';

const tick = (): Promise<void> =>
  new Promise((resolve) => {
    setImmediate(resolve);
  });

interface Deferred<T> {
  readonly promise: Promise<T>;
  readonly resolve: (value: T) => void;
  readonly reject: (reason?: unknown) => void;
}

function deferred<T = void>(): Deferred<T> {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((yes, no) => {
    resolve = yes;
    reject = no;
  });
  return { promise, resolve, reject };
}

function setupRowViews(
  override?: (
    url: string,
    body: Record<string, unknown> | null,
    options: ApiRequestOptions,
  ) => unknown,
) {
  const dom = installDom({ fromIndexHtml: true });
  (document.getElementById('rowsRemember') as HTMLInputElement).checked = true;

  const calls: {
    readonly url: string;
    readonly body: Record<string, unknown> | null;
    readonly signal: AbortSignal | null | undefined;
  }[] = [];
  const commits: string[] = [];
  const tasks = new Map<string, RowViewState>();
  let next = 0;

  const s = createInitialWorkbookState(1_048_576, 16_384);
  s.workbookKey = 'fingerprint';
  s.book = 'source-path';
  s.sheet = 'A';
  s.sheetEpoch = 1;
  s.filterId = '';
  s.indexReady = true;

  const snapshot = (
    id: string,
    filterId = '',
    extra: Partial<RowViewState> = {},
  ): RowViewState =>
    makeRowViewState({
      id,
      workbookKey: s.workbookKey,
      book: s.book,
      sheet: s.sheet ?? 'A',
      filterId,
      maxRow: 120,
      totalRows: 120,
      revision: 3,
      digest: 'validated-digest',
      hiddenRanges: [],
      settings: { mode: 'original' },
      ...extra,
    });

  const api = async <T = unknown>(url: string, options: ApiRequestOptions = {}): Promise<T> => {
    const body =
      typeof options.body === 'string'
        ? (JSON.parse(options.body) as Record<string, unknown>)
        : null;
    calls.push({ url, body, signal: options.signal });
    const custom = override?.(url, body, options);
    if (custom !== undefined) return (await Promise.resolve(custom)) as T;
    const query = new URL(url, 'http://qa');
    const id = query.searchParams.get('id') ?? '';
    if (query.pathname === '/api/row-view/start') {
      const jobId = `job-${++next}`;
      const bodyFilterId = typeof body?.filterId === 'string' ? body.filterId : '';
      const bodySheet = typeof body?.sheet === 'string' ? body.sheet : (s.sheet ?? 'A');
      const bodySettings =
        (body?.settings as AxisViewSettings | undefined) ?? s.rowView?.settings ?? { mode: 'original' };
      tasks.set(jobId, snapshot(jobId, bodyFilterId, { sheet: bodySheet, settings: bodySettings }));
      const res: unknown = { jobId };
      return res as T;
    }
    if (query.pathname === '/api/row-view/status') {
      const res: unknown = { id, status: 'done' };
      return res as T;
    }
    if (query.pathname === '/api/row-view') return (tasks.get(id) ?? snapshot(id)) as T;
    if (query.pathname === '/api/row-view/groups') {
      const res: unknown = { id, total: 0, groups: [] };
      return res as T;
    }
    if (query.pathname === '/api/status') {
      const res: unknown = { workbookKey: s.workbookKey, book: s.book };
      return res as T;
    }
    if (query.pathname === '/api/row-view/cancel' || query.pathname === '/api/row-view/clear') {
      const res: unknown = {};
      return res as T;
    }
    throw new Error(`unexpected ${url}`);
  };

  const views = new RowViews({
    state: s,
    api,
    maximum: () => 120,
    commit: async (data, guard) => {
      if (!guard()) return;
      views.install(data);
      commits.push(data.id);
    },
    toast: () => undefined,
    navigate: async () => undefined,
    reload: async () => undefined,
    openDialog: (id) => {
      const dlg = document.getElementById(id);
      if (dlg instanceof HTMLDialogElement) dlg.showModal();
    },
  });
  views.install(snapshot('base'));

  return {
    views,
    s,
    calls,
    commits,
    snapshot,
    tasks,
    restore: () => {
      views.resetBook();
      dom.restore();
    },
  };
}

test('a cancelled late start owns its cleanup and cannot cancel a newer task through the old caller signal', async () => {
  const first = deferred<{ readonly jobId: string }>();
  const second = deferred<{ readonly id: string; readonly status: string }>();
  const signal = new AbortController();
  let starts = 0;
  const h = setupRowViews((url) => {
    if (url === '/api/row-view/start' && ++starts === 1) return first.promise;
    if (url.startsWith('/api/row-view/status?id=job-1')) return second.promise;
    return undefined;
  });
  try {
    const old = h.views.build({ signal: signal.signal });
    const rejected = assert.rejects(
      old,
      (error: unknown) => error instanceof Error && error.name === 'AbortError',
    );
    const fresh = h.views.build();
    await tick();
    signal.abort();
    first.resolve({ jobId: 'late-old' });
    await rejected;
    assert.equal(
      h.calls.some((x) => x.url === '/api/row-view/cancel' && x.body?.id === 'job-1'),
      false,
    );
    assert.ok(h.calls.some((x) => x.url === '/api/row-view/clear' && x.body?.id === 'late-old'));
    second.resolve({ id: 'job-1', status: 'done' });
    assert.equal((await fresh).id, 'job-1');
  } finally {
    h.restore();
  }
});

test('cancel and retry ignore a late result even when the transport ignores abort', async () => {
  const pending = deferred<RowViewState>();
  const h = setupRowViews((url) => (url === '/api/row-view?id=job-1' ? pending.promise : undefined));
  try {
    const old = h.views.change({ action: 'hide-all' });
    await tick();
    assert.equal(h.views.busy, true);
    assert.equal((document.getElementById('rowsHideRange') as HTMLButtonElement).disabled, true);
    h.views.cancel();
    const fresh = h.views.change({ action: 'show-all' });
    assert.equal(await fresh, true);
    pending.resolve(h.snapshot('job-1'));
    assert.equal(await old, false);
    assert.deepEqual(h.commits, ['job-2']);
    assert.equal(h.s.rowView?.id, 'job-2');
    assert.equal(document.getElementById('rowsStatus')?.textContent, '已更新行显隐');
    assert.equal(h.views.busy, false);
  } finally {
    h.restore();
  }
});

test('cancelling during a pending commit preserves the new controls and prevents obsolete post-commit status', async () => {
  const pending = deferred();
  const h = setupRowViews();
  try {
    let checked: boolean | undefined;
    h.views.commit = async (data, guard) => {
      h.views.install(data);
      await pending.promise;
      checked = guard();
    };
    const changing = h.views.change({ action: 'show-all' });
    await tick();
    h.views.cancel();
    pending.resolve();
    assert.equal(await changing, false);
    assert.equal(checked, false);
    assert.equal(document.getElementById('rowsStatus')?.textContent, '已取消更新');
    assert.equal(document.getElementById('rowsCancel')?.hidden, true);
  } finally {
    h.restore();
  }
});

test('a source switch or a later preparation cannot publish an obsolete view or its failure state', async () => {
  for (const mode of ['source', 'new-task'] as const) {
    const response = deferred<RowViewState>();
    const h = setupRowViews((url) =>
      url === '/api/row-view?id=job-1' ? response.promise : undefined,
    );
    try {
      h.views.cache.clear();
      const pending = h.views.prepare();
      await tick();
      if (mode === 'source') {
        h.s.workbookKey = 'new-book';
        h.s.sheetEpoch++;
        h.s.rowViewFailure = false;
      } else {
        await h.views.build();
      }
      response.resolve(h.snapshot('job-1', '', { workbookKey: 'fingerprint' }));
      await assert.rejects(
        pending,
        (error: unknown) => error instanceof Error && error.name === 'AbortError',
      );
      assert.equal(h.s.rowView?.id, 'base');
      assert.equal(h.s.rowViewFailure, false);
    } finally {
      h.restore();
    }
  }
});

test('an expired snapshot rebuilds from its captured settings while a real service error preserves the view', async () => {
  let failed = false;
  const h = setupRowViews((url) => {
    if (url === '/api/row-view?id=base' && !failed) {
      failed = true;
      throw Object.assign(new Error('expired'), { status: 404 });
    }
    return undefined;
  });
  try {
    assert.ok(h.s.rowView);
    h.s.rowView = { ...h.s.rowView, settings: { mode: 'hide-all' } };
    h.views.cache.set('A', h.s.rowView);
    const data = await h.views.prepare();
    assert.equal(data.settings.mode, 'hide-all');
    assert.equal(h.s.rowView.id, data.id);
  } finally {
    h.restore();
  }

  const bad = setupRowViews((url) =>
    url === '/api/row-view/start'
      ? Promise.reject(Object.assign(new Error('settings corrupt'), { status: 422 }))
      : undefined,
  );
  try {
    bad.views.cache.clear();
    await assert.rejects(bad.views.prepare(), /settings corrupt/);
    assert.equal(bad.s.rowView?.id, 'base');
    assert.equal(bad.s.rowViewFailure, true);
  } finally {
    bad.restore();
  }
});

test('cross-page revision conflicts preserve the confirmed view and surface the server reason', async () => {
  const h = setupRowViews((url) =>
    url === '/api/row-view/start'
      ? Promise.reject(Object.assign(new Error('其他页面已修改'), { status: 409 }))
      : undefined,
  );
  try {
    assert.equal(await h.views.change({ action: 'hide-all' }), false);
    assert.equal(h.s.rowView?.id, 'base');
    assert.equal(document.getElementById('rowsStatus')?.textContent, '其他页面已修改');
    assert.equal(
      h.calls.find((x) => x.url === '/api/row-view/start')?.body?.expectedRevision,
      3,
    );
    assert.equal(h.views.busy, false);
  } finally {
    h.restore();
  }
});

test('temporary recovery retains its warning and never attempts to overwrite unreadable preferences', async () => {
  const h = setupRowViews();
  try {
    assert.ok(h.s.rowView);
    h.s.rowView = { ...h.s.rowView, settingsWarning: '原文件已保留' };
    h.views.controls();
    assert.equal((document.getElementById('rowsRemember') as HTMLInputElement).disabled, true);
    assert.equal(await h.views.change({ action: 'show-all' }), true);
    assert.equal(h.calls.find((x) => x.url === '/api/row-view/start')?.body?.persist, false);
    h.calls.length = 0;
    assert.equal(await h.views.reveal(7), true);
    assert.equal(h.calls.find((x) => x.url === '/api/row-view/start')?.body?.persist, false);
  } finally {
    h.restore();
  }
});

test('malformed task identifiers, unknown states, and mismatched result identities fail without polling forever', async () => {
  for (const stage of ['start', 'status', 'source', 'result'] as const) {
    const h = setupRowViews((url) => {
      if (stage === 'start' && url === '/api/row-view/start') return {};
      if (stage === 'status' && url.startsWith('/api/row-view/status')) {
        return { status: 'mystery' };
      }
      if ((stage === 'source' || stage === 'result') && url === '/api/row-view?id=job-1') {
        return h.snapshot(stage === 'result' ? 'wrong-id' : 'job-1', '', {
          book: stage === 'source' ? 'other-source' : 'source-path',
        });
      }
      return undefined;
    });
    try {
      await assert.rejects(h.views.build());
      assert.equal(h.s.rowView?.id, 'base');
      assert.ok(h.calls.length < 7);
      if (stage !== 'start') {
        assert.ok(h.calls.some((x) => x.url === '/api/row-view/clear' && x.body?.id === 'job-1'));
      }
    } finally {
      h.restore();
    }
  }
});

test('late outline pages and failures cannot replace the latest page or its status', async () => {
  const first = deferred<{ readonly groups: readonly []; readonly total: number }>();
  const h = setupRowViews((url) =>
    url.includes('/groups?') && url.includes('offset=0')
      ? first.promise
      : url.includes('/groups?')
        ? { groups: [], total: 401 }
        : undefined,
  );
  try {
    const old = h.views.groups();
    h.views.page = 1;
    await h.views.groups();
    first.resolve({ groups: [], total: 200 });
    await old;
    assert.equal(document.getElementById('rowsPage')?.textContent, '2 / 3');
    assert.equal(h.views.groupTotal, 401);
  } finally {
    h.restore();
  }

  const failure = deferred<never>();
  const late = setupRowViews((url) => (url.includes('/groups?') ? failure.promise : undefined));
  try {
    const request = late.views.groups();
    late.s.rowView = late.snapshot('new-view');
    late.views.status('新视图');
    failure.reject(new Error('obsolete failure'));
    await request;
    assert.equal(document.getElementById('rowsStatus')?.textContent, '新视图');
  } finally {
    late.restore();
  }
});

test('snapshot ownership is bounded, cleans evicted owners, and survives a cached snapshot disappearing', () => {
  const h = setupRowViews();
  try {
    for (let n = 1; n <= 8; n++) {
      h.s.sheet = `Sheet${n}`;
      h.views.install(h.snapshot(`view-${n}`));
    }
    assert.equal(h.views.cache.size, 4);
    assert.equal(h.s.rowView?.id, 'view-8');
    assert.ok(h.calls.some((x) => x.url === '/api/row-view/clear' && x.body?.id === 'base'));
    h.views.resetBook();
    assert.equal(h.views.cache.size, 0);
    assert.equal(h.s.rowVisibility, null);
    assert.ok(h.calls.some((x) => x.url === '/api/row-view/clear' && x.body?.id === 'view-8'));
  } finally {
    h.restore();
  }
});

test('row view validation and outline rendering tolerate null slices from Go JSON responses', async () => {
  const h = setupRowViews((url) => {
    if (url.includes('/api/row-view/groups?')) {
      return { id: 'null-view', total: 0, groups: null };
    }
    return undefined;
  });
  try {
    const snap = h.snapshot('null-view', '', { hiddenRanges: null });
    h.views.install(snap);
    const vis = h.s.rowVisibility;
    assert.ok(vis);
    assert.equal(vis.hidden.length, 0);
    assert.equal(vis.count(1, 120), 120);
    await h.views.groups();
    assert.equal(h.views.groupTotal, 0);
    assert.match(document.getElementById('rowsGroupList')?.textContent ?? '', /没有分组大纲/);
  } finally {
    h.restore();
  }
});
