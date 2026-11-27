import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { AnalysisCell, AnalysisResult } from '../contracts/analysis.ts';
import type { ChartExportLimits } from '../contracts/formats.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import { makeNoopApi } from '../test-helpers/grid-fixture.ts';
import {
  ChartExportController,
  type ChartExportOpenInput,
} from './chart-export.ts';
import type { ChartExportPayload, ChartExportSpec } from './chart-export-model.ts';
import type { ChartRenderSnapshot } from './charts.ts';
import {
  AnalysisPivotView,
  type AnalysisPivotViewState,
  type EditableAnalysisQuery,
} from './pivot-view.ts';
import { AnalysisTree, AnalysisTreeRenderer } from './tree.ts';

const tick = (): Promise<void> =>
  new Promise((resolve) => {
    setTimeout(resolve, 2);
  });

const limits: ChartExportLimits = {
  types: ['png', 'svg'],
  scales: [1, 2],
  minWidth: 640,
  maxWidth: 2400,
  minHeight: 480,
  maxHeight: 1600,
  defaultWidth: 1200,
  defaultHeight: 800,
  pixels: 20_000_000,
  dataBytes: 16 << 20,
  imageBytes: 20 << 20,
  svgBytes: 4 << 20,
  svgNodes: 20_000,
  packageBytes: 40 << 20,
};

const cell = (exact: string, display: string = exact): AnalysisCell => ({
  exact,
  display,
  count: exact ? 1 : 0,
});

function harness(
  override?: (
    payload: ChartExportPayload,
    spec: ChartExportSpec,
    lim: ChartExportLimits,
    checkpoint: () => Promise<void>,
    signal?: AbortSignal,
  ) => Promise<Blob> | Blob | undefined,
) {
  const dom = installDom();
  const urls: { readonly url: string; readonly blob: Blob }[] = [];
  const revoked: string[] = [];
  const messages: (readonly [string, string | undefined])[] = [];
  const images: { readonly payload: ChartExportPayload; readonly spec: ChartExportSpec }[] = [];
  let failURL = 0;

  const origCreateObjectURL = URL.createObjectURL.bind(URL);
  const origRevokeObjectURL = URL.revokeObjectURL.bind(URL);
  URL.createObjectURL = (blob: Blob | MediaSource): string => {
    if (failURL && urls.length + 1 === failURL) throw new Error('URL allocation failed');
    const url = `blob:local/${urls.length + 1}`;
    if (blob instanceof Blob) urls.push({ url, blob });
    return url;
  };
  URL.revokeObjectURL = (url: string): void => {
    revoked.push(url);
  };

  const result: AnalysisResult = {
    rowFields: ['C1'],
    columnKeys: ['金额'],
    totals: [cell('9007199254740993.31')],
    grand: cell('9007199254740993.31'),
    scanned: 1,
    included: 1,
    invalidNumbers: 0,
    missingFormulaCaches: 0,
    aggregation: 'sum',
    rows: [
      {
        keys: ['001'],
        values: [cell('9007199254740993.31')],
        total: cell('9007199254740993.31'),
        count: 1,
      },
    ],
  };

  let drawingResult: AnalysisResult = result;
  const drawing = (): ChartRenderSnapshot => ({
    result: drawingResult,
    mode: 'bar',
    theme: {
      themed: false,
      paper: '#fff',
      ink: '#263b55',
      muted: '#627184',
      grid: '#ccc',
      error: '#b42318',
    },
    numberSettings: { groupThousands: true, scientific: true, scientificExponent: 12 },
    hoverFrame: 0,
  });

  const center = new ChartExportController(limits, undefined, {
    snapshot: () => drawing(),
    descriptions: () => ['完整已接受结果'],
    image: async (payload, spec, lim, checkpoint, signal) => {
      images.push({ payload, spec });
      const custom = override?.(payload, spec, lim, checkpoint, signal);
      if (custom !== undefined) return await custom;
      return new Blob(['image'], {
        type: spec.type === 'png' ? 'image/png' : 'image/svg+xml',
      });
    },
  });

  const state = { id: 'one' };
  const canvas = document.createElement('canvas');
  const input: ChartExportOpenInput = {
    kind: 'analysis',
    origin: { workbookKey: 'content', book: 'path', job: 'accepted' },
    query: { source: 'wb:原表', rows: ['C1'], aggregate: 'sum' },
    fields: [{ id: 'C1', name: '原字段', column: 1 }],
    result,
    canvas,
    expectedResult: result,
    mode: 'bar',
    metricIndex: 0,
    title: '<img src=x>',
    source: '源<原表>.xlsx',
    scope: '当前选区',
    metric: '金额',
    unit: '元',
    current: () => state.id === 'one',
    notify: (msg, tone) => {
      messages.push([msg, tone]);
    },
  };

  return {
    center,
    state,
    input,
    urls,
    revoked,
    messages,
    images,
    setDrawingResult: (next: AnalysisResult) => {
      drawingResult = next;
    },
    failURL: (n: number) => {
      failURL = n;
    },
    restore: () => {
      URL.createObjectURL = origCreateObjectURL;
      URL.revokeObjectURL = origRevokeObjectURL;
      dom.restore();
    },
  };
}

