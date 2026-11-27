import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { ApiRequestOptions } from '../contracts/common.ts';
import type { CatalogueArea } from '../contracts/features.ts';
import { createInitialWorkbookState } from '../grid/state.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import { GridNavigation, makeDefaultNavigationDeps } from '../test-helpers/grid-fixture.ts';
import { Catalogue } from './catalogue.ts';

const tick = (): Promise<void> =>
  new Promise((resolve) => {
    setImmediate(resolve);
  });

function setupCatalogue(
  override?: (
    route: string,
    options: ApiRequestOptions | undefined,
    body: Record<string, unknown> | undefined,
  ) => unknown,
) {
  const dom = installDom({ fromIndexHtml: true });
  const calls: { readonly route: string; readonly body: Record<string, unknown> | undefined }[] = [];
  const notices: [string, string | undefined][] = [];
  const arrivals: Pick<CatalogueArea, 'sheet' | 'start' | 'end'>[] = [];
  let next = 0;
  let supportedFlag = true;

  const state = createInitialWorkbookState(1_048_576, 16_384);
  state.opened = true;
  state.indexReady = true;
  state.workbookKey = 'content';
  state.book = 'path-1';
  state.fileName = 'source.xlsx';
  state.sheet = '明细';
  state.sheetEpoch = 1;
  state.selected = {
    sheet: '明细',
    row: 1,
    col: 1,
    ref: 'A1',
    raw: '',
    display: '',
    hasFormula: false,
  };
  state.sheets = [{ name: '明细' }, { name: '汇总' }];

  const baseItems: {
    id: string;
    kind: string;
    name: string;
    scope: string;
    sheet: string;
    status: string;
    areas: number;
  }[] = [
    { id: 'range', kind: 'name', name: '金额范围', scope: '', sheet: '', status: 'ready', areas: 1 },
    { id: 'table', kind: 'table', name: '账表', scope: '', sheet: '明细', status: 'unresolved', areas: 0 },
    { id: 'external', kind: 'name', name: '外部', scope: '', sheet: '', status: 'unavailable', areas: 0 },
  ];
  const area: CatalogueArea = { sheet: '明细', start: 'B2', end: 'B4', row: 2, col: 2, endRow: 4, endCol: 2 };
  const identities = new Map<string, { readonly workbookKey: string; readonly book: string }>();

  const api = async <T = unknown>(route: string, options?: ApiRequestOptions): Promise<T> => {
    const body =
      typeof options?.body === 'string'
        ? (JSON.parse(options.body) as Record<string, unknown>)
        : undefined;
    calls.push({ route, body });
    const special = override?.(route, options, body);
    if (special !== undefined) return (await Promise.resolve(special)) as T;
    const parsed = new URL(route, 'http://local');
    const q = parsed.searchParams;
    const id = q.get('id') ?? '';
    const identity = identities.get(id) ?? { workbookKey: state.workbookKey, book: state.book };
    if (parsed.pathname === '/api/catalogue/start') {
      const jobId = `job-${++next}`;
      identities.set(jobId, {
        workbookKey: typeof body?.workbookKey === 'string' ? body.workbookKey : '',
        book: typeof body?.book === 'string' ? body.book : '',
      });
      const res: unknown = { jobId };
      return res as T;
    }
    if (parsed.pathname === '/api/catalogue/status') {
      const res: unknown = { id, status: 'done', stage: '目录读取完成', ...identity };
      return res as T;
    }
    if (parsed.pathname === '/api/catalogue/items') {
      const res: unknown = {
        items: baseItems,
        offset: 0,
        previous: 0,
        next: 3,
        matched: 3,
        total: 3,
        complete: true,
        notices: [],
        ...identity,
      };
      return res as T;
    }
    if (parsed.pathname === '/api/catalogue/detail') {
      const item = baseItems.find((entry) => entry.id === q.get('item'));
      const res: unknown = {
        item: item
          ? {
              ...item,
              definition: item.id === 'range' ? "'明细'!$B$2:$B$4" : item.name,
              reason: item.id === 'range' ? '' : '尚未确认',
              areas: item.id === 'range' ? [area] : [],
            }
          : undefined,
        ...identity,
      };
      return res as T;
    }
    if (parsed.pathname === '/api/catalogue/resolve') {
      const res: unknown = { area, ...identity };
      return res as T;
    }
    const res: unknown = { changed: true };
    return res as T;
  };

  const directory = new Catalogue({
    state,
    api,
    toast: (msg, tone) => notices.push([msg, tone]),
    supported: () => supportedFlag,
    limits: { pageRows: 3, searchBytes: 1024 },
    sleep: async () => undefined,
    navigate: async (value, guard) => {
      if (guard()) {
        arrivals.push(value);
        return true;
      }
      return false;
    },
  });

  return {
    directory,
    state,
    calls,
    notices,
    arrivals,
    area,
    baseItems,
    setSupported: (v: boolean) => {
      supportedFlag = v;
    },
    restore: () => {
      directory.resetBook();
      dom.restore();
    },
  };
}

