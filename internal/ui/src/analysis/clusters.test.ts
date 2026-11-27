import { test } from 'node:test';
import assert from 'node:assert/strict';
import type {
  AnalysisTransform,
  ClusterEnvelope,
  ClusterGroupSummary,
  ClusterGroupsResponse,
  ClusterVariant,
  SourceScope,
} from '../contracts/analysis.ts';
import type { ApiRequestOptions } from '../contracts/common.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import {
  AnalysisClusters,
  type ClusterContext,
  type ClusterDetailPayload,
  type ClusterResolveResponse,
} from './clusters.ts';

const tick = (): Promise<void> =>
  new Promise((resolve) => {
    setImmediate(resolve);
  });

function gate<T>() {
  let resolve: (value: T | PromiseLike<T>) => void = () => undefined;
  let reject: (reason?: unknown) => void = () => undefined;
  const promise = new Promise<T>((a, b) => {
    resolve = a;
    reject = b;
  });
  return { promise, resolve, reject };
}

interface ClusterCall {
  readonly route: string;
  readonly body: Record<string, unknown> | null;
  readonly signal: AbortSignal | null | undefined;
}

interface MutableClusterContext {
  bookKey: string;
  book: string;
  opened: boolean;
  ready: boolean;
  source: string;
  headerRow: number;
  sourceScope: SourceScope | null;
  sourceId: string;
}

