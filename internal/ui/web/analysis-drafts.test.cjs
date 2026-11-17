const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const source=fs.readFileSync(__dirname+'/analytics.js','utf8');
function setup(override){
  const nodes=new Map(),node=id=>{if(!nodes.has(id))nodes.set(id,{textContent:'',hidden:false,disabled:false,querySelectorAll:()=>[]});return nodes.get(id);};
  const ctx={window:{},Map,Promise,$:node,console};vm.runInNewContext(fs.readFileSync(__dirname+'/analysis-drafts.js','utf8'),ctx);
  vm.runInNewContext(fs.readFileSync(__dirname+'/clipboard.js','utf8'),ctx);
  ctx.drafts=new ctx.window.ExcelLensAnalysisDrafts();ctx.state={fingerprint:'book',loadedSheet:'first',sheetPage:0,sheetRevisions:new Map([['first',0]]),reportCache:new Map(),undoHistory:new Map(),workspace:{sheets:[{id:'first',columns:['x']} ]}};
  ctx.pendingWrites=0;const calls=[],messages=[];
  Object.assign(ctx,{setText:(id,text)=>node(id).textContent=text,toast:message=>messages.push(message),post:async(path,body)=>{calls.push({path,body});return override?override(path,body):{revision:1,previous:[]};},loadSheet:async()=>{},refreshWorkspace:async()=>{},renderSheets(){},setTab(){}});
  const begin=source.indexOf('  let saveChain=Promise.resolve();'),end=source.indexOf('  async function createSheet()',begin);
  const a=source.indexOf('  function getSheetHistory('),b=source.indexOf('  async function changeSheetHistory(',a);
  const c=source.indexOf('  function sheetPageSize('),d=source.indexOf('  function editGridWriteLocal(',c);
  const e=source.indexOf('  async function addRows(){'),f=source.indexOf('  async function deleteSheet()',e);
  vm.runInNewContext(source.slice(begin,end)+source.slice(a,b)+source.slice(c,d)+source.slice(e,f)+';this.commit=commitBatch;this.retry=retryDrafts;this.addRows=addRows;this.unsaved=hasUnsaved;',ctx);
  return {ctx,state:ctx.state,drafts:ctx.drafts,node,calls,messages};
}
const tick=()=>new Promise(resolve=>setImmediate(resolve));
test('restored drafts wait for a deliberate retry and validate the whole batch before adding cells',()=>{
  const h=setup(),draft={book:'book',id:'first',row:0,col:0,value:'恢复输入'},key=h.drafts.key('book','first',0,0);
  h.drafts.restore({[key]:draft});assert.equal(h.calls.length,0);assert.equal(h.drafts.get('book','first',0,0).value,'恢复输入');assert.equal(h.ctx.unsaved(),true);
  h.drafts.stage('book','first',[{row:0,col:0,value:'本页新输入'}]);h.drafts.restore({[key]:draft});assert.equal(h.drafts.get('book','first',0,0).value,'本页新输入');
  const other=h.drafts.key('book','first',1,0);
  assert.throws(()=>h.drafts.restore({[other]:{...draft,row:1},bad:{...draft,row:10000}}),/无效/);assert.equal(h.drafts.get('book','first',1,0),undefined);
});
test('a recovered draft on the home page does not prevent opening its source or an unrelated workbook',()=>{
  const h=setup();h.drafts.stage('book','first',[{row:0,col:0,value:'恢复草稿'}]);
  h.state.currentBook='';assert.equal(h.ctx.unsaved(),false);h.state.currentBook='other';assert.equal(h.ctx.unsaved(),false);assert.equal(h.drafts.size,1);
  h.state.currentBook='book';assert.equal(h.ctx.unsaved(),true);
});
test('an earlier save acknowledgement cannot erase a newer typed draft',async()=>{
  let finish;const h=setup(()=>new Promise(resolve=>finish=resolve));
  const saving=h.ctx.commit('first',[{row:0,col:0,value:'sent'}]);await tick();
  h.drafts.stage('book','first',[{row:0,col:0,value:'new text'}]);finish({revision:1,previous:[]});await saving;
  assert.equal(h.drafts.get('book','first',0,0).value,'new text');assert.equal(h.ctx.unsaved(),true);assert.equal(h.node('analysisAutoSave').textContent,'1格未保存');
});
test('failed saves preserve all pasted cells and a deliberate retry clears only acknowledged drafts',async()=>{
  let fail=true;const h=setup(()=>{if(fail)throw Error('disk unavailable');return {revision:1,previous:[]};});
  await assert.rejects(h.ctx.commit('first',[{row:9,col:0,value:'0001'},{row:10,col:0,value:'line\r\nnext'}]),/unavailable/);
  assert.equal(h.drafts.size,2);assert.equal(h.node('analysisRetrySave').hidden,false);assert.equal(h.ctx.unsaved(),true);
  fail=false;await h.ctx.retry();assert.equal(h.drafts.size,0);assert.equal(h.ctx.unsaved(),false);assert.equal(h.node('analysisAutoSave').textContent,'已保存');
  assert.deepEqual(Array.from(h.calls[1].body.changes,x=>x.value),['0001','line\r\nnext']);assert.equal(h.calls[1].body.workbookKey,'book');
});
test('queued append captures its original sheet and uses the revision after prior saves',async()=>{
  let release;const h=setup((path)=>path.endsWith('/batch')?new Promise(resolve=>release=resolve):{revision:5,total:30});
  const saving=h.ctx.commit('first',[{row:19,col:0,value:'keep'}]);await tick();
  const adding=h.ctx.addRows();h.state.loadedSheet='other';release({revision:4,previous:[]});await saving;await adding;
  assert.equal(h.calls[1].path,'/api/analysis/sheet/rows');assert.equal(h.calls[1].body.id,'first');assert.equal(h.calls[1].body.expectedRevision,4);assert.equal(h.calls[1].body.count,10);assert.equal(h.calls[1].body.row,undefined);
});
test('typing while a save fails is retained and workbook identities keep drafts separate',async()=>{
  let reject;const h=setup(()=>new Promise((resolve,r)=>reject=r));const saving=h.ctx.commit('first',[{row:0,col:0,value:'sent'}]);await tick();
  h.drafts.stage('book','first',[{row:0,col:0,value:'latest'}]);reject(Error('offline'));await assert.rejects(saving);
  h.drafts.stage('other','first',[{row:0,col:0,value:'different book'}]);
  assert.equal(h.drafts.get('book','first',0,0).value,'latest');assert.equal(h.drafts.get('other','first',0,0).value,'different book');
});
test('Unicode drafts retry within encoded request budgets, while invalid typed values stay recoverable',async()=>{
  const h=setup(),value='文'.repeat(4000);
  h.drafts.stage('book','first',Array.from({length:200},(_,row)=>({row,col:0,value})));
  await h.ctx.retry();assert.equal(h.calls.length,2);assert.equal(h.drafts.size,0);
  for(const call of h.calls)assert(Buffer.byteLength(JSON.stringify(call.body),'utf8')<=2*1024*1024);
  const typed='😀'.repeat(4001);await assert.rejects(h.ctx.commit('first',[{row:0,col:0,value:typed}]),/4000/);
  assert.equal(h.calls.length,2);assert.equal(h.drafts.get('book','first',0,0).value,typed);assert.equal(h.ctx.unsaved(),true);
});
