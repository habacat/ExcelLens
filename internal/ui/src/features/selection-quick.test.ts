import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { ApiRequestOptions } from '../contracts/common.ts';
import type { SelectionQuickLimits } from '../contracts/formats.ts';
import { formatAnalysisNumber } from '../analysis/numbers.ts';
import { createInitialWorkbookState } from '../grid/state.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import { makeRowViewState } from '../test-helpers/grid-fixture.ts';
import {
  validateSelectionQuick,
  type SelectionQuickChartData,
  type SelectionQuickRequest,
} from './selection-quick-model.ts';
import { SelectionQuick } from './selection-quick.ts';

const tick = (): Promise<void> =>
  new Promise((resolve) => {
    setImmediate(resolve);
  });

const colName = (col: number): string => String.fromCharCode(64 + col);
const limits: SelectionQuickLimits = {
  cells: 400_000,
  columns: 16,
  segments: 60,
  requestBytes: 8192,
  bytes: 4 << 20,
};

interface MutableSummary {
  count: number;
  sum: string | number;
  average: string;
  minimum: string;
  maximum: string;
  minimumRow: number;
  maximumRow: number;
}

interface MutableSegment {
  first: number;
  last: number;
  firstRow: number;
  lastRow: number;
  summary: MutableSummary;
}

interface MutableColumn {
  column: number;
  summary: MutableSummary;
  segments: MutableSegment[];
  counts: {
    number: number;
    negative: number;
    zero: number;
    positive: number;
    blank: number;
    other: number;
    errors: number;
    missing: number;
    invalid: number;
  };
}

interface MutableQuickPage {
  workbookKey: string;
  book: string;
  sheet: string;
  viewId: string;
  filterId: string;
  excludeThroughRow: number;
  start: string;
  end: string;
  visualStart?: number;
  visualEnd?: number;
  skipFirstRow: boolean;
  minuend: number;
  subtrahend: number;
  selectedRows: number;
  rows: number;
  firstColumn: number;
  lastColumn: number;
  columns: MutableColumn[];
  difference?: {
    summary: MutableSummary;
    segments: MutableSegment[];
    paired: number;
    unpaired: number;
  };
}

function computeSummary(values: readonly { readonly value: string | null; readonly row: number }[]): MutableSummary {
  const items = values
    .filter((x): x is { readonly value: string; readonly row: number } => x.value !== null)
    .map((x) => ({ ...x, n: BigInt(x.value.replace('.', '')) }));
  // Fixture literals have two decimal places.
  const sum = items.reduce((n, x) => n + x.n, 0n);
  const first = items[0];
  const min = first ? items.reduce((a, b) => (a.n <= b.n ? a : b), first) : undefined;
  const max = first ? items.reduce((a, b) => (a.n >= b.n ? a : b), first) : undefined;
  return {
    count: items.length,
    sum: items.length ? `${sum}/100` : '0',
    average: items.length ? `${sum}/${100n * BigInt(items.length)}` : '',
    minimum: min?.value ?? '',
    maximum: max?.value ?? '',
    minimumRow: min?.row ?? 0,
    maximumRow: max?.row ?? 0,
  };
}

function makeQuickResponse(req: SelectionQuickRequest): MutableQuickPage {
  const source: readonly (readonly (string | null)[])[] = [
    [null, '9007199254740993.31', '-0.01', '0.00'],
    [null, '9007199254740993.30', '0.01', null],
  ];
  const start = req.skipFirstRow ? 1 : 0;
  const series = (raw: readonly (string | null)[]) => {
    const values = raw.map((value, i) => ({ value, row: i + 1 })).slice(start);
    return {
      summary: computeSummary(values),
      segments: values.map((v, i) => ({
        first: i + 1,
        last: i + 1,
        firstRow: v.row,
        lastRow: v.row,
        summary: computeSummary([v]),
      })),
    };
  };
  const columns: MutableColumn[] = source.map((raw, i) => {
    const s = series(raw);
    const numbers = raw.slice(start).filter((v): v is string => v !== null);
    const counts = {
      number: numbers.length,
      negative: numbers.filter((v) => v.startsWith('-')).length,
      zero: numbers.filter((v) => v === '0.00').length,
      positive: numbers.filter((v) => v !== '0.00' && !v.startsWith('-')).length,
      blank: i === 0 ? 0 : 1,
      other: start ? 0 : 1,
      errors: 0,
      missing: 0,
      invalid: 0,
    };
    return { column: i + 1, ...s, counts };
  });
  const page: MutableQuickPage = {
    ...req,
    selectedRows: 4,
    rows: 4 - start,
    firstColumn: 1,
    lastColumn: 2,
    columns,
  };
  if (req.minuend) {
    const raw =
      req.minuend === 2
        ? [null, '-0.01', '0.02', null]
        : [null, '0.01', '-0.02', null];
    page.difference = { ...series(raw), paired: 2, unpaired: 2 - start };
  }
  return page;
}