test('directory lists metadata, loads selected definitions and resolves source areas without source queries', async () => {
  const h = setupCatalogue();
  try {
    await h.directory.open();
    assert.equal(h.directory.page?.total, 3);
    assert.equal(h.directory.item?.id, 'range');
    assert.equal(document.getElementById('catalogueList')?.children.length, 3);
    assert.equal((document.getElementById('catalogueLocate') as HTMLButtonElement).disabled, false);

    await h.directory.locate();
    assert.equal(h.arrivals.length, 1);
    assert.ok(h.arrivals[0]);
    assert.equal(h.arrivals[0].start, 'B2');
    assert.equal(h.arrivals[0].end, 'B4');
    assert.equal(h.directory.dialog.open, false);
    assert.ok(h.calls.some((c) => c.route.startsWith('/api/catalogue/resolve?')));
    assert.equal(
      h.calls.some((c) => /\/api\/(?:sheet|range|cell|analysis\/query|index\/start)/.test(c.route)),
      false,
    );

    await h.directory.open();
    await h.directory.select('table', false);
    assert.equal((document.getElementById('catalogueLocate') as HTMLButtonElement).disabled, true);
    await h.directory.locate();
    assert.equal(h.arrivals.length, 1);
    assert.match(document.getElementById('catalogueDetail')?.textContent ?? '', /尚未确认/);
  } finally {
    h.restore();
  }
});

test('cancel during a delayed start owns its late ID and cannot replace a retry', async () => {
  let release: ((value: unknown) => void) | undefined;
  let starts = 0;
  const h = setupCatalogue((route) =>
    route === '/api/catalogue/start' && ++starts === 1
      ? new Promise((resolve) => {
          release = resolve;
        })
      : undefined,
  );
  try {
    const old = h.directory.open();
    await tick();
    h.directory.cancel();
    const retry = h.directory.refresh('read');
    await retry;
    const current = h.directory.job;
    release?.({ jobId: 'late-owned' });
    await old;
    assert.equal(h.directory.job, current);
    assert.ok(h.calls.some((c) => c.route === '/api/catalogue/cancel' && c.body?.id === 'late-owned'));
    assert.equal(h.directory.busy, false);
  } finally {
    h.restore();
  }
});

test('late filtered pages and details cannot overwrite newer selections or reopen a closed dialog', async () => {
  let holdPage = false;
  let releasePage: ((value: unknown) => void) | undefined;
  let holdDetail = false;
  let releaseDetail: ((value: unknown) => void) | undefined;
  const h = setupCatalogue((route) => {
    if (holdPage && route.startsWith('/api/catalogue/items?')) {
      return new Promise((resolve) => {
        releasePage = resolve;
      });
    }
    if (holdDetail && route.includes('/api/catalogue/detail?') && route.includes('item=table')) {
      return new Promise((resolve) => {
        releaseDetail = resolve;
      });
    }
    return undefined;
  });
  try {
    await h.directory.open();
    holdDetail = true;
    const oldDetail = h.directory.select('table', false);
    await tick();
    await h.directory.select('range', false);
    releaseDetail?.({
      item: { id: 'table', name: '迟到表', definition: 'late' },
      workbookKey: 'content',
      book: 'path-1',
    });
    await oldDetail;
    assert.equal(h.directory.item?.id, 'range');

    holdPage = true;
    const old = h.directory.loadPage(0);
    await tick();
    h.directory.close();
    holdPage = false;
    await h.directory.open();
    const accepted = h.directory.page;
    releasePage?.({
      items: [],
      offset: 0,
      previous: 0,
      next: 0,
      matched: 0,
      total: 0,
      complete: true,
      notices: [],
      workbookKey: 'content',
      book: 'path-1',
    });
    await old;
    assert.equal(h.directory.page, accepted);
    assert.equal(h.directory.dialog.open, true);
  } finally {
    h.restore();
  }
});

