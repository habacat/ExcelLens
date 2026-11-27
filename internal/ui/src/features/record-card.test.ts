import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { ApiRequestOptions } from '../contracts/common.ts';
import type { RecordCardResponse } from '../contracts/features.ts';
import type { RecordCardLimits, SourceCapabilities } from '../contracts/formats.ts';
import type { CellData } from '../contracts/workbook.ts';
import { columnName } from '../core/cell-address.ts';
import { createInitialWorkbookState } from '../grid/state.ts';
import { installDom, MockHTMLOptionElement, type MockDocument } from '../test-helpers/dom-mock.ts';
import { makeDefaultCapabilities } from '../test-helpers/grid-fixture.ts';
import type { RecordCardRequest } from './record-card-model.ts';
import { RecordCard } from './record-card.ts';

const tick = (): Promise<void> =>
  new Promise((resolve) => {
    setImmediate(resolve);
  });

function installOptionGlobal(doc: MockDocument): () => void {
  const saved = Object.getOwnPropertyDescriptor(globalThis, 'Option');
  class MockOption extends MockHTMLOptionElement {
    constructor(text = '', value?: string) {
      super('option', doc);
      this.text = text;
      this.value = value ?? text;
    }
  }
  Object.defineProperty(globalThis, 'Option', {
    value: MockOption,
    configurable: true,
    writable: true,
  });
  return () => {
    if (saved === undefined) Reflect.deleteProperty(globalThis, 'Option');
    else Object.defineProperty(globalThis, 'Option', saved);
  };
}

interface RecordedCall {
  readonly route: string;
  readonly body: RecordCardRequest;
  readonly signal: AbortSignal | undefined;
}

interface RecordedNavigation {
  readonly sheet: string;
  readonly ref: string;
  readonly guard: () => boolean;
}

type ApiOverride = (
  route: string,
  init: ApiRequestOptions | undefined,
  body: RecordCardRequest,
) => unknown;