function setupSelectionQuick(
  override?: (req: SelectionQuickRequest, init: ApiRequestOptions | undefined) => unknown,
) {
  const dom = installDom({ fromIndexHtml: true });
  const calls: {
    readonly url: string;
    readonly init: ApiRequestOptions | undefined;
    readonly req: SelectionQuickRequest;
  }[] = [];
  const charts: {
    readonly data: SelectionQuickChartData;
    readonly mode: string;
    readonly onPick: (keys: readonly string[]) => void;
  }[] = [];
  const toasts: [string, string | undefined][] = [];
  const navigations: { readonly sheet: string; readonly ref: string }[] = [];

  const state = createInitialWorkbookState(1_048_576, 16_384);
  state.opened = true;
  state.indexReady = true;
  state.index = {
    id: 'idx',
    sheet: '<img src=x>',
    status: 'done',
    doneBytes: 0,
    totalBytes: 0,
    maxRow: 4,
    maxCol: 2,
    totalRows: 4,
  };
  state.workbookKey = 'content';
  state.book = 'path';
  state.sheet = '<img src=x>';
  state.fileName = '<script>bad</script>';
  state.sheetEpoch = 1;
  state.rowView = makeRowViewState({ id: 'view', sheet: '<img src=x>' });
  state.filtered = false;
  state.range = { row1: 1, row2: 4, col1: 1, col2: 2, visual1: 0, visual2: 0 };

  let pinned = 0;
  const quick = new SelectionQuick({
    state,
    colName,
    limits,
    toast: (msg, tone) => toasts.push([msg, tone]),
    pinnedThroughRow: () => pinned,
    formatNumber: (value) => formatAnalysisNumber(value),
    drawChart: (_canvas, data, mode, onPick) => {
      charts.push({ data, mode, onPick });
    },
    api: async <T = unknown>(url: string, init?: ApiRequestOptions): Promise<T> => {
      const req = JSON.parse(
        typeof init?.body === 'string' ? init.body : '{}',
      ) as SelectionQuickRequest;
      calls.push({ url, init, req });
      const result = override?.(req, init);
      return (result === undefined ? makeQuickResponse(req) : await Promise.resolve(result)) as T;
    },
    navigate: async (sheet, ref, guard) => {
      if (guard?.()) {
        navigations.push({ sheet, ref });
        state.range = null;
        state.selected = {
          sheet,
          row: 2,
          col: 1,
          ref,
          raw: '',
          display: '',
          hasFormula: false,
        };
        assert.ok(guard(), 'navigation must survive its own selection update');
      }
    },
  });

  return {
    quick,
    state,
    calls,
    charts,
    toasts,
    navigations,
    setPinned: (n: number) => {
      pinned = n;
    },
    restore: () => {
      quick.close();
      dom.restore();
    },
  };
}

test('the source line names hidden columns inside the selection and says nothing when there are none', () => {
  const plain = setupSelectionQuick();
  try {
    plain.quick.open();
    assert.doesNotMatch(
      document.getElementById('selectionQuickSource')?.textContent ?? '',
      /隐藏列/,
    );
  } finally {
    plain.restore();
  }

  const hidden = setupSelectionQuick();
  try {
    hidden.state.hiddenColumns = new Set([2, 5]);
    hidden.quick.open();
    assert.match(
      document.getElementById('selectionQuickSource')?.textContent ?? '',
      / · 4 行 · 含隐藏列 B$/,
    );
  } finally {
    hidden.restore();
  }
});

