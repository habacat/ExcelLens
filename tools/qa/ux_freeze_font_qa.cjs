// Developer-only Chrome DevTools Protocol scroll regression harness.
// Uses an about:blank page with mocked local APIs: browser administrator rules
// in some CI containers block loopback and file:// navigation.
// Node is NOT part of the Windows viewer's runtime dependencies.
const fs=require('fs');
const path=require('path');
const PORT=Number(process.env.CHROME_PORT||9224);
const ROOT=path.resolve(__dirname,'../../internal/ui/web')+path.sep;
const sleep=ms=>new Promise(r=>setTimeout(r,ms));
(async()=>{
 const pages=await(await fetch(`http://127.0.0.1:${PORT}/json`)).json();
 const target=pages.find(p=>p.type==='page');
 if(!target) throw Error('No Chrome page target');
 const ws=new WebSocket(target.webSocketDebuggerUrl);
 await new Promise((ok,fail)=>{ws.addEventListener('open',ok,{once:true});ws.addEventListener('error',fail,{once:true});});
 let seq=1;const tasks=new Map();
 ws.addEventListener('message',evt=>{let d;try{d=JSON.parse(evt.data);}catch{return;}const p=tasks.get(d.id);if(!p)return;clearTimeout(p.timer);tasks.delete(d.id);d.error?p.reject(Error(d.error.message)):p.resolve(d.result);});
 function cdp(method,params={}){const id=seq++;return new Promise((resolve,reject)=>{const timer=setTimeout(()=>{if(tasks.has(id)){tasks.delete(id);reject(Error(method+' timed out'));}},15000);tasks.set(id,{resolve,reject,timer});ws.send(JSON.stringify({id,method,params}));});}
 async function evaluate(expression){const out=await cdp('Runtime.evaluate',{expression,returnByValue:true,awaitPromise:true});if(out.exceptionDetails)throw Error(out.exceptionDetails.text+' '+JSON.stringify(out.exceptionDetails.exception));return out.result.value;}
 async function waitFor(check,limit=18000){const start=Date.now();while(Date.now()-start<limit){const v=await evaluate(check);if(v)return v;await sleep(120);}throw Error('waitFor timeout: '+check+' '+JSON.stringify(await evaluate('document.querySelector("#globalStatus")?.textContent')));}
 async function shot(name){const r=await cdp('Page.captureScreenshot',{format:'png',captureBeyondViewport:false});fs.writeFileSync('/tmp/'+name+'.png',Buffer.from(r.data,'base64'));}
 await cdp('Page.enable');await cdp('Runtime.enable');await cdp('Emulation.setDeviceMetricsOverride',{width:Number(process.env.QA_WIDTH||1600),height:Number(process.env.QA_HEIGHT||900),deviceScaleFactor:1,mobile:false});
 await cdp('Page.navigate',{url:'about:blank'});
 let html=require('./project_config.cjs').index();
 const css=fs.readFileSync(ROOT+'style.css','utf8');
 html=html.replace(/<link rel="stylesheet" href="\/assets\/style.css">/,'<style>'+css+'</style>');
 html=html.replace(/<script src="\/assets\/(?:clipboard|geometry|freeze|autofilter|export|bookmarks|profile|app).js"><\/script>/g,'');
 await evaluate('document.open();document.write('+JSON.stringify(html)+');document.close();true');
 const mockScript=`(()=>{const sleep=ms=>new Promise(r=>setTimeout(r,ms));let calls=0;
 const addr=(c)=>{let v='';while(c>0){c--;v=String.fromCharCode(65+c%26)+v;c=Math.floor(c/26);}return v;};
 const styles=Array.from({length:12},(_,i)=>({background:i%5===0?'#fff0db':(i%3===0?'#e4f0fa':''),color:i%4===0?'#164c7d':'',bold:i%7===0}));
 window.__mock={get calls(){return calls;},delay:75,failNext:false};
 window.fetch=async(url,options={})=>{
  const u=new URL(url,'http://mock.local');const name=u.pathname;calls++;
  await sleep(name==='/api/sheet' ? window.__mock.delay+(calls%4===0?75:0) : 2);
  if(options.signal?.aborted) throw new DOMException('The operation was aborted.','AbortError');
  if(name==='/api/sheet' && window.__mock.failNext){window.__mock.failNext=false;return new Response(JSON.stringify({error:'test-only transient error'}),{status:503});}
  let data={};
  if(name==='/api/status') data={opened:true,fileName:'UI-Performance-Test.xlsm',fileSize:133337593,sheetCount:2,activeTab:0,sheets:[{name:'Dense Large Sheet',path:'sheet1.xml',state:'visible'},{name:'Extra Sheet',path:'sheet2.xml',state:'visible'}]};
  else if(name==='/api/styles') data={styles};
  else if(name==='/api/bookmarks') data={version:1,items:[]};
  else if(name==='/api/view') data={rows:2,cols:1};
  else if(name==='/api/layout') data={rows:{},cols:{}};
  else if(name==='/api/index/start') data={ready:true,dimension:'A1:IV120000',maxRow:120000,maxCol:256,totalRows:120000};
  else if(name==='/api/search/history') data={history:[]};
  else if(name==='/api/filter/distinct') data={values:[{value:'Value 1:1',count:1},{value:'Value 2:1',count:1},{value:'Value 3:1',count:1}],truncated:false};
  else if(name==='/api/sheet') {
   const startCol=Number(u.searchParams.get('col')||1),cols=Number(u.searchParams.get('cols')||24),limit=Number(u.searchParams.get('limit')||100);
   const start=Number(u.searchParams.get('row')||0)||Number(u.searchParams.get('offset')||0)+1;
   const rows=[];
   for(let n=start;n<Math.min(120001,start+limit);n++){
    const cells=[];
    for(let c=startCol;c<startCol+cols;c++){
     if(c%11===0)continue;
     const formula=(c%7===0),txt=formula?('='+addr(Math.max(1,c-1))+n+'*1.18'):('Value '+n+':'+c);
     cells.push({ref:addr(c)+n,col:c,raw:formula?'100':'Value '+n+':'+c,display:formula?String(Math.round((n+c)*1.18)):'Value '+n+':'+c,formula:formula?txt:'',style:(n+c)%12,hasFormula:formula});
    }
    rows.push({number:n,cells,style:n%3===0?1:0});
   }
   data={sheet:'Dense Large Sheet',dimension:'A1:IV120000',offset:start-1,limit,totalRows:120000,
    totalRowsOriginal:120000,filtered:false,filterId:'',maxRow:120000,maxCol:256,startCol,colCount:cols,rows};
  }
  return new Response(JSON.stringify(data),{status:200,headers:{'Content-Type':'application/json'}});
 };
})()`;
 await evaluate(mockScript);
 for(const module of ['recent.js','multisort.js','selectionstats.js','inspect.js'])await evaluate(fs.readFileSync(ROOT+module,'utf8'));
 await evaluate(fs.readFileSync(ROOT+'clipboard.js','utf8'));
 await evaluate(fs.readFileSync(ROOT+'geometry.js','utf8'));
 await evaluate(fs.readFileSync(ROOT+'freeze.js','utf8'));
 await evaluate(fs.readFileSync(ROOT+'autofilter.js','utf8'));
 await evaluate(fs.readFileSync(ROOT+'export.js','utf8'));
 await evaluate(fs.readFileSync(ROOT+'bookmarks.js','utf8'));
 await evaluate(fs.readFileSync(ROOT+'profile.js','utf8'));
 await evaluate(fs.readFileSync(ROOT+'formula_inspector.js','utf8'));
 await evaluate(fs.readFileSync(ROOT+'app.js','utf8'));
 console.log('loaded scripts');
 await waitFor('document.querySelector("#globalStatus")?.textContent.includes("已打开")');
 console.log('initial',await evaluate('({status:document.querySelector("#globalStatus").textContent,sheet:document.querySelector("#gridStatus").textContent,perf:window.ExcelLensPerformance()})'));
 // Regression: frozen overlays must never cover real native scrollbar
 // gutters. Their size varies with OS/browser/100%-150% DPI settings.
 await waitFor('!document.querySelector("#freezeTop").hidden && !document.querySelector("#freezeLeft").hidden');
 const gutter=await evaluate(`(()=>{
   const grid=document.querySelector('#grid'),top=document.querySelector('#freezeTop'),left=document.querySelector('#freezeLeft'),r=grid.getBoundingClientRect();
   const v=grid.offsetWidth-grid.clientWidth-grid.clientLeft;
   const h=grid.offsetHeight-grid.clientHeight-grid.clientTop;
   const tr=top.getBoundingClientRect(),lr=left.getBoundingClientRect();
   const vh=v>0?document.elementFromPoint(r.right-v/2,r.top+80):null;
   const hh=h>0?document.elementFromPoint(r.left+200,r.bottom-h/2):null;
   return {v,h, topRight:tr.right, leftBottom:lr.bottom, right:r.right,bottom:r.bottom,
     vHit:vh?.id||vh?.className,hHit:hh?.id||hh?.className,
     cssRight:getComputedStyle(top).right,cssBottom:getComputedStyle(left).bottom};})()`);
 if(gutter.v>0&&gutter.topRight>gutter.right-gutter.v+1)throw Error('freezeTop overlaps vertical scrollbar '+JSON.stringify(gutter));
 if(gutter.h>0&&gutter.leftBottom>gutter.bottom-gutter.h+1)throw Error('freezeLeft overlaps horizontal scrollbar '+JSON.stringify(gutter));
 console.log('freeze-pane-scrollbar-gutter',gutter);
 const fontView=await evaluate(`(()=>{
   const sel=document.querySelector('#fontSelect');sel.value='楷体';sel.dispatchEvent(new Event('change',{bubbles:true}));
   const cell=document.querySelector('.grid-cell');
   return {value:sel.value,font:cell?.style.fontFamily,computed:cell?getComputedStyle(cell).fontFamily:null};})()`);
 if(!fontView.font?.includes('KaiTi'))throw Error('font override not applied '+JSON.stringify(fontView));
 console.log('font-override',fontView);
 await evaluate(`(()=>{const sel=document.querySelector('#fontSelect');sel.value='';sel.dispatchEvent(new Event('change',{bubbles:true}));})()`);
 // Excel-like per-column resize: double-click the boundary; frozen headers
 // and the main canvas must keep the same custom coordinate geometry.
 const beforeResize=await evaluate(`(()=>{const h=document.querySelector('.column-head[data-col="4"]');return h?Number.parseFloat(h.style.width):0;})()`);
 await evaluate(`(()=>{const g=document.querySelector('.column-head[data-col="4"] .column-resize-grip');if(!g)throw Error('column resize handle missing');g.dispatchEvent(new MouseEvent('dblclick',{bubbles:true}));return true;})()`);
 await sleep(350);
 await waitFor('window.ExcelLensPerformance().hasVisibleCoverage',12000);
 const colResized=await evaluate(`(()=>{const h=document.querySelector('.column-head[data-col="4"]');return h?Number.parseFloat(h.style.width):0;})()`);
 if(!(colResized>30 && colResized<beforeResize))throw Error('column auto-fit failed '+JSON.stringify({beforeResize,colResized}));
 console.log('column-fit', {beforeResize,colResized});
 const sizeMenu=await evaluate(`(()=>{
   const h=document.querySelector('.column-head[data-col="4"]');
   h.dispatchEvent(new MouseEvent('contextmenu',{bubbles:true,cancelable:true,clientX:250,clientY:150}));
   return {open:!document.querySelector('#sizeContextMenu').hidden,
     title:document.querySelector('#sizeContextAdjust').textContent};
 })()`);
 if(!sizeMenu.open||!sizeMenu.title.includes('D 列宽度')) throw Error('header context resize menu failed '+JSON.stringify(sizeMenu));
 await evaluate("document.querySelector('#grid').click()");
 const pane=await evaluate(`(()=>({frozenCols:document.querySelectorAll('.pinned-head').length,resizeHandles:document.querySelectorAll('.pinned-head .column-resize-grip').length,canvasPx:parseFloat(document.querySelector('#gridCanvas').style.width),scrollbar:getComputedStyle(document.querySelector('#grid')).scrollbarWidth}))()`);
 if(pane.frozenCols<1||pane.resizeHandles<1||pane.canvasPx>50000)throw Error('freeze or trim extent failed: '+JSON.stringify(pane));
 console.log('frozen-column-resize-and-trimmed-scroll-extent',pane);
 const rowGrip=await evaluate(`(()=>{const el=[...document.querySelectorAll('.grid-row .row-resize-grip')].find(x=>Number(x.closest('.grid-row')?.dataset.row)>4);if(!el)throw Error('row resize handle missing');const r=el.getBoundingClientRect();return {x:r.left+r.width/2,y:r.top+r.height/2,row:Number(el.closest('.grid-row')?.dataset.row)};})()`);
 if(rowGrip.y>=0&&rowGrip.y<900){
   await cdp('Input.dispatchMouseEvent',{type:'mouseMoved',x:rowGrip.x,y:rowGrip.y});
   await cdp('Input.dispatchMouseEvent',{type:'mousePressed',x:rowGrip.x,y:rowGrip.y,button:'left',clickCount:1});
   await sleep(70);
   await cdp('Input.dispatchMouseEvent',{type:'mouseMoved',x:rowGrip.x,y:rowGrip.y+24,button:'left',buttons:1});
   await sleep(70);
   await cdp('Input.dispatchMouseEvent',{type:'mouseReleased',x:rowGrip.x,y:rowGrip.y+24,button:'left',clickCount:1});
   await sleep(350);
   await waitFor('window.ExcelLensPerformance().hasVisibleCoverage',12000);
   const height=await evaluate(`(()=>{const el=document.querySelector('.grid-row[data-row="${rowGrip.row}"]');return el?Number.parseFloat(el.style.height):0;})()`);
   if(height<40)throw Error('row height resize failed '+JSON.stringify({rowGrip,height}));
   console.log('row-resize', {row:rowGrip.row,height});
 }

 await shot('excellens-v8-initial');
 // Scroll across overlapping tiles: most DOM nodes must be reused.
 const measurements=[];
 for(let i=0;i<12;i++){
   await evaluate(`document.querySelector('#grid').scrollTo({top:${(i+1)*260},left:${(i+1)*190}})`);
   await sleep(155);
   measurements.push(await evaluate('window.ExcelLensPerformance()'));
 }
 console.log('incremental',measurements.map(m=>[m.reusedRows,m.createdRows,m.createdCells,m.lastMs,m.visibleNodes,m.hasVisibleCoverage]));
 // A-B-A-C fast reversals, with intentionally slow network-like responses.
 await evaluate('window.__mock.delay=240');
 const sequence=[[12000,14000],[1800,1300],[21000,20000],[120,100],[6000,6000],[200,200]];
 for(const [top,left] of sequence){await evaluate(`document.querySelector('#grid').scrollTo({top:${top},left:${left}})`);await sleep(25);}
 await waitFor('window.ExcelLensPerformance().hasVisibleCoverage',12000);
 console.log('rapid-reversal',await evaluate('({scroll:[document.querySelector("#grid").scrollTop,document.querySelector("#grid").scrollLeft],perf:window.ExcelLensPerformance(),liveRows:document.querySelectorAll(".grid-row").length})'));
 const verified=await evaluate(`(()=>{const p=window.ExcelLensPerformance();const left=document.querySelector('#freezeLeft');const top=document.querySelector('#freezeTop');return {coverage:p.hasVisibleCoverage,queue:p.hasPendingRequest,rows:p.currentRows,cells:p.visibleNodes,frozenLeftVisible:left&&!left.hidden,frozenTopVisible:top&&!top.hidden};})()`);
 if(!verified.coverage || verified.queue || !verified.frozenLeftVisible || !verified.frozenTopVisible || verified.cells>14000 || verified.cells<100) throw Error('scroll/freeze verification failed: '+JSON.stringify(verified));
 // A single failed tile fetch must never leave the viewport permanently blank
 // if the user stops scrolling at that coordinate.
 await evaluate("window.__mock.delay=60;window.__mock.failNext=true;document.querySelector('#grid').scrollTo({top:8500,left:12000})");
 await waitFor('window.ExcelLensPerformance().hasVisibleCoverage && !window.ExcelLensPerformance().hasPendingRequest',12000);
 const recovered=await evaluate('({coverage:window.ExcelLensPerformance().hasVisibleCoverage,cellCount:window.ExcelLensPerformance().visibleNodes})');
 if(!recovered.coverage || recovered.cellCount<100) throw Error('transient failure was not recovered: '+JSON.stringify(recovered));
 console.log('transient-error-auto-recovery',recovered);
 await shot('excellens-v8-rapid');
 const keyboard=await evaluate(`(()=>{const c=[...document.querySelectorAll('.grid-cell')].find(x=>x.classList.contains('formula-cell'));if(!c)return {found:false};c.click();const original=document.querySelector('#nameBox').value;const grid=document.querySelector('#grid');const scroll=[grid.scrollLeft,grid.scrollTop];grid.dispatchEvent(new KeyboardEvent('keydown',{key:'ArrowRight',bubbles:true}));return {found:true,original,scroll};})()`);
 await sleep(200);
 console.log('keyboard',keyboard,await evaluate('({cell:document.querySelector("#nameBox").value,scroll:[document.querySelector("#grid").scrollLeft,document.querySelector("#grid").scrollTop]})'));

 // Excel-style auto filter must be available from a selected *worksheet row*,
 // not from the A/B/C column-letter header. The popover must have checklist.
 const auto=await evaluate(`(()=>{
  const head=document.querySelector('.grid-row .row-head');
  if(!head)return {error:'row header not visible'};
  head.dispatchEvent(new MouseEvent('click',{bubbles:true}));
  document.querySelector('#filterOpenBtn').click();
  const arrows=document.querySelectorAll('.grid-cell .auto-filter-button');
  if(!arrows.length)return {error:'no in-cell arrows',header:document.querySelector('#nameBox').value};
  arrows[0].dispatchEvent(new MouseEvent('click',{bubbles:true}));
  return {arrows:arrows.length,popoverVisible:!document.querySelector('#autoFilterPopover').hidden};
 })()`);
 if(auto.error || !auto.popoverVisible || auto.arrows<1)throw Error('AutoFilter failed: '+JSON.stringify(auto));
 await waitFor('document.querySelectorAll("#autoFilterOptions input[type=checkbox]").length >= 3');
 const checked=await evaluate('({all:document.querySelectorAll("#autoFilterOptions input[type=checkbox]").length,selected:[...document.querySelectorAll("#autoFilterOptions input[type=checkbox]")].filter(e=>e.checked).length})');
 if(checked.all<3 || checked.selected<3)throw Error('AutoFilter checkbox failure '+JSON.stringify(checked));
 console.log('excel-style-header-auto-filter',auto,checked);
 const ui=await evaluate(`(async()=>{
  const check={};
  document.querySelector('#autoFilterClose').click();
  document.querySelector('#exportOpenBtn').click();
  check.exportOpen=document.querySelector('#exportDialog').open;
  check.scopes=[...document.querySelector('#exportScope').options].map(x=>x.value);
  check.formats=[...document.querySelector('#exportFormat').options].map(x=>x.value);
  document.querySelector('#exportDialog').close();
  document.querySelector('#profileOpenBtn').click();
  check.profileOpen=document.querySelector('#profileDialog').open;
  document.querySelector('#profileDialog').close();
  document.querySelector('#bookmarkOpenBtn').click();
  check.bookmarkOpen=document.querySelector('#bookmarkDialog').open;
  check.bookmarkCategories=[...document.querySelector('#bookmarkTag').options].map(x=>x.value);
  document.querySelector('#bookmarkDialog').close();
  document.querySelector('#commandOpenBtn').click();
  check.commandOpen=document.querySelector('#commandDialog').open;
  document.querySelector('#commandSearch').value='导出';
  document.querySelector('#commandSearch').dispatchEvent(new Event('input',{bubbles:true}));
  check.commandMatches=document.querySelectorAll('#commandList button').length;
  document.querySelector('#commandDialog').close();
  return check;
 })()`);
 if(!ui.exportOpen||!ui.profileOpen||!ui.bookmarkOpen||!ui.commandOpen||ui.scopes.length!==4||ui.formats.length!==6||ui.bookmarkCategories.length!==5||ui.commandMatches<1)throw Error('V10 new dialogs malfunction '+JSON.stringify(ui));
 console.log('v10-export-inspector-bookmarks-command-palette',ui);
 console.log('api-calls',await evaluate('window.__mock.calls'));
 ws.close();process.exit(0);
})().catch(e=>{console.error(e.stack||e);process.exit(1);});
