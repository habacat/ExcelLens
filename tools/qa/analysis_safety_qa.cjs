// Synthetic checks against an isolated production service and headless Chrome.
const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict');
const {spawn}=require('node:child_process'),{createHash}=require('node:crypto');
const root=path.resolve(__dirname,'../..'),version=require('./project_config.cjs').version();
const outputVersion=require('./project_config.cjs').validationVersion();
const out=path.join(root,'validation',outputVersion,'private','analysis-safety');fs.mkdirSync(out,{recursive:true});
const exe=process.argv[2];if(!exe)throw Error('Usage: node tools/qa/analysis_safety_qa.cjs UIPROBE_EXE');
const pause=ms=>new Promise(resolve=>setTimeout(resolve,ms));
const report={version,result:'FAIL',desktopManualAcceptance:false,checks:[],errors:[]};
const file=path.join(out,'只读源.csv');fs.writeFileSync(file,'标签,金额\n甲,2\n乙,3\n丙,4\n');
const hash=()=>createHash('sha256').update(fs.readFileSync(file)).digest('hex'),before=hash();
const profile=fs.mkdtempSync(path.join(out,'chrome-'));
let browser,server,socket,seq=0;const pending=new Map();
function cdp(method,params={}){return new Promise((resolve,reject)=>{const id=++seq,timer=setTimeout(()=>{pending.delete(id);reject(Error(method+' timeout'));},20000);pending.set(id,{resolve,reject,timer});socket.send(JSON.stringify({id,method,params}));});}
async function evaluate(expression){const r=await cdp('Runtime.evaluate',{expression,returnByValue:true,awaitPromise:true});if(r.exceptionDetails)throw Error(JSON.stringify(r.exceptionDetails));return r.result.value;}
const run=(fn,...args)=>evaluate('('+fn.toString()+')('+args.map(x=>JSON.stringify(x)).join(',')+')');
async function waitFor(expression){for(let i=0;i<300;i++){if(await evaluate(expression))return;await pause(50);}throw Error('Timeout: '+expression);}
const click=selector=>run(selector=>document.querySelector(selector).click(),selector);
const api=(url,data)=>run(async(url,data)=>{
  await window.ExcelLensSession.ready;
  const headers=window.ExcelLensSession.apply(new Headers({'X-ExcelLens-Token':document.querySelector('meta[name="app-token"]').content,'Content-Type':'application/json'}));
  const response=await fetch(url,{headers,...(data===null?{}:{method:'POST',body:JSON.stringify(data)})}),value=await response.json();
  if(!response.ok)throw Error(JSON.stringify(value));return value;
},url,data===undefined?null:data);
const saved=()=>waitFor('window.ExcelLensAnalytics.diagnostics().pendingWrites===0&&window.ExcelLensAnalytics.diagnostics().drafts===0');
const edit=(row,col,value)=>run((row,col,value)=>{
  const n=document.querySelector('textarea[data-row="'+row+'"][data-col="'+col+'"]');n.value=value;
  n.dispatchEvent(new Event('input',{bubbles:true}));n.dispatchEvent(new Event('change',{bubbles:true}));
},row,col,value);
async function shot(name){await evaluate('new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))');const result=await cdp('Page.captureScreenshot',{format:'png',captureBeyondViewport:false});fs.writeFileSync(path.join(out,name+'.png'),Buffer.from(result.data,'base64'));}
async function upload(file){const result=await cdp('Runtime.evaluate',{expression:'document.querySelector("#fileInput")'});await cdp('DOM.setFileInputFiles',{objectId:result.result.objectId,files:[file]});}
(async()=>{
  try{
    server=spawn(path.resolve(exe),['-duration','5m'],{cwd:root,windowsHide:true,stdio:['ignore','pipe','pipe']});
    const address=await new Promise((resolve,reject)=>{let data='';server.stdout.on('data',chunk=>{data+=chunk;if(data.includes('\n')){try{resolve(JSON.parse(data.split('\n')[0]).url)}catch(error){reject(error)}}});server.once('error',reject);server.once('exit',code=>reject(Error('uiprobe exited '+code)));});
    browser=spawn(process.env.QA_CHROME||'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe',['--headless=new','--no-first-run','--no-default-browser-check','--disable-extensions','--disable-component-update','--disable-sync','--disable-background-networking','--remote-debugging-port=0','--user-data-dir='+profile,'about:blank'],{windowsHide:true,stdio:'ignore'});
    const active=path.join(profile,'DevToolsActivePort');for(let i=0;i<100&&!fs.existsSync(active);i++)await pause(100);
    const port=fs.readFileSync(active,'utf8').split('\n')[0],tabs=await(await fetch('http://127.0.0.1:'+port+'/json/list')).json();
    socket=new WebSocket(tabs.find(tab=>tab.type==='page').webSocketDebuggerUrl);
    await new Promise((resolve,reject)=>{socket.addEventListener('open',resolve,{once:true});socket.addEventListener('error',reject,{once:true});});
    socket.addEventListener('message',event=>{const value=JSON.parse(event.data);if(value.id){const item=pending.get(value.id);if(item){pending.delete(value.id);clearTimeout(item.timer);value.error?item.reject(Error(JSON.stringify(value.error))):item.resolve(value.result);}}else if(value.method==='Runtime.exceptionThrown')report.errors.push(value.params.exceptionDetails);});
    await cdp('Page.enable');await cdp('Runtime.enable');
    await cdp('Page.setDownloadBehavior',{behavior:'allow',downloadPath:path.join(out,'downloads')});
    await cdp('Emulation.setDeviceMetricsOverride',{width:1600,height:1000,deviceScaleFactor:1,mobile:false});await cdp('Page.navigate',{url:address});
    await waitFor('document.readyState==="complete"&&!!window.ExcelLensSession&&!!window.ExcelLensAnalytics');await evaluate('window.ExcelLensSession.ready');
    await upload(file);await waitFor('window.ExcelLensPerformance().hasVisibleCoverage&&!window.ExcelLensPerformance().sheetLoading');
    const status=await api('/api/status');assert.equal(status.version,version);
    const sheet=await api('/api/analysis/sheet/create',{name:'安全检查表',columns:['内容','编号']});
    await click('#analysisOpenBtn');await waitFor('!document.querySelector("#analysisRoot").hidden');await click('[data-analysis-tab="sheets"]');
    await waitFor('!!document.querySelector("textarea[data-row=\\"19\\"][data-col=\\"0\\"]")');
    await edit(9,0,'保留第十行');await saved();await edit(19,0,'保留第二十行');await saved();
    await click('#analysisAddRows');await saved();await waitFor('!!document.querySelector("textarea[data-row=\\"29\\"][data-col=\\"0\\"]")');
    let page=await api('/api/analysis/sheet?id='+sheet.id);assert.equal(page.total,30);assert.equal(page.rows[9][0],'保留第十行');assert.equal(page.rows[19][0],'保留第二十行');
    report.checks.push('real UI append after stale summary preserves rows 10 and 20 and produces 30 rows');
    await run(()=>{
      window.qaFetch=window.fetch.bind(window);window.qaFailBatch=0;window.qaFailStatus=0;
      window.fetch=async(url,options)=>{
        const name=String(url);
        if(name==='/api/analysis/sheet/batch'&&window.qaFailBatch){
          window.qaFailBatch--;
          if(window.qaDelay)return new Promise(resolve=>window.qaRelease=()=>resolve(new Response(JSON.stringify({error:'临时保存失败'}),{status:503})));
          return new Response(JSON.stringify({error:'临时保存失败'}),{status:503});
        }
        if(name.startsWith('/api/export/status')&&window.qaFailStatus){window.qaFailStatus--;return new Response(JSON.stringify({error:'临时状态中断'}),{status:503});}
        return window.qaFetch(url,options);
      };window.qaFailBatch=1;
    });
    await edit(0,0,'未保存草稿\n0001');await waitFor('window.ExcelLensAnalytics.diagnostics().pendingWrites===0&&window.ExcelLensAnalytics.diagnostics().drafts===1');
    assert.equal(await evaluate('document.querySelector("textarea[data-row=\\"0\\"][data-col=\\"0\\"]").value'),'未保存草稿\n0001');
    assert.equal(await evaluate('document.querySelector("#analysisRetrySave").hidden'),false);await shot('failed-draft');
    await click('#analysisClose');await click('#openBtn');
    assert((await evaluate('document.querySelector("#toast").textContent')).includes('未保存'));
    await click('#exitBtn');assert.equal((await api('/api/status')).workbookKey,status.workbookKey);
    report.checks.push('failed draft survives; file opening and page exit are blocked before transition');
    await click('#analysisOpenBtn');await waitFor('!!document.querySelector("textarea.is-unsaved")');await click('#analysisRetrySave');await saved();
    assert.equal((await api('/api/analysis/sheet?id='+sheet.id)).rows[0][0],'未保存草稿\n0001');
    await evaluate('window.qaFailBatch=1;window.qaDelay=true');await edit(0,0,'已发出文本');await waitFor('typeof window.qaRelease==="function"');
    await run(()=>{const n=document.querySelector('textarea[data-row="0"][data-col="0"]');n.value='失败期间的新草稿';n.dispatchEvent(new Event('input',{bubbles:true}));window.qaRelease();window.qaDelay=false;});
    await waitFor('window.ExcelLensAnalytics.diagnostics().pendingWrites===0');
    assert.equal(await evaluate('document.querySelector("textarea[data-row=\\"0\\"][data-col=\\"0\\"]").value'),'失败期间的新草稿');
    await click('#analysisRetrySave');await saved();assert.equal((await api('/api/analysis/sheet?id='+sheet.id)).rows[0][0],'失败期间的新草稿');
    report.checks.push('new typing during a failed older save remains intact and retry saves latest text');
    page=await api('/api/analysis/sheet?id='+sheet.id);
    await api('/api/analysis/sheet/batch',{id:sheet.id,workbookKey:status.workbookKey,expectedRevision:page.revision,changes:[{row:0,col:0,value:'另一标签已保存'}]});
    await edit(0,0,'本页冲突草稿');
    await waitFor('!!document.querySelector("textarea.is-unsaved")&&document.querySelector("textarea.is-unsaved").title.includes("另一标签已保存")');
    await shot('revision-conflict');await click('#analysisRetrySave');await saved();
    assert.equal((await api('/api/analysis/sheet?id='+sheet.id)).rows[0][0],'本页冲突草稿');
    report.checks.push('409 reload preserves draft and exposes saved value for review before retry');
    await run(()=>{
      window.qaFailBatch=1;const n=document.querySelector('textarea[data-row="2"][data-col="0"]'),event=new Event('paste',{bubbles:true,cancelable:true});
      Object.defineProperty(event,'clipboardData',{value:{getData:()=>'"多\n行"\t00042\nx\ty'}});n.dispatchEvent(event);
    });
    await waitFor('window.ExcelLensAnalytics.diagnostics().pendingWrites===0&&window.ExcelLensAnalytics.diagnostics().drafts===4');
    await click('#analysisRetrySave');await saved();page=await api('/api/analysis/sheet?id='+sheet.id);
    assert.deepEqual(page.rows[2],['多\n行','00042']);assert.deepEqual(page.rows[3],['x','y']);
    report.checks.push('failed Excel/WPS multiline paste retains four exact cells and retries successfully');
    const chinese='文'.repeat(4000),emoji='😀'.repeat(4000);
    await edit(4,0,chinese);await saved();await edit(5,0,emoji);await saved();
    page=await api('/api/analysis/sheet?id='+sheet.id);assert.equal(page.rows[4][0],chinese);assert.equal(page.rows[5][0],emoji);
    await edit(5,0,emoji+'😀');await waitFor('window.ExcelLensAnalytics.diagnostics().pendingWrites===0&&window.ExcelLensAnalytics.diagnostics().drafts===1');
    assert.equal((await api('/api/analysis/sheet?id='+sheet.id)).rows[5][0],emoji);
    assert.equal(await evaluate('document.querySelector("textarea[data-row=\\"5\\"][data-col=\\"0\\"]").value.length'),8002);
    await edit(5,0,emoji);await saved();
    report.checks.push('real Unicode editor saves 4000 Chinese characters and emoji, rejects 4001 without truncating the draft');
    await cdp('Emulation.setDeviceMetricsOverride',{width:1000,height:900,deviceScaleFactor:1,mobile:false});await shot('saved-narrow');
    await cdp('Emulation.setDeviceMetricsOverride',{width:1600,height:1000,deviceScaleFactor:1,mobile:false});
    const workspace=await api('/api/analysis/workspace'),source='wb:'+workspace.sourceSheets[0].name;
    const query={source,headerRow:1,rows:['C1'],columns:[],value:'C2',aggregate:'sum',metrics:[],filters:[],transforms:[],sort:'label_asc',top:30,precision:2,chart:'bar',chartMetric:0};
    const reportA=await api('/api/analysis/report',{name:'全部金额',query}),reportB=await api('/api/analysis/report',{name:'甲金额',query:{...query,filters:[{field:'C1',op:'eq',value:'甲'}]}});
    await run(()=>{
      const previous=window.fetch;window.qaAnalysisStarts=[];window.qaAnalysisCancels=[];window.qaAnalysisExports=[];window.qaHoldStart=0;
      window.fetch=async(url,options)=>{
        const name=String(url),hold=name==='/api/analysis/query/start'&&window.qaHoldStart>0;if(hold)window.qaHoldStart--;
        const response=await previous(url,options);
        if(name==='/api/analysis/query/start'){
          const started=await response.clone().json();window.qaAnalysisStarts.push({id:started.jobId,query:JSON.parse(options.body)});
          if(hold){window.qaHeldJob=started.jobId;return new Promise(resolve=>window.qaReleaseStart=()=>resolve(response));}
        }
        if(name==='/api/analysis/query/cancel')window.qaAnalysisCancels.push(JSON.parse(options.body).id);
        if(name.startsWith('/api/analysis/export?'))window.qaAnalysisExports.push({url:name,status:response.status,body:await response.clone().text()});
        return response;
      };
    });
    await evaluate('window.ExcelLensAnalytics.refresh()');await click('[data-analysis-tab="dashboard"]');
    await waitFor('document.querySelectorAll(".analysis-dashboard-card").length===2&&!document.querySelector(".analysis-card-loading")&&window.ExcelLensAnalytics.diagnostics().activeJobs===0');
    const taskA=await run(()=>window.qaAnalysisStarts.find(x=>!x.query.filters?.length).id);
    await click('[data-report-id="'+reportB.id+'"] [data-report-open]');
    await waitFor('document.querySelector("#analysisReportSubtitle").textContent.includes("金额")&&!document.querySelector("#analysisExport").disabled');
    await evaluate('window.qaHoldStart=1');await click('#analysisRun');await waitFor('typeof window.qaReleaseStart==="function"');
    await click('#analysisCancel');assert.equal(await evaluate('document.querySelector("#analysisRun").disabled'),false);
    await click('#analysisRun');await waitFor('window.qaAnalysisStarts.length===4&&!document.querySelector("#analysisRun").disabled');
    await evaluate('window.qaReleaseStart()');await waitFor('window.ExcelLensAnalytics.diagnostics().activeJobs===0&&window.qaAnalysisCancels.includes(window.qaHeldJob)');
    assert.equal(await evaluate('document.querySelector("#analysisProgress").hidden'),true);
    report.checks.push('cancel before start response returns reclaims the correct job and cannot disturb a newer completed analysis');
    await click('[data-analysis-tab="dashboard"]');await waitFor('!document.querySelector(".analysis-card-loading")');
    const startsBefore=await evaluate('window.qaAnalysisStarts.length');
    await click('[data-report-id="'+reportA.id+'"] [data-report-open]');await waitFor('!document.querySelector("#analysisExport").disabled');
    assert((await evaluate('document.querySelector("#analysisSourceInfo").textContent')).includes('条源记录'));
    assert.equal(await evaluate('document.querySelector("#analysisAggregate").value'),'sum');
    assert.equal(await evaluate('document.querySelector("#analysisSort").value'),'label_asc');
    await run(()=>{document.querySelector('#analysisExportFormat').value='csv';});await click('#analysisExport');await waitFor('window.qaAnalysisExports.length===1');
    const exported=await evaluate('window.qaAnalysisExports[0]');assert.equal(exported.status,200);assert(exported.url.includes('id='+encodeURIComponent(taskA)+'&'));assert(exported.body.includes('乙')&&exported.body.includes('丙')&&/总计,9(?:\r?\n|$)/.test(exported.body),exported.body);
    assert.equal(await evaluate('window.qaAnalysisStarts.length'),startsBefore,'cached report must preserve the existing snapshot');
    await shot('cached-report-export');
    await click('#analysisMaterialize');await waitFor('!!document.querySelector(".ask-dialog[open] input")');
    await run(()=>{document.querySelector('.ask-dialog[open] input').value='缓存报表A结果';});await click('.ask-dialog[open] [data-yes]');
    await waitFor('window.ExcelLensAnalytics.diagnostics().tab==="sheets"');
    const materialized=(await api('/api/analysis/workspace')).sheets.find(x=>x.name==='缓存报表A结果');assert(materialized);
    const resultSheet=await api('/api/analysis/sheet?id='+materialized.id);assert(resultSheet.rows.some(row=>row[0]==='总计'&&row.at(-1)==='9'));assert(resultSheet.rows.some(row=>row.includes('乙')));
    report.checks.push('cached report A after report B restores builder controls, exports its own task and materializes the same full result');
    const reportZero=await api('/api/analysis/report',{name:'无分类整数汇总',query:{...query,rows:null,top:0,precision:0}});
    await evaluate('window.ExcelLensAnalytics.refresh()');await click('[data-analysis-tab="dashboard"]');await waitFor('!document.querySelector(".analysis-card-loading")&&window.ExcelLensAnalytics.diagnostics().activeJobs===0');
    await click('[data-report-id="'+reportZero.id+'"] [data-report-open]');await waitFor('!document.querySelector("#analysisExport").disabled');
    assert.equal(await evaluate('document.querySelector("#analysisPrecision").value'),'0');assert.equal(await evaluate('document.querySelector("#analysisTop").value'),'0');
    assert((await evaluate('document.querySelector("#analysisSourceInfo").textContent')).includes('条源记录'));
    assert.equal(await evaluate('document.querySelector("#analysisMetrics .analysis-metric-value").textContent'),'9');
    report.checks.push('saved report with null dimensions and omitted zero settings restores the full builder and integer display');
    await click('#analysisClose');await click('#exportOpenBtn');await run(()=>{const n=document.querySelector('#exportFormat');n.value='csv';n.dispatchEvent(new Event('change'));window.qaFailStatus=1;});await click('#exportStartBtn');
    await waitFor('!document.querySelector("#exportRetryBtn").hidden');assert.equal(await evaluate('document.querySelector("#exportStartBtn").disabled'),true);
    await shot('export-status-interrupted');await waitFor('!document.querySelector("#exportResult").hidden');
    assert((await evaluate('document.querySelector("#exportDownload").getAttribute("href")')).startsWith('/api/export/download'));
    report.checks.push('export status interruption keeps settings locked and recovers the same download');
    await evaluate('document.querySelector("#exportDialog").close()');
    const other=path.join(out,'另一文件.csv');fs.writeFileSync(other,'姓名,金额\n丁,8\n');await upload(other);
    await waitFor('document.querySelector("#fileBadge").textContent.includes("另一文件")&&window.ExcelLensPerformance().hasVisibleCoverage');
    assert.equal(await evaluate('document.querySelector("#exportResult").hidden'),true);assert.equal(await evaluate('document.querySelector("#exportDownload").getAttribute("href")'),null);
    report.checks.push('next workbook clears earlier export result and link');
    assert.equal(hash(),before);assert.equal(report.errors.length,0);report.sourceSHA256=before;report.sourceUnchanged=true;report.result='PASS';
  }catch(error){report.error=error.stack;process.exitCode=1;}
  finally{
    fs.writeFileSync(path.join(root,'validation',outputVersion,'analysis-safety.json'),JSON.stringify(report,null,2)+'\n');
    for(const item of pending.values())clearTimeout(item.timer);pending.clear();socket?.close();browser?.kill();server?.kill();
    console.log(JSON.stringify(report,null,2));
  }
})().catch(error=>{console.error(error);process.exitCode=1;});
