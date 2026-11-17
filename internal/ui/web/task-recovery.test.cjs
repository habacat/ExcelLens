const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const deferred=()=>{let resolve;const promise=new Promise(yes=>resolve=yes);return {promise,resolve};};
function setup(){
  class Node{
    constructor(){this.children=[];this.dataset={};this.listeners={};this.classList={toggle(){}};this.open=false;this.scrollTop=0;}
    append(...nodes){this.children.push(...nodes);}replaceChildren(...nodes){this.children=nodes.flatMap(node=>node.fragment?node.children:[node]);}
    addEventListener(name,fn){(this.listeners[name]??=[]).push(fn);}querySelectorAll(tag){return this.children.flatMap(node=>[...(node.tag===tag?[node]:[]),...node.querySelectorAll(tag)]);}querySelector(){return null;}
    close(){this.open=false;for(const fn of this.listeners.close||[])fn();}focus(){}
  }
  const nodes=new Map(),node=id=>{if(!nodes.has(id))nodes.set(id,new Node());return nodes.get(id);};
  const ctx={window:{},document:{getElementById:node,createElement:tag=>Object.assign(new Node(),{tag}),createDocumentFragment:()=>Object.assign(new Node(),{fragment:true})},AbortController,Intl};
  for(const file of ['task-recovery-model.js','task-recovery.js'])vm.runInNewContext(fs.readFileSync(__dirname+'/'+file,'utf8'),ctx);
  const s={opened:true,book:'book',workbookKey:'hash',sheetEpoch:1},calls=[],adopted=[],notices=[];
  const item={id:'a'.repeat(32),revision:'9007199254740993',driver:'source-search',kind:'query',file:'账目.csv',path:'C:/source/账目.csv',book:'book',workbookKey:'hash',sheet:'数据',query:'金额',mode:'value',scope:'current',updated:'2026-11-17T00:00:00Z',busy:false,complete:false,backup:false,error:''};
  const center=new ctx.window.ExcelLensTaskRecovery({state:s,maximum:32,api:async(url,opts)=>{calls.push({url,body:opts?.body&&JSON.parse(opts.body)});return url==='/api/tasks'?{items:[item]}:{jobId:'own-job',operation:'search'};},toast:x=>notices.push(x),openDialog:id=>node(id).open=true,context:()=>JSON.stringify(s),openSource:async()=>{},canResume:()=>true,adopt:async(...args)=>{adopted.push(args);return true;}});
  return {center,s,calls,adopted,notices,node,item,model:ctx.window.ExcelLensTaskRecoveryModel};
}
test('opening the task directory only lists metadata and preserves precise revisions',async()=>{
  const h=setup();await h.center.open();assert.deepEqual(h.calls.map(x=>x.url),['/api/tasks']);
  await h.center.run(h.center.items[0],'resume');assert.equal(h.calls[1].body.revision,'9007199254740993');assert.equal(h.calls[1].body.book,'book');assert.equal(h.adopted.length,1);
});
test('task actions follow source identity, busy/corrupt state and completed output',()=>{
  const h=setup(),m=h.model;
  assert.equal(m.action(h.item,h.s),'resume');h.s.book='copy-at-another-path';assert.equal(m.action(h.item,h.s),'open');
  assert.equal(m.action({...h.item,busy:true},h.s),'');assert.equal(m.action({...h.item,error:'来源变化'},h.s),'');
  h.s.book='book';assert.equal(m.action({...h.item,driver:'source-export',kind:'export',complete:true},h.s),'download');
  assert.equal(m.action({...h.item,driver:'future'},h.s),'');
});
test('invalid or duplicate catalog entries fail without replacing accepted tasks',async()=>{
  const h=setup();await h.center.open();const accepted=h.center.items;
  for(const item of [{...h.item,revision:9007199254740993},{...h.item,revision:'18446744073709551616'},{...h.item,busy:'false'}])assert.throws(()=>h.model.items({items:[item]},32));
  assert.throws(()=>h.model.items({items:[h.item,h.item]},32));assert.throws(()=>h.model.items({items:[h.item]},0));
  h.center.api=async()=>({items:[{...h.item,revision:1}]});await h.center.load();assert.equal(h.center.items,accepted);assert.match(h.node('taskRecoveryStatus').textContent,/任务/);
});
test('a closed or switched view cancels only the late job created by its own resume',async()=>{
  for(const reason of ['close','source','query']){
    const h=setup();await h.center.open();const gate=deferred();h.center.api=(url,opts)=>{h.calls.push({url,body:JSON.parse(opts.body)});return url==='/api/tasks/resume'?gate.promise:Promise.resolve({});};
    const pending=h.center.run(h.center.items[0],'resume');
    if(reason==='close')h.center.close();else if(reason==='source')h.s.book='other';else h.s.sheetEpoch++;
    gate.resolve({jobId:'late-own-job',operation:'search'});await pending;
    assert.equal(h.adopted.length,0);assert.deepEqual(h.calls.at(-1),{url:'/api/search/cancel',body:{id:'late-own-job'}});assert.equal(h.center.busy,false);
  }
});
test('another running intent prevents resume and failed adoption releases its own export',async()=>{
  const h=setup();await h.center.open();h.center.canResume=()=>false;await h.center.run(h.center.items[0],'resume');assert.equal(h.calls.length,1);
  const item=h.center.items[0];Object.assign(item,{kind:'export',driver:'source-export',complete:true});h.center.canResume=()=>true;h.center.adopt=async()=>false;
  h.center.api=async(url,opts)=>{h.calls.push({url,body:JSON.parse(opts.body)});return {jobId:'output-own-job',operation:'export'};};
  await h.center.run(item,'download');assert.equal(h.calls.at(-1).url,'/api/export/cancel');assert.equal(h.calls.at(-1).body.id,'output-own-job');
});
test('explicit source opening never resumes, locks close during install and changes actions on success',async()=>{
  const h=setup();h.s.book='other';await h.center.open();const gate=deferred();let openings=0;
  h.center.openSource=async(payload,current)=>{openings++;assert.equal(payload.book,'other');await gate.promise;assert(current());h.s.book='book';return true;};
  const pending=h.center.run(h.center.items[0],'open');h.center.close();assert.equal(h.node('taskRecoveryDialog').open,true);assert.equal(openings,1);
  gate.resolve();await pending;assert.deepEqual(h.calls.map(x=>x.url),['/api/tasks']);assert.equal(h.model.action(h.center.items[0],h.s),'resume');assert.equal(h.center.opening,false);
});
test('stale list responses and old row handlers cannot replace or remove newer tasks',async()=>{
  const h=setup(),gate=deferred();h.center.api=()=>gate.promise;const first=h.center.open();h.center.close();
  h.center.api=async()=>({items:[{...h.item,id:'b'.repeat(32)}]});await h.center.open();gate.resolve({items:[h.item]});await first;
  assert.equal(h.center.items[0].id,'b'.repeat(32));const count=h.calls.length;await h.center.run(h.item,'remove');assert.equal(h.calls.length,count);
});
test('removing a task uses its expected revision and leaves source state alone',async()=>{
  const h=setup();await h.center.open();const before=JSON.stringify(h.s);await h.center.run(h.center.items[0],'remove');
  assert.equal(h.calls.at(-1).url,'/api/tasks/discard');assert.equal(h.calls.at(-1).body.revision,h.item.revision);assert.equal(h.center.items.length,0);assert.equal(JSON.stringify(h.s),before);
});
