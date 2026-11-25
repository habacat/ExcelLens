(() => {
  'use strict';
  const get = id => document.getElementById(id);
  const endpoint = '/api/database/snapshots';

  class DatabaseSnapshots {
    constructor({ api, fmtBytes, openWorkbook, onRetained }) {
      this.api = api;
      this.fmtBytes = fmtBytes;
      this.openWorkbook = openWorkbook;
      this.onRetained = onRetained;
      this.dialog = get('databaseSnapshotsDialog');
      this.list = get('databaseSnapshotsList');
      this.dialog.addEventListener('cancel', event => { event.preventDefault(); this.close(); });
      for (const id of ['databaseSnapshotsClose', 'databaseSnapshotsCancel']) {
        get(id).addEventListener('click', () => this.close());
      }
    }

    show(legacyKey = '') {
      if (this.dialog.open) return;
      this.list.replaceChildren();
      this.dialog.showModal();
      if (legacyKey) { this.retain(legacyKey); return; }
      this.run('正在读取快照', async signal => {
        const result = await this.api(endpoint, { signal });
        if (signal.aborted) return;
        this.render(result.items);
        this.feedback('');
      });
    }

    request(action, key, signal) {
      return this.api(endpoint, { method: 'POST', body: JSON.stringify({ action, key }), signal });
    }

    render(items) {
      this.list.replaceChildren();
      for (const item of items) {
        const row = document.createElement('div');
        row.className = 'database-snapshot-entry';
        const identity = document.createElement('div');
        identity.className = 'database-snapshot-identity';
        const name = document.createElement('strong');
        const details = document.createElement('small');
        name.textContent = item.name;
        const parts = [new Date(item.taken).toLocaleString(), (item.sheets || 0).toLocaleString() + ' 张表'];
        if (item.retained) parts.push(item.records.toLocaleString() + ' 条记录', this.fmtBytes(item.bytes));
        else parts.push('旧版快照');
        details.textContent = item.error || parts.join(' · ');
        identity.append(name, details);
        row.append(identity);
        if (!item.error) {
          const actions = document.createElement('div');
          actions.className = 'database-actions';
          const secondary = item.retained ? ['核验', () => this.verify(item)] : ['保存', () => this.retain(item.key)];
          for (const [text, work] of [secondary, ['打开', () => this.open(item)]]) {
            const button = document.createElement('button');
            button.type = 'button';
            button.className = 'mini-button';
            button.textContent = text;
            button.setAttribute('aria-label', text + ' ' + item.name);
            button.addEventListener('click', work);
            actions.append(button);
          }
          row.append(actions);
        }
        this.list.append(row);
      }
      if (!items.length) {
        const empty = document.createElement('p');
        empty.className = 'database-empty';
        empty.textContent = '暂无已保存的数据库快照';
        this.list.append(empty);
      }
    }

    retain(key) {
      this.run('正在保存旧快照', async signal => {
        await this.request('retain', key, signal);
        if (signal.aborted) return;
        this.onRetained(key);
        const result = await this.api(endpoint, { signal });
        if (signal.aborted) return;
        this.render(result.items);
        this.feedback('已保存旧快照');
      });
    }

    verify(item) {
      this.run('正在核验 ' + item.name, async signal => {
        const result = await this.request('verify', item.key, signal);
        if (!signal.aborted && result.verified) this.feedback('校验通过　SHA-256 ' + result.key);
      });
    }

    open(item) {
      this.run('正在核验并打开 ' + item.name, async signal => {
        const opened = await this.openWorkbook(() => this.request('open', item.key, signal), '', 0, () => !signal.aborted);
        if (opened && !signal.aborted) this.dialog.close();
      });
    }

    feedback(message, error = false) {
      const status = get('databaseSnapshotsStatus');
      status.textContent = message;
      status.classList.toggle('is-error', error);
    }

    async run(message, work) {
      if (this.controller) return;
      const controller = new AbortController();
      this.controller = controller;
      this.setBusy(true);
      this.feedback(message);
      try {
        await work(controller.signal);
      } catch (err) {
        if (!controller.signal.aborted) this.feedback(err.message, true);
      } finally {
        if (this.controller === controller) {
          this.controller = null;
          this.setBusy(false);
        }
      }
    }

    setBusy(busy) {
      this.list.inert = busy;
      get('databaseSnapshotsProgress').hidden = !busy;
      get('databaseSnapshotsCancel').textContent = busy ? '取消' : '关闭';
    }

    close() {
      this.controller?.abort();
      this.controller = null;
      this.setBusy(false);
      this.dialog.close();
    }
  }

  window.ExcelLensDatabaseSnapshots = DatabaseSnapshots;
})();
