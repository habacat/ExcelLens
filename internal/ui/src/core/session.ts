/**
 * Per-tab browser session negotiation, heartbeat lease renewal, and reload handover.
 * sessionStorage identifies the tab across reloads; BroadcastChannel prevents
 * duplicate tabs from sharing a single lease.
 */
import type { SessionService } from '../contracts/common.ts';
import { errorMessage } from './display-format.ts';

interface TabProbeMessage {
  readonly kind: 'probe';
  readonly id: string;
  readonly nonce: string;
}

interface TabOccupiedMessage {
  readonly kind: 'occupied';
  readonly id: string;
  readonly nonce: string;
  readonly target: string;
}

type TabChannelMessage = TabProbeMessage | TabOccupiedMessage;

function isTabChannelMessage(value: unknown): value is TabChannelMessage {
  if (typeof value !== 'object' || value === null) return false;
  const obj = value as Record<string, unknown>;
  if (typeof obj.id !== 'string' || typeof obj.nonce !== 'string') return false;
  if (obj.kind === 'probe') return true;
  if (obj.kind === 'occupied' && typeof obj.target === 'string') return true;
  return false;
}

interface ErrorPayload {
  readonly error?: string;
}

function readErrorPayload(value: unknown): string {
  if (typeof value === 'object' && value !== null && 'error' in value) {
    const err = (value as ErrorPayload).error;
    if (typeof err === 'string') return err;
  }
  return '';
}

const SESSION_KEY = 'ExcelLens.tabSession.v1';
const GENERATION_KEY = `${SESSION_KEY}.generation`;

function isValidId(value: string | null | undefined): value is string {
  return typeof value === 'string' && /^[A-Za-z0-9_-]{20,64}$/.test(value);
}

function makeID(): string {
  if (typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID();
  }
  return Array.from(crypto.getRandomValues(new Uint8Array(20)), (x) =>
    x.toString(16).padStart(2, '0'),
  ).join('');
}

function readStorage(name: string): string {
  try {
    return sessionStorage.getItem(name) ?? '';
  } catch {
    return '';
  }
}

function saveStorage(name: string, value: string): void {
  try {
    sessionStorage.setItem(name, value);
  } catch {
    // Storage may be unavailable in restricted browser contexts.
  }
}

export interface SessionTimers {
  setTimeout(fn: () => void, ms: number): number;
  clearTimeout(id: number): void;
  setInterval(fn: () => void, ms: number): number;
  clearInterval(id: number): void;
}

const DEFAULT_SESSION_TIMERS: SessionTimers = {
  setTimeout: (fn, ms) => window.setTimeout(fn, ms),
  clearTimeout: (id) => window.clearTimeout(id),
  setInterval: (fn, ms) => window.setInterval(fn, ms),
  clearInterval: (id) => window.clearInterval(id),
};

