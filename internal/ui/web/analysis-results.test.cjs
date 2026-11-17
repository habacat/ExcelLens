const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const source=fs.readFileSync(__dirname+'/analytics.js','utf8');
function setup(){
  const nodes=new Map(),node=id=>{if(!nodes.has(id))nodes.set(id,{value:'csv',replaceChildren(){}});return nodes.get(id);};
  const ctx={window:{},WeakSet,Map,Headers,URLSearchParams,AbortController,$:node,encodeURIComponent,JSON,Date};
  vm.runInNewContext(fs.readFileSync(__dirname+'/analysis-results.js','utf8'),ctx);
  vm.runInNewContext(fs.readFileSync(__dirname+'/analysis-sources.js','utf8'),ctx);
  vm.runInNewContext(fs.readFileSync(__dirname+'/analysis-jobs.js','utf8'),ctx);
  vm.runInNewContext(fs.readFileSync(__dirname+'/analysis-tree.js','utf8'),ctx);
  const cache=new ctx.window.ExcelLensAnalysisResults();
  ctx.state={fingerprint:'book',opened:true,reportOpenSerial:0,reportCache:cache,jobID:'previous-job'};
  ctx.sourceRows=new ctx.window.ExcelLensAnalysisSources({api:(...args)=>ctx.api(...args),context:()=>({bookKey:ctx.state.fingerprint,book:'path',opened:ctx.state.opened})});
  ctx.tree=new ctx.window.ExcelLensAnalysisTree({api:(...args)=>ctx.api(...args),context:()=>({bookKey:ctx.state.fingerprint,book:ctx.state.book,opened:ctx.state.opened}),limits:{nodes:8,bytes:100000,pageRows:150,active:2}});
  const exports=[],materialized=[],shown=[],messages=[];
  Object.assign(ctx,{analysisJobs:{cancelMain(){}},charts:{draw(){}},setAnalysisBusy(){},setTab(){},loadFields:async()=>{},run:async()=>{throw Error('cached report must not re-run');},
    renderResult:result=>shown.push(result),askText:async()=> '快照',refreshWorkspace:async()=>{},toast:message=>messages.push(message),downloadBlob(){},TOKEN:'test',
    fetch:async url=>{exports.push(url);return {ok:true,status:200,blob:async()=>({})};},
    post:async(path,body)=>{materialized.push(body.jobId);return {id:'saved'};}});
  function section(begin,end){const a=source.indexOf(begin),b=source.indexOf(end,a);assert(a>=0&&b>a);return source.slice(a,b);}
  vm.runInNewContext(section('  function activateResult(','  function metricCell(')+section('  async function materialize(){','  async function materializeDetails(')+section('  async function exportCSV(){','  async function renderDashboard(')+';this.openReport=openReport;this.exportCSV=exportCSV;this.materialize=materialize;this.restoreReportQuery=restoreReportQuery;this.activateResult=activateResult;this.resultFieldName=resultFieldName;',ctx);
  return {ctx,cache,exports,materialized,shown,messages};
}
test('cached report open, export and result sheet use the same result task after visiting another report',async()=>{
  const h=setup(),a={id:'a',query:{source:'wb:test',rows:['C1']}},b={id:'b',query:{source:'wb:test',rows:['C2']}};
  h.cache.set('book',a.query,{job:'task-a',result:{grand:'11'}});h.cache.set('book',b.query,{job:'task-b',result:{grand:'99'}});
  await h.ctx.openReport(b);await h.ctx.openReport(a);await h.ctx.exportCSV();await h.ctx.materialize();
  assert.equal(h.ctx.state.activeResult.grand,'11');assert.equal(h.ctx.state.jobID,'task-a');assert(h.exports[0].includes('id=task-a&'));assert.deepEqual(h.materialized,['task-a']);
  h.ctx.fetch=async()=>({status:404});await h.ctx.exportCSV();assert(h.messages.at(-1).includes('重新运行'));assert.equal(h.ctx.state.jobID,'task-a');
});