async function localFiles(blob: Blob): Promise<Record<string, Buffer>> {
  const data = Buffer.from(await blob.arrayBuffer());
  const files: Record<string, Buffer> = {};
  let at = 0;
  while (data.readUInt32LE(at) === 0x04034b50) {
    const size = data.readUInt32LE(at + 18);
    const n = data.readUInt16LE(at + 26);
    const name = data.subarray(at + 30, at + 30 + n).toString();
    const start = at + 30 + n;
    files[name] = data.subarray(start, start + size);
    at = start + size;
  }
  return files;
}

test('opening performs no encoding; explicit generation packages exact accepted strings and safe source labels', async () => {
  const h = harness();
  try {
    assert.ok(h.center.open(h.input));
    assert.equal(h.images.length, 0);
    assert.equal(h.center.dialog.querySelector('img[src="x"]'), null);

    const mutableRowCell = h.input.result.rows[0]?.values[0] as { exact: string } | undefined;
    if (mutableRowCell) mutableRowCell.exact = '99';
    h.input.query.source = 'wb:later';

    await h.center.generate();
    assert.ok(h.center.accepted);
    const files = await localFiles(h.center.accepted.blob);
    const rawJson = files['exact-data.json'];
    assert.ok(rawJson);
    const data = JSON.parse(rawJson.toString()) as {
      result: { rows: { values: { exact: string }[] }[] };
      query: { source: string };
      output: {
        width: number;
        height: number;
        pixelWidth: number;
        pixelHeight: number;
        scale: number;
        type: string;
      };
      origin: { job: string };
    };
    assert.equal(data.result.rows[0]?.values[0]?.exact, '9007199254740993.31');
    assert.equal(data.query.source, 'wb:原表');
    assert.equal(data.output.width, 1200);
    assert.equal(data.output.height, 800);
    assert.equal(data.output.pixelWidth, 2400);
    assert.equal(data.output.pixelHeight, 1600);
    assert.equal(data.output.scale, 2);
    assert.equal(data.output.type, 'png');
    assert.equal(data.origin.job, 'accepted');
    assert.deepEqual(Object.keys(files), ['chart.png', 'exact-data.json', 'README.txt']);
    assert.equal(h.urls.length, 2);
    assert.equal(h.center.busy, false);
  } finally {
    h.restore();
  }
});

test('cancel followed by retry preserves the new complete package against a late ignored-abort image', async () => {
  let hold = true;
  let release: (() => void) | undefined;
  const h = harness(() =>
    hold
      ? new Promise<Blob>((resolve) => {
          release = () => resolve(new Blob(['late'], { type: 'image/png' }));
        })
      : undefined,
  );
  try {
    h.center.open(h.input);
    const first = h.center.generate();
    for (let i = 0; i < 30 && !release; i++) await tick();
    assert.ok(release);
    h.center.cancel();
    assert.equal(h.center.busy, false);
    hold = false;
    await h.center.generate();
    const accepted = h.center.accepted;
    release();
    await first;
    assert.equal(h.center.accepted, accepted);
    assert.equal(h.urls.length, 2);
    assert.equal(h.revoked.length, 0);
  } finally {
    h.restore();
  }
});

test('failed regeneration and failed URL publication retain the previous complete package and revoke new resources', async () => {
  let fail = false;
  const h = harness(() => {
    if (fail) throw new Error('image failed');
    return undefined;
  });
  try {
    h.center.open(h.input);
    await h.center.generate();
    const accepted = h.center.accepted;

    fail = true;
    await h.center.generate();
    assert.equal(h.center.accepted, accepted);
    assert.match(document.getElementById('chartExportStatus')?.textContent ?? '', /image failed.*保留上次结果/);
    assert.equal(h.revoked.length, 0);

    fail = false;
    h.failURL(4);
    await h.center.generate();
    assert.equal(h.center.accepted, accepted);
    assert.deepEqual(h.revoked, ['blob:local/3']);
    assert.match(document.getElementById('chartExportStatus')?.textContent ?? '', /URL allocation failed/);
  } finally {
    h.restore();
  }
});

