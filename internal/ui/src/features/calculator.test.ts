import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import type { ApiRequestOptions } from '../contracts/common.ts';
import { FormulaInput } from '../editing/formula-entry-model.ts';
import { createInitialWorkbookState } from '../grid/state.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import {
  GridInteractions,
  makeDefaultGridInteractionsDeps,
} from '../test-helpers/grid-fixture.ts';
import { Calculator } from './calculator.ts';
import type { CalculatorRowRecord, CalculatorSuggestion } from './calculator-model.ts';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const INDEX_HTML = fs.readFileSync(path.join(HERE, '../../web/index.html'), 'utf8');

const tick = (): Promise<void> =>
  new Promise((resolve) => {
    setImmediate(resolve);
  });

function deferred<T = unknown>() {
  let resolve: (value: T | PromiseLike<T>) => void = () => undefined;
  let reject: (reason?: unknown) => void = () => undefined;
  const promise = new Promise<T>((a, b) => {
    resolve = a;
    reject = b;
  });
  return { resolve, reject, promise };
}

const calcResult = (value: string) => ({ value, exact: value, display: value, copy: value });

const mouseEvent = (detail?: number): MouseEvent => {
  const ev = detail === undefined ? new Event('click') : Object.assign(new Event('click'), { detail });
  return ev as MouseEvent;
};

function setupCalculator(
  apiImpl: (url: string, options?: ApiRequestOptions) => Promise<unknown> = async () =>
    calcResult('3'),
  recoveryRecords?: Readonly<Record<string, CalculatorRowRecord>>,
) {
  const dom = installDom({ fromIndexHtml: true });
  Object.defineProperty(window, 'innerWidth', { value: 1200, configurable: true, writable: true });
  Object.defineProperty(window, 'innerHeight', { value: 800, configurable: true, writable: true });
  const calcWindow = dom.document.getElementById('calculatorWindow');
  assert.ok(calcWindow);
  calcWindow.style.left = '760px';
  calcWindow.style.top = '54px';
  calcWindow.style.width = '440px';
  calcWindow.getBoundingClientRect = () => ({
    x: parseFloat(String(calcWindow.style.left)) || 760,
    y: parseFloat(String(calcWindow.style.top)) || 54,
    left: parseFloat(String(calcWindow.style.left)) || 760,
    top: parseFloat(String(calcWindow.style.top)) || 54,
    right:
      (parseFloat(String(calcWindow.style.left)) || 760) +
      (parseFloat(String(calcWindow.style.width)) || 440),
    bottom: (parseFloat(String(calcWindow.style.top)) || 54) + 370,
    width: parseFloat(String(calcWindow.style.width)) || 440,
    height: 370,
    toJSON() {
      return this;
    },
  });
  for (const id of [
    'calculatorDrag',
    'calculatorRows',
    'calculatorScroll',
    'calculatorCompletion',
    'calculatorClose',
    'calculatorMinimize',
    'calculatorNotes',
    'calculatorStatus',
    'calculatorRecoveryBtn',
  ]) {
    const child = dom.document.getElementById(id);
    if (child) calcWindow.appendChild(child);
  }
  const scrollRaw = dom.document.getElementById('calculatorScroll');
  const rowsRaw = dom.document.getElementById('calculatorRows');
  assert.ok(scrollRaw && rowsRaw);
  Object.defineProperty(scrollRaw, 'clientHeight', {
    configurable: true,
    get: () => parseFloat(String(scrollRaw.style.height)) || 156,
  });
  Object.defineProperty(scrollRaw, 'scrollHeight', {
    configurable: true,
    get: () => parseFloat(String(rowsRaw.style.height)) || 0,
  });

  const grid = document.getElementById('grid');
  assert.ok(grid);
  grid.className = 'grid-region';

  const state = createInitialWorkbookState(1_048_576, 16_384);
  state.workbookKey = 'file-A';
  state.sheet = '数据';
  state.indexReady = true;

  const copied: string[] = [];
  const toasts: string[] = [];
  const wrappedAPI = async <T = unknown>(
    url: string,
    options?: ApiRequestOptions,
  ): Promise<T> => {
    if (url.startsWith('/api/recovery?')) {
      const res: unknown = { revision: 1, records: recoveryRecords ?? {} };
      return res as T;
    }
    if (url === '/api/recovery') {
      const body =
        typeof options?.body === 'string'
          ? (JSON.parse(options.body) as { revision?: number })
          : {};
      const res: unknown = { revision: (body.revision ?? 0) + 1 };
      return res as T;
    }
    if (url === '/api/calculator/functions') {
      const res: unknown = {
        items: [
          { name: 'SUM', signature: 'SUM(数值1, …)' },
          { name: 'SUBSTITUTE', signature: 'SUBSTITUTE(文本, 原文本, 新文本)' },
          { name: 'IF', signature: 'IF(条件, 成立时, 不成立时)' },
          { name: 'LEFT', signature: 'LEFT(文本, 字符数)' },
        ],
      };
      return res as T;
    }
    return (await apiImpl(url, options)) as T;
  };

  const calc = new Calculator({
    state,
    api: wrappedAPI,
    grid,
    copy: async (s) => {
      copied.push(s);
      return true;
    },
    toast: (s) => toasts.push(s),
    enableRecovery: Boolean(recoveryRecords),
    session: recoveryRecords
      ? {
          id: 'recovery-page-0000000001',
          nativeLaunch: false,
          restoring: false,
          cacheCleared: false,
          ready: Promise.resolve(),
          apply: (h) => h,
          returnHomeAfterCacheReset: () => undefined,
        }
      : undefined,
    formulaContext: (text, caret) => FormulaInput.context(text, caret),
    closeFormula: (text) => FormulaInput.close(text),
  });
  calc.open();

  const type = (index: number, text: string): HTMLInputElement => {
    calc.focus(index);
    const view = calc.views.get(index);
    assert.ok(view);
    view.input.value = text;
    view.input.setSelectionRange(text.length, text.length);
    view.input.dispatchEvent(new Event('input', { bubbles: true }));
    return view.input;
  };

  const key = (target: HTMLElement, k: string, extra: Record<string, unknown> = {}) => {
    const e = Object.assign(new Event('keydown', { cancelable: true, bubbles: true }), {
      key: k,
      ...extra,
    });
    Object.defineProperty(e, 'target', { value: target, configurable: true });
    calc.handleKey(e as KeyboardEvent);
    return e;
  };

  return {
    calc,
    state,
    copied,
    toasts,
    type,
    key,
    restore: () => {
      calc.recovery?.dispose();
      dom.restore();
    },
  };
}

