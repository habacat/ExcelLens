import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from '../test-helpers/dom-mock.ts';
import { createActivityService } from './activity.ts';

interface ScheduledTimer {
  readonly fn: () => void;
  readonly time: number;
}

interface RecordedAnimation {
  readonly options: KeyframeAnimationOptions;
  cancelled: boolean;
  cancel(): void;
}

function setupPage(compositor = false) {
  const dom = installDom();
  let now = 0;
  let sequence = 0;
  const timers = new Map<number, ScheduledTimer>();
  const animations: RecordedAnimation[] = [];

  const host = dom.register('tabActivity', 'div');
  host.hidden = true;
  const labelEl = document.createElement('span');
  labelEl.className = 'activity-label';
  const valueEl = document.createElement('span');
  valueEl.className = 'activity-value';
  const cancelBtn = document.createElement('button');
  cancelBtn.className = 'activity-cancel';
  host.append(labelEl, valueEl, cancelBtn);

  if (compositor) {
    Reflect.set(
      host,
      'animate',
      (_frames: Keyframe[], options: KeyframeAnimationOptions): RecordedAnimation => {
        const animation: RecordedAnimation = {
          options,
          cancelled: false,
          cancel() {
            this.cancelled = true;
          },
        };
        animations.push(animation);
        return animation;
      },
    );
  }

  const service = createActivityService(
    { now: () => now },
    {
      setTimeout: (fn, ms = 0) => {
        const id = ++sequence;
        timers.set(id, { fn, time: now + ms });
        return id;
      },
      clearTimeout: (id) => {
        if (id !== undefined) timers.delete(id);
      },
      requestAnimationFrame: (cb) => {
        const id = ++sequence;
        timers.set(id, { fn: () => cb(now), time: now });
        return id;
      },
      cancelAnimationFrame: (id) => {
        timers.delete(id);
      },
    },
  );

  function advance(ms: number): void {
    const end = now + ms;
    while (timers.size > 0) {
      const sorted = [...timers].sort((a, b) => a[1].time - b[1].time);
      const next = sorted[0];
      if (!next || next[1].time > end) break;
      timers.delete(next[0]);
      now = next[1].time;
      next[1].fn();
    }
    now = end;
  }

  return {
    activity: service,
    host,
    advance,
    timers,
    animations,
    label: () => labelEl.textContent,
    value: () => valueEl.textContent,
    restore: () => dom.restore(),
  };
}

test('waiting for a paste choice hides progress and excludes time spent in the dialog', () => {
  const p = setupPage();
  try {
    const task = p.activity.begin('正在粘贴');
    p.advance(600);
    assert.equal(p.host.hidden, false);
    task.pause();
    p.advance(60000);
    assert.equal(p.host.hidden, true);
    task.resume();
    p.advance(499);
    assert.equal(p.host.hidden, true);
    p.advance(1);
    assert.equal(p.host.hidden, false);
    assert.match(p.value(), /^约 1[0-9]%$/);
    task.end();
    task.resume();
    assert.equal(p.host.hidden, true);
  } finally {
    p.restore();
  }
});

test('activity waits 500 ms across work phases and clears its only timer on completion', () => {
  const p = setupPage();
  try {
    const quick = p.activity.begin('打开');
    p.advance(499);
    assert.equal(p.host.hidden, true);
    quick.end();
    p.advance(1);
    assert.equal(p.host.hidden, true);
    assert.equal(p.timers.size, 0);

    const work = p.activity.begin('打开');
    p.advance(310);
    work.stage('准备首屏', 60, 90);
    p.advance(189);
    assert.equal(p.host.hidden, true);
    p.advance(1);
    assert.equal(p.host.hidden, false);
    assert.equal(p.label(), '准备首屏');
    assert.match(p.value(), /^约 /);
    work.report(32, 64, '索引', '32 MB / 64 MB');
    assert.equal(p.value(), '32 MB / 64 MB');
    work.stage('显示单元格', 95, 99);
    p.advance(90000);
    assert.match(p.value(), /^约 9[5-8]%$/);
    work.end();
    work.end();
    assert.equal(p.host.hidden, true);
    assert.equal(p.timers.size, 0);
  } finally {
    p.restore();
  }
});

test('a workbook render primes a delayed compositor layer and drops fast or superseded reveals', () => {
  const p = setupPage(true);
  try {
    const old = p.activity.begin('文件甲', { priority: 2 });
    assert.equal(p.animations[0]?.options.delay, 500);
    assert.equal(p.host.getAttribute('aria-hidden'), 'true');
    p.advance(300);
    old.end();
    assert.equal(p.animations[0].cancelled, true);

    const next = p.activity.begin('文件乙', { priority: 2 });
    p.advance(499);
    assert.equal(p.host.getAttribute('aria-hidden'), 'true');
    p.advance(1);
    assert.equal(p.host.hasAttribute('aria-hidden'), false);
    assert.equal(p.label(), '文件乙');
    next.end();
    assert.equal(p.host.hidden, true);
  } finally {
    p.restore();
  }
});

test('overlapping work stays with its document and an aborted or stale handle cannot hide a different job', () => {
  const a = setupPage();
  try {
    const abort = new AbortController();
    const first = a.activity.begin('筛选');
    const second = a.activity.begin('打开', { priority: 2, signal: abort.signal });
    a.advance(501);
    assert.equal(a.label(), '打开');

    const low = a.activity.request('/api/cell?ref=B2', {});
    assert.ok(low);
    a.advance(501);
    assert.equal(a.label(), '打开');

    first.job({ stage: '筛选', scanned: 12, total: 40 });
    abort.abort();
    assert.equal(a.label(), '筛选');
    assert.equal(a.value(), '30%');
    second.stage('旧结果');
    second.end();
    assert.equal(a.label(), '筛选');
    first.end();
    assert.equal(a.label(), '正在读取单元格');
    low.end();
    assert.equal(a.host.hidden, true);
    assert.equal(a.activity.request('/api/search/status?id=7', {}), null);
    assert.equal(a.activity.request('/api/tasks', {}), null);
  } finally {
    a.restore();
  }
});
