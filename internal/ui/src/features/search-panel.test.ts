import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createInitialWorkbookState } from '../grid/state.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import { makeNoopApi } from '../test-helpers/grid-fixture.ts';
import { SearchPanel } from './search-panel.ts';

function setupSearchPanel() {
  const dom = installDom({ fromIndexHtml: true });
  const state = createInitialWorkbookState(1_048_576, 16_384);
  state.opened = true;
  state.sheet = 'Sheet1';
  state.sheets = [{ name: 'Sheet1' }];
  state.searchSession = {
    key: 'value\u0000current\u0000q\u0000Sheet1',
    sheet: 'Sheet1',
    input: 'q',
    query: 'q',
    mode: 'value',
    scope: 'current',
    full: false,
    matches: [],
    status: 'done',
    truncated: false,
    summary: '',
    selected: -1,
  };

  const navigated: string[] = [];
  const runs: number[] = [];
  const panel = new SearchPanel({
    state,
    api: makeNoopApi(),
    toast: () => undefined,
    delay: async () => undefined,
    copyText: () => true,
    navigateToCell: async (sheet, ref) => {
      navigated.push(`${sheet}!${ref}`);
    },
    runSearch: () => {
      runs.push(1);
    },
    cancelSearch: async () => undefined,
  });
  panel.bind();

  return {
    panel,
    state,
    navigated,
    runs,
    restore: () => {
      panel.resetWorkbook();
      dom.restore();
    },
  };
}

test('search panel chunks large match lists, loads more on scroll and toggles active item without full rebuild', async () => {
  const { panel, state, navigated, restore } = setupSearchPanel();
  try {
    const matches = Array.from({ length: 500 }, (_, i) => ({
      sheet: 'Sheet1',
      ref: `A${i + 1}`,
      row: i + 1,
      col: 1,
      value: `条目 ${i + 1}`,
      formula: '',
    }));
    if (state.searchSession) state.searchSession.matches = matches;
    panel.renderResults({ matches, mode: 'value', status: 'done' });

    const box = document.getElementById('searchResults');
    assert.ok(box);
    assert.equal(box.children.length, 200);
    const firstNode = box.children[0];

    // Scrolling near the bottom appends the next 200 items without recreating existing nodes.
    Object.defineProperty(box, 'clientHeight', { value: 200, configurable: true });
    Object.defineProperty(box, 'scrollHeight', { value: 800, configurable: true });
    box.scrollTop = 650;
    box.dispatchEvent(new Event('scroll'));
    assert.equal(box.children.length, 400);
    assert.equal(box.children[0], firstNode);

    // Navigating past rendered items materializes through the target and toggles active in place.
    await panel.goResult(450);
    assert.ok(box.children.length >= 451);
    assert.equal(box.children[0], firstNode);
    assert.ok(box.children[450]);
    assert.equal(box.children[450].classList.contains('active'), true);
    assert.equal(box.children[450].getAttribute('aria-selected'), 'true');
    assert.deepEqual(navigated, ['Sheet1!A451']);

    await panel.goResult(10);
    assert.equal(box.children[450].classList.contains('active'), false);
    assert.equal(box.children[10]?.classList.contains('active'), true);
  } finally {
    restore();
  }
});

test('search panel ignores Enter while IME composition is active', () => {
  const { runs, restore } = setupSearchPanel();
  try {
    const input = document.getElementById('searchInput');
    assert.ok(input);
    input.dispatchEvent(
      Object.assign(new Event('keydown', { cancelable: true }), {
        key: 'Enter',
        isComposing: true,
        keyCode: 229,
      }),
    );
    assert.equal(runs.length, 0);
    input.dispatchEvent(
      Object.assign(new Event('keydown', { cancelable: true }), {
        key: 'Enter',
        isComposing: false,
        keyCode: 13,
      }),
    );
    assert.equal(runs.length, 1);
  } finally {
    restore();
  }
});
