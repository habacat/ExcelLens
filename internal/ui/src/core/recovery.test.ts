import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { ApiClient, ApiRequestOptions, HttpError } from '../contracts/common.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import {
  RecoveryJournal,
  type RecoveryJournalState,
  type RecoveryKind,
  type RecoveryRestoreContext,
} from './recovery.ts';

interface RowDraft {
  readonly text: string;
  readonly note: string;
  readonly source: null;
}

interface RecordedCall {
  readonly path: string;
  readonly opts: ApiRequestOptions | undefined;
  readonly body: Record<string, unknown> | undefined;
}

const tick = (): Promise<void> =>
  new Promise((resolve) => {
    setImmediate(resolve);
  });

function deferred<T = void>() {
  let resolve: (value: T | PromiseLike<T>) => void = () => undefined;
  let reject: (reason?: unknown) => void = () => undefined;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { resolve, reject, promise };
}

const data = (text: string): RowDraft => ({ text, note: '', source: null });

function makeHttpError(message: string, status: number): HttpError {
  const err: HttpError = new Error(message);
  err.status = status;
  return err;
}

function setup(
  override?: (
    path: string,
    opts: ApiRequestOptions | undefined,
    body: Record<string, unknown> | undefined,
  ) => unknown,
  options: { readonly kind?: RecoveryKind } = {},
) {
  const dom = installDom();
  const calls: RecordedCall[] = [];
  const statuses: RecoveryJournalState[] = [];
  const restores: { readonly records: Record<string, RowDraft>; readonly meta: RecoveryRestoreContext }[] = [];
  let revision = 0;

  const api: ApiClient = async <T = unknown>(
    path: string,
    opts?: ApiRequestOptions,
  ): Promise<T> => {
    const body =
      typeof opts?.body === 'string' ? (JSON.parse(opts.body) as Record<string, unknown>) : undefined;
    calls.push({ path, opts, body });
    if (override) {
      const result = await override(path, opts, body);
      if (result !== undefined) return result as T;
    }
    if (opts?.method === 'POST') {
      const res: unknown = { revision: ++revision };
      return res as T;
    }
    const empty: unknown = { revision: 0, records: {} };
    return empty as T;
  };

  const journal = new RecoveryJournal<RowDraft>({
    kind: options.kind ?? 'calculator',
    api,
    session: {
      id: 'recovery-page-0000000001',
      nativeLaunch: false,
      restoring: false,
      cacheCleared: false,
      ready: Promise.resolve(),
      apply: (h) => h,
      returnHomeAfterCacheReset: () => undefined,
    },
    onRestore: (records, meta) => restores.push({ records, meta }),
    onStatus: (state) => statuses.push(state),
  });

  return {
    journal,
    calls,
    statuses,
    restores,
    storage: dom.sessionStorage,
    restore: () => {
      journal.dispose();
      dom.restore();
    },
  };
}

test('recovery loads input without evaluation and preserves typing while the load is in flight', async () => {
  const gate = deferred<{ revision: number; records: Record<string, RowDraft> }>();
  const h = setup((_path, opts) => (!opts ? gate.promise : undefined));
  try {
    await tick();
    h.journal.set('0', data('fresh input'));
    gate.resolve({ revision: 7, records: { '0': data('old formula'), '1': data('=SUM(A:A)') } });
    await h.journal.ready;
    assert.equal(h.journal.records.get('0')?.text, 'fresh input');
    assert.equal(h.restores[0]?.records['0'], undefined);
    assert.equal(h.restores[0]?.records['1']?.text, '=SUM(A:A)');
    assert.equal(h.calls.length, 1);
    assert.equal(h.journal.revision, 7);
    assert.equal(h.journal.dirty, true);
  } finally {
    h.restore();
  }
});

test('old durable acknowledgements cannot clear newer input and saves stay serialized', async () => {
  const gate = deferred();
  let running = 0;
  let max = 0;
  let rev = 0;
  const h = setup(async (_path, opts) => {
    if (opts) {
      running++;
      max = Math.max(max, running);
      if (rev === 0) await gate.promise;
      running--;
      return { revision: ++rev };
    }
    return undefined;
  });
  try {
    await h.journal.ready;
    h.journal.set('0', data('sent'));
    const save = h.journal.flush();
    await tick();
    assert.equal(h.journal.flush(), save);
    h.journal.set('0', data('newer'));
    gate.resolve();
    await save;
    assert.equal(h.calls.length, 3);
    const changes1 = h.calls[1]?.body?.changes as { data: RowDraft }[] | undefined;
    const changes2 = h.calls[2]?.body?.changes as { data: RowDraft }[] | undefined;
    assert.equal(changes1?.[0]?.data.text, 'sent');
    assert.equal(changes2?.[0]?.data.text, 'newer');
    assert.equal(h.journal.dirty, false);
    assert.equal(h.journal.revision, 2);
    assert.equal(max, 1);
  } finally {
    h.restore();
  }
});

test('a lost acknowledgement retries the exact transaction before saving later edits', async () => {
  let fail = true;
  let revision = 0;
  const seen = new Set<string>();
  const h = setup((_path, opts, body) => {
    if (!opts) return undefined;
    const reqId = typeof body?.requestId === 'string' ? body.requestId : '';
    if (!seen.has(reqId)) {
      seen.add(reqId);
      revision++;
    }
    if (fail) {
      fail = false;
      throw new Error('connection lost after sync');
    }
    return { revision };
  });
  try {
    await h.journal.ready;
    h.journal.set('0', data('first'));
    assert.equal(await h.journal.flush(), false);
    const original = h.calls[1]?.opts?.body;
    assert.equal(h.journal.dirty, true);
    assert.match(h.journal.error, /connection lost/);
    h.journal.set('0', data('newer'));
    assert.equal(await h.journal.flush(), true);
    assert.equal(h.calls[2]?.opts?.body, original);
    assert.equal(h.calls[3]?.body?.revision, 1);
    const changes3 = h.calls[3].body.changes as { data: RowDraft }[] | undefined;
    assert.equal(changes3?.[0]?.data.text, 'newer');
    assert.equal(revision, 2);
  } finally {
    h.restore();
  }
});

