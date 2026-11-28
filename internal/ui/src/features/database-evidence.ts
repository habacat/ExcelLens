/**
 * Cryptographic collection-evidence inspection dialog for retained online database snapshots.
 */
import { interfaceLocale, t as localize } from '../core/i18n.ts';
import type { ApiClient } from '../contracts/common.ts';
import type { DatabaseSnapshotSource } from '../contracts/features.ts';
import { errorMessage } from '../core/display-format.ts';
import {
  requireButton,
  requireDialog,
  requireEl,
  requireSelect,
} from '../core/dom.ts';

export interface DatabaseEvidenceColumn {
  readonly name: string;
  readonly type?: string;
  readonly nullable?: boolean | null;
  readonly precision?: number | string | null;
  readonly scale?: number | string | null;
}

export interface DatabaseEvidenceFileDigest {
  readonly sha256: string;
  readonly bytes: number;
}

export interface DatabaseEvidenceTableDetail {
  readonly records: number;
  readonly startedAt?: string;
  readonly finishedAt?: string;
  readonly query: string;
  readonly rows: DatabaseEvidenceFileDigest;
  readonly index: DatabaseEvidenceFileDigest;
  readonly columns: readonly DatabaseEvidenceColumn[];
}

export interface DatabaseEvidenceTable {
  readonly schema: string;
  readonly name: string;
  readonly kind?: string;
  readonly evidence: DatabaseEvidenceTableDetail;
}

export interface DatabaseEvidencePayload {
  readonly label: string;
  readonly source?: DatabaseSnapshotSource | null;
  readonly taken?: string;
  readonly finishedAt?: string;
  readonly consistency?: string;
  readonly legacyKey?: string;
  readonly tables: readonly DatabaseEvidenceTable[];
}

/** @httpResponse DatabaseEvidenceVerifyResult */
export interface DatabaseEvidenceVerifyResult {
  readonly verified: boolean;
  readonly key: string;
  readonly verifiedAt?: string;
  readonly evidence: DatabaseEvidencePayload;
}

const PAGE_SIZE = 100;

function makeNode<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  text: string,
): HTMLElementTagNameMap[K] {
  const el = document.createElement(tag);
  el.textContent = text;
  return el;
}

function formatWhen(value: string | undefined): string {
  return value ? new Date(value).toLocaleString(interfaceLocale()) : localize('ui.common.notRecorded');
}

function renderFacts(rows: readonly (readonly [string, string])[]): HTMLDListElement {
  const dl = document.createElement('dl');
  dl.className = 'snapshot-facts';
  for (const [label, value] of rows) {
    dl.append(makeNode('dt', label), makeNode('dd', value));
  }
  return dl;
}

export class DatabaseEvidence {
  private readonly api: ApiClient;
  private readonly fmtBytes: (value: unknown) => string;
  public readonly dialog: HTMLDialogElement;
  private readonly table: HTMLSelectElement;
  private result: DatabaseEvidenceVerifyResult | null = null;
  private controller: AbortController | null = null;

  constructor(api: ApiClient, fmtBytes: (value: unknown) => string) {
    this.api = api;
    this.fmtBytes = fmtBytes;
    this.dialog = requireDialog('dbSnapshotEvidence');
    this.table = requireSelect('dbEvidenceTable');
    this.table.addEventListener('change', () => this.renderTable());
    requireButton('dbEvidenceClose').addEventListener('click', () => this.dialog.close());
    requireButton('dbEvidenceDownload').addEventListener('click', () => {
      void this.download();
    });
    this.dialog.addEventListener('close', () => {
      this.controller?.abort();
      this.result = null;
      this.table.replaceChildren();
      requireEl('dbEvidenceColumns').replaceChildren();
    });
  }

  show(result: DatabaseEvidenceVerifyResult): void {
    this.result = result;
    const e = result.evidence;
    const source = e.source;
    requireEl('dbEvidenceName').textContent = e.label;
    const rows: [string, string][] = source
      ? [
          [localize('ui.databaseEvidence.database'), source.database || localize('ui.databaseEvidence.notSpecified')],
          [localize('ui.databaseEvidence.server'), `${source.host} · ${source.port}`],
          [localize('ui.databaseEvidence.engineProtocol'), `${source.engine} / ${source.protocol}`],
          ['TLS', source.tls || localize('ui.common.notRecorded')],
        ]
      : [[localize('ui.databaseEvidence.sourceConnection'), localize('ui.common.thisFeatureNotRecordedPrevious')]];
    rows.push(
      [localize('ui.databaseEvidence.collectionStarted'), formatWhen(e.taken)],
      [e.legacyKey ? localize('ui.databaseEvidence.saveTime') : localize('ui.databaseEvidence.collectionCompleted'), formatWhen(e.finishedAt)],
      [localize('ui.databaseEvidence.consistency'), e.consistency === 'per-table' ? localize('ui.databaseEvidence.independentCollectionEachTable') : localize('ui.common.thisFeatureNotRecordedPrevious')],
      [localize('ui.databaseEvidence.listSha'), result.key],
      [localize('ui.databaseEvidence.verificationPassed'), formatWhen(result.verifiedAt)],
    );
    if (e.legacyKey) {
      rows.push([localize('ui.databaseEvidence.oldVersionListSha'), e.legacyKey], [localize('ui.databaseEvidence.summaryRange'), localize('ui.databaseEvidence.verifiableSinceSnapshotWasSaved')]);
    }
    requireEl('dbEvidenceOrigin').replaceChildren(...Array.from(renderFacts(rows).children));
    this.table.replaceChildren(
      ...e.tables.map(
        (t, i) =>
          new Option(localize('ui.databaseEvidence.items', { schema: t.schema, name: t.name, records: t.evidence.records.toLocaleString(interfaceLocale()) }), String(i)),
      ),
    );
    this.table.selectedIndex = 0;
    this.renderTable();
    requireEl('dbEvidenceStatus').textContent = '';
    requireButton('dbEvidenceDownload').disabled = false;
    this.dialog.showModal();
  }

