(() => {
  'use strict';
  window.ExcelLensStorage = function({api, openDialog}) {
    const $ = id => document.getElementById(id);
    const dialog = $('storageDialog'), status = $('storageStatus'), body = $('storageRows');
    const buttons = Array.from(dialog.querySelectorAll('[data-storage]'));
    let busy = false;
    function working(value) {
      busy = value;
      for (const button of buttons) button.disabled = value;
      $('storageDirectory').disabled = value;
      dialog.setAttribute('aria-busy', String(value));
    }
    function show(message, failed = false) {
      status.textContent = message;
      status.classList.toggle('inline-error', failed);
    }
    async function refresh() {
      const {roots, groups} = await api('/api/storage');
      $('storageRoots').textContent = '用户配置　' + roots.config + '\n本机数据　' + roots.local;
      const fragment = document.createDocumentFragment();
      for (const group of groups) {
        const row = document.createElement('tr');
        const name = document.createElement('td'), details = document.createElement('details'), summary = document.createElement('summary'), paths = document.createElement('div');
        summary.textContent = group.name;
        paths.className = 'storage-paths';
        paths.textContent = group.paths.join('\n');
        details.append(summary, paths); name.append(details); row.append(name);
        const values = [group.kind, group.error ? '未知' : window.ExcelLensDisplayFormat.bytes(group.allocated)];
        for (const value of values) {
          const cell = document.createElement('td');
          cell.textContent = value;
          row.append(cell);
        }
        fragment.append(row);
        if (group.error) {
          const error = document.createElement('tr'), cell = document.createElement('td');
          cell.colSpan = 3; cell.className = 'inline-error'; cell.textContent = group.error;
          error.append(cell); fragment.append(error);
        }
      }
      body.replaceChildren(fragment);
    }
    const confirmations = {
      'clear-cache':'清理未使用的工作簿索引？',
      'clear-uploads':'删除未使用的上传副本？仅存于此处的文件将无法从这里找回。'
    };
    async function run(action) {
      if (busy) return;
      if (confirmations[action] && !window.confirm(confirmations[action])) return;
      const directory = $('storageDirectory').value.trim();
      if ((action === 'backup' || action === 'restore') && !directory) {
        show('请输入目录的完整路径', true); $('storageDirectory').focus(); return;
      }
      working(true);
      show(action === 'backup' ? '正在复制并核验备份…' : action === 'restore' ? '正在核验并迁入备份…' : action === 'refresh' ? '正在统计…' : '正在处理…');
      let message = '';
      try {
        if (action !== 'refresh') {
          const result = await api('/api/storage', {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify({action,directory})});
          message = result.message;
        }
        await refresh();
        show(message);
      } catch (error) { show([message, error.message].filter(Boolean).join('。'), true); }
      finally { working(false); }
    }
    for (const button of buttons) button.addEventListener('click', () => run(button.dataset.storage));
    for (const id of ['storageHome','storageOpenBtn']) $(id).addEventListener('click', () => {
      openDialog('storageDialog');
      if (!busy) void run('refresh');
    });
  };
})();
