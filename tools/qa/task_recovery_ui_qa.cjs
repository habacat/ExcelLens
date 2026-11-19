// Real app + isolated headless Chrome. Synthetic sources, actual restart and
// actual downloads. Never connects to the user's running application/profile.
const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict'),{createHash}=require('node:crypto'),{inflateRawSync}=require('node:zlib');
const {launch,pause}=require('./browser_harness.cjs'),config=require('./project_config.cjs');
const root=path.resolve(__dirname,'../..'),base=path.join(root,'validation',config.validationVersion()),out=path.join(base,'private','task-recovery-ui');fs.mkdirSync(out,{recursive:true});
const source=path.join(out,'恢复原始账簿.csv'),copy=path.join(out,'同内容副本.csv'),rows=['编号,金额,分组'];
for(let row=1;row<=12000;row++)rows.push(`${String(row).padStart(9,'0')},9007199254740993.${String(row%17).padStart(2,'0')},${row%7===0?'drop':'keep'}`);
fs.writeFileSync(source,rows.join('\n')+'\n');fs.copyFileSync(source,copy);rows.length=0;
const digest=bytes=>createHash('sha256').update(bytes).digest('hex'),before=digest(fs.readFileSync(source));
const report={version:config.version(),result:'FAIL',desktopManualAcceptance:false,officeWpsAcceptance:false,checks:[],screenshots:[],downloads:[],errors:[]};let h,stage='bootstrap';
const assetsOnly=process.argv.includes('--assets-only');
const ready='window.ExcelLensPerformance?.().hasVisibleCoverage&&!window.ExcelLensPerformance().sheetLoading';
async function set(p,selector,value){await p.run((selector,value)=>{const node=document.querySelector(selector);if(typeof value==='boolean')node.checked=value;else node.value=value;node.dispatchEvent(new Event('change',{bubbles:true}));},selector,value);}
async function close(p,id){await p.run(id=>document.querySelector(id).close(),id);}
async function shot(p,name){const file=path.join(out,name+'.png');await p.evaluate('Promise.all(document.getAnimations().filter(a=>Number.isFinite(a.effect?.getComputedTiming().endTime)).map(a=>a.finished.catch(()=>{})))');await p.shot(file);report.screenshots.push(path.relative(root,file).replaceAll('\\','/'));}
async function upload(p,file){await p.file('#fileInput',file);await p.wait(ready+'&&document.querySelector("#fileBadge").textContent==='+JSON.stringify(path.basename(file)));}
async function catalog(p){return (await p.api('/api/tasks')).items;}
async function openTasks(p){
  if(await p.evaluate('!document.querySelector("#welcome").hidden')){
    await p.wait('document.querySelector(".recent-heading #taskRecoveryHome").getClientRects().length>0');
    await p.click('#taskRecoveryHome');
  }else{
    await p.click('#commandOpenBtn');
    await p.run(()=>{const field=document.querySelector('#commandSearch');field.value='任务恢复';field.dispatchEvent(new Event('input',{bubbles:true}));});
    await p.click('#commandList .command-entry');
  }
  await p.wait('document.querySelector("#taskRecoveryDialog").open&&document.querySelector("#taskRecoveryStatus").hidden');
}
function unzip(data){
  let end=data.length-22;while(end>=Math.max(0,data.length-65557)&&data.readUInt32LE(end)!==0x06054b50)end--;assert(end>=0,'missing zip directory');
  const count=data.readUInt16LE(end+10),result={};let offset=data.readUInt32LE(end+16);
  for(let i=0;i<count;i++){
    assert.equal(data.readUInt32LE(offset),0x02014b50);const method=data.readUInt16LE(offset+10),compressed=data.readUInt32LE(offset+20),size=data.readUInt32LE(offset+24),n=data.readUInt16LE(offset+28),extra=data.readUInt16LE(offset+30),comment=data.readUInt16LE(offset+32),local=data.readUInt32LE(offset+42),name=data.subarray(offset+46,offset+46+n).toString('utf8');
    const start=local+30+data.readUInt16LE(local+26)+data.readUInt16LE(local+28),payload=data.subarray(start,start+compressed),body=method===8?inflateRawSync(payload):payload;assert.equal(body.length,size);result[name]=digest(body);offset+=46+n+extra+comment;
  }
  return result;
}
async function download(p,name){
  const folder=fs.mkdtempSync(path.join(out,name+'-'));
  const link=await p.run(()=>{const a=document.querySelector('#exportDownload');return {id:new URL(a.href).searchParams.get('id'),name:a.download};});
  const status=await p.api('/api/export/status?id='+encodeURIComponent(link.id));
  assert.equal(status.status,'done');assert.equal(link.name,status.fileName);assert.equal(path.basename(link.name),link.name);
  const file=path.join(folder,link.name);
  await p.call('Browser.setDownloadBehavior',{behavior:'allow',downloadPath:folder});await p.click('#exportDownload');
  for(let n=0;n<400;n++){
    if(fs.existsSync(file)&&fs.statSync(file).size===status.size)return {file,data:fs.readFileSync(file)};
    await pause(50);
  }
  throw Error('Download timeout '+name);
}
async function monitor(p){await p.run(()=>{
  const original=window.fetch.bind(window);window.__taskQA={calls:[],starts:[],lastFilter:null,holds:[],holdResume:false};
  window.fetch=async(url,options)=>{
    const name=String(url);window.__taskQA.calls.push(name);if(window.__taskQA.calls.length>300)window.__taskQA.calls.shift();
    const response=await original(url,options);
    if(['/api/search/start','/api/filter/start','/api/export/start','/api/tasks/resume'].includes(name)){
      const data=await response.clone().json();window.__taskQA.starts.push({url:name,request:JSON.parse(options.body),data});
      if(name==='/api/tasks/resume'&&window.__taskQA.holdResume){window.__taskQA.holdResume=false;return new Promise(resolve=>window.__taskQA.holds.push(()=>resolve(response)));}
    }
    if(name.startsWith('/api/filter/status')){const data=await response.clone().json();if(data.status==='done')window.__taskQA.lastFilter=data;}
    return response;
  };
});}
async function taskAction(p,id,action){await p.click(`[data-task-id="${id}"] [data-action="${action}"]`);}
async function exportReady(p){await p.wait('document.querySelector("#exportDialog").open&&!document.querySelector("#exportResult").hidden&&!document.querySelector("#exportDownload").getAttribute("href").startsWith("#")');}
(async()=>{
try{
  h=await launch({out,errors:report.errors,duration:'12m'});const p=h.page;await p.evaluate('window.ExcelLensWorkbookReady');
  if(assetsOnly){
    report.assets=[];
    for(const name of ['task-recovery-model.js','task-recovery.js','task-recovery.css','app.js','export.js','autofilter.js','formats.js','display-format.js']){
      const response=await fetch(new URL('/assets/'+name,h.address));assert(response.ok);const served=Buffer.from(await response.arrayBuffer()),sha=digest(served);assert.equal(sha,digest(fs.readFileSync(path.join(root,'internal/ui/web',name))));report.assets.push({path:'internal/ui/web/'+name,sha256:sha});
    }
    const text=await(await fetch(h.address)).text(),configuration=JSON.parse(text.match(/<script id="fileFormats" type="application\/json">([^<]+)<\/script>/)[1]);
    const limits=fs.readFileSync(path.join(root,'internal/formats/checkpoint.go'),'utf8');assert.equal(configuration.taskRecovery.tasks,Number(limits.match(/CheckpointTasks\s+=\s+(\d+)/)[1]));assert.equal((await p.api('/api/status')).version,config.version());assert.deepEqual(report.errors,[]);
    report.checks.push('eight served production assets match current disk SHA256; bootstrap version and injected checkpoint capacity match their single Go sources');report.result='PASS';return;
  }
  await monitor(p);
  await p.wait('performance.getEntriesByType("resource").some(entry=>new URL(entry.name).pathname==="/api/tasks")');
  assert.equal((await p.api('/api/status')).opened,false);assert.deepEqual(await catalog(p),[]);
  assert.ok(await p.evaluate('document.querySelector("#taskRecoveryHome").hidden && !document.querySelector("#editCopyHome") && !document.querySelector("#taskRecoveryDialog").open'));
  report.checks.push('empty home has no task or edit action and does not open a source or start a job');
  stage='opt-in query';await upload(p,source);const originalStatus=await p.api('/api/status'),sheet=originalStatus.sheets[0].name;
  await p.click('#searchOpenBtn');await set(p,'#searchInput','000001000');await p.click('#searchBtn');await p.wait('!document.querySelector("#searchBtn").disabled');assert.equal((await catalog(p)).length,0);
  await set(p,'#searchRecoverable',true);await p.click('#searchBtn');await p.wait('!document.querySelector("#searchBtn").disabled');
  let entries=await catalog(p);const search=entries.find(x=>x.driver==='source-search');assert(search?.complete);assert.equal(search.file,path.basename(source));assert.equal(search.query,'000001000');
  assert.equal(await p.evaluate('window.__taskQA.starts.at(-1).request.recoverable'),true);await close(p,'#searchDialog');
  report.checks.push('default search leaves no persistent task; explicit opt-in bypasses same-query browser cache and keeps original uploaded name');
  stage='filter and sort';if(await p.evaluate('document.querySelector("#filterOpenBtn").getAttribute("aria-pressed")!=="true"'))await p.click('#filterOpenBtn');await p.click('#advancedFilterBtn');await set(p,'.filter-column','C');await set(p,'.filter-operator','equals');await set(p,'.filter-value','keep');await set(p,'#filterRecoverable',true);await p.click('#applyFilterBtn');await p.wait('!document.querySelector("#applyFilterBtn").disabled&&!!window.__taskQA.lastFilter');
  const filter=(await catalog(p)).find(x=>x.driver==='source-filter');assert(filter?.complete);const filterStatus=await p.evaluate('window.__taskQA.lastFilter');assert.equal(filterStatus.matchedRows,10287);
  await close(p,'#filterDialog');await p.click('#multiSortBtn');await set(p,'#sortHeaderRow','1');await set(p,'.sort-column','B');await set(p,'.sort-mode','number');await set(p,'.sort-direction','desc');await set(p,'#sortRecoverable',true);await p.run(()=>window.__taskQA.lastFilter=null);await p.click('#sortApply');await p.wait('!!window.__taskQA.lastFilter&&!document.querySelector("#applyFilterBtn").disabled');
  const sort=(await catalog(p)).filter(x=>x.driver==='source-filter').find(x=>x.id!==filter.id);assert(sort?.complete);
  const sorted=await p.evaluate('window.__taskQA.lastFilter');assert.equal(sorted.matchedRows,10287);
  const range=await p.api('/api/range?'+new URLSearchParams({sheet,start:'A17',end:'C51',visualStart:'2',visualEnd:'4',excludeThroughRow:'0',filterId:sorted.filterId,format:'json'}));assert.deepEqual(range.rows.map(x=>x.row),[17,34,51]);report.sortedRangeHash=digest(Buffer.from(JSON.stringify(range)));
  report.checks.push('explicit advanced filtering and exact numeric multi-sort persist their original predicates and header; 10286 matching source records');
  stage='six output formats';const outputs=[];
  for(const format of ['csv','tsv','jsonl','html','xlsx','zipcsv']){
    await p.click('#exportOpenBtn');await set(p,'#exportScope',format==='zipcsv'?'all':'sheet');await set(p,'#exportFormat',format);await set(p,'#exportMode','values');await set(p,'#exportRecoverable',true);await p.click('#exportStartBtn');await exportReady(p);
    const started=await p.evaluate('window.__taskQA.starts.filter(x=>x.url==="/api/export/start").at(-1)');assert.equal(started.request.recoverable,true);assert.equal(started.request.book,originalStatus.book);
    const completed=await p.api('/api/export/status?id='+encodeURIComponent(started.data.jobId));assert(completed.recoverable);const item=(await catalog(p)).find(x=>x.id===completed.taskId);assert(item.complete);const saved=await download(p,'before-'+format);
    if(['xlsx','zipcsv'].includes(format))assert(Object.keys(unzip(saved.data)).length>0);else{assert(saved.data.includes(Buffer.from('000000001')));assert(saved.data.includes(Buffer.from('9007199254740993.01')));}
    outputs.push({format,id:item.id,saved});await close(p,'#exportDialog');
  }
  assert.equal((await catalog(p)).length,9);
  report.checks.push('all six original export formats opt in through the existing export center and download real files with source leading-zero IDs and exact amount text');
  stage='real restart and catalog';const address=await h.restart();await p.call('Page.navigate',{url:address});await p.wait('document.readyState==="complete"&&!!window.ExcelLensWorkbookReady');await p.evaluate('window.ExcelLensWorkbookReady');await monitor(p);assert.equal((await p.api('/api/status')).opened,false);
  await p.wait('document.querySelector(".recent-heading #taskRecoveryHome").getClientRects().length>0');
  await p.call('Emulation.setDeviceMetricsOverride',{width:420,height:780,deviceScaleFactor:1,mobile:false});
  assert.ok(await p.run(()=>{const button=document.querySelector('#taskRecoveryHome').getBoundingClientRect(),header=document.querySelector('.recent-heading').getBoundingClientRect();return button.width>0&&button.left>=header.left&&button.right<=header.right&&header.right<=innerWidth;}));
  await shot(p,'home-recovery-420px');
  await p.call('Emulation.setDeviceMetricsOverride',{width:1600,height:1050,deviceScaleFactor:1,mobile:false});
  await openTasks(p);assert.equal((await catalog(p)).length,9);assert.equal((await p.api('/api/status')).opened,false);
  await shot(p,'tasks-desktop');await p.call('Emulation.setDeviceMetricsOverride',{width:420,height:780,deviceScaleFactor:1,mobile:false});
  const layout=()=>{const dialog=document.querySelector('#taskRecoveryDialog'),list=document.querySelector('#taskRecoveryList'),rect=dialog.getBoundingClientRect();return {left:rect.left,right:rect.right,bottom:rect.bottom,width:innerWidth,height:innerHeight,scroll:list.scrollHeight>list.clientHeight,dialogOverflow:dialog.scrollHeight>dialog.clientHeight+1,listOverflow:list.scrollWidth>list.clientWidth+1,buttons:[...dialog.querySelectorAll('button')].every(x=>{const b=x.getBoundingClientRect();return b.left>=rect.left&&b.right<=rect.right;})};};
  const narrow=await p.run(layout);assert(narrow.left>=0&&narrow.right<=420&&narrow.bottom<=780&&narrow.scroll&&!narrow.dialogOverflow&&!narrow.listOverflow&&narrow.buttons);await shot(p,'tasks-420px');
  await p.run(()=>{document.documentElement.setAttribute('data-reading-preset','graphite');document.documentElement.setAttribute('data-reading-tone','dark');});await shot(p,'tasks-dark-420px');assert.equal(await p.evaluate('getComputedStyle(document.querySelector("#taskRecoveryDialog")).backgroundColor'),'rgb(46, 51, 58)');
  await p.run(()=>{document.documentElement.removeAttribute('data-reading-preset');document.documentElement.removeAttribute('data-reading-tone');});await p.call('Emulation.setDeviceMetricsOverride',{width:1600,height:1050,deviceScaleFactor:1,mobile:false});
  report.checks.push('actual server restart retains nine tasks, opens no source automatically, and compact list fits desktop / 420px / dark theme with one scroll container');
  stage='source reopening';await taskAction(p,outputs[0].id,'open');await p.wait(ready+'&&document.querySelector("#taskRecoveryStatus").hidden');const reopened=await p.api('/api/status');assert.equal(reopened.fileName,path.basename(source));assert.equal(reopened.book,originalStatus.book);
  assert.equal(await p.evaluate('window.__taskQA.starts.length'),0);
  report.checks.push('explicit Open original file validates then installs the original uploaded copy without starting any query/export');
  stage='restored downloads';
  for(const output of outputs){
    if(!await p.evaluate('document.querySelector("#taskRecoveryDialog").open'))await openTasks(p);
    await taskAction(p,output.id,'download');await exportReady(p);const recovered=await download(p,'after-'+output.format);
    if(['xlsx','zipcsv'].includes(output.format))assert.deepEqual(unzip(recovered.data),unzip(output.saved.data));else assert.deepEqual(recovered.data,output.saved.data);
    report.downloads.push({format:output.format,beforeSHA256:digest(output.saved.data),afterSHA256:digest(recovered.data),verified:output.format==='xlsx'||output.format==='zipcsv'?'all decompressed entries equal':'all bytes equal',bytes:recovered.data.length});await close(p,'#exportDialog');
  }
  report.checks.push('after real restart all six tasks download again; complete text bytes or every decompressed container part independently match the original download');
  stage='restored search/filter/sort';await openTasks(p);await taskAction(p,search.id,'resume');await p.wait('document.querySelector("#searchDialog").open&&!document.querySelector("#searchBtn").disabled');assert.match(await p.evaluate('document.querySelector("#searchSummary").textContent'),/1 条匹配/);assert.equal(await p.evaluate('document.querySelector("#searchInput").value'),'000001000');await close(p,'#searchDialog');
  await openTasks(p);await p.run(()=>window.__taskQA.lastFilter=null);await taskAction(p,sort.id,'resume');await p.wait('document.querySelector("#filterDialog").open&&!document.querySelector("#applyFilterBtn").disabled&&!!window.__taskQA.lastFilter');
  const restored=await p.evaluate('window.__taskQA.lastFilter');assert.equal(restored.matchedRows,10287);
  const afterRange=await p.api('/api/range?'+new URLSearchParams({sheet,start:'A17',end:'C51',visualStart:'2',visualEnd:'4',excludeThroughRow:'0',filterId:restored.filterId,format:'json'}));assert.equal(digest(Buffer.from(JSON.stringify(afterRange))),report.sortedRangeHash);
  await shot(p,'restored-filter');await close(p,'#filterDialog');await p.click('#multiSortBtn');assert.equal(await p.evaluate('document.querySelector(".sort-mode").value'),'number');assert.equal(await p.evaluate('document.querySelector(".sort-direction").value'),'desc');await close(p,'#multiSortDialog');
  report.checks.push('resumed search uses the existing search panel; restored filtering/sorting commits the same exact source order and restores visible sort settings');
  stage='late response ownership';await openTasks(p);await p.run(()=>window.__taskQA.holdResume=true);await taskAction(p,search.id,'resume');await p.wait('window.__taskQA.holds.length===1');await p.click('#taskRecoveryClose');await p.run(()=>window.__taskQA.holds.shift()());await p.wait('window.__taskQA.calls.includes("/api/search/cancel")');assert.equal(await p.evaluate('document.querySelector("#searchDialog").open'),false);
  report.checks.push('a deliberately delayed accepted resume after closing the task window cancels only that returned job and cannot reopen results');
  stage='wrong-path and changed source';await upload(p,copy);const copyStatus=await p.api('/api/status');assert.notEqual(copyStatus.book,originalStatus.book);await openTasks(p);assert.equal(await p.evaluate(`!!document.querySelector('[data-task-id="${search.id}"] [data-action="open"]')`),true);
  // Locate only this harness's uploaded original by its recent-record path.
  const recent=await p.api('/api/recent'),originalRecent=recent.items.find(x=>x.name===path.basename(source));assert(originalRecent?.path&&path.resolve(originalRecent.path).toLowerCase().startsWith((path.resolve(h.storage)+path.sep).toLowerCase()));
  const originalUpload=fs.readFileSync(originalRecent.path);fs.writeFileSync(originalRecent.path,'changed,source\n1,2\n');await taskAction(p,search.id,'open');await p.wait('document.querySelector("#taskRecoveryStatus").classList.contains("is-error")');assert.equal((await p.api('/api/status')).book,copyStatus.book);fs.writeFileSync(originalRecent.path,originalUpload);
  await shot(p,'source-changed');await taskAction(p,search.id,'remove');await p.wait(`!document.querySelector('[data-task-id="${search.id}"]')`);assert.equal((await p.api('/api/status')).book,copyStatus.book);assert(fs.existsSync(originalRecent.path));assert.equal((await catalog(p)).length,8);
  report.checks.push('same bytes at another path require Open original; changed original is rejected while current grid survives; removing one task leaves both source files and other tasks intact');
  stage='home resume and whitelist';await p.click('#taskRecoveryClose');
  const whitelist=await p.api('/api/filter/start',{sheet,workbookKey:copyStatus.workbookKey,book:copyStatus.book,recoverable:true,headerRow:1,rules:[{column:3,operator:'in',values:['keep']}]});
  let whitelistStatus;for(let n=0;n<200;n++){whitelistStatus=await p.api('/api/filter/status?id='+whitelist.jobId);if(['done','error','cancelled'].includes(whitelistStatus.status))break;await pause(30);}assert.equal(whitelistStatus.status,'done');
  await p.click('#homeBtn');assert.equal(await p.evaluate('document.body.classList.contains("home-view")'),true);await openTasks(p);await p.run(()=>window.__taskQA.lastFilter=null);await taskAction(p,whitelist.taskId,'resume');
  await p.wait('document.querySelector("#filterDialog").open&&!document.querySelector("#cancelFilterJobBtn").hidden||!!window.__taskQA.lastFilter');
  await p.wait('!!window.__taskQA.lastFilter&&document.querySelector("#cancelFilterJobBtn").hidden');
  assert.equal(await p.evaluate('document.body.classList.contains("home-view")'),false);assert.equal(await p.evaluate('document.querySelector("#filterRules").hidden&&document.querySelector("#applyFilterBtn").hidden'),true);assert.equal((await p.evaluate('window.__taskQA.lastFilter')).matchedRows,10287);
  await close(p,'#filterDialog');await p.click('#advancedFilterBtn');assert.equal(await p.evaluate('document.querySelector("#filterRules").hidden||document.querySelector("#applyFilterBtn").hidden'),false);await close(p,'#filterDialog');
  report.checks.push('resume from home reveals the workspace; recovered multi-value predicates remain exact and use progress/cancel controls; a newly opened advanced query restores its normal editor');
  stage='last recoverable task';await p.click('#homeBtn');await openTasks(p);
  for(const item of await catalog(p)){
    await taskAction(p,item.id,'remove');await p.wait(`!document.querySelector('[data-task-id="${item.id}"]')`);
  }
  await p.click('#taskRecoveryClose');await p.wait('document.querySelector("#taskRecoveryHome").hidden');
  assert.deepEqual(await catalog(p),[]);
  report.checks.push('discarding the last usable checkpoint hides the home action immediately');
  assert.equal(digest(fs.readFileSync(source)),before);assert.equal(digest(fs.readFileSync(copy)),before);assert.deepEqual(report.errors,[]);
  report.sourceSHA256=before;report.sourceUnchanged=true;report.result='PASS';
}catch(error){report.failure=error.stack||String(error);report.failedStage=stage;if(h){try{report.diagnostic=await h.page.run(()=>({file:document.querySelector('#fileBadge')?.textContent,status:document.querySelector('#taskRecoveryStatus')?.textContent,search:document.querySelector('#searchSummary')?.textContent,filter:document.querySelector('#filterProgressText')?.textContent,export:document.querySelector('#exportStage')?.textContent,starts:window.__taskQA?.starts.slice(-3),calls:window.__taskQA?.calls.slice(-15)}));await h.page.shot(path.join(out,'failure.png'));}catch(e){report.diagnosticError=e.message;}}process.exitCode=1;
}finally{if(h)await h.close();fs.writeFileSync(path.join(base,assetsOnly?'task-recovery-ui-assets.json':'task-recovery-ui-browser.json'),JSON.stringify(report,null,2)+'\n');console.log(JSON.stringify(report,null,2));}
})();
