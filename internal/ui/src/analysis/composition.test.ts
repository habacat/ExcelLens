import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { CompositionSourceInfo } from '../contracts/analysis.ts';
import type { ApiRequestOptions } from '../contracts/common.ts';
import { createFileFormats } from '../core/formats.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import { makeTestFileFormatsConfig } from '../test-helpers/grid-fixture.ts';
import { CompositionPanel, type CompositionContext } from './composition.ts';

function deferred<T = void>() {
  let resolve: (value: T | PromiseLike<T>) => void = () => undefined;
  const promise = new Promise<T>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

const dummyInfo: CompositionSourceInfo = {
  ref: { book: 'main', fingerprint: 'content', source: 'wb:源', headerRow: 1 },
  name: '源',
  sheet: '源',
  kind: 'workbook',
  rows: 10,
  fields: [],
};

function setup() {
  const dom = installDom({ fromIndexHtml: true });
  const c: CompositionContext = {
    bookKey: 'content',
    book: 'path',
    opened: true,
    source: 'wb:源',
    keepId: '',
  };
  let apiImpl: (url: string, options?: ApiRequestOptions) => Promise<unknown> = async () => ({
    book: 'upload',
  });
  const formats = createFileFormats(makeTestFileFormatsConfig({ extensions: ['.xlsx', '.csv'] }));
  const h = new CompositionPanel({
    api: async <T = unknown>(url: string, options?: ApiRequestOptions): Promise<T> =>
      (await apiImpl(url, options)) as T,
    context: () => c,
    onUse: async () => true,
    limits: formats.composition,
    formats,
    toast: () => undefined,
  });
  h.identityKey = h.identity();
  h.dialog.showModal();
  h.render = () => {
    throw new Error('obsolete render');
  };
  h.renderSources = () => undefined;
  h.renderLibrary = () => undefined;
  h.controls = () => undefined;

  return {
    h,
    c,
    setApi: (fn: typeof apiImpl) => {
      apiImpl = fn;
    },
    restore: () => dom.restore(),
  };
}

test('a cancelled initial source-list refresh cannot populate a reopened empty configuration', async () => {
  const { h, restore } = setup();
  try {
    const gate = deferred();
    const entered = deferred();
    h.refresh = () => {
      entered.resolve();
      return gate.promise;
    };
    let adds = 0;
    h.addSource = () => {
      adds++;
    };
    const pending = h.open();
    await entered.promise;
    h.cancel();
    h.dialog.showModal();
    gate.resolve();
    await pending;
    assert.equal(adds, 0);
    assert.equal(h.inputs.length, 0);
  } finally {
    restore();
  }
});

test('a late upload-list refresh cannot replace sources selected after closing and reopening', async () => {
  const { h, restore } = setup();
  try {
    const gate = deferred();
    const entered = deferred();
    let current = true;
    h.refresh = () => {
      entered.resolve();
      return gate.promise;
    };
    h.books = [{ id: 'upload', name: '上传', sheets: [{ name: '上传表' }] }];
    h.inputs = [
      { book: 'main', source: 'wb:源', headerRow: 1, scope: null, expectedRef: null, info: dummyInfo },
      { book: 'empty', source: '', headerRow: 1, scope: null, expectedRef: null, info: null },
    ];
    h.rows = [{ name: 'A', columns: [1, null] }];

    const pending = h.upload(new File(['a,b'], 'upload.csv'), () => current);
    await entered.promise;
    current = false;
    const newInputs = [
      { book: 'new-main', source: 'wb:源', headerRow: 1, scope: null, expectedRef: null, info: dummyInfo },
      { book: 'chosen', source: '', headerRow: 1, scope: null, expectedRef: null, info: null },
    ];
    const newRows = [{ name: 'A', columns: [2, 3] }];
    h.inputs = newInputs;
    h.rows = newRows;
    gate.resolve();
    await pending;
    assert.equal(h.inputs, newInputs);
    assert.equal(h.inputs[1]?.book, 'chosen');
    assert.equal(h.rows, newRows);
    assert.deepEqual(h.rows[0]?.columns, [2, 3]);
  } finally {
    restore();
  }
});

for (const operation of ['save', 'removeSaved'] as const) {
  test(`a late ${operation} catalogue refresh cannot overwrite a newer status`, async () => {
    const { h, setApi, restore } = setup();
    try {
      const gate = deferred();
      const entered = deferred();
      let current = true;
      h.preview = {
        id: 'prepared',
        mode: 'append',
        workbookKey: 'content',
        book: 'path',
        source: 'append:1',
        rows: 1,
        previewTruncated: false,
      };
      h.savedID = 'definition';
      setApi(async () => ({ id: 'definition', revision: 3 }));
      h.refresh = () => {
        entered.resolve();
        return gate.promise;
      };
      const pending = h[operation](() => current);
      await entered.promise;
      current = false;
      const statusEl = document.getElementById('compositionStatus');
      assert.ok(statusEl);
      statusEl.textContent = '新候选读取失败';
      gate.resolve();
      await pending;
      assert.equal(statusEl.textContent, '新候选读取失败');
    } finally {
      restore();
    }
  });
}

test('a queued native close event cannot cancel an immediately reopened preparation', async () => {
  const { h, restore } = setup();
  try {
    let resets = 0;
    h.tasks.reset = () => {
      resets++;
    };
    h.dialog.close();
    h.dialog.showModal();
    const before = h.serial;
    await Promise.resolve();
    assert.equal(h.serial, before);
    assert.equal(resets, 0);
    assert.equal(h.dialog.open, true);

    h.dialog.close();
    await Promise.resolve();
    assert.equal(resets, 1);
  } finally {
    restore();
  }
});

test('reopening before the native close event cancels the pending upload and starts a fresh list request', async () => {
  const { h, restore } = setup();
  try {
    const gate = deferred();
    const entered = deferred();
    let refreshes = 0;
    h.books = [{ id: 'upload', name: '上传', sheets: [{ name: '上传表' }] }];
    h.inputs = [
      { book: 'main', source: 'wb:源', headerRow: 1, scope: null, expectedRef: null, info: dummyInfo },
      { book: 'chosen', source: '', headerRow: 1, scope: null, expectedRef: null, info: null },
    ];
    h.rows = [{ name: 'A', columns: [1, 2] }];
    h.refresh = () => {
      if (++refreshes === 1) {
        entered.resolve();
        return gate.promise;
      }
      return Promise.resolve();
    };
    const pending = h.action((current) => h.upload(new File(['a,b'], 'upload.csv'), current), false);
    await entered.promise;
    h.dialog.close();
    await h.open();
    await Promise.resolve();
    gate.resolve();
    await pending;
    assert.equal(refreshes, 2);
    assert.equal(h.inputs[1]?.book, 'chosen');
    assert.equal(h.busy, false);
    assert.equal(h.dialog.open, true);
  } finally {
    restore();
  }
});