test('pasting formula rows decodes quoted cells and refuses extra columns before changing any row', async () => {
  const h = setupCalculator();
  try {
    const input = h.calc.views.get(0)?.input;
    assert.ok(input);
    const pasteEv = Object.assign(new Event('paste', { cancelable: true, bubbles: true }), {
      clipboardData: { getData: () => '"=IF(TRUE,""a"",""b"")"\r\n=1+2\r\n' },
    });
    Object.defineProperty(pasteEv, 'target', { value: input, configurable: true });
    h.calc.paste(pasteEv);
    await tick();
    assert.equal(h.calc.model.get(0).text, '=IF(TRUE,"a","b")');
    assert.equal(h.calc.model.get(1).text, '=1+2');

    const badPaste = Object.assign(new Event('paste', { cancelable: true, bubbles: true }), {
      clipboardData: { getData: () => '=10\t=20\n' },
    });
    Object.defineProperty(badPaste, 'target', { value: input, configurable: true });
    h.calc.paste(badPaste);
    assert.equal(h.calc.model.get(0).text, '=IF(TRUE,"a","b")');
    assert.match(h.toasts.at(-1) ?? '', /1列/);
  } finally {
    h.restore();
  }
});

test('restored formulas wait through focus changes, notes and Escape until explicitly calculated', async () => {
  let calls = 0;
  const h = setupCalculator(
    async () => {
      calls++;
      return calcResult('3');
    },
    {
      '0': {
        text: '=SUM(1,2',
        note: '待复核',
        source: { workbookKey: 'file-A', sheet: '数据' },
      },
    },
  );
  try {
    await h.calc.recovery?.ready;
    assert.equal(h.calc.model.get(0).restored, true);
    assert.equal(h.calc.views.get(0)?.value.textContent, '待计算');
    h.calc.focus(1);
    h.calc.focus(0, 'note');
    h.calc.model.setNote(0, '只改备注');
    h.calc.hide();
    h.calc.open();
    assert.equal(calls, 0);
    assert.equal(h.calc.model.get(0).text, '=SUM(1,2');
    h.type(0, '=100');
    const input = h.calc.views.get(0)?.input;
    assert.ok(input);
    h.key(input, 'Escape');
    assert.equal(h.calc.model.get(0).text, '=SUM(1,2');
    h.calc.focus(1);
    assert.equal(calls, 0);
    h.calc.focus(0);
    h.key(input, 'Enter');
    await tick();
    assert.equal(calls, 1);
    assert.equal(h.calc.model.get(0).text, '=SUM(1,2)');
    assert.equal(h.calc.model.get(0).result?.display, '3');
  } finally {
    h.restore();
  }
});

