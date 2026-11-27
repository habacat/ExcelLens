import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { ApiRequestOptions } from '../contracts/common.ts';
import {
  DEFAULT_ANALYSIS_NUMBER_OPTIONS,
  formatAnalysisNumber,
  getAnalysisNumberOptions,
  setAnalysisNumberOptions,
} from '../analysis/numbers.ts';
import { createInitialWorkbookState } from '../grid/state.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import { makeDefaultCapabilities, makeRowViewState } from '../test-helpers/grid-fixture.ts';
import { ProfileCenter } from './profile.ts';
import { ProfileModel, type ProfileRequest } from './profile-model.ts';

const tick = (): Promise<void> =>
  new Promise((resolve) => {
    setImmediate(resolve);
  });

interface MutableProfileResponse {
  workbookKey: string;
  book: string;
  sheet: string;
  bindingViewId: string;
  bindingFilterId: string;
  start: string;
  end: string;
  filterId: string;
  includeDistribution?: boolean;
  range: string;
  rows: number;
  firstColumn: number;
  lastColumn: number;
  total: number;
  nonEmpty: number;
  blank: number;
  number: number;
  text: number;
  formulas: number;
  cacheMissing: number;
  errors: number;
  sum: string;
  average: string;
  minimum: string;
  maximum: string;
  sumExact: string;
  averageExact: string;
  minimumExact: string;
  maximumExact: string;
  unique: number;
  uniqueTruncated: boolean;
  topValues: { value: string; count: number }[];
  errorRefs: string[];
  missingRefs: string[];
  filtered: boolean;
  warning?: string;
  distribution?: {
    method: string;
    definition: string;
    count: number;
    quantiles: { percent: number; value: string }[];
    bins: { lower: string; upper: string; upperInclusive: boolean; count: number }[];
  };
}

