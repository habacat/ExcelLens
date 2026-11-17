const test=require('node:test'),assert=require('node:assert/strict'),vm=require('node:vm'),fs=require('node:fs');
const read=name=>fs.readFileSync(__dirname+'/'+name,'utf8'),deferred=()=>{let resolve,reject;const promise=new Promise((a,b)=>{resolve=a;reject=b;});return{promise,resolve,reject};};
const limits={entries:24,rows:200,series:12,bytes:1<<20,storeBytes:16<<20,pageRows:40};
const cell=exact=>({exact,display:exact,count:exact?1:0});
function snapshot(id){return {version:1,id,name:'快照 '+id,file:'<原文件>.xlsx',created:'2026-11-17T00:00:00Z',workbookKey:'content',book:'path',metric:{field:'C2',aggregate:'sum'},fields:[{id:'C2',name:'金额'}],query:{source:'wb:原表'},result:{columnKeys:['金额'],rows:[{keys:['001'],values:[cell('9007199254740993.31')],total:cell('9007199254740993.31')}],totals:[cell('9007199254740993.31')],grand:cell('9007199254740993.31')}};}
function comparison(left,right){const delta={left:cell('1'),right:cell('2'),difference:cell('1'),change:{display:'100.00%',exact:'1',percent:true}};return {leftId:left.id,rightId:right.id,columnKeys:['金额'],totals:[delta],rows:[{keys:['001'],values:[delta],total:delta}],grand:delta};}
function harness(){
  const nodes=new Map(),draws=[],messages=[];
  class Node{
    constructor(tag='div'){this.tagName=tag;this.children=[];this.listeners={};this.value='';this.hidden=false;this.open=false;this.attributes={};this.classList={toggle(){}};}
    set id(id){this._id=id;nodes.set(id,this);}get id(){return this._id;}
    set innerHTML(value){for(const m of value.matchAll(/<([a-z]+)\b[^>]*\bid="([^"]+)"[^>]*>/g)){const n=new Node(m[1]);n.id=m[2];}}
    append(...n){this.children.push(...n);}replaceChildren(...n){this.children=[...n];}setAttribute(k,v){this.attributes[k]=v;}
    addEventListener(type,fn){(this.listeners[type]??=[]).push(fn);}querySelector(selector){if(selector==='form')return nodes.get('snapshotsCapture');return null;}
    showModal(){this.open=true;}close(){this.open=false;}getBoundingClientRect(){return{width:320,height:64};}
  }
  const env={window:{},document:{body:new Node(),createElement:tag=>new Node(tag),getElementById:id=>nodes.get(id),addEventListener(){}},TextEncoder,AbortController,URLSearchParams,
    Option:function(text,value){this.textContent=text;this.value=value;}};
  for(const file of ['analysis-numbers.js','analysis-comparisons.js','analysis-results.js','chart-export-model.js','snapshot-model.js','snapshots.js'])vm.runInNewContext(read(file),env);
  env.window.ExcelLensAnalyticsCharts={sparkline:(...args)=>draws.push(args)};
  const controller=new env.window.ExcelLensSnapshotsController({api:async()=>{throw Error('Unexpected request');},limits,toast:(...args)=>messages.push(args)});
  controller.dialog.open=true;controller.store={version:1,revision:4,items:[{id:'a',name:'基准',file:'原表',created:'2026-11-17T00:00:00Z'},{id:'b',name:'对比',file:'原表',created:'2026-11-17T00:00:00Z'}]};
  nodes.get('snapshotsLeft').value='a';nodes.get('snapshotsRight').value='b';
  const api=async url=>{if(url.includes('/compare?')){const p=new URL('http://local'+url).searchParams;return comparison(snapshot(p.get('left')),snapshot(p.get('right')));}if(url.includes('?id='))return snapshot(decodeURIComponent(url.split('=')[1]));return controller.store;};controller.api=api;
  return{controller,nodes,draws,env,api};
}

test('snapshot model retains exact strings and refuses wrong identities, shapes and capacity',()=>{
  const {env}=harness(),model=env.window.ExcelLensSnapshotModel,s=snapshot('a');
  assert.equal(model.snapshot(s,'a',limits).result.grand.exact,'9007199254740993.31');
  assert.throws(()=>model.snapshot(s,'b',limits));
  assert.throws(()=>model.snapshot({...s,result:{...s.result,rows:Array(201).fill(s.result.rows[0])}},'a',limits));
  assert.throws(()=>model.snapshot({...s,result:{...s.result,grand:cell(123)}},'a',limits));
  assert.throws(()=>model.comparison(comparison(s,snapshot('b')),s,snapshot('c'),limits));
  assert.equal(model.metric(s),'金额 求和');
});

test('a late pair cannot replace the newly selected complete comparison',async()=>{
  const {controller:c,nodes,api}=harness(),gate=deferred();c.api=url=>url.endsWith('?id=a')?gate.promise:api(url);
  const first=c.loadPair();nodes.get('snapshotsLeft').value='c';nodes.get('snapshotsRight').value='d';await c.loadPair();const accepted=c.accepted;assert.equal(accepted.left.id,'c');
  gate.resolve(snapshot('a'));await first;assert.equal(c.accepted,accepted);assert.equal(c.accepted.comparison.rightId,'d');
});

test('read failure retains previous accepted results and stale pair data is never shown as current',async()=>{
  const {controller:c,nodes}=harness();await c.loadPair();const old=c.accepted;c.api=async()=>{throw Error('读取失败');};
  nodes.get('snapshotsRight').value='c';await c.loadPair();assert.equal(c.accepted,old);assert.match(nodes.get('snapshotsStatus').textContent,/读取失败.*保留上次/);
});

test('a save conflict preserves user name, accepted results and expected revision',async()=>{
  const {controller:c,nodes}=harness();await c.loadPair();const old=c.accepted;let request;
  c.capture={job:'finished',metric:1,workbookKey:'content',book:'path',current:()=>true};nodes.get('snapshotsName').value='月末比较';
  c.api=async(url,opts)=>{request=JSON.parse(opts.body);throw Object.assign(Error('目录已变化，请刷新'),{status:409});};await c.save();
  assert.equal(c.accepted,old);assert.equal(c.store.revision,4);assert.equal(nodes.get('snapshotsName').value,'月末比较');assert.equal(request.jobId,'finished');assert.equal(request.metricIndex,1);assert.equal(request.expectedRevision,4);assert(!('result'in request));assert.equal(c.busy,false);
});

test('closing and reopening ignores late catalog and late committed-save callbacks',async()=>{
  const {controller:c,nodes,api}=harness(),readGate=deferred(),writeGate=deferred();c.api=()=>readGate.promise;const oldRead=c.refresh();c.close();c.dialog.open=true;c.api=api;await c.refresh();const current=c.store;
  readGate.resolve({version:1,revision:1,items:[]});await oldRead;assert.equal(c.store,current);
  c.capture={job:'job',metric:0,workbookKey:'content',book:'path',current:()=>true};nodes.get('snapshotsName').value='新快照';c.api=()=>writeGate.promise;const saving=c.save();c.close();c.dialog.open=true;c.api=api;c.store=current;const epoch=c.epoch;writeGate.resolve({version:1,revision:5,items:[]});await saving;assert.equal(c.epoch,epoch);assert.equal(c.store,current);
});

test('series/page/size redraws are local and both mini trends share the aligned domain',async()=>{
  const {controller:c,nodes,draws}=harness();await c.loadPair();c.api=()=>{throw Error('Redraw must not make a request');};
  nodes.get('snapshotsSeries').value='0';c.renderRows();c.draw();c.draw();const last=draws.slice(-2);assert.equal(last[0][2],last[1][2]);assert.equal(last[0][1][0].exact,'1');assert.equal(last[1][1][0].exact,'2');assert.equal(nodes.get('snapshotsPage').textContent,'1 / 1');
});

test('delete requires a second deliberate action for the same selected snapshot',async()=>{
  const {controller:c,nodes}=harness();let count=0,request;c.write=async r=>{count++;request=r;};await c.remove('Left');assert.equal(count,0);assert.equal(nodes.get('snapshotsDeleteLeft').textContent,'确认删除');
  nodes.get('snapshotsRight').value='a';await c.remove('Right');assert.equal(count,0,'the other side needs its own confirmation even for the same snapshot');assert.equal(nodes.get('snapshotsDeleteLeft').textContent,'删除');
  nodes.get('snapshotsLeft').value='c';await c.remove('Left');assert.equal(count,0);await c.remove('Left');assert.equal(count,1);assert.equal(request.id,'c');
});