function harness(override?: ApiOverride) {
  const dom = installDom();
  const restoreOption = installOptionGlobal(dom.document);
  dom.register('recordCardOpenBtn', 'button');
  dom.register('menuRecordCard', 'button');

  const calls: RecordedCall[] = [];
  const copied: string[] = [];
  const toasts: (readonly unknown[])[] = [];
  const navigations: RecordedNavigation[] = [];

  const source: SourceCapabilities = makeDefaultCapabilities({
    maxRows: 1_000_000,
    maxColumns: 16_384,
  });
  const limits: RecordCardLimits = {
    columns: 32,
    bytes: 8 << 20,
    requestBytes: 8192,
    textUnits: 4096,
  };

  const state = createInitialWorkbookState(1_000_000, 16_384);
  state.opened = true;
  state.indexReady = true;
  state.index = {
    id: 'idx-1',
    sheet: '明细',
    status: 'done',
    doneBytes: 0,
    totalBytes: 0,
    maxRow: 5,
    maxCol: 35,
    totalRows: 5,
  };
  state.sheet = '明细';
  state.sheetEpoch = 1;
  state.workbookKey = 'content';
  state.book = 'path';
  state.rowView = {
    id: 'view',
    sheet: '明细',
    filterId: 'filter',
    workbookKey: 'content',
    book: 'path',
    revision: 0,
    settings: { mode: 'original' },
    totalRows: 5,
    maxRow: 5,
    digest: 'd',
    notice: '',
    settingsWarning: '',
    matchedPinnedRows: null,
    hiddenRanges: [],
    groupCount: 0,
  };
  state.selected = {
    ref: 'A2',
    row: 2,
    col: 1,
    sheet: '明细',
    raw: '000123',
    display: '000123',
    hasFormula: false,
  };
  state.fileName = '<img src=x>.xlsx';
  state.filtered = true;
  state.filterId = 'filter';

  const options = { pinned: 1, canRead: true, copyResult: true };
  let navigateImpl: (sheet: string, ref: string, guard: () => boolean) => Promise<unknown> = async (
    sheet,
    ref,
    guard,
  ) => {
    navigations.push({ sheet, ref, guard });
    return guard();
  };

  const long = `${'长'.repeat(4095)}😀<script>危险文本</script>\n${'尾😀'.repeat(4000)}`;
  const header = `${'名'.repeat(4095)}${'😀字段名'.repeat(100)}`;

  const neighbors: Readonly<Record<number, readonly [number, number, number]>> = {
    4: [1, 0, 5],
    5: [2, 4, 2],
    2: [3, 5, 1],
    1: [4, 2, 0],
  };

  const response = (req: RecordCardRequest): RecordCardResponse => {
    const neighbor = neighbors[req.row] ?? ([1, 0, 0] as const);
    const columns = Math.min(req.columns, 35 - req.firstColumn + 1);
    return {
      ...req,
      columns,
      position: neighbor[0],
      totalRecords: req.row === 3 ? 1 : 4,
      previousRow: neighbor[1],
      nextRow: neighbor[2],
      totalColumns: 35,
      rowStyle: 0,
      headerStyle: 0,
      rowIndexed: true,
      notices: [],
      fields: Array.from({ length: columns }, (_, at) => {
        const column = req.firstColumn + at;
        const raw =
          column === 1
            ? '000123'
            : column === 2
              ? '9007199254740993.31'
              : column === 3
                ? long
                : column === 4
                  ? ''
                  : column === 5
                    ? '2'
                    : String(column);
        const cell: CellData = {
          ref: `${columnName(column)}${req.row}`,
          col: column,
          raw,
          display: column === 2 ? '9,007,199,254,740,993.31' : raw,
          hasFormula: column === 4 || column === 5,
          style: 0,
          ...(column === 1 ? { type: 's' } : {}),
          ...(column === 4
            ? {
                cacheMissing: true,
                sharedFormula: true,
                formula: '[共享公式组 si=9：未找到主公式]',
              }
            : {}),
          ...(column === 5 ? { formula: '=1+1' } : {}),
        };
        if (!req.headerRow) {
          return { column, cell };
        }
        const headerVal = column === 1 ? '' : column === 3 ? header : '重复字段';
        return {
          column,
          cell,
          header: {
            ref: `${columnName(column)}${req.headerRow}`,
            col: column,
            raw: headerVal,
            display: headerVal,
            hasFormula: false,
            style: 0,
            type: 's',
          },
        };
      }),
    };
  };

  const api = async <T = unknown>(route: string, init?: ApiRequestOptions): Promise<T> => {
    const body = JSON.parse(typeof init?.body === 'string' ? init.body : '{}') as RecordCardRequest;
    calls.push({ route, body, signal: init?.signal ?? undefined });
    const special = override?.(route, init, body);
    const value = special !== undefined ? await Promise.resolve(special) : response(body);
    return value as T;
  };

  const card = new RecordCard({
    state,
    api,
    toast: (...args) => toasts.push(args),
    copy: async (text) => {
      copied.push(text);
      return options.copyResult;
    },
    navigate: (sheet, ref, guard) => navigateImpl(sheet, ref, guard),
    colName: columnName,
    parseColumn: (text) =>
      /^\d+$/.test(text)
        ? Number(text)
        : /^[a-z]+$/i.test(text)
          ? Array.from(text.toUpperCase()).reduce((n, c) => n * 26 + c.charCodeAt(0) - 64, 0)
          : 0,
    pinnedThroughRow: () => options.pinned,
    source: () => source,
    limits,
    canRead: () => options.canRead,
  });

  const requireNode = (id: string): HTMLElement => {
    const el = document.getElementById(id);
    assert.ok(el, `Missing #${id}`);
    return el;
  };
  const requireBtn = (id: string): HTMLButtonElement => {
    const el = requireNode(id);
    assert.ok(el instanceof HTMLButtonElement, `Expected button #${id}`);
    return el;
  };
  const requireInp = (id: string): HTMLInputElement => {
    const el = requireNode(id);
    assert.ok(el instanceof HTMLInputElement, `Expected input #${id}`);
    return el;
  };
  const requireSel = (id: string): HTMLSelectElement => {
    const el = requireNode(id);
    assert.ok(el instanceof HTMLSelectElement, `Expected select #${id}`);
    return el;
  };

  return {
    card,
    state,
    source,
    limits,
    options,
    calls,
    copied,
    toasts,
    navigations,
    response,
    long,
    header,
    requireNode,
    requireBtn,
    requireInp,
    requireSel,
    setNavigate(fn: (sheet: string, ref: string, guard: () => boolean) => Promise<unknown>): void {
      navigateImpl = fn;
    },
    restore(): void {
      card.close();
      restoreOption();
      dom.restore();
    },
  };
}

