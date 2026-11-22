// The native tray and the page edit the same application-wide preference.
(() => {
  'use strict';
  const bar = window.ExcelLensRibbon, menus = window.ExcelLensOverlays.menus;
  const menu = document.getElementById('toolbarLayoutMenu'), toggle = document.getElementById('toolbarLayoutToggle');
  const headers = {'X-ExcelLens-Token':document.querySelector('meta[name="app-token"]').content};
  const epoch = document.documentElement.dataset.cacheEpoch;
  let serial = 0, loading = false, saving = false, timer = 0;

  function apply(settings) {
    if (String(settings.cacheEpoch) !== epoch) {
      window.ExcelLensSession.returnHomeAfterCacheReset();
      return;
    }
    bar.setLayout(settings.ribbon);
    toggle.setAttribute('aria-checked', String(settings.ribbon));
  }
  async function sync() {
    if (document.hidden || loading || saving) return;
    const ticket = serial;
    loading = true;
    try {
      const response = await fetch('/api/interface', {headers, cache:'no-store'});
      const currentEpoch = response.headers.get('X-ExcelLens-Cache-Epoch');
      if (currentEpoch !== null && currentEpoch !== epoch) {
        window.ExcelLensSession.returnHomeAfterCacheReset();
        return;
      }
      if (response.ok) {
        const settings = await response.json();
        if (ticket === serial && typeof settings.ribbon === 'boolean') apply(settings);
      }
    } catch { /* The next activation retries after a suspended tab or disconnected service. */ }
    finally { loading = false; }
  }
  function watch() {
    clearInterval(timer);
    if (!document.hidden) {
      sync();
      timer = setInterval(sync, 4000);
    }
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
    ++serial;
    try {
      const response = await fetch('/api/interface', {method:'POST', headers:{...headers,'Content-Type':'application/json'},body:JSON.stringify({ribbon:!bar.ribbon})});
      const settings = await response.json();
      if (!response.ok) throw Error(settings.error || '无法保存工具栏布局');
      apply(settings);
    } catch (error) {
      document.dispatchEvent(new CustomEvent('toolbar-setting-error', {detail:error.message}));
    } finally { saving = false; }
  });
  window.addEventListener('focus', sync);
  window.addEventListener('pageshow', watch);
  window.addEventListener('pagehide', () => clearInterval(timer));
  document.addEventListener('visibilitychange', watch);
})();
