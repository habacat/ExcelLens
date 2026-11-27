import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { ApiRequestOptions } from '../contracts/common.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import {
  makeDefaultSheetControllerDeps,
  SheetController,
} from '../test-helpers/grid-fixture.ts';

function setupTextHeader(apiImpl: (url: string, options?: ApiRequestOptions) => Promise<unknown>) {
  const dom = installDom({ fromIndexHtml: true });
  const deps = makeDefaultSheetControllerDeps({
    api: async <T = unknown>(url: string, options?: ApiRequestOptions): Promise<T> =>
      (await apiImpl(url, options)) as T,
  });
  const S = deps.S;
  S.indexReady = true;
  S.source = {
    format: 'txt',
    tableHeader: { row: 1, columns: ['账号', '金额'], detected: true },
  };
  S.sheet = 'Data';
  S.sheetEpoch = 7;
  S.textHeaderRevision = 0;
  S.usedMaxRow = 100;
  S.filterId = '';
  S.filterJob = null;
  const controller = new SheetController(deps);
  controller.updateAutoFilterButton = () => undefined;
  controller.refreshAutoFilterHeaders = () => undefined;
  controller.cancelFilter = async () => undefined;
  controller.clearFilter = async () => undefined;
  return { S, controller, restore: () => dom.restore() };
}

test('late manual header cannot override a newer choice or a switch to ordinary text', async () => {
  let resolveReq: ((value: unknown) => void) | undefined;
  const h = setupTextHeader(
    () =>
      new Promise((r) => {
        resolveReq = r;
      }),
  );
  try {
    const pending = h.controller.applyTextTableHeader(3);
    await h.controller.disableTextTableHeader();
    resolveReq?.({ row: 3, columns: ['old', 'stale'] });
    await pending;
    assert.equal(h.S.autoFilter, null);
    assert.equal(h.S.autoFiltersBySheet.has('Data'), true);

    const next = h.controller.applyTextTableHeader(5);
    await h.controller.applyTextTableHeader(0, true);
    resolveReq?.({ row: 5, columns: ['old', 'stale'] });
    await next;
    const readAutoFilter = () => h.S.autoFilter;
    const restoredFilter = readAutoFilter();
    assert.ok(restoredFilter);
    assert.equal(restoredFilter.row, 1);
    assert.equal(restoredFilter.names?.[0], '账号');
  } finally {
    h.restore();
  }
});

test('a previous file header cannot alter the next workbook', async () => {
  let resolveReq: ((value: unknown) => void) | undefined;
  const h = setupTextHeader(
    () =>
      new Promise((r) => {
        resolveReq = r;
      }),
  );
  try {
    const pending = h.controller.applyTextTableHeader(4);
    h.S.sheetEpoch++;
    h.S.sheet = 'Next';
    h.S.autoFilter = null;
    resolveReq?.({ row: 4, columns: ['old'] });
    await pending;
    assert.equal(h.S.autoFilter, null);
    assert.equal(h.S.autoFiltersBySheet.has('Next'), false);
  } finally {
    h.restore();
  }
});

test('a cancelled filter finishing late cannot replace the current table view', async () => {
  const dom = installDom({ fromIndexHtml: true });
  let release: ((value: unknown) => void) | undefined;
  const deps = makeDefaultSheetControllerDeps({
    api: async <T = unknown>(): Promise<T> =>
      (await new Promise<unknown>((r) => {
        release = r;
      })) as T,
  });
  const S = deps.S;
  S.filterJob = 'old-job';
  S.sheet = 'Data';
  S.sheetEpoch = 2;
  S.filterId = 'keep-current';
  const controller = new SheetController(deps);
  try {
    const pending = controller.pollFilter('old-job', 'Data', 2);
    await new Promise<void>((resolve) => {
      setImmediate(resolve);
    });
    S.filterJob = 'new-job';
    release?.({ status: 'done', filterId: 'old-result', matchedRows: 99 });
    await pending;
    assert.equal(S.filterId, 'keep-current');
    assert.equal(S.filterJob, 'new-job');
  } finally {
    dom.restore();
  }
});

test('a late filter start after a sheet switch is cancelled without publishing or polling', async () => {
  const dom = installDom({ fromIndexHtml: true });
  let release: ((value: unknown) => void) | undefined;
  const cancelled: string[] = [];
  const deps = makeDefaultSheetControllerDeps({
    api: async <T = unknown>(url: string, options?: ApiRequestOptions): Promise<T> => {
      if (url.endsWith('/start')) {
        return (await new Promise<unknown>((resolve) => {
          release = resolve;
        })) as T;
      }
      const body =
        typeof options?.body === 'string' ? (JSON.parse(options.body) as { id?: string }) : {};
      if (body.id) cancelled.push(body.id);
      const empty: unknown = {};
      return empty as T;
    },
  });
  const S = deps.S;
  S.sheet = 'Data';
  S.sheetEpoch = 2;
  S.workbookKey = 'book';
  S.filterJob = null;
  S.filterRequest = 0;
  const controller = new SheetController(deps);
  controller.pollFilter = () => {
    throw new Error('old source must not poll');
  };
  try {
    const pending = controller.submitFilterRules([{ column: 1, operator: 'notBlank', value: '' }]);
    const readFilterJob = (): string | null => S.filterJob;
    assert.match(readFilterJob() ?? '', /pending:/);
    S.sheet = 'Next';
    S.sheetEpoch++;
    release?.({ jobId: 'late' });
    assert.equal(await pending, false);
    await new Promise<void>((resolve) => {
      setImmediate(resolve);
    });
    assert.deepEqual(cancelled, ['late']);
    assert.equal(S.filterJob, null);
  } finally {
    dom.restore();
  }
});
