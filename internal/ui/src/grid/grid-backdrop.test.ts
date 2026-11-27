import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from '../test-helpers/dom-mock.ts';
import { makeDefaultSheetControllerDeps } from '../test-helpers/grid-fixture.ts';
import { gridBackdropPanes } from './grid-backdrop.ts';
import { ReadingMode } from './reading-mode.ts';
import { SheetController } from './sheet-controller.ts';

test('one viewport texture covers frozen blank strips while protecting frozen source cells after scrolling', () => {
  const bounds = {
    width: 1200,
    height: 800,
    rowHead: 42,
    header: 27,
    pinWidth: 260,
    pinHeight: 50,
    right: 100,
    bottom: 100,
    fixedRight: 700,
    fixedBottom: 400,
  };
  const panes = gridBackdropPanes(bounds);
  assert.equal(panes.length, 4);
  const alpha = (x: number, y: number): number => {
    const p = panes.find((item) => x >= item.x && x < item.x + item.width && y >= item.y && y < item.y + item.height);
    if (!p) return 0;
    const a = Math.max(0, Math.min(1, (x - p.x - p.right) / 96));
    const b = Math.max(0, Math.min(1, (y - p.y - p.bottom) / 80));
    return 1 - (1 - a) * (1 - b);
  };
  assert.equal(alpha(200, 50), 0, 'frozen source corner must stay clear');
  assert.equal(alpha(200, 250), 1, 'bottom blank strip in frozen columns must be decorated');
  assert.equal(alpha(900, 50), 1, 'right blank strip in frozen rows must be decorated');
  assert.equal(alpha(900, 400), 1, 'scrolling blank area must join the frozen strips');
  assert.equal(alpha(20, 400), 0);
  assert.equal(alpha(900, 10), 0);
  assert.equal(
    panes.reduce((n, p) => n + p.width * p.height, 0),
    (bounds.width - bounds.rowHead) * (bounds.height - bounds.header),
  );
});

test('freezing beyond the used extent and billion-row scroll positions keep all masks viewport bounded', () => {
  const panes = gridBackdropPanes({
    width: 1900,
    height: 900,
    rowHead: 72,
    header: 33.75,
    pinWidth: 700,
    pinHeight: 400,
    right: -25_000_000_000,
    bottom: -25_000_000_000,
    fixedRight: 400,
    fixedBottom: 200,
  });
  const corner = panes.find((p) => p.x === 72 && p.y === 33.75);
  assert.ok(corner);
  assert.equal(corner.right, 328);
  assert.equal(corner.bottom, 166.25);
  for (const p of panes) {
    assert.ok(p.width <= 1900 && p.height <= 900);
    assert.ok(p.right >= -96);
    assert.ok(p.bottom >= -80);
  }
});

test('disabled texture and sheets still indexing skip all geometry and mask work', () => {
  const dom = installDom({ fromIndexHtml: true });
  try {
    const backdrop = document.getElementById('gridBackdrop');
    assert.ok(backdrop);
    backdrop.hidden = false;
    let geometryCalls = 0;
    const mode = new ReadingMode({
      button: document.createElement('button'),
      panel: document.createElement('div'),
      veil: document.createElement('div'),
    });
    mode.textureEnabled = false;
    const deps = makeDefaultSheetControllerDeps({
      headerHeight: () => {
        geometryCalls++;
        return 27;
      },
      rowHead: () => {
        geometryCalls++;
        return 36;
      },
      panels: {
        readingMode: mode,
        searchPanel: {
          leavePages: () => undefined,
          buildHits: () => undefined,
          applyHighlightClasses: () => undefined,
          displaySummary: () => undefined,
          renderResults: () => undefined,
          syncMode: () => undefined,
          poll: async () => undefined,
        },
        exportCenter: { adoptRecovered: () => false },
      },
    });
    deps.S.indexReady = true;
    const controller = new SheetController(deps);
    controller.syncGridBackdrop();
    assert.equal(backdrop.hidden, true);
    assert.equal(geometryCalls, 0, 'hidden decoration must not query grid geometry');

    mode.textureEnabled = true;
    deps.S.indexReady = false;
    backdrop.hidden = false;
    controller.syncGridBackdrop();
    assert.equal(backdrop.hidden, true);
    assert.equal(geometryCalls, 0, 'unindexed sheet must not query grid geometry');
  } finally {
    dom.restore();
  }
});
