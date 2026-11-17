(() => {
  'use strict';
  const $=id=>document.getElementById(id),make=(tag,cls='',text='')=>{const n=document.createElement(tag);n.className=cls;n.textContent=text;return n;};
  const types={none:'任意值',whole:'整数',decimal:'小数',list:'列表',date:'日期',time:'时间',textLength:'文本长度',custom:'自定义公式'};
  const operators={between:'介于',notBetween:'不介于',equal:'等于',notEqual:'不等于',greaterThan:'大于',lessThan:'小于',greaterThanOrEqual:'大于或等于',lessThanOrEqual:'小于或等于'};
  const states={described:'已读取定义',unresolved:'未完整解释',invalid:'定义需核对'},styles={stop:'停止',warning:'警告',information:'信息'};
  const label=(dictionary,value,fallback=value)=>Object.hasOwn(dictionary,value)?dictionary[value]:fallback;
  const integer=n=>Number.isSafeInteger(n)&&n>=0;
  class DataRules {
    constructor({state,api,toast,supported,navigate,limits,canNavigate=()=>true,sleep=ms=>new Promise(resolve=>setTimeout(resolve,ms))}){
      Object.assign(this,{s:state,api,toast,supported,navigateArea:navigate,limits,canNavigate});
      if(!Number.isSafeInteger(limits?.pageRows)||limits.pageRows<1)throw Error('数据规则分页容量无效');
      this.epoch=0;this.request=0;this.pageRequest=0;this.detailRequest=0;this.nav=0;this.job='';this.sheet='';this.ref='';this.page=null;this.detail=null;this.selected=0;this.busy=false;this.loading=false;
      this.dialog=this.create();
      this.jobs=new window.ExcelLensAnalysisJobs({api,identity:()=>this.identity(),requestIdentity:()=>this.s.workbookKey,sleep,endpoint:'/api/data-rules',label:'数据规则',maxPolls:0,resultFromStatus:status=>status,onProgress:status=>{this.validSource(status);this.status((status.stage||'正在读取数据规则')+(status.totalBytes>0?` · ${Math.min(100,Math.round(status.doneBytes/status.totalBytes*100))}%`:''));}});
      $('dataRulesOpenBtn').addEventListener('click',()=>this.open());$('dataRuleClose').addEventListener('click',()=>this.close());
      this.dialog.addEventListener('cancel',event=>{event.preventDefault();this.close();});
      this.dialog.addEventListener('close',()=>{if(!this.dialog.open)this.stop();});
      $('dataRuleRefresh').addEventListener('click',()=>this.refresh());$('dataRuleCancel').addEventListener('click',()=>this.cancel());
      for(const id of ['dataRuleScope','dataRuleType','dataRuleState'])$(id).addEventListener('change',()=>this.loadPage(0));
      $('dataRulePrev').addEventListener('click',()=>this.loadPage(this.page?.page.previous||0));$('dataRuleNext').addEventListener('click',()=>this.loadPage(this.page?.page.next||0));
      $('dataRuleList').addEventListener('keydown',event=>this.key(event));
      document.addEventListener('pointerdown',()=>this.cancelNavigation(),true);document.addEventListener('keydown',()=>this.cancelNavigation(),true);
      this.controls();
    }
    create(){
      const d=make('dialog','float-dialog data-rules-dialog');d.id='dataRuleDialog';d.setAttribute('aria-labelledby','dataRuleTitle');
      // Workbook content is inserted exclusively as text, never as markup.
      d.innerHTML='<div class="dialog-head"><div id="dataRuleTitle" class="dialog-title">数据规则</div><button id="dataRuleClose" class="dialog-close" type="button" aria-label="关闭数据规则">×</button></div><p id="dataRuleSource" class="data-rule-source"></p><div class="data-rule-tools"><select id="dataRuleScope" class="field-control" aria-label="规则范围"><option value="cell">当前单元格</option><option value="sheet">本表全部规则</option></select><select id="dataRuleType" class="field-control" aria-label="规则类型"><option value="">全部类型</option></select><select id="dataRuleState" class="field-control" aria-label="规则定义状态"><option value="">全部状态</option><option value="issues">需核对的定义</option><option value="described">已读取的定义</option></select><button id="dataRuleRefresh" class="mini-button" type="button">重新读取</button></div><p id="dataRuleSummary" class="data-rule-summary" role="status"></p><div class="data-rule-main"><div class="data-rule-master"><div id="dataRuleList" class="data-rule-list" role="listbox" aria-label="保存的数据规则" tabindex="0"></div><div class="data-rule-paging"><button id="dataRulePrev" class="mini-button" type="button">上一页</button><span id="dataRuleCount"></span><button id="dataRuleNext" class="mini-button" type="button">下一页</button></div></div><section id="dataRuleDetail" class="data-rule-detail" aria-label="规则定义详情"></section></div><div class="data-rule-footer"><p id="dataRuleStatus" role="status" aria-live="polite"></p><button id="dataRuleCancel" class="mini-button" type="button" hidden>取消读取</button><span class="data-rule-readonly">仅查看保存的规则，未校验数据</span></div>';
      document.body.append(d);for(const [value,text] of Object.entries(types))$('dataRuleType').append(new Option(text,value));return d;
    }
    identity(){return JSON.stringify([this.s.workbookKey,this.s.book,this.s.sheet,this.s.sheetEpoch,this.epoch]);}
    validSource(value){if(value?.workbookKey!==this.s.workbookKey||value?.book!==this.s.book||value?.sheet!==this.sheet||this.sheet!==this.s.sheet)throw Error('数据规则来源已切换，请重新读取');}
    release(id){if(id)this.api('/api/data-rules/clear',{method:'POST',body:JSON.stringify({id})}).catch(()=>{});}
    status(text,error=false){$('dataRuleStatus').textContent=text||'';$('dataRuleStatus').classList.toggle('is-error',error);$('dataRuleStatus').setAttribute('role',error?'alert':'status');}
    controls(){
      const enabled=!!this.s.opened&&!!this.s.sheet&&!!this.s.indexReady&&!this.s.opening&&!this.s.sheetLoadingEpoch&&!!this.supported(),pending=this.busy||this.loading;
      $('dataRulesOpenBtn').disabled=!enabled;if($('menuDataRules'))$('menuDataRules').disabled=!enabled;
      $('dataRuleRefresh').disabled=!enabled||this.busy;$('dataRuleCancel').hidden=!pending;
      for(const id of ['dataRuleScope','dataRuleType','dataRuleState'])$(id).disabled=pending;
      $('dataRulePrev').disabled=pending||!this.page||this.page.page.offset===0;$('dataRuleNext').disabled=pending||!this.page||this.page.page.next>=this.page.page.matched;
      for(const button of $('dataRuleList').querySelectorAll('button[data-id]'))button.disabled=pending;
      const locate=$('dataRuleLocate');if(locate)locate.disabled=pending||!this.detail?.item.ranges?.length||!this.canNavigate();
    }
    async open(){
      if(!this.s.opened||!this.supported()||!this.s.sheet||!this.s.indexReady||this.s.opening||this.s.sheetLoadingEpoch){this.toast('请先打开并完成支持数据规则的工作表加载','warning');return;}
      const source=JSON.stringify([this.s.workbookKey,this.s.book,this.s.sheet]);if(this.boundSource&&this.boundSource!==source)this.resetBook();this.boundSource=source;this.sheet=this.s.sheet;this.ref=this.s.selected?.ref||'A1';
      $('dataRuleSource').textContent=`${this.s.fileName||'当前工作簿'} / ${this.sheet}`;
      $('dataRuleScope').options[0].textContent=`当前单元格 ${this.ref}`;
      if(!this.dialog.open)this.dialog.showModal();this.controls();
      if(this.job&&!this.busy)await this.loadPage(0);else if(!this.busy)await this.refresh();
      if(this.dialog.open)$('dataRuleScope').focus({preventScroll:true});
    }
    stop(){++this.epoch;++this.request;++this.pageRequest;++this.detailRequest;this.jobs.reset();this.pageController?.abort();this.detailController?.abort();this.busy=false;this.loading=false;this.controls();}
    close(){this.stop();if(this.dialog.open)this.dialog.close();}
    resetBook(){this.cancelNavigation();this.stop();this.release(this.job);this.job='';this.sheet='';this.ref='';this.boundSource='';this.page=this.detail=null;this.selected=0;$('dataRuleList').replaceChildren();$('dataRuleDetail').replaceChildren();$('dataRuleSummary').textContent='';$('dataRuleScope').value='cell';$('dataRuleType').value='';$('dataRuleState').value='';this.status('');if(this.dialog.open)this.dialog.close();this.controls();}
    cancel(){this.stop();this.status('已取消读取，原规则结果保留');}
    async refresh(){
      if(this.busy)return;const request=++this.request,identity=this.identity();this.pageController?.abort();this.detailController?.abort();++this.pageRequest;++this.detailRequest;
      this.busy=true;this.loading=false;this.controls();this.status('正在读取保存的数据规则…');
      const current=()=>request===this.request&&identity===this.identity()&&this.dialog.open;let owned='',adopted=false;
      try{
        const done=await this.jobs.execute({workbookKey:this.s.workbookKey,book:this.s.book,sheet:this.sheet},{main:true});owned=done.job;
        if(!current()||!done.current())return;
        const controller=new AbortController();this.pageController=controller;const page=await this.readPage(done.job,0,controller.signal);
        if(!current()||controller.signal.aborted)return;
        const old=this.job;this.job=done.job;adopted=true;this.detail=null;this.selected=0;this.adoptPage(page);this.release(old);
        if(page.page.items.length)await this.select(page.page.items[0].id);
      }catch(error){if(current()&&error.name!=='AbortError')this.status(error.message,true);}
      finally{if(owned&&!adopted)this.release(owned);if(current()){this.busy=false;this.controls();}}
    }
    async readPage(job,offset,signal){
      const ref=$('dataRuleScope').value==='cell'?this.ref:'',q=new URLSearchParams({id:job,offset:String(offset),ref,type:$('dataRuleType').value,status:$('dataRuleState').value});
      const value=await this.api('/api/data-rules/items?'+q,{signal});this.validSource(value);const p=value.page;
      if(!p||!Array.isArray(p.items)||p.items.length>this.limits.pageRows||![p.offset,p.previous,p.next,p.matched,p.total].every(integer)||p.offset!==offset||p.previous!==Math.max(0,offset-this.limits.pageRows)||p.next!==offset+p.items.length||p.next>p.matched||p.matched>p.total||offset>p.matched||typeof p.scopeComplete!=='boolean'||value.ref!==ref||!value.summary||!integer(value.summary.rules))throw Error('数据规则分页响应无效');
      const ids=new Set();for(const item of p.items){if(!Number.isSafeInteger(item.id)||item.id<1||item.id>p.total||ids.has(item.id)||typeof item.sqref!=='string'||!integer(item.rangeCount)||!integer(item.listItems)||!Object.hasOwn(states,item.status))throw Error('数据规则列表条目无效');ids.add(item.id);}
      return value;
    }
    async loadPage(offset){
      if(!this.job||!this.dialog.open||this.busy)return;const serial=++this.pageRequest,identity=this.identity(),job=this.job;this.pageController?.abort();this.detailController?.abort();++this.detailRequest;
      const controller=new AbortController();this.pageController=controller;this.loading=true;this.detail=null;this.selected=0;this.controls();
      try{
        const value=await this.readPage(job,offset,controller.signal);
        if(serial!==this.pageRequest||identity!==this.identity()||job!==this.job||!this.dialog.open)return;
        this.adoptPage(value);if(value.page.items.length)await this.select(value.page.items[0].id);
      }catch(error){if(serial===this.pageRequest&&identity===this.identity()&&error.name!=='AbortError'){this.status(error.message+'；列表未更新，请重试',true);$('dataRuleDetail').textContent='规则列表读取失败，请重试';}}
      finally{if(serial===this.pageRequest&&identity===this.identity()){this.loading=false;this.controls();}}
    }
    adoptPage(value){
      this.page=value;this.renderList();const summary=value.summary;
      $('dataRuleSummary').textContent=`本表 ${summary.rules} 条规则`+(summary.unresolved?`，${summary.unresolved} 条定义需核对`:'')+(summary.promptSettingsKnown===false?'；不同规则容器的提示开关不一致':summary.disablePrompts?'；本表全局关闭输入提示':'')+(summary.inputWindowClosed?'；源提示窗口已关闭':'');
      this.status((value.page.scopeComplete?'':'存在未完整读取或范围未知的规则。')+(summary.notices||[]).join('；'));
      $('dataRuleDetail').textContent='选择规则查看保存的条件、提示与列表项';this.controls();
    }
    renderList(){
      const list=$('dataRuleList'),p=this.page.page;list.replaceChildren();
      if(!p.items.length){const message=!p.scopeComplete?'未找到已识别的规则；仍有未读取或范围未知的定义。':p.total?'没有符合条件的规则。':this.page.ref?`${this.ref} 没有保存的数据规则。`:'本表没有保存的数据规则。';list.append(make('p','data-rule-empty',message));}
      for(const item of p.items){const button=make('button','data-rule-item');button.type='button';button.id='dataRuleItem'+item.id;button.dataset.id=String(item.id);button.tabIndex=-1;button.setAttribute('role','option');button.setAttribute('aria-selected',String(this.selected===item.id));button.append(make('span','data-rule-item-title',`#${item.id} ${label(types,item.type,'未知类型：'+item.type)}`),make('span','data-rule-item-range',item.sqref||'适用范围未知'),make('span','data-rule-item-state',states[item.status]));button.addEventListener('click',()=>this.select(item.id,true));list.append(button);}
      if(this.selected)list.setAttribute('aria-activedescendant','dataRuleItem'+this.selected);else list.removeAttribute('aria-activedescendant');
      $('dataRuleCount').textContent=p.matched?`${p.offset+1}–${p.next} / ${p.matched}`:'0 条';
    }
    async select(id,focus=false,rangeOffset=0,listOffset=0,focusID=''){
      if(!this.page?.page.items.some(item=>item.id===id))return;const serial=++this.detailRequest,identity=this.identity(),job=this.job;this.detailController?.abort();const controller=new AbortController();this.detailController=controller;
      this.selected=id;this.detail=null;this.renderList();if(focus)$('dataRuleList').focus({preventScroll:true});$('dataRuleDetail').textContent='正在读取规则定义…';this.controls();
      try{
        const value=await this.api('/api/data-rules/detail?'+new URLSearchParams({id:job,item:String(id),rangeOffset:String(rangeOffset),listOffset:String(listOffset)}),{signal:controller.signal});
        if(serial!==this.detailRequest||identity!==this.identity()||job!==this.job||!this.dialog.open)return;
        this.validSource(value);const d=value.detail,ranges=d?.item?.ranges||[],items=d?.item?.inlineList||[];
        if(!d||d.item?.id!==id||!Array.isArray(ranges)||!Array.isArray(items)||![d.rangeCount,d.rangeOffset,d.rangePrevious,d.rangeNext,d.listCount,d.listOffset,d.listPrevious,d.listNext].every(integer)||d.rangeOffset!==rangeOffset||d.listOffset!==listOffset||d.rangeNext!==rangeOffset+ranges.length||d.listNext!==listOffset+items.length||ranges.length>this.limits.pageRows||items.length>this.limits.pageRows||d.rangeNext>d.rangeCount||d.listNext>d.listCount||d.rangePrevious!==Math.max(0,rangeOffset-this.limits.pageRows)||d.listPrevious!==Math.max(0,listOffset-this.limits.pageRows))throw Error('数据规则详情响应无效');
        this.detail=d;this.renderDetail();this.controls();if(focusID)$(focusID)?.focus({preventScroll:true});
      }catch(error){if(serial===this.detailRequest&&identity===this.identity()&&error.name!=='AbortError'){this.status(error.message,true);$('dataRuleDetail').textContent='定义未读取完成，请重新选择或重试';}}
    }
    field(body,label,text,pre=false){body.append(make('h4','',label),make(pre?'pre':'p',pre?'data-rule-definition':'',text));}
    pager(body,kind,offset,next,total){
      if(total<=this.limits.pageRows)return;const row=make('div','data-rule-paging'),prev=make('button','mini-button','上一页'),more=make('button','mini-button','下一页');prev.type=more.type='button';prev.id='dataRule'+kind+'Prev';more.id='dataRule'+kind+'Next';prev.disabled=offset===0;more.disabled=next>=total;
      const go=(at,focusID)=>this.select(this.selected,false,kind==='Range'?at:this.detail.rangeOffset,kind==='List'?at:this.detail.listOffset,focusID);
      prev.addEventListener('click',()=>go(Math.max(0,offset-this.limits.pageRows),prev.id));more.addEventListener('click',()=>go(next,more.id));row.append(prev,make('span','',`${offset+1}–${next} / ${total}`),more);body.append(row);
    }
    renderDetail(){
      const d=this.detail,item=d.item,body=$('dataRuleDetail');body.replaceChildren(make('h3','',`#${item.id} ${label(types,item.type)}`),make('p','data-rule-definition-state',label(states,item.status)));
      if(item.reason)body.append(make('p','data-rule-reason',item.reason));
      if(item.type==='list'&&item.listSource==='inline'){
        body.append(make('h4','',`保存的列表项（${d.listCount}）`));const list=make('ol','data-rule-values');list.id='dataRuleValues';list.start=d.listOffset+1;
        for(const text of item.inlineList||[]){const n=make('li','',text===''?'（空白项）':text);n.dataset.empty=String(text==='');list.append(n);}body.append(list);this.pager(body,'List',d.listOffset,d.listNext,d.listCount);
      }
      if(item.type!=='none'&&item.type!=='list'&&item.type!=='custom')this.field(body,'条件',label(operators,item.operator));
      if(item.formula1!==undefined&&item.formula1!==''&&!(item.type==='list'&&item.listSource==='inline'))this.field(body,'条件定义 1',item.formula1,true);
      if(item.formula2!==undefined&&item.formula2!=='')this.field(body,'条件定义 2',item.formula2,true);
      const flag=value=>item.status==='invalid'?'未确认':value?'是':'否';
      this.field(body,'保存的设置',`允许空白：${flag(item.allowBlank)}`+(item.type==='list'?`；隐藏下拉箭头：${flag(item.suppressDropDown)}`:'')+`；显示输入提示：${flag(item.showInputMessage)}；显示错误提示：${flag(item.showErrorMessage)}`);
      if(item.promptTitle||item.prompt)this.field(body,'输入提示'+(item.promptTitle?' · '+item.promptTitle:''),item.prompt||'（空白）');
      if(item.errorTitle||item.error||item.showErrorMessage)this.field(body,'错误提示 · '+label(styles,item.errorStyle)+(item.errorTitle?' · '+item.errorTitle:''),item.error||'（未保存提示文字）');
      body.append(make('h4','',`适用源区域（${d.rangeCount}）`));
      const ranges=make('div','data-rule-areas'),select=make('select','field-control');select.id='dataRuleArea';select.setAttribute('aria-label','选择数据规则源区域');
      for(const [at,area] of (item.ranges||[]).entries())select.append(new Option(this.areaText(area),String(d.rangeOffset+at)));
      const go=make('button','primary-button compact','定位区域');go.type='button';go.id='dataRuleLocate';go.addEventListener('click',()=>this.locate());select.disabled=!(item.ranges||[]).length;ranges.append(select,go);body.append(ranges);if(!d.rangeCount)body.append(make('p','data-rule-reason','源范围未确认，不能定位。'));
      this.pager(body,'Range',d.rangeOffset,d.rangeNext,d.rangeCount);
      if(item.type==='list'&&item.listSource!=='inline'){
        body.append(make('h4','',`保存的列表项（${d.listCount}）`));
        body.append(make('p','data-rule-reason','列表来自引用或未确认的定义，尚未读取候选值。'));
      }
      const raw=make('details','data-rule-raw');raw.append(make('summary','','原始定义'));
      if(item.type==='list'&&item.listSource==='inline'&&item.formula1)this.field(raw,'列表源表达式',item.formula1,true);
      for(const [label,text] of [['源区域文字',item.sqref],['源命名空间',item.namespace],['原始 XML',item.rawXml],['BIFF 记录',item.recordHex],['条件 1 原始令牌',item.formula1Tokens],['条件 2 原始令牌',item.formula2Tokens],['扩展列表定义',item.listDefinition]])if(text)this.field(raw,label,text,true);
      if(item.attributes)this.field(raw,'原始属性',JSON.stringify(item.attributes,null,2),true);
      for(const [at,text] of (item.continuationHex||[]).entries())this.field(raw,`续记录 ${at+1}`,text,true);body.append(raw);
    }
    areaText(area){const column=n=>{let text='';for(;n>0;n=Math.floor((n-1)/26))text=String.fromCharCode(65+(n-1)%26)+text;return text;};const first=column(area.col)+area.row,last=column(area.endCol)+area.endRow;return first===last?first:first+':'+last;}
    key(event){
      const items=this.page?.page.items;if(!items?.length)return;let at=Math.max(0,items.findIndex(item=>item.id===this.selected));
      if(['ArrowDown','ArrowUp','Home','End'].includes(event.key)){event.preventDefault();at=event.key==='Home'?0:event.key==='End'?items.length-1:Math.max(0,Math.min(items.length-1,at+(event.key==='ArrowDown'?1:-1)));this.select(items[at].id,true);}
      if(event.key==='Enter'){event.preventDefault();this.locate();}
    }
    cancelNavigation(){if(this.navigating){++this.nav;this.navigating=false;}}
    async locate(){
      if(this.busy||this.loading||!this.detail?.item.ranges?.length||this.detail.item.id!==this.selected||!this.canNavigate())return;
      const area=Number($('dataRuleArea').value);if(!Number.isSafeInteger(area)||area<this.detail.rangeOffset||area>=this.detail.rangeNext)return;
      const nav=++this.nav,identity=this.identity(),book=this.s.book,key=this.s.workbookKey,sheet=this.sheet,origin=JSON.stringify([this.s.sheetEpoch,this.s.selected?.ref]);this.navigating=true;
      const resolving=()=>nav===this.nav&&identity===this.identity()&&this.dialog.open;
      const arriving=()=>nav===this.nav&&book===this.s.book&&key===this.s.workbookKey&&sheet===this.s.sheet;
      try{
        const value=await this.api('/api/data-rules/resolve?'+new URLSearchParams({id:this.job,item:String(this.selected),area:String(area),workbookKey:key,book}));
        if(!resolving()||origin!==JSON.stringify([this.s.sheetEpoch,this.s.selected?.ref]))return;this.validSource(value);this.close();
        if(await this.navigateArea(value.area,arriving)&&arriving())this.toast('已定位：'+value.area.sheet+'!'+value.area.start+(value.area.end===value.area.start?'':':'+value.area.end),'success');
      }catch(error){if(arriving()){if(this.dialog.open)this.status(error.message,true);else this.toast(error.message,'warning');}}
      finally{if(nav===this.nav)this.navigating=false;}
    }
  }
  window.ExcelLensDataRules=DataRules;
})();