test('equal content at another path rejects old responses and interrupts pending navigation', async () => {
  let release: ((value: unknown) => void) | undefined;
  const h = setupCatalogue((route) =>
    route.startsWith('/api/catalogue/resolve?')
      ? new Promise((resolve) => {
          release = resolve;
        })
      : undefined,
  );
  try {
    await h.directory.open();
    const pending = h.directory.locate();
    await tick();
    h.state.book = 'path-2';
    h.directory.resetBook();
    release?.({ area: h.area, workbookKey: 'content', book: 'path-1' });
    await pending;
    assert.equal(h.arrivals.length, 0);
    assert.equal(h.directory.item, null);

    h.state.book = 'path-1';
    await h.directory.open();
    const action = h.directory.locate();
    await tick();
    document.dispatchEvent(new Event('pointerdown'));
    release?.({ area: h.area, workbookKey: 'content', book: 'path-1' });
    await action;
    assert.equal(h.arrivals.length, 0);
  } finally {
    h.restore();
  }
});

test('malformed source bindings cannot publish definitions or navigate, and unknown formats keep the entry disabled', async () => {
  let bad = false;
  const h = setupCatalogue((route) =>
    bad && route.startsWith('/api/catalogue/detail?')
      ? { item: { id: 'table', name: 'foreign' }, workbookKey: 'content', book: 'other-path' }
      : undefined,
  );
  try {
    await h.directory.open();
    bad = true;
    await h.directory.select('table', false);
    assert.equal(h.directory.item, null);
    assert.match(document.getElementById('catalogueStatus')?.textContent ?? '', /来源已切换/);

    h.setSupported(false);
    h.directory.resetBook();
    assert.equal((document.getElementById('catalogueOpenBtn') as HTMLButtonElement).disabled, true);
    await h.directory.open();
    assert.equal(h.directory.dialog.open, false);
  } finally {
    h.restore();
  }
});

test('literal source names stay out of HTML and long column lists have bounded page DOM', async () => {
  const malicious = '<img src=x onerror=alert(1)>';
  const h = setupCatalogue((route) =>
    route.includes('/api/catalogue/detail?')
      ? {
          item: {
            id: 'range',
            kind: 'table',
            name: malicious,
            definition: malicious,
            reason: '原文保留',
            status: 'unresolved',
            table: {
              headerRows: 1,
              totalRows: 0,
              columns: Array.from({ length: 11 }, (_, i) => ({ id: String(i), name: `列${i}` })),
            },
          },
          workbookKey: 'content',
          book: 'path-1',
        }
      : undefined,
  );
  try {
    await h.directory.open();
    const detail = document.getElementById('catalogueDetail');
    assert.ok(detail);
    assert.ok(detail.textContent.includes(malicious));
    assert.equal(detail.querySelector('img'), null);
    const table = detail.querySelector('table');
    assert.ok(table);
    assert.equal(table.children[0]?.children.length, 3);
    const paging = detail.children[detail.children.length - 1];
    const nextBtn = paging?.children[paging.children.length - 1] as HTMLButtonElement | undefined;
    assert.ok(nextBtn);
    nextBtn.click();
    const nextTable = detail.querySelector('table');
    assert.equal(nextTable?.children[0]?.children.length, 3);
    assert.equal(nextTable.children[0].children[0]?.children[1]?.textContent, '列3');
  } finally {
    h.restore();
  }
});

test('confirmation is explicit and needs the prepared selected sheet, while search rejects over-budget input', async () => {
  const h = setupCatalogue();
  try {
    await h.directory.open();
    const targetSheet = document.getElementById('catalogueTargetSheet') as HTMLSelectElement;
    targetSheet.value = '汇总';
    h.directory.controls();
    assert.equal((document.getElementById('catalogueConfirm') as HTMLButtonElement).disabled, true);
    const before = h.calls.length;
    await h.directory.refresh('confirm');
    assert.equal(h.calls.length, before);

    targetSheet.value = '明细';
    h.directory.controls();
    await h.directory.refresh('confirm');
    assert.ok(h.calls.some((c) => c.body?.mode === 'confirm' && c.body.sheet === '明细'));

    (document.getElementById('catalogueSearch') as HTMLInputElement).value = '长'.repeat(400);
    const queries = h.calls.length;
    await h.directory.loadPage(0);
    assert.equal(h.calls.length, queries);
    assert.match(document.getElementById('catalogueStatus')?.textContent ?? '', /超过容量/);
  } finally {
    h.restore();
  }
});

