import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { ApiRequestOptions } from '../contracts/common.ts';
import type { CellData, RichText, SheetViewportResponse, VisualStyle } from '../contracts/workbook.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import { makeDefaultViewportPipelineDeps } from '../test-helpers/grid-fixture.ts';
import { buildClipboard, configureClipboard, tsvCell } from './clipboard.ts';
import { FrozenPanes } from './freeze.ts';
import {
  eachRichPart,
  estimateRichTextBytes,
  inspectRichText,
  richTextHtml,
  sameRichText,
} from './richtext.ts';

const limits = { cellRuns: 4096, viewRuns: 8192 };

const sampleCell: CellData = {
  ref: 'A1',
  col: 1,
  type: 'inlineStr',
  raw: '中😀<&\n尾',
  display: '中😀<&\n尾',
  hasFormula: false,
  richText: {
    runs: [
      { end: 3, font: { bold: false, underline: 'none', strike: false } },
      {
        end: 7,
        font: {
          color: '#1255AA',
          vertical: 'subscript',
          name: '";><img src=x onerror=1>',
        },
      },
    ],
  },
};

test('HTML copy and source text share exact UTF16 spans, inherit styles and never execute values or font names', () => {
  configureClipboard({
    rows: 10,
    columns: 10,
    cells: 100,
    textBytes: 10000,
    htmlBytes: 20000,
    tsvRows: 100,
    tsvColumns: 10,
    tsvCells: 1000,
  });
  const base: VisualStyle = {
    bold: true,
    underline: true,
    strike: true,
    fontName: '宋体',
    fontSize: 11,
  };
  const value = buildClipboard(
    { startCol: 1, endCol: 1, rows: [{ row: 1, cells: [sampleCell] }] },
    [base],
    true,
    '',
    limits,
  );
  assert.equal(value.text, tsvCell(sampleCell.raw));
  assert.ok(!value.html.includes('<img'));
  assert.ok(!value.html.includes('<&'));
  assert.match(value.html, /font-weight:400/);
  assert.match(value.html, /text-decoration-line:none/);
  assert.match(value.html, /vertical-align:sub/);
  assert.match(value.html, /<br style="mso-data-placement:same-cell">/);
  const td = value.html.slice(value.html.indexOf('<td'), value.html.indexOf('><span'));
  assert.ok(!td.includes('text-decoration:underline'));
  const override = richTextHtml(sampleCell, base, { limits, fontOverride: 'Arial' });
  assert.ok(override);
  assert.ok(override.html.includes('Arial'));
  assert.ok(!override.html.includes('onerror'));
});

test('malformed runs, surrogate splits, display transformations and unknown font fields retain complete text and a cause', () => {
  const badRuns: readonly RichText['runs'][] = [
    [{ end: 2, font: {} }],
    [{ end: 8, font: {} }],
    [
      { end: 3, font: {} },
      { end: 3, font: {} },
    ],
    [{ end: 7, font: { color: 'url(x)' } }],
    [{ end: 7, font: { size: Infinity } }],
  ];
  for (const runs of badRuns) {
    const result = inspectRichText({ ...sampleCell, richText: { runs } }, limits);
    assert.ok(result);
    assert.equal(result.text, sampleCell.raw);
    assert.equal(result.runs.length, 0);
    assert.match(result.notice, /无效|覆盖/);
  }
  const changed = inspectRichText({ ...sampleCell, display: 'formatted' }, limits);
  assert.ok(changed);
  assert.equal(changed.text, 'formatted');
  assert.match(changed.notice, /不同/);

  const partial = inspectRichText(
    {
      ...sampleCell,
      richText: { runs: [{ end: 3, font: { bold: true } }], notice: '部分样式未还原' },
    },
    limits,
  );
  assert.ok(partial);
  const parts: string[] = [];
  eachRichPart(partial, (text) => parts.push(text));
  assert.equal(parts.join(''), sampleCell.raw);

  const accounting = inspectRichText(
    { ...sampleCell, richText: { runs: [{ end: 7, font: { underline: 'singleAccounting' } }] } },
    limits,
  );
  assert.ok(accounting);
  assert.match(accounting.notice, /会计下划线/);
});

test('style equality distinguishes missing and explicit false; retained metadata is included in cache cost', () => {
  const dom = installDom();
  try {
    const rt = sampleCell.richText;
    assert.ok(rt);
    assert.equal(sameRichText(rt, JSON.parse(JSON.stringify(rt)) as RichText), true);
    const changed = JSON.parse(JSON.stringify(rt)) as {
      runs: { end: number; font?: Record<string, unknown> }[];
    };
    if (changed.runs[0]?.font) {
      delete changed.runs[0].font.bold;
    }
    assert.equal(sameRichText(rt, changed as RichText), false);
    assert.ok(estimateRichTextBytes(rt) > 500);
    assert.equal(estimateRichTextBytes(null), 0);
  } finally {
    dom.restore();
  }
});

