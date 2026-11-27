import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { ApiRequestOptions } from '../contracts/common.ts';
import type { SourceCapabilities } from '../contracts/formats.ts';
import { columnName, parseRef as parseCellRef } from '../core/cell-address.ts';
import { createInitialWorkbookState } from '../grid/state.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import {
  makeDefaultCapabilities,
  makeRowViewState,
} from '../test-helpers/grid-fixture.ts';
import {
  CellExplanation,
  CellExplanationModel,
  type CellExplanationRequest,
} from './cell-explanation.ts';

const parseRef = (ref: string) => parseCellRef(ref, 1_048_576, 16_384);

const tick = (): Promise<void> =>
  new Promise((resolve) => {
    setImmediate(resolve);
  });

interface MutableExplanationPayload {
  workbookKey: string;
  book: string;
  sheet: string;
  viewId: string;
  filterId: string;
  ref: string;
  pinnedThroughRow: number;
  cell: {
    ref: string;
    col: number;
    raw: string;
    display: string;
    type?: string;
    style: number;
    hasFormula: boolean;
    formula?: string;
    cacheMissing?: boolean;
  };
  numeric: {
    eligible: boolean;
    reason: string;
    exact?: string | number;
  };
  formulaState: string;
  formulaAvailable: boolean;
  rowState: string;
  presentationNotice?: unknown;
  filter?: {
    headerRow: number;
    rules: { column: number; operator: string; value: string; values?: string[] }[];
    sortKeys: { column: number; descending: boolean; mode?: string }[];
  };
}

function setupCellExplanation(
  override?: (
    url: string,
    init: ApiRequestOptions | undefined,
    req: CellExplanationRequest,
  ) => unknown,
) {
  const dom = installDom({ fromIndexHtml: true });
  const calls: {
    readonly url: string;
    readonly init: ApiRequestOptions | undefined;
    readonly req: CellExplanationRequest;
  }[] = [];
  const toasts: [string, string | undefined][] = [];

  const state = createInitialWorkbookState(1_048_576, 16_384);
  state.opened = true;
  state.indexReady = true;
  state.book = 'path';
  state.workbookKey = 'content';
  state.sheet = '明细';
  state.rowView = makeRowViewState({ id: 'view', sheet: '明细' });
  state.index = {
    id: 'idx',
    sheet: '明细',
    status: 'done',
    doneBytes: 0,
    totalBytes: 0,
    maxRow: 4,
    maxCol: 3,
    totalRows: 4,
  };
  state.sheetEpoch = 1;
  state.selected = {
    sheet: '明细',
    row: 2,
    col: 1,
    ref: 'A2',
    raw: '',
    display: '',
    hasFormula: false,
  };
  state.showStyles = true;

  const limits: { bytes: number; requestBytes: number } = {
    bytes: 2 << 20,
    requestBytes: 8192,
  };
  const source: { -readonly [K in keyof SourceCapabilities]: SourceCapabilities[K] } = {
    ...makeDefaultCapabilities({ textAmounts: false }),
  };
  const options = { pinned: 1, canRead: true, notice: '' };

  const response = (req: CellExplanationRequest): MutableExplanationPayload => ({
    ...req,
    cell: {
      ref: req.ref,
      col: parseRef(req.ref)?.col ?? 1,
      raw: '9007199254740993.31',
      display: '9,007,199,254,740,993.31',
      type: 'n',
      style: 0,
      hasFormula: false,
    },
    numeric: { eligible: true, reason: 'number', exact: '9007199254740993.31' },
    formulaState: 'none',
    formulaAvailable: false,
    rowState: 'visible',
  });

  const api = async <T = unknown>(url: string, init?: ApiRequestOptions): Promise<T> => {
    const req = JSON.parse(
      typeof init?.body === 'string' ? init.body : '{}',
    ) as CellExplanationRequest;
    calls.push({ url, init, req });
    const result = override?.(url, init, req);
    return (result === undefined ? response(req) : await Promise.resolve(result)) as T;
  };

  const ui = new CellExplanation({
    state,
    api,
    toast: (msg, tone) => toasts.push([msg, tone]),
    limits: limits,
    source: () => source,
    parseRef,
    colName: columnName,
    pinnedThroughRow: () => options.pinned,
    canRead: () => options.canRead,
    notice: () => options.notice,
  });

  const rows = (): Record<string, string> => {
    const body = document.getElementById('detailBody');
    assert.ok(body);
    return Object.fromEntries(
      Array.from({ length: Math.floor(body.children.length / 2) }, (_, i) => [
        body.children[i * 2]?.textContent ?? '',
        body.children[i * 2 + 1]?.textContent ?? '',
      ]),
    );
  };

  return {
    ui,
    state,
    calls,
    toasts,
    limits,
    source,
    options,
    response,
    rows,
    restore: () => dom.restore(),
  };
}

