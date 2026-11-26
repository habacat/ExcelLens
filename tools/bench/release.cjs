// Run with Node 22+ and Python 3. All fixtures, service state and browser data
// are synthetic. The report is written after each scenario, including failures.
const fs=require('node:fs'),path=require('node:path'),os=require('node:os'),crypto=require('node:crypto');
const {spawn,execFileSync}=require('node:child_process');
const {createInterface}=require('node:readline');
const {performance}=require('node:perf_hooks');
const assert=require('node:assert/strict');
const {launch,pause}=require('../qa/browser_harness.cjs');
const config=require('../qa/project_config.cjs');
const {compareBaseline}=require('./compare.cjs');
const root=path.resolve(__dirname,'../..');
const args=process.argv.slice(2);
const smoke=args.includes('--smoke');
const only=args.find(s=>s.startsWith('--only='))?.slice(7);
const baselineTarget=args.find(s=>s.startsWith('--save-baseline='))?.slice(16);
if(baselineTarget&&(only||smoke))throw Error('A release baseline requires the complete release matrix');
const output=path.resolve(args.find(x=>x.startsWith('--out='))?.slice(6)||path.join(root,'validation',config.version(),'private','performance.json'));
const temp=fs.mkdtempSync(path.join(os.tmpdir(),'excellens-benchmark-'));
const python=process.env.QA_PYTHON||(process.platform==='win32'?'python':'python3');
const go=args.find(x=>x.startsWith('--go='))?.slice(5)||process.env.QA_GO||'go';
function componentHash(...inputs){
  const h=crypto.createHash('sha256');
  function scan(file){
    const stat=fs.lstatSync(file);
    if(stat.isFile()){
      h.update(path.relative(root,file).split(path.sep).join('/')+'\0').update(fs.readFileSync(file)).update('\0');
    }else if(stat.isDirectory()){
      for(const name of fs.readdirSync(file).sort()){
        if(name!=='__pycache__')scan(path.join(file,name));
      }
    }
  }
  for(const input of inputs)scan(path.join(root,input));
  return h.digest('hex');
}
const exe=path.join(temp,process.platform==='win32'?'probe.exe':'probe');
const reading=smoke?{csv:[1001],xlsx:[1001]}:{csv:[1000000,5000000,10000000],xlsx:[100000,500000,1000000]};
const ledgerRows=smoke?1000:1000000,editRows=smoke?1001:100000,scrollSeconds=smoke?2:60,cycles=smoke?3:100;
const gitInfo=args=>execFileSync('git',args,{cwd:root,encoding:'utf8'}).trim();
const checkout=fs.existsSync(path.join(root,'.git'));
const report={schema:2,profile:only?'diagnostic':smoke?'smoke':'release',version:config.version(),
  checkoutHead:checkout?gitInfo(['rev-parse','HEAD']):null,modifiedBuildInputs:checkout?Boolean(gitInfo(['status','--porcelain','--','internal','vendor','tools/bench','tools/uiprobe','tools/qa','go.mod','go.sum'])):null,
  started:new Date().toISOString(),host:{platform:process.platform,os:os.release(),cpu:os.cpus()[0].model,logicalCPUs:os.cpus().length,ramBytes:os.totalmem(),node:process.version,go:execFileSync(go,['version'],{encoding:'utf8'}).trim()},samples:[],readings:[],cleanup:[],result:'RUNNING'};
