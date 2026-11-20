/* Each document owns its foreground work. A task can span several requests
 * and polling intervals without sharing a clock or progress with other tabs. */
(() => {
  'use strict';
  const delay = 500;
  const clamp = n => Math.max(0, Math.min(100, n));

  class ActivityQueue {
    constructor(paint, clock = performance, timers = window) {
      this.paint = paint;
      this.clock = clock;
      this.timers = timers;
      this.tasks = new Set();
      this.timer = null;
      this.visible = null;
    }
    begin(label, {priority = 1, signal, slot} = {}) {
      const start = this.clock.now();
      const state = {label, priority, slot, start, phaseStart:start, from:0, to:100, value:null, detail:'', cancel:null};
      const end = () => {
        if (!this.tasks.delete(state)) return;
        signal?.removeEventListener('abort', end);
        this.refresh();
      };
      if (!signal?.aborted) {
        this.tasks.add(state);
        signal?.addEventListener('abort', end, {once:true});
        this.refresh();
      }
      const task = {
        pause: () => {
          if (!this.tasks.has(state) || state.pausedAt !== undefined) return;
          state.pausedAt = this.clock.now();
          this.refresh();
        },
        resume: () => {
          if (!this.tasks.has(state) || state.pausedAt === undefined) return;
          const now = this.clock.now();
          state.start = now;
          state.phaseStart += now - state.pausedAt;
          delete state.pausedAt;
          this.refresh();
        },
        stage: (name, from = 0, to = 100) => {
          Object.assign(state, {label:name, from, to, phaseStart:this.clock.now(), value:null, detail:'', cancel:null});
          if (this.visible === state) this.refresh();
        },
        report: (done, total, name, detail = '') => {
          if (name && name !== state.label) {
            state.label = name;
            state.phaseStart = this.clock.now();
            state.value = null;
            state.detail = '';
          }
          if (Number.isFinite(done) && Number.isFinite(total) && total > 0) {
            state.value = clamp(done / total * 100);
            state.detail = detail;
          }
          if (this.visible === state) this.refresh();
        },
        onCancel: action => {
          state.cancel = action;
          if (this.visible === state) this.refresh();
        },
        paintIfDue: () => {
          if (!this.tasks.has(state) || state.pausedAt !== undefined || this.clock.now() - state.start < delay || this.timers.document?.visibilityState !== 'visible') return;
          this.refresh();
          // A synchronous layout can cross the deadline and finish in the
          // same event-loop turn. Give the compositor a frame before teardown.
          return new Promise(resolve => {
            const win = this.timers;
            let first = 0, second = 0;
            const done = () => {
              win.clearTimeout(fallback);
              win.cancelAnimationFrame(first);
              win.cancelAnimationFrame(second);
              resolve();
            };
            const fallback = win.setTimeout(done, 100);
            first = win.requestAnimationFrame(() => { second = win.requestAnimationFrame(done); });
          });
        },
        end
      };
      task.job = result => {
        if (result.totalBytes > 0) task.report(result.doneBytes, result.totalBytes, result.stage);
        else if (result.totalRows > 0) task.report(result.doneRows, result.totalRows, result.stage);
        else task.report(result.scanned ?? result.done ?? result.progress, result.total, result.stage);
      };
      return task;
    }
    refresh() {
      this.timers.clearTimeout(this.timer);
      this.timer = null;
      const now = this.clock.now();
      let chosen = null, preparing = null, next = Infinity;
      for (const task of this.tasks) {
        if (task.pausedAt !== undefined) continue;
        const remaining = task.start + delay - now;
        if (remaining > 0) {
          next = Math.min(next, remaining);
          // Prime the compositor before workbook layout can occupy the main
          // thread. Short opens remove the invisible layer before its delay.
          if (task.priority >= 2) preparing = task;
        }
        else if (!chosen || task.priority >= chosen.priority) chosen = task;
      }
      chosen ||= preparing;
      if (chosen) {
        const remaining = Math.max(0, chosen.start + delay - now);
        const estimated = chosen.value === null;
        const elapsed = now + remaining - chosen.phaseStart;
        const fraction = estimated ? .94 * (1 - Math.exp(-elapsed / 6500)) : chosen.value / 100;
        const percent = Math.floor(chosen.from + (chosen.to - chosen.from) * fraction);
        const approximate = estimated || chosen.from !== 0 || chosen.to !== 100;
        const value = chosen.detail || (approximate ? '约 ' : '') + percent + '%';
        this.paint({owner:chosen, remaining, estimated, label:chosen.label, value, percent, cancel:chosen.cancel});
        if (estimated && !remaining) next = Math.min(next, 250);
      } else if (this.visible) this.paint(null);
      this.visible = chosen;
      if (Number.isFinite(next)) this.timer = this.timers.setTimeout(() => this.refresh(), next);
    }
  }

  // Polls belong to their enclosing job. Passive startup, cache prefetches
  // and shared history refreshes must never put a busy badge on an idle tab.
  const quiet = new Set(['status','recent','tasks','recovery','bookmarks','images','version','formats','search/history',
    'heartbeat','analysis/workspace','archive/job','preview','styles','view']);
  const labels = {
    edits:'正在处理编辑副本', analysis:'正在读取分析数据', export:'正在生成导出文件', archive:'正在读取压缩包',
    sheet:'正在读取单元格', cell:'正在读取单元格', range:'正在读取选区', filter:'正在筛选数据',
    search:'正在搜索', profile:'正在统计数据', inspect:'正在检查数据', calculator:'正在计算',
    clipboard:'正在准备复制内容', print:'正在准备打印', printing:'正在准备打印', layout:'正在读取工作表布局',
    navigate:'正在定位单元格', catalogue:'正在读取工作表目录', reconcile:'正在核对数据', autofit:'正在测量列宽',
    'data-rules':'正在读取数据规则','row-view':'正在更新可见行',recipes:'正在处理分析方案'
  };
  const editLabels = {start:'正在准备编辑副本',save:'正在生成副本',commit:'正在应用修改',open:'正在打开副本',download:'正在读取副本'};
  function requestLabel(url, method = 'GET') {
    const path = url.split('?')[0].replace(/^\/api\//, '');
    if (path.endsWith('/status') || path.endsWith('/cancel') || path.endsWith('/clear') ||
        (path.endsWith('/start') && path !== 'edits/start') ||
        path === 'archive/job' || (method === 'GET' && quiet.has(path))) return '';
    const [group, action] = path.split('/');
    return (group === 'edits' && editLabels[action]) || labels[group] || '正在处理请求';
  }

  const host = document.getElementById('tabActivity');
  if (!host.animate) host.classList?.add('activity-static');
  const label = host.querySelector('.activity-label'), value = host.querySelector('.activity-value');
  const cancel = host.querySelector('button');
  let cancelCurrent = null, owner = null, reveal = null;
  const dialogs = [];
  function mount() {
    const modal = dialogs.filter(node => node.matches(':modal')).at(-1) || document.querySelector('dialog:modal');
    const home = !modal && !document.fullscreenElement && document.body.classList?.contains('home-view');
    const slot = owner?.slot && document.getElementById(owner.slot);
    const local = slot && !slot.closest('[hidden],dialog:not([open])') && (!modal || modal.contains(slot));
    const anchor = local ? slot : home && (document.querySelector('.recent-working .recent-meta') || document.getElementById('homeActivitySlot'));
    if (anchor) {
      if (host.hidePopover && host.matches(':popover-open')) host.hidePopover();
      host.removeAttribute('popover');
      if (host.parentElement !== anchor) anchor.append(host);
      return;
    }
    const parent = modal || document.fullscreenElement || document.body;
    host.setAttribute('popover','manual');
    if (host.parentElement !== parent) parent.append(host);
    if (host.showPopover && !host.matches(':popover-open')) host.showPopover();
  }
  const queue = new ActivityQueue(status => {
    if (!status) {
      if (host.hidePopover && host.matches(':popover-open')) host.hidePopover();
      host.hidden = true;
      // Recent rows are replaced on refresh. Keep the dormant live region in
      // the document so a failed open or a new selection can use it again.
      if (host.parentElement !== document.body) document.body.append(host);
      cancelCurrent = null;
      owner = null;
      reveal?.cancel();
      reveal = null;
      return;
    }
    const changed = owner !== status.owner;
    const entering = changed && (!owner || status.remaining > 0);
    owner = status.owner;
    if (status.estimated) host.setAttribute('data-estimate','');
    else host.removeAttribute('data-estimate');
    if (status.remaining) host.setAttribute('aria-hidden','true');
    else host.removeAttribute('aria-hidden');
    if (label.textContent !== status.label) label.textContent = status.label;
    if (value.textContent !== status.value) {
      value.textContent = status.value;
      value.setAttribute('aria-valuetext',status.value);
    }
    value.setAttribute('aria-valuenow',String(status.percent));
    cancelCurrent = status.cancel;
    cancel.hidden = !cancelCurrent;
    if (host.hidden || changed || owner.slot) { host.hidden = false; mount(); }
    if (!status.remaining && reveal?.effect) {
      const threshold = reveal.effect.getTiming().delay;
      if (reveal.currentTime < threshold) reveal.currentTime = threshold;
    }
    if (changed) {
      reveal?.cancel();
      reveal = null;
      if (entering && host.animate && !window.matchMedia('(prefers-reduced-motion: reduce)').matches) {
        reveal = host.animate([{opacity:0},{opacity:1}], {delay:status.remaining, duration:120, easing:'ease-out', fill:'backwards'});
      }
    }
  });
  cancel.addEventListener('click', () => cancelCurrent?.());
  document.addEventListener('toggle', event => {
    const node = event.target;
    if (node.tagName !== 'DIALOG') return;
    const old = dialogs.indexOf(node);
    if (old >= 0) dialogs.splice(old, 1);
    if (node.open) dialogs.push(node);
    if (!host.hidden) mount();
  }, true);
  document.addEventListener('fullscreenchange', () => { if (!host.hidden) mount(); });
  if (typeof MutationObserver !== 'undefined') {
    const placement = new MutationObserver(() => { if (!host.hidden) mount(); });
    placement.observe(document.body,{attributes:true,attributeFilter:['class']});
    placement.observe(document.getElementById('recentList'),{childList:true});
  }
  document.addEventListener('visibilitychange', () => { if (!document.hidden) queue.refresh(); });
  window.ExcelLensActivity = {begin:queue.begin.bind(queue), request(url, options) {
    if (options.activity === false) return null;
    const name = requestLabel(url, options.method);
    return name ? queue.begin(name, {priority:0, signal:options.signal}) : null;
  }};
})();
