import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { ApiRequestOptions } from '../contracts/common.ts';
import type { CatalogueArea } from '../contracts/features.ts';
import { createInitialWorkbookState } from '../grid/state.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import { DataRules } from './data-rules.ts';

const tick = (): Promise<void> =>
  new Promise((resolve) => {
    setImmediate(resolve);
  });

function setupDataRules(
  override?: (
    route: string,
    options: ApiRequestOptions | undefined,
    body: Record<string, unknown> | undefined,
  ) => unknown,
) {
  const dom = installDom({ fromIndexHtml: true });
  const calls: { readonly route: string; readonly body: Record<string, unknown> | undefined }[] = [];
  const arrivals: Pick<CatalogueArea, 'sheet' | 'start' | 'end'>[] = [];
  const toasts: [string, string | undefined][] = [];
  let seq = 0;
  let supportedFlag = true;

  const state = createInitialWorkbookState(1_048_576, 16_384);
  state.opened = true;
  state.indexReady = true;
  state.sheet = '明细';
  state.sheetEpoch = 1;
  state.workbookKey = 'content';
  state.book = 'path-1';
  state.fileName = 'source.xlsx';
  state.selected = {
    sheet: '明细',
    row: 2,
    col: 1,
    ref: 'A2',
    raw: '',
    display: '',
    hasFormula: false,
  };

  const summaries: {
    id: number;
    type: string;
    status: string;
    sqref: string;
    rangeCount: number;
    listItems: number;
    listSource?: string;
    shortened: boolean;
  }[] = [
    {
      id: 1,
      type: 'list',
      status: 'described',
      sqref: 'A1:A5',
      rangeCount: 5,
      listItems: 5,
      listSource: 'inline',
      shortened: false,
    },
    {
      id: 2,
      type: 'whole',
      status: 'invalid',
      sqref: '',
      rangeCount: 0,
      listItems: 0,
      shortened: false,
    },
  ];
  const identities = new Map<
    string,
    { readonly workbookKey: string; readonly book: string; readonly sheet: string }
  >();
  const areas = Array.from({ length: 5 }, (_, i) => ({
    row: i + 1,
    col: 1,
    endRow: i + 1,
    endCol: 1,
  }));
  const values = ['001', '', '<img src=x onerror=alert(1)>', '004', '005'];

  const api = async <T = unknown>(route: string, options?: ApiRequestOptions): Promise<T> => {
    const body =
      typeof options?.body === 'string'
        ? (JSON.parse(options.body) as Record<string, unknown>)
        : undefined;
    calls.push({ route, body });
    const special = override?.(route, options, body);
    if (special !== undefined) return (await Promise.resolve(special)) as T;
    const url = new URL(route, 'http://local');
    const q = url.searchParams;
    const id = q.get('id') ?? '';
    const identity = identities.get(id) ?? {
      workbookKey: state.workbookKey,
      book: state.book,
      sheet: state.sheet ?? '',
    };
    if (url.pathname === '/api/data-rules/start') {
      const jobId = `rule-${++seq}`;
      identities.set(jobId, {
        workbookKey: typeof body?.workbookKey === 'string' ? body.workbookKey : '',
        book: typeof body?.book === 'string' ? body.book : '',
        sheet: typeof body?.sheet === 'string' ? body.sheet : '',
      });
      const res: unknown = { jobId };
      return res as T;
    }
    if (url.pathname === '/api/data-rules/status') {
      const res: unknown = { id, status: 'done', ...identity };
      return res as T;
    }
    if (url.pathname === '/api/data-rules/items') {
      const res: unknown = {
        page: {
          items: summaries,
          offset: 0,
          previous: 0,
          next: 2,
          matched: 2,
          total: 2,
          scopeComplete: false,
        },
        ref: q.get('ref') ?? '',
        summary: {
          rules: 2,
          unresolved: 1,
          unknownScopes: 1,
          complete: true,
          disablePrompts: false,
          promptSettingsKnown: true,
        },
        ...identity,
      };
      return res as T;
    }
    if (url.pathname === '/api/data-rules/detail') {
      const item = summaries.find((n) => n.id === Number(q.get('item')));
      const r = Number(q.get('rangeOffset') || 0);
      const l = Number(q.get('listOffset') || 0);
      const count = item?.id === 1 ? 5 : 0;
      const res: unknown = {
        detail: {
          item: {
            ...item,
            ranges: areas.slice(r, Math.min(count, r + 3)),
            inlineList: values.slice(l, Math.min(count, l + 3)),
            formula1: '"001,,中文"',
            rawXml: '<dataValidation value="<img>">',
            attributes: { allowBlank: '1' },
            allowBlank: true,
          },
          rangeCount: count,
          rangeOffset: r,
          rangePrevious: Math.max(0, r - 3),
          rangeNext: Math.min(count, r + 3),
          listCount: count,
          listOffset: l,
          listPrevious: Math.max(0, l - 3),
          listNext: Math.min(count, l + 3),
        },
        ...identity,
      };
      return res as T;
    }
    if (url.pathname === '/api/data-rules/resolve') {
      const at = Number(q.get('area') || 0);
      const res: unknown = {
        area: {
          sheet: state.sheet ?? '',
          start: `A${at + 1}`,
          end: `A${at + 1}`,
          row: at + 1,
          col: 1,
          endRow: at + 1,
          endCol: 1,
        },
        ...identity,
      };
      return res as T;
    }
    const res: unknown = { changed: true };
    return res as T;
  };

  const rules = new DataRules({
    state,
    api,
    toast: (msg, tone) => toasts.push([msg, tone]),
    supported: () => supportedFlag,
    limits: { pageRows: 3 },
    sleep: async () => undefined,
    navigate: async (area, guard) => {
      if (guard()) {
        arrivals.push(area);
        return true;
      }
      return false;
    },
  });

  return {
    rules,
    state,
    calls,
    arrivals,
    toasts,
    summaries,
    setSupported: (v: boolean) => {
      supportedFlag = v;
    },
    restore: () => {
      rules.resetBook();
      dom.restore();
    },
  };
}

