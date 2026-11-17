const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const tick=()=>new Promise(resolve=>setImmediate(resolve));
function deferred(){let resolve,reject;const promise=new Promise((yes,no)=>{resolve=yes;reject=no;});return {promise,resolve,reject};}
function harness(override){
  const nodes=new Map(),calls=[],commits=[],tasks=new Map();let next=0,views;
  function element(){const classes=new Set();return {children:[],listeners:new Map(),checked:true,disabled:false,hidden:true,open:false,value:'',textContent:'',style:{setProperty(){}},classList:{toggle(name,on){on?classes.add(name):classes.delete(name);},contains:name=>classes.has(name)},addEventListener(name,fn){this.listeners.set(name,fn);},append(...items){this.children.push(...items);},replaceChildren(...items){this.children=items;},setAttribute(){},close(){this.open=false;this.listeners.get('close')?.();}};}
  const node=id=>{if(!nodes.has(id))nodes.set(id,element());return nodes.get(id);};
  const s={workbookKey:'fingerprint',book:'source-path',sheet:'A',sheetEpoch:1,filterId:'',indexReady:true,rowView:null,rowVisibility:null};
  const snapshot=(id,filterId='',extra={})=>({id,workbookKey:s.workbookKey,book:s.book,sheet:s.sheet,filterId,maxRow:120,totalRows:120,revision:3,digest:'validated-digest',hiddenRanges:[],settings:{mode:'original'},...extra});
  const ctx={window:{},document:{getElementById:node,createElement:element},DOMException,AbortController,Map,Set,Math,Number,JSON,encodeURIComponent,setTimeout};
  for(const file of ['row-visibility.js','row-views.js'])vm.runInNewContext(fs.readFileSync(__dirname+'/'+file,'utf8'),ctx);
  const api=async(url,options={})=>{const body=options.body?JSON.parse(options.body):null;calls.push({url,body,signal:options.signal});const custom=override?.(url,body,options);if(custom!==undefined)return custom;
    const query=new URL(url,'http://qa'),id=query.searchParams.get('id');
    if(query.pathname==='/api/row-view/start'){const id='job-'+(++next);tasks.set(id,snapshot(id,body.filterId,{sheet:body.sheet,settings:body.settings||s.rowView.settings}));return {jobId:id};}
    if(query.pathname==='/api/row-view/status')return {id,status:'done'};
    if(query.pathname==='/api/row-view')return tasks.get(id)||snapshot(id);
    if(query.pathname==='/api/row-view/groups')return {id,total:0,groups:[]};
    if(query.pathname==='/api/status')return {workbookKey:s.workbookKey,book:s.book};
    if(query.pathname==='/api/row-view/cancel'||query.pathname==='/api/row-view/clear')return {};
    throw Error('unexpected '+url);
  };
  views=new ctx.window.ExcelLensRowViews({state:s,api,maximum:()=>120,commit:async(data,guard)=>{if(!guard())return false;views.install(data);commits.push(data.id);return true;},toast(){},navigate(){},reload(){},openDialog:id=>node(id).open=true});
  views.install(snapshot('base'));
  return {views,s,node,calls,commits,snapshot,tasks};
}

test('a cancelled late start owns its cleanup and cannot cancel a newer task through the old caller signal',async()=>{
  const first=deferred(),second=deferred(),signal=new AbortController();let starts=0;
  const h=harness((url)=>{if(url==='/api/row-view/start'&&++starts===1)return first.promise;if(url.startsWith('/api/row-view/status?id=job-1'))return second.promise;});
  const old=h.views.build({signal:signal.signal}),rejected=assert.rejects(old,error=>error.name==='AbortError');
  const fresh=h.views.build();await tick();signal.abort();first.resolve({jobId:'late-old'});await rejected;
  assert.equal(h.calls.some(x=>x.url==='/api/row-view/cancel'&&x.body.id==='job-1'),false);
  assert.ok(h.calls.some(x=>x.url==='/api/row-view/clear'&&x.body.id==='late-old'));
  second.resolve({id:'job-1',status:'done'});assert.equal((await fresh).id,'job-1');
});

test('cancel and retry ignore a late result even when the transport ignores abort',async()=>{
  const pending=deferred();const h=harness(url=>url==='/api/row-view?id=job-1'?pending.promise:undefined);
  const old=h.views.change({action:'hide-all'});await tick();assert.equal(h.views.busy,true);assert.equal(h.node('rowsHideRange').disabled,true);
  h.views.cancel();const fresh=h.views.change({action:'show-all'});assert.equal(await fresh,true);
  pending.resolve(h.snapshot('job-1'));assert.equal(await old,false);
  assert.deepEqual(h.commits,['job-2']);assert.equal(h.s.rowView.id,'job-2');assert.equal(h.node('rowsStatus').textContent,'已更新行显隐');assert.equal(h.views.busy,false);
});

test('cancelling during a pending commit preserves the new controls and prevents obsolete post-commit status',async()=>{
  const pending=deferred(),h=harness();let checked;
  h.views.commit=async(data,guard)=>{h.views.install(data);await pending.promise;checked=guard();return checked;};
  const changing=h.views.change({action:'show-all'});await tick();h.views.cancel();pending.resolve();assert.equal(await changing,false);
  assert.equal(checked,false);assert.equal(h.node('rowsStatus').textContent,'已取消更新');assert.equal(h.node('rowsCancel').hidden,true);
});

