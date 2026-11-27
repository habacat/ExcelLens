import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { ApiRequestOptions, HttpError } from '../contracts/common.ts';
import { createInitialWorkbookState } from '../grid/state.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import { ExportCenter } from './export.ts';

function setupExport() {
  const dom = installDom({ fromIndexHtml: true });
  const timers = new Map<number, () => void>();
  let timerSeq = 0;

  const state = createInitialWorkbookState(1_048_576, 16_384);
  state.opened = true;
  state.workbookKey = 'one';
  state.sheet = 'Data';
  state.sheets = [{ name: 'Data' }];
  state.filtered = true;
  state.filterId = 'f';
  state.multiRanges = [];
  state.range = { row1: 2, row2: 8, col1: 2, col2: 1, visual1: 2, visual2: 1 };

  const calls: { readonly path: string; readonly payload: Record<string, unknown> }[] = [];
  const notices: string[] = [];
  let apiImpl: (path: string, options?: ApiRequestOptions) => Promise<unknown> = async (
    path,
    options,
  ) => {
    const payload =
      typeof options?.body === 'string'
        ? (JSON.parse(options.body) as Record<string, unknown>)
        : {};
    calls.push({ path, payload });
    return { jobId: 'job' };
  };

  const center = new ExportCenter({
    state,
    openDialog: () => undefined,
    api: async <T = unknown>(path: string, options?: ApiRequestOptions): Promise<T> =>
      (await apiImpl(path, options)) as T,
    toast: (x) => notices.push(x),
    colName: (c) => String.fromCharCode(64 + c),
    excludeThroughRow: () => 1,
    timers: {
      setTimeout: (fn) => {
        const id = ++timerSeq;
        timers.set(id, fn);
        return id;
      },
      clearTimeout: (id) => {
        timers.delete(Number(id));
      },
    },
  });
  const realPoll = center.poll.bind(center);
  const realRefresh = center.refresh.bind(center);
  center.poll = async () => undefined;
  center.refresh = () => undefined;
  (document.getElementById('exportScope') as HTMLSelectElement).value = 'selection';
  (document.getElementById('exportFormat') as HTMLSelectElement).value = 'csv';
  (document.getElementById('exportMode') as HTMLSelectElement).value = 'values';

  return {
    state,
    center,
    calls,
    notices,
    poll: realPoll,
    refresh: realRefresh,
    timers,
    setApi: (fn: typeof apiImpl) => {
      apiImpl = fn;
    },
    restore: () => {
      center.resetBook();
      dom.restore();
    },
  };
}

test('visual selection export sends paired endpoints and freeze coordinates instead of a source-row rectangle', async () => {
  const h = setupExport();
  try {
    await h.center.start();
    const p = h.calls[0]?.payload;
    assert.ok(p);
    assert.equal(p.start, 'A2');
    assert.equal(p.end, 'B8');
    assert.equal(p.visualStart, 2);
    assert.equal(p.visualEnd, 1);
    assert.equal(p.excludeThroughRow, 1);
    assert.equal(p.filterId, 'f');
  } finally {
    h.restore();
  }
});

test('generated issue exports share admission and retry their own snapshot instead of the current selection', async () => {
  const h = setupExport();
  try {
    await h.center.startGenerated(
      '/api/analysis/issues/export',
      { jobId: 'analysis-one', protectCSV: false },
      '异常清单',
    );
    assert.ok(h.calls[0]);
    assert.equal(h.calls[0].path, '/api/analysis/issues/export');
    assert.equal(h.calls[0].payload.jobId, 'analysis-one');
    assert.equal(h.calls[0].payload.protectCSV, false);
    await h.center.startGenerated('/api/analysis/issues/export', { jobId: 'analysis-two' }, 'other');
    assert.equal(h.calls.length, 1);
    assert.match(h.notices[0] ?? '', /已有导出/);
    h.center.running = false;
    h.center.job = '';
    h.state.sheet = 'different';
    await h.center.start();
    assert.equal(h.calls[1]?.payload.jobId, 'analysis-one');
    h.center.resetBook();
    assert.equal(h.center.generated, null);
  } finally {
    h.restore();
  }
});

