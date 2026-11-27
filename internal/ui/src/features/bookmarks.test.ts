import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { ApiRequestOptions } from '../contracts/common.ts';
import type { Bookmark } from '../contracts/features.ts';
import { createInitialWorkbookState } from '../grid/state.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import { Bookmarks } from './bookmarks.ts';

function deferred<T = unknown>() {
  let resolve: (value: T | PromiseLike<T>) => void = () => undefined;
  let reject: (reason?: unknown) => void = () => undefined;
  const promise = new Promise<T>((a, b) => {
    resolve = a;
    reject = b;
  });
  return { promise, resolve, reject };
}

const tick = (): Promise<void> =>
  new Promise((resolve) => {
    setImmediate(resolve);
  });

function setupBookmarks(
  apiImpl: (url: string, options?: ApiRequestOptions) => Promise<unknown>,
) {
  const dom = installDom({ fromIndexHtml: true });
  const state = createInitialWorkbookState(1_048_576, 16_384);
  state.opened = true;
  state.fileName = 'same.xlsx';
  state.workbookKey = 'A';
  state.sheet = 'Data';
  state.selected = {
    sheet: 'Data',
    row: 1,
    col: 1,
    ref: 'A1',
    raw: '42',
    display: '42',
    hasFormula: false,
  };
  const notices: string[] = [];
  let confirmImpl: () => Promise<boolean> = async () => true;

  const marks = new Bookmarks({
    state,
    api: async <T = unknown>(url: string, options?: ApiRequestOptions): Promise<T> =>
      (await apiImpl(url, options)) as T,
    toast: (message) => notices.push(message),
    openDialog: () => undefined,
    navigateToCell: async () => true,
    onChange: () => undefined,
    confirm: () => confirmImpl(),
  });
  marks.render = () => undefined;

  return {
    marks,
    state,
    notices,
    setConfirm: (fn: () => Promise<boolean>) => {
      confirmImpl = fn;
    },
    restore: () => dom.restore(),
  };
}

test('late loads cannot install marks from another workbook even when filenames match and abort is ignored', async () => {
  const a = deferred();
  const b = deferred();
  let calls = 0;
  const h = setupBookmarks(() => (++calls === 1 ? a.promise : b.promise));
  try {
    const first = h.marks.load();
    h.state.workbookKey = 'B';
    h.marks.resetBook();
    const second = h.marks.load();
    b.resolve({ items: [{ id: 'B', sheet: 'Data', ref: 'B1' }] });
    await second;
    a.resolve({ items: [{ id: 'A', sheet: 'Data', ref: 'A1' }] });
    await first;
    assert.equal(h.marks.items[0]?.id, 'B');
    assert.equal(h.marks.loadedBook, 'B');
    assert.equal(h.marks.has('Data', 'A1'), false);
    assert.equal(h.notices.length, 0);
  } finally {
    h.restore();
  }
});

test('loading marks on opening the panel preserves a note being typed while updating its saved ID', async () => {
  const pending = deferred();
  const h = setupBookmarks(() => pending.promise);
  try {
    h.marks.open();
    const note = document.getElementById('bookmarkNote') as HTMLTextAreaElement;
    note.value = 'new draft';
    pending.resolve({
      items: [{ id: 'saved-id', sheet: 'Data', ref: 'A1', note: 'old note', tag: '异常' }],
    });
    await tick();
    assert.equal(note.value, 'new draft');
    assert.equal(note.dataset.id, 'saved-id');
  } finally {
    h.restore();
  }
});

test('a save completing after a workbook change cannot paint marks, claim success or disable the new form', async () => {
  const pending = deferred();
  const h = setupBookmarks(() => pending.promise);
  try {
    h.marks.prepareCurrent();
    (document.getElementById('bookmarkNote') as HTMLTextAreaElement).value = 'A note';
    const save = h.marks.save();
    h.state.workbookKey = 'B';
    h.marks.resetBook();
    h.marks.prepareCurrent();
    pending.resolve({ items: [{ id: 'A', sheet: 'Data', ref: 'A1' }] });
    await save;
    assert.equal(h.marks.items.length, 0);
    assert.equal((document.getElementById('bookmarkSaveBtn') as HTMLButtonElement).disabled, false);
    assert.equal(h.notices.length, 0);
  } finally {
    h.restore();
  }
});

test('typing a newer draft while saving retains the draft and installs the saved mark ID', async () => {
  const pending = deferred();
  const h = setupBookmarks(() => pending.promise);
  try {
    h.marks.prepareCurrent();
    const note = document.getElementById('bookmarkNote') as HTMLTextAreaElement;
    note.value = 'first draft';
    const save = h.marks.save();
    note.value = 'second draft';
    pending.resolve({
      items: [{ id: 'saved', sheet: 'Data', ref: 'A1', note: 'first draft', tag: '待复核' }],
    });
    await save;
    assert.equal(note.value, 'second draft');
    assert.equal(note.dataset.id, 'saved');
    assert.match(h.notices.at(-1) ?? '', /未保存/);
  } finally {
    h.restore();
  }
});

test('confirming an old delete after loading another workbook sends no deletion', async () => {
  const decision = deferred<boolean>();
  const deleted: string[] = [];
  const h = setupBookmarks(async (_url, options) => {
    if (options?.method === 'POST' && typeof options.body === 'string') {
      const body = JSON.parse(options.body) as { id?: string };
      if (body.id) deleted.push(body.id);
    }
    return { items: [{ id: 'same-id', sheet: 'Data', ref: 'A1' }] };
  });
  try {
    await h.marks.load();
    h.setConfirm(() => decision.promise);
    const firstItem = h.marks.items[0];
    assert.ok(firstItem);
    const pending = h.marks.remove(firstItem);
    h.state.workbookKey = 'B';
    h.marks.resetBook();
    await h.marks.load();
    decision.resolve(true);
    await pending;
    assert.deepEqual(deleted, []);
  } finally {
    h.restore();
  }
});

test('deleting a current bookmark submits only after explicit confirmation', async () => {
  const deleted: string[] = [];
  const mark: Bookmark = {
    id: 'current',
    sheet: 'Data',
    ref: 'A1',
    note: '',
    tag: '',
    value: '',
    created: '',
    updated: '',
  };
  const h = setupBookmarks(async (_url, options) => {
    if (options?.method === 'POST' && typeof options.body === 'string') {
      const body = JSON.parse(options.body) as { id?: string };
      if (body.id) deleted.push(body.id);
    }
    return { items: deleted.length ? [] : [mark] };
  });
  try {
    await h.marks.load();
    h.setConfirm(async () => false);
    await h.marks.remove(mark);
    assert.deepEqual(deleted, []);
    h.setConfirm(async () => true);
    await h.marks.remove(mark);
    assert.deepEqual(deleted, ['current']);
  } finally {
    h.restore();
  }
});
