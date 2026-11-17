const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
function setup(){
  const nodes=new Map(),node=id=>{if(!nodes.has(id))nodes.set(id,{value:'',checked:false,hidden:true,textContent:'',style:{},addEventListener(){},setAttribute(k,v){this[k]=v;},removeAttribute(k){delete this[k];},close(){},querySelector(){return {};}});return nodes.get(id);};
  const timers=new Map();let timer=0;
  const ctx={window:{},document:{getElementById:node},AbortController,clearTimeout:id=>timers.delete(id),setTimeout:fn=>{timers.set(++timer,fn);return timer;}};
  for(const file of ['display-format.js','multiselection.js','export.js'])vm.runInNewContext(fs.readFileSync(__dirname+'/'+file,'utf8'),ctx);
  const state={opened:true,workbookKey:'one',sheet:'Data',sheets:[{}],filtered:true,filterId:'f',multiRanges:[],range:{row1:2,row2:8,col1:2,col2:1,visual1:2,visual2:1}};
  const calls=[],notices=[],center=new ctx.window.ExcelLensExportCenter({state,openDialog(){},api:async(path,options)=>{calls.push({path,payload:JSON.parse(options.body)});return {jobId:'job'};},toast:x=>notices.push(x),colName:c=>String.fromCharCode(64+c),excludeThroughRow:()=>1});
  const poll=center.poll.bind(center),refresh=center.refresh.bind(center);
  center.poll=async()=>{};center.refresh=()=>{};node('exportScope').value='selection';node('exportFormat').value='csv';node('exportMode').value='values';
  return {state,center,calls,notices,node,poll,refresh,timers};
}
test('visual selection export sends paired endpoints and freeze coordinates instead of a source-row rectangle',async()=>{
  const h=setup();await h.center.start();const p=h.calls[0].payload;
  assert.equal(p.start,'A2');assert.equal(p.end,'B8');assert.equal(p.visualStart,2);assert.equal(p.visualEnd,1);assert.equal(p.excludeThroughRow,1);assert.equal(p.filterId,'f');
});

test('generated issue exports share admission and retry their own snapshot instead of the current selection',async()=>{
  const h=setup();await h.center.startGenerated('/api/analysis/issues/export',{jobId:'analysis-one',protectCSV:false},'异常清单');
  assert.equal(h.calls[0].path,'/api/analysis/issues/export');assert.equal(h.calls[0].payload.jobId,'analysis-one');assert.equal(h.calls[0].payload.protectCSV,false);
  await h.center.startGenerated('/api/analysis/issues/export',{jobId:'analysis-two'},'other');assert.equal(h.calls.length,1);assert.match(h.notices[0],/已有导出/);
  h.center.running=false;h.center.job='';h.state.sheet='different';await h.center.start();assert.equal(h.calls[1].payload.jobId,'analysis-one');h.center.resetBook();assert.equal(h.center.generated,null);
});

test('generated ZIP keeps its true format during shared progress and retries the same complete export',async()=>{
  const h=setup();h.center.refresh=h.refresh;await h.center.startGenerated('/api/relation/export',{jobId:'accepted',format:'zipcsv',mode:'left',maximum:'1000000'},'关联结果');
  assert.equal(h.node('exportFormat').value,'zipcsv');assert.equal(h.node('exportFormat').disabled,true);assert.equal(h.calls[0].payload.format,'zipcsv');assert.equal(h.node('exportAdvisory').textContent,'');
  h.center.running=false;h.center.job='';h.state.sheet='different';await h.center.start();assert.equal(h.calls[1].path,'/api/relation/export');assert.equal(h.calls[1].payload.format,'zipcsv');assert.equal(h.calls[1].payload.jobId,'accepted');
});
test('discontinuous selection export explains the unsupported scope instead of dropping areas',async()=>{
  const h=setup();h.state.multiRanges.push({});await h.center.start();assert.equal(h.calls.length,0);assert.equal(h.notices.length,1);
});

