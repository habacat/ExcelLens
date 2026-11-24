/* sessionStorage identifies the tab across reloads. Each document negotiates
 * ownership before it attaches or sends workbook requests. */
(() => {
  'use strict';
  const key = 'ExcelLens.tabSession.v1';
  const generationKey = key + '.generation';
  const valid = value => /^[A-Za-z0-9_-]{20,64}$/.test(value || '');
  const makeID = () => crypto.randomUUID?.() || Array.from(crypto.getRandomValues(new Uint8Array(20)), x => x.toString(16).padStart(2, '0')).join('');
  const read = name => { try { return sessionStorage.getItem(name); } catch { return ''; } };
  const save = (name, value) => { try { sessionStorage.setItem(name, value); } catch {} };
  const url = new URL(window.location.href);
  const launch = url.searchParams.get('session');
  const background = valid(launch) && !!url.searchParams.get('job');
  const stored = read(key);
  let id = valid(launch) ? launch : valid(stored) ? stored : makeID();
  save(key, id);
  if (valid(launch)) {
    url.searchParams.delete('session');
    history.replaceState(history.state, '', url.pathname + url.search + url.hash);
  }

  let page, channel, negotiation, finishProbe, attaching, heartbeat, generation;
  let attached = false, requested = false, away = false, resumed = false, failure = '', cacheCleared = false;
  function negotiate(native) {
    page = makeID();
    const previous = Number(read(generationKey));
    generation = Number.isSafeInteger(previous) && previous < Number.MAX_SAFE_INTEGER ? Math.max(Date.now(), previous + 1) : Date.now();
    save(generationKey, String(generation));
    try { channel = new BroadcastChannel('ExcelLens.workbookTabs.v1'); } catch { channel = null; }
    if (!channel) return Promise.resolve();
    return new Promise(resolve => {
      let claimed = native, timer;
      finishProbe = () => { clearTimeout(timer); claimed = true; resolve(); };
      const probe = () => {
        channel.postMessage({kind:'probe', id, nonce:page});
        clearTimeout(timer);
        timer = setTimeout(finishProbe, 125);
      };
      const rotate = () => { id = makeID(); save(key, id); probe(); };
      channel.onmessage = ({data}) => {
        if (!data || data.id !== id || data.nonce === page) return;
        if (data.kind === 'probe') {
          if (claimed || page < data.nonce) channel.postMessage({kind:'occupied', id, nonce:page, target:data.nonce});
          else rotate();
        } else if (data.kind === 'occupied' && data.target === page && !claimed) rotate();
      };
      if (claimed) finishProbe(); else probe();
    });
  }

  function apply(headers, session = id, documentID = page) {
    headers.set('X-ExcelLens-Session', session);
    headers.set('X-ExcelLens-Page', documentID);
    headers.set('X-ExcelLens-Cache-Epoch', document.documentElement.dataset.cacheEpoch);
    return headers;
  }
  function control(action, session, documentID) {
    const headers = apply(new Headers(), session, documentID);
    headers.set('X-ExcelLens-Token', document.querySelector('meta[name="app-token"]').content);
    if (action === 'attach') {
      headers.set('X-ExcelLens-Generation', String(generation));
      if (background && !resumed) headers.set('X-ExcelLens-Background', '1');
      if (resumed) headers.set('X-ExcelLens-Resume', '1');
    }
    return fetch('/api/session/' + action, {method:'POST', headers, cache:'no-store', keepalive:action === 'detach'});
  }
  async function checkLease(response) {
    if (response.ok) return;
    const data = await response.json().catch(() => ({}));
    throw new Error(data.error || '无法连接页面会话（HTTP ' + response.status + '）');
  }
  async function renew() {
    if (!attached || away || failure) return;
    const current = page;
    try {
      const response = await control('heartbeat', id, current);
      if (response.status === 410 || response.status === 409) await checkLease(response);
    } catch (error) {
      if (away || current !== page || error instanceof TypeError) return;
      failure = error.message;
      clearInterval(heartbeat);
      window.dispatchEvent(new CustomEvent('excellens:session-expired', {detail:failure}));
    }
  }
  async function attach(current) {
    await negotiation;
    if (away || current !== page) throw new Error('页面已关闭');
    const session = id;
    requested = true;
    const response = await control('attach', session, current);
    await checkLease(response);
    if (away || current !== page) {
      void control('detach', session, current).catch(() => {});
      throw new Error('页面已关闭');
    }
    attached = true;
    heartbeat = setInterval(renew, 30000);
  }
  negotiation = negotiate(valid(launch));
  window.addEventListener('pagehide', () => {
    away = true;
    finishProbe?.();
    channel?.close();
    clearInterval(heartbeat);
    if (requested) void control('detach', id, page).catch(() => {});
    attached = requested = false;
  });
  window.addEventListener('pageshow', event => {
    if (!event.persisted) return;
    away = false;
    resumed = true;
    attaching = null;
    failure = '';
    negotiation = negotiate(false);
    window.ExcelLensSession.ready.catch(error => {
      window.dispatchEvent(new CustomEvent('excellens:session-expired', {detail:error.message}));
    });
  });
  document.addEventListener('visibilitychange', () => { if (!document.hidden) void renew(); });

  window.ExcelLensSession = Object.freeze({
    get cacheCleared() { return cacheCleared; },
    returnHomeAfterCacheReset() {
      if (cacheCleared) return;
      cacheCleared = true;
      window.location.replace(window.location.pathname);
    },
    nativeLaunch:valid(launch),
    get restoring() { return valid(stored) && id === stored; },
    get id() { return id; },
    get ready() {
      if (failure) return Promise.reject(new Error(failure));
      if (!attaching) {
        const current = page;
        attaching = attach(current).catch(error => { if (current === page) attaching = null; throw error; });
      }
      return attaching;
    },
    apply
  });
})();
