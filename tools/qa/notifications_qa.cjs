// Real-page notification colors and workflow classification in an isolated
// headless browser. Clipboard outcomes are simulated; no system clipboard writes.
const fs=require('node:fs'),path=require('node:path'),{spawn}=require('node:child_process'),assert=require('node:assert/strict');
const address=process.argv[2];if(!address)throw Error('Usage: node notifications_qa.cjs SERVER_URL');
const root=path.resolve(__dirname,'../..'),version=require('./project_config.cjs').version();
const output=path.join(root,'validation',version,'private','notifications');fs.mkdirSync(output,{recursive:true});
const profile=fs.mkdtempSync(path.join(output,'chrome-')),file=path.join(output,'notifications.txt');
fs.writeFileSync(file,'账号@金额\n00001@1.25\n00002@2.50\n');
const child=spawn(process.env.QA_CHROME||'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe',['--headless=new','--window-size=1500,900','--no-first-run','--no-default-browser-check','--disable-extensions','--disable-background-networking','--remote-debugging-port=0','--user-data-dir='+profile,'about:blank'],{windowsHide:true,stdio:'ignore'});
const pause=ms=>new Promise(resolve=>setTimeout(resolve,ms));let socket,sequence=0;const pending=new Map(),errors=[],cases=[];
function cdp(method,params={}){return new Promise((resolve,reject)=>{const id=++sequence,timer=setTimeout(()=>{pending.delete(id);reject(Error(method+' timeout'));},15000);pending.set(id,{resolve,reject,timer});socket.send(JSON.stringify({id,method,params}));});}
async function evaluate(expression){const response=await cdp('Runtime.evaluate',{expression,awaitPromise:true,returnByValue:true});if(response.exceptionDetails)throw Error(JSON.stringify(response.exceptionDetails));return response.result.value;}
async function waitFor(expression){for(let n=0;n<200;n++){const value=await evaluate(expression);if(value)return value;await pause(80);}throw Error('Timeout: '+expression);}
async function check(name,tone,id='toast'){
  await waitFor(`(()=>{const n=document.querySelector('#${id}');return !n.hidden&&n.dataset.tone===${JSON.stringify(tone)};})()`);
  const result=await evaluate(`(()=>{const n=document.querySelector('#${id}'),s=getComputedStyle(n),r=n.getBoundingClientRect();return {text:n.textContent,tone:n.dataset.tone,role:n.getAttribute('role'),color:s.color,background:s.backgroundColor,border:s.borderTopColor,clip:{x:r.x-3,y:r.y-3,width:r.width+6,height:r.height+6,scale:1}};})()`);
  const colors={info:'rgb(56, 83, 107)',success:'rgb(35, 99, 71)',warning:'rgb(130, 96, 29)',error:'rgb(163, 59, 59)'};
  assert.equal(result.color,colors[tone]);assert.equal(result.role,tone==='error'?'alert':'status');
  const shot=await cdp('Page.captureScreenshot',{format:'png',captureBeyondViewport:false,clip:result.clip});fs.writeFileSync(path.join(output,name+'.png'),Buffer.from(shot.data,'base64'));cases.push({name,...result});
}
(async()=>{try{
  let port;for(let i=0;i<150;i++){try{port=Number(fs.readFileSync(path.join(profile,'DevToolsActivePort'),'utf8').split('\n')[0]);break;}catch{}await pause(100);}assert(port,'Chrome did not start');
  const pages=await(await fetch('http://127.0.0.1:'+port+'/json')).json();socket=new WebSocket(pages.find(p=>p.type==='page').webSocketDebuggerUrl);
  await new Promise((resolve,reject)=>{socket.addEventListener('open',resolve,{once:true});socket.addEventListener('error',reject,{once:true});});
  socket.addEventListener('message',event=>{const response=JSON.parse(event.data);if(response.method==='Runtime.exceptionThrown')errors.push(response.params.exceptionDetails.exception?.description||response.params.exceptionDetails.text);const task=pending.get(response.id);if(task){pending.delete(response.id);clearTimeout(task.timer);response.error?task.reject(Error(response.error.message)):task.resolve(response.result);}});
  await cdp('Page.enable');await cdp('Runtime.enable');await cdp('Emulation.setDeviceMetricsOverride',{width:1500,height:900,deviceScaleFactor:1,mobile:false});
  await cdp('Page.navigate',{url:address});await waitFor('document.readyState==="complete"&&!!document.querySelector("#fileInput")');await evaluate('window.ExcelLensSession.ready');
  const input=await cdp('Runtime.evaluate',{expression:'document.querySelector("#fileInput")'});await cdp('DOM.setFileInputFiles',{objectId:input.result.objectId,files:[file]});
  await waitFor('document.querySelector("#indexOverlay").hidden&&!!document.querySelector(".grid-cell")&&window.ExcelLensPerformance().hasVisibleCoverage');
  await evaluate(`Object.defineProperty(navigator,'clipboard',{configurable:true,value:{writeText:async()=>{}}});document.querySelector('#copyValueBtn').click()`);await check('copy-success','success');
  await evaluate(`Object.defineProperty(navigator,'clipboard',{configurable:true,value:{writeText:async()=>{throw Error('合成复制错误')}}});document.querySelector('#copyValueBtn').click()`);await check('copy-error','error');
  await evaluate(`document.querySelector('#formulaViewBtn').click()`);await check('info-after-error','info');
  await evaluate(`(()=>{const n=document.querySelector('#nameBox');n.value='??';n.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true}));})()`);await check('address-warning','warning');
  await evaluate(`document.querySelector('#analysisOpenBtn').click()`);await waitFor('!document.querySelector("#analysisRoot").hidden');
  // Use the maintained analysis renderer with the production stylesheet; do not
  // create another notification implementation just for the visual check.
  const analytics=fs.readFileSync(path.join(root,'internal/ui/web/analytics.js'),'utf8'),start=analytics.indexOf('  function toast('),end=analytics.indexOf('\n  function ',start+1);
  await evaluate(`(()=>{const $=id=>document.getElementById(id);let toastTimer=0;${analytics.slice(start,end)};window.__qaAnalysisNotice=toast;})()`);
  for(const tone of ['info','success','warning','error','info']){await evaluate(`window.__qaAnalysisNotice('分析提示：'+${JSON.stringify(tone)},${JSON.stringify(tone)})`);await check('analysis-'+tone+'-'+cases.length,tone,'analysisToast');}
  assert.deepEqual(errors,[]);
  const report={version,result:'PASS',browser:(await cdp('Browser.getVersion')).product,desktopManualAcceptance:false,clipboard:'simulated success and refusal, no system clipboard writes',cases};
  fs.writeFileSync(path.join(root,'validation',version,'notifications.json'),JSON.stringify(report,null,2)+'\n');console.log(JSON.stringify(report,null,2));
}finally{
  if(socket?.readyState===1){try{await cdp('Browser.close');}catch{}socket.close();}
  if(child.exitCode===null&&child.signalCode===null)await Promise.race([new Promise(resolve=>child.once('exit',resolve)),pause(1500)]);if(child.exitCode===null&&child.signalCode===null)child.kill();
  await pause(300);if(path.dirname(profile)===output&&path.basename(profile).startsWith('chrome-'))try{fs.rmSync(profile,{recursive:true,force:true,maxRetries:12,retryDelay:200});}catch(error){console.error('QA profile retained: '+profile+' ('+error.code+')');}
}})().catch(error=>{console.error(error);process.exitCode=1;});
