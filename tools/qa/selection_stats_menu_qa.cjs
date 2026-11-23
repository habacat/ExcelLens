// The UI fixture mixes native numbers, numeric-looking text, an empty cell and
// a dependent formula. All browser preferences and workbook revisions are private.
const assert = require('node:assert/strict'), fs = require('node:fs'), path = require('node:path');
const {launch} = require('./browser_harness.cjs');
const {workbook} = require('./generated_workbook.cjs');
const {validationVersion} = require('./project_config.cjs');
const out = path.resolve('validation',validationVersion(),'private','selection-stats');
fs.mkdirSync(out,{recursive:true});
const report = {result:'FAIL',checks:[],errors:[]};
const ready = 'window.ExcelLensPerformance?.().hasVisibleCoverage && !window.ExcelLensPerformance().sheetLoading';

async function main(){
  const h=await launch({errors:report.errors});
  try{
    const p=h.page;
    const source=path.join(h.storage,'统计口径.xlsx');
    fs.writeFileSync(source,workbook([{name:'台账',rows:[
      ['机构','金额','调整','合计'],['001',{raw:'12345678.5'},'',{formula:'B2+C2',raw:'12345678.5'}],['002',{raw:'-2.5'},'14','']
    ]}]));
    const select=async(range)=>{
      await p.run(ref=>{nameBox.focus();nameBox.value=ref;nameBox.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true}));},range);
    };
    const key=async(key,code,number)=>{
      for(const type of ['keyDown','keyUp'])await p.call('Input.dispatchKeyEvent',{type,key,code,windowsVirtualKeyCode:number});
    };
    const toggle=letter=>p.run(letter=>[...quickStatsMenu.querySelectorAll('button')].find(n=>n.firstChild.textContent.endsWith('('+letter+')')).click(),letter);
    const getValues=()=>p.evaluate('[...quickStatsMenu.querySelectorAll(".stat-result")].map(n=>n.textContent)');
    await p.file('#fileInput',source);await p.wait(ready);
    await p.run(()=>{
      window.__statReads=0;const original=window.fetch;
      window.fetch=(url,...args)=>{if(String(url).includes('/api/selection/stats'))window.__statReads++;return original(url,...args);};
      Object.defineProperty(navigator,'clipboard',{configurable:true,value:{writeText:async value=>window.__copiedStat=value}});
    });
    await select('B2:C3');
    await p.wait('quickStats.textContent.includes("计数=3") && quickStats.textContent.includes("求和=")');
    const reads=await p.evaluate('window.__statReads');
    await p.click('#quickStatsOptions');await p.wait('!quickStatsMenu.hidden');
    assert.deepEqual(await getValues(),['617万2838','3','2','-2.5','1234万5678.5','1234万5676']);
    await key('o','KeyO',79);await toggle('I');await toggle('M');await toggle('T');
    assert.deepEqual(await getValues(),['6,172,838','3','2','-2.5','12,345,678.5','12,345,676']);
    await toggle('E');
    assert.equal((await getValues()).at(-1),'1234,5676');
    await toggle('E');assert.equal((await getValues()).at(-1),'12345676');
    await toggle('T');
    await p.shot(path.join(out,'menu.png'));
    await key('Escape','Escape',27);
    await p.wait('quickStatsMenu.hidden && document.activeElement===quickStatsOptions');
    await p.click('#quickStats .quick-stat:last-child b');await p.wait('window.__copiedStat==="12345676"');
    assert.equal(await p.evaluate('window.__statReads'),reads);
    report.checks.push('Six exact metrics distinguish native text from numbers; four display formats, keyboard mnemonics and raw copy reuse the same response');
    await p.run(()=>{window.__oldStatsPage=true;});
    await p.call('Page.reload');await p.wait('!window.__oldStatsPage && '+ready);await select('B2:C3');
    await p.wait('quickStats.textContent.includes("求和=12,345,676") && quickStats.children.length===6');
    await p.call('Emulation.setDeviceMetricsOverride',{width:320,height:620,deviceScaleFactor:1.25,mobile:false});
    await p.click('#quickStatsOptions');await p.wait('!quickStatsMenu.hidden');
    const bounds=await p.evaluate('quickStatsMenu.getBoundingClientRect().toJSON()');
    assert(bounds.left>=0 && bounds.right<=320 && bounds.top>=0 && bounds.bottom<=620);
    await key('Escape','Escape',27);
    await p.call('Emulation.setDeviceMetricsOverride',{width:1280,height:780,deviceScaleFactor:1,mobile:false});
    await p.click('#editCopyOpen');await p.wait('document.body.classList.contains("editing-copy") && !editGridSave.disabled');
    const write=async(ref,text,kind)=>{
      await p.click('.grid-cell[data-ref="'+ref+'"]');await p.wait('nameBox.value==='+JSON.stringify(ref)+' && document.activeElement===editGridInput');
      await key('F2','F2',113);await p.wait('editGridInput.classList.contains("writing")');
      await p.run(value=>{editGridInput.select();document.execCommand('insertText',false,value);},text);
      if(kind) await p.run(value=>{editGridKind.value=value;editGridKind.dispatchEvent(new Event('change',{bubbles:true}));},kind);
      await key('Enter','Enter',13);await p.wait('!editGridInput.classList.contains("writing") && !editGridSave.disabled');
    };
    await write('B2','10');await write('C2','7');await select('B2:D3');
    await p.wait('document.querySelector(".grid-region.has-range") && document.querySelector("#quickStats .quick-stat-copy")');
    report.textEdit=await p.run(()=>({summary:quickStats.textContent,status:editGridStatus.textContent,address:nameBox.value,
      grid:['B2','C2','D2','B3','C3'].map(ref=>document.querySelector('.grid-cell[data-ref="'+ref+'"]').textContent)}));
    assert(report.textEdit.summary.includes('求和=24.5') && report.textEdit.summary.includes('数值计数=3'),JSON.stringify(report.textEdit));
    await write('C2','7','number');await select('B2:D3');
    await p.wait('quickStats.textContent.includes("求和=31.5")');
    await p.click('#quickStatsOptions');
    assert.deepEqual(await getValues(),['7.875','5','4','-2.5','17','31.5']);
    await key('Escape','Escape',27);
    await p.click('#editGridUndo');await p.wait('!editGridSave.disabled');
    await select('B2:D3');await p.wait('quickStats.textContent.includes("求和=24.5")');
    await p.click('#editGridExit');await p.wait('!document.body.classList.contains("editing-copy")');
    await select('B2:C3');await p.wait('quickStats.textContent.includes("求和=12,345,676")');
    report.checks.push('Preferences survive reload and the menu fits 320 px; edits to an empty cell, dependent formula recalculation, undo and exit use the correct revision');
    assert.deepEqual(report.errors,[]);report.result='PASS';
  }finally{await h.close();}
}
main().catch(e=>{report.result='FAIL';report.failure=e.stack;process.exitCode=1;}).finally(()=>{
  fs.writeFileSync(path.join(out,'report.json'),JSON.stringify(report,null,2)+'\n');
  console.log(JSON.stringify(report));
});
