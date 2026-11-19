/* Shared read-only precedent/dependent tracing. Results and origin survive
 * minimization. Arrival rectangles live above the grid, never in its layout. */
(() => {
  'use strict';
  const names={precedents:'追踪引用',dependents:'追踪从属'};
  class Tracing {
    constructor(o){
      this.o=o;this.refs=[];this.request=0;this.nav=0;this.motion=0;this.job=null;this.muted=false;this.pulseRefs=[];this.pulseTicket=0;this.ignoreClose=0;
      this.panel=o.panel;this.dock=o.dock;this.layer=o.layer;
      o.minimize.addEventListener('click',()=>this.minimize());
      o.restore.addEventListener('click',()=>this.restore());
      o.origin.addEventListener('click',()=>this.goOrigin());
      o.end.addEventListener('click',()=>this.reset());
      o.cancel.addEventListener('click',()=>{this.stopRun();o.summary.textContent='已取消 · 已找到的结果仍可定位';o.cancel.hidden=true;});
      o.scope.addEventListener('change',()=>{o.writeScope?.(o.scope.value);if(this.source)this.open(this.kind,this.source);});
      this.panel.addEventListener('close',()=>{if(this.ignoreClose){this.ignoreClose--;return;}if(this.source)this.reset();});
      this.panel.addEventListener('keydown',event=>{if(event.key==='Escape'){event.preventDefault();event.stopPropagation();this.minimize();}});
      o.grid.addEventListener('scroll',()=>this.refreshPulse(),{passive:true});
      window.addEventListener('resize',()=>this.refreshPulse());
    }
    stopRun(){
      this.activity?.end();
      ++this.request;if(this.waiting){clearTimeout(this.waiting.timer);this.waiting.resolve();this.waiting=null;}
      const job=this.job;this.job=null;
      if(job?.owned)this.o.api('/api/search/cancel',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({id:job.id})}).catch(()=>{});
    }
    reset(){
      this.stopRun();++this.nav;++this.motion;this.clearPulse();this.source=null;this.refs=[];this.muted=false;this.minimized=false;
      this.o.setRefs([]);this.dock.hidden=true;window.ExcelLensMotion?.stop(this.panel);
      if(this.panel.open){this.ignoreClose++;this.panel.close();}
    }
    selectionChanged(){++this.nav;this.clearPulse();if(this.source){this.muted=true;this.o.setRefs([]);}}
    updateDock(){this.o.restore.textContent=`${names[this.kind]||'追踪'} · ${this.refs.length}`;this.o.restore.title=this.source?`${this.source.sheet}!${this.source.ref} · 展开追踪结果`:'';this.o.origin.title=this.source?`回到起点：${this.source.sheet}!${this.source.ref}`:'';}
    restore(){
      if(!this.source)return;++this.motion;this.minimized=false;this.dock.hidden=true;
      if(!this.panel.open)this.panel.show();window.ExcelLensMotion?.enter(this.panel,{distance:6});
      this.o.summary.focus?.({preventScroll:true});
    }
    async minimize(){
      if(!this.source)return;const ticket=++this.motion;this.minimized=true;this.updateDock();this.dock.hidden=false;
      window.ExcelLensMotion?.enter(this.dock,{distance:3});
      if(this.panel.open){await window.ExcelLensMotion?.exit(this.panel);if(ticket===this.motion&&this.minimized&&this.panel.open){this.ignoreClose++;this.panel.close();}}
    }
    async open(kind,source=this.o.state.selected){
      if(!source||kind==='precedents'&&(!source.hasFormula||!source.formula))return;
      this.stopRun();++this.nav;this.clearPulse();this.muted=false;this.didLocate=false;this.source={...source,sheet:source.sheet||this.o.state.sheet};this.book=this.o.state.workbookKey;this.kind=kind;this.refs=[];this.o.setRefs([]);
      const ticket=this.request;this.count=100;this.o.title.textContent=`${names[kind]} · ${this.source.sheet}!${this.source.ref}`;
      this.o.formula.textContent=this.source.formula||`${this.source.sheet}!${this.source.ref}`;
      this.o.scopeRow.hidden=kind!=='dependents';this.o.scope.value=this.o.readScope?.()||this.o.scope.value||'current';
      this.o.list.replaceChildren();this.o.summary.textContent=kind==='dependents'?'正在查找直接引用此单元格的公式…':'读取当前公式的直接引用…';this.o.cancel.hidden=kind!=='dependents';this.restore();
      if(kind==='precedents'){
        this.refs=this.o.parse(this.source.formula,this.source.sheet).map(ref=>({...ref,sheet:this.o.state.sheets?.find(s=>s.name.toLocaleLowerCase()===ref.sheet.toLocaleLowerCase())?.name||ref.sheet}));this.o.setRefs(this.refs);this.render(ticket);
        this.o.summary.textContent=this.refs.length?`${this.refs.length} 个直接引用 · 点击定位后可从底部继续追踪`:'未识别到 A1 引用；名称、结构化引用和外部工作簿引用暂不展开';
        this.updateDock();this.pulse(this.refs,`${names[kind]} · ${this.refs.length} 处`);return;
      }
      const scope=this.o.scope.value,query=scope==='all'?`'${this.source.sheet.replaceAll("'","''")}'!${this.source.ref}`:this.source.ref;
      const activity=this.activity=globalThis.ExcelLensActivity?.begin('正在追踪从属公式');
      try{
        const start=await this.o.api('/api/search/start',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({sheet:this.source.sheet,query,mode:'reference',scope})});
        if(ticket!==this.request){if(!start.reused)this.o.api('/api/search/cancel',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({id:start.jobId})}).catch(()=>{});return;}
        this.job={id:start.jobId,owned:!start.reused};
        while(ticket===this.request){
          const data=await this.o.api('/api/search/status?id='+encodeURIComponent(start.jobId));if(ticket!==this.request)return;
          activity?.report(data.progress,100,data.stage);
          const refs=(data.matches||[]).map(match=>{const p=this.o.parseRef(match.ref);return p?{sheet:match.sheet||this.source.sheet,label:`${match.sheet||this.source.sheet}!${match.ref}`,start:p,end:p,formula:match.formula}:null;}).filter(Boolean);
          const changed=refs.length!==this.refs.length||refs.at(-1)?.label!==this.refs.at(-1)?.label||data.status!=='running';this.refs=refs;
          if(changed){if(!this.muted)this.o.setRefs(this.refs);this.render(ticket);this.updateDock();}
          const incomplete=data.truncated?' · 结果已截断（最多500条）':'';
          if(data.status!=='running'){
            this.job=null;this.o.cancel.hidden=true;
            this.o.summary.textContent=data.status==='error'?`未完成：${data.error||'追踪失败'} · 已找到 ${this.refs.length} 处`:data.status==='cancelled'?`已取消 · 已找到 ${this.refs.length} 处`:`找到 ${this.refs.length} 个直接从属公式${incomplete}`;
            if(data.status==='done'&&!this.muted&&!this.didLocate)this.pulse(this.refs,`${names[kind]} · ${this.refs.length} 处`);return;
          }
          this.o.summary.textContent=`${data.stage||'查找中'} · ${Math.round(data.progress||0)}% · ${this.refs.length} 处`;
          await new Promise(resolve=>{this.waiting={resolve,timer:setTimeout(()=>{this.waiting=null;resolve();},250)};});
        }
      }catch(error){if(ticket===this.request){this.stopRun();this.o.summary.textContent='追踪失败：'+error.message;this.o.cancel.hidden=true;}}
      finally{activity?.end();}
    }
    render(ticket){
      const fragment=document.createDocumentFragment();
      for(const ref of this.refs.slice(0,this.count)){
        const item=document.createElement('div');item.className='trace-item';
        const address=document.createElement('div');address.className='trace-ref';address.textContent=ref.label;address.title=ref.label;
        const value=document.createElement('div');value.className='trace-value';
        if(ref.formula)value.textContent=ref.formula;
        else if(ref.range)value.textContent='区域引用 · 定位左上角';
        else if(ref.sheet!==this.o.state.sheet)value.textContent='跨工作表引用 · 按需定位';
        else{value.textContent='读取中…';this.o.getCell(ref.sheet,ref.start.ref).then(cell=>{if(ticket!==this.request)return;value.textContent=cell.cacheMissing?'无缓存':String(cell.display??cell.raw??'')||'空白';value.title=cell.formula||value.textContent;}).catch(error=>{if(ticket===this.request)value.textContent=error.message;});}
        const go=document.createElement('button');go.type='button';go.className='trace-go';go.textContent='定位';go.addEventListener('click',()=>this.locate(ref));item.append(address,value,go);fragment.append(item);
      }
      if(this.refs.length>this.count){const more=document.createElement('button');more.type='button';more.className='trace-go trace-more';more.textContent=`显示更多（${this.count}/${this.refs.length}）`;more.addEventListener('click',()=>{this.count+=100;this.render(ticket);});fragment.append(more);}
      const top=this.o.list.scrollTop;this.o.list.replaceChildren(fragment);this.o.list.scrollTop=top;
    }
    async locate(ref,origin=false){
      if(!this.source)return;const ticket=++this.nav,book=this.book;this.clearPulse();this.muted=false;this.didLocate=true;this.o.setRefs(this.refs);
      const current=()=>ticket===this.nav&&book===this.o.state.workbookKey&&Boolean(this.source);
      try{
        await Promise.all([this.minimize(),this.o.navigate(ref.sheet,ref.start.ref,current)]);
        if(!current())return;
        if(!this.o.atTarget(ref)){this.o.notify('目标未能显示，请检查筛选或工作表状态','warning');return;}
        const label=`${origin?'追踪起点':names[this.kind]} · ${ref.label}`;
        this.pulse([ref],label,true,current);
      }catch(error){if(current())this.o.notify(error.message,'error');}
    }
    goOrigin(){if(!this.source)return;const p=this.o.parseRef(this.source.ref);if(p)return this.locate({sheet:this.source.sheet,label:`${this.source.sheet}!${this.source.ref}`,start:p,end:p},true);}
    clearPulse(){++this.pulseTicket;clearTimeout(this.pulseTimer);cancelAnimationFrame(this.pulseFrame);this.pulseFrame=0;this.pulseRefs=[];this.layer.hidden=true;this.layer.replaceChildren();}
    pulse(refs,label,wait=false,current=()=>true){
      this.clearPulse();this.pulseTicket=(this.pulseTicket||0)+1;const ticket=this.pulseTicket,deadline=Date.now()+1200;
      this.pulseRefs=refs;this.pulseLabel=label;this.pulseUntil=0;
      const arrive=()=>{this.pulseFrame=0;if(ticket!==this.pulseTicket||!current())return;const boxes=this.measure();if(boxes.length){this.pulseUntil=Date.now()+2000;this.paint(boxes);this.pulseTimer=setTimeout(()=>{if(ticket===this.pulseTicket)this.clearPulse();},2000);}else if(wait&&Date.now()<deadline)this.pulseFrame=requestAnimationFrame(arrive);else this.clearPulse();};arrive();
    }
    measure(){
      const sheet=this.o.state.sheet,region=this.layer.parentElement.getBoundingClientRect(),cells=[];
      for(const node of document.querySelectorAll('.grid-region .grid-cell[data-row][data-col]')){const bounds=this.o.bounds(Number(node.dataset.row),Number(node.dataset.col));cells.push({node,bounds});}
      const boxes=[],measured=new Map();
      for(const ref of this.pulseRefs){if(ref.sheet.toLocaleLowerCase()!==String(sheet).toLocaleLowerCase())continue;const groups=new Map();
        for(const {node,bounds:b}of cells){if(b.row>ref.end.row||b.endRow<ref.start.row||b.col>ref.end.col||b.endCol<ref.start.col)continue;
          if(!measured.has(node))measured.set(node,this.o.visibleRect(node));const r=measured.get(node);if(!r)continue;
          const old=groups.get(r.pane);groups.set(r.pane,old?{left:Math.min(old.left,r.left),top:Math.min(old.top,r.top),right:Math.max(old.right,r.right),bottom:Math.max(old.bottom,r.bottom)}:r);
        }
        for(const r of groups.values())boxes.push({left:r.left-region.left,top:r.top-region.top,width:r.right-r.left,height:r.bottom-r.top});
      }return boxes;
    }
    paint(boxes){
      if(!boxes.length){this.layer.hidden=true;return;}this.layer.hidden=false;
      let nodes=[...this.layer.querySelectorAll('.trace-arrival-box')];while(nodes.length>boxes.length)nodes.pop().remove();
      for(let i=0;i<boxes.length;i++){let node=nodes[i];if(!node){node=document.createElement('div');node.className='trace-arrival-box';this.layer.append(node);}const b=boxes[i];Object.assign(node.style,{left:b.left+'px',top:b.top+'px',width:b.width+'px',height:b.height+'px'});}
      let label=this.layer.querySelector('.trace-arrival-label');if(!label){label=document.createElement('div');label.className='trace-arrival-label';label.setAttribute('role','status');this.layer.append(label);}label.textContent=this.pulseLabel;
      const first=boxes[0],width=this.layer.clientWidth;label.style.left=Math.max(8,Math.min(first.left,width-Math.min(380,width-16)-8))+'px';label.style.top=Math.max(4,first.top-29)+'px';
    }
    refreshPulse(){if(!this.pulseRefs.length||!this.pulseUntil||this.pulseFrame)return;this.pulseFrame=requestAnimationFrame(()=>{this.pulseFrame=0;if(Date.now()>=this.pulseUntil)this.clearPulse();else this.paint(this.measure());});}
  }
  window.ExcelLensTracing=Tracing;
})();
