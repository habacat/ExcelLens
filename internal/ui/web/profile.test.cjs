const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),vm=require('node:vm');
const tick=()=>new Promise(resolve=>setImmediate(resolve));
function harness(override){
  const nodes=new Map(),events={},calls=[],toasts=[],navigations=[];
  class Node{
    constructor(tag='div'){this.tagName=tag;this.children=[];this.listeners={};this.attributes={};this.className='';this.value='';this.checked=false;this.hidden=true;this.open=false;}
    set textContent(value){this.text=String(value);this.children=[];}get textContent(){return (this.text||'')+this.children.map(n=>n.textContent).join('');}
    append(...values){for(const value of values)if(value.tagName==='#fragment')this.append(...value.children);else this.children.push(value);}
    replaceChildren(...values){this.text='';this.children=[];this.append(...values);}
    setAttribute(key,value){this.attributes[key]=value;}addEventListener(key,fn){(this.listeners[key]??=[]).push(fn);}dispatch(key,event={}){for(const fn of this.listeners[key]||[])fn(event);}
    close(){this.open=false;queueMicrotask(()=>this.dispatch('close'));}showModal(){this.open=true;}
  }
  for(const id of ['profileDialog','profileSummary','profileDetail','profilePending','profileAnalyzeBtn','profileCancelBtn','profileStart','profileEnd','profileFiltered','profileDistribution'])nodes.set(id,new Node());
  const document={getElementById:id=>nodes.get(id),createElement:tag=>new Node(tag),createDocumentFragment:()=>new Node('#fragment'),addEventListener(key,fn){(events[key]??=[]).push(fn);}};
  const context={window:{},document,TextEncoder,AbortController};
  for(const file of ['multiselection.js','analysis-numbers.js','profile-model.js','profile.js'])vm.runInNewContext(fs.readFileSync(path.join(__dirname,file),'utf8'),context);
  const state={opened:true,indexReady:true,workbookKey:'content',book:'path',sheet:'明细',sheetEpoch:1,index:{maxRow:4,maxCol:2},rowView:{id:'view'},selected:{row:2,col:1},usedMaxRow:4,usedMaxCol:2,filtered:false},options={through:0,canRead:true},limits={bytes:2<<20,requestBytes:8192,cells:4000000,bins:10},source={maxRows:1048576,maxColumns:16384};
  const response=req=>({...req,range:req.start+':'+req.end,rows:4,firstColumn:1,lastColumn:1,total:4,nonEmpty:4,blank:0,number:4,text:0,formulas:0,cacheMissing:0,errors:0,sum:'10',average:'2.5',minimum:'1',maximum:'4',sumExact:'10',averageExact:'2.5',minimumExact:'1',maximumExact:'4',unique:4,uniqueTruncated:false,topValues:[{value:'1',count:1},{value:'2',count:1},{value:'3',count:1},{value:'4',count:1}],errorRefs:[],missingRefs:[],filtered:!!req.filterId,...(req.includeDistribution?{distribution:{method:'exact',definition:'inclusive-linear',count:4,quantiles:[{percent:5,value:'1.15'},{percent:25,value:'1.75'},{percent:50,value:'2.5'},{percent:75,value:'3.25'},{percent:95,value:'3.85'}],bins:Array.from({length:10},(_,i)=>({lower:(1+i*.3).toFixed(1),upper:(1+(i+1)*.3).toFixed(1),upperInclusive:i===9,count:[0,3,6,9].includes(i)?1:0})).map((b,i)=>({...b,lower:i===0?'1':b.lower,upper:i===9?'4':b.upper}))}}:{})});
  const api=async(url,init)=>{const req=JSON.parse(init.body);calls.push({url,init,req});const special=override?.(url,init,req);return special===undefined?response(req):await special;};
  const ui=new context.window.ExcelLensProfileCenter({state,api,toast:(...args)=>toasts.push(args),openDialog:id=>nodes.get(id).showModal(),navigateToCell:async(...args)=>navigations.push(args),colName:c=>String.fromCharCode(64+c),limits,source:()=>source,canRead:()=>options.canRead,excludeThroughRow:()=>options.through});
  const all=(node,cls)=>[...(node.className.split(' ').includes(cls)?[node]:[]),...node.children.flatMap(n=>all(n,cls))];
  return {ui,context,state,options,limits,source,nodes,events,calls,toasts,navigations,response,all};
}
test('profile reads only on explicit analyze, exposes optional whole-data percentiles and keeps source values as text',async()=>{
  const h=harness();h.ui.open();assert.equal(h.calls.length,0);await h.ui.analyze();assert.equal(h.calls.length,1);assert.equal(h.ui.page.distribution,undefined);assert.equal(h.nodes.get('profileCancelBtn').hidden,true);assert.equal(h.ui.page.sum,'10');assert(!h.nodes.get('profileDetail').textContent.includes('有理数'));
  h.nodes.get('profileDistribution').checked=true;h.nodes.get('profileDistribution').dispatch('change');assert.equal(h.ui.page,null);await h.ui.analyze();assert.equal(h.calls[1].req.includeDistribution,true);assert(h.nodes.get('profileDetail').textContent.includes('中位数'));assert.equal(h.all(h.nodes.get('profileDetail'),'profile-bin').length,10);assert.equal(h.calls[1].req.bindingViewId,'view');
});
test('display preferences redraw accepted data without a source call and expose exact financial values',async()=>{
  const h=harness((url,init,req)=>({...h.response(req),sum:'9007199254740993.31',sumExact:'9007199254740993.31'}));h.ui.open();await h.ui.analyze();assert(h.nodes.get('profileDetail').textContent.includes('9.01e+15'));const before=h.calls.length;
  const numbers=h.context.window.ExcelLensAnalysisNumbers;numbers.set({...numbers.defaults,scientific:false});for(const fn of h.events['excellens:analysisnumberchange'])fn();assert.equal(h.calls.length,before);assert(h.nodes.get('profileDetail').textContent.includes('9,007,199,254,740,993.31'));assert.equal(h.ui.page.sum,'9007199254740993.31');
});
test('cancel plus immediate retry rejects a late result even if the transport ignores abort',async()=>{
  let hold=true,resolve;const h=harness((url,init,req)=>hold?new Promise(r=>resolve=()=>r({...h.response(req),sum:'999'})):undefined);h.ui.open();const old=h.ui.analyze();await tick();h.ui.cancel();hold=false;await h.ui.analyze();const accepted=h.ui.page;resolve();await old;assert.equal(h.ui.page,accepted);assert.equal(h.calls[0].init.signal.aborted,true);assert.equal(h.nodes.get('profileAnalyzeBtn').disabled,false);
});
test('file path, index, sheet, filter, visible view and frozen changes clear accepted and pending profiles',async()=>{
  for(const change of [h=>h.state.book='other-path',h=>h.state.index={...h.state.index},h=>h.state.sheetEpoch++,h=>h.state.rowView={id:'new'},h=>{h.state.filtered=true;h.state.filterId='new';},h=>h.options.through=1]){
    let hold=false,resolve;const h=harness((url,init,req)=>hold?new Promise(r=>resolve=()=>r(h.response(req))):undefined);h.ui.open();await h.ui.analyze();hold=true;const old=h.ui.analyze();await tick();change(h);h.ui.changed();assert.equal(h.nodes.get('profileDialog').open,false);assert.equal(h.ui.page,null);resolve();await old;assert.equal(h.nodes.get('profileSummary').children.length,0);
  }
});
test('edits cancel unaccepted results while read failures and foreign envelopes preserve the last complete candidate',async()=>{
  let mode='';const h=harness((url,init,req)=>{if(mode==='failure')throw Error('模拟失败');if(mode==='foreign')return {...h.response(req),book:'foreign'};});h.ui.open();await h.ui.analyze();const accepted=h.ui.page;
  for(const value of ['failure','foreign']){mode=value;await h.ui.analyze();assert.equal(h.ui.page,accepted);assert.equal(h.nodes.get('profileSummary').hidden,false);assert.equal(h.nodes.get('profilePending').attributes.role,'alert');}
  h.nodes.get('profileStart').value='B1';h.nodes.get('profileStart').dispatch('input');assert.equal(h.ui.page,null);assert.equal(h.nodes.get('profileSummary').hidden,true);
});
test('partial counts, wrong scope, missing bins, approximate claims and byte overruns are refused before publication',()=>{
  const h=harness();h.ui.open();h.nodes.get('profileDistribution').checked=true;const req=h.ui.input(),validate=p=>h.context.window.ExcelLensProfileModel.validate(p,req,h.limits,h.source);
  for(const change of [p=>p.bindingViewId='foreign',p=>p.filterId='foreign',p=>p.nonEmpty=3,p=>p.number=3,p=>p.cacheMissing=1,p=>p.distribution.method='estimate',p=>p.distribution.count=3,p=>p.distribution.quantiles.pop(),p=>p.distribution.bins.pop(),p=>p.distribution.bins[0].count=2,p=>p.distribution.bins[1].lower='1.2',p=>p.distribution.bins[2].upperInclusive=true]){const p=h.response(req);change(p);assert.throws(()=>validate(p));}
  h.limits.bytes=100;assert.throws(()=>validate(h.response(req)));
});
test('empty numeric results, partially tracked frequencies and untrusted display strings remain explicit and safe',async()=>{
  const h=harness((url,init,req)=>({...h.response(req),number:0,text:4,sum:'0',average:'',minimum:'',maximum:'',sumExact:'0',averageExact:'',minimumExact:'',maximumExact:'',unique:1,uniqueTruncated:true,topValues:[{value:'<script>unsafe</script>',count:4}],warning:'频次达到容量',...(req.includeDistribution?{distribution:{method:'exact',definition:'inclusive-linear',count:0,quantiles:[],bins:[]}}:{})}));h.ui.open();h.nodes.get('profileDistribution').checked=true;await h.ui.analyze();assert(h.ui.page);assert(h.nodes.get('profileDetail').textContent.includes('无可用数值'));assert(h.nodes.get('profileDetail').textContent.includes('常见值（部分）'));assert(h.nodes.get('profileDetail').textContent.includes('<script>unsafe</script>'));assert(h.nodes.get('profileSummary').textContent.includes('≥ 1'));
});
test('a queued native close event cannot cancel a profile that was immediately reopened',async()=>{
  const h=harness();h.ui.open();await h.ui.analyze();h.ui.resetBook();h.ui.open();await h.ui.analyze();await tick();assert(h.ui.page);assert.equal(h.nodes.get('profileDialog').open,true);
});
