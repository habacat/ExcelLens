import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { ApiRequestOptions } from '../contracts/common.ts';
import type { TaskCheckpointInfo } from '../contracts/features.ts';
import type { RecoveredTaskStart } from '../grid/sheet-controller.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import {
  TaskRecoveryModel,
  type TaskRecoveryAction,
  type TaskRecoveryRequestPayload,
} from './task-recovery-model.ts';
import { TaskRecovery } from './task-recovery.ts';

interface Deferred<T> {
  readonly promise: Promise<T>;
  readonly resolve: (value: T) => void;
}

function deferred<T = unknown>(): Deferred<T> {
  let resolve: (value: T) => void = () => undefined;
  const promise = new Promise<T>((yes) => {
    resolve = yes;
  });
  return { promise, resolve };
}

function setupTaskRecovery() {
  const dom = installDom({ fromIndexHtml: true });
  const timers = new Map<number, () => void>();
  let timerID = 0;

  const s = { opened: true, book: 'book', workbookKey: 'hash', sheetEpoch: 1 };
  const calls: { readonly url: string; readonly body: Record<string, unknown> | undefined }[] = [];
  const adopted: [RecoveredTaskStart, TaskCheckpointInfo][] = [];
  const notices: string[] = [];
  const item: TaskCheckpointInfo = {
    id: 'a'.repeat(32),
    revision: '9007199254740993',
    driver: 'source-search',
    kind: 'query',
    title: '',
    file: '账目.csv',
    path: 'C:/source/账目.csv',
    book: 'book',
    workbookKey: 'hash',
    sheet: '数据',
    query: '金额',
    mode: 'value',
    scope: 'current',
    updated: '2026-11-17T00:00:00Z',
    busy: false,
    complete: false,
    backup: false,
    error: '',
  };

  let apiImpl: (url: string, opts?: ApiRequestOptions) => Promise<unknown> = async (url, opts) => {
    calls.push({
      url,
      body:
        typeof opts?.body === 'string'
          ? (JSON.parse(opts.body) as Record<string, unknown>)
          : undefined,
    });
    return url === '/api/tasks' ? { items: [item] } : { jobId: 'own-job', operation: 'search' };
  };
  let openSourceImpl: (
    payload: TaskRecoveryRequestPayload,
    current: () => boolean,
  ) => Promise<void> = async () => undefined;
  let canResumeImpl: (
    item: TaskCheckpointInfo,
    action: Exclude<TaskRecoveryAction, ''>,
  ) => boolean = () => true;
  let adoptImpl: (start: RecoveredTaskStart, item: TaskCheckpointInfo) => Promise<boolean> = async (
    start,
    task,
  ) => {
    adopted.push([start, task]);
    return true;
  };

  const center = new TaskRecovery({
    state: s,
    maximum: 32,
    api: async <T = unknown>(url: string, opts?: ApiRequestOptions): Promise<T> =>
      (await apiImpl(url, opts)) as T,
    toast: (msg) => notices.push(msg),
    openDialog: (id) => {
      const dlg = document.getElementById(id);
      if (dlg instanceof HTMLDialogElement) dlg.showModal();
    },
    context: () => JSON.stringify(s),
    openSource: (payload, current) => openSourceImpl(payload, current),
    canResume: (task, action) => canResumeImpl(task, action),
    adopt: (start, task) => adoptImpl(start, task),
    timers: {
      setTimeout: (fn) => {
        const id = ++timerID;
        timers.set(id, fn);
        return id;
      },
      clearTimeout: (id) => {
        if (typeof id === 'number') timers.delete(id);
      },
    },
  });

  const welcome = document.getElementById('welcome');
  assert.ok(welcome);
  welcome.hidden = true;

  return {
    center,
    s,
    calls,
    adopted,
    notices,
    item,
    timers,
    setApi: (fn: typeof apiImpl) => {
      apiImpl = fn;
    },
    setOpenSource: (fn: typeof openSourceImpl) => {
      openSourceImpl = fn;
    },
    setCanResume: (fn: typeof canResumeImpl) => {
      canResumeImpl = fn;
    },
    setAdopt: (fn: typeof adoptImpl) => {
      adoptImpl = fn;
    },
    restore: () => {
      center.dispose();
      dom.restore();
    },
  };
}

