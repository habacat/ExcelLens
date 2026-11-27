import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { ApiRequestOptions, CellRef } from '../contracts/common.ts';
import type { CellData } from '../contracts/workbook.ts';
import { GridNavigation } from '../grid/navigation.ts';
import { createInitialWorkbookState, type TraceRefEntry } from '../grid/state.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import { makeDefaultNavigationDeps } from '../test-helpers/grid-fixture.ts';
import { Tracing, type TracingMotion } from './tracing.ts';

function makeCell(overrides: Partial<CellData> = {}): CellData {
  return {
    ref: 'A1',
    col: 1,
    raw: '',
    display: '',
    style: 0,
    hasFormula: false,
    ...overrides,
  };
}

const tick = (): Promise<void> =>
  new Promise((resolve) => {
    setImmediate(resolve);
  });

function setupTracing(
  apiOverride?: (path: string, options: ApiRequestOptions | undefined) => unknown,
) {
  const dom = installDom({ fromIndexHtml: true });
  let now = 10_000;
  let nextTimer = 0;
  const timers = new Map<number, { readonly fn: () => void; readonly at: number }>();
  const closed: (() => void)[] = [];
  const calls: { readonly path: string; readonly body: Record<string, unknown> | undefined }[] = [];
  const notices: [string, string | undefined][] = [];
  let traced: TraceRefEntry[] = [];

  const gridRegion = document.querySelector('.grid-region');
  assert.ok(gridRegion);
  const cellA1 = document.createElement('div');
  cellA1.className = 'grid-cell';
  cellA1.dataset.row = '1';
  cellA1.dataset.col = '1';
  cellA1.dataset.ref = 'A1';
  const cellC3 = document.createElement('div');
  cellC3.className = 'grid-cell';
  cellC3.dataset.row = '3';
  cellC3.dataset.col = '3';
  cellC3.dataset.ref = 'C3';
  gridRegion.append(cellA1, cellC3);

  const panel = document.getElementById('traceDialog') as HTMLDialogElement;
  const dock = document.getElementById('traceDock') as HTMLElement;
  const layer = document.getElementById('traceArrival') as HTMLElement;
  const grid = document.getElementById('grid') as HTMLElement;
  const title = document.getElementById('traceTitle') as HTMLElement;
  const formula = document.getElementById('traceFormula') as HTMLElement;
  const summary = document.getElementById('traceSummary') as HTMLElement;
  const list = document.getElementById('traceList') as HTMLElement;
  const scopeRow = document.getElementById('traceScopeRow') as HTMLElement;
  const scope = document.getElementById('traceScope') as HTMLSelectElement;
  const cancel = document.getElementById('traceCancelBtn') as HTMLButtonElement;
  const minimize = document.getElementById('traceMinimizeBtn') as HTMLButtonElement;
  const restoreBtn = document.getElementById('traceRestoreBtn') as HTMLButtonElement;
  const origin = document.getElementById('traceOriginBtn') as HTMLButtonElement;
  const end = document.getElementById('traceEndBtn') as HTMLButtonElement;

  scope.value = 'current';
  Object.defineProperty(layer, 'clientWidth', { value: 1000, configurable: true });
  if (layer.parentElement) {
    layer.parentElement.getBoundingClientRect = () =>
      ({
        left: 0,
        top: 0,
        right: 1000,
        bottom: 600,
        width: 1000,
        height: 600,
        x: 0,
        y: 0,
        toJSON: () => ({}),
      });
  }

  // Defer native dialog close event firing to simulate browser queued close events.
  panel.close = () => {
    panel.open = false;
    closed.push(() => {
      panel.dispatchEvent(new Event('close'));
    });
  };

  const parseRef = (ref: string | null | undefined): CellRef | null => {
    const m = /^([A-Z])(\d+)$/.exec(ref ?? '');
    if (!m?.[1] || !m[2]) return null;
    return { ref: `${m[1]}${m[2]}`, row: Number(m[2]), col: m[1].charCodeAt(0) - 64 };
  };
  const makeRef = (address: string): TraceRefEntry => {
    const p = parseRef(address);
    assert.ok(p);
    return { sheet: '源表', label: `源表!${address}`, start: p, end: p };
  };

  const state = createInitialWorkbookState(1_048_576, 16_384);
  state.workbookKey = 'book-1';
  state.sheet = '源表';
  state.sheets = [{ name: '源表' }];
  state.selected = {
    sheet: '源表',
    ref: 'C3',
    row: 3,
    col: 3,
    raw: '',
    display: '',
    hasFormula: true,
    formula: 'A1+B2',
  };

  let navigateImpl: (sheet: string, address: string, guard?: () => boolean) => Promise<void> =
    async (sheet, address, guard) => {
      if (!guard || guard()) {
        const p = parseRef(address);
        if (p) {
          state.selected = {
            sheet,
            row: p.row,
            col: p.col,
            ref: p.ref,
            raw: '',
            display: '',
            hasFormula: false,
          };
        }
      }
    };

  let motionExitImpl: TracingMotion['exit'] = async () => true;

  const trace = new Tracing({
    state,
    panel,
    dock,
    layer,
    grid,
    title,
    formula,
    summary,
    list,
    scopeRow,
    scope,
    cancel,
    minimize,
    restore: restoreBtn,
    origin,
    end,
    api: async <T = unknown>(path: string, options?: ApiRequestOptions): Promise<T> => {
      calls.push({
        path,
        body:
          typeof options?.body === 'string'
            ? (JSON.parse(options.body) as Record<string, unknown>)
            : undefined,
      });
      const r = apiOverride?.(path, options);
      if (r !== undefined) return (await Promise.resolve(r)) as T;
      if (path === '/api/search/start') {
        const res: unknown = { jobId: 'job', cached: true };
        return res as T;
      }
      if (path.startsWith('/api/search/status')) {
        const res: unknown = {
          status: 'done',
          matches: [{ sheet: '源表', ref: 'A1', formula: 'C3+1' }],
        };
        return res as T;
      }
      const res: unknown = {};
      return res as T;
    },
    parse: () => [makeRef('A1'), makeRef('B2')],
    parseRef,
    getCell: async (): Promise<CellData> => makeCell({ display: '12' }),
    setRefs: (refs) => {
      traced = refs;
    },
    bounds: (row, col) => ({ row, col, endRow: row, endCol: col }),
    visibleRect: (node) => ({
      left: Number(node.dataset.col) * 100,
      top: Number(node.dataset.row) * 25,
      right: Number(node.dataset.col) * 100 + 100,
      bottom: Number(node.dataset.row) * 25 + 25,
      pane: 'body',
    }),
    navigate: (sheet, address, guard) => navigateImpl(sheet, address, guard),
    atTarget: (r) => state.selected?.sheet === r.sheet && state.selected.ref === r.start.ref,
    notify: (msg, tone) => notices.push([msg, tone]),
    timers: {
      now: () => now,
      setTimeout: (fn, ms) => {
        const id = ++nextTimer;
        timers.set(id, { fn, at: now + ms });
        return id;
      },
      clearTimeout: (id) => {
        if (typeof id === 'number') timers.delete(id);
      },
      requestAnimationFrame: (fn) => {
        const id = ++nextTimer;
        timers.set(id, { fn: () => fn(now), at: now + 16 });
        return id;
      },
      cancelAnimationFrame: (id) => {
        timers.delete(id);
      },
    },
    motion: {
      enter: () => undefined,
      stop: () => undefined,
      exit: (el, opts) => motionExitImpl(el, opts),
    },
  });

  const advance = (ms: number): void => {
    const target = now + ms;
    for (;;) {
      const first = [...timers.entries()]
        .filter(([, t]) => t.at <= target)
        .sort((a, b) => a[1].at - b[1].at)[0];
      if (!first) break;
      now = first[1].at;
      timers.delete(first[0]);
      first[1].fn();
    }
    now = target;
  };

  return {
    trace,
    state,
    panel,
    dock,
    layer,
    list,
    scopeRow,
    title,
    summary,
    calls,
    notices,
    timers,
    advance,
    makeRef,
    parseRef,
    flushClose: () => {
      while (closed.length) closed.shift()?.();
    },
    getTraced: () => traced,
    setNavigate: (fn: typeof navigateImpl) => {
      navigateImpl = fn;
    },
    setMotionExit: (fn: TracingMotion['exit']) => {
      motionExitImpl = fn;
    },
    restore: () => {
      trace.reset();
      dom.restore();
    },
  };
}

