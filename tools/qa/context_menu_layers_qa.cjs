// Real paint/hit tests for viewport menus overlapping colored worksheet tabs.
// Uses generated data and an isolated browser; no desktop manual acceptance.
const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict'),{createHash}=require('node:crypto');
const config=require('./project_config.cjs'),{launch}=require('./browser_harness.cjs'),{zip}=require('./generated_workbook.cjs');
const root=path.resolve(__dirname,'../..'),base=path.join(root,'validation',config.validationVersion());
const phase=process.env.QA_LAYER_PHASE||'after';assert(['before','after'].includes(phase));
const out=path.join(base,'private','context-menu-layers',phase);fs.mkdirSync(out,{recursive:true});
const source=path.join(out,'彩色工作表.xlsx'),ns='http://schemas.openxmlformats.org/spreadsheetml/2006/main',rel='http://schemas.openxmlformats.org/officeDocument/2006/relationships';
const sheets=['明细','有色标签','普通标签'];
let rows='';for(let row=1;row<=100;row++){rows+=`<row r="${row}"><c r="A${row}" t="inlineStr"><is><t>记录${row}</t></is></c><c r="B${row}"><v>${row}.25</v></c><c r="C${row}" t="inlineStr"><is><t>000${row}</t></is></c></row>`;}
const files={'xl/workbook.xml':`<workbook xmlns="${ns}" xmlns:r="${rel}"><sheets>${sheets.map((s,i)=>`<sheet name="${s}" sheetId="${i+1}" r:id="s${i+1}"/>`).join('')}</sheets></workbook>`,
  'xl/_rels/workbook.xml.rels':`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">${sheets.map((_,i)=>`<Relationship Id="s${i+1}" Type="${rel}/worksheet" Target="worksheets/sheet${i+1}.xml"/>`).join('')}</Relationships>`};
