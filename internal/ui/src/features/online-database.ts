/**
 * Online database connection, schema catalogue selection, and streaming snapshot import dialog.
 */
import type { ApiClient } from '../contracts/common.ts';
import type { DBConfig, DBEngine, DBImportProgress } from '../contracts/features.ts';
import { errorMessage } from '../core/display-format.ts';
import {
  requireButton,
  requireDialog,
  requireEl,
  requireInput,
} from '../core/dom.ts';

export interface DatabasePanelOptions {
  readonly api: ApiClient;
  readonly onOpened: (status: DBImportProgress) => Promise<void>;
}

interface DatabaseOptionsResponse {
  readonly engines: readonly DBEngine[];
  readonly certificateBytes: number;
  readonly tablesLimit: number;
}

interface DatabaseTableEntry {
  readonly schema: string;
  readonly name: string;
  readonly kind?: string;
}

interface DatabaseIndexedTable extends DatabaseTableEntry {
  readonly key: string;
  readonly searchKey: string;
}

interface DatabaseConnectResponse {
  readonly name: string;
  readonly tables: readonly DatabaseTableEntry[];
  readonly truncated: boolean;
}

interface DatabaseCancelResponse {
  readonly status?: string;
}

const PAGE_SIZE = 80;
const delay = (ms: number): Promise<void> => new Promise((resolve) => setTimeout(resolve, ms));

function requireForm(id: string): HTMLFormElement {
  const node = requireEl(id);
  if (!(node instanceof HTMLFormElement)) {
    throw new Error(`Expected HTMLFormElement for #${id}`);
  }
  return node;
}

function requireFieldSet(id: string): HTMLFieldSetElement {
  const node = requireEl(id);
  if (!(node instanceof HTMLFieldSetElement)) {
    throw new Error(`Expected HTMLFieldSetElement for #${id}`);
  }
  return node;
}

function requireProgress(id: string): HTMLProgressElement {
  const node = requireEl(id);
  if (!(node instanceof HTMLProgressElement)) {
    throw new Error(`Expected HTMLProgressElement for #${id}`);
  }
  return node;
}

export class DatabasePanel {
  private readonly api: ApiClient;
  private readonly onOpened: (status: DBImportProgress) => Promise<void>;
  private readonly form: HTMLFormElement;
  public readonly dialog: HTMLDialogElement;

  private readonly selected = new Set<DatabaseIndexedTable>();
  private tables: DatabaseIndexedTable[] = [];
  private options: DatabaseOptionsResponse | null = null;
  private credentials: DBConfig | null = null;
  private connector: AbortController | null = null;
  private job: string | null = null;
  private page = 0;
  private matchCount = 0;
  private truncated = false;
  private busy = false;
  private cancelRequested = false;
  private step: 'connection' | 'tables' = 'connection';

  constructor(options: DatabasePanelOptions) {
    this.api = options.api;
    this.onOpened = options.onOpened;
    this.form = requireForm('databaseConnectionForm');
    this.dialog = requireDialog('databaseDialog');

    this.form.addEventListener('submit', (event: SubmitEvent) => {
      event.preventDefault();
      void this.connect();
    });
    this.selectField('engine').addEventListener('change', () => this.protocols());
    this.selectField('protocol').addEventListener('change', () => this.defaults());
    this.selectField('tls').addEventListener('change', () => this.defaults());
    requireInput('databaseTableSearch').addEventListener('input', () => {
      this.page = 0;
      this.renderTables();
      requireEl('databaseTableScroll').scrollTop = 0;
    });
    requireButton('databasePrev').addEventListener('click', () => this.changePage(-1));
    requireButton('databaseNext').addEventListener('click', () => this.changePage(1));
    requireButton('databaseImport').addEventListener('click', () => {
      void this.importTables();
    });
    requireButton('databaseBack').addEventListener('click', () => this.back());
    requireButton('databaseCancel').addEventListener('click', () => this.cancel());
    requireButton('databaseClose').addEventListener('click', () => this.cancel());
    this.dialog.addEventListener('cancel', (event: Event) => {
      event.preventDefault();
      this.cancel();
    });
    this.dialog.addEventListener('close', () => {
      this.connector?.abort();
      this.connector = null;
      this.credentials = null;
      this.inputField('password').value = '';
      requireInput('databaseCertificate').value = '';
    });
  }

