import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createFileFormats } from './formats.ts';
import { createToastController } from './overlays.ts';
import { columnName } from './cell-address.ts';
import { GridClipboard } from '../grid/grid-clipboard.ts';
import { MergedCells } from '../grid/merges.ts';
import { createInitialWorkbookState } from '../grid/state.ts';
import { SelectionInspector } from '../features/inspect.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import {
  makeNoopApi,
  makeTestFileFormatsConfig,
  makeTestFrozenPanes,
} from '../test-helpers/grid-fixture.ts';

for (const id of ['toast', 'analysisToast'] as const) {
  test(`${id}: explicit notification tones reset after errors and never infer from message text`, () => {
    const dom = installDom({ fromIndexHtml: true });
    try {
      const node = document.getElementById(id);
      assert.ok(node);
      const timers = new Map<number, () => void>();
      let sequence = 0;
      const controller = createToastController(id, {
        timers: {
          setTimeout: (fn) => {
            const handle = ++sequence;
            timers.set(handle, fn);
            return handle;
          },
          clearTimeout: (handle) => {
            timers.delete(Number(handle));
          },
        },
      });
      controller.show('网络连接失败', 'error');
      assert.equal(node.dataset.tone, 'error');
      assert.equal(node.getAttribute('role'), 'alert');
      controller.show('已复制「失败记录.txt」', 'success');
      assert.equal(node.dataset.tone, 'success');
      assert.equal(node.getAttribute('role'), 'status');
      controller.show('请先选择单元格', 'warning');
      assert.equal(node.dataset.tone, 'warning');
      controller.show('当前文件名包含错误二字');
      assert.equal(node.dataset.tone, 'info');
      assert.equal(node.getAttribute('role'), 'status');
      controller.show('<img src=x onerror=bad>', 'unknown');
      assert.equal(node.dataset.tone, 'info');
      assert.equal(node.textContent, '<img src=x onerror=bad>');
      assert.equal(timers.size, 1, 'previous messages must not dismiss the latest one');
      assert.equal(node.hidden, false);
      const pending = [...timers.values()][0];
      assert.ok(pending);
      pending();
      assert.equal(node.hidden, true);
    } finally {
      dom.restore();
    }
  });
}

test('clipboard outcomes report green only after a successful write, with failed legacy copies remaining red', async () => {
  for (const scenario of ['success', 'denied', 'legacy-failed'] as const) {
    const dom = installDom({ fromIndexHtml: true });
    const prevNavigator = Object.getOwnPropertyDescriptor(globalThis, 'navigator');
    try {
      const notices: [string, string | undefined][] = [];
      let removed = false;
      const origCreate = dom.document.createMockElement.bind(dom.document);
      dom.document.createMockElement = (tag: string) => {
        const el = origCreate(tag);
        if (tag.toLowerCase() === 'textarea') {
          const origRemove = el.remove.bind(el);
          el.remove = () => {
            removed = true;
            origRemove();
          };
        }
        return el;
      };
      Reflect.set(document, 'execCommand', () => false);
      Object.defineProperty(globalThis, 'navigator', {
        configurable: true,
        writable: true,
        value:
          scenario === 'legacy-failed'
            ? {}
            : {
                clipboard: {
                  writeText: async () => {
                    if (scenario === 'denied') throw new Error('权限不足');
                  },
                },
              },
      });
      const S = createInitialWorkbookState(1_048_576, 16_384);
      const clipboard = new GridClipboard({
        S,
        api: makeNoopApi(),
        colName: columnName,
        fileFormats: createFileFormats(makeTestFileFormatsConfig()),
        mergedCells: new MergedCells(),
        sourceRowLimit: () => 1_048_576,
        getFrozenPanes: () => makeTestFrozenPanes(S),
        toast: (msg, tone) => notices.push([msg, tone]),
      });
      assert.equal(await clipboard.copyText('测试文本'), scenario === 'success');
      assert.equal(notices.length, 1);
      assert.equal(notices[0]?.[1], scenario === 'success' ? 'success' : 'error');
      if (scenario === 'legacy-failed') {
        assert.equal(removed, true, 'failed fallback must remove its hidden textarea');
      }
    } finally {
      if (prevNavigator) Object.defineProperty(globalThis, 'navigator', prevNavigator);
      else Reflect.deleteProperty(globalThis, 'navigator');
      dom.restore();
    }
  }
});

test('qualified cell address escapes single quotes in sheet names and evidence copies immutable metadata', async () => {
  const dom = installDom({ fromIndexHtml: true });
  const prevNavigator = Object.getOwnPropertyDescriptor(globalThis, 'navigator');
  try {
    const written: string[] = [];
    const notices: [string, string | undefined][] = [];
    Object.defineProperty(globalThis, 'navigator', {
      configurable: true,
      writable: true,
      value: {
        clipboard: {
          writeText: async (text: string) => {
            written.push(text);
          },
        },
      },
    });
    const S = createInitialWorkbookState(1_048_576, 16_384);
    S.fileName = 'report.xlsx';
    S.sheet = "Q1'26 汇总";
    S.selected = {
      sheet: "Q1'26 汇总",
      row: 5,
      col: 8,
      ref: 'H5',
      display: '140.87',
      raw: '140.87',
      formula: '=SUMIFS(A:A,B:B,C1)',
      hasFormula: true,
      cacheMissing: false,
    };
    const clipboard = new GridClipboard({
      S,
      api: makeNoopApi(),
      colName: columnName,
      fileFormats: createFileFormats(makeTestFileFormatsConfig()),
      mergedCells: new MergedCells(),
      sourceRowLimit: () => 1_048_576,
      getFrozenPanes: () => makeTestFrozenPanes(S),
      toast: (msg, tone) => notices.push([msg, tone]),
    });
    assert.equal(await clipboard.copyQualifiedAddress(), true);
    assert.equal(written[0], "'Q1''26 汇总'!H5");
    assert.equal(await clipboard.copyCellEvidence(), true);
    assert.match(written[1] ?? '', /工作簿：report\.xlsx/);
    assert.match(written[1] ?? '', /公式：=SUMIFS\(A:A,B:B,C1\)/);
    assert.equal(notices.length, 2);
  } finally {
    if (prevNavigator) Object.defineProperty(globalThis, 'navigator', prevNavigator);
    else Reflect.deleteProperty(globalThis, 'navigator');
    dom.restore();
  }
});

test('a failed inspector copy cannot replace its error with a green completion message', async () => {
  const dom = installDom({ fromIndexHtml: true });
  try {
    const notices: [string, string | undefined][] = [];
    let copyResult = false;
    const S = createInitialWorkbookState(1_048_576, 16_384);
    const inspector = new SelectionInspector({
      state: S,
      api: makeNoopApi(),
      colName: columnName,
      parseColumn: () => 1,
      toast: (msg, tone) => notices.push([msg, tone]),
      navigate: async () => true,
      onChange: () => undefined,
      copy: async () => copyResult,
    });
    inspector.matches = [{ ref: 'A1', row: 1, col: 1, value: '001' }];
    inspector.sheet = '数据';
    await inspector.copyResults();
    assert.equal(notices.length, 0);
    copyResult = true;
    await inspector.copyResults();
    assert.equal(notices.length, 1);
    assert.equal(notices[0]?.[1], 'success');
  } finally {
    dom.restore();
  }
});
