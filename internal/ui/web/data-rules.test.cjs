const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),vm=require('node:vm');
const read=name=>fs.readFileSync(path.join(__dirname,name),'utf8'),source=read('data-rules.js'),tick=()=>new Promise(resolve=>setImmediate(resolve));
function harness(override){
  const nodes=new Map(),calls=[],arrivals=[],html=[],toasts=[];let seq=0;
  class Node{
    constructor(tag='div'){this.tagName=tag;this.children=[];this.listeners={};this.attributes={};this.dataset={};this.value='';this.textContent='';this.disabled=false;this.open=false;this.className='';this.classList={toggle(){}};}
    set id(value){this._id=value;nodes.set(value,this);}get id(){return this._id;}
    set innerHTML(text){html.push(text);}get innerHTML(){return html.at(-1)||'';}
    get options(){return this.children;}
    append(...items){this.children.push(...items);if(this.tagName==='select'&&this.children.length===items.length&&items.length)this.value=items[0].value;}
    replaceChildren(...items){this.children=[];this.textContent='';this.append(...items);}
    setAttribute(key,value){this.attributes[key]=value;}getAttribute(key){return this.attributes[key];}removeAttribute(key){delete this.attributes[key];}
    addEventListener(key,fn){(this.listeners[key]??=[]).push(fn);}dispatch(key,event={}){for(const fn of this.listeners[key]||[])fn(event);}
    querySelectorAll(selector){return this.children.filter(n=>selector==='button[data-id]'&&n.tagName==='button'&&n.dataset.id);}
    focus(){document.activeElement=this;}showModal(){this.open=true;}close(){this.open=false;queueMicrotask(()=>this.dispatch('close'));}
  }
  const document={getElementById:id=>nodes.get(id),createElement:tag=>new Node(tag),body:new Node('body'),listeners:{},addEventListener(key,fn){(this.listeners[key]??=[]).push(fn);}};
  for(const id of [...source.matchAll(/id="(dataRule\w+)"/g)].map(m=>m[1]).concat('dataRulesOpenBtn','menuDataRules')){const node=new Node(['dataRuleScope','dataRuleType','dataRuleState'].includes(id)?'select':'div');node.id=id;}
  const Option=function(text,value){const n=new Node('option');n.textContent=text;n.value=value;return n;};
  nodes.get('dataRuleScope').append(new Option('当前单元格','cell'),new Option('本表全部规则','sheet'));
  const state={opened:true,indexReady:true,sheet:'明细',sheetEpoch:1,workbookKey:'content',book:'path-1',fileName:'source.xlsx',selected:{ref:'A2'}};
  const summaries=[{id:1,type:'list',status:'described',sqref:'A1:A5',rangeCount:5,listItems:5,listSource:'inline'},{id:2,type:'whole',status:'invalid',sqref:'',rangeCount:0,listItems:0}];
  const identities=new Map(),areas=Array.from({length:5},(_,i)=>({row:i+1,col:1,endRow:i+1,endCol:1})),values=['001','','<img src=x onerror=alert(1)>','004','005'];
  const api=async(route,options)=>{
    const body=options?.body&&JSON.parse(options.body);calls.push({route,body});const special=override?.(route,options,body);if(special!==undefined)return await special;
    const url=new URL(route,'http://local'),q=url.searchParams,id=q.get('id'),identity=identities.get(id)||{workbookKey:state.workbookKey,book:state.book,sheet:state.sheet};
    if(url.pathname==='/api/data-rules/start'){const jobId='rule-'+(++seq);identities.set(jobId,{workbookKey:body.workbookKey,book:body.book,sheet:body.sheet});return {jobId};}
    if(url.pathname==='/api/data-rules/status')return {id,status:'done',...identity};
    if(url.pathname==='/api/data-rules/items')return {page:{items:summaries,offset:0,previous:0,next:2,matched:2,total:2,scopeComplete:false},ref:q.get('ref'),summary:{rules:2,unresolved:1,unknownScopes:1,complete:true},...identity};
    if(url.pathname==='/api/data-rules/detail'){
      const item=summaries.find(n=>n.id===Number(q.get('item'))),r=Number(q.get('rangeOffset')||0),l=Number(q.get('listOffset')||0),count=item.id===1?5:0;
      return {detail:{item:{...item,ranges:areas.slice(r,Math.min(count,r+3)),inlineList:values.slice(l,Math.min(count,l+3)),formula1:'"001,,中文"',rawXml:'<dataValidation value="<img>">',attributes:{allowBlank:'1'},allowBlank:true},rangeCount:count,rangeOffset:r,rangePrevious:Math.max(0,r-3),rangeNext:Math.min(count,r+3),listCount:count,listOffset:l,listPrevious:Math.max(0,l-3),listNext:Math.min(count,l+3)},...identity};
    }
    if(url.pathname==='/api/data-rules/resolve'){const at=Number(q.get('area')||0);return {area:{sheet:state.sheet,start:'A'+(at+1),end:'A'+(at+1)},...identity};}
    return {changed:true};
  };
  const context={window:{},document,Option,URLSearchParams,AbortController,setTimeout,clearTimeout,JSON,Number,Math,Object,Set};
  vm.runInNewContext(read('analysis-jobs.js'),context);vm.runInNewContext(source,context);
  const rules=new context.window.ExcelLensDataRules({state,api,toast:(...args)=>toasts.push(args),supported:()=>state.supported!==false,limits:{pageRows:3},sleep:async()=>{},navigate:async(area,guard)=>{if(guard()){arrivals.push(area);return true;}return false;}});
  return {rules,state,nodes,calls,arrivals,html,toasts,summaries,document};
}

