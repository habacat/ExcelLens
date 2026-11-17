/* Offline analysis-panel smoke test: renders the exact shipped HTML/CSS/JS
 * into about:blank, without a mock web server or network permissions. */
const fs=require('node:fs'),path=require('node:path');
const root=path.resolve(__dirname,'../../internal/ui/web');
const port=process.env.CHROME_PORT||9449;
const sleep=ms=>new Promise(r=>setTimeout(r,ms));
(async()=>{
 const pages=await(await fetch('http://127.0.0.1:'+port+'/json')).json();
 const target=pages.find(p=>p.type==='page');
 const ws=new WebSocket(target.webSocketDebuggerUrl);await new Promise(r=>ws.addEventListener('open',r,{once:true}));
 let serial=1;const pending=new Map();
 ws.addEventListener('message',event=>{const msg=JSON.parse(event.data);if(!msg.id)return;const p=pending.get(msg.id);if(!p)return;pending.delete(msg.id);msg.error?p.reject(Error(msg.error.message)):p.resolve(msg.result)});
 const cdp=(method,params={})=>new Promise((resolve,reject)=>{const id=serial++;pending.set(id,{resolve,reject});ws.send(JSON.stringify({id,method,params}));setTimeout(()=>{if(pending.has(id)){pending.delete(id);reject(Error('CDP timeout '+method))}},15000)});
 const evalJS=async code=>{const r=await cdp('Runtime.evaluate',{expression:code,returnByValue:true,awaitPromise:true});if(r.exceptionDetails)throw Error(JSON.stringify(r.exceptionDetails));return r.result.value};
 const wait=async cond=>{for(let i=0;i<90;i++){if(await evalJS(cond))return;await sleep(160)}throw Error('Wait failed: '+cond+' '+await evalJS('document.querySelector("#analysisToast").textContent'))};
 await cdp('Page.enable');await cdp('Runtime.enable');await cdp('Emulation.setDeviceMetricsOverride',{width:1440,height:900,deviceScaleFactor:1,mobile:false});
 await cdp('Page.navigate',{url:'about:blank'});
 const html=require('./project_config.cjs').index()
  .replace('<link rel="stylesheet" href="/assets/style.css">','<style>'+fs.readFileSync(path.join(root,'style.css'),'utf8')+'</style>')
  .replace('<link rel="stylesheet" href="/assets/analytics.css">','<style>'+fs.readFileSync(path.join(root,'analytics.css'),'utf8')+'</style>')
  .replace(/<script src="\/assets\/[\w-]+\.js"><\/script>/g,'');
 await evalJS('document.open();document.write('+JSON.stringify(html)+');document.close();true');
 const mock=`(()=>{
  let sheets=[];
  window.fetch=async(url,options={})=>{
   const p=new URL(url,'http://dummy.local').pathname;
   let data={};
   if(p==='/api/status')data={opened:true,fileName:'费用台账.xlsx',sheets:[{name:'Sheet1'}],activeTab:0};
   if(p==='/api/analysis/workspace')data={sheets,reports:[],dashboardOrder:[],sourceSheets:[{name:'Sheet1'}],workbook:'费用台账.xlsx',fingerprint:'qa-133'};
   if(p==='/api/analysis/fields')data={fields:[{id:'C1',name:'金额',numeric:true}],totalRows:2,preview:[]};
   if(p==='/api/analysis/sheet/create'){let payload=JSON.parse(options.body);let id='sheet-test';sheets.push({id,name:payload.name,columns:['字段1','字段2','字段3','字段4','字段5'],rows:0,revision:0});data={id};}
   if(p==='/api/analysis/sheet')data={id:'sheet-test',name:'数据分析表 1',columns:['字段1','字段2','字段3','字段4','字段5'],rows:[],offset:0,total:0,revision:0};
   return new Response(JSON.stringify(data),{status:200,headers:{'Content-Type':'application/json'}});
  };
 })();`;
 await evalJS(mock);
 for(const script of ['analytics-templates.js','analytics-charts.js','analytics.js'])await evalJS(fs.readFileSync(path.join(root,script),'utf8'));
 await evalJS('document.querySelector("#analysisOpenBtn").click();true');
 await wait('!document.querySelector("#analysisRoot").hidden');
 await evalJS('document.querySelector("[data-analysis-tab=sheets]").click();true');
 await wait('!!document.querySelector(".analysis-sheet-empty")');
 let data=await evalJS('({message:document.querySelector(".analysis-sheet-empty h3").textContent,actions:document.querySelectorAll(".analysis-sheet-empty button").length,overflow:document.documentElement.scrollWidth-innerWidth})');
 if(data.actions!==2||data.overflow>2)throw Error('Empty UI invalid: '+JSON.stringify(data));
 let image=await cdp('Page.captureScreenshot',{format:'png'});
 fs.writeFileSync('/mnt/data/ExcelLens_qa_133/analysis_empty.png',Buffer.from(image.data,'base64'));
 await evalJS('document.querySelector(".analysis-sheet-empty .analysis-primary-button").click();true');
 await wait('!!document.querySelector("[data-yes]")');
 await evalJS('document.querySelector("[data-yes]").click();true');
 await wait('!!document.querySelector("#analysisEditableGrid table")');
 data=await evalJS('({title:document.querySelector("#analysisSheetTitle").textContent,headers:document.querySelectorAll("#analysisEditableGrid th").length,rows:document.querySelectorAll("#analysisEditableGrid tbody tr").length})');
 if(data.headers!==6||data.rows<20)throw Error('Edit table render invalid: '+JSON.stringify(data));
 image=await cdp('Page.captureScreenshot',{format:'png'});
 fs.writeFileSync('/mnt/data/ExcelLens_qa_133/analysis_edit.png',Buffer.from(image.data,'base64'));
 console.log('ANALYSIS_EMPTY',data,'PASS');ws.close();
})().catch(e=>{console.error(e);process.exit(1)});
