import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { EditBatchPreviewResponse, PortableClipboard } from '../contracts/edit.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import {
  encodePasteMarkup,
  fetchNativePaste,
  identifyPasteClipboard,
  isEditClipboard,
  pastePortableRange,
  readPortableFromClip,
  resolvePasteDestination,
  type GridPasteRangesHost,
} from './edit-grid-paste.ts';

function samplePortable(extra: Partial<NonNullable<PortableClipboard['range']>> = {}): PortableClipboard {
  return {
    proof: 'signed-proof-1',
    range: {
      start: { sheet: 'Sheet1', row: 2, col: 1 },
      rows: 2,
      cols: 2,
      cells: [],
      styles: [],
      ...extra,
    },
  };
}

test('portable clipboard markup round-trips UTF-8 ranges, enforces capacity limits, and distinguishes marker/external HTML', () => {
  const portable = samplePortable({
    cells: [{ value: { kind: 'text', raw: '审计金额 ¥100' } }],
  });
  const attr = encodePasteMarkup({ clip: { portable } });
  assert.match(attr, /data-excellens-range="[A-Za-z0-9+/=]+"/);

  const identified = identifyPasteClipboard('a\tb', `<table${attr}><tr><td>a</td></tr></table>`, null, 10_000);
  assert.ok(identified && 'portable' in identified);
  assert.deepEqual(identified.portable, portable);

  assert.throws(
    () => identifyPasteClipboard('a', `<table${attr}></table>`, null, 10),
    /复制区域超过编辑容量/,
  );

  const markerSnap = { text: '1\t2\n3\t4', marker: 'xl-copy-99' };
  const byMarker = identifyPasteClipboard('1\t2\r\n3\t4', '<table id="xl-copy-99"></table>', markerSnap, 10_000);
  assert.equal(byMarker, markerSnap);

  const external = identifyPasteClipboard('1\t2', '<table><tr><td>1</td></tr></table>', null, 10_000);
  assert.deepEqual(external, { external: true });
  assert.equal(identifyPasteClipboard('plain', 'plain text', null, 10_000), null);

  assert.equal(
    isEditClipboard({
      format: 'ExcelLens.EditSelection',
      version: 2,
      start: { sheet: 'S1', row: 1, col: 1 },
      rows: 1,
      cols: 1,
      cells: [],
    }),
    true,
  );
  assert.equal(isEditClipboard({ format: 'ExcelLens.EditSelection', version: 1 }), false);
  assert.equal(readPortableFromClip({ portable: { proof: 123, range: null } }), undefined);
});

test('resolvePasteDestination enforces rectangle multiples, batch capacity, filtered rows, and sheet boundaries', async () => {
  let mapped = false;
  let visibleCount = 1;
  let pasteRowResult: readonly number[] = [];
  const host: GridPasteRangesHost = {
    owner: {
      limits: {
        extensions: ['.xlsx'],
        cellBytes: 32_768,
        formulaBytes: 32_768,
        batchCells: 6,
        requestBytes: 1_048_576,
      },
      colName: (c) => String.fromCharCode(64 + c),
      post: async () => ({}),
      commit: async () => true,
    },
    grid: { shape: { name: 'Sheet1', firstRow: 1, firstCol: 1, rows: 10, cols: 5 } },
    view: {
      mappedRows: () => mapped,
      visibleSelection: () => ({ rows: visibleCount }),
      pasteRows: async () => pasteRowResult,
    },
    current: () => undefined,
  };

  assert.deepEqual(
    await resolvePasteDestination(host, { top: 2, bottom: 2, left: 2, right: 2 }, 2, 2),
    { rows: [2, 3], width: 2, left: 2 },
  );

  await assert.rejects(
    () => resolvePasteDestination(host, { top: 2, bottom: 4, left: 1, right: 2 }, 2, 2),
    /匹配的矩形/,
  );

  await assert.rejects(
    () => resolvePasteDestination(host, { top: 1, bottom: 4, left: 1, right: 2 }, 2, 2),
    /一次编辑容量/,
  );

  await assert.rejects(
    () => resolvePasteDestination(host, { top: 9, bottom: 9, left: 5, right: 5 }, 2, 2),
    /超出可编辑区域/,
  );

  mapped = true;
  visibleCount = 1;
  pasteRowResult = [2, 7];
  assert.deepEqual(
    await resolvePasteDestination(host, { top: 2, bottom: 2, left: 1, right: 1 }, 2, 2),
    { rows: [2, 7], width: 2, left: 1 },
  );

  pasteRowResult = [2, 2];
  await assert.rejects(
    () => resolvePasteDestination(host, { top: 2, bottom: 2, left: 1, right: 1 }, 2, 2),
    /可见行不足/,
  );
});

