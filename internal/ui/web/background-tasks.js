(() => {
  'use strict';
  const names={analysis:'分析',reconcile:'对账',export:'导出',search:'搜索',filter:'筛选'};
  const endpoint=kind=>'/api/'+(kind==='analysis'?'analysis/query':kind);
  const $=id=>document.getElementById(id);
  window.ExcelLensBackground=function({api,state,openDialog,toast,adoptTask,analysis,reconcile}){
    let timer,serial=0;
    const box=$('storageBackground'),holder=$('storageWork');
    async function openResult(item){
      if(!item.canAttach)throw Error('任务仍在原标签页中');
      if(item.session!==window.ExcelLensSession.id){
        const url=new URL(window.location.pathname,window.location.href);
        url.search=new URLSearchParams({session:item.session,job:item.id}).toString();
        window.open(url.href,'_blank','noopener');return;
      }
      if(item.state!=='done')throw Error(item.error||item.stage||'任务仍在运行');
      const status=await api(endpoint(item.kind)+'/status?id='+encodeURIComponent(item.id));
      if(status.status!=='done')throw Error(status.error||'任务尚未完成');
      $('storageDialog').close();
      if(item.kind==='analysis')return analysis(item.id,status);
      if(item.kind==='reconcile')return reconcile(item.id,status);
      const ok=await adoptTask({operation:item.kind,jobId:item.id,filter:status.filter},
        {...status,mode:status.mode||status.format,book:state.book,workbookKey:state.workbookKey,file:item.file});
      if(!ok)throw Error('当前视图正在使用，请结束当前操作后再打开结果');
    }
    function addAction(row,label,payload){
      const button=document.createElement('button');
      button.type='button';button.className='mini-button';button.textContent=label;
      button.onclick=async()=>{
        if(payload.action==='release' && !await window.ExcelLensOverlays.confirm({title:'释放会话',message:'释放此工作簿的后台结果？已保存的文件仍可使用。',confirmLabel:label}))return;
        button.disabled=true;
        try{await api('/api/background',{method:'POST',body:JSON.stringify(payload)});await refresh();}
        catch(error){button.disabled=false;toast(error.message,'error');}
      };
      row.append(button);
    }
    async function refresh(){
      clearTimeout(timer);
      const ticket=++serial;
      try{
        const {items,sessions}=await api('/api/background');
        if(ticket!==serial||!box.open||!$('storageDialog').open)return;
        holder.replaceChildren();
        for(const item of items||[]){
          const row=document.createElement('div'),label=document.createElement('span');
          label.textContent=[names[item.kind],item.file,item.error||item.stage||item.state].join(' · ');row.append(label);
          if(item.state==='done'&&item.canAttach){
            const button=document.createElement('button');button.type='button';button.className='mini-button';button.textContent='查看结果';
            button.onclick=()=>openResult(item).catch(e=>toast(e.message,'error'));row.append(button);
          }
          const live=item.state==='running'||item.state==='queued';
          addAction(row,live?'取消任务':'丢弃结果',{action:live?'cancel':'discard',session:item.session,kind:item.kind,id:item.id});
          holder.append(row);
        }
        for(const session of sessions||[]){
          const row=document.createElement('div');
          row.append(session.file);
          addAction(row,'释放会话',{action:'release',session:session.session});
          holder.append(row);
        }
        if(!items?.length&&!sessions?.length)holder.textContent='没有后台任务';
        timer=setTimeout(refresh,1500);
      }catch(e){if(ticket===serial)holder.textContent=e.message;}
    }
    box.addEventListener('toggle',()=>{if(box.open)refresh();else{++serial;clearTimeout(timer);}});
    const job=new URLSearchParams(window.location.search).get('job');
    if(job)window.addEventListener('load',async()=>{
      try{
        await window.ExcelLensWorkbookReady;
        const {items}=await api('/api/background');
        const item=items?.find(x=>x.id===job&&x.session===window.ExcelLensSession.id);
        if(!item)throw Error('后台结果已到期，请查看已保存的可恢复任务');
        await openResult(item);
      }catch(e){toast(e.message,'error');}
      finally{const url=new URL(location.href);url.searchParams.delete('job');history.replaceState(history.state,'',url.pathname+url.search+url.hash);}
    },{once:true});
    return refresh;
  };
})();