  private selectField(name: string): HTMLSelectElement {
    const item = this.form.elements.namedItem(name);
    if (!(item instanceof HTMLSelectElement)) {
      throw new Error(`Expected HTMLSelectElement named "${name}"`);
    }
    return item;
  }

  private inputField(name: string): HTMLInputElement {
    const item = this.form.elements.namedItem(name);
    if (!(item instanceof HTMLInputElement)) {
      throw new Error(`Expected HTMLInputElement named "${name}"`);
    }
    return item;
  }

  async open(): Promise<void> {
    if (this.dialog.open) return;
    this.back();
    this.dialog.showModal();
    if (!this.options) {
      requireButton('databaseConnect').disabled = true;
      try {
        this.options = await this.api<DatabaseOptionsResponse>('/api/database/options');
        this.selectField('engine').replaceChildren(
          ...this.options.engines.map((engine) => new Option(engine.label, engine.id)),
        );
        this.protocols();
      } catch (error: unknown) {
        this.feedback(errorMessage(error), true);
      } finally {
        this.controls();
      }
    }
  }

  private protocols(): void {
    if (!this.options) return;
    const engineId = this.selectField('engine').value;
    const engine = this.options.engines.find((item) => item.id === engineId);
    if (!engine) return;
    const selector = this.selectField('protocol');
    selector.replaceChildren(...engine.protocols.map((p) => new Option(p.label, p.id)));
    const hasChoice = engine.protocols.length > 1;
    selector.disabled = !hasChoice;
    requireEl('databaseProtocolField').hidden = !hasChoice;
    this.defaults();
  }

  private defaults(): void {
    if (!this.options) return;
    const engineId = this.selectField('engine').value;
    const engine = this.options.engines.find((item) => item.id === engineId);
    if (!engine) return;
    const protocolId = this.selectField('protocol').value;
    const protocol = engine.protocols.find((p) => p.id === protocolId);
    if (!protocol) return;
    const tls = this.selectField('tls').value === 'verify';
    this.inputField('port').placeholder = String(tls ? protocol.tlsPort : protocol.plainPort);
    const oracle = engine.id === 'oracle';
    const dbInput = this.inputField('database');
    dbInput.required = oracle;
    dbInput.placeholder = engine.id === 'postgres' ? 'postgres' : oracle ? '' : '可选';
    requireEl('databaseNameLabel').textContent = oracle
      ? protocol.id === 'sid'
        ? 'SID'
        : '服务名'
      : '数据库';
    requireInput('databaseCertificate').disabled = !tls;
    requireEl('databaseCertificateField').hidden = !tls;
  }