test('the window contains just a formula/result table and title controls', () => {
  const h = setupCalculator();
  try {
    assert.equal(h.calc.views.size, 5);
    assert.equal(document.getElementById('calculatorPick'), null);
    assert.equal(document.getElementById('calculatorNote'), null);
    assert.equal(document.getElementById('calculatorHistoryBtn'), null);
    assert.equal(document.getElementById('calculatorCopyProcess'), null);
    assert.match(
      INDEX_HTML,
      /role="columnheader">公式<\/span><span role="columnheader">结果<\/span>/,
    );
    assert.equal(h.calc.picking, false);
  } finally {
    h.restore();
  }
});

test('typing equals and clicking cells builds a normal formula without a pick button', async () => {
  const calls: { expression?: string }[] = [];
  const h = setupCalculator(async (_url, o) => {
    calls.push(
      typeof o?.body === 'string' ? (JSON.parse(o.body) as { expression?: string }) : {},
    );
    return calcResult('0.3');
  });
  try {
    h.type(0, '=');
    assert.equal(h.calc.picking, true);
    h.calc.captureCell('A2', mouseEvent(1));
    assert.equal(h.calc.model.get(0).text, '=A2');
    h.type(0, '=A2+');
    h.calc.captureCell('B3', mouseEvent(1));
    assert.equal(h.calc.model.get(0).text, '=A2+B3');
    assert.equal(calls.length, 0);
    const input = h.calc.views.get(0)?.input;
    assert.ok(input);
    h.key(input, 'Enter');
    await tick();
    assert.equal(calls[0]?.expression, '=A2+B3');
    assert.equal(h.calc.views.get(0)?.value.textContent, '0.3');
    assert.equal(h.calc.model.active, 1);
    assert.equal(h.calc.picking, false);
  } finally {
    h.restore();
  }
});

test('clicking a different cell replaces the current pointed reference without inventing an operator', () => {
  const h = setupCalculator();
  try {
    h.type(0, '=');
    h.calc.captureCell('A1', mouseEvent());
    h.calc.captureCell('B2', mouseEvent());
    assert.equal(h.calc.model.get(0).text, '=B2');
    h.type(0, '=100+A1*2');
    h.calc.views.get(0)?.input.setSelectionRange(5, 7);
    h.calc.captureCell('C4', mouseEvent());
    assert.equal(h.calc.model.get(0).text, '=100+C4*2');
  } finally {
    h.restore();
  }
});

test('cross-sheet picks retain the original formula sheet and quote the selected sheet name', async () => {
  let request: { sheet?: string; workbookKey?: string } | undefined;
  const h = setupCalculator(async (_url, o) => {
    request =
      typeof o?.body === 'string'
        ? (JSON.parse(o.body) as { sheet?: string; workbookKey?: string })
        : {};
    return calcResult('1');
  });
  try {
    h.type(0, '=1+');
    h.state.sheet = "O'Brien";
    h.calc.captureCell('A2', mouseEvent());
    assert.equal(h.calc.model.get(0).text, "=1+'O''Brien'!A2");
    await h.calc.commit(0);
    assert.ok(request);
    assert.equal(request.sheet, '数据');
    assert.equal(request.workbookKey, 'file-A');
  } finally {
    h.restore();
  }
});

test('dragging a range inserts A1:A10 in SUM and keeps the input focused', () => {
  const h = setupCalculator();
  try {
    h.type(0, '=SUM(');
    h.calc.captureRange('A1', 'A10');
    assert.equal(h.calc.model.get(0).text, '=SUM(A1:A10');
    assert.equal(document.activeElement, h.calc.views.get(0)?.input);
    h.type(0, '=SUM(A1:A10)');
    assert.equal(h.calc.model.get(0).text, '=SUM(A1:A10)');
  } finally {
    h.restore();
  }
});

test('whole column and row picks keep colon references and reuse replacement and sheet quoting', async () => {
  let request: { expression?: string; sheet?: string } | undefined;
  const h = setupCalculator(async (_url, options) => {
    request =
      typeof options?.body === 'string'
        ? (JSON.parse(options.body) as { expression?: string; sheet?: string })
        : {};
    return calcResult('60');
  });
  try {
    h.type(0, '=SUM(');
    h.calc.captureRange('B:B');
    assert.equal(h.calc.model.get(0).text, '=SUM(B:B');
    h.calc.captureRange('A:C');
    assert.equal(h.calc.model.get(0).text, '=SUM(A:C');
    h.calc.captureRange('2:2');
    assert.equal(h.calc.model.get(0).text, '=SUM(2:2');
    assert.equal(document.activeElement, h.calc.views.get(0)?.input);
    h.state.sheet = "O'Brien";
    h.calc.captureRange('D:D');
    assert.equal(h.calc.model.get(0).text, "=SUM('O''Brien'!D:D");
    await h.calc.commit(0);
    assert.ok(request);
    assert.equal(request.expression, "=SUM('O''Brien'!D:D)");
    assert.equal(request.sheet, '数据');
  } finally {
    h.restore();
  }
});

