const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const read=name=>fs.readFileSync(__dirname+'/'+name,'utf8');
const tick=()=>new Promise(resolve=>setImmediate(resolve));
const deferred=()=>{let resolve,reject;const promise=new Promise((a,b)=>{resolve=a;reject=b;});return {resolve,reject,promise};};
const result=value=>({value,exact:value,display:value,copy:value});
function setup(api=async()=>result('3'),recoveryRecords){
  const elements=new Map(),timers=new Map();let timer=0;const document={activeElement:null,handlers:{},addEventListener(name,fn){(this.handlers[name]??=[]).push(fn);}};
  class Element{
    constructor(tag='div',id=''){this.tagName=tag.toUpperCase();this.id=id;this.children=[];this.parent=null;this.dataset={};this.handlers={};this.attrs={};this.style={};this.hidden=false;this.scrollTop=0;this._value='';this.textContent='';this.className='';this.selectionStart=this.selectionEnd=0;const classes=new Set();this.classList={add:(...x)=>x.forEach(s=>classes.add(s)),remove:(...x)=>x.forEach(s=>classes.delete(s)),contains:s=>classes.has(s),toggle:(s,on)=>{on=on??!classes.has(s);on?classes.add(s):classes.delete(s);}};}
    set value(v){this._value=String(v);this.selectionStart=this.selectionEnd=this._value.length;}get value(){return this._value;}
    addEventListener(name,fn){(this.handlers[name]??=[]).push(fn);}
    emit(name,extra={}){const e={target:this,preventDefault(){this.prevented=true;},stopPropagation(){this.stopped=true;},...extra};for(const fn of this.handlers[name]||[])fn(e);return e;}
    append(...nodes){for(const n of nodes){this.children.push(n);n.parent=this;}}
    replaceChildren(...nodes){this.children=[];this.append(...nodes);}
    setAttribute(name,value){this.attrs[name]=value;} removeAttribute(name){delete this.attrs[name];}
    focus(){document.activeElement=this;if(this.dataset.formulaRow!==undefined||this.dataset.noteRow!==undefined)elements.get('calculatorRows').emit('focusin',{target:this});}
    setSelectionRange(a,b){this.selectionStart=a;this.selectionEnd=b;}
    select(){this.setSelectionRange(0,this.value.length);}scrollIntoView(){}
    closest(selector){for(let node=this;node;node=node.parent)for(const s of selector.split(',')){if(s.startsWith('#')&&node.id===s.slice(1)||s.startsWith('.')&&node.className.split(' ').includes(s.slice(1))||s==='.column-head[data-col]'&&node.className.split(' ').includes('column-head')&&node.dataset.col!==undefined||s==='button'&&node.tagName==='BUTTON'||s==='[data-formula-row]'&&node.dataset.formulaRow!==undefined||s==='[data-result-row]'&&node.dataset.resultRow!==undefined||s==='[data-note-row]'&&node.dataset.noteRow!==undefined)return node;}return null;}
    matches(selector){return selector.split(',').includes(this.tagName.toLowerCase());}
    contains(node){return node===this||this.children.some(x=>x.contains(node));}
    getBoundingClientRect(){return {left:parseFloat(this.style.left)||760,top:parseFloat(this.style.top)||54,width:parseFloat(this.style.width)||440,height:370};}
    get clientHeight(){return this.id==='calculatorScroll'?parseFloat(this.style.height)||156:0;} get scrollHeight(){return this.id==='calculatorScroll'?parseFloat(elements.get('calculatorRows').style.height)||0:0;}
    querySelectorAll(){return [];}setPointerCapture(){}getContext(){return {measureText:s=>({width:String(s).length*7})};}
  }
  for(const m of read('index.html').matchAll(/<(\w+)\b[^>]*\bid="([^"]+)"[^>]*>/g)){const el=new Element(m[1],m[2]);el.hidden=/\shidden(?:\s|>)/.test(m[0]);elements.set(el.id,el);}
  for(const [id,el] of elements)if(id.startsWith('calculator')&&id!=='calculatorWindow')el.parent=elements.get('calculatorWindow');
  elements.get('grid').className='grid-region';
  document.getElementById=id=>{assert(elements.has(id),'unknown DOM id '+id);return elements.get(id);};document.createElement=tag=>new Element(tag);
  const schedule=fn=>{const id=++timer;timers.set(id,fn);return id;};
  const context={document,window:{innerWidth:1200,innerHeight:800,addEventListener(){}},AbortController,setTimeout:schedule,clearTimeout:id=>timers.delete(id),requestAnimationFrame:schedule,cancelAnimationFrame:id=>timers.delete(id)};
  for(const name of ['cell-address.js','viewport-cache.js','command-palette.js','virtualscroll.js','cellview.js','clipboard.js','formula-entry-model.js','calculator-model.js','calculator.js'])vm.runInNewContext(read(name),context);
  if(recoveryRecords){Object.assign(context,{TextEncoder,crypto:require('node:crypto').webcrypto,sessionStorage:{getItem(){return null;},setItem(){}}});context.window.ExcelLensSession={id:'recovery-page-0000000001',ready:Promise.resolve()};vm.runInNewContext(read('recovery.js'),context);}
  const state={workbookKey:'file-A',sheet:'数据',indexReady:true},copied=[],toasts=[];
  const wrappedAPI=(url,options)=>url.startsWith('/api/recovery?')?Promise.resolve({revision:1,records:recoveryRecords}):url==='/api/recovery'?Promise.resolve({revision:JSON.parse(options.body).revision+1}):url==='/api/calculator/functions'?Promise.resolve({items:[{name:'SUM',signature:'SUM(数值1, …)'},{name:'SUBSTITUTE',signature:'SUBSTITUTE(文本, 原文本, 新文本)'},{name:'IF',signature:'IF(条件, 成立时, 不成立时)'},{name:'LEFT',signature:'LEFT(文本, 字符数)'}]}):api(url,options);
  const calc=new context.window.ExcelLensCalculator({state,api:wrappedAPI,grid:elements.get('grid'),copy:async s=>copied.push(s),toast:s=>toasts.push(s)});calc.open();
  const type=(index,text)=>{calc.focus(index);const input=calc.views.get(index).input;input.value=text;calc.body.emit('input',{target:input});return input;};
  const key=(input,key,extra={})=>{const e={target:input,key,preventDefault(){this.prevented=true;},...extra};calc.handleKey(e);return e;};
  return {calc,state,elements,context,document,timers,copied,toasts,type,key};
}

