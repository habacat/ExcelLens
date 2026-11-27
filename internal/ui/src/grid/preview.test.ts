import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { PreviewResponse, SheetViewportResponse } from '../contracts/workbook.ts';
import type { ViewportRequest } from './state.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import {
  makeDefaultViewportPipelineDeps,
  ViewportPipeline,
} from '../test-helpers/grid-fixture.ts';

function makePreviewRequest(overrides: Partial<ViewportRequest> = {}): ViewportRequest {
  return {
    context: '',
    filtered: false,
    filterId: '',
    viewId: '',
    excludeThroughRow: 0,
    displayStart: 1,
    displayEnd: 20,
    visibleStart: 1,
    visibleEnd: 20,
    visibleColStart: 1,
    visibleColEnd: 10,
    row: 1,
    offset: 0,
    limit: 20,
    startCol: 1,
    cols: 10,
    selectedCols: null,
    leadRow: 0,
    leadCol: 0,
    ...overrides,
  };
}

function setupPreview(apiImpl: () => Promise<PreviewResponse>) {
  const dom = installDom({ fromIndexHtml: true });
  const deps = makeDefaultViewportPipelineDeps({
    api: async <T = unknown>(): Promise<T> => (await apiImpl()) as T,
  });
  deps.S.filtered = false;
  deps.S.sheetEpoch = 3;
  deps.S.indexReady = false;
  deps.S.sheet = '台账';
  const pipeline = new ViewportPipeline(deps);
  const renders: { readonly data: SheetViewportResponse; readonly request: ViewportRequest }[] = [];
  let req = makePreviewRequest();
  pipeline.getViewportRequest = () => req;
  pipeline.updateCanvasSize = () => undefined;
  pipeline.renderGrid = (data, request) => {
    renders.push({ data, request });
  };
  return {
    S: deps.S,
    pipeline,
    renders,
    setRequest: (next: Partial<ViewportRequest>) => {
      req = makePreviewRequest(next);
    },
    restore: () => dom.restore(),
  };
}

test('preview paints a bounded prefix without claiming complete indexing', async () => {
  const h = setupPreview(async () => ({
    available: true,
    partial: true,
    maxRow: 80,
    maxCol: 39,
    rows: [],
  }));
  try {
    await h.pipeline.paintInitialPreview('台账', 3);
    assert.equal(h.renders.length, 1);
    assert.equal(h.S.indexReady, false);
    assert.equal(h.S.previewOnly, true);
    assert.equal(h.renders[0]?.request.displayEnd, 28);
    assert.equal(h.renders[0].data.colCount, 22);
    assert.match(document.getElementById('gridStatus')?.textContent ?? '', /首屏预览/);
  } finally {
    h.restore();
  }
});

test('late preview does not overwrite a completed or different sheet', async () => {
  for (const change of [
    (s: ReturnType<typeof setupPreview>['S']) => {
      s.indexReady = true;
    },
    (s: ReturnType<typeof setupPreview>['S']) => {
      s.sheetEpoch = 4;
    },
    (s: ReturnType<typeof setupPreview>['S']) => {
      s.previewAbort?.abort();
    },
  ]) {
    let release: ((value: PreviewResponse) => void) | undefined;
    const response = new Promise<PreviewResponse>((resolve) => {
      release = resolve;
    });
    const h = setupPreview(() => response);
    try {
      const pending = h.pipeline.paintInitialPreview('台账', 3);
      change(h.S);
      release?.({ available: true, maxRow: 80, maxCol: 4, rows: [] });
      await pending;
      assert.equal(h.renders.length, 0);
    } finally {
      h.restore();
    }
  }
});

test('an existing filter never displays unfiltered source rows as its preview', async () => {
  let called = false;
  const h = setupPreview(async () => {
    called = true;
    return { available: true, maxRow: 80, maxCol: 4, rows: [] };
  });
  try {
    h.S.filtered = true;
    await h.pipeline.paintInitialPreview('台账', 3);
    assert.equal(called, false);
    assert.equal(h.renders.length, 0);
  } finally {
    h.restore();
  }
});

test('preview scrolling repaints from existing prefix data without another request', async () => {
  let calls = 0;
  const h = setupPreview(async () => {
    calls++;
    return { available: true, maxRow: 80, maxCol: 39, rows: [] };
  });
  try {
    await h.pipeline.paintInitialPreview('台账', 3);
    h.setRequest({
      visibleStart: 40,
      visibleEnd: 60,
      visibleColStart: 15,
      visibleColEnd: 24,
    });
    h.pipeline.renderInitialPreview();
    assert.equal(calls, 1);
    assert.equal(h.renders[1]?.request.displayStart, 32);
    assert.equal(h.renders[1].request.displayEnd, 68);
    assert.equal(h.renders[1].data.columns?.length, 34);
    assert.equal(h.renders[1].data.startCol, 3);
    assert.equal(h.S.indexReady, false);
  } finally {
    h.restore();
  }
});
