const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const tick=()=>new Promise(resolve=>setImmediate(resolve));
function gate(){let resolve,reject;const promise=new Promise((a,b)=>{resolve=a;reject=b;});return {promise,resolve,reject};}
function harness(){
  const nodes=new Map(),calls=[],transforms=[],arrivals=[],html=[];
  class Node{
    constructor(tag='div'){this.tagName=tag;this.children=[];this.listeners={};this.dataset={};this.value='';this.checked=false;this.disabled=false;this.hidden=false;this.textContent='';this.open=false;this.classList={toggle(){}};}
    append(...items){this.children.push(...items);}
    replaceChildren(...items){this.children=[];this.append(...items);}
    setAttribute(name,value){this[name]=value;}
    addEventListener(name,fn){(this.listeners[name]??=[]).push(fn);}
    dispatch(name,event={}){for(const fn of this.listeners[name]||[])fn(event);}
    showModal(){this.open=true;}
    close(){this.open=false;queueMicrotask(()=>this.dispatch('close'));}
    set innerHTML(value){html.push(value);}
  }
  const document={getElementById:id=>nodes.get(id),createElement:tag=>new Node(tag)};
  const markup=fs.readFileSync(__dirname+'/index.html','utf8');
  for(const [,id] of markup.matchAll(/id="(analysisCluster\w+)"/g))nodes.set(id,new Node());
  const state={bookKey:'content',book:'path',opened:true,ready:true,source:'wb:来源',headerRow:1,sourceScope:null,sourceId:''};
  const groups=[{id:0,key:'acme',kind:'similar',count:3,variants:3},{id:1,key:'0007',kind:'exact',count:2,variants:1},{id:2,key:'<img>',kind:'similar',count:2,variants:2}];
  const values=[[{raw:'A',type:'s',count:1,rows:[2],formulaCount:0},{raw:'a',type:'inlineStr',count:1,rows:[3],formulaCount:0},{raw:'Ａ',type:'str',count:1,rows:[4],formulaCount:1}],
    [{raw:'0007',type:'s',count:2,rows:[5,6],formulaCount:0}],
    [{raw:'<img src=x onerror=alert(1)>',type:'s',count:1,rows:[7],formulaCount:0},{raw:'<IMG SRC=x onerror=alert(1)>',type:'s',count:1,rows:[8],formulaCount:0}]];
  let seq=0,hook=()=>undefined;const identities=new Map();
  const envelope=id=>({id,workbookKey:identities.get(id)?.workbookKey||state.bookKey,book:identities.get(id)?.book||state.book,source:state.source,headerRow:1,field:'C1',sourceScope:state.sourceScope});
  const page=(id,offset)=>({...envelope(id),items:groups.slice(offset,offset+2),offset,next:Math.min(groups.length,offset+2),total:groups.length,
    counts:{scanned:8,eligible:7,similarGroups:2,exactGroups:1,blank:1,numeric:0,otherTypes:0,missingCache:0,emptyKey:0}});
  const detail=(id,group,offset=0)=>({...envelope(id),group,key:groups[group].key,kind:groups[group].kind,count:groups[group].count,variants:values[group].slice(offset,offset+2),offset,next:Math.min(values[group].length,offset+2),total:values[group].length});
  const api=async(route,opts={})=>{
    const body=opts.body?JSON.parse(opts.body):null;calls.push({route,body,signal:opts.signal});
    const held=hook(route,body);if(held!==undefined)return await held;
    const url=new URL(route,'http://local'),q=url.searchParams,id=q.get('id');
    if(url.pathname.endsWith('/start')){const jobId='job-'+(++seq);identities.set(jobId,body);return {jobId};}
    if(url.pathname.endsWith('/status'))return {status:'done',id,stage:'完整候选已读取'};
    if(url.pathname.endsWith('/groups'))return page(id,Number(q.get('offset')||0));
    if(url.pathname.endsWith('/detail'))return detail(id,Number(q.get('group')),Number(q.get('offset')||0));
    if(url.pathname.endsWith('/confirm'))return {...envelope(body.id),transform:{id:'D_map',name:'文本核对',source:'C1',op:'map_text',mapping:[{from:'a',to:body.choices[0].representative}]}};
    if(url.pathname.endsWith('/resolve'))return {...envelope(id),ref:'A2',row:2,column:1};
    return {changed:true};
  };
  const context={window:{},document,URLSearchParams,AbortController,setTimeout,clearTimeout,Map,Set};
  for(const file of ['cell-address.js','analysis-jobs.js','analysis-clusters.js'])vm.runInNewContext(fs.readFileSync(__dirname+'/'+file,'utf8'),context);
  const panel=new context.window.ExcelLensAnalysisClusters({api,context:()=>state,limits:{pageGroups:2,pageVariants:2},sleep:async()=>{},toast(){},
    onTransform:(step,guard)=>{if(guard())transforms.push(step);},navigate:(data,guard)=>{if(guard())arrivals.push(data);},startExport:(...args)=>arrivals.push(args)});
  panel.open({id:'C1',name:'机构'});
  return {panel,state,nodes,calls,transforms,arrivals,html,page,detail,envelope,setHook:fn=>hook=fn};
}