function save(){fs.mkdirSync(path.dirname(output),{recursive:true});fs.writeFileSync(output+'.tmp',JSON.stringify(report,null,2)+'\n');fs.renameSync(output+'.tmp',output);}
const elapsed=t=>Math.round((performance.now()-t)*100)/100;
const p95=arr=>arr.toSorted((a,b)=>a-b)[Math.ceil(arr.length*.95)-1];
async function sha(file){const h=crypto.createHash('sha256');for await(const chunk of fs.createReadStream(file))h.update(chunk);return h.digest('hex');}
const sampleFiles=new Map();
async function fixture(kind,rows,different=false){
  const name=kind+'-'+rows+(different?'-right':'')+'.'+kind,file=path.join(temp,name);
  if(!sampleFiles.has(name)){
    execFileSync(python,['-X','utf8',path.join(__dirname,'fixtures.py'),kind,file,String(rows),...(different?['right']:[])],{windowsHide:true});
    const entry={name,rows,bytes:fs.statSync(file).size,sha256:await sha(file)};
    sampleFiles.set(name,entry);report.samples.push(entry);save();
  }
  return file;
}
async function browserProcessIDs(h){
  const version=await (await fetch(h.debugEndpoint+'/json/version')).json();
  const socket=new WebSocket(version.webSocketDebuggerUrl);
  try{
    await new Promise((ok,no)=>{socket.onopen=ok;socket.onerror=no;});
    const message=new Promise((ok,no)=>{
      const timeout=setTimeout(()=>no(Error('browser process query timed out')),15000);
      socket.onmessage=event=>{const data=JSON.parse(event.data);if(data.id!==1)return;clearTimeout(timeout);data.error?no(Error(JSON.stringify(data.error))):ok(data.result);};
    });
    socket.send(JSON.stringify({id:1,method:'SystemInfo.getProcessInfo'}));
    return (await message).processInfo.map(p=>p.id);
  }finally{socket.close();}
}
async function meter(pid,data){
  const p=spawn(python,['-u',path.join(__dirname,'metrics.py'),String(pid),data],{windowsHide:true,stdio:['pipe','pipe','inherit']});
  const lines=createInterface({input:p.stdout})[Symbol.asyncIterator]();
  const read=async()=>{const next=await lines.next();if(next.done)throw Error('process resource monitor stopped');return JSON.parse(next.value);};
  await read();
  return {async sample(action='read',disk=false,pids=[]){p.stdin.write(JSON.stringify({action,disk,pids})+'\n');return read();},async close(){p.stdin.end();await lines.return();}};
}
async function withService(label,work){
  const errors=[],h=await launch({errors,exe,duration:'3h'}),m=await meter(h.servicePID,path.join(h.storage,'ExcelLens'));
  try{
    report.host.browser=(await h.page.call('Browser.getVersion')).product;
    await work(h,m);assert.deepEqual(errors,[]);save();
  }finally{
    await m.close();
    try{await h.close();}
    catch(error){if(!['EPERM','EBUSY','ENOTEMPTY'].includes(error.code))throw error;report.cleanup.push({scenario:label,code:error.code,temporaryProfile:path.basename(h.storage)});save();}
  }
}
async function waitFor(test,description,ms=900000){
  const end=Date.now()+ms;
  while(Date.now()<end){if(await test())return;await pause(25);}
  throw Error('Timeout: '+description);
}
async function open(page,file,rows){
  await page.run(()=>{
    if(!window.benchFetch){
      const actual=window.fetch;window.benchFetch=true;
      window.fetch=async(...args)=>{
        const route=String(args[0]),sample=window.benchOpen;
        if(sample&&route==='/api/index/start')sample.indexStart=performance.now();
        const res=await actual(...args);
        if(sample&&route.startsWith('/api/index/status?')&&res.ok){const v=await res.clone().json();if(v.status==='done'&&!sample.indexDone){sample.indexDone=performance.now();sample.rows=v.totalRows;}}
        return res;
      };
    }
    window.benchOpen={};
    const sample=window.benchOpen;
    document.querySelector('#fileInput').addEventListener('change',()=>{
      sample.start=performance.now();
      const frame=()=>{
        if(sample!==window.benchOpen)return;
        const cells=document.querySelectorAll('#rowsLayer .grid-cell');
        if(cells.length&&!sample.first)sample.first=performance.now();
        if(window.ExcelLensPerformance?.().hasVisibleCoverage&&!window.ExcelLensPerformance().sheetLoading&&sample.first){sample.ready=performance.now();return;}
        requestAnimationFrame(frame);
      };
      requestAnimationFrame(frame);
    },{once:true});
  });
  await page.file('#fileInput',file);
  await waitFor(()=>page.evaluate('!!window.benchOpen.ready'),path.basename(file));
  const time=await page.evaluate('window.benchOpen');
  const status=await page.api('/api/status');assert.equal(status.fileName,path.basename(file));
  if(time.rows!=null)assert.equal(time.rows,rows);
  return {status,time};
}
async function searchCase(h,book,rows){
  const name=book.sheets[0].name,t=performance.now();
  const {jobId}=await h.page.api('/api/search/start',{sheet:name,scope:'all',query:'office-42',mode:'value',full:true,book:book.book,workbookKey:book.workbookKey});
  let result;
  await waitFor(async()=>{
    result=await h.page.api('/api/search/status?id='+jobId);
    if(result.status!=='done'&&result.status!=='running')throw Error(result.error||result.status);
    return result.status==='done';
  },'full search');
  const searchMs=elapsed(t),count=Math.floor((rows+57)/100);
  assert.equal(result.hitCount,count);assert.equal(result.matches.length,0);
  const page=await h.page.api('/api/search/page?id='+jobId);
  assert.equal(page.matches.length,Math.min(count,100));
  const readStart=performance.now(),response=await fetch(new URL(result.downloadUrl,h.address));
  assert.equal(response.status,200);
  const lines=createInterface({input:require('node:stream').Readable.fromWeb(response.body),crlfDelay:Infinity});
  let n=0;
  for await(const line of lines){
    if(n===0)assert.ok(line.includes('工作表'));
    else assert.equal(line,'"'+name+'","C'+(43+(n-1)*100)+'","office-42",""');
    n++;
  }
  assert.equal(n,count+1);report.fullSearch={rows,hits:count,searchMs,csvStreamAndVerifyMs:elapsed(readStart),everyCoordinateVerified:true};
}
async function readingCases(){
  for(const [kind,list] of Object.entries(reading))for(const rows of list){
    const file=await fixture(kind,rows);
    await withService(kind+'-'+rows,async(h,m)=>{
      const start=await m.sample('start');
      const {status,time}=await open(h.page,file,rows);
      const met=await m.sample();
      assert.ok(time.indexDone>=time.indexStart,'missing real index response');
      const result={format:kind,rows,firstVisibleMs:time.first-time.start,indexRequestToReadyMs:time.indexDone-time.indexStart,
        visibleCoverageMs:time.ready-time.start,peakServiceRSSBytes:met.current.processPeakRSSBytes,sampledPeakServiceRSSBytes:met.peak.rssBytes,initialServiceRSSBytes:start.current.rssBytes};
      report.readings.push(result);save();console.log(kind,rows,Math.round(result.visibleCoverageMs)+' ms');
      if(kind==='csv'&&(smoke||rows===5000000))await searchCase(h,status,rows);
      if(kind==='csv'&&rows===list[0]){
        await h.page.call('Page.bringToFront');
        await h.page.run(seconds=>{
          const grid=document.getElementById('grid'),start=performance.now();
          let last=start;const intervals=[];let noCoverage=0,blank=0;
          function frame(t){
            if(t>last)intervals.push(t-last);last=t;
            grid.scrollTop=(t-start)*.9%(Math.max(1,grid.scrollHeight-grid.clientHeight));
            if(!window.ExcelLensPerformance().hasVisibleCoverage)noCoverage++;
            if(!document.querySelector('#rowsLayer .grid-cell')&&document.querySelector('#viewportFallback').hidden)blank++;
            if(t-start<seconds*1000)requestAnimationFrame(frame);
            else{intervals.sort((a,b)=>a-b);window.benchScroll={durationMs:t-start,frames:intervals.length,fps:intervals.length*1000/(t-start),p95FrameMs:intervals[Math.ceil(intervals.length*.95)-1],noCoverageFrames:noCoverage,emptyFrames:blank};}
          }
          requestAnimationFrame(frame);
        },scrollSeconds);
        await waitFor(()=>h.page.evaluate('!!window.benchScroll'),'scroll sampling',scrollSeconds*1000+60000);
        report.scroll=await h.page.evaluate('window.benchScroll');assert.equal(report.scroll.emptyFrames,0);
      }
    });
  }
}
async function newPage(h){
  const {targetId}=await h.page.call('Target.createTarget',{url:h.address});
  let tab;await waitFor(async()=>!!(tab=(await h.tabs()).find(t=>t.id===targetId)),'new tab');
  const p=await h.connect(tab);await p.wait('document.readyState==="complete"&&!!window.ExcelLensSession');await p.evaluate('ExcelLensSession.ready');return p;
}
async function reconcileCase(){
  const leftFile=await fixture('csv',ledgerRows+1),rightFile=await fixture('csv',ledgerRows+1,true);
  await withService('reconcile',async(h,m)=>{
    const left=h.page,leftBook=(await open(left,leftFile,ledgerRows+1)).status;
    const right=await newPage(h),rightBook=(await open(right,rightFile,ledgerRows+1)).status;
    async function ref(w){const r={book:w.book,source:'wb:'+w.sheets[0].name,headerRow:1};r.stamp=(await left.api('/api/reconcile/fields',r)).stamp;return r;}
    const params={primaryBook:leftBook.book,workbookKey:leftBook.workbookKey,left:await ref(leftBook),right:await ref(rightBook),query:{keys:[{left:1,right:1}],fields:[{left:2,right:2}]}};
    const initial=(await m.sample('start',true)).current,t=performance.now();
    const start=await left.api('/api/reconcile/start',params);let state;
    await waitFor(async()=>{state=await left.api('/api/reconcile/status?id='+start.jobId);if(!['queued','running','done'].includes(state.status))throw Error(state.error||state.status);return state.status==='done';},'reconcile');
    const ms=elapsed(t),metrics=await m.sample(),counts=state.result.counts;
    assert.equal(counts.leftRecords,ledgerRows);assert.equal(counts.rightRecords,ledgerRows);
    assert.equal(counts.different,1);assert.equal(counts.equal,ledgerRows-1);
    report.reconcile={rowsPerLedger:ledgerRows,elapsedMs:ms,peakServiceRSSBytes:metrics.peak.rssBytes,peakAdditionalDataBytes:Math.max(0,metrics.peak.dataBytes-initial.dataBytes),counts};
  });
}
async function tabCases(){
  await withService('tabs',async(h,m)=>{
    const pages=[h.page];report.tabs=[];
    const file=await fixture('csv',501);await open(h.page,file,501);
    for(const n of smoke?[2]:[10,30,64]){
      const before=await m.sample('start');
      await h.page.call('Page.bringToFront');
      while(pages.length<n){const page=await newPage(h);await open(page,file,501);pages.push(page);}
      const latencies=[];
      await Promise.all(pages.map(async page=>{
        for(let i=0;i<3;i++){const start=performance.now();const state=await page.api('/api/status');assert.equal(state.opened,true);assert.equal(state.fileName,path.basename(file));latencies.push(elapsed(start));}
      }));
      const met=await m.sample('browser',false,await browserProcessIDs(h));report.tabs.push({pages:n,perWorkbookRows:501,browser:met.browser,requests:latencies.length,p95StatusMs:p95(latencies),serviceRSSBytes:met.current.rssBytes,serviceHandles:met.current.handles,initialRSSBytes:before.current.rssBytes,peakRSSBytes:met.peak.rssBytes});save();
    }
  });
}
async function lifecycle(){
  const file=await fixture('csv',501);
  await withService('lifecycle',async(h,m)=>{
    const baseline=await m.sample('start'),points=[];report.lifecycle={roundsCompleted:0,samples:points};
    const batch=smoke?1:10;
    for(let round=0;round<cycles;round+=batch){
      const pages=[];
      for(let i=0;i<Math.min(batch,cycles-round);i++){const p=await newPage(h);await open(p,file,501);pages.push({p,id:await p.evaluate('ExcelLensSession.id')});}
      for(const {p,id} of pages){
        await p.call('Page.close');
        await waitFor(async()=>{try{await h.page.api('/api/background',{action:'release',session:id});return true;}catch(e){if(!String(e).includes('先关闭'))throw e;return false;}},'detached workbook');
      }
      points.push({round:Math.min(cycles,round+batch),...(await m.sample()).current});report.lifecycle.roundsCompleted=points.at(-1).round;save();
    }
    report.lifecycle={rounds:cycles,method:'close actual tab then release detached session through the task center',baseline:baseline.current,samples:points,deltaRSSBytes:points.at(-1).rssBytes-baseline.current.rssBytes,deltaHandles:points.at(-1).handles-baseline.current.handles};
  });
}
async function editCase(){
  const file=await fixture('xlsx',editRows);
  await withService('edit',async(h,m)=>{
    const p=h.page,book=(await open(p,file,editRows)).status,start=await p.api('/api/edits/start',{book:book.book}),doc=start.document;
    const key={id:doc.id,binding:doc.binding},sheet=book.sheets[0].name;
    const change=await p.api('/api/edits/commit',{document:key,operation:{revision:doc.revision,request:crypto.randomBytes(16).toString('hex'),action:'change',changes:[{sheet,row:3,col:2,value:{kind:'number',raw:'4242.5'}}]}});
    await m.sample('start');const clock=performance.now();
    const artifact=await p.api('/api/edits/save',{document:key,revision:change.document.revision});
    const saveMs=elapsed(clock),met=await m.sample();
    const address=new URL(artifact.downloadUrl,h.address);
    address.searchParams.set('session',await p.evaluate('ExcelLensSession.id'));
    const response=await fetch(address);assert.equal(response.status,200);
    const copy=path.join(temp,'edited.xlsx');fs.writeFileSync(copy,Buffer.from(await response.arrayBuffer()));
    const fidelity=JSON.parse(execFileSync(python,['-X','utf8',path.join(__dirname,'fixtures.py'),'verify',file,copy],{encoding:'utf8',windowsHide:true}));
    report.edit={rows:editRows,saveAndVerifyMs:saveMs,peakServiceRSSBytes:met.peak.rssBytes,fidelity};
  });
}
(async()=>{
 try{
   execFileSync(go,['build','-mod=vendor','-o',exe,'./tools/uiprobe'],{cwd:root,windowsHide:true,stdio:'inherit'});
   report.productionSHA256=componentHash('internal','tools/uiprobe','go.mod','go.sum','vendor');
   report.suiteSHA256=componentHash('tools/bench','tools/qa/browser_harness.cjs','tools/qa/project_config.cjs',
     'tools/qa/conditional_scroll_qa.cjs','tools/qa/conditional_scroll_fixture.cjs','tools/qa/generated_workbook.cjs',
     'internal/workbook/conditional_statistics_test.go','internal/workbook/conditional_bench_test.go');
   const scenarios={reading:readingCases,reconcile:reconcileCase,tabs:tabCases,lifecycle,edit:editCase,
     conditional:async()=>{report.conditional=await require('./conditional.cjs').measureConditional({root,temp,go,exe,meter,smoke});}};
   if(only&&!scenarios[only])throw Error('Unknown performance scenario: '+only);
   save();
   for(const [name,work] of Object.entries(scenarios)){if(!only||name===only){await work();console.log(name+' complete');}}
   for(const [name,s] of sampleFiles)assert.equal(await sha(path.join(temp,name)),s.sha256,name);
   report.sourceDigestsVerified=true;report.result='PASS';
   if(report.profile==='release'){
     report.comparison=compareBaseline(report,root,output);
     if(baselineTarget){
       const target=path.resolve(baselineTarget);fs.mkdirSync(path.dirname(target),{recursive:true});
       const clean={...report,finished:new Date().toISOString(),cleanup:report.cleanup.map(({scenario,code})=>({scenario,code}))};
       fs.writeFileSync(target+'.tmp',JSON.stringify(clean,null,2)+'\n');fs.renameSync(target+'.tmp',target);
     }
   }
 }catch(error){report.result='FAIL';report.error=error.stack;process.exitCode=1;}
 finally{
   report.finished=new Date().toISOString();save();console.log(report.result,output,report.error||'');
   if(path.dirname(temp)===path.resolve(os.tmpdir())&&path.basename(temp).startsWith('excellens-benchmark-'))fs.rmSync(temp,{recursive:true,force:true,maxRetries:3,retryDelay:200});
 }
})();
