const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const tick=()=>new Promise(resolve=>setImmediate(resolve));
function setup(){
  const ctx={window:{},AbortController,encodeURIComponent};vm.runInNewContext(fs.readFileSync(__dirname+'/analysis-jobs.js','utf8'),ctx);
  let book='one';const starts=[],statuses=[],cancelled=[],busy=[],progress=[];
  const jobs=new ctx.window.ExcelLensAnalysisJobs({identity:()=>book,sleep:async()=>{},onBusy:value=>busy.push(value),onProgress:value=>progress.push(value),
    api:async(url,options)=>{
      if(url.endsWith('/start'))return new Promise(resolve=>starts.push({resolve,options}));
      if(url.endsWith('/cancel')){cancelled.push(JSON.parse(options.body).id);return {};}
      return new Promise(resolve=>statuses.push({resolve,url}));
    }});
  return {jobs,starts,statuses,cancelled,busy,progress,setBook:value=>book=value};
}
test('cancelling before start returns cancels the late owned job without polling or publishing',async()=>{
  const h=setup(),pending=h.jobs.execute({}, {main:true}).catch(error=>error);
  assert.deepEqual(h.busy,[true]);assert.equal(h.starts[0].options.headers['X-ExcelLens-Workbook'],'one');
  h.jobs.cancelMain();h.starts[0].resolve({jobId:'late'});const error=await pending;await tick();
  assert.equal(error.name,'AbortError');assert.deepEqual(h.cancelled,['late']);assert.equal(h.statuses.length,0);assert.equal(h.jobs.tasks.size,0);assert.deepEqual(h.busy,[true,false]);
});
test('out-of-order starts and obsolete results cannot finish the newer analysis UI',async()=>{
  const h=setup(),old=h.jobs.execute({}, {main:true}).catch(error=>error),fresh=h.jobs.execute({}, {main:true});
  h.starts[1].resolve({jobId:'new'});await tick();assert(h.jobs.main);
  h.starts[0].resolve({jobId:'old'});await old;assert.equal(h.busy.at(-1),true);assert.deepEqual(h.cancelled,['old']);
  h.statuses[0].resolve({status:'done',result:{amount:'2'}});const result=await fresh;
  assert.equal(result.job,'new');assert.equal(result.result.amount,'2');assert.equal(result.current(),true);assert.equal(h.busy.at(-1),false);
});
test('cancelled polling ignores an in-flight completion even if the transport ignores abort',async()=>{
  const h=setup(),old=h.jobs.execute({}, {main:true}).catch(error=>error);
  h.starts[0].resolve({jobId:'old'});await tick();h.jobs.cancelMain();
  const fresh=h.jobs.execute({}, {main:true});h.starts[1].resolve({jobId:'new'});await tick();
  h.statuses[0].resolve({status:'done',result:{old:true}});assert.equal((await old).name,'AbortError');assert.equal(h.progress.length,0);assert.equal(h.busy.at(-1),true);
  h.statuses[1].resolve({status:'done',result:{fresh:true}});await fresh;assert.equal(h.progress.length,1);
});
test('workbook changes invalidate background jobs and clean up their late responses',async()=>{
  const h=setup(),pending=h.jobs.execute({}).catch(error=>error);h.starts[0].resolve({jobId:'dashboard'});await tick();h.setBook('two');h.jobs.reset();
  h.statuses[0].resolve({status:'done',result:{}});assert.equal((await pending).name,'AbortError');assert.deepEqual(h.cancelled,['dashboard']);assert.equal(h.jobs.tasks.size,0);
});

test('malformed start IDs fail before polling or cancelling an unrelated identifier',async()=>{
  for(const value of [{},{jobId:1},{jobId:''},{jobId:'foreign?task'}]){
    const h=setup(),pending=h.jobs.execute({}, {main:true}).catch(error=>error);h.starts[0].resolve(value);
    assert.match((await pending).message,/标识无效/);assert.equal(h.statuses.length,0);assert.deepEqual(h.cancelled,[]);assert.equal(h.jobs.tasks.size,0);assert.equal(h.busy.at(-1),false);
  }
});

test('unknown states and mismatched response IDs stop unbounded polling and clean the owned task',async()=>{
  for(const value of [{},{status:'mystery'},{status:'done',id:'unrelated',result:{}}]){
    const h=setup(),pending=h.jobs.execute({}, {main:true}).catch(error=>error);h.jobs.maxPolls=0;h.starts[0].resolve({jobId:'owned'});await tick();h.statuses[0].resolve(value);
    assert.match((await pending).message,/状态响应无效/);assert.equal(h.progress.length,0);assert.deepEqual(h.cancelled,['owned']);assert.equal(h.jobs.tasks.size,0);assert.equal(h.busy.at(-1),false);
  }
});