test('clipboard rejects entire oversized UTF8 source or escaped HTML before publishing any clipboard payload', () => {
  const payload = { startCol: 1, endCol: 1, rows: [{ row: 1, cells: [sampleCell] }] };
  assert.throws(
    () => buildClipboard(payload, [], true, '', limits, { textBytes: 20, htmlBytes: 10000 }),
    /文字超过/,
  );
  assert.throws(
    () => buildClipboard(payload, [], true, '', limits, { textBytes: 10000, htmlBytes: 150 }),
    /HTML超过/,
  );
  const value = buildClipboard(payload, [], true, '', limits, {
    textBytes: 10000,
    htmlBytes: 10000,
  });
  assert.equal(value.text, tsvCell(sampleCell.raw));
});

test('frozen cache uses measured bytes, retains its budget across an oversized tile, rejects late aborted fetch and resets accounting', async () => {
  const dom = installDom({ fromIndexHtml: true });
  try {
    interface SizedViewport extends SheetViewportResponse {
      readonly bytes: number;
    }
    const makeTile = (bytes: number): SizedViewport => ({
      sheet: 'A',
      offset: 0,
      limit: 10,
      totalRows: 10,
      filtered: false,
      maxRow: 10,
      maxCol: 10,
      startCol: 1,
      colCount: 10,
      rows: [],
      bytes,
    });
    const deps = makeDefaultViewportPipelineDeps();
    let deliver: ((value: SizedViewport) => void) | undefined;
    let capturedSignal: AbortSignal | undefined;
    const grid = document.getElementById('grid');
    assert.ok(grid);
    const cache = new FrozenPanes({
      state: deps.S,
      grid,
      rowHead: 26,
      cacheBudget: 100,
      estimateBytes: (tile) => (tile as SizedViewport).bytes,
      api: <T = unknown>(_url: string, options?: ApiRequestOptions): Promise<T> => {
        capturedSignal = options?.signal ?? undefined;
        return new Promise<T>((resolve) => {
          deliver = (value) => resolve(value as T);
        });
      },
      getRequest: () => ({
        context: '',
        filtered: false,
        filterId: '',
        viewId: '',
        excludeThroughRow: 0,
        displayStart: 1,
        displayEnd: 10,
        visibleStart: 1,
        visibleEnd: 10,
        visibleColStart: 1,
        visibleColEnd: 10,
        row: 1,
        offset: 0,
        limit: 10,
        startCol: 1,
        cols: 10,
        selectedCols: null,
        leadRow: 0,
        leadCol: 0,
      }),
      rowHeight: () => 25,
      colWidth: () => 132,
      rowTop: (r) => (r - 1) * 25,
      colLeft: (c) => (c - 1) * 132,
      headerHeight: () => 27,
      colName: deps.colName,
      bindResizeGrip: () => undefined,
      getVirtualScrollTop: () => 0,
      paintRowHeaderBorder: () => undefined,
      updateSharedBorders: () => undefined,
      isPrecedent: () => false,
      isSearchMatch: () => false,
      applyCellStyle: () => undefined,
      onRender: () => undefined,
      paintCell: () => undefined,
      onError: () => undefined,
    });

    cache.cachePart('a', makeTile(80));
    cache.cachePart('b', makeTile(80));
    assert.equal(cache.cache.size, 1);
    assert.equal(cache.cacheBytes, 80);
    cache.cachePart('b', makeTile(20));
    cache.cachePart('c', makeTile(30));
    assert.equal(cache.cacheBytes, 50);
    cache.cachePart('giant', makeTile(200));
    assert.equal(cache.cache.size, 2);
    assert.equal(cache.cacheBytes, 50);

    const pending = cache.fetchPart('left', new URLSearchParams(), {
      context: '',
      filtered: false,
      filterId: '',
      viewId: '',
      excludeThroughRow: 0,
      displayStart: 1,
      displayEnd: 10,
      visibleStart: 1,
      visibleEnd: 10,
      visibleColStart: 1,
      visibleColEnd: 10,
      row: 1,
      offset: 0,
      limit: 10,
      startCol: 1,
      cols: 10,
      selectedCols: null,
      leadRow: 0,
      leadCol: 0,
    });
    cache.reset();
    assert.equal(capturedSignal?.aborted, true);
    deliver?.(makeTile(50));
    await pending;
    assert.equal(cache.cache.size, 0);
    assert.equal(cache.cacheBytes, 0);
    assert.equal(cache.cacheSizes.size, 0);
  } finally {
    dom.restore();
  }
});
