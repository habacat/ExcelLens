const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const source=fs.readFileSync(__dirname+'/app.js','utf8');
function setup(){
  const nodes=new Map(),node=id=>{if(!nodes.has(id))nodes.set(id,{value:'',checked:false,hidden:true,replaceChildren(){}});return nodes.get(id);};
  const S={sheet:'数据',sheetEpoch:1,book:'book',workbookKey:'hash',searchRequestID:0,searchJob:null,searchSession:null,searchCache:new Map()};
  const calls=[],polled=[],ctx={S,$:node,searchPanel:{leavePages(){}},window:{},buildSearchHits(){},displaySearchSummary(){},renderSearchResults(){},applySearchHighlightClasses(){},api:async(url,opts)=>{calls.push({url,body:JSON.parse(opts.body)});return {jobId:'search-job'};},pollSearch:async(...args)=>polled.push(args),syncSearchMode(){},toast(){}};
  for(const [start,end] of [['  async function runSearch(','  async function pollSearch('],['  async function cancelSearch(','  function addFilterRule(']]){const a=source.indexOf(start),b=source.indexOf(end,a);vm.runInNewContext(source.slice(a,b),ctx);}
  ctx.searchKey=(...args)=>args.join('|');node('searchMode').value='value';node('searchScope').value='current';node('searchInput').value='金额';return {ctx,S,node,calls,polled};
}
test('explicit recoverable search bypasses in-page cache and binds both source identities',async()=>{
  const h=setup(),key=h.ctx.searchKey('value','current','金额','数据');h.S.searchCache.set(key,{matches:[]});h.S.searchSession={key,status:'done',matches:[]};h.node('searchRecoverable').checked=true;
  await h.ctx.runSearch();assert.equal(h.calls[0].body.recoverable,true);assert.equal(h.calls[0].body.book,'book');assert.equal(h.calls[0].body.workbookKey,'hash');assert.equal(h.polled[0][0],'search-job');
});
test('search startup delivered after source change is cancelled without taking the new session',async()=>{
  const h=setup();let release;h.ctx.api=(url,opts)=>{if(url==='/api/search/cancel'){h.calls.push(JSON.parse(opts.body));return Promise.resolve({});}return new Promise(resolve=>release=resolve);};
  const pending=h.ctx.runSearch();h.S.book='new';h.S.searchJob='new-query';h.S.searchSession={status:'running',query:'new'};release({jobId:'late-query'});await pending;
  assert.equal(h.S.searchJob,'new-query');assert.equal(h.S.searchSession.query,'new');assert.deepEqual(h.calls,[{id:'late-query'}]);assert.equal(h.polled.length,0);
});
test('late cancellation does not invalidate a newer running search',async()=>{
  const h=setup();h.S.searchJob='old';let release;h.ctx.api=()=>new Promise(resolve=>release=resolve);
  const pending=h.ctx.cancelSearch('old');h.S.searchJob='new';h.S.searchSession={status:'running'};const request=h.S.searchRequestID;release({});await pending;
  assert.equal(h.S.searchJob,'new');assert.equal(h.S.searchSession.status,'running');assert.equal(h.S.searchRequestID,request);
});

test('sorting preserves the full committed advanced predicates including repeated columns',()=>{
  const h=setup(),rules=[{column:2,operator:'gt',value:'10'},{column:2,operator:'lt',value:'20'},{column:3,operator:'equals',value:'keep'}];
  h.S.lastRulesBySheet=new Map([['数据',rules]]);h.S.autoFilter={rules:new Map()};h.S.filtered=true;
  assert.equal(h.ctx.currentQueryRules(),rules);assert.equal(h.ctx.currentQueryRules().length,3);
  h.S.filtered=false;assert.equal(h.ctx.currentQueryRules().length,0);
});

test('resuming a query from the home screen reveals the workspace before taking the task',async()=>{
  const h=setup();let shown=0;h.ctx.showWorkspace=()=>shown++;h.ctx.openDialog=()=>{};
  assert.equal(await h.ctx.adoptRecoveredTask({jobId:'restored',operation:'search'},{book:'book',workbookKey:'hash',query:'金额',sheet:'数据',mode:'value',scope:'current'}),true);
  assert.equal(shown,1);assert.equal(h.S.searchJob,'restored');assert.equal(h.polled[0][0],'restored');
});
