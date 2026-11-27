import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { AnalysisCell, AnalysisResult } from '../contracts/analysis.ts';
import type { ChartExportLimits } from '../contracts/formats.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import { CHART_MODES, describeChart } from './charts.ts';
import {
  ChartExportModel,
  type ChartExportAnalysisInput,
  type ChartExportSpec,
} from './chart-export-model.ts';
import { ChartExportSurface } from './chart-export-surface.ts';
import {
  DEFAULT_ANALYSIS_NUMBER_OPTIONS,
  getAnalysisNumberOptions,
  setAnalysisNumberOptions,
} from './numbers.ts';

const tick = (): Promise<void> =>
  new Promise((resolve) => {
    setImmediate(resolve);
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

const spec: ChartExportSpec = {
  type: 'svg',
  width: 1200,
  height: 800,
  scale: 1,
  background: 'captured',
};

const cell = (exact: string, display: string = exact): AnalysisCell => ({
  exact,
  display,
  count: exact ? 1 : 0,
});

function setup() {
  const dom = installDom();
  setAnalysisNumberOptions(DEFAULT_ANALYSIS_NUMBER_OPTIONS);

  const canvases: HTMLCanvasElement[] = [];
  const callbacks: ((blob: Blob | null) => void)[] = [];
  const makeContext = () => ({
    font: '11px "Segoe UI"',
    fillStyle: '',
    strokeStyle: '',
    lineWidth: 1,
    textAlign: 'left' as CanvasTextAlign,
    measureText(value: string) {
      const size = Number.parseFloat(this.font.replace('bold ', '')) || 11;
      return { width: Array.from(value).length * size * 0.55 };
    },
    setTransform() {},
    save() {},
    restore() {},
    translate() {},
    fillRect() {},
    fillText() {},
    beginPath() {},
    moveTo() {},
    lineTo() {},
    arc() {},
    closePath() {},
    fill() {},
    stroke() {},
  });

  const origCreate = dom.document.createMockElement.bind(dom.document);
  dom.document.createMockElement = (tag: string) => {
    const el = origCreate(tag);
    if (tag.toLowerCase() === 'canvas') {
      const ctx = makeContext();
      Object.defineProperty(el, 'getContext', {
        value: () => ctx,
        configurable: true,
        writable: true,
      });
      Object.defineProperty(el, 'toBlob', {
        value: (cb: (blob: Blob | null) => void) => {
          callbacks.push(cb);
        },
        configurable: true,
        writable: true,
      });
      if (el instanceof HTMLCanvasElement) canvases.push(el);
    }
    return el;
  };

  const result: AnalysisResult = {
    rowFields: ['C1'],
    columnKeys: ['金额', '次列'],
    totals: [cell('9007199254740993.31'), cell('1/3', '0.33')],
    grand: cell('9007199254740993.31'),
    scanned: 2,
    included: 2,
    invalidNumbers: 0,
    missingFormulaCaches: 0,
    aggregation: 'sum',
    rows: [
      {
        keys: ['<img src=x> 中😀'],
        values: [cell('9007199254740993.31'), cell('1/3', '0.33')],
        total: cell('9007199254740993.31'),
        count: 1,
        metricValues: [
          [cell('9007199254740993.31'), cell('1/3', '0.33')],
          [cell('1/3', '0.33'), cell('2/3', '0.67')],
        ],
      },
      {
        keys: ['001'],
        values: [cell(''), cell('0')],
        total: cell('0'),
        count: 1,
        metricValues: [
          [cell(''), cell('2/3', '0.67')],
          [cell('0'), cell('0')],
        ],
      },
    ],
  };

  const input: ChartExportAnalysisInput = {
    kind: 'analysis',
    origin: { workbookKey: 'content', book: 'path', job: 'completed' },
    query: {
      source: 'wb:明细',
      sourceScope: { version: 1, mode: 'selection', fingerprint: 'content' },
      rows: ['C1'],
      aggregate: 'sum',
    },
    fields: [{ id: 'C1', name: '原字段', column: 1 }],
    result,
    mode: 'bar',
    metricIndex: 0,
    title: '<script>alert(1)</script> 中😀',
    source: '源文件.xlsx / 明细',
    scope: '当前画面选区',
    metric: '金额',
    unit: '源指标原单位',
    theme: {
      themed: true,
      paper: '#272b30',
      ink: '#dce2e8',
      muted: '#abb5bf',
      grid: '#39424c',
      error: '#ef9999',
    },
    numberSettings: getAnalysisNumberOptions(),
    applicationVersion: 'test',
  };

  return {
    canvases,
    callbacks,
    measure: makeContext(),
    input,
    result,
    restore: () => {
      setAnalysisNumberOptions(DEFAULT_ANALYSIS_NUMBER_OPTIONS);
      dom.restore();
    },
  };
}

function unpack(bytes: ArrayBuffer) {
  const buf = Buffer.from(bytes);
  const files: { readonly name: string; readonly data: Buffer }[] = [];
  let at = 0;
  const crc = (data: Buffer): number => {
    let n = 0xffffffff;
    for (const b of data) {
      n ^= b;
      for (let k = 0; k < 8; k++) n = n & 1 ? (n >>> 1) ^ 0xedb88320 : n >>> 1;
    }
    return (n ^ 0xffffffff) >>> 0;
  };
  while (buf.readUInt32LE(at) === 0x04034b50) {
    assert.equal(buf.readUInt16LE(at + 8), 0);
    assert.equal(buf.readUInt16LE(at + 6), 0x800);
    const size = buf.readUInt32LE(at + 18);
    const nameLength = buf.readUInt16LE(at + 26);
    const name = buf.subarray(at + 30, at + 30 + nameLength).toString('utf8');
    const data = buf.subarray(at + 30 + nameLength, at + 30 + nameLength + size);
    assert.equal(crc(data), buf.readUInt32LE(at + 14));
    files.push({ name, data });
    at += 30 + nameLength + size;
  }
  const directoryStart = at;
  for (const file of files) {
    assert.equal(buf.readUInt32LE(at), 0x02014b50);
    assert.equal(buf.readUInt32LE(at + 24), file.data.length);
    const n = buf.readUInt16LE(at + 28);
    assert.equal(buf.subarray(at + 46, at + 46 + n).toString(), file.name);
    at += 46 + n;
  }
  assert.equal(buf.readUInt32LE(at), 0x06054b50);
  assert.equal(buf.readUInt16LE(at + 10), files.length);
  assert.equal(buf.readUInt32LE(at + 12), at - directoryStart);
  assert.equal(buf.readUInt32LE(at + 16), directoryStart);
  assert.equal(buf.length, at + 22);
  return files;
}

test('bounded JSON preserves exact strings, identifiers, Unicode, control characters, missing states and native JSON omission rules', () => {
  const value = {
    amount: '9007199254740993.31',
    id: '0001',
    fraction: '1/3',
    missing: { exact: '', cacheMissing: true },
    text: '中😀\u0000\r\n<&>',
    array: [null, undefined, 0],
    omitted: undefined,
  };
  const want = JSON.stringify(value);
  const bytes = Buffer.byteLength(want);
  assert.equal(ChartExportModel.stringify(value, bytes), want);
  assert.throws(() => ChartExportModel.stringify(value, bytes - 1), /容量/);
  for (const bad of [NaN, Infinity, 1n, () => 1]) {
    assert.throws(() => ChartExportModel.stringify({ bad }, 1000));
  }
  const cycle: Record<string, unknown> = {};
  cycle.self = cycle;
  assert.throws(() => ChartExportModel.stringify(cycle, 1000), /循环/);
  assert.equal(
    ChartExportModel.stringify({ a: value, b: value }, 100_000),
    JSON.stringify({ a: value, b: value }),
  );
  assert.throws(() => ChartExportModel.stringify({ text: '\u0000'.repeat(300) }, 1000), /容量/);
});

test('captured results detach the completed metric and source from later builder or original-object edits', () => {
  const h = setup();
  try {
    const input = { ...h.input, metricIndex: 1 };
    const payload = ChartExportModel.capture(input, limits);
    const plot = ChartExportModel.plot(payload);
    assert.equal(plot.rows[0]?.values[0]?.exact, '1/3');
    assert.equal(plot.rows[1]?.values[0]?.exact, '2/3');

    input.query.source = 'wb:other';
    const field0 = input.fields[0] as { name: string } | undefined;
    if (field0) field0.name = 'changed';
    const metricCell = h.result.rows[0]?.metricValues?.[0]?.[1] as { exact: string } | undefined;
    if (metricCell) metricCell.exact = '99';

    assert.equal(payload.kind, 'analysis');
    assert.equal(payload.query.source, 'wb:明细');
    assert.equal(payload.fields[0]?.name, '原字段');
    assert.equal(ChartExportModel.plot(payload).rows[0]?.values[0]?.exact, '1/3');
    assert.equal((payload.origin as { job: string }).job, 'completed');
    assert.throws(() => ChartExportModel.capture(input, { ...limits, dataBytes: 100 }), /容量/);
  } finally {
    h.restore();
  }
});

test('drawing descriptions match the shared plan for every chart and preserve actual pie row indexes', () => {
  const h = setup();
  try {
    const r: AnalysisResult = {
      rowFields: ['C1'],
      columnKeys: Array.from({ length: 14 }, (_, i) => String(i)),
      totals: [],
      grand: cell('1'),
      scanned: 100,
      included: 100,
      invalidNumbers: 0,
      missingFormulaCaches: 0,
      aggregation: 'sum',
      rows: Array.from({ length: 100 }, (_, i) => ({
        keys: [String(i)],
        values: Array.from({ length: 14 }, () =>
          cell(i === 0 ? '-1' : i === 1 ? '0' : '1', '1'),
        ),
        total: cell('1'),
        count: 1,
      })),
    };
    const expected: readonly (readonly [typeof CHART_MODES[number], number, number])[] = [
      ['bar', 60, 12],
      ['horizontal', 24, 1],
      ['line', 60, 12],
      ['area', 60, 12],
      ['donut', 16, 1],
      ['pie', 16, 1],
      ['scatter', 60, 12],
      ['stacked', 60, 12],
      ['heatmap', 28, 12],
    ];
    for (const [mode, rows, series] of expected) {
      const d = describeChart(r, mode);
      assert.equal(d.rowIndices.length, rows, mode);
      assert.equal(d.seriesIndices.length, series, mode);
      assert.equal(d.totalRows, 100);
      assert.equal(d.totalSeries, 14);
      if (d.positiveOnly) {
        assert.equal(d.rowIndices[0], 2);
        assert.equal(d.rowIndices.at(-1), 17);
        assert.equal(d.negativeExcluded, true);
      }
    }
    const firstRow = r.rows[0];
    assert.ok(firstRow);
    const unrep: AnalysisResult = {
      ...r,
      rows: [
        {
          ...firstRow,
          values: [cell('1e400', '1'), ...firstRow.values.slice(1)],
        },
        ...r.rows.slice(1),
      ],
    };
    assert.ok(describeChart(unrep).unrepresentable);
    assert.throws(() => ChartExportModel.capture({ ...h.input, result: unrep }, limits), /有效绘图/);
  } finally {
    h.restore();
  }
});

test('image dimensions and physical pixel admission are exact, typed and checked before creating a bitmap', async () => {
  const h = setup();
  try {
    assert.deepEqual(
      { ...ChartExportModel.dimensions({ ...spec, type: 'png', scale: 2 }, limits) },
      { width: 2400, height: 1600, pixels: 3_840_000 },
    );
    for (const bad of [
      { width: 639 },
      { width: '1200' },
      { height: 1601 },
      { type: 'jpeg' },
      { scale: 2 },
      { background: 'url(x)' },
    ]) {
      const candidate: unknown = { ...spec, ...bad };
      assert.throws(() =>
        ChartExportModel.dimensions(candidate as ChartExportSpec, limits),
      );
    }
    const payload = ChartExportModel.capture(h.input, limits);
    await assert.rejects(
      ChartExportSurface.image(
        payload,
        { ...spec, type: 'png', scale: 2 },
        { ...limits, pixels: 1 },
        async () => undefined,
      ),
      /容量/,
    );
    assert.equal(h.canvases.length, 0);
  } finally {
    h.restore();
  }
});

test('all nine formats share document paint and produce bounded vectors with safely escaped source text', () => {
  const h = setup();
  try {
    const original = JSON.stringify(h.input);
    for (const mode of CHART_MODES) {
      const payload = ChartExportModel.capture({ ...h.input, mode }, limits);
      const ctx = new ChartExportSurface.SVGContext(h.measure, limits);
      ChartExportSurface.paintDocument(ctx, payload, spec);
      const svg = ctx.serialize(spec.width, spec.height, payload.chart.title);
      assert.match(svg, /<svg xmlns="http:\/\/www\.w3\.org\/2000\/svg"/);
      assert.match(svg, /&lt;script&gt;alert\(1\)&lt;\/script&gt;/);
      assert.ok(!/<(?:script|image|foreignObject)\b|\shref=|\sonload=/.test(svg));
      assert.ok(!/NaN|Infinity/.test(svg));
      assert.match(svg, /exact-data\.json/);
      assert.ok(ChartExportModel.stringify(payload, limits.dataBytes).includes('9007199254740993.31'));
      if (['pie', 'donut', 'line', 'area', 'scatter'].includes(mode)) {
        assert.match(svg, /A[\d.]/, mode);
      }
    }
    assert.equal(JSON.stringify(h.input), original);
  } finally {
    h.restore();
  }
});

test('SVG arcs, state, negative rectangles and Unicode replacement obey finite geometry and resource limits', () => {
  const h = setup();
  try {
    const ctx = new ChartExportSurface.SVGContext(h.measure, limits);
    ctx.beginPath();
    ctx.arc(20, 20, 10, 0, Math.PI * 2);
    ctx.arc(20, 20, 5, Math.PI * 2, 0, true);
    ctx.closePath();
    ctx.fill();
    ctx.fillRect(10, 10, -5, -7);
    const svg = ctx.serialize(100, 100, 'title');
    const pathMatch = /<path d="([^"]+)"/.exec(svg);
    assert.ok(pathMatch?.[1]);
    const path = pathMatch[1];
    assert.equal((path.match(/A/g) ?? []).length, 4);
    assert.equal((path.match(/ 0 0 1 /g) ?? []).length, 2);
    assert.equal((path.match(/ 0 0 0 /g) ?? []).length, 2);
    assert.match(svg, /x="5" y="3" width="5" height="7"/);

    for (const action of [
      () => ctx.arc(1, 1, -1, 0, 2),
      () => ctx.moveTo(NaN, 0),
      () => ctx.translate(Infinity, 0),
      () => {
        ctx.fillStyle = 'url(https://x)';
        ctx.fillRect(0, 0, 1, 1);
      },
    ]) {
      assert.throws(action);
    }
    assert.equal(ChartExportSurface.xml('中😀\u0000\u000b\uD800<&>'), '中😀\uFFFD\uFFFD\uFFFD&lt;&amp;&gt;');
    const tiny = new ChartExportSurface.SVGContext(h.measure, { ...limits, svgNodes: 1 });
    tiny.fillRect(0, 0, 1, 1);
    assert.throws(() => tiny.fillRect(0, 0, 1, 1), /容量/);
    const short = new ChartExportSurface.SVGContext(h.measure, { ...limits, svgBytes: 30 });
    assert.throws(() => short.fillRect(0, 0, 1, 1), /容量/);
  } finally {
    h.restore();
  }
});