function harness() {
  const dom = installDom({ fromIndexHtml: true });
  const calls: ClusterCall[] = [];
  const transforms: AnalysisTransform[] = [];
  const arrivals: (ClusterResolveResponse | readonly [string, boolean])[] = [];

  const state: MutableClusterContext = {
    bookKey: 'content',
    book: 'path',
    opened: true,
    ready: true,
    source: 'wb:来源',
    headerRow: 1,
    sourceScope: null,
    sourceId: '',
  };

  const groups: readonly ClusterGroupSummary[] = [
    { id: 0, key: 'acme', kind: 'similar', count: 3, variants: 3, shortened: false },
    { id: 1, key: '0007', kind: 'exact', count: 2, variants: 1, shortened: false },
    { id: 2, key: '<img>', kind: 'similar', count: 2, variants: 2, shortened: false },
  ];
  const values: readonly (readonly ClusterVariant[])[] = [
    [
      { raw: 'A', type: 's', count: 1, rows: [2], formulaCount: 0 },
      { raw: 'a', type: 'inlineStr', count: 1, rows: [3], formulaCount: 0 },
      { raw: 'Ａ', type: 'str', count: 1, rows: [4], formulaCount: 1 },
    ],
    [{ raw: '0007', type: 's', count: 2, rows: [5, 6], formulaCount: 0 }],
    [
      { raw: '<img src=x onerror=alert(1)>', type: 's', count: 1, rows: [7], formulaCount: 0 },
      { raw: '<IMG SRC=x onerror=alert(1)>', type: 's', count: 1, rows: [8], formulaCount: 0 },
    ],
  ];

  let seq = 0;
  let hook: (route: string, body: Record<string, unknown> | null) => unknown = () => undefined;
  const identities = new Map<string, { readonly workbookKey?: string; readonly book?: string }>();

  const envelope = (id: string): ClusterEnvelope => ({
    id,
    workbookKey: identities.get(id)?.workbookKey ?? state.bookKey,
    book: identities.get(id)?.book ?? state.book,
    source: state.source,
    headerRow: 1,
    field: 'C1',
    fieldName: '机构',
    stamp: 'stamp-1',
    sourceScope: state.sourceScope,
  });

  const page = (id: string, offset: number): ClusterGroupsResponse => ({
    ...envelope(id),
    items: groups.slice(offset, offset + 2),
    offset,
    next: Math.min(groups.length, offset + 2),
    total: groups.length,
    counts: {
      scanned: 8,
      eligible: 7,
      unique: 6,
      similarGroups: 2,
      exactGroups: 1,
      blank: 1,
      numeric: 0,
      otherTypes: 0,
      missingCache: 0,
      emptyKey: 0,
    },
    options: {
      foldCase: false,
      foldWidth: false,
      space: '',
      ignorePunctuation: false,
    },
  });

  const detail = (id: string, group: number, offset = 0): ClusterDetailPayload => {
    const g = groups[group];
    const v = values[group] ?? [];
    assert.ok(g);
    return {
      ...envelope(id),
      group,
      key: g.key,
      kind: g.kind,
      count: g.count,
      variants: v.slice(offset, offset + 2),
      offset,
      next: Math.min(v.length, offset + 2),
      total: v.length,
    };
  };

  const api = async <T = unknown>(route: string, opts?: ApiRequestOptions): Promise<T> => {
    const body =
      typeof opts?.body === 'string'
        ? (JSON.parse(opts.body) as Record<string, unknown>)
        : null;
    calls.push({ route, body, signal: opts?.signal });
    const held = hook(route, body);
    if (held !== undefined) return (await Promise.resolve(held)) as T;

    const url = new URL(route, 'http://local');
    const q = url.searchParams;
    const id = q.get('id') ?? '';
    if (url.pathname.endsWith('/start')) {
      const jobId = `job-${++seq}`;
      identities.set(jobId, {
        workbookKey: typeof body?.workbookKey === 'string' ? body.workbookKey : undefined,
        book: typeof body?.book === 'string' ? body.book : undefined,
      });
      const res: unknown = { jobId };
      return res as T;
    }
    if (url.pathname.endsWith('/status')) {
      const res: unknown = { status: 'done', id, stage: '完整候选已读取' };
      return res as T;
    }
    if (url.pathname.endsWith('/groups')) {
      return page(id, Number(q.get('offset') ?? 0)) as T;
    }
    if (url.pathname.endsWith('/detail')) {
      return detail(id, Number(q.get('group')), Number(q.get('offset') ?? 0)) as T;
    }
    if (url.pathname.endsWith('/confirm')) {
      const choices = body?.choices as readonly { readonly representative: string }[] | undefined;
      const bodyId = typeof body?.id === 'string' ? body.id : '';
      const res: unknown = {
        ...envelope(bodyId),
        transform: {
          id: 'D_map',
          name: '文本核对',
          source: 'C1',
          op: 'map_text',
          mapping: [{ from: 'a', to: choices?.[0]?.representative ?? '' }],
        },
      };
      return res as T;
    }
    if (url.pathname.endsWith('/resolve')) {
      const resolved: ClusterResolveResponse = { ...envelope(id), ref: 'A2', row: 2, column: 1 };
      return resolved as T;
    }
    const res: unknown = { changed: true };
    return res as T;
  };

  const panel = new AnalysisClusters({
    api,
    context: (): ClusterContext => state,
    limits: { pageGroups: 2, pageVariants: 2 },
    sleep: async () => undefined,
    toast: () => undefined,
    onTransform: (step, guard) => {
      if (guard()) transforms.push(step);
    },
    navigate: (data, guard) => {
      if (!guard()) return false;
      arrivals.push(data);
      return true;
    },
    startExport: (jobId, protectCSV) => {
      arrivals.push([jobId, protectCSV]);
    },
  });
  panel.open({ id: 'C1', name: '机构', column: 1 });

  return {
    panel,
    state,
    calls,
    transforms,
    arrivals,
    page,
    detail,
    envelope,
    setHook: (fn: typeof hook) => {
      hook = fn;
    },
    restore: () => dom.restore(),
  };
}

