const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const tick=()=>new Promise(resolve=>setImmediate(resolve));
function fixture(){
  const nodes=new Map(),calls=[];let request={startCol:3,cols:10,displayStart:3,displayEnd:40,row:3,limit:38,visibleStart:3,visibleEnd:38,visibleColStart:3,visibleColEnd:12};
  const node=id=>{if(!nodes.has(id))nodes.set(id,{hidden:true,style:{},replaceChildren(){}});return nodes.get(id);};
  const state={indexReady:true,sheet:'A',sheetEpoch:1,filtered:false,filterId:'',freeze:{rows:2,cols:2},rowView:{id:'view-a'},frozenCellMap:new Map(),hiddenColumns:new Set()};
  const options={state,grid:{clientHeight:900,clientWidth:1200,scrollLeft:0},rowHeight:()=>24,colWidth:()=>100,rowTop:r=>(r-1)*24,colLeft:c=>(c-1)*100,headerHeight:()=>27,rowHead:26,getRequest:()=>request,
    api(url,{signal}){return new Promise((resolve,reject)=>{calls.push({url,signal,resolve,reject});signal.addEventListener('abort',()=>reject(signal.reason),{once:true});});}};
  const context={window:{},document:{getElementById:node},AbortController,URLSearchParams,Map,Set,Math,setTimeout,clearTimeout};
  vm.runInNewContext(fs.readFileSync(__dirname+'/freeze.js','utf8'),context);
  const panes=new context.window.ExcelLensFrozenPanes(options);
  const pending=promise=>{promise.catch(()=>{});return promise;};
  return {state,options,panes,calls,pending,move(){request={...request,displayStart:103,displayEnd:140,row:103,visibleStart:103,visibleEnd:138};}};
}
test('identical frozen refreshes share the active read during a warm sheet handoff',async()=>{
  for(const force of [false,true]){
    const h=fixture(),first=h.pending(h.panes.refresh(true));await tick();const second=h.pending(h.panes.refresh(force));
    try{assert.equal(h.calls.length,3,'The same corner/top/left read must not be restarted');assert(h.calls.every(call=>!call.signal.aborted),'A scheduled refresh must not abort the sheet-opening refresh');}
    finally{h.panes.reset();await Promise.allSettled([first,second]);}
  }
});
test('a superseded frozen read does not turn a current sheet into a read failure',async()=>{
  const h=fixture(),first=h.pending(h.panes.refresh(true));await tick();h.move();const second=h.pending(h.panes.refresh());
  try{await first;assert(h.calls.slice(0,3).every(call=>call.signal.aborted));assert(h.calls.slice(3).every(call=>!call.signal.aborted));assert.equal(h.state.frozenCellMap.size,0);}
  finally{h.panes.reset();await Promise.allSettled([first,second]);}
});
test('disabling effective frozen panes cancels an in-flight frozen read',async()=>{
  const h=fixture(),first=h.pending(h.panes.refresh(true));await tick();h.state.freeze={rows:0,cols:0};
  try{await h.panes.refresh();assert(h.calls.every(call=>call.signal.aborted));await first;assert.equal(h.state.frozenCellMap.size,0);}
  finally{h.panes.reset();await Promise.allSettled([first]);}
});
test('a current frozen read still propagates genuine source failures',async()=>{
  const h=fixture(),reading=h.pending(h.panes.refresh(true));h.calls[0].reject(Error('source CRC mismatch'));
  await assert.rejects(reading,/source CRC mismatch/);assert(h.calls.every(call=>call.signal.aborted),'A failed part releases its sibling reads');h.panes.reset();
});