test('displayed result captions remain bound to the completed source after builder fields change',()=>{
  const h=setup(),query={source:'wb:original',rows:['C1'],transforms:[{id:'D_amount',name:'原转换金额',source:'C2',op:'number'}]},fields=[{id:'C1',name:'原分类'},{id:'C2',name:'原金额'}];
  const snapshot=h.cache.set('book',query,{job:'done',result:{},sourceFields:fields});fields[0].name='changed';h.ctx.activateResult(snapshot);h.ctx.state.fields=[{id:'C1',name:'新来源分类'}];h.ctx.state.query={transforms:[{id:'D_amount',name:'新转换'}]};
  assert.equal(h.ctx.resultFieldName('C1'),'原分类');assert.equal(h.ctx.resultFieldName('D_amount'),'原转换金额');assert.equal(h.ctx.state.activeResultFields[1].name,'原金额');
});
test('a delayed older report open cannot replace the latest report or its task',async()=>{
  const h=setup(),a={id:'a',query:{source:'wb:a'}},b={id:'b',query:{source:'wb:b'}};
  h.cache.set('book',a.query,{job:'a',result:{value:'a'}});h.cache.set('book',b.query,{job:'b',result:{value:'b'}});
  let release;h.ctx.loadFields=()=>new Promise(resolve=>release=resolve);const earlier=h.ctx.openReport(a);
  h.ctx.loadFields=async()=>{};await h.ctx.openReport(b);release();await earlier;
  assert.equal(h.ctx.state.jobID,'b');assert.deepEqual(h.shown.map(x=>x.value),['b']);
});
test('result cache keeps workbook identity, query snapshots, LRU and retained byte limits',()=>{
  const h=setup(),Cache=h.ctx.window.ExcelLensAnalysisResults,cache=new Cache({maxEntries:2,maxBytes:4000}),q={source:'wb:x'};
  const snapshot=cache.set('one',q,{job:'1',result:{value:'same name'}});q.source='changed';assert.equal(snapshot.query.source,'wb:x');
  assert.equal(cache.get('two',{source:'wb:x'}),null);
  cache.set('two',{source:'wb:x'},{job:'2',result:{}});assert.equal(cache.get('one',{source:'wb:x'}).job,'1');
  cache.set('three',{}, {job:'3',result:{}});assert.equal(cache.get('two',{source:'wb:x'}),null);
  const huge=cache.set('huge',{}, {job:'huge',result:{value:'文'.repeat(4000)}});assert.equal(huge.job,'huge');assert.equal(cache.get('huge',{}),null);assert(cache.bytes<=4000);
  cache.clear();assert.equal(cache.bytes,0);assert.equal(cache.size,0);
});
test('saved reports restore omitted arrays and zero-valued display settings without changing stored queries',()=>{
  const h=setup(),saved={source:'wb:test',rows:null,aggregate:'sum',value:'C1'},q=h.ctx.restoreReportQuery(saved);
  for(const key of ['rows','columns','metrics','filters','transforms'])assert(Array.isArray(q[key]));
  assert.equal(q.top,0);assert.equal(q.precision,0);assert.equal(q.aggregate,'sum');assert.equal(q.value,'C1');assert.equal(saved.rows,null);assert(!('precision'in saved));
  h.ctx.activateResult(h.cache.set('book',saved,{job:'zero',result:{}}));assert.equal(h.ctx.state.activeResultQuery.rows.length,0);assert.equal(h.ctx.state.activeResultQuery.precision,0);
});

test('composition reports preserve the header-zero boundary and their complete immutable descriptor',()=>{
  const h=setup(),definition={version:1,name:'联合来源',fields:['金额'],inputs:[{ref:{book:'first'},columns:[2]},{ref:{book:'second'},columns:[1]}]},saved={source:'append:descriptor',composition:definition,aggregate:'sum',value:'C1'};
  const restored=h.ctx.restoreReportQuery(saved);assert.equal(restored.headerRow,0);assert.equal(restored.composition.inputs[1].columns[0],1);restored.composition.inputs[1].columns[0]=4;assert.equal(definition.inputs[1].columns[0],1);
});