test('record card reads only on explicit open, preserves source strings and renders untrusted names as text', async () => {
  const h = harness();
  try {
    assert.equal(h.calls.length, 0);
    await h.card.open();
    assert.equal(h.calls.length, 1);
    const fieldsHost = h.requireNode('recordCardFields');
    assert.equal(fieldsHost.children.length, 32);
    assert.equal(h.card.dialog.querySelectorAll('img').length, 0);
    assert.equal(h.card.dialog.querySelectorAll('script').length, 0);
    assert.match(h.requireNode('recordCardSource').textContent, /<img src=x>/);
    assert.match(fieldsHost.textContent, /空白字段名/);

    const copyBtn1 = fieldsHost.children[1]?.querySelector('.record-field-copy');
    assert.ok(copyBtn1 instanceof HTMLButtonElement);
    copyBtn1.click();
    await tick();
    assert.equal(h.copied.at(-1), '9,007,199,254,740,993.31');

    const modeSelect = h.requireSel('recordCardMode');
    modeSelect.value = 'raw';
    modeSelect.dispatchEvent(new Event('change'));

    const rawCopyBtn1 = fieldsHost.children[1]?.querySelector('.record-field-copy');
    assert.ok(rawCopyBtn1 instanceof HTMLButtonElement);
    rawCopyBtn1.click();
    await tick();
    assert.equal(h.copied.at(-1), '9007199254740993.31');

    const rawCopyBtn0 = fieldsHost.children[0]?.querySelector('.record-field-copy');
    assert.ok(rawCopyBtn0 instanceof HTMLButtonElement);
    rawCopyBtn0.click();
    await tick();
    assert.equal(h.copied.at(-1), '000123');
    assert.equal(h.calls.length, 1);
  } finally {
    h.restore();
  }
});

test('field pages accept the short last page; previous and next use the source rows from the visible order', async () => {
  const h = harness();
  try {
    await h.card.open();
    h.requireBtn('recordFieldNext').click();
    await tick();
    const fieldsHost = h.requireNode('recordCardFields');
    assert.equal(fieldsHost.children.length, 3);
    assert.equal(fieldsHost.children[0]?.getAttribute('data-column'), '33');
    assert.equal(h.requireBtn('recordFieldNext').disabled, true);

    h.requireBtn('recordCardPrevious').click();
    await tick();
    assert.equal(h.calls.at(-1)?.body.row, 5);
    assert.equal(fieldsHost.children[0].getAttribute('data-column'), '33');

    h.requireBtn('recordCardPrevious').click();
    await tick();
    assert.match(h.requireNode('recordCardPosition').textContent, /源行 4$/);
    assert.equal(h.requireBtn('recordCardPrevious').disabled, true);

    h.requireInp('recordHeaderRow').value = '0';
    await h.card.apply();
    assert.equal(h.requireNode('recordCardHeader').textContent, '字段名使用列字母');
    assert.match(fieldsHost.textContent, /列 AG/);

    h.requireBtn('recordFieldPrevious').click();
    await tick();
    assert.equal(fieldsHost.children[0].getAttribute('data-column'), '1');
  } finally {
    h.restore();
  }
});

