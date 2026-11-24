const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

function makeNode(tag = 'div') {
  const classes = new Set(), attrs = new Map(), listeners = new Map(), children = [];
  const node = {
    tagName: tag.toUpperCase(),
    className: '',
    textContent: '',
    value: '',
    title: '',
    hidden: false,
    disabled: false,
    scrollTop: 0,
    clientHeight: 200,
    scrollHeight: 800,
    dataset: {},
    style: {},
    children,
    childNodes: children,
    classList: {
      add(...names) { names.forEach(n => classes.add(n)); node.className = [...classes].join(' '); },
      remove(...names) { names.forEach(n => classes.delete(n)); node.className = [...classes].join(' '); },
      contains: n => classes.has(n),
      toggle(n, force) { const on = force === undefined ? !classes.has(n) : Boolean(force); if (on) classes.add(n); else classes.delete(n); node.className = [...classes].join(' '); return on; }
    },
    setAttribute(k, v) { attrs.set(k, String(v)); },
    getAttribute(k) { return attrs.has(k) ? attrs.get(k) : null; },
    addEventListener(type, fn) { if (!listeners.has(type)) listeners.set(type, []); listeners.get(type).push(fn); },
    emit(type, event = {}) { for (const fn of listeners.get(type) || []) fn(event); },
    append(...items) {
      for (const item of items) {
        if (item?.isFragment) children.push(...item.children);
        else children.push(item);
      }
    },
    appendChild(item) { this.append(item); return item; },
    replaceChildren(...items) { children.length = 0; this.append(...items); },
    scrollIntoView() { this.scrolledIntoView = true; },
    focus() { this.focused = true; },
    select() {},
    querySelectorAll(selector) {
      if (selector === '.result-item.active') return children.filter(c => c.className.split(/\s+/).includes('active'));
      return [];
    },
    querySelector(selector) {
      const m = selector.match(/data-match-index="(\d+)"/);
      if (m) return children.find(c => c.dataset.matchIndex === m[1]) || null;
      return null;
    }
  };
  Object.defineProperty(node, 'className', {
    get() { return [...classes].join(' '); },
    set(v) { classes.clear(); for (const part of String(v || '').split(/\s+/)) if (part) classes.add(part); }
  });
  return node;
}

function setupSearchPanel() {
  const nodes = new Map();
  const get = id => { if (!nodes.has(id)) nodes.set(id, makeNode()); return nodes.get(id); };
  const document = {
    getElementById: get,
    createElement: tag => makeNode(tag),
    createDocumentFragment: () => ({isFragment: true, children: [], append(...items) { this.children.push(...items); }, appendChild(i) { this.children.push(i); return i; }}),
    querySelectorAll: () => []
  };
  const window = {ExcelLensDisplayFormat: {isComposing: e => Boolean(e?.isComposing || e?.keyCode === 229)}};
  const ctx = {window, document, setTimeout, clearTimeout, URLSearchParams};
  vm.runInNewContext(fs.readFileSync(__dirname + '/search-panel.js', 'utf8'), ctx);
  const state = {
    opened: true, sheet: 'Sheet1', sheets: [{name: 'Sheet1'}],
    searchSession: {sheet: 'Sheet1', matches: [], mode: 'value', status: 'done', selected: -1},
    searchCache: new Map(), searchSelectedIndex: -1, searchRequestID: 0, searchJob: null,
    visibleSearchIndexes: [], serverSearchHistory: []
  };
  const navigated = [], runs = [];
  const panel = new window.ExcelLensSearchPanel({
    state, api: async () => ({}), toast() {}, delay: async () => {}, copyText() {},
    navigateToCell: async (sheet, ref) => navigated.push(`${sheet}!${ref}`),
    runSearch: () => runs.push(1), cancelSearch: async () => {}
  });
  panel.bind();
  return {panel, state, nodes, get, navigated, runs};
}

test('search panel chunks large match lists, loads more on scroll and toggles active item without full rebuild', async () => {
  const {panel, state, get, navigated} = setupSearchPanel();
  const matches = Array.from({length: 500}, (_, i) => ({sheet: 'Sheet1', ref: `A${i + 1}`, value: `条目 ${i + 1}`, formula: ''}));
  state.searchSession.matches = matches;
  panel.renderResults({matches, mode: 'value', status: 'done'});

  const box = get('searchResults');
  assert.equal(box.children.length, 200);
  const firstNode = box.children[0];

  // Scrolling near the bottom appends the next 200 items without recreating existing nodes.
  box.scrollTop = 650;
  box.emit('scroll');
  assert.equal(box.children.length, 400);
  assert.equal(box.children[0], firstNode);

  // Navigating past rendered items materializes through the target and toggles active in place.
  await panel.goResult(450);
  assert.ok(box.children.length >= 451);
  assert.equal(box.children[0], firstNode);
  assert.ok(box.children[450].classList.contains('active'));
  assert.equal(box.children[450].getAttribute('aria-selected'), 'true');
  assert.deepEqual(navigated, ['Sheet1!A451']);

  await panel.goResult(10);
  assert.equal(box.children[450].classList.contains('active'), false);
  assert.equal(box.children[10].classList.contains('active'), true);
});

test('search panel ignores Enter while IME composition is active', () => {
  const {get, runs} = setupSearchPanel();
  const input = get('searchInput');
  input.emit('keydown', {key: 'Enter', isComposing: true, keyCode: 229});
  assert.equal(runs.length, 0);
  input.emit('keydown', {key: 'Enter', isComposing: false, keyCode: 13});
  assert.equal(runs.length, 1);
});
