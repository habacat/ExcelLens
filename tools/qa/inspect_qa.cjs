// Regression test for ExcelLens 5.5 in Chromium CDP with mocked local API.
// Runs as development QA; no Node dependency is included in Windows executable.
const fs=require('fs');const path=require('path');
const root=path.join(__dirname,'../../internal/ui/web');
const port=process.env.CHROME_PORT||9468;
const wait=ms=>new Promise(r=>setTimeout(r,ms));
(async()=>{
 const pages=await (await fetch(`http://127.0.0.1:${port}/json`)).json();
 const page=pages.find(x=>x.type==='page');if(!page)throw Error('missing CDP page');
 const ws=new WebSocket(page.webSocketDebuggerUrl);await new Promise(resolve=>ws.addEventListener('open',resolve,{once:true}));
 const pending=new Map();let seq=1;const errors=[];
 ws.addEventListener('message',e=>{const m=JSON.parse(e.data);if(m.method==='Runtime.exceptionThrown')errors.push(m.params.exceptionDetails.text);const task=pending.get(m.id);if(task){pending.delete(m.id);m.error?task.reject(new Error(JSON.stringify(m.error))):task.resolve(m.result)}});
 const cdp=(method,params={})=>new Promise((resolve,reject)=>{const id=seq++;pending.set(id,{resolve,reject});ws.send(JSON.stringify({id,method,params}));setTimeout(()=>{if(pending.has(id)){pending.delete(id);reject(Error(method+' timed out'))}},15000)});
 const ev=async expr=>{let r=await cdp('Runtime.evaluate',{expression:expr,returnByValue:true,awaitPromise:true});if(r.exceptionDetails)throw Error(JSON.stringify(r.exceptionDetails));return r.result.value};
 const eventually=async expr=>{for(let n=0;n<80;n++){if(await ev(expr))return;await wait(90)}throw Error('condition failed '+expr+' '+JSON.stringify(errors))};
 await cdp('Page.enable');await cdp('Runtime.enable');await cdp('Emulation.setDeviceMetricsOverride',{width:1500,height:890,deviceScaleFactor:1,mobile:false});
 await cdp('Page.navigate',{url:'about:blank'});
 const icon='data:image/svg+xml;charset=utf-8,'+encodeURIComponent(fs.readFileSync(path.join(root,'icon.svg'),'utf8'));
 const html=require('./project_config.cjs').index().replaceAll('/assets/icon.svg',icon)
 .replace('<link rel="stylesheet" href="/assets/style.css">','<style>'+fs.readFileSync(path.join(root,'style.css'),'utf8')+'</style>')
 .replace(/<script src="\/assets\/[\w]+\.js"><\/script>/g,'');
 await ev('document.open();document.write('+JSON.stringify(html)+');document.close();true');
 const mock=`(()=>{
 const colName=n=>{let s='';while(n){n--;s=String.fromCharCode(65+n%26)+s;n=Math.floor(n/26)}return s};
 window.__inspectTest={requests:[],sheets:0};
 window.fetch=async(url,options={})=>{
 const u=new URL(url,'http://example.org');const route=u.pathname;
 const sleep=n=>new Promise(r=>setTimeout(r,n));await sleep(3);
 let data={},status=200;
 if(route==='/api/status')data={opened:true,fileName:'inspection_test.xlsx',fileSize:1000,sheetCount:1,activeTab:0,sheets:[{name:'Data',state:'visible'}]};
 else if(route==='/api/styles')data={styles:[{}]};
 else if(route==='/api/index/start')data={ready:true,maxRow:30,maxCol:7,usedDimension:'A1:G30'};
 else if(route==='/api/layout')data={rows:{},cols:{}};
 else if(route==='/api/view')data={rows:1,cols:1};
 else if(route==='/api/bookmarks')data={items:[]};
 else if(route==='/api/search/history')data={history:[]};
 else if(route==='/api/inspect'){
   const r=JSON.parse(options.body);window.__inspectTest.requests.push(r);
   if(r.mode==='duplicates')data={mode:r.mode,matched:2,duplicateGroups:1,durationMs:7,truncated:false,matches:[{ref:'A2',row:2,col:1,value:'abc'},{ref:'A3',row:3,col:1,value:'ABC'}]};
   else data={mode:r.mode,matched:2,durationMs:12,truncated:false,matches:[{ref:'B2',row:2,col:2,formula:'=A2*2',value:'6'},{ref:'B3',row:3,col:2,formula:'=A3*2',value:'8'}]};
 }
 else if(route==='/api/sheet'){
   window.__inspectTest.sheets++;
   const first=Number(u.searchParams.get('col')||1),ncols=Number(u.searchParams.get('cols')||10),start=Number(u.searchParams.get('row')||1),count=Number(u.searchParams.get('limit')||50);
   let rows=[];
   for(let r=start;r<Math.min(31,start+count);r++){
      let cells=[];
      for(let c=first;c<Math.min(8,first+ncols);c++){
       const ref=colName(c)+r;
       if(c===1){cells.push({ref,col:c,raw:r===2?'abc':r===3?'ABC':'text'+r,display:r===2?'abc':r===3?'ABC':'text'+r,hasFormula:false});}
       else if(c===2){cells.push({ref,col:c,raw:r===2?'6':'8',display:r===2?'6':'8',formula:'=A'+r+'*2',hasFormula:true});}
       else cells.push({ref,col:c,raw:String(c*r),display:String(c*r),hasFormula:false});
      }rows.push({number:r,cells});
   }
   data={sheet:'Data',offset:start-1,totalRows:30,maxRow:30,maxCol:7,startCol:first,colCount:ncols,rows};
 }
 else if(route==='/api/cell')data={cell:{ref:u.searchParams.get('ref'),col:2,display:'6',raw:'6',formula:'=A2*2',hasFormula:true}};
 else if(route==='/api/profile')data={nonEmpty:1,number:1,formulas:0,sum:'6',average:'6'};
 return new Response(JSON.stringify(data),{status,headers:{'Content-Type':'application/json'}});
 };
})();`;
 await ev(mock);
 for(const f of ['recent.js','multisort.js','selectionstats.js','inspect.js','clipboard.js','geometry.js','freeze.js','autofilter.js','export.js','bookmarks.js','profile.js','formula_inspector.js','app.js'])await ev(fs.readFileSync(path.join(root,f),'utf8'));
 await eventually('document.querySelector("#indexOverlay").hidden && document.querySelector("#workspace").hidden === false && document.querySelector(".grid-cell[data-ref=B2]")');
 await wait(250);
 const base=await ev('({cell:document.querySelector(".grid-cell[data-ref=B2]").textContent,sheets:window.__inspectTest.sheets})');
 await ev('document.querySelector("#formulaViewBtn").click();true');
 const after=await ev('({cell:document.querySelector(".grid-cell[data-ref=B2]").textContent,pressed:document.querySelector("#formulaViewBtn").getAttribute("aria-pressed"),sheets:window.__inspectTest.sheets})');
 if(base.cell===after.cell || after.cell!=='=A2*2' || after.pressed!=='true'||base.sheets!==after.sheets)throw Error('formula-view toggle failed '+JSON.stringify({base,after}));
 await ev('document.querySelector("#formulaViewBtn").click();true');
 if(await ev('document.querySelector(".grid-cell[data-ref=B2]").textContent')!==base.cell)throw Error('formula view did not restore cached value');
 await ev('document.querySelector("#duplicateOpenBtn").click();document.querySelector("#inspectRun").click();true');
 await eventually('document.querySelectorAll(".inspect-hit").length===2');
 const hi=await ev('({matches:document.querySelectorAll(".inspect-hit").length,highlight:document.querySelector(".grid-cell[data-ref=A2]").classList.contains("inspect-highlight"),request:window.__inspectTest.requests[0]})');
 if(!hi.highlight || hi.request.mode!=='duplicates' || hi.request.start!=='A2')throw Error('duplicate inspection failed '+JSON.stringify(hi));
 const shot=await cdp('Page.captureScreenshot',{format:'png'});
 fs.writeFileSync('/mnt/data/ExcelLens_5.5_inspector_preview.png',Buffer.from(shot.data,'base64'));
 await ev('document.querySelectorAll(".inspect-hit")[1].click();true');
 await eventually('document.querySelector("#nameBox").value==="A3"');
 if(await ev('document.querySelector("#inspectPanel").hidden'))throw Error('inspector closed after navigation');
 await ev('document.querySelector("#inspectMode").value="formulas";document.querySelector("#inspectMode").dispatchEvent(new Event("change"));document.querySelector("#inspectRun").click();true');
 await eventually('window.__inspectTest.requests.length===2 && document.querySelectorAll(".inspect-hit").length===2');
 await ev('document.querySelector("#inspectClear").click();true');
 if(await ev('document.querySelectorAll(".inspect-highlight").length'))throw Error('inspection markers persisted after clear');
 if(errors.length)throw Error('uncaught exceptions: '+JSON.stringify(errors));
 console.log('PASS',JSON.stringify({base,after,hi,exceptions:errors.length}));
 ws.close();
})().catch(e=>{console.error(e);process.exit(1)});