  private async connect(): Promise<void> {
    if (this.busy || !this.options || !this.form.reportValidity()) return;
    const tlsValue = this.selectField('tls').value;
    const controller = new AbortController();
    this.connector = controller;
    this.busy = true;
    this.controls();
    this.feedback('正在连接并读取目录');
    try {
      let ca = '';
      const cert = requireInput('databaseCertificate').files?.[0];
      if (tlsValue === 'verify' && cert) {
        if (cert.size > this.options.certificateBytes) throw new Error('CA 证书超过 64 KB');
        ca = await cert.text();
      }
      const cfg: DBConfig = {
        engine: this.selectField('engine').value,
        protocol: this.selectField('protocol').value,
        host: this.inputField('host').value,
        port: Number(this.inputField('port').value || 0),
        database: this.inputField('database').value,
        schema: this.inputField('schema').value,
        user: this.inputField('user').value,
        password: this.inputField('password').value,
        tls: tlsValue,
        ca,
      };
      const result = await this.post<DatabaseConnectResponse>(
        '/api/database/connect',
        cfg,
        controller.signal,
      );
      if (controller.signal.aborted || !this.dialog.open) return;
      this.credentials = cfg;
      this.tables = result.tables.map((table) => {
        const key = `${table.schema}.${table.name}`;
        return { ...table, key, searchKey: key.toLocaleLowerCase() };
      });
      this.truncated = result.truncated;
      this.selected.clear();
      this.step = 'tables';
      const sourceLabel = requireEl('databaseSourceLabel');
      sourceLabel.textContent = result.name;
      sourceLabel.title = result.name;
      requireInput('databaseTableSearch').value = '';
      this.page = 0;
      this.renderTables();
      requireEl('databaseTableScroll').scrollTop = 0;
      this.feedback(this.catalogueNotice());
    } catch (error: unknown) {
      if (!controller.signal.aborted) this.feedback(errorMessage(error), true);
    } finally {
      if (this.connector === controller) {
        this.connector = null;
        this.busy = false;
        this.controls();
        if (this.step === 'tables') requireInput('databaseTableSearch').focus();
      }
    }
  }

  private post<T = unknown>(path: string, body: unknown, signal?: AbortSignal): Promise<T> {
    return this.api<T>(path, { method: 'POST', body: JSON.stringify(body), signal });
  }

  private renderTables(): void {
    const query = requireInput('databaseTableSearch').value.trim().toLocaleLowerCase();
    const matches = this.tables.filter((table) => table.searchKey.includes(query));
    this.matchCount = matches.length;
    const last = Math.max(0, Math.ceil(matches.length / PAGE_SIZE) - 1);
    this.page = Math.min(this.page, last);
    const fragment = document.createDocumentFragment();
    const tablesLimit = this.options?.tablesLimit ?? 0;

    for (const table of matches.slice(this.page * PAGE_SIZE, (this.page + 1) * PAGE_SIZE)) {
      const row = document.createElement('tr');
      const check = document.createElement('input');
      check.type = 'checkbox';
      check.checked = this.selected.has(table);
      check.setAttribute('aria-label', `选择 ${table.key}`);
      check.addEventListener('change', () => {
        if (check.checked && this.selected.size >= tablesLimit) {
          check.checked = false;
          this.feedback(`一次最多打开 ${tablesLimit} 张表`, true);
          return;
        }
        if (check.checked) this.selected.add(table);
        else this.selected.delete(table);
        row.classList.toggle('is-selected', check.checked);
        this.feedback(this.catalogueNotice());
        this.controls();
      });
      row.classList.toggle('is-selected', check.checked);
      row.insertCell().append(check);
      const name = row.insertCell();
      name.textContent = table.name;
      name.title = table.name;
      name.addEventListener('click', () => {
        check.focus();
        check.click();
      });
      for (const text of [table.schema, table.kind === 'view' ? '视图' : '表']) {
        const cell = row.insertCell();
        cell.textContent = text;
        cell.title = text;
      }
      fragment.append(row);
    }

    requireEl('databaseTableRows').replaceChildren(fragment);
    const emptyEl = requireEl('databaseTableEmpty');
    emptyEl.hidden = matches.length > 0;
    emptyEl.textContent = this.tables.length
      ? '没有匹配的表或视图'
      : '当前账户下没有可读的表或视图';
    requireEl('databasePaging').hidden = last === 0;
    requireEl('databasePage').textContent = `${this.page + 1} / ${last + 1}`;
    requireButton('databasePrev').disabled = this.page === 0;
    requireButton('databaseNext').disabled = this.page >= last;
    this.controls();
  }

  private changePage(step: number): void {
    this.page += step;
    this.renderTables();
    requireEl('databaseTableScroll').scrollTop = 0;
  }

  private catalogueNotice(): string {
    return this.truncated
      ? `目录只显示前 ${this.tables.length.toLocaleString()} 项。可返回连接指定架构。`
      : '';
  }

