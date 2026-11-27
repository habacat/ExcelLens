import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { FileFormatsConfig, SourceCapabilities } from '../contracts/formats.ts';
import { createFileFormats, requireFileFormats, resetFileFormatsCache } from './formats.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import { makeTestFileFormatsConfig } from '../test-helpers/grid-fixture.ts';

function makeCap(overrides: Partial<SourceCapabilities>): SourceCapabilities {
  return {
    id: 'xlsx',
    maxRows: 1_048_576,
    maxColumns: 16_384,
    styled: true,
    textAmounts: false,
    conditionalMetadata: false,
    catalogueMetadata: false,
    dataRuleMetadata: false,
    noteMetadata: false,
    pictureMetadata: false,
    printMetadata: false,
    textClustering: false,
    ...overrides,
  };
}

test('file selection uses injected extensions and resolved source capabilities', () => {
  const xlsx = makeCap({ id: 'xlsx', maxRows: 1_048_576, maxColumns: 16_384, styled: true });
  const xlsb = makeCap({
    id: 'xlsb',
    maxRows: 1_048_576,
    maxColumns: 16_384,
    styled: true,
    dataRuleMetadata: true,
  });
  const txt = makeCap({ id: 'txt', maxRows: 1_000_000_000, maxColumns: 16_384, styled: false });
  const config: FileFormatsConfig = {
    extensions: ['.xlsx', '.xlsb', '.txt', '.gz'],
    sources: { xlsx, xlsb, txt },
    largeDataRows: 5_000_000,
    queryTempBytes: 8 * 1024 ** 3,
    richText: { cellRuns: 4096, viewRuns: 8192 },
    clipboard: {
      rows: 5000,
      columns: 200,
      cells: 200000,
      textBytes: 8_000_000,
      htmlBytes: 16_000_000,
      tsvRows: 50000,
      tsvColumns: 200,
      tsvCells: 500000,
    },
    reconcileRecordBytes: 1_048_576,
    printing: {
      outputPages: 200,
      outputBytes: 16_777_216,
      pages: 200,
      rows: 5000,
      columns: 200,
      cells: 100000,
      areas: 16,
      parts: 64,
      bytes: 16_777_216,
      requestBytes: 1_048_576,
      notices: 32,
      domPixels: 16_777_216,
      unitsPerPoint: 1.333333,
      firstPageMin: 1,
      firstPageMax: 9999,
    },
    record: { columns: 100, bytes: 1_048_576, requestBytes: 65536, textUnits: 4000 },
    cellExplanation: { bytes: 65536, requestBytes: 65536 },
    profile: { cells: 5_000_000, requestBytes: 65536, bytes: 4_194_304, bins: 32 },
    clustering: { pageGroups: 50, pageVariants: 50 },
    composition: {
      sources: 8,
      columns: 256,
      previewRows: 100,
      requestBytes: 1_048_576,
      resultBytes: 8_388_608,
      attachments: 8,
      fieldPageRows: 40,
      pickerRows: 40,
    },
    selectionQuick: { cells: 200000, columns: 20, segments: 24, requestBytes: 65536, bytes: 1_048_576 },
    chartExport: {
      types: ['png', 'svg'],
      scales: [1, 2],
      minWidth: 320,
      maxWidth: 4096,
      minHeight: 240,
      maxHeight: 4096,
      defaultWidth: 1200,
      defaultHeight: 600,
      pixels: 16_777_216,
      dataBytes: 4_194_304,
      imageBytes: 16_777_216,
      svgBytes: 4_194_304,
      svgNodes: 50000,
      packageBytes: 16_777_216,
    },
    transpose: { rows: 500, columns: 200, cells: 20000, bytes: 4_194_304, pageColumns: 40 },
    snapshots: {
      entries: 100,
      rows: 2000,
      series: 16,
      bytes: 4_194_304,
      storeBytes: 33_554_432,
      pageRows: 50,
    },
    editing: {
      extensions: ['.xlsx', '.csv'],
      cellBytes: 32768,
      formulaBytes: 32768,
      batchCells: 10000,
      requestBytes: 4_194_304,
    },
    relationExports: [],
    taskRecovery: { tasks: 50 },
    catalogue: { pageRows: 50, searchBytes: 256 },
    dataRules: { pageRows: 50 },
    analysisTree: { nodes: 20000, bytes: 16_777_216, pageRows: 200, active: 2 },
  };
  const formats = createFileFormats(config);
  assert.equal(formats.maxRows, 1_000_000_000);
  assert.equal(formats.maxColumns, 16_384);
  assert.equal(formats.largeDataRows, 5_000_000);
  assert.equal(formats.queryTempBytes, 8 * 1024 ** 3);
  assert.equal(formats.supports('账簿.XLSX'), true);
  assert.equal(formats.supports('凭证明细.tar.gz'), true);
  assert.equal(formats.supports('xlsb'), false);
  assert.equal(formats.supports('账簿.XLSB'), true);
  assert.equal(formats.source('xlsb'), xlsb);
  assert.equal(formats.source('txt'), txt);
  assert.equal(formats.source(undefined), xlsx);
});

test('a missing registry entry or invalid replacement never reuses capabilities from a previous bootstrap', () => {
  const dom = installDom();
  try {
    const node = dom.register('fileFormats', 'script');
    const config = makeTestFileFormatsConfig();
    node.textContent = JSON.stringify(config);
    assert.equal(requireFileFormats().maxRows, config.sources.txt?.maxRows);
    node.textContent = JSON.stringify({ ...config, sources: {} });
    assert.throws(requireFileFormats, /fileFormats/);
    assert.throws(requireFileFormats, /fileFormats/);
    node.textContent = '{';
    assert.throws(requireFileFormats);
    assert.throws(requireFileFormats);
    node.textContent = JSON.stringify(config);
    assert.equal(requireFileFormats().source('txt').id, 'txt');
  } finally {
    resetFileFormatsCache();
    dom.restore();
  }
});
