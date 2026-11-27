import { decodeSheetViewport } from './viewport-response.ts';
import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { ActivityHandle, ActivityService, HttpError, SessionService } from '../contracts/common.ts';
import type { WorkbookStatusResponse } from '../contracts/workbook.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import { formatBytes } from './display-format.ts';
import { createTransport } from './transport.ts';

interface RecordedFetch {
  readonly url: string;
  readonly init: RequestInit | undefined;
}

class MockXHR {
  public static instances: MockXHR[] = [];
  public method = '';
  public url = '';
  public readonly headers = new Map<string, string>();
  public body: unknown = null;
  public status = 200;
  public responseText = '';
  public readonly upload: {
    onprogress: ((event: ProgressEvent) => void) | null;
    onload: (() => void) | null;
  } = { onprogress: null, onload: null };
  public onload: (() => void) | null = null;
  public onerror: (() => void) | null = null;
  public onabort: (() => void) | null = null;

  constructor() {
    MockXHR.instances.push(this);
  }

  open(method: string, url: string): void {
    this.method = method;
    this.url = url;
  }

  setRequestHeader(name: string, value: string): void {
    this.headers.set(name.toLowerCase(), value);
  }

  send(body: unknown): void {
    this.body = body;
  }
}

test('createTransport waits for session readiness, injects CSRF/session headers, strips activity option, and always ends activity', async () => {
  const dom = installDom();
  const origFetch = globalThis.fetch;
  try {
    let releaseSession: (() => void) | undefined;
    const session: SessionService = {
      cacheCleared: false,
      nativeLaunch: false,
      restoring: false,
      id: 'tab-session-1',
      ready: new Promise<void>((resolve) => {
        releaseSession = resolve;
      }),
      returnHomeAfterCacheReset: () => undefined,
      apply: (headers) => {
        headers.set('X-ExcelLens-Session', 'tab-session-1');
        return headers;
      },
    };

    const events: string[] = [];
    const activity: ActivityService = {
      begin: () => ({
        stage: () => undefined,
        report: () => undefined,
        job: () => undefined,
        onCancel: () => undefined,
        pause: () => undefined,
        resume: () => undefined,
        paintIfDue: async () => undefined,
        end: () => undefined,
      }),
      request: (url) => {
        events.push(`start:${url}`);
        return {
          stage: () => undefined,
          report: () => undefined,
          job: () => undefined,
          onCancel: () => undefined,
          pause: () => undefined,
          resume: () => undefined,
          paintIfDue: async () => undefined,
          end: () => {
            events.push(`end:${url}`);
          },
        };
      },
    };

    const calls: RecordedFetch[] = [];
    Reflect.set(globalThis, 'fetch', async (url: string, init?: RequestInit) => {
      events.push(`fetch:${url}`);
      calls.push({ url, init });
      return new Response(JSON.stringify({ ok: true }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      });
    });

    const transport = createTransport({ session, activity, token: 'csrf-secret' });
    const pending = transport.post(
      '/api/ping',
      { a: 1 },
      { activity: true },
    );
    await new Promise<void>((resolve) => setImmediate(resolve));
    assert.deepEqual(events, ['start:/api/ping']);
    assert.equal(calls.length, 0, 'fetch must wait until session.ready resolves');

    releaseSession?.();
    const res = await pending;
    assert.deepEqual(res, { ok: true });
    assert.deepEqual(events, ['start:/api/ping', 'fetch:/api/ping', 'end:/api/ping']);

    const sent = calls[0];
    assert.ok(sent?.init);
    assert.equal(sent.init.cache, 'no-store');
    assert.equal('activity' in sent.init, false, 'internal activity option must not leak to fetch');
    const headers = sent.init.headers as Headers;
    assert.equal(headers.get('X-ExcelLens-Token'), 'csrf-secret');
    assert.equal(headers.get('X-ExcelLens-Session'), 'tab-session-1');
    assert.equal(headers.get('Content-Type'), 'application/json');

    Reflect.set(globalThis, 'fetch', async () => {
      throw new Error('network down');
    });
    await assert.rejects(() => transport.api('/api/fail'), /network down/);
    assert.equal(events.at(-1), 'end:/api/fail');
  } finally {
    Reflect.set(globalThis, 'fetch', origFetch);
    dom.restore();
  }
});

