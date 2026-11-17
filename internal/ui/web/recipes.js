/* Saved analysis plans contain only declared operations and field bindings.
 * Capture, preview and replay share the normal cancellable analysis jobs. */
(() => {
  'use strict';
  const $=id=>document.getElementById(id);
  const matchLabels={match:'已匹配',missing:'名称未匹配，请选择',manual:'需手动选择',ambiguous:'名称重复，请选择',uncertain:'读取类型不确定',untyped:'样本为空，请选择',type_mismatch:'读取类型不兼容',mixed:'混合类型，请核对后选择'};
  const compatible=(expected,actual)=>!!(expected&&actual&&expected.mask&&!expected.uncertain&&!actual.uncertain&&!(actual.mask&~expected.mask));
  function validateMapping(preview,mapping){
    if(!preview)return '请先读取字段';
    const used=new Set();
    for(const field of preview.recipe.fields){
      const target=preview.fields.find(item=>item.id===mapping[field.id]);
      if(!target)return '请逐项选择目标字段';
      if(used.has(target.id))return '每个目标字段只能使用一次';
      if(!compatible(field,target))return '目标字段的读取类型不兼容';
      used.add(target.id);
    }
    return '';
  }
  function summary(value){
    if(!value)return '';
    const steps=value.steps||[],group=steps.find(step=>step.kind==='group')?.group;
    return `${group?.layout==='tree'?'层级展开 · ':''}${steps.filter(step=>step.kind==='transform').length}项转换 · ${steps.filter(step=>step.kind==='filter').length}项筛选 · ${(group?.rows?.length||0)+(group?.columns?.length||0)}个维度 · ${group?.metrics?.length||1}个指标 · ${steps.find(step=>step.kind==='export')?.export?.format?.toUpperCase()||''}`;
  }
  class Recipes{
    constructor({api,context,capture,adopt,toast}){
      Object.assign(this,{api,context,capture,adopt,toast});this.dialog=$('analysisPlanDialog');this.epoch=0;this.serial=0;this.catalogSerial=0;this.catalogLoading=false;this.opened=false;this.busy=false;this.mode='';this.book='';this.store=null;this.preview=null;this.draft=null;
      const jobOptions={api,identity:()=>this.identity(),requestIdentity:()=>this.context().bookKey,sleep:ms=>new Promise(resolve=>setTimeout(resolve,ms)),maxPolls:0,onProgress:status=>this.status(`${status.stage||'正在分析'}${status.total?' · '+Number(status.progress||0).toLocaleString()+' / '+Number(status.total).toLocaleString()+'行':''}`)};
      this.captureJobs=new window.ExcelLensAnalysisJobs({...jobOptions,endpoint:'/api/recipes/capture',label:'方案字段核对'});
      this.runJobs=new window.ExcelLensAnalysisJobs({...jobOptions,endpoint:'/api/recipes/run',label:'方案分析',resultFromStatus:status=>status});
      $('analysisPlansOpen').addEventListener('click',()=>this.open());
      $('analysisPlanRefresh').addEventListener('click',()=>this.refresh());
      $('analysisPlanSave').addEventListener('click',()=>this.save());
      $('analysisPlanList').addEventListener('change',()=>this.select());
      $('analysisPlanDelete').addEventListener('click',()=>this.remove());
      $('analysisPlanPreview').addEventListener('click',()=>this.readPreview());
      $('analysisPlanRun').addEventListener('click',()=>this.run());
      $('analysisPlanCancel').addEventListener('click',()=>this.cancel());
      for(const id of ['analysisPlanTarget','analysisPlanHeader','analysisPlanScope'])$(id).addEventListener('change',()=>{this.clearPreview();this.targetControls();});
      this.dialog.addEventListener('close',()=>{if(!this.dialog.open)this.reset();});
    }
    identity(){const c=this.context();return JSON.stringify([c.bookKey,c.opened,c.intent,this.epoch,this.opened,!!this.dialog.open]);}
    current(serial,epoch,key){return this.opened&&this.dialog.open&&serial===this.serial&&epoch===this.epoch&&this.context().bookKey===key&&this.context().opened;}
    post(url,data,options={}){return this.api(url,{method:'POST',body:JSON.stringify(data),...options});}
    status(text,error=false){$('analysisPlanStatus').textContent=text;$('analysisPlanStatus').classList.toggle('is-error',error);}
    selected(){return this.store?.items?.find(item=>item.id===$('analysisPlanList').value);}
    mapping(){return Object.fromEntries([...$('analysisPlanBindings').querySelectorAll('select[data-field]')].map(select=>[select.dataset.field,select.value]));}
    controls(){
      const selected=!!this.selected(),c=this.context(),blocked=this.busy||this.catalogLoading;
      for(const id of ['analysisPlanRefresh','analysisPlanName','analysisPlanTarget','analysisPlanHeader','analysisPlanScope'])$(id).disabled=blocked;
      $('analysisPlanList').disabled=blocked||!this.store;
      $('analysisPlanSave').disabled=blocked||!this.store||!c.resultJob;
      $('analysisPlanDelete').disabled=blocked||!selected;
      $('analysisPlanPreview').disabled=blocked||!selected||!$('analysisPlanTarget').value;
      $('analysisPlanRun').disabled=blocked||!!validateMapping(this.preview,this.mapping());
      for(const select of $('analysisPlanBindings').querySelectorAll('select'))select.disabled=blocked;
      $('analysisPlanCancel').hidden=!this.busy||!['capture','preview','run'].includes(this.mode);
      this.targetControls();
    }
    targetControls(){
      const scratch=$('analysisPlanTarget').value.startsWith('scratch:'),c=this.context(),scope=$('analysisPlanScope');
      $('analysisPlanHeader').disabled=this.busy||this.catalogLoading||scratch;if(scratch)$('analysisPlanHeader').value='1';
      const current=!scratch&&c.source===$('analysisPlanTarget').value&&!!c.sourceId&&!!c.sourceScope;
      scope.querySelector('[value="current"]').disabled=!current;
      if(!current)scope.value='full';
    }
    stopCatalog(){++this.catalogSerial;this.catalogController?.abort();this.catalogController=null;this.catalogLoading=false;}
    clearPreview(){this.preview=null;$('analysisPlanBindings').replaceChildren();$('analysisPlanSample').textContent='';$('analysisPlanMapping').hidden=true;this.controls();}
    reset(){++this.epoch;++this.serial;this.stopCatalog();this.controller?.abort();this.controller=null;this.captureJobs.reset();this.runJobs.reset();this.opened=false;this.busy=false;this.mode='';this.draft=null;this.clearPreview();}
    close(){this.reset();if(this.dialog.open)this.dialog.close();}
    async open(){
      const c=this.context();if(!c.opened||!c.bookKey){this.toast('请先打开分析工作区','warning');return;}
      this.opened=true;this.store=null;this.book='';const target=$('analysisPlanTarget');target.replaceChildren();
      for(const source of c.sources)target.append(new Option(source.label,source.value));target.value=c.source;
      $('analysisPlanHeader').value=String(c.headerRow||1);
      $('analysisPlanScope').value='full';
      $('analysisPlanSaveHint').textContent=c.resultJob?'':'请先运行分析';
      if(!this.dialog.open)this.dialog.showModal();this.clearPreview();await this.refresh();
    }
    setCatalog(data,selected=''){
      this.book=data.book;this.store=data.store;const list=$('analysisPlanList'),previous=selected||list.value;list.replaceChildren();
      list.append(new Option(this.store.items.length?'选择分析方案':'尚无分析方案',''));
      for(const value of this.store.items)list.append(new Option(value.name,value.id));
      list.value=this.store.items.some(value=>value.id===previous)?previous:'';this.select();
    }
    select(){this.clearPreview();$('analysisPlanSummary').textContent=summary(this.selected());this.controls();}
    async refresh(){
      if(this.busy)return;const serial=++this.catalogSerial,epoch=this.epoch,key=this.context().bookKey;
      this.catalogController?.abort();const controller=new AbortController();this.catalogController=controller;
      this.catalogLoading=true;this.controls();
      try{const data=await this.api('/api/recipes',{signal:controller.signal});if(serial!==this.catalogSerial||epoch!==this.epoch||key!==this.context().bookKey||!this.opened)return;this.setCatalog(data);this.status(this.draft?'列表已刷新，可重试保存已核对的方案。':`${this.store.items.length}个方案`);}
      catch(error){if(serial===this.catalogSerial&&epoch===this.epoch&&this.opened&&error.name!=='AbortError'){this.store=null;this.status(error.message,true);this.controls();}}
      finally{if(this.catalogController===controller){this.catalogController=null;this.catalogLoading=false;this.controls();}}
    }
    async save(){
      if(this.busy||!this.store)return;
      const name=$('analysisPlanName').value.trim();if(!name||[...name].length>80||/[\r\n\0]/.test(name)){this.status('请输入1至80字的方案名称',true);$('analysisPlanName').focus();return;}
      let captured;try{captured=this.capture();}catch(error){this.status(error.message,true);return;}
      this.stopCatalog();const serial=++this.serial,epoch=this.epoch,key=this.context().bookKey,identity=this.identity(),current=()=>this.current(serial,epoch,key)&&identity===this.identity();
      this.busy=true;this.mode='capture';this.controls();
      try{
        if(!this.draft||this.draft.job!==captured.jobId||this.draft.format!==captured.format||this.draft.key!==key){
          this.status('正在核对完整输入的字段类型…');
          const completed=await this.captureJobs.execute({...captured,book:this.book,workbookKey:key},{main:true});
          if(!current()||!completed.current())return;
          this.draft={id:completed.result.captureId,job:captured.jobId,format:captured.format,key};
        }
        this.mode='commit';this.controls();this.status('正在保存分析方案…');
        const saved=await this.post('/api/recipes/capture/commit',{id:this.draft.id,name,book:this.book,workbookKey:key,expectedRevision:this.store.revision});
        if(!current())return;
        this.draft=null;this.store.revision=saved.revision;
        try{const data=await this.api('/api/recipes');if(current())this.setCatalog(data,saved.id);}catch{if(current())this.status('方案已保存；刷新列表后查看。');return;}
        if(current())this.status('分析方案已保存到本机');
      }catch(error){if(current()&&error.name!=='AbortError')this.status(error.message+(this.draft?'；可刷新列表或更名后重试。':''),true);}
      finally{if(this.current(serial,epoch,key)){this.busy=false;this.mode='';this.controls();}}
    }
    async remove(){
      const selected=this.selected();if(this.busy||!selected)return;
      this.stopCatalog();const serial=++this.serial,epoch=this.epoch,key=this.context().bookKey;this.busy=true;this.mode='delete';this.controls();
      try{const data=await this.post('/api/recipes',{action:'delete',id:selected.id,expectedRevision:this.store.revision});if(this.current(serial,epoch,key)){this.setCatalog(data);this.status('已删除所选分析方案');}}
      catch(error){if(this.current(serial,epoch,key))this.status(error.message,true);}
      finally{if(this.current(serial,epoch,key)){this.busy=false;this.mode='';this.controls();}}
    }
    target(){return {book:this.book,source:$('analysisPlanTarget').value,headerRow:Number($('analysisPlanHeader').value)};}
    async readPreview(){
      const selected=this.selected();if(this.busy||!selected)return;
      this.stopCatalog();const serial=++this.serial,epoch=this.epoch,key=this.context().bookKey,controller=new AbortController();this.controller=controller;
      this.busy=true;this.mode='preview';this.clearPreview();this.status('正在读取字段与类型样本…');
      try{const target=this.target();if(!Number.isInteger(target.headerRow)||target.headerRow<1)throw Error('表头行号须为正整数');
        const c=this.context(),sourceId=$('analysisPlanScope').value==='current'?c.sourceId||'':'';
        if($('analysisPlanScope').value==='current'&&(!sourceId||c.source!==target.source||!c.sourceScope))throw Error('当前分析范围已变化，请重新选择');
        const data=await this.post('/api/recipes/preview',{id:selected.id,book:this.book,workbookKey:key,target,...(sourceId?{sourceId}:{})},{signal:controller.signal});
        if(!this.current(serial,epoch,key))return;
        this.preview={...data,sourceId,ref:{...target,stamp:data.target.stamp}};this.renderPreview();this.status('核对字段映射后运行');
      }catch(error){if(this.current(serial,epoch,key)&&error.name!=='AbortError')this.status(error.message,true);}
      finally{if(this.current(serial,epoch,key)){this.busy=false;this.mode='';this.controls();}if(this.controller===controller)this.controller=null;}
    }
    renderPreview(){
      const preview=this.preview,host=$('analysisPlanBindings');host.replaceChildren();$('analysisPlanMapping').hidden=false;
      $('analysisPlanSample').textContent=`预览 ${preview.sampled} / ${Number(preview.target.total).toLocaleString()} 行${preview.fieldsTruncated?' · 仅前256列':''}`;
      if(!preview.recipe.fields.length){const note=document.createElement('p');note.textContent='此方案只统计记录数，无需绑定字段。';host.append(note);return;}
      for(const field of preview.recipe.fields){
        const match=preview.matches.find(item=>item.field===field.id),row=document.createElement('div');row.className='analysis-plan-binding';
        const label=document.createElement('label'),name=document.createElement('strong'),type=document.createElement('span');name.textContent=field.name+' · '+field.id;type.textContent=preview.expectedTypes[field.id];label.append(name,type);
        const select=document.createElement('select');select.className='field-control';select.dataset.field=field.id;select.id='analysisPlanField_'+field.id;label.htmlFor=select.id;
        select.append(new Option('选择目标字段',''));
        for(const target of preview.fields){const option=new Option(`${target.name} · ${target.id} · ${preview.targetTypes[target.id]}`,target.id);option.disabled=!compatible(field,target);select.append(option);}
        select.value=match?.status==='match'?match.target:'';
        const note=document.createElement('small');note.textContent=matchLabels[match?.status]||'需手动选择';
        select.addEventListener('change',()=>{note.textContent=select.value?'已手动选择，请核对读取类型':matchLabels[match?.status]||'需手动选择';this.controls();const error=validateMapping(preview,this.mapping());if(error)this.status(error,true);else this.status('字段映射已就绪');});
        const control=document.createElement('div');control.append(select,note);row.append(label,control);host.append(row);
      }
    }
    cancel(){
      if(!this.busy||!['capture','preview','run'].includes(this.mode))return;
      ++this.serial;this.controller?.abort();this.captureJobs.cancelMain();this.runJobs.cancelMain();this.busy=false;this.mode='';this.status('已请求取消，当前分析结果保留');this.controls();
    }
    async run(){
      if(this.busy||!this.preview)return;const mapping=this.mapping(),error=validateMapping(this.preview,mapping);if(error){this.status(error,true);return;}
      this.stopCatalog();const serial=++this.serial,epoch=this.epoch,key=this.context().bookKey,identity=this.identity(),current=()=>this.current(serial,epoch,key)&&identity===this.identity();
      const preview=this.preview;this.busy=true;this.mode='run';this.controls();this.status('正在核对来源并运行分析…');
      try{
        const completed=await this.runJobs.execute({id:preview.recipe.id,digest:preview.digest,book:this.book,workbookKey:key,target:preview.ref,stamp:preview.stamp,mapping,...(preview.sourceId?{sourceId:preview.sourceId}:{})},{main:true});
        if(!current()||!completed.current())return;
        this.mode='adopt';this.controls();
        if(!await this.adopt(completed,preview,current))return;
        if(this.current(serial,epoch,key)){this.toast('已运行分析方案：'+preview.recipe.name,'success');this.dialog.close();}
      }catch(error){if(this.current(serial,epoch,key)&&error.name!=='AbortError')this.status(error.message,true);}
      finally{if(this.current(serial,epoch,key)){this.busy=false;this.mode='';this.controls();}}
    }
  }
  window.ExcelLensRecipes=Recipes;window.ExcelLensRecipesModel={compatible,validateMapping,summary};
})();
