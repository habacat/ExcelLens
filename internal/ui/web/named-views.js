(() => {
  'use strict';
  const $=id=>document.getElementById(id),copy=value=>JSON.parse(JSON.stringify(value));
  const active=query=>!!(query.rules?.length||query.sortKeys?.length||query.sortColumn);
  function summary(view){const q=view.query,c=view.config,parts=[view.source.sheet],sort=q.sortKeys?.length||(q.sortColumn?1:0);if(q.rules?.length)parts.push(q.rules.length+'项筛选');if(sort)parts.push(sort+'级排序');if(c.freeze.rows||c.freeze.cols)parts.push(`冻结${c.freeze.rows}行${c.freeze.cols}列`);if(c.zoom!==100)parts.push(c.zoom+'%');return parts.join(' · ');}
  class NamedViews{
    constructor({state,api,toast,capture,apply,canApply=()=>true,supportsRowVisibility=()=>false}){
      Object.assign(this,{s:state,api,toast,capture,applyView:apply,canApply,supportsRowVisibility});this.dialog=$('namedViewDialog');this.epoch=0;this.serial=0;this.catalogSerial=0;this.busy=false;this.mode='';this.controller=null;this.catalogController=null;this.book='';this.store=null;
      this.jobs=new window.ExcelLensAnalysisJobs({api,identity:()=>this.identity(),requestIdentity:()=>this.s.workbookKey,sleep:ms=>new Promise(resolve=>setTimeout(resolve,ms)),endpoint:'/api/filter',label:'视图查询',maxPolls:0,resultFromStatus:status=>status,onProgress:status=>this.status(`${status.stage||'正在恢复视图'}${status.total?' · '+Number(status.scanned||0).toLocaleString()+' / '+Number(status.total).toLocaleString()+'行':''}`)});
      $('namedViewOpenBtn').addEventListener('click',()=>this.open());$('namedViewRefresh').addEventListener('click',()=>this.refresh());$('namedViewList').addEventListener('change',()=>this.select());
      $('namedViewSave').addEventListener('click',()=>this.save(false));$('namedViewUpdate').addEventListener('click',()=>this.save(true));$('namedViewDelete').addEventListener('click',()=>this.remove());$('namedViewApply').addEventListener('click',()=>this.apply());$('namedViewCancel').addEventListener('click',()=>this.cancel());
    }
    identity(){return JSON.stringify([this.s.workbookKey,this.s.sheet,this.s.sheetEpoch]);}
    post(url,data,options={}){return this.api(url,{method:'POST',body:JSON.stringify(data),...options});}
    selected(){return this.store?.items?.find(item=>item.id===$('namedViewList').value);}
    status(text,error=false){$('namedViewStatus').textContent=text;$('namedViewStatus').classList.toggle('is-error',error);}
    controls(){const selected=!!this.selected();$('namedViewRefresh').disabled=this.busy;for(const id of ['namedViewList','namedViewName','namedViewSave'])$(id).disabled=this.busy||!this.store;for(const id of ['namedViewApply','namedViewUpdate','namedViewDelete'])$(id).disabled=this.busy||!selected;$('namedViewCancel').hidden=!(this.busy&&this.mode==='apply');}
    stopCatalog(){++this.catalogSerial;this.catalogController?.abort();this.catalogController=null;}
    resetBook(){++this.epoch;++this.serial;++this.catalogSerial;this.jobs.reset();this.controller?.abort();this.catalogController?.abort();this.controller=this.catalogController=null;this.book='';this.store=null;this.busy=false;this.mode='';$('namedViewList').replaceChildren();$('namedViewName').value='';$('namedViewSummary').textContent='';this.status('');this.dialog.close();this.controls();}
    async open(){if(!this.s.opened||!this.s.indexReady){this.toast('请先打开工作表','warning');return;}if(!this.dialog.open)this.dialog.showModal();await this.refresh();}
    setCatalog(data,selected=''){
      this.book=data.book;this.store=data.store;const list=$('namedViewList'),previous=selected||list.value;list.replaceChildren();
      const empty=new Option(this.store.items.length?'选择已保存视图':'尚无已保存视图','');list.append(empty);
      for(const view of this.store.items)list.append(new Option(view.name+' · '+view.source.sheet,view.id));
      list.value=this.store.items.some(item=>item.id===previous)?previous:'';this.select(false);this.controls();
    }
    async refresh(){
      if(this.busy)return;const serial=++this.catalogSerial,epoch=this.epoch,book=this.s.workbookKey;this.catalogController?.abort();const controller=new AbortController();this.catalogController=controller;
      try{const data=await this.api('/api/named-views',{signal:controller.signal});if(serial!==this.catalogSerial||epoch!==this.epoch||book!==this.s.workbookKey)return;this.setCatalog(data);this.status(`${this.store.items.length} 个视图`);}
      catch(error){if(serial===this.catalogSerial&&epoch===this.epoch&&error.name!=='AbortError'){this.store=null;this.status(error.message,true);this.controls();}}
      finally{if(this.catalogController===controller)this.catalogController=null;}
    }
    select(fillName=true){const view=this.selected();if(view&&fillName)$('namedViewName').value=view.name;$('namedViewSummary').textContent=view?summary(view):'';this.controls();}
    current(serial,epoch,book){return serial===this.serial&&epoch===this.epoch&&book===this.s.workbookKey;}
    async save(update){
      if(this.busy||!this.store)return;const selected=this.selected();if(update&&!selected)return;
      let captured;try{captured=this.capture();}catch(error){this.toast(error.message,'warning');return;}
      const name=$('namedViewName').value.trim();if(!name){this.toast('请输入视图名称','warning');$('namedViewName').focus();return;}
      this.stopCatalog();const serial=++this.serial,epoch=this.epoch,book=this.s.workbookKey;this.busy=true;this.mode='save';this.controls();
      try{const request={action:'save',id:update?selected.id:'',name,book:this.book,workbookKey:book,expectedRevision:this.store.revision,...copy(captured)};
        if(new TextEncoder().encode(JSON.stringify(request)).length>256*1024+8192)throw Error('视图配置过大，请减少尺寸覆盖或筛选值');
        const data=await this.post('/api/named-views',request);if(!this.current(serial,epoch,book))return;this.setCatalog(data,data.id);this.status('已保存到本机');
      }catch(error){if(this.current(serial,epoch,book))this.status(error.message,true);}
      finally{if(this.current(serial,epoch,book)){this.busy=false;this.mode='';this.controls();}}
    }
    async remove(){
      const selected=this.selected();if(this.busy||!selected)return;this.stopCatalog();const serial=++this.serial,epoch=this.epoch,book=this.s.workbookKey;this.busy=true;this.mode='delete';this.controls();
      try{const data=await this.post('/api/named-views',{action:'delete',id:selected.id,book:this.book,workbookKey:book,expectedRevision:this.store.revision});if(!this.current(serial,epoch,book))return;this.setCatalog(data);this.status('已删除所选视图');}
      catch(error){if(this.current(serial,epoch,book))this.status(error.message,true);}
      finally{if(this.current(serial,epoch,book)){this.busy=false;this.mode='';this.controls();}}
    }
    cancel(){if(this.mode!=='apply')return;++this.serial;this.controller?.abort();this.jobs.cancelMain();this.busy=false;this.mode='';this.status('已请求取消应用，当前视图保留');this.controls();}
    async apply(){
      const selected=this.selected();if(this.busy||!selected||!this.canApply())return;
      this.stopCatalog();
      const serial=++this.serial,epoch=this.epoch,book=this.s.workbookKey,identity=this.identity(),controller=new AbortController();this.controller=controller;this.busy=true;this.mode='apply';this.controls();this.status('正在核对来源与字段…');
      const current=()=>this.current(serial,epoch,book),unchanged=()=>current()&&identity===this.identity();let owned='',adopted=false;
      try{
        const prepared=await this.post('/api/named-views/resolve',{id:selected.id,book:this.book,workbookKey:book},{signal:controller.signal});if(!unchanged())return;
        const version=prepared.view.ruleVersion??1;
        if(version!==1&&!(version===2&&this.supportsRowVisibility?.()))throw Error('此视图版本或行显隐设置尚不支持，未应用');
        let data=null;
        if(active(prepared.view.query)){const completed=await this.jobs.execute({...copy(prepared.view.query),sheet:prepared.view.source.sheet,book:this.book,workbookKey:book},{main:true});owned=completed.job;if(!unchanged()||!completed.current())return;data=completed.result;}
        if(!unchanged())return;
        this.mode='commit';this.controls();this.status('正在显示已核对的视图…');
        adopted=await this.applyView(prepared,data,current);if(!adopted)return;
        if(current()){this.status('已应用「'+prepared.view.name+'」');this.dialog.close();this.toast('已应用视图：'+prepared.view.name,'success');}
      }catch(error){if(current()&&error.name!=='AbortError')this.status(error.message,true);}
      finally{
        if(owned&&!adopted)this.post('/api/filter/cancel',{id:owned}).catch(()=>{});
        if(current()){this.busy=false;this.mode='';this.controls();}if(this.controller===controller)this.controller=null;
      }
    }
  }
  window.ExcelLensNamedViews=NamedViews;window.ExcelLensNamedViewsModel={summary,active};
})();
