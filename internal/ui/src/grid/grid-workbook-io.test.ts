import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { WorkbookStatusResponse } from '../contracts/workbook.ts';
import { formatBytes } from '../core/display-format.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import {
  makeDefaultWorkbookIODeps,
  makeTestTransport,
} from '../test-helpers/grid-fixture.ts';
import { renderSourceIntegrity, WorkbookIO } from './grid-workbook-io.ts';

test('canOpenWorkbook blocks switching while opening, during blocked edit entry, or with unsaved analysis', () => {
  const dom = installDom({ fromIndexHtml: true });
  try {
    const toasts: string[] = [];
    let editActive = false;
    let editBlocked = false;
    let unsaved = false;
    const deps = makeDefaultWorkbookIODeps({
      toast: (msg) => toasts.push(msg),
      panels: {
        editCopy: {
          grid: {
            get active() {
              return editActive;
            },
            get blocked() {
              return editBlocked;
            },
            leave: () => {
              editActive = false;
            },
          },
        },
        analytics: {
          bookChanged: () => undefined,
          hasUnsaved: () => unsaved,
        },
      },
    });
    const io = new WorkbookIO(deps);

    assert.equal(io.canOpenWorkbook(), true);
    deps.S.opening = true;
    assert.equal(io.canOpenWorkbook(), false);
    deps.S.opening = false;

    editActive = true;
    editBlocked = true;
    assert.equal(io.canOpenWorkbook(), false);
    assert.match(toasts.at(-1) ?? '', /请先确认当前单元格/);
    editBlocked = false;

    unsaved = true;
    assert.equal(io.canOpenWorkbook('退出程序'), false);
    assert.match(toasts.at(-1) ?? '', /退出程序/);
    unsaved = false;

    assert.equal(io.canOpenWorkbook(), true);
  } finally {
    dom.restore();
  }
});

test('acceptOpenedWorkbook isolates archiveOnly replies and fully resets workbook state on real handoff', async () => {
  const dom = installDom({ fromIndexHtml: true });
  try {
    const events: string[] = [];
    let editActive = true;
    const deps = makeDefaultWorkbookIODeps({
      setStatus: (text) => events.push(`status:${text}`),
      showWorkspace: () => events.push('showWorkspace'),
      loadStylePalette: async () => {
        events.push('styles');
      },
      selectSheet: async (name, ref) => {
        events.push(`select:${name}:${ref ?? ''}`);
        return true;
      },
      invalidateViewport: () => events.push('invalidate'),
      cancelFilter: async (jobID) => {
        events.push(`cancelFilter:${jobID}`);
      },
      cancelConditionalStats: () => events.push('cancelCond'),
      panels: {
        archiveCenter: {
          use: () => events.push('archive:use'),
          present: () => events.push('archive:present'),
        },
        editCopy: {
          grid: {
            get active() {
              return editActive;
            },
            leave: () => {
              editActive = false;
              events.push('edit:leave');
            },
          },
        },
        bookmarkCenter: {
          resetBook: () => events.push('bookmark:reset'),
          load: async () => {
            events.push('bookmark:load');
          },
        },
        analytics: {
          bookChanged: (key, book) => events.push(`analytics:${key}:${book}`),
          hasUnsaved: () => false,
        },
      },
    });
    deps.S.workbookKey = 'old-key';
    deps.S.fileName = 'old.xlsx';
    deps.S.sheetEpoch = 3;
    deps.S.filterJob = 'flt-9';
    deps.S.filtersBySheet.set('Old', 'f1');
    deps.S.sortKeysBySheet.set('Old', [{ column: 1, descending: false }]);

    const io = new WorkbookIO(deps);
    await io.acceptOpenedWorkbook({ opened: false, archiveOnly: true, archive: { name: 'a.zip' } });
    assert.equal(deps.S.workbookKey, 'old-key', 'archiveOnly must not wipe active workbook');
    assert.equal(deps.S.sheetEpoch, 3);
    assert.ok(events.includes('archive:present'));

    events.length = 0;
    await io.acceptOpenedWorkbook(
      {
        opened: true,
        workbookKey: 'new-key',
        book: 'new-book',
        fileName: 'ledger.xlsx',
        fileSize: 4096,
        activeTab: 1,
        sheets: [
          { name: 'Summary', state: 'visible' },
          { name: 'Detail', state: 'visible' },
        ],
        source: { format: 'xlsx' },
      },
      { revisionRef: 'C12' },
    );

    assert.equal(deps.S.sheetEpoch, 4);
    assert.equal(deps.S.workbookKey, 'new-key');
    assert.equal(deps.S.book, 'new-book');
    assert.equal(deps.S.fileName, 'ledger.xlsx');
    assert.equal(deps.S.filtersBySheet.size, 0);
    assert.equal(deps.S.sortKeysBySheet.size, 0);
    assert.equal(document.getElementById('fileBadge')?.textContent, 'ledger.xlsx');
    assert.ok(events.includes('cancelFilter:flt-9'));
    assert.ok(events.includes('cancelCond'));
    assert.ok(events.includes('bookmark:reset'));
    assert.ok(events.includes('analytics:new-key:new-book'));
    assert.ok(events.includes('select:Detail:C12'));
  } finally {
    dom.restore();
  }
});