test('stored ZIP has independently verified CRC, directory, exact bytes and all-or-nothing byte admission', async () => {
  const files = [
    { name: 'chart.svg', bytes: new TextEncoder().encode('<svg/>') },
    {
      name: 'exact-data.json',
      bytes: new TextEncoder().encode(
        ChartExportModel.stringify({ amount: '9007199254740993.31', id: '001', text: '中😀' }, 1000),
      ),
    },
    { name: 'README.txt', bytes: new Uint8Array() },
  ];
  const blob = await ChartExportModel.zip(files, 100_000);
  const unpacked = unpack(await blob.arrayBuffer());
  assert.equal(unpacked[1]?.data.toString(), new TextDecoder().decode(files[1]?.bytes));
  assert.equal(unpacked.length, 3);

  await assert.rejects(ChartExportModel.zip(files, blob.size - 1), /容量/);
  assert.equal((await ChartExportModel.zip(files, blob.size)).size, blob.size);

  const firstFile = files[0];
  assert.ok(firstFile);
  const invalidCases: readonly (readonly { readonly name: string; readonly bytes: unknown }[])[] = [
    [{ name: '../source', bytes: new Uint8Array() }],
    [firstFile, firstFile],
    [{ name: 'x', bytes: 'text' }],
  ];
  for (const bad of invalidCases) {
    await assert.rejects(
      ChartExportModel.zip(
        bad as readonly { readonly name: string; readonly bytes: Uint8Array<ArrayBuffer> }[],
        10_000,
      ),
    );
  }
  let calls = 0;
  await assert.rejects(
    ChartExportModel.zip([{ name: 'x', bytes: new Uint8Array(3 << 20) }], 4 << 20, async () => {
      if (++calls === 2) throw new Error('cancel');
    }),
    /cancel/,
  );
});