test('pasting formula rows decodes quoted cells and refuses extra columns before changing any row',async()=>{
  const h=setup(async()=>result('3')),input=h.calc.views.get(0).input;
  h.calc.paste({target:input,clipboardData:{getData:()=> '"=IF(TRUE,""a"",""b"")"\r\n=1+2\r\n'},preventDefault(){}});await tick();
  assert.equal(h.calc.model.get(0).text,'=IF(TRUE,"a","b")');assert.equal(h.calc.model.get(1).text,'=1+2');
  h.calc.paste({target:input,clipboardData:{getData:()=> '=10\t=20\n'},preventDefault(){}});
  assert.equal(h.calc.model.get(0).text,'=IF(TRUE,"a","b")');assert.match(h.toasts.at(-1),/1列/);
});

test('restored formulas wait through focus changes, notes and Escape until explicitly calculated',async()=>{
  let calls=0;const h=setup(async()=>{calls++;return result('3');},{0:{text:'=SUM(1,2',note:'待复核',source:{workbookKey:'file-A',sheet:'数据'}}});
  await h.calc.recovery.ready;assert.equal(h.calc.model.get(0).restored,true);assert.equal(h.calc.views.get(0).value.textContent,'待计算');
  h.calc.focus(1);h.calc.focus(0,'note');h.calc.model.setNote(0,'只改备注');h.calc.hide();h.calc.open();assert.equal(calls,0);assert.equal(h.calc.model.get(0).text,'=SUM(1,2');
  h.type(0,'=100');h.key(h.calc.views.get(0).input,'Escape');assert.equal(h.calc.model.get(0).text,'=SUM(1,2');h.calc.focus(1);assert.equal(calls,0);
  h.calc.focus(0);h.key(h.calc.views.get(0).input,'Enter');await tick();assert.equal(calls,1);assert.equal(h.calc.model.get(0).text,'=SUM(1,2)');assert.equal(h.calc.model.get(0).result.display,'3');
});

