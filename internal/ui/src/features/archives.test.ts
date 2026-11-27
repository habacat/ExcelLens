import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { ApiRequestOptions } from '../contracts/common.ts';
import type { WorkbookStatusResponse } from '../contracts/workbook.ts';
import { createTransport } from '../core/transport.ts';
import { renderSourceIntegrity } from '../grid/grid-workbook-io.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import { Archives } from './archives.ts';

const openedMember = {
  opened: true, workbookKey: 'content', book: 'member', fileName: 'a.txt', fileSize: 2,
  sheetCount: 1, activeTab: 0, sheets: [{ name: '数据' }], source: { format: 'txt', readOnly: true },
};

function setupArchives(
  apiImpl: (url: string, options?: ApiRequestOptions) => Promise<unknown>,
) {
  const dom = installDom({ fromIndexHtml: true });
  const opened: WorkbookStatusResponse[] = [];
  const instance = new Archives({
    api: async <T = unknown>(url: string, options?: ApiRequestOptions): Promise<T> =>
      (await apiImpl(url, options)) as T,
    open: async (data) => {
      opened.push(data);
    },
    fmtBytes: (n) => String(n),
  });
  return { instance, opened, restore: () => dom.restore() };
}

test('archive rendering stays bounded and filenames remain literal text', async () => {
  const h = setupArchives(async () => ({
    items: Array.from({ length: 100 }, (_, id) => ({
      id,
      name: `folder/<img onerror=bad>${id}.xlsx`,
      size: 123,
    })),
    total: 100000,
  }));
  try {
    h.instance.use({ id: 'archive-one', name: '账簿.zip', total: 100000 });
    await h.instance.load();
    const list = document.getElementById('archiveList');
    assert.ok(list);
    assert.equal(list.children.length, 100);
    const first = list.children[0];
    assert.equal(first?.children[1]?.children[0]?.textContent, '<img onerror=bad>0.xlsx');
    assert.equal((document.getElementById('archiveNext') as HTMLButtonElement).disabled, false);
    h.instance.select(33);
    await h.instance.load();
    assert.equal(h.instance.selected, 33);
  } finally {
    h.restore();
  }
});

test('search keyboard connects directly to member selection and opening', async () => {
  const h = setupArchives(async (url) =>
    url.startsWith('/api/archive/list')
      ? {
          items: [
            { id: 5, name: 'a.txt', size: 2 },
            { id: 8, name: 'b.txt', size: 2 },
          ],
          total: 2,
        }
      : url === '/api/archive/open'
        ? { jobId: 'job' }
        : { status: 'done', result: openedMember },
  );
  try {
    h.instance.use({ id: 'one', name: 'archive.zip', total: 2 });
    await h.instance.load();
    const search = document.getElementById('archiveSearch');
    assert.ok(search);
    const down = Object.assign(new Event('keydown', { cancelable: true }), { key: 'ArrowDown' });
    search.dispatchEvent(down);
    assert.equal(document.activeElement, document.getElementById('archiveList'));
    const listDown = Object.assign(new Event('keydown', { cancelable: true }), {
      key: 'ArrowDown',
    });
    document.getElementById('archiveList')?.dispatchEvent(listDown);
    assert.equal(h.instance.selected, 8);
    await h.instance.openSelected();
    assert.equal(h.opened.length, 1);
  } finally {
    h.restore();
  }
});

test('an extracting archive can report null until ready and cannot open a member without its source identity', async t => {
  const dom = installDom({ fromIndexHtml: true });
  t.after(() => dom.restore());
  const archive = { id: 'zip', name: 'data.zip', count: 1, total: 1, depth: 0, format: 'ZIP', notice: '' };
  let polls = 0;
  let result: unknown = { ...openedMember, book: '' };
  t.mock.method(globalThis, 'fetch', async (url: string) => {
    if (url.startsWith('/api/archive/list')) {
      return Response.json({ archive, items: [{ id: 0, name: 'a.txt', size: 2 }], total: 1, offset: 0, limit: 100 });
    }
    if (url === '/api/archive/open') return Response.json({ jobId: 'extract' });
    assert.match(url, /archive\/job/);
    const running = ++polls === 1;
    return Response.json({ id: 'extract', status: running ? 'running' : 'done', stage: '', doneBytes: 0,
      totalBytes: 2, error: '', result: running ? null : result });
  });
  const opened: WorkbookStatusResponse[] = [];
  const instance = new Archives({ api: createTransport({ token: 'test' }).api,
    open: value => { opened.push(value); }, fmtBytes: String });
  instance.use(archive);
  await instance.load();
  await instance.openSelected();
  assert.equal(polls, 2);
  assert.equal(opened.length, 0);
  assert.match(document.getElementById('archiveError')?.textContent ?? '', /来源/);
  result = openedMember;
  await instance.openSelected();
  assert.equal(opened[0]?.book, 'member');
});

