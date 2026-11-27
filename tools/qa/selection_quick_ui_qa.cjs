// Real Go service and isolated headless Chrome, using generated public data.
// Browser automation is separate from desktop or Office/WPS manual acceptance.
const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict'),{createHash}=require('node:crypto');
const config=require('./project_config.cjs'),{launch,pause}=require('./browser_harness.cjs'),{zip}=require('./generated_workbook.cjs');
const root=path.resolve(__dirname,'../..'),base=path.join(root,'validation',config.validationVersion()),out=path.join(base,'private','selection-quick-ui');fs.mkdirSync(out,{recursive:true});
const file=path.join(out,'选区分析.xlsx'),copy=path.join(out,'同内容另一路径.xlsx'),csv=path.join(out,'选区文字.csv');
const text=(ref,value)=>`<c r="${ref}" t="inlineStr"><is><t>${value.replaceAll('&','&amp;').replaceAll('<','&lt;')}</t></is></c>`,num=(ref,raw)=>`<c r="${ref}"><v>${raw}</v></c>`;
let rows='<row r="1">'+text('A1','编号')+text('B1','本期')+text('C1','上期')+text('D1','<img src=x onerror=alert(1)>')+'</row>';
for(let r=2;r<=125;r++){
  if(r===9)continue;
  let b,c;
  if(r===2){b=num('B2','9007199254740993.31');c=num('C2','9007199254740993.30');}
  else if(r===3){b=num('B3','-2.50');c=num('C3','0.25');}
  else if(r===4){b=num('B4','999999999999999999999');c=num('C4','0');}
  else if(r===5){b=num('B5','0');c='';}
  else if(r===6){b='<c r="B6"><f>1+1</f></c>';c=num('C6','1');}
  else if(r===7){b='<c r="B7"><f>1+2.2</f><v>3.2</v></c>';c=num('C7','0.2');}
  else if(r===8){b='<c r="B8" t="b"><v>1</v></c>';c=num('C8','3');}
  else if(r===10){b='<c r="B10" t="e"><v>#DIV/0!</v></c>';c=num('C10','5');}
  else if(r===11||r===12){b=text('B'+r,r===11?'001':'7');c=num('C'+r,'1');}
  else {b=num('B'+r,((r-65)/100).toFixed(2));c=num('C'+r,((r-66)/100).toFixed(2));}
  rows+=`<row r="${r}"${r===4?' hidden="1"':''}>${text('A'+r,'000'+r)}${b}${c}${text('D'+r,'原文'+r)}</row>`;
}
fs.writeFileSync(file,zip({
  'xl/workbook.xml':'<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="明细" sheetId="1" r:id="s1"/></sheets></workbook>',
  'xl/_rels/workbook.xml.rels':'<Relationships><Relationship Id="s1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>',
  'xl/worksheets/sheet1.xml':'<worksheet><cols><col min="3" max="3" hidden="1"/></cols><sheetData>'+rows+'</sheetData></worksheet>'
}));
fs.copyFileSync(file,copy);fs.writeFileSync(csv,'编号,左列,右列\n001,9007199254740993.31,9007199254740993.30\n002,-0.01,0.01\n');
const hash=file=>createHash('sha256').update(fs.readFileSync(file)).digest('hex'),hashes=Object.fromEntries([file,copy,csv].map(f=>[path.basename(f),hash(f)]));
const report={version:config.version(),result:'FAIL',checks:[],errors:[],screenshots:[],desktopManualAcceptance:false,officeWpsAcceptance:false};let h;
const ready='window.ExcelLensPerformance?.().hasVisibleCoverage&&!window.ExcelLensPerformance().sheetLoading',done='document.querySelector("#selectionQuickDialog").open&&document.querySelector("#selectionQuickCancel").hidden&&!!document.querySelector("#selectionQuickSummary")&&!document.querySelector("#selectionQuickStatus").classList.contains("is-error")';
async function choose(p,id,value){await p.run((id,value)=>{const n=document.getElementById(id);n.value=value;n.dispatchEvent(new Event('change',{bubbles:true}));},id,value);}
async function source(p,file){await p.file('#fileInput',file);await p.wait(ready+'&&document.querySelector("#fileBadge").textContent.includes('+JSON.stringify(path.basename(file))+')');}
async function select(p,ref){await p.run(ref=>{const n=document.getElementById('nameBox');n.focus();n.value=ref;n.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',code:'Enter',bubbles:true}));},ref);await p.wait(ready+'&&document.querySelector("#nameBox").value==='+JSON.stringify(ref));}
async function key(p){await p.run(()=>document.getElementById('grid').focus());await p.call('Input.dispatchKeyEvent',{type:'keyDown',key:'q',code:'KeyQ',modifiers:2,windowsVirtualKeyCode:81});await p.call('Input.dispatchKeyEvent',{type:'keyUp',key:'q',code:'KeyQ',modifiers:2,windowsVirtualKeyCode:81});await p.wait('document.querySelector("#selectionQuickDialog").open');}
async function run(p){const old=await p.evaluate('window.quickQA.pages.length');await p.click('#selectionQuickRun');await p.wait(done+'&&window.quickQA.pages.length>'+old);return p.evaluate('window.quickQA.pages.at(-1)');}
async function shot(p,name){await p.wait('[...document.querySelectorAll("dialog[open]")].every(n=>getComputedStyle(n).opacity==="1")');const output=path.join(out,name+'.png');await p.shot(output);report.screenshots.push(path.relative(root,output).split(path.sep).join('/'));}
(async()=>{try{
  h=await launch({out,errors:report.errors,duration:'15m'});const p=h.page;
  report.assetSHA256 = await config.verifyAssets(h.address, 'selection-quick.css');
  await p.run(()=>{window.quickQA={requests:[],pages:[],held:[],hold:false,fail:false,alerts:[]};window.alert=s=>window.quickQA.alerts.push(s);const fetchOriginal=window.fetch.bind(window);window.fetch=async(url,init)=>{
    if(String(url)!=='/api/selection/quick')return fetchOriginal(url,init);
    const q=window.quickQA;q.requests.push(JSON.parse(init.body));const response=await fetchOriginal(url,init);if(!response.ok)return response;
    q.pages.push(await response.clone().json());if(q.hold){q.hold=false;return new Promise(resolve=>q.held.push(()=>resolve(response)));}
    if(q.fail){q.fail=false;return new Response(JSON.stringify({error:'模拟选区读取失败'}),{status:503,headers:{'Content-Type':'application/json'}});}return response;
  };});
  await source(p,file);await select(p,'B1:D125');assert.equal(await p.evaluate('window.quickQA.requests.length'),0);await key(p);assert.equal(await p.evaluate('window.quickQA.requests.length'),0);
  await p.click('#selectionQuickSkip');await choose(p,'selectionQuickMinuend','2');await choose(p,'selectionQuickSubtrahend','3');
  const first=await run(p);assert.equal(first.selectedRows,124);assert.equal(first.rows,123);assert.equal(first.difference.paired,116);assert.equal(first.difference.unpaired,7);assert.equal(first.difference.summary.sum,'1.39');assert.equal(first.columns[0].counts.missing,1);assert.equal(first.columns[0].counts.errors,1);assert.equal(first.columns[0].counts.other,3);assert.equal(first.columns[0].counts.blank,1);assert.equal(first.columns[2].counts.number,0);
  assert.equal(first.columns[0].segments.length,60);assert(!first.columns[0].segments.some(x=>x.firstRow===4));assert.equal(await p.evaluate('document.querySelectorAll("#selectionQuickBody img,#selectionQuickBody script").length'),0);
  assert.match(await p.evaluate('document.querySelector("#selectionQuickSource").textContent'),/含隐藏列/);await shot(p,'selection-quick-desktop');
  const calls=await p.evaluate('window.quickQA.requests.length');await choose(p,'selectionQuickSeries','2');await p.run(()=>document.dispatchEvent(new Event('excellens:analysisnumberchange')));await pause(80);assert.equal(await p.evaluate('window.quickQA.requests.length'),calls);
  await p.call('Emulation.setDeviceMetricsOverride',{width:420,height:860,deviceScaleFactor:1,mobile:false});await shot(p,'selection-quick-420');
  assert(await p.run(()=>{const d=document.getElementById('selectionQuickDialog').getBoundingClientRect();return d.left>=0&&d.right<=innerWidth&&['selectionQuickRun','selectionQuickClose'].every(id=>{const r=document.getElementById(id).getBoundingClientRect();return r.left>=0&&r.right<=innerWidth&&r.top>=0&&r.bottom<=innerHeight;});}));
  await p.run(()=>{document.documentElement.setAttribute('data-reading-preset','graphite');document.documentElement.setAttribute('data-reading-tone','dark');document.dispatchEvent(new Event('excellens:readingchange'));});await shot(p,'selection-quick-dark-420');
  report.contrasts=await p.run(()=>{const rgb=s=>s.match(/[\d.]+/g).slice(0,3).map(Number),light=s=>rgb(s).reduce((sum,n,i)=>{n/=255;return sum+[.2126,.7152,.0722][i]*(n<=.04045?n/12.92:((n+.055)/1.055)**2.4);},0);return ['#selectionQuickRun','#selectionQuickSummary .selection-quick-value','#selectionQuickSource'].map(selector=>{const node=document.querySelector(selector),color=getComputedStyle(node).color;let background='';for(let parent=node;parent;parent=parent.parentElement){background=getComputedStyle(parent).backgroundColor;if(!['rgba(0, 0, 0, 0)','transparent'].includes(background))break;}const a=light(color),b=light(background);return {selector,color,background,ratio:(Math.max(a,b)+.05)/(Math.min(a,b)+.05)};});});
  assert(report.contrasts.every(x=>x.ratio>=4.5),JSON.stringify(report.contrasts));assert.equal(await p.evaluate('window.quickQA.requests.length'),calls);
  report.checks.push('Ctrl+Q and explicit run use the actual hidden-row view, include declared hidden source columns, preserve native text/errors/missing formula caches, and compute all 116 paired rows exactly as 1.39; 60 full-data segments, 420px controls, dark contrast, safe text, number/theme/resize without requery');
  await p.run(()=>{document.documentElement.removeAttribute('data-reading-preset');document.documentElement.removeAttribute('data-reading-tone');});await p.call('Emulation.setDeviceMetricsOverride',{width:1600,height:1050,deviceScaleFactor:1,mobile:false});
  const old=await p.evaluate('document.querySelector("#selectionQuickSummary").textContent');await p.run(()=>window.quickQA.fail=true);await p.click('#selectionQuickRun');await p.wait('document.querySelector("#selectionQuickStatus").textContent.includes("模拟选区读取失败")');assert.equal(await p.evaluate('document.querySelector("#selectionQuickSummary").textContent'),old);
  await p.run(()=>window.quickQA.hold=true);await p.click('#selectionQuickRun');await p.wait('window.quickQA.held.length===1');await p.click('#selectionQuickCancel');const retried=await run(p);await p.run(()=>window.quickQA.held.shift()());await pause(80);assert.equal(retried.difference.summary.sum,'1.39');assert.match(await p.evaluate('document.querySelector("#selectionQuickPairing").textContent'),/116/);
  await p.click('#selectionQuickClose');await select(p,'B1:D125');await p.click('#commandOpenBtn');await p.run(()=>{const n=document.getElementById('commandSearch');n.value='选区快速分析';n.dispatchEvent(new Event('input'));});await p.click('#commandList .command-entry');await p.wait('document.getElementById("selectionQuickDialog").open');await run(p);
  await p.click('#selectionQuickSummary .selection-quick-ref');await p.wait('!document.getElementById("selectionQuickDialog").open&&'+ready);assert.match(await p.evaluate('document.getElementById("nameBox").value'),/^B\d+$/);
  await select(p,'B1:D125');await key(p);await p.run(()=>window.quickQA.hold=true);await p.click('#selectionQuickRun');await p.wait('window.quickQA.held.length===1');const original=await p.api('/api/status'),oldRequest=await p.evaluate('window.quickQA.requests.at(-1)');await source(p,copy);await p.run(()=>window.quickQA.held.shift()());await pause(80);assert.equal(await p.evaluate('document.getElementById("selectionQuickDialog").open'),false);
  const switched=await p.api('/api/status');assert.equal(switched.workbookKey,original.workbookKey);assert.notEqual(switched.book,original.book);let refused=false;try{await p.api('/api/selection/quick',oldRequest);}catch(error){refused=/来源已切换/.test(error.message);}assert(refused);
  report.checks.push('failed refresh preserves the accepted complete result; cancel/retry ignores a deliberately held late response; command entry and source-reference navigation work; same-content different-path switch closes and rejects a late old-source result');
  await p.click('#freezeBtn');await choose(p,'freezeRowsInput','1');await choose(p,'freezeColsInput','0');await p.click('#freezeApplyBtn');await p.wait(ready);
  await p.click('#multiSortBtn');await choose(p,'sortHeaderRow','1');await p.run(()=>{document.querySelector('.sort-column').value='B';document.querySelector('.sort-direction').value='desc';});await p.click('#sortApply');await p.wait(ready+'&&!document.getElementById("clearFilterBtn").disabled');
  const drag=await p.run(()=>['B2','D7'].map(ref=>{const n=document.querySelector('#grid .grid-cell[data-ref="'+ref+'"]');if(!n)throw Error('Sorted drag target missing '+ref);const r=n.getBoundingClientRect();return {x:r.x+r.width/2,y:r.y+r.height/2};}));
  await p.call('Input.dispatchMouseEvent',{type:'mouseMoved',...drag[0]});await p.call('Input.dispatchMouseEvent',{type:'mousePressed',button:'left',buttons:1,clickCount:1,...drag[0]});await p.call('Input.dispatchMouseEvent',{type:'mouseMoved',button:'left',buttons:1,...drag[1]});await p.call('Input.dispatchMouseEvent',{type:'mouseReleased',button:'left',buttons:0,clickCount:1,...drag[1]});await key(p);
  await choose(p,'selectionQuickMinuend','2');await choose(p,'selectionQuickSubtrahend','3');const sorted=await run(p);assert(sorted.filterId&&sorted.viewId);assert.equal(sorted.excludeThroughRow,1);assert.equal(sorted.columns[0].segments[0].firstRow,2);assert.equal(sorted.columns[0].segments.at(-1).lastRow,7);assert.equal(sorted.difference.summary.sum,'3.01');await shot(p,'selection-quick-sorted');await p.click('#selectionQuickClose');
  report.checks.push('a real mouse drag across descending sorted source rows 2 to 7 with a frozen prefix carries paired source/visual endpoints and returns exact same-row difference 3.01 without scanning the intervening physical rows');
  await source(p,csv);await select(p,'A1:C3');await p.run(()=>{const cell=document.querySelector('#grid .grid-cell[data-ref="B2"]');cell.dispatchEvent(new MouseEvent('contextmenu',{bubbles:true,cancelable:true,clientX:350,clientY:250}));});await p.click('#menuSelectionQuick');await p.wait('document.getElementById("selectionQuickDialog").open');await p.click('#selectionQuickSkip');await choose(p,'selectionQuickMinuend','3');await choose(p,'selectionQuickSubtrahend','2');const imported=await run(p);assert.equal(imported.difference.summary.sum,'0.01');assert.equal(imported.columns[0].counts.number,0);assert.equal(imported.columns[0].counts.other,2);
  await p.click('#selectionQuickClose');await p.click('#profileOpenBtn');await p.click('#profileQuickBtn');await p.wait('document.getElementById("selectionQuickDialog").open');await shot(p,'selection-quick-profile-entry');
  assert.equal(await p.evaluate('window.quickQA.alerts.length'),0);for(const f of [file,copy,csv])assert.equal(hash(f),hashes[path.basename(f)]);assert.equal(report.errors.length,0);report.sourceSHA256=hashes;report.sourceUnchanged=true;report.result='PASS';
  report.checks.push('actual CSV right-click entry preserves leading-zero IDs and exact imported text amounts; statistics-window entry reuses the current selected region without implicit reads; generated source hashes unchanged');
}catch(error){report.errors.push(String(error.stack||error));if(h)try{report.failure=await h.page.run(()=>({source:document.getElementById('fileBadge')?.textContent,range:document.getElementById('nameBox')?.value,status:document.getElementById('selectionQuickStatus')?.textContent,dialogs:[...document.querySelectorAll('dialog[open]')].map(x=>x.id),requests:window.quickQA?.requests.slice(-3),pages:window.quickQA?.pages.slice(-1)}));await shot(h.page,'selection-quick-failure');}catch(diagnostic){report.errors.push(diagnostic.message);}}
finally{if(h)await h.close();fs.writeFileSync(path.join(base,'selection-quick-ui-browser.json'),JSON.stringify(report,null,2)+'\n');console.log(JSON.stringify(report,null,2));if(report.result!=='PASS')process.exitCode=1;}})();
