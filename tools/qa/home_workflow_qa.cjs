// Native Chromium CDP smoke test. No runtime/browser automation dependency in EXE.
const fs=require('fs');const path=require('path');
const root=path.join(__dirname,'../../internal/ui/web/');
const port=process.env.CHROME_PORT||9449;
const sleep=(n)=>new Promise(ok=>setTimeout(ok,n));
(async()=>{
 const pages=await(await fetch(`http://127.0.0.1:${port}/json`)).json();
 const page=pages.find(x=>x.type==='page');if(!page)throw Error('CDP page missing');
 const ws=new WebSocket(page.webSocketDebuggerUrl);await new Promise(ok=>ws.addEventListener('open',ok,{once:true}));
 let serial=1;const tasks=new Map();
 ws.addEventListener('message',e=>{let v=JSON.parse(e.data);const job=tasks.get(v.id);if(!job)return;tasks.delete(v.id);v.error?job.reject(Error(JSON.stringify(v.error))):job.resolve(v.result);});
 function cdp(method,params={}){const id=serial++;return new Promise((resolve,reject)=>{tasks.set(id,{resolve,reject});ws.send(JSON.stringify({id,method,params}));setTimeout(()=>{if(tasks.has(id)){tasks.delete(id);reject(Error(method+' timeout'));}},15000);});}
 async function evalJS(code){const r=await cdp('Runtime.evaluate',{expression:code,returnByValue:true,awaitPromise:true});if(r.exceptionDetails)throw Error('Page error '+JSON.stringify(r.exceptionDetails));return r.result.value;}
 async function screenshot(name){const data=await cdp('Page.captureScreenshot',{format:'png'});fs.writeFileSync('/mnt/data/'+name,Buffer.from(data.data,'base64'));}
 async function waitFor(expression){for(let i=0;i<65;i++){if(await evalJS(expression))return;await sleep(130)}throw Error('Wait failed '+expression+' status '+await evalJS('document.querySelector("#globalStatus").textContent'));}
 await cdp('Page.enable');await cdp('Runtime.enable');
 await cdp('Emulation.setDeviceMetricsOverride',{width:Number(process.env.QA_WIDTH||1500),height:Number(process.env.QA_HEIGHT||890),deviceScaleFactor:1,mobile:false});
 await cdp('Page.navigate',{url:'about:blank'});
 const icon='data:image/svg+xml;charset=utf-8,'+encodeURIComponent(fs.readFileSync(path.join(root,'icon.svg'),'utf8'));
 const html=require('./project_config.cjs').index()
  .replaceAll('/assets/icon.svg',icon)
  .replace('<link rel="stylesheet" href="/assets/style.css">','<style>'+fs.readFileSync(path.join(root,'style.css'),'utf8')+'</style>')
  .replace(/<script src="\/assets\/[\w-]+\.js"><\/script>/g,'');
 await evalJS('document.open();document.write('+JSON.stringify(html)+');document.close();true');
 const mock=`(()=>{
 const sleep=ms=>new Promise(r=>setTimeout(r,ms));
 let recent=[{id:'one',name:'投资估值核查2026.xlsm',path:'C:\\\\Work\\\\审计\\\\投资估值核查2026.xlsm',fileSize:134217728,sheetCount:79,openedAt:new Date().toISOString(),pinned:true,available:true},
 {id:'two',name:'2026年各机构审计底稿.xlsx',path:'D:\\\\Audit\\\\2026年各机构审计底稿.xlsx',fileSize:8322896,sheetCount:16,openedAt:new Date(Date.now()-86400000).toISOString(),pinned:false,available:true},
 {id:'three',name:'已移动的旧台账.xlsm',path:'D:\\\\Old\\\\已移动的旧台账.xlsm',fileSize:9042,sheetCount:3,openedAt:new Date(Date.now()-172800000).toISOString(),pinned:false,available:false}];
 let opened=false;
 const colName=n=>{let out='';while(n){n--;out=String.fromCharCode(65+n%26)+out;n=Math.floor(n/26)}return out;};
 window.__test={get opened(){return opened},recent,get calls(){return this.count||0},lastFilter:null};
 window.fetch=async(url,options={})=>{
  const u=new URL(url,'http://dummy.local');const route=u.pathname;window.__test.count=(window.__test.count||0)+1;
  await sleep(route==='/api/sheet'?9:2);
  if(options.signal?.aborted)throw new DOMException('aborted','AbortError');
  let data={},code=200;
  const book={opened:true,fileName:'投资估值核查2026.xlsm',fileSize:134217728,sheetCount:2,activeTab:0,sheets:[{name:'投资估值表',state:'visible'},{name:'收益穿透',state:'visible'}]};
  if(route==='/api/status')data=opened?book:{opened:false};
  else if(route==='/api/recent'&&(!options.method||options.method==='GET'))data={items:recent};
  else if(route==='/api/recent'){
   const body=JSON.parse(options.body||'{}');if(body.action==='pin'){recent.find(x=>x.id===body.id).pinned=true;data={pinned:true}}
   else if(body.action==='remove'){recent=recent.filter(x=>x.id!==body.id);data={removed:true}}
   else {opened=true;data=book;}
  }
  else if(route==='/api/styles')data={styles:[{}]};
  else if(route==='/api/bookmarks')data={items:[]};
  else if(route==='/api/search/history')data={history:[]};
  else if(route==='/api/index/start')data={ready:true,maxRow:1200,maxCol:40,totalRows:1200};
  else if(route==='/api/layout')data={rows:{},cols:{}};
  else if(route==='/api/view')data={rows:1,cols:0};
  else if(route==='/api/filter/start'){window.__test.lastFilter=JSON.parse(options.body);data={jobId:'filter123'};}
  else if(route==='/api/filter/status')data={status:'done',progress:100,scanned:1200,total:1200,filterId:'filter123',matchedRows:1200};
  else if(route==='/api/filter/clear')data={cleared:true};
  else if(route==='/api/navigate')data={ref:'C7',col:3,row:7};
  else if(route==='/api/profile')data={nonEmpty:4,number:3,formulas:1,sum:'120.20',average:'40.066666',range:'A1:B2'};
  else if(route==='/api/selection/stats')data={total:2,nonEmpty:2,number:2,sum:'120.20',average:'60.10',ranges:1};
  else if(route==='/api/sheet'){
    const first=Number(u.searchParams.get('col')||1),ncols=Number(u.searchParams.get('cols')||10),nrows=Number(u.searchParams.get('limit')||50),start=Number(u.searchParams.get('row')||1);
    const rows=[];for(let r=start;r<Math.min(1201,start+nrows);r++){
      const cells=[];for(let c=first;c<first+ncols;c++){
        if(c===1){cells.push({ref:colName(c)+r,col:c,raw:'项目 '+r,display:'项目 '+r,hasFormula:false});continue}
        cells.push({ref:colName(c)+r,col:c,raw:String(r*c),display:String(r*c),hasFormula:false});
      }rows.push({number:r,cells});
    }
    data={sheet:'投资估值表',offset:start-1,totalRows:1200,maxRow:1200,maxCol:40,startCol:first,colCount:ncols,rows};
  }
  return new Response(JSON.stringify(data),{status:code,headers:{'Content-Type':'application/json'}});
 };
})();`;
 await evalJS(mock);
 for(const f of ['session.js','grid-borders.js','recent.js','multiselection.js','multisort.js','selectionstats.js','inspect.js','clipboard.js','geometry.js','freeze.js','autofilter.js','export.js','bookmarks.js','profile.js','formula_inspector.js','app.js']){
   await evalJS(fs.readFileSync(path.join(root,f),'utf8'));
 }
 await waitFor('document.querySelectorAll(".recent-item").length===3');
 await screenshot('ExcelLens_5.4_home_preview.png');
 const home=await evalJS('({items:document.querySelectorAll(".recent-item").length,main:document.querySelector("#welcome").offsetWidth,recent:document.querySelector("#recentList").clientHeight,author:document.querySelector(".landing-credits").textContent})');
 console.log('HOME',home);
 if(await evalJS('document.documentElement.scrollWidth>innerWidth+2'))throw Error('Homepage horizontally overflows');
 await evalJS('document.querySelector(".recent-item").click();true');
 await waitFor('document.querySelector("#workspace").hidden===false && document.querySelector("#globalStatus").textContent.includes("已打开")');
 await screenshot('ExcelLens_5.4_sheet_preview.png');
 await evalJS('document.getElementById("grid").focus();document.getElementById("grid").dispatchEvent(new KeyboardEvent("keydown",{key:"ArrowRight",ctrlKey:true,bubbles:true}));true');
 await waitFor('document.querySelector("#nameBox").value==="C7"');
 console.log('NAVIGATION',await evalJS('document.querySelector("#nameBox").value'));
 await evalJS('document.getElementById("grid").dispatchEvent(new KeyboardEvent("keydown",{key:"ArrowRight",shiftKey:true,bubbles:true}));true');
 await waitFor('document.getElementById("quickStats").textContent.includes("120.20")');
 console.log('SELECTION_STATS',await evalJS('document.getElementById("quickStats").textContent'));
 await evalJS('document.querySelector("#multiSortBtn").click();true');
 await waitFor('document.querySelector("#multiSortDialog").open');
 await evalJS('document.querySelector("#sortAddLevel").click();document.querySelectorAll(".sort-column")[0].value="A";document.querySelectorAll(".sort-column")[1].value="B";document.querySelector("#sortApply").click();true');
 await waitFor('window.__test.lastFilter?.sortKeys?.length===2');
 await waitFor('document.querySelector("#filterStatus").hidden===false');
 console.log('MULTISORT',await evalJS('window.__test.lastFilter'));
 await screenshot('ExcelLens_5.4_sort_preview.png');
 console.log('PASS');
 ws.close();
})().catch(err=>{console.error(err);process.exit(1)});