test('rules use explicit metadata IO, safe text, bounded detail arrays and source navigation',async()=>{
  const h=harness();await h.rules.open();assert.equal(h.rules.detail.item.id,1);assert.equal(h.nodes.get('dataRuleValues').children.length,3);assert.equal(h.nodes.get('dataRuleValues').children[0].textContent,'001');assert.equal(h.nodes.get('dataRuleValues').children[1].textContent,'（空白项）');assert.equal(h.html.length,1);assert(!h.html[0].includes('<img'));
  assert.match(h.nodes.get('dataRuleStatus').textContent,/范围未知/);assert(!h.calls.some(c=>/\/api\/(?:range|cell|index|analysis)/.test(c.route)));
  await h.rules.select(1,false,3,3);assert.equal(h.nodes.get('dataRuleArea').options.length,2);assert.equal(h.nodes.get('dataRuleValues').children.length,2);assert.equal(h.nodes.get('dataRuleArea').value,'3');await h.rules.locate();assert.equal(h.arrivals[0].start,'A4');assert.equal(h.rules.dialog.open,false);
});

test('cancel then immediate retry owns the late start without replacing accepted rules',async()=>{
  let release,starts=0;const h=harness(route=>route==='/api/data-rules/start'&&++starts===1?new Promise(resolve=>release=resolve):undefined);
  const old=h.rules.open();await tick();h.rules.cancel();await h.rules.refresh();const current=h.rules.job;
  release({jobId:'late-rule'});await old;assert.equal(h.rules.job,current);assert(h.calls.some(c=>c.route==='/api/data-rules/cancel'&&c.body.id==='late-rule'));assert.equal(h.rules.busy,false);
});

test('late definitions, page responses and source-path changes cannot overwrite the current selection',async()=>{
  let hold=false,release;const h=harness(route=>hold&&route.includes('/detail?')&&route.includes('item=2')?new Promise(resolve=>release=resolve):undefined);
  await h.rules.open();hold=true;const old=h.rules.select(2);await tick();await h.rules.select(1);release({detail:{item:{id:2}},workbookKey:'content',book:'path-1',sheet:'明细'});await old;assert.equal(h.rules.detail.item.id,1);
  let releasePage;const g=harness(route=>hold&&route.includes('/items?')?new Promise(resolve=>releasePage=resolve):undefined);hold=false;await g.rules.open();hold=true;const pending=g.rules.loadPage(0);await tick();g.state.book='path-2';g.rules.resetBook();releasePage({page:{items:[]},workbookKey:'content',book:'path-1',sheet:'明细'});await pending;assert.equal(g.rules.page,null);assert.equal(g.rules.dialog.open,false);
});

test('same-content paths and sheet epochs reject delayed navigation before moving the grid',async()=>{
  let release;const h=harness(route=>route.includes('/resolve?')?new Promise(resolve=>release=resolve):undefined);await h.rules.open();const old=h.rules.locate();await tick();h.state.book='path-2';h.rules.resetBook();release({area:{sheet:'明细',start:'A1',end:'A5'},workbookKey:'content',book:'path-1',sheet:'明细'});await old;assert.equal(h.arrivals.length,0);
  await h.rules.open();const action=h.rules.locate();await tick();h.state.sheetEpoch++;release({area:{sheet:'明细',start:'A1',end:'A5'},workbookKey:'content',book:'path-2',sheet:'明细'});await action;assert.equal(h.arrivals.length,0);
});

