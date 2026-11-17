const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const deferred=()=>{let resolve;const promise=new Promise(r=>resolve=r);return {promise,resolve};};
function setup(){const c={bookKey:'fingerprint',book:'path-book',opened:true},calls=[],ctx={window:{},setTimeout};vm.runInNewContext(fs.readFileSync(__dirname+'/composition-tasks.js','utf8'),ctx);const task=new ctx.window.ExcelLensCompositionTasks({context:()=>c,sleep:async()=>{},api:async(path,options)=>{calls.push({path,request:options?JSON.parse(options.body):null});if(path.endsWith('/start'))return {jobId:'job'};if(path.includes('/status'))return {status:'done'};if(path.includes('/result'))return {id:'job',source:'append:digest',workbookKey:c.bookKey,book:c.book};return {};}});return {task,c,calls};}
test('cancelled delayed starts observe and release their exact token',async()=>{
  const {task,calls}=setup(),gate=deferred(),api=task.api;task.api=(path,...rest)=>path.endsWith('/start')?gate.promise:api(path,...rest);
  const pending=task.run({mode:'append'});task.reset();gate.resolve({jobId:'late'});await assert.rejects(pending,{name:'AbortError'});assert.deepEqual(calls.map(c=>c.request?.id),['late']);assert.equal(task.pending.size,0);
});
test('accepted tasks transfer ownership; stale identity and result mismatch clear it',async()=>{
  const {task,calls,c}=setup(),result=await task.run({mode:'append'});assert.equal(result.id,'job');assert.equal(calls.some(c=>c.path.endsWith('/clear')),false);
  const api=task.api;task.api=async(path,...rest)=>{const value=await api(path,...rest);if(path.includes('/result')){c.book='different-path';}return value;};await assert.rejects(task.run({mode:'append'}),{name:'AbortError'});assert.equal(calls.at(-1).request.id,'job');
});
test('server errors release failed jobs and leave other successful jobs alone',async()=>{
  const {task,calls}=setup(),api=task.api;task.api=async(path,...rest)=>path.includes('/status')?{status:'error',error:'源文件已改变'}:api(path,...rest);await assert.rejects(task.run({mode:'append'}),/改变/);assert.equal(calls.at(-1).request.id,'job');
});
