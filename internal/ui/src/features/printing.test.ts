import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { ApiRequestOptions } from '../contracts/common.ts';
import type { SourceCapabilities } from '../contracts/formats.ts';
import { createInitialWorkbookState } from '../grid/state.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import { Printing } from './printing.ts';
import type { PrintPageResponse, PrintSummaryResponse } from './printing-model.ts';
import type { PrintingLimits } from '../contracts/formats.ts';

const testPrintLimits: PrintingLimits = {
  outputPages: 20,
  outputBytes: 65536,
  pages: 20,
  rows: 100,
  columns: 20,
  cells: 500,
  areas: 4,
  parts: 50,
  bytes: 65536,
  requestBytes: 8192,
  notices: 32,
  domPixels: 8_000_000,
  unitsPerPoint: 1_000_000_000,
  firstPageMin: -32768,
  firstPageMax: 4_294_967_295,
};

const testPrintSource: SourceCapabilities = {
  id: 'xls',
  maxRows: 65536,
  maxColumns: 256,
  styled: true,
  textAmounts: false,
  conditionalMetadata: false,
  catalogueMetadata: false,
  dataRuleMetadata: false,
  noteMetadata: false,
  pictureMetadata: false,
  printMetadata: true,
  textClustering: false,
};

const tick = (): Promise<void> =>
  new Promise((resolve) => {
    setImmediate(resolve);
  });

function setupPrinting(
  override?: (
    route: string,
    options: ApiRequestOptions | undefined,
    body: Record<string, unknown> | undefined,
  ) => unknown,
) {
  const dom = installDom({ fromIndexHtml: true });
  const calls: {
    readonly route: string;
    readonly body: Record<string, unknown> | undefined;
    readonly signal: AbortSignal | undefined;
  }[] = [];
  const toasts: [string, string | undefined][] = [];
  const bindings = new Map<string, { readonly workbookKey: string; readonly book: string; readonly sheet: string }>();
  let sequence = 0;

  const state = createInitialWorkbookState(65536, 256);
  state.opened = true;
  state.indexReady = true;
  state.sheet = '明细';
  state.sheetEpoch = 1;
  state.workbookKey = 'content';
  state.book = 'path-one';
  state.fileName = '<img src=x>.xlsx';

  const source: { -readonly [K in keyof SourceCapabilities]: SourceCapabilities[K] } = {
    ...testPrintSource,
    printMetadata: true,
  };

  const envelope = (id: string) => ({
    id,
    ...(bindings.get(id) ?? { workbookKey: 'content', book: 'path-one', sheet: '明细' }),
  });

  const summary = (id: string): PrintSummaryResponse => ({
    ...envelope(id),
    sourceComplete: true,
    sourceProperties: {
      headerFooter: {
        oddHeader: {
          type: 'text',
          status: 'stored',
          value: '<img src=x onerror=alert(1)>',
        },
      },
    },
    sourceNotices: [],
    summary: {
      sourceId: 'a'.repeat(64),
      sheet: '明细',
      pages: 3,
      paper: { width: '400000000000', height: '150000000000' },
      margins: { left: '0', right: '0', top: '0', bottom: '0', header: '0', footer: '0' },
      headings: { width: '0', height: '0' },
      scale: { numerator: '1', denominator: '2', percent: '50' },
      areas: [{ row: 1, col: 1, endRow: 3, endCol: 1 }],
      repeatRows: [],
      repeatColumns: [],
      pageOrder: 'downThenOver',
      firstPageNumber: 1,
      defaults: {},
      approximate: false,
      origins: { paper: 'source' },
      notices: [],
    },
  });

  const page = (id: string, index: number): PrintPageResponse => ({
    ...envelope(id),
    page: index,
    content: {
      page: {
        number: index + 1,
        area: 1,
        offsetX: '0',
        offsetY: '0',
        content: { width: '150000000000', height: '7500000000' },
        body: { width: '150000000000', height: '7500000000' },
        rows: [{ first: index + 1, last: index + 1, offset: '0', length: '15000000000', repeated: false }],
        columns: [{ first: 1, last: 1, offset: '0', length: '300000000000', repeated: false }],
        oversize: false,
      },
      rows: [
        {
          number: index + 1,
          part: 0,
          start: '0',
          size: '15000000000',
          clipStart: '0',
          clipSize: '15000000000',
          repeated: false,
        },
      ],
      columns: [
        {
          number: 1,
          part: 0,
          start: '0',
          size: '300000000000',
          clipStart: '0',
          clipSize: '300000000000',
          repeated: false,
        },
      ],
      cells: [
        {
          row: index + 1,
          col: 1,
          ref: `A${index + 1}`,
          style: 0,
          cell: {
            ref: `A${index + 1}`,
            col: 1,
            raw: '9007199254740993.31',
            display: '9,007,199,254,740,993.31',
            hasFormula: false,
            style: 0,
            type: 'n',
          },
          box: { x: '0', y: '0', width: '300000000000', height: '15000000000' },
          clip: { x: '0', y: '0', width: '300000000000', height: '15000000000' },
        },
      ],
      styles: { '0': {} },
      notices: [],
    },
  });

  const api = async <T = unknown>(route: string, options?: ApiRequestOptions): Promise<T> => {
    const body =
      typeof options?.body === 'string'
        ? (JSON.parse(options.body) as Record<string, unknown>)
        : undefined;
    calls.push({ route, body, signal: options?.signal ?? undefined });
    const special = override?.(route, options, body);
    if (special !== undefined) return (await Promise.resolve(special)) as T;
    const u = new URL(route, 'http://local');
    const id = u.searchParams.get('id') ?? '';
    switch (u.pathname) {
      case '/api/printing/start': {
        const jobId = `print-${++sequence}`;
        bindings.set(jobId, {
          workbookKey: typeof body?.workbookKey === 'string' ? body.workbookKey : '',
          book: typeof body?.book === 'string' ? body.book : '',
          sheet: typeof body?.sheet === 'string' ? body.sheet : '',
        });
        const res: unknown = { jobId };
        return res as T;
      }
      case '/api/printing/status': {
        const res: unknown = { ...envelope(id), status: 'done' };
        return res as T;
      }
      case '/api/printing/summary':
        return summary(id) as T;
      case '/api/printing/page':
        return page(id, Number(u.searchParams.get('page'))) as T;
      default: {
        const res: unknown = { changed: true };
        return res as T;
      }
    }
  };

  const printing = new Printing({
    state,
    api,
    toast: (msg, tone) => toasts.push([msg, tone]),
    source: () => source,
    limits: testPrintLimits,
    richLimits: { cellRuns: 20, viewRuns: 50 },
    fontStack: (x) => x,
    sleep: async () => undefined,
  });

  return {
    printing,
    state,
    source,
    calls,
    toasts,
    summary,
    page,
    restore: () => {
      printing.resetBook();
      dom.restore();
    },
  };
}

