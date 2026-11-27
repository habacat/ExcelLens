import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { ClipboardPayload } from '../contracts/workbook.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import { configureClipboard, copyLimits, type PasteCellChange } from './clipboard.ts';
import { CopySpecialController, PasteSpecialController } from './clipboard-panels.ts';

function deferred<T>() {
  let resolve: (value: T) => void = () => undefined;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

const makePayload = (value: string): ClipboardPayload => ({
  startCol: 1,
  endCol: 1,
  rows: [
    {
      row: 1,
      cells: [{ ref: 'A1', col: 1, raw: value, display: value, hasFormula: false, type: 'inlineStr' }],
    },
  ],
});

function setup() {
  const dom = installDom({ fromIndexHtml: true });
  configureClipboard({
    rows: 10,
    columns: 5,
    cells: 50,
    textBytes: 10000,
    htmlBytes: 20000,
    tsvRows: 2000,
    tsvColumns: 200,
    tsvCells: 50000,
  });
  const setSelect = (id: string, val: string): void => {
    const el = document.getElementById(id);
    if (el instanceof HTMLSelectElement) el.value = val;
  };
  setSelect('copySpecialFormat', 'tsv');
  setSelect('copySpecialMode', 'raw');
  setSelect('pasteSpecialFormat', 'tsv');
  setSelect('pasteSpecialMode', 'raw');

  let readTextImpl = async (): Promise<string> => '';
  const savedClipboard = Object.getOwnPropertyDescriptor(globalThis.navigator, 'clipboard');
  Object.defineProperty(globalThis.navigator, 'clipboard', {
    value: { readText: () => readTextImpl() },
    configurable: true,
    writable: true,
  });

  return {
    setReadText: (fn: () => Promise<string>) => {
      readTextImpl = fn;
    },
    restore(): void {
      if (savedClipboard) {
        Object.defineProperty(globalThis.navigator, 'clipboard', savedClipboard);
      }
      dom.restore();
    },
  };
}

test('cancelled copy reads cannot overwrite a newer dialog, and a changed source cannot write the clipboard', async () => {
  const h = setup();
  try {
    const first = deferred<ClipboardPayload>();
    const second = deferred<ClipboardPayload>();
    const writes: string[] = [];
    let number = 0;
    let current = true;
    const ui = new CopySpecialController({
      capture: () => {
        const next = number++ ? second : first;
        return {
          label: '原表',
          headerRow: 1,
          maxRow: 100,
          styled: false,
          sheet: '原表',
          current: () => current,
          read: () => next.promise,
        };
      },
      copy: async (content) => {
        writes.push(content.text);
        return true;
      },
      toast: () => undefined,
      limits: copyLimits(),
    });
    ui.open();
    document.getElementById('copySpecialClose')?.click();
    ui.open();
    document.getElementById('copySpecialDialog')?.dispatchEvent(new Event('close'));
    second.resolve(makePayload('新的001'));
    await new Promise((r) => setImmediate(r));
    first.resolve(makePayload('旧的999'));
    await new Promise((r) => setImmediate(r));
    const preview = document.getElementById('copySpecialPreview');
    assert.ok(preview instanceof HTMLTextAreaElement);
    assert.equal(preview.value, '新的001');
    current = false;
    await ui.write();
    assert.deepEqual(writes, []);
    assert.ok(document.getElementById('copySpecialStatus')?.textContent.includes('变化'));
  } finally {
    h.restore();
  }
});

test('a late clipboard write cannot close a newly reopened copy dialog', async () => {
  const h = setup();
  try {
    const write = deferred<boolean>();
    const ui = new CopySpecialController({
      capture: () => ({
        label: '原表',
        headerRow: 1,
        maxRow: 100,
        styled: false,
        sheet: '原表',
        current: () => true,
        read: async () => makePayload('001'),
      }),
      copy: () => write.promise,
      toast: () => undefined,
      limits: copyLimits(),
    });
    ui.open();
    await new Promise((r) => setImmediate(r));
    const pending = ui.write();
    document.getElementById('copySpecialClose')?.click();
    ui.open();
    await new Promise((r) => setImmediate(r));
    write.resolve(true);
    await pending;
    const dialog = document.getElementById('copySpecialDialog');
    assert.ok(dialog instanceof HTMLDialogElement);
    assert.equal(dialog.open, true);
    const preview = document.getElementById('copySpecialPreview');
    assert.ok(preview instanceof HTMLTextAreaElement);
    assert.equal(preview.value, '001');
  } finally {
    h.restore();
  }
});

test('late clipboard reads do not erase text typed after the read request', async () => {
  const h = setup();
  try {
    const read = deferred<string>();
    h.setReadText(() => read.promise);
    const ui = new PasteSpecialController({
      capture: () => ({ id: 's1', label: '目标A1', row: 0, col: 0, columns: 3, current: () => true }),
      commit: async () => undefined,
      refresh: async () => undefined,
      toast: () => undefined,
    });
    ui.open();
    const pending = ui.readClipboard();
    const textArea = document.getElementById('pasteSpecialText');
    assert.ok(textArea instanceof HTMLTextAreaElement);
    textArea.value = '自己输入001';
    textArea.dispatchEvent(new Event('input'));
    read.resolve('迟到的剪贴板');
    await pending;
    assert.equal(textArea.value, '自己输入001');
    document.getElementById('pasteSpecialClose')?.click();
  } finally {
    h.restore();
  }
});

test('paste preview is read-only, a changed target refuses the whole transaction, and a save failure retains input', async () => {
  const h = setup();
  try {
    const commits: (readonly PasteCellChange[])[] = [];
    let current = true;
    let fail = true;
    const ui = new PasteSpecialController({
      capture: () => ({ id: 's1', label: '目标A1', row: 0, col: 0, columns: 3, current: () => current }),
      commit: async (_context, changes) => {
        commits.push(changes);
        if (fail) throw new Error('磁盘未确认');
      },
      refresh: async () => undefined,
      toast: () => undefined,
    });
    ui.open();
    const textArea = document.getElementById('pasteSpecialText');
    assert.ok(textArea instanceof HTMLTextAreaElement);
    textArea.value = '001\t=literal';
    ui.preview();
    assert.equal(commits.length, 0);
    current = false;
    await ui.apply();
    assert.equal(commits.length, 0);
    current = true;
    ui.preview();
    await ui.apply();
    assert.equal(commits.length, 1);
    assert.equal(ui.dialog.open, true);
    assert.equal(textArea.value, '001\t=literal');
    assert.ok(document.getElementById('pasteSpecialStatus')?.textContent.includes('未确认'));
    fail = false;
    ui.preview();
    await ui.apply();
    assert.equal(commits.length, 2);
    assert.equal(ui.dialog.open, false);
    assert.equal(commits[1]?.[0]?.value, '001');
  } finally {
    h.restore();
  }
});
