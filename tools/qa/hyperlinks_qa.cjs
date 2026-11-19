// Developer-only Chrome DevTools Protocol scroll regression harness.
// Uses an about:blank page with mocked local APIs: browser administrator rules
// in some CI containers block loopback and file:// navigation.
// Node is NOT part of the Windows viewer's runtime dependencies.
const fs=require('fs');
const path=require('path');
const PORT=Number(process.env.CHROME_PORT||9229);
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
     const item={ref:addr(c)+n,col:c,raw:formula?'100':'Value '+n+':'+c,display:formula?String(Math.round((n+c)*1.18)):'Value '+n+':'+c,formula:formula?txt:'',style:(n+c)%12,hasFormula:formula};
     if(n===1&&c===1&&u.searchParams.get('sheet')==='Dense Large Sheet')item.hyperlink={kind:'internal',sheet:'Extra Sheet',ref:'B8'};
     cells.push(item);
    }
    rows.push({number:n,cells,style:n%3===0?1:0});
   }
   data={sheet:u.searchParams.get('sheet')||'Dense Large Sheet',dimension:'A1:IV120000',offset:start-1,limit,totalRows:120000,
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
 await evaluate(fs.readFileSync(ROOT+'cell-address.js','utf8'));
 await evaluate(fs.readFileSync(ROOT+'formula_inspector.js','utf8'));
 for(const asset of ['viewport-cache.js','command-palette.js','app.js']) await evaluate(fs.readFileSync(ROOT+asset,'utf8'));
 console.log('loaded scripts');
 await waitFor('document.querySelector("#globalStatus")?.textContent.includes("已打开")');
 const selector=process.env.QA_FROZEN ? '.pinned-cell[data-ref="A1"]' : '.grid-row .grid-cell[data-ref="A1"]';
 await waitFor(`Boolean(document.querySelector(${JSON.stringify(selector)})?.classList.contains('hyperlink-cell'))`);
 await evaluate(`document.querySelector(${JSON.stringify(selector)}).click();true`);
 await waitFor('document.querySelector("#gridStatus")?.textContent.includes("Extra Sheet")');
 await waitFor('document.querySelector("#nameBox")?.value === "B8"');
 console.log('PASS hyperlink UI navigation ('+(process.env.QA_FROZEN?'frozen':'grid')+'): Dense Large Sheet!A1 -> Extra Sheet!B8');
 ws.close();return;

 const measurements=[];
 for(let i=0;i<8;i++){
  await evaluate(`document.querySelector('#grid').scrollTo({top:${(i+1)*420},left:${(i+1)*320}})`);
  await sleep(250);
  measurements.push(await evaluate('window.ExcelLensPerformance()'));
 }
 console.log('samples',measurements.map(m=>({ms:m.lastMs,reused:m.reusedRows,created:m.createdCells,nodes:m.visibleNodes,valid:m.hasVisibleCoverage})));
 console.log('summary',{avg:measurements.reduce((s,m)=>s+m.lastMs,0)/measurements.length,max:Math.max(...measurements.map(m=>m.lastMs))});
 ws.close();
})().catch(err=>{console.error(err);process.exitCode=1;});
