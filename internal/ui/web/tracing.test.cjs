const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const read=name=>fs.readFileSync(__dirname+'/'+name,'utf8'),tick=()=>new Promise(r=>setImmediate(r));
function harness(apiOverride){
  let now=10000,next=0;const timers=new Map(),closed=[],calls=[],notices=[];let traced=[];
  class Node {
    constructor(cls=''){this.className=cls;this.hidden=true;this.open=false;this.children=[];this.handlers={};this.style={};this.attrs={};this.dataset={};this.clientWidth=1000;this.scrollTop=0;}
    addEventListener(k,f){(this.handlers[k]??=[]).push(f);}emit(k,e={}){for(const f of this.handlers[k]||[])f(e);}
    setAttribute(k,v){this.attrs[k]=v;}append(...nodes){for(const n of nodes){if(n.fragment)this.append(...n.children);else{n.parentElement=this;this.children.push(n);}}}replaceChildren(...nodes){this.children=[];this.append(...nodes);}
    querySelectorAll(q){const cls=q.slice(1),out=[];for(const n of this.children){if(n.className===cls)out.push(n);out.push(...n.querySelectorAll(q));}return out;}querySelector(q){return this.querySelectorAll(q)[0]||null;}
    remove(){if(this.parentElement)this.parentElement.children=this.parentElement.children.filter(n=>n!==this);}show(){this.open=true;}close(){this.open=false;closed.push(()=>this.emit('close'));}focus(){}
    getBoundingClientRect(){return {left:0,top:0,right:1000,bottom:600,width:1000,height:600};}
  }
  const ids=['panel','dock','layer','grid','title','formula','summary','list','scopeRow','scope','cancel','minimize','restore','origin','end'],o=Object.fromEntries(ids.map(id=>[id,new Node()]));o.scope.value='current';o.layer.parentElement=new Node();
  const cells=[{row:1,col:1,ref:'A1'},{row:3,col:3,ref:'C3'}].map(p=>Object.assign(new Node('grid-cell'),{dataset:{row:String(p.row),col:String(p.col),ref:p.ref}}));
  const parseRef=ref=>{const m=/^([A-Z])(\d+)$/.exec(ref);return m?{ref,row:Number(m[2]),col:m[1].charCodeAt(0)-64}:null;},ref=address=>{const p=parseRef(address);return {sheet:'源表',label:'源表!'+address,start:p,end:p};};
  o.state={workbookKey:'book-1',sheet:'源表',selected:{sheet:'源表',ref:'C3',row:3,col:3,hasFormula:true,formula:'A1+B2'}};
  o.api=async(path,options)=>{calls.push({path,body:options?.body&&JSON.parse(options.body)});const r=apiOverride?.(path,options);if(r!==undefined)return r;if(path==='/api/search/start')return {jobId:'job',cached:true};if(path.startsWith('/api/search/status'))return {status:'done',matches:[{sheet:'源表',ref:'A1',formula:'C3+1'}]};return {};};
  o.parse=()=>[ref('A1'),ref('B2')];o.parseRef=parseRef;o.getCell=async()=>({display:'12'});o.setRefs=refs=>{traced=refs;};o.bounds=(row,col)=>({row,col,endRow:row,endCol:col});o.visibleRect=node=>({left:Number(node.dataset.col)*100,top:Number(node.dataset.row)*25,right:Number(node.dataset.col)*100+100,bottom:Number(node.dataset.row)*25+25,pane:'body'});
  o.navigate=async(sheet,address,guard)=>{if(guard())o.state.selected={sheet,...parseRef(address)};};o.atTarget=r=>o.state.selected.sheet===r.sheet&&o.state.selected.ref===r.start.ref;o.notify=(...args)=>notices.push(args);
  const context={window:{addEventListener(){},ExcelLensMotion:{enter(){},stop(){},exit:async()=>true}},document:{createElement:()=>new Node(),createDocumentFragment:()=>Object.assign(new Node(),{fragment:true}),querySelectorAll:()=>cells},Date:{now:()=>now},setTimeout:(fn,ms)=>{const id=++next;timers.set(id,{fn,at:now+ms});return id;},clearTimeout:id=>timers.delete(id),requestAnimationFrame:fn=>{const id=++next;timers.set(id,{fn,at:now+16});return id;},cancelAnimationFrame:id=>timers.delete(id)};
  vm.runInNewContext(read('tracing.js'),context);const trace=new context.window.ExcelLensTracing(o);
  function advance(ms){const target=now+ms;for(;;){const first=[...timers].filter(([,t])=>t.at<=target).sort((a,b)=>a[1].at-b[1].at)[0];if(!first)break;now=first[1].at;timers.delete(first[0]);first[1].fn();}now=target;}
  return {trace,o,calls,notices,timers,context,advance,ref,flushClose(){while(closed.length)closed.shift()();},get traced(){return traced;}};
}
test('both directions share result rows; minimized results restore without another request and return to origin',async()=>{
  const h=harness();await h.trace.open('precedents');assert.equal(h.o.list.children[0].className,'trace-item');assert(h.o.panel.open);assert.equal(h.trace.refs.length,2);
  await h.trace.locate(h.ref('A1'));assert.equal(h.o.dock.hidden,false);assert.equal(h.o.panel.open,false);assert.equal(h.trace.source.ref,'C3');
  h.trace.restore();h.flushClose();assert(h.o.panel.open);assert.equal(h.trace.refs.length,2);assert.equal(h.calls.length,0);
  await h.trace.goOrigin();assert.equal(h.o.state.selected.ref,'C3');assert.equal(h.trace.source.ref,'C3');assert.equal(h.o.layer.hidden,false);
  h.flushClose();await h.trace.open('dependents');assert.equal(h.o.list.children[0].className,'trace-item');assert.equal(h.o.scopeRow.hidden,false);assert.match(h.o.title.textContent,/追踪从属/);
});
test('arrival stays for two seconds; stale expiry and scrolling do not hide or extend a newer cue',async()=>{
  const h=harness();await h.trace.open('precedents');const old=h.timers.get(h.trace.pulseTimer).fn;assert.equal(h.o.layer.hidden,false);h.advance(1000);
  await h.trace.locate(h.ref('A1'));old();assert.equal(h.o.layer.hidden,false);h.trace.refreshPulse();h.advance(1999);assert.equal(h.o.layer.hidden,false);h.advance(1);assert.equal(h.o.layer.hidden,true);assert.equal(h.traced.length,2);
});
test('a queued dialog-close event and an old exit animation cannot erase restored tracing',async()=>{
  const h=harness();await h.trace.open('precedents');let finish;h.context.window.ExcelLensMotion.exit=()=>new Promise(r=>finish=r);
  const pending=h.trace.minimize();h.trace.restore();finish(true);await pending;assert(h.o.panel.open);assert.equal(h.o.dock.hidden,true);assert.equal(h.trace.refs.length,2);
  h.context.window.ExcelLensMotion.exit=async()=>true;await h.trace.minimize();h.trace.restore();h.flushClose();assert(h.o.panel.open);assert.equal(h.trace.source.ref,'C3');
});
test('late searches are discarded and only owned jobs are cancelled; truncation and failure remain explicit',async()=>{
  let release;const pending=new Promise(r=>release=r),h=harness(path=>path==='/api/search/start'?pending:undefined);
  const old=h.trace.open('dependents');h.trace.reset();await h.trace.open('precedents');release({jobId:'obsolete',reused:false});await old;assert(h.calls.some(c=>c.path==='/api/search/cancel'&&c.body.id==='obsolete'));assert.match(h.o.title.textContent,/追踪引用/);
  const reused=harness(path=>path==='/api/search/start'?{jobId:'shared',reused:true}:path.startsWith('/api/search/status')?{status:'running',matches:[],progress:10}:undefined);const run=reused.trace.open('dependents');await tick();reused.trace.reset();await run;assert(!reused.calls.some(c=>c.path==='/api/search/cancel'));
  const truncated=harness(path=>path.startsWith('/api/search/status')?{status:'done',matches:[],truncated:true}:undefined);await truncated.trace.open('dependents');assert.match(truncated.o.summary.textContent,/截断/);
  const failed=harness(path=>path.startsWith('/api/search/status')?{status:'error',matches:[],error:'索引失败'}:undefined);await failed.trace.open('dependents');assert.match(failed.o.summary.textContent,/未完成.*索引失败/);
});
test('an interrupted navigation cannot show a cue for the previous destination',async()=>{
  const h=harness();await h.trace.open('precedents');let finish;h.o.navigate=async(sheet,ref,guard)=>{await new Promise(r=>finish=r);if(guard())h.o.state.selected={sheet,...h.o.parseRef(ref)};};
  const pending=h.trace.locate(h.ref('A1'));await tick();h.trace.selectionChanged();finish();await pending;assert.equal(h.o.state.selected.ref,'C3');assert.equal(h.o.layer.hidden,true);
});
test('a search completing after a partial-result jump does not replace or prolong the arrival cue',async()=>{
  let polls=0,release;const h=harness(path=>path.startsWith('/api/search/status')?++polls===1?{status:'running',matches:[{ref:'A1',formula:'C3'}]}:new Promise(resolve=>release=resolve):undefined);
  const search=h.trace.open('dependents');await tick();await h.trace.locate(h.ref('A1'));const deadline=h.trace.pulseUntil;h.advance(250);await tick();release({status:'done',matches:[{ref:'A1',formula:'C3'}]});await search;
  assert.equal(h.trace.pulseUntil,deadline);assert.match(h.trace.pulseLabel,/源表!A1/);h.advance(1749);assert.equal(h.o.layer.hidden,false);h.advance(1);assert.equal(h.o.layer.hidden,true);
});
test('the application rejects delayed navigation after a sheet, workbook or trace intent changes',async()=>{
  const source=read('app.js'),start=source.indexOf('  async function navigateToCell('),end=source.indexOf('  async function getCell(',start);
  for(const change of ['guard','sheet','book']){
    let release,allowed=true,selected=0;const S={workbookKey:'old',sheet:'源表',sheetEpoch:1,selected:null,filtered:false,maxRow:100,maxCol:20,cellMap:new Map([['A1',{raw:'12'}]]),frozenCellMap:new Map()},grid={clientWidth:1000,clientHeight:600,scrollLeft:0,scrollTo(){},focus(){}};
    const ctx={S,Map,Math,ROW_HEAD:26,$:()=>grid,parseRef:()=>({ref:'A1',row:1,col:1}),activeMerge:()=>null,frozenPanes:{effective:()=>({rows:0,cols:0})},ensureExtent(){},rowTop:()=>0,colLeft:()=>0,rowHeight:()=>25,colWidth:()=>100,headerHeight:()=>27,getVirtualScrollTop:()=>0,scrollTopToPhysical:v=>v,sourceRowLimit:()=>50000000,updateCanvasSize(){},loadViewport:()=>new Promise(r=>release=r),selectCell:()=>selected++,recordVisit(){}};
    ctx.sourceRowTop=()=>0;
    vm.runInNewContext(source.slice(start,end)+';this.navigate=navigateToCell;',ctx);const request=ctx.navigate('源表','A1',true,()=>allowed);await tick();if(change==='guard')allowed=false;if(change==='sheet')S.sheetEpoch++;if(change==='book')S.workbookKey='new';release();await request;assert.equal(selected,0,change);
  }
});
