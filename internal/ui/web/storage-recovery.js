(() => {
  'use strict';
  const button = document.getElementById('recoveryRetry');
  const status = document.getElementById('recoveryProgress');
  const headers = {'X-ExcelLens-Token': document.querySelector('meta[name="app-token"]').content};
  async function update(method) {
    button.disabled = true;
    try {
      const response = await fetch('/api/storage/recovery', {method, headers, cache:'no-store'});
      const result = await response.json();
      if (!response.ok) throw Error(result.error || '无法读取恢复事务');
      if (!result.pending) { window.location.replace('/'); return; }
      const reason = document.getElementById('recoveryReason');
      reason.hidden = result.running;
      reason.textContent = result.running ? '' : result.reason;
      document.getElementById('recoveryDirectories').textContent = '配置目录　'+result.roots.config+'\n数据目录　'+result.roots.local;
      const progress = result.progress;
      const bytes = window.ExcelLensDisplayFormat.bytes;
      const transferred = progress.total ? bytes(progress.bytes || 0) + ' / ' + bytes(progress.total) : '';
      const files = progress.items ? (progress.files || 0) + ' / ' + progress.items + ' 项' : '';
      status.textContent = result.running ? [progress.phase, transferred, files].filter(Boolean).join(' · ') : '';
      button.disabled = result.running;
      if (result.running) setTimeout(() => update('GET'), 600);
    } catch (error) { status.textContent = error.message; button.disabled = false; }
  }
  document.getElementById('recoveryUsage').ontoggle = async event => {
    if (!event.target.open) return;
    const holder = document.getElementById('recoveryGroups');
    try {
      const response = await fetch('/api/storage',{headers,cache:'no-store'});
      const data = await response.json();
      if (!response.ok) throw Error(data.error);
      holder.replaceChildren(...data.groups.map(group => {
        const row = document.createElement('p');
        row.textContent = group.name+'　'+(group.error || window.ExcelLensDisplayFormat.bytes(group.allocated));
        return row;
      }));
    } catch (error) { holder.textContent = error.message; }
  };
  button.onclick = () => update('POST');
  void update('GET');
})();