test('recoverable exports opt in explicitly and restored output reuses the export center',async()=>{
  const h=setup();h.state.book='path-book';h.node('exportRecoverable').checked=true;await h.center.start();
  assert.equal(h.calls[0].payload.recoverable,true);assert.equal(h.calls[0].payload.book,'path-book');
  h.center.resetBook();assert.equal(h.center.adoptRecovered({jobId:'restored'},{book:'other',workbookKey:'one'}),false);
  assert.equal(h.center.adoptRecovered({jobId:'restored'},{book:'path-book',workbookKey:'one',file:'原件.csv',sheet:'Data',mode:'csv',scope:'sheet'}),true);
  assert.equal(h.center.job,'restored');assert.equal(h.center.running,true);assert.equal(h.node('exportRecoverable').checked,true);
});

test('a late export start and status cannot install an earlier workbook download',async()=>{
  for(const operation of ['start','status']){
    const h=setup();let release;const cancelled=[];h.center.api=(path,options)=>{if(path==='/api/export/cancel'){cancelled.push(JSON.parse(options.body).id);return Promise.resolve({});}return new Promise(resolve=>release=resolve);};
    h.center.job='old';h.center.running=operation==='status';
    const pending=operation==='start'?h.center.start():h.poll();
    h.state.workbookKey='two';h.center.resetBook();
    release(operation==='start'?{jobId:'old'}:{status:'done',downloadUrl:'/old',fileName:'old.xlsx'});await pending;
    assert.equal(h.center.job,'');assert.equal(h.center.running,false);assert.equal(h.node('exportResult').hidden,true);assert.equal(h.node('exportDownload').href,undefined);assert.equal(h.timers.size,0);
    assert.deepEqual(cancelled,['old']);
  }
});

test('temporary status failures retain cancellation, prevent duplicate starts and can recover',async()=>{
  const h=setup();h.center.running=true;h.center.job='owned';h.center.api=async()=>{throw Error('connection interrupted');};
  for(let i=0;i<4;i++)await h.poll();
  assert.equal(h.center.running,true);assert.equal(h.node('exportRetryBtn').hidden,false);assert.equal(h.timers.size,0);
  await h.center.start();assert.equal(h.center.job,'owned');
  h.center.api=async()=>({status:'done',downloadUrl:'/owned',fileName:'correct.csv'});await h.poll();
  assert.equal(h.center.running,false);assert.equal(h.node('exportResult').hidden,false);assert.equal(h.node('exportDownload').href,'/owned');assert.equal(h.node('exportRetryBtn').hidden,true);
});

test('a server-confirmed lost task permits a new export and active settings describe its snapshot',async()=>{
  const h=setup();h.center.running=true;h.center.job='lost';h.center.snapshot='Data · B2:B8';h.refresh();
  assert.equal(h.node('exportRangeLabel').textContent,'Data · B2:B8');assert.equal(h.node('exportFormat').disabled,true);assert.equal(h.node('exportStartBtn').disabled,true);
  h.center.api=async()=>{const error=Error('missing');error.status=404;throw error;};await h.poll();
  assert.equal(h.center.running,false);assert.equal(h.timers.size,0);
});

test('a completed download keeps its original source after filters clear and the current sheet changes',async()=>{
  const h=setup();h.state.fileName='原始账目.csv';h.state.filterTotal=262145;h.state.filterTotalsBySheet=new Map([['Data',262146]]);h.node('exportScope').value='filtered';h.refresh();
  assert.equal(h.node('exportRangeLabel').textContent,'已筛选：262,146 行');
  await h.center.start();
  h.state.filtered=false;h.state.filterId='';h.state.sheet='Other';h.node('exportScope').value='sheet';
  h.center.api=async()=>({status:'done',downloadUrl:'/snapshot',fileName:'sorted.csv'});await h.poll();h.refresh();
  assert.equal(h.node('exportSnapshot').textContent,'来源：原始账目.csv · Data · 已筛选：262,146 行');
  assert.equal(h.node('exportDownload').href,'/snapshot');assert(!h.node('exportRangeLabel').textContent.includes('已筛选'));
  h.center.resetBook();assert.equal(h.node('exportSnapshot').textContent,'');assert.equal(h.center.resultSource,'');
});