test('formula picking preserves header focus and leaves resize and filter controls usable', () => {
  const h = setupCalculator();
  try {
    h.type(0, '=SUM(');
    const grid = document.getElementById('grid');
    assert.ok(grid);
    for (const className of ['column-head', 'column-head pinned-head', 'row-head', 'pinned-row-head']) {
      const head = document.createElement('div');
      head.className = className;
      head.dataset.col = '2';
      grid.appendChild(head);
      const dispatch = (target: HTMLElement) => {
        const event = new Event('pointerdown', { cancelable: true, bubbles: true });
        target.dispatchEvent(event);
        return event;
      };
      assert.equal(dispatch(head).defaultPrevented, true, className);
      for (const control of [
        'column-resize-grip',
        'row-resize-grip',
        'column-filter',
        'auto-filter-button',
      ]) {
        const child = document.createElement('span');
        child.className = control;
        head.appendChild(child);
        assert.equal(dispatch(child).defaultPrevented, false, control);
      }
      h.calc.editing = null;
      assert.equal(dispatch(head).defaultPrevented, false);
      h.calc.activate(0);
    }
  } finally {
    h.restore();
  }
});

test('whole-column range endpoints do not trigger function completion when Enter calculates', async () => {
  const h = setupCalculator();
  try {
    await tick();
    h.calc.catalog = [
      { name: 'CEILING', signature: 'CEILING(数值, 基数)' },
      { name: 'SUM', signature: 'SUM(数值1, …)' },
    ];
    const input = h.type(0, '=SUM(');
    h.calc.captureRange('A:C');
    await h.calc.updateCompletion();
    assert.notEqual(h.calc.completionMode, 'list');
    assert.equal(FormulaInput.context('=SUM(A:C', 8)?.prefix, '');
    h.key(input, 'Enter');
    await tick();
    assert.equal(h.calc.model.get(0).text, '=SUM(A:C)');
    assert.equal(h.calc.model.get(0).result?.display, '3');
  } finally {
    h.restore();
  }
});

test('formula rows calculate independently even when responses arrive in reverse order', async () => {
  const calls: ReturnType<typeof deferred>[] = [];
  const h = setupCalculator(async () => {
    const d = deferred();
    calls.push(d);
    return d.promise;
  });
  try {
    h.type(0, '=1+2');
    const a = h.calc.commit(0);
    h.type(1, '=3+4');
    const b = h.calc.commit(1);
    calls[1]?.resolve(calcResult('7'));
    await b;
    calls[0]?.resolve(calcResult('3'));
    await a;
    assert.equal(h.calc.views.get(0)?.value.textContent, '3');
    assert.equal(h.calc.views.get(1)?.value.textContent, '7');
  } finally {
    h.restore();
  }
});

test('a stale evaluation cannot overwrite an edited row', async () => {
  const calls: ReturnType<typeof deferred>[] = [];
  const h = setupCalculator(async () => {
    const d = deferred();
    calls.push(d);
    return d.promise;
  });
  try {
    h.type(0, '=1+2');
    const old = h.calc.commit(0);
    h.type(0, '=10+20');
    const fresh = h.calc.commit(0);
    calls[1]?.resolve(calcResult('30'));
    await fresh;
    calls[0]?.resolve(calcResult('3'));
    await old;
    assert.equal(h.calc.views.get(0)?.value.textContent, '30');
  } finally {
    h.restore();
  }
});

test('Escape restores the last committed formula and native undo is not intercepted', async () => {
  const h = setupCalculator();
  try {
    h.type(0, '=1+2');
    await h.calc.commit(0);
    h.type(0, '=1+');
    const input = h.calc.views.get(0)?.input;
    assert.ok(input);
    h.key(input, 'Escape');
    assert.equal(h.calc.model.get(0).text, '=1+2');
    assert.equal(h.calc.views.get(0)?.value.textContent, '3');
    assert.equal(h.calc.picking, false);
    h.type(0, '=9');
    const e = h.key(input, 'z', { ctrlKey: true });
    assert.equal(e.defaultPrevented, false);
    assert.equal(h.calc.model.get(0).text, '=9');
  } finally {
    h.restore();
  }
});

