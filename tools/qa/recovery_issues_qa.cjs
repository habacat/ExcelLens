// Synthetic end-to-end checks. Only child processes and isolated QA storage
// created here may be terminated or removed; the user's app is never touched.
const fs=require('node:fs'),os=require('node:os'),path=require('node:path'),assert=require('node:assert/strict');
const {spawn}=require('node:child_process'),{createHash}=require('node:crypto');
const root=path.resolve(__dirname,'../..'),version=require('./project_config.cjs').version();
const outputVersion=require('./project_config.cjs').validationVersion();
const out=path.join(root,'validation',outputVersion,'private','recovery-issues');fs.mkdirSync(out,{recursive:true});
const exe=path.resolve(process.argv[2]||'artifacts/qa/uiprobe.exe');
const pause=ms=>new Promise(resolve=>setTimeout(resolve,ms));
const report={version,result:'FAIL',desktopManualAcceptance:false,checks:[],errors:[]};
const storage=fs.mkdtempSync(path.join(os.tmpdir(),'excellens-ui-qa-'));
const profile=fs.mkdtempSync(path.join(out,'chrome-'));
const source=path.join(out,'只读源.csv');fs.writeFileSync(source,'标签,金额\n甲,2\n乙,3\n丙,4\n');
const hash=()=>createHash('sha256').update(fs.readFileSync(source)).digest('hex'),original=hash();
let browser,server,socket,seq=0;const pending=new Map();
function cdp(method,params={}){return new Promise((resolve,reject)=>{const id=++seq,timer=setTimeout(()=>{pending.delete(id);reject(Error(method+' timeout'));},20000);pending.set(id,{resolve,reject,timer});socket.send(JSON.stringify({id,method,params}));});}
async function evaluate(expression){const r=await cdp('Runtime.evaluate',{expression,returnByValue:true,awaitPromise:true});if(r.exceptionDetails)throw Error(JSON.stringify(r.exceptionDetails));return r.result.value;}
const run=(fn,...args)=>evaluate('('+fn.toString()+')('+args.map(x=>JSON.stringify(x)).join(',')+')');
async function waitFor(expression){for(let i=0;i<300;i++){try{if(await evaluate(expression))return;}catch(error){if(!/Cannot find context|Execution context was destroyed|Inspected target navigated/.test(error.message))throw error;}await pause(50);}throw Error('Timeout: '+expression);}
const click=selector=>run(selector=>{const node=document.querySelector(selector);if(!node)throw Error('Missing '+selector);node.click();},selector);
const api=(url,data)=>run(async(url,data)=>{
  await window.ExcelLensSession.ready;
  const headers=window.ExcelLensSession.apply(new Headers({'X-ExcelLens-Token':document.querySelector('meta[name="app-token"]').content,'Content-Type':'application/json'}));
  const response=await fetch(url,{headers,...(data===null?{}:{method:'POST',body:JSON.stringify(data)})}),value=await response.json();
  if(!response.ok)throw Error(JSON.stringify(value));return value;
},url,data===undefined?null:data);
async function upload(){const r=await cdp('Runtime.evaluate',{expression:'document.querySelector("#fileInput")'});await cdp('DOM.setFileInputFiles',{objectId:r.result.objectId,files:[source]});await waitFor('window.ExcelLensPerformance().hasVisibleCoverage&&!window.ExcelLensPerformance().sheetLoading');}
async function shot(name){await evaluate('new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))');const result=await cdp('Page.captureScreenshot',{format:'png',captureBeyondViewport:false});fs.writeFileSync(path.join(out,name+'.png'),Buffer.from(result.data,'base64'));}
async function startServer(){
  server=spawn(exe,['-duration','5m','-storage',storage],{cwd:root,windowsHide:true,stdio:['ignore','pipe','pipe']});
  return new Promise((resolve,reject)=>{let text='',errors='';server.stderr.on('data',chunk=>errors+=chunk);server.stdout.on('data',chunk=>{text+=chunk;if(text.includes('\n')){try{resolve(JSON.parse(text.split('\n')[0]).url);}catch(error){reject(error);}}});server.once('error',reject);server.once('exit',code=>reject(Error('QA server exited '+code+' '+errors)));});
}
async function stopChild(child){if(!child||child.exitCode!==null)return;await new Promise(resolve=>{child.once('exit',resolve);child.kill();});}
async function ready(url){const previous=await evaluate('performance.timeOrigin');await cdp('Page.navigate',{url});await waitFor('performance.timeOrigin!=='+previous+'&&document.readyState==="complete"&&!!window.ExcelLensAnalytics&&!!window.ExcelLensAnalysisRecovery');await evaluate('Promise.all([window.ExcelLensSession.ready,window.ExcelLensAnalysisRecovery.ready])');}
async function monitor(){await run(()=>{
  window.qaFormulaCalls=0;window.qaBatchCalls=0;window.qaRecoveryWrites=[];window.qaRecoveryHold=false;
  const original=window.fetch.bind(window);window.qaFetch=original;
  window.fetch=async(url,opts)=>{
    const name=String(url);
    if(name==='/api/calculator/formula')window.qaFormulaCalls++;
    if(name==='/api/analysis/sheet/batch')window.qaBatchCalls++;
    if(name==='/api/recovery'&&opts?.method==='POST'){
      const body=JSON.parse(opts.body);window.qaRecoveryWrites.push(body);
      const response=await original(url,opts);
      if(window.qaRecoveryHold&&body.kind==='calculator'){window.qaRecoveryHold=false;window.qaHeldRequest=body;return new Promise(resolve=>window.qaReleaseRecovery=()=>resolve(response));}
      return response;
    }
    return original(url,opts);
  };
});}
async function loadRecovery(button){
  await click(button);await waitFor('!!document.querySelector(".recovery-dialog[open] .recovery-entry")');
  await click('.recovery-dialog[open] .recovery-entry');await waitFor('!document.querySelector(".recovery-dialog[open] .recovery-load").hidden');
  await click('.recovery-dialog[open] .recovery-load');await waitFor('document.querySelector(".recovery-dialog[open] .recovery-status").textContent.includes("已载入")');
  await shot(button==='#calculatorRecovery'?'calculator-recovery':'analysis-draft-recovery');
  await click('.recovery-dialog[open] .recovery-head button');
}
(async()=>{
  try{
    const address=await startServer();
    browser=spawn(process.env.QA_CHROME||'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe',['--headless=new','--no-first-run','--no-default-browser-check','--disable-extensions','--disable-component-update','--disable-sync','--disable-background-networking','--remote-debugging-port=0','--user-data-dir='+profile,'about:blank'],{windowsHide:true,stdio:'ignore'});
    const active=path.join(profile,'DevToolsActivePort');for(let i=0;i<100&&!fs.existsSync(active);i++)await pause(100);
    const port=fs.readFileSync(active,'utf8').split('\n')[0],tabs=await(await fetch('http://127.0.0.1:'+port+'/json/list')).json();
    socket=new WebSocket(tabs.find(tab=>tab.type==='page').webSocketDebuggerUrl);
    await new Promise((resolve,reject)=>{socket.addEventListener('open',resolve,{once:true});socket.addEventListener('error',reject,{once:true});});
    socket.addEventListener('message',event=>{const value=JSON.parse(event.data);if(value.id){const item=pending.get(value.id);if(item){pending.delete(value.id);clearTimeout(item.timer);value.error?item.reject(Error(JSON.stringify(value.error))):item.resolve(value.result);}}else if(value.method==='Runtime.exceptionThrown')report.errors.push(value.params.exceptionDetails);else if(value.method==='Page.javascriptDialogOpening')cdp('Page.handleJavaScriptDialog',{accept:true}).catch(()=>{});});
    await cdp('Page.enable');await cdp('Runtime.enable');await cdp('Emulation.setDeviceMetricsOverride',{width:1600,height:1000,deviceScaleFactor:1,mobile:false});
    await ready(address);await upload();await monitor();
    const status=await api('/api/status');assert.equal(status.version,version);
    await click('#calculatorOpenBtn');
    await run(()=>{const input=document.querySelector('input[data-formula-row="0"]');input.focus();input.value='=SUM(B2:B4)';input.dispatchEvent(new Event('input',{bubbles:true}));const note=document.querySelector('input[data-note-row="0"]');note.value='重启后核对';note.dispatchEvent(new Event('input',{bubbles:true}));});
    await waitFor('document.querySelector("#calculatorSaveStatus").textContent==="已保存"');
    const sheet=await api('/api/analysis/sheet/create',{name:'恢复测试表',columns:['输入','备注']});
    await click('#analysisOpenBtn');await click('[data-analysis-tab="sheets"]');await waitFor('!!document.querySelector("textarea[data-row=\\"0\\"][data-col=\\"0\\"]")');
    await run(()=>{const input=document.querySelector('textarea[data-row="0"][data-col="0"]');input.value='未确认草稿\n0001';input.dispatchEvent(new Event('input',{bubbles:true}));});
    assert.equal(await evaluate('window.ExcelLensAnalysisRecovery.flush()'),true);
    assert.equal((await api('/api/analysis/sheet?id='+sheet.id)).rows.length,0);
    // Refresh with a confirmed recovery journal, not a submitted worksheet edit.
    await ready(address);
    await click('#analysisOpenBtn');await click('[data-analysis-tab="sheets"]');await waitFor('!!document.querySelector("textarea.is-unsaved")');
    assert.equal(await evaluate('document.querySelector("textarea.is-unsaved").value'),'未确认草稿\n0001');assert.equal((await api('/api/analysis/sheet?id='+sheet.id)).rows.length,0);
    report.checks.push('same-page refresh restores unconfirmed draft without submitting it to the saved worksheet');
    await stopChild(server);const restarted=await startServer();
    await ready(restarted);await monitor();
    await click('#calculatorOpenBtn');await loadRecovery('#calculatorRecovery');
    assert.equal(await evaluate('document.querySelector("input[data-formula-row=\\"0\\"]").value'),'=SUM(B2:B4)');assert.equal(await evaluate('document.querySelector("input[data-note-row=\\"0\\"]").value'),'重启后核对');
    assert.equal(await evaluate('document.querySelector("[data-result-row=\\"0\\"]").textContent'),'待计算');assert.equal(await evaluate('window.qaFormulaCalls'),0);
    await run(()=>{document.querySelector('input[data-formula-row="1"]').focus();document.querySelector('input[data-note-row="0"]').focus();});assert.equal(await evaluate('window.qaFormulaCalls'),0);
    report.checks.push('forced service termination and new port preserve calculator inputs; load and focus changes execute no restored formula');
    await upload();await click('#analysisOpenBtn');await click('[data-analysis-tab="sheets"]');await waitFor('!!document.querySelector("textarea[data-row=\\"0\\"][data-col=\\"0\\"]")');
    await loadRecovery('#analysisDraftRecovery');await waitFor('!!document.querySelector("textarea.is-unsaved")');
    assert.equal(await evaluate('window.qaBatchCalls'),0);assert.equal((await api('/api/analysis/sheet?id='+sheet.id)).rows.length,0);
    assert.equal(await evaluate('document.querySelector("textarea.is-unsaved").value'),'未确认草稿\n0001');
    await click('#analysisRetrySave');await waitFor('window.ExcelLensAnalytics.diagnostics().drafts===0&&window.ExcelLensAnalytics.diagnostics().pendingWrites===0');await evaluate('window.ExcelLensAnalysisRecovery.flush()');
    assert.equal((await api('/api/analysis/sheet?id='+sheet.id)).rows[0][0],'未确认草稿\n0001');
    report.checks.push('cross-port analysis recovery overlays saved values and submits only after the user presses retry');
    await click('#analysisClose');await click('#calculatorOpenBtn');
    await run(()=>{window.qaRecoveryHold=true;const input=document.querySelector('input[data-formula-row="0"]');input.value='=1+10';input.dispatchEvent(new Event('input',{bubbles:true}));});
    await waitFor('typeof window.qaReleaseRecovery==="function"');
    await run(()=>{const input=document.querySelector('input[data-formula-row="0"]');input.value='=2+20';input.dispatchEvent(new Event('input',{bubbles:true}));window.qaReleaseRecovery();});
    await waitFor('document.querySelector("#calculatorSaveStatus").textContent==="已保存"');
    const owners=(await api('/api/recovery/list?kind=calculator')).entries;let newest=await api('/api/recovery?kind=calculator&owner='+owners[0].owner);assert.equal(newest.records[0].text,'=2+20');
    report.checks.push('delayed durable acknowledgement cannot erase or falsely confirm a newer formula input');
    const current=await api('/api/analysis/sheet?id='+sheet.id),values=['0001','=1+2','中文,"原文"\n下一行'];
    await api('/api/analysis/sheet/batch',{id:sheet.id,workbookKey:status.workbookKey,expectedRevision:current.revision,changes:Array.from({length:121},(_,row)=>({row,col:0,value:values[row%3]}))});
    const query={source:'scratch:'+sheet.id,headerRow:1,rows:[],columns:[],value:'C1',aggregate:'sum',filters:[],transforms:[],sort:'label_asc',top:0,precision:2,chart:'bar'};
    const saved=await api('/api/analysis/report',{name:'异常清单测试',query});
    await click('#calculatorClose');await click('#analysisOpenBtn');await evaluate('window.ExcelLensAnalytics.refresh()');await click('[data-analysis-tab="dashboard"]');
    await waitFor('!!document.querySelector("[data-report-id=\\"'+saved.id+'\\"] [data-report-open]")&&!document.querySelector(".analysis-card-loading")');
    await click('[data-report-id="'+saved.id+'"] [data-report-open]');await waitFor('!document.querySelector("#analysisIssuesOpen").hidden');await click('#analysisIssuesOpen');
    assert((await evaluate('document.querySelector("#analysisIssuesSummary").textContent')).includes('121'));assert.equal(await evaluate('document.querySelectorAll("#analysisIssuesTable tbody tr").length'),100);
    assert.equal(await evaluate('document.querySelector("#analysisIssuesTable tbody tr td").textContent'),'A1');await shot('issues-light');
    await run(()=>{document.querySelector('input[name="readingPreset"][value="graphite"]').click();});await cdp('Emulation.setDeviceMetricsOverride',{width:1000,height:900,deviceScaleFactor:1,mobile:false});await shot('issues-dark-narrow');
    assert.equal(await evaluate('getComputedStyle(document.querySelector("#analysisIssuesDialog")).backgroundColor'),'rgb(46, 51, 58)');
    assert.equal(await evaluate('getComputedStyle(document.querySelector("#analysisIssuesClose")).backgroundColor'),'rgb(35, 39, 44)');
    assert.equal(await evaluate('getComputedStyle(document.querySelector("#analysisIssuesTable td")).borderBottomColor'),'rgb(70, 80, 91)');
    assert.equal(await evaluate('getComputedStyle(document.querySelector("#analysisIssuesTable button")).color'),'rgb(161, 201, 234)');
    await click('#analysisIssuesExtract');await waitFor('!!document.querySelector(".analysis-modal-shade input")');await run(()=>{document.querySelector('.analysis-modal-shade input').value='完整核对编辑表';});await click('.analysis-modal-shade [data-yes]');
    await waitFor('window.ExcelLensAnalytics.diagnostics().tab==="sheets"');const materialized=(await api('/api/analysis/workspace')).sheets.find(item=>item.name==='完整核对编辑表');assert(materialized);
    const materializedRows=await api('/api/analysis/sheet?id='+materialized.id+'&limit=200');assert.equal(materializedRows.total,121);assert.equal(materializedRows.rows[2][9],values[2]);
    report.checks.push('issue dialog shows exactly 100 samples with correct source positions; full editable extraction preserves 121 original items and dark/narrow layout');
    await click('[data-analysis-tab="pivot"]');await click('#analysisIssuesOpen');await run(()=>{document.querySelector('#analysisIssuesCSVGuard').checked=false;});await click('#analysisIssuesExport');
    await waitFor('document.querySelector("#exportDialog").open&&!document.querySelector("#exportResult").hidden');
    const download=await evaluate('document.querySelector("#exportDownload").getAttribute("href")');assert(download.includes('/api/export/download'));
    const exported=await run(async url=>(await fetch(url)).text(),download);assert(exported.includes('0001'));assert(exported.includes('"中文,""原文""\n下一行"'));assert(!exported.includes("'=1+2"));
    assert.equal(await evaluate('document.querySelector("#exportRangeLabel").textContent'),'异常核对清单（完整）');await shot('issues-export-center');
    report.checks.push('full issue CSV streams through the shared export center and preserves multiline original text when CSV protection is disabled');
    assert.equal(hash(),original);assert.equal(report.errors.length,0);report.sourceSHA256=original;report.sourceUnchanged=true;report.result='PASS';
  }catch(error){report.error=error.stack;process.exitCode=1;}
  finally{
    fs.writeFileSync(path.join(root,'validation',outputVersion,'recovery-issues.json'),JSON.stringify(report,null,2)+'\n');
    for(const item of pending.values())clearTimeout(item.timer);pending.clear();socket?.close();await stopChild(browser);await stopChild(server);
    const target=path.resolve(storage);if(path.dirname(target)===path.resolve(os.tmpdir())&&path.basename(target).startsWith('excellens-ui-qa-'))fs.rmSync(target,{recursive:true,force:true});
    console.log(JSON.stringify(report,null,2));
  }
})().catch(error=>{console.error(error);process.exitCode=1;});
