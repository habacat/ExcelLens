// Exercise formula entry with browser pointer and keyboard events, then export
// the actual revisions for independent inspection in Excel.
const fs = require('node:fs'), path = require('node:path'), assert = require('node:assert/strict');
const {launch} = require('./browser_harness.cjs');

async function run(spec, out) {
  fs.mkdirSync(out,{recursive:true});
  const errors=[], steps=[], h=await launch({errors,duration:'20m'}), p=h.page;
  const ready='document.body.classList.contains("editing-copy") && !editGridSave.disabled';
  async function key(key, modifiers=0) {
    const codes={Enter:13,Escape:27,Tab:9,F2:113,F4:115,ArrowUp:38,ArrowDown:40,ArrowLeft:37,ArrowRight:39};
    for (const type of ['rawKeyDown','keyUp']) await p.call('Input.dispatchKeyEvent',
      {type,key,modifiers,windowsVirtualKeyCode:codes[key] || key.toUpperCase().charCodeAt(0)});
  }
  const type=text=>p.call('Input.insertText',{text});
  async function sheet(name) {
    await p.run(name=>[...document.querySelectorAll('.sheet-tab')].find(b=>b.textContent===name).click(),name);
    await p.wait('window.ExcelLensPerformance().hasVisibleCoverage && !window.ExcelLensPerformance().sheetLoading && document.querySelector(".sheet-tab.active").textContent === '+JSON.stringify(name));
  }
  async function select(ref) {
    await p.run(ref=>{nameBox.focus();nameBox.value=ref;nameBox.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true}));},ref);
    await p.wait(ready+' && document.activeElement===editGridInput && nameBox.value==='+JSON.stringify(ref));
  }
  async function point(ref) {
    return p.run(ref=>{
      const selector='.grid-cell[data-ref="'+ref+'"]';
      for (const node of document.querySelectorAll(selector)) {
        const r=node.getBoundingClientRect(), x=(r.left+r.right)/2, y=(r.top+r.bottom)/2;
        const found=document.elementsFromPoint(x,y).find(n=>n.closest('.grid-cell'));
        if (found?.closest('.grid-cell')===node) return {x,y};
      }
      throw Error('Cell is not visible: '+ref);
    },ref);
  }
  async function mouse(type,pt,buttons,mods=0,n=1) {
    await p.call('Input.dispatchMouseEvent',{type,...pt,button:'left',buttons,modifiers:mods,clickCount:n});
  }
  async function pick(ref,to=ref,mods=0) {
    const a=await point(ref), b=await point(to);
    await mouse('mousePressed',a,1,mods);
    if (to!==ref) await mouse('mouseMoved',b,1,mods);
    await mouse('mouseReleased',b,0,mods);
  }
  async function value(text) {
    const data=await p.evaluate('({formula:editGridFormula.value,cell:editGridInput.value,status:editGridStatus.textContent})');
    assert.equal(data.formula,text,JSON.stringify(data));
    assert.equal(data.cell,text);
  }
  async function save(rev,file) {
    await p.wait(ready+' && editCopyRevision.textContent.startsWith("修订 '+rev+' ·")');
    await p.click('#editGridSave');
    await p.wait('!editGridDownload.hidden && !editGridSave.disabled');
    const data=await p.run(async()=>{
      const headers=window.ExcelLensSession.apply(new Headers({'X-ExcelLens-Token':document.querySelector('meta[name=app-token]').content}));
      return {url:editGridDownload.href,headers:Object.fromEntries(headers)};
    });
    const res=await fetch(data.url,{headers:data.headers});
    assert.equal(res.status,200);
    fs.writeFileSync(path.join(out,file+'.xlsx'),Buffer.from(await res.arrayBuffer()));
    steps.push(file);
  }
  try {
    if (spec.width) await p.call('Emulation.setDeviceMetricsOverride',{width:spec.width,height:720,deviceScaleFactor:1,mobile:false});
    await p.file('#fileInput',spec.file);
    await p.wait('window.ExcelLensPerformance().hasVisibleCoverage && !window.ExcelLensPerformance().sheetLoading');
    await sheet(spec.sheet); await p.click('#editCopyOpen'); await p.wait(ready);
    await p.call('Emulation.setFocusEmulationEnabled',{enabled:true});
    await select(spec.target);
    await type('=SUM');
    await p.wait('!editFormulaSuggestions.hidden && editFormulaSuggestions.querySelector("[aria-selected=true]")?.textContent==="SUM"');
    const offered=await p.evaluate('Array.from(editFormulaSuggestions.children,o=>o.textContent)');
    assert.ok(offered.includes('SUMIF')&&offered.includes('SUMPRODUCT'));
    assert.ok(await p.evaluate('(()=>{const r=editFormulaPopup.getBoundingClientRect();return r.width>0&&r.left>=0&&r.right<=innerWidth&&r.top>=0&&r.bottom<=innerHeight})()'),'completion stays inside the viewport');
    await p.shot(path.join(out,'suggestions.png'));
    if (spec.mouseCompletion) {
      const pos=await p.run(()=>{const r=editFormulaSuggestions.querySelector('[aria-selected=true]').getBoundingClientRect();return {x:r.x+r.width/2,y:r.y+r.height/2};});
      await mouse('mousePressed',pos,1);await mouse('mouseReleased',pos,0);
    } else await key('Tab');
    await value('=SUM(');
    await p.wait('!editFormulaHint.hidden && editFormulaHint.querySelector("strong")');
    await pick(spec.start,spec.end); await value('=SUM('+spec.start+':'+spec.end);
    assert.ok(await p.evaluate('formulaEntryFrames.children.length'));
    await type(','); await sheet(spec.other); steps.push('origin draft kept across sheets');
    await pick(spec.ref);
    const prefix='=SUM('+spec.start+':'+spec.end+','+spec.qualifier+'!';
    await value(prefix+spec.ref);
    const [col,row]=spec.ref.match(/^([A-Z]+)(\d+)$/).slice(1);
    for (const expected of ['$'+col+'$'+row,col+'$'+row,'$'+col+row,col+row,'$'+col+'$'+row]) {
      await key('F4');await value(prefix+expected);
    }
    await key('Enter'); await save(2,'changed');
    assert.equal(await p.evaluate('document.querySelector(".sheet-tab.active").textContent'),spec.sheet);
    await p.click('#editGridUndo');await save(3,'undo');
    await p.click('#editGridRedo');await save(4,'redo');
    await select(spec.target);
    assert.equal(await p.evaluate('editGridFormula.value'),prefix+'$'+col+'$'+row+')');
    if (spec.extra) {
      // Formula bar and double-click/F2 entry use the same draft target.
      await p.run(()=>{editGridFormula.focus();editGridFormula.select();});
      await type('='); await pick('B2');await value('=B2');await pick('B3');await value('=B3');
      await key('z',2);await value('=B2');await key('y',2);await value('=B3');
      await pick('B3');
      await pick('C4','C4',8);await value('=B3:C4');
      await sheet(spec.other);await pick('C3');await key('Escape');await p.wait(ready+' && nameBox.value==='+JSON.stringify(spec.target)+' && !editGridInput.classList.contains("writing")');
      await select('E7');await type('=');await key('ArrowLeft');await value('=D7');
      await key('ArrowRight',8);await value('=D7:E7');await key('Escape');
      await select('D4');
      const pt=await point('D4');await mouse('mousePressed',pt,1,0,2);await mouse('mouseReleased',pt,0,0,2);
      await key('a',2);await type('=');await pick('B3','C4');await value('=B3:C4');await key('Escape');
      await select('E6');await type('="SU');await p.wait('editFormulaPopup.hidden');
      await key('Escape');await type('=SU');await p.wait('!editFormulaSuggestions.hidden');
      await key('ArrowDown');const chosen=await p.evaluate('editFormulaSuggestions.querySelector("[aria-selected=true]").textContent');
      await key('Tab');await value('='+chosen+'(');await key('Escape');
      await select('F8');await type('=SUM(');
      const header=await p.run(()=>{const r=document.querySelector('.column-head[data-col="2"]').getBoundingClientRect();return {x:r.x+r.width/2,y:r.y+r.height/2};});
      await mouse('mousePressed',header,1);await mouse('mouseReleased',header,0);await value('=SUM(B:B');
      await type(',');await pick('B3');await pick('C4','C4',2);await value('=SUM(B:B,B3,C4');
      await key('Escape');
      await select('B3:D4');await type('=');await sheet(spec.other);await pick('A1');await key('F4');
      await key('Enter',2);await p.wait(ready+' && editCopyRevision.textContent.startsWith("修订 5 ·")');
      for (const ref of ['B3','C3','D4']) {
        await select(ref);assert.equal(await p.evaluate('editGridFormula.value'),'='+spec.qualifier+'!$A$1');
      }
      await p.click('#editGridUndo');await p.wait(ready+' && editCopyRevision.textContent.startsWith("修订 6 ·")');
      steps.push('draft undo/redo, whole column and disjoint references, cross-sheet Ctrl+Enter');
      steps.push('formula bar, repeated and shift pick, keyboard range, double-click, quotes, function navigation, cancel');
    }
    await p.file('#fileInput',path.join(out,'redo.xlsx'));
    await p.wait('!document.body.classList.contains("editing-copy") && window.ExcelLensPerformance().hasVisibleCoverage');
    await sheet(spec.sheet);await p.click('#editCopyOpen');await p.wait(ready);await select(spec.target);
    assert.equal(await p.evaluate('editGridFormula.value'),prefix+'$'+col+'$'+row+')');
    steps.push('reopened formula'); assert.deepEqual(errors,[]);
  } catch (error) {
    const state=await p.evaluate('({formula:editGridFormula.value,status:editGridStatus.textContent,ref:nameBox.value,sheet:document.querySelector(".sheet-tab.active")?.textContent,focus:document.activeElement?.id,rev:editCopyRevision.textContent})').catch(()=>null);
    console.error(state);throw error;
  } finally {
    fs.writeFileSync(path.join(out,'browser.json'),JSON.stringify({steps,errors},null,2));
    try { await h.close(); } catch(e) { if(e.code!=='EPERM')throw e; }
  }
}
run(JSON.parse(fs.readFileSync(process.argv[2],'utf8')),path.resolve(process.argv[3])).catch(e=>{console.error(e);process.exitCode=1;});
