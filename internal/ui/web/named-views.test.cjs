const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const deferred=()=>{let resolve,reject;const promise=new Promise((a,b)=>{resolve=a;reject=b;});return {promise,resolve,reject};};
const tick=()=>new Promise(resolve=>setImmediate(resolve));
function setup(){
  const nodes=new Map(),get=id=>{if(!nodes.has(id))nodes.set(id,{value:'',textContent:'',classList:{toggle(){}},close(){}});return nodes.get(id);};
  const ctx={window:{},document:{getElementById:get},AbortController,TextEncoder,encodeURIComponent};
  vm.runInNewContext(fs.readFileSync(__dirname+'/analysis-jobs.js','utf8'),ctx);vm.runInNewContext(fs.readFileSync(__dirname+'/named-views.js','utf8'),ctx);
  const prototype=ctx.window.ExcelLensNamedViews.prototype,s={workbookKey:'one',sheet:'数据',sheetEpoch:1},applied=[],calls=[];
  const h=Object.assign(Object.create(prototype),{s,epoch:0,serial:0,catalogSerial:0,book:'book',store:{revision:1,items:[]},busy:false,mode:'',dialog:get('dialog'),controls(){},toast(){},canApply:()=>true,applyView:async(...args)=>{applied.push(args);return true;},post:async(url,data)=>{calls.push({url,data});return {};}});
  return {ctx,h,s,get,applied,calls};
}
const view={id:'saved',name:'常用视图',source:{sheet:'数据'},query:{headerRow:1,rules:[],sortKeys:[{column:1,mode:'natural'}]},config:{freeze:{rows:1,cols:0},zoom:105}};
test('view summaries use bounded configuration facts without claiming cached data was restored',()=>{
  const {ctx}=setup(),model=ctx.window.ExcelLensNamedViewsModel;
  assert.equal(model.active(view.query),true);assert.equal(model.active({rules:[],sortKeys:[]}),false);assert.match(model.summary(view),/1级排序.*105%/);
});
test('saved-view jobs use a sheet identity without placing Unicode sheet names in request headers',async()=>{
  const {ctx}=setup(),calls=[];let identity='["one","数据",1]';
  const jobs=new ctx.window.ExcelLensAnalysisJobs({api:async(url,opts)=>{calls.push({url,opts});return url.endsWith('/start')?{jobId:'filter'}:{status:'done',filterId:'filter',matchedRows:3};},identity:()=>identity,requestIdentity:()=> 'one',sleep:async()=>{},endpoint:'/api/filter',resultFromStatus:status=>status});
  const completed=await jobs.execute({sortKeys:[{column:1}]},{main:true});assert.equal(calls[0].opts.headers['X-ExcelLens-Workbook'],'one');assert.equal(completed.result.filterId,'filter');assert.equal(completed.current(),true);identity='["one","下一表",2]';assert.equal(completed.current(),false);
});
test('metadata arriving after sheet replacement cannot apply an old view',async()=>{
  const {h,s,applied}=setup(),gate=deferred();h.selected=()=>view;h.post=()=>gate.promise;h.jobs={cancelMain(){}};
  const pending=h.apply();s.sheet='下一表';s.sheetEpoch++;gate.resolve({view});await pending;assert.equal(applied.length,0);assert.equal(h.busy,false);
});
test('cancelling an in-flight start cleans up its late job and preserves the current view',async()=>{
  const {ctx,h,applied,calls}=setup(),gate=deferred(),cancelled=[];h.selected=()=>view;
  h.post=async()=>({view});h.jobs=new ctx.window.ExcelLensAnalysisJobs({api:async(url,options)=>{calls.push({url,options});if(url.endsWith('/start'))return gate.promise;if(url.endsWith('/cancel')){cancelled.push(JSON.parse(options.body).id);return {};}throw Error('obsolete job must not poll');},identity:()=>h.identity(),requestIdentity:()=>h.s.workbookKey,sleep:async()=>{},endpoint:'/api/filter',resultFromStatus:status=>status});
  const pending=h.apply();await tick();h.cancel();gate.resolve({jobId:'late-view'});await pending;await tick();assert.deepEqual(cancelled,['late-view']);assert.equal(applied.length,0);assert.equal(h.busy,false);
});
test('query failures leave the existing view and its saved configuration intact',async()=>{
  const {h,applied,get}=setup();h.selected=()=>view;h.post=async()=>({view});h.jobs={execute:async()=>{throw Error('sort disk budget exhausted');}};
  await h.apply();assert.equal(applied.length,0);assert.match(get('namedViewStatus').textContent,/disk budget/);assert.equal(h.store.revision,1);
});
test('a view requiring row visibility never partly applies through a client without the capability',async()=>{
  const {h,applied,get}=setup();h.selected=()=>view;h.post=async()=>({view:{...view,ruleVersion:2,config:{...view.config,rowVisibility:{mode:'hide-all'}}}});
  let queries=0;h.jobs={execute:async()=>{queries++;throw Error('unsupported view must not run a query');}};
  await h.apply();assert.equal(queries,0);assert.equal(applied.length,0);assert.match(get('namedViewStatus').textContent,/行显隐.*未应用/);assert.equal(h.busy,false);
});
test('a successful save invalidates an older catalog response even if abort is ignored',async()=>{
  const {h,get}=setup(),gate=deferred();get('namedViewName').value='新视图';h.capture=()=>({sheet:'数据'});h.api=()=>gate.promise;const installed=[];h.setCatalog=data=>{installed.push(data);h.store=data.store;};
  const old=h.refresh();h.post=async()=>({book:'book',store:{revision:2,items:[view]},id:view.id});await h.save(false);gate.resolve({book:'book',store:{revision:1,items:[]}});await old;
  assert.equal(h.store.revision,2);assert.equal(installed.length,1);
});
