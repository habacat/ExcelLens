import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { CompositionField, CompositionSourceInfo } from '../contracts/analysis.ts';
import type { ApiRequestOptions } from '../contracts/common.ts';
import type {
  RelationLimits,
  RelationQuery,
  RelationResultResponse,
} from '../contracts/features.ts';
import { createFileFormats } from '../core/formats.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import { makeTestFileFormatsConfig } from '../test-helpers/grid-fixture.ts';
import {
  Relations,
  type RelationContext,
  type RelationExportPayload,
} from './relations.ts';

interface Deferred<T> {
  readonly promise: Promise<T>;
  readonly resolve: (value: T) => void;
}

function deferred<T = void>(): Deferred<T> {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

interface MockPopup {
  opener: unknown;
  closed: boolean;
  readonly urls: string[];
  close(): void;
  readonly location: { assign(url: string): void };
}

function setupRelations() {
  const dom = installDom({ fromIndexHtml: true });
  const popups: MockPopup[] = [];
  const originalOpenDesc = Object.getOwnPropertyDescriptor(window, 'open');
  Object.defineProperty(window, 'open', {
    configurable: true,
    writable: true,
    value: () => {
      const popup: MockPopup = {
        opener: {},
        closed: false,
        urls: [],
        close() {
          this.closed = true;
        },
        location: {
          assign(url: string) {
            popup.urls.push(url);
          },
        },
      };
      popups.push(popup);
      return popup;
    },
  });

  const c: { bookKey: string; book: string; opened: boolean; source: string } = {
    bookKey: 'primary-fingerprint',
    book: 'primary-book',
    opened: true,
    source: 'wb:数据',
  };
  const calls: { readonly path: string; readonly request: Record<string, unknown> | null }[] = [];
  const exports: [RelationExportPayload, string][] = [];
  const navigations: [string, string][] = [];
  let next = 0;

  const limits: RelationLimits = {
    keys: 4,
    columns: 64,
    previewRows: 100,
    defaultRows: 1_000_000,
    maximumRows: 1_000_000_000,
    requestBytes: 16_384,
    resultBytes: 8_388_608,
    attachments: 0,
    viewRows: 20,
    viewColumns: 12,
    initialFields: 8,
    snippetUnits: 256,
    zipRows: 0,
  };
  const formats = createFileFormats(
    makeTestFileFormatsConfig({
      relationExports: [
        { format: 'csv', name: 'CSV', compressed: false },
        { format: 'zipcsv', name: 'ZIP', compressed: true },
      ],
    }),
  );
  const fields: CompositionField[] = [
    { column: 1, name: '编号', headerHash: 'id', headerLoaded: true, headerUnique: true },
    { column: 2, name: '金额', headerHash: 'amount', headerLoaded: true, headerUnique: true },
  ];
  const makeInfo = (side: 'left' | 'right'): CompositionSourceInfo => ({
    ref: {
      book: side === 'left' ? c.book : 'attached-book',
      source: 'wb:数据',
      headerRow: 1,
      fingerprint: `${side}-fp`,
      stamp: `${side}-stamp`,
    },
    name: `${side}.csv`,
    sheet: '数据',
    kind: 'workbook',
    format: 'csv',
    rows: 1,
    fields: structuredClone(fields),
  });
  const infos: [CompositionSourceInfo, CompositionSourceInfo] = [
    makeInfo('left'),
    makeInfo('right'),
  ];
  const query: RelationQuery = {
    keys: [{ left: 1, right: 1, mode: '', trim: false, ignoreCase: false }],
    leftColumns: [1, 2],
    rightColumns: [1, 2],
  };

  const makeData = (id: string): RelationResultResponse => ({
    id,
    mode: 'prepare',
    outputMode: 'left',
    parentId: id,
    workbookKey: c.bookKey,
    book: c.book,
    query: structuredClone(query),
    left: {
      id: `${infos[0].ref.book}/wb:数据`,
      name: infos[0].name,
      sheet: infos[0].sheet,
      kind: infos[0].kind,
      format: infos[0].format,
      fingerprint: infos[0].ref.fingerprint,
      stamp: infos[0].ref.stamp ?? 'left-stamp',
      headerRow: 1,
      fields: structuredClone(fields),
      total: 1,
    },
    right: {
      id: `${infos[1].ref.book}/wb:数据`,
      name: infos[1].name,
      sheet: infos[1].sheet,
      kind: infos[1].kind,
      format: infos[1].format,
      fingerprint: infos[1].ref.fingerprint,
      stamp: infos[1].ref.stamp ?? 'right-stamp',
      headerRow: 1,
      fields: structuredClone(fields),
      total: 1,
    },
    counts: {
      leftRecords: '1',
      rightRecords: '1',
      leftInvalid: '0',
      rightInvalid: '0',
      leftDuplicateGroups: '0',
      rightDuplicateGroups: '0',
      manyToManyGroups: '0',
      matchingGroups: '1',
      leftMatched: '1',
      rightMatched: '1',
      leftOnly: '0',
      rightOnly: '0',
      innerRows: '1',
      leftJoinRows: '1',
    },
    preview: {
      total: '1',
      truncated: false,
      rows: [
        {
          sequence: '1',
          left: {
            position: 0,
            number: 2,
            values: {
              1: { raw: '001', exists: true, type: 's' },
              2: { raw: '9007199254740993.31', exists: true, type: 'n' },
            },
          },
          right: {
            position: 0,
            number: 2,
            values: {
              1: { raw: '001', exists: true, type: 's' },
              2: { raw: '0.1', exists: true, type: 'n' },
            },
          },
          status: 'matched',
          rightMatches: '1',
        },
      ],
    },
  });

  const baseApi = async (path: string, options?: ApiRequestOptions): Promise<unknown> => {
    const request =
      options?.body && typeof options.body === 'string'
        ? (JSON.parse(options.body) as Record<string, unknown>)
        : null;
    calls.push({ path, request });
    if (path.endsWith('/start')) return { jobId: `new-${++next}` };
    if (path.includes('/status?')) return { status: 'done' };
    if (path.includes('/result?')) {
      return makeData(new URL(path, 'http://test').searchParams.get('id') ?? '');
    }
    if (path.endsWith('/limits')) return limits;
    if (path.startsWith('/api/relation/sources')) return { currentBook: c.book, books: [] };
    return {};
  };

  let apiImpl: (path: string, options?: ApiRequestOptions) => Promise<unknown> = baseApi;
  const api = async <T = unknown>(path: string, options?: ApiRequestOptions): Promise<T> =>
    (await apiImpl(path, options)) as T;

  const h = new Relations({
    api,
    context: (): RelationContext => c,
    formats,
    fieldLimits: { pickerRows: 3 },
    onExport: async (payload, name) => {
      exports.push([payload, name]);
    },
    navigate: async (sheet, ref) => {
      navigations.push([sheet, ref]);
    },
    toast: () => undefined,
  });

  h.identityKey = h.identity();
  h.limits = { ...limits };
  h.dialog.showModal();
  h.inputs = infos.map((info) => ({
    book: info.ref.book,
    source: info.ref.source,
    headerRow: 1,
    info,
    columns: [1, 2],
  }));
  h.keys = structuredClone(query.keys).map((k) => ({
    left: k.left,
    right: k.right,
    mode: k.mode ?? '',
    trim: Boolean(k.trim),
    ignoreCase: Boolean(k.ignoreCase),
  }));
  h.accepted = { data: makeData('accepted'), infos: structuredClone(infos) };

  const restore = (): void => {
    h.resetBook();
    if (originalOpenDesc) Object.defineProperty(window, 'open', originalOpenDesc);
    else Reflect.deleteProperty(window, 'open');
    dom.restore();
  };

  return {
    h,
    c,
    calls,
    exports,
    navigations,
    popups,
    makeData,
    infos,
    query,
    baseApi,
    setApi: (fn: typeof apiImpl) => {
      apiImpl = fn;
    },
    restore,
  };
}

test('failed or malformed candidates release only their token and preserve the accepted result', async () => {
  for (const invalid of ['response', 'render'] as const) {
    const s = setupRelations();
    try {
      const old = s.h.accepted;
      s.h.tasks.run = async <T = { readonly id: string }>(): Promise<T> => {
        const value = s.makeData('candidate');
        if (invalid === 'response') {
          (value.counts as { leftOnly: string }).leftOnly = '1';
        }
        return value as T;
      };
      if (invalid === 'render') {
        s.h.buildResult = () => {
          throw new Error('渲染失败');
        };
      }
      await s.h.action((current) => s.h.run(current));
      assert.equal(s.h.accepted, old);
      assert.equal(s.calls.filter((call) => call.path.endsWith('/clear')).length, 1);
      assert.equal(s.calls.at(-1)?.request?.id, 'candidate');
      assert.equal(document.getElementById('relationsStatus')?.getAttribute('role'), 'alert');
      assert.equal(s.h.busy, false);
    } finally {
      s.restore();
    }
  }
});

test('publishing a fully built candidate atomically replaces and then releases the old result', async () => {
  const s = setupRelations();
  try {
    const old = s.h.accepted;
    let renderedCount = 0;
    const origRender = s.h.renderResult.bind(s.h);
    s.h.renderResult = (view) => {
      renderedCount++;
      origRender(view);
    };
    await s.h.action((current) => s.h.run(current));
    assert.notEqual(s.h.accepted, old);
    assert.equal(s.h.accepted?.data.id, 'new-1');
    assert.equal(renderedCount, 1);
    assert.equal(s.calls.at(-1)?.request?.id, 'accepted');
    assert.equal(s.calls[0]?.request?.keepId, 'accepted');
  } finally {
    s.restore();
  }
});

test('cancelling a late real start cleans its own backend job and keeps the accepted result', async () => {
  const s = setupRelations();
  try {
    const old = s.h.accepted;
    const gate = deferred<{ readonly jobId: string }>();
    s.setApi((path, opts) => (path.endsWith('/start') ? gate.promise : s.baseApi(path, opts)));
    const pending = s.h.action((current) => s.h.run(current));
    s.h.cancel(true);
    gate.resolve({ jobId: 'late' });
    await pending;
    assert.equal(s.h.accepted, old);
    assert.deepEqual(
      s.calls.filter((call) => call.path.endsWith('/clear')).map((call) => call.request?.id),
      ['late'],
    );
    assert.equal(document.getElementById('relationsStatus')?.textContent, '已取消');
  } finally {
    s.restore();
  }
});

test('a late candidate body cannot replace a new accepted result after cancellation and retry', async () => {
  const s = setupRelations();
  try {
    const gate = deferred<RelationResultResponse>();
    const entered = deferred();
    s.setApi((path, opts) => {
      if (path.includes('/result?') && path.endsWith('new-1')) {
        entered.resolve();
        return gate.promise;
      }
      return s.baseApi(path, opts);
    });
    const first = s.h.action((current) => s.h.run(current));
    await entered.promise;
    s.h.cancel(true);
    await s.h.action((current) => s.h.run(current));
    const accepted = s.h.accepted;
    gate.resolve(s.makeData('new-1'));
    await first;
    assert.equal(s.h.accepted, accepted);
    assert.equal(accepted?.data.id, 'new-2');
    assert.equal(s.h.busy, false);
    assert.equal(
      s.calls.some((call) => call.path.endsWith('/clear') && call.request?.id === 'new-2'),
      false,
    );
  } finally {
    s.restore();
  }
});

test('an older finally cannot enable controls while a newer operation is pending', async () => {
  const s = setupRelations();
  try {
    const first = deferred();
    const second = deferred();
    const p = s.h.action(() => first.promise);
    s.h.cancel(false);
    const q = s.h.action(() => second.promise);
    first.resolve();
    await p;
    assert.equal(s.h.busy, true);
    assert.equal((document.getElementById('relationsConfig') as HTMLFieldSetElement).disabled, true);
    second.resolve();
    await q;
    assert.equal(s.h.busy, false);
  } finally {
    s.restore();
  }
});

test('changing book resets the result and late field capture releases without populating the new book', async () => {
  const s = setupRelations();
  try {
    const gate = deferred<{
      readonly id: string;
      readonly mode: string;
      readonly book: string;
      readonly workbookKey: string;
      readonly info: CompositionSourceInfo;
    }>();
    s.h.fieldsTasks.run = <T = { readonly id: string }>(): Promise<T> => gate.promise as Promise<T>;
    const pending = s.h.action((current) => s.h.read(0, current));
    s.c.book = 'other-book';
    s.h.resetBook();
    gate.resolve({
      id: 'late-fields',
      mode: 'fields',
      book: 'primary-book',
      workbookKey: s.c.bookKey,
      info: s.infos[0],
    });
    await pending;
    assert.equal(s.h.accepted, null);
    assert.equal(s.h.inputs.length, 0);
    assert.ok(
      s.calls.some((call) => call.path === '/api/composition/clear' && call.request?.id === 'late-fields'),
    );
    assert.ok(
      s.calls.some((call) => call.path === '/api/relation/clear' && call.request?.id === 'accepted'),
    );
  } finally {
    s.restore();
  }
});

test('failed mode switch restores the accepted selector and does not discard its parent', async () => {
  const s = setupRelations();
  try {
    const old = s.h.accepted;
    const modeSelect = document.getElementById('relationsMode') as HTMLSelectElement;
    modeSelect.append(new Option('左表全部', 'left'), new Option('右表未匹配', 'right_unmatched'));
    s.h.tasks.run = async <T = { readonly id: string }>(): Promise<T> => {
      const value = s.makeData('candidate');
      const mutated = {
        ...value,
        mode: 'preview',
        parentId: 'accepted',
        outputMode: 'right_unmatched',
      };
      return mutated as T;
    };
    modeSelect.value = 'right_unmatched';
    await s.h.action((current) => s.h.changeMode('right_unmatched', current));
    assert.equal(s.h.accepted, old);
    assert.equal(modeSelect.value, 'left');
    assert.equal(s.calls.at(-1)?.request?.id, 'candidate');
  } finally {
    s.restore();
  }
});

test('cancelled initial source-list refresh cannot initialize a reopened configuration', async () => {
  const s = setupRelations();
  try {
    const gate = deferred();
    const entered = deferred();
    s.h.inputs = [];
    s.h.refresh = () => {
      entered.resolve();
      return gate.promise;
    };
    const pending = s.h.open();
    await entered.promise;
    s.h.cancel(false);
    s.h.dialog.showModal();
    gate.resolve();
    await pending;
    assert.equal(s.h.inputs.length, 0);
  } finally {
    s.restore();
  }
});

test('late upload refresh cannot overwrite newly selected sources after reopening', async () => {
  const s = setupRelations();
  try {
    const gate = deferred();
    const entered = deferred();
    s.setApi(async () => ({ book: 'uploaded' }));
    s.h.refresh = () => {
      entered.resolve();
      return gate.promise;
    };
    let current = true;
    const file = new File(['a,b'], 'upload.csv', { type: 'text/csv' });
    const pending = s.h.upload(file, () => current);
    await entered.promise;
    current = false;
    const inputs = (s.h.inputs = [
      { book: 'chosen-left', source: '', headerRow: 1, info: null, columns: [] },
      { book: 'chosen-right', source: '', headerRow: 1, info: null, columns: [] },
    ]);
    gate.resolve();
    await pending;
    assert.equal(s.h.inputs, inputs);
    assert.equal(s.h.inputs[1]?.book, 'chosen-right');
  } finally {
    s.restore();
  }
});

test('queued native close cannot cancel an immediately reopened operation', async () => {
  const s = setupRelations();
  try {
    let resets = 0;
    s.h.tasks.reset = () => {
      resets++;
    };
    const serial = s.h.serial;
    // Simulate a queued native close event firing after dialog was immediately reopened.
    s.h.dialog.dispatchEvent(new Event('close'));
    assert.equal(s.h.serial, serial);
    assert.equal(resets, 0);
    s.h.dialog.close();
    await new Promise((resolve) => {
      setImmediate(resolve);
    });
    assert.equal(resets, 1);
  } finally {
    s.restore();
  }
});

test('late foreign navigation closes only its empty popup and cannot move a new result', async () => {
  const s = setupRelations();
  try {
    const gate = deferred<{ readonly sameBook: false; readonly url: string }>();
    s.setApi(() => gate.promise);
    assert.ok(s.h.accepted);
    const pending = s.h.goSource(s.h.accepted, 1, 2, 2);
    assert.equal(s.popups.length, 1);
    s.h.cancel(false);
    gate.resolve({ sameBook: false, url: 'http://source/B2' });
    await pending;
    assert.ok(s.popups[0]);
    assert.equal(s.popups[0].closed, true);
    assert.deepEqual(s.popups[0].urls, []);
    assert.equal(s.navigations.length, 0);
  } finally {
    s.restore();
  }
});

test('export uses the accepted snapshot rather than changed inputs and rejects an exact over-limit count', async () => {
  const s = setupRelations();
  try {
    if (s.h.keys[0]) s.h.keys[0].mode = 'number';
    if (s.h.inputs[0]) s.h.inputs[0].book = 'new-selection';
    (document.getElementById('relationsMaximum') as HTMLInputElement).value = '1';
    (document.getElementById('relationsProtect') as HTMLInputElement).checked = true;
    await s.h.export();
    assert.equal(s.exports.length, 1);
    assert.ok(s.exports[0]);
    assert.equal(s.exports[0][0].jobId, 'accepted');
    assert.equal(s.exports[0][0].maximum, '1');
    assert.equal(s.exports[0][0].protectCSV, true);

    s.h.dialog.showModal();
    if (s.h.accepted) {
      (s.h.accepted.data.preview as { total: string }).total = '1000000000000000000';
    }
    (document.getElementById('relationsMaximum') as HTMLInputElement).value = '1000000000';
    await s.h.export();
    assert.equal(s.exports.length, 1);
    assert.match(
      document.getElementById('relationsStatus')?.textContent ?? '',
      /1,000,000,000,000,000,000/,
    );
    assert.equal(s.h.dialog.open, true);
  } finally {
    s.restore();
  }
});

test('large complete results recommend injected compressed format and preserve an explicit user choice', () => {
  const s = setupRelations();
  try {
    assert.ok(s.h.limits);
    assert.ok(s.h.accepted);
    s.h.limits = { ...s.h.limits, zipRows: 100_000 };
    const formatSelect = document.getElementById('relationsFormat') as HTMLSelectElement;
    (s.h.accepted.data.preview as { total: string }).total = '99999';
    s.h.chooseExportFormat();
    assert.equal(formatSelect.value, 'csv');
    (s.h.accepted.data.preview as { total: string }).total = '100000';
    s.h.chooseExportFormat();
    assert.equal(formatSelect.value, 'zipcsv');
    formatSelect.value = 'csv';
    formatSelect.dispatchEvent(new Event('change'));
    (s.h.accepted.data.preview as { total: string }).total = '1000000000';
    s.h.chooseExportFormat();
    assert.equal(formatSelect.value, 'csv');
  } finally {
    s.restore();
  }
});

test('compressed format belongs to the accepted export and forged formats do not start a download', async () => {
  const s = setupRelations();
  try {
    const formatSelect = document.getElementById('relationsFormat') as HTMLSelectElement;
    formatSelect.value = 'zipcsv';
    (document.getElementById('relationsMaximum') as HTMLInputElement).value = '10';
    (document.getElementById('relationsProtect') as HTMLInputElement).checked = true;
    await s.h.export();
    assert.equal(s.exports.length, 1);
    assert.ok(s.exports[0]);
    assert.equal(s.exports[0][0].format, 'zipcsv');
    assert.equal(s.exports[0][0].jobId, 'accepted');
    assert.equal(s.exports[0][0].protectCSV, true);

    s.h.dialog.showModal();
    formatSelect.append(new Option('XLSX', 'xlsx'));
    formatSelect.value = 'xlsx';
    await s.h.export();
    assert.equal(s.exports.length, 1);
    assert.match(document.getElementById('relationsStatus')?.textContent ?? '', /格式无效/);
  } finally {
    s.restore();
  }
});