export function createBrowserSession(timers: SessionTimers = DEFAULT_SESSION_TIMERS): SessionService {
  const url = new URL(window.location.href);
  const launch = url.searchParams.get('session');
  const nativeLaunch = isValidId(launch);
  const background = nativeLaunch && Boolean(url.searchParams.get('job'));
  const stored = readStorage(SESSION_KEY);
  let id = nativeLaunch ? launch : isValidId(stored) ? stored : makeID();
  saveStorage(SESSION_KEY, id);
  if (nativeLaunch) {
    url.searchParams.delete('session');
    history.replaceState(history.state, '', url.pathname + url.search + url.hash);
  }

  interface SessionInternalState {
    page: string;
    channel: BroadcastChannel | null;
    negotiation: Promise<void>;
    finishProbe: (() => void) | null;
    attaching: Promise<void> | null;
    heartbeat: number | undefined;
    generation: number;
    attached: boolean;
    requested: boolean;
    away: boolean;
    resumed: boolean;
    failure: string;
    cacheCleared: boolean;
  }

  const state: SessionInternalState = {
    page: '',
    channel: null,
    negotiation: Promise.resolve(),
    finishProbe: null,
    attaching: null,
    heartbeat: undefined,
    generation: 0,
    attached: false,
    requested: false,
    away: false,
    resumed: false,
    failure: '',
    cacheCleared: false,
  };

  function negotiate(isNative: boolean): Promise<void> {
    state.page = makeID();
    const previous = Number(readStorage(GENERATION_KEY));
    state.generation =
      Number.isSafeInteger(previous) && previous < Number.MAX_SAFE_INTEGER
        ? Math.max(Date.now(), previous + 1)
        : Date.now();
    saveStorage(GENERATION_KEY, String(state.generation));
    try {
      state.channel = new BroadcastChannel('ExcelLens.workbookTabs.v1');
    } catch {
      state.channel = null;
    }
    if (!state.channel) return Promise.resolve();
    const activeChannel = state.channel;
    return new Promise<void>((resolve) => {
      let claimed = isNative;
      let timer: number | undefined;
      const done = (): void => {
        if (timer !== undefined) timers.clearTimeout(timer);
        claimed = true;
        resolve();
      };
      state.finishProbe = done;
      const probe = (): void => {
        const msg: TabProbeMessage = { kind: 'probe', id, nonce: state.page };
        activeChannel.postMessage(msg);
        if (timer !== undefined) timers.clearTimeout(timer);
        timer = timers.setTimeout(done, 125);
      };
      const rotate = (): void => {
        id = makeID();
        saveStorage(SESSION_KEY, id);
        probe();
      };
      activeChannel.onmessage = (event: MessageEvent): void => {
        if (!isTabChannelMessage(event.data)) return;
        const data = event.data;
        if (data.id !== id || data.nonce === state.page) return;
        if (data.kind === 'probe') {
          if (claimed || state.page < data.nonce) {
            const reply: TabOccupiedMessage = {
              kind: 'occupied',
              id,
              nonce: state.page,
              target: data.nonce,
            };
            activeChannel.postMessage(reply);
          } else {
            rotate();
          }
        } else if (data.target === state.page && !claimed) {
          rotate();
        }
      };
      if (claimed) done();
      else probe();
    });
  }

  function apply(headers: Headers, session = id, documentID = state.page): Headers {
    headers.set('X-ExcelLens-Session', session);
    headers.set('X-ExcelLens-Page', documentID);
    const cacheEpoch = document.documentElement.dataset.cacheEpoch ?? '';
    headers.set('X-ExcelLens-Cache-Epoch', cacheEpoch);
    return headers;
  }

  function control(
    action: 'attach' | 'heartbeat' | 'detach',
    session: string,
    documentID: string,
  ): Promise<Response> {
    const headers = apply(new Headers(), session, documentID);
    const tokenMeta = document.querySelector('meta[name="app-token"]');
    const token = tokenMeta instanceof HTMLMetaElement ? tokenMeta.content : '';
    headers.set('X-ExcelLens-Token', token);
    if (action === 'attach') {
      headers.set('X-ExcelLens-Generation', String(state.generation));
      if (background && !state.resumed) headers.set('X-ExcelLens-Background', '1');
      if (state.resumed) headers.set('X-ExcelLens-Resume', '1');
    }
    return fetch(`/api/session/${action}`, {
      method: 'POST',
      headers,
      cache: 'no-store',
      keepalive: action === 'detach',
    });
  }

  async function checkLease(response: Response): Promise<void> {
    if (response.ok) return;
    const raw: unknown = await response.json().catch(() => ({}));
    const msg = readErrorPayload(raw);
    throw new Error(msg || `无法连接页面会话（HTTP ${response.status}）`);
  }

  const isAway = (): boolean => state.away;

  async function renew(): Promise<void> {
    if (!state.attached || isAway() || state.failure) return;
    const current = state.page;
    try {
      const response = await control('heartbeat', id, current);
      if (response.status === 410 || response.status === 409) {
        await checkLease(response);
      }
    } catch (error: unknown) {
      if (isAway() || current !== state.page || error instanceof TypeError) return;
      state.failure = errorMessage(error);
      if (state.heartbeat !== undefined) timers.clearInterval(state.heartbeat);
      window.dispatchEvent(
        new CustomEvent<string>('excellens:session-expired', { detail: state.failure }),
      );
    }
  }

  async function attach(current: string): Promise<void> {
    await state.negotiation;
    if (isAway() || current !== state.page) throw new Error('页面已关闭');
    const session = id;
    state.requested = true;
    const response = await control('attach', session, current);
    await checkLease(response);
    if (isAway() || current !== state.page) {
      void control('detach', session, current).catch(() => {
        // Best-effort cleanup during teardown.
      });
      throw new Error('页面已关闭');
    }
    state.attached = true;
    state.heartbeat = timers.setInterval(() => {
      void renew();
    }, 30000);
  }

  state.negotiation = negotiate(nativeLaunch);

  const service: SessionService = Object.freeze({
    get cacheCleared(): boolean {
      return state.cacheCleared;
    },
    returnHomeAfterCacheReset(): void {
      if (state.cacheCleared) return;
      state.cacheCleared = true;
      saveStorage(SESSION_KEY, '');
      saveStorage(GENERATION_KEY, '');
      window.location.replace(window.location.pathname);
    },
    nativeLaunch,
    get restoring(): boolean {
      return isValidId(stored) && id === stored;
    },
    get id(): string {
      return id;
    },
    get ready(): Promise<void> {
      if (state.failure) return Promise.reject(new Error(state.failure));
      if (!state.attaching) {
        const current = state.page;
        state.attaching = attach(current).catch((error: unknown) => {
          if (current === state.page) state.attaching = null;
          throw error;
        });
      }
      return state.attaching;
    },
    apply,
  });

  window.addEventListener('pagehide', () => {
    state.away = true;
    state.finishProbe?.();
    state.channel?.close();
    if (state.heartbeat !== undefined) timers.clearInterval(state.heartbeat);
    if (state.requested) {
      void control('detach', id, state.page).catch(() => {
        // Ignore network failure while leaving page.
      });
    }
    state.attached = false;
    state.requested = false;
  });

  window.addEventListener('pageshow', (event: PageTransitionEvent) => {
    if (!event.persisted) return;
    state.away = false;
    state.resumed = true;
    state.attaching = null;
    state.failure = '';
    state.negotiation = negotiate(false);
    service.ready.catch((error: unknown) => {
      window.dispatchEvent(
        new CustomEvent<string>('excellens:session-expired', { detail: errorMessage(error) }),
      );
    });
  });

  document.addEventListener('visibilitychange', () => {
    if (!document.hidden) void renew();
  });

  return service;
}
