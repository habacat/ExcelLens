const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const load=file=>fs.readFileSync(__dirname+'/'+file,'utf8'),tick=()=>new Promise(resolve=>setImmediate(resolve));
const colName=col=>String.fromCharCode(64+col),limits={cells:400000,columns:16,segments:60,requestBytes:8192,bytes:4<<20};
function summary(values){
  const items=values.filter(x=>x.value!==null).map(x=>({...x,n:BigInt(x.value.replace('.',''))}));
  // Fixture literals have two decimal places.
  const sum=items.reduce((n,x)=>n+x.n,0n),min=items.reduce((a,b)=>a.n<=b.n?a:b,items[0]),max=items.reduce((a,b)=>a.n>=b.n?a:b,items[0]);
  return {count:items.length,sum:sum+'/100',average:items.length?sum+'/'+(100n*BigInt(items.length)):'',minimum:min?.value||'',maximum:max?.value||'',minimumRow:min?.row||0,maximumRow:max?.row||0,...(!items.length?{sum:'0'}:{})};
}
function response(req){
  const source=[[null,'9007199254740993.31','-0.01','0.00'],[null,'9007199254740993.30','0.01',null]],start=req.skipFirstRow?1:0;
  const series=raw=>{const values=raw.map((value,i)=>({value,row:i+1})).slice(start);return {summary:summary(values),segments:values.map((v,i)=>({first:i+1,last:i+1,firstRow:v.row,lastRow:v.row,summary:summary([v])}))};};
  const columns=source.map((raw,i)=>{const s=series(raw),numbers=raw.slice(start).filter(v=>v!==null),counts={number:numbers.length,negative:numbers.filter(v=>v[0]==='-').length,zero:numbers.filter(v=>v==='0.00').length,positive:numbers.filter(v=>v!=='0.00'&&v[0]!=='-').length,blank:i===0?0:1,other:start?0:1,errors:0,missing:0,invalid:0};return {column:i+1,...s,counts};});
  const page={...req,selectedRows:4,rows:4-start,firstColumn:1,lastColumn:2,columns};
  if(req.minuend){const raw=req.minuend===2?[null,'-0.01','0.02',null]:[null,'0.01','-0.02',null];page.difference={...series(raw),paired:2,unpaired:2-start};}
  return page;
}
function harness(override){
  const nodes=new Map(),events={},calls=[],html=[],charts=[],toasts=[],navigations=[];
  class Node{
    constructor(tag='div'){this.tagName=tag;this.children=[];this.listeners={};this.value='';this.disabled=false;this.hidden=false;this.open=false;this.classList={toggle(){}};}
    set id(id){this._id=id;nodes.set(id,this);}get id(){return this._id;}
    set textContent(value){this.text=String(value);this.children=[];}get textContent(){return (this.text||'')+this.children.map(n=>n.textContent).join('');}
    set innerHTML(value){html.push(value);for(const m of value.matchAll(/<([a-z]+)\b[^>]*\bid="([^"]+)"[^>]*>/g)){const n=new Node(m[1]);n.id=m[2];}}
    append(...nodes){for(const node of nodes)if(node.tagName==='#fragment')this.append(...node.children);else this.children.push(node);}
    replaceChildren(...nodes){this.text='';this.children=[];this.append(...nodes);}
    addEventListener(type,fn){(this.listeners[type]??=[]).push(fn);}dispatch(type,event={}){for(const f of this.listeners[type]||[])f(event);}
    setAttribute(){}showModal(){this.open=true;}close(){this.open=false;queueMicrotask(()=>this.dispatch('close'));}focus(){}
  }
  const document={body:new Node(),createElement:tag=>new Node(tag),createDocumentFragment:()=>new Node('#fragment'),getElementById:id=>nodes.get(id),addEventListener(type,fn){(events[type]??=[]).push(fn);}};
  const state={opened:true,indexReady:true,index:{maxRow:4,maxCol:2},workbookKey:'content',book:'path',sheet:'<img src=x>',fileName:'<script>bad</script>',sheetEpoch:1,rowView:{id:'view'},filtered:false,range:{row1:1,row2:4,col1:1,col2:2,visual1:0,visual2:0}};
  const env={window:{addEventListener:(type,fn)=>{(events[type]??=[]).push(fn);}},document,TextEncoder,AbortController,queueMicrotask,Option:function(text,value){const n=new Node('option');n.textContent=text;n.value=value;return n;}};
  for(const file of ['multiselection.js','analysis-numbers.js','selection-quick-model.js','selection-quick.js'])vm.runInNewContext(load(file),env);
  env.window.ExcelLensAnalyticsCharts={draw(canvas,data,mode,onPick){charts.push({data,mode,onPick});}};
  let pinned=0;
  const quick=new env.window.ExcelLensSelectionQuick({state,colName,limits,toast:(...x)=>toasts.push(x),pinnedThroughRow:()=>pinned,
    api:async(url,init)=>{const req=JSON.parse(init.body);calls.push({url,init,req});const result=override?.(req,init);return result===undefined?response(req):result;},
    navigate:async(sheet,ref,guard)=>{if(guard()){navigations.push({sheet,ref});state.range=null;state.selected={row:2,col:1};assert(guard(),'navigation must survive its own selection update');}}
  });
  return {quick,state,nodes,calls,html,charts,toasts,navigations,events,model:env.window.ExcelLensSelectionQuickModel,pinned:n=>pinned=n};
}

test('quick selection only reads on explicit run, preserves exact values, and renders hostile source labels as text',async()=>{
  const h=harness();h.quick.open();assert.equal(h.calls.length,0);assert.match(h.nodes.get('selectionQuickSource').textContent,/<img src=x>/);await h.quick.run();assert.equal(h.calls.length,1);
  assert.equal(h.quick.page.columns[0].segments[1].summary.maximum,'9007199254740993.31');assert(h.html.every(x=>!x.includes('<script>bad')&&!x.includes('<img src=x>')));assert.match(h.nodes.get('selectionQuickBody').textContent,/公式无结果/);assert.equal(h.charts.at(-1).data.rows[0].values[0].exact,'');
  h.nodes.get('selectionQuickMinuend').value='2';h.nodes.get('selectionQuickSubtrahend').value='1';h.nodes.get('selectionQuickSkip').checked=true;await h.quick.run();
  assert.equal(h.quick.page.rows,3);assert.equal(h.quick.page.difference.paired,2);assert.equal(h.quick.page.minuend,2);assert.equal(h.quick.page.subtrahend,1);assert.match(h.nodes.get('selectionQuickPairing').textContent,/未配对 1 行/);
});
test('cancel and immediate retry ignore a late response even when transport ignores abort',async()=>{
  let hold=false,resolve;const h=harness(req=>hold?new Promise(r=>{resolve=()=>r(response(req));}):undefined);h.quick.open();await h.quick.run();hold=true;const pending=h.quick.run();await tick();h.quick.cancel();hold=false;await h.quick.run();const accepted=h.quick.page;resolve();await pending;
  assert.equal(h.quick.page,accepted);assert.equal(h.calls[1].init.signal.aborted,true);assert.equal(h.quick.busy,false);
});
test('failed, forged and incomplete candidates preserve the previous complete result',async()=>{
  let mode='';const h=harness(req=>{if(mode==='fail')throw Error('读取失败');if(mode){const p=response(req);if(mode==='foreign')p.book='foreign';else p.columns[0].segments.pop();return p;}});h.quick.open();await h.quick.run();const accepted=h.quick.page;
  for(const value of ['fail','foreign','partial']){mode=value;await h.quick.run();assert.equal(h.quick.page,accepted);assert.match(h.nodes.get('selectionQuickStatus').textContent,/保留上次结果/);assert.equal(h.quick.busy,false);}
});
test('source, index, visibility, filter, freeze and selection changes cancel and clear accepted results',async()=>{
  for(const change of [h=>h.state.book='other-path',h=>h.state.sheetEpoch++,h=>h.state.index={...h.state.index},h=>h.state.rowView={id:'new'},h=>h.state.filtered=true,h=>h.pinned(1),h=>h.state.range.row2=3,h=>h.state.opened=false]){
    const h=harness();h.quick.open();await h.quick.run();change(h);h.quick.changed();assert.equal(h.quick.page,null);assert.equal(h.quick.dialog.open,false);
  }
});
test('held responses after close or a changed selection cannot publish and prevent no later retry',async()=>{
  let hold=true,resolve;const h=harness(req=>hold?new Promise(r=>{resolve=()=>r(response(req));}):undefined);h.quick.open();const pending=h.quick.run();await tick();h.state.range.row2=3;resolve();await pending;assert.equal(h.quick.page,null);
  h.quick.close();await tick();h.state.range.row2=4;hold=false;h.quick.open();await h.quick.run();assert(h.quick.page);
});
test('number and theme repaint and series switching never read the source again',async()=>{
  const h=harness();h.quick.open();await h.quick.run();const accepted=JSON.stringify(h.quick.page);h.nodes.get('selectionQuickSeries').value='2';h.quick.renderSeries();
  for(const type of ['excellens:readingchange','excellens:analysisnumberchange','resize'])for(const fn of h.events[type]||[])fn();
  assert.equal(h.charts.at(-1).data.rows[1].keys[0],'2');h.charts.at(-1).onPick(['2']);assert.match(h.nodes.get('selectionQuickStatus').textContent,/第 2 段.*选中行 2–2.*源行 2 → 2/);
  assert.equal(h.calls.length,1);assert.equal(JSON.stringify(h.quick.page),accepted);assert(h.charts.length>=4);
});
test('source position navigation is bound to its accepted source and survives its own selection update',async()=>{
  const h=harness();h.quick.open();await h.quick.run();await h.quick.locate('A2');assert.equal(h.navigations[0].ref,'A2');assert.equal(h.quick.dialog.open,false);
});
test('multi-area and oversized selections reject before requesting data and equal columns reject before comparison',async()=>{
  for(const change of [h=>h.state.multiRanges=[{...h.state.range}],h=>h.state.range.col2=17,h=>{h.state.index.maxRow=400001;h.state.range.row2=400001;}]){const h=harness();change(h);h.quick.open();assert.equal(h.quick.dialog.open,false);assert.equal(h.calls.length,0);assert.equal(h.toasts.length,1);}
  const h=harness();h.quick.open();h.nodes.get('selectionQuickMinuend').value='1';h.nodes.get('selectionQuickSubtrahend').value='1';await h.quick.run();assert.equal(h.calls.length,0);assert.match(h.nodes.get('selectionQuickStatus').textContent,/两个不同/);
});
test('model rejects corrupt partitions, source rows, summary types and segment coverage as whole results',()=>{
  const h=harness(),req={workbookKey:'content',book:'path',sheet:'sheet',viewId:'view',filterId:'',start:'A1',end:'B4',skipFirstRow:false,minuend:2,subtrahend:1};
  const validate=p=>h.model.validate(p,req,limits,4,1,2,4);assert(validate(response(req)));
  for(const change of [p=>p.columns[0].counts.zero++,p=>p.columns[0].summary.sum=4,p=>p.columns[0].segments[0].first=0,p=>p.columns[0].segments[1].summary.minimumRow=5,p=>p.columns[0].segments[0].summary.average='0',p=>p.difference.unpaired++,p=>p.skipFirstRow=true,p=>p.visualStart=1,p=>p.selectedRows=3,p=>p.columns.reverse()]){const p=response(req);change(p);assert.throws(()=>validate(p));}
});