test('printing starts only on explicit open and keeps source text out of markup', async () => {
  const h = setupPrinting();
  try {
    assert.equal(h.calls.length, 0);
    await h.printing.open();
    assert.ok(h.printing.job);
    const wrap = document.getElementById('printPaperWrap');
    assert.ok(wrap);
    assert.equal(wrap.children.length, 1);
    assert.ok(wrap.textContent.includes('9,007,199,254,740,993.31'));
    assert.equal(document.getElementById('printPreviewDialog')?.querySelector('img'), null);
    assert.match(
      document.getElementById('printRawSettings')?.textContent ?? '',
      /<img src=x onerror=alert/,
    );
    assert.match(document.getElementById('printWarnings')?.textContent ?? '', /页眉或页脚/);
    assert.equal(
      h.calls.some((call) => /\/api\/(?:index|analysis|range|layout)/.test(call.route)),
      false,
    );
    const id = h.printing.job;
    h.printing.close();
    await tick();
    assert.equal(wrap.children.length, 0);
    assert.ok(h.calls.some((c) => c.route === '/api/printing/clear' && c.body?.id === id));
  } finally {
    h.restore();
  }
});

test('cancel and immediate retry reclaim a late start without replacing the accepted page', async () => {
  let resolveReq: ((value: unknown) => void) | undefined;
  let starts = 0;
  const h = setupPrinting((route) =>
    route === '/api/printing/start' && ++starts === 1
      ? new Promise((r) => {
          resolveReq = r;
        })
      : undefined,
  );
  try {
    const old = h.printing.open();
    await tick();
    h.printing.cancel();
    await h.printing.refresh();
    const accepted = h.printing.job;
    resolveReq?.({ jobId: 'late-print' });
    await old;
    assert.equal(h.printing.job, accepted);
    assert.ok(
      h.calls.some((c) => c.route === '/api/printing/cancel' && c.body?.id === 'late-print'),
    );
    assert.equal(h.printing.busy, false);
  } finally {
    h.restore();
  }
});

test('failed candidate first-page loading retains the complete accepted preview and releases the candidate', async () => {
  let fail = false;
  const h = setupPrinting((route) =>
    fail && route.startsWith('/api/printing/page?')
      ? Promise.reject(new Error('模拟读页失败'))
      : undefined,
  );
  try {
    await h.printing.open();
    const id = h.printing.job;
    const node = document.getElementById('printPaperWrap')?.children[0];
    fail = true;
    h.printing.changed();
    await h.printing.refresh();
    assert.equal(h.printing.job, id);
    assert.equal(document.getElementById('printPaperWrap')?.children[0], node);
    assert.match(
      document.getElementById('printPreviewStatus')?.textContent ?? '',
      /保留上一次预览.*读页失败/,
    );
    assert.equal(h.printing.dirty, true);
    assert.ok(h.calls.some((c) => c.route === '/api/printing/clear' && c.body?.id !== id));
  } finally {
    h.restore();
  }
});