test('native PNG cancellation retains one real encoder, one cancellable waiter and releases all canvases on late completion', async () => {
  const h = setup();
  try {
    const payload = ChartExportModel.capture(h.input, limits);
    const png: ChartExportSpec = { ...spec, type: 'png' };
    const control = () => {
      const c = new AbortController();
      return {
        c,
        check: async () => {
          if (c.signal.aborted) {
            const err = new Error('cancel');
            err.name = 'AbortError';
            throw err;
          }
        },
      };
    };

    const a = control();
    const first = ChartExportSurface.image(payload, png, limits, a.check, a.c.signal).then(
      () => null,
      (e: unknown) => e,
    );
    for (let i = 0; i < 20 && !h.callbacks.length; i++) await tick();
    assert.equal(h.callbacks.length, 1);
    a.c.abort();

    const b = control();
    const second = ChartExportSurface.image(payload, png, limits, b.check, b.c.signal).then(
      () => null,
      (e: unknown) => e,
    );
    await tick();
    assert.equal(h.canvases.length, 1);

    const c = control();
    await assert.rejects(
      ChartExportSurface.image(payload, png, limits, c.check, c.c.signal),
      /等待编码/,
    );
    b.c.abort();
    const secondErr = await second;
    assert.ok(secondErr instanceof Error && secondErr.name === 'AbortError');

    const d = control();
    const last = ChartExportSurface.image(payload, png, limits, d.check, d.c.signal);
    await tick();
    assert.equal(h.canvases.length, 1);

    h.callbacks[0]?.(new Blob([new Uint8Array([137, 80, 78, 71, 13, 10, 26, 10])], { type: 'image/png' }));
    const firstErr = await first;
    assert.ok(firstErr instanceof Error && firstErr.name === 'AbortError');

    const callbackCount = (): number => h.callbacks.length;
    for (let i = 0; i < 20 && callbackCount() < 2; i++) await tick();
    assert.equal(h.callbacks.length, 2);
    h.callbacks[1]?.(new Blob(['png'], { type: 'image/png' }));
    assert.equal((await last).type, 'image/png');
    assert.ok(h.canvases.every((canvas) => canvas.width === 0 && canvas.height === 0));
  } finally {
    h.restore();
  }
});