test('uploadFile rejects unsupported formats and openWorkbook guards stale opens while restoring controls on failure', async () => {
  const dom = installDom({ fromIndexHtml: true });
  try {
    const toasts: string[] = [];
    let uploadCalls = 0;
    const transport = makeTestTransport();
    const deps = makeDefaultWorkbookIODeps({
      toast: (msg) => toasts.push(msg),
      transport: {
        ...transport,
        upload: async () => {
          uploadCalls++;
          return { opened: true, sheets: [] };
        },
      },
    });
    const io = new WorkbookIO(deps);

    await io.uploadFile(new File(['bin'], 'notes.pdf'));
    assert.equal(uploadCalls, 0);
    assert.match(toasts.at(-1) ?? '', /受支持的数据文件/);

    let current = true;
    const staleResult = await io.openWorkbook(
      async () => {
        current = false;
        return { opened: true, workbookKey: 'stale-book', sheets: [] };
      },
      'stale.xlsx',
      100,
      () => current,
    );
    assert.equal(staleResult, false);
    assert.notEqual(deps.S.workbookKey, 'stale-book');
    assert.equal(deps.S.opening, false);
    assert.equal((document.getElementById('openBtn') as HTMLButtonElement).disabled, false);

    await assert.rejects(
      () =>
        io.openWorkbook(async () => {
          throw new Error('损坏的工作簿');
        }),
      /损坏的工作簿/,
    );
    assert.equal(deps.S.opening, false);
    assert.equal((document.getElementById('openBtn') as HTMLButtonElement).disabled, false);
    assert.equal((document.getElementById('chooseFileBtn') as HTMLButtonElement).disabled, false);
    assert.equal((document.getElementById('homeBtn') as HTMLButtonElement).disabled, false);
    assert.equal(document.getElementById('welcomeError')?.textContent, '损坏的工作簿');
    assert.equal(document.getElementById('welcomeError')?.hidden, false);
  } finally {
    dom.restore();
  }
});

test('loadStatus restores requested launch sheet/ref and preserves bookmark state when workbookKey is unchanged', async () => {
  const dom = installDom({ fromIndexHtml: true });
  try {
    Reflect.set(window, 'location', {
      pathname: '/',
      search: '?sourceSheet=Sheet2&sourceRef=D8',
      replace: () => undefined,
    });
    const selected: string[] = [];
    let bookmarkResets = 0;
    const statusReply: WorkbookStatusResponse = {
      opened: true,
      workbookKey: 'same-key',
      book: 'book-1',
      fileName: 'report.xlsx',
      activeTab: 0,
      sheets: [
        { name: 'Sheet1', state: 'visible' },
        { name: 'Sheet2', state: 'visible' },
      ],
    };
    const deps = makeDefaultWorkbookIODeps({
      api: async <T = unknown>() => statusReply as T,
      selectSheet: async (name, ref) => {
        selected.push(`${name}!${ref ?? ''}`);
        return true;
      },
      panels: {
        bookmarkCenter: {
          resetBook: () => {
            bookmarkResets++;
          },
          load: async () => undefined,
        },
      },
    });
    deps.S.workbookKey = 'same-key';
    const io = new WorkbookIO(deps);
    await io.loadStatus();
    assert.equal(bookmarkResets, 0, 'unchanged workbookKey must not reset bookmarks');
    assert.deepEqual(selected, ['Sheet2!D8']);
  } finally {
    dom.restore();
  }
});

test('renderSourceIntegrity combines notices and toggles retainDatabaseSnapshot for legacy snapshots only', () => {
  const dom = installDom({ fromIndexHtml: true });
  try {
    renderSourceIntegrity({
      format: 'sqlite',
      incomplete: true,
      notice: '仅导入前 1000 行',
      presentationNotice: '样式已简化',
      databaseSnapshot: { legacy: true, retained: false },
    });
    assert.equal(document.getElementById('sourceIntegrityNotice')?.hidden, false);
    assert.equal(
      document.getElementById('sourceIntegrityMessage')?.textContent,
      '仅导入前 1000 行；样式已简化',
    );
    assert.equal(document.getElementById('retainDatabaseSnapshot')?.hidden, false);

    renderSourceIntegrity({
      format: 'sqlite',
      databaseSnapshot: { legacy: true, retained: true },
    });
    assert.equal(document.getElementById('sourceIntegrityNotice')?.hidden, true);
    assert.equal(document.getElementById('retainDatabaseSnapshot')?.hidden, true);
    assert.equal(formatBytes(2048), '2 KB');
  } finally {
    dom.restore();
  }
});