test('source/result guard invalidation closes the export, revokes accepted URLs, and rejects stale downloads', async () => {
  const h = harness();
  try {
    h.center.open(h.input);
    await h.center.generate();
    h.state.id = 'same-content-other-path';
    assert.equal(h.center.revalidate(), false);
    assert.equal(h.center.dialog.open, false);
    assert.equal(h.center.accepted, null);
    assert.equal(h.revoked.length, 2);
    for (const id of ['chartExportImageDownload', 'chartExportDownload']) {
      const node = document.getElementById(id);
      assert.ok(node instanceof HTMLAnchorElement);
      const ev = new Event('click', { cancelable: true });
      node.dispatchEvent(ev);
      assert.equal(ev.defaultPrevented, true);
      assert.equal(node.getAttribute('href'), null);
    }
  } finally {
    h.restore();
  }
});

test('direct image and ZIP share the complete candidate, retain its filename after option edits, and block both downloads during replacement', async () => {
  let hold = false;
  let release: (() => void) | undefined;
  const h = harness(() =>
    hold
      ? new Promise<Blob>((resolve) => {
          release = () => resolve(new Blob(['replacement'], { type: 'image/svg+xml' }));
        })
      : undefined,
  );
  try {
    h.center.open(h.input);
    await h.center.generate();
    const image = document.getElementById('chartExportImageDownload');
    const zip = document.getElementById('chartExportDownload');
    assert.ok(image instanceof HTMLAnchorElement && zip instanceof HTMLAnchorElement);
    const accepted = h.center.accepted;
    assert.ok(accepted);
    assert.equal(image.href, accepted.imageURL);
    assert.equal(image.download, 'ExcelLens-Chart.png');
    assert.equal(zip.href, accepted.packageURL);
    assert.equal(h.urls.find((x) => x.url === image.href)?.blob, accepted.image);
    const files = await localFiles(accepted.blob);
    assert.equal(files['chart.png']?.toString(), await accepted.image.text());
    assert.equal(h.urls.length, 2);

    const typeSel = document.getElementById('chartExportType');
    const optionsEl = document.getElementById('chartExportOptions');
    assert.ok(typeSel instanceof HTMLSelectElement && optionsEl);
    typeSel.value = 'svg';
    optionsEl.dispatchEvent(new Event('change'));
    assert.equal(image.download, 'ExcelLens-Chart.png');
    assert.equal(image.href, accepted.imageURL);

    hold = true;
    const pending = h.center.generate();
    for (let i = 0; i < 30 && !release; i++) await tick();
    assert.ok(release);
    for (const node of [image, zip]) {
      assert.equal(node.getAttribute('aria-disabled'), 'true');
      assert.equal(node.getAttribute('tabindex'), '-1');
      const ev = new Event('click', { cancelable: true });
      node.dispatchEvent(ev);
      assert.equal(ev.defaultPrevented, true);
    }
    release();
    await pending;
    assert.ok(h.center.accepted);
    assert.equal(image.download, 'ExcelLens-Chart.svg');
    assert.equal(image.href, h.center.accepted.imageURL);
    assert.equal(zip.href, h.center.accepted.packageURL);
    assert.deepEqual(h.revoked, [accepted.imageURL, accepted.packageURL]);
    for (const node of [image, zip]) {
      assert.equal(node.getAttribute('aria-disabled'), 'false');
      assert.equal(node.getAttribute('tabindex'), '0');
      const ev = new Event('click', { cancelable: true });
      node.dispatchEvent(ev);
      assert.equal(ev.defaultPrevented, false);
    }
    h.center.close();
    assert.equal(image.getAttribute('href'), null);
    assert.equal(zip.getAttribute('href'), null);
    assert.equal(h.revoked.length, 4);
  } finally {
    h.restore();
  }
});

test('late generation after source change produces no public URL and leaves no retained model', async () => {
  let release: (() => void) | undefined;
  const h = harness(
    () =>
      new Promise<Blob>((resolve) => {
        release = () => resolve(new Blob(['late'], { type: 'image/png' }));
      }),
  );
  try {
    h.center.open(h.input);
    const pending = h.center.generate();
    for (let i = 0; i < 30 && !release; i++) await tick();
    assert.ok(release);
    h.state.id = 'new-result';
    release();
    await pending;
    assert.equal(h.urls.length, 0);
    assert.equal(h.center.dialog.open, false);
    assert.equal(h.center.bound, null);
    assert.equal(h.center.busy, false);
  } finally {
    h.restore();
  }
});