test('a workbook switch aborts pending source formulas and leaves completed results', async () => {
  const d = deferred();
  const h = setupCalculator(() => d.promise);
  try {
    h.type(0, '=A2+1');
    const pending = h.calc.commit(0);
    h.state.workbookKey = 'file-B';
    h.calc.bookChanged();
    d.resolve(calcResult('999'));
    await pending;
    assert.equal(h.calc.views.get(0)?.value.textContent, '#REF!');
    assert.equal(h.calc.picking, false);
  } finally {
    h.restore();
  }
});

test('multi-line formula paste fills rows and computes without showing a workflow', async () => {
  const h = setupCalculator();
  try {
    const input = h.type(0, '');
    const e = Object.assign(new Event('paste', { cancelable: true, bubbles: true }), {
      clipboardData: { getData: () => '=1+2\r\n=2+1\r\n' },
    });
    Object.defineProperty(e, 'target', { value: input, configurable: true });
    h.calc.paste(e);
    await tick();
    assert.equal(e.defaultPrevented, true);
    assert.equal(h.calc.model.get(0).text, '=1+2');
    assert.equal(h.calc.model.get(1).text, '=2+1');
    assert.equal(h.calc.views.get(1)?.value.textContent, '3');
    assert.equal(h.calc.model.active, 2);
  } finally {
    h.restore();
  }
});

test('inline errors clear stale results, and result Ctrl+C copies the displayed value', async () => {
  let fails = false;
  const h = setupCalculator(async () => {
    if (fails) throw new Error('除数不能为0');
    return calcResult('3');
  });
  try {
    h.type(0, '=1+2');
    await h.calc.commit(0);
    const valEl = h.calc.views.get(0)?.value;
    assert.ok(valEl);
    h.key(valEl, 'c', { ctrlKey: true });
    await tick();
    assert.deepEqual(h.copied, ['3']);
    fails = true;
    h.type(0, '=1/0');
    assert.equal(h.calc.views.get(0)?.value.textContent, '');
    await h.calc.commit(0);
    assert.equal(h.calc.views.get(0)?.value.textContent, '#DIV/0!');
    assert.equal(h.calc.model.get(0).result, null);
  } finally {
    h.restore();
  }
});

test('native numeric literals and long identifiers are not converted through Number', async () => {
  const h = setupCalculator();
  try {
    h.type(0, '000123456789012345678901');
    await h.calc.commit(0);
    assert.equal(h.calc.views.get(0)?.value.textContent, '000123456789012345678901');
  } finally {
    h.restore();
  }
});

test('the input keeps focus when selecting a source cell; normal table clicks do not get captured', () => {
  const h = setupCalculator();
  try {
    const grid = document.getElementById('grid');
    assert.ok(grid);
    const cell = document.createElement('div');
    cell.className = 'grid-cell';
    grid.appendChild(cell);
    h.type(0, '=');
    const ev = new Event('pointerdown', { cancelable: true, bubbles: true });
    cell.dispatchEvent(ev);
    assert.equal(ev.defaultPrevented, true);
    assert.equal(h.calc.captureCell('A2', mouseEvent(1)), true);
    h.calc.editing = null;
    assert.equal(h.calc.captureCell('A3', mouseEvent(1)), false);
  } finally {
    h.restore();
  }
});

test('the draggable window stays within the viewport', () => {
  const h = setupCalculator();
  try {
    h.calc.host.style.left = '1190px';
    h.calc.host.style.top = '790px';
    h.calc.clamp();
    assert.equal(h.calc.host.style.left, '752px');
    assert.equal(h.calc.host.style.top, '422px');
  } finally {
    h.restore();
  }
});

test('date/time suggestion waits for a click and inserts an explicit conversion formula', async () => {
  for (const [kind, formula] of [
    ['date', '=DATEVALUE(A2)'],
    ['time', '=TIMEVALUE(A2)'],
    ['datetime', '=DATEVALUE(A2)+TIMEVALUE(A2)'],
  ] as const) {
    const calls: { expression?: string }[] = [];
    const suggestion: CalculatorSuggestion = { kind, label: '转换', preview: '2026-01-02' };
    const h = setupCalculator(async (_url, options) => {
      calls.push(
        typeof options?.body === 'string'
          ? (JSON.parse(options.body) as { expression?: string })
          : {},
      );
      return calls.length === 1
        ? {
            kind: 'text',
            value: '2026/1/2',
            display: '2026/1/2',
            copy: '2026/1/2',
            suggestions: [suggestion],
          }
        : calcResult('46024');
    });
    try {
      h.type(0, '=A2');
      await h.calc.commit(0);
      assert.equal(h.calc.model.get(0).text, '=A2');
      assert.equal(calls.length, 1);
      assert.equal(h.calc.model.get(0).result?.kind, 'text');
      h.calc.applyConversion(0, suggestion);
      await tick();
      assert.equal(h.calc.model.get(0).text, formula);
      assert.equal(calls[1]?.expression, formula);
      assert.equal(h.calc.model.get(0).result?.display, '46024');
    } finally {
      h.restore();
    }
  }
});

