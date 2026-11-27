import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from '../test-helpers/dom-mock.ts';
import {
  buttonCommand,
  CommandPalette,
  windowStart,
  type CommandAction,
} from './command-palette.ts';

const tick = (): Promise<void> =>
  new Promise((resolve) => {
    setImmediate(resolve);
  });

function makeFixture(actions: readonly CommandAction[] = []) {
  const dom = installDom();
  const nodes = new Map<string, HTMLElement>();
  const closed: string[] = [];
  const errors: [string, string | undefined][] = [];
  const palette = new CommandPalette(actions, {
    node: (id) => nodes.get(id) ?? null,
    sheets: () => [],
    selectSheet: () => undefined,
    canOpen: () => true,
    openDialog: () => undefined,
    closeDialog: (id) => closed.push(id),
    toast: (msg, tone) => errors.push([msg, tone]),
  });
  return { palette, nodes, closed, errors, restore: () => dom.restore() };
}

test('a control folded out of the toolbar retains its original command action', async () => {
  const h = makeFixture();
  try {
    let calls = 0;
    const btn = document.createElement('button');
    btn.hidden = false;
    btn.disabled = false;
    btn.dataset.commandOnly = '';
    btn.dataset.toolbarOverflow = '';
    btn.addEventListener('click', () => {
      calls++;
    });
    h.nodes.set('rowsBtn', btn);
    const action = buttonCommand('行与大纲', 'rowsBtn');
    assert.equal(h.palette.unavailable(action), false);
    await h.palette.run(action);
    assert.equal(calls, 1);
  } finally {
    h.restore();
  }
});

test('commands preserve hidden and disabled source-capability guards', async () => {
  for (const unavailable of [
    { hidden: true, disabled: false },
    { hidden: false, disabled: true },
  ]) {
    const h = makeFixture();
    try {
      let calls = 0;
      const btn = document.createElement('button');
      btn.hidden = unavailable.hidden;
      btn.disabled = unavailable.disabled;
      btn.addEventListener('click', () => {
        calls++;
      });
      h.nodes.set('source', btn);
      const action = buttonCommand('源操作', 'source');
      assert.equal(h.palette.unavailable(action), true);
      await h.palette.run(action);
      assert.equal(calls, 0);
      assert.equal(h.closed.length, 0);
    } finally {
      h.restore();
    }
  }
});

test('command browsing always renders the selected result beyond the first thirty', () => {
  for (const total of [0, 1, 30, 60, 80, 32768]) {
    for (const index of [0, 1, 29, 30, 59, 60, total - 1].filter((i) => i >= 0 && i < total)) {
      const start = windowStart(total, index, 60);
      assert.ok(start >= 0);
      assert.ok(start <= index && index < start + 60);
      assert.ok(start <= Math.max(0, total - 60));
    }
  }
});

test('command search ranks name matches first, needs every word and still finds earlier names', () => {
  const actions: readonly CommandAction[] = [
    buttonCommand('高级筛选', 'a', '高级条件筛选 条件'),
    buttonCommand('清除筛选', 'b', '恢复'),
    buttonCommand('筛选', 'c', '自动筛选 表头', 'Ctrl+Shift+L'),
    buttonCommand('转置查看选区', 'd', '转置阅读当前选区 行列'),
  ];
  const h = makeFixture(actions);
  try {
    const names = (query: string) => Array.from(h.palette.matches(query), (action) => action.name);
    assert.deepEqual(names(''), ['高级筛选', '清除筛选', '筛选', '转置查看选区']);
    assert.deepEqual(names('筛选'), ['筛选', '高级筛选', '清除筛选']);
    assert.deepEqual(names('筛选 条件'), ['高级筛选']);
    assert.deepEqual(names('转置阅读'), ['转置查看选区']);
    assert.deepEqual(names('ctrl+shift+l'), ['筛选']);
    assert.deepEqual(names('不存在'), []);
  } finally {
    h.restore();
  }
});

test('synchronous command failures are reported without an uncaught UI exception', async () => {
  const h = makeFixture();
  try {
    void h.palette.run({
      name: 'failing',
      run() {
        throw new Error('read failed');
      },
    });
    await tick();
    assert.deepEqual(h.closed, ['commandDialog']);
    assert.deepEqual(h.errors, [['read failed', 'error']]);
  } finally {
    h.restore();
  }
});
