const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

test('grid controls clamp zoom steps and preserve range-aware tooltip suppression', () => {
  const nodes = new Map();
  const node = id => {
    if (!nodes.has(id)) nodes.set(id, { hidden: false, textContent: '', style: {}, scrollLeft: 0, replaceChildren() {} });
    return nodes.get(id);
  };
  const cssVars = {};
  let viewportScheduled = 0;
  let framesPainted = 0;
  const ctx = {
    window: { innerWidth: 1200, innerHeight: 800 },
    document: {
      documentElement: { style: { setProperty: (k, v) => { cssVars[k] = v; } } },
      createElement: () => ({ textContent: '', className: '' }),
    },
    Math,
    Number,
    String,
    Boolean,
  };
  vm.runInNewContext(fs.readFileSync(__dirname + '/grid-context-menu.js', 'utf8'), ctx);
  const S = { zoom: 100, selected: { row: 2, col: 3 }, previewOnly: false, cellMap: new Map(), frozenCellMap: new Map(), range: null, multiRanges: [] };
  const controls = new ctx.window.ExcelLensGridControls({
    $: node,
    S,
    rowHeight: () => Math.round(25 * S.zoom / 100),
    colWidth: () => Math.round(132 * S.zoom / 100),
    headerHeight: () => Math.round(27 * S.zoom / 100),
    rowTop: r => (r - 1) * 25,
    colLeft: c => (c - 1) * 132,
    rowHead: () => 26,
    invalidateViewport() {},
    updateCanvasSize() {},
    setVirtualScrollTop() {},
    scheduleViewport() { viewportScheduled++; },
    paintRangeFrames() { framesPainted++; },
    getFrozenPanes: () => ({ reset() {}, schedule() {} }),
    getPictures: () => ({ schedule() {} }),
    isDragging: () => true,
  });

  controls.setZoom(143);
  assert.equal(S.zoom, 125);
  assert.equal(node('zoomValue').textContent, '125%');
  assert.equal(viewportScheduled, 1);
  assert.equal(framesPainted, 1);
  assert.equal(cssVars['--row-height'], '31px');

  controls.setZoom(42);
  assert.equal(S.zoom, 70);
  assert.equal(node('zoomValue').textContent, '70%');

  // While dragging a selection, hover tooltips stay hidden.
  node('cellTooltip').hidden = false;
  controls.showTooltip({ target: { closest: () => ({ dataset: { ref: 'A1' }, classList: { contains: () => false } }) }, clientX: 50, clientY: 50 });
  assert.equal(node('cellTooltip').hidden, true);
});

test('grid interactions ribbon access keys Alt+H, O, I/A/W/H trigger resize commands and expire cleanly', () => {
  const ctx = { window: {}, Date, Math };
  vm.runInNewContext(fs.readFileSync(__dirname + '/grid-interactions.js', 'utf8'), ctx);
  const calls = [];
  const interactions = new ctx.window.ExcelLensGridInteractions({
    S: { selected: { row: 5, col: 4 } },
    autoFitSize: (kind, index) => calls.push(['fit', kind, index]),
    promptResize: (kind, index) => calls.push(['prompt', kind, index]),
  });
  const taken = [];
  const take = fn => { taken.push(fn); fn(); };

  assert.equal(interactions.handleRibbonSequence({ altKey: true, key: 'h' }, 'h', true, take), true);
  assert.equal(interactions.handleRibbonSequence({ altKey: false, key: 'o' }, 'o', true, take), true);
  assert.equal(interactions.handleRibbonSequence({ altKey: false, key: 'i' }, 'i', true, take), true);
  assert.deepEqual(calls, [['fit', 'col', 4]]);

  // Expired sequence should not fire.
  assert.equal(interactions.handleRibbonSequence({ altKey: true, key: 'h' }, 'h', true, take), true);
  interactions.ribbonExpires = Date.now() - 1;
  assert.equal(interactions.handleRibbonSequence({ altKey: false, key: 'o' }, 'o', true, take), false);
});

test('column visibility follows the active source when switching between BIFF and OOXML', async () => {
  const ctx = { window: {} };
  vm.runInNewContext(fs.readFileSync(__dirname + '/grid-columns.js', 'utf8'), ctx);
  const state = { indexReady: true, sheet: 'Data', columnMode: 'hide-all', columnOverrides: {} };
  const axis = { setBase() {}, load() {} };
  let sourceLimit = 256;
  const controller = new ctx.window.ExcelLensColumnController({
    state, colLimit: () => sourceLimit, geometry: { rows: axis, cols: axis },
    api() { throw Error('outside source columns'); },
  });
  controller.applyVisibility();
  assert.equal(state.hiddenColumns.size, 256);
  await controller.setVisibility('column', 257, true);
  assert.deepEqual(state.columnOverrides, {});
  sourceLimit = 16384;
  controller.applyVisibility();
  assert.equal(state.hiddenColumns.size, 16384);
});

test('index progress distinguishes source extents, bytes and statistics rows', () => {
  const ctx = { window: {} };
  vm.runInNewContext(fs.readFileSync(__dirname + '/grid-workbook-io.js', 'utf8'), ctx);
  const io = new ctx.window.ExcelLensWorkbookIO({fmtBytes:n=>n+' B'});
  const calls = [], activity = {report:(...args)=>calls.push(args)};
  io.reportIndexProgress(activity,{stage:'索引完成',status:'done',totalRows:75,doneBytes:42,totalBytes:42});
  io.reportIndexProgress(activity,{stage:'条件统计',status:'running',statisticsProgress:{done:4,total:8}});
  assert.deepEqual(calls,[[42,42,'索引完成','42 B / 42 B'],[4,8,'条件统计','4 / 8 行']]);
});
