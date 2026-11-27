import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { ApiRequestOptions } from '../contracts/common.ts';
import { createInitialWorkbookState } from '../grid/state.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import {
  makeDefaultCapabilities,
  makeRowViewState,
} from '../test-helpers/grid-fixture.ts';
import { SelectionInspector } from './inspect.ts';
import { ProfileCenter } from './profile.ts';

function setupInspection() {
  const dom = installDom({ fromIndexHtml: true });
  const state = createInitialWorkbookState(1_048_576, 16_384);
  state.opened = true;
  state.sheet = 'Data';
  state.book = 'path';
  state.rowView = makeRowViewState({ id: 'view', sheet: 'Data' });
  state.workbookKey = 'one';
  state.sheetEpoch = 1;
  state.indexReady = true;
  state.range = { row1: 4, row2: 2, col1: 2, col2: 2, visual1: 1, visual2: 2 };
  state.filtered = true;
  state.filterId = 'f';
  state.usedMaxRow = 4;
  state.usedMaxCol = 2;

  const pending: ((value: unknown) => void)[] = [];
  const calls: { readonly path: string; readonly body: Record<string, unknown> }[] = [];
  const api = async <T = unknown>(path: string, options?: ApiRequestOptions): Promise<T> => {
    const body =
      typeof options?.body === 'string'
        ? (JSON.parse(options.body) as Record<string, unknown>)
        : {};
    calls.push({ path, body });
    return (await new Promise<unknown>((resolve) => pending.push(resolve))) as T;
  };

  const profile = new ProfileCenter({
    state,
    api,
    toast: () => undefined,
    openDialog: (id) => {
      const dlg = document.getElementById(id);
      if (dlg instanceof HTMLDialogElement) dlg.showModal();
    },
    navigateToCell: async () => true,
    colName: (c) => String.fromCharCode(64 + c),
    limits: { cells: 4_000_000, requestBytes: 8192, bytes: 2 << 20, bins: 10 },
    source: () => makeDefaultCapabilities(),
    excludeThroughRow: () => 1,
  });

  const inspector = new SelectionInspector({
    state,
    api,
    colName: (c) => String.fromCharCode(64 + c),
    parseColumn: () => 2,
    toast: () => undefined,
    navigate: async () => true,
    onChange: () => undefined,
    copy: async () => true,
    excludeThroughRow: () => 1,
  });

  return { state, profile, inspector, pending, calls, restore: () => dom.restore() };
}

test('profile and inspection send paired visual endpoints and keep edited source addresses explicit', async () => {
  const h = setupInspection();
  try {
    h.profile.open();
    const p = h.profile.analyze();
    assert.ok(h.calls[0]);
    assert.equal(h.calls[0].body.start, 'B4');
    assert.equal(h.calls[0].body.end, 'B2');
    assert.equal(h.calls[0].body.visualStart, 1);
    assert.equal(h.calls[0].body.excludeThroughRow, 1);
    h.pending.shift()?.({});
    await p;

    (document.getElementById('profileStart') as HTMLInputElement).value = 'B1';
    const manual = h.profile.analyze();
    assert.equal(h.calls[1]?.body.visualStart, undefined);
    h.pending.shift()?.({});
    await manual;

    (document.getElementById('inspectMode') as HTMLSelectElement).value = 'formulas';
    (document.getElementById('inspectScope') as HTMLSelectElement).value = 'selection';
    const opt = h.inspector.options();
    assert.equal(opt.start, 'B4');
    assert.equal(opt.end, 'B2');
    assert.equal(opt.visualStart, 1);
    assert.equal(opt.visualEnd, 2);
    assert.equal(opt.excludeThroughRow, 1);
  } finally {
    h.restore();
  }
});

test('late profile responses cannot render a different workbook or a reopened dialog', async () => {
  const h = setupInspection();
  try {
    h.profile.open();
    const old = h.profile.analyze();
    h.state.workbookKey = 'two';
    h.profile.resetBook();
    h.profile.open();
    const fresh = h.profile.analyze();
    h.pending[1]?.({
      workbookKey: 'two',
      book: 'path',
      sheet: 'Data',
      bindingViewId: 'view',
      viewId: 'view',
      bindingFilterId: 'f',
      filterId: 'f',
      includeDistribution: false,
      start: 'B4',
      end: 'B2',
      visualStart: 1,
      visualEnd: 2,
      excludeThroughRow: 1,
      range: 'B4:B2',
      rows: 2,
      firstColumn: 2,
      lastColumn: 2,
      total: 2,
      nonEmpty: 2,
      blank: 0,
      number: 2,
      text: 0,
      formulas: 0,
      cacheMissing: 0,
      errors: 0,
      sum: '42',
      average: '21',
      minimum: '20',
      maximum: '22',
      sumExact: '42',
      averageExact: '21',
      minimumExact: '20',
      maximumExact: '22',
      unique: 2,
      uniqueTruncated: false,
      topValues: [],
      errorRefs: [],
      missingRefs: [],
      filtered: true,
    });
    await fresh;
    h.pending[0]?.({
      workbookKey: 'one',
      book: 'path',
      sheet: 'Data',
      sum: '999',
    });
    await old;
    assert.equal(
      (document.getElementById('profileAnalyzeBtn') as HTMLButtonElement).disabled,
      false,
    );
    assert.ok(
      (document.getElementById('profileDetail')?.textContent ?? '').includes('42'),
      'fresh profile must remain rendered',
    );
    assert.ok(!(document.getElementById('profileDetail')?.textContent ?? '').includes('999'));
  } finally {
    h.restore();
  }
});

test('a cancelled inspector response cannot repopulate caches after a workbook reset', async () => {
  const h = setupInspection();
  try {
    (document.getElementById('inspectMode') as HTMLSelectElement).value = 'formulas';
    (document.getElementById('inspectScope') as HTMLSelectElement).value = 'selection';
    const old = h.inspector.run();
    h.inspector.resetWorkbook();
    h.state.workbookKey = 'two';
    h.state.filtered = false;
    h.pending[0]?.({ matches: [{ ref: 'B3', row: 3, col: 2 }], matched: 1 });
    await old;
    assert.equal(h.inspector.matches.length, 0);

    const fresh = h.inspector.run();
    assert.equal(h.calls.length, 2);
    h.pending[1]?.({ matches: [], matched: 0 });
    await fresh;
    assert.equal(h.inspector.matches.length, 0);
    // Re-running with same options uses cache (still 2 calls)
    await h.inspector.run();
    assert.equal(h.calls.length, 2);
  } finally {
    h.restore();
  }
});