test('candidate IO is explicit, raw values stay safe, and a representative is never preselected', async () => {
  const h = harness();
  try {
    assert.equal(h.calls.length, 0);
    await h.panel.run();
    assert.equal(h.panel.snapshot?.job, 'job-1');
    assert.equal(h.panel.choice, null);
    const confirmBtn = document.getElementById('analysisClustersConfirm');
    const variantsEl = document.getElementById('analysisClusterVariants');
    assert.ok(confirmBtn instanceof HTMLButtonElement && variantsEl);
    assert.equal(confirmBtn.disabled, true);
    assert.equal(variantsEl.children.length, 2);
    assert.deepEqual(h.calls[0]?.body?.options, {
      foldCase: false,
      foldWidth: false,
      space: '',
      ignorePunctuation: false,
    });

    await h.panel.select(2);
    const firstRow = variantsEl.children[0];
    assert.ok(firstRow);
    const labelText = firstRow.querySelector('label pre')?.textContent;
    assert.equal(labelText, '<img src=x onerror=alert(1)>');
    assert.equal(variantsEl.querySelector('img'), null);

    const radio = firstRow.querySelector('input[type="radio"]');
    assert.ok(radio instanceof HTMLInputElement);
    radio.checked = true;
    radio.dispatchEvent(new Event('change'));
    await h.panel.confirm();
    assert.equal(h.transforms.length, 1);
    assert.equal(h.transforms[0]?.op, 'map_text');
    assert.equal(confirmBtn.disabled, true);

    await h.panel.confirm();
    assert.equal(h.transforms.length, 1);
    assert.ok(
      !h.calls.some(
        (c) => c.route.includes('/query/start') || c.route.includes('/sheet/batch'),
      ),
    );
  } finally {
    h.restore();
  }
});

test('group and variant pages preserve only the selected original values and discard old detail responses', async () => {
  const h = harness();
  try {
    await h.panel.run();
    await h.panel.select(0, 2, true);
    assert.equal(h.panel.detail?.variants[0]?.raw, 'Ａ');
    assert.equal(document.getElementById('analysisClusterVariants')?.children.length, 1);

    await h.panel.loadPage(2);
    assert.equal(h.panel.page?.items.length, 1);
    assert.equal(h.panel.detail.group, 2);

    const held = gate<ClusterDetailPayload>();
    h.setHook((route) =>
      route.includes('/detail?') && route.includes('group=0') ? held.promise : undefined,
    );
    const old = h.panel.select(0);
    await tick();
    await h.panel.select(1);
    held.resolve(h.detail('job-1', 0));
    await old;
    assert.equal(h.panel.detail.group, 1);
    const confirmBtn = document.getElementById('analysisClustersConfirm');
    assert.ok(confirmBtn instanceof HTMLButtonElement);
    assert.equal(confirmBtn.disabled, true);
  } finally {
    h.restore();
  }
});

test('a failed new first page retains the accepted snapshot and releases only the new completed candidate', async () => {
  const h = harness();
  try {
    await h.panel.run();
    const accepted = h.panel.snapshot;
    const acceptedPage = h.panel.page;
    h.setHook((route) =>
      route.includes('/groups?') && route.includes('job-2')
        ? Promise.reject(new Error('读取失败'))
        : undefined,
    );
    await h.panel.run();
    assert.equal(h.panel.snapshot, accepted);
    assert.equal(h.panel.page, acceptedPage);
    assert.match(document.getElementById('analysisClustersStatus')?.textContent ?? '', /读取失败/);
    assert.ok(h.calls.some((c) => c.route.endsWith('/clear') && c.body?.id === 'job-2'));
    assert.ok(!h.calls.some((c) => c.route.endsWith('/clear') && c.body?.id === 'job-1'));
  } finally {
    h.restore();
  }
});

test('cancel and immediate retry owns the late start without erasing the newer result', async () => {
  const h = harness();
  try {
    const held = gate<{ jobId: string }>();
    let first = true;
    h.setHook((route) => {
      if (route.endsWith('/start') && first) {
        first = false;
        return held.promise;
      }
      return undefined;
    });
    const old = h.panel.run();
    await tick();
    document.getElementById('analysisClustersCancel')?.click();
    await h.panel.run();
    const newer = h.panel.snapshot;
    held.resolve({ jobId: 'late-cancelled' });
    await old;
    assert.equal(h.panel.snapshot, newer);
    assert.equal(h.panel.loading, false);
    assert.equal(h.panel.busy, false);
    assert.ok(h.calls.some((c) => c.route.endsWith('/cancel') && c.body?.id === 'late-cancelled'));
  } finally {
    h.restore();
  }
});

