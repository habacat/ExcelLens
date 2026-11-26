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
      this.evidence = new window.ExcelLensDatabaseEvidence(api, fmtBytes);
      this.filters = get('dbSnapshotFilters');
      this.policyForm = get('dbSnapshotPolicy');
      this.items = [];
      this.filters.addEventListener('input', () => this.render());
      this.filters.addEventListener('submit', event => event.preventDefault());
      this.filters.addEventListener('reset', () => queueMicrotask(() => this.render()));
      this.policyForm.addEventListener('submit', event => { event.preventDefault(); this.savePolicy(); });
      get('dbSnapshotRemovalBack').addEventListener('click', () => this.setRemoval(null));
      get('dbSnapshotRemovalConfirm').addEventListener('click', () => this.confirmRemoval());
      this.dialog.addEventListener('cancel', event => { event.preventDefault(); this.close(); });
      for (const id of ['databaseSnapshotsClose', 'databaseSnapshotsCancel']) {
        get(id).addEventListener('click', () => this.close());
      }
    }

    show(legacyKey = '') {
      if (this.dialog.open) return;
      this.items = [];
      this.filters.reset();
      this.setRemoval(null);
      get('dbSnapshotUsage').textContent = '';
      get('dbSnapshotMatches').textContent = '';
      get('dbSnapshotStages').hidden = true;
      this.list.replaceChildren();
      this.dialog.showModal();
      if (legacyKey) { this.retain(legacyKey); return; }
      this.run('正在读取快照', signal => this.refresh(signal));
    }

    async refresh(signal, message = '') {
      const result = await this.api(endpoint, {signal});
      if (signal.aborted) return;
      this.items = result.items || [];
      this.policy = result.policy || {};
      this.policyForm.elements.budget.value = this.policy.budgetBytes ? this.policy.budgetBytes / (1024 ** 3) : '';
      this.policyForm.elements.days.value = this.policy.reviewDays || '';
      this.policyForm.querySelector('button').disabled = !!result.policyError;
      this.renderStages(result.stages || [], result.stageRoot);
      this.render();
      this.feedback(result.policyError || message, !!result.policyError);
    }

    renderStages(stages, root) {
      const area = get('dbSnapshotStages'), body = area.querySelector('div');
      area.hidden = !stages.length;
      body.replaceChildren();
      if (!stages.length) return;
      area.querySelector('summary').textContent = '采集暂存 · ' + this.fmtBytes(stages.reduce((n, s) => n + s.bytes, 0));
      const location = document.createElement('p');
      location.textContent = root;
      body.append(location);
      for (const stage of stages) {
        const line = document.createElement('p');
        const state = {active:'正在使用', orphan:'已中断，下次启动清理', unknown:'归属未确认，关闭采集程序后核对目录'}[stage.state];
        line.textContent = [stage.name, this.fmtBytes(stage.bytes), stage.error || state].join(' · ');
        body.append(line);
      }
    }

    savePolicy() {
      const budget = this.policyForm.elements.budget.valueAsNumber, days = this.policyForm.elements.days.valueAsNumber;
      const policy = {budgetBytes:Math.round((budget || 0) * 1024 ** 3), reviewDays:days || 0};
      this.run('正在保存提醒', async signal => {
        const saved = await this.api(endpoint, {method:'POST', body:JSON.stringify({action:'policy',policy}), signal});
        if (signal.aborted) return;
        this.policy = saved;
        this.render();
        this.feedback('已保存提醒');
      });
    }

    due(item) {
      return !!this.policy?.reviewDays && !item.error &&
        Date.now() - new Date(item.taken).getTime() >= this.policy.reviewDays * 86400000;
    }

    request(action, key, signal) {
      return this.api(endpoint, { method: 'POST', body: JSON.stringify({ action, key }), signal });
    }

    render() {
      const fields = this.filters.elements;
      const lower = value => value.trim().toLocaleLowerCase();
      const min = fields.min.valueAsNumber, max = fields.max.valueAsNumber;
      const from = fields.from.value, until = fields.until.value;
      const source = lower(fields.source.value), table = lower(fields.table.value);
      const invalid = !this.filters.checkValidity() || min > max || (from && until && from > until);
      const items = invalid ? [] : this.items.filter(item => {
        const date = new Date(item.taken);
        const day = [date.getFullYear(), String(date.getMonth()+1).padStart(2, '0'), String(date.getDate()).padStart(2, '0')].join('-');
        if ((from || until) && (item.error || (from && day < from) || (until && day > until))) return false;
        const origin = [item.name, ...Object.values(item.source || {})].join(' ').toLocaleLowerCase();
        return !(item.bytes < min * 1048576 || item.bytes > max * 1048576) &&
          (!source || origin.includes(source)) && (!table || item.tables?.some(name => lower(name).includes(table))) &&
          (!fields.due.checked || this.due(item));
      });
      const bytes = this.items.reduce((sum, item) => sum + item.bytes, 0);
      const summary = ['共 ' + this.items.length + ' 份', '目录占用 ' + this.fmtBytes(bytes)];
      const budget = this.policy?.budgetBytes;
      if (budget) summary.push((bytes >= budget ? '已达容量提醒 ' : '提醒容量 ') + this.fmtBytes(budget));
      if (this.items.some(item => item.error)) summary.push('含异常目录，请核对容量');
      get('dbSnapshotUsage').textContent = summary.join(' · ');
      const active = source || table || from || until || fields.min.value || fields.max.value || fields.due.checked;
      get('dbSnapshotMatches').textContent = invalid ? '请核对日期和容量范围' : active ?
        '符合条件 ' + items.length + ' 份 · ' + this.fmtBytes(items.reduce((sum, item) => sum + item.bytes, 0)) : '';
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
        else parts.push('旧版快照', this.fmtBytes(item.bytes));
        if (item.legacyKey) parts.push('旧版固化副本');
        if (this.due(item)) parts.push('待复核');
        details.textContent = item.error || parts.join(' · ');
        identity.append(name, details);
        row.append(identity);
        const actions = document.createElement('div');
        actions.className = 'database-actions';
        const secondary = item.retained ? ['核验详情', () => this.verify(item)] : ['保存', () => this.retain(item.key)];
        const choices = item.error ? [] : [secondary, ['打开', () => this.open(item)]];
        choices.push(['删除', () => this.inspectRemoval(item)]);
        for (const [text, work] of choices) {
          const button = document.createElement('button');
          button.type = 'button';
          button.className = 'mini-button';
          button.textContent = text;
          button.setAttribute('aria-label', text + ' ' + item.name);
          button.addEventListener('click', work);
          actions.append(button);
        }
        row.append(actions);
        this.list.append(row);
      }
      if (!items.length) {
        const empty = document.createElement('p');
        empty.className = 'database-empty';
        empty.textContent = this.items.length ? '没有符合条件的快照' : '暂无已保存的数据库快照';
        this.list.append(empty);
      }
    }

    retain(key) {
      this.run('正在保存旧快照', async signal => {
        await this.request('retain', key, signal);
        if (signal.aborted) return;
        this.onRetained(key);
        await this.refresh(signal, '已保存旧快照');
      });
    }

    verify(item) {
      this.run('正在核验 ' + item.name, async signal => {
        const result = await this.request('verify', item.key, signal);
        if (signal.aborted) return;
        if (!result.verified || result.key !== item.key) throw Error('快照核验结果与选择不一致');
        this.feedback('');
        this.evidence.show(result);
      });
    }

    setRemoval(item) {
      this.removing = item;
      get('dbSnapshotRemoval').hidden = !item;
      get('dbSnapshotControls').hidden = !!item;
      get('dbSnapshotMatches').hidden = !!item;
      this.list.hidden = !!item;
      get('dbSnapshotRemovalConfirm').hidden = true;
    }

    inspectRemoval(item) {
      this.run('正在检查快照引用', async signal => {
        const result = await this.api(endpoint + '/removal', {method:'POST', body:JSON.stringify({key:item.key}), signal});
        if (signal.aborted) return;
        this.setRemoval(item);
        get('dbSnapshotRemovalName').textContent = item.name + ' · ' + this.fmtBytes(item.bytes);
        const refs = result.references || [];
        get('dbSnapshotRemovalReason').textContent = refs.length ? '仍被引用：' + refs.join('、') :
          '未发现本机工作成果引用。确认删除这份快照？删除后无法撤销。';
        get('dbSnapshotRemovalConfirm').hidden = !!refs.length;
        get('dbSnapshotRemovalBack').focus();
        this.feedback('');
      });
    }

    confirmRemoval() {
      const item = this.removing;
      if (!item || get('dbSnapshotRemovalConfirm').hidden) return;
      this.run('正在重新核对引用并删除', async signal => {
        await this.api(endpoint + '/removal', {method:'POST', body:JSON.stringify({key:item.key,confirm:true}),signal});
        if (signal.aborted) return;
        this.setRemoval(null);
        await this.refresh(signal, '已删除 ' + item.name);
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
      get('dbSnapshotControls').inert = busy;
      get('dbSnapshotRemovalConfirm').disabled = busy;
      get('dbSnapshotRemovalBack').disabled = busy;
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