function setupProfile(
  override?: (
    url: string,
    init: ApiRequestOptions | undefined,
    req: ProfileRequest,
  ) => unknown,
) {
  const dom = installDom({ fromIndexHtml: true });
  const calls: {
    readonly url: string;
    readonly init: ApiRequestOptions | undefined;
    readonly req: ProfileRequest;
  }[] = [];
  const toasts: [string, string | undefined][] = [];

  const state = createInitialWorkbookState(1_048_576, 16_384);
  state.opened = true;
  state.indexReady = true;
  state.workbookKey = 'content';
  state.book = 'path';
  state.sheet = '明细';
  state.sheetEpoch = 1;
  state.index = {
    id: 'idx',
    sheet: '明细',
    status: 'done',
    doneBytes: 0,
    totalBytes: 0,
    maxRow: 4,
    maxCol: 2,
    totalRows: 4,
  };
  state.rowView = makeRowViewState({ id: 'view', sheet: '明细' });
  state.selected = {
    sheet: '明细',
    row: 2,
    col: 1,
    ref: 'A2',
    raw: '',
    display: '',
    hasFormula: false,
  };
  state.usedMaxRow = 4;
  state.usedMaxCol = 2;
  state.filtered = false;

  const options = { through: 0, canRead: true };
  const limits: { bytes: number; requestBytes: number; cells: number; bins: number } = {
    bytes: 2 << 20,
    requestBytes: 8192,
    cells: 4_000_000,
    bins: 10,
  };
  const source = makeDefaultCapabilities();

  const response = (req: ProfileRequest): MutableProfileResponse => ({
    workbookKey: req.workbookKey,
    book: req.book,
    sheet: req.sheet,
    bindingViewId: req.bindingViewId,
    bindingFilterId: req.bindingFilterId,
    start: req.start,
    end: req.end,
    filterId: req.filterId,
    includeDistribution: req.includeDistribution,
    range: `${req.start}:${req.end}`,
    rows: 4,
    firstColumn: 1,
    lastColumn: 1,
    total: 4,
    nonEmpty: 4,
    blank: 0,
    number: 4,
    text: 0,
    formulas: 0,
    cacheMissing: 0,
    errors: 0,
    sum: '10',
    average: '2.5',
    minimum: '1',
    maximum: '4',
    sumExact: '10',
    averageExact: '2.5',
    minimumExact: '1',
    maximumExact: '4',
    unique: 4,
    uniqueTruncated: false,
    topValues: [
      { value: '1', count: 1 },
      { value: '2', count: 1 },
      { value: '3', count: 1 },
      { value: '4', count: 1 },
    ],
    errorRefs: [],
    missingRefs: [],
    filtered: Boolean(req.filterId),
    ...(req.includeDistribution
      ? {
          distribution: {
            method: 'exact',
            definition: 'inclusive-linear',
            count: 4,
            quantiles: [
              { percent: 5, value: '1.15' },
              { percent: 25, value: '1.75' },
              { percent: 50, value: '2.5' },
              { percent: 75, value: '3.25' },
              { percent: 95, value: '3.85' },
            ],
            bins: Array.from({ length: 10 }, (_, i) => ({
              lower: i === 0 ? '1' : (1 + i * 0.3).toFixed(1),
              upper: i === 9 ? '4' : (1 + (i + 1) * 0.3).toFixed(1),
              upperInclusive: i === 9,
              count: [0, 3, 6, 9].includes(i) ? 1 : 0,
            })),
          },
        }
      : {}),
  });

  const api = async <T = unknown>(url: string, init?: ApiRequestOptions): Promise<T> => {
    const req = JSON.parse(typeof init?.body === 'string' ? init.body : '{}') as ProfileRequest;
    calls.push({ url, init, req });
    const special = override?.(url, init, req);
    return (special === undefined ? response(req) : await Promise.resolve(special)) as T;
  };

  const ui = new ProfileCenter({
    state,
    api,
    toast: (msg, tone) => toasts.push([msg, tone]),
    openDialog: (id) => {
      const dlg = document.getElementById(id);
      if (dlg instanceof HTMLDialogElement) dlg.showModal();
    },
    navigateToCell: async () => true,
    colName: (c) => String.fromCharCode(64 + c),
    limits: limits,
    source: () => source,
    canRead: () => options.canRead,
    excludeThroughRow: () => options.through,
    numbers: {
      get: () => getAnalysisNumberOptions(),
      format: (cell, settings) => formatAnalysisNumber(cell, settings),
    },
  });

  return {
    ui,
    state,
    options,
    limits,
    source,
    calls,
    toasts,
    response,
    restore: () => {
      setAnalysisNumberOptions(DEFAULT_ANALYSIS_NUMBER_OPTIONS);
      dom.restore();
    },
  };
}

test('profile reads only on explicit analyze, exposes optional whole-data percentiles and keeps source values as text', async () => {
  const h = setupProfile();
  try {
    h.ui.open();
    assert.equal(h.calls.length, 0);
    await h.ui.analyze();
    assert.equal(h.calls.length, 1);
    assert.equal(h.ui.page?.distribution, undefined);
    assert.equal(document.getElementById('profileCancelBtn')?.hidden, true);
    assert.equal(h.ui.page?.sum, '10');
    assert.equal((document.getElementById('profileDetail')?.textContent ?? '').includes('有理数'), false);

    const dist = document.getElementById('profileDistribution') as HTMLInputElement;
    dist.checked = true;
    dist.dispatchEvent(new Event('change'));
    assert.equal(h.ui.page, null);
    await h.ui.analyze();
    assert.ok(h.calls[1]);
    assert.equal(h.calls[1].req.includeDistribution, true);
    assert.ok((document.getElementById('profileDetail')?.textContent ?? '').includes('中位数'));
    assert.equal(document.getElementById('profileDetail')?.querySelectorAll('.profile-bin').length, 10);
    assert.equal(h.calls[1].req.bindingViewId, 'view');
  } finally {
    h.restore();
  }
});

