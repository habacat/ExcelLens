import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from '../test-helpers/dom-mock.ts';
import { AdaptiveToolbar, type ToolbarMeasureItem } from './toolbar.ts';

function makeItem(id: string, width: number, priority: number): ToolbarMeasureItem {
  const node = document.createElement('button');
  node.id = id;
  return { node, width, priority };
}

test('narrow toolbar retains named core actions and the command entry', () => {
  const dom = installDom();
  try {
    const items = [
      makeItem('open', 55, 0),
      makeItem('search', 55, 0),
      makeItem('export', 55, 1),
      makeItem('freeze', 90, 2),
      makeItem('commands', 55, 0),
    ];
    const shown = AdaptiveToolbar.choose(items, 180, 5);
    assert.deepEqual(
      [...shown].map((node) => node.id),
      ['open', 'search', 'commands'],
    );
  } finally {
    dom.restore();
  }
});

test('available container width admits useful actions in priority order', () => {
  const dom = installDom();
  try {
    const items = [
      makeItem('open', 55, 0),
      makeItem('search', 55, 0),
      makeItem('export', 55, 1),
      makeItem('freeze', 90, 2),
      makeItem('commands', 55, 0),
    ];
    assert.deepEqual(
      [...AdaptiveToolbar.choose(items, 235, 5)].map((node) => node.id),
      ['open', 'search', 'commands', 'export'],
    );
    assert.deepEqual(
      [...AdaptiveToolbar.choose(items, 330, 5)].map((node) => node.id),
      ['open', 'search', 'commands', 'export', 'freeze'],
    );
  } finally {
    dom.restore();
  }
});

test('core controls remain available for wrapping even below a single-row minimum', () => {
  const dom = installDom();
  try {
    const items = [
      makeItem('open', 55, 0),
      makeItem('search', 55, 0),
      makeItem('export', 55, 1),
      makeItem('freeze', 90, 2),
      makeItem('commands', 55, 0),
    ];
    assert.deepEqual(
      [...AdaptiveToolbar.choose(items, 60, 5)].map((node) => node.id),
      ['open', 'search', 'commands'],
    );
  } finally {
    dom.restore();
  }
});

test('toolbar gap accounting includes fractional text widths at the boundary', () => {
  const dom = installDom();
  try {
    const list = [
      makeItem('open', 55.5, 0),
      makeItem('commands', 55.5, 0),
      makeItem('sort', 55.5, 1),
    ];
    assert.equal(AdaptiveToolbar.choose(list, 176.49, 5).size, 2);
    assert.equal(AdaptiveToolbar.choose(list, 176.5, 5).size, 3);
  } finally {
    dom.restore();
  }
});