test('single cell reads only on explicit open and presents exact source facts without nonformula cache or internal labels', async () => {
  const h = setupCellExplanation();
  try {
    h.ui.changed();
    assert.equal(h.calls.length, 0);
    await h.ui.open();
    assert.equal(h.calls.length, 1);
    assert.equal(h.calls[0]?.url, '/api/cell/explain');
    const r = h.rows();
    assert.equal(r['位置'], '明细!A2');
    assert.equal(r['原始值'], '9007199254740993.31');
    assert.equal(r['源类型'], '数值');
    assert.equal(r['数值统计'], '可用');
    assert.equal(Object.hasOwn(r, '计算结果'), false);
    assert.equal(Object.hasOwn(r, '公式'), false);
    assert.equal(Object.hasOwn(r, '样式索引'), false);
    assert.equal(Object.hasOwn(r, '共享公式'), false);
    assert.equal(document.getElementById('detailStatus')?.textContent, '');
  } finally {
    h.restore();
  }
});

test('native text, imported amounts, identifiers, zero and formula caches keep distinct explanations', async () => {
  let modify: (p: MutableExplanationPayload) => void = () => undefined;
  const h = setupCellExplanation((_url, _init, req) => {
    const p = h.response(req);
    modify(p);
    return p;
  });
  try {
    modify = (p) => {
      p.cell.raw = p.cell.display = '1,234.50';
      p.cell.type = 'inlineStr';
      p.numeric = { eligible: false, reason: 'source-text' };
    };
    await h.ui.open();
    assert.equal(h.rows()['源类型'], '文本');
    assert.match(h.rows()['数值统计'] ?? '', /源为文本/);

    h.source.textAmounts = true;
    modify = (p) => {
      p.cell.raw = p.cell.display = '1,234.50';
      p.cell.type = 'inlineStr';
      p.numeric = { eligible: true, reason: 'imported-amount', exact: '1234.5' };
    };
    await h.ui.open();
    assert.equal(h.rows()['统计数值'], '1234.5');
    assert.match(h.rows()['数值统计'] ?? '', /导入金额/);

    modify = (p) => {
      p.cell.raw = p.cell.display = '00123';
      p.cell.type = 'inlineStr';
      p.numeric = { eligible: false, reason: 'text-or-identifier' };
    };
    await h.ui.open();
    assert.equal(h.rows()['原始值'], '00123');
    assert.match(h.rows()['数值统计'] ?? '', /编号/);

    modify = (p) => {
      p.cell.raw = p.cell.display = '0';
      p.numeric.exact = '0';
    };
    await h.ui.open();
    assert.equal(h.rows()['数值统计'], '可用');
    assert.equal(h.rows()['原始值'], '0');

    modify = (p) => {
      p.cell.hasFormula = true;
      p.cell.formula = '[无法还原]';
      p.formulaState = 'cached';
    };
    await h.ui.open();
    assert.equal(h.rows()['公式'], '原文无法还原');
    assert.equal(h.rows()['计算结果'], '已保存');

    modify = (p) => {
      p.cell.hasFormula = true;
      p.cell.formula = '=A1*2';
      p.cell.cacheMissing = true;
      p.cell.raw = '';
      p.cell.display = '#N/A';
      p.numeric = { eligible: false, reason: 'missing-result' };
      p.formulaState = 'missing';
      p.formulaAvailable = true;
    };
    await h.ui.open();
    assert.equal(h.rows()['公式'], '=A1*2');
    assert.equal(h.rows()['计算结果'], '未保存');
    assert.match(h.rows()['显示值'] ?? '', /未保存/);
  } finally {
    h.restore();
  }
});

