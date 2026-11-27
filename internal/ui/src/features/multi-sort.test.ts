import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { SourceSortKey } from '../contracts/workbook.ts';
import { createInitialWorkbookState } from '../grid/state.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import { MultiSort } from './multi-sort.ts';

function setupMultiSort() {
  const dom = installDom({ fromIndexHtml: true });
  const state = createInitialWorkbookState(1_048_576, 16_384);
  state.sheet = 'Data';
  state.indexReady = true;
  state.selected = {
    sheet: 'Data',
    row: 1,
    col: 1,
    ref: 'A1',
    raw: '',
    display: '',
    hasFormula: false,
  };
  state.usedMaxRow = 100;
  state.usedMaxCol = 20;
  const submitted: { readonly keys: SourceSortKey[]; readonly header: number }[] = [];
  const instance = new MultiSort({
    state,
    parseColumn: (v) => Number(v) || String(v).toUpperCase().charCodeAt(0) - 64,
    colName: (c) => String.fromCharCode(c + 64),
    toast: () => undefined,
    openDialog: () => undefined,
    submit: async (keys, header) => {
      submitted.push({ keys, header });
    },
    clear: async () => undefined,
  });
  return { state, instance, submitted, restore: () => dom.restore() };
}

test('numeric sort mode survives dialog reopen and reaches the server request', async () => {
  const h = setupMultiSort();
  try {
    h.state.sortKeysBySheet.set('Data', [{ column: 2, mode: 'number', descending: true }]);
    h.instance.show();
    const levels = document.getElementById('sortLevels');
    const row = levels?.children[0];
    assert.ok(row);
    const modeSelect = row.querySelector<HTMLSelectElement>('.sort-mode');
    const colInput = row.querySelector<HTMLInputElement>('.sort-column');
    assert.ok(modeSelect && colInput);
    assert.equal(modeSelect.value, 'number');
    colInput.value = '3';
    await h.instance.apply();
    assert.deepEqual(h.submitted, [{ keys: [{ column: 3, mode: 'number', descending: true }], header: 1 }]);
    h.instance.show();
    const reopenedMode = levels.children[0]?.querySelector('.sort-mode') as HTMLSelectElement | null;
    assert.equal(reopenedMode?.value, 'number');
  } finally {
    h.restore();
  }
});

test('source-type sorting stays the default and each level chooses its own mode', () => {
  const h = setupMultiSort();
  try {
    h.instance.show();
    h.instance.add({ column: 2, mode: 'text' });
    assert.equal(h.instance.values().keys[0]?.mode, '');
    assert.equal(h.instance.values().keys[1]?.mode, 'text');
    const secondCol = document
      .getElementById('sortLevels')
      ?.children[1]?.querySelector('.sort-column') as HTMLInputElement | null;
    assert.ok(secondCol);
    secondCol.value = 'A';
    assert.throws(() => h.instance.values(), /重复/);
  } finally {
    h.restore();
  }
});

test('natural text sorting is explicit and remains selected when reopening', async () => {
  const h = setupMultiSort();
  try {
    h.state.largeData = true;
    h.instance.show();
    const levels = document.getElementById('sortLevels');
    const mode = levels?.children[0]?.querySelector('.sort-mode') as HTMLSelectElement | null;
    assert.ok(mode);
    assert.ok([...mode.children].some((option) => (option as HTMLOptionElement).value === 'natural'));
    mode.value = 'natural';
    await h.instance.apply();
    assert.equal(h.submitted[0]?.keys[0]?.mode, 'natural');
    h.instance.show();
    const reopenedMode = levels?.children[0]?.querySelector('.sort-mode') as HTMLSelectElement | null;
    assert.equal(reopenedMode?.value, 'natural');
  } finally {
    h.restore();
  }
});
