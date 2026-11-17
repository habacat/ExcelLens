const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const source=fs.readFileSync(__dirname+'/analytics.js','utf8'),tick=()=>new Promise(r=>setImmediate(r));
const deferred=()=>{let resolve,reject;const promise=new Promise((a,b)=>{resolve=a;reject=b;});return {promise,resolve,reject};};
function setup(){
  const nodes=new Map();function node(id){if(!nodes.has(id))nodes.set(id,{textContent:'',value:'',hidden:false,options:[{value:'full'},{value:'visible'},{value:'selection'}],children:[],style:{},replaceChildren(...children){this.children=children;},append(...items){this.children.push(...items);}});return nodes.get(id);}
  const state={fingerprint:'same-content',book:'path-one',opened:true,source:'wb:同名表',headerRow:1,query:{source:'wb:同名表'},fields:[{id:'C1',name:'old'}],fieldsReady:true,abortIndex:0},messages=[];
  const ctx={window:{},state,document:{createElement:()=>node('created-'+nodes.size)},$:node,JSON,URLSearchParams,AbortController,api:async()=>{},analysisJobs:{main:null},fmtNum:String,setText:(id,s)=>node(id).textContent=s,toast:s=>messages.push(s),setAnalysisBusy(){},renderFields(){},renderBuilder(){},renderRules(){}};
  ctx.clusters={reset(){ctx.clusterResets=(ctx.clusterResets||0)+1;}};
  vm.runInNewContext(fs.readFileSync(__dirname+'/analysis-sources.js','utf8'),ctx);ctx.sourceModel=ctx.window.ExcelLensAnalysisSourceModel;
  ctx.sourceRows=new ctx.window.ExcelLensAnalysisSources({api:(...args)=>ctx.api(...args),context:()=>({bookKey:state.fingerprint,book:state.book,opened:state.opened}),grid:()=>({source:state.source,workbookKey:state.fingerprint,book:state.book,viewId:'view'})});
  function section(begin,end){return source.slice(source.indexOf(begin),source.indexOf(end,source.indexOf(begin)));}
  vm.runInNewContext(section('  function invalidateSourceRequests()','  function clearResultDisplay()')+section('  function renderSourceScope()','  function allFields()')+section('  async function loadFieldProfile(','  async function backupExport()')+section('  async function drill(','  function askText(')+';this.loadFieldProfile=loadFieldProfile;this.loadFields=loadFields;this.drill=drill;',ctx);
  return {ctx,state,node,messages};
}
test('a profile from identical sheet names at another workbook path cannot publish data or errors',async()=>{
  for(const error of [false,true]){
    const {ctx,state,node,messages}=setup(),gate=deferred();ctx.api=()=>gate.promise;
    const pending=ctx.loadFieldProfile({id:'C1',name:'原字段'});state.book='path-two';node('analysisFieldProfile').textContent='新文件';
    error?gate.reject(Error('旧文件错误')):gate.resolve({rows:99});await pending;
    assert.equal(node('analysisFieldProfile').textContent,'新文件');assert.deepEqual(messages,[]);
  }
});
test('new field intent cancels the older profile even when the old response ignores abort',async()=>{
  const {ctx,node,messages}=setup(),first=deferred(),second=deferred();let calls=0;ctx.api=()=>++calls===1?first.promise:second.promise;
  const a=ctx.loadFieldProfile({id:'C1',name:'甲'}),b=ctx.loadFieldProfile({id:'C2',name:'乙'});
  first.reject(Error('甲的过期错误'));await a;assert.match(node('analysisFieldProfile').textContent,/乙/);assert.deepEqual(messages,[]);
  second.reject(Object.assign(Error('cancelled'),{name:'AbortError'}));await b;
});
test('a late captured range and schema cannot replace a newer full-source intent',async()=>{
  const {ctx,state,messages}=setup(),gate=deferred(),cleared=[];
  ctx.api=async(path,options)=>{if(path.endsWith('/capture'))return gate.promise;if(path.endsWith('/clear')){cleared.push(JSON.parse(options.body).id);return {};}return {fields:[{id:'C1',name:'完整源字段'}],preview:[],totalRows:10};};
  const old=ctx.loadFields({mode:'visible'});await tick();await ctx.loadFields({mode:'full'});
  gate.resolve({id:'late',source:state.source,workbookKey:state.fingerprint,book:state.book,sourceScope:{version:1,mode:'visible'}});await old;
  assert.equal(state.fields[0].name,'完整源字段');assert.equal(state.query.sourceScope,undefined);assert.equal(state.fieldsReady,true);assert.equal(state.fieldsLoading,false);assert.deepEqual(cleared,['late']);assert.deepEqual(messages,[]);
  assert.equal(ctx.clusterResets,2);
});
test('drill guards both group and result task when requests reuse the same page offset',async()=>{
  const {ctx,state,node,messages}=setup(),first=deferred(),second=deferred(),requests=[];
  state.jobID='completed';state.activeResultQuery={source:state.source};ctx.api=async(path,options)=>{requests.push(JSON.parse(options.body));return requests.length===1?first.promise:second.promise;};
  const a=ctx.drill(['甲'],'',0),b=ctx.drill(['乙'],'',0);first.reject(Error('旧组错误'));await a;
  assert.equal(node('analysisDrillInfo').textContent,'正在读取源数据…');assert.deepEqual(messages,[]);assert.equal(requests[0].jobId,'completed');
  second.resolve({rows:[],fields:[],scanned:3});await b;assert.match(node('analysisDrillInfo').textContent,/3/);
});