test('a failed refreshed first page preserves the adopted snapshot and releases its candidate',async()=>{
  let fail=false;const h=harness(route=>fail&&route.includes('/items?')?Promise.reject(Error('读取失败')):undefined);await h.rules.open();const accepted=h.rules.job,page=h.rules.page,detail=h.rules.detail;fail=true;await h.rules.refresh();assert.equal(h.rules.job,accepted);assert.equal(h.rules.page,page);assert.equal(h.rules.detail,detail);assert.match(h.nodes.get('dataRuleStatus').textContent,/读取失败/);assert(h.calls.some(c=>c.route==='/api/data-rules/clear'&&c.body.id!==accepted));assert(!h.calls.some(c=>c.route==='/api/data-rules/clear'&&c.body.id===accepted));
});

test('empty hits preserve unknown coverage, and malformed paging cannot be adopted',async()=>{
  let mode='unknown';const h=harness(route=>route.includes('/items?')?{page:{items:[],offset:0,previous:0,next:mode==='bad'?2:0,matched:0,total:0,scopeComplete:mode==='complete'},summary:{rules:0,complete:mode==='complete'},ref:'A2',workbookKey:'content',book:'path-1',sheet:'明细'}:undefined);
  await h.rules.open();assert.match(h.nodes.get('dataRuleList').children[0].textContent,/未读取或范围未知/);mode='complete';await h.rules.loadPage(0);assert.match(h.nodes.get('dataRuleList').children[0].textContent,/没有保存/);const accepted=h.rules.page;mode='bad';await h.rules.loadPage(0);assert.equal(h.rules.page,accepted);assert.match(h.nodes.get('dataRuleStatus').textContent,/分页响应无效/);
});

test('unsupported sources and unprepared sheets stay disabled without querying',async()=>{
  const h=harness();h.state.supported=false;h.rules.controls();await h.rules.open();assert.equal(h.nodes.get('dataRulesOpenBtn').disabled,true);assert.equal(h.calls.length,0);h.state.supported=true;h.state.indexReady=false;h.rules.controls();await h.rules.open();assert.equal(h.calls.length,0);assert.equal(h.rules.dialog.open,false);
});

test('source-bound detail arrays reject excess pages and unknown scopes disable navigation',async()=>{
  let bad=false;const h=harness(route=>bad&&route.includes('/detail?')?{detail:{item:{id:1,ranges:[],inlineList:[]},rangeCount:0,rangeOffset:0,rangePrevious:0,rangeNext:50,listCount:0,listOffset:0,listPrevious:0,listNext:0},workbookKey:'content',book:'path-1',sheet:'明细'}:undefined);await h.rules.open();await h.rules.select(2);assert.equal(h.nodes.get('dataRuleLocate').disabled,true);await h.rules.locate();assert.equal(h.arrivals.length,0);bad=true;await h.rules.select(1);assert.equal(h.rules.detail,null);assert.match(h.nodes.get('dataRuleStatus').textContent,/详情响应无效/);
});

test('a completed status from the wrong source releases its unadopted server owner',async()=>{
  let foreign=false;const h=harness(route=>foreign&&route.startsWith('/api/data-rules/status?')?{id:new URL(route,'http://local').searchParams.get('id'),status:'done',workbookKey:'content',book:'another-path',sheet:'明细'}:undefined);await h.rules.open();const adopted=h.rules.job;foreign=true;await h.rules.refresh();assert.equal(h.rules.job,adopted);assert.match(h.nodes.get('dataRuleStatus').textContent,/来源已切换/);assert(h.calls.some(c=>c.route==='/api/data-rules/cancel'&&c.body.id!==adopted),'completed but rejected status must still release its owned task');
});

test('unknown source enum text cannot resolve JavaScript object prototype properties',async()=>{
  const h=harness();h.summaries[0].type='constructor';h.summaries[0].status='invalid';await h.rules.open();assert.equal(h.nodes.get('dataRuleDetail').children[0].textContent,'#1 constructor');assert.equal(h.nodes.get('dataRuleList').children[0].children[0].textContent,'#1 未知类型：constructor');
});