for(let i=0;i<sheets.length;i++)files[`xl/worksheets/sheet${i+1}.xml`]=`<worksheet xmlns="${ns}">${i<2?`<sheetPr><tabColor rgb="${i?'FFFFD700':'FF008800'}"/></sheetPr>`:''}<sheetFormatPr defaultRowHeight="24"/><sheetData>${rows}</sheetData></worksheet>`;
fs.writeFileSync(source,zip(files));const digest=file=>createHash('sha256').update(fs.readFileSync(file)).digest('hex'),sourceBefore=digest(source);
const report={version:config.version(),phase,result:'FAIL',checks:[],cases:[],screenshots:[],errors:[],desktopManualAcceptance:false,officeWpsAcceptance:false};let h;
const ready='window.ExcelLensPerformance?.().hasVisibleCoverage&&!window.ExcelLensPerformance().sheetLoading';
async function shot(name){const output=path.join(out,name+'.png');await h.page.shot(output);report.screenshots.push(path.relative(root,output).replaceAll('\\','/'));}
async function input(type,point,button='left'){await h.page.call('Input.dispatchMouseEvent',{type,x:point.x,y:point.y,button,clickCount:1});}
async function dismiss(){await h.page.run(()=>document.body.dispatchEvent(new MouseEvent('click',{bubbles:true})));}
async function open(kind){
  await dismiss();
  const point=await h.page.run(kind=>{const grid=document.querySelector('.grid-region').getBoundingClientRect(),tab=document.querySelector('.sheet-tab.active').getBoundingClientRect();return {x:kind==='row'?12:tab.left+9,y:grid.bottom-35};},kind);
  await input('mousePressed',point,'right');await input('mouseReleased',point,'right');
  const id=kind==='row'?'sizeContextMenu':'cellMenu';await h.page.wait(`!document.getElementById('${id}').hidden&&getComputedStyle(document.getElementById('${id}')).opacity==='1'`);return id;
}
async function overlap(kind,label){
  const id=await open(kind);
  const result=await h.page.run(id=>{
    const menu=document.getElementById(id),bounds=menu.getBoundingClientRect(),tabs=[...document.querySelectorAll('.sheet-tab.has-tab-color')];
    const hits=tabs.map(tab=>{const r=tab.getBoundingClientRect(),left=Math.max(bounds.left+6,r.left+4),right=Math.min(bounds.right-6,r.right-4),top=Math.max(bounds.top+4,r.top+4),bottom=Math.min(bounds.bottom-4,r.bottom-4);if(left>=right||top>=bottom)return null;const x=(left+right)/2,y=(top+bottom)/2,hit=document.elementFromPoint(x,y);return {x,y,tab:tab.textContent,active:tab.classList.contains('active'),top:hit?.id||hit?.className,menuHit:!!hit?.closest('#'+id),item:hit?.closest('button')?.id};}).filter(Boolean);
    return {id,hits,gridIsolation:getComputedStyle(document.querySelector('.grid-region')).isolation,sheet:document.querySelector('.sheet-tab.active').textContent,bounds:{left:bounds.left,top:bounds.top,right:bounds.right,bottom:bounds.bottom},height:innerHeight};
  },id);
  report.cases.push({label,kind,...result});
  if(!result.hits.length||!result.hits.every(hit=>hit.menuHit)){await shot(label+'-overlap');assert(result.hits.length,'The menu must actually overlap a colored worksheet tab');assert(result.hits.every(hit=>hit.menuHit),'A colored worksheet tab paints over the context menu');}
  assert.equal(result.gridIsolation,'isolate','Grid drawing must keep its isolation');
  const point=result.hits[0];assert(point.item,'Overlapping menu action must be hittable');
  await h.page.run(item=>{window.__layerClicks=[];document.getElementById(item).addEventListener('click',()=>window.__layerClicks.push(item),{once:true});},point.item);
  await input('mousePressed',point);await input('mouseReleased',point);
  assert.deepEqual(await h.page.evaluate('window.__layerClicks'),[point.item]);
  assert.equal(await h.page.evaluate('document.querySelector(".sheet-tab.active").textContent'),result.sheet);
  assert(await h.page.evaluate(`document.getElementById('${id}').hidden`),'Action must retain its existing menu dismissal');
  await h.page.run(()=>{for(const d of document.querySelectorAll('dialog[open]'))d.close();});
}
(async()=>{try{
  h=await launch({out,errors:report.errors,duration:'10m'});const p=h.page;
  await p.file('#fileInput',source);await p.wait(ready);await p.wait('document.querySelectorAll(".sheet-tab.has-tab-color").length===2');
  for(const [width,height,dpr,preset] of [[1200,900,1,'off'],[900,760,1.25,'warm'],[420,760,2,'graphite']]){
    await p.call('Emulation.setDeviceMetricsOverride',{width,height,deviceScaleFactor:dpr,mobile:false});
    await p.run(preset=>{if(preset==='off')delete document.documentElement.dataset.readingPreset;else document.documentElement.dataset.readingPreset=preset;},preset);
    await p.wait(ready);await overlap('cell',width+'-cell');await overlap('row',width+'-row');
    report.checks.push(width+'px DPR'+dpr+': both context menus paint above colored tabs and receive clicks');
  }
  await p.call('Emulation.setDeviceMetricsOverride',{width:1200,height:900,deviceScaleFactor:1,mobile:false});
  await p.click('#freezeBtn');
  await p.run(()=>{delete document.documentElement.dataset.readingPreset;document.getElementById('freezeRowsInput').value='2';document.getElementById('freezeColsInput').value='0';document.getElementById('freezeApplyBtn').click();});
  await p.wait(ready+'&&!document.getElementById("freezeTop").hidden');await overlap('cell','frozen-cell');
  await open('cell');await shot('menu-above-tabs');await dismiss();
  const tab=await p.run(()=>{const r=document.querySelectorAll('.sheet-tab')[1].getBoundingClientRect();return {x:r.x+r.width/2,y:r.y+r.height/2};});
  await input('mousePressed',tab);await input('mouseReleased',tab);await p.wait(ready+'&&document.querySelector(".sheet-tab.active").textContent==="有色标签"');
  await overlap('cell','second-colored-active');
  report.checks.push('Frozen rows and changing the active colored tab preserve menu hit testing; tabs remain clickable after dismissal');
  report.sourceUnchanged=digest(source)===sourceBefore;assert(report.sourceUnchanged);report.sourceSHA256=sourceBefore;
  report.assets={};for(const name of ['index.html','style.css','app.js'])report.assets[name]=digest(path.join(root,'internal/ui/web',name));
  assert.deepEqual(report.errors,[]);report.result='PASS';
}catch(error){report.errors.push(error.stack||String(error));if(h)try{await shot('failure');}catch{}}
finally{if(h)await h.close();fs.writeFileSync(path.join(base,`context-menu-layering-${phase}.json`),JSON.stringify(report,null,2)+'\n');console.log(JSON.stringify({result:report.result,phase,checks:report.checks,cases:report.cases,errors:report.errors},null,2));if(report.result!=='PASS')process.exitCode=1;}})();