test('display preferences redraw accepted data without a source call and expose exact financial values', async () => {
  const h = setupProfile((_url, _init, req) => ({
    ...h.response(req),
    sum: '9007199254740993.31',
    sumExact: '9007199254740993.31',
  }));
  try {
    h.ui.open();
    await h.ui.analyze();
    assert.ok((document.getElementById('profileDetail')?.textContent ?? '').includes('9.01e+15'));
    const before = h.calls.length;
    setAnalysisNumberOptions({ ...DEFAULT_ANALYSIS_NUMBER_OPTIONS, scientific: false });
    document.dispatchEvent(new Event('excellens:analysisnumberchange'));
    assert.equal(h.calls.length, before);
    assert.ok(
      (document.getElementById('profileDetail')?.textContent ?? '').includes(
        '9,007,199,254,740,993.31',
      ),
    );
    assert.equal(h.ui.page?.sum, '9007199254740993.31');
  } finally {
    h.restore();
  }
});

test('cancel plus immediate retry rejects a late result even if the transport ignores abort', async () => {
  let hold = true;
  let resolveReq: (() => void) | undefined;
  const h = setupProfile((_url, _init, req) =>
    hold
      ? new Promise<unknown>((r) => {
          resolveReq = () => r({ ...h.response(req), sum: '999' });
        })
      : undefined,
  );
  try {
    h.ui.open();
    const old = h.ui.analyze();
    await tick();
    h.ui.cancel();
    hold = false;
    await h.ui.analyze();
    const accepted = h.ui.page;
    resolveReq?.();
    await old;
    assert.equal(h.ui.page, accepted);
    assert.equal(h.calls[0]?.init?.signal?.aborted, true);
    assert.equal((document.getElementById('profileAnalyzeBtn') as HTMLButtonElement).disabled, false);
  } finally {
    h.restore();
  }
});

test('file path, index, sheet, filter, visible view and frozen changes clear accepted and pending profiles', async () => {
  const changes: readonly ((h: ReturnType<typeof setupProfile>) => void)[] = [
    (h) => {
      h.state.book = 'other-path';
    },
    (h) => {
      h.state.index = h.state.index ? { ...h.state.index } : null;
    },
    (h) => {
      h.state.sheetEpoch++;
    },
    (h) => {
      h.state.rowView = makeRowViewState({ id: 'new', sheet: '明细' });
    },
    (h) => {
      h.state.filtered = true;
      h.state.filterId = 'new';
    },
    (h) => {
      h.options.through = 1;
    },
  ];
  for (const change of changes) {
    let hold = false;
    let resolveReq: (() => void) | undefined;
    const h = setupProfile((_url, _init, req) =>
      hold
        ? new Promise<unknown>((r) => {
            resolveReq = () => r(h.response(req));
          })
        : undefined,
    );
    try {
      h.ui.open();
      await h.ui.analyze();
      hold = true;
      const old = h.ui.analyze();
      await tick();
      change(h);
      h.ui.changed();
      assert.equal((document.getElementById('profileDialog') as HTMLDialogElement).open, false);
      assert.equal(h.ui.page, null);
      resolveReq?.();
      await old;
      assert.equal(document.getElementById('profileSummary')?.children.length, 0);
    } finally {
      h.restore();
    }
  }
});

test('edits cancel unaccepted results while read failures and foreign envelopes preserve the last complete candidate', async () => {
  let mode = '';
  const h = setupProfile((_url, _init, req) => {
    if (mode === 'failure') throw new Error('模拟失败');
    if (mode === 'foreign') return { ...h.response(req), book: 'foreign' };
    return undefined;
  });
  try {
    h.ui.open();
    await h.ui.analyze();
    const accepted = h.ui.page;
    for (const value of ['failure', 'foreign']) {
      mode = value;
      await h.ui.analyze();
      assert.equal(h.ui.page, accepted);
      assert.equal(document.getElementById('profileSummary')?.hidden, false);
      assert.equal(document.getElementById('profilePending')?.getAttribute('role'), 'alert');
    }
    const startEl = document.getElementById('profileStart') as HTMLInputElement;
    startEl.value = 'B1';
    startEl.dispatchEvent(new Event('input'));
    assert.equal(h.ui.page, null);
    assert.equal(document.getElementById('profileSummary')?.hidden, true);
  } finally {
    h.restore();
  }
});

