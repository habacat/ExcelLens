/**
 * Cryptographic collection-evidence inspection dialog for retained online database snapshots.
 */
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
  return value ? new Date(value).toLocaleString() : '未记录';
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
          ['数据库', source.database || '未指定'],
          ['服务器', `${source.host} · ${source.port}`],
          ['引擎 / 协议', `${source.engine} / ${source.protocol}`],
          ['TLS', source.tls || '未记录'],
        ]
      : [['连接来源', '旧版未记录']];
    rows.push(
      ['采集开始', formatWhen(e.taken)],
      [e.legacyKey ? '固化时间' : '采集完成', formatWhen(e.finishedAt)],
      ['一致性', e.consistency === 'per-table' ? '各表独立采集' : '旧版未记录'],
      ['清单 SHA-256', result.key],
      ['核验通过', formatWhen(result.verifiedAt)],
    );
    if (e.legacyKey) {
      rows.push(['旧版清单 SHA-256', e.legacyKey], ['摘要范围', '从固化时起可核验']);
    }
    requireEl('dbEvidenceOrigin').replaceChildren(...Array.from(renderFacts(rows).children));
    this.table.replaceChildren(
      ...e.tables.map(
        (t, i) =>
          new Option(`${t.schema}.${t.name}（${t.evidence.records.toLocaleString()} 条）`, String(i)),
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
      ['对象类型', t.kind === 'view' ? '视图' : '表'],
      ['记录数', e.records.toLocaleString()],
      ['开始 / 完成', `${formatWhen(e.startedAt)} / ${formatWhen(e.finishedAt)}`],
      ['采集 SQL', proof.legacyKey ? '旧版未记录原始 SQL' : e.query],
      ['排序', proof.legacyKey ? '旧版未记录' : '未指定排序'],
      ['行文件 SHA-256', `${e.rows.sha256}（${this.fmtBytes(e.rows.bytes)}）`],
      ['索引 SHA-256', `${e.index.sha256}（${this.fmtBytes(e.index.bytes)}）`],
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
    table.createCaption().textContent = `字段定义（${columns.length.toLocaleString()}）`;
    for (const label of ['字段', '类型', '可空', '精度', '小数位']) {
      head.append(makeNode('th', label));
    }
    const body = table.createTBody();
    for (const col of columns.slice(start, start + PAGE_SIZE)) {
      const row = body.insertRow();
      const cells: readonly string[] = [
        col.name,
        col.type || '未记录',
        col.nullable === undefined || col.nullable === null ? '未记录' : col.nullable ? '是' : '否',
        col.precision !== undefined && col.precision !== null ? String(col.precision) : '未记录',
        col.scale !== undefined && col.scale !== null ? String(col.scale) : '未记录',
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
      ['上一页字段', -PAGE_SIZE],
      ['下一页字段', PAGE_SIZE],
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
    status.textContent = '正在核验导出文件';
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
      status.textContent = '已导出采集清单';
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
