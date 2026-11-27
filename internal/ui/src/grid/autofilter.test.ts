import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { AutoFilterRule } from '../contracts/workbook.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import { AutoFilterDropdown } from './autofilter.ts';
import { createInitialWorkbookState } from './state.ts';

function setup(values: readonly string[], truncated: boolean, rule?: AutoFilterRule) {
  const dom = installDom({ fromIndexHtml: true });
  const rules = new Map<number, AutoFilterRule>(rule ? [[1, rule]] : []);
  const state = createInitialWorkbookState(1_048_576, 16_384);
  state.sheet = 'Data';
  state.indexReady = true;
  state.autoFilter = {
    row: 1,
    startCol: 1,
    endCol: 1,
    rules,
    sortColumn: 0,
    descending: false,
  };
  let applied = 0;
  const dropdown = new AutoFilterDropdown({
    state,
    api: async <T = unknown>() => {
      const res: unknown = { values: values.map((value) => ({ value, count: 1 })), truncated };
      return res as T;
    },
    colName: () => 'A',
    toast: () => undefined,
    onApply: async () => {
      applied++;
    },
  });
  const anchor = document.createElement('button');
  return {
    dropdown,
    state,
    rules,
    anchor,
    get applied(): number {
      return applied;
    },
    restore: () => dom.restore(),
  };
}

test('confirming an incomplete list preserves the active whitelist and does not repeat the scan', async () => {
  const rule: AutoFilterRule = { column: 1, operator: 'in', values: ['a', 'b'] };
  const h = setup(['a', 'b'], true, rule);
  try {
    await h.dropdown.open(1, h.anchor);
    await h.dropdown.apply();
    assert.equal(h.rules.get(1), rule);
    assert.equal(h.applied, 0);
    await h.dropdown.open(1, h.anchor);
    await h.dropdown.clearColumn();
    assert.equal(h.rules.size, 0);
    assert.equal(h.applied, 1);
  } finally {
    h.restore();
  }
});

test('equal-sized selections containing unlisted values are not mistaken for selecting every value', async () => {
  const h = setup(['a', 'b'], false, { column: 1, operator: 'in', values: ['a', 'unlisted'] });
  try {
    await h.dropdown.open(1, h.anchor);
    const list = document.getElementById('autoFilterOptions');
    assert.ok(list);
    const items = [...list.querySelectorAll('label.auto-filter-item')];
    const findBox = (label: string): HTMLInputElement | undefined => {
      const item = items.find((el) => el.querySelector('.auto-filter-value')?.textContent === label);
      const input = item?.querySelector<HTMLInputElement>('input[type="checkbox"]');
      return input ?? undefined;
    };
    const boxA = findBox('a');
    const boxB = findBox('b');
    assert.ok(boxA && boxB);
    boxA.checked = false;
    boxA.dispatchEvent(new Event('change'));
    boxB.checked = true;
    boxB.dispatchEvent(new Event('change'));
    await h.dropdown.apply();
    assert.deepEqual([...(h.rules.get(1)?.values ?? [])], ['unlisted', 'b']);
    assert.equal(h.applied, 1);
  } finally {
    h.restore();
  }
});

test('an explicitly changed complete selection may clear a rule, while an incomplete one remains a whitelist', async () => {
  for (const truncated of [false, true]) {
    const h = setup(['a', 'b'], truncated, { column: 1, operator: 'in', values: ['a'] });
    try {
      await h.dropdown.open(1, h.anchor);
      const list = document.getElementById('autoFilterOptions');
      const items = [...(list?.querySelectorAll('label.auto-filter-item') ?? [])];
      const itemB = items.find((el) => el.querySelector('.auto-filter-value')?.textContent === 'b');
      const boxB = itemB?.querySelector<HTMLInputElement>('input[type="checkbox"]');
      assert.ok(boxB);
      boxB.checked = true;
      boxB.dispatchEvent(new Event('change'));
      await h.dropdown.apply();
      assert.equal(h.rules.has(1), truncated);
      assert.equal(h.applied, 1);
    } finally {
      h.restore();
    }
  }
});

test('large worksheets can use the header sort without discarding predicates', async () => {
  const rule: AutoFilterRule = { column: 1, operator: 'in', values: ['a'] };
  const h = setup(['a', 'b'], false, rule);
  try {
    h.state.largeData = true;
    await h.dropdown.open(1, h.anchor);
    await h.dropdown.sort(true);
    assert.equal(h.applied, 1);
    assert.equal(h.state.autoFilter?.sortColumn, 1);
    assert.equal(h.state.autoFilter.descending, true);
    assert.equal(h.rules.get(1), rule);
  } finally {
    h.restore();
  }
});