test('the window contains just a formula/result table and title controls',()=>{
  const {calc,elements}=setup();assert.equal(calc.views.size,5);assert(!elements.has('calculatorPick'));assert(!elements.has('calculatorNote'));assert(!elements.has('calculatorHistoryBtn'));assert(!elements.has('calculatorCopyProcess'));
  assert.match(read('index.html'),/role="columnheader">公式<\/span><span role="columnheader">结果<\/span>/);assert.equal(calc.picking,false);
});
test('an obsolete close animation cannot hide a reopened calculation window',async()=>{
  const {calc,context}=setup();let finish;calc.host.animate=()=>({});context.window.ExcelLensMotion={enter(){},exit:()=>new Promise(resolve=>finish=resolve)};
  calc.minimize();assert.equal(calc.host.hidden,false);calc.open();finish(true);await new Promise(resolve=>setImmediate(resolve));assert.equal(calc.host.hidden,false);
  calc.hide();calc.toggle();finish(true);await new Promise(resolve=>setImmediate(resolve));assert.equal(calc.host.hidden,false);
});

test('a calculation exit does not steal focus from a newly opened control',async()=>{
  const {calc,context,document,elements}=setup();let finish;calc.host.animate=()=>({});context.window.ExcelLensMotion={enter(){},exit:()=>new Promise(resolve=>finish=resolve)};
  calc.minimize();elements.get('nameBox').focus();finish(true);await tick();assert.equal(calc.host.hidden,true);assert.equal(document.activeElement,elements.get('nameBox'));
  calc.open();calc.host.focus();calc.hide();finish(true);await tick();assert.equal(document.activeElement,elements.get('grid'));
});
test('typing equals and clicking cells builds a normal formula without a pick button',async()=>{
  const calls=[];const {calc,type,key}=setup(async(url,o)=>{calls.push(JSON.parse(o.body));return result('0.3');});
  type(0,'=');assert.equal(calc.picking,true);calc.captureCell('A2',{detail:1});assert.equal(calc.model.get(0).text,'=A2');
  type(0,'=A2+');calc.captureCell('B3',{detail:1});assert.equal(calc.model.get(0).text,'=A2+B3');assert.equal(calls.length,0);
  key(calc.views.get(0).input,'Enter');await tick();assert.equal(calls[0].expression,'=A2+B3');assert.equal(calc.views.get(0).value.textContent,'0.3');assert.equal(calc.model.active,1);assert.equal(calc.picking,false);
});
test('clicking a different cell replaces the current pointed reference without inventing an operator',()=>{
  const {calc,type}=setup();type(0,'=');calc.captureCell('A1',{});calc.captureCell('B2',{});assert.equal(calc.model.get(0).text,'=B2');
  type(0,'=100+A1*2');calc.views.get(0).input.setSelectionRange(5,7);calc.captureCell('C4',{});assert.equal(calc.model.get(0).text,'=100+C4*2');
});
test('cross-sheet picks retain the original formula sheet and quote the selected sheet name',async()=>{
  let request;const {calc,state,type}=setup(async(url,o)=>{request=JSON.parse(o.body);return result('1');});
  type(0,'=1+');state.sheet="O'Brien";calc.captureCell('A2',{});assert.equal(calc.model.get(0).text,"=1+'O''Brien'!A2");
  await calc.commit(0);assert.equal(request.sheet,'数据');assert.equal(request.workbookKey,'file-A');
});
test('dragging a range inserts A1:A10 in SUM and keeps the input focused',()=>{
  const {calc,type,document}=setup();type(0,'=SUM(');calc.captureRange('A1','A10');assert.equal(calc.model.get(0).text,'=SUM(A1:A10');assert.equal(document.activeElement,calc.views.get(0).input);
  type(0,'=SUM(A1:A10)');assert.equal(calc.model.get(0).text,'=SUM(A1:A10)');
});

