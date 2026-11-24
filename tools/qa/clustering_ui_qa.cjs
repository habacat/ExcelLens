// Generated workbooks, real Go service and isolated headless browser.
// This is automated UI evidence, not Office/WPS or desktop acceptance.
const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict'),{createHash}=require('node:crypto');
const {zip}=require('./generated_workbook.cjs'),{launch,pause,reopenRange}=require('./browser_harness.cjs'),config=require('./project_config.cjs');
const root=path.resolve(__dirname,'../..'),base=path.join(root,'validation',config.validationVersion()),out=path.join(base,'private','clustering-ui');fs.mkdirSync(out,{recursive:true});
const ns='http://schemas.openxmlformats.org/spreadsheetml/2006/main',rel='http://schemas.openxmlformats.org/officeDocument/2006/relationships';
const xml=v=>String(v).replaceAll('&','&amp;').replaceAll('<','&lt;').replaceAll('>','&gt;').replaceAll('"','&quot;');
function sheet(rows,hidden=0){
  return '<worksheet xmlns="'+ns+'"><sheetData>'+rows.map((cells,r)=>'<row r="'+(r+1)+'"'+(hidden===r+1?' hidden="1"':'')+'>'+cells.map((v,c)=>{
    const ref=String.fromCharCode(65+c)+(r+1);
    if(typeof v==='string')return '<c r="'+ref+'" t="inlineStr"><is><t xml:space="preserve">'+xml(v)+'</t></is></c>';
    return '<c r="'+ref+'"'+(v.type?' t="'+v.type+'"':'')+'>'+(v.f?'<f>'+xml(v.f)+'</f>':'')+(v.raw===undefined?'':'<v>'+xml(v.raw)+'</v>')+'</c>';
  }).join('')+'</row>').join('')+'</sheetData></worksheet>';
}
const rows=[['机构','金额'],['华夏（上海）',{raw:'10'}],['华夏(上海)',{raw:'100'}],['华夏 上海',{raw:'20'}],['华夏（上海）',{raw:'30'}],['0007',{raw:'1'}],['0007',{raw:'2'}],[{type:'b',raw:'1'},{raw:'3'}],[{raw:'1234.5'},{raw:'4'}],['1234.50',{raw:'5'}],[{type:'e',raw:'#N/A'},{raw:'6'}],[{f:'1+1'},{raw:'7'}],[{type:'str',f:'"华夏(上海)"',raw:'华夏(上海)'},{raw:'7'}]];
for(let i=0;i<30;i++){const label='组'+String(i).padStart(2,'0');rows.push([label+'Ａ',{raw:'1'}],[label+'A',{raw:'2'}]);}
rows.push(['<img src=x onerror=alert(1)>',{raw:'1'}],['<IMG src=x onerror=alert(1)>',{raw:'2'}]);
const file=path.join(out,'候选原值.xlsx'),copy=path.join(out,'相同内容副本.xlsx'),csvFile=path.join(out,'候选清单.csv');
fs.writeFileSync(file,zip({
  'xl/workbook.xml':'<workbook xmlns="'+ns+'" xmlns:r="'+rel+'"><sheets><sheet name="候选源" sheetId="1" r:id="s1"/><sheet name="方案源" sheetId="2" r:id="s2"/></sheets></workbook>',
  'xl/_rels/workbook.xml.rels':'<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="s1" Type="'+rel+'/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="s2" Type="'+rel+'/worksheet" Target="worksheets/sheet2.xml"/></Relationships>',
  'xl/worksheets/sheet1.xml':sheet(rows,3),'xl/worksheets/sheet2.xml':sheet([['机构','金额'],['机构Ａ',{raw:'1'}],['机构A',{raw:'2'}]])
}));fs.copyFileSync(file,copy);
const hash=file=>createHash('sha256').update(fs.readFileSync(file)).digest('hex'),before=hash(file);
const report={version:config.version(),result:'FAIL',checks:[],errors:[],screenshots:[],desktopManualAcceptance:false,officeWpsAcceptance:false};let h;
const gridReady='window.ExcelLensPerformance?.().hasVisibleCoverage&&!window.ExcelLensPerformance().sheetLoading';
const ready='window.ExcelLensAnalytics?.diagnostics().opened&&window.ExcelLensAnalytics.diagnostics().fieldsReady&&!window.ExcelLensAnalytics.diagnostics().fieldsLoading&&!document.querySelector("#analysisRun").disabled';
async function choose(p,selector,value){await p.run((selector,value)=>{const node=document.querySelector(selector);node.value=value;node.dispatchEvent(new Event('change',{bubbles:true}));},selector,value);}
async function shot(p,name){const target=path.join(out,name+'.png');await p.shot(target);report.screenshots.push(path.relative(root,target).replaceAll('\\','/'));}
async function openCandidates(p){
  await p.run(()=>document.querySelector('.analysis-field-chip[data-field="C1"]').closest('.analysis-field-wrapper').querySelector('.analysis-field-info-btn').click());
  await p.wait('!!document.querySelector("[data-cluster-field=C1]")');await p.click('[data-cluster-field=C1]');await p.wait('document.querySelector("#analysisClustersDialog").open');
}
async function candidates(p){
  const previous=await p.evaluate('window.qaClusters.starts.length');
  await p.click('#analysisClustersRun');
  await p.wait('window.qaClusters.starts.length>'+previous+'&&!document.querySelector("#analysisClustersRun").disabled&&!!document.querySelector("#analysisClusterVariants .analysis-cluster-variant")');
  return await p.evaluate('window.qaClusters.starts.at(-1).value.jobId');
}
async function selectKey(p,key){
  await p.run(key=>{const button=[...document.querySelectorAll('.analysis-cluster-group')].find(n=>n.querySelector('strong').textContent===key);if(!button)throw Error('Missing candidate '+key);button.click();},key);
  await p.wait('document.querySelector("#analysisClusterDetailKey").textContent==='+JSON.stringify(key)+'&&!!document.querySelector("#analysisClusterVariants .analysis-cluster-variant input:not(:disabled)")');
}
async function representative(p,raw){
  await p.run(raw=>{const row=[...document.querySelectorAll('.analysis-cluster-variant')].find(n=>n.querySelector('pre').textContent===raw);if(!row)throw Error('Missing raw representative');row.querySelector('input').click();},raw);
  await p.click('#analysisClustersConfirm');await p.wait('document.querySelector("#analysisClustersStatus").textContent.includes("已添加独立文本映射列")');
  return await p.evaluate('window.qaClusters.confirmed.at(-1).transform');
}
async function assign(p,id,zone){await p.run((id,zone)=>{const data=new DataTransfer();data.setData('text/plain',id);document.querySelector('[data-analysis-zone="'+zone+'"]').dispatchEvent(new DragEvent('drop',{bubbles:true,dataTransfer:data}));},id,zone);}
async function run(p){const previous=await p.evaluate('window.ExcelLensAnalytics.diagnostics().resultJob');await p.click('#analysisRun');await p.wait(ready+'&&window.ExcelLensAnalytics.diagnostics().resultJob!=='+JSON.stringify(previous));return await p.api('/api/analysis/query/status?id='+await p.evaluate('window.ExcelLensAnalytics.diagnostics().resultJob'));}
async function save(p,title){await p.click('#analysisSaveReport');await p.wait('!!document.querySelector(".ask-dialog[open] input")');await p.run(title=>document.querySelector('.ask-dialog[open] input').value=title,title);await p.click('.ask-dialog[open] [data-yes]');for(let i=0;i<80;i++){const ws=await p.api('/api/analysis/workspace'),item=ws.reports.find(r=>r.name===title);if(item)return item;await pause(50);}throw Error('Saved report did not appear');}
(async()=>{try{
  h=await launch({out,errors:report.errors,duration:'18m'});const p=h.page;
  report.assetSHA256={};
  for(const name of ['analysis-clusters.js','analysis-clusters.css','analytics.js','analysis-jobs.js','app.js']){
    const served=await p.run(async name=>await(await fetch('/assets/'+name)).text(),name),digest=createHash('sha256').update(served).digest('hex');
    assert.equal(digest,hash(path.join(root,'internal/ui/web',name)));report.assetSHA256[name]=digest;
  }
  await p.file('#fileInput',file);await p.wait(gridReady);
  await p.run(()=>{window.qaClusters={starts:[],confirmed:[],held:[],hold:false,failPage:false,alerts:[]};window.alert=s=>window.qaClusters.alerts.push(s);const original=fetch.bind(window);window.fetch=async(url,options)=>{
    const route=String(url),q=window.qaClusters;
    if(q.failPage&&route.startsWith('/api/analysis/clusters/groups?')){q.failPage=false;return new Response(JSON.stringify({error:'模拟首屏读取失败'}),{status:503,headers:{'Content-Type':'application/json'}});}
    const response=await original(url,options);
    if(route==='/api/analysis/clusters/start'){q.starts.push({request:JSON.parse(options.body),value:await response.clone().json()});if(q.hold){q.hold=false;return new Promise(resolve=>q.held.push(()=>resolve(response)));}}
    if(route==='/api/analysis/clusters/confirm'&&response.ok)q.confirmed.push(await response.clone().json());
    return response;
  };});
  await p.click('#analysisOpenBtn');await p.wait(ready);await choose(p,'#analysisSourceScope','visible');await p.wait(ready);await openCandidates(p);
  assert.equal(await p.evaluate('window.qaClusters.starts.length'),0);
  await p.click('#analysisClustersCase');await p.click('#analysisClustersWidth');await p.click('#analysisClustersPunctuation');await choose(p,'#analysisClustersSpace','remove');
  const job=await candidates(p),page=await p.api('/api/analysis/clusters/groups?id='+job);
  assert.equal(page.total,33);assert.equal(page.counts.similarGroups,32);assert.equal(page.counts.exactGroups,1);assert.equal(page.counts.missingCache,1);assert.equal(page.counts.numeric,1);
  await selectKey(p,'华夏上海');
  const originalDetail=await p.evaluate('document.querySelector("#analysisClusterVariants").textContent');
  assert.match(originalDetail,/华夏（上海）/);assert.match(originalDetail,/公式缓存/);assert(!await p.evaluate('[...document.querySelectorAll("#analysisClusterVariants .analysis-drill-link")].some(n=>n.textContent==="A3")'));
  assert.equal(await p.evaluate('document.querySelector("#analysisClustersConfirm").disabled'),true);await shot(p,'clustering-desktop');
  const mapped=await representative(p,'华夏（上海）');assert.equal(mapped.mapping.length,2);assert(mapped.mapping.some(m=>m.from==='华夏 上海'));
  assert.equal(await p.evaluate('document.querySelector("#analysisClustersConfirm").disabled'),true);
  await p.click('#analysisClustersNext');await p.wait('document.querySelector("#analysisClustersPage").textContent==="26–33 / 33"');await p.click('#analysisClustersPrev');await p.wait('document.querySelector("#analysisClustersPage").textContent==="1–25 / 33"');
  await p.call('Emulation.setDeviceMetricsOverride',{width:420,height:860,deviceScaleFactor:1,mobile:false});await selectKey(p,'华夏上海');
  await shot(p,'clustering-420');
  assert(await p.run(()=>{const dialog=document.querySelector('#analysisClustersDialog').getBoundingClientRect(),ids=['analysisClustersRun','analysisClustersConfirm','analysisClustersNext','analysisClusterVariantsNext'];return dialog.left>=0&&dialog.right<=innerWidth&&ids.every(id=>{const r=document.getElementById(id).getBoundingClientRect();return r.left>=0&&r.right<=innerWidth&&r.top>=0&&r.bottom<=innerHeight;});}));
  await p.run(()=>{document.documentElement.setAttribute('data-reading-preset','graphite');document.documentElement.setAttribute('data-reading-tone','dark');});await shot(p,'clustering-dark-420');
  report.darkContrasts=await p.run(()=>{
    const rgb=text=>text.match(/[\d.]+/g).slice(0,3).map(Number);
    const luminance=color=>rgb(color).map(n=>{n/=255;return n<=0.04045?n/12.92:((n+0.055)/1.055)**2.4;}).reduce((a,n,i)=>a+n*[0.2126,0.7152,0.0722][i],0);
    return ['#analysisClustersRun','.analysis-cluster-group[aria-pressed=true]','#analysisClusterVariants .analysis-drill-link'].map(selector=>{
      const node=document.querySelector(selector),color=getComputedStyle(node).color;let background='rgba(0,0,0,0)';
      for(let parent=node;parent;parent=parent.parentElement){background=getComputedStyle(parent).backgroundColor;if(background!=='rgba(0, 0, 0, 0)'&&background!=='transparent')break;}
      const a=luminance(color),b=luminance(background);return {selector,foreground:color,background,ratio:(Math.max(a,b)+0.05)/(Math.min(a,b)+0.05)};
    });
  });
  assert(report.darkContrasts.every(item=>item.ratio>=4.5),JSON.stringify(report.darkContrasts));
  await p.run(()=>{document.documentElement.removeAttribute('data-reading-preset');document.documentElement.removeAttribute('data-reading-tone');});
  await p.call('Emulation.setDeviceMetricsOverride',{width:1600,height:1050,deviceScaleFactor:1,mobile:false});
  report.checks.push('actual field profile entry performs no implicit candidate scan; visible-source candidates preserve Chinese raw values, saved formula text types and hidden-row exclusion; explicit representative adds an exact map only once; 25-group paging and 420px light/dark controls remain reachable');
  await p.run(()=>window.qaClusters.failPage=true);await p.click('#analysisClustersRun');await p.wait('document.querySelector("#analysisClustersStatus").textContent.includes("模拟首屏读取失败")');
  assert.equal(await p.evaluate('document.querySelector("#analysisClusterDetailKey").textContent'),'华夏上海');
  await p.run(()=>window.qaClusters.hold=true);await p.click('#analysisClustersRun');await p.wait('window.qaClusters.held.length===1');await p.click('#analysisClustersCancel');const retry=await candidates(p);
  await p.run(()=>window.qaClusters.held.shift()());await pause(80);
  assert.equal((await p.api('/api/analysis/clusters/groups?id='+retry)).total,33);
  await p.run(()=>document.querySelector('#analysisClustersCSVGuard').checked=false);await p.click('#analysisClustersExport');
  await p.wait('document.querySelector("#exportDialog").open&&!document.querySelector("#exportResult").hidden&&!!document.querySelector("#exportDownload").getAttribute("href")');
  const bytes=await p.run(async()=>Array.from(new Uint8Array(await(await fetch(document.querySelector('#exportDownload').href)).arrayBuffer())));
  fs.writeFileSync(csvFile,Buffer.from(bytes));await p.click('#exportDialog .dialog-close');
  const reopened=await reopenRange(h,csvFile,'A1','L75');
  const cells=reopened.range.cells||reopened.range.rows?.flatMap(r=>r.cells)||[];
  assert(cells.some(c=>c.raw==='0007'));assert(cells.some(c=>c.raw==='华夏 上海'));assert.equal(await p.evaluate('window.qaClusters.alerts.length'),0);
  report.checks.push('failed first-page refresh retains the previous accepted result; a cancelled held start cannot replace an immediate retry; real candidate CSV download reopens through the actual adapter with leading-zero and Chinese raw text intact and no source script execution');
  await assign(p,mapped.id,'rows');await assign(p,'C2','values');await choose(p,'#analysisTop','0');const analyzed=await run(p);
  assert.equal(analyzed.result.rows.find(r=>r.keys[0]==='华夏（上海）').total.exact,'67');
  const saved=await save(p,'机构文字核对'),backup=await p.api('/api/analysis/backup');assert.equal(backup.workspace.version,5);assert.equal(saved.query.transforms[0].mapping.length,2);
  await p.click('[data-analysis-tab="dashboard"]');await p.wait('!!document.querySelector('+JSON.stringify('.analysis-dashboard-card[data-report-id="'+saved.id+'"]')+')');await p.click('.analysis-dashboard-card[data-report-id="'+saved.id+'"] [data-report-open]');await p.wait(ready);
  assert.match(await p.evaluate('document.querySelector("#analysisTransformList").textContent'),/精确文本映射/);
  const restored=await p.api('/api/analysis/query/status?id='+await p.evaluate('window.ExcelLensAnalytics.diagnostics().resultJob'));assert.equal(restored.query.transforms[0].mapping.length,2);
  await choose(p,'#analysisSourceSelect','wb:方案源');await p.wait(ready);await openCandidates(p);await candidates(p);await selectKey(p,'机构A');const cleanMap=await representative(p,'机构A');await p.click('#analysisClustersClose');
  await assign(p,cleanMap.id,'rows');await assign(p,'C2','values');const clean=await run(p);assert.equal(clean.result.rows.length,1);assert.equal(clean.result.grand.exact,'3');
  await p.click('#analysisPlansOpen');await p.wait('!document.querySelector("#analysisPlanSave").disabled');await p.run(()=>document.querySelector('#analysisPlanName').value='文字映射方案');await p.click('#analysisPlanSave');await p.wait('document.querySelector("#analysisPlanStatus").textContent==="分析方案已保存到本机"');
  const plan=(await p.api('/api/recipes')).store.items.find(i=>i.name==='文字映射方案');assert.equal(plan.version,4);await choose(p,'#analysisPlanList',plan.id);await p.click('#analysisPlanPreview');await p.wait('!document.querySelector("#analysisPlanRun").disabled');await p.click('#analysisPlanRun');await p.wait('!document.querySelector("#analysisPlanDialog").open&&'+ready);
  const replay=await p.api('/api/analysis/query/status?id='+await p.evaluate('window.ExcelLensAnalytics.diagnostics().resultJob'));assert.equal(replay.query.transforms[0].mapping.length,1);assert.equal(replay.result.rows.length,1);await shot(p,'clustering-map-report');
  report.checks.push('confirmed text mapping is used by the real analysis engine, combines the visible source into exact 67, saves and reopens a version-5 workspace, and captures/previews/replays a version-4 plan through actual controls with all mapping entries retained');
  await p.click('#analysisClose');await p.wait(gridReady);const oldBook=await p.api('/api/status');await p.file('#fileInput',copy);await p.wait(gridReady+'&&document.querySelector("#fileBadge").textContent.includes("相同内容副本")');const newBook=await p.api('/api/status');assert.equal(oldBook.workbookKey,newBook.workbookKey);assert.notEqual(oldBook.book,newBook.book);
  let refused=false;try{await p.api('/api/analysis/clusters/groups?id='+job);}catch{refused=true;}assert(refused);
  assert.equal(hash(file),before);assert.equal(hash(copy),before);assert.equal(report.errors.length,0);report.sourceSHA256=before;report.sourceUnchanged=true;report.result='PASS';
}catch(error){report.errors.push(String(error.stack||error));if(h)try{report.failure=await h.page.run(()=>({cluster:document.querySelector('#analysisClustersStatus')?.textContent,summary:document.querySelector('#analysisClustersSummary')?.textContent,plan:document.querySelector('#analysisPlanStatus')?.textContent,toast:document.querySelector('#analysisToast')?.textContent,diagnostics:window.ExcelLensAnalytics?.diagnostics()}));await shot(h.page,'clustering-failure');}catch{}}
finally{if(h)await h.close();fs.writeFileSync(path.join(base,'clustering-ui-browser.json'),JSON.stringify(report,null,2)+'\n');console.log(JSON.stringify(report,null,2));if(report.result!=='PASS')process.exitCode=1;}})();
