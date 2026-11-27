import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { ApiRequestOptions } from '../contracts/common.ts';
import type { ReconcileField, ReconcileSourceInfo } from '../contracts/features.ts';
import { columnName } from '../core/cell-address.ts';
import { createInitialWorkbookState } from '../grid/state.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import { makeNoopApi } from '../test-helpers/grid-fixture.ts';
import {
  Reconcile,
  ReconcileModel,
  type ReconcileExportExtra,
  type ReconcileResultWithMeta,
} from './reconcile.ts';

interface Deferred<T> {
  readonly promise: Promise<T>;
  readonly resolve: (value: T) => void;
  readonly reject: (reason?: unknown) => void;
}

function deferred<T = unknown>(): Deferred<T> {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function makeField(
  column: number,
  rawName: string,
  extras: Partial<ReconcileField> = {},
): ReconcileField {
  return {
    column,
    rawName,
    name: rawName,
    headerLoaded: true,
    headerHash: rawName,
    ...extras,
  };
}

function makeSourceInfo(overrides: Partial<ReconcileSourceInfo> = {}): ReconcileSourceInfo {
  return {
    id: 'book/wb:sheet',
    name: '基线.csv',
    sheet: 'sheet',
    kind: 'workbook',
    fingerprint: 'f'.repeat(64),
    stamp: 'a'.repeat(64),
    headerRow: 1,
    fields: [],
    total: 0,
    ...overrides,
  };
}

function makeReconcileResult(
  overrides: Partial<ReconcileResultWithMeta> = {},
): ReconcileResultWithMeta {
  return {
    left: makeSourceInfo({ id: 'primary/wb:left', name: '左.csv', sheet: '旧左' }),
    right: makeSourceInfo({ id: 'foreign/wb:sheet', name: '右.csv', sheet: '旧右' }),
    query: {
      keys: [{ left: 1, right: 1, mode: '' }],
      fields: [],
      includeEqual: false,
    },
    counts: {
      leftRecords: 0,
      rightRecords: 0,
      equal: 0,
      withinTolerance: 0,
      different: 0,
      leftOnly: 0,
      rightOnly: 0,
      duplicate: 0,
      review: 0,
      invalidKeys: 0,
    },
    samples: [],
    samplesTruncated: false,
    ...overrides,
  };
}

test('saved comparison rules rebind both sides by unique headers without guessing', () => {
  const digest = 'b'.repeat(64);
  const oldStamp = 'a'.repeat(64);
  const fields: ReconcileField[] = [
    makeField(1, '编号', { uniqueHeader: true }),
    makeField(2, '金额', { uniqueHeader: true }),
  ];
  const input = {
    kind: 'workbook' as const,
    name: '基线.csv',
    sheet: '明细',
    stamp: oldStamp,
    fileSHA256: digest,
    fields,
  };
  const inputSource = makeSourceInfo({
    kind: 'workbook',
    name: '基线.csv',
    sheet: '明细',
    stamp: oldStamp,
    fields,
  });
  const field0 = fields[0];
  const field1 = fields[1];
  assert.ok(field0 && field1);
  const target = makeSourceInfo({
    kind: 'workbook',
    stamp: 'c'.repeat(64),
    fields: [
      { ...field1, column: 1 },
      { ...field0, column: 2 },
    ],
  });
  const record = {
    kind: 'ExcelLens.Reconcile',
    version: 1,
    ruleVersion: 1,
    scope: 'all-data-rows',
    exceptionGroupsSHA256: digest,
    inputs: [input, input],
    query: {
      keys: [{ left: 1, right: 1, mode: 'text', trim: true }],
      fields: [{ left: 2, right: 2, mode: 'number' }],
    },
    counts: {
      equal: 1,
      different: 1,
      leftOnly: 0,
      rightOnly: 0,
      duplicate: 0,
      review: 0,
      invalidKeys: 0,
    },
    detail: { status: 'different', sha256: digest, protectCSV: true },
  };
  const mapped = ReconcileModel.rebindRecord(record, [inputSource, target]);
  assert.deepEqual(mapped, {
    keys: [{ left: 1, right: 2, mode: 'text', trim: true, ignoreCase: false }],
    fields: [{ left: 2, right: 1, mode: 'number', trim: false, ignoreCase: false }],
    includeEqual: false,
  });
  assert.throws(
    () =>
      ReconcileModel.rebindRecord(record, [
        target,
        makeSourceInfo({
          ...target,
          fields: [...target.fields, { ...field0, column: 3 }],
        }),
      ]),
    /唯一表头/,
  );
  assert.throws(
    () =>
      ReconcileModel.rebindRecord(record, [
        target,
        makeSourceInfo({
          ...target,
          fields: target.fields.slice(0, 1),
        }),
      ]),
    /唯一表头/,
  );
  const firstQueryField = record.query.fields[0];
  assert.ok(firstQueryField);
  const withTolerance = {
    ...record,
    ruleVersion: 2,
    counts: { ...record.counts, withinTolerance: 1 },
    query: {
      ...record.query,
      fields: [{ ...firstQueryField, tolerance: '0.010' }],
    },
  };
  assert.equal(
    ReconcileModel.rebindRecord(withTolerance, [inputSource, target]).fields[0]?.tolerance,
    '0.010',
  );
  assert.throws(
    () => ReconcileModel.rebindRecord({ ...withTolerance, ruleVersion: 1 }, [inputSource, target]),
    /容差/,
  );
  assert.throws(
    () => ReconcileModel.rebindRecord({ ...record, ruleVersion: 3 }, [inputSource, target]),
    /版本/,
  );
  assert.throws(
    () =>
      ReconcileModel.rebindRecord({ ...record, counts: { duplicate: -1 } }, [inputSource, target]),
    /计数/,
  );
});

test('automatic field suggestions require complete unique names on both sides', () => {
  const left: ReconcileField[] = [
    makeField(1, '编号'),
    makeField(2, '金额'),
    makeField(3, '金额'),
    makeField(4, '未读', { headerLoaded: false }),
    makeField(5, '截取', { truncated: true }),
    makeField(6, '备注'),
  ];
  const right: ReconcileField[] = [
    makeField(2, '编号'),
    makeField(3, '金额'),
    makeField(4, '未读'),
    makeField(5, '截取'),
    makeField(7, '备注'),
  ];
  const result = ReconcileModel.suggest(left, right);
  assert.deepEqual(result, {
    keys: [{ left: 1, right: 2, mode: '' }],
    fields: [{ left: 6, right: 7, mode: '' }],
  });
});

test('column selection refuses duplicate, truncated and ambiguous names while retaining explicit column access', () => {
  const fields: ReconcileField[] = [
    makeField(1, '编号'),
    makeField(2, '金额'),
    makeField(3, '金额'),
    makeField(4, 'A'),
    makeField(5, '长名', { truncated: true }),
  ];
  assert.equal(ReconcileModel.resolveColumn(' 1 ', fields), 1);
  assert.equal(ReconcileModel.resolveColumn('e', fields), 5);
  assert.equal(ReconcileModel.resolveColumn('编号', fields), 1);
  assert.throws(() => ReconcileModel.resolveColumn('金额', fields), /重复/);
  assert.throws(() => ReconcileModel.resolveColumn('A', fields), /歧义/);
  assert.throws(() => ReconcileModel.resolveColumn('长名', fields), /无效/);
});

test('late field requests cannot overwrite a changed side or a replaced workbook', async () => {
  const dom = installDom({ fromIndexHtml: true });
  try {
    const state = createInitialWorkbookState(1_048_576, 16_384);
    const requests: Deferred<ReconcileSourceInfo>[] = [];
    const h = new Reconcile({
      state,
      api: <T = unknown>(_path: string, _options?: ApiRequestOptions): Promise<T> => {
        const d = deferred<ReconcileSourceInfo>();
        requests.push(d);
        return d.promise as Promise<T>;
      },
      toast: () => undefined,
      colName: columnName,
      navigate: async () => undefined,
      recordBytes: 1_048_576,
    });
    const bookSelect = document.getElementById('reconcileLeftBook') as HTMLSelectElement;
    bookSelect.append(new Option('book', 'book'));
    bookSelect.value = 'book';
    const sheetSelect = document.getElementById('reconcileLeftSheet') as HTMLSelectElement;
    sheetSelect.append(new Option('sheet', 'wb:sheet'));
    sheetSelect.value = 'wb:sheet';
    (document.getElementById('reconcileLeftHeader') as HTMLInputElement).value = '1';

    const old = h.loadSide('left');
    const fresh = h.loadSide('left');
    requests[1]?.resolve(
      makeSourceInfo({ stamp: 'fresh', total: 2, fields: [makeField(1, '新列')] }),
    );
    await fresh;
    requests[0]?.resolve(
      makeSourceInfo({ stamp: 'old', total: 1, fields: [makeField(1, '旧列')] }),
    );
    await old;
    assert.equal(h.sides.left.info?.stamp, 'fresh');
    const optionsEl = document.getElementById('reconcileLeftOptions');
    assert.ok(optionsEl);
    assert.equal(optionsEl.children.length, 1);
    assert.equal((optionsEl.children[0] as HTMLOptionElement | undefined)?.label, '新列');

    const replaced = h.loadSide('left');
    h.epoch++;
    h.sides.left.serial = 0;
    h.sides.left.info = null;
    requests[2]?.resolve(
      makeSourceInfo({ stamp: 'replaced', total: 3, fields: [makeField(1, '替换列')] }),
    );
    await replaced;
    assert.equal(h.sides.left.info, null);
    assert.equal((optionsEl.children[0] as HTMLOptionElement | undefined)?.label, '新列');
  } finally {
    dom.restore();
  }
});

test('source navigation closes blank popups after stale results, errors or same-book responses', async () => {
  const dom = installDom({ fromIndexHtml: true });
  try {
    let opened = 0;
    let closed = 0;
    let navigated = 0;
    let assigned = 0;
    let apiImpl: () => Promise<unknown> = async () => ({});

    const originalOpenDesc = Object.getOwnPropertyDescriptor(window, 'open');
    Object.defineProperty(window, 'open', {
      configurable: true,
      writable: true,
      value: () => {
        opened++;
        return {
          opener: {},
          close: () => {
            closed++;
          },
          location: {
            assign: () => {
              assigned++;
            },
          },
        };
      },
    });
    try {
      const state = createInitialWorkbookState(1_048_576, 16_384);
      const h = new Reconcile({
        state,
        api: async <T = unknown>(): Promise<T> => (await apiImpl()) as T,
        toast: () => undefined,
        colName: () => 'B',
        closeAnalysis: () => undefined,
        navigate: async () => {
          navigated++;
        },
        recordBytes: 1_048_576,
      });
      h.job = 'one';
      h.epoch = 0;
      h.primaryBook = 'primary';
      h.result = makeReconcileResult({
        right: makeSourceInfo({ id: 'foreign/wb:sheet' }),
      });

      const gate = deferred<{ readonly sameBook: false; readonly url: string }>();
      apiImpl = () => gate.promise;
      const stale = h.goSource('right', 2, 2);
      h.job = 'two';
      gate.resolve({ sameBook: false, url: '/?old' });
      await stale;
      assert.equal(closed, 1);
      assert.equal(assigned, 0);

      apiImpl = async () => {
        throw new Error('changed source');
      };
      await h.goSource('right', 2, 2);
      assert.equal(closed, 2);

      apiImpl = async () => ({ sameBook: true, kind: 'workbook', sheet: 'sheet' });
      await h.goSource('right', 2, 2);
      assert.equal(closed, 3);
      assert.equal(navigated, 1);

      apiImpl = async () => ({ sameBook: false, url: '/?fresh' });
      await h.goSource('right', 2, 2);
      assert.equal(assigned, 1);
      assert.equal(closed, 3);
      assert.equal(opened, 4);
    } finally {
      if (originalOpenDesc) Object.defineProperty(window, 'open', originalOpenDesc);
      else Reflect.deleteProperty(window, 'open');
    }
  } finally {
    dom.restore();
  }
});

test('downloads keep the completed result source after the editable configuration changes', async () => {
  const dom = installDom({ fromIndexHtml: true });
  try {
    (document.getElementById('reconcileProtectCSV') as HTMLInputElement).checked = true;
    let exportArgs:
      | readonly [string, boolean, string, ReconcileExportExtra | undefined]
      | undefined;
    const state = createInitialWorkbookState(1_048_576, 16_384);
    const h = new Reconcile({
      state,
      api: makeNoopApi(),
      toast: () => undefined,
      colName: columnName,
      navigate: async () => undefined,
      onExport: async (jobId, protectCSV, label, extra) => {
        exportArgs = [jobId, protectCSV, label, extra];
      },
      recordBytes: 1_048_576,
    });
    h.job = 'completed';
    h.result = makeReconcileResult({
      left: makeSourceInfo({ name: '左.csv', sheet: '旧左' }),
      right: makeSourceInfo({ name: '右.csv', sheet: '旧右' }),
    });
    h.configChanged = true;
    h.busy = false;
    h.sides.left.info = makeSourceInfo({ name: '新左' });

    await h.export();
    assert.ok(exportArgs);
    assert.equal(exportArgs[0], 'completed');
    assert.equal(exportArgs[1], true);
    assert.match(exportArgs[2], /旧左.*旧右/);
    assert.doesNotMatch(exportArgs[2], /新左/);
  } finally {
    dom.restore();
  }
});
