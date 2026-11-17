/* Lazy branches own only bounded responses. Runtime job IDs never enter a
 * saved query, and a cancelled late start is cleared by its own job owner. */
(() => {
  'use strict';
  const key=path=>JSON.stringify(path),equal=(a,b)=>Array.isArray(a)&&Array.isArray(b)&&a.length===b.length&&a.every((v,i)=>v===b[i]);
  const prefix=(a,b)=>Array.isArray(a)&&Array.isArray(b)&&a.length<=b.length&&a.every((v,i)=>v===b[i]);
  const label=value=>value===''?'（空白）':value;
  const count=n=>Number(n||0).toLocaleString('zh-CN');
  class Tree {
    constructor({api,context,limits,onChange=()=>{},onMessage=()=>{},sleep=ms=>new Promise(r=>setTimeout(r,ms))}){
      if(!limits||!['nodes','bytes','pageRows','active'].every(p=>Number.isSafeInteger(limits[p])&&limits[p]>0))throw Error('层级视图配置无效');
      Object.assign(this,{api,context,limits,onChange,onMessage,sleep});this.nodes=new Map();this.index=new Map();this.serial=0;this.clock=0;this.active=0;this.bytes=0;this.page=0;this.blocked=false;this.root=null;
    }
    identity(){const c=this.context();return key([c.bookKey,c.book,c.opened,this.serial,this.root?.job]);}
    current(root=this.root){const c=this.context();return !!root&&root===this.root&&c.opened&&root.bookKey===c.bookKey&&root.book===c.book;}
    notify(type='change',node=null){this.onChange({type,node});}
    setRoot(root){
      if(!root?.result?.tree){this.reset();return;}
      if(this.root?.job===root.job&&this.root.book===root.book&&this.root.bookKey===root.bookKey&&this.root.result===root.result)return;
      this.reset();this.root={...root,query:JSON.parse(JSON.stringify(root.query))};
    }
    reset(){++this.serial;for(const node of this.nodes.values())this.cancelNode(node);this.nodes.clear();this.index.clear();this.bytes=0;this.page=0;this.root=null;}
    get(path){let map=this.index,node;for(const value of path){node=map.get(value);if(!node)return null;map=node.children;}return node||null;}
    remove(node){
      const victims=[...this.nodes.values()].filter(other=>prefix(node.path,other.path));
      for(const old of victims){this.cancelNode(old);this.nodes.delete(old.key);this.bytes-=old.bytes;}
      const parent=node.path.length===1?this.index:this.get(node.path.slice(0,-1))?.children;
      parent?.delete(node.path.at(-1));
    }
    fit(extraBytes,extraNodes,protect){
      while(this.bytes+extraBytes>this.limits.bytes||this.nodes.size+extraNodes>this.limits.nodes){
        const candidates=[...this.nodes.values()].filter(n=>!n.pending&&!prefix(n.path,protect)&&![...this.nodes.values()].some(active=>active.pending&&prefix(n.path,active.path)));
        candidates.sort((a,b)=>Number(a.expanded)-Number(b.expanded)||a.used-b.used);
        if(!candidates.length)return false;
        this.remove(candidates[0]);
      }
      return true;
    }
    row(path){const parent=path.length===1?this.root?.result:this.get(path.slice(0,-1))?.result;return parent?.rows?.find(row=>equal(row.keys,path));}
    expand(path){
      if(!this.current()||this.blocked)return null;
      if(!path.length||path.length>=this.root.query.rows.length||!this.row(path))return null;
      let node=this.get(path);
      if(!node){
        const id=key(path),bytes=512+window.ExcelLensAnalysisResults.retainedBytes({path,id},this.limits.bytes);
        if(!this.fit(bytes,1,path)){this.onMessage('展开内容已达到缓存容量，请先收起部分分组');return null;}
        node={key:id,path:[...path],children:new Map(),expanded:false,result:null,pending:false,running:false,version:0,phase:'idle',error:'',progress:0,total:0,bytes,baseBytes:bytes,used:++this.clock};
        (path.length===1?this.index:this.get(path.slice(0,-1)).children).set(path.at(-1),node);this.nodes.set(id,node);this.bytes+=bytes;
      }
      node.expanded=true;node.used=++this.clock;this.page=0;
      if(!node.result&&!node.pending){this.bytes+=node.baseBytes-node.bytes;node.bytes=node.baseBytes;node.pending=true;node.phase='queued';node.error='';node.ticket=++this.clock;}
      this.notify();this.pump();return node;
    }
    cancelNode(node){++node.version;node.jobs?.cancelMain();node.pending=false;if(!node.result){this.bytes+=node.baseBytes-node.bytes;node.bytes=node.baseBytes;node.phase='idle';node.error='';}}
    cancel(path){const node=this.get(path);if(!node)return;this.cancelNode(node);this.notify();this.pump();}
    collapse(path){const node=this.get(path);if(!node)return;node.expanded=false;for(const other of this.nodes.values())if(prefix(path,other.path)&&other.pending)this.cancelNode(other);this.page=0;this.notify();this.pump();}
    collapseAll(){for(const node of this.nodes.values()){node.expanded=false;if(node.pending)this.cancelNode(node);}this.page=0;this.notify();this.pump();}
    setBlocked(blocked){
      if(this.blocked===blocked)return;this.blocked=blocked;
      if(blocked)for(const node of this.nodes.values())if(node.pending)this.cancelNode(node);
      this.notify();if(!blocked)this.pump();
    }
    pump(){
      if(!this.current()||this.blocked)return;
      for(const node of [...this.nodes.values()].filter(n=>n.pending&&!n.running).sort((a,b)=>a.ticket-b.ticket)){
        if(this.active>=this.limits.active)break;
        node.running=true;++this.active;this.execute(node);
      }
    }
    validResult(status,node){
      const result=status?.result,q=this.root.query;
      return status.rootJobId===this.root.job&&equal(status.path||[],node.path)&&result?.tree&&equal(result.tree.path||[],node.path)&&result.tree.levels===q.rows.length&&result.tree.hasChildren===(node.path.length+1<q.rows.length)&&Array.isArray(result.rows)&&equal(result.rowFields||[],q.rows.slice(0,node.path.length+1))&&(result.columnKeys||[]).every(col=>this.root.result.columnKeys.includes(col))&&result.rows.every(row=>Array.isArray(row.keys)&&row.keys.length===node.path.length+1&&prefix(node.path,row.keys));
    }
    async execute(node){
      const root=this.root,identity=this.identity(),version=node.version;
      const current=()=>this.current(root)&&identity===this.identity()&&version===node.version&&this.get(node.path)===node&&node.pending;
      // Cancel also forgets any completed response. AnalysisJobs observes a
      // start response even after cancellation, so its late ID is not lost.
      const jobs=new window.ExcelLensAnalysisJobs({api:(url,options)=>this.api(url==='/api/analysis/tree/cancel'?'/api/analysis/tree/clear':url,options),identity:()=>this.identity()+':'+node.version,requestIdentity:()=>root.bookKey,sleep:this.sleep,endpoint:'/api/analysis/tree',label:'层级展开',maxPolls:0,resultFromStatus:status=>status,
        onProgress:status=>{if(current()){node.progress=status.progress||0;node.total=status.total||0;node.phase='running';this.notify('progress',node);}}});
      node.jobs=jobs;node.phase='running';this.notify('progress',node);let completed;
      try{
        completed=await jobs.execute({jobId:root.job,workbookKey:root.bookKey,book:root.book,path:node.path},{main:true});
        if(!current()||!completed.current())return;
        if(!this.validResult(completed.result,node))throw Error('层级结果与当前分组不一致，请重新运行分析');
        const result=completed.result.result,bytes=node.baseBytes+window.ExcelLensAnalysisResults.retainedBytes(result,this.limits.bytes);
        if(bytes>this.limits.bytes||!this.fit(bytes-node.bytes,0,node.path))throw Error('此分组结果超过缓存容量，请缩小分析范围');
        this.bytes+=bytes-node.bytes;node.bytes=bytes;node.result=result;node.phase='done';node.used=++this.clock;
      }catch(error){if(current()&&error.name!=='AbortError'){
        let text=String(error.message||'展开失败');if(text.length>2048)text=text.slice(0,2047).replace(/[\uD800-\uDBFF]$/,'')+'…';
        const bytes=node.baseBytes+window.ExcelLensAnalysisResults.retainedBytes({error:text},this.limits.bytes);
        if(bytes<=this.limits.bytes&&this.fit(bytes-node.bytes,0,node.path)){this.bytes+=bytes-node.bytes;node.bytes=bytes;node.error=text;}else node.error='展开失败，请缩小范围后重试';
        node.phase='error';
      }}
      finally{
        if(completed?.job)this.api('/api/analysis/tree/clear',{method:'POST',body:JSON.stringify({id:completed.job})}).catch(()=>{});
        if(node.jobs===jobs){node.jobs=null;node.running=false;if(version===node.version)node.pending=false;}
        --this.active;if(this.current(root))this.notify();this.pump();
      }
    }
    *rows(result=this.root?.result){
      for(const row of result?.rows||[]){
        const node=this.get(row.keys);yield {kind:'row',row,result,node};
        if(!node?.expanded)continue;
        if(node.result){yield* this.rows(node.result);if(node.result.truncated)yield {kind:'truncated',node};}
        else yield {kind:'status',node};
      }
    }
    pageRows(){
      const rows=[],skip=this.page*this.limits.pageRows;let i=0,more=false;
      for(const item of this.rows()){if(i++<skip)continue;if(rows.length>=this.limits.pageRows){more=true;break;}rows.push(item);}
      if(!rows.length&&this.page>0){this.page=0;return this.pageRows();}
      return {rows,more,first:rows.length?skip+1:0,last:skip+rows.length};
    }
    diagnostics(){return {nodes:this.nodes.size,bytes:this.bytes,active:this.active,queued:[...this.nodes.values()].filter(n=>n.pending&&!n.running).length,page:this.page};}
  }

  class Renderer {
    constructor(){this.statusNodes=new Map();}
    status(node){return node.pending?(node.phase==='queued'?'等待展开…':`正在展开${node.total?' · '+count(node.progress)+' / '+count(node.total):'…'}`):(node.error?'展开失败：'+node.error:'展开已取消');}
    progress(node){const text=this.statusNodes.get(node.key);if(text)text.textContent=this.status(node);}
    render(table,model,{fieldName,metricName,metricCell,drill,formatNumber=cell=>cell?.display||'—'}){
      const root=model.root;if(!root)return {more:false,first:0,last:0};
      const previous=document.activeElement?.dataset?.treeFocus,scroll=table.parentElement?.scrollTop||0;
      table.replaceChildren();table.setAttribute('role','treegrid');table.setAttribute('aria-label','层级透视');this.statusNodes.clear();
      const q=root.query,result=root.result,metrics=q.metrics?.length?q.metrics:[{field:q.value,aggregate:q.aggregate}],columns=q.columns?.length?result.columnKeys:[],headers=[q.rows.map(fieldName).join(' / ')];
      for(const col of columns)for(const metric of metrics)headers.push((col===''?'（空白列）':col)+' · '+metricName(metric));
      for(const metric of metrics)headers.push((columns.length?'小计 · ':'')+metricName(metric));
      const head=document.createElement('thead'),hr=document.createElement('tr');for(const title of headers){const th=document.createElement('th');th.textContent=title;th.scope='col';hr.append(th);}head.append(hr);table.append(head);
      const body=document.createElement('tbody'),page=model.pageRows();
      const button=(text,action,focus)=>{const b=document.createElement('button');b.type='button';b.textContent=text;b.onclick=action;if(focus)b.dataset.treeFocus=focus;return b;};
      const value=(tr,cell,path,col,selected)=>{
        const td=document.createElement('td');td.className='analysis-number';td.title=window.ExcelLensAnalysisComparisons?.title(cell)||cell?.exact||'';
        if(cell?.exact!==undefined&&cell.exact!==''){const b=button(formatNumber(cell),()=>drill(path,col,0,selected));b.className='analysis-drill-link';b.title=td.title+' · 查看当前分组来源明细';b.disabled=model.blocked;td.append(b);}else td.textContent=formatNumber(cell);tr.append(td);
      };
      for(const item of page.rows){
        const tr=document.createElement('tr');
        if(item.kind!=='row'){
          const td=document.createElement('td');td.colSpan=headers.length;td.className='analysis-tree-status';td.style.paddingInlineStart=(18+item.node.path.length*16)+'px';
          const text=document.createElement('span');text.setAttribute('role','status');text.textContent=item.kind==='truncated'?'此层仅显示前 N 项，小计包含未显示项':this.status(item.node);td.append(text);
          if(item.kind==='status'){this.statusNodes.set(item.node.key,text);const b=button(item.node.pending?'取消':'重试',()=>item.node.pending?model.cancel(item.node.path):model.expand(item.node.path),key(['status',item.node.path]));b.className='analysis-quiet-button';b.disabled=model.blocked;td.append(b);}
          tr.append(td);body.append(tr);continue;
        }
        const {row,node}=item,path=row.keys,hasChildren=path.length<q.rows.length;
        tr.className='analysis-tree-row';tr.dataset.treePath=key(path);tr.setAttribute('aria-level',String(path.length));
        if(hasChildren)tr.setAttribute('aria-expanded',String(!!node?.expanded));if(node?.pending)tr.setAttribute('aria-busy','true');
        const td=document.createElement('td');td.className='analysis-tree-label';td.style.paddingInlineStart=(10+(path.length-1)*16)+'px';
        const b=button('',()=>hasChildren?(node?.expanded?model.collapse(path):model.expand(path)):drill(path,'',0,false),key(['row',path]));b.className='analysis-tree-toggle';b.disabled=model.blocked;
        if(hasChildren)b.setAttribute('aria-expanded',String(!!node?.expanded));
        b.setAttribute('aria-label',(hasChildren?(node?.expanded?'收起 ':'展开 '):'查看明细 ')+path.map((v,i)=>fieldName(q.rows[i])+'：'+(v===''?'空白值':v)).join(' / '));
        const arrow=document.createElement('span');arrow.className='analysis-tree-arrow';arrow.setAttribute('aria-hidden','true');arrow.textContent=hasChildren?(node?.expanded?'▾':'▸'):'·';
        const name=document.createElement('span');name.textContent=path.at(-1)===''?'空白':path.at(-1);if(path.at(-1)==='')name.className='analysis-tree-empty';b.append(arrow,name);td.append(b);td.title=path.map(label).join(' / ');
        if(path.length>1){const context=document.createElement('small');context.className='analysis-tree-context';context.textContent=path.slice(0,-1).map(label).join(' / ');td.append(context);}
        b.onkeydown=event=>{
          if(event.key==='ArrowRight'&&hasChildren){event.preventDefault();if(!node?.expanded)model.expand(path);else{const next=[...table.querySelectorAll('.analysis-tree-row')].find(n=>{const p=JSON.parse(n.dataset.treePath);return p.length===path.length+1&&prefix(path,p);});next?.querySelector('.analysis-tree-toggle')?.focus();}}
          else if(event.key==='ArrowLeft'){event.preventDefault();if(node?.expanded)model.collapse(path);else{const parent=[...table.querySelectorAll('.analysis-tree-row')].find(n=>equal(JSON.parse(n.dataset.treePath),path.slice(0,-1)));parent?.querySelector('.analysis-tree-toggle')?.focus();}}
          else if(['ArrowUp','ArrowDown','Home','End'].includes(event.key)){event.preventDefault();const buttons=[...table.querySelectorAll('.analysis-tree-toggle')],i=buttons.indexOf(b),at=event.key==='Home'?0:event.key==='End'?buttons.length-1:Math.max(0,Math.min(buttons.length-1,i+(event.key==='ArrowDown'?1:-1)));buttons[at]?.focus();}
        };
        tr.append(td);for(const col of columns){const index=item.result.columnKeys.indexOf(col);for(let m=0;m<metrics.length;m++)value(tr,index<0?{}:metricCell(item.result,row,index,m),path,col,true);}
        for(let m=0;m<metrics.length;m++)value(tr,metricCell(item.result,row,0,m,true),path,'',false);body.append(tr);
      }
      table.append(body);const foot=document.createElement('tfoot'),total=document.createElement('tr'),caption=document.createElement('td');caption.textContent=result.truncated?'全量总计（含未显示项）':'范围总计';total.append(caption);
      for(let c=0;c<columns.length;c++)for(let m=0;m<metrics.length;m++)value(total,metricCell(result,null,c,m),[],columns[c],true);
      for(let m=0;m<metrics.length;m++)value(total,metricCell(result,null,0,m,true),[],'',false);foot.append(total);table.append(foot);
      if(previous)[...table.querySelectorAll('[data-tree-focus]')].find(n=>n.dataset.treeFocus===previous)?.focus({preventScroll:true});if(table.parentElement)table.parentElement.scrollTop=scroll;
      return page;
    }
  }
  window.ExcelLensAnalysisTree=Tree;window.ExcelLensAnalysisTreeRenderer=Renderer;
})();