test('conflict stops retries and a fork preserves both journals and the latest page input', async () => {
  let first = true;
  const h = setup((_path, opts, body) => {
    if (opts && first) {
      first = false;
      throw makeHttpError('another page saved', 409);
    }
    if (opts) return { revision: Number(body?.revision ?? 0) + 1 };
    return undefined;
  });
  try {
    await h.journal.ready;
    const original = h.journal.owner;
    h.journal.set('0', data('keep this'));
    await h.journal.flush();
    assert.equal(h.journal.blocked, true);
    assert.equal(h.journal.dirty, true);
    assert.equal(await h.journal.retry(), false);
    assert.equal(h.calls.length, 2);
    assert.equal(await h.journal.fork(), true);
    assert.notEqual(h.journal.owner, original);
    assert.equal(h.calls[2]?.body?.revision, 0);
    const changes2 = h.calls[2].body.changes as { data: RowDraft }[] | undefined;
    assert.equal(changes2?.[0]?.data.text, 'keep this');
    assert.equal(h.storage.getItem(h.journal.ownerKey), h.journal.owner);
    assert.equal(h.journal.dirty, false);
  } finally {
    h.restore();
  }
});

test('corrupt stored data never replaces current page input and can be saved as a new owner', async () => {
  let fail = true;
  const h = setup((_path, opts, body) => {
    if (!opts && fail) {
      throw makeHttpError('checksum mismatch', 422);
    }
    if (opts) return { revision: Number(body?.revision ?? 0) + 1 };
    return undefined;
  });
  try {
    assert.equal(await h.journal.ready, false);
    h.journal.set('0', data('still editable'));
    assert.equal(await h.journal.flush(), false);
    assert.equal(h.restores.length, 0);
    fail = false;
    assert.equal(await h.journal.fork(), true);
    assert.equal(h.journal.records.get('0')?.text, 'still editable');
  } finally {
    h.restore();
  }
});

test('import creates an independent copy, keeps same-cell current drafts and does not submit analysis', async () => {
  const h = setup(
    (path, opts, body) => {
      if (path.includes('previous-owner')) {
        return { revision: 9, records: { '0': data('old overlap'), '1': data('restored') } };
      }
      if (opts) return { revision: Number(body?.revision ?? 0) + 1 };
      return undefined;
    },
    { kind: 'analysis' },
  );
  try {
    await h.journal.ready;
    h.journal.set('0', data('local'));
    await h.journal.flush();
    const original = h.journal.owner;
    const result = await h.journal.importCopy({
      owner: 'previous-owner-00000001',
      updated: '',
      bytes: 0,
    });
    assert.equal(result.count, 1);
    assert.equal(result.skipped, 1);
    assert.equal(result.saved, true);
    assert.notEqual(h.journal.owner, original);
    assert.equal(h.journal.records.get('0')?.text, 'local');
    assert.equal(h.restores.at(-1)?.records['1']?.text, 'restored');
    assert.ok(h.calls.every((call) => call.path.startsWith('/api/recovery')));
  } finally {
    h.restore();
  }
});

test('Unicode and escaped input is batched by UTF-8 JSON bytes and unloading sends a bounded keepalive', async () => {
  const h = setup();
  try {
    await h.journal.ready;
    for (let i = 0; i < 20; i++) h.journal.set(String(i), data('文😀\n"'.repeat(1000)));
    await h.journal.flush();
    const writes = h.calls.filter((c) => c.opts !== undefined);
    assert.ok(writes.length > 1);
    for (const call of writes) {
      assert.ok(
        Buffer.byteLength(typeof call.opts?.body === 'string' ? call.opts.body : '', 'utf8') <
          60 * 1024,
      );
    }
    h.journal.set('0', data('latest'));
    const ev = new Event('beforeunload', { cancelable: true });
    window.dispatchEvent(ev);
    await tick();
    assert.equal(ev.defaultPrevented, true);
    assert.equal(h.calls.at(-1)?.opts?.keepalive, true);
  } finally {
    h.restore();
  }
});

test('deleted input remains a tombstone until a durable acknowledgement, and oversized rows remain dirty', async () => {
  const h = setup();
  try {
    await h.journal.ready;
    h.journal.set('0', data('value'));
    await h.journal.flush();
    h.journal.set('0', null);
    assert.equal(h.journal.dirty, true);
    await h.journal.flush();
    const changes = h.calls.at(-1)?.body?.changes as { data: RowDraft | null }[] | undefined;
    assert.equal(changes?.[0]?.data, null);
    assert.equal(h.journal.records.size, 0);
    h.journal.set('1', data('x'.repeat(70000)));
    assert.equal(await h.journal.flush(), false);
    assert.equal(h.journal.dirty, true);
    assert.match(h.journal.error, /过长/);
    h.journal.set('1', data('shorter'));
    assert.equal(h.journal.blocked, false);
    assert.equal(await h.journal.flush(), true);
    assert.equal(h.journal.dirty, false);
  } finally {
    h.restore();
  }
});