test('latest page intent wins even when the transport ignores abort; screen zoom performs no new read', async () => {
  let hold = false;
  let resolveReq: ((value: unknown) => void) | undefined;
  let oldValue: PrintPageResponse | undefined;
  const h = setupPrinting((route) =>
    hold && route.includes('/page?') && route.includes('page=1')
      ? new Promise((r) => {
          resolveReq = r;
          oldValue = h.page(h.printing.job, 1);
        })
      : undefined,
  );
  try {
    await h.printing.open();
    hold = true;
    const old = h.printing.go(1);
    await tick();
    await h.printing.go(2);
    resolveReq?.(oldValue);
    await old;
    assert.equal(h.printing.index, 2);
    assert.equal(h.printing.page?.content.cells[0]?.ref, 'A3');
    const count = h.calls.length;
    (document.getElementById('printScreenZoom') as HTMLSelectElement).value = '200';
    h.printing.zoom();
    assert.equal(h.calls.length, count);
    assert.equal(h.printing.rendered?.node.style.transform, 'scale(2)');
  } finally {
    h.restore();
  }
});

test('same-content path changes clear source-bound pages before delayed results arrive', async () => {
  let hold = false;
  let resolveReq: ((value: unknown) => void) | undefined;
  let oldValue: PrintPageResponse | undefined;
  const h = setupPrinting((route) =>
    hold && route.includes('/page?')
      ? new Promise((r) => {
          resolveReq = r;
          oldValue = h.page(h.printing.job, 1);
        })
      : undefined,
  );
  try {
    await h.printing.open();
    hold = true;
    const pending = h.printing.go(1);
    await tick();
    h.state.book = 'same-content-other-path';
    h.printing.resetBook();
    resolveReq?.(oldValue);
    await pending;
    assert.equal(h.printing.page, null);
    assert.equal(h.printing.job, '');
    assert.equal(h.printing.dialog.open, false);
    assert.equal(document.getElementById('printPaperWrap')?.children.length, 0);
    assert.equal((document.getElementById('printMarginLeft') as HTMLInputElement).value, '20');
  } finally {
    h.restore();
  }
});

test('closing after task completion but before summary adoption releases the completed task', async () => {
  let resolveReq: ((value: unknown) => void) | undefined;
  const h = setupPrinting((route) =>
    route.includes('/summary?')
      ? new Promise((r) => {
          resolveReq = r;
        })
      : undefined,
  );
  try {
    const pending = h.printing.open();
    await tick();
    const summaryCall = h.calls.find((c) => c.route.includes('/summary?'));
    assert.ok(summaryCall);
    const job = new URL(summaryCall.route, 'http://local').searchParams.get('id') ?? '';
    h.printing.close();
    resolveReq?.(h.summary(job));
    await pending;
    assert.equal(h.printing.job, '');
    assert.ok(h.calls.some((c) => c.route === '/api/printing/clear' && c.body?.id === job));
    assert.equal(document.getElementById('printPaperWrap')?.children.length, 0);
  } finally {
    h.restore();
  }
});

test('foreign summaries and invalid input never replace a valid page or schedule implicit source changes', async () => {
  let foreign = false;
  const h = setupPrinting((route) =>
    foreign && route.includes('/summary?')
      ? { ...h.summary('print-2'), book: 'foreign-path' }
      : undefined,
  );
  try {
    await h.printing.open();
    const node = h.printing.rendered?.node;
    foreign = true;
    await h.printing.refresh();
    assert.equal(h.printing.rendered?.node, node);
    assert.match(document.getElementById('printPreviewStatus')?.textContent ?? '', /来源或任务/);
    const before = h.calls.length;
    (document.getElementById('printArea') as HTMLInputElement).value = 'IW65537';
    await h.printing.refresh();
    assert.equal(h.calls.length, before);
    assert.match(document.getElementById('printPreviewStatus')?.textContent ?? '', /超出/);
  } finally {
    h.restore();
  }
});

test('unsupported/unprepared sources stay disabled and empty previews fetch no fake page', async () => {
  const h = setupPrinting();
  try {
    h.source.printMetadata = false;
    h.printing.controls();
    await h.printing.open();
    assert.equal(h.calls.length, 0);
    assert.equal((document.getElementById('printPreviewOpenBtn') as HTMLButtonElement).disabled, true);
  } finally {
    h.restore();
  }

  const e = setupPrinting((route) =>
    route.includes('/summary?')
      ? {
          ...e.summary('print-1'),
          summary: {
            ...e.summary('print-1').summary,
            pages: 0,
            areas: null,
            paper: { width: '0', height: '0' },
            scale: { numerator: '', denominator: '', percent: '' },
          },
        }
      : undefined,
  );
  try {
    await e.printing.open();
    assert.equal(document.getElementById('printPaperWrap')?.children.length, 0);
    assert.equal(
      e.calls.some((c) => c.route.includes('/page?')),
      false,
    );
    assert.match(document.getElementById('printPreviewEmpty')?.textContent ?? '', /没有可见内容/);
  } finally {
    e.restore();
  }
});
