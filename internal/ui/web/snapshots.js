(() => {
  'use strict';
  const $=id=>document.getElementById(id),make=(tag,cls='',text='')=>{const n=document.createElement(tag);n.className=cls;n.textContent=text;return n;};
  const states={only_left:'仅基准有',only_right:'仅对比有',empty:'无有效值',unavailable:'无法比较',missing:'两侧均无',zero_base:'基准为0'};
  class Snapshots {
    constructor({api,toast,limits}){
      Object.assign(this,{api,toast,limits});this.model=window.ExcelLensSnapshotModel;this.epoch=0;this.readSerial=0;this.pairSerial=0;this.busy=false;this.dialog=this.create();
      $('snapshotsClose').addEventListener('click',()=>this.close());this.dialog.addEventListener('cancel',e=>{e.preventDefault();this.close();});this.dialog.addEventListener('close',()=>{if(!this.dialog.open)this.reset();});
      $('snapshotsRefresh').addEventListener('click',()=>this.refresh());$('snapshotsSave').addEventListener('click',()=>this.save());
      for(const side of ['Left','Right']){$('snapshots'+side).addEventListener('change',()=>this.loadPair());$('snapshotsDelete'+side).addEventListener('click',()=>this.remove(side));}
      $('snapshotsSeries').addEventListener('change',()=>{this.page=0;this.renderRows();this.draw();});
      $('snapshotsPrevious').addEventListener('click',()=>{this.page=Math.max(0,this.page-1);this.renderRows();});$('snapshotsNext').addEventListener('click',()=>{this.page++;this.renderRows();});
      if(window.ResizeObserver)this.observer=new ResizeObserver(()=>this.draw());this.observer?.observe($('snapshotsSides'));
      for(const event of ['excellens:readingchange','excellens:analysisnumberchange'])document.addEventListener(event,()=>{if(this.accepted&&this.dialog.open){this.renderSides();this.renderRows();this.draw();}});
    }
    create(){
      const d=make('dialog','float-dialog snapshots-dialog');d.id='snapshotsDialog';d.setAttribute('aria-labelledby','snapshotsTitle');
      d.innerHTML=`<div class="dialog-head"><div id="snapshotsTitle" class="dialog-title">比较快照</div><button id="snapshotsClose" type="button" class="dialog-close" aria-label="关闭比较快照">×</button></div>
        <form id="snapshotsCapture" class="snapshots-tools"><input id="snapshotsName" class="field-control" aria-label="新快照名称" maxlength="80" placeholder="快照名称"><button id="snapshotsSave" type="button" class="mini-button">保存当前结果</button></form>
        <div class="snapshots-scroll"><div id="snapshotsSides" class="snapshots-sides">${['Left','Right'].map((side,i)=>`<section><div class="snapshots-picker"><label for="snapshots${side}">${i?'对比':'基准'}</label><select id="snapshots${side}" class="field-control"></select><button id="snapshotsDelete${side}" type="button" class="mini-button" aria-label="删除${i?'对比':'基准'}快照">删除</button></div><div id="snapshots${side}Content" class="snapshots-content"></div></section>`).join('')}</div>
        <div id="snapshotsComparison" hidden><div class="snapshots-table-tools"><label>系列 <select id="snapshotsSeries" class="field-control"></select></label><span id="snapshotsCount"></span></div><div id="snapshotsTable" class="snapshots-table"></div><div class="snapshots-pager"><button id="snapshotsPrevious" class="mini-button" type="button">上一页</button><span id="snapshotsPage"></span><button id="snapshotsNext" class="mini-button" type="button">下一页</button></div></div></div>
        <div class="snapshots-footer"><span id="snapshotsStatus" role="status" aria-live="polite"></span><button id="snapshotsRefresh" type="button" class="mini-button">刷新目录</button></div>`;
      d.querySelector('form').addEventListener('submit',e=>{e.preventDefault();this.save();});document.body.append(d);return d;
    }
    status(text,error=false){const node=$('snapshotsStatus');node.textContent=text;node.classList.toggle('is-error',error);node.setAttribute('role',error?'alert':'status');}
    controls(){const available=this.capture?.current?.();$('snapshotsCapture').hidden=!available;$('snapshotsSave').disabled=this.busy||!this.store||!available;$('snapshotsName').disabled=this.busy;$('snapshotsRefresh').disabled=this.busy;for(const side of ['Left','Right']){$('snapshots'+side).disabled=this.busy;$('snapshotsDelete'+side).disabled=this.busy||!$('snapshots'+side).value;}}
    open(capture=null){
      if(this.dialog.open){this.capture=capture?.current?.()?capture:null;this.controls();return;}
      this.reset();this.capture=capture?.current?.()?capture:null;$('snapshotsName').value=capture?.title||'分析快照';this.controls();this.dialog.showModal();this.refresh();
    }
    reset(){++this.epoch;++this.readSerial;++this.pairSerial;this.readAbort?.abort();this.pairAbort?.abort();this.readAbort=this.pairAbort=null;this.busy=false;this.capture=null;this.store=this.accepted=null;this.deleteIntent=null;this.page=0;for(const side of ['Left','Right']){$('snapshots'+side).replaceChildren();$('snapshots'+side+'Content').replaceChildren();}$('snapshotsComparison').hidden=true;$('snapshotsTable').replaceChildren();this.status('');}
    close(){this.reset();if(this.dialog.open)this.dialog.close();}
    changed(){if(this.capture&&!this.capture.current()){this.capture=null;this.controls();}}
    async refresh(){
      if(this.busy||!this.dialog.open)return;const epoch=this.epoch,serial=++this.readSerial;this.readAbort?.abort();const ctrl=new AbortController();this.readAbort=ctrl;this.status('正在读取…');
      try{const data=this.model.catalog(await this.api('/api/analysis/snapshots',{signal:ctrl.signal}),this.limits);if(this.epoch!==epoch||serial!==this.readSerial||!this.dialog.open)return;this.adoptCatalog(data);await this.loadPair();}
      catch(e){if(this.epoch===epoch&&serial===this.readSerial&&e.name!=='AbortError')this.status(e.message,true);}
    }
    adoptCatalog(data,newID=''){
      this.store=data;const items=data.items,ids=new Set(items.map(i=>i.id));
      for(const [i,side]of ['Left','Right'].entries()){const node=$('snapshots'+side),old=node.value;node.replaceChildren(new Option('选择快照',''));for(const item of items)node.append(new Option(item.name+' — '+item.file,item.id));node.value=side==='Right'&&newID?newID:ids.has(old)?old:items[Math.max(0,items.length-(i?1:2))]?.id||'';}
      this.controls();
    }
    async save(){
      if(this.busy||!this.store||!this.capture?.current?.())return;const capture=this.capture;
      const name=$('snapshotsName').value.trim();if(!name){this.status('请填写快照名称',true);return;}
      await this.write({op:'capture',name,jobId:capture.job,metricIndex:capture.metric,workbookKey:capture.workbookKey,book:capture.book},true);
    }
    async remove(side){
      if(this.busy||!this.store)return;const id=$('snapshots'+side).value;if(!id)return;
      if(this.deleteIntent?.id!==id||this.deleteIntent?.side!==side){this.deleteIntent={id,side};for(const key of ['Left','Right'])$('snapshotsDelete'+key).textContent=key===side?'确认删除':'删除';return;}
      await this.write({op:'delete',id});
    }
    async write(request,capture=false){
      const epoch=this.epoch,revision=this.store.revision,oldIDs=new Set(this.store.items.map(i=>i.id));this.busy=true;++this.readSerial;this.readAbort?.abort();++this.pairSerial;this.pairAbort?.abort();this.controls();this.status('正在保存…');
      try{const data=this.model.catalog(await this.api('/api/analysis/snapshots',{method:'POST',body:JSON.stringify({...request,expectedRevision:revision})}),this.limits);if(this.epoch!==epoch||!this.dialog.open)return;this.busy=false;const id=capture?data.items.find(i=>!oldIDs.has(i.id))?.id||'':'';this.adoptCatalog(data,id);await this.loadPair();}
      catch(e){if(this.epoch===epoch&&this.dialog.open)this.status(e.message,true);}
      finally{if(this.epoch===epoch){this.busy=false;this.controls();}}
    }
    async loadPair(){
      if(this.busy||!this.dialog.open)return;const leftID=$('snapshotsLeft').value,rightID=$('snapshotsRight').value,serial=++this.pairSerial,epoch=this.epoch;
      this.deleteIntent=null;for(const side of ['Left','Right'])$('snapshotsDelete'+side).textContent='删除';this.pairAbort?.abort();const ctrl=new AbortController();this.pairAbort=ctrl;this.controls();
      if(!leftID||!rightID){this.accepted=null;for(const side of ['Left','Right'])$('snapshots'+side+'Content').replaceChildren();$('snapshotsTable').replaceChildren();$('snapshotsComparison').hidden=true;this.status(this.store.items.length?'请选择两个快照':'尚无快照');return;}
      const current=()=>this.dialog.open&&epoch===this.epoch&&serial===this.pairSerial&&!ctrl.signal.aborted;this.status('正在比较…');
      try{
        const get=id=>this.api('/api/analysis/snapshots?id='+encodeURIComponent(id),{signal:ctrl.signal}).then(s=>this.model.snapshot(s,id,this.limits));
        const [left,right]=await Promise.all([get(leftID),get(rightID)]);if(!current())return;
        let comparison=null,message='';try{comparison=this.model.comparison(await this.api('/api/analysis/snapshots/compare?'+new URLSearchParams({left:leftID,right:rightID}),{signal:ctrl.signal}),left,right,this.limits);}catch(e){if(e.status!==422)throw e;message=e.message;}
        if(!current())return;this.accepted={left,right,comparison};this.page=0;this.renderSides();const select=$('snapshotsSeries');select.replaceChildren(new Option('分组合计','-1'));for(const [i,key]of (comparison?.columnKeys||[]).entries())select.append(new Option(key||'空白列',String(i)));select.value='-1';this.renderRows();this.draw();this.status(message,!!message);
      }catch(e){if(current()&&e.name!=='AbortError')this.status(e.message+(this.accepted?'（保留上次结果）':''),true);}
    }
    renderSides(){
      if(!this.accepted)return;
      for(const side of ['Left','Right']){const s=this.accepted[side.toLowerCase()],r=s.result,host=$('snapshots'+side+'Content');host.replaceChildren();
        const meta=make('p','snapshots-meta',s.file+'  '+new Date(s.created).toLocaleString()),metric=make('div','snapshots-metric',this.model.metric(s)),value=make('strong','snapshots-grand',window.ExcelLensAnalysisNumbers.format(r.grand));value.title=r.grand.comparison?.exact||r.grand.exact;
        const canvas=make('canvas','snapshots-trend');canvas.id='snapshotsTrend'+side;canvas.setAttribute('role','img');canvas.setAttribute('aria-label',(side==='Left'?'基准':'对比')+'分类趋势');host.append(make('h3','',s.name),meta,metric,value,canvas);
        if(r.truncated)host.append(make('p','snapshots-limit','仅含前 '+r.rows.length+' 个分类'));if(r.tree)host.append(make('p','snapshots-limit','首层分类'));
        const details=make('details'),summary=make('summary','','来源与规则'),pre=make('pre');details.append(summary,pre);details.addEventListener('toggle',()=>{if(details.open)pre.textContent=this.model.description(s);});host.append(details);
      }
    }
    value(cell){const n=make('td','snapshots-number',cell?window.ExcelLensAnalysisNumbers.format(cell):'—');if(cell)n.title=cell.comparison?.exact||cell.exact;return n;}
    renderRows(){
      const c=this.accepted?.comparison;$('snapshotsComparison').hidden=!c;if(!c)return;
      const table=make('table'),head=make('thead'),tr=make('tr');for(const title of ['分类','基准','对比','差额','变化']){const th=make('th','',title);th.scope='col';tr.append(th);}head.append(tr);table.append(head);
      const col=Number($('snapshotsSeries').value),body=make('tbody'),pages=Math.max(1,Math.ceil(c.rows.length/this.limits.pageRows));this.page=Math.min(Math.max(0,this.page),pages-1);
      const add=(host,keys,d)=>{const row=make('tr'),label=make('td','',keys.map(k=>k||'（空白）').join(' / ')||'全部');label.title=JSON.stringify(keys);row.append(label,this.value(d.left),this.value(d.right));row.append(d.status?make('td','snapshots-state',states[d.status]||'无法比较'):this.value(d.difference));row.append(d.change.status?make('td','snapshots-state',states[d.change.status]||'无法计算'):this.value(d.change));host.append(row);};
      for(const r of c.rows.slice(this.page*this.limits.pageRows,(this.page+1)*this.limits.pageRows))add(body,r.keys,col<0?r.total:r.values[col]);table.append(body);const foot=make('tfoot');add(foot,['全量总计'],col<0?c.grand:c.totals[col]);table.append(foot);$('snapshotsTable').replaceChildren(table);
      $('snapshotsPrevious').disabled=this.page===0;$('snapshotsNext').disabled=this.page>=pages-1;$('snapshotsPage').textContent=(this.page+1)+' / '+pages;$('snapshotsCount').textContent=c.rows.length+' 个分类';
    }
    draw(){
      if(!this.dialog.open||!this.accepted)return;const a=this.accepted,c=a.comparison,j=Number($('snapshotsSeries').value);
      const left=c?c.rows.map(r=>(j<0?r.total:r.values[j]).left):this.model.seriesCells(a.left,null),right=c?c.rows.map(r=>(j<0?r.total:r.values[j]).right):this.model.seriesCells(a.right,null),domain=[...left,...right];
      window.ExcelLensAnalyticsCharts.sparkline($('snapshotsTrendLeft'),left,domain);window.ExcelLensAnalyticsCharts.sparkline($('snapshotsTrendRight'),right,domain);
    }
  }
  window.ExcelLensSnapshotsController=Snapshots;
})();
