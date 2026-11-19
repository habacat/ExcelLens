(() => {
  'use strict';
  const $=id=>document.getElementById(id),interrupted=()=>new DOMException('已取消行显隐更新','AbortError');
  class RowViews {
    constructor({state,api,maximum,commit,toast,navigate,reload,openDialog}){
      Object.assign(this,{s:state,api,maximum,commit,toast,navigate,reload,openDialog});
      this.serial=0;this.job='';this.abort=null;this.cache=new Map();this.busy=false;this.page=0;this.pageSerial=0;this.groupTotal=0;
      $('rowsBtn').addEventListener('click',()=>this.open());
      $('rowsDialog').addEventListener('close',()=>{this.pageSerial++;});
      $('rowsCancel').addEventListener('click',()=>this.cancel());
      for(const [id,action]of [['rowsOriginal','original'],['rowsShowAll','show-all']])$(id).addEventListener('click',()=>this.change({action}));
      $('rowsHideRange').addEventListener('click',()=>this.range(true));$('rowsShowRange').addEventListener('click',()=>this.range(false));
      $('rowsDepthApply').addEventListener('click',()=>this.change({action:'depth',depth:Number($('rowsDepth').value)}));
      $('rowsPrevious').addEventListener('click',()=>{this.page=Math.max(0,this.page-1);this.groups();});
      $('rowsNext').addEventListener('click',()=>{this.page++;this.groups();});
      $('showRowsRecoveryBtn').addEventListener('click',()=>{this.recoverySettings={mode:'show-all'};this.reload();});
    }
    post(url,body,options={}){return this.api(url,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body),...options});}
    cleanup(id){if(id)this.post('/api/row-view/clear',{id}).catch(()=>{});}
    cancel(keepBusy=false){++this.serial;this.abort?.abort();this.abort=null;if(this.job)this.post('/api/row-view/cancel',{id:this.job}).catch(()=>{});this.job='';if(!keepBusy){this.changeSerial=(this.changeSerial||0)+1;if(this.busy)this.status('已取消更新');this.busy=false;}this.controls();}
    resetBook(){this.cancel();++this.pageSerial;for(const data of this.cache.values())this.cleanup(data.id);this.cache.clear();this.s.rowView=null;this.s.rowVisibility=null;this.s.rowViewFailure=false;this.recoverySettings=null;$('rowsDialog').close();}
    current(ticket,book,sheet,epoch){return ticket===this.serial&&book===this.s.workbookKey&&sheet===this.s.sheet&&epoch===this.s.sheetEpoch;}
    validate(data,sheet,filterId){
      if(!data||data.workbookKey!==this.s.workbookKey||this.s.book&&data.book!==this.s.book||data.sheet!==sheet||data.filterId!==filterId||typeof data.id!=='string'||!data.id||typeof data.digest!=='string'||!data.digest||!Number.isSafeInteger(data.revision)||data.revision<0||!Number.isSafeInteger(data.maxRow)||data.maxRow<0||data.maxRow>this.maximum()||!Number.isSafeInteger(data.totalRows)||data.totalRows<0||data.totalRows>this.maximum())throw Error('可见行视图来源或数量无效');
      const visibility=new window.ExcelLensRowVisibility(this.maximum(),data.hiddenRanges||[]);
      if(!filterId&&data.totalRows!==visibility.count(1,data.maxRow))throw Error('可见行数量与源显隐不一致');
      return visibility;
    }
    install(data){
      const visibility=this.validate(data,this.s.sheet,this.s.filterId||'');this.s.rowView=data;this.s.rowVisibility=visibility;this.s.rowViewFailure=false;
      if(data.settingsWarning)$('rowsRemember').checked=false;
      const previous=this.cache.get(data.sheet);if(previous&&previous.id!==data.id)this.cleanup(previous.id);
      this.cache.delete(data.sheet);this.cache.set(data.sheet,data);
      let bytes=0;for(const item of this.cache.values())bytes+=(item.hiddenRanges?.length||0)*96+JSON.stringify(item.settings||{}).length*2+512;
      while(this.cache.size>1&&(this.cache.size>4||bytes>16*1024*1024)){const key=this.cache.keys().next().value,old=this.cache.get(key);bytes-=(old.hiddenRanges?.length||0)*96+JSON.stringify(old.settings||{}).length*2+512;this.cache.delete(key);this.cleanup(old.id);}
    }
    async build({sheet=this.s.sheet,filterId=this.s.filterId||'',baseId='',settings,change,persist=false,revision,signal,guard=()=>true}={}){
      this.cancel(true);const ticket=this.serial,book=this.s.workbookKey,origin=this.s.sheet,epoch=this.s.sheetEpoch,abort=new AbortController();this.abort=abort;
      let owned='',accepted=false;
      const activity=globalThis.ExcelLensActivity?.begin('正在更新可见行',{signal:abort.signal});
      const current=()=>this.current(ticket,book,origin,epoch)&&!abort.signal.aborted&&!signal?.aborted&&guard();
      const stop=()=>{abort.abort();if(owned)this.post('/api/row-view/cancel',{id:owned}).catch(()=>{});};signal?.addEventListener('abort',stop,{once:true});
      try{
        if(!this.s.book){const status=await this.api('/api/status',{signal:abort.signal});if(!current()||status.workbookKey!==book)throw new DOMException('已切换来源','AbortError');this.s.book=status.book;}
        if(!current())throw new DOMException('已取消','AbortError');
        const request={workbookKey:book,book:this.s.book,sheet,filterId,baseId,settings,change,persist};if(persist)request.expectedRevision=revision;
        // Keep ownership of a late start response even if the caller is gone;
        // its server task must be cancelled, rather than losing the returned ID.
        const started=await this.post('/api/row-view/start',request);owned=started?.jobId;
        if(typeof owned!=='string'||!owned){owned='';throw Error('可见行任务标识无效，请重试');}
        if(!current())throw new DOMException('已取消','AbortError');this.job=owned;
        for(;;){
          const state=await this.api('/api/row-view/status?id='+encodeURIComponent(owned),{signal:abort.signal});
          if(!current())throw new DOMException('已取消','AbortError');
          activity?.job(state);
          if(state.status==='done')break;
          if(state.status==='error'||state.status==='cancelled')throw Error(state.error||'可见行任务已取消');
          if(state.status!=='queued'&&state.status!=='running')throw Error('可见行任务状态无效，请重试');
          if(this.busy)this.status('正在更新可见行'+(state.total?' · '+Number(state.scanned||0).toLocaleString()+' / '+Number(state.total).toLocaleString():'')+'…');
          await new Promise(resolve=>setTimeout(resolve,60));
        }
        const data=await this.api('/api/row-view?id='+encodeURIComponent(owned),{signal:abort.signal});
        if(!current())throw interrupted();this.validate(data,sheet,filterId);if(data.id!==owned)throw Error('可见行任务结果不一致，请重试');accepted=true;return data;
      }catch(error){
        if(!current())throw interrupted();throw error;
      }finally{
        activity?.end();
        signal?.removeEventListener('abort',stop);if(!accepted&&owned){this.post('/api/row-view/cancel',{id:owned}).catch(()=>{});this.cleanup(owned);}
        if(ticket===this.serial){this.job='';if(this.abort===abort)this.abort=null;}
      }
    }
    async prepare({signal,settings:requestedSettings}={}){
      const sheet=this.s.sheet,filterId=this.s.filterId||'',cached=this.cache.get(sheet),book=this.s.workbookKey,epoch=this.s.sheetEpoch;
      const current=()=>book===this.s.workbookKey&&sheet===this.s.sheet&&epoch===this.s.sheetEpoch&&filterId===(this.s.filterId||'')&&!signal?.aborted;
      let ticket=this.serial,data;
      this.s.rowViewFailure=false;
      try{
        if(cached&&cached.filterId===filterId&&!this.recoverySettings&&!requestedSettings){
          try{data=await this.api('/api/row-view?id='+encodeURIComponent(cached.id),{signal});if(!current()||ticket!==this.serial)throw interrupted();if(data?.id!==cached.id)throw Error('可见行快照不一致，请重试');this.install(data);return data;}
          catch(error){if(error.status!==404)throw error;this.cache.delete(sheet);}
        }
        const settings=requestedSettings||this.recoverySettings||cached?.settings;this.recoverySettings=null;
        const pending=this.build({filterId,settings,signal,guard:current});ticket=this.serial;data=await pending;
        if(!current()||ticket!==this.serial){this.cleanup(data.id);throw interrupted();}this.install(data);return data;
      }catch(error){if(error.name!=='AbortError'&&current()&&ticket===this.serial)this.s.rowViewFailure=true;throw error;}
    }
    async prepareFilter(filterId,options={}){return this.build({filterId,baseId:this.s.rowView?.id||'',...options});}
    controls(){for(const id of ['rowsOriginal','rowsShowAll','rowsHideRange','rowsShowRange','rowsDepthApply','rowsFirst','rowsLast','rowsDepth'])$(id).disabled=this.busy;$('rowsRemember').disabled=this.busy||!!this.s.rowView?.settingsWarning;$('rowsRemember').title=this.s.rowView?.settingsWarning?'原设置无法读取；当前操作临时生效，重新打开后可重试保存':'';$('rowsCancel').hidden=!this.busy;$('rowsGroupList').inert=this.busy;$('rowsPrevious').disabled=this.busy||this.page===0;$('rowsNext').disabled=this.busy||(this.page+1)*200>=this.groupTotal;}
    status(text,error=false){$('rowsStatus').textContent=text;$('rowsStatus').classList.toggle('is-error',error);}
    async open(){if(!this.s.indexReady){this.toast('请先打开工作表','warning');return;}const range=this.s.range,selected=this.s.selected?.row||1;$('rowsFirst').value=range?Math.min(range.row1,range.row2):selected;$('rowsLast').value=range?Math.max(range.row1,range.row2):selected;this.page=0;this.status(this.s.rowView?.notice||'');this.controls();this.openDialog('rowsDialog');await this.groups();}
    async groups(){
      const id=this.s.rowView?.id,ticket=++this.pageSerial;if(!id)return;
      try{const data=await this.api(`/api/row-view/groups?id=${encodeURIComponent(id)}&offset=${this.page*200}&limit=200`);if(ticket!==this.pageSerial||id!==this.s.rowView?.id)return;
        const list=$('rowsGroupList');list.replaceChildren();
        for(const group of data.groups||[]){const row=document.createElement('div');row.className='outline-item';row.style.setProperty('--outline-level',String(group.level));const label=document.createElement('span');label.textContent=`${group.min}–${group.max} 行`;const level=document.createElement('small');level.textContent=`第${group.level}级`;const button=document.createElement('button');button.type='button';button.className='tool-button';button.textContent=group.collapsed||!group.visible?'展开':'折叠';button.setAttribute('aria-expanded',String(!!group.visible&&!group.collapsed));button.addEventListener('click',()=>this.change({action:'group',group:{min:group.min,max:group.max,level:group.level,collapsed:button.textContent==='折叠'}}));row.append(label,level,button);list.append(row);}
        if(!data.total){const empty=document.createElement('p');empty.className='dialog-summary';empty.textContent='此工作表没有分组大纲';list.append(empty);}
        this.groupTotal=data.total;$('rowsPage').textContent=data.total?`${this.page+1} / ${Math.ceil(data.total/200)}`:'';this.controls();
      }catch(error){if(ticket===this.pageSerial&&id===this.s.rowView?.id)this.status(error.message,true);}
    }
    range(hidden){const first=Number($('rowsFirst').value),last=Number($('rowsLast').value);if(!Number.isSafeInteger(first)||!Number.isSafeInteger(last)||first<1||last<first||last>this.maximum()){this.status('请输入有效的源行范围',true);return;}this.change({action:'range',first,last,hidden});}
    async change(change,{temporary=false,guard=()=>true}={}){
      if(this.busy)return false;
      const base=this.s.rowView;if(!base)return false;const intent=this.changeSerial=(this.changeSerial||0)+1,book=this.s.workbookKey,sheet=this.s.sheet,epoch=this.s.sheetEpoch,filterId=this.s.filterId||'';
      let ticket,data;const current=()=>intent===this.changeSerial&&(ticket===undefined||ticket===this.serial)&&this.current(this.serial,book,sheet,epoch)&&filterId===(this.s.filterId||'')&&guard();
      this.busy=true;this.controls();this.status('正在更新可见行…');
      try{const pending=this.build({baseId:base.id,change,persist:!temporary&&!base.settingsWarning&&$('rowsRemember').checked,revision:base.revision,guard:current});ticket=this.serial;data=await pending;if(!current())throw interrupted();await this.commit(data,current);if(!current()||this.s.rowView?.id!==data.id)throw interrupted();this.status(temporary?'已临时显示；未改动已保存设置':data.settingsWarning?'已临时更新；原设置文件已保留':'已更新行显隐');this.page=0;if($('rowsDialog').open)await this.groups();return true;}
      catch(error){if(current()&&error.name!=='AbortError')this.status(error.message,true);return false;}
      finally{if(data&&!Array.from(this.cache.values()).some(item=>item.id===data.id))this.cleanup(data.id);if(intent===this.changeSerial){this.busy=false;this.controls();}}
    }
    async reveal(row,guard=()=>true){return this.change({action:'range',first:row,last:row,hidden:false},{temporary:true,guard});}
  }
  window.ExcelLensRowViews=RowViews;
})();