test('an obsolete conversion hint cannot replace a newly edited formula', async () => {
  const suggestion: CalculatorSuggestion = { kind: 'date', label: '转换', preview: '2026-01-02' };
  const calls: unknown[] = [];
  const h = setupCalculator(async (_url, options) => {
    calls.push(typeof options?.body === 'string' ? JSON.parse(options.body) : {});
    return { kind: 'text', display: '2026/1/2', suggestions: [suggestion] };
  });
  try {
    h.type(0, '=A2');
    await h.calc.commit(0);
    h.type(0, '=B2+1');
    h.calc.applyConversion(0, suggestion);
    assert.equal(h.calc.model.get(0).text, '=B2+1');
    assert.equal(calls.length, 1);
  } finally {
    h.restore();
  }
});

test('the calculation viewport grows from five rows to twelve, with no data row cap', () => {
  const h = setupCalculator();
  try {
    assert.equal(h.calc.scroll.style.height, '156px');
    h.type(4, '1');
    assert.equal(h.calc.scroll.style.height, '182px');
    h.type(20, '1');
    assert.equal(h.calc.scroll.style.height, '338px');
    assert.ok(h.calc.model.rowCount > 20);
    assert.ok(h.calc.views.size <= 20);
    h.calc.focus(1_000_000);
    assert.equal(h.calc.model.active, 1_000_000);
    assert.ok(h.calc.views.has(1_000_000));
    assert.ok(h.calc.model.rowCount > 1_000_000);
    assert.ok(h.calc.model.rows.size < 10);
    assert.ok(h.calc.pool.length <= 21);
    assert.ok(h.calc.virtual.scale > 1);
  } finally {
    h.restore();
  }
});

test('recycled rows remain bounded and keep the active formula when scrolling elsewhere', () => {
  const h = setupCalculator();
  try {
    const input = h.type(0, '=1+');
    h.calc.model.ensure(10000);
    h.calc.render();
    for (let i = 1; i < 30; i++) {
      h.calc.scroll.scrollTop = i * 700;
      h.calc.scroll.dispatchEvent(new Event('scroll'));
      assert.equal(h.calc.views.get(0)?.input, input);
      assert.equal(h.calc.editing, 0);
      assert.ok(h.calc.pool.length <= 21);
    }
    const visible = h.calc.pool.filter((v) => !v.tr.hidden);
    assert.equal(new Set(visible.map((v) => v.index)).size, visible.length);
  } finally {
    h.restore();
  }
});

test('minimize removes the window and restores the same rows from the status button', async () => {
  const h = setupCalculator();
  try {
    h.type(0, '=1+2');
    h.calc.minimize();
    await tick();
    assert.equal(h.calc.host.hidden, true);
    assert.equal(document.getElementById('calculatorDock')?.hidden, false);
    assert.equal(document.getElementById('calculatorHomeDock')?.hidden, false);
    document.getElementById('calculatorDock')?.click();
    assert.equal(h.calc.host.hidden, false);
    assert.equal(document.getElementById('calculatorDock')?.hidden, true);
    assert.equal(h.calc.model.get(0).result?.display, '3');
    assert.equal(h.calc.host.classList.contains('is-minimized'), false);
  } finally {
    h.restore();
  }
});

test('completion uses the shared function catalog and Enter closes missing parentheses', async () => {
  const h = setupCalculator();
  try {
    const input = h.type(0, '=su');
    await tick();
    assert.equal(h.calc.popup.hidden, false);
    assert.ok(h.calc.suggestions.some((f) => f.name === 'SUM'));
    h.calc.suggestion = h.calc.suggestions.findIndex((f) => f.name === 'SUM');
    h.key(input, 'Tab');
    await tick();
    assert.equal(h.calc.model.get(0).text, '=SUM(');
    assert.equal(h.calc.model.active, 0);
    h.calc.captureRange('A2', 'A3');
    h.key(input, 'Enter');
    await tick();
    assert.equal(h.calc.model.get(0).text, '=SUM(A2:A3)');
    assert.equal(h.calc.model.active, 1);
    assert.equal(h.calc.model.get(0).result?.display, '3');
  } finally {
    h.restore();
  }
});

