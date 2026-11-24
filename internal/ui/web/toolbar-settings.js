// The native tray and the page edit the same application-wide preference.
// Uses event-driven long-polling plus BroadcastChannel instead of fixed polling.
(() => {
  'use strict';
  const bar = window.ExcelLensRibbon, menus = window.ExcelLensOverlays.menus;
  const menu = document.getElementById('toolbarLayoutMenu'), toggle = document.getElementById('toolbarLayoutToggle');
  const headers = {'X-ExcelLens-Token':document.querySelector('meta[name="app-token"]').content};
  const epoch = document.documentElement.dataset.cacheEpoch;
  const watchName = 'ExcelLens.interface.v1';
  let channel, controller, saving = false, revision = 0;
  function apply(settings) {
    if (String(settings.cacheEpoch) !== epoch) {
      window.ExcelLensSession.returnHomeAfterCacheReset();
      return;
    }
    if (typeof settings.ribbon === 'boolean') {
      bar.setLayout(settings.ribbon);
      toggle.setAttribute('aria-checked', String(settings.ribbon));
    }
  }
  async function read(query, signal) {
    const response = await fetch('/api/interface'+query, {headers, signal, cache:'no-store'});
    if (response.headers.get('X-ExcelLens-Recovery') === 'required') {
      window.location.replace(window.location.pathname);
      return null;
    }
    if (!response.ok) throw Error('无法同步工具栏布局');
    return response.json();
  }
  async function syncNow(signal) {
    const ticket = revision;
    try {
      const settings = await read('', signal);
      if (settings && !signal?.aborted && ticket === revision && !saving) apply(settings);
    } catch { /* The next activation or watch retries. */ }
  }
  function pause(ms, signal) {
    return new Promise(resolve => {
      const finish = () => { clearTimeout(timer); signal.removeEventListener('abort', finish); resolve(); };
      const timer = setTimeout(finish, ms);
      signal.addEventListener('abort', finish, {once:true});
    });
  }
  async function watchLoop(signal) {
    let delay = 400;
    while (!signal.aborted && !window.ExcelLensSession?.cacheCleared) {
      const ticket = revision, ribbon = !!bar.ribbon;
      const query = new URLSearchParams({wait:'1', ribbon:String(ribbon), epoch});
      try {
        const settings = await read('?'+query, signal);
        if (!settings || signal.aborted) return;
        if (!saving && ticket === revision) {
          if (settings.ribbon !== ribbon || String(settings.cacheEpoch) !== epoch) channel?.postMessage(settings);
          apply(settings);
        }
        delay = 400;
      } catch {
        if (signal.aborted) return;
        await pause(delay, signal);
        delay = Math.min(8000, delay * 2);
      }
    }
  }
  function stopWatch() {
    controller?.abort(); controller = null;
    channel?.close(); channel = null;
  }
  function watch() {
    if (document.hidden || window.ExcelLensSession?.cacheCleared) { stopWatch(); return; }
    if (controller) return;
    controller = new AbortController();
    const {signal} = controller;
    try {
      channel = new BroadcastChannel(watchName);
      channel.onmessage = ({data}) => { if (!saving && typeof data?.ribbon === 'boolean') { ++revision; apply(data); } };
    } catch { channel = null; }
    if (channel && window.navigator?.locks) {
      // Keep one long poll per origin so idle tabs leave HTTP connections for workbook requests.
      void syncNow(signal);
      void window.navigator.locks.request(watchName, {signal}, () => watchLoop(signal)).catch(() => {});
    } else void watchLoop(signal);
  }
  document.addEventListener('contextmenu', event => {
    if (!event.target.closest('.appbar,.formula-toolbar button')) return;
    event.preventDefault();
    const trigger = event.target.closest('button,[tabindex]') || document.getElementById('homeBtn');
    const point = event.clientX || event.clientY ? {left:event.clientX,top:event.clientY,bottom:event.clientY} : trigger;
    toggle.setAttribute('aria-checked', String(bar.ribbon));
    menus.show(trigger, menu, point);
  });
  toggle.addEventListener('click', async () => {
    if (saving) return;
    saving = true;
    ++revision;
    try {
      const response = await fetch('/api/interface', {method:'POST', headers:{...headers,'Content-Type':'application/json'},body:JSON.stringify({ribbon:!bar.ribbon})});
      const settings = await response.json();
      if (!response.ok) throw Error(settings.error || '无法保存工具栏布局');
      apply(settings);
      channel?.postMessage(settings);
      watch();
    } catch (error) {
      document.dispatchEvent(new CustomEvent('toolbar-setting-error', {detail:error.message}));
    } finally { saving = false; }
  });
  window.addEventListener('focus', () => { if (!document.hidden && !saving) void syncNow(controller?.signal); });
  window.addEventListener('pageshow', watch);
  window.addEventListener('pagehide', stopWatch);
  document.addEventListener('visibilitychange', watch);
  watch();
})();
