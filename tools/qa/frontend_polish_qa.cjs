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
 window.__mock={get calls(){return calls;},delay:75,failNext:false, mode:'original',overrides:{}};
 window.fetch=async(url,options={})=>{
  const u=new URL(url,'http://mock.local');const name=u.pathname;calls++;
  await sleep(name==='/api/sheet' ? window.__mock.delay+(calls%4===0?75:0) : 2);
  if(options.signal?.aborted) throw new DOMException('The operation was aborted.','AbortError');
  if(name==='/api/sheet' && window.__mock.failNext){window.__mock.failNext=false;return new Response(JSON.stringify({error:'test-only transient error'}),{status:503});}
  let data={};
  if(name==='/api/status') data={opened:true,fileName:'UI-Performance-Test.xlsm',fileSize:133337593,sheetCount:2,activeTab:0,sheets:[{name:'Dense Large Sheet',path:'sheet1.xml',state:'visible'},{name:'Extra Sheet',path:'sheet2.xml',state:'visible'}]};
  else if(name==='/api/styles') data={styles};
  else if(name==='/api/bookmarks') data={version:1,items:[]};
  else if(name==='/api/view') data={rows:0,cols:0};
  else if(name==='/api/layout') {
   if(options.method==='POST'){
     const body=JSON.parse(options.body||'{}');
     if(body.kind==='visibility') {
       if(body.mode==='column')window.__mock.overrides[String(body.index)]=Boolean(body.hidden);
       else{window.__mock.mode=body.mode;window.__mock.overrides={};}
     }
   }
   data={rows:{},cols:{},columnMode:window.__mock.mode,
     columnVisibility:window.__mock.overrides,sourceHidden:[{min:2,max:4},{min:6,max:15}]};
  }
  else if(name==='/api/index/start') data={ready:true,dimension:'A1:IV120000',maxRow:120000,maxCol:256,totalRows:120000};
  else if(name==='/api/search/history') data={history:[]};
  else if(name==='/api/search/start') data={jobId:'ui-search-job'};
  else if(name==='/api/search/status')data={status:'done',matches:[
    {sheet:'Dense Large Sheet',ref:'A2',row:2,col:1,value:'上海资产管理合计',formula:''},
    {sheet:'Dense Large Sheet',ref:'P2',row:2,col:16,value:'合计余额',formula:''},
    {sheet:'Dense Large Sheet',ref:'P20',row:20,col:16,value:'年度合计',formula:''}
   ],truncated:false,durationMs:25};
  else if(name==='/api/filter/distinct') data={values:[{value:'Value 1:1',count:1},{value:'Value 2:1',count:1},{value:'Value 3:1',count:1}],truncated:false};
  else if(name==='/api/sheet') {
   const startCol=Number(u.searchParams.get('col')||1),cols=Number(u.searchParams.get('cols')||24),limit=Number(u.searchParams.get('limit')||100);
   const selected=u.searchParams.get('columns')?.split(',').map(Number)||Array.from({length:cols},(_,i)=>startCol+i);
   const start=Number(u.searchParams.get('row')||0)||Number(u.searchParams.get('offset')||0)+1;
   const rows=[];
   for(let n=start;n<Math.min(120001,start+limit);n++){
    const cells=[];
    for(const c of selected){
     if(c%11===0 || (n===2 && c===5))continue;
     if(n===2&&c===1){cells.push({ref:'A2',col:1,raw:'上海资产管理合计报表中文字段非常长',display:'上海资产管理合计报表中文字段非常长',type:'inlineStr',style:0,hasFormula:false});continue;}
     if(n===2&&c===16){cells.push({ref:'P2',col:16,raw:'合计余额',display:'合计余额',type:'s',style:0,hasFormula:false});continue;}
     const formula=(c%7===0),txt=formula?('='+addr(Math.max(1,c-1))+n+'*1.18'):('Value '+n+':'+c);
     cells.push({ref:addr(c)+n,col:c,raw:formula?'100':'Value '+n+':'+c,display:formula?String(Math.round((n+c)*1.18)):'Value '+n+':'+c,formula:formula?txt:'',style:(n+c)%12,hasFormula:formula});
    }
    rows.push({number:n,cells,style:n%3===0?1:0});
   }
   data={sheet:'Dense Large Sheet',dimension:'A1:IV120000',offset:start-1,limit,totalRows:120000,
    totalRowsOriginal:120000,filtered:false,filterId:'',maxRow:120000,maxCol:256,startCol:selected[0],colCount:selected.length,columns:u.searchParams.get('columns')?selected:null,rows};
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

 console.log('loaded frontend polish mock');
 const errors=[];
 ws.addEventListener('message',evt=>{let d;try{d=JSON.parse(evt.data);}catch{return;}if(d.method==='Runtime.exceptionThrown')errors.push(d.params?.exceptionDetails?.exception?.description||d.params?.exceptionDetails?.text);});
 await waitFor('window.ExcelLensPerformance()?.hasVisibleCoverage');
 const initial=await evaluate(`(()=>({mode:document.querySelector('#searchMode').value,
  topStatusHidden:document.querySelector('#globalStatus').hidden,
  fileBadgeHidden:getComputedStyle(document.querySelector('#fileBadge')).display==='none',
  heads:[...document.querySelectorAll('.column-head[data-col]')].map(x=>+x.dataset.col),
  visibleCols:document.querySelectorAll('.column-head[data-col]').length,
  bottomScrollbar:getComputedStyle(document.querySelector('#sheetTabs')).scrollbarWidth,
  overflow:document.querySelector('.grid-cell[data-ref="A2"]')?.classList.contains('cell-overflow'),
  overflowWidth:document.querySelector('.grid-cell[data-ref="A2"]')?.style.getPropertyValue('--overflow-width'),
  screen:document.querySelector('#gridStatus').textContent}))()`);
 console.log('initial columns and overflow',initial);
 // Since 1.0.6 the active workbook name MUST remain visible in the toolbar.
 if(initial.mode!=='value'||!initial.topStatusHidden||initial.fileBadgeHidden)throw Error('default UI/status mismatch: '+JSON.stringify(initial));
 if(initial.heads.includes(2)||initial.heads.includes(3)||initial.heads.includes(4)||initial.heads.includes(6)||initial.heads.includes(15))throw Error('source hidden columns leaked: '+JSON.stringify(initial));
 if(!initial.heads.includes(1)||!initial.heads.includes(5)||!initial.heads.includes(16)||!initial.overflow)throw Error('visible columns / text overflow missing: '+JSON.stringify(initial));
 if(initial.bottomScrollbar!=='none')throw Error('sheet tabs scrollbar not hidden');
 await shot('excellens-58-sheet');
 await evaluate(`document.querySelector('#searchOpenBtn').click(); true`);
 const searchInitial=await evaluate(`(()=>{const button=document.querySelector('#searchBtn');const r=button.getBoundingClientRect();
 return {open:document.querySelector('#searchDialog').open,paneHidden:document.querySelector('#searchResultsPane').hidden,
 summaryHidden:document.querySelector('#searchSummary').hidden,
 mode:document.querySelector('#searchMode').value,button:{width:r.width,height:r.height,text:button.innerText},
 optionGaps:getComputedStyle(document.querySelector('.search-options')).gap,
 entryGaps:getComputedStyle(document.querySelector('.search-entry')).gap};})()`);
 console.log('search initial',searchInitial);
 if(!searchInitial.open||!searchInitial.paneHidden||!searchInitial.summaryHidden||searchInitial.mode!=='value'||searchInitial.button.width<65||searchInitial.button.text!=='查找')throw Error('Search search/button not compact: '+JSON.stringify(searchInitial));
 await shot('excellens-58-search-empty');
 await evaluate(`(()=>{const el=document.querySelector('#searchInput');el.value='合计';document.querySelector('#searchBtn').click();return true;})()`);
 await waitFor('!document.querySelector("#searchResultsPane").hidden',10000);
 const searchDone=await evaluate(`(()=>({paneHidden:document.querySelector('#searchResultsPane').hidden,results:document.querySelectorAll('#searchResults .result-item').length,
 visibleCount:document.querySelector('#searchVisibleCount').textContent,cancelHidden:document.querySelector('#cancelSearchBtn').hidden}))()`);
 console.log('search done',searchDone);
 if(searchDone.paneHidden||searchDone.results!==3)throw Error('Search results not revealed '+JSON.stringify(searchDone));
 await evaluate(`(()=>{const el=document.querySelector('#searchResultFilter');el.value='合计余额';el.dispatchEvent(new Event('input',{bubbles:true}));return true;})()`);
 const filtered=await evaluate('document.querySelectorAll("#searchResults .result-item").length');
 if(filtered!==1)throw Error('Search within results local filter failed '+filtered);
 await shot('excellens-58-search-results');
 await evaluate(`document.querySelector('#searchDialog').close();document.querySelector('#columnsBtn').click();true`);
 const menu=await evaluate(`(()=>({open:!document.querySelector('#columnsMenu').hidden,value:document.querySelector('#columnTarget').value,show:document.querySelector('#showSelectedColumnBtn').disabled}))()`);
 if(!menu.open)throw Error('column menu did not open '+JSON.stringify(menu));
 await evaluate(`(()=>{const input=document.querySelector('#columnTarget');input.value='C';input.dispatchEvent(new Event('input',{bubbles:true}));document.querySelector('#showSelectedColumnBtn').click();return true;})()`);
 await waitFor('Array.from(document.querySelectorAll(".column-head[data-col]")).some(x=>x.dataset.col==="3")',15000);
 const unhidden=await evaluate('({heads:[...document.querySelectorAll(".column-head[data-col]")].map(x=>+x.dataset.col),mode:window.__mock.mode,overrides:window.__mock.overrides})');
 console.log('manually unhidden C',unhidden);
 if(!unhidden.heads.includes(3)||unhidden.overrides['3']!==false)throw Error('manual unhide failed');
 await evaluate(`document.querySelector('#columnsBtn').click();document.querySelector('#hideAllColumnsBtn').click()`);
 await waitFor('window.__mock.mode === "hide-all"',12000);
 await evaluate(`document.querySelector('#columnsBtn').click();document.querySelector('#showAllColumnsBtn').click()`);
 await waitFor('window.__mock.mode === "show-all"',12000);
 await waitFor('Array.from(document.querySelectorAll(".column-head[data-col]")).some(x=>x.dataset.col==="2")',15000);
 const shown=await evaluate('({heads:document.querySelectorAll(".column-head[data-col]").length,coverage:window.ExcelLensPerformance().hasVisibleCoverage,rows:window.ExcelLensPerformance().currentRows})');
 console.log('show all',shown);
 if(shown.heads<3||!shown.coverage)throw Error('show all coverage missing');
 await evaluate(`document.querySelector('#columnsBtn').click();document.querySelector('#restoreColumnsBtn').click()`);
 await waitFor('window.__mock.mode === "original"',12000);
 await waitFor('window.ExcelLensPerformance().hasVisibleCoverage',15000);
 await evaluate("window.__mock.delay=240");
 await evaluate("document.querySelector('#grid').scrollTo({top:42000,left:3200})");
 await sleep(65);await shot('excellens-58-scroll-pending-65ms');
 await sleep(105);await shot('excellens-58-scroll-pending-170ms');
 for(let i=0;i<7;i++){await evaluate(`document.querySelector('#grid').scrollTo({top:${1800+i*1350},left:${i*120}})`);await sleep(55);}
 await waitFor('window.ExcelLensPerformance().hasVisibleCoverage && !window.ExcelLensPerformance().hasPendingRequest',18000);
 const scroll=await evaluate('({perf:window.ExcelLensPerformance(),loadingHidden:document.querySelector("#viewportLoading").hidden,canvasHidden:document.querySelector("#viewportFallback").hidden})');
 console.log('fast scroll stable',scroll);
 if(!scroll.perf.hasVisibleCoverage||!scroll.loadingHidden||!scroll.canvasHidden)throw Error('scroll did not settle');
 if(errors.length)throw Error('uncaught browser exceptions: '+errors.join('\n'));
 await shot('excellens-58-after-scroll');
 console.log('PASS frontend polish QA');
 ws.close();
})().catch(error=>{console.error(error.stack||error);process.exit(1);});
