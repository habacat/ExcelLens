(() => {
  'use strict';
  window.ExcelLensStorage = function({api, openDialog, background}) {
    const $ = id => document.getElementById(id);
    const dialog = $('storageDialog'), status = $('storageStatus'), body = $('storageRows');
    const buttons = Array.from(dialog.querySelectorAll('[data-storage]'));
    let historyIssue=null;
    let busy = false,job=null,timer,serial=0,sources=[];
    function working(value) {
      busy = value;
      for (const button of buttons) button.disabled = value;
      $('storageDirectory').disabled = value;
      $('storagePassword').disabled=value;
      if ($('storageConditionalBudget')) $('storageConditionalBudget').disabled = value;
      dialog.setAttribute('aria-busy', String(value));
    }
    function show(message, failed = false) {
      status.textContent = message;
      status.classList.toggle('inline-error', failed);
    }
    async function refresh() {
      const {roots, groups, conditionalStats} = await api('/api/storage');
      $('storageRoots').textContent = '用户配置　' + roots.config + '\n本机数据　' + roots.local;
      if (conditionalStats && $('storageConditionalBudget')) {
        const fmt = window.ExcelLensDisplayFormat.bytes;
        const budgetMB = Math.round((conditionalStats.budgetBytes || 0) / (1024 * 1024));
        if (document.activeElement !== $('storageConditionalBudget')) {
          $('storageConditionalBudget').value = String(budgetMB);
        }
        const summary = $('storageConditionalSummary');
        if (summary) {
          const used = fmt(conditionalStats.usedBytes || 0);
          const limit = fmt(conditionalStats.budgetBytes || 0);
          const active = conditionalStats.protectedFiles ? `（在用 ${conditionalStats.protectedFiles} 个）` : '';
          summary.textContent = `当前 ${used} / 上限 ${limit}${active}`;
        }
      }
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
    const askConfirm = options => window.ExcelLensOverlays?.confirm ? window.ExcelLensOverlays.confirm(options) : Promise.resolve(window.confirm(options.message));
    const confirmations = {
      'clear-cache': {title:'清理历史索引', message:'清理未使用的工作簿索引？', confirmLabel:'清理', danger:true},
      'clear-conditional-stats': {title:'清理条件统计缓存', message:'清理未使用的条件统计索引？正在使用的索引会保留。', confirmLabel:'清理', danger:true},
      'clear-uploads': {title:'清理闲置上传副本', message:'删除未使用的上传副本？仅存于此处的文件将无法从这里找回。', confirmLabel:'删除', danger:true}
    };
    const commands={backup:'备份',restore:'迁入备份',migrate:'迁入旧设置','verify-source':'核验来源','relocate-source':'更新来源'};
    const post=(url,payload)=>api(url,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(payload)});
    function describe(item){
      const p=item.progress||{},fmt=window.ExcelLensDisplayFormat.bytes;
      return [item.message,item.error].filter(Boolean).join('。')||[p.phase,p.files?p.files+' / '+(p.items||'…')+' 项':'',p.bytes?fmt(p.bytes)+(p.total?' / '+fmt(p.total):''):''].filter(Boolean).join(' · ');
    }
    function renderHistory(jobs){
      const holder=$('storageJobs');holder.replaceChildren();
      for(const item of jobs){const row=document.createElement('div');row.textContent=new Date(item.started).toLocaleString()+'　'+commands[item.action]+'　'+describe(item);holder.append(row);}
      if(!jobs.length)holder.textContent='没有传输记录';
    }
    function schedule(ticket){
      clearTimeout(timer);
      if(dialog.open)timer=setTimeout(()=>observe(ticket),500);
    }
    async function observe(ticket){
      try{
        const data=await api('/api/storage/jobs');
        if(ticket!==serial)return;
        const jobs=data.jobs;
        historyIssue=data.historyIssue;
        $('storageHistoryIssue').hidden=!historyIssue;
        if(historyIssue){
          $('storageHistoryReason').textContent=historyIssue.reason+'。本次传输记录暂存于内存。';
          $('storageHistoryIssue').querySelector('button').hidden=!historyIssue.revision;
        }
        renderHistory(jobs);
        const item=jobs.find(x=>x.id===job)||jobs[0];
        if(item?.state==='running'){
          job=item.id;working(true);show(describe(item));$('storageCancel').hidden=item.transaction==='committing'||item.transaction==='cancelling';schedule(ticket);return;
        }
        job=null;$('storageCancel').hidden=true;
        if(dialog.open)await refresh();
        if(ticket!==serial)return;
        show(item?describe(item):'',!!item?.error);
        if($('storageSources').open)await loadSources();
        if(ticket===serial)working(false);
      }catch(e){if(ticket===serial){show(e.message,true);if(job)schedule(ticket);else working(false);}}
    }
    async function run(action){
      if(busy)return;
      if(action==='repair-history'){
        const revision=historyIssue?.revision;
        if(!revision||!await askConfirm({title:'重建传输历史',message:'保留原文件作为诊断副本，并使用本次运行的记录重建传输历史？',confirmLabel:'重建'}))return;
        if(busy||!dialog.open||revision!==historyIssue?.revision)return;
        const ticket=++serial;working(true);
        try{const result=await post('/api/storage',{action,revision,confirm:true});await observe(ticket);if(ticket===serial)show(result.message);}
        catch(e){if(ticket===serial){await observe(ticket);show(e.message,true);}}
        return;
      }
      if(confirmations[action]&&!(await askConfirm(confirmations[action])))return;
      if(busy||!dialog.open)return;
      const directory=$('storageDirectory').value.trim();
      if((action==='backup'||action==='restore')&&!directory){show('请输入目录的完整路径',true);$('storageDirectory').focus();return;}
      const password=$('storagePassword').value;
      if(action==='backup'&&password&&!(await askConfirm({title:'加密备份',message:'使用此口令加密备份？恢复时需要同一口令。',confirmLabel:'开始备份'})))return;
      if(busy||!dialog.open)return;
      const ticket=++serial;working(true);show('正在处理…');
      try{
        if(action==='refresh'){await observe(ticket);return;}
        if(action==='list-stages'){
          const {stages}=await post('/api/storage',{action,directory});
          const holder=$('storageStageList');holder.replaceChildren();
          for(const stage of stages){
            const row=document.createElement('div');
            row.textContent=stage.name+'　'+({active:'正在使用',orphan:'未完成',unknown:'缺少标记，请在目录中核对'}[stage.state]);
            if(stage.state==='orphan'){
              const remove=document.createElement('button');remove.type='button';remove.className='mini-button';remove.textContent='清理';
              remove.onclick=async()=>{
                if(!await askConfirm({title:'清理未完成备份',message:stage.name,confirmLabel:'清理',danger:true}))return;
                try{await post('/api/storage',{action:'clean-stage',directory,stage:stage.name});row.remove();show('已清理未完成备份');}catch(error){show(error.message,true);}
              };
              row.append(remove);
            }
            holder.append(row);
          }
          if(!stages.length)holder.textContent='没有未完成的备份';
          working(false);show('');return;
        }
        if(action==='set-conditional-stats-budget'){
          const mb=Number($('storageConditionalBudget')?.value);
          if(!Number.isFinite(mb)||mb<64||mb>65536){show('请输入 64 到 65536 MB 之间的上限',true);$('storageConditionalBudget')?.focus();working(false);return;}
          const item=await post('/api/storage',{action,budgetBytes:Math.round(mb)*1024*1024});
          await refresh();show(item.message);working(false);return;
        }
        $('storagePassword').value='';
        const item=await post('/api/storage',{action,directory,password:(action==='backup'||action==='restore')?password:''});
        if(commands[action]){job=item.id;await observe(ticket);return;}
        await refresh();show(item.message);working(false);
      }catch(e){show(e.message,true);working(false);}
    }
    $('storageCancel').addEventListener('click',async()=>{
      if(!job)return;
      try{await post('/api/storage/jobs?id='+encodeURIComponent(job),{});show('正在取消…');}catch(e){show(e.message,true);}
    });
    function selectSource(){
      const item=sources.find(x=>x.book===$('storageSource').value);
      $('storageCandidate').value=item?.candidate||'';
      $('storageMoves').textContent=item?[item.original,...(item.moves||[]).map(x=>new Date(x.at).toLocaleString()+'　'+x.to)].join('\n'):'没有可重定位的来源';
      $('storageRelocate').disabled=!item;
    }
    async function loadSources(){
      try{
        const {items}=await api('/api/storage/sources');sources=items||[];
        const select=$('storageSource'),previous=select.value;select.replaceChildren();
        for(const x of sources){const option=document.createElement('option');option.value=x.book;option.textContent=x.path;select.append(option);}
        if(sources.some(x=>x.book===previous))select.value=previous;
        selectSource();
      }catch(e){show(e.message,true);}
    }
    $('storageSources').addEventListener('toggle',()=>{if($('storageSources').open&&!busy)loadSources();});
    $('storageSource').addEventListener('change',selectSource);
    async function locate(request,ticket){
      const started=await post('/api/storage/sources',request);
      job=started.id;
      while(ticket===serial&&dialog.open){
        const item=await api('/api/storage/jobs?id='+encodeURIComponent(started.id));
        if(ticket!==serial||!dialog.open)return null;
        show(describe(item),!!item.error);
        $('storageCancel').hidden=item.state!=='running'||item.transaction==='committing'||item.transaction==='cancelling';
        if(item.state==='done'){job=null;return item;}
        if(item.state!=='running'){job=null;throw Error(item.error||'来源核验失败');}
        await new Promise(resolve=>setTimeout(resolve,350));
      }
      return null;
    }
    $('storageRelocate').addEventListener('click',async()=>{
      const item=sources.find(x=>x.book===$('storageSource').value);
      if(!item||busy)return;
      const request={book:item.book,previous:item.path,path:$('storageCandidate').value.trim()};
      const ticket=++serial;working(true);
      try{
        const checked=await locate(request,ticket);
        if(!checked||ticket!==serial||!dialog.open)return;
        const accepted=await askConfirm({title:'确认更新来源位置',message:'内容指纹相符。确认更新来源位置？\n'+item.path+'\n→ '+checked.candidate,confirmLabel:'确认更新'});
        if(!accepted||ticket!==serial||!dialog.open)return;
        if(await locate({...request,path:checked.candidate,confirm:true},ticket))await loadSources();
      }catch(e){if(ticket===serial)show(e.message,true);}finally{if(ticket===serial){$('storageCancel').hidden=true;working(false);}}
    });
    for(const button of buttons)button.addEventListener('click',()=>run(button.dataset.storage));
    dialog.addEventListener('close',()=>{++serial;clearTimeout(timer);$('storagePassword').value='';});
    for(const id of ['storageHome','storageOpenBtn'])$(id).addEventListener('click',()=>{
      working(true);openDialog('storageDialog');void observe(++serial);if($('storageBackground').open)background();
    });
  };
})();