test('applied filters use accepted response rules, preserve literal values, and expand long sets only on demand', async () => {
  const h = setupCellExplanation((_url, _init, req) => ({
    ...h.response(req),
    filter: {
      headerRow: 1,
      rules: [
        { column: 1, operator: 'in', value: '', values: ['', '<img src=x>', '00123'] },
        { column: 2, operator: 'gt', value: '1,234.50' },
      ],
      sortKeys: [{ column: 2, descending: true, mode: 'number' }],
    },
    rowState: 'matched',
  }));
  try {
    h.state.filtered = true;
    h.state.filterId = 'accepted';
    await h.ui.open();
    assert.match(h.rows()['已应用排序'] ?? '', /B列 降序（数值）/);
    assert.equal(h.rows()['当前行'], '在筛选结果中');
    const body = document.getElementById('detailBody');
    assert.ok(body);
    const labelIdx = [...body.children].findIndex((n) => n.textContent === '已应用筛选');
    const rules = body.children[labelIdx + 1]?.children[0];
    const details = rules?.children[0] as HTMLDetailsElement | undefined;
    assert.ok(rules && details);
    assert.equal(details.children.length, 1);
    assert.match(details.textContent, /3 项/);
    details.open = true;
    details.dispatchEvent(new Event('toggle'));
    assert.equal(details.children[1]?.textContent, '""\n"<img src=x>"\n"00123"');
    details.dispatchEvent(new Event('toggle'));
    assert.equal(details.children.length, 2);
    assert.match(rules.children[1]?.textContent ?? '', /B列 大于 "1,234\.50"/);
  } finally {
    h.restore();
  }
});

test('cancel and immediate reopen reject late responses even when transport ignores abort', async () => {
  let hold = false;
  let resolveReq: (() => void) | undefined;
  const h = setupCellExplanation((_url, _init, req) =>
    hold
      ? new Promise<unknown>((r) => {
          resolveReq = () => r(h.response(req));
        })
      : undefined,
  );
  try {
    hold = true;
    const pending = h.ui.open();
    await tick();
    h.ui.resetBook();
    hold = false;
    await h.ui.open();
    const page = h.ui.page;
    assert.equal(h.calls[0]?.init?.signal?.aborted, true);
    resolveReq?.();
    await pending;
    await tick();
    assert.equal(h.ui.page, page);
    assert.equal(h.ui.dialog.open, true);
  } finally {
    h.restore();
  }
});

test('selection, path, index, sheet, filter, view and frozen changes invalidate accepted and pending details', async () => {
  const changes: readonly ((h: ReturnType<typeof setupCellExplanation>) => void)[] = [
    (h) => {
      h.state.selected = {
        sheet: '明细',
        row: 2,
        col: 2,
        ref: 'B2',
        raw: '',
        display: '',
        hasFormula: false,
      };
    },
    (h) => {
      h.state.book = 'same-content-other-path';
    },
    (h) => {
      h.state.workbookKey = 'other';
    },
    (h) => {
      h.state.sheetEpoch++;
    },
    (h) => {
      h.state.index = h.state.index ? { ...h.state.index } : null;
    },
    (h) => {
      h.state.filtered = true;
      h.state.filterId = 'other';
    },
    (h) => {
      h.state.rowView = makeRowViewState({ id: 'other', sheet: '明细' });
    },
    (h) => {
      h.options.pinned = 2;
    },
  ];
  for (const change of changes) {
    let hold = false;
    let resolveReq: (() => void) | undefined;
    const h = setupCellExplanation((_url, _init, req) =>
      hold
        ? new Promise<unknown>((r) => {
            resolveReq = () => r(h.response(req));
          })
        : undefined,
    );
    try {
      await h.ui.open();
      hold = true;
      const pending = h.ui.load();
      await tick();
      change(h);
      h.ui.changed();
      assert.equal(h.ui.page, null);
      assert.equal(h.ui.dialog.open, false);
      assert.equal(h.calls.at(-1)?.init?.signal?.aborted, true);
      resolveReq?.();
      await pending;
      assert.equal(document.getElementById('detailBody')?.children.length, 0);
    } finally {
      h.restore();
    }
  }
});

