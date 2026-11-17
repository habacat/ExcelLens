const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const tick=()=>new Promise(resolve=>setImmediate(resolve));
const deferred=()=>{let resolve,reject;const promise=new Promise((a,b)=>{resolve=a;reject=b;});return {promise,resolve,reject};};
function setup(){
  const nodes=new Map(),get=id=>{if(!nodes.has(id))nodes.set(id,{value:'',textContent:'',hidden:false,classList:{toggle(){}},focus(){},replaceChildren(){},querySelector:selector=>get(id+selector),querySelectorAll(){return [];}});return nodes.get(id);};
  const ctx={window:{},document:{getElementById:get},AbortController,encodeURIComponent};
  vm.runInNewContext(fs.readFileSync(__dirname+'/analysis-jobs.js','utf8'),ctx);vm.runInNewContext(fs.readFileSync(__dirname+'/recipes.js','utf8'),ctx);
  const c={bookKey:'one',opened:true,intent:1,resultJob:'finished'},adopted=[],calls=[];
  const h=Object.assign(Object.create(ctx.window.ExcelLensRecipes.prototype),{epoch:1,serial:0,catalogSerial:0,opened:true,busy:false,mode:'',book:'book',store:{revision:1,items:[]},preview:null,draft:null,context:()=>c,controls(){},capture:()=>({jobId:c.resultJob,format:'csv'}),adopt:async(...args)=>{adopted.push(args);return true;},toast(){},dialog:{open:true,close(){this.open=false;}},post:async(url,data)=>{calls.push({url,data});return {};},setCatalog(data){this.book=data.book;this.store=data.store;}});
  get('analysisPlanName').value='月度核对';
  return {h,ctx,c,get,adopted,calls};
}
const preview={recipe:{id:'plan',fields:[{id:'C1',name:'项目',mask:2},{id:'C2',name:'金额',mask:4}]},fields:[{id:'C1',mask:4},{id:'C3',mask:2},{id:'C4',mask:0},{id:'C5',mask:4,uncertain:true}],ref:{book:'book',source:'wb:数据',headerRow:1,stamp:'target'},digest:'digest',stamp:'stamp'};

test('plan mapping keeps native text, amount text, empty samples and repeated targets distinct',()=>{
  const model=setup().ctx.window.ExcelLensRecipesModel;
  assert.equal(model.compatible({mask:2},{mask:4}),false);assert.equal(model.compatible({mask:1},{mask:4}),false);
  assert.equal(model.compatible({mask:4},{mask:0}),true);assert.equal(model.compatible({mask:4},{mask:4,uncertain:true}),false);
  assert.equal(model.validateMapping(preview,{C1:'C3',C2:'C1'}),'');
  assert.match(model.validateMapping(preview,{C1:'C3'}),/逐项/);assert.match(model.validateMapping(preview,{C1:'C1',C2:'C3'}),/不兼容/);
  assert.match(model.validateMapping({...preview,recipe:{fields:[{id:'C1',mask:4},{id:'C2',mask:4}]}},{C1:'C1',C2:'C1'}),/一次/);
});

test('catalog conflicts retain the full-input draft and retry only its commit',async()=>{
  const {h,get,calls}=setup();let captures=0,commits=0;
  h.captureJobs={execute:async request=>{captures++;assert.equal(request.jobId,'finished');assert.equal(request.format,'csv');return {result:{captureId:'validated'},current:()=>true};}};
  h.post=async(url,data)=>{calls.push({url,data});if(!commits++)throw Object.assign(Error('目录已修改'),{status:409});return {id:'plan',revision:3};};
  h.api=async()=>({book:'book',store:{revision:3,items:[]}});
  await h.save();assert.equal(h.draft.id,'validated');assert.match(get('analysisPlanStatus').textContent,/目录已修改/);assert.equal(h.busy,false);
  h.store.revision=2;get('analysisPlanName').value='新的名称';await h.save();
  assert.equal(captures,1);assert.equal(calls.length,2);assert.equal(calls[1].data.id,'validated');assert.equal(calls[1].data.expectedRevision,2);assert.equal(calls[1].data.name,'新的名称');assert.equal(h.store.revision,3);assert.equal(h.draft,null);
});

test('cancelled type capture reclaims a late job without committing a catalog entry',async()=>{
  const {h,ctx,calls}=setup(),gate=deferred(),cancelled=[];
  h.runJobs={cancelMain(){}};
  h.captureJobs=new ctx.window.ExcelLensAnalysisJobs({api:async(url,options)=>{if(url.endsWith('/start'))return gate.promise;if(url.endsWith('/cancel')){cancelled.push(JSON.parse(options.body).id);return {};}throw Error('must not poll cancelled job');},identity:()=>h.identity(),requestIdentity:()=>h.context().bookKey,sleep:async()=>{},endpoint:'/api/recipes/capture'});
  const pending=h.save();await tick();h.cancel();gate.resolve({jobId:'late-capture'});await pending;await tick();
  assert.deepEqual(cancelled,['late-capture']);assert.equal(calls.length,0);assert.equal(h.draft,null);assert.equal(h.busy,false);
});

