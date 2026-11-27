import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { SessionService } from '../contracts/common.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import { createBrowserSession } from './session.ts';

interface FetchRecord {
  readonly url: string;
  readonly options: RequestInit | undefined;
}

interface MockChannel {
  closed: boolean;
  onmessage: ((event: { readonly data: unknown }) => void) | null;
  postMessage(data: unknown): void;
  close(): void;
}

interface SessionPageHandle {
  readonly session: SessionService;
  readonly intervals: Map<number, () => unknown>;
  readonly values: Map<string, string>;
  readonly requests: FetchRecord[];
  firePageHide(): void;
  firePageShow(persisted: boolean): void;
  fireVisibilityChange(): void;
}

function createBus() {
  const channels: MockChannel[] = [];
  const queue: (() => void)[] = [];
  const timers = new Map<number, () => void>();
  let serial = 0;
  let timer = 0;

  const savedFetch = Object.getOwnPropertyDescriptor(globalThis, 'fetch');
  const savedBC = Object.getOwnPropertyDescriptor(globalThis, 'BroadcastChannel');
  const savedCrypto = Object.getOwnPropertyDescriptor(globalThis, 'crypto');
  const restores: (() => void)[] = [];

  function page(options: {
    readonly launch?: string;
    readonly stored?: string;
    readonly blocked?: boolean;
    readonly respond?: (url: string) => Promise<{ ok: boolean; status: number }> | { ok: boolean; status: number };
  } = {}): SessionPageHandle {
    const { launch = '', stored = '', blocked = false, respond } = options;
    const dom = installDom({
      url: `http://localhost/${launch ? `?session=${launch}` : ''}`,
      token: 'token',
      cacheEpoch: '3',
    });
    restores.push(() => dom.restore());

    const intervals = new Map<number, () => unknown>();
    const requests: FetchRecord[] = [];
    const values = new Map<string, string>([['ExcelLens.tabSession.v1', stored]]);

    const storageProxy = {
      getItem: (key: string): string | null => values.get(key) ?? null,
      setItem: (key: string, value: string): void => {
        values.set(key, value);
      },
      removeItem: (key: string): void => {
        values.delete(key);
      },
      clear: (): void => values.clear(),
    };
    Object.defineProperty(globalThis, 'sessionStorage', {
      value: storageProxy,
      configurable: true,
      writable: true,
    });

    class Channel implements MockChannel {
      closed = false;
      onmessage: ((event: { readonly data: unknown }) => void) | null = null;
      constructor() {
        if (blocked) throw new Error('blocked');
        channels.push(this);
      }
      postMessage(data: unknown): void {
        for (const peer of channels) {
          if (peer !== this && !peer.closed) {
            queue.push(() => peer.onmessage?.({ data }));
          }
        }
      }
      close(): void {
        this.closed = true;
      }
    }

    Object.defineProperty(globalThis, 'BroadcastChannel', {
      value: Channel,
      configurable: true,
      writable: true,
    });
    Object.defineProperty(globalThis, 'crypto', {
      value: { randomUUID: () => String(++serial).padStart(24, '0') },
      configurable: true,
      writable: true,
    });
    Object.defineProperty(globalThis, 'fetch', {
      value: async (input: string, init?: RequestInit) => {
        requests.push({ url: input, options: init });
        return respond ? respond(input) : { ok: true, status: 204 };
      },
      configurable: true,
      writable: true,
    });
    const winListeners = new Map<string, ((ev: Event) => void)[]>();
    const origWinAdd = Reflect.get(window, 'addEventListener') as (
      type: string,
      fn: (ev: Event) => void,
    ) => void;
    Reflect.set(window, 'addEventListener', (type: string, fn: (ev: Event) => void) => {
      const list = winListeners.get(type) ?? [];
      list.push(fn);
      winListeners.set(type, list);
      origWinAdd(type, fn);
    });

    const docListeners = new Map<string, ((ev: Event) => void)[]>();
    const origDocAdd = dom.document.addEventListener.bind(dom.document);
    dom.document.addEventListener = (type, fn, opts) => {
      if (typeof fn === 'function') {
        const list = docListeners.get(type) ?? [];
        list.push(fn);
        docListeners.set(type, list);
      }
      origDocAdd(type, fn, opts);
    };

    const session = createBrowserSession({
      setTimeout: (fn) => {
        const id = ++timer;
        timers.set(id, fn);
        return id;
      },
      clearTimeout: (id) => {
        timers.delete(id);
      },
      setInterval: (fn) => {
        const id = ++timer;
        intervals.set(id, fn);
        return id;
      },
      clearInterval: (id) => {
        intervals.delete(id);
      },
    });

    return {
      session,
      intervals,
      values,
      requests,
      firePageHide(): void {
        for (const fn of winListeners.get('pagehide') ?? []) fn(new Event('pagehide'));
      },
      firePageShow(persisted: boolean): void {
        const ev = new Event('pageshow');
        Object.defineProperty(ev, 'persisted', { value: persisted });
        for (const fn of winListeners.get('pageshow') ?? []) fn(ev);
      },
      fireVisibilityChange(): void {
        for (const fn of docListeners.get('visibilitychange') ?? []) {
          fn(new Event('visibilitychange'));
        }
      },
    };
  }

  function flush(): void {
    let count = 0;
    while (queue.length > 0) {
      if (++count > 1000) throw new Error('unstable session negotiation');
      queue.shift()?.();
    }
    for (const [id, fn] of [...timers]) {
      if (timers.delete(id)) fn();
    }
  }

  function restore(): void {
    for (const r of restores.reverse()) r();
    const entries: readonly (readonly [string, PropertyDescriptor | undefined])[] = [
      ['fetch', savedFetch],
      ['BroadcastChannel', savedBC],
      ['crypto', savedCrypto],
    ];
    for (const [key, desc] of entries) {
      if (desc) Object.defineProperty(globalThis, key, desc);
      else Reflect.deleteProperty(globalThis, key);
    }
  }

  return { page, flush, restore };
}