test('rules use explicit metadata IO, safe text, bounded detail arrays and source navigation', async () => {
  const h = setupDataRules();
  try {
    await h.rules.open();
    assert.equal(h.rules.detail?.item.id, 1);
    const valuesEl = document.getElementById('dataRuleValues');
    assert.ok(valuesEl);
    assert.equal(valuesEl.children.length, 3);
    assert.equal(valuesEl.children[0]?.textContent, '001');
    assert.equal(valuesEl.children[1]?.textContent, '（空白项）');
    assert.equal(valuesEl.querySelector('img'), null);
    assert.match(document.getElementById('dataRuleStatus')?.textContent ?? '', /范围未知/);
    assert.equal(
      h.calls.some((c) => /\/api\/(?:range|cell|index|analysis)/.test(c.route)),
      false,
    );

    await h.rules.select(1, false, 3, 3);
    const areaSelect = document.getElementById('dataRuleArea') as HTMLSelectElement;
    assert.equal(areaSelect.options.length, 2);
    assert.equal(document.getElementById('dataRuleValues')?.children.length, 2);
    assert.equal(areaSelect.value, '3');
    await h.rules.locate();
    assert.equal(h.arrivals[0]?.start, 'A4');
    assert.equal(h.rules.dialog.open, false);
  } finally {
    h.restore();
  }
});

test('cancel then immediate retry owns the late start without replacing accepted rules', async () => {
  let release: ((value: unknown) => void) | undefined;
  let starts = 0;
  const h = setupDataRules((route) =>
    route === '/api/data-rules/start' && ++starts === 1
      ? new Promise((resolve) => {
          release = resolve;
        })
      : undefined,
  );
  try {
    const old = h.rules.open();
    await tick();
    h.rules.cancel();
    await h.rules.refresh();
    const current = h.rules.job;
    release?.({ jobId: 'late-rule' });
    await old;
    assert.equal(h.rules.job, current);
    assert.ok(h.calls.some((c) => c.route === '/api/data-rules/cancel' && c.body?.id === 'late-rule'));
    assert.equal(h.rules.busy, false);
  } finally {
    h.restore();
  }
});