test('a stale catalog response cannot overwrite a successfully saved plan',async()=>{
  const {h}=setup(),gate=deferred();let reads=0;
  h.api=()=>++reads===1?gate.promise:Promise.resolve({book:'book',store:{revision:2,items:[]}});
  h.captureJobs={execute:async()=>({result:{captureId:'draft'},current:()=>true})};h.post=async()=>({id:'new',revision:2});
  const stale=h.refresh();await h.save();gate.resolve({book:'book',store:{revision:1,items:[]}});await stale;assert.equal(h.store.revision,2);
});

test('a preview arriving after workbook replacement is ignored',async()=>{
  const {h,c}=setup(),gate=deferred();h.selected=()=>({id:'plan'});h.clearPreview=()=>{h.preview=null;};h.target=()=>preview.ref;h.post=()=>gate.promise;h.renderPreview=()=>{throw Error('obsolete schema rendered');};
  const pending=h.readPreview();c.bookKey='two';gate.resolve(preview);await pending;assert.equal(h.preview,null);
});

test('replay failures and cancelled late completions preserve the active result',async()=>{
  const {h,ctx,get,adopted}=setup();h.preview=preview;h.mapping=()=>({C1:'C3',C2:'C1'});
  h.runJobs={execute:async()=>{throw Error('B11的读取类型不兼容');}};
  await h.run();assert.equal(adopted.length,0);assert.match(get('analysisPlanStatus').textContent,/B11/);assert.equal(h.busy,false);
  const gate=deferred(),cancelled=[];h.captureJobs={cancelMain(){}};
  h.runJobs=new ctx.window.ExcelLensAnalysisJobs({api:async(url,options)=>{if(url.endsWith('/start'))return gate.promise;if(url.endsWith('/cancel')){cancelled.push(JSON.parse(options.body).id);return {};}throw Error('late replay must not poll');},identity:()=>h.identity(),requestIdentity:()=>h.context().bookKey,sleep:async()=>{},endpoint:'/api/recipes/run',resultFromStatus:status=>status});
  const pending=h.run();await tick();h.cancel();gate.resolve({jobId:'late-run'});await pending;await tick();assert.deepEqual(cancelled,['late-run']);assert.equal(adopted.length,0);
});

test('completed replay cannot adopt into a newer analysis intent',async()=>{
  const {h,c,adopted}=setup(),gate=deferred();h.preview=preview;h.mapping=()=>({C1:'C3',C2:'C1'});h.runJobs={execute:()=>gate.promise};
  const pending=h.run();c.intent++;gate.resolve({result:{result:{},query:{}},current:()=>true});await pending;assert.equal(adopted.length,0);assert.equal(h.busy,false);
});

test('catalog loading disables stale source actions until the fresh workbook identity is installed',async()=>{
  const {h,ctx,get}=setup(),gate=deferred();h.controls=ctx.window.ExcelLensRecipes.prototype.controls;h.selected=()=>({id:'plan'});get('analysisPlanTarget').value='wb:Data';h.api=()=>gate.promise;
  const pending=h.refresh();assert.equal(get('analysisPlanList').disabled,true);assert.equal(get('analysisPlanPreview').disabled,true);assert.equal(get('analysisPlanSave').disabled,true);
  gate.resolve({book:'fresh-book',store:{revision:2,items:[]}});await pending;assert.equal(h.book,'fresh-book');assert.equal(get('analysisPlanPreview').disabled,false);assert.equal(get('analysisPlanSave').disabled,false);
});

test('a native dialog close blocks capture publication before its queued close event runs',async()=>{
  const {h,calls}=setup(),gate=deferred();h.captureJobs={execute:()=>gate.promise};const pending=h.save();h.dialog.open=false;gate.resolve({result:{captureId:'late-draft'},current:()=>true});await pending;assert.equal(calls.length,0);assert.equal(h.draft,null);
});
test('recipe preview and replay explicitly bind the chosen target range, never the origin range',async()=>{
  const {h,c,get,calls}=setup();c.source='wb:数据';c.sourceId='range-owner';c.sourceScope={version:1,mode:'selection'};
  get('analysisPlanScope').value='current';h.target=()=>preview.ref;h.selected=()=>preview.recipe;h.renderPreview=()=>{};
  h.post=async(url,data)=>{calls.push({url,data});return {...preview,target:{stamp:'target'},sourceScope:c.sourceScope};};
  await h.readPreview();assert.equal(calls[0].data.sourceId,'range-owner');assert.equal(h.preview.sourceId,'range-owner');
  h.mapping=()=>({C1:'C3',C2:'C1'});h.runJobs={execute:async request=>{assert.equal(request.sourceId,'range-owner');throw Error('test stop before adoption');}};await h.run();
  get('analysisPlanScope').value='full';await h.readPreview();assert.equal(calls.at(-1).data.sourceId,undefined);
});