test('whole column and row picks keep colon references and reuse replacement and sheet quoting',async()=>{
  let request;const {calc,type,state,document}=setup(async(url,options)=>{request=JSON.parse(options.body);return result('60');});
  type(0,'=SUM(');calc.captureRange('B:B');assert.equal(calc.model.get(0).text,'=SUM(B:B');
  calc.captureRange('A:C');assert.equal(calc.model.get(0).text,'=SUM(A:C');
  calc.captureRange('2:2');assert.equal(calc.model.get(0).text,'=SUM(2:2');assert.equal(document.activeElement,calc.views.get(0).input);
  state.sheet="O'Brien";calc.captureRange('D:D');assert.equal(calc.model.get(0).text,"=SUM('O''Brien'!D:D");
  await calc.commit(0);assert.equal(request.expression,"=SUM('O''Brien'!D:D)");assert.equal(request.sheet,'数据');
});

test('formula picking preserves header focus and leaves resize and filter controls usable',()=>{
  const {calc,type,document,elements}=setup();type(0,'=SUM(');
  for(const className of ['column-head','column-head pinned-head','row-head','pinned-row-head']){
    const head=document.createElement('div');head.className=className;head.dataset.col='2';head.parent=elements.get('grid');
    const dispatch=target=>{const event={target,preventDefault(){this.prevented=true;}};for(const handler of document.handlers.pointerdown)handler(event);return event;};
    assert(dispatch(head).prevented,className);
    for(const control of ['column-resize-grip','row-resize-grip','column-filter','auto-filter-button']){const child=document.createElement('span');child.className=control;child.parent=head;assert(!dispatch(child).prevented,control);}
    calc.editing=null;assert(!dispatch(head).prevented);calc.activate(0);
  }
});

