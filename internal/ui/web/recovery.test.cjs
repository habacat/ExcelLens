const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm'),{randomUUID}=require('node:crypto');
const tick=()=>new Promise(resolve=>setImmediate(resolve));
const deferred=()=>{let resolve,reject;const promise=new Promise((a,b)=>{resolve=a;reject=b;});return {resolve,reject,promise};};
function setup(override,options={}){
  const timers=new Map(),storage=options.storage||new Map(),events={},calls=[],statuses=[],restores=[];let n=0,revision=0;
  const ctx={TextEncoder,AbortController,crypto:{randomUUID},setTimeout:fn=>{const id=++n;timers.set(id,fn);return id;},clearTimeout:id=>timers.delete(id),sessionStorage:{getItem:key=>storage.get(key),setItem:(key,value)=>storage.set(key,value)},window:{ExcelLensSession:{id:'recovery-page-0000000001',ready:Promise.resolve()},addEventListener:(name,fn)=>(events[name]??=[]).push(fn)}};
  vm.runInNewContext(fs.readFileSync(__dirname+'/recovery.js','utf8'),ctx);
  const api=async(path,opts)=>{
    calls.push({path,opts,body:opts?.body&&JSON.parse(opts.body)});
    if(override){const result=await override(path,opts,calls.at(-1).body);if(result!==undefined)return result;}
    return opts?.method==='POST'?{revision:++revision}:{revision:0,records:{}};
  };
  const journal=new ctx.window.ExcelLensRecovery.Journal({kind:'calculator',api,onRestore:(records,meta)=>restores.push({records,meta}),onStatus:state=>statuses.push(state),...options});
  return {journal,calls,statuses,restores,ctx,timers,storage,events};
}
const data=text=>({text,note:'',source:null});

test('recovery loads input without evaluation and preserves typing while the load is in flight',async()=>{
  const gate=deferred(),h=setup((path,opts)=>!opts?gate.promise:undefined);
  await tick();h.journal.set('0',data('fresh input'));
  gate.resolve({revision:7,records:{0:data('old formula'),1:data('=SUM(A:A)')}});await h.journal.ready;
  assert.equal(h.journal.records.get('0').text,'fresh input');assert.equal(h.restores[0].records[0],undefined);assert.equal(h.restores[0].records[1].text,'=SUM(A:A)');
  assert.equal(h.calls.length,1);assert.equal(h.journal.revision,7);assert.equal(h.journal.dirty,true);
});

test('old durable acknowledgements cannot clear newer input and saves stay serialized',async()=>{
  const gate=deferred();let running=0,max=0,rev=0;
  const h=setup(async(path,opts)=>{if(opts){running++;max=Math.max(max,running);if(rev===0)await gate.promise;running--;return {revision:++rev};}});
  await h.journal.ready;h.journal.set('0',data('sent'));
  const save=h.journal.flush();await tick();assert.equal(h.journal.flush(),save);
  h.journal.set('0',data('newer'));gate.resolve();await save;
  assert.equal(h.calls.length,3);assert.equal(h.calls[1].body.changes[0].data.text,'sent');assert.equal(h.calls[2].body.changes[0].data.text,'newer');
  assert.equal(h.journal.dirty,false);assert.equal(h.journal.revision,2);assert.equal(max,1);
});

test('a lost acknowledgement retries the exact transaction before saving later edits',async()=>{
  let fail=true,revision=0;const seen=new Set();
  const h=setup((path,opts,body)=>{
    if(!opts)return;
    if(!seen.has(body.requestId)){seen.add(body.requestId);revision++;}
    if(fail){fail=false;throw Error('connection lost after sync');}
    return {revision};
  });
  await h.journal.ready;h.journal.set('0',data('first'));assert.equal(await h.journal.flush(),false);
  const original=h.calls[1].opts.body;assert.equal(h.journal.dirty,true);assert.match(h.journal.error,/connection lost/);
  h.journal.set('0',data('newer'));assert.equal(await h.journal.flush(),true);
  assert.equal(h.calls[2].opts.body,original);assert.equal(h.calls[3].body.revision,1);assert.equal(h.calls[3].body.changes[0].data.text,'newer');assert.equal(revision,2);
});