test('quick selection only reads on explicit run, preserves exact values, and renders hostile source labels as text', async () => {
  const h = setupSelectionQuick();
  try {
    h.quick.open();
    assert.equal(h.calls.length, 0);
    assert.match(
      document.getElementById('selectionQuickSource')?.textContent ?? '',
      /<img src=x>/,
    );
    await h.quick.run();
    assert.equal(h.calls.length, 1);
    assert.equal(
      h.quick.page?.columns[0]?.segments[1]?.summary.maximum,
      '9007199254740993.31',
    );
    assert.equal(h.quick.dialog.querySelector('img'), null);
    assert.equal(h.quick.dialog.querySelector('script'), null);
    assert.match(document.getElementById('selectionQuickBody')?.textContent ?? '', /公式无结果/);
    assert.equal(h.charts.at(-1)?.data.rows[0]?.values[0].exact, '');

    (document.getElementById('selectionQuickMinuend') as HTMLSelectElement).value = '2';
    (document.getElementById('selectionQuickSubtrahend') as HTMLSelectElement).value = '1';
    (document.getElementById('selectionQuickSkip') as HTMLInputElement).checked = true;
    await h.quick.run();
    assert.equal(h.quick.page.rows, 3);
    assert.equal(h.quick.page.difference?.paired, 2);
    assert.equal(h.quick.page.minuend, 2);
    assert.equal(h.quick.page.subtrahend, 1);
    assert.match(document.getElementById('selectionQuickPairing')?.textContent ?? '', /未配对 1 行/);
  } finally {
    h.restore();
  }
});

test('cancel and immediate retry ignore a late response even when transport ignores abort', async () => {
  let hold = false;
  let release: (() => void) | undefined;
  const h = setupSelectionQuick((req) =>
    hold
      ? new Promise<MutableQuickPage>((r) => {
          release = () => r(makeQuickResponse(req));
        })
      : undefined,
  );
  try {
    h.quick.open();
    await h.quick.run();
    hold = true;
    const pending = h.quick.run();
    await tick();
    h.quick.cancel();
    hold = false;
    await h.quick.run();
    const accepted = h.quick.page;
    release?.();
    await pending;
    assert.equal(h.quick.page, accepted);
    assert.equal(h.calls[1]?.init?.signal?.aborted, true);
    assert.equal(h.quick.busy, false);
  } finally {
    h.restore();
  }
});

test('failed, forged and incomplete candidates preserve the previous complete result', async () => {
  let mode: '' | 'fail' | 'foreign' | 'partial' = '';
  const h = setupSelectionQuick((req) => {
    if (mode === 'fail') throw new Error('读取失败');
    if (mode) {
      const p = makeQuickResponse(req);
      if (mode === 'foreign') p.book = 'foreign';
      else p.columns[0]?.segments.pop();
      return p;
    }
    return undefined;
  });
  try {
    h.quick.open();
    await h.quick.run();
    const accepted = h.quick.page;
    for (const value of ['fail', 'foreign', 'partial'] as const) {
      mode = value;
      await h.quick.run();
      assert.equal(h.quick.page, accepted);
      assert.match(
        document.getElementById('selectionQuickStatus')?.textContent ?? '',
        /保留上次结果/,
      );
      assert.equal(h.quick.busy, false);
    }
  } finally {
    h.restore();
  }
});

test('source, index, visibility, filter, freeze and selection changes cancel and clear accepted results', async () => {
  const changes: readonly ((h: ReturnType<typeof setupSelectionQuick>) => void)[] = [
    (h) => {
      h.state.book = 'other-path';
    },
    (h) => {
      h.state.sheetEpoch++;
    },
    (h) => {
      if (h.state.index) h.state.index = { ...h.state.index };
    },
    (h) => {
      h.state.rowView = makeRowViewState({ id: 'new', sheet: '<img src=x>' });
    },
    (h) => {
      h.state.filtered = true;
    },
    (h) => {
      h.setPinned(1);
    },
    (h) => {
      if (h.state.range) h.state.range = { ...h.state.range, row2: 3 };
    },
    (h) => {
      h.state.opened = false;
    },
  ];
  for (const change of changes) {
    const h = setupSelectionQuick();
    try {
      h.quick.open();
      await h.quick.run();
      change(h);
      h.quick.changed();
      assert.equal(h.quick.page, null);
      assert.equal(h.quick.dialog.open, false);
    } finally {
      h.restore();
    }
  }
});

test('held responses after close or a changed selection cannot publish and prevent no later retry', async () => {
  let hold = true;
  let release: (() => void) | undefined;
  const h = setupSelectionQuick((req) =>
    hold
      ? new Promise<MutableQuickPage>((r) => {
          release = () => r(makeQuickResponse(req));
        })
      : undefined,
  );
  try {
    h.quick.open();
    const pending = h.quick.run();
    await tick();
    if (h.state.range) h.state.range = { ...h.state.range, row2: 3 };
    release?.();
    await pending;
    assert.equal(h.quick.page, null);
    h.quick.close();
    await tick();
    if (h.state.range) h.state.range = { ...h.state.range, row2: 4 };
    hold = false;
    h.quick.open();
    await h.quick.run();
    assert.ok(h.quick.page);
  } finally {
    h.restore();
  }
});