test('both directions share result rows; minimized results restore without another request and return to origin', async () => {
  const h = setupTracing();
  try {
    await h.trace.open('precedents');
    assert.equal(h.list.children[0]?.className, 'trace-item');
    assert.equal(h.panel.open, true);
    assert.equal(h.trace.refs.length, 2);

    await h.trace.locate(h.makeRef('A1'));
    assert.equal(h.dock.hidden, false);
    assert.equal(h.panel.open, false);
    assert.equal(h.trace.source?.ref, 'C3');

    h.trace.restore();
    h.flushClose();
    assert.equal(h.panel.open, true);
    assert.equal(h.trace.refs.length, 2);
    assert.equal(h.calls.length, 0);

    await h.trace.goOrigin();
    assert.equal(h.state.selected?.ref, 'C3');
    assert.equal(h.trace.source.ref, 'C3');
    assert.equal(h.layer.hidden, false);

    h.flushClose();
    await h.trace.open('dependents');
    assert.equal(h.list.children[0].className, 'trace-item');
    assert.equal(h.scopeRow.hidden, false);
    assert.match(h.title.textContent, /追踪从属/);
  } finally {
    h.restore();
  }
});

test('arrival stays for two seconds; stale expiry and scrolling do not hide or extend a newer cue', async () => {
  const h = setupTracing();
  try {
    await h.trace.open('precedents');
    const oldTimerId = h.trace.pulseTimer as number;
    const old = h.timers.get(oldTimerId)?.fn;
    assert.ok(old);
    assert.equal(h.layer.hidden, false);
    h.advance(1000);
    await h.trace.locate(h.makeRef('A1'));
    old();
    assert.equal(h.layer.hidden, false);
    h.trace.refreshPulse();
    h.advance(1999);
    assert.equal(h.layer.hidden, false);
    h.advance(1);
    assert.equal(h.layer.hidden, true);
    assert.equal(h.getTraced().length, 2);
  } finally {
    h.restore();
  }
});

