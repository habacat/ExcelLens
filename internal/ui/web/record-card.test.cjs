const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),vm=require('node:vm');
const read=file=>fs.readFileSync(path.join(__dirname,file),'utf8'),tick=()=>new Promise(resolve=>setImmediate(resolve));
const colName=col=>{let name='';for(;col;col=Math.floor((col-1)/26))name=String.fromCharCode(65+(col-1)%26)+name;return name;};
function harness(override){
  const nodes=new Map(),calls=[],html=[],copied=[],toasts=[],navigations=[],listeners={};
  class Node{
    constructor(tag='div'){this.tagName=tag;this.children=[];this.listeners={};this.attributes={};this.dataset={};this.value='';this.disabled=false;this.hidden=false;this.open=false;this.className='';this.classes=new Set();this.classList={toggle:(key,on)=>on?this.classes.add(key):this.classes.delete(key),contains:key=>this.classes.has(key)};}
    set id(value){this._id=value;nodes.set(value,this);}get id(){return this._id;}
    set textContent(value){this._text=String(value);this.children=[];}get textContent(){return (this._text||'')+this.children.map(n=>n.textContent).join('');}
    set innerHTML(value){html.push(value);for(const match of value.matchAll(/<([a-z]+)\b([^>]*\bid="([^"]+)"[^>]*)>/g)){const n=new Node(match[1]);n.id=match[3];n.value=/\bvalue="([^"]*)"/.exec(match[2])?.[1]||'';n.hidden=/\bhidden\b/.test(match[2]);}for(const match of value.matchAll(/<select\b[^>]*id="([^"]+)"[^>]*>([\s\S]*?)<\/select>/g))nodes.get(match[1]).value=/<option\b[^>]*value="([^"]*)"/.exec(match[2])?.[1]||'';}
    append(...items){for(const n of items)if(n.tagName==='#fragment')this.append(...n.children);else this.children.push(n);}replaceChildren(...items){this._text='';this.children=[];this.append(...items);}
    addEventListener(key,fn){(this.listeners[key]??=[]).push(fn);}dispatch(key,event={}){for(const fn of this.listeners[key]||[])fn(event);}
    setAttribute(key,value){this.attributes[key]=value;}getAttribute(key){return this.attributes[key];}
    showModal(){this.open=true;}close(){this.open=false;queueMicrotask(()=>this.dispatch('close'));}focus(){document.activeElement=this;}closest(){return null;}
    querySelectorAll(selector){const matches=n=>selector.startsWith('.')?n.className.split(' ').includes(selector.slice(1)):n.tagName==='button'&&(selector==='button'||selector==='button[data-unavailable="true"]'&&n.dataset.unavailable==='true');return this.children.flatMap(n=>[...(matches(n)?[n]:[]),...n.querySelectorAll(selector)]);}
  }
  const document={body:new Node('body'),getElementById:id=>nodes.get(id),createElement:tag=>new Node(tag),createDocumentFragment:()=>new Node('#fragment'),addEventListener(key,fn){(listeners[key]??=[]).push(fn);}};
  for(const id of ['recordCardOpenBtn','menuRecordCard'])new Node('button').id=id;
  const source={maxRows:1000000,maxColumns:16384},limits={columns:32,bytes:8<<20,requestBytes:8192,textUnits:4096};
  const state={opened:true,indexReady:true,index:{maxRow:5,maxCol:35,totalRows:5},sheet:'明细',sheetEpoch:1,workbookKey:'content',book:'path',rowView:{id:'view'},selected:{row:2,col:1},fileName:'<img src=x>.xlsx',filtered:true,filterId:'filter'};
  const options={pinned:1,canRead:true,copyResult:true},long='长'.repeat(4095)+'😀<script>危险文本</script>\n'+'尾😀'.repeat(4000),header='名'.repeat(4095)+'😀字段名'.repeat(100);
  const response=request=>{
    const neighbor={4:[1,0,5],5:[2,4,2],2:[3,5,1],1:[4,2,0]}[request.row]||[1,0,0],columns=Math.min(request.columns,35-request.firstColumn+1);
    return {...request,columns,position:neighbor[0],totalRecords:request.row===3?1:4,previousRow:neighbor[1],nextRow:neighbor[2],totalColumns:35,rowStyle:0,headerStyle:0,rowIndexed:true,notices:[],fields:Array.from({length:columns},(_,at)=>{
      const column=request.firstColumn+at,raw=column===1?'000123':column===2?'9007199254740993.31':column===3?long:column===4?'':column===5?'2':String(column),cell={ref:colName(column)+request.row,col:column,raw,display:column===2?'9,007,199,254,740,993.31':raw,hasFormula:column===4||column===5,style:0};
      if(column===1)cell.type='s';if(column===4){cell.cacheMissing=true;cell.sharedFormula=true;cell.formula='[共享公式组 si=9：未找到主公式]';}if(column===5)cell.formula='=1+1';
      return {column,cell,...(request.headerRow?{header:{ref:colName(column)+request.headerRow,col:column,raw:column===1?'':column===3?header:'重复字段',display:column===1?'':column===3?header:'重复字段',hasFormula:false,style:0,type:'s'}}:{})};
    })};
  };
  const api=async(route,init)=>{const body=JSON.parse(init.body);calls.push({route,body,signal:init.signal});const special=override?.(route,init,body);return special!==undefined?await special:response(body);};
  const context={window:{addEventListener(){}},document,AbortController,TextEncoder,Option:function(text,value){const n=new Node('option');n.textContent=text;n.value=value;return n;}};
  for(const file of ['cellview.js','record-card-model.js','record-card.js'])vm.runInNewContext(read(file),context);
  const card=new context.window.ExcelLensRecordCard({state,api,toast:(...args)=>toasts.push(args),copy:async text=>{copied.push(text);return options.copyResult;},navigate:async(sheet,ref,guard)=>{navigations.push({sheet,ref,guard});return guard();},colName,parseColumn:text=>/^\d+$/.test(text)?Number(text):/^[a-z]+$/i.test(text)?[...text.toUpperCase()].reduce((n,c)=>n*26+c.charCodeAt(0)-64,0):0,pinnedThroughRow:()=>options.pinned,source:()=>source,limits,canRead:()=>options.canRead});
  return {card,state,source,limits,options,nodes,calls,html,copied,toasts,navigations,response,long,header,listeners};
}
test('record card reads only on explicit open, preserves source strings and renders untrusted names as text',async()=>{
  const h=harness();assert.equal(h.calls.length,0);await h.card.open();assert.equal(h.calls.length,1);assert.equal(h.card.page.fields.length,32);assert(h.html.every(text=>!text.includes('<img src=x>')));assert.match(h.nodes.get('recordCardSource').textContent,/<img src=x>/);
  assert.match(h.nodes.get('recordCardFields').textContent,/空白字段名/);assert.equal(h.nodes.get('recordCardFields').children.length,32);
  await h.card.copyField(h.card.page.fields[1]);assert.equal(h.copied.at(-1),'9,007,199,254,740,993.31');h.nodes.get('recordCardMode').value='raw';h.nodes.get('recordCardMode').dispatch('change');await h.card.copyField(h.card.page.fields[1]);assert.equal(h.copied.at(-1),'9007199254740993.31');await h.card.copyField(h.card.page.fields[0]);assert.equal(h.copied.at(-1),'000123');assert.equal(h.calls.length,1);
});
test('field pages accept the short last page; previous and next use the source rows from the visible order',async()=>{
  const h=harness();await h.card.open();await h.card.fields(1);assert.equal(h.card.page.firstColumn,33);assert.equal(h.card.page.fields.length,3);assert.equal(h.nodes.get('recordFieldNext').disabled,true);
  await h.card.record(-1);assert.equal(h.calls.at(-1).body.row,5);assert.equal(h.card.page.firstColumn,33);await h.card.record(-1);assert.equal(h.card.page.row,4);assert.equal(h.nodes.get('recordCardPrevious').disabled,true);
  h.nodes.get('recordHeaderRow').value='0';await h.card.apply();assert.equal(h.card.page.headerRow,0);assert.equal(h.card.page.fields[0].header,undefined);assert.match(h.nodes.get('recordCardFields').textContent,/列 AG/);await h.card.fields(-1);assert.equal(h.card.page.firstColumn,1);
});
test('missing caches never acquire a value-copy action and unrecoverable formulas have no formula text action',async()=>{
  const h=harness();await h.card.open();const field=h.card.page.fields[3],article=h.nodes.get('recordCardFields').children[3];assert.match(article.textContent,/公式未保存计算结果/);assert.match(article.textContent,/公式无法还原/);
  assert(article.querySelectorAll('button[data-unavailable="true"]').every(button=>button.disabled));assert.equal(article.querySelectorAll('.record-field-formula').length,0);assert.equal(h.nodes.get('recordCardFields').children[4].querySelectorAll('.record-field-formula').length,1);
  h.card.controls();h.card.cancel();assert(article.querySelectorAll('button[data-unavailable="true"]').every(button=>button.disabled));await h.card.copyField(field);assert.equal(h.copied.length,0);h.card.full(field,'value');assert.equal(h.card.detail,null);
  h.card.full(field,'formula');assert.equal(h.card.detail,null);assert.match(article.textContent,/公式无法还原/);
});
test('long values and field names are segmented for display while full copy remains exact',async()=>{
  const h=harness();await h.card.open();const field=h.card.page.fields[2],article=h.nodes.get('recordCardFields').children[2];assert(article.querySelectorAll('.record-field-value')[0].textContent.length<=h.limits.textUnits);
  for(const [kind,text]of [['value',h.long],['header',h.header]]){
    h.card.full(field,kind);const parts=[];for(let i=0;i<h.card.detail.offsets.length-1;i++){const part=h.nodes.get('recordTextValue').textContent;assert(part.length<=h.limits.textUnits);assert(!/^[\uDC00-\uDFFF]|[\uD800-\uDBFF]$/.test(part));parts.push(part);h.card.textPart(1);}assert.equal(parts.join(''),text);await h.card.copyDetail();assert.equal(h.copied.at(-1),text);h.card.closeDetail();
  }
  h.card.full(h.card.page.fields[4],'formula');assert.equal(h.nodes.get('recordTextValue').textContent,'=1+1');assert.equal(h.calls.length,1);
});
test('cancel then immediate retry rejects a late response even when the transport ignores abort',async()=>{
  let hold=false,resolve,late;const h=harness((route,init,req)=>hold?new Promise(r=>{resolve=r;late=h.response(req);}):undefined);await h.card.open();hold=true;const pending=h.card.record(-1);await tick();h.card.cancel();hold=false;await h.card.record(1);const accepted=h.card.page;assert.equal(accepted.row,1);resolve(late);await pending;assert.equal(h.card.page,accepted);assert.equal(h.card.busy,false);assert.equal(h.calls[1].signal.aborted,true);
});
test('failed and foreign candidates leave the last complete record and its DOM available for retry',async()=>{
  let mode='';const h=harness((route,init,req)=>mode==='fail'?Promise.reject(Error('模拟读取失败')):mode==='foreign'?{...h.response(req),book:'foreign'}:undefined);await h.card.open();const accepted=h.card.page,article=h.nodes.get('recordCardFields').children[0];
  for(const value of ['fail','foreign']){mode=value;await h.card.record(-1);assert.equal(h.card.page,accepted);assert.equal(h.nodes.get('recordCardFields').children[0],article);assert.match(h.nodes.get('recordCardStatus').textContent,/保留上一次完整记录/);assert.equal(h.nodes.get('recordCardNext').disabled,false);}
  mode='';await h.card.record(-1);assert.equal(h.card.page.row,5);
});
test('file path, sheet, exact index, filter, row view and frozen-prefix changes invalidate accepted and late pages',async()=>{
  for(const change of [h=>h.state.book='same-content-other-path',h=>h.state.sheetEpoch++,h=>h.state.index={...h.state.index},h=>h.state.filterId='new-filter',h=>h.state.rowView={id:'new-view'},h=>h.options.pinned=2]){
    let hold=false,resolve,late;const h=harness((route,init,req)=>hold?new Promise(r=>{resolve=r;late=h.response(req);}):undefined);await h.card.open();hold=true;const pending=h.card.record(-1);await tick();change(h);h.card.controls();assert.equal(h.card.dialog.open,false);assert.equal(h.card.page,null);assert.equal(h.calls.at(-1).signal.aborted,true);resolve(late);await pending;assert.equal(h.card.page,null);assert.equal(h.nodes.get('recordCardFields').children.length,0);
  }
});
test('queued native close events cannot erase a card or full-value dialog that was immediately reopened',async()=>{
  const h=harness();await h.card.open();h.card.close();const reopening=h.card.open();await reopening;await tick();assert(h.card.page);assert.equal(h.card.dialog.open,true);
  const field=h.card.page.fields[2];h.card.full(field,'value');h.card.closeDetail();h.card.full(field,'header');await tick();assert.equal(h.card.detail.text,h.header);assert.equal(h.nodes.get('recordTextValue').textContent,h.header.slice(0,4095));
});
test('navigation survives its own asynchronous close event and stops when its captured source changes',async()=>{
  const h=harness();await h.card.open();await h.card.locate(2);await tick();assert.equal(h.card.dialog.open,false);assert.equal(h.navigations[0].ref,'B2');assert.equal(h.navigations[0].guard(),true);h.state.book='other';assert.equal(h.navigations[0].guard(),false);
  await h.card.open();h.card.navigating=true;const ticket=h.card.nav;for(const fn of h.listeners.pointerdown)fn();assert(h.card.nav>ticket);
});
test('copy failure is reported accurately and unfinished row-view changes cannot navigate or read',async()=>{
  const h=harness();await h.card.open();h.options.copyResult=false;await h.card.copyField(h.card.page.fields[0]);assert.match(h.nodes.get('recordCardStatus').textContent,/复制失败/);assert(h.nodes.get('recordCardStatus').classList.contains('is-error'));
  h.card.full(h.card.page.fields[2],'value');await h.card.copyDetail();assert.match(h.nodes.get('recordTextStatus').textContent,/复制失败/);h.card.closeDetail();
  const reads=h.calls.length,copies=h.copied.length;h.options.canRead=false;await h.card.record(-1);await h.card.copyField(h.card.page.fields[0]);await h.card.locate(1);assert.equal(h.calls.length,reads);assert.equal(h.copied.length,copies);assert.equal(h.navigations.length,0);
});
test('invalid field settings issue no request; empty or unprepared sources keep the entry disabled',async()=>{
  const h=harness();await h.card.open();const reads=h.calls.length;for(const [header,column]of [['','A'],['-1','A'],['1.2','A'],['1','ZZZZ'],['1','']]){h.nodes.get('recordHeaderRow').value=header;h.nodes.get('recordStartColumn').value=column;await h.card.apply();assert.equal(h.calls.length,reads);}
  h.card.close();h.state.index={maxRow:1,maxCol:1,totalRows:0};h.state.selected={row:1,col:1};h.card.controls();assert.equal(h.nodes.get('recordCardOpenBtn').disabled,true);await h.card.open();assert.equal(h.calls.length,reads);
});