test('missing caches never acquire a value-copy action and unrecoverable formulas have no formula text action', async () => {
  const h = harness();
  try {
    await h.card.open();
    const fieldsHost = h.requireNode('recordCardFields');
    const article = fieldsHost.children[3];
    const articleFormula = fieldsHost.children[4];
    assert.ok(article);
    assert.ok(articleFormula);

    assert.match(article.textContent, /公式未保存计算结果/);
    assert.match(article.textContent, /公式无法还原/);
    const unavailable = [...article.querySelectorAll('button[data-unavailable="true"]')];
    assert.ok(unavailable.length >= 2);
    assert.ok(unavailable.every((btn) => btn instanceof HTMLButtonElement && btn.disabled));
    assert.equal(article.querySelectorAll('.record-field-formula').length, 0);
    assert.equal(articleFormula.querySelectorAll('.record-field-formula').length, 1);

    h.card.controls();
    h.card.cancel();
    assert.ok(
      [...article.querySelectorAll('button[data-unavailable="true"]')].every(
        (btn) => btn instanceof HTMLButtonElement && btn.disabled,
      ),
    );

    const copyBtn = article.querySelector('.record-field-copy');
    const fullBtn = article.querySelector('.record-field-full');
    assert.ok(copyBtn instanceof HTMLButtonElement);
    assert.ok(fullBtn instanceof HTMLButtonElement);
    copyBtn.disabled = false;
    copyBtn.click();
    await tick();
    assert.equal(h.copied.length, 0);

    fullBtn.disabled = false;
    fullBtn.click();
    assert.equal(h.card.detailDialog.open, false);
    assert.match(article.textContent, /公式无法还原/);
  } finally {
    h.restore();
  }
});

test('long values and field names are segmented for display while full copy remains exact', async () => {
  const h = harness();
  try {
    await h.card.open();
    const fieldsHost = h.requireNode('recordCardFields');
    const article = fieldsHost.children[2];
    assert.ok(article);
    const previewVal = article.querySelector('.record-field-value');
    assert.ok(previewVal);
    assert.ok(previewVal.textContent.length <= h.limits.textUnits);

    const cases: readonly (readonly [string, string])[] = [
      ['.record-field-full', h.long],
      ['.record-header-full', h.header],
    ];
    for (const [selector, expectedText] of cases) {
      const trigger: Element | null = article.querySelector(selector);
      assert.ok(trigger instanceof HTMLButtonElement);
      trigger.click();
      assert.equal(h.card.detailDialog.open, true);

      const parts: string[] = [];
      const nextBtn = h.requireBtn('recordTextNext');
      const valueEl = h.requireNode('recordTextValue');
      for (;;) {
        const part = valueEl.textContent;
        assert.ok(part.length <= h.limits.textUnits);
        assert.ok(!/^[\uDC00-\uDFFF]|[\uD800-\uDBFF]$/.test(part));
        parts.push(part);
        if (nextBtn.disabled) break;
        nextBtn.click();
      }
      assert.equal(parts.join(''), expectedText);

      h.requireBtn('recordTextCopy').click();
      await tick();
      assert.equal(h.copied.at(-1), expectedText);
      h.card.closeDetail();
    }

    const formulaBtn = fieldsHost.children[4]?.querySelector('.record-field-formula');
    assert.ok(formulaBtn instanceof HTMLButtonElement);
    formulaBtn.click();
    assert.equal(h.requireNode('recordTextValue').textContent, '=1+1');
    assert.equal(h.calls.length, 1);
  } finally {
    h.restore();
  }
});

test('cancel then immediate retry rejects a late response even when the transport ignores abort', async () => {
  let hold = false;
  let resolveLate: ((value: RecordCardResponse) => void) | undefined;
  let latePage: RecordCardResponse | undefined;
  const h = harness((_route, _init, req) => {
    if (!hold) return undefined;
    latePage = h.response(req);
    return new Promise<RecordCardResponse>((resolve) => {
      resolveLate = resolve;
    });
  });
  try {
    await h.card.open();
    hold = true;
    h.requireBtn('recordCardPrevious').click();
    await tick();
    h.card.cancel();
    hold = false;
    h.requireBtn('recordCardNext').click();
    await tick();
    await tick();
    assert.match(h.requireNode('recordCardPosition').textContent, /源行 1$/);

    assert.ok(resolveLate && latePage);
    resolveLate(latePage);
    await tick();
    await tick();
    assert.match(h.requireNode('recordCardPosition').textContent, /源行 1$/);
    assert.equal(h.requireBtn('recordCardCancel').hidden, true);
    assert.equal(h.calls[1]?.signal?.aborted, true);
  } finally {
    h.restore();
  }
});