test('plan adoption preserves the visible result when schema identity changed after completion',async()=>{
  const h=setup(),previous={amount:'previous'};h.ctx.state.activeResult=previous;h.ctx.state.source='wb:old';h.ctx.api=async()=>({sourceStamp:'edited',fields:[]});
  const completed={job:'plan',result:{query:{source:'scratch:target',rows:['C1'],aggregate:'count'},result:{amount:'new'}},current:()=>true};
  await assert.rejects(h.ctx.adoptAnalysisPlan(completed,{ref:{source:'scratch:target'},stamp:'completed'},()=>true),/发生变化/);
  assert.equal(h.ctx.state.activeResult,previous);assert.equal(h.ctx.state.source,'wb:old');assert.equal(h.ctx.state.jobID,'previous-job');
});

test('a delayed plan schema response cannot change a newer result intent',async()=>{
  const h=setup(),previous={amount:'previous'};h.ctx.state.activeResult=previous;let release;h.ctx.api=()=>new Promise(resolve=>release=resolve);
  const pending=h.ctx.adoptAnalysisPlan({job:'plan',result:{query:{source:'wb:new',aggregate:'count'},result:{}},current:()=>true},{ref:{source:'wb:new'},stamp:'stamp'},()=>true);
  await Promise.resolve();h.ctx.state.reportOpenSerial++;release({sourceStamp:'stamp'});assert.equal(await pending,false);assert.equal(h.ctx.state.activeResult,previous);assert.equal(h.ctx.state.jobID,'previous-job');
});
test('failed scoped report restoration keeps the completed result instead of querying the full source',async()=>{
  const h=setup(),previous={amount:'old'},scope={version:1,mode:'selection',fingerprint:'book',selection:{startRow:2,endRow:3,startColumn:2,endColumn:2}};
  h.ctx.state.activeResult=previous;h.ctx.loadFields=async()=>false;
  await h.ctx.openReport({id:'scoped',query:{source:'wb:test',sourceScope:scope}});
  assert.equal(h.ctx.state.activeResult,previous);assert.equal(h.ctx.state.jobID,'previous-job');assert.equal(h.ctx.state.query.sourceScope.mode,'selection');assert.equal(h.shown.length,0);
});
test('reports using different scopes never share a cached result',()=>{
  const h=setup(),full={source:'wb:test'},visible={...full,sourceScope:{version:1,mode:'visible'}};
  h.cache.set('book',full,{job:'full',result:{}});h.cache.set('book',visible,{job:'visible',result:{}});
  assert.equal(h.cache.get('book',full).job,'full');assert.equal(h.cache.get('book',visible).job,'visible');
});

function savingHarness(){
  const h=setup(),ctx=h.ctx,select={options:[],replaceChildren(){this.options=[];},append(group){this.options.push(...group.options);}};
  const original=ctx.$;ctx.$=id=>id==='analysisSourceSelect'?select:original(id);
  ctx.document={createElement:()=>({options:[],append(option){this.options.push(option);}})};
  ctx.Option=class{constructor(text,value){this.textContent=text;this.value=value;}};
  ctx.queryBase=source=>({source});ctx.clearResultDisplay=()=>{};ctx.state.book='path';ctx.state.openSerial=1;ctx.state.source='wb:test';ctx.state.tab='pivot';ctx.state.workspace={reports:[]};
  ctx.state.activeResultQuery={source:'wb:test',aggregate:'count'};ctx.state.activeResult={grand:'17'};ctx.state.activeReportID='';
  const renders=[];ctx.renderDashboard=()=>{assert(ctx.state.reportCache.get('book',ctx.state.activeResultQuery));renders.push(ctx.state.workspace.reports.map(r=>r.id));};
  function section(a,b){return source.slice(source.indexOf(a),source.indexOf(b,source.indexOf(a)));}
  vm.runInNewContext(section('  function appendCompositionSources(','  function queryScope(')+section('  async function refreshWorkspace(){','  async function ensureIndex(')+section('  async function saveReport(){','  async function materialize(){')+';this.refreshWorkspace=refreshWorkspace;this.saveReport=saveReport;',ctx);
  const workspace=ids=>({fingerprint:'book',sourceSheets:[{name:'test'}],sheets:[],reports:ids.map(id=>({id,name:id,query:ctx.state.activeResultQuery}))});
  return {...h,renders,workspace};
}

