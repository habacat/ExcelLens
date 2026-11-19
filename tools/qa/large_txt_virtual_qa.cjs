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
  if(name==='/api/status') data={opened:true,fileName:'huge-oracle-ledger.txt',fileSize:330337593,sheetCount:2,source:{format:'txt',readOnly:true},activeTab:0,sheets:[{name:'Dense Large Sheet',path:'sheet1.xml',state:'visible'},{name:'Extra Sheet',path:'sheet2.xml',state:'visible'}]};
  else if(name==='/api/styles') data={styles};
  else if(name==='/api/bookmarks') data={version:1,items:[]};
  else if(name==='/api/view') data={rows:2,cols:1};
  else if(name==='/api/layout') data={rows:{},cols:{}};
  else if(name==='/api/index/start') data={ready:true,dimension:'A1:IV2000000',maxRow:2000000,maxCol:256,totalRows:2000000};
  else if(name==='/api/search/history') data={history:[]};
  else if(name==='/api/filter/distinct') data={values:[{value:'Value 1:1',count:1},{value:'Value 2:1',count:1},{value:'Value 3:1',count:1}],truncated:false};
  else if(name==='/api/sheet') {
   const startCol=Number(u.searchParams.get('col')||1),cols=Number(u.searchParams.get('cols')||24),limit=Number(u.searchParams.get('limit')||100);
   const start=Number(u.searchParams.get('row')||0)||Number(u.searchParams.get('offset')||0)+1;
   const rows=[];
   for(let n=start;n<Math.min(2000001,start+limit);n++){
    const cells=[];
    for(let c=startCol;c<startCol+cols;c++){
     if(c%11===0)continue;
     const formula=(c%7===0),txt=formula?('='+addr(Math.max(1,c-1))+n+'*1.18'):('Value '+n+':'+c);
     cells.push({ref:addr(c)+n,col:c,raw:formula?'100':'Value '+n+':'+c,display:formula?String(Math.round((n+c)*1.18)):'Value '+n+':'+c,formula:formula?txt:'',style:(n+c)%12,hasFormula:formula});
    }
    rows.push({number:n,cells,style:n%3===0?1:0});
   }
   data={sheet:'Dense Large Sheet',dimension:'A1:IV2000000',offset:start-1,limit,totalRows:2000000,
    totalRowsOriginal:2000000,filtered:false,filterId:'',maxRow:2000000,maxCol:256,startCol,colCount:cols,rows};
  }
  return new Response(JSON.stringify(data),{status:200,headers:{'Content-Type':'application/json'}});
 };
})()`;
 await evaluate(mockScript);
 for(const module of ['session.js','grid-borders.js','recent.js','multisort.js','inspect.js','multiselection.js','selectionstats.js','images.js'])await evaluate(fs.readFileSync(ROOT+module,'utf8'));
 await evaluate(fs.readFileSync(ROOT+'clipboard.js','utf8'));
 await evaluate(fs.readFileSync(ROOT+'geometry.js','utf8'));
 await evaluate(fs.readFileSync(ROOT+'freeze.js','utf8'));
 await evaluate(fs.readFileSync(ROOT+'autofilter.js','utf8'));
 await evaluate(fs.readFileSync(ROOT+'export.js','utf8'));
 await evaluate(fs.readFileSync(ROOT+'bookmarks.js','utf8'));
 await evaluate(fs.readFileSync(ROOT+'profile.js','utf8'));
 await evaluate(fs.readFileSync(ROOT+'cell-address.js','utf8'));
 await evaluate(fs.readFileSync(ROOT+'formula_inspector.js','utf8'));
 let appSource=fs.readFileSync(ROOT+'app.js','utf8');
 appSource=appSource.replace('  function selectCell(cell, rowNumber, colNumber, ref, visualIndex, preserveTrace = false) {','  function selectCell(cell, rowNumber, colNumber, ref, visualIndex, preserveTrace = false) { window.__selcalls=(window.__selcalls||[]);window.__selcalls.push({row:rowNumber,ref:ref,source:(new Error()).stack?.slice(0,260)});');
 appSource=appSource.replace('  async function navigateToCell(sheet, ref, preserveTrace = false) {','  async function navigateToCell(sheet, ref, preserveTrace = false) {window.__navcalls=(window.__navcalls||[]);window.__navcalls.push({sheet:sheet,ref:ref});');
 await evaluate(appSource);
 console.log('loaded scripts');
 await sleep(1500); console.log('DIAG',await evaluate('({status:document.querySelector("#globalStatus")?.textContent,detail:document.querySelector("#indexDetail")?.textContent,tt:document.querySelector("#indexTitle")?.textContent,toast:document.querySelector("#toast")?.textContent,fetch:window.__mock?.calls,main:document.querySelector("#workspace")?.hidden})')); await waitFor('document.querySelector("#globalStatus")?.textContent.includes("已打开")');

 const grid='#grid';
 const initial=await evaluate(`(()=>{const g=document.querySelector('#grid'),c=document.querySelector('#gridCanvas');return {height:Number.parseFloat(c.style.height),scrollHeight:g.scrollHeight,clientHeight:g.clientHeight,source:document.querySelector('#fileBadge').title}})()`);
 console.log('large-source-initial',initial);
 if(!initial.source.includes('TXT') || initial.height>15000001 || initial.height<14000000)throw Error('virtual canvas height incorrect: '+JSON.stringify(initial));
 await evaluate('(()=>{const g=document.querySelector("#grid");g.scrollTop=(g.scrollHeight-g.clientHeight)*0.90;return true;})()');
 await waitFor(`(()=>{const rs=[...document.querySelectorAll('.grid-row')].map(r=>Number(r.dataset.row));return rs.length&&Math.max(...rs)>1500000&&window.ExcelLensPerformance().hasVisibleCoverage;})()`,20000);
 const nearEnd=await evaluate(`(()=>{const g=document.querySelector('#grid'),rs=[...document.querySelectorAll('.grid-row')];const closest=rs.find(r=>{const b=r.getBoundingClientRect(),p=g.getBoundingClientRect();return b.top>=p.top+25&&b.top<p.bottom-25;});return {gridTop:g.scrollTop,canvasHeight:parseFloat(document.querySelector('#gridCanvas').style.height),first:Math.min(...rs.map(r=>Number(r.dataset.row))),last:Math.max(...rs.map(r=>Number(r.dataset.row))),visible:closest?{row:closest.dataset.row,top:closest.getBoundingClientRect().top}:null,viewport:window.ExcelLensPerformance()}})()`);
 console.log('large-source-near-end',nearEnd);
 if(nearEnd.first<1500000 || !nearEnd.visible || nearEnd.visible.row<1500000)throw Error('rows not painted at true large row coordinate');
 await evaluate(`(()=>{const el=document.querySelector('#nameBox');el.focus();el.value='A1900123';el.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true}));return true})()`);
 await sleep(1500); console.log('NAVDIAG',await evaluate('({name:document.querySelector("#nameBox")?.value, status:document.querySelector("#globalStatus")?.textContent,toast:document.querySelector("#toast")?.textContent,index:document.querySelector("#indexDetail")?.textContent,perf:window.ExcelLensPerformance(),selections:window.__selcalls?.slice(-8),navigations:window.__navcalls?.slice(-8)})')); await waitFor(`document.querySelector('#nameBox').value==='A1900123'&&window.ExcelLensPerformance().hasVisibleCoverage`,6000);
 const goTo=await evaluate('({address:document.querySelector("#nameBox").value,selected:document.querySelector("#gridStatus").textContent,scroll:document.querySelector("#grid").scrollTop})');
 console.log('large-source-navigate',goTo);
 ws.close();process.exit(0);
})().catch(e=>{console.error(e.stack||e);process.exit(1);});