test('a queued dialog-close event and an old exit animation cannot erase restored tracing', async () => {
  const h = setupTracing();
  try {
    await h.trace.open('precedents');
    let finish: ((value: boolean) => void) | undefined;
    h.setMotionExit(
      () =>
        new Promise<boolean>((r) => {
          finish = r;
        }),
    );
    const pending = h.trace.minimize();
    h.trace.restore();
    assert.ok(finish);
    finish(true);
    await pending;
    assert.equal(h.panel.open, true);
    assert.equal(h.dock.hidden, true);
    assert.equal(h.trace.refs.length, 2);

    h.setMotionExit(async () => true);
    await h.trace.minimize();
    h.trace.restore();
    h.flushClose();
    assert.equal(h.panel.open, true);
    assert.equal(h.trace.source?.ref, 'C3');
  } finally {
    h.restore();
  }
});

test('late searches are discarded and only owned jobs are cancelled; truncation and failure remain explicit', async () => {
  let release: ((value: { readonly jobId: string; readonly reused: boolean }) => void) | undefined;
  const pending = new Promise<{ readonly jobId: string; readonly reused: boolean }>((r) => {
    release = r;
  });
  const h = setupTracing((path) => (path === '/api/search/start' ? pending : undefined));
  try {
    const old = h.trace.open('dependents');
    h.trace.reset();
    await h.trace.open('precedents');
    assert.ok(release);
    release({ jobId: 'obsolete', reused: false });
    await old;
    assert.ok(h.calls.some((c) => c.path === '/api/search/cancel' && c.body?.id === 'obsolete'));
    assert.match(h.title.textContent, /追踪引用/);
  } finally {
    h.restore();
  }

  const reused = setupTracing((path) =>
    path === '/api/search/start'
      ? { jobId: 'shared', reused: true }
      : path.startsWith('/api/search/status')
        ? { status: 'running', matches: [], progress: 10 }
        : undefined,
  );
  try {
    const run = reused.trace.open('dependents');
    await tick();
    reused.trace.reset();
    await run;
    assert.equal(
      reused.calls.some((c) => c.path === '/api/search/cancel'),
      false,
    );
  } finally {
    reused.restore();
  }

  const truncated = setupTracing((path) =>
    path.startsWith('/api/search/status') ? { status: 'done', matches: [], truncated: true } : undefined,
  );
  try {
    await truncated.trace.open('dependents');
    assert.match(truncated.summary.textContent, /截断/);
  } finally {
    truncated.restore();
  }

  const failed = setupTracing((path) =>
    path.startsWith('/api/search/status')
      ? { status: 'error', matches: [], error: '索引失败' }
      : undefined,
  );
  try {
    await failed.trace.open('dependents');
    assert.match(failed.summary.textContent, /未完成.*索引失败/);
  } finally {
    failed.restore();
  }
});