test('completion does not rewrite quoted text, and boolean/text results render as values', async () => {
  const h = setupCalculator(async () => ({
    kind: 'boolean',
    display: 'TRUE',
    copy: 'TRUE',
    exact: 'TRUE',
  }));
  try {
    h.type(0, '="SUM("');
    await tick();
    assert.equal(h.calc.popup.hidden, true);
    assert.equal(FormulaInput.close('="("'), '="("');
    h.type(0, '=1=1');
    await h.calc.commit(0);
    assert.equal(h.calc.views.get(0)?.value.textContent, 'TRUE');
    assert.equal(h.calc.views.get(0)?.value.classList.contains('is-boolean'), true);
  } finally {
    h.restore();
  }
});

test('dotted function names and digit suffixes share completion and signature parsing', async () => {
  const h = setupCalculator();
  try {
    await tick();
    h.calc.catalog = [
      { name: 'NORM.S.DIST', signature: 'NORM.S.DIST(数值, 累计)' },
      { name: 'LOG10', signature: 'LOG10(数值)' },
    ];
    h.type(0, '=norm.s.d');
    await tick();
    assert.equal(h.calc.suggestions[0]?.name, 'NORM.S.DIST');
    h.calc.complete('NORM.S.DIST');
    await tick();
    assert.equal(h.calc.model.get(0).text, '=NORM.S.DIST(');
    assert.equal(FormulaInput.context('=NORM.S.DIST(0,', 15)?.fn, 'NORM.S.DIST');
    assert.equal(FormulaInput.context('=LOG10(', 7)?.fn, 'LOG10');
  } finally {
    h.restore();
  }
});

test('pasting over 200 formulas keeps a bounded DOM and only four evaluations in flight', async () => {
  let running = 0;
  let maximum = 0;
  const h = setupCalculator(async () => {
    running++;
    maximum = Math.max(maximum, running);
    await tick();
    running--;
    return calcResult('3');
  });
  try {
    const input = h.type(0, '');
    const pasteEv = Object.assign(new Event('paste', { cancelable: true, bubbles: true }), {
      clipboardData: { getData: () => Array.from({ length: 1000 }, () => '=1+2').join('\n') },
    });
    Object.defineProperty(pasteEv, 'target', { value: input, configurable: true });
    h.calc.paste(pasteEv);
    const promises = [...h.calc.jobs.values()].map((job) => job.promise);
    await Promise.all(promises);
    assert.equal(h.calc.model.get(999).result?.display, '3');
    assert.equal(h.calc.model.active, 1000);
    assert.equal(maximum, 4);
    assert.ok(h.calc.pool.length <= 21);
    assert.equal(h.calc.jobs.size, 0);
  } finally {
    h.restore();
  }
});

test('the optional notes column keeps annotations when hidden and does not change formula results', async () => {
  let calls = 0;
  const h = setupCalculator(async () => {
    calls++;
    return calcResult('3');
  });
  try {
    assert.equal(h.calc.notes, false);
    h.type(0, '=1+2');
    await h.calc.commit(0);
    const revision = h.calc.model.get(0).revision;
    document.getElementById('calculatorNotes')?.click();
    assert.equal(h.calc.notes, true);
    assert.equal(h.calc.host.classList.contains('has-notes'), true);
    const note = h.calc.views.get(0)?.note;
    assert.ok(note);
    note.focus();
    note.value = '待复核';
    note.dispatchEvent(new Event('input', { bubbles: true }));
    assert.equal(h.calc.model.get(0).note, '待复核');
    assert.equal(h.calc.model.get(0).revision, revision);
    assert.equal(h.calc.model.get(0).result?.display, '3');
    assert.equal(h.calc.picking, false);
    h.calc.toggleNotes();
    assert.equal(h.calc.notes, false);
    assert.equal(h.calc.model.get(0).note, '待复核');
    assert.equal(h.calc.picking, false);
    h.calc.toggleNotes();
    assert.equal(h.calc.views.get(0)?.note.value, '待复核');
    h.calc.minimize();
    h.calc.open();
    assert.equal(h.calc.model.get(0).note, '待复核');
    assert.equal(h.calc.model.get(0).result?.display, '3');
    assert.equal(calls, 1);
  } finally {
    h.restore();
  }
});

test('notes can mark otherwise empty rows and remain attached through virtual scrolling', () => {
  const h = setupCalculator();
  try {
    h.calc.toggleNotes();
    h.calc.focus(1000, 'note');
    const note = h.calc.views.get(1000)?.note;
    assert.ok(note);
    note.value = '抽查标记';
    note.dispatchEvent(new Event('input', { bubbles: true }));
    assert.equal(h.calc.model.get(1000).note, '抽查标记');
    assert.ok(h.calc.model.rowCount > 1000);
    h.calc.focus(0);
    h.calc.focus(1000, 'note');
    assert.equal(h.calc.views.get(1000)?.note.value, '抽查标记');
    assert.ok(h.calc.pool.length <= 21);
  } finally {
    h.restore();
  }
});