test('partial counts, wrong scope, missing bins, approximate claims and byte overruns are refused before publication', () => {
  const h = setupProfile();
  try {
    h.ui.open();
    (document.getElementById('profileDistribution') as HTMLInputElement).checked = true;
    const req = h.ui.input();
    const validate = (p: unknown) => ProfileModel.validate(p, req, h.limits, h.source);
    const mutations: readonly ((p: MutableProfileResponse) => void)[] = [
      (p) => {
        p.bindingViewId = 'foreign';
      },
      (p) => {
        p.filterId = 'foreign';
      },
      (p) => {
        p.nonEmpty = 3;
      },
      (p) => {
        p.number = 3;
      },
      (p) => {
        p.cacheMissing = 1;
      },
      (p) => {
        if (p.distribution) p.distribution.method = 'estimate';
      },
      (p) => {
        if (p.distribution) p.distribution.count = 3;
      },
      (p) => {
        p.distribution?.quantiles.pop();
      },
      (p) => {
        p.distribution?.bins.pop();
      },
      (p) => {
        if (p.distribution?.bins[0]) p.distribution.bins[0].count = 2;
      },
      (p) => {
        if (p.distribution?.bins[1]) p.distribution.bins[1].lower = '1.2';
      },
      (p) => {
        if (p.distribution?.bins[2]) p.distribution.bins[2].upperInclusive = true;
      },
    ];
    for (const change of mutations) {
      const p = h.response(req);
      change(p);
      assert.throws(() => validate(p));
    }
    h.limits.bytes = 100;
    assert.throws(() => validate(h.response(req)));
  } finally {
    h.restore();
  }
});

test('empty numeric results, partially tracked frequencies and untrusted display strings remain explicit and safe', async () => {
  const h = setupProfile((_url, _init, req) => ({
    ...h.response(req),
    number: 0,
    text: 4,
    sum: '0',
    average: '',
    minimum: '',
    maximum: '',
    sumExact: '0',
    averageExact: '',
    minimumExact: '',
    maximumExact: '',
    unique: 1,
    uniqueTruncated: true,
    topValues: [{ value: '<script>unsafe</script>', count: 4 }],
    warning: '频次达到容量',
    ...(req.includeDistribution
      ? {
          distribution: {
            method: 'exact',
            definition: 'inclusive-linear',
            count: 0,
            quantiles: [],
            bins: [],
          },
        }
      : {}),
  }));
  try {
    h.ui.open();
    (document.getElementById('profileDistribution') as HTMLInputElement).checked = true;
    await h.ui.analyze();
    assert.ok(h.ui.page);
    const detailText = document.getElementById('profileDetail')?.textContent ?? '';
    assert.ok(detailText.includes('无可用数值'));
    assert.ok(detailText.includes('常见值（部分）'));
    assert.ok(detailText.includes('<script>unsafe</script>'));
    assert.ok((document.getElementById('profileSummary')?.textContent ?? '').includes('≥ 1'));
  } finally {
    h.restore();
  }
});

test('a queued native close event cannot cancel a profile that was immediately reopened', async () => {
  const h = setupProfile();
  try {
    h.ui.open();
    await h.ui.analyze();
    h.ui.resetBook();
    h.ui.open();
    await h.ui.analyze();
    await tick();
    assert.ok(h.ui.page);
    assert.equal((document.getElementById('profileDialog') as HTMLDialogElement).open, true);
  } finally {
    h.restore();
  }
});