test('invalid sizes refuse before encoding, SVG uses logical dimensions, and option edits never relabel an old package', async () => {
  const h = harness();
  try {
    h.center.open(h.input);
    const widthInput = document.getElementById('chartExportWidth');
    const typeSel = document.getElementById('chartExportType');
    const optionsEl = document.getElementById('chartExportOptions');
    assert.ok(
      widthInput instanceof HTMLInputElement && typeSel instanceof HTMLSelectElement && optionsEl,
    );
    widthInput.value = '1e3';
    await h.center.generate();
    assert.equal(h.images.length, 0);
    assert.match(document.getElementById('chartExportStatus')?.textContent ?? '', /整数/);

    widthInput.value = '1200';
    typeSel.value = 'svg';
    await h.center.generate();
    assert.equal(h.images[0]?.spec.scale, 1);
    const accepted = h.center.accepted;
    assert.ok(accepted);
    widthInput.value = '1000';
    optionsEl.dispatchEvent(new Event('change'));
    assert.equal(h.center.accepted, accepted);
    assert.match(document.getElementById('chartExportStatus')?.textContent ?? '', /上次结果/);
    assert.equal(h.center.accepted.spec.width, 1200);
  } finally {
    h.restore();
  }
});

test('native queued close events cannot clear an immediate reopen; unpainted or foreign draw snapshots refuse opening', async () => {
  const h = harness();
  try {
    h.center.open(h.input);
    h.center.close();
    assert.ok(h.center.open(h.input));
    await tick();
    assert.ok(h.center.bound);
    await h.center.generate();
    assert.ok(h.center.accepted);

    h.center.close();
    h.setDrawingResult({ ...h.input.result });
    assert.equal(h.center.open(h.input), false);
    assert.equal(h.messages.length, 1);
    assert.equal(h.center.bound, null);
    assert.equal(h.revoked.length, 2);
  } finally {
    h.restore();
  }
});

function chartHarness() {
  const dom = installDom({ fromIndexHtml: true });
  const frames: FrameRequestCallback[] = [];
  Object.defineProperty(globalThis, 'requestAnimationFrame', {
    value: (fn: FrameRequestCallback): number => {
      frames.push(fn);
      return frames.length;
    },
    configurable: true,
    writable: true,
  });

  const painted: {
    readonly canvas: HTMLCanvasElement | null | undefined;
    readonly result: unknown;
    readonly mode: unknown;
  }[] = [];
  const exports: ChartExportOpenInput[] = [];

  const result: AnalysisResult = {
    rowFields: ['C1'],
    columnKeys: ['金额'],
    totals: [cell('1')],
    grand: cell('1'),
    scanned: 1,
    included: 1,
    invalidNumbers: 0,
    missingFormulaCaches: 0,
    aggregation: 'sum',
    rows: [
      {
        keys: ['原分类'],
        values: [cell('1')],
        total: cell('1'),
        count: 1,
        metricValues: [[cell('1'), cell('9007199254740993.31')]],
      },
    ],
  };

  const q: EditableAnalysisQuery = {
    source: 'wb:原表',
    headerRow: 1,
    rows: ['C1'],
    columns: [],
    value: 'C2',
    aggregate: 'sum',
    metrics: [
      { field: 'C2', aggregate: 'sum' },
      { field: 'C3', aggregate: 'sum' },
    ],
    filters: [],
    transforms: [],
    sort: 'value_desc',
    top: 30,
    precision: 2,
    chart: 'bar',
    chartMetric: 0,
    layout: '',
  };

  const state: AnalysisPivotViewState = {
    opened: true,
    tab: 'pivot',
    fingerprint: 'content',
    book: 'path',
    openSerial: 1,
    reportOpenSerial: 2,
    reportPending: 0,
    jobID: 'done',
    activeReportID: '',
    chartBusy: false,
    pivotLimit: 150,
    workspace: { workbook: '源.xlsx', sheets: [] },
    query: null,
    activeResult: result,
    activeResultQuery: q,
    activeResultFields: [{ id: 'C3', name: '完成时金额', column: 3 }],
    chartCapture: null,
    drillQuery: null,
    drillJob: '',
    drillKeys: [],
    drillColumn: '',
    drillColumnSelected: undefined,
    drillOffset: 0,
    drillAbort: null,
  };

  const chartSelect = document.getElementById('analysisChartSelect');
  const metricSelect = document.getElementById('analysisChartMetric');
  assert.ok(chartSelect instanceof HTMLSelectElement && metricSelect instanceof HTMLSelectElement);
  chartSelect.value = 'bar';
  metricSelect.value = '0';

  const tree = new AnalysisTree({
    api: makeNoopApi(),
    context: () => ({ bookKey: state.fingerprint, book: state.book, opened: state.opened }),
    limits: { nodes: 8, bytes: 100_000, pageRows: 150, active: 2 },
  });

  const view = new AnalysisPivotView({
    state,
    api: makeNoopApi(),
    toast: () => undefined,
    setText: (id, text) => {
      const el = document.getElementById(id);
      if (el) el.textContent = text;
    },
    tree,
    treeRenderer: new AnalysisTreeRenderer(),
    issuePanel: null,
    snapshots: null,
    navigateComposition: async () => undefined,
    navigateSheet: async () => undefined,
    navigateWorkbook: () => undefined,
    close: () => undefined,
    drawChart: (canvas, res, mode) => {
      painted.push({ canvas, result: res, mode });
      return () => null;
    },
    openChartExport: (input) => {
      exports.push(input);
      return true;
    },
    revalidateChartExport: () => false,
  });

  return {
    view,
    state,
    q,
    result,
    frames,
    painted,
    exports,
    chartSelect,
    metricSelect,
    restore: () => dom.restore(),
  };
}