test('failure offers a retry; foreign or internally contradictory candidates never replace complete facts', async () => {
  let mode = '';
  const h = setupCellExplanation((_url, _init, req) => {
    if (mode === 'fail') throw new Error('模拟失败');
    if (mode === 'foreign') return { ...h.response(req), book: 'foreign' };
    if (mode === 'cache') return { ...h.response(req), formulaState: 'cached' };
    return undefined;
  });
  try {
    mode = 'fail';
    await h.ui.open();
    assert.equal(document.getElementById('detailRetry')?.hidden, false);
    assert.match(document.getElementById('detailStatus')?.textContent ?? '', /模拟失败/);
    mode = '';
    await h.ui.load();
    const accepted = h.ui.page;
    const rowsBefore = h.rows();
    for (const value of ['foreign', 'cache']) {
      mode = value;
      await h.ui.load();
      assert.equal(h.ui.page, accepted);
      assert.deepEqual(h.rows(), rowsBefore);
      assert.equal(document.getElementById('detailRetry')?.hidden, false);
    }
  } finally {
    h.restore();
  }
});

test('response validation rejects wrong coordinates, fabricated text statistics, partial state and budget overruns', () => {
  const h = setupCellExplanation();
  try {
    const req = h.ui.binding().request;
    const validate = (p: unknown) =>
      CellExplanationModel.validate(p, req, h.limits, h.source, parseRef);
    const mutations: readonly ((p: MutableExplanationPayload) => void)[] = [
      (p) => {
        p.cell.ref = 'B2';
      },
      (p) => {
        p.cell.col = 2;
      },
      (p) => {
        p.numeric.exact = 123;
      },
      (p) => {
        p.cell.raw = '';
      },
      (p) => {
        p.cell.type = 's';
        p.numeric.reason = 'imported-amount';
      },
      (p) => {
        p.numeric.reason = 'constructor';
      },
      (p) => {
        p.formulaAvailable = true;
      },
      (p) => {
        p.rowState = 'unknown';
      },
      (p) => {
        p.presentationNotice = {};
      },
      (p) => {
        p.filter = { headerRow: 1, rules: [], sortKeys: [] };
      },
    ];
    for (const change of mutations) {
      const p = h.response(req);
      change(p);
      assert.throws(() => validate(p));
    }
    h.limits.bytes = 100;
    assert.throws(() => validate(h.response(req)), /容量/);
  } finally {
    h.restore();
  }
});

test('blank and missing caches, current presentation notices and untrusted formula text remain factual and safe', async () => {
  const h = setupCellExplanation((_url, _init, req) => {
    const p = h.response(req);
    p.cell.raw = p.cell.display = '';
    p.numeric = { eligible: false, reason: 'blank' };
    p.rowState = 'outside';
    return p;
  });
  try {
    await h.ui.open();
    assert.equal(h.rows()['原始值'], '（空白）');
    assert.equal(h.rows()['源类型'], '空白');
    assert.equal(h.rows()['当前行'], '已用范围外');
    h.options.notice = '<script>unsafe</script>';
    await h.ui.load();
    assert.equal(h.rows()['呈现状态'], '<script>unsafe</script>');
  } finally {
    h.restore();
  }
});

test('unready or changing views issue no request and escape aborts the current read', async () => {
  const h = setupCellExplanation();
  try {
    h.options.canRead = false;
    await h.ui.open();
    assert.equal(h.calls.length, 0);
    h.options.canRead = true;
    await h.ui.open();
    h.ui.dialog.dispatchEvent(new Event('cancel', { cancelable: true }));
    assert.equal(h.ui.page, null);
    assert.equal(h.ui.dialog.open, false);
  } finally {
    h.restore();
  }
});