test('late definitions, page responses and source-path changes cannot overwrite the current selection', async () => {
  let hold = false;
  let release: ((value: unknown) => void) | undefined;
  const h = setupDataRules((route) =>
    hold && route.includes('/detail?') && route.includes('item=2')
      ? new Promise((resolve) => {
          release = resolve;
        })
      : undefined,
  );
  try {
    await h.rules.open();
    hold = true;
    const old = h.rules.select(2);
    await tick();
    await h.rules.select(1);
    release?.({ detail: { item: { id: 2 } }, workbookKey: 'content', book: 'path-1', sheet: '明细' });
    await old;
    assert.equal(h.rules.detail?.item.id, 1);
  } finally {
    h.restore();
  }

  let releasePage: ((value: unknown) => void) | undefined;
  hold = false;
  const g = setupDataRules((route) =>
    hold && route.includes('/items?')
      ? new Promise((resolve) => {
          releasePage = resolve;
        })
      : undefined,
  );
  try {
    await g.rules.open();
    hold = true;
    const pending = g.rules.loadPage(0);
    await tick();
    g.state.book = 'path-2';
    g.rules.resetBook();
    releasePage?.({ page: { items: [] }, workbookKey: 'content', book: 'path-1', sheet: '明细' });
    await pending;
    assert.equal(g.rules.page, null);
    assert.equal(g.rules.dialog.open, false);
  } finally {
    g.restore();
  }
});

test('same-content paths and sheet epochs reject delayed navigation before moving the grid', async () => {
  let release: ((value: unknown) => void) | undefined;
  const h = setupDataRules((route) =>
    route.includes('/resolve?')
      ? new Promise((resolve) => {
          release = resolve;
        })
      : undefined,
  );
  try {
    await h.rules.open();
    const old = h.rules.locate();
    await tick();
    h.state.book = 'path-2';
    h.rules.resetBook();
    release?.({
      area: { sheet: '明细', start: 'A1', end: 'A5' },
      workbookKey: 'content',
      book: 'path-1',
      sheet: '明细',
    });
    await old;
    assert.equal(h.arrivals.length, 0);

    await h.rules.open();
    const action = h.rules.locate();
    await tick();
    h.state.sheetEpoch++;
    release?.({
      area: { sheet: '明细', start: 'A1', end: 'A5' },
      workbookKey: 'content',
      book: 'path-2',
      sheet: '明细',
    });
    await action;
    assert.equal(h.arrivals.length, 0);
  } finally {
    h.restore();
  }
});

test('a failed refreshed first page preserves the adopted snapshot and releases its candidate', async () => {
  let fail = false;
  const h = setupDataRules((route) =>
    fail && route.includes('/items?') ? Promise.reject(new Error('读取失败')) : undefined,
  );
  try {
    await h.rules.open();
    const accepted = h.rules.job;
    const page = h.rules.page;
    const detail = h.rules.detail;
    fail = true;
    await h.rules.refresh();
    assert.equal(h.rules.job, accepted);
    assert.equal(h.rules.page, page);
    assert.equal(h.rules.detail, detail);
    assert.match(document.getElementById('dataRuleStatus')?.textContent ?? '', /读取失败/);
    assert.ok(h.calls.some((c) => c.route === '/api/data-rules/clear' && c.body?.id !== accepted));
    assert.equal(
      h.calls.some((c) => c.route === '/api/data-rules/clear' && c.body?.id === accepted),
      false,
    );
  } finally {
    h.restore();
  }
});

