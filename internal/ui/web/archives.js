(() => {
  'use strict';
  const $ = id => document.getElementById(id);
  const delay = ms => new Promise(resolve => setTimeout(resolve, ms));

  class Archives {
    constructor({api, toast, open, fmtBytes,canOpen=()=>true}) {
      this.canOpen=canOpen;
      Object.assign(this, {api, toast, openWorkbook:open, fmtBytes, archive:null, items:[], offset:0, total:0, selected:-1, ticket:0, operation:0, busy:false, job:''});
      this.dialog=$('archiveDialog');
      $('archiveBtn').addEventListener('click',()=>this.present(this.archive));
      $('archiveSearch').addEventListener('input',()=>{
        clearTimeout(this.searchTimer);
        this.searchTimer=setTimeout(()=>{this.offset=0;this.load();},120);
      });
      $('archiveSearch').addEventListener('keydown',event=>{
        if(event.isComposing||event.keyCode===229)return;
        if(event.key==='ArrowDown'&&this.items.length){event.preventDefault();$('archiveList').focus();this.select(this.selected>=0?this.selected:this.items[0].id);}
        if(event.key==='Enter'&&this.selected>=0){event.preventDefault();this.openSelected();}
      });
      $('archivePrev').addEventListener('click',()=>{this.offset=Math.max(0,this.offset-100);this.load();});
      $('archiveNext').addEventListener('click',()=>{this.offset+=100;this.load();});
      $('archiveOpen').addEventListener('click',()=>this.openSelected());
      $('archiveAllowPartial').addEventListener('change',()=>this.controls());
      $('archiveCancel').addEventListener('click',()=>this.cancel());
      $('archiveList').addEventListener('click',event=>{
        const row=event.target.closest('[data-member]');
        if(row&&!this.busy)this.select(Number(row.dataset.member));
      });
      $('archiveList').addEventListener('dblclick',event=>{
        const row=event.target.closest('[data-member]');
        if(row&&!this.busy){this.select(Number(row.dataset.member));this.openSelected();}
      });
      $('archiveList').addEventListener('keydown',event=>this.keydown(event));
      this.dialog.addEventListener('close',()=>{if(this.busy)this.cancel();});
    }
    use(summary) {
      if(this.archive?.id!==summary?.id){
        this.activity?.end();
        ++this.ticket;++this.operation;this.controller?.abort();
        const previousJob=this.job;
        this.job='';this.busy=false;
        if(previousJob)this.api('/api/archive/job?id='+encodeURIComponent(previousJob),{method:'POST'}).catch(()=>{});
        this.items=[];this.offset=0;this.total=0;this.selected=-1;$('archiveSearch').value='';
        $('archiveAllowPartial').checked=false;
        $('archivePartialChoice').hidden=true;
        $('archiveSearch').disabled=false;$('archiveProgress').hidden=true;
      }
      this.archive=summary||null;
      $('archiveNotice').textContent=summary?.notice||'';
      $('archiveNotice').hidden=!summary?.notice;
      $('archiveBtn').hidden=!summary;
      $('archiveBtn').title=summary?'浏览 '+summary.name:'浏览当前压缩包';
    }
    present(summary) {
      if(!summary)return;
      this.use(summary);
      $('archiveTitle').textContent=summary.name;
      if(!this.dialog.open)this.dialog.showModal();
      this.load();
      $('archiveSearch').focus();
    }
    error(message) {
      $('archiveError').textContent=message||'';
      $('archiveError').hidden=!message;
    }
    async load() {
      if(!this.archive||this.busy)return;
      const ticket=++this.ticket;
      this.controller?.abort();
      this.controller=new AbortController();
      this.error('');
      $('archiveSummary').textContent='正在读取文件目录…';
      try {
        const params=new URLSearchParams({id:this.archive.id,q:$('archiveSearch').value,offset:this.offset,limit:100});
        const data=await this.api('/api/archive/list?'+params,{signal:this.controller.signal});
        if(ticket!==this.ticket)return;
        const previous=this.selected;
        this.items=data.items||[];this.total=data.total;
        if(data.archive){this.archive=data.archive;$('archiveNotice').textContent=data.archive.notice||'';$('archiveNotice').hidden=!data.archive.notice;}
        this.selected=this.items.some(item=>item.id===previous)?previous:this.items[0]?.id??-1;
        if(this.selected!==previous)$('archiveAllowPartial').checked=false;
        this.render();
      } catch(error){if(error.name!=='AbortError'&&ticket===this.ticket)this.error(error.message);}
    }
    render() {
      const list=$('archiveList'), fragment=document.createDocumentFragment();
      for(const item of this.items){
        const row=document.createElement('div');row.className='archive-member';row.dataset.member=item.id;row.id='archive-member-'+item.id;row.setAttribute('role','option');
        row.classList.toggle('incomplete',!!item.incomplete);
        const icon=document.createElement('span');icon.className='archive-file-kind';icon.textContent=item.archive?'压缩包':item.name.split('.').pop().toUpperCase();
        const content=document.createElement('span');content.className='archive-file-label';
        const slash=item.name.lastIndexOf('/');
        const name=document.createElement('strong');name.textContent=item.name.slice(slash+1);
        if(item.incomplete)name.textContent+='（不完整片段）';
        const folder=document.createElement('span');folder.textContent=slash<0?'根目录':item.name.slice(0,slash);folder.title=item.name;
        content.append(name,folder);
        const size=document.createElement('span');size.className='archive-file-size';size.textContent=item.size<0?'按需读取':this.fmtBytes(item.size);
        if(item.incomplete)size.title=`现存 ${this.fmtBytes(item.size)} / 原声明 ${this.fmtBytes(item.declaredSize)}`;
        row.append(icon,content,size);fragment.append(row);
      }
      if(!this.items.length){const empty=document.createElement('div');empty.className='archive-empty';empty.textContent='没有匹配的文件，请调整筛选条件。';fragment.append(empty);}
      list.replaceChildren(fragment);list.scrollTop=0;
      this.select(this.selected,false);
      this.controls();
    }
    controls() {
      const partial=!!this.items.find(item=>item.id===this.selected)?.incomplete;
      $('archivePartialChoice').hidden=!partial;
      $('archiveAllowPartial').disabled=this.busy;
      $('archiveOpen').textContent=partial?'查看 TXT 片段':'打开文件';
      $('archiveOpen').disabled=this.busy||this.selected<0||(partial&&!$('archiveAllowPartial').checked);
      $('archiveSearch').disabled=this.busy;
      $('archivePrev').disabled=this.busy||this.offset===0;
      $('archiveNext').disabled=this.busy||this.offset+this.items.length>=this.total;
      $('archiveCancel').disabled=!this.busy;
      $('archiveList').setAttribute('aria-busy',String(this.busy));
      $('archiveSummary').textContent=this.total?`${this.offset+1}–${this.offset+this.items.length} / ${this.total} 个文件`:'0 个匹配文件';
    }
    select(id,scroll=true) {
      if(id!==this.selected)$('archiveAllowPartial').checked=false;
      this.selected=id;
      for(const row of $('archiveList').children){const active=Number(row.dataset.member)===id;row.classList.toggle('selected',active);row.setAttribute('aria-selected',String(active));if(active&&scroll)row.scrollIntoView({block:'nearest'});}
      $('archiveList').setAttribute('aria-activedescendant',id>=0?'archive-member-'+id:'');
      this.controls();
    }
    keydown(event) {
      if(this.busy||!this.items.length||event.isComposing||event.keyCode===229)return;
      let index=this.items.findIndex(x=>x.id===this.selected);
      if(event.key==='Enter'){event.preventDefault();this.openSelected();return;}
      if(event.key==='ArrowDown')index++;
      else if(event.key==='ArrowUp')index--;
      else if(event.key==='Home')index=0;
      else if(event.key==='End')index=this.items.length-1;
      else if(event.key==='PageDown')index+=10;
      else if(event.key==='PageUp')index-=10;
      else return;
      event.preventDefault();this.select(this.items[Math.max(0,Math.min(this.items.length-1,index))].id);
    }
    async cancel() {
      if(!this.job)return;
      $('archiveCancel').disabled=true;
      $('archiveStage').textContent='正在取消读取…';
      try{await this.api('/api/archive/job?id='+encodeURIComponent(this.job),{method:'POST'});}catch(error){this.error(error.message);}
    }
    async openSelected() {
      if(this.busy||this.selected<0)return;
      if(!this.canOpen())return;
      const partial=!!this.items.find(item=>item.id===this.selected)?.incomplete;
      if(partial&&!$('archiveAllowPartial').checked){this.error('该 TXT 不完整，请先勾选「仅查看现存 TXT 片段」。');return;}
      const operation=++this.operation;
      const activity=this.activity=globalThis.ExcelLensActivity?.begin('正在读取压缩包',{slot:'archiveActivity'});
      this.busy=true;this.error('');this.controls();
      $('archiveProgress').hidden=false;
      $('archiveStage').textContent='正在准备读取…';$('archiveBar').removeAttribute('value');
      try {
        const started=await this.api('/api/archive/open',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({archive:this.archive.id,member:this.selected,allowPartial:partial&&!!$('archiveAllowPartial').checked})});
        if(operation!==this.operation){this.api('/api/archive/job?id='+encodeURIComponent(started.jobId),{method:'POST'}).catch(()=>{});return;}
        this.job=started.jobId;
        for(;;){
          const state=await this.api('/api/archive/job?id='+encodeURIComponent(started.jobId));
          if(operation===this.operation)activity?.job(state);
          if(operation!==this.operation)return;
          $('archiveStage').textContent=state.stage;
          if(state.totalBytes>0){$('archiveBar').value=Math.min(100,state.doneBytes/state.totalBytes*100);}else $('archiveBar').removeAttribute('value');
          if(state.status==='error')throw new Error(state.error);
          if(state.status==='cancelled'){this.error('读取已取消。可以选择其他文件。');break;}
          if(state.status==='done'){
            this.busy=false;this.job='';this.dialog.close();
            await this.openWorkbook(state.result);break;
          }
          await delay(240);
        }
      }catch(error){if(operation===this.operation)this.error(error.message);}
      finally{activity?.end();if(operation===this.operation){this.activity=null;this.busy=false;this.job='';$('archiveProgress').hidden=true;this.controls();}}
    }
  }
  window.ExcelLensArchives=Archives;
})();
