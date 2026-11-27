import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { RecentEntry } from '../contracts/features.ts';
import { formatBytes } from '../core/display-format.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import { RecentWorkbooks } from './recent.ts';

function setup() {
  const dom = installDom();
  const list = dom.register('recentList', 'div');
  const search = dom.register('recentSearch', 'input');
  dom.register('recentRefreshBtn', 'button');
  dom.register('recentSummary', 'div');
  const recent = new RecentWorkbooks({
    api: async <T = unknown>(): Promise<T> => {
      const res: unknown = { items: [] };
      return res as T;
    },
    open: async () => undefined,
    toast: () => undefined,
  });
  return {
    recent,
    list,
    search,
    restore(): void {
      recent.dispose();
      dom.restore();
    },
  };
}

test('recent file sizes use KB, MB or GB without inventing missing values', () => {
  const h = setup();
  try {
    const cases: readonly (readonly [number, string])[] = [
      [0, '0 KB'],
      [1, '0.001 KB'],
      [512, '0.5 KB'],
      [1024, '1 KB'],
      [1536, '1.5 KB'],
      [1024 ** 2, '1 MB'],
      [1024 ** 3 * 1.5, '1.5 GB'],
    ];
    for (const [bytes, label] of cases) {
      assert.equal(h.recent.formatSize(bytes), label);
    }
    for (const bytes of [undefined, Number.NaN, -1]) {
      assert.equal(h.recent.formatSize(bytes), '—');
    }
    for (const bytes of [undefined, null, Number.NaN, -1, '1024']) {
      assert.equal(formatBytes(bytes), '—');
    }
  } finally {
    h.restore();
  }
});

test('size stays beside opening time while metadata, search and time refresh retain their roles', () => {
  const h = setup();
  try {
    const entries: RecentEntry[] = [
      {
        id: 'a',
        name: '<表格>.xlsx',
        path: 'D:/资料/表格.xlsx',
        fingerprint: 'fp-a',
        fileSize: 2 * 1024 ** 2,
        sheetCount: 4,
        available: true,
        pinned: false,
        openedAt: new Date().toISOString(),
      },
      {
        id: 'b',
        name: '成员.txt',
        path: 'temp.txt',
        archiveName: '资料.7z',
        archivePath: 'D:/资料.7z',
        fingerprint: 'fp-b',
        fileSize: 3 * 1024 ** 3,
        sheetCount: 1,
        available: false,
        pinned: false,
        openedAt: '2020-01-01T00:00:00Z',
      },
    ];
    h.recent.items = entries;
    h.recent.render();

    assert.deepEqual(
      [...h.list.querySelectorAll('.recent-size')].map((n) => n.textContent),
      ['2 MB', '3 GB'],
    );
    assert.equal(h.list.querySelectorAll('.recent-name')[0]?.textContent, '<表格>.xlsx');
    const side = h.list.querySelectorAll('.recent-side')[0];
    assert.ok(side);
    assert.deepEqual(
      [...side.children].map((n) => n.className),
      ['recent-date', 'recent-size', 'recent-controls'],
    );
    assert.equal(h.list.querySelectorAll('.recent-meta')[0]?.children[0]?.textContent, '4 张表');

    h.recent.refreshTimes();
    assert.equal(side.children[1]?.textContent, '2 MB');

    h.search.value = '成员';
    h.recent.render();
    assert.deepEqual(
      [...h.list.querySelectorAll('.recent-size')].map((n) => n.textContent),
      ['3 GB'],
    );
    assert.equal(
      h.list.querySelectorAll('.recent-meta')[0]?.children[0]?.textContent,
      '文件已移动或删除',
    );
  } finally {
    h.restore();
  }
});
