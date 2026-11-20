// Real browser geometry for the in-cell editor, including the classic
// Windows scrollbar gutter. Pass --baseline to record an older probe.
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const {launch} = require('./browser_harness.cjs');
const {workbook} = require('./generated_workbook.cjs');

function metrics() {
  const input=editGridInput, s=getComputedStyle(input);
  const grid=document.getElementById('grid'), g=grid.getBoundingClientRect(), e=input.getBoundingClientRect();
  const cell=[...document.querySelectorAll('.grid-cell.selected')].find(n=>n.getBoundingClientRect().width>0);
  const c=cell.getBoundingClientRect(), border=parseFloat(s.borderLeftWidth)+parseFloat(s.borderRightWidth);
  return {width:e.width,height:e.height,cellWidth:c.width,cellHeight:c.height,gutter:input.offsetWidth-input.clientWidth-border,
    scrollbar:s.scrollbarWidth,pseudo:getComputedStyle(input,'::-webkit-scrollbar').display,
    scrollHeight:input.scrollHeight,clientHeight:input.clientHeight,scrollTop:input.scrollTop,
    sameText:input.value===editGridFormula.value,caret:input.selectionEnd,length:input.value.length,
    visible:e.left>=g.left-1 && e.top>=g.top-1 && e.right<=g.left+grid.clientWidth+1 && e.bottom<=g.top+grid.clientHeight+1,
    sheetScrollHeight:grid.scrollHeight,sheetScrollWidth:grid.scrollWidth};
}