test('home shows only actionable checkpoints and rechecks a running task when it settles', async () => {
  const h = setupTaskRecovery();
  try {
    const button = document.getElementById('taskRecoveryHome');
    const welcome = document.getElementById('welcome');
    assert.ok(button);
    assert.ok(welcome);
    welcome.hidden = false;
    button.hidden = true;

    let items: TaskCheckpointInfo[] = [];
    h.setApi(async (url) => {
      assert.equal(url, '/api/tasks');
      return { items };
    });
    const cases: readonly (Partial<TaskCheckpointInfo> | null)[] = [
      null,
      { error: '记录损坏', revision: '0' },
      { driver: 'unknown' },
    ];
    for (const row of cases) {
      items = row ? [{ ...h.item, ...row }] : [];
      await h.center.refreshHome();
      assert.equal(button.hidden, true);
      assert.equal(h.timers.size, 0);
    }

    items = [{ ...h.item, busy: true }];
    await h.center.refreshHome();
    assert.equal(button.hidden, true);
    assert.equal(h.timers.size, 1);

    items = [{ ...h.item, backup: false }];
    const scheduled = [...h.timers.values()][0];
    assert.ok(scheduled);
    h.timers.clear();
    scheduled();
    await new Promise<void>((resolve) => setImmediate(resolve));
    assert.equal(button.hidden, false);
    assert.equal(h.timers.size, 0);

    items = [{ ...h.item, complete: true, driver: 'source-export' }];
    await h.center.refreshHome();
    assert.equal(button.hidden, false);

    h.setApi(async () => {
      throw new Error('unavailable');
    });
    await h.center.refreshHome();
    assert.equal(button.hidden, true);
    assert.equal((document.getElementById('taskRecoveryDialog') as HTMLDialogElement).open, false);
    assert.equal(h.adopted.length, 0);
  } finally {
    h.restore();
  }
});

test('late home reads cannot resurrect the button after the last recoverable task is removed', async () => {
  const h = setupTaskRecovery();
  try {
    const gate = deferred<{ readonly items: readonly TaskCheckpointInfo[] }>();
    const button = document.getElementById('taskRecoveryHome');
    const welcome = document.getElementById('welcome');
    assert.ok(button);
    assert.ok(welcome);
    welcome.hidden = false;

    h.setApi(() => gate.promise);
    const first = h.center.refreshHome();
    h.setApi(async () => ({
      items: [h.item, { ...h.item, id: 'b'.repeat(32), error: '不可恢复' }],
    }));
    await h.center.open();
    assert.equal(button.hidden, false);
    const target = h.center.items[0];
    assert.ok(target);
    await h.center.run(target, 'remove');
    assert.equal(button.hidden, true);
    gate.resolve({ items: [h.item] });
    await first;
    assert.equal(button.hidden, true);
  } finally {
    h.restore();
  }
});

test('opening the task directory only lists metadata and preserves precise revisions', async () => {
  const h = setupTaskRecovery();
  try {
    await h.center.open();
    assert.deepEqual(
      h.calls.map((x) => x.url),
      ['/api/tasks'],
    );
    const target = h.center.items[0];
    assert.ok(target);
    await h.center.run(target, 'resume');
    assert.ok(h.calls[1]?.body);
    assert.equal(h.calls[1].body.revision, '9007199254740993');
    assert.equal(h.calls[1].body.book, 'book');
    assert.equal(h.adopted.length, 1);
  } finally {
    h.restore();
  }
});

test('task actions follow source identity, busy/corrupt state and completed output', () => {
  const h = setupTaskRecovery();
  try {
    assert.equal(TaskRecoveryModel.action(h.item, h.s), 'resume');
    h.s.book = 'copy-at-another-path';
    assert.equal(TaskRecoveryModel.action(h.item, h.s), 'open');
    assert.equal(TaskRecoveryModel.action({ ...h.item, busy: true }, h.s), '');
    assert.equal(TaskRecoveryModel.action({ ...h.item, error: '来源变化' }, h.s), '');
    h.s.book = 'book';
    assert.equal(
      TaskRecoveryModel.action(
        { ...h.item, driver: 'source-export', complete: true },
        h.s,
      ),
      'download',
    );
    assert.equal(TaskRecoveryModel.action({ ...h.item, driver: 'future' }, h.s), '');
  } finally {
    h.restore();
  }
});

test('invalid or duplicate catalog entries fail without replacing accepted tasks', async () => {
  const h = setupTaskRecovery();
  try {
    await h.center.open();
    const accepted = h.center.items;
    const badItems: readonly Record<string, unknown>[] = [
      { ...h.item, revision: Number('9007199254740993') },
      { ...h.item, revision: '18446744073709551616' },
      { ...h.item, busy: 'false' },
    ];
    for (const item of badItems) {
      assert.throws(() => TaskRecoveryModel.items({ items: [item] }, 32));
    }
    assert.throws(() => TaskRecoveryModel.items({ items: [h.item, h.item] }, 32));
    assert.throws(() => TaskRecoveryModel.items({ items: [h.item] }, 0));

    h.setApi(async () => ({ items: [{ ...h.item, revision: 1 }] }));
    await h.center.load();
    assert.equal(h.center.items, accepted);
    assert.match(document.getElementById('taskRecoveryStatus')?.textContent ?? '', /任务/);
  } finally {
    h.restore();
  }
});