test('failed and foreign candidates leave the last complete record and its DOM available for retry', async () => {
  let mode = '';
  const h = harness((_route, _init, req) => {
    if (mode === 'fail') return Promise.reject(new Error('模拟读取失败'));
    if (mode === 'foreign') return { ...h.response(req), book: 'foreign' };
    return undefined;
  });
  try {
    await h.card.open();
    const fieldsHost = h.requireNode('recordCardFields');
    const article = fieldsHost.children[0];
    assert.ok(article);

    for (const value of ['fail', 'foreign']) {
      mode = value;
      h.requireBtn('recordCardPrevious').click();
      await tick();
      await tick();
      assert.equal(fieldsHost.children[0], article);
      assert.match(h.requireNode('recordCardStatus').textContent, /保留上一次完整记录/);
      assert.equal(h.requireBtn('recordCardNext').disabled, false);
    }

    mode = '';
    h.requireBtn('recordCardPrevious').click();
    await tick();
    await tick();
    assert.match(h.requireNode('recordCardPosition').textContent, /源行 5$/);
  } finally {
    h.restore();
  }
});

test('file path, sheet, exact index, filter, row view and frozen-prefix changes invalidate accepted and late pages', async () => {
  const changes: readonly ((h: ReturnType<typeof harness>) => void)[] = [
    (x) => {
      x.state.book = 'same-content-other-path';
    },
    (x) => {
      x.state.sheetEpoch++;
    },
    (x) => {
      if (x.state.index) x.state.index = { ...x.state.index };
    },
    (x) => {
      x.state.filterId = 'new-filter';
    },
    (x) => {
      if (x.state.rowView) x.state.rowView = { ...x.state.rowView, id: 'new-view' };
    },
    (x) => {
      x.options.pinned = 2;
    },
  ];
  for (const change of changes) {
    let hold = false;
    let resolveLate: ((value: RecordCardResponse) => void) | undefined;
    let latePage: RecordCardResponse | undefined;
    const h = harness((_route, _init, req) => {
      if (!hold) return undefined;
      latePage = h.response(req);
      return new Promise<RecordCardResponse>((resolve) => {
        resolveLate = resolve;
      });
    });
    try {
      await h.card.open();
      hold = true;
      h.requireBtn('recordCardPrevious').click();
      await tick();
      change(h);
      h.card.controls();
      assert.equal(h.card.dialog.open, false);
      assert.equal(h.calls.at(-1)?.signal?.aborted, true);
      assert.ok(resolveLate && latePage);
      resolveLate(latePage);
      await tick();
      await tick();
      assert.equal(h.requireNode('recordCardFields').children.length, 0);
    } finally {
      h.restore();
    }
  }
});

test('queued native close events cannot erase a card or full-value dialog that was immediately reopened', async () => {
  const h = harness();
  try {
    await h.card.open();
    h.card.close();
    const reopening = h.card.open();
    await reopening;
    await tick();
    assert.equal(h.card.dialog.open, true);
    assert.equal(h.requireNode('recordCardFields').children.length, 32);

    const article = h.requireNode('recordCardFields').children[2];
    assert.ok(article);
    const fullBtn = article.querySelector('.record-field-full');
    const headerBtn = article.querySelector('.record-header-full');
    assert.ok(fullBtn instanceof HTMLButtonElement);
    assert.ok(headerBtn instanceof HTMLButtonElement);

    fullBtn.click();
    h.card.closeDetail();
    headerBtn.click();
    await tick();
    assert.equal(h.card.detailDialog.open, true);
    assert.equal(h.requireNode('recordTextValue').textContent, h.header.slice(0, 4095));
  } finally {
    h.restore();
  }
});

