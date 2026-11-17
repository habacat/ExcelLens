const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const deferred=()=>{let resolve;const promise=new Promise(r=>resolve=r);return {promise,resolve};};
function setup(){
  const ctx={window:{}},c={bookKey:'fingerprint',book:'path-book',opened:true},calls=[];
  vm.runInNewContext(fs.readFileSync(__dirname+'/analysis-sources.js','utf8'),ctx);
  const h=new ctx.window.ExcelLensAnalysisSources({context:()=>({...c}),grid:()=>({workbookKey:c.bookKey,book:c.book,source:'wb:源',viewId:'view'}),api:async(path,options)=>{calls.push({path,data:JSON.parse(options.body)});return {};}});
  const response=(id,mode='visible')=>({id,source:'wb:源',workbookKey:c.bookKey,book:c.book,sourceScope:{version:1,mode,fingerprint:c.bookKey}});
  return {h,c,calls,response};
}
test('cancel and replacement release late owners without clearing the newer binding',async()=>{
  const {h,calls,response}=setup(),gate=deferred();h.api=async(path,options)=>{calls.push({path,data:JSON.parse(options.body)});return path.endsWith('/capture')?gate.promise:{};};
  const late=h.prepare({source:'wb:源',mode:'visible'});h.cancel();
  const full=await h.prepare({source:'wb:源',mode:'full'});h.commit(full);
  gate.resolve(response('late'));await assert.rejects(late,{name:'AbortError'});
  assert.equal(h.binding,full);assert.deepEqual(calls.filter(c=>c.path.endsWith('/clear')).map(c=>c.data.id),['late']);
});
test('a candidate cannot publish before schema validation and failed preparation retains the old owner',async()=>{
  const {h,calls,response}=setup();h.api=async(path,options)=>{calls.push({path,data:JSON.parse(options.body)});return path.endsWith('/capture')?response('visible'):{};};
  const old=await h.prepare({source:'wb:源',mode:'visible'});h.commit(old);
  const candidate=await h.prepare({source:'wb:源',mode:'full'});assert.equal(h.binding,old);h.release(candidate);assert.equal(h.binding,old);
  h.grid=()=>{throw Error('仍在加载');};await assert.rejects(h.prepare({source:'wb:源',mode:'selection'}),/仍在加载/);assert.equal(h.binding,old);
  assert.equal(calls.some(c=>c.path.endsWith('/clear')),false);
});
test('same content at a different path and a closed workspace invalidate delayed restores',async()=>{
  for(const mutate of [c=>c.book='other-path',c=>c.opened=false]){
    const {h,c,calls,response}=setup(),gate=deferred(),reply=response('stale');h.api=async(path,options)=>{calls.push({path,data:JSON.parse(options.body)});return path.endsWith('/restore')?gate.promise:{};};
    const pending=h.prepare({source:'wb:源',sourceScope:reply.sourceScope});mutate(c);gate.resolve(reply);
    await assert.rejects(pending,{name:'AbortError'});assert.equal(h.binding,null);assert.equal(calls.at(-1).data.id,'stale');
  }
});
test('runtime tokens stay outside durable queries and only match their source and scope',async()=>{
  const {h,response}=setup(),data=response('owner');h.api=async()=>data;h.commit(await h.prepare({source:'wb:源',mode:'visible'}));
  const query={source:'wb:源',sourceScope:data.sourceScope};assert.equal(h.query(query).sourceId,'owner');assert.equal(query.sourceId,undefined);
  assert.equal(h.query({...query,source:'wb:另一表'}).sourceId,undefined);assert.equal(h.query({source:'wb:源'}).sourceId,undefined);
  const candidate=await h.prepare({source:'wb:源',mode:'full'});h.commit(candidate);assert.equal(h.binding.sourceScope,null);
});
test('an obsolete schema intent cannot commit its prepared source',async()=>{
  const {h,response}=setup();let current=true;h.api=async()=>response('pending');
  const candidate=await h.prepare({source:'wb:源',mode:'visible'},()=>current);current=false;
  assert.throws(()=>h.commit(candidate),{name:'AbortError'});assert.equal(h.binding,null);assert.equal(h.candidates.size,0);
});

test('append definitions use composition owners and atomically replace only after fields succeed',async()=>{
  const {h,calls}=setup();h.tasks={run:async()=>({id:'append-job',source:'append:hash',workbookKey:'fingerprint',book:'path-book',definition:{version:1,name:'联合'}})};
  const candidate=await h.prepare({source:'append:hash',composition:{version:1,name:'联合'}});assert.equal(h.binding,null);h.commit(candidate);
  assert.equal(h.query({source:'append:hash',composition:candidate.composition}).sourceId,'append-job');h.reset();assert.equal(calls.at(-1).path,'/api/composition/clear');
});

test('a fresh prepared append replaces an equal definition owner and releases the prior token',async()=>{
  const {h,calls}=setup(),definition={version:1,name:'联合'},payload={source:'append:hash',workbookKey:'fingerprint',book:'path-book',definition};h.tasks={run:async()=>{throw Error('prepared source must not rescan');}};
  h.commit(await h.prepare({source:payload.source,composition:definition,prepared:{...payload,id:'old'}}));
  h.commit(await h.prepare({source:payload.source,composition:definition,prepared:{...payload,id:'new'}}));
  assert.equal(h.binding.id,'new');assert.equal(calls.at(-1).path,'/api/composition/clear');assert.equal(calls.at(-1).data.id,'old');
});