test('an interrupted navigation cannot show a cue for the previous destination', async () => {
  const h = setupTracing();
  try {
    await h.trace.open('precedents');
    let finish: (() => void) | undefined;
    h.setNavigate(async (sheet, ref, guard) => {
      await new Promise<void>((r) => {
        finish = r;
      });
      if (!guard || guard()) {
        const p = h.parseRef(ref);
        if (p) {
          h.state.selected = {
            sheet,
            row: p.row,
            col: p.col,
            ref: p.ref,
            raw: '',
            display: '',
            hasFormula: false,
          };
        }
      }
    });
    const pending = h.trace.locate(h.makeRef('A1'));
    await tick();
    h.trace.selectionChanged();
    assert.ok(finish);
    finish();
    await pending;
    assert.equal(h.state.selected?.ref, 'C3');
    assert.equal(h.layer.hidden, true);
  } finally {
    h.restore();
  }
});

test('a search completing after a partial-result jump does not replace or prolong the arrival cue', async () => {
  let polls = 0;
  let release: ((value: unknown) => void) | undefined;
  const h = setupTracing((path) =>
    path.startsWith('/api/search/status')
      ? ++polls === 1
        ? { status: 'running', matches: [{ ref: 'A1', formula: 'C3' }] }
        : new Promise((resolve) => {
            release = resolve;
          })
      : undefined,
  );
  try {
    const search = h.trace.open('dependents');
    await tick();
    await h.trace.locate(h.makeRef('A1'));
    const deadline = h.trace.pulseUntil;
    h.advance(250);
    await tick();
    assert.ok(release);
    release({ status: 'done', matches: [{ ref: 'A1', formula: 'C3' }] });
    await search;
    assert.equal(h.trace.pulseUntil, deadline);
    assert.match(h.trace.pulseLabel, /源表!A1/);
    h.advance(1749);
    assert.equal(h.layer.hidden, false);
    h.advance(1);
    assert.equal(h.layer.hidden, true);
  } finally {
    h.restore();
  }
});

test('the application rejects delayed navigation after a sheet, workbook or trace intent changes', async () => {
  for (const change of ['guard', 'sheet', 'book'] as const) {
    const dom = installDom({ fromIndexHtml: true });
    try {
      let release: (() => void) | undefined;
      let allowed = true;
      let selected = 0;
      const S = createInitialWorkbookState(1_048_576, 16_384);
      S.workbookKey = 'old';
      S.sheet = '源表';
      S.sheetEpoch = 1;
      S.selected = null;
      S.filtered = false;
      S.maxRow = 100;
      S.maxCol = 20;
      S.cellMap.set('A1', makeCell({ ref: 'A1', col: 1, raw: '12', display: '12' }));

      const nav = new GridNavigation(
        makeDefaultNavigationDeps({
          S,
          loadViewport: () =>
            new Promise<void>((r) => {
              release = r;
            }),
        }),
      );
      nav.selectCell = () => {
        selected++;
      };
      nav.recordVisit = () => undefined;

      const request = nav.navigateToCell('源表', 'A1', true, () => allowed);
      await tick();
      if (change === 'guard') allowed = false;
      if (change === 'sheet') S.sheetEpoch++;
      if (change === 'book') S.workbookKey = 'new';
      assert.ok(release);
      release();
      await request;
      assert.equal(selected, 0, change);
    } finally {
      dom.restore();
    }
  }
});
