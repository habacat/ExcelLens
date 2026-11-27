// Real controls/service with generated data and isolated user storage. This is
// automated browser evidence, not desktop or Office/WPS manual acceptance.
const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict'),{createHash}=require('node:crypto');
const {zip}=require('./generated_workbook.cjs'),{launch,reopenRange}=require('./browser_harness.cjs'),config=require('./project_config.cjs');
const root=path.resolve(__dirname,'../..'),out=path.join(root,'validation',config.validationVersion(),'private','analysis-numbers-ui');fs.mkdirSync(out,{recursive:true});
const ns='http://schemas.openxmlformats.org/spreadsheetml/2006/main',rel='http://schemas.openxmlformats.org/officeDocument/2006/relationships';
const file=path.join(out,'数字显示.xlsx'),sum='9007200490542216.82',average='450360024527110841/300';
const rows=[['机构','团队','金额','编号'],['甲','001',{n:'9007199254740993.31'},'001'],['甲','002',{n:'1234567.89'},'002'],['乙','003',{n:'-1234.50'},'003'],['门槛','004',{n:'1234567890.12'},'004'],['零一','005',{n:'0'},'005'],['零二','006',{n:'0'},'006'],['原文','007','8000.00','007'],['缺缓存','008',{f:'1+1'},'008']];
const xml=s=>String(s).replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&apos;'}[c]));
const sheet=`<worksheet xmlns="${ns}"><sheetData>`+rows.map((row,r)=>`<row r="${r+1}">`+row.map((cell,c)=>{const ref=String.fromCharCode(65+c)+(r+1);return typeof cell==='string'?`<c r="${ref}" t="inlineStr"><is><t>${xml(cell)}</t></is></c>`:cell.f?`<c r="${ref}"><f>${cell.f}</f></c>`:`<c r="${ref}"><v>${cell.n}</v></c>`;}).join('')+'</row>').join('')+'</sheetData></worksheet>';
fs.writeFileSync(file,zip({'xl/workbook.xml':`<workbook xmlns="${ns}" xmlns:r="${rel}"><sheets><sheet name="数字显示" sheetId="1" r:id="s1"/></sheets></workbook>`,'xl/_rels/workbook.xml.rels':`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="s1" Type="${rel}/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,'xl/worksheets/sheet1.xml':sheet}));
const hash=()=>createHash('sha256').update(fs.readFileSync(file)).digest('hex'),before=hash();
const report={version:config.version(),result:'FAIL',desktopManualAcceptance:false,officeWpsAcceptance:false,checks:[],screenshots:[],errors:[]};let h;
const gridReady=`typeof window.ExcelLensPerformance==='function'&&window.ExcelLensPerformance().hasVisibleCoverage&&!window.ExcelLensPerformance().sheetLoading`;
const ready=`window.ExcelLensAnalytics.diagnostics().opened&&window.ExcelLensAnalytics.diagnostics().fieldsReady&&!window.ExcelLensAnalytics.diagnostics().fieldsLoading&&!document.querySelector('#analysisRun').disabled`;
async function choose(p,selector,value){await p.run((selector,value)=>{const n=document.querySelector(selector);n.value=value;n.dispatchEvent(new Event('change',{bubbles:true}));},selector,value);}
async function assign(p,id,zone){await p.run((id,zone)=>{const data=new DataTransfer();data.setData('text/plain',id);document.querySelector('[data-analysis-zone="'+zone+'"]').dispatchEvent(new DragEvent('drop',{bubbles:true,dataTransfer:data}));},id,zone);}
async function frame(p){await p.evaluate('new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))');}
async function open(p){await p.click('#analysisOpenBtn');await p.wait(ready);}
async function configure(p,layout){
  await assign(p,'C1','rows');await assign(p,'C2','rows');await assign(p,'C3','values');await p.click('#analysisAddMetric');
  await p.run(()=>{const select=document.querySelectorAll('#analysisMetricEditor .analysis-metric-row')[1].querySelectorAll('select')[1];select.value='average';select.dispatchEvent(new Event('change'));});
  await choose(p,'#analysisLayout',layout);await choose(p,'#analysisSort','label_asc');await choose(p,'#analysisTop','0');
}
async function run(p){
  const previous=await p.evaluate('window.ExcelLensAnalytics.diagnostics().resultJob');await p.click('#analysisRun');
  await p.wait(`${ready}&&window.ExcelLensAnalytics.diagnostics().resultJob!==${JSON.stringify(previous)}`);
  const id=await p.evaluate('window.ExcelLensAnalytics.diagnostics().resultJob'),status=await p.api('/api/analysis/query/status?id='+id);
  assert.equal(status.result.grand.exact,sum);assert.equal(status.result.metricGrand[1].exact,average);assert.equal(status.result.invalidNumbers,1);assert.equal(status.result.missingFormulaCaches,1);return status;
}
async function monitor(p){await p.run(()=>{
  window.qaNumbers={starts:[],drills:[],exports:[],failSave:false};const original=fetch.bind(window);
  window.fetch=async(url,options)=>{
    const name=String(url),q=window.qaNumbers;
    if(name==='/api/settings/analysis-display'&&options?.method==='POST'&&q.failSave){q.failSave=false;return new Response(JSON.stringify({error:'模拟保存失败，请重试'}),{status:503,headers:{'Content-Type':'application/json'}});}
    const response=await original(url,options);
    if(name==='/api/analysis/workspace'&&q.holdWorkspace){q.workspaceHeld=true;await new Promise(resolve=>q.releaseWorkspace=resolve);}
    if(['/api/analysis/query/start','/api/analysis/tree/start','/api/recipes/run/start'].includes(name))q.starts.push({path:name,request:JSON.parse(options.body),value:await response.clone().json()});
    if(name==='/api/analysis/drill')q.drills.push({request:JSON.parse(options.body),value:await response.clone().json()});
    if(name.startsWith('/api/analysis/export?'))q.exports.push({url:name,value:name.includes('format=csv')?await response.clone().text():Array.from(new Uint8Array(await response.clone().arrayBuffer()))});
    return response;
  };
});}
async function cards(p){return p.run(()=>[...document.querySelectorAll('#analysisMetrics .analysis-metric-value')].map(n=>({text:n.textContent,title:n.title})));}
async function settings(p,options){
  await p.click('#analysisSettingsOpen');await p.wait('document.querySelector("#analysisSettingsDialog").open&&!document.querySelector("#analysisSettingsSave").disabled');
  await p.run(options=>{for(const [key,id] of [['groupThousands','analysisGroupThousands'],['scientific','analysisScientific']])if(key in options){const n=document.getElementById(id);n.checked=options[key];n.dispatchEvent(new Event('change',{bubbles:true}));}if('scientificExponent' in options){const n=document.querySelector('#analysisScientificExponent');n.value=String(options.scientificExponent);n.dispatchEvent(new Event('change',{bubbles:true}));}},options);
}
async function saveSettings(p){await p.click('#analysisSettingsSave');await p.wait('!document.querySelector("#analysisSettingsDialog").open');await frame(p);}
async function treeRow(p,path){return p.run(path=>{const row=[...document.querySelectorAll('.analysis-tree-row')].find(n=>n.dataset.treePath===JSON.stringify(path));return row?{text:row.innerText,numbers:[...row.querySelectorAll('.analysis-number')].map(n=>n.textContent)}:null;},path);}
async function expand(p,path){await p.run(path=>[...document.querySelectorAll('.analysis-tree-row')].find(n=>n.dataset.treePath===JSON.stringify(path)).querySelector('.analysis-tree-toggle').click(),path);await p.wait('window.ExcelLensAnalytics.diagnostics().tree.active===0&&window.ExcelLensAnalytics.diagnostics().tree.queued===0');}
async function tooltip(p,selector,expected){return p.run(async(selector,expected)=>{
  const canvas=document.querySelector(selector),bounds=canvas.getBoundingClientRect(),tip=canvas.parentElement.querySelector('.analysis-chart-tooltip');
  for(let y=bounds.height-45;y>=25;y-=18)for(let x=65;x<bounds.width-15;x+=10){canvas.dispatchEvent(new MouseEvent('mousemove',{clientX:bounds.left+x,clientY:bounds.top+y}));await new Promise(requestAnimationFrame);if(!tip.hidden&&tip.textContent.includes(expected))return tip.textContent;}
  throw Error('No tooltip for '+expected+' in '+selector);
},selector,expected);}
async function shot(p,title){const file=path.join(out,title+'.png');await p.shot(file);report.screenshots.push(path.relative(root,file).split(path.sep).join('/'));}
async function exportValue(p,format){await choose(p,'#analysisExportFormat',format);const old=await p.evaluate('window.qaNumbers.exports.length');await p.click('#analysisExport');await p.wait(`window.qaNumbers.exports.length>${old}`);return p.evaluate('window.qaNumbers.exports.at(-1).value');}

(async()=>{try{
  h=await launch({out,errors:report.errors,duration:'15m'});const p=h.page;await p.file('#fileInput',file);await p.wait(gridReady);await monitor(p);await open(p);await configure(p,'tree');const status=await run(p);await frame(p);
  assert.equal((await cards(p))[0].text,'9.01e+15');assert.equal((await cards(p))[0].title,'精确值：'+sum);assert.equal((await cards(p))[1].text,'1.50e+15');assert.equal((await cards(p))[1].title,'精确值：'+average);
  assert.equal((await treeRow(p,['乙'])).numbers[0],'-1,234.50');assert.equal((await treeRow(p,['门槛'])).numbers[0],'1,234,567,890.12');await expand(p,['甲']);assert.equal((await treeRow(p,['甲','002'])).numbers[0],'1,234,567.89');assert((await treeRow(p,['甲','001'])).text.includes('001'));
  const rowExact=status.result.rows.find(r=>r.keys[0]==='甲').total.exact,tip=await tooltip(p,'#analysisChart',rowExact);assert(tip.includes('9.01e+15'));await shot(p,'default-scientific');
  const csvBefore=await exportValue(p,'csv'),starts=await p.evaluate('window.qaNumbers.starts.length');
  await settings(p,{scientific:false});assert.equal(await p.evaluate('document.querySelector("#analysisLargePreview").textContent'),'9,007,199,254,741,015.11');await saveSettings(p);
  assert.equal((await cards(p))[0].text,'9,007,200,490,542,216.82');assert.equal((await cards(p))[1].text,'1,501,200,081,757,036.14');assert.equal((await treeRow(p,['甲','001'])).numbers[0],'9,007,199,254,740,993.31');assert((await tooltip(p,'#analysisChart',rowExact)).includes('9,007,199,255,975,561.20'));
  await settings(p,{groupThousands:false});await saveSettings(p);assert.equal((await cards(p))[0].text,'9007200490542216.82');assert.equal((await treeRow(p,['甲','002'])).numbers[0],'1234567.89');
  await settings(p,{scientific:true,scientificExponent:15});await saveSettings(p);assert.equal((await cards(p))[0].text,'9.01e+15');assert.equal((await treeRow(p,['门槛'])).numbers[0],'1234567890.12');
  await settings(p,{groupThousands:true,scientificExponent:9});await saveSettings(p);assert.equal((await treeRow(p,['门槛'])).numbers[0],'1.23e+9');assert.equal((await treeRow(p,['甲','002'])).numbers[0],'1,234,567.89');assert.equal(await p.evaluate('window.qaNumbers.starts.length'),starts);
  report.checks.push('default thousands/scientific formatting covers exact large sum, rational average, negatives, real zero and cached child cells; independent switches and 10^9/10^12/10^15 thresholds update completed results and tooltips without starting another query');
  await p.run(()=>[...document.querySelectorAll('.analysis-tree-row')].find(n=>n.dataset.treePath===JSON.stringify(['甲'])).querySelector('.analysis-drill-link').click());await p.wait('window.qaNumbers.drills.length>0');const drilled=await p.evaluate('window.qaNumbers.drills.at(-1)');assert.deepEqual(drilled.value.rows.map(row=>row.row),[2,3]);assert(drilled.value.rows[0].cells.includes('001'));assert(drilled.value.rows[0].cells.includes('9007199254740993.31'));await p.click('#analysisDrillClose');
  assert.equal(await exportValue(p,'csv'),csvBefore);const xlsx=Buffer.from(await exportValue(p,'xlsx')),exportFile=path.join(out,'首层精确值.xlsx');fs.writeFileSync(exportFile,xlsx);
  const {range,status:reopenedStatus}=await reopenRange(h,exportFile,'A1','C20');
  const footer=range.rows.find(row=>row.cells.some(cell=>cell.col===1&&cell.raw==='总计'));assert(footer);assert.equal(footer.cells.find(c=>c.col===2).raw,sum);assert.equal(footer.cells.find(c=>c.col===3).raw,average);assert.equal(footer.cells.find(c=>c.col===2).type,'inlineStr');assert.equal(footer.cells.find(c=>c.col===3).type,'inlineStr');assert.equal(range.rows[0].cells.length,3);assert.equal(reopenedStatus.opened,true);
  report.checks.push('source drill preserves raw amount and leading-zero text; CSV is byte-identical across display settings; actual downloaded first-level XLSX reopens through the real adapter with three columns and exact large/rational totals stored as text to preserve precision');
  await choose(p,'#analysisLayout','');await run(p);assert.equal(await p.evaluate('document.querySelector("#analysisPivotTable").hasAttribute("role")'),false);assert.equal(await p.evaluate(`document.querySelector('#analysisPivotTable tfoot .analysis-number').textContent`),'9.01e+15');
  await p.run(()=>window.qaNumbers.holdWorkspace=true);
  await p.click('#analysisSaveReport');await p.wait('!!document.querySelector(".ask-dialog[open] input")');await p.run(()=>document.querySelector('.ask-dialog[open] input').value='数字显示报表');await p.click('.ask-dialog[open] [data-yes]');await p.wait('window.qaNumbers.workspaceHeld===true');await p.click('[data-analysis-tab="dashboard"]');
  await p.run(()=>{window.qaNumbers.holdWorkspace=false;window.qaNumbers.workspaceHeld=false;window.qaNumbers.releaseWorkspace();});
  await p.wait('typeof document.querySelector(".analysis-dashboard-card canvas")?.onmousemove==="function"');await frame(p);const dashBefore=await p.evaluate('window.qaNumbers.starts.length');
  assert((await tooltip(p,'.analysis-dashboard-card canvas','9007199254740993.31')).includes('9.01e+15'));await settings(p,{scientific:false});await saveSettings(p);assert((await tooltip(p,'.analysis-dashboard-card canvas','9007199254740993.31')).includes('9,007,199,254,740,993.31'));assert.equal(await p.evaluate('window.qaNumbers.starts.length'),dashBefore);
  await p.click('[data-analysis-tab="pivot"]');assert.equal((await cards(p))[0].text,'9,007,200,490,542,216.82');assert.equal(await p.evaluate(`document.querySelector('#analysisPivotTable tfoot .analysis-number').textContent`),'9,007,200,490,542,216.82');
  report.checks.push('saving while the workspace catalogue response is deliberately held, then entering dashboard before release, still installs the saved report; flat pivot cells and already-loaded dashboard chart tooltips adopt settings immediately, without re-running saved reports');
  await settings(p,{groupThousands:false});await p.run(()=>window.qaNumbers.failSave=true);await p.click('#analysisSettingsSave');await p.wait('document.querySelector("#analysisSettingsStatus").textContent.includes("模拟保存失败")');assert.equal((await cards(p))[0].text,'9,007,200,490,542,216.82');assert.equal(await p.evaluate('document.querySelector("#analysisSettingsDialog").open'),true);await p.click('#analysisSettingsClose');
  await settings(p,{groupThousands:false});const remote=await p.api('/api/settings/analysis-display');await p.api('/api/settings/analysis-display',{expectedRevision:remote.revision,options:{groupThousands:true,scientific:true,scientificExponent:12}});await p.click('#analysisSettingsSave');await p.wait('document.querySelector("#analysisSettingsStatus").textContent.includes("其他页面修改")');assert.equal(await p.evaluate('document.querySelector("#analysisSettingsSave").disabled'),true);assert.equal((await cards(p))[0].text,'9,007,200,490,542,216.82');await p.click('#analysisSettingsReload');await p.wait('!document.querySelector("#analysisSettingsSave").disabled');assert.equal((await cards(p))[0].text,'9.01e+15');await p.click('#analysisSettingsDefaults');await saveSettings(p);
  await p.call('Emulation.setDeviceMetricsOverride',{width:420,height:820,deviceScaleFactor:1,mobile:false});assert.equal(await p.evaluate('(()=>{const r=document.querySelector("#analysisSettingsOpen").getBoundingClientRect();return r.left>=0&&r.right<=innerWidth;})()'),true);await settings(p,{});await shot(p,'settings-420px');assert.equal(await p.evaluate('(()=>{const r=document.querySelector("#analysisSettingsDialog").getBoundingClientRect();return r.left>=0&&r.right<=innerWidth&&r.top>=0&&r.bottom<=innerHeight;})()'),true);await p.click('#analysisSettingsClose');await p.call('Emulation.setDeviceMetricsOverride',{width:1600,height:1050,deviceScaleFactor:1,mobile:false});
  await settings(p,{groupThousands:false,scientific:false,scientificExponent:15});await saveSettings(p);const saved=(await p.api('/api/settings/analysis-display')).options;assert.deepEqual(saved,{groupThousands:false,scientific:false,scientificExponent:15});
  report.checks.push('failed saves keep displayed and persisted settings; stale cross-page revisions are rejected until explicit reload; restore-default and narrow 420px native dialog controls work');
  const oldAddress=h.address;await h.restart();assert.notEqual(h.address,oldAddress);await p.call('Page.navigate',{url:h.address});await p.wait('document.readyState==="complete"&&!!window.ExcelLensSession');await p.evaluate('window.ExcelLensSession.ready');assert.deepEqual((await p.api('/api/settings/analysis-display')).options,saved);await p.file('#fileInput',file);await p.wait(gridReady);await monitor(p);await open(p);await configure(p,'tree');await run(p);assert.equal((await cards(p))[0].text,'9007200490542216.82');await settings(p,{});assert.deepEqual(await p.run(()=>({groupThousands:analysisGroupThousands.checked,scientific:analysisScientific.checked,scientificExponent:Number(analysisScientificExponent.value)})),saved);await p.click('#analysisSettingsClose');await shot(p,'restart-preferences');
  report.checks.push('actual service process restart on a different random port retains both independent preferences and the selected threshold in user config; rerun exact result and rendered card agree');
  assert.equal(hash(),before);assert.deepEqual(report.errors,[]);report.sourceSHA256=before;report.sourceUnchanged=true;report.result='PASS';
}catch(error){report.error=error.stack;process.exitCode=1;if(h)try{await h.page.shot(path.join(out,'failure.png'));report.ui=await h.page.run(()=>({toast:document.querySelector('#analysisToast')?.textContent,settings:document.querySelector('#analysisSettingsStatus')?.textContent,cards:[...document.querySelectorAll('.analysis-metric-value')].map(n=>n.textContent),table:document.querySelector('#analysisPivotTable')?.innerText,diagnostics:window.ExcelLensAnalytics?.diagnostics(),last:window.qaNumbers}));}catch{}}
finally{if(h)await h.close();fs.writeFileSync(path.join(root,'validation',config.validationVersion(),'analysis-numbers-ui-browser.json'),JSON.stringify(report,null,2)+'\n');console.log(JSON.stringify(report,null,2));}})();