test('notes round-trip restores position and respects a deliberate move while expanded', () => {
  const h = setupCalculator();
  try {
    const initial = h.calc.host.getBoundingClientRect();
    for (let i = 0; i < 5; i++) {
      h.calc.toggleNotes();
      const expanded = h.calc.host.getBoundingClientRect();
      assert.equal(expanded.left + expanded.width, initial.left + initial.width);
      h.calc.toggleNotes();
      const restored = h.calc.host.getBoundingClientRect();
      assert.equal(restored.left, initial.left);
      assert.equal(restored.width, initial.width);
    }
    h.calc.toggleNotes();
    h.calc.host.style.left = `${parseFloat(h.calc.host.style.left) - 40}px`;
    h.calc.toggleNotes();
    assert.equal(h.calc.host.getBoundingClientRect().left, initial.left - 40);
  } finally {
    h.restore();
  }
});

test('an obsolete close animation cannot hide a reopened calculation window', async () => {
  const h = setupCalculator();
  try {
    let finish: (() => void) | undefined;
    Reflect.set(window, 'matchMedia', () => ({ matches: false }));
    Reflect.set(globalThis, 'getComputedStyle', () => ({
      transform: 'none',
      getPropertyValue: () => '',
    }));
    Reflect.set(h.calc.host, 'animate', () => ({
      cancel(): void {
        // Leave pending promise unsettled until explicitly resolved below.
      },
      finished: new Promise<void>((resolve) => {
        finish = resolve;
      }),
    }));

    h.calc.minimize();
    assert.equal(h.calc.host.hidden, false);
    const firstFinish = finish;
    h.calc.open();
    firstFinish?.();
    await tick();
    assert.equal(h.calc.host.hidden, false);

    h.calc.hide();
    const secondFinish = finish;
    h.calc.toggle();
    secondFinish?.();
    await tick();
    assert.equal(h.calc.host.hidden, false);
  } finally {
    h.restore();
  }
});

test('a calculation exit does not steal focus from a newly opened control', async () => {
  const h = setupCalculator();
  try {
    let finish: (() => void) | undefined;
    Reflect.set(window, 'matchMedia', () => ({ matches: false }));
    Reflect.set(globalThis, 'getComputedStyle', () => ({
      transform: 'none',
      getPropertyValue: () => '',
    }));
    Reflect.set(h.calc.host, 'animate', () => ({
      cancel(): void {
        // No-op cancel for controlled resolution.
      },
      finished: new Promise<void>((resolve) => {
        finish = resolve;
      }),
    }));

    const nameBox = document.getElementById('nameBox');
    const grid = document.getElementById('grid');
    assert.ok(nameBox && grid);

    h.calc.minimize();
    nameBox.focus();
    finish?.();
    await tick();
    assert.equal(h.calc.host.hidden, true);
    assert.equal(document.activeElement, nameBox);

    h.calc.open();
    h.calc.host.focus();
    h.calc.hide();
    finish?.();
    await tick();
    assert.equal(h.calc.host.hidden, true);
    assert.equal(document.activeElement, grid);
  } finally {
    h.restore();
  }
});

test('the complete application boots with the formula table and one keyboard dispatcher', () => {
  const h = setupCalculator();
  try {
    const deps = makeDefaultGridInteractionsDeps({ S: h.state });
    const interactions = new GridInteractions({
      ...deps,
      panels: { ...deps.panels, calculator: h.calc },
    });
    let keydownCount = 0;
    const origAdd = document.addEventListener.bind(document);
    Reflect.set(
      document,
      'addEventListener',
      (
        type: string,
        listener: EventListenerOrEventListenerObject,
        options?: boolean | AddEventListenerOptions,
      ) => {
        if (type === 'keydown') keydownCount++;
        origAdd(type, listener, options);
      },
    );
    interactions.bindEvents();
    assert.equal(keydownCount, 1);

    const grid = document.getElementById('grid');
    assert.ok(grid);
    const bodyCell = document.createElement('div');
    bodyCell.className = 'grid-cell';
    bodyCell.dataset.ref = 'A2';
    bodyCell.dataset.visualIndex = '2';
    grid.appendChild(bodyCell);

    const input = h.type(0, '=');
    bodyCell.dispatchEvent(new Event('click', { bubbles: true }));
    assert.equal(h.calc.model.get(0).text, '=A2');
    assert.equal(document.activeElement, input);

    h.calc.editing = null;
    bodyCell.dispatchEvent(new Event('click', { bubbles: true }));
    assert.equal(document.activeElement, grid);
  } finally {
    h.restore();
  }
});
