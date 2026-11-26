const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const read = name => fs.readFileSync(__dirname + '/' + name, 'utf8');

function renderer(name) {
  const source = read(name);
  const start = source.indexOf('  function toast(');
  const end = source.indexOf('\n  function ', start + 1);
  assert(start >= 0 && end > start);
  const node = { hidden: true, dataset: {}, attrs: {}, setAttribute(key, value) { this.attrs[key] = value; } };
  const timers = new Map();
  let sequence = 0;
  const context = {
    $: () => node,
    window: {},
    toastTimer: 0,
    setTimeout: fn => { const id = ++sequence; timers.set(id, fn); return id; },
    clearTimeout: id => timers.delete(id),
  };
  vm.runInNewContext(source.slice(start, end) + ';this.show=toast;', context);
  return { node, timers, show: context.show };
}

for (const name of ['app.js', 'analytics.js']) {
  test(name + ': explicit notification tones reset after errors and never infer from message text', () => {
    const h = renderer(name);
    h.show('网络连接失败', 'error'); assert.equal(h.node.dataset.tone, 'error'); assert.equal(h.node.attrs.role, 'alert');
    h.show('已复制「失败记录.txt」', 'success'); assert.equal(h.node.dataset.tone, 'success'); assert.equal(h.node.attrs.role, 'status');
    h.show('请先选择单元格', 'warning'); assert.equal(h.node.dataset.tone, 'warning');
    h.show('当前文件名包含错误二字'); assert.equal(h.node.dataset.tone, 'info'); assert.equal(h.node.attrs.role, 'status');
    h.show('<img src=x onerror=bad>', 'unknown'); assert.equal(h.node.dataset.tone, 'info'); assert.equal(h.node.textContent, '<img src=x onerror=bad>');
    assert.equal(h.timers.size, 1, 'previous messages must not dismiss the latest one'); assert.equal(h.node.hidden, false);
    [...h.timers.values()][0](); assert.equal(h.node.hidden, true);
  });
}

test('clipboard outcomes report green only after a successful write, with failed legacy copies remaining red', async () => {
  for (const scenario of ['success', 'denied', 'legacy-failed']) {
    const notices = [];
    let removed = false;
    const helper = { style: {}, select() {}, remove() { removed = true; } };
    const context = {
      window: {},
      navigator: scenario === 'legacy-failed' ? {} : { clipboard: { writeText: async () => { if (scenario === 'denied') throw Error('权限不足'); } } },
      document: { createElement: () => helper, body: { appendChild() {} }, execCommand: () => false },
    };
    vm.runInNewContext(read('grid-clipboard.js'), context);
    const clipboard = new context.window.ExcelLensGridClipboard({ toast: (...args) => notices.push(args) });
    assert.equal(await clipboard.copyText('测试文本'), scenario === 'success');
    assert.equal(notices.length, 1);
    assert.equal(notices[0][1], scenario === 'success' ? 'success' : 'error');
    if (scenario === 'legacy-failed') assert.equal(removed, true, 'failed fallback must remove its hidden textarea');
  }
});

test('qualified cell address escapes single quotes in sheet names and evidence copies immutable metadata', async () => {
  const written = [];
  const notices = [];
  const context = {
    window: {},
    navigator: { clipboard: { writeText: async text => { written.push(text); } } },
    document: {},
  };
  vm.runInNewContext(read('grid-clipboard.js'), context);
  const S = {
    fileName: 'report.xlsx',
    sheet: "Q1'26 汇总",
    selected: { ref: 'H5', display: '140.87', raw: '140.87', formula: '=SUMIFS(A:A,B:B,C1)', cacheMissing: false },
  };
  const clipboard = new context.window.ExcelLensGridClipboard({ S, toast: (...args) => notices.push(args) });
  assert.equal(await clipboard.copyQualifiedAddress(), true);
  assert.equal(written[0], "'Q1''26 汇总'!H5");
  assert.equal(await clipboard.copyCellEvidence(), true);
  assert.match(written[1], /工作簿：report\.xlsx/);
  assert.match(written[1], /公式：=SUMIFS\(A:A,B:B,C1\)/);
  assert.equal(notices.length, 2);
});

test('a failed inspector copy cannot replace its error with a green completion message', async () => {
  const notices = [];
  const context = { window: {}, document: {} };
  vm.runInNewContext(read('inspect.js'), context);
  const inspector = Object.create(context.window.ExcelLensSelectionInspector.prototype);
  Object.assign(inspector, { matches: [{ ref: 'A1', value: '001' }], sheet: '数据', copy: async () => false, toast: (...args) => notices.push(args) });
  await inspector.copyResults(); assert.equal(notices.length, 0);
  inspector.copy = async () => true; await inspector.copyResults(); assert.equal(notices.length, 1); assert.equal(notices[0][1], 'success');
});