test('a source switch or a later preparation cannot publish an obsolete view or its failure state',async()=>{
  for(const mode of ['source','new-task']){
    const response=deferred(),h=harness(url=>url==='/api/row-view?id=job-1'?response.promise:undefined);h.views.cache.clear();const pending=h.views.prepare();await tick();
    if(mode==='source'){h.s.workbookKey='new-book';h.s.sheetEpoch++;h.s.rowViewFailure=false;}
    else await h.views.build();
    response.resolve(h.snapshot('job-1','',{workbookKey:'fingerprint'}));await assert.rejects(pending,error=>error.name==='AbortError');
    assert.equal(h.s.rowView.id,'base');assert.equal(h.s.rowViewFailure,false);
  }
});

test('an expired snapshot rebuilds from its captured settings while a real service error preserves the view',async()=>{
  let failed=false;const h=harness(url=>{if(url==='/api/row-view?id=base'&&!failed){failed=true;throw Object.assign(Error('expired'),{status:404});}});
  h.s.rowView.settings={mode:'hide-all'};const data=await h.views.prepare();assert.equal(data.settings.mode,'hide-all');assert.equal(h.s.rowView.id,data.id);
  const bad=harness(url=>url==='/api/row-view/start'?Promise.reject(Object.assign(Error('settings corrupt'),{status:422})):undefined);
  bad.views.cache.clear();await assert.rejects(bad.views.prepare(),/settings corrupt/);assert.equal(bad.s.rowView.id,'base');assert.equal(bad.s.rowViewFailure,true);
});

test('cross-page revision conflicts preserve the confirmed view and surface the server reason',async()=>{
  const h=harness(url=>url==='/api/row-view/start'?Promise.reject(Object.assign(Error('其他页面已修改'),{status:409})):undefined);
  assert.equal(await h.views.change({action:'hide-all'}),false);assert.equal(h.s.rowView.id,'base');assert.equal(h.node('rowsStatus').textContent,'其他页面已修改');
  assert.equal(h.calls.find(x=>x.url==='/api/row-view/start').body.expectedRevision,3);assert.equal(h.views.busy,false);
});

test('temporary recovery retains its warning and never attempts to overwrite unreadable preferences',async()=>{
  const h=harness();h.s.rowView.settingsWarning='原文件已保留';h.views.controls();assert.equal(h.node('rowsRemember').disabled,true);
  assert.equal(await h.views.change({action:'show-all'}),true);assert.equal(h.calls.find(x=>x.url==='/api/row-view/start').body.persist,false);
  h.calls.length=0;assert.equal(await h.views.reveal(7),true);assert.equal(h.calls.find(x=>x.url==='/api/row-view/start').body.persist,false);
});

test('malformed task identifiers, unknown states, and mismatched result identities fail without polling forever',async()=>{
  for(const stage of ['start','status','source','result']){
    const h=harness(url=>{
      if(stage==='start'&&url==='/api/row-view/start')return {};
      if(stage==='status'&&url.startsWith('/api/row-view/status'))return {status:'mystery'};
      if((stage==='source'||stage==='result')&&url==='/api/row-view?id=job-1')return h.snapshot(stage==='result'?'wrong-id':'job-1','',{book:stage==='source'?'other-source':'source-path'});
    });
    await assert.rejects(h.views.build());assert.equal(h.s.rowView.id,'base');assert.ok(h.calls.length<7);
    if(stage!=='start')assert.ok(h.calls.some(x=>x.url==='/api/row-view/clear'&&x.body.id==='job-1'));
  }
});

test('late outline pages and failures cannot replace the latest page or its status',async()=>{
  const first=deferred(),h=harness(url=>url.includes('/groups?')&&url.includes('offset=0')?first.promise:url.includes('/groups?')?{groups:[],total:401}:undefined);
  const old=h.views.groups();h.views.page=1;await h.views.groups();first.resolve({groups:[],total:200});await old;
  assert.equal(h.node('rowsPage').textContent,'2 / 3');assert.equal(h.views.groupTotal,401);
  const failure=deferred(),late=harness(url=>url.includes('/groups?')?failure.promise:undefined),request=late.views.groups();late.s.rowView=late.snapshot('new-view');late.views.status('新视图');failure.reject(Error('obsolete failure'));await request;
  assert.equal(late.node('rowsStatus').textContent,'新视图');
});

test('snapshot ownership is bounded, cleans evicted owners, and survives a cached snapshot disappearing',async()=>{
  const h=harness();for(let n=1;n<=8;n++){h.s.sheet='Sheet'+n;h.views.install(h.snapshot('view-'+n));}
  assert.equal(h.views.cache.size,4);assert.equal(h.s.rowView.id,'view-8');assert.ok(h.calls.some(x=>x.url==='/api/row-view/clear'&&x.body.id==='base'));
  h.views.resetBook();assert.equal(h.views.cache.size,0);assert.equal(h.s.rowVisibility,null);assert.ok(h.calls.some(x=>x.url==='/api/row-view/clear'&&x.body.id==='view-8'));
});
