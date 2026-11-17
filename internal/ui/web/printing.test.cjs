const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),vm=require('node:vm');
const read=name=>fs.readFileSync(path.join(__dirname,name),'utf8'),tick=()=>new Promise(resolve=>setImmediate(resolve));
function harness(override){
  const nodes=new Map(),calls=[],html=[],toasts=[],renders=[],bindings=new Map();let sequence=0;
  class Node{
    constructor(tag='div'){this.tagName=tag;this.children=[];this.listeners={};this.attributes={};this.dataset={};this.value=this.defaultValue='';this.checked=this.defaultChecked=false;this.disabled=false;this.open=false;this.hidden=false;this.clientWidth=800;this.clientHeight=600;this.classes=new Set();this.classList={toggle:(key,value)=>{if(value)this.classes.add(key);else this.classes.delete(key);},contains:key=>this.classes.has(key)};this.style={setProperty(key,value){this[key]=value;},removeProperty(key){delete this[key];}};}
    set id(value){this._id=value;nodes.set(value,this);}get id(){return this._id;}
    set textContent(value){this._text=String(value);}get textContent(){return (this._text||'')+this.children.map(n=>n.textContent||'').join('');}
    set innerHTML(value){html.push(value);for(const match of value.matchAll(/<([a-z]+)\b([^>]*\bid="([^"]+)"[^>]*)>/g)){const n=new Node(match[1]);n.id=match[3];const val=/\bvalue="([^"]*)"/.exec(match[2]);if(val)n.value=n.defaultValue=val[1];n.checked=n.defaultChecked=/\bchecked\b/.test(match[2]);n.hidden=/\bhidden\b/.test(match[2]);n.open=/\bopen\b/.test(match[2]);}for(const match of value.matchAll(/<select\b[^>]*id="([^"]+)"[^>]*>([\s\S]*?)<\/select>/g)){const first=/<option\b[^>]*value="([^"]*)"/.exec(match[2]);if(first)nodes.get(match[1]).value=nodes.get(match[1]).defaultValue=first[1];}}
    append(...items){this.children.push(...items);}replaceChildren(...items){this.children=[];this._text='';this.append(...items);}
    addEventListener(key,fn){(this.listeners[key]??=[]).push(fn);}dispatch(key,event={}){for(const fn of this.listeners[key]||[])fn(event);}
    setAttribute(key,value){this.attributes[key]=value;}getAttribute(key){return this.attributes[key];}
    showModal(){this.open=true;}close(){this.open=false;queueMicrotask(()=>this.dispatch('close'));}focus(){document.activeElement=this;}closest(){return null;}
    reset(){for(const n of nodes.values()){n.value=n.defaultValue;n.checked=n.defaultChecked;}}
  }
  const document={body:new Node('body'),getElementById:id=>nodes.get(id),createElement:tag=>new Node(tag),listeners:{},addEventListener(key,fn){(this.listeners[key]??=[]).push(fn);}};
  for(const id of ['printPreviewOpenBtn','menuPrintPreview'])new Node('button').id=id;
  const state={opened:true,indexReady:true,sheet:'明细',sheetEpoch:1,workbookKey:'content',book:'path-one',fileName:'<img src=x>.xlsx'};
  const limits={pages:20,rows:100,columns:20,cells:500,areas:4,parts:50,bytes:65536,requestBytes:8192,notices:32,domPixels:8000000,unitsPerPoint:1000000000,firstPageMin:-32768,firstPageMax:4294967295};
  const source={maxRows:65536,maxColumns:256,printMetadata:true};
  const envelope=id=>({id,...(bindings.get(id)||{workbookKey:'content',book:'path-one',sheet:'明细'})});
  const summary=id=>({...envelope(id),sourceComplete:true,sourceProperties:{headerFooter:{oddHeader:{type:'text',status:'stored',value:'<img src=x onerror=alert(1)>'}}},sourceNotices:[],summary:{sourceId:'a'.repeat(64),sheet:'明细',pages:3,paper:{width:'100000000000',height:'150000000000'},margins:{left:'0',right:'0',top:'0',bottom:'0',header:'0',footer:'0'},headings:{width:'0',height:'0'},scale:{numerator:'1',denominator:'2',percent:'50'},areas:[{row:1,col:1,endRow:3,endCol:1}],firstPageNumber:1,origins:{paper:'source'},notices:[]}});
  const page=(id,index)=>({...envelope(id),page:index,content:{page:{number:index+1,area:1,offsetX:'0',offsetY:'0',content:{width:'15000000000',height:'7500000000'},body:{width:'15000000000',height:'7500000000'},rows:[{first:index+1,last:index+1,offset:'0',length:'15000000000',repeated:false}],columns:[{first:1,last:1,offset:'0',length:'30000000000',repeated:false}]},rows:[{number:index+1,part:0,start:'0',size:'15000000000',clipStart:'0',clipSize:'15000000000',repeated:false}],columns:[{number:1,part:0,start:'0',size:'30000000000',clipStart:'0',clipSize:'30000000000',repeated:false}],cells:[{row:index+1,col:1,ref:'A'+(index+1),style:0,cell:{ref:'A'+(index+1),col:1,raw:'9007199254740993.31',display:'9,007,199,254,740,993.31',hasFormula:false,style:0,type:'n'},box:{x:'0',y:'0',width:'30000000000',height:'15000000000'},clip:{x:'0',y:'0',width:'30000000000',height:'15000000000'}}],styles:{0:{}},notices:[]}});
  const api=async(route,options)=>{const body=options?.body&&JSON.parse(options.body);calls.push({route,body,signal:options?.signal});const special=override?.(route,options,body);if(special!==undefined)return await special;const u=new URL(route,'http://local'),id=u.searchParams.get('id');switch(u.pathname){case '/api/printing/start':{const jobId='print-'+(++sequence);bindings.set(jobId,{workbookKey:body.workbookKey,book:body.book,sheet:body.sheet});return {jobId};}case '/api/printing/status':return {...envelope(id),status:'done'};case '/api/printing/summary':return summary(id);case '/api/printing/page':return page(id,Number(u.searchParams.get('page')));default:return {changed:true};}};
  const context={window:{},document,AbortController,URLSearchParams,setTimeout,clearTimeout,matchMedia:()=>({matches:false})};
  vm.runInNewContext(read('printing-model.js'),context);context.window.ExcelLensPrintingRenderer=class{build(s,p){const v=context.window.ExcelLensPrintingModel.project(s,p,limits),node=new Node();node.textContent=p.content.cells[0].cell.raw;node.dataset.ref=p.content.cells[0].ref;renders.push(node);return {node,width:v.width,height:v.height,warnings:[]};}};
  vm.runInNewContext(read('analysis-jobs.js'),context);vm.runInNewContext(read('printing.js'),context);
  const printing=new context.window.ExcelLensPrinting({state,api,toast:(...args)=>toasts.push(args),source:()=>source,limits,richLimits:{cellRuns:20,viewRuns:50},fontStack:x=>x,sleep:async()=>{}});
  return {printing,state,source,limits,nodes,calls,html,toasts,renders,summary,page,document};
}
test('printing starts only on explicit open and keeps source text out of markup',async()=>{
  const h=harness();assert.equal(h.calls.length,0);await h.printing.open();assert(h.printing.job);assert.equal(h.renders.length,1);assert.equal(h.nodes.get('printPaperWrap').children[0].textContent,'9007199254740993.31');
  assert(h.html.every(text=>!text.includes('<img src=x')));assert.match(h.nodes.get('printRawSettings').textContent,/<img src=x onerror=alert/);assert.match(h.nodes.get('printWarnings').textContent,/页眉或页脚/);
  assert(!h.calls.some(call=>/\/api\/(?:index|analysis|range|layout)/.test(call.route)));
  const id=h.printing.job;h.printing.close();await tick();assert.equal(h.nodes.get('printPaperWrap').children.length,0);assert(h.calls.some(c=>c.route==='/api/printing/clear'&&c.body.id===id));
});
test('cancel and immediate retry reclaim a late start without replacing the accepted page',async()=>{
  let resolve,starts=0;const h=harness(route=>route==='/api/printing/start'&&++starts===1?new Promise(r=>resolve=r):undefined);
  const old=h.printing.open();await tick();h.printing.cancel();await h.printing.refresh();const accepted=h.printing.job;resolve({jobId:'late-print'});await old;
  assert.equal(h.printing.job,accepted);assert(h.calls.some(c=>c.route==='/api/printing/cancel'&&c.body.id==='late-print'));assert.equal(h.printing.busy,false);
});
test('failed candidate first-page loading retains the complete accepted preview and releases the candidate',async()=>{
  let fail=false;const h=harness(route=>fail&&route.startsWith('/api/printing/page?')?Promise.reject(Error('模拟读页失败')):undefined);await h.printing.open();const id=h.printing.job,node=h.nodes.get('printPaperWrap').children[0];
  fail=true;h.printing.changed();await h.printing.refresh();assert.equal(h.printing.job,id);assert.equal(h.nodes.get('printPaperWrap').children[0],node);assert.match(h.nodes.get('printPreviewStatus').textContent,/保留上一次预览.*读页失败/);assert.equal(h.printing.dirty,true);
  assert(h.calls.some(c=>c.route==='/api/printing/clear'&&c.body.id!==id));
});
test('latest page intent wins even when the transport ignores abort; screen zoom performs no new read',async()=>{
  let hold=false,resolve,oldValue;const h=harness(route=>hold&&route.includes('/page?')&&route.includes('page=1')?new Promise(r=>{resolve=r;oldValue=h.page(h.printing.job,1);}):undefined);
  await h.printing.open();hold=true;const old=h.printing.go(1);await tick();await h.printing.go(2);resolve(oldValue);await old;
  assert.equal(h.printing.index,2);assert.equal(h.nodes.get('printPaperWrap').children[0].dataset.ref,'A3');const count=h.calls.length;h.nodes.get('printScreenZoom').value='200';h.printing.zoom();assert.equal(h.calls.length,count);assert.equal(h.printing.rendered.node.style.transform,'scale(2)');
});
test('same-content path changes clear source-bound pages before delayed results arrive',async()=>{
  let hold=false,resolve,oldValue;const h=harness(route=>hold&&route.includes('/page?')?new Promise(r=>{resolve=r;oldValue=h.page(h.printing.job,1);}):undefined);await h.printing.open();hold=true;const pending=h.printing.go(1);await tick();
  h.state.book='same-content-other-path';h.printing.resetBook();resolve(oldValue);await pending;assert.equal(h.printing.page,null);assert.equal(h.printing.job,'');assert.equal(h.printing.dialog.open,false);assert.equal(h.nodes.get('printPaperWrap').children.length,0);assert.equal(h.nodes.get('printMarginLeft').value,'20');
});
test('closing after task completion but before summary adoption releases the completed task',async()=>{
  let resolve;const h=harness(route=>route.includes('/summary?')?new Promise(r=>resolve=r):undefined);const pending=h.printing.open();await tick();const job=h.calls.find(c=>c.route.includes('/summary?')).route.split('id=')[1];h.printing.close();resolve(h.summary(job));await pending;assert.equal(h.printing.job,'');assert(h.calls.some(c=>c.route==='/api/printing/clear'&&c.body.id===job));assert.equal(h.renders.length,0);
});
test('foreign summaries and invalid input never replace a valid page or schedule implicit source changes',async()=>{
  let foreign=false;const h=harness(route=>foreign&&route.includes('/summary?')?{...h.summary('print-2'),book:'foreign-path'}:undefined);await h.printing.open();const node=h.printing.rendered.node;foreign=true;await h.printing.refresh();assert.equal(h.printing.rendered.node,node);assert.match(h.nodes.get('printPreviewStatus').textContent,/来源或任务/);
  const before=h.calls.length;h.nodes.get('printArea').value='IW65537';await h.printing.refresh();assert.equal(h.calls.length,before);assert.match(h.nodes.get('printPreviewStatus').textContent,/超出/);
});
test('unsupported/unprepared sources stay disabled and empty previews fetch no fake page',async()=>{
  const h=harness();h.source.printMetadata=false;h.printing.controls();await h.printing.open();assert.equal(h.calls.length,0);assert.equal(h.nodes.get('printPreviewOpenBtn').disabled,true);
  const e=harness(route=>route.includes('/summary?')?(()=>{const s=e.summary('print-1');s.summary.pages=0;s.summary.areas=null;s.summary.paper={width:'0',height:'0'};s.summary.scale={numerator:'',denominator:'',percent:''};return s;})():undefined);await e.printing.open();assert.equal(e.renders.length,0);assert(!e.calls.some(c=>c.route.includes('/page?')));assert.match(e.nodes.get('printPreviewEmpty').textContent,/没有可见内容/);
});