test('createTransport auto-unpacks compact wire rows and supports text, blob, and raw response modes', async () => {
  const dom = installDom();
  const origFetch = globalThis.fetch;
  try {
    Reflect.set(globalThis, 'fetch', async (url: string) => {
      if (url === '/api/sheet') {
        return new Response(
          JSON.stringify({
            sheet: 'S1', dimension: 'A1:B2', totalRowsOriginal: 1, filterId: '', columns: null, hyperlinkError: '',
            offset: 0,
            limit: 10,
            totalRows: 1,
            filtered: false,
            maxRow: 1,
            maxCol: 2,
            startCol: 1,
            colCount: 2,
            compact: 1,
            rows: [{ n: 2, c: [{ c: 1, v: '00042', t: 's' }] }],
          }),
          { status: 200 },
        );
      }
      if (url === '/api/text') return new Response('plain-body', { status: 200 });
      if (url === '/api/blob') return new Response('blob-bytes', { status: 200 });
      return new Response('raw-resp', { status: 202 });
    });

    const transport = createTransport({ token: 'tok' });
    const unpacked = await transport.api('/api/sheet', undefined, decodeSheetViewport);
    assert.equal(unpacked.rows.length, 1);
    const firstRow = unpacked.rows[0];
    assert.ok(firstRow);
    const firstCell = firstRow.cells[0];
    assert.ok(firstCell);
    assert.equal(firstRow.number, 2);
    assert.equal(firstCell.raw, '00042');
    assert.equal(firstCell.ref, 'A2');

    const text = await transport.api('/api/text', {}, 'text');
    assert.equal(text, 'plain-body');

    const blob = await transport.api('/api/blob', {}, 'blob');
    assert.equal(await blob.text(), 'blob-bytes');

    const rawResp = await transport.api('/api/raw', {}, 'response');
    assert.equal(rawResp.status, 202);
  } finally {
    Reflect.set(globalThis, 'fetch', origFetch);
    dom.restore();
  }
});

test('createTransport redirects on storage-recovery header and propagates structured HTTP status errors', async () => {
  const dom = installDom();
  const origFetch = globalThis.fetch;
  try {
    const replaced: string[] = [];
    Reflect.set(window, 'location', {
      pathname: '/workspace',
      search: '',
      replace: (target: string) => {
        replaced.push(target);
      },
    });
    Reflect.set(globalThis, 'fetch', async (url: string) => {
      if (url === '/api/recover') {
        return new Response(JSON.stringify({ error: '需要恢复本机数据' }), {
          status: 503,
          headers: { 'X-ExcelLens-Recovery': 'required' },
        });
      }
      return new Response('not-json', { status: 502 });
    });

    const transport = createTransport({ token: 'tok' });
    await assert.rejects(
      () => transport.api('/api/recover'),
      (err: unknown) => {
        const httpErr = err as HttpError;
        return httpErr.message === '需要恢复本机数据' && httpErr.status === 503;
      },
    );
    assert.deepEqual(replaced, ['/workspace']);

    await assert.rejects(
      () => transport.api('/api/bad-gateway'),
      (err: unknown) => {
        const httpErr = err as HttpError;
        return httpErr.message === '请求失败（HTTP 502）' && httpErr.status === 502;
      },
    );
  } finally {
    Reflect.set(globalThis, 'fetch', origFetch);
    dom.restore();
  }
});