test('navigation survives its own asynchronous close event and stops when its captured source changes', async () => {
  const h = harness();
  try {
    await h.card.open();
    const locateBtn = h.requireNode('recordCardFields').children[1]?.querySelector('.record-field-locate');
    assert.ok(locateBtn instanceof HTMLButtonElement);
    locateBtn.click();
    await tick();
    assert.equal(h.card.dialog.open, false);
    assert.ok(h.navigations[0]);
    assert.equal(h.navigations[0].ref, 'B2');
    assert.equal(h.navigations[0].guard(), true);
    h.state.book = 'other';
    assert.equal(h.navigations[0].guard(), false);

    h.state.book = 'path';
    await h.card.open();
    let activeGuard: (() => boolean) | undefined;
    let releaseNav: (() => void) | undefined;
    h.setNavigate((_sheet, _ref, guard) => {
      activeGuard = guard;
      return new Promise<void>((resolve) => {
        releaseNav = resolve;
      });
    });
    const locateAgain = h.requireNode('recordCardFields').children[0]?.querySelector('.record-field-locate');
    assert.ok(locateAgain instanceof HTMLButtonElement);
    locateAgain.click();
    await tick();
    assert.ok(activeGuard);
    assert.equal(activeGuard(), true);
    document.dispatchEvent(new Event('pointerdown'));
    assert.equal(activeGuard(), false);
    releaseNav?.();
    await tick();
  } finally {
    h.restore();
  }
});

test('copy failure is reported accurately and unfinished row-view changes cannot navigate or read', async () => {
  const h = harness();
  try {
    await h.card.open();
    h.options.copyResult = false;
    const fieldsHost = h.requireNode('recordCardFields');
    const copy0 = fieldsHost.children[0]?.querySelector('.record-field-copy');
    assert.ok(copy0 instanceof HTMLButtonElement);
    copy0.click();
    await tick();
    const statusEl = h.requireNode('recordCardStatus');
    assert.match(statusEl.textContent, /复制失败/);
    assert.equal(statusEl.classList.contains('is-error'), true);

    const full2 = fieldsHost.children[2]?.querySelector('.record-field-full');
    assert.ok(full2 instanceof HTMLButtonElement);
    full2.click();
    h.requireBtn('recordTextCopy').click();
    await tick();
    assert.match(h.requireNode('recordTextStatus').textContent, /复制失败/);
    h.card.closeDetail();

    const reads = h.calls.length;
    const copies = h.copied.length;
    h.options.canRead = false;
    h.requireBtn('recordCardPrevious').click();
    copy0.click();
    const locate0 = fieldsHost.children[0]?.querySelector('.record-field-locate');
    assert.ok(locate0 instanceof HTMLButtonElement);
    locate0.click();
    await tick();
    assert.equal(h.calls.length, reads);
    assert.equal(h.copied.length, copies);
    assert.equal(h.navigations.length, 0);
  } finally {
    h.restore();
  }
});

test('invalid field settings issue no request; empty or unprepared sources keep the entry disabled', async () => {
  const h = harness();
  try {
    await h.card.open();
    const reads = h.calls.length;
    const invalidInputs: readonly (readonly [string, string])[] = [
      ['', 'A'],
      ['-1', 'A'],
      ['1.2', 'A'],
      ['1', 'ZZZZ'],
      ['1', ''],
    ];
    for (const [headerRow, startColumn] of invalidInputs) {
      h.requireInp('recordHeaderRow').value = headerRow;
      h.requireInp('recordStartColumn').value = startColumn;
      await h.card.apply();
      assert.equal(h.calls.length, reads);
    }

    h.card.close();
    h.state.index = {
      id: 'idx-empty',
      sheet: '明细',
      status: 'done',
      doneBytes: 0,
      totalBytes: 0,
      maxRow: 1,
      maxCol: 1,
      totalRows: 0,
    };
    h.state.selected = {
      ref: 'A1',
      row: 1,
      col: 1,
      sheet: '明细',
      raw: '',
      display: '',
      hasFormula: false,
    };
    h.card.controls();
    assert.equal(h.requireBtn('recordCardOpenBtn').disabled, true);
    await h.card.open();
    assert.equal(h.calls.length, reads);
  } finally {
    h.restore();
  }
});
