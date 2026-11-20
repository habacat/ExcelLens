// Drive the embedded grid through its public UI. Private cases supply the
// workbook, target sheet, actions and the revisions for Excel to reopen.
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const {launch} = require('./browser_harness.cjs');

async function main() {
  const [input, output] = process.argv.slice(2);
  const directory = path.resolve(output);
  const spec = JSON.parse(fs.readFileSync(input,'utf8'));
  const errors = [], operations = [];
  const h = await launch({errors});
  const p = h.page;
  const ready = '!editGridSave.disabled && document.activeElement === editGridInput';
  const revision = n => '!editGridSave.disabled && editCopyRevision.textContent.startsWith("修订 '+n+' ·")';
  async function key(name,modifiers=0) {
    const code = {Enter:13,Delete:46,Escape:27,Tab:9,F10:121,ArrowDown:40,ArrowRight:39,ArrowLeft:37,ArrowUp:38}[name] || name.toUpperCase().charCodeAt(0);
    for (const type of ['rawKeyDown','keyUp']) await p.call('Input.dispatchKeyEvent',{type,key:name,windowsVirtualKeyCode:code,modifiers});
  }
  async function select(ref) {
    await p.run(ref=>{
      nameBox.focus(); nameBox.value=ref;
      nameBox.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true}));
    },ref);
    await p.wait(ready+' && nameBox.value === '+JSON.stringify(ref));
  }
  async function sheet(name) {
    await p.run(name=>[...document.querySelectorAll('.sheet-tab')].find(n=>n.textContent.trim()===name).click(),name);
    await p.wait('document.querySelector(".sheet-tab.active").textContent.trim() === '+JSON.stringify(name)+' && !window.ExcelLensPerformance().sheetLoading && window.ExcelLensPerformance().hasVisibleCoverage');
  }
  try {
    await p.file('#fileInput',spec.file);
    await p.wait('window.ExcelLensPerformance().hasVisibleCoverage && !window.ExcelLensPerformance().sheetLoading');
    await sheet(spec.sheet);
    await p.click('#editCopyOpen');
    await p.wait(ready);
    await p.call('Page.bringToFront');
    await p.call('Emulation.setFocusEmulationEnabled',{enabled:true});
    await p.call('Browser.grantPermissions',{origin:new URL(h.address).origin,permissions:['clipboardReadWrite','clipboardSanitizedWrite']});
    for (const [index,step] of spec.steps.entries()) {
      if (step.reopen) {
        await p.file('#fileInput',path.join(directory,step.reopen+'.xlsx'));
        await p.wait('!document.body.classList.contains("editing-copy") && window.ExcelLensPerformance().hasVisibleCoverage && !window.ExcelLensPerformance().sheetLoading');
        await p.click('#editCopyOpen');
        await p.wait(ready);
      }
      if (step.width) await p.call('Emulation.setDeviceMetricsOverride',{width:step.width,height:760,deviceScaleFactor:1,mobile:false});
      if (step.sheet) await sheet(step.sheet);
      if (step.range) await select(step.range);
      if (step.text !== undefined) await p.call('Input.insertText',{text:step.text});
      if (step.barText !== undefined) {
        await p.run(()=>{editGridFormula.focus();editGridFormula.select();});
        await p.call('Input.insertText',{text:step.barText});
      }
      if (step.copy || step.cut) {
        await key(step.cut?'x':'c',2);
        await p.wait('(async()=>{for(const item of await navigator.clipboard.read()) if(item.types.includes("text/html") && (await (await item.getType("text/html")).text()).includes("excellens-copy-")) return true; return false})()');
      }
      if (step.paste !== undefined) {
        await p.run(text=>navigator.clipboard.writeText(text),step.paste);
        await key('v',2);
      }
      if (step.key) await key(step.key,step.mods||0);
      for (const k of step.keys || []) await key(k);
      if (step.menu) {
        if (step.pointer) {
          const point=await p.run(ref=>{
            const target=[...document.querySelectorAll('.grid-region .grid-cell')].find(n=>n.dataset.ref===ref && n.offsetWidth);
            const r=target.getBoundingClientRect(); return {x:r.left+3,y:r.top+3};
          },step.pointer);
          for (const type of ['mousePressed','mouseReleased']) await p.call('Input.dispatchMouseEvent',{type,...point,button:'right',clickCount:1});
        } else await key('F10',8);
        await p.wait('!editGridMenu.hidden && editGridMenu.contains(document.activeElement)');
        assert.ok(await p.evaluate('(()=>{const r=editGridMenu.getBoundingClientRect();return r.width>100 && r.left>=0 && r.top>=0 && r.right<=innerWidth && r.bottom<=innerHeight})()'));
        if (step.shot) await p.shot(path.join(directory,step.shot+'.png'));
        await p.click('#editGridMenu [data-edit-action="'+step.menu+'"]');
      }
      if (step.resize) {
        await p.wait('editCopyDialog.open && editCopyStructureKind.value==="resizeTable" && !editCopyTableName.disabled && editCopyRange.value.includes(":")');
        if (step.resizeShot) {
          await p.wait('document.activeElement===editCopyRange');
          await p.shot(path.join(directory,step.resizeShot+'.png'));
        }
        if (step.table) assert.equal(await p.evaluate('editCopyTableName.value'),step.table);
        await p.run(ref=>{editCopyRange.value=ref;editCopyRange.dispatchEvent(new Event('input',{bubbles:true}));},step.resize);
        await p.click('#editCopyApplyStructure');
        if (step.rejected) {
          await p.wait('editCopyStatus.textContent.includes('+JSON.stringify(step.rejected)+')');
          await p.click('#editCopyClose');
        } else await p.wait('!editCopyDialog.open && !editGridSave.disabled');
      }
      if (step.list) {
        await p.wait('!editGridListButton.hidden');
        if (step.click) await p.click('#editGridListButton');
        else await key('ArrowDown',1);
        await p.wait('!editGridList.hidden && document.activeElement===editGridList');
        assert.deepEqual(await p.evaluate('Array.from(editGridList.options,o=>o.value)'),step.list);
        const bounds = await p.evaluate('(()=>{const r=editGridList.getBoundingClientRect(),g=document.querySelector(".grid-region").getBoundingClientRect();return r.x>=g.x && r.y>=g.y && r.right<=g.right+1 && r.bottom<=g.bottom+1})()');
        assert.ok(bounds,'list inside worksheet');
        if (step.shot) await p.shot(path.join(directory,step.shot+'.png'));
        if (step.pick !== undefined) {
          await p.run(v=>{editGridList.value=v;},step.pick);
          await key('Enter');
        } else await key('Escape');
      }
      if (step.cut || step.menu==='cut') await p.wait('document.querySelector(".grid-cell.cut-source") && editGridStatus.textContent.includes("Esc")');
      if (step.noCut) await p.wait('!document.querySelector(".grid-cell.cut-source")');
      if (step.rev) await p.wait(revision(step.rev));
      if (step.formula!==undefined) await p.wait('editGridFormula.value==='+JSON.stringify(step.formula));
      if (step.fullDisplay!==undefined) {
        const full=await p.evaluate('(()=>{const n=document.querySelector(".grid-cell.selected");return (n?._xlNumericOriginal?.textContent??n?.textContent??"").trim()})()');
        assert.equal(full,step.fullDisplay,step.range || step.ref);
      }
      if (step.display!==undefined) {
        const text=await p.evaluate('document.querySelector(".grid-cell.selected").textContent.trim()');
        assert.equal(text,step.display,step.range || step.ref);
      }
      if (step.overflow) assert.match(await p.evaluate('document.querySelector(".grid-cell.selected").textContent.trim()'),/^#+$/);
      if (step.noList) await p.wait('editGridListButton.hidden');
      if (step.ref) await p.wait('document.querySelector(".grid-cell.selected")?.dataset.ref==='+JSON.stringify(step.ref));
      if (step.error) await p.wait('editGridStatus.classList.contains("is-error") && editGridStatus.textContent.includes('+JSON.stringify(step.error)+')');
      if (step.save) {
        await p.click('#editGridSave');
        await p.wait('!editGridDownload.hidden && !editGridSave.disabled');
        const url = await p.evaluate('editGridDownload.href');
        const headers = await p.run(()=>Object.fromEntries(window.ExcelLensSession.apply(new Headers({'X-ExcelLens-Token':document.querySelector('meta[name=app-token]').content}))));
        const res = await fetch(url,{headers});
        assert.equal(res.status,200);
        fs.writeFileSync(path.join(directory,step.save+'.xlsx'),Buffer.from(await res.arrayBuffer()));
        await p.evaluate('editGridInput.focus()');
      }
      operations.push(index+1);
    }
    assert.deepEqual(errors,[]);
    console.log(JSON.stringify({operations:operations.length,errors}));
  } catch (error) {
    const diagnostic=await p.evaluate('({address:nameBox.value,focus:document.activeElement.id,revision:editCopyRevision.textContent,status:editGridStatus.textContent,formula:editGridFormula.value,dropdown:!editGridListButton.hidden})');
    diagnostic.cell=await p.run(()=>{
      const n=document.querySelector('.grid-cell.selected');
      if(!n)return null;
      const css=getComputedStyle(n),ctx=document.createElement('canvas').getContext('2d');
      ctx.font=css.fontWeight+' '+css.fontSize+' '+css.fontFamily;
      return {text:n.textContent,model:n._xlModel?.display,width:n.getBoundingClientRect().width,
        padding:[css.paddingLeft,css.paddingRight],font:ctx.font,textWidth:ctx.measureText(n._xlModel?.display||'').width};
    });
    fs.writeFileSync(path.join(directory,'failure.json'),JSON.stringify({next:operations.length+1,diagnostic,errors,message:error.message},null,2));
    throw Error(JSON.stringify({next:operations.length+1,diagnostic,errors})+'\n'+error.stack);
  } finally {
    try { await h.close(); }
    catch (error) {
      if (error.code!=='EPERM' || process.platform!=='win32') throw error;
      console.warn('Windows retained the isolated test profile:',h.storage);
    }
  }
}
main().catch(error=>{console.error(error);process.exitCode=1;});