test('an obsolete archive opener cannot replace the next archive or its error state', async () => {
  let release: ((value: unknown) => void) | undefined;
  const pending = new Promise((resolve) => {
    release = resolve;
  });
  let calls = 0;
  const h = setupArchives(async (url, options) => {
    if (url === '/api/archive/open') return { jobId: 'old-job' };
    if (options?.method === 'POST') return { status: 'cancelled' };
    if (url.startsWith('/api/archive/job')) {
      calls++;
      return pending;
    }
    return { items: [{ id: 0, name: 'new.txt', size: 1 }], total: 1 };
  });
  try {
    h.instance.use({ id: 'old', name: 'old.zip', total: 1 });
    h.instance.selected = 0;
    const opening = h.instance.openSelected();
    await new Promise<void>((resolve) => {
      setImmediate(resolve);
    });
    assert.equal(calls, 1);
    h.instance.use({ id: 'new', name: 'new.zip', total: 1 });
    await h.instance.load();
    release?.({ status: 'done', result: { opened: true, fileName: 'stale.txt' } });
    await opening;
    assert.equal(h.instance.archive?.id, 'new');
    assert.equal(h.instance.items[0]?.name, 'new.txt');
    assert.equal(h.opened.length, 0);
    assert.equal(document.getElementById('archiveError')?.hidden, true);
    assert.equal(h.instance.busy, false);
  } finally {
    h.restore();
  }
});

test('late directory results cannot overwrite a newer search', async () => {
  let release: ((value: unknown) => void) | undefined;
  const first = new Promise((resolve) => {
    release = resolve;
  });
  let calls = 0;
  const h = setupArchives(async () =>
    ++calls === 1 ? first : { items: [{ id: 2, name: 'latest.csv', size: 1 }], total: 1 },
  );
  try {
    h.instance.use({ id: 'same', name: 'source.zip', total: 1 });
    const pending = h.instance.load();
    await h.instance.load();
    release?.({ items: [{ id: 1, name: 'stale.csv', size: 1 }], total: 1 });
    await pending;
    assert.equal(h.instance.items[0]?.name, 'latest.csv');
  } finally {
    h.restore();
  }
});

test('incomplete TXT needs a deliberate choice for clicks and keyboard, and resets on another member', async () => {
  let opens = 0;
  let lastBody: { allowPartial?: boolean } | undefined;
  const notice = 'TAR 不完整 <img onerror=bad>，仅存 123 / 456 字节';
  const h = setupArchives(async (url, options) => {
    if (url.startsWith('/api/archive/list')) {
      return {
        archive: { id: 'cut', name: 'cut.tar', notice },
        items: [
          { id: 0, name: 'ledger.txt', size: 123, declaredSize: 456, incomplete: true },
          { id: 1, name: 'other.txt', size: 3 },
        ],
        total: 2,
      };
    }
    if (url === '/api/archive/open') {
      opens++;
      lastBody =
        typeof options?.body === 'string'
          ? (JSON.parse(options.body) as { allowPartial?: boolean })
          : undefined;
      return { jobId: 'partial' };
    }
    return { status: 'done', result: openedMember };
  });
  try {
    h.instance.use({ id: 'cut', name: 'cut.tar', total: 2, notice });
    await h.instance.load();
    assert.equal(document.getElementById('archiveNotice')?.textContent, notice);
    assert.equal(document.getElementById('archiveNotice')?.hidden, false);
    assert.equal((document.getElementById('archiveOpen') as HTMLButtonElement).disabled, true);
    assert.equal(document.getElementById('archivePartialChoice')?.hidden, false);
    await h.instance.openSelected();
    assert.equal(opens, 0);
    const enter = Object.assign(new Event('keydown', { cancelable: true }), { key: 'Enter' });
    document.getElementById('archiveList')?.dispatchEvent(enter);
    assert.equal(opens, 0);
    const allowPartial = document.getElementById('archiveAllowPartial') as HTMLInputElement;
    allowPartial.checked = true;
    allowPartial.dispatchEvent(new Event('change'));
    assert.equal((document.getElementById('archiveOpen') as HTMLButtonElement).disabled, false);
    await h.instance.openSelected();
    assert.equal(opens, 1);
    assert.equal(lastBody?.allowPartial, true);
    h.instance.select(1);
    assert.equal(allowPartial.checked, false);
    assert.equal(document.getElementById('archivePartialChoice')?.hidden, true);
    h.instance.select(0);
    assert.equal((document.getElementById('archiveOpen') as HTMLButtonElement).disabled, true);
    h.instance.use({ id: 'full', name: 'full.zip', total: 1 });
    assert.equal(document.getElementById('archiveNotice')?.hidden, true);
    assert.equal(allowPartial.checked, false);
  } finally {
    h.restore();
  }
});

test('data integrity notice remains visible on reload and clears for a complete source', () => {
  const dom = installDom({ fromIndexHtml: true });
  try {
    const notice = document.getElementById('sourceIntegrityNotice');
    const message = document.getElementById('sourceIntegrityMessage');
    assert.ok(notice && message);
    renderSourceIntegrity({
      format: 'txt',
      incomplete: true,
      archiveNotice: '原始压缩包不完整，末条记录未纳入表格',
    });
    assert.equal(notice.hidden, false);
    assert.equal(message.textContent, '原始压缩包不完整，末条记录未纳入表格');

    renderSourceIntegrity({ format: 'xlsx' });
    assert.equal(notice.hidden, true);
    assert.equal(message.textContent, '');

    renderSourceIntegrity({ format: 'txt', incomplete: true, notice: '片段' });
    assert.equal(notice.hidden, false);
    assert.equal(message.textContent, '片段');
  } finally {
    dom.restore();
  }
});
