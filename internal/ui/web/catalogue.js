(() => {
  'use strict';
  const $=id=>document.getElementById(id),label=item=>item.status==='ready'?'可定位':item.kind==='table'&&item.status==='unresolved'?'待确认':'不可定位';
  const element=(tag,className='',text='')=>{const node=document.createElement(tag);node.className=className;node.textContent=text;return node;};
  class Catalogue {
    constructor({state,api,toast,supported,navigate,limits,canNavigate=()=>true,sleep=ms=>new Promise(resolve=>setTimeout(resolve,ms))}){
      Object.assign(this,{s:state,api,toast,supported,navigateArea:navigate,canNavigate,limits});
      if(!Number.isSafeInteger(limits?.pageRows)||limits.pageRows<1||!Number.isSafeInteger(limits?.searchBytes)||limits.searchBytes<1)throw Error('目录显示容量配置无效');
      this.epoch=0;this.request=0;this.pageRequest=0;this.detailRequest=0;this.nav=0;this.job='';this.offset=0;this.page=null;this.item=null;this.selected='';this.busy=false;this.navigating=false;
      this.dialog=this.createDialog();
      this.jobs=new window.ExcelLensAnalysisJobs({api,identity:()=>this.identity(),requestIdentity:()=>this.s.workbookKey,sleep,endpoint:'/api/catalogue',label:'目录读取',maxPolls:0,resultFromStatus:status=>status,onProgress:status=>this.status((status.stage||'正在读取')+(status.totalBytes>0?` · ${Math.round(status.doneBytes/status.totalBytes*100)}%`:''))});
      $('catalogueOpenBtn').addEventListener('click',()=>this.open());$('catalogueClose').addEventListener('click',()=>this.close());
      this.dialog.addEventListener('cancel',event=>{event.preventDefault();this.close();});
      this.dialog.addEventListener('close',()=>{if(!this.dialog.open)this.stop();});
      $('catalogueRefresh').addEventListener('click',()=>this.refresh('read'));
      $('catalogueCancel').addEventListener('click',()=>this.cancel());$('catalogueConfirm').addEventListener('click',()=>this.refresh('confirm'));
      $('catalogueOpenSheet').addEventListener('click',()=>this.openSheet());
      for(const id of ['catalogueKind','catalogueSheet'])$(id).addEventListener('change',()=>this.loadPage(0));
      $('catalogueTargetSheet').addEventListener('change',()=>this.controls());
      $('catalogueSearch').addEventListener('input',()=>{clearTimeout(this.searchTimer);this.searchTimer=setTimeout(()=>this.loadPage(0),160);});
      $('cataloguePrev').addEventListener('click',()=>this.loadPage(this.page?.previous||0));$('catalogueNext').addEventListener('click',()=>this.loadPage(this.page?.next||0));
      $('catalogueList').addEventListener('keydown',event=>this.key(event));
      // A later user action cancels a pending arrival; programmatic shared-grid
      // navigation emits neither event.
      document.addEventListener('pointerdown',()=>this.cancelNavigation(),true);document.addEventListener('keydown',()=>this.cancelNavigation(),true);
      this.controls();
    }
    createDialog(){
      const dialog=element('dialog','float-dialog catalogue-dialog');dialog.id='catalogueDialog';dialog.setAttribute('aria-labelledby','catalogueTitle');
      // Static markup only. Workbook names, definitions and reasons never enter HTML.
      dialog.innerHTML='<div class="dialog-head"><div id="catalogueTitle" class="dialog-title">名称与表目录</div><button id="catalogueClose" class="dialog-close" type="button" aria-label="关闭名称与表目录">×</button></div><p id="catalogueSource" class="catalogue-source"></p><div class="catalogue-tools"><input id="catalogueSearch" class="field-control" type="search" placeholder="搜索名称或工作表" aria-label="搜索名称或工作表"><select id="catalogueKind" class="field-control" aria-label="目录类型"><option value="">全部类型</option><option value="name">定义名称</option><option value="table">表对象</option></select><select id="catalogueSheet" class="field-control" aria-label="目录工作表"></select><button id="catalogueRefresh" class="mini-button" type="button">重新读取</button></div><div class="catalogue-main"><div class="catalogue-master"><div id="catalogueList" class="catalogue-list" role="listbox" aria-label="名称与表对象" tabindex="0"></div><div class="catalogue-paging"><button id="cataloguePrev" class="mini-button" type="button">上一页</button><span id="catalogueCount"></span><button id="catalogueNext" class="mini-button" type="button">下一页</button></div></div><section id="catalogueDetail" class="catalogue-detail" aria-label="目录定义详情"></section></div><div class="catalogue-footer"><p id="catalogueStatus" class="catalogue-status" role="status" aria-live="polite"></p><button id="catalogueCancel" class="mini-button" type="button" hidden>取消读取</button><div class="catalogue-confirm"><select id="catalogueTargetSheet" class="field-control" aria-label="要确认的表对象工作表"></select><button id="catalogueOpenSheet" class="mini-button" type="button">打开工作表</button><button id="catalogueConfirm" class="mini-button" type="button">确认表对象</button></div></div>';
      document.body.append(dialog);return dialog;
    }
    identity(){return JSON.stringify([this.s.workbookKey,this.s.book,this.epoch]);}
    post(path,data){return this.api(path,{method:'POST',body:JSON.stringify(data)});}
    release(id){if(id)this.post('/api/catalogue/clear',{id}).catch(()=>{});}
    validSource(value){if(value?.workbookKey!==this.s.workbookKey||value?.book!==this.s.book)throw Error('目录来源已切换，请重新读取');}
    status(text,error=false){$('catalogueStatus').textContent=text||'';$('catalogueStatus').classList.toggle('is-error',error);$('catalogueStatus').setAttribute('role',error?'alert':'status');}
    controls(){
      const enabled=!!this.s.opened&&!!this.supported(),target=$('catalogueTargetSheet').value;
      $('catalogueOpenBtn').disabled=!enabled;$('catalogueRefresh').disabled=!enabled||this.busy;$('catalogueCancel').hidden=!this.busy;
	  for(const id of ['catalogueSearch','catalogueKind','catalogueSheet','catalogueTargetSheet'])$(id).disabled=this.busy;
      $('catalogueConfirm').disabled=!enabled||this.busy||!target||target!==this.s.sheet||!this.s.indexReady;
      $('catalogueConfirm').title=target===this.s.sheet&&this.s.indexReady?'核对这张工作表真实声明的表对象':'先打开并完成索引，再确认这张表的对象';
      $('catalogueOpenSheet').disabled=!target||this.busy||!this.canNavigate();
      $('cataloguePrev').disabled=this.busy||!this.page||this.offset===0;$('catalogueNext').disabled=this.busy||!this.page||this.page.next>=this.page.matched;
    }
    sheets(){
      const filter=$('catalogueSheet'),target=$('catalogueTargetSheet'),chosen=filter.value;
      filter.replaceChildren(new Option('全部作用域与表',''),new Option('工作簿名称','workbook:'));target.replaceChildren();
      for(const sheet of this.s.sheets||[]){filter.append(new Option(sheet.name,'sheet:'+sheet.name));target.append(new Option(sheet.name,sheet.name));}
      filter.value=[...(this.s.sheets||[]).map(s=>'sheet:'+s.name),'','workbook:'].includes(chosen)?chosen:'';
      target.value=this.s.sheet||'';
    }
    async open(search=''){
      if(!this.s.opened||!this.supported()){this.toast('当前来源没有工作簿名称或表对象目录','warning');return;}
      this.sheets();$('catalogueSource').textContent=this.s.fileName||'当前工作簿';if(search)$('catalogueSearch').value=search;
      if(!this.dialog.open)this.dialog.showModal();this.controls();
      if(this.job&&!this.busy)await this.loadPage(search?0:this.offset);else if(!this.busy)await this.refresh('read');
      if(this.dialog.open)$('catalogueSearch').focus({preventScroll:true});
    }
    stop(){++this.epoch;++this.request;++this.pageRequest;++this.detailRequest;this.jobs.reset();this.pageController?.abort();this.detailController?.abort();clearTimeout(this.searchTimer);this.busy=false;this.controls();}
    close(){this.stop();if(this.dialog.open)this.dialog.close();}
    resetBook(){this.cancelNavigation();this.stop();this.release(this.job);this.job='';this.page=this.item=null;this.selected='';this.offset=0;$('catalogueSearch').value='';$('catalogueList').replaceChildren();$('catalogueDetail').replaceChildren();this.status('');if(this.dialog.open)this.dialog.close();this.controls();}
    cancel(){++this.request;++this.pageRequest;++this.detailRequest;this.pageController?.abort();this.detailController?.abort();this.jobs.cancelMain();this.busy=false;this.status('已取消读取，原目录保留');this.controls();}
    async refresh(mode){
      if(this.busy)return;if(mode==='confirm'&&$('catalogueConfirm').disabled)return;
      const request=++this.request,identity=this.identity(),book=this.s.book,workbookKey=this.s.workbookKey,sheet=$('catalogueTargetSheet').value;
      this.pageController?.abort();this.detailController?.abort();++this.pageRequest;++this.detailRequest;
      this.busy=true;this.controls();this.status(mode==='confirm'?'正在确认表对象…':'正在读取名称与表定义…');
      const current=()=>request===this.request&&identity===this.identity()&&this.dialog.open;let owned='',adopted=false;
      try{
        const done=await this.jobs.execute({book,workbookKey,mode,...(mode==='confirm'?{sheet}:{})},{main:true});owned=done.job;
        if(!current()||!done.current())return;this.validSource(done.result);
		const controller=new AbortController();this.pageController=controller;
		const page=await this.readPage(done.job,0,controller.signal);
		if(!current()||controller.signal.aborted)return;
        const old=this.job;this.job=done.job;adopted=true;this.item=null;this.selected='';this.adoptPage(page,0);this.release(old);
		if(page.items.length)await this.select(page.items[0].id,false);
      }catch(error){if(current()&&error.name!=='AbortError')this.status(error.message,true);}
      finally{if(owned&&!adopted)this.release(owned);if(current()){this.busy=false;this.controls();}}
    }
    async loadPage(offset){
      if(!this.job||!this.dialog.open||this.busy)return;
      if(new TextEncoder().encode($('catalogueSearch').value).length>this.limits.searchBytes){this.status('搜索文字超过容量，请缩短后重试',true);return;}
      const serial=++this.pageRequest,identity=this.identity(),job=this.job;this.pageController?.abort();const controller=new AbortController();this.pageController=controller;
      ++this.detailRequest;this.detailController?.abort();this.item=null;this.selected='';$('catalogueDetail').textContent='选择条目查看原始定义和定位范围';$('catalogueList').replaceChildren(element('div','catalogue-empty','正在读取目录…'));
      try{
        const page=await this.readPage(job,offset,controller.signal);
        if(serial!==this.pageRequest||identity!==this.identity()||job!==this.job||!this.dialog.open)return;
        this.adoptPage(page,offset);
        if(page.items.length)await this.select(page.items[0].id,false);
      }catch(error){if(serial===this.pageRequest&&identity===this.identity()&&error.name!=='AbortError'){if(this.page)this.renderList();this.status(error.message,true);this.controls();}}
    }
	async readPage(job,offset,signal){
	  const scope=$('catalogueSheet').value;
	  const q=new URLSearchParams({id:job,offset:String(offset),q:$('catalogueSearch').value,kind:$('catalogueKind').value,scope:scope==='workbook:'?'workbook':'',sheet:scope.startsWith('sheet:')?scope.slice(6):''});
	  if(new TextEncoder().encode($('catalogueSearch').value).length>this.limits.searchBytes)throw Error('搜索文字超过容量，请缩短后重试');
	  const page=await this.api('/api/catalogue/items?'+q,{signal});this.validSource(page);
	  if(!Array.isArray(page.items)||page.items.length>this.limits.pageRows||!Number.isSafeInteger(page.next)||!Number.isSafeInteger(page.previous)||!Number.isSafeInteger(page.matched)||!Number.isSafeInteger(page.total)||page.offset!==offset||page.next!==offset+page.items.length||page.previous<0||page.previous>offset||page.next>page.matched&&page.items.length)throw Error('目录分页响应无效');
	  return page;
	}
	adoptPage(page,offset){this.page=page;this.offset=offset;this.renderList();this.controls();this.status((page.complete?'':'目录尚不完整。')+(page.notices||[]).join('；'));}
    renderList(){
      const list=$('catalogueList');list.replaceChildren();
      if(!this.page.items.length)list.append(element('div','catalogue-empty',this.page.total?'没有符合条件的名称或表对象':'没有已读取的名称或表对象。可在下方确认当前工作表。'));
      for(const item of this.page.items){
        const button=element('button','catalogue-item');button.type='button';button.dataset.id=item.id;button.setAttribute('role','option');button.setAttribute('aria-selected',String(this.selected===item.id));button.tabIndex=-1;
        const name=element('span','catalogue-item-name',item.name||'未能读取名称');name.title=item.shortened?'完整名称请查看条目详情':item.name;
        const meta=element('span','catalogue-item-meta');meta.append(element('span','catalogue-item-scope',(item.kind==='table'?'表对象':'名称')+' · '+(item.scope||item.sheet||(item.unknownScope?'作用域未能读取':'工作簿'))+(item.hidden?' · 隐藏':'')),element('span','catalogue-item-state',label(item)));
        button.append(name,meta);button.addEventListener('click',()=>this.select(item.id,true));button.addEventListener('dblclick',()=>this.locate());list.append(button);
      }
      $('catalogueCount').textContent=this.page.matched?`${this.offset+1}–${this.page.next} / ${this.page.matched}`:`0 / ${this.page.total}`;
    }
    async select(id,focus){
      this.selected=id;this.item=null;const serial=++this.detailRequest,identity=this.identity(),job=this.job;this.detailController?.abort();const controller=new AbortController();this.detailController=controller;
      for(const button of $('catalogueList').querySelectorAll('button[data-id]')){const chosen=button.dataset.id===id;button.setAttribute('aria-selected',String(chosen));button.tabIndex=chosen?0:-1;if(chosen&&focus)button.focus({preventScroll:true});}
      $('catalogueDetail').textContent='正在读取定义…';
      try{
        const data=await this.api('/api/catalogue/detail?'+new URLSearchParams({id:job,item:id}),{signal:controller.signal});
        if(serial!==this.detailRequest||identity!==this.identity()||job!==this.job||!this.dialog.open||this.selected!==id)return;
        this.validSource(data);if(data.item?.id!==id)throw Error('目录条目响应无效');this.item=data.item;this.columnOffset=0;
		if(data.item.kind==='table'&&(this.s.sheets||[]).some(sheet=>sheet.name===data.item.sheet))$('catalogueTargetSheet').value=data.item.sheet;
		this.renderDetail();this.controls();
      }catch(error){if(serial===this.detailRequest&&identity===this.identity()&&error.name!=='AbortError'){$('catalogueDetail').textContent=error.message;this.status(error.message,true);}}
    }
    renderDetail(){
      const item=this.item,body=$('catalogueDetail');body.replaceChildren();if(!item)return;
      body.append(element('h3','',item.name||'未能读取名称'),element('p','catalogue-reason',(item.kind==='table'?'表对象':'定义名称')+' · '+(item.scope||item.sheet||(item.scopeId?'作用域未能读取':'工作簿'))+(item.hidden?' · 隐藏':'')));
      body.append(element('h4','','原始定义'),element('pre','catalogue-definition',item.definition||'没有可读取的文本定义'));
      if(item.comment)body.append(element('h4','','注释'),element('p','',item.comment));body.append(element('p','catalogue-reason',item.reason||'已确认固定范围，可按源坐标定位。'));
      const areas=element('div','catalogue-areas'),pick=element('select','field-control');pick.id='catalogueArea';pick.setAttribute('aria-label','选择定位区域');
      for(const [index,area] of (item.areas||[]).entries())pick.append(new Option(`${area.sheet}!${area.start}${area.end===area.start?'':':'+area.end}`,String(index)));
      const go=element('button','primary-button compact','定位区域');go.id='catalogueLocate';go.type='button';go.disabled=item.status!=='ready'||!item.areas?.length;go.addEventListener('click',()=>this.locate());pick.disabled=go.disabled;areas.append(pick,go);body.append(areas);
      if(item.tokenHex){const details=element('details');details.append(element('summary','','原始公式标记'),element('pre','catalogue-definition',item.tokenHex));body.append(details);}
      if(item.table){body.append(element('h4','',`表头 ${item.table.headerRows} 行，合计 ${item.table.totalRows} 行，${item.table.columns?.length||0} 列`));this.renderColumns(body,item.table.columns||[]);}
    }
    renderColumns(body,columns){
      const size=this.limits.pageRows,table=element('table','catalogue-columns');table.setAttribute('aria-label','表对象列定义');
      const rows=element('tbody');for(const [index,column] of columns.slice(this.columnOffset,this.columnOffset+size).entries()){const tr=element('tr');tr.append(element('td','',String(this.columnOffset+index+1)),element('td','',column.name));rows.append(tr);}table.append(rows);body.append(table);
      if(columns.length>size){const paging=element('div','catalogue-paging'),prev=element('button','mini-button','上一页列'),next=element('button','mini-button','下一页列');prev.type=next.type='button';prev.disabled=this.columnOffset===0;next.disabled=this.columnOffset+size>=columns.length;prev.onclick=()=>{this.columnOffset=Math.max(0,this.columnOffset-size);this.renderDetail();};next.onclick=()=>{this.columnOffset+=size;this.renderDetail();};paging.append(prev,element('span','',`${this.columnOffset+1}–${Math.min(columns.length,this.columnOffset+size)} / ${columns.length}`),next);body.append(paging);}
    }
    key(event){
      if(!this.page?.items.length)return;const at=Math.max(0,this.page.items.findIndex(item=>item.id===this.selected));
      if(event.key==='ArrowDown'||event.key==='ArrowUp'){event.preventDefault();this.select(this.page.items[Math.max(0,Math.min(this.page.items.length-1,at+(event.key==='ArrowDown'?1:-1)))].id,true);}
      if(event.key==='Enter'){event.preventDefault();this.locate();}
    }
    cancelNavigation(){if(this.navigating){++this.nav;this.navigating=false;}}
    async openSheet(){const sheet=$('catalogueTargetSheet').value;if(sheet&&this.canNavigate())await this.go({sheet,start:'A1',end:'A1'},null);}
    async locate(){
      if(this.item?.id!==this.selected||this.item?.status!=='ready'||!this.canNavigate())return;
      const index=Number($('catalogueArea').value);if(!Number.isSafeInteger(index)||index<0||!this.item.areas?.[index])return;
      await this.go(this.item.areas[index],{id:this.job,item:this.item.id,area:String(index),workbookKey:this.s.workbookKey,book:this.s.book});
    }
    async go(area,resolve){
      const nav=++this.nav,book=this.s.book,key=this.s.workbookKey,origin=JSON.stringify([this.s.sheet,this.s.sheetEpoch,this.s.selected?.ref]);this.navigating=true;
      const current=()=>nav===this.nav&&book===this.s.book&&key===this.s.workbookKey;
      try{
        if(resolve){const result=await this.api('/api/catalogue/resolve?'+new URLSearchParams(resolve));if(!current()||origin!==JSON.stringify([this.s.sheet,this.s.sheetEpoch,this.s.selected?.ref]))return;this.validSource(result);area=result.area;}
        if(!current())return;this.close();const arrived=await this.navigateArea(area,current);
        if(current()&&arrived)this.toast('已定位：'+area.sheet+'!'+area.start+(area.end===area.start?'':':'+area.end),'success');
      }catch(error){if(current()){if(this.dialog.open)this.status(error.message,true);else this.toast(error.message,'warning');}}
      finally{if(current())this.navigating=false;}
    }
  }
  window.ExcelLensCatalogue=Catalogue;window.ExcelLensCatalogueModel={label};
})();