  private renderTable(): void {
    if (!this.result) return;
    const proof = this.result.evidence;
    const t = proof.tables[this.table.selectedIndex];
    if (!t) return;
    const e = t.evidence;
    const rows: [string, string][] = [
      [localize('ui.databaseEvidence.objectType'), t.kind === 'view' ? localize('ui.databaseEvidence.view') : localize('ui.databaseEvidence.table')],
      [localize('ui.common.numberRecords'), e.records.toLocaleString(interfaceLocale())],
      [localize('ui.databaseEvidence.startComplete'), `${formatWhen(e.startedAt)} / ${formatWhen(e.finishedAt)}`],
      [localize('ui.databaseEvidence.collectingSql'), proof.legacyKey ? localize('ui.databaseEvidence.oldVersionDoesNotRecord') : e.query],
      [localize('ui.databaseEvidence.sort'), proof.legacyKey ? localize('ui.common.thisFeatureNotRecordedPrevious') : localize('ui.databaseEvidence.noSortingSpecified')],
      [localize('ui.databaseEvidence.rowFileSha'), `${e.rows.sha256}（${this.fmtBytes(e.rows.bytes)}）`],
      [localize('ui.databaseEvidence.indexSha'), `${e.index.sha256}（${this.fmtBytes(e.index.bytes)}）`],
    ];
    requireEl('dbEvidenceTableFacts').replaceChildren(renderFacts(rows));
    this.renderColumns(e.columns, 0);
  }

  private renderColumns(columns: readonly DatabaseEvidenceColumn[], start: number): void {
    const pane = requireEl('dbEvidenceColumns');
    const wrap = document.createElement('div');
    const table = document.createElement('table');
    const head = table.createTHead().insertRow();
    wrap.className = 'snapshot-column-wrap';
    table.createCaption().textContent = localize('ui.databaseEvidence.fieldDefinition', { count: columns.length.toLocaleString(interfaceLocale()) });
    for (const label of [localize('ui.common.field'), localize('ui.common.type'), localize('ui.databaseEvidence.optional'), localize('ui.databaseEvidence.precision'), localize('ui.databaseEvidence.numberDecimalPlaces')]) {
      head.append(makeNode('th', label));
    }
    const body = table.createTBody();
    for (const col of columns.slice(start, start + PAGE_SIZE)) {
      const row = body.insertRow();
      const cells: readonly string[] = [
        col.name,
        col.type || localize('ui.common.notRecorded'),
        col.nullable === undefined || col.nullable === null ? localize('ui.common.notRecorded') : col.nullable ? localize('ui.databaseEvidence.sJustThat') : localize('ui.databaseEvidence.no'),
        col.precision !== undefined && col.precision !== null ? String(col.precision) : localize('ui.common.notRecorded'),
        col.scale !== undefined && col.scale !== null ? String(col.scale) : localize('ui.common.notRecorded'),
      ];
      for (const value of cells) {
        row.insertCell().textContent = value;
      }
    }
    wrap.append(table);
    pane.replaceChildren(wrap);
    if (columns.length <= PAGE_SIZE) return;
    const nav = document.createElement('div');
    nav.className = 'database-paging';
    nav.append(makeNode('span', `${start + 1}–${Math.min(start + PAGE_SIZE, columns.length)}`));
    const controls: readonly (readonly [string, number])[] = [
      [localize('ui.databaseEvidence.previousPageField'), -PAGE_SIZE],
      [localize('ui.databaseEvidence.nextPageField'), PAGE_SIZE],
    ];
    for (const [label, offset] of controls) {
      const button = makeNode('button', label);
      const next = start + offset;
      button.type = 'button';
      button.className = 'mini-button';
      button.disabled = next < 0 || next >= columns.length;
      button.onclick = () => this.renderColumns(columns, next);
      nav.append(button);
    }
    pane.append(nav);
  }

  private async download(): Promise<void> {
    if (!this.result) return;
    const controller = new AbortController();
    const key = this.result.key;
    this.controller = controller;
    const status = requireEl('dbEvidenceStatus');
    const button = requireButton('dbEvidenceDownload');
    button.disabled = true;
    status.textContent = localize('ui.databaseEvidence.verifyingExportedFiles');
    try {
      const blob = await this.api(
        '/api/database/snapshots',
        {
          method: 'POST',
          body: JSON.stringify({ action: 'export', key }),
          signal: controller.signal,
        },
        'blob',
      );
      if (controller.signal.aborted) return;
      const url = URL.createObjectURL(blob);
      const link = document.createElement('a');
      link.href = url;
      link.download = `ExcelLens-snapshot-${key}.json`;
      link.click();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
      status.textContent = localize('ui.databaseEvidence.collectionListExported');
    } catch (err: unknown) {
      if (!controller.signal.aborted) status.textContent = errorMessage(err);
    } finally {
      if (this.controller === controller) {
        this.controller = null;
        button.disabled = false;
      }
    }
  }
}