test('generated ZIP keeps its true format during shared progress and retries the same complete export', async () => {
  const h = setupExport();
  try {
    h.center.refresh = h.refresh;
    await h.center.startGenerated(
      '/api/relation/export',
      { jobId: 'accepted', format: 'zipcsv', mode: 'left', maximum: '1000000' },
      '关联结果',
    );
    const fmt = document.getElementById('exportFormat') as HTMLSelectElement;
    assert.equal(fmt.value, 'zipcsv');
    assert.equal(fmt.disabled, true);
    assert.equal(h.calls[0]?.payload.format, 'zipcsv');
    assert.equal(document.getElementById('exportAdvisory')?.textContent, '');
    h.center.running = false;
    h.center.job = '';
    h.state.sheet = 'different';
    await h.center.start();
    assert.ok(h.calls[1]);
    assert.equal(h.calls[1].path, '/api/relation/export');
    assert.equal(h.calls[1].payload.format, 'zipcsv');
    assert.equal(h.calls[1].payload.jobId, 'accepted');
  } finally {
    h.restore();
  }
});

test('discontinuous selection export explains the unsupported scope instead of dropping areas', async () => {
  const h = setupExport();
  try {
    h.state.multiRanges.push({ row1: 1, row2: 1, col1: 1, col2: 1, visual1: 1, visual2: 1 });
    await h.center.start();
    assert.equal(h.calls.length, 0);
    assert.equal(h.notices.length, 1);
  } finally {
    h.restore();
  }
});

test('recoverable exports opt in explicitly and restored output reuses the export center', async () => {
  const h = setupExport();
  try {
    h.state.book = 'path-book';
    (document.getElementById('exportRecoverable') as HTMLInputElement).checked = true;
    await h.center.start();
    assert.ok(h.calls[0]);
    assert.equal(h.calls[0].payload.recoverable, true);
    assert.equal(h.calls[0].payload.book, 'path-book');
    h.center.resetBook();
    assert.equal(
      h.center.adoptRecovered(
        { jobId: 'restored' },
        { book: 'other', workbookKey: 'one', file: 'x' },
      ),
      false,
    );
    assert.equal(
      h.center.adoptRecovered(
        { jobId: 'restored' },
        {
          book: 'path-book',
          workbookKey: 'one',
          file: '原件.csv',
          sheet: 'Data',
          mode: 'csv',
          scope: 'sheet',
        },
      ),
      true,
    );
    assert.equal(h.center.job, 'restored');
    assert.equal(h.center.running, true);
    assert.equal((document.getElementById('exportRecoverable') as HTMLInputElement).checked, true);
  } finally {
    h.restore();
  }
});

test('a late export start and status cannot install an earlier workbook download', async () => {
  for (const operation of ['start', 'status'] as const) {
    const h = setupExport();
    try {
      let release: ((value: unknown) => void) | undefined;
      const cancelled: string[] = [];
      h.setApi((path, options) => {
        if (path === '/api/export/cancel') {
          const body =
            typeof options?.body === 'string' ? (JSON.parse(options.body) as { id?: string }) : {};
          if (body.id) cancelled.push(body.id);
          return Promise.resolve({});
        }
        return new Promise((resolve) => {
          release = resolve;
        });
      });
      h.center.job = 'old';
      h.center.running = operation === 'status';
      const pending = operation === 'start' ? h.center.start() : h.poll();
      h.state.workbookKey = 'two';
      h.center.resetBook();
      release?.(
        operation === 'start'
          ? { jobId: 'old' }
          : { status: 'done', downloadUrl: '/old', fileName: 'old.xlsx' },
      );
      await pending;
      assert.equal(h.center.job, '');
      assert.equal(h.center.running, false);
      assert.equal(document.getElementById('exportResult')?.hidden, true);
      assert.equal((document.getElementById('exportDownload') as HTMLAnchorElement).getAttribute('href'), null);
      assert.equal(h.timers.size, 0);
      assert.deepEqual(cancelled, ['old']);
    } finally {
      h.restore();
    }
  }
});

