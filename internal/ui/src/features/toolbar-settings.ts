/**
 * Application-wide toolbar layout preference synchronization shared between
 * browser tabs and the native tray via event-driven long-polling and BroadcastChannel.
 */
import type { SessionService } from '../contracts/common.ts';
import { errorMessage } from '../core/display-format.ts';
import { byId, readAppToken } from '../core/dom.ts';
import { getOverlayMenus, type AnchorBox, type OverlayMenus } from '../core/overlays.ts';
import type { AdaptiveToolbar } from './toolbar.ts';

const WATCH_NAME = 'ExcelLens.interface.v1';

interface InterfaceSettings {
  readonly ribbon?: boolean;
  readonly cacheEpoch?: string | number;
  readonly error?: string;
}

export interface ToolbarSettingsDependencies {
  readonly ribbon: AdaptiveToolbar;
  readonly session?: SessionService | null;
  readonly menus?: OverlayMenus | null;
  readonly token?: string;
}

export interface ToolbarSettingsController {
  dispose(): void;
}

function isInterfaceSettings(value: unknown): value is InterfaceSettings {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return false;
  return true;
}

function pause(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise<void>((resolve) => {
    const finish = (): void => {
      clearTimeout(timer);
      signal.removeEventListener('abort', finish);
      resolve();
    };
    const timer = setTimeout(finish, ms);
    signal.addEventListener('abort', finish, { once: true });
  });
}

export function initToolbarSettings(
  deps: ToolbarSettingsDependencies,
): ToolbarSettingsController | null {
  const bar = deps.ribbon;
  const menus = deps.menus ?? getOverlayMenus();
  const menu = byId('toolbarLayoutMenu');
  const toggle = byId('toolbarLayoutToggle');
  if (!menu || !toggle) return null;

  const headers: Record<string, string> = {
    'X-ExcelLens-Token': deps.token ?? readAppToken(),
  };
  const epoch = document.documentElement.dataset.cacheEpoch ?? '';
  let channel: BroadcastChannel | null = null;
  let controller: AbortController | null = null;
  let saving = false;
  let revision = 0;

  function apply(settings: InterfaceSettings): void {
    if (String(settings.cacheEpoch ?? '') !== epoch) {
      deps.session?.returnHomeAfterCacheReset();
      return;
    }
    if (typeof settings.ribbon === 'boolean') {
      bar.setLayout(settings.ribbon);
      toggle?.setAttribute('aria-checked', String(settings.ribbon));
    }
  }

  async function read(query: string, signal?: AbortSignal): Promise<InterfaceSettings | null> {
    const response = await fetch(`/api/interface${query}`, {
      headers,
      signal,
      cache: 'no-store',
    });
    if (response.headers.get('X-ExcelLens-Recovery') === 'required') {
      window.location.replace(window.location.pathname);
      return null;
    }
    if (!response.ok) throw new Error('无法同步工具栏布局');
    const raw: unknown = await response.json();
    return isInterfaceSettings(raw) ? raw : null;
  }

  async function syncNow(signal?: AbortSignal): Promise<void> {
    const ticket = revision;
    try {
      const settings = await read('', signal);
      if (settings && !signal?.aborted && ticket === revision && !saving) {
        apply(settings);
      }
    } catch {
      // The next activation or watch retries.
    }
  }

  async function watchLoop(signal: AbortSignal): Promise<void> {
    const isAborted = (): boolean => signal.aborted;
    let delay = 400;
    while (!isAborted() && !deps.session?.cacheCleared) {
      const ticket = revision;
      const ribbon = bar.ribbon;
      const query = new URLSearchParams({ wait: '1', ribbon: String(ribbon), epoch });
      try {
        const settings = await read(`?${query.toString()}`, signal);
        if (!settings || isAborted()) return;
        if (!saving && ticket === revision) {
          if (settings.ribbon !== ribbon || String(settings.cacheEpoch ?? '') !== epoch) {
            channel?.postMessage(settings);
          }
          apply(settings);
        }
        delay = 400;
      } catch {
        if (isAborted()) return;
        await pause(delay, signal);
        delay = Math.min(8000, delay * 2);
      }
    }
  }

  function stopWatch(): void {
    controller?.abort();
    controller = null;
    channel?.close();
    channel = null;
  }

  function watch(): void {
    if (document.hidden || deps.session?.cacheCleared) {
      stopWatch();
      return;
    }
    if (controller) return;
    controller = new AbortController();
    const { signal } = controller;
    try {
      channel = new BroadcastChannel(WATCH_NAME);
      channel.onmessage = (event: MessageEvent): void => {
        if (!saving && isInterfaceSettings(event.data) && typeof event.data.ribbon === 'boolean') {
          ++revision;
          apply(event.data);
        }
      };
    } catch {
      channel = null;
    }
    const locks = navigator.locks as LockManager | undefined;
    if (channel && locks) {
      // Keep one long poll per origin so idle tabs leave HTTP connections for workbook requests.
      void syncNow(signal);
      void locks.request(WATCH_NAME, { signal }, () => watchLoop(signal)).catch(() => {
        // Ignore aborted lock acquisition.
      });
    } else {
      void watchLoop(signal);
    }
  }

  const onContextMenu = (event: MouseEvent): void => {
    const target = event.target instanceof Element ? event.target : null;
    if (!target?.closest('.appbar,.formula-toolbar button')) return;
    event.preventDefault();
    const trigger = target.closest('button,[tabindex]') ?? byId('homeBtn');
    if (!(trigger instanceof HTMLElement)) return;
    const point: HTMLElement | AnchorBox =
      event.clientX || event.clientY
        ? {
            left: event.clientX,
            right: event.clientX,
            top: event.clientY,
            bottom: event.clientY,
            width: 0,
            height: 0,
          }
        : trigger;
    toggle.setAttribute('aria-checked', String(bar.ribbon));
    menus?.show(trigger, menu, point);
  };

  const onToggleClick = (): void => {
    if (saving) return;
    saving = true;
    ++revision;
    void (async () => {
      try {
        const response = await fetch('/api/interface', {
          method: 'POST',
          headers: { ...headers, 'Content-Type': 'application/json' },
          body: JSON.stringify({ ribbon: !bar.ribbon }),
        });
        const raw: unknown = await response.json();
        const settings = isInterfaceSettings(raw) ? raw : {};
        if (!response.ok) {
          throw new Error(settings.error || '无法保存工具栏布局');
        }
        apply(settings);
        channel?.postMessage(settings);
        watch();
      } catch (error: unknown) {
        document.dispatchEvent(
          new CustomEvent<string>('toolbar-setting-error', { detail: errorMessage(error) }),
        );
      } finally {
        saving = false;
      }
    })();
  };

  const onFocus = (): void => {
    if (!document.hidden && !saving) void syncNow(controller?.signal);
  };

  document.addEventListener('contextmenu', onContextMenu);
  toggle.addEventListener('click', onToggleClick);
  window.addEventListener('focus', onFocus);
  window.addEventListener('pageshow', watch);
  window.addEventListener('pagehide', stopWatch);
  document.addEventListener('visibilitychange', watch);
  watch();

  return {
    dispose(): void {
      stopWatch();
      document.removeEventListener('contextmenu', onContextMenu);
      toggle.removeEventListener('click', onToggleClick);
      window.removeEventListener('focus', onFocus);
      window.removeEventListener('pageshow', watch);
      window.removeEventListener('pagehide', stopWatch);
      document.removeEventListener('visibilitychange', watch);
    },
  };
}
