import assert from 'node:assert/strict';
import { test } from 'node:test';
import { createTransport } from '../core/transport.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import { makeDefaultViewportPipelineDeps } from '../test-helpers/grid-fixture.ts';
import { GridSizing } from './grid-sizing.ts';

function setup() {
  const dom = installDom({ fromIndexHtml: true });
  const messages: string[] = [];
  const deps = makeDefaultViewportPipelineDeps({ api: createTransport({ token: 'test' }).api });
  const { S, geometry } = deps;
  S.workbookKey = 'first-fingerprint';
  S.sheet = '明细';
  S.indexReady = true;
  const sizing = new GridSizing({
    ...deps, scale: () => 1, colWidth: col => geometry.cols.measure(col),
    rowHeight: row => geometry.rows.measure(row), relayoutGrid: () => undefined,
    toast: message => messages.push(message),
  });
  return { S, geometry, sizing, messages, dom };
}

test('rapid sizes serialize, a failed acknowledgment preserves the last saved width, and a late reply cannot change a replacement sheet', async t => {
  const { S, sizing, geometry, dom, messages } = setup();
  t.after(() => dom.restore());
  const first = Promise.withResolvers<Response>();
  const last = Promise.withResolvers<Response>();
  const lastStarted = Promise.withResolvers<undefined>();
  let writes = 0;
  t.mock.method(globalThis, 'fetch', (_url: string, options: RequestInit) => {
    assert.equal(new Headers(options.headers).get('X-ExcelLens-Workbook'), 'first-fingerprint');
    switch (++writes) {
      case 1: return first.promise;
      case 2: return Promise.resolve(Response.json({ rows: {}, cols: { '3': 170 } }));
      default: lastStarted.resolve(undefined); return last.promise;
    }
  });
  const accepted = sizing.commitResize('col', 3, 170);
  const refused = sizing.commitResize('col', 3, 190);
  await new Promise<void>(resolve => setImmediate(resolve));
  assert.equal(writes, 1);
  first.resolve(Response.json({ rows: {}, cols: { '3': 170 } }));
  assert.deepEqual(await Promise.all([accepted, refused]), [true, false]);
  assert.equal(geometry.cols.measure(3), 170);
  assert.equal(S.columnWidths['3'], 170);
  assert.match(messages.join(' '), /未确认/);

  const stale = sizing.commitResize('col', 3, 205);
  await lastStarted.promise;
  const queued = sizing.commitResize('col', 3, 220);
  S.sheetEpoch++;
  S.workbookKey = 'replacement-fingerprint';
  S.columnWidths = {};
  geometry.cols.load({});
  last.resolve(Response.json({ rows: {}, cols: { '3': 205 } }));
  assert.deepEqual(await Promise.all([stale, queued]), [false, false]);
  assert.deepEqual(S.columnWidths, {});
  assert.equal(writes, 3);
});

test('blur and a sheet switch cancel a drag and lifting a different pointer cannot finish it', async t => {
  const { S, sizing, dom } = setup();
  t.after(() => dom.restore());
  const writes = t.mock.method(globalThis, 'fetch', async () => Response.json({ cols: { '1': 200 } }));
  const grip = document.createElement('div');
  const guide = document.getElementById('resizeGuide');
  assert.ok(guide);
  sizing.bindResizeGrip(grip, 'col', 1);
  const pointer = (type: string, id: number, x = 10) =>
    Object.assign(new Event(type), { pointerId: id, button: 0, clientX: x, clientY: 10 });
  grip.dispatchEvent(pointer('pointerdown', 1));
  document.dispatchEvent(pointer('pointermove', 1, 80));
  document.dispatchEvent(pointer('pointerup', 2));
  assert.equal(guide.hidden, false);
  window.dispatchEvent(new Event('blur'));
  assert.equal(guide.hidden, true);
  document.dispatchEvent(pointer('pointerup', 1));
  grip.dispatchEvent(pointer('pointerdown', 3));
  S.sheetEpoch++;
  document.dispatchEvent(pointer('pointerup', 3));
  await new Promise<void>(resolve => setImmediate(resolve));
  assert.equal(guide.hidden, true);
  assert.equal(writes.mock.callCount(), 0);
});