test('a refresh page failure keeps the previous directory and releases only the unadopted candidate', async () => {
  let failed = false;
  const h = setupCatalogue((route) =>
    failed && route.startsWith('/api/catalogue/items?')
      ? Promise.reject(new Error('读取失败'))
      : undefined,
  );
  try {
    await h.directory.open();
    const job = h.directory.job;
    const item = h.directory.item;
    const page = h.directory.page;
    failed = true;
    await h.directory.refresh('read');
    assert.equal(h.directory.job, job);
    assert.equal(h.directory.item, item);
    assert.equal(h.directory.page, page);
    assert.match(document.getElementById('catalogueStatus')?.textContent ?? '', /读取失败/);
    assert.ok(h.calls.some((c) => c.route === '/api/catalogue/clear' && c.body?.id !== job));
    assert.equal(
      h.calls.some((c) => c.route === '/api/catalogue/clear' && c.body?.id === job),
      false,
    );
  } finally {
    h.restore();
  }
});

test('an unconfirmed table offers its real owning sheet without preparing or changing the current view', async () => {
  const h = setupCatalogue();
  try {
    await h.directory.open();
    if (h.baseItems[1]) h.baseItems[1].sheet = '汇总';
    await h.directory.select('table', false);
    assert.equal((document.getElementById('catalogueTargetSheet') as HTMLSelectElement).value, '汇总');
    assert.equal((document.getElementById('catalogueConfirm') as HTMLButtonElement).disabled, true);
    assert.equal(h.state.sheet, '明细');
    await h.directory.openSheet();
    assert.ok(h.arrivals[0]);
    assert.equal(h.arrivals[0].sheet, '汇总');
    assert.equal(h.arrivals[0].start, 'A1');
  } finally {
    h.restore();
  }
});

test('shared grid arrival preserves source coordinates and rejects filtered rectangles and late guards', async () => {
  const dom = installDom({ fromIndexHtml: true });
  try {
    const state = createInitialWorkbookState(1_048_576, 16_384);
    state.opened = true;
    state.book = 'path';
    state.workbookKey = 'content';
    state.sheet = '明细';
    state.sheets = [{ name: '明细' }];
    state.selected = {
      sheet: '明细',
      row: 1,
      col: 1,
      ref: 'A1',
      raw: '',
      display: '',
      hasFormula: false,
    };
    state.indexReady = true;
    state.filtered = false;
    state.multiRanges = [{ row1: 1, row2: 1, col1: 1, col2: 1, visual1: 1, visual2: 1 }];

    let release: (() => void) | false | undefined;
    let allowed = true;
    const extendedCalls: {
      readonly anchor: { readonly row: number; readonly col: number };
      readonly row: number;
      readonly col: number;
    }[] = [];

    const nav = new GridNavigation(makeDefaultNavigationDeps({ S: state }));
    nav.navigateToCell = async (_sheet, ref, _trace, guard) => {
      if (release !== false) {
        await new Promise<void>((resolve) => {
          release = resolve;
        });
      }
      if (!guard || guard()) {
        state.selected = {
          sheet: '明细',
          row: 2,
          col: 2,
          ref,
          raw: '',
          display: '',
          hasFormula: false,
        };
        return true;
      }
      return false;
    };
    nav.extendRange = (anchor, row, col) => {
      if (anchor) extendedCalls.push({ anchor, row, col });
    };

    const target = { sheet: '明细', start: 'B2', end: 'D8' };
    const first = nav.navigateCatalogueArea(target, () => allowed);
    await tick();
    allowed = false;
    if (typeof release === 'function') release();
    assert.equal(await first, false);
    assert.equal(extendedCalls.length, 0);

    allowed = true;
    release = false;
    state.filtered = true;
    await assert.rejects(() => nav.navigateCatalogueArea(target, () => true), /先清除筛选/);
    assert.equal(state.filtered, true);

    state.filtered = false;
    assert.equal(await nav.navigateCatalogueArea(target, () => true), true);
    assert.equal(extendedCalls.at(-1)?.anchor.row, 2);
    assert.equal(extendedCalls.at(-1)?.row, 8);
    assert.equal(extendedCalls.at(-1)?.col, 4);
    assert.equal(state.multiRanges.length, 0);
  } finally {
    dom.restore();
  }
});