test('fetchNativePaste normalizes CRLF and trailing newline before SHA-256 proofing', async () => {
  const sent: { readonly command: string; readonly body: unknown }[] = [];
  await fetchNativePaste(
    {
      post: async (command: string, body: unknown) => {
        sent.push({ command, body });
        return null;
      },
    },
    { document: { id: 'doc-1', binding: 'bind-1' }, revision: '3', sheet: 'Sheet1' },
    'A\tB\r\n1\t2\r\n',
  );
  await fetchNativePaste(
    {
      post: async (command: string, body: unknown) => {
        sent.push({ command, body });
        return null;
      },
    },
    { document: { id: 'doc-1', binding: 'bind-1' }, revision: '3', sheet: 'Sheet1' },
    'A\tB\n1\t2',
  );
  assert.equal(sent.length, 2);
  const firstProof = (sent[0]?.body as { readonly proof?: string }).proof;
  const secondProof = (sent[1]?.body as { readonly proof?: string }).proof;
  assert.match(firstProof ?? '', /^[0-9a-f]{64}$/);
  assert.equal(firstProof, secondProof);
});

test('pastePortableRange respects hidden-row prompt choices and routes change vs structure commits', async () => {
  const dom = installDom({ fromIndexHtml: true });
  try {
    const dialog = document.getElementById('editPasteHiddenRows') as HTMLDialogElement;
    assert.ok(dialog);
    let nextDialogChoice = 'cancel';
    dialog.showModal = () => {
      dialog.open = true;
      dialog.returnValue = nextDialogChoice;
      dialog.close();
    };

    const commits: { readonly action: string; readonly options: unknown }[] = [];
    const previews: unknown[] = [];
    const host: GridPasteRangesHost = {
      owner: {
        limits: {
          extensions: ['.xlsx'],
          cellBytes: 32_768,
          formulaBytes: 32_768,
          batchCells: 100,
          requestBytes: 1_048_576,
        },
        colName: (c) => String.fromCharCode(64 + c),
        post: async (_cmd: string, body: unknown) => {
          previews.push(body);
          const reply: EditBatchPreviewResponse = {
            proof: 'preview-proof',
            structure: false,
            last: { sheet: 'Sheet1', row: 2, col: 1 },
            changes: [{ sheet: 'Sheet1', row: 2, col: 1, value: { kind: 'text', raw: 'ok' } }],
          };
          return reply;
        },
        commit: async (action, _changes, options) => {
          commits.push({ action, options });
          return true;
        },
      },
      grid: { shape: { name: 'Sheet1', firstRow: 1, firstCol: 1, rows: 20, cols: 10 } },
      view: {
        mappedRows: () => false,
        visibleSelection: () => ({ rows: 1 }),
        pasteRows: async () => [2],
      },
      current: () => undefined,
    };

    const target = { document: { id: 'd1', binding: 'b1' }, revision: '1', sheet: 'Sheet1' };
    const clipWithHidden = samplePortable({ rows: 3, cols: 2, visibleSourceRows: [2] });

    nextDialogChoice = 'cancel';
    await pastePortableRange(host, target, { top: 2, bottom: 2, left: 1, right: 1 }, clipWithHidden);
    assert.equal(previews.length, 0);
    assert.equal(commits.length, 0);

    nextDialogChoice = 'visible';
    await pastePortableRange(host, target, { top: 2, bottom: 2, left: 1, right: 1 }, clipWithHidden);
    assert.equal(previews.length, 1);
    assert.equal(commits[0]?.action, 'change');
    assert.equal(
      (previews[0] as { readonly operation: { readonly structure: { readonly range: string; readonly pasteVisibleOnly?: boolean } } }).operation.structure.range,
      'A2:B2',
    );
    assert.equal(
      (previews[0] as { readonly operation: { readonly structure: { readonly pasteVisibleOnly?: boolean } } }).operation.structure.pasteVisibleOnly,
      true,
    );

    const clipWithMerge = samplePortable({
      merges: [{ start: { sheet: 'Sheet1', row: 2, col: 1 }, endRow: 3, endCol: 2 }],
    });
    await pastePortableRange(host, target, { top: 4, bottom: 4, left: 2, right: 2 }, clipWithMerge);
    assert.equal(previews.length, 1, 'merged portable paste must bypass plain change preview');
    assert.equal(commits[1]?.action, 'structure');
  } finally {
    dom.restore();
  }
});