test('whole-column range endpoints do not trigger function completion when Enter calculates',async()=>{
  const {calc,type,key,context}=setup();await tick();calc.catalog=[{name:'CEILING',signature:'CEILING(数值, 基数)'},{name:'SUM',signature:'SUM(数值1, …)'}];
  const input=type(0,'=SUM(');calc.captureRange('A:C');await calc.updateCompletion();
  assert.notEqual(calc.completionMode,'list');assert.equal(context.window.ExcelLensFormulaInput.context('=SUM(A:C',8).prefix,'');
  key(input,'Enter');await tick();assert.equal(calc.model.get(0).text,'=SUM(A:C)');assert.equal(calc.model.get(0).result.display,'3');
});
test('formula rows calculate independently even when responses arrive in reverse order',async()=>{
  const calls=[];const {calc,type}=setup(async()=>{const d=deferred();calls.push(d);return d.promise;});
  type(0,'=1+2');const a=calc.commit(0);type(1,'=3+4');const b=calc.commit(1);
  calls[1].resolve(result('7'));await b;calls[0].resolve(result('3'));await a;
  assert.equal(calc.views.get(0).value.textContent,'3');assert.equal(calc.views.get(1).value.textContent,'7');
});
test('a stale evaluation cannot overwrite an edited row',async()=>{
  const calls=[];const {calc,type}=setup(async()=>{const d=deferred();calls.push(d);return d.promise;});
  type(0,'=1+2');const old=calc.commit(0);type(0,'=10+20');const fresh=calc.commit(0);
  calls[1].resolve(result('30'));await fresh;calls[0].resolve(result('3'));await old;assert.equal(calc.views.get(0).value.textContent,'30');
});
test('Escape restores the last committed formula and native undo is not intercepted',async()=>{
  const {calc,type,key}=setup();type(0,'=1+2');await calc.commit(0);type(0,'=1+');key(calc.views.get(0).input,'Escape');assert.equal(calc.model.get(0).text,'=1+2');assert.equal(calc.views.get(0).value.textContent,'3');assert.equal(calc.picking,false);
  type(0,'=9');const e=key(calc.views.get(0).input,'z',{ctrlKey:true});assert(!e.prevented);assert.equal(calc.model.get(0).text,'=9');
});
test('a workbook switch aborts pending source formulas and leaves completed results',async()=>{
  const d=deferred();const {calc,type,state}=setup(()=>d.promise);type(0,'=A2+1');const pending=calc.commit(0);state.workbookKey='file-B';calc.bookChanged();d.resolve(result('999'));await pending;assert.equal(calc.views.get(0).value.textContent,'#REF!');assert.equal(calc.picking,false);
});
test('multi-line formula paste fills rows and computes without showing a workflow',async()=>{
  const {calc,type}=setup();const input=type(0,'');const e=calc.body.emit('paste',{target:input,clipboardData:{getData:()=>'=1+2\r\n=2+1\r\n'}});await tick();
  assert(e.prevented);assert.equal(calc.model.get(0).text,'=1+2');assert.equal(calc.model.get(1).text,'=2+1');assert.equal(calc.views.get(1).value.textContent,'3');assert.equal(calc.model.active,2);
});
test('inline errors clear stale results, and result Ctrl+C copies the displayed value',async()=>{
  let fails=false;const {calc,type,key,copied}=setup(async()=>{if(fails)throw Error('除数不能为0');return result('3');});type(0,'=1+2');await calc.commit(0);key(calc.views.get(0).value,'c',{ctrlKey:true});await tick();assert.deepEqual(copied,['3']);
  fails=true;type(0,'=1/0');assert.equal(calc.views.get(0).value.textContent,'');await calc.commit(0);assert.equal(calc.views.get(0).value.textContent,'#DIV/0!');assert.equal(calc.model.get(0).result,null);
});
test('native numeric literals and long identifiers are not converted through Number',async()=>{
  const {calc,type}=setup();type(0,'000123456789012345678901');await calc.commit(0);assert.equal(calc.views.get(0).value.textContent,'000123456789012345678901');
});
test('the input keeps focus when selecting a source cell; normal table clicks do not get captured',()=>{
  const {calc,type,document,elements}=setup();const cell=document.createElement('div');cell.className='grid-cell';cell.parent=elements.get('grid');type(0,'=');
  const event={target:cell,preventDefault(){this.prevented=true;}};for(const fn of document.handlers.pointerdown)fn(event);assert(event.prevented);
  assert.equal(calc.captureCell('A2',{detail:1}),true);calc.editing=null;assert.equal(calc.captureCell('A3',{detail:1}),false);
});
test('the draggable window stays within the viewport',()=>{
  const {calc,elements,timers}=setup();calc.host.style.left='1190px';calc.host.style.top='790px';calc.clamp();assert.equal(calc.host.style.left,'752px');assert.equal(calc.host.style.top,'422px');
  const drag=elements.get('calculatorDrag');drag.emit('pointerdown',{button:0,pointerId:1,clientX:800,clientY:450});drag.emit('pointermove',{pointerId:1,clientX:-1000,clientY:-1000});drag.emit('pointerup',{pointerId:1});assert.equal(calc.host.style.left,'8px');assert.equal(calc.host.style.top,'8px');assert.equal(timers.size,0);
});
test('the complete application boots with the formula table and one keyboard dispatcher',()=>{
  const {context,document}=setup(),extra=document.createElement('div');extra.content='token';document.querySelector=s=>s==='dialog:modal'?null:extra;document.querySelectorAll=()=>[];
  context.window.ExcelLensSession={ready:new Promise(()=>{})};context.ResizeObserver=class{observe(){}};context.Headers=Headers;context.URLSearchParams=URLSearchParams;context.window.location={search:""};
  vm.runInNewContext(read('formats.js'),context);document.getElementById('fileFormats').textContent=JSON.stringify({extensions:[],sources:{xlsx:{maxRows:1048576,styled:true}},clipboard:{rows:10,columns:5,cells:50,textBytes:10000,htmlBytes:10000},taskRecovery:{tasks:2}});
  vm.runInNewContext(read('transport.js'),context);
  vm.runInNewContext(read('background-tasks.js'),context);
  vm.runInNewContext(read('storage.js'),context);
  const app=read('app.js');for(const m of app.matchAll(/new window\.(ExcelLens\w+)/g))if(!context.window[m[1]])context.window[m[1]]=class{};
  context.window.ExcelLensFormulaInspector={FormulaInspector:class{}};
  assert.doesNotThrow(()=>vm.runInNewContext(app,context));assert.equal(document.handlers.keydown.length,1);
  assert.equal((app.match(/if\(!calculating\)\$\("grid"\)\.focus/g)||[]).length,2);
});