test('a closed or switched view cancels only the late job created by its own resume', async () => {
  for (const reason of ['close', 'source', 'query'] as const) {
    const h = setupTaskRecovery();
    try {
      await h.center.open();
      const gate = deferred<{ readonly jobId: string; readonly operation: string }>();
      h.setApi((url, opts) => {
        h.calls.push({
          url,
          body:
            typeof opts?.body === 'string'
              ? (JSON.parse(opts.body) as Record<string, unknown>)
              : undefined,
        });
        return url === '/api/tasks/resume' ? gate.promise : Promise.resolve({});
      });
      const target = h.center.items[0];
      assert.ok(target);
      const pending = h.center.run(target, 'resume');
      if (reason === 'close') h.center.close();
      else if (reason === 'source') h.s.book = 'other';
      else h.s.sheetEpoch++;
      gate.resolve({ jobId: 'late-own-job', operation: 'search' });
      await pending;
      assert.equal(h.adopted.length, 0);
      assert.deepEqual(h.calls.at(-1), {
        url: '/api/search/cancel',
        body: { id: 'late-own-job' },
      });
      assert.equal(h.center.busy, false);
    } finally {
      h.restore();
    }
  }
});

test('another running intent prevents resume and failed adoption releases its own export', async () => {
  const h = setupTaskRecovery();
  try {
    await h.center.open();
    h.setCanResume(() => false);
    const firstItem = h.center.items[0];
    assert.ok(firstItem);
    await h.center.run(firstItem, 'resume');
    assert.equal(h.calls.length, 1);

    const exportItem: TaskCheckpointInfo = {
      ...firstItem,
      kind: 'export',
      driver: 'source-export',
      complete: true,
    };
    h.center.items = [exportItem];
    h.setCanResume(() => true);
    h.setAdopt(async () => false);
    h.setApi(async (url, opts) => {
      h.calls.push({
        url,
        body:
          typeof opts?.body === 'string'
            ? (JSON.parse(opts.body) as Record<string, unknown>)
            : undefined,
      });
      return { jobId: 'output-own-job', operation: 'export' };
    });
    await h.center.run(exportItem, 'download');
    assert.equal(h.calls.at(-1)?.url, '/api/export/cancel');
    assert.equal(h.calls.at(-1)?.body?.id, 'output-own-job');
  } finally {
    h.restore();
  }
});

test('explicit source opening never resumes, locks close during install and changes actions on success', async () => {
  const h = setupTaskRecovery();
  try {
    h.s.book = 'other';
    await h.center.open();
    const gate = deferred<undefined>();
    let openings = 0;
    h.setOpenSource(async (payload, current) => {
      openings++;
      assert.equal(payload.book, 'other');
      await gate.promise;
      assert.ok(current());
      h.s.book = 'book';
    });
    const target = h.center.items[0];
    assert.ok(target);
    const pending = h.center.run(target, 'open');
    h.center.close();
    assert.equal((document.getElementById('taskRecoveryDialog') as HTMLDialogElement).open, true);
    assert.equal(openings, 1);
    gate.resolve(undefined);
    await pending;
    assert.deepEqual(
      h.calls.map((x) => x.url),
      ['/api/tasks'],
    );
    assert.equal(TaskRecoveryModel.action(target, h.s), 'resume');
    assert.equal(h.center.opening, false);
  } finally {
    h.restore();
  }
});

test('stale list responses and old row handlers cannot replace or remove newer tasks', async () => {
  const h = setupTaskRecovery();
  try {
    const gate = deferred<{ readonly items: readonly TaskCheckpointInfo[] }>();
    h.setApi(() => gate.promise);
    const first = h.center.open();
    h.center.close();
    h.setApi(async () => ({ items: [{ ...h.item, id: 'b'.repeat(32) }] }));
    await h.center.open();
    gate.resolve({ items: [h.item] });
    await first;
    assert.equal(h.center.items[0]?.id, 'b'.repeat(32));
    const count = h.calls.length;
    await h.center.run(h.item, 'remove');
    assert.equal(h.calls.length, count);
  } finally {
    h.restore();
  }
});

test('removing a task uses its expected revision and leaves source state alone', async () => {
  const h = setupTaskRecovery();
  try {
    await h.center.open();
    const before = JSON.stringify(h.s);
    const target = h.center.items[0];
    assert.ok(target);
    await h.center.run(target, 'remove');
    assert.equal(h.calls.at(-1)?.url, '/api/tasks/discard');
    assert.equal(h.calls.at(-1)?.body?.revision, h.item.revision);
    assert.equal(h.center.items.length, 0);
    assert.equal(JSON.stringify(h.s), before);
  } finally {
    h.restore();
  }
});