test('native launch owns its workbook and a duplicated tab negotiates a different session', async () => {
  const b = createBus();
  try {
    const id = 'native-session-1234567890';
    const native = b.page({ launch: id });
    await native.session.ready;
    const duplicate = b.page({ stored: id });
    b.flush();
    await duplicate.session.ready;
    assert.equal(native.session.id, id);
    assert.notEqual(duplicate.session.id, id);
    const headers = new Headers();
    duplicate.session.apply(headers);
    assert.equal(headers.get('X-ExcelLens-Session'), duplicate.session.id);
    assert.equal(headers.get('X-ExcelLens-Cache-Epoch'), '3');
  } finally {
    b.restore();
  }
});

test('simultaneous normal duplicates separate while a reload after pagehide preserves its session', async () => {
  const b = createBus();
  try {
    const id = 'stored-session-1234567890';
    const a = b.page({ stored: id });
    const c = b.page({ stored: id });
    b.flush();
    await Promise.all([a.session.ready, c.session.ready]);
    assert.notEqual(a.session.id, c.session.id);
    a.firePageHide();
    const reload = b.page({ stored: a.session.id });
    b.flush();
    await reload.session.ready;
    assert.equal(reload.session.id, a.session.id);
  } finally {
    b.restore();
  }
});

test('an unavailable BroadcastChannel does not prevent the page bootstrap', async () => {
  const b = createBus();
  try {
    const id = 'stored-session-1234567890';
    const a = b.page({ stored: id, blocked: true });
    await a.session.ready;
    assert.equal(a.session.id, id);
  } finally {
    b.restore();
  }
});

test('closing releases only the attached document and keeps the request authenticated', async () => {
  const b = createBus();
  try {
    const a = b.page({ blocked: true });
    await a.session.ready;
    a.firePageHide();
    assert.deepEqual(
      a.requests.map((x) => x.url),
      ['/api/session/attach', '/api/session/detach'],
    );
    const attach = a.requests[0]?.options;
    const detach = a.requests[1]?.options;
    assert.equal(detach?.keepalive, true);
    const attachHeaders = new Headers(attach?.headers);
    const detachHeaders = new Headers(detach.headers);
    for (const key of ['Token', 'Session', 'Page', 'Cache-Epoch']) {
      assert.equal(detachHeaders.get(`X-ExcelLens-${key}`), attachHeaders.get(`X-ExcelLens-${key}`));
    }
  } finally {
    b.restore();
  }
});

test('heartbeats stop while away and BFCache restoration claims a newer generation', async () => {
  const b = createBus();
  try {
    const a = b.page({ blocked: true });
    await a.session.ready;
    const heartbeat = [...a.intervals.values()][0];
    assert.ok(heartbeat);
    await heartbeat();
    a.fireVisibilityChange();
    const initial = new Headers(a.requests[0]?.options?.headers);
    assert.equal(a.requests.filter((x) => x.url.endsWith('heartbeat')).length, 2);
    a.firePageHide();
    assert.equal(a.intervals.size, 0);
    a.firePageShow(true);
    await a.session.ready;
    const renewed = new Headers(a.requests.at(-1)?.options?.headers);
    assert.equal(renewed.get('X-ExcelLens-Session'), initial.get('X-ExcelLens-Session'));
    assert.notEqual(renewed.get('X-ExcelLens-Page'), initial.get('X-ExcelLens-Page'));
    assert.equal(renewed.get('X-ExcelLens-Resume'), '1');
    assert.ok(
      Number(renewed.get('X-ExcelLens-Generation')) > Number(initial.get('X-ExcelLens-Generation')),
    );
  } finally {
    b.restore();
  }
});

test('closing while attach is in flight sends a release before the response', async () => {
  const b = createBus();
  try {
    let started: (() => void) | undefined;
    let finish: ((res: { ok: boolean; status: number }) => void) | undefined;
    const pending = new Promise<void>((resolve) => {
      started = resolve;
    });
    const a = b.page({
      blocked: true,
      respond: (url) => {
        if (url.endsWith('attach')) {
          started?.();
          return new Promise<{ ok: boolean; status: number }>((resolve) => {
            finish = resolve;
          });
        }
        return { ok: true, status: 204 };
      },
    });
    const ready = a.session.ready;
    await pending;
    a.firePageHide();
    assert.equal(a.requests.at(-1)?.url, '/api/session/detach');
    finish?.({ ok: true, status: 204 });
    await assert.rejects(ready, /页面已关闭/);
    assert.equal(a.intervals.size, 0);
  } finally {
    b.restore();
  }
});

test('a confirmed full reset opens a fresh home instead of resuming the retired tab', async () => {
  const b = createBus();
  try {
    const a = b.page({ stored: 'stored-session-1234567890', blocked: true });
    await a.session.ready;
    a.session.returnHomeAfterCacheReset();
    const stored = a.values.get('ExcelLens.tabSession.v1') ?? '';
    const home = b.page({ stored, blocked: true });
    assert.equal(stored, '');
    assert.equal(home.session.restoring, false);
    assert.notEqual(home.session.id, a.session.id);
  } finally {
    b.restore();
  }
});
