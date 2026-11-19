(() => {
  'use strict';
  const $=id=>document.getElementById(id);
  const interrupted=()=>Object.assign(Error('疑似重复检查已取消'),{name:'AbortError'});
  const colName = window.ExcelLensAddress.columnName;
  const model={
    identity:(c,scopeKey)=>JSON.stringify([c.bookKey,c.book,c.opened,c.source,c.headerRow,scopeKey(c.sourceScope)]),
    owner(data,c,job,field,scopeKey){
      if(data?.id!==job||data.workbookKey!==c.bookKey||data.book!==c.book||data.source!==c.source||data.headerRow!==c.headerRow||data.field!==field||scopeKey(data.sourceScope)!==scopeKey(c.sourceScope))
        throw Error('候选响应与当前来源范围不一致');
    },
    page(data,offset,limit,key='items'){
      const items=data?.[key];
      if(!Number.isSafeInteger(offset)||offset<0||data?.offset!==offset||!Number.isSafeInteger(data.total)||data.total<offset||
        !Array.isArray(items)||items.length>limit||data.next!==offset+items.length||data.next>data.total||
        (data.next<data.total&&items.length!==limit))throw Error('候选分页响应无效');
    }
  };
  class Clusters{
    constructor({api,context,scopeKey=scope=>JSON.stringify(scope||null),onTransform,navigate,startExport,toast,limits,sleep=ms=>new Promise(resolve=>setTimeout(resolve,ms))}){
      Object.assign(this,{api,context,scopeKey,onTransform,navigate,startExport,toast,limits});
      this.dialog=$('analysisClustersDialog');this.serial=0;this.pageSerial=0;this.detailSerial=0;this.navSerial=0;this.snapshot=null;this.page=null;this.detail=null;this.choice=null;this.confirmed=new Map();this.opened=false;
      this.jobs=new window.ExcelLensAnalysisJobs({api,identity:()=>this.identity(),requestIdentity:()=>this.context().bookKey,sleep,
        endpoint:'/api/analysis/clusters',label:'疑似重复检查',resultFromStatus:s=>s,
        onBusy:busy=>{this.busy=busy;this.controls();},onProgress:s=>this.status(s.stage||'正在检查疑似重复…')});
      $('analysisClustersClose').addEventListener('click',()=>this.close());
      $('analysisClustersRun').addEventListener('click',()=>this.run());
      $('analysisClustersCancel').addEventListener('click',()=>{this.serial++;this.loading=false;this.jobs.cancelMain();this.controls();this.status('检查已取消；上次成功结果保留');});
      $('analysisClustersPrev').addEventListener('click',()=>this.loadPage(Math.max(0,this.page.offset-this.limits.pageGroups)));
      $('analysisClustersNext').addEventListener('click',()=>this.loadPage(this.page.next));
      $('analysisClusterVariantsPrev').addEventListener('click',()=>this.select(this.detail.group,Math.max(0,this.detail.offset-this.limits.pageVariants),true));
      $('analysisClusterVariantsNext').addEventListener('click',()=>this.select(this.detail.group,this.detail.next,true));
      $('analysisClustersConfirm').addEventListener('click',()=>this.confirm());
      $('analysisClustersExport').addEventListener('click',()=>this.export());
      this.dialog.addEventListener('cancel',event=>{event.preventDefault();this.close();});
      this.dialog.addEventListener('close',()=>{if(!this.dialog.open&&this.opened)this.close();});
      this.controls();
    }
    identity(){return model.identity(this.context(),this.scopeKey);}
    status(text,error=false){const el=$('analysisClustersStatus');el.textContent=text;el.classList.toggle('is-error',error);}
    current(snapshot=this.snapshot){return this.opened&&snapshot&&snapshot===this.snapshot&&snapshot.identity===this.identity();}
    release(id){if(id)this.api('/api/analysis/clusters/clear',{method:'POST',body:JSON.stringify({id})}).catch(()=>{});}
    reset(){this.close();this.release(this.snapshot?.job);this.snapshot=null;this.page=null;this.detail=null;this.choice=null;this.confirmed.clear();this.renderList();this.renderDetail();this.renderSummary();this.controls();}
    close(){this.opened=false;this.loading=false;this.serial++;this.pageSerial++;this.detailSerial++;this.jobs.reset();this.pageController?.abort();this.detailController?.abort();if(this.dialog.open)this.dialog.close();}
    open(field){
      const context=this.context();if(!context.opened||!context.ready)return;
      if(this.snapshot&&(this.snapshot.identity!==this.identity()||this.snapshot.field.id!==field.id))this.reset();
      this.field={...field};this.opened=true;this.serial++;
      $('analysisClustersTitle').textContent='疑似重复 · '+field.name;
      this.status(this.snapshot?'保留上次成功结果；可调整规则重新检查':'选择比较规则，再点击“检查”。原始列不变。');
      this.controls();if(!this.dialog.open)this.dialog.showModal();
    }
    controls(){
      const busy=!!this.busy||!!this.loading;
      $('analysisClustersOptions').disabled=busy;
      $('analysisClustersRun').disabled=busy||!!this.confirming;
      $('analysisClustersCancel').hidden=!busy;
      $('analysisClustersPrev').disabled=busy||!this.page||this.page.offset===0;
      $('analysisClustersNext').disabled=busy||!this.page||this.page.next>=this.page.total;
      $('analysisClusterVariantsPrev').disabled=busy||!this.detail||this.detail.offset===0;
      $('analysisClusterVariantsNext').disabled=busy||!this.detail||this.detail.next>=this.detail.total;
      $('analysisClustersConfirm').disabled=busy||!!this.confirming||!!this.detailLoading||!this.current()||this.detail?.kind!=='similar'||this.choice===null||this.confirmed.get(this.detail?.group)===this.choice;
      $('analysisClustersExport').disabled=busy||!this.current();
    }
    options(){return {foldCase:$('analysisClustersCase').checked,foldWidth:$('analysisClustersWidth').checked,space:$('analysisClustersSpace').value,ignorePunctuation:$('analysisClustersPunctuation').checked};}
    async run(){
      if(!this.opened||this.busy||this.loading)return;
      const c={...this.context()},field={...this.field},identity=this.identity(),ticket=++this.serial;
      if(c.sourceScope&&!c.sourceId){this.status('来源范围已释放，请重新读取分析字段',true);return;}
      this.pageController?.abort();this.detailController?.abort();this.pageSerial++;this.detailSerial++;
      this.loading=true;this.controls();
      let candidate='',adopted=false;
      try{
        const completed=await this.jobs.execute({workbookKey:c.bookKey,book:c.book,source:c.source,sourceId:c.sourceId||'',headerRow:c.headerRow,field:field.id,options:this.options()},{main:true});
        candidate=completed.job;
        if(!this.opened||ticket!==this.serial||identity!==this.identity())throw interrupted();
        const page=await this.api('/api/analysis/clusters/groups?'+new URLSearchParams({id:candidate,offset:'0'}));
        if(!this.opened||ticket!==this.serial||identity!==this.identity())throw interrupted();
        model.owner(page,c,candidate,field.id,this.scopeKey);model.page(page,0,this.limits.pageGroups);
        const previous=this.snapshot?.job;
        this.snapshot={job:candidate,identity,context:c,field};this.page=page;this.detail=null;this.choice=null;this.confirmed.clear();adopted=true;this.loading=false;
        this.renderList();this.renderDetail();this.renderSummary();this.controls();if(previous&&previous!==candidate)this.release(previous);
        this.status('已检查完整分析范围。疑似相同仅供核对，需逐组选择代表原文。');
        if(page.items.length)await this.select(page.items[0].id);
      }catch(error){if(this.opened&&ticket===this.serial&&identity===this.identity()&&error.name!=='AbortError')this.status(error.message+'；上次成功结果保留',true);}
      finally{if(candidate&&!adopted)this.release(candidate);if(ticket===this.serial)this.loading=false;this.controls();}
    }
    async loadPage(offset){
      const snapshot=this.snapshot;if(!this.current(snapshot)||this.busy)return;
      const ticket=++this.pageSerial,controller=new AbortController();this.pageController?.abort();this.pageController=controller;
      try{
        const data=await this.api('/api/analysis/clusters/groups?'+new URLSearchParams({id:snapshot.job,offset:String(offset)}),{signal:controller.signal});
        if(!this.current(snapshot)||ticket!==this.pageSerial||controller.signal.aborted)return;
        model.owner(data,snapshot.context,snapshot.job,snapshot.field.id,this.scopeKey);model.page(data,offset,this.limits.pageGroups);
        this.page=data;this.detail=null;this.choice=null;this.renderList();this.renderDetail();this.controls();
        if(data.items.length)await this.select(data.items[0].id);
      }catch(error){if(this.current(snapshot)&&ticket===this.pageSerial&&error.name!=='AbortError')this.status(error.message,true);}
    }
    async select(group,offset=0,keepChoice=false){
      const snapshot=this.snapshot;if(!this.current(snapshot)||this.busy)return;
      const ticket=++this.detailSerial,controller=new AbortController();this.detailController?.abort();this.detailController=controller;
      if(!keepChoice)this.choice=null;
      this.detailLoading=true;this.renderDetail();this.controls();
      try{
        const data=await this.api('/api/analysis/clusters/detail?'+new URLSearchParams({id:snapshot.job,group:String(group),offset:String(offset)}),{signal:controller.signal});
        if(!this.current(snapshot)||ticket!==this.detailSerial||controller.signal.aborted)return;
        model.owner(data,snapshot.context,snapshot.job,snapshot.field.id,this.scopeKey);model.page(data,offset,this.limits.pageVariants,'variants');
        if(data.group!==group||!['exact','similar'].includes(data.kind)||data.variants.some(v=>typeof v.raw!=='string'||!Number.isSafeInteger(v.count)||v.count<1||!Array.isArray(v.rows)))throw Error('候选原文响应无效');
        this.detail=data;this.renderList();this.renderDetail();this.controls();
      }catch(error){if(this.current(snapshot)&&ticket===this.detailSerial&&error.name!=='AbortError')this.status(error.message,true);}
      finally{if(ticket===this.detailSerial){this.detailLoading=false;this.renderDetail();this.controls();}}
    }
    renderSummary(){
      const counts=this.page?.counts;
      $('analysisClustersSummary').textContent=counts?
        '范围 '+counts.scanned.toLocaleString()+' 行；参与 '+counts.eligible.toLocaleString()+' 项，疑似重复 '+counts.similarGroups.toLocaleString()+' 组，原文相同 '+counts.exactGroups.toLocaleString()+' 组。排除：空值 '+counts.blank+'、数值 '+counts.numeric+'、其他类型 '+counts.otherTypes+'、缺缓存 '+counts.missingCache+'、空比较键 '+counts.emptyKey+'。':'';
    }
    renderList(){
      const host=$('analysisClustersGroups');host.replaceChildren();
      for(const item of this.page?.items||[]){
        const button=document.createElement('button');button.type='button';button.className='analysis-cluster-group';button.dataset.group=String(item.id);
        button.setAttribute('aria-pressed',String(this.detail?.group===item.id));
        const title=document.createElement('strong');title.textContent=item.key;title.title=item.key;
        const meta=document.createElement('span');meta.textContent=(item.kind==='similar'?'疑似重复':'原文相同')+' · '+item.variants+' 项原文与类型 · '+item.count.toLocaleString()+' 次';
        button.append(title,meta);button.addEventListener('click',()=>this.select(item.id));host.append(button);
      }
      if(this.page&&!this.page.items.length){const note=document.createElement('p');note.textContent='此范围和规则下没有重复候选。';host.append(note);}
      $('analysisClustersPage').textContent=this.page?(this.page.total?this.page.offset+1:0)+'–'+this.page.next+' / '+this.page.total:'尚未检查';
    }
    renderDetail(){
      const detail=this.detail;
      const host=$('analysisClusterVariants');host.replaceChildren();
      $('analysisClusterDetailTitle').textContent=this.detail?(this.detail.kind==='similar'?'选择代表原文':'原文相同，无需映射'):'选择候选组查看原文';
      $('analysisClusterDetailKey').textContent=this.detail?.key||'';
      for(const [i,value] of (this.detail?.variants||[]).entries()){
        const item=document.createElement('div');item.className='analysis-cluster-variant';
        const label=document.createElement('label'),radio=document.createElement('input');radio.type='radio';radio.name='analysisClusterRepresentative';radio.disabled=!!this.detailLoading||this.detail.kind!=='similar';radio.checked=this.choice===value.raw;
        radio.addEventListener('change',()=>{if(this.detail!==detail||this.detailLoading)return;this.choice=value.raw;this.renderDetail();this.controls();});
        const raw=document.createElement('pre');raw.textContent=value.raw;label.append(radio,raw);item.append(label);
        const meta=document.createElement('div');meta.className='analysis-cluster-variant-meta';
        const count=document.createElement('span');count.textContent='文本 · '+value.count.toLocaleString()+' 次'+(value.formulaCount?' · 其中公式缓存 '+value.formulaCount+' 次':'');count.title='源读取类型：'+value.type;meta.append(count);
        for(const [sample,row] of value.rows.entries()){
          const link=document.createElement('button');link.type='button';link.className='analysis-drill-link';link.textContent=colName(this.detail.column||Number(this.snapshot?.field.id.slice(1)))+row;
          link.addEventListener('click',()=>{if(this.detail===detail)this.locate(detail.offset+i,sample);});meta.append(link);
        }
        item.append(meta);host.append(item);
      }
      $('analysisClusterVariantsPage').textContent=this.detail?(this.detail.offset+1)+'–'+this.detail.next+' / '+this.detail.total+' 项原文与类型':'原文按读取类型分别列出；位置为有限样例，次数覆盖完整范围';
    }
    async confirm(){
      const snapshot=this.snapshot,detail=this.detail,choice=this.choice,ticket=this.serial;
      if(!this.current(snapshot)||this.busy||this.loading||this.detailLoading||this.confirming||choice===null||detail?.kind!=='similar'||this.confirmed.get(detail.group)===choice)return;
      this.confirming=true;this.controls();
      try{
        const data=await this.api('/api/analysis/clusters/confirm',{method:'POST',body:JSON.stringify({id:snapshot.job,workbookKey:snapshot.context.bookKey,book:snapshot.context.book,choices:[{group:detail.group,representative:choice}]})});
        if(!this.current(snapshot)||ticket!==this.serial||this.detail!==detail||this.choice!==choice)return;
        model.owner(data,snapshot.context,snapshot.job,snapshot.field.id,this.scopeKey);
        if(data.transform?.op!=='map_text'||data.transform.source!==snapshot.field.id||!Array.isArray(data.transform.mapping)||!data.transform.mapping.length)throw Error('文本映射响应无效');
        await this.onTransform(data.transform,()=>this.current(snapshot)&&ticket===this.serial);
        this.confirmed.set(detail.group,choice);
        this.status('已添加独立文本映射列；将新列用于分类并运行分析后生效。');this.toast('已添加文本核对列，运行分析后生效','success');
      }catch(error){if(this.current(snapshot)&&ticket===this.serial)this.status(error.message,true);}
      finally{this.confirming=false;this.controls();}
    }
    async locate(variant,sample){
      const snapshot=this.snapshot,detail=this.detail,ticket=++this.navSerial;
      if(!this.current(snapshot)||!detail)return;
      const serial=this.serial;
      try{
        const data=await this.api('/api/analysis/clusters/resolve?'+new URLSearchParams({id:snapshot.job,group:String(detail.group),variant:String(variant),sample:String(sample),workbookKey:snapshot.context.bookKey,book:snapshot.context.book}));
        if(!this.current(snapshot)||serial!==this.serial||ticket!==this.navSerial)return;
        model.owner(data,snapshot.context,snapshot.job,snapshot.field.id,this.scopeKey);
        this.close();
        await this.navigate(data,()=>snapshot.context.bookKey===this.context().bookKey&&snapshot.context.book===this.context().book&&ticket===this.navSerial);
      }catch(error){if(this.current(snapshot)&&serial===this.serial)this.status(error.message,true);}
    }
    export(){
      const snapshot=this.snapshot;if(!this.current(snapshot)||this.busy)return;
      const protect=$('analysisClustersCSVGuard').checked;this.close();this.startExport(snapshot.job,protect);
    }
  }
  window.ExcelLensClusterModel=model;window.ExcelLensAnalysisClusters=Clusters;
})();
