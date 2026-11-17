const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const tick=()=>new Promise(r=>setImmediate(r));
const gate=()=>{let resolve;const promise=new Promise(r=>resolve=r);return {promise,resolve};};
async function until(predicate){for(let i=0;i<200;i++){if(predicate())return;await tick();}throw Error('model did not settle');}
function setup(limits={}){
  const ctx={window:{},AbortController,encodeURIComponent},state={bookKey:'content',book:'path',opened:true},calls=[],started=new Map(),messages=[],changes=[];
  for(const name of ['analysis-results.js','analysis-jobs.js','analysis-tree.js'])vm.runInNewContext(fs.readFileSync(__dirname+'/'+name,'utf8'),ctx);
  const query={source:'wb:来源',layout:'tree',rows:['C1','C2','C3'],columns:['C4'],aggregate:'sum',value:'C5'},result={tree:{path:[],levels:3,hasChildren:true},rowFields:['C1'],columnKeys:['','X'],grand:{exact:'9007199254740993.31'},rows:['甲','乙','丙','丁'].map(value=>({keys:[value]}))};
  const root={...state,job:'root',query,result};
  const reply=(request,changes={})=>({status:'done',rootJobId:request.jobId,path:request.path,result:{tree:{path:request.path,levels:3,hasChildren:request.path.length+1<3},rowFields:query.rows.slice(0,request.path.length+1),columnKeys:['X'],rows:['1','001'].map(value=>({keys:[...request.path,value],total:{exact:'1'}})),grand:{exact:'2'}},...changes});
  let next=0,handler;
  const api=async(url,options={})=>{
    const data=options.body?JSON.parse(options.body):null;calls.push({url,data});
    if(handler){const value=handler(url,data);if(value!==undefined)return value;}
    if(url.endsWith('/start')){const id='child-'+(++next);started.set(id,data);return {jobId:id};}
    if(url.includes('/status?'))return reply(started.get(new URL(url,'http://local').searchParams.get('id')));
    return {};
  };
  const tree=new ctx.window.ExcelLensAnalysisTree({api,context:()=>state,limits:{nodes:8,bytes:100000,pageRows:2,active:2,...limits},sleep:tick,onChange:event=>changes.push(event),onMessage:message=>messages.push(message)});tree.setRoot(root);
  return {tree,state,root,query,result,calls,messages,changes,reply,started,setHandler:value=>handler=value};
}
test('collapsed branches reuse bounded data while root totals and source queries remain immutable',async()=>{
  const h=setup(),node=h.tree.expand(['甲']);await until(()=>node.phase==='done'&&!node.running);
  assert.equal(h.tree.nodes.size,1);assert.equal(h.calls.filter(c=>c.url.endsWith('/start')).length,1);assert.equal(h.calls.filter(c=>c.url.endsWith('/clear')).length,1);
  assert.equal(JSON.stringify(node.result.rows[1].keys),'["甲","001"]');h.tree.collapse(['甲']);h.tree.expand(['甲']);await tick();
  assert.equal(h.calls.filter(c=>c.url.endsWith('/start')).length,1);assert.equal(h.result.grand.exact,'9007199254740993.31');assert.equal(h.query.sourceId,undefined);
  assert.equal(h.calls[0].data.jobId,'root');assert.equal(h.calls[0].data.book,'path');assert.equal(h.calls[0].data.query,undefined);
});
test('cancel then immediate retry keeps the newer node intent when a start ID arrives late',async()=>{
  const h=setup(),held=gate();let starts=0;
  h.setHandler((url,data)=>{if(url.endsWith('/start')&&starts++===0)return held.promise;});
  const node=h.tree.expand(['甲']);await tick();h.tree.cancel(['甲']);h.tree.expand(['甲']);held.resolve({jobId:'late-cancelled'});
  await until(()=>node.phase==='done'&&!node.running);assert.equal(starts,2);assert.equal(node.error,'');
  assert(h.calls.some(c=>c.url.endsWith('/clear')&&c.data.id==='late-cancelled'));assert.equal(h.tree.active,0);
});
test('old root completion cannot publish and its exit wakes the new root queue',async()=>{
  const h=setup({active:1}),held=gate();let first=true;
  h.setHandler(url=>{if(url.endsWith('/start')&&first){first=false;return held.promise;}});
  h.tree.expand(['甲']);await tick();h.tree.setRoot({...h.root,job:'new-root'});const current=h.tree.expand(['乙']);assert.equal(h.tree.active,1);assert.equal(current.phase,'queued');
  held.resolve({jobId:'old-root-late'});await until(()=>current.phase==='done'&&!current.running);
  assert.equal(h.tree.get(['甲']),null);assert.equal(h.tree.root.job,'new-root');assert(h.calls.some(c=>c.url.endsWith('/clear')&&c.data.id==='old-root-late'));assert.equal(h.tree.nodes.size,1);
});
test('same content at a different path invalidates the old source without keeping a late response',async()=>{
  const h=setup(),held=gate();h.setHandler(url=>url.endsWith('/start')?held.promise:undefined);const node=h.tree.expand(['甲']);h.state.book='another-path';held.resolve({jobId:'old-path'});await until(()=>h.tree.active===0);
  assert.equal(node.result,null);assert(h.calls.some(c=>c.url.endsWith('/clear')&&c.data.id==='old-path'));
});
test('client queue bounds simultaneous tasks and cancelling a waiting branch never starts it',async()=>{
  const h=setup({active:1}),held=gate();let first=true;
  h.setHandler(url=>{if(url.endsWith('/start')&&first){first=false;return held.promise;}});
  const a=h.tree.expand(['甲']),b=h.tree.expand(['乙']),c=h.tree.expand(['丙']);h.tree.cancel(['丙']);held.resolve({jobId:'first'});
  h.started.set('first',{jobId:'root',path:['甲']});await until(()=>a.phase==='done'&&b.phase==='done'&&h.tree.active===0);
  assert.equal(c.phase,'idle');assert.equal(h.calls.filter(call=>call.url.endsWith('/start')).length,2);assert.equal(h.tree.diagnostics().queued,0);
});
test('LRU eviction removes a whole subtree without changing its root result',async()=>{
  const h=setup({nodes:3}),a=h.tree.expand(['甲']);await until(()=>a.phase==='done');const leaf=h.tree.expand(['甲','1']);await until(()=>leaf.phase==='done');
  const b=h.tree.expand(['乙']);await until(()=>b.phase==='done');const c=h.tree.expand(['丙']);await until(()=>c.phase==='done');
  assert.equal(h.tree.get(['甲']),null);assert.equal(h.tree.get(['甲','1']),null);assert(h.tree.nodes.size<=3);assert.equal(h.result.rows.length,4);assert.equal(h.result.grand.exact,'9007199254740993.31');
});
test('oversized results and mismatched response ownership become explicit branch errors',async()=>{
  for(const kind of ['size','owner','path','column']){
    const h=setup({bytes:6000});h.setHandler(url=>{if(url.includes('/status?')){const request=[...h.started.values()][0],response=h.reply(request);if(kind==='size')response.result.rows[0].keys[1]='大'.repeat(10000);if(kind==='owner')response.rootJobId='other';if(kind==='path')response.result.tree.path=['乙'];if(kind==='column')response.result.columnKeys=['unexpected'];return response;}});
    const node=h.tree.expand(['甲']);await until(()=>!node.running&&!node.pending);
    assert.equal(node.result,null);assert.equal(node.phase,'error');assert.match(node.error,kind==='size'?/缓存容量/:/不一致/);assert(h.tree.bytes<=6000);assert(h.calls.some(c=>c.url.endsWith('/clear')));
  }
});
test('pages enumerate only loaded branches and collapsing cancels work without querying more source rows',async()=>{
  const h=setup(),first=h.tree.pageRows();assert.equal(first.rows.length,2);assert.equal(first.more,true);assert.equal(h.calls.length,0);
  h.tree.page=1;assert.equal(h.tree.pageRows().rows[0].row.keys[0],'丙');const node=h.tree.expand(['甲']);await until(()=>node.phase==='done');
  assert.equal(h.tree.page,0);assert.equal([...h.tree.rows()].length,6);h.tree.collapseAll();assert.equal([...h.tree.rows()].length,4);assert.equal(h.calls.filter(c=>c.url.endsWith('/start')).length,1);
});
