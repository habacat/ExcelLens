const test=require('node:test'),assert=require('node:assert/strict'),vm=require('node:vm'),fs=require('node:fs'),path=require('node:path');
const read=name=>fs.readFileSync(path.join(__dirname,name),'utf8'),tick=()=>new Promise(resolve=>setImmediate(resolve));
const source=read('catalogue.js');
function harness(override){
  const nodes=new Map(),calls=[],notices=[],arrivals=[],html=[];let next=0;
  class Node{
    constructor(tag='div'){this.tagName=tag;this.children=[];this.listeners={};this.attributes={};this.dataset={};this.value='';this.textContent='';this.disabled=false;this.open=false;this.className='';this.classList={toggle(){},add(){},remove(){}};}
    set id(value){this._id=value;nodes.set(value,this);}get id(){return this._id;}
    set innerHTML(value){html.push(value);this.html=value;}get innerHTML(){return this.html||'';}
    append(...children){this.children.push(...children);if(this.tagName==='select'&&this.children.length===children.length&&children.length)this.value=children[0].value;}
    replaceChildren(...children){this.children=[];this.textContent='';this.append(...children);}
    addEventListener(type,fn){(this.listeners[type]??=[]).push(fn);}
    dispatch(type,event={}){for(const fn of this.listeners[type]||[])fn(event);}
    setAttribute(name,value){this.attributes[name]=value;}getAttribute(name){return this.attributes[name];}
    querySelectorAll(selector){return this.children.filter(n=>selector==='button[data-id]'&&n.tagName==='button'&&n.dataset.id);}
    focus(){document.activeElement=this;}
    showModal(){this.open=true;}close(){this.open=false;queueMicrotask(()=>this.dispatch('close'));}
  }
  for(const id of [...source.matchAll(/id="(catalogue\w+)"/g)].map(m=>m[1]).concat('catalogueOpenBtn')){const n=new Node(id==='catalogueSheet'||id==='catalogueTargetSheet'||id==='catalogueKind'?'select':'div');n.id=id;}
  const document={getElementById:id=>nodes.get(id),createElement:tag=>new Node(tag),body:new Node('body'),listeners:{},addEventListener(type,fn){(this.listeners[type]??=[]).push(fn);}};
  const Option=function(text,value){const n=new Node('option');n.textContent=text;n.value=value;return n;};
  const state={opened:true,indexReady:true,workbookKey:'content',book:'path-1',fileName:'source.xlsx',sheet:'明细',sheetEpoch:1,selected:{ref:'A1'},sheets:[{name:'明细'},{name:'汇总'}]};
  const baseItems=[{id:'range',kind:'name',name:'金额范围',scope:'',sheet:'',status:'ready',areas:1},{id:'table',kind:'table',name:'账表',scope:'',sheet:'明细',status:'unresolved',areas:0},{id:'external',kind:'name',name:'外部',scope:'',sheet:'',status:'unavailable',areas:0}];
  const area={sheet:'明细',start:'B2',end:'B4',row:2,col:2,endRow:4,endCol:2};
  const identities=new Map();
  const api=async(route,options)=>{
    const body=options?.body&&JSON.parse(options.body);calls.push({route,body});const special=override?.(route,options,body);if(special!==undefined)return await special;
    const parsed=new URL(route,'http://local'),q=parsed.searchParams,id=q.get('id'),identity=identities.get(id)||{workbookKey:state.workbookKey,book:state.book};
    if(parsed.pathname==='/api/catalogue/start'){const jobId='job-'+(++next);identities.set(jobId,{workbookKey:body.workbookKey,book:body.book});return {jobId};}
    if(parsed.pathname==='/api/catalogue/status')return {id,status:'done',stage:'目录读取完成',...identity};
    if(parsed.pathname==='/api/catalogue/items')return {items:baseItems,offset:0,previous:0,next:3,matched:3,total:3,complete:true,notices:[],...identity};
    if(parsed.pathname==='/api/catalogue/detail'){
      const item=baseItems.find(item=>item.id===q.get('item'));return {item:{...item,definition:item.id==='range'?"'明细'!$B$2:$B$4":item.name,reason:item.id==='range'?'':'尚未确认',areas:item.id==='range'?[area]:[]},...identity};
    }
    if(parsed.pathname==='/api/catalogue/resolve')return {area,...identity};return {changed:true};
  };
  const context={window:{},document,Option,URLSearchParams,TextEncoder,AbortController,setTimeout,clearTimeout,JSON,Number,Math};
  vm.runInNewContext(read('analysis-jobs.js'),context);vm.runInNewContext(source,context);
  const directory=new context.window.ExcelLensCatalogue({state,api,toast:(...args)=>notices.push(args),supported:()=>state.supported!==false,limits:{pageRows:3,searchBytes:1024},sleep:async()=>{},navigate:async(value,guard)=>{if(guard()){arrivals.push(value);return true;}return false;}});
  return {directory,state,calls,notices,arrivals,nodes,document,html,area,baseItems,api};
}