test('empty hits preserve unknown coverage, and malformed paging cannot be adopted', async () => {
  let mode: 'unknown' | 'complete' | 'bad' = 'unknown';
  const h = setupDataRules((route) =>
    route.includes('/items?')
      ? {
          page: {
            items: [],
            offset: 0,
            previous: 0,
            next: mode === 'bad' ? 2 : 0,
            matched: 0,
            total: 0,
            scopeComplete: mode === 'complete',
          },
          summary: {
            rules: 0,
            unresolved: 0,
            unknownScopes: 0,
            complete: mode === 'complete',
            disablePrompts: false,
            promptSettingsKnown: true,
          },
          ref: 'A2',
          workbookKey: 'content',
          book: 'path-1',
          sheet: '明细',
        }
      : undefined,
  );
  try {
    await h.rules.open();
    assert.match(
      document.getElementById('dataRuleList')?.children[0]?.textContent ?? '',
      /未读取或范围未知/,
    );
    mode = 'complete';
    await h.rules.loadPage(0);
    assert.match(document.getElementById('dataRuleList')?.children[0]?.textContent ?? '', /没有保存/);
    const accepted = h.rules.page;
    mode = 'bad';
    await h.rules.loadPage(0);
    assert.equal(h.rules.page, accepted);
    assert.match(document.getElementById('dataRuleStatus')?.textContent ?? '', /分页响应无效/);
  } finally {
    h.restore();
  }
});

test('unsupported sources and unprepared sheets stay disabled without querying', async () => {
  const h = setupDataRules();
  try {
    h.setSupported(false);
    h.rules.controls();
    await h.rules.open();
    assert.equal((document.getElementById('dataRulesOpenBtn') as HTMLButtonElement).disabled, true);
    assert.equal(h.calls.length, 0);

    h.setSupported(true);
    h.state.indexReady = false;
    h.rules.controls();
    await h.rules.open();
    assert.equal(h.calls.length, 0);
    assert.equal(h.rules.dialog.open, false);
  } finally {
    h.restore();
  }
});

test('source-bound detail arrays reject excess pages and unknown scopes disable navigation', async () => {
  let bad = false;
  const h = setupDataRules((route) =>
    bad && route.includes('/detail?')
      ? {
          detail: {
            item: { id: 1, ranges: [], inlineList: [] },
            rangeCount: 0,
            rangeOffset: 0,
            rangePrevious: 0,
            rangeNext: 50,
            listCount: 0,
            listOffset: 0,
            listPrevious: 0,
            listNext: 0,
          },
          workbookKey: 'content',
          book: 'path-1',
          sheet: '明细',
        }
      : undefined,
  );
  try {
    await h.rules.open();
    await h.rules.select(2);
    assert.equal((document.getElementById('dataRuleLocate') as HTMLButtonElement).disabled, true);
    await h.rules.locate();
    assert.equal(h.arrivals.length, 0);

    bad = true;
    await h.rules.select(1);
    assert.equal(h.rules.detail, null);
    assert.match(document.getElementById('dataRuleStatus')?.textContent ?? '', /详情响应无效/);
  } finally {
    h.restore();
  }
});

test('a completed status from the wrong source releases its unadopted server owner', async () => {
  let foreign = false;
  const h = setupDataRules((route) =>
    foreign && route.startsWith('/api/data-rules/status?')
      ? {
          id: new URL(route, 'http://local').searchParams.get('id'),
          status: 'done',
          workbookKey: 'content',
          book: 'another-path',
          sheet: '明细',
        }
      : undefined,
  );
  try {
    await h.rules.open();
    const adopted = h.rules.job;
    foreign = true;
    await h.rules.refresh();
    assert.equal(h.rules.job, adopted);
    assert.match(document.getElementById('dataRuleStatus')?.textContent ?? '', /来源已切换/);
    assert.ok(
      h.calls.some((c) => c.route === '/api/data-rules/cancel' && c.body?.id !== adopted),
      'completed but rejected status must still release its owned task',
    );
  } finally {
    h.restore();
  }
});

test('unknown source enum text cannot resolve JavaScript object prototype properties', async () => {
  const h = setupDataRules();
  try {
    if (h.summaries[0]) {
      h.summaries[0].type = 'constructor';
      h.summaries[0].status = 'invalid';
    }
    await h.rules.open();
    assert.equal(
      document.getElementById('dataRuleDetail')?.children[0]?.textContent,
      '#1 constructor',
    );
    assert.equal(
      document.getElementById('dataRuleList')?.children[0]?.children[0]?.textContent,
      '#1 未知类型：constructor',
    );
  } finally {
    h.restore();
  }
});