test('number and theme repaint and series switching never read the source again', async () => {
  const h = setupSelectionQuick();
  try {
    h.quick.open();
    await h.quick.run();
    const accepted = JSON.stringify(h.quick.page);
    (document.getElementById('selectionQuickSeries') as HTMLSelectElement).value = '2';
    h.quick.renderSeries();
    document.dispatchEvent(new Event('excellens:readingchange'));
    document.dispatchEvent(new Event('excellens:analysisnumberchange'));
    window.dispatchEvent(new Event('resize'));
    const lastChart = h.charts.at(-1);
    assert.ok(lastChart);
    assert.equal(lastChart.data.rows[1]?.keys[0], '2');
    lastChart.onPick(['2']);
    assert.match(
      document.getElementById('selectionQuickStatus')?.textContent ?? '',
      /第 2 段.*选中行 2–2.*源行 2 → 2/,
    );
    assert.equal(h.calls.length, 1);
    assert.equal(JSON.stringify(h.quick.page), accepted);
    assert.ok(h.charts.length >= 4);
  } finally {
    h.restore();
  }
});

test('source position navigation is bound to its accepted source and survives its own selection update', async () => {
  const h = setupSelectionQuick();
  try {
    h.quick.open();
    await h.quick.run();
    await h.quick.locate('A2');
    assert.equal(h.navigations[0]?.ref, 'A2');
    assert.equal(h.quick.dialog.open, false);
  } finally {
    h.restore();
  }
});

test('multi-area and oversized selections reject before requesting data and equal columns reject before comparison', async () => {
  const rejections: readonly ((h: ReturnType<typeof setupSelectionQuick>) => void)[] = [
    (h) => {
      if (h.state.range) h.state.multiRanges = [{ ...h.state.range }];
    },
    (h) => {
      if (h.state.range) h.state.range = { ...h.state.range, col2: 17 };
    },
    (h) => {
      if (h.state.index) h.state.index = { ...h.state.index, maxRow: 400_001 };
      if (h.state.range) h.state.range = { ...h.state.range, row2: 400_001 };
    },
  ];
  for (const change of rejections) {
    const h = setupSelectionQuick();
    try {
      change(h);
      h.quick.open();
      assert.equal(h.quick.dialog.open, false);
      assert.equal(h.calls.length, 0);
      assert.equal(h.toasts.length, 1);
    } finally {
      h.restore();
    }
  }

  const h = setupSelectionQuick();
  try {
    h.quick.open();
    (document.getElementById('selectionQuickMinuend') as HTMLSelectElement).value = '1';
    (document.getElementById('selectionQuickSubtrahend') as HTMLSelectElement).value = '1';
    await h.quick.run();
    assert.equal(h.calls.length, 0);
    assert.match(document.getElementById('selectionQuickStatus')?.textContent ?? '', /两个不同/);
  } finally {
    h.restore();
  }
});

test('model rejects corrupt partitions, source rows, summary types and segment coverage as whole results', () => {
  const req: SelectionQuickRequest = {
    workbookKey: 'content',
    book: 'path',
    sheet: 'sheet',
    viewId: 'view',
    filterId: '',
    excludeThroughRow: 0,
    start: 'A1',
    end: 'B4',
    skipFirstRow: false,
    minuend: 2,
    subtrahend: 1,
  };
  const validate = (p: MutableQuickPage) => validateSelectionQuick(p, req, limits, 4, 1, 2, 4);
  assert.ok(validate(makeQuickResponse(req)));

  const mutations: readonly ((p: MutableQuickPage) => void)[] = [
    (p) => {
      if (p.columns[0]) p.columns[0].counts.zero++;
    },
    (p) => {
      if (p.columns[0]) p.columns[0].summary.sum = 4;
    },
    (p) => {
      if (p.columns[0]?.segments[0]) p.columns[0].segments[0].first = 0;
    },
    (p) => {
      if (p.columns[0]?.segments[1]) p.columns[0].segments[1].summary.minimumRow = 5;
    },
    (p) => {
      if (p.columns[0]?.segments[0]) p.columns[0].segments[0].summary.average = '0';
    },
    (p) => {
      if (p.difference) p.difference.unpaired++;
    },
    (p) => {
      p.skipFirstRow = true;
    },
    (p) => {
      p.visualStart = 1;
    },
    (p) => {
      p.selectedRows = 3;
    },
    (p) => {
      p.columns.reverse();
    },
  ];
  for (const change of mutations) {
    const p = makeQuickResponse(req);
    change(p);
    assert.throws(() => validate(p));
  }
});