test('createTransport.upload streams progress stages, attaches auth headers, and rejects invalid/error/aborted replies', async () => {
  const dom = installDom();
  const origXHR = globalThis.XMLHttpRequest;
  MockXHR.instances = [];
  try {
    Reflect.set(globalThis, 'XMLHttpRequest', MockXHR);
    const session: SessionService = {
      cacheCleared: false,
      nativeLaunch: false,
      restoring: false,
      id: 'sess-9',
      ready: Promise.resolve(),
      returnHomeAfterCacheReset: () => undefined,
      apply: (headers) => {
        headers.set('X-ExcelLens-Session', 'sess-9');
        return headers;
      },
    };
    const transport = createTransport({ session, token: 'tok-upload' });
    const stages: string[] = [];
    const reports: string[] = [];
    const activity: ActivityHandle = {
      stage: (text) => {
        stages.push(text);
      },
      report: (_done, _total, _stage, detail) => {
        reports.push(detail ?? '');
      },
      job: () => undefined,
      onCancel: () => undefined,
      pause: () => undefined,
      resume: () => undefined,
      paintIfDue: async () => undefined,
      end: () => undefined,
    };
    const file = new File(['a,b\n1,2\n'], 'ledger.csv', { type: 'text/csv' });

    const okPromise = transport.upload(file, activity, formatBytes);
    const xhr1 = MockXHR.instances.at(-1);
    assert.ok(xhr1);
    assert.equal(xhr1.method, 'POST');
    assert.equal(xhr1.url, '/api/open');
    assert.equal(xhr1.headers.get('x-excellens-token'), 'tok-upload');
    assert.equal(xhr1.headers.get('x-excellens-session'), 'sess-9');
    const progressEvent = Object.assign(new Event('progress'), {
      lengthComputable: true,
      loaded: 1024,
      total: 2048,
    }) as ProgressEvent;
    xhr1.upload.onprogress?.(progressEvent);
    xhr1.upload.onload?.();
    xhr1.status = 200;
    xhr1.responseText = JSON.stringify({ opened: true, fileName: 'ledger.csv', book: 'file', workbookKey: 'fingerprint', sheetCount: 1, activeTab: 0, fileSize: file.size, sheets: [{name: '数据'}], source: {format: 'csv', readOnly: true} });
    xhr1.onload?.();
    const opened: WorkbookStatusResponse = await okPromise;
    assert.equal(opened.opened, true);
    assert.deepEqual(stages, ['正在读取文件', '正在解析文件']);
    assert.deepEqual(reports, ['1 KB / 2 KB']);

    const archivePromise = transport.upload(file, null, formatBytes);
    const xhr2 = MockXHR.instances.at(-1);
    assert.ok(xhr2);
    xhr2.status = 200;
    xhr2.responseText = JSON.stringify({ opened: false, archiveOnly: true, archive: { id:'zip', name:'test.zip', format:'zip', count:2, total:2, depth:0, notice:'' } });
    xhr2.onload?.();
    assert.equal((await archivePromise).archiveOnly, true);

    const errPromise = transport.upload(file, null, formatBytes);
    const xhr3 = MockXHR.instances.at(-1);
    assert.ok(xhr3);
    xhr3.status = 413;
    xhr3.responseText = JSON.stringify({ error: '文件超出容量限制' });
    xhr3.onload?.();
    await assert.rejects(() => errPromise, /文件超出容量限制/);

    const badJsonPromise = transport.upload(file, null, formatBytes);
    const xhr4 = MockXHR.instances.at(-1);
    assert.ok(xhr4);
    xhr4.status = 200;
    xhr4.responseText = 'not-json';
    xhr4.onload?.();
    await assert.rejects(() => badJsonPromise, /无法打开文件（HTTP 200）/);

    const abortPromise = transport.upload(file, null, formatBytes);
    MockXHR.instances.at(-1)?.onabort?.();
    await assert.rejects(() => abortPromise, /读取已取消/);

    const netPromise = transport.upload(file, null, formatBytes);
    MockXHR.instances.at(-1)?.onerror?.();
    await assert.rejects(() => netPromise, /本机文件读取失败/);
  } finally {
    Reflect.set(globalThis, 'XMLHttpRequest', origXHR);
    dom.restore();
  }
});

test('a truncated JSON success is rejected before a caller can confirm the write', async (t) => {
  t.mock.method(globalThis, 'fetch', () => Promise.resolve(new Response('{"revision":')));
  const transport = createTransport({ token: 'test' });
  await assert.rejects(transport.post('/api/test-write', {}), /JSON/);
});