async function main() {
  const [directory,mode,realFile,realSheet,realRef] = process.argv.slice(2);
  fs.mkdirSync(directory,{recursive:true});
  const baseline=mode==='--baseline', report={result:'FAIL',baseline,errors:[],cases:{}};
  const h=await launch({errors:report.errors}),p=h.page;
  const frames = 'new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))';
  const key=async (key,code,modifiers=0)=>{
    for (const type of ['rawKeyDown','keyUp']) await p.call('Input.dispatchKeyEvent',{type,key,windowsVirtualKeyCode:code,modifiers});
  };
  const select=async ref=>{
    await p.run(ref=>{nameBox.focus();nameBox.value=ref;nameBox.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true}));},ref);
    await p.wait('document.activeElement===editGridInput && nameBox.value==='+JSON.stringify(ref)+' && !editGridSave.disabled');
  };
  const enter=async text=>{
    await p.evaluate('editGridInput.select()');
    await p.call('Input.insertText',{text});
    await p.evaluate(frames);
    return p.run(metrics);
  };
  try {
    const file=path.join(h.storage,'公式录入.xlsx');
    fs.writeFileSync(file,workbook([{name:'录入',rows:Array.from({length:22},(_,i)=>[String(i+1),{raw:'7'},'原文'])}]));
    await p.file('#fileInput',file);
    await p.wait('window.ExcelLensPerformance().hasVisibleCoverage && !window.ExcelLensPerformance().sheetLoading');
    await p.click('#editCopyOpen');
    await select('B2');
    await key('F2',113); await p.wait('editGridInput.classList.contains("writing")');
    const short=report.cases.short=await enter('7');
    const formula=report.cases.formula=await enter('=SUM(L5:L3500)');
    await p.shot(path.join(directory,'formula.png'));
    const clip=await p.evaluate('(()=>{const r=editGridInput.getBoundingClientRect();return {x:Math.max(0,r.x-14),y:Math.max(0,r.y-12),width:Math.min(180,innerWidth-r.x),height:r.height+24,scale:2};})()');
    const image=await p.call('Page.captureScreenshot',{format:'png',clip});
    fs.writeFileSync(path.join(directory,'formula-detail.png'),Buffer.from(image.data,'base64'));
    if (baseline) {
      assert.ok(formula.scrollHeight>formula.clientHeight || formula.gutter>0);
      report.result='REPRODUCED'; return;
    }
    assert.ok(formula.width>short.cellWidth && formula.scrollHeight<=formula.clientHeight+1);
    assert.ok(Math.abs(short.width-short.cellWidth)<=1);
    await key('Enter',13,1);
    await p.call('Input.insertText',{text:'+1'});await p.evaluate(frames);
    const multi=report.cases.multiline=await p.run(metrics);
    assert.equal(await p.evaluate('editGridInput.value'),'=SUM(L5:L3500)\n+1');
    assert.ok(multi.height>short.height && multi.scrollHeight<=multi.clientHeight+1);
    await key('Escape',27);await p.wait('editGridFormula.value==="7"');
    await p.call('Emulation.setDeviceMetricsOverride',{width:360,height:520,deviceScaleFactor:1.25,mobile:false});
    await select('D12');await key('F2',113);
    await p.wait('editGridInput.classList.contains("writing")');
    const long=report.cases.viewport=await enter(('中文与空格  软件费用\t'+ 'x'.repeat(25)+'\n').repeat(80));
    await key('End',35,2); await p.evaluate(frames);
    const end=report.cases.caret=await p.run(metrics);
    assert.ok(long.visible && end.scrollHeight>end.clientHeight && end.scrollTop>0 && end.caret===end.length);
    await p.click('#formulaExpandBtn'); await p.evaluate(frames);
    assert.equal(await p.evaluate('getComputedStyle(editGridFormula).scrollbarWidth'),'none');
    await p.evaluate('editGridInput.focus()'); await key('Escape',27); await p.click('#formulaExpandBtn');
    await p.call('Emulation.setDeviceMetricsOverride',{width:1000,height:700,deviceScaleFactor:1,mobile:false});
    await select('B2');await key('F2',113);await p.wait('editGridInput.classList.contains("writing")');
    await enter('=SUM(1,2)');await key('Enter',13);
    await p.wait('editCopyRevision.textContent.startsWith("修订 2 ·") && !editGridSave.disabled');
    await select('B2'); await key('F2',113); await p.wait('editGridInput.value==="=SUM(1,2)"');
    await key('Escape',27);
    await p.click('#editGridExit');await p.wait('!document.body.classList.contains("editing-copy")');
    await p.click('#freezeBtn');await p.wait('freezeDialog.open');
    await p.evaluate('freezeRowsInput.value=2;freezeColsInput.value=2;freezeApplyBtn.click()');
    await p.wait('!freezeDialog.open && !freezeCorner.hidden');
    await p.click('#editCopyOpen');await select('A1');await key('F2',113);
    await p.wait('editGridInput.classList.contains("writing")');
    const frozen=report.cases.frozen=await enter('=SUM(B1:B10000)+SUM(C10:C25)');
    assert.ok(frozen.width>frozen.cellWidth && frozen.visible);
    if (mode==='--real') {
      await key('Escape',27);
      await p.click('#editGridExit');
      await p.wait('!document.body.classList.contains("editing-copy")');
      await p.file('#fileInput',path.resolve(realFile));
      await p.wait('fileBadge.textContent==='+JSON.stringify(path.basename(realFile))+' && window.ExcelLensPerformance().hasVisibleCoverage && !window.ExcelLensPerformance().sheetLoading');
      await p.run(name=>[...document.querySelectorAll('.sheet-tab')].find(n=>n.textContent.trim()===name).click(),realSheet);
      await p.wait('document.querySelector(".sheet-tab.active").textContent.trim()==='+JSON.stringify(realSheet)+' && !window.ExcelLensPerformance().sheetLoading');
      await p.click('#editCopyOpen');await select(realRef);await key('F2',113);
      await p.wait('editGridInput.value.startsWith("=") && editGridInput.classList.contains("writing")');
      const saved = await p.evaluate('editGridInput.value');
      await p.evaluate(frames);
      report.cases.realFormula=await p.run(metrics);
      await p.call('Input.imeSetComposition',{text:'费用',selectionStart:2,selectionEnd:2});
      await p.call('Input.insertText',{text:'费用'});await p.evaluate(frames);
      assert.ok(await p.evaluate('editGridInput.value.includes("费用") && editGridInput.value===editGridFormula.value'));
      await key('Escape',27);await p.wait('!editGridInput.classList.contains("writing")');
      assert.equal(await p.evaluate('editGridFormula.value'),saved);
    }
    for (const c of Object.values(report.cases)) {
      assert.ok(c.visible && c.sameText && c.gutter===0 && c.scrollbar==='none' && c.pseudo==='none',JSON.stringify(c));
    }
    assert.deepEqual(report.errors,[]);report.result='PASS';
  } catch (error) {
    report.failure=String(error.stack);
    report.state=await p.evaluate('({focus:document.activeElement.id,ref:nameBox.value,draft:editGridInput.className,status:editGridStatus.textContent})').catch(()=>null);
    throw error;
  } finally {
    fs.writeFileSync(path.join(directory,'input.json'),JSON.stringify(report,null,2)+'\n');
    await h.close();
  }
}
main().catch(error=>{console.error(error);process.exitCode=1;});