test('temporary status failures retain cancellation, prevent duplicate starts and can recover', async () => {
  const h = setupExport();
  try {
    h.center.running = true;
    h.center.job = 'owned';
    h.setApi(async () => {
      throw new Error('connection interrupted');
    });
    for (let i = 0; i < 4; i++) await h.poll();
    assert.equal(h.center.running, true);
    assert.equal(document.getElementById('exportRetryBtn')?.hidden, false);
    assert.equal(h.timers.size, 0);
    await h.center.start();
    assert.equal(h.center.job, 'owned');
    h.setApi(async () => ({
      status: 'done',
      downloadUrl: '/owned',
      fileName: 'correct.csv',
      size: 12,
      doneRows: 1,
      totalRows: 1,
      progress: 100,
      elapsedMs: 10,
      stage: '完成',
      error: '',
      id: 'owned',
    }));
    await h.poll();
    assert.equal(h.center.running, false);
    assert.equal(document.getElementById('exportResult')?.hidden, false);
    assert.equal((document.getElementById('exportDownload') as HTMLAnchorElement).href, '/owned');
    assert.equal(document.getElementById('exportRetryBtn')?.hidden, true);
  } finally {
    h.restore();
  }
});

test('a server-confirmed lost task permits a new export and active settings describe its snapshot', async () => {
  const h = setupExport();
  try {
    h.center.running = true;
    h.center.job = 'lost';
    h.center.snapshot = 'Data · B2:B8';
    h.refresh();
    assert.equal(document.getElementById('exportRangeLabel')?.textContent, 'Data · B2:B8');
    assert.equal((document.getElementById('exportFormat') as HTMLSelectElement).disabled, true);
    assert.equal((document.getElementById('exportStartBtn') as HTMLButtonElement).disabled, true);
    h.setApi(async () => {
      const error: HttpError = new Error('missing');
      error.status = 404;
      throw error;
    });
    await h.poll();
    assert.equal(h.center.running, false);
    assert.equal(h.timers.size, 0);
  } finally {
    h.restore();
  }
});

test('a completed download keeps its original source after filters clear and the current sheet changes', async () => {
  const h = setupExport();
  try {
    h.state.fileName = '原始账目.csv';
    h.state.filterTotal = 262145;
    h.state.filterTotalsBySheet = new Map([['Data', 262146]]);
    const scope = document.getElementById('exportScope') as HTMLSelectElement;
    scope.value = 'filtered';
    h.refresh();
    assert.equal(document.getElementById('exportRangeLabel')?.textContent, '已筛选：262,146 行');
    await h.center.start();
    h.state.filtered = false;
    h.state.filterId = '';
    h.state.sheet = 'Other';
    scope.value = 'sheet';
    h.setApi(async () => ({
      id: 'job',
      status: 'done',
      downloadUrl: '/snapshot',
      fileName: 'sorted.csv',
      size: 100,
      doneRows: 262146,
      totalRows: 262146,
      progress: 100,
      elapsedMs: 100,
      stage: '完成',
      error: '',
    }));
    await h.poll();
    h.refresh();
    assert.equal(
      document.getElementById('exportSnapshot')?.textContent,
      '来源：原始账目.csv · Data · 已筛选：262,146 行',
    );
    assert.equal((document.getElementById('exportDownload') as HTMLAnchorElement).href, '/snapshot');
    assert.equal(
      (document.getElementById('exportRangeLabel')?.textContent ?? '').includes('已筛选'),
      false,
    );
    h.center.resetBook();
    assert.equal(document.getElementById('exportSnapshot')?.textContent, '');
    assert.equal(h.center.resultSource, '');
  } finally {
    h.restore();
  }
});
