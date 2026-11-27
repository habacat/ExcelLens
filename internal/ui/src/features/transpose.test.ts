import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { CellData, ClipboardPayload } from '../contracts/workbook.ts';
import type { ClipboardReadOptions, ClipboardSnapshot } from '../grid/grid-clipboard.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import { Transpose } from './transpose.ts';

function makeCell(overrides: Partial<CellData> = {}): CellData {
  return {
    ref: 'A1',
    col: 1,
    raw: '',
    display: '',
    style: 0,
    hasFormula: false,
    ...overrides,
  };
}

function setupTranspose() {
  const dom = installDom({ fromIndexHtml: true });
  let current = true;
  const calls: (ClipboardReadOptions | undefined)[] = [];
  const copied: string[] = [];
  const accepted: unknown[] = [];
  const destinations: { readonly sheet: string; readonly ref: string; readonly allowed: boolean }[] =
    [];

  let readImpl: (
    options?: ClipboardReadOptions,
    signal?: AbortSignal,
  ) => Promise<ClipboardPayload> = async (options) => {
    calls.push(options);
    return {
      book: 'book',
      workbookKey: 'hash',
      sheet: '数据',
      startCol: 1,
      endCol: 1,
      rows: [
        {
          row: 2,
          cells: [
            makeCell({
              ref: 'A2',
              col: 1,
              raw: '001',
              display: '001',
              style: 0,
              type: 's',
              hasFormula: false,
            }),
          ],
        },
      ],
    };
  };

  let navigateImpl: (sheet: string, ref: string, guard: () => boolean) => Promise<void> = async (
    sheet,
    ref,
    guard,
  ) => {
    destinations.push({ sheet, ref, allowed: guard() });
    ui.navigation++;
    assert.equal(guard(), false);
  };

  const snapshot: ClipboardSnapshot = {
    book: 'book',
    workbookKey: 'hash',
    sheet: '数据',
    rows: 1,
    firstColumn: 1,
    lastColumn: 1,
    start: 'A2',
    end: 'A2',
    maxRow: 10,
    headerRow: 1,
    label: 'A2',
    styled: false,
    styles: [],
    fontOverride: '',
    current: () => current,
    sourceCurrent: () => current,
    readTSV: async () => '',
    read: (options, signal) => readImpl(options, signal),
  };

  const ui = new Transpose({
    capture: () => snapshot,
    limits: { rows: 250, columns: 50, cells: 5000, bytes: 1 << 20, pageColumns: 16 },
    colName: (n) => String.fromCharCode(64 + n),
    fontStack: () => 'sans-serif',
    copy: async (x) => {
      copied.push(x);
      return true;
    },
    navigate: (sheet, ref, guard) => navigateImpl(sheet, ref, guard),
    toast: () => undefined,
  });

  const bodyEl = document.getElementById('transposeBody');
  assert.ok(bodyEl);
  Object.defineProperty(bodyEl, 'clientWidth', { value: 900, configurable: true });

  const origRender = ui.render.bind(ui);
  ui.render = (focus?: boolean) => {
    accepted.push(ui.data);
    origRender(focus);
  };

  ui.snapshot = snapshot;
  ui.dialog.showModal();

  return {
    ui,
    calls,
    copied,
    accepted,
    destinations,
    snapshot,
    setCurrent: (value: boolean) => {
      current = value;
    },
    setRead: (fn: typeof readImpl) => {
      readImpl = fn;
    },
    setNavigate: (fn: typeof navigateImpl) => {
      navigateImpl = fn;
    },
    restore: () => {
      ui.close();
      dom.restore();
    },
  };
}

test('one shared source read is forced even for a single cell and mode/page changes reuse accepted data', async () => {
  const h = setupTranspose();
  try {
    await h.ui.load();
    assert.equal(h.calls.length, 1);
    assert.equal(h.calls[0]?.force, true);
    assert.equal(h.accepted.length, 1);
    h.ui.mode = 'raw';
    h.ui.page(1);
    assert.equal(h.calls.length, 1);
  } finally {
    h.restore();
  }
});

test('closing or cancelling an accepted network request cannot publish its late data', async () => {
  for (const action of ['close', 'cancel', 'switch'] as const) {
    const h = setupTranspose();
    try {
      let release: ((value: ClipboardPayload) => void) | undefined;
      h.setRead(
        () =>
          new Promise<ClipboardPayload>((resolve) => {
            release = resolve;
          }),
      );
      const pending = h.ui.load();
      if (action === 'switch') h.setCurrent(false);
      else if (action === 'close') h.ui.close();
      else h.ui.cancel();
      assert.ok(release);
      release({
        book: 'book',
        workbookKey: 'hash',
        sheet: '数据',
        startCol: 1,
        endCol: 1,
        rows: [],
      });
      await pending;
      assert.equal(h.accepted.length, 0);
      assert.equal(h.ui.data, null);
    } finally {
      h.restore();
    }
  }
});

test('a failed retry preserves the previously accepted source snapshot', async () => {
  const h = setupTranspose();
  try {
    await h.ui.load();
    const old = h.ui.data;
    h.setRead(async () => {
      throw new Error('source unavailable');
    });
    await h.ui.load();
    assert.equal(h.ui.data, old);
    assert.match(document.getElementById('transposeStatus')?.textContent ?? '', /保留上次内容/);
  } finally {
    h.restore();
  }
});

test('copying never turns a missing result into an empty string', async () => {
  const h = setupTranspose();
  try {
    h.ui.mode = 'raw';
    h.ui.selectedItem = {
      row: 2,
      column: 1,
      ref: 'A2',
      style: 0,
      notice: '',
      cell: makeCell({ ref: 'A2', col: 1, cacheMissing: true, hasFormula: true, raw: '', display: '' }),
    };
    await h.ui.copySelected();
    assert.deepEqual(h.copied, []);

    h.ui.selectedItem = {
      row: 2,
      column: 1,
      ref: 'A2',
      style: 0,
      notice: '',
      cell: makeCell({ ref: 'A2', col: 1, raw: '000001', display: '000001' }),
    };
    await h.ui.copySelected();
    assert.deepEqual(h.copied, ['000001']);
  } finally {
    h.restore();
  }
});

test('source navigation keeps its original ref but is rejected after a newer user intent', async () => {
  const h = setupTranspose();
  try {
    h.ui.selectedItem = {
      row: 2,
      column: 1,
      ref: 'A2',
      style: 0,
      notice: '',
      cell: makeCell({ ref: 'A2', col: 1, raw: '001', display: '001' }),
    };
    await h.ui.locate();
    assert.deepEqual(h.destinations, [{ sheet: '数据', ref: 'A2', allowed: true }]);
  } finally {
    h.restore();
  }
});