test('old animation frames cannot draw a new metric with an old projection or enable export for a foreign source', () => {
  const h = chartHarness();
  try {
    h.view.renderChart();
    h.metricSelect.value = '1';
    h.view.renderChart();
    h.frames.shift()?.(0);
    assert.equal(h.painted.length, 0);
    h.frames.shift()?.(0);
    assert.equal(h.painted.length, 1);
    const paintedResult = h.painted[0]?.result as AnalysisResult;
    assert.equal(paintedResult.rows[0]?.values[0]?.exact, '9007199254740993.31');

    h.state.query = { ...h.q, source: 'wb:new' };
    h.view.exportChartImage();
    const exported = h.exports[0];
    assert.ok(exported);
    assert.ok(exported.kind === 'analysis');
    assert.equal(exported.metric, '完成时金额 · 求和');
    assert.equal(exported.query.source, 'wb:原表');
    assert.equal(exported.current(), true);
    h.state.book = 'other-path';
    assert.equal(exported.current(), false);

    h.view.renderChart();
    h.state.activeResult = { ...h.result };
    h.frames.shift()?.(0);
    assert.equal(h.painted.length, 1);
  } finally {
    h.restore();
  }
});

test('chart type changes persist the displayed presentation on the accepted report without changing its data source', () => {
  const h = chartHarness();
  try {
    h.state.query = { ...h.q, source: 'wb:builder' };
    h.chartSelect.value = 'donut';
    h.view.setChartMode();
    assert.equal(h.q.chart, 'donut');
    assert.equal(h.state.query.chart, 'donut');
    assert.equal(h.q.source, 'wb:原表');
    assert.equal(h.state.query.source, 'wb:builder');
    h.frames.shift()?.(0);
    h.view.exportChartImage();
    const exported = h.exports[0];
    assert.ok(exported);
    assert.ok(exported.kind === 'analysis');
    assert.equal(exported.mode, 'donut');
    assert.equal(exported.query.chart, 'donut');
  } finally {
    h.restore();
  }
});

test('composition chart exports retain the completed definition and source count after builder edits', () => {
  const h = chartHarness();
  try {
    h.q.source = 'append:accepted';
    h.q.composition = {
      version: 1,
      name: '月度联合',
      fields: ['金额'],
      inputs: [
        { ref: { book: 'left', fingerprint: 'c1', source: 'wb:a', headerRow: 1 }, columns: [1] },
        { ref: { book: 'right', fingerprint: 'c2', source: 'wb:b', headerRow: 1 }, columns: [1] },
      ],
    };
    h.q.filters = [{ field: 'C2', op: 'gt', value: '0' }];
    h.view.renderChart();
    h.frames.shift()?.(0);
    h.state.query = { ...h.q, source: 'wb:builder', composition: undefined };
    h.view.exportChartImage();
    const exported = h.exports[0];
    assert.ok(exported);
    assert.ok(exported.kind === 'analysis');
    assert.equal(exported.source, '源.xlsx / 月度联合');
    assert.equal(exported.scope, '2 个来源 · 已筛选');
    assert.equal(exported.query.source, 'append:accepted');
    assert.equal(exported.query.composition?.name, '月度联合');
    assert.equal((exported.origin as { job: string }).job, 'done');
  } finally {
    h.restore();
  }
});