test('directory lists metadata, loads selected definitions and resolves source areas without source queries',async()=>{
  const h=harness();await h.directory.open();assert.equal(h.directory.page.total,3);assert.equal(h.directory.item.id,'range');assert.equal(h.nodes.get('catalogueList').children.length,3);assert.equal(h.nodes.get('catalogueLocate').disabled,false);
  await h.directory.locate();assert.equal(h.arrivals.length,1);assert.equal(h.arrivals[0].start,'B2');assert.equal(h.arrivals[0].end,'B4');assert.equal(h.directory.dialog.open,false);assert(h.calls.some(c=>c.route.startsWith('/api/catalogue/resolve?')));assert(!h.calls.some(c=>/\/api\/(?:sheet|range|cell|analysis\/query|index\/start)/.test(c.route)));
  await h.directory.open();await h.directory.select('table',false);assert.equal(h.nodes.get('catalogueLocate').disabled,true);await h.directory.locate();assert.equal(h.arrivals.length,1);assert.match(h.nodes.get('catalogueDetail').children.map(n=>n.textContent).join(' '),/尚未确认/);
});

test('cancel during a delayed start owns its late ID and cannot replace a retry',async()=>{
  let release,starts=0;const h=harness(route=>route==='/api/catalogue/start'&&++starts===1?new Promise(resolve=>release=resolve):undefined);
  const old=h.directory.open();await tick();h.directory.cancel();const retry=h.directory.refresh('read');await retry;
  const current=h.directory.job;release({jobId:'late-owned'});await old;assert.equal(h.directory.job,current);assert(h.calls.some(c=>c.route==='/api/catalogue/cancel'&&c.body.id==='late-owned'));assert.equal(h.directory.busy,false);
});

test('late filtered pages and details cannot overwrite newer selections or reopen a closed dialog',async()=>{
  let holdPage=false,releasePage,holdDetail=false,releaseDetail;const h=harness(route=>{
    if(holdPage&&route.startsWith('/api/catalogue/items?'))return new Promise(resolve=>releasePage=resolve);
    if(holdDetail&&route.includes('/api/catalogue/detail?')&&route.includes('item=table'))return new Promise(resolve=>releaseDetail=resolve);
  });
  await h.directory.open();holdDetail=true;const oldDetail=h.directory.select('table',false);await tick();await h.directory.select('range',false);releaseDetail({item:{id:'table',name:'迟到表',definition:'late'},workbookKey:'content',book:'path-1'});await oldDetail;assert.equal(h.directory.item.id,'range');
  holdPage=true;const old=h.directory.loadPage(0);await tick();h.directory.close();holdPage=false;await h.directory.open();const accepted=h.directory.page;releasePage({items:[],offset:0,previous:0,next:0,matched:0,total:0,complete:true,notices:[],workbookKey:'content',book:'path-1'});await old;assert.equal(h.directory.page,accepted);assert(h.directory.dialog.open);
});

test('equal content at another path rejects old responses and interrupts pending navigation',async()=>{
  let release;const h=harness(route=>route.startsWith('/api/catalogue/resolve?')?new Promise(resolve=>release=resolve):undefined);
  await h.directory.open();const pending=h.directory.locate();await tick();h.state.book='path-2';h.directory.resetBook();release({area:h.area,workbookKey:'content',book:'path-1'});await pending;assert.equal(h.arrivals.length,0);assert.equal(h.directory.item,null);
  h.state.book='path-1';await h.directory.open();const action=h.directory.locate();await tick();for(const handler of h.document.listeners.pointerdown)handler({});release({area:h.area,workbookKey:'content',book:'path-1'});await action;assert.equal(h.arrivals.length,0);
});

test('malformed source bindings cannot publish definitions or navigate, and unknown formats keep the entry disabled',async()=>{
  let bad=false;const h=harness(route=>bad&&route.startsWith('/api/catalogue/detail?')?{item:{id:'table',name:'foreign'},workbookKey:'content',book:'other-path'}:undefined);
  await h.directory.open();bad=true;await h.directory.select('table',false);assert.equal(h.directory.item,null);assert.match(h.nodes.get('catalogueStatus').textContent,/来源已切换/);
  h.state.supported=false;h.directory.resetBook();assert.equal(h.nodes.get('catalogueOpenBtn').disabled,true);await h.directory.open();assert.equal(h.directory.dialog.open,false);
});