test('source path changes reject late pages, confirmations and navigation', async () => {
  for (const mode of ['page', 'confirm', 'resolve'] as const) {
    const h = harness();
    try {
      await h.panel.run();
      const held = gate<unknown>();
      h.panel.choice = 'A';
      h.setHook((route) =>
        route.includes(mode === 'page' ? '/groups?' : mode === 'confirm' ? '/confirm' : '/resolve?')
          ? held.promise
          : undefined,
      );
      const pending =
        mode === 'page'
          ? h.panel.loadPage(2)
          : mode === 'confirm'
            ? h.panel.confirm()
            : h.panel.locate(0, 0);
      await tick();
      h.state.book = 'other-path';
      h.panel.reset();
      held.resolve(
        mode === 'page'
          ? h.page('job-1', 2)
          : mode === 'confirm'
            ? { ...h.envelope('job-1'), transform: { id: 'D_map', name: '核对', source: 'C1', op: 'map_text' } }
            : { ...h.envelope('job-1'), ref: 'A2', row: 2, column: 1 },
      );
      await pending;
      assert.equal(h.transforms.length, 0);
      assert.equal(h.arrivals.length, 0);
      assert.equal(h.panel.snapshot, null);
    } finally {
      h.restore();
    }
  }
});

test('malformed pagination and a wrong scope cannot replace the accepted snapshot', async () => {
  const h = harness();
  try {
    await h.panel.run();
    const accepted = h.panel.snapshot;
    h.setHook((route) => (route.includes('/groups?') ? { ...h.page('job-2', 0), next: 99 } : undefined));
    await h.panel.run();
    assert.equal(h.panel.snapshot, accepted);
    assert.match(document.getElementById('analysisClustersStatus')?.textContent ?? '', /分页/);

    h.setHook((route) =>
      route.includes('/groups?')
        ? { ...h.page('job-3', 0), sourceScope: { version: 1, mode: 'visible' } }
        : undefined,
    );
    await h.panel.run();
    assert.equal(h.panel.snapshot, accepted);
    assert.match(document.getElementById('analysisClustersStatus')?.textContent ?? '', /范围不一致/);
  } finally {
    h.restore();
  }
});

test('a delayed native close event cannot erase an immediately reopened candidate panel', async () => {
  const h = harness();
  try {
    await h.panel.run();
    h.panel.close();
    h.panel.open({ id: 'C1', name: '机构', column: 1 });
    await tick();
    assert.equal(h.panel.dialog.open, true);
    assert.equal(h.panel.opened, true);
    assert.ok(h.panel.snapshot);
    await h.panel.locate(0, 0);
    const arrival = h.arrivals[0];
    assert.ok(arrival && typeof arrival === 'object' && 'ref' in arrival);
    assert.equal(arrival.ref, 'A2');
    assert.equal(h.panel.dialog.open, false);
  } finally {
    h.restore();
  }
});

test('reloading a source clears old counts and a lost scoped binding never starts a widened scan', async () => {
  const h = harness();
  try {
    await h.panel.run();
    h.panel.reset();
    assert.equal(document.getElementById('analysisClustersSummary')?.textContent, '');
    h.state.sourceScope = { version: 1, mode: 'visible', fingerprint: 'content' };
    h.panel.open({ id: 'C1', name: '机构', column: 1 });
    const starts = h.calls.filter((c) => c.route.endsWith('/start')).length;
    await h.panel.run();
    assert.equal(h.calls.filter((c) => c.route.endsWith('/start')).length, starts);
    assert.match(document.getElementById('analysisClustersStatus')?.textContent ?? '', /范围已释放/);
  } finally {
    h.restore();
  }
});
