(() => {
  'use strict';
  const get = id => document.getElementById(id);
  const node = (tag, text) => {
    const el = document.createElement(tag);
    el.textContent = text;
    return el;
  };
  const when = value => value ? new Date(value).toLocaleString() : '未记录';
  const pageSize = 100;
  function facts(rows) {
    const dl = document.createElement('dl');
    dl.className = 'snapshot-facts';
    for (const [label, value] of rows) dl.append(node('dt', label), node('dd', value));
    return dl;
  }

  class DatabaseEvidence {
    constructor(api, fmtBytes) {
      this.api = api;
      this.fmtBytes = fmtBytes;
      this.dialog = get('dbSnapshotEvidence');
      this.table = get('dbEvidenceTable');
      this.table.addEventListener('change', () => this.renderTable());
      get('dbEvidenceClose').addEventListener('click', () => this.dialog.close());
      get('dbEvidenceDownload').addEventListener('click', () => this.download());
      this.dialog.addEventListener('close', () => {
        this.controller?.abort();
        this.result = null;
        this.table.replaceChildren();
        get('dbEvidenceColumns').replaceChildren();
      });
    }

    show(result) {
      this.result = result;
      const e = result.evidence, source = e.source;
      get('dbEvidenceName').textContent = e.label;
      const rows = source ? [
        ['数据库', source.database || '未指定'], ['服务器', source.host + ' · ' + source.port],
        ['引擎 / 协议', source.engine + ' / ' + source.protocol], ['TLS', source.tls || '未记录']
      ] : [['连接来源', '旧版未记录']];
      rows.push(['采集开始', when(e.taken)], [e.legacyKey ? '固化时间' : '采集完成', when(e.finishedAt)],
        ['一致性', e.consistency === 'per-table' ? '各表独立采集' : '旧版未记录'],
        ['清单 SHA-256', result.key], ['核验通过', when(result.verifiedAt)]);
      if (e.legacyKey) rows.push(['旧版清单 SHA-256', e.legacyKey], ['摘要范围', '从固化时起可核验']);
      get('dbEvidenceOrigin').replaceChildren(...facts(rows).children);
      this.table.replaceChildren(...e.tables.map((t, i) => new Option(
        t.schema + '.' + t.name + '（' + t.evidence.records.toLocaleString() + ' 条）', String(i))));
      this.table.selectedIndex = 0;
      this.renderTable();
      get('dbEvidenceStatus').textContent = '';
      get('dbEvidenceDownload').disabled = false;
      this.dialog.showModal();
    }

    renderTable() {
      const proof = this.result.evidence;
      const t = proof.tables[this.table.selectedIndex], e = t.evidence;
      const rows = [['对象类型', t.kind === 'view' ? '视图' : '表'], ['记录数', e.records.toLocaleString()],
        ['开始 / 完成', when(e.startedAt) + ' / ' + when(e.finishedAt)],
        ['采集 SQL', proof.legacyKey ? '旧版未记录原始 SQL' : e.query],
        ['排序', proof.legacyKey ? '旧版未记录' : '未指定排序'],
        ['行文件 SHA-256', e.rows.sha256 + '（' + this.fmtBytes(e.rows.bytes) + '）'],
        ['索引 SHA-256', e.index.sha256 + '（' + this.fmtBytes(e.index.bytes) + '）']];
      get('dbEvidenceTableFacts').replaceChildren(facts(rows));
      this.renderColumns(e.columns, 0);
    }

    renderColumns(columns, start) {
      const pane = get('dbEvidenceColumns'), wrap = document.createElement('div');
      const table = document.createElement('table'), head = table.createTHead().insertRow();
      wrap.className = 'snapshot-column-wrap';
      table.createCaption().textContent = '字段定义（' + columns.length.toLocaleString() + '）';
      for (const label of ['字段', '类型', '可空', '精度', '小数位']) head.append(node('th', label));
      const body = table.createTBody();
      for (const col of columns.slice(start, start + pageSize)) {
        const row = body.insertRow();
        for (const value of [col.name, col.type || '未记录', col.nullable == null ? '未记录' : col.nullable ? '是' : '否',
          col.precision ?? '未记录', col.scale ?? '未记录']) row.insertCell().textContent = value;
      }
      wrap.append(table);
      pane.replaceChildren(wrap);
      if (columns.length <= pageSize) return;
      const nav = document.createElement('div');
      nav.className = 'database-paging';
      nav.append(node('span', (start + 1) + '–' + Math.min(start + pageSize, columns.length)));
      for (const [label, offset] of [['上一页字段', -pageSize], ['下一页字段', pageSize]]) {
        const button = node('button', label), next = start + offset;
        button.className = 'mini-button';
        button.disabled = next < 0 || next >= columns.length;
        button.onclick = () => this.renderColumns(columns, next);
        nav.append(button);
      }
      pane.append(nav);
    }

    async download() {
      const controller = new AbortController(), key = this.result.key;
      this.controller = controller;
      const status = get('dbEvidenceStatus'), button = get('dbEvidenceDownload');
      button.disabled = true;
      status.textContent = '正在核验导出文件';
      try {
        const blob = await this.api('/api/database/snapshots', {method:'POST', body:JSON.stringify({action:'export',key}), signal:controller.signal}, 'blob');
        if (controller.signal.aborted) return;
        const url = URL.createObjectURL(blob), link = document.createElement('a');
        link.href = url;
        link.download = 'ExcelLens-snapshot-' + key + '.json';
        link.click();
        setTimeout(() => URL.revokeObjectURL(url), 1000);
        status.textContent = '已导出采集清单';
      } catch (err) {
        if (!controller.signal.aborted) status.textContent = err.message;
      } finally {
        if (this.controller === controller) { this.controller = null; button.disabled = false; }
      }
    }
  }
  window.ExcelLensDatabaseEvidence = DatabaseEvidence;
})();