test('date/time suggestion waits for a click and inserts an explicit conversion formula',async()=>{
  for(const [kind,formula] of [['date','=DATEVALUE(A2)'],['time','=TIMEVALUE(A2)'],['datetime','=DATEVALUE(A2)+TIMEVALUE(A2)']]){
    const calls=[],suggestion={kind,label:'转换',preview:'2026-01-02'};
    const {calc,type}=setup(async(url,options)=>{calls.push(JSON.parse(options.body));return calls.length===1?{kind:'text',value:'2026/1/2',display:'2026/1/2',copy:'2026/1/2',suggestions:[suggestion]}:result('46024');});
    type(0,'=A2');await calc.commit(0);assert.equal(calc.model.get(0).text,'=A2');assert.equal(calls.length,1);assert.equal(calc.model.get(0).result.kind,'text');
    calc.applyConversion(0,suggestion);await tick();assert.equal(calc.model.get(0).text,formula);assert.equal(calls[1].expression,formula);assert.equal(calc.model.get(0).result.display,'46024');
  }
});

test('an obsolete conversion hint cannot replace a newly edited formula',async()=>{
  const suggestion={kind:'date',label:'转换',preview:'2026-01-02'},calls=[];
  const {calc,type}=setup(async(url,options)=>{calls.push(JSON.parse(options.body));return {kind:'text',display:'2026/1/2',suggestions:[suggestion]};});
  type(0,'=A2');await calc.commit(0);type(0,'=B2+1');calc.applyConversion(0,suggestion);
  assert.equal(calc.model.get(0).text,'=B2+1');assert.equal(calls.length,1);
});

test('the calculation viewport grows from five rows to twelve, with no data row cap',()=>{
  const {calc,type}=setup();assert.equal(calc.scroll.style.height,'156px');type(4,'1');assert.equal(calc.scroll.style.height,'182px');
  type(20,'1');assert.equal(calc.scroll.style.height,'338px');assert(calc.model.rowCount>20);assert(calc.views.size<=20);
  calc.focus(1000000);assert.equal(calc.model.active,1000000);assert(calc.views.has(1000000));assert(calc.model.rowCount>1000000);assert(calc.model.rows.size<10);assert(calc.pool.length<=21);assert(calc.virtual.scale>1);
});
test('recycled rows remain bounded and keep the active formula when scrolling elsewhere',()=>{
  const {calc,type}=setup();const input=type(0,'=1+');calc.model.ensure(10000);calc.render();
  for(let i=1;i<30;i++){calc.scroll.scrollTop=i*700;calc.scroll.emit('scroll');assert.equal(calc.views.get(0).input,input);assert.equal(calc.editing,0);assert(calc.pool.length<=21);}
  const visible=calc.pool.filter(v=>!v.tr.hidden);assert.equal(new Set(visible.map(v=>v.index)).size,visible.length);
});
test('minimize removes the window and restores the same rows from the status button',async()=>{
  const {calc,type,elements}=setup();type(0,'=1+2');calc.minimize();await tick();assert.equal(calc.host.hidden,true);assert.equal(elements.get('calculatorDock').hidden,false);assert.equal(elements.get('calculatorHomeDock').hidden,false);
  elements.get('calculatorDock').emit('click');assert.equal(calc.host.hidden,false);assert.equal(elements.get('calculatorDock').hidden,true);assert.equal(calc.model.get(0).result.display,'3');assert(!calc.host.classList.contains('is-minimized'));
});
test('completion uses the shared function catalog and Enter closes missing parentheses',async()=>{
  const {calc,type,key}=setup();const input=type(0,'=su');await tick();assert.equal(calc.popup.hidden,false);assert(calc.suggestions.some(f=>f.name==='SUM'));
  calc.suggestion=calc.suggestions.findIndex(f=>f.name==='SUM');key(input,'Tab');await tick();assert.equal(calc.model.get(0).text,'=SUM(');assert.equal(calc.model.active,0);
  calc.captureRange('A2','A3');key(input,'Enter');await tick();assert.equal(calc.model.get(0).text,'=SUM(A2:A3)');assert.equal(calc.model.active,1);assert.equal(calc.model.get(0).result.display,'3');
});
test('completion does not rewrite quoted text, and boolean/text results render as values',async()=>{
  const {calc,type,context}=setup(async()=>({kind:'boolean',display:'TRUE',copy:'TRUE',exact:'TRUE'}));type(0,'="SUM("');await tick();assert.equal(calc.popup.hidden,true);assert.equal(context.window.ExcelLensFormulaInput.close('="("'),'="("');
  type(0,'=1=1');await calc.commit(0);assert.equal(calc.views.get(0).value.textContent,'TRUE');assert(calc.views.get(0).value.classList.contains('is-boolean'));
});

