(() => {
  'use strict';
  const el = id => document.getElementById(id);
  const pageSize = 80;
  const delay = ms => new Promise(resolve => setTimeout(resolve, ms));

  class DatabasePanel {
    constructor({ api, onOpened }) {
      this.api = api;
      this.onOpened = onOpened;
      this.form = el('databaseConnectionForm');
      this.dialog = el('databaseDialog');
      this.selected = new Set();
      this.tables = [];
      this.page = 0;
      this.busy = false;
      this.step = 'connection';
      this.form.addEventListener('submit', event => { event.preventDefault(); this.connect(); });
      this.field('engine').addEventListener('change', () => this.protocols());
      this.field('protocol').addEventListener('change', () => this.defaults());
      this.field('tls').addEventListener('change', () => this.defaults());
      el('databaseTableSearch').addEventListener('input', () => {
        this.page = 0;
        this.renderTables();
        el('databaseTableScroll').scrollTop = 0;
      });
      el('databasePrev').addEventListener('click', () => this.changePage(-1));
      el('databaseNext').addEventListener('click', () => this.changePage(1));
      el('databaseImport').addEventListener('click', () => this.importTables());
      el('databaseBack').addEventListener('click', () => this.back());
      el('databaseCancel').addEventListener('click', () => this.cancel());
      el('databaseClose').addEventListener('click', () => this.cancel());
      this.dialog.addEventListener('cancel', event => { event.preventDefault(); this.cancel(); });
      this.dialog.addEventListener('close', () => {
        this.connector?.abort();
        this.connector = null;
        this.credentials = null;
        this.field('password').value = '';
        el('databaseCertificate').value = '';
      });
    }

    field(name) { return this.form.elements.namedItem(name); }

    async open() {
      if (this.dialog.open) return;
      this.back();
      this.dialog.showModal();
      if (!this.options) {
        el('databaseConnect').disabled = true;
        try {
          this.options = await this.api('/api/database/options');
          this.field('engine').replaceChildren(...this.options.engines.map(engine => new Option(engine.label, engine.id)));
          this.protocols();
        } catch (error) {
          this.feedback(error.message, true);
        } finally {
          this.controls();
        }
      }
    }

    protocols() {
      const engine = this.options.engines.find(item => item.id === this.field('engine').value);
      const selector = this.field('protocol');
      selector.replaceChildren(...engine.protocols.map(p => new Option(p.label, p.id)));
      const hasChoice = engine.protocols.length > 1;
      selector.disabled = !hasChoice;
      el('databaseProtocolField').hidden = !hasChoice;
      this.defaults();
    }

    defaults() {
      const engine = this.options.engines.find(item => item.id === this.field('engine').value);
      const protocol = engine.protocols.find(p => p.id === this.field('protocol').value);
      const tls = this.field('tls').value === 'verify';
      this.field('port').placeholder = String(tls ? protocol.tlsPort : protocol.plainPort);
      const oracle = engine.id === 'oracle';
      this.field('database').required = oracle;
      this.field('database').placeholder = engine.id === 'postgres' ? 'postgres' : oracle ? '' : '可选';
      el('databaseNameLabel').textContent = oracle ? (protocol.id === 'sid' ? 'SID' : '服务名') : '数据库';
      el('databaseCertificate').disabled = !tls;
      el('databaseCertificateField').hidden = !tls;
    }

    async connect() {
      if (this.busy || !this.options || !this.form.reportValidity()) return;
      const fields = new FormData(this.form);
      const cfg = Object.fromEntries(fields);
      cfg.protocol = this.field('protocol').value;
      cfg.port = Number(cfg.port || 0);
      const controller = new AbortController();
      this.connector = controller;
      this.busy = true;
      this.controls();
      this.feedback('正在连接并读取目录');
      try {
        const cert = el('databaseCertificate').files?.[0];
        if (cfg.tls === 'verify' && cert) {
          if (cert.size > this.options.certificateBytes) throw new Error('CA 证书超过 64 KB');
          cfg.ca = await cert.text();
        }
        const result = await this.post('/api/database/connect', cfg, controller.signal);
        if (controller.signal.aborted || !this.dialog.open) return;
        this.credentials = cfg;
        this.tables = result.tables.map(table => {
          const key = `${table.schema}.${table.name}`;
          return { ...table, key, searchKey: key.toLocaleLowerCase() };
        });
        this.truncated = result.truncated;
        this.selected.clear();
        this.step = 'tables';
        el('databaseSourceLabel').textContent = result.name;
        el('databaseSourceLabel').title = result.name;
        el('databaseTableSearch').value = '';
        this.page = 0;
        this.renderTables();
        el('databaseTableScroll').scrollTop = 0;
        this.feedback(this.catalogueNotice());
      } catch (error) {
        if (!controller.signal.aborted) this.feedback(error.message, true);
      } finally {
        if (this.connector === controller) {
          this.connector = null;
          this.busy = false;
          this.controls();
          if (this.step === 'tables') el('databaseTableSearch').focus();
        }
      }
    }

    post(path, body, signal) { return this.api(path, { method: 'POST', body: JSON.stringify(body), signal }); }

    renderTables() {
      const query = el('databaseTableSearch').value.trim().toLocaleLowerCase();
      const matches = this.tables.filter(table => table.searchKey.includes(query));
      this.matchCount = matches.length;
      const last = Math.max(0, Math.ceil(matches.length / pageSize) - 1);
      this.page = Math.min(this.page, last);
      const fragment = document.createDocumentFragment();
      for (const table of matches.slice(this.page * pageSize, (this.page + 1) * pageSize)) {
        const row = document.createElement('tr');
        const check = document.createElement('input');
        check.type = 'checkbox';
        check.checked = this.selected.has(table);
        check.setAttribute('aria-label', `选择 ${table.key}`);
        check.addEventListener('change', () => {
          if (check.checked && this.selected.size >= this.options.tablesLimit) {
            check.checked = false;
            this.feedback(`一次最多打开 ${this.options.tablesLimit} 张表`, true);
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
        name.addEventListener('click', () => { check.focus(); check.click(); });
        for (const text of [table.schema, table.kind === 'view' ? '视图' : '表']) {
          const cell = row.insertCell();
          cell.textContent = text;
          cell.title = text;
        }
        fragment.append(row);
      }
      el('databaseTableRows').replaceChildren(fragment);
      el('databaseTableEmpty').hidden = matches.length > 0;
      el('databaseTableEmpty').textContent = this.tables.length ? '没有匹配的表或视图' : '当前账户下没有可读的表或视图';
      el('databasePaging').hidden = last === 0;
      el('databasePage').textContent = `${this.page + 1} / ${last + 1}`;
      el('databasePrev').disabled = this.page === 0;
      el('databaseNext').disabled = this.page >= last;
      this.controls();
    }

    changePage(step) {
      this.page += step;
      this.renderTables();
      el('databaseTableScroll').scrollTop = 0;
    }

    catalogueNotice() {
      return this.truncated ? `目录只显示前 ${this.tables.length.toLocaleString()} 项。可返回连接指定架构。` : '';
    }

    feedback(text, error = false) {
      const status = el('databaseStatus');
      status.textContent = text;
      status.classList.toggle('is-error', error);
    }

    controls() {
      const tables = this.step === 'tables';
      this.form.hidden = tables;
      el('databaseConnectionFields').disabled = this.busy;
      el('databaseCatalogue').hidden = !tables;
      el('databaseCatalogue').inert = this.busy;
      el('databaseBack').hidden = !tables || this.busy;
      el('databaseConnect').hidden = tables;
      el('databaseConnect').disabled = this.busy || !this.options;
      el('databaseImport').hidden = !tables;
      el('databaseImport').disabled = this.busy || this.selected.size === 0;
      const counts = [];
      if (tables) counts.push(`${this.matchCount.toLocaleString()} 个表或视图`);
      if (this.selected.size) counts.push(`已选 ${this.selected.size} 张表`);
      el('databaseSelection').textContent = counts.join('，');
      el('databaseCancel').textContent = this.busy ? '取消读取' : '取消';
      el('databaseProgress').hidden = !this.busy;
      if (!this.busy) el('databaseProgress').removeAttribute('value');
    }

    back() {
      this.step = 'connection';
      this.credentials = null;
      this.selected.clear();
      this.tables = [];
      this.truncated = false;
      this.feedback('');
      this.controls();
      if (this.dialog.open) this.field('host').focus();
    }

    async importTables() {
      if (this.busy || this.selected.size === 0 || !this.credentials) return;
      this.busy = true;
      this.cancelRequested = false;
      this.controls();
      this.feedback('正在连接数据库');
      try {
        const tables = Array.from(this.selected, ({ schema, name }) => ({ schema, name }));
        let status = await this.post('/api/database/import', { connection: this.credentials, tables });
        this.job = status.id;
        if (this.cancelRequested) await this.cancelJob();
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
          if (status.status === 'error') throw new Error(status.error || '数据库读取失败');
          if (status.status === 'reading') {
            const progress = el('databaseProgress');
            progress.max = status.tables;
            progress.value = status.table - 1;
            if (!this.cancelRequested) this.feedback(`正在读取 ${status.name}　${status.table}/${status.tables}　${status.rows.toLocaleString()} 行`);
          } else if (status.status === 'opening') {
            this.feedback('正在打开工作表');
          }
          await delay(300);
          status = await this.api(`/api/database/import?id=${encodeURIComponent(this.job)}`);
        }
      } catch (error) {
        this.feedback(error.message, true);
      } finally {
        this.busy = false;
        this.job = null;
        this.cancelRequested = false;
        this.controls();
      }
    }

    async cancelJob() {
      try {
        const status = await this.post('/api/database/cancel', { id: this.job });
        this.feedback(status.status === 'opening' ? '正在打开工作表' : '正在取消');
      } catch (error) {
        this.cancelRequested = false;
        this.feedback(error.message, true);
      }
    }

    cancel() {
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

  window.ExcelLensDatabasePanel = DatabasePanel;
})();
