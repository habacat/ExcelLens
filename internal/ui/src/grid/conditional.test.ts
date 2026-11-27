import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { CellData, ConditionalPresentation, ConditionalStatsStartResponse } from '../contracts/workbook.ts';
import { unpackWireRows } from '../core/wire.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import { buildClipboard, configureClipboard } from './clipboard.ts';
import {
  applyConditionalNotice,
  createStatsRequests,
  estimateConditionalBytes,
  sameConditional,
  type PresentationNoticeElement,
} from './conditional.ts';

test('conditional display and clipboard keep exact source text, false font flags and known/pending effects', () => {
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
  const cf: ConditionalPresentation = {
    style: { font: { bold: false } },
    visual: { bold: false, color: '#FF0033', background: '#FFFF00' },
    display: 'derived display',
    valueColor: '',
    notice: '一条规则尚未求值',
  };
  const cell: CellData = {
    ref: 'A1',
    col: 1,
    raw: '9007199254740993.31',
    display: 'source display',
    hasFormula: false,
    style: 0,
    valueColor: '#008800',
    conditional: cf,
  };
  const before = JSON.stringify(cell);
  const payload = {
    startCol: 1,
    endCol: 2,
    rows: [
      {
        row: 1,
        presentationNotice: '显示容量不足',
        cells: [
          cell,
          {
            ref: 'B1',
            col: 2,
            raw: '',
            display: '',
            hasFormula: false,
            presentationOnly: true,
            conditional: { style: { font: {} }, visual: { background: '#FFFF00' } },
          },
        ],
      },
    ],
  };
  const clip = buildClipboard(
    payload,
    [{ bold: true, color: '#008800' }],
    true,
    '',
    { cellRuns: 4096, viewRuns: 8192 },
    { textBytes: 10000, htmlBytes: 20000 },
  );
  assert.equal(clip.text, 'derived display\t');
  assert.match(clip.html, /color:#FF0033/);
  assert.ok(!clip.html.includes('font-weight:bold'));
  assert.match(clip.html, /background-color:#FFFF00/);
  assert.ok(clip.notices.includes('显示容量不足'));
  assert.ok(clip.notices.some((n) => n.includes('尚未求值')));
  assert.equal(buildClipboard(payload, [], false).text, 'source display\t');
  assert.equal(JSON.stringify(cell), before);
  assert.ok(estimateConditionalBytes(cf) > 500);
  assert.equal(sameConditional(cf, JSON.parse(JSON.stringify(cf)) as ConditionalPresentation), true);
  assert.equal(sameConditional(cf, { ...cf, style: { font: {} } }), false);
});

test('compact transport retains sparse presentation markers and a row budget notice', () => {
  const cf: ConditionalPresentation = {
    style: { font: { bold: false, underline: 'none' } },
    visual: { color: '#1255AA' },
    notice: '未读取的规则',
    pending: true,
  };
  const row = unpackWireRows([{ n: 9, p: '显示超限', c: [{ c: 5, v: '', k: 16, q: cf }] }])[0];
  assert.ok(row);
  assert.equal(row.number, 9);
  assert.equal(row.presentationNotice, '显示超限');
  assert.equal(row.cells[0]?.ref, 'E9');
  assert.equal(row.cells[0].raw, '');
  assert.equal(row.cells[0].presentationOnly, true);
  assert.equal(row.cells[0].conditional, cf);
  assert.equal(row.cells[0].conditional.pending, true);
});

test('pending conditional marker toggles pending class cleanly and clears on recycle', () => {
  const dom = installDom();
  try {
    const node = document.createElement('div') as PresentationNoticeElement;
    applyConditionalNotice(
      node,
      { conditional: { style: { font: {} }, pending: true, notice: '条件格式统计计算中' } },
      null,
      true,
    );
    assert.equal(node.classList.contains('cell-conditional-pending'), true);
    assert.equal(node.classList.contains('cell-presentation-notice'), true);
    assert.match(node.title, /计算中/);
    applyConditionalNotice(
      node,
      { conditional: { style: { font: {} }, pending: true, notice: '条件格式统计计算中' } },
      null,
      false,
    );
    assert.equal(node.classList.contains('cell-conditional-pending'), false);
    assert.equal(node.classList.contains('cell-presentation-notice'), false);
    applyConditionalNotice(node, { conditional: { style: { font: {} }, matched: 1 } }, null, true);
    assert.equal(node.classList.contains('cell-conditional-pending'), false);
  } finally {
    dom.restore();
  }
});

test('late conditional start is cancelled by its own ID before the next start', async () => {
  const calls: [string, string][] = [];
  let finishFirst: ((value: ConditionalStatsStartResponse) => void) | undefined;
  const makeResp = (sheet: string, jobId: string): ConditionalStatsStartResponse => ({
    sheet,
    needed: true,
    ready: false,
    status: 'running',
    jobId,
  });
  const queue = createStatsRequests((action, body) => {
    const label =
      typeof body.sheet === 'string'
        ? body.sheet
        : typeof body.id === 'string'
          ? body.id
          : '';
    calls.push([action, label]);
    if (calls.length === 1) {
      return new Promise<ConditionalStatsStartResponse>((resolve) => {
        finishFirst = resolve;
      });
    }
    return Promise.resolve(makeResp(label, 'second'));
  });
  const first = queue.start('old sheet');
  const cancel = queue.cancel(null);
  const retry = queue.start('new sheet');
  await Promise.resolve();
  finishFirst?.(makeResp('old sheet', 'first'));
  await Promise.all([first, cancel, retry]);
  assert.deepEqual(calls, [
    ['start', 'old sheet'],
    ['cancel', 'first'],
    ['start', 'new sheet'],
  ]);
});