  private feedback(text: string, error = false): void {
    const status = requireEl('databaseStatus');
    status.textContent = text;
    status.classList.toggle('is-error', error);
  }

  private controls(): void {
    const tables = this.step === 'tables';
    this.form.hidden = tables;
    requireFieldSet('databaseConnectionFields').disabled = this.busy;
    const catalogue = requireEl('databaseCatalogue');
    catalogue.hidden = !tables;
    catalogue.inert = this.busy;
    requireButton('databaseBack').hidden = !tables || this.busy;
    const connectBtn = requireButton('databaseConnect');
    connectBtn.hidden = tables;
    connectBtn.disabled = this.busy || !this.options;
    const importBtn = requireButton('databaseImport');
    importBtn.hidden = !tables;
    importBtn.disabled = this.busy || this.selected.size === 0;

    const counts: string[] = [];
    if (tables) counts.push(`${this.matchCount.toLocaleString()} 个表或视图`);
    if (this.selected.size) counts.push(`已选 ${this.selected.size} 张表`);
    requireEl('databaseSelection').textContent = counts.join('，');
    requireButton('databaseCancel').textContent = this.busy ? '取消读取' : '取消';
    const progress = requireProgress('databaseProgress');
    progress.hidden = !this.busy;
    if (!this.busy) progress.removeAttribute('value');
  }

  private back(): void {
    this.step = 'connection';
    this.credentials = null;
    this.selected.clear();
    this.tables = [];
    this.truncated = false;
    this.feedback('');
    this.controls();
    if (this.dialog.open) this.inputField('host').focus();
  }

  private isCancelRequested(): boolean {
    return this.cancelRequested;
  }

  private async importTables(): Promise<void> {
    if (this.busy || this.selected.size === 0 || !this.credentials) return;
    this.busy = true;
    this.cancelRequested = false;
    this.controls();
    this.feedback('正在连接数据库');
    try {
      const tables = Array.from(this.selected, ({ schema, name }) => ({ schema, name }));
      let status = await this.post<DBImportProgress>('/api/database/import', {
        connection: this.credentials,
        tables,
      });
      this.job = status.id;
      if (this.isCancelRequested()) await this.cancelJob();
      for (;;) {
        if (status.status === 'done') {
          await this.onOpened(status);
          this.dialog.close();
          return;
        }
        if (status.status === 'cancelled') {
          this.feedback('已取消读取');
          return;
        }
        if (status.status === 'error') {
          throw new Error(status.error || '数据库读取失败');
        }
        if (status.status === 'reading') {
          const progress = requireProgress('databaseProgress');
          progress.max = status.tables;
          progress.value = status.table - 1;
          if (!this.isCancelRequested()) {
            this.feedback(
              `正在读取 ${status.name ?? ''}　${status.table}/${status.tables}　${status.rows.toLocaleString()} 行`,
            );
          }
        } else if (status.status === 'opening') {
          this.feedback('正在打开工作表');
        }
        await delay(300);
        status = await this.api<DBImportProgress>(
          `/api/database/import?id=${encodeURIComponent(this.job)}`,
        );
      }
    } catch (error: unknown) {
      this.feedback(errorMessage(error), true);
    } finally {
      this.busy = false;
      this.job = null;
      this.cancelRequested = false;
      this.controls();
    }
  }

  private async cancelJob(): Promise<void> {
    if (!this.job) return;
    try {
      const status = await this.post<DatabaseCancelResponse>('/api/database/cancel', { id: this.job });
      this.feedback(status.status === 'opening' ? '正在打开工作表' : '正在取消');
    } catch (error: unknown) {
      this.cancelRequested = false;
      this.feedback(errorMessage(error), true);
    }
  }

  cancel(): void {
    if (this.busy && this.step === 'tables') {
      if (this.cancelRequested) return;
      this.cancelRequested = true;
      if (this.job) void this.cancelJob();
      return;
    }
    this.connector?.abort();
    this.busy = false;
    this.dialog.close();
  }
}