test('conflict stops retries and a fork preserves both journals and the latest page input',async()=>{
  let first=true;const h=setup((path,opts,body)=>{
    if(opts&&first){first=false;const error=Error('another page saved');error.status=409;throw error;}
    if(opts)return {revision:body.revision+1};
  });
  await h.journal.ready;const original=h.journal.owner;h.journal.set('0',data('keep this'));
  await h.journal.flush();assert.equal(h.journal.blocked,true);assert.equal(h.journal.dirty,true);assert.equal(await h.journal.retry(),false);assert.equal(h.calls.length,2);
  assert.equal(await h.journal.fork(),true);assert.notEqual(h.journal.owner,original);assert.equal(h.calls[2].body.revision,0);assert.equal(h.calls[2].body.changes[0].data.text,'keep this');
  assert.equal(h.storage.get(h.journal.ownerKey),h.journal.owner);assert.equal(h.journal.dirty,false);
});

test('corrupt stored data never replaces current page input and can be saved as a new owner',async()=>{
  let fail=true;const h=setup((path,opts,body)=>{
    if(!opts&&fail){const error=Error('checksum mismatch');error.status=422;throw error;}
    if(opts)return {revision:body.revision+1};
  });
  assert.equal(await h.journal.ready,false);h.journal.set('0',data('still editable'));assert.equal(await h.journal.flush(),false);assert.equal(h.restores.length,0);
  fail=false;assert.equal(await h.journal.fork(),true);assert.equal(h.journal.records.get('0').text,'still editable');
});

test('import creates an independent copy, keeps same-cell current drafts and does not submit analysis',async()=>{
  const h=setup((path,opts,body)=>{
    if(path.includes('previous-owner'))return {revision:9,records:{0:data('old overlap'),1:data('restored')}};
    if(opts)return {revision:body.revision+1};
  },{kind:'analysis'});
  await h.journal.ready;h.journal.set('0',data('local'));await h.journal.flush();const original=h.journal.owner;
  const result=await h.journal.importCopy({owner:'previous-owner-00000001'});
  assert.equal(result.count,1);assert.equal(result.skipped,1);assert.equal(result.saved,true);assert.notEqual(h.journal.owner,original);assert.equal(h.journal.records.get('0').text,'local');assert.equal(h.restores.at(-1).records[1].text,'restored');
  assert(h.calls.every(call=>call.path.startsWith('/api/recovery')));
});

test('Unicode and escaped input is batched by UTF-8 JSON bytes and unloading sends a bounded keepalive',async()=>{
  const h=setup();await h.journal.ready;
  for(let i=0;i<20;i++)h.journal.set(String(i),data('文😀\n"'.repeat(1000)));
  await h.journal.flush();const writes=h.calls.filter(c=>c.opts);
  assert(writes.length>1);for(const call of writes)assert(Buffer.byteLength(call.opts.body,'utf8')<60*1024);
  h.journal.set('0',data('latest'));const event={preventDefault(){this.prevented=true;}};h.events.beforeunload[0](event);await tick();
  assert.equal(event.prevented,true);assert.equal(h.calls.at(-1).opts.keepalive,true);
});

test('deleted input remains a tombstone until a durable acknowledgement, and oversized rows remain dirty',async()=>{
  const h=setup();await h.journal.ready;h.journal.set('0',data('value'));await h.journal.flush();h.journal.set('0',null);
  assert.equal(h.journal.dirty,true);await h.journal.flush();assert.equal(h.calls.at(-1).body.changes[0].data,null);assert.equal(h.journal.records.size,0);
  h.journal.set('1',data('x'.repeat(70000)));assert.equal(await h.journal.flush(),false);assert.equal(h.journal.dirty,true);assert.match(h.journal.error,/过长/);
  h.journal.set('1',data('shorter'));assert.equal(h.journal.blocked,false);assert.equal(await h.journal.flush(),true);assert.equal(h.journal.dirty,false);
});