test('literal source names stay out of HTML and long column lists have bounded page DOM',async()=>{
  const malicious='<img src=x onerror=alert(1)>',h=harness(route=>route.includes('/api/catalogue/detail?')?{item:{id:'range',kind:'table',name:malicious,definition:malicious,reason:'原文保留',status:'unresolved',table:{headerRows:1,totalRows:0,columns:Array.from({length:11},(_,i)=>({id:String(i),name:'列'+i}))}},workbookKey:'content',book:'path-1'}:undefined);
  await h.directory.open();assert.equal(h.html.length,1);assert(!h.html[0].includes(malicious));const detail=h.nodes.get('catalogueDetail'),table=detail.children.find(n=>n.tagName==='table');assert.equal(table.children[0].children.length,3);
  const paging=detail.children.at(-1);paging.children.at(-1).onclick();const next=detail.children.find(n=>n.tagName==='table');assert.equal(next.children[0].children.length,3);assert.equal(next.children[0].children[0].children[1].textContent,'列3');
});

test('confirmation is explicit and needs the prepared selected sheet, while search rejects over-budget input',async()=>{
  const h=harness();await h.directory.open();h.nodes.get('catalogueTargetSheet').value='汇总';h.directory.controls();assert.equal(h.nodes.get('catalogueConfirm').disabled,true);
  const before=h.calls.length;await h.directory.refresh('confirm');assert.equal(h.calls.length,before);
  h.nodes.get('catalogueTargetSheet').value='明细';h.directory.controls();await h.directory.refresh('confirm');assert(h.calls.some(c=>c.body?.mode==='confirm'&&c.body.sheet==='明细'));
  h.nodes.get('catalogueSearch').value='长'.repeat(400);const queries=h.calls.length;await h.directory.loadPage(0);assert.equal(h.calls.length,queries);assert.match(h.nodes.get('catalogueStatus').textContent,/超过容量/);
});

test('a refresh page failure keeps the previous directory and releases only the unadopted candidate',async()=>{
  let failed=false;const h=harness(route=>failed&&route.startsWith('/api/catalogue/items?')?Promise.reject(Error('读取失败')):undefined);
  await h.directory.open();const job=h.directory.job,item=h.directory.item,page=h.directory.page;failed=true;await h.directory.refresh('read');
  assert.equal(h.directory.job,job);assert.equal(h.directory.item,item);assert.equal(h.directory.page,page);assert.match(h.nodes.get('catalogueStatus').textContent,/读取失败/);assert(h.calls.some(c=>c.route==='/api/catalogue/clear'&&c.body.id!==job));assert(!h.calls.some(c=>c.route==='/api/catalogue/clear'&&c.body.id===job));
});

test('an unconfirmed table offers its real owning sheet without preparing or changing the current view',async()=>{
  const h=harness();await h.directory.open();h.baseItems[1].sheet='汇总';await h.directory.select('table',false);
  assert.equal(h.nodes.get('catalogueTargetSheet').value,'汇总');assert.equal(h.nodes.get('catalogueConfirm').disabled,true);assert.equal(h.state.sheet,'明细');
  await h.directory.openSheet();assert.equal(h.arrivals[0].sheet,'汇总');assert.equal(h.arrivals[0].start,'A1');
});

test('shared grid arrival preserves source coordinates and rejects filtered rectangles and late guards',async()=>{
  const app=read('app.js'),start=app.indexOf('  async function navigateCatalogueArea('),end=app.indexOf('\n  async function moveSelection(',start);assert(start>=0&&end>start);
  let selected,release,allowed=true;const state={opened:true,book:'path',workbookKey:'content',sheet:'明细',sheets:[{name:'明细'}],selected:{ref:'A1'},indexReady:true,filtersBySheet:new Map(),filtered:false,multiRanges:['old']};
  const ctx={S:state,Error,parseRef:ref=>{const m=/^([A-Z])(\d+)$/.exec(ref);return m?{ref,col:m[1].charCodeAt(0)-64,row:Number(m[2])}:null;},rowViews:null,showWorkspace(){},navigateToCell:async(sheet,ref,trace,guard)=>{if(release!==false)await new Promise(resolve=>release=resolve);if(guard())state.selected={ref};},activeMerge:()=>null,colName:n=>String.fromCharCode(64+n),ensureExtent(){},extendRange:(anchor,row,col)=>selected={anchor,row,col},$:()=>({value:''})};
  vm.runInNewContext(app.slice(start,end)+';this.go=navigateCatalogueArea;',ctx);
  const area={sheet:'明细',start:'B2',end:'D8'};const first=ctx.go(area,()=>allowed);await tick();allowed=false;release();assert.equal(await first,false);assert.equal(selected,undefined);
  allowed=true;release=false;state.filtered=true;await assert.rejects(()=>ctx.go(area,()=>true),/先清除筛选/);assert.equal(state.filtered,true);
  state.filtered=false;assert.equal(await ctx.go(area,()=>true),true);assert.equal(selected.anchor.row,2);assert.equal(selected.row,8);assert.equal(selected.col,4);assert.equal(state.multiRanges.length,0);
});