test('candidate IO is explicit, raw values stay safe, and a representative is never preselected',async()=>{
  const h=harness();assert.equal(h.calls.length,0);
  await h.panel.run();assert.equal(h.panel.snapshot.job,'job-1');assert.equal(h.panel.choice,null);
  assert.equal(h.nodes.get('analysisClustersConfirm').disabled,true);assert.equal(h.nodes.get('analysisClusterVariants').children.length,2);
  assert.deepEqual(h.calls[0].body.options,{foldCase:false,foldWidth:false,space:'',ignorePunctuation:false});
  await h.panel.select(2);assert.equal(h.nodes.get('analysisClusterVariants').children[0].children[0].children[1].textContent,'<img src=x onerror=alert(1)>');
  assert.equal(h.html.length,0);
  const radio=h.nodes.get('analysisClusterVariants').children[0].children[0].children[0];radio.dispatch('change');
  await h.panel.confirm();assert.equal(h.transforms.length,1);assert.equal(h.transforms[0].op,'map_text');assert.equal(h.nodes.get('analysisClustersConfirm').disabled,true);
  await h.panel.confirm();assert.equal(h.transforms.length,1);assert(!h.calls.some(c=>c.route.includes('/query/start')||c.route.includes('/sheet/batch')));
});

test('group and variant pages preserve only the selected original values and discard old detail responses',async()=>{
  const h=harness();await h.panel.run();await h.panel.select(0,2,true);
  assert.equal(h.panel.detail.variants[0].raw,'Ａ');assert.equal(h.nodes.get('analysisClusterVariants').children.length,1);
  await h.panel.loadPage(2);assert.equal(h.panel.page.items.length,1);assert.equal(h.panel.detail.group,2);
  const held=gate();h.setHook(route=>route.includes('/detail?')&&route.includes('group=0')?held.promise:undefined);
  const old=h.panel.select(0);await tick();await h.panel.select(1);held.resolve(h.detail('job-1',0));await old;
  assert.equal(h.panel.detail.group,1);assert.equal(h.nodes.get('analysisClustersConfirm').disabled,true);
});

test('a failed new first page retains the accepted snapshot and releases only the new completed candidate',async()=>{
  const h=harness();await h.panel.run();const accepted=h.panel.snapshot,page=h.panel.page;
  h.setHook(route=>route.includes('/groups?')&&route.includes('job-2')?Promise.reject(Error('读取失败')):undefined);
  await h.panel.run();assert.equal(h.panel.snapshot,accepted);assert.equal(h.panel.page,page);
  assert.match(h.nodes.get('analysisClustersStatus').textContent,/读取失败/);
  assert(h.calls.some(c=>c.route.endsWith('/clear')&&c.body.id==='job-2'));
  assert(!h.calls.some(c=>c.route.endsWith('/clear')&&c.body.id==='job-1'));
});

test('cancel and immediate retry owns the late start without erasing the newer result',async()=>{
  const h=harness(),held=gate();let first=true;
  h.setHook(route=>{if(route.endsWith('/start')&&first){first=false;return held.promise;}});
  const old=h.panel.run();await tick();h.nodes.get('analysisClustersCancel').dispatch('click');
  await h.panel.run();const newer=h.panel.snapshot;held.resolve({jobId:'late-cancelled'});await old;
  assert.equal(h.panel.snapshot,newer);assert.equal(h.panel.loading,false);assert.equal(h.panel.busy,false);
  assert(h.calls.some(c=>c.route.endsWith('/cancel')&&c.body.id==='late-cancelled'));
});

test('source path changes reject late pages, confirmations and navigation',async()=>{
  for(const mode of ['page','confirm','resolve']){
    const h=harness();await h.panel.run();const held=gate();h.panel.choice='A';
    h.setHook(route=>route.includes(mode==='page'?'/groups?':mode==='confirm'?'/confirm':'/resolve?')?held.promise:undefined);
    const pending=mode==='page'?h.panel.loadPage(2):mode==='confirm'?h.panel.confirm():h.panel.locate(0,0);
    await tick();h.state.book='other-path';h.panel.reset();
    held.resolve(mode==='page'?h.page('job-1',2):mode==='confirm'?{...h.envelope('job-1'),transform:{op:'map_text'}}:{...h.envelope('job-1'),ref:'A2'});
    await pending;assert.equal(h.transforms.length,0);assert.equal(h.arrivals.length,0);assert.equal(h.panel.snapshot,null);
  }
});

test('malformed pagination and a wrong scope cannot replace the accepted snapshot',async()=>{
  const h=harness();await h.panel.run();const accepted=h.panel.snapshot;
  h.setHook(route=>route.includes('/groups?')?{...h.page('job-2',0),next:99}:undefined);
  await h.panel.run();assert.equal(h.panel.snapshot,accepted);assert.match(h.nodes.get('analysisClustersStatus').textContent,/分页/);
  h.setHook(route=>route.includes('/groups?')?{...h.page('job-3',0),sourceScope:{mode:'visible'}}:undefined);
  await h.panel.run();assert.equal(h.panel.snapshot,accepted);assert.match(h.nodes.get('analysisClustersStatus').textContent,/范围不一致/);
});

test('a delayed native close event cannot erase an immediately reopened candidate panel',async()=>{
  const h=harness();await h.panel.run();h.panel.close();h.panel.open({id:'C1',name:'机构'});await tick();
  assert.equal(h.panel.dialog.open,true);assert.equal(h.panel.opened,true);assert(h.panel.snapshot);
  await h.panel.locate(0,0);assert.equal(h.arrivals[0].ref,'A2');assert.equal(h.panel.dialog.open,false);
});

test('reloading a source clears old counts and a lost scoped binding never starts a widened scan',async()=>{
  const h=harness();await h.panel.run();h.panel.reset();
  assert.equal(h.nodes.get('analysisClustersSummary').textContent,'');
  h.state.sourceScope={mode:'visible'};h.panel.open({id:'C1',name:'机构'});
  const starts=h.calls.filter(c=>c.route.endsWith('/start')).length;await h.panel.run();
  assert.equal(h.calls.filter(c=>c.route.endsWith('/start')).length,starts);assert.match(h.nodes.get('analysisClustersStatus').textContent,/范围已释放/);
});
