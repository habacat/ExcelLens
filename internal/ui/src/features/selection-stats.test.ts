import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { SelectionStatsResponse } from '../contracts/features.ts';
import { createInitialWorkbookState } from '../grid/state.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import { makeNoopApi } from '../test-helpers/grid-fixture.ts';
import { formatStatusNumber, SelectionStats } from './selection-stats.ts';

function makeStats(overrides: Partial<SelectionStatsResponse> = {}): SelectionStatsResponse {
  return {
    ranges: 1,
    total: 0,
    nonEmpty: 0,
    number: 0,
    sum: '',
    average: '',
    filtered: false,
    ...overrides,
  };
}

function setupSelectionStats() {
  const dom = installDom({ fromIndexHtml: true });
  const copied: string[] = [];
  const originalClipboard = navigator.clipboard;
  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: {
      writeText: async (value: string) => {
        copied.push(value);
      },
    },
  });

  const state = createInitialWorkbookState(1_048_576, 16_384);
  const stats = new SelectionStats({
    state,
    api: makeNoopApi(),
    colName: (c) => String.fromCharCode(64 + c),
    toast: () => undefined,
    profile: () => undefined,
  });
  const host = document.getElementById('quickStats');
  assert.ok(host);

  return {
    stats,
    host,
    copied,
    restore: () => {
      stats.resetBook();
      Object.defineProperty(navigator, 'clipboard', {
        configurable: true,
        value: originalClipboard,
      });
      dom.restore();
    },
  };
}

test('selection totals match the supplied Excel order, labels and ten-thousand grouping', () => {
  const { stats, host, restore } = setupSelectionStats();
  try {
    stats.render(
      makeStats({ nonEmpty: 6, number: 6, total: 6, sum: '133759.02', average: '22293.17' }),
    );
    assert.deepEqual(
      Array.from(host.children).map((x) => x.textContent),
      ['平均值=2万2293.17', '计数=6', '求和=13万3759.02'],
    );
    assert.equal(
      (host.children[0]?.children[1] as HTMLElement | undefined)?.dataset.statValue,
      '22293.17',
    );
    assert.equal(
      (host.children[2]?.children[1] as HTMLElement | undefined)?.dataset.statValue,
      '133759.02',
    );
  } finally {
    restore();
  }
});

test('status formatting never passes monetary values through floating point', () => {
  const { stats, host, restore } = setupSelectionStats();
  try {
    assert.equal(formatStatusNumber('-133759.000001'), '-13万3759.000001');
    assert.equal(
      formatStatusNumber('9007199254740993123.0000001'),
      '9007199254740993123.0000001',
    );
    assert.equal(formatStatusNumber('0.000000001'), '0.000000001');
    stats.render(makeStats({ nonEmpty: 2, number: 0, total: 3 }));
    assert.deepEqual(
      Array.from(host.children).map((x) => x.textContent),
      ['计数=2'],
    );
  } finally {
    restore();
  }
});

test('copying formatted statistics copies the original numeric string', async () => {
  const { stats, host, copied, restore } = setupSelectionStats();
  try {
    stats.render(
      makeStats({ nonEmpty: 6, number: 6, total: 6, sum: '133759.02', average: '22293.17' }),
    );
    const strong = host.children[2]?.children[1];
    assert.ok(strong);
    strong.dispatchEvent(new Event('click', { bubbles: true }));
    await new Promise<void>((resolve) => {
      setImmediate(resolve);
    });
    assert.deepEqual(copied, ['133759.02']);
  } finally {
    restore();
  }
});

test('six metrics distinguish text from numbers and group decimal strings without rounding', () => {
  const { stats, host, restore } = setupSelectionStats();
  try {
    stats.metrics = new Set(['average', 'nonEmpty', 'number', 'minimum', 'maximum', 'sum']);
    stats.format = 'thousands';
    stats.render(
      makeStats({
        nonEmpty: 3,
        number: 2,
        total: 3,
        average: '1.75',
        minimum: '-12345.50',
        maximum: '12349.00',
        sum: '3.50',
      }),
    );
    assert.deepEqual(
      Array.from(host.children).map((n) => n.textContent),
      [
        '平均值=1.75',
        '计数=3',
        '数值计数=2',
        '最小值=-12,345.50',
        '最大值=12,349.00',
        '求和=3.50',
      ],
    );
    assert.equal(
      formatStatusNumber('-90071992547409930001.0020 ≈', 'myriad'),
      '-9007,1992,5474,0993,0001.0020 ≈',
    );
    assert.equal(formatStatusNumber('12345678.500', 'plain'), '12345678.500');
    stats.render(makeStats({ nonEmpty: 1, number: 0, total: 1, sum: '0', average: '0' }));
    assert.deepEqual(
      Array.from(host.children).map((n) => n.textContent),
      ['计数=1', '数值计数=0'],
    );
  } finally {
    restore();
  }
});