test('dotted function names and digit suffixes share completion and signature parsing',async()=>{
  const {calc,type,context}=setup();await tick();
  calc.catalog=[{name:'NORM.S.DIST',signature:'NORM.S.DIST(数值, 累计)'},{name:'LOG10',signature:'LOG10(数值)'}];
  type(0,'=norm.s.d');await tick();assert.equal(calc.suggestions[0].name,'NORM.S.DIST');
  calc.complete('NORM.S.DIST');await tick();assert.equal(calc.model.get(0).text,'=NORM.S.DIST(');
  assert.equal(context.window.ExcelLensFormulaInput.context('=NORM.S.DIST(0,',15).fn,'NORM.S.DIST');
  assert.equal(context.window.ExcelLensFormulaInput.context('=LOG10(',7).fn,'LOG10');
});
test('pasting over 200 formulas keeps a bounded DOM and only four evaluations in flight',async()=>{
  let running=0,maximum=0;const {calc,type}=setup(async()=>{running++;maximum=Math.max(maximum,running);await tick();running--;return result('3');});
  const input=type(0,'');calc.body.emit('paste',{target:input,clipboardData:{getData:()=>Array.from({length:1000},()=> '=1+2').join('\n')}});const promises=[...calc.jobs.values()].map(job=>job.promise);await Promise.all(promises);
  assert.equal(calc.model.get(999).result.display,'3');assert.equal(calc.model.active,1000);assert.equal(maximum,4);assert(calc.pool.length<=21);assert.equal(calc.jobs.size,0);
});
test('the optional notes column keeps annotations when hidden and does not change formula results',async()=>{
  let calls=0;const {calc,type,elements}=setup(async()=>{calls++;return result('3');});assert.equal(calc.notes,false);type(0,'=1+2');await calc.commit(0);const revision=calc.model.get(0).revision;
  elements.get('calculatorNotes').emit('click');assert.equal(calc.notes,true);assert(calc.host.classList.contains('has-notes'));
  const note=calc.views.get(0).note;note.focus();note.value='待复核';calc.body.emit('input',{target:note});
  assert.equal(calc.model.get(0).note,'待复核');assert.equal(calc.model.get(0).revision,revision);assert.equal(calc.model.get(0).result.display,'3');assert.equal(calc.picking,false);
  calc.toggleNotes();assert.equal(calc.notes,false);assert.equal(calc.model.get(0).note,'待复核');assert.equal(calc.picking,false);
  calc.toggleNotes();assert.equal(calc.views.get(0).note.value,'待复核');calc.minimize();calc.open();assert.equal(calc.model.get(0).note,'待复核');
  assert.equal(calc.model.get(0).result.display,'3');assert.equal(calls,1);
});
test('notes can mark otherwise empty rows and remain attached through virtual scrolling',()=>{
  const {calc}=setup();calc.toggleNotes();calc.focus(1000,'note');const note=calc.views.get(1000).note;note.value='抽查标记';calc.body.emit('input',{target:note});
  assert.equal(calc.model.get(1000).note,'抽查标记');assert(calc.model.rowCount>1000);calc.focus(0);calc.focus(1000,'note');assert.equal(calc.views.get(1000).note.value,'抽查标记');assert(calc.pool.length<=21);
});

test('notes round-trip restores position and respects a deliberate move while expanded',()=>{
  const {calc}=setup(),initial=calc.host.getBoundingClientRect();
  for(let i=0;i<5;i++){calc.toggleNotes();assert.equal(calc.host.getBoundingClientRect().left+calc.host.getBoundingClientRect().width,initial.left+initial.width);calc.toggleNotes();assert.equal(calc.host.getBoundingClientRect().left,initial.left);assert.equal(calc.host.getBoundingClientRect().width,initial.width);}
  calc.toggleNotes();calc.host.style.left=parseFloat(calc.host.style.left)-40+'px';calc.toggleNotes();assert.equal(calc.host.getBoundingClientRect().left,initial.left-40);
});