test('saving then opening dashboard during a delayed catalogue refresh renders the saved result after adoption',async()=>{
  const h=savingHarness();let release,entered;const waiting=new Promise(resolve=>entered=resolve);
  h.ctx.api=()=>{entered();return new Promise(resolve=>release=resolve);};h.ctx.post=async()=>({id:'saved-report'});
  const pending=h.ctx.saveReport();await waiting;h.ctx.state.tab='dashboard';
  assert.equal(h.ctx.state.reportCache.get('book',h.ctx.state.activeResultQuery).job,'previous-job');
  release(h.workspace(['saved-report']));await pending;
  assert.deepEqual(h.renders,[['saved-report']]);assert.equal(h.ctx.state.activeReportID,'saved-report');
});

test('older workspace refreshes and responses from a previous opening cannot replace the latest report catalogue',async()=>{
  const h=savingHarness(),releases=[];h.ctx.api=()=>new Promise(resolve=>releases.push(resolve));h.ctx.state.tab='dashboard';
  h.cache.set('book',h.ctx.state.activeResultQuery,{job:'cached',result:{}});
  const earlier=h.ctx.refreshWorkspace(),latest=h.ctx.refreshWorkspace();releases[1](h.workspace(['new']));await latest;releases[0](h.workspace(['old']));assert.equal(await earlier,false);
  assert.equal(h.ctx.state.workspace.reports[0].id,'new');assert.deepEqual(h.renders,[['new']]);
  const reopened=h.ctx.refreshWorkspace();h.ctx.state.openSerial++;releases[2](h.workspace(['obsolete-opening']));assert.equal(await reopened,false);assert.equal(h.ctx.state.workspace.reports[0].id,'new');
});

test('save captures the original report and source identities and never relabels a newer result',async()=>{
  const h=savingHarness(),requests=[];let answer;h.ctx.state.activeReportID='original';h.ctx.askText=()=>new Promise(resolve=>answer=resolve);
  h.ctx.post=async(path,body)=>{requests.push(body);return {id:'original'};};h.ctx.api=async()=>h.workspace(['original']);
  const pending=h.ctx.saveReport();h.ctx.state.activeReportID='another';h.ctx.state.activeResult={grand:'99'};h.ctx.state.jobID='another-task';answer('原报表');await pending;
  assert.equal(requests[0].id,'original');assert.equal(requests[0].workbookKey,'book');assert.equal(requests[0].book,'path');assert.equal(h.ctx.state.activeReportID,'another');assert.equal(h.ctx.state.jobID,'another-task');
});

test('save acknowledgements from a replaced source do not update the report catalogue or current UI',async()=>{
  const h=savingHarness();let release,started;const posted=new Promise(resolve=>started=resolve);h.ctx.post=()=>{started();return new Promise(resolve=>release=resolve);};h.ctx.api=async()=>{throw Error('obsolete save must not refresh');};
  const pending=h.ctx.saveReport();await posted;h.ctx.state.book='another-path';release({id:'old'});await pending;
  assert.equal(h.ctx.state.activeReportID,'');assert.equal(h.cache.size,0);assert.equal(h.messages.length,0);
});
