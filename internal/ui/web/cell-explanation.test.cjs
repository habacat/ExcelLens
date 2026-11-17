const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),vm=require('node:vm');
const colName=col=>{let result='';for(;col;col=Math.floor((col-1)/26))result=String.fromCharCode(65+(col-1)%26)+result;return result;};
const parseRef=ref=>{const m=/^([A-Z]+)([1-9]\d*)$/.exec(ref);return m?{col:[...m[1]].reduce((n,c)=>n*26+c.charCodeAt(0)-64,0),row:Number(m[2]),ref}:null;};
const tick=()=>new Promise(resolve=>setImmediate(resolve));
function harness(override){
  const nodes=new Map(),calls=[],toasts=[];
  class Node{
    constructor(tag='div'){this.tagName=tag;this.children=[];this.attributes={};this.listeners={};this.open=false;this.hidden=false;this.className='';}
    set textContent(value){this.text=String(value);this.children=[];}get textContent(){return (this.text||'')+this.children.map(n=>n.textContent).join('');}
    append(...values){for(const value of values)if(value.tagName==='#fragment')this.append(...value.children);else this.children.push(value);}
    replaceChildren(...values){this.children=[];this.text='';this.append(...values);}
    setAttribute(key,value){this.attributes[key]=value;}addEventListener(key,fn){(this.listeners[key]??=[]).push(fn);}
    dispatch(key,event={}){for(const fn of this.listeners[key]||[])fn(event);}
    showModal(){this.open=true;}close(){this.open=false;queueMicrotask(()=>this.dispatch('close'));}
  }
  const document={getElementById:id=>nodes.get(id),createElement:tag=>new Node(tag),createDocumentFragment:()=>new Node('#fragment')};
  for(const id of ['detailDialog','detailBody','detailStatus','detailRetry'])nodes.set(id,new Node());
  const context={window:{},document,TextEncoder,AbortController};
  for(const file of ['cellview.js','cell-explanation.js'])vm.runInNewContext(fs.readFileSync(path.join(__dirname,file),'utf8'),context);
  const state={opened:true,indexReady:true,book:'path',workbookKey:'content',sheet:'明细',rowView:{id:'view'},index:{maxRow:4,maxCol:3},sheetEpoch:1,selected:{ref:'A2'},showStyles:true};
  const limits={bytes:2<<20,requestBytes:8192},source={maxRows:1048576,maxColumns:16384,textAmounts:false},options={pinned:1,canRead:true,notice:''};
  const response=req=>({...req,cell:{ref:req.ref,col:parseRef(req.ref).col,raw:'9007199254740993.31',display:'9,007,199,254,740,993.31',type:'n',style:0,hasFormula:false},numeric:{eligible:true,reason:'number',exact:'9007199254740993.31'},formulaState:'none',formulaAvailable:false,rowState:'visible'});
  const api=async(url,init)=>{const req=JSON.parse(init.body);calls.push({url,init,req});const result=override?.(url,init,req);return result===undefined?response(req):await result;};
  const ui=new context.window.ExcelLensCellExplanation({state,api,toast:(...args)=>toasts.push(args),limits,source:()=>source,parseRef,colName,pinnedThroughRow:()=>options.pinned,canRead:()=>options.canRead,notice:()=>options.notice});
  const rows=()=>Object.fromEntries(Array.from({length:nodes.get('detailBody').children.length/2},(_,i)=>[nodes.get('detailBody').children[i*2].textContent,nodes.get('detailBody').children[i*2+1].textContent]));
  return {ui,context,state,nodes,calls,toasts,limits,source,options,response,rows};
}
test('single cell reads only on explicit open and presents exact source facts without nonformula cache or internal labels',async()=>{
  const h=harness();h.ui.changed();assert.equal(h.calls.length,0);await h.ui.open();assert.equal(h.calls.length,1);assert.equal(h.calls[0].url,'/api/cell/explain');
  const rows=h.rows();assert.equal(rows['位置'],'明细!A2');assert.equal(rows['原始值'],'9007199254740993.31');assert.equal(rows['源类型'],'数值');assert.equal(rows['数值统计'],'可用');
  assert(!Object.hasOwn(rows,'计算结果'));assert(!Object.hasOwn(rows,'公式'));assert(!Object.hasOwn(rows,'样式索引'));assert(!Object.hasOwn(rows,'共享公式'));assert.equal(h.nodes.get('detailStatus').textContent,'');
});
test('native text, imported amounts, identifiers, zero and formula caches keep distinct explanations',async()=>{
  let modify=()=>{};const h=harness((url,init,req)=>{const p=h.response(req);modify(p);return p;});
  modify=p=>{p.cell.raw=p.cell.display='1,234.50';p.cell.type='inlineStr';p.numeric={eligible:false,reason:'source-text'};};await h.ui.open();assert.equal(h.rows()['源类型'],'文本');assert.match(h.rows()['数值统计'],/源为文本/);
  h.source.textAmounts=true;modify=p=>{p.cell.raw=p.cell.display='1,234.50';p.cell.type='inlineStr';p.numeric={eligible:true,reason:'imported-amount',exact:'1234.5'};};await h.ui.open();assert.equal(h.rows()['统计数值'],'1234.5');assert.match(h.rows()['数值统计'],/导入金额/);
  modify=p=>{p.cell.raw=p.cell.display='00123';p.cell.type='inlineStr';p.numeric={eligible:false,reason:'text-or-identifier'};};await h.ui.open();assert.equal(h.rows()['原始值'],'00123');assert.match(h.rows()['数值统计'],/编号/);
  modify=p=>{p.cell.raw=p.cell.display='0';p.numeric.exact='0';};await h.ui.open();assert.equal(h.rows()['数值统计'],'可用');assert.equal(h.rows()['原始值'],'0');
  modify=p=>{p.cell.hasFormula=true;p.cell.formula='[无法还原]';p.formulaState='cached';};await h.ui.open();assert.equal(h.rows()['公式'],'原文无法还原');assert.equal(h.rows()['计算结果'],'已保存');
  modify=p=>{p.cell.hasFormula=true;p.cell.formula='=A1*2';p.cell.cacheMissing=true;p.cell.raw='';p.cell.display='#N/A';p.numeric={eligible:false,reason:'missing-result'};p.formulaState='missing';p.formulaAvailable=true;};await h.ui.open();assert.equal(h.rows()['公式'],'=A1*2');assert.equal(h.rows()['计算结果'],'未保存');assert.match(h.rows()['显示值'],/未保存/);
});
test('applied filters use accepted response rules, preserve literal values, and expand long sets only on demand',async()=>{
  const h=harness((url,init,req)=>({...h.response(req),filter:{headerRow:1,rules:[{column:1,operator:'in',value:'',values:['','<img src=x>','00123']},{column:2,operator:'gt',value:'1,234.50'}],sortKeys:[{column:2,descending:true,mode:'number'}]},rowState:'matched'}));
  h.state.filtered=true;h.state.filterId='accepted';await h.ui.open();assert.match(h.rows()['已应用排序'],/B列 降序（数值）/);assert.equal(h.rows()['当前行'],'在筛选结果中');
  const body=h.nodes.get('detailBody'),rules=body.children[body.children.findIndex(n=>n.textContent==='已应用筛选')+1].children[0],details=rules.children[0];assert.equal(details.children.length,1);assert.match(details.textContent,/3 项/);
  details.open=true;details.dispatch('toggle');assert.equal(details.children[1].textContent,'""\n"<img src=x>"\n"00123"');details.dispatch('toggle');assert.equal(details.children.length,2);assert.match(rules.children[1].textContent,/B列 大于 "1,234.50"/);
});
test('cancel and immediate reopen reject late responses even when transport ignores abort',async()=>{
  let hold=false,resolve;const h=harness((url,init,req)=>hold?new Promise(r=>resolve=()=>r(h.response(req))):undefined);
  hold=true;const pending=h.ui.open();await tick();h.ui.resetBook();hold=false;await h.ui.open();const page=h.ui.page;assert.equal(h.calls[0].init.signal.aborted,true);resolve();await pending;await tick();assert.equal(h.ui.page,page);assert.equal(h.ui.dialog.open,true);
});
test('selection, path, index, sheet, filter, view and frozen changes invalidate accepted and pending details',async()=>{
  for(const change of [h=>h.state.selected={ref:'B2'},h=>h.state.book='same-content-other-path',h=>h.state.workbookKey='other',h=>h.state.sheetEpoch++,h=>h.state.index={...h.state.index},h=>{h.state.filtered=true;h.state.filterId='other';},h=>h.state.rowView={id:'other'},h=>h.options.pinned=2]){
    let hold=false,resolve;const h=harness((url,init,req)=>hold?new Promise(r=>resolve=()=>r(h.response(req))):undefined);await h.ui.open();hold=true;const pending=h.ui.load();await tick();change(h);h.ui.changed();assert.equal(h.ui.page,null);assert.equal(h.ui.dialog.open,false);assert.equal(h.calls.at(-1).init.signal.aborted,true);resolve();await pending;assert.equal(h.nodes.get('detailBody').children.length,0);
  }
});
test('failure offers a retry; foreign or internally contradictory candidates never replace complete facts',async()=>{
  let mode='';const h=harness((url,init,req)=>{if(mode==='fail')throw Error('模拟失败');if(mode==='foreign')return {...h.response(req),book:'foreign'};if(mode==='cache')return {...h.response(req),formulaState:'cached'};});
  mode='fail';await h.ui.open();assert.equal(h.nodes.get('detailRetry').hidden,false);assert.match(h.nodes.get('detailStatus').textContent,/模拟失败/);mode='';await h.ui.load();const accepted=h.ui.page,rows=h.rows();
  for(const value of ['foreign','cache']){mode=value;await h.ui.load();assert.equal(h.ui.page,accepted);assert.deepEqual(h.rows(),rows);assert.equal(h.nodes.get('detailRetry').hidden,false);}
});
test('response validation rejects wrong coordinates, fabricated text statistics, partial state and budget overruns',()=>{
  const h=harness(),req=h.ui.binding().request,validate=p=>h.context.window.ExcelLensCellExplanationModel.validate(p,req,h.limits,h.source,parseRef);
  for(const change of [p=>p.cell.ref='B2',p=>p.cell.col=2,p=>p.numeric.exact=123,p=>p.cell.raw='',p=>{p.cell.type='s';p.numeric.reason='imported-amount';},p=>p.numeric.reason='constructor',p=>p.formulaAvailable=true,p=>p.rowState='unknown',p=>p.presentationNotice={},p=>p.filter={headerRow:1,rules:[],sortKeys:[]}]){const p=h.response(req);change(p);assert.throws(()=>validate(p));}
  h.limits.bytes=100;assert.throws(()=>validate(h.response(req)),/容量/);
});
test('blank and missing caches, current presentation notices and untrusted formula text remain factual and safe',async()=>{
  const h=harness((url,init,req)=>{const p=h.response(req);p.cell.raw=p.cell.display='';p.numeric={eligible:false,reason:'blank'};p.rowState='outside';return p;});await h.ui.open();assert.equal(h.rows()['原始值'],'（空白）');assert.equal(h.rows()['源类型'],'空白');assert.equal(h.rows()['当前行'],'已用范围外');h.options.notice='<script>unsafe</script>';await h.ui.load();assert.equal(h.rows()['呈现状态'],'<script>unsafe</script>');
});
test('unready or changing views issue no request and escape aborts the current read',async()=>{
  const h=harness();h.options.canRead=false;await h.ui.open();assert.equal(h.calls.length,0);h.options.canRead=true;await h.ui.open();h.nodes.get('detailDialog').dispatch('cancel',{preventDefault(){}});assert.equal(h.ui.page,null);assert.equal(h.ui.dialog.open,false);
});
