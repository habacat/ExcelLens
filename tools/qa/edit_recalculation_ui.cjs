// Run with a private case JSON and an output path. Witness values are the
// formatted results observed in Excel for the same edit, before and after.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {launch} = require('./browser_harness.cjs');

async function configureAxes(p, axes) {
  await p.run(axes => {
    for (const key of ['rows','columns','pages']) {
      const area = editCopyPivotAxes.querySelector('[data-axis="'+key+'"]');
      const desired = (axes[key] || []).map(f=>typeof f==='string'?f:f.field);
      const current = () => [...editCopyPivotAxes.querySelectorAll('[data-axis="'+key+'"] .edit-pivot-field')];
      const names = () => current().map(row=>row.querySelector('select').value);
      for (const name of names()) if (!desired.includes(name)) current()[names().indexOf(name)].querySelector('.edit-pivot-order button:last-child').click();
      for (const name of desired) if (!names().includes(name)) {
        const add = editCopyPivotAxes.querySelector('[data-axis="'+key+'"] .edit-pivot-add');
        add.value = name; add.dispatchEvent(new Event('change',{bubbles:true}));
      }
      desired.forEach((name,index)=>{
        for(let i=names().indexOf(name);i>index;i--)current()[i].querySelector('.edit-pivot-order button').click();
      });
      if (!area || JSON.stringify(names())!==JSON.stringify(desired)) throw Error('Field order '+key);
    }
  },axes);
  for (const [index,field] of (axes.pages||[]).entries()) {
    const box='[data-axis="pages"] .edit-pivot-field:nth-child('+(index+1)+') details';
    await p.click(box+' summary');
    await p.wait('document.querySelector('+JSON.stringify(box+' .edit-pivot-items input')+')');
    await p.click(box+' .edit-pivot-options > button');
    for(const item of (field.items||[])) {
      await p.run((box,value)=>{
        const option=[...document.querySelectorAll(box+' .edit-pivot-items label')].find(l=>l.textContent===value);
        if(!option)throw Error('Page item '+value);
        option.querySelector('input').click();
      },box,item.value);
    }
    await p.click(box+' summary');
  }
}

async function main() {
  const [caseFile, output] = process.argv.slice(2);
  const sample = JSON.parse(fs.readFileSync(caseFile, 'utf8'));
  const report = {result:'FAIL', stages:[], errors:[]};
  const h = await launch({errors:report.errors});
  const p = h.page;
  try {
    const ready = 'window.ExcelLensPerformance().hasVisibleCoverage && !window.ExcelLensPerformance().sheetLoading';
    const revision = n => p.wait(`document.body.classList.contains("editing-copy") && !editGridSave.disabled && editCopyRevision.textContent.startsWith("修订 ${n} ·")`);
    const sheet = async name => {
      await p.run(name => {
        const tab = [...document.querySelectorAll('.sheet-tab')].find(el => el.textContent.trim() === name);
        if (!tab) throw Error('未找到工作表 '+name);
        if (!tab.classList.contains('active')) tab.click();
      }, name);
      await p.wait(ready+' && document.querySelector(".sheet-tab.active")?.textContent.trim() === '+JSON.stringify(name));
    };
    const select = async ref => {
      await p.run(ref => {
        nameBox.focus(); nameBox.value = ref;
        nameBox.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true}));
      }, ref);
      await p.wait('document.querySelector(".grid-cell.selected")?.dataset.ref === '+JSON.stringify(ref));
    };
    const verify = async (stage, expected) => {
      for (const w of sample.witnesses) {
        await sheet(w.sheetAfter && expected === 'after' ? w.sheetAfter : w.sheet); await select(w.ref);
        const value = await p.evaluate('document.querySelector(".grid-cell.selected").textContent.trim()');
        assert.equal(value, w[expected], w.sheet+'!'+w.ref+' '+stage);
        if (w.formula !== false) assert.match(await p.evaluate('editGridFormula.value'), /^=/);
      }
      report.stages.push(stage);
    };
    await p.file('#fileInput',path.resolve(sample.source));
    await p.wait(ready); await p.click('#editCopyOpen'); await revision(1);
    await verify('base','before');
    if (sample.structure) {
      const op = sample.structure;
      await sheet(op.sheet); await select(op.ref);
      if (op.axes) assert.equal(await p.evaluate('editGridMore.textContent'),'透视布局');
      await p.click('#editGridMore');
      await p.wait('editCopyDialog.open && !editCopyClose.disabled');
      const pivot = op.kind === 'configurePivot';
      if(op.axes) {
        assert.ok(await p.evaluate('editCopyTabObjects.getAttribute("aria-selected")==="true" && editCopyTitle.textContent==="透视布局"'));
        assert.equal(await p.evaluate('editCopyPivot.value'),op.pivot);
        report.contextualBody=true;
      } else await p.click(pivot ? '#editCopyTabObjects' : '#editCopyTabStructure');
      if (pivot) {
        await p.run(name=>{editCopyPivot.value=name;editCopyPivot.dispatchEvent(new Event('change',{bubbles:true}));},op.pivot);
        await p.wait('!editCopyPivotFields.hidden && !editCopyPivotFields.disabled');
        if (op.axes) await configureAxes(p,op.axes);
      }
      await p.run(op => {
        function set(id, value) {
          const node = document.getElementById('editCopy'+id);
          node.value = value;
          node.dispatchEvent(new Event(node.tagName === 'SELECT' ? 'change' : 'input', {bubbles:true}));
        }
        if (op.kind === 'configurePivot') {
          if (!op.axes) set('PivotRows',op.rows);
          set('PivotData',op.data);
          set('PivotAggregate',op.aggregate); set('PivotFilter',op.item ? 'item' : 'keep');
          if (op.item) set('PivotItem',op.item);
        } else {
          set('StructureKind',op.kind);
          if (op.name) set('NewName',op.name);
          if (op.at) set('At',op.at);
          if (op.count) set('Count',op.count);
          if (op.before) set('Before',op.before);
        }
      }, op);
      if (op.axes) {
        report.layouts=[];
        for (const [width,height] of [[850,820],[360,640],[320,420]]) {
          await p.call('Emulation.setDeviceMetricsOverride',{width,height,deviceScaleFactor:1,mobile:false});
          const bounds=await p.evaluate('(()=>{const s=document.querySelector(".edit-copy-scroll"),f=editCopyPivotApply.getBoundingClientRect();return {overflow:s.scrollWidth-s.clientWidth,footer:f.bottom,applyVisible:f.width>0&&f.top>=s.getBoundingClientRect().bottom-1,fields:[...editCopyPivotAxes.querySelectorAll(".edit-pivot-field > select")].map(n=>[n.value,n.getBoundingClientRect().left,n.getBoundingClientRect().right])}})()');
          assert.ok(bounds.applyVisible && bounds.overflow<=1 && bounds.footer<=height && bounds.fields.every(f=>f[1]>=0&&f[2]<=width));
          report.layouts.push({width,height,...bounds});
          if (width===850) await p.shot(output.replace(/\.json$/,'-layout.png'));
        }
        await p.call('Emulation.setDeviceMetricsOverride',{width:1200,height:850,deviceScaleFactor:1,mobile:false});
      }
      await p.click(pivot ? '#editCopyPivotApply' : '#editCopyApplyStructure');
      await revision(2);
        if (op.axes) {
          await p.wait('!editCopyDialog.open');
          report.returnedToGrid = true;
        } else if (await p.evaluate('editCopyDialog.open')) await p.click('#editCopyClose');
    } else {
      await sheet(sample.input.sheet); await select(sample.input.ref);
      if (sample.inline) {
        await p.evaluate('document.querySelector(".grid-cell.selected").dispatchEvent(new MouseEvent("dblclick",{bubbles:true}))');
        await p.wait('document.activeElement===editGridInput && editGridInput.classList.contains("writing")');
        await p.evaluate('editGridInput.select()');
      } else {
        await p.evaluate('editGridFormula.focus();editGridFormula.select()');
      }
      await p.call('Input.insertText',{text:sample.input.raw});
      for (const type of ['rawKeyDown','keyUp']) {
        await p.call('Input.dispatchKeyEvent',{type,key:'Enter',code:'Enter',windowsVirtualKeyCode:13});
      }
    }
    await revision(2); await verify('edit','after');
    if(sample.pageRef) {
      await sheet(sample.structure.sheet); await select(sample.pageRef);
      assert.equal(await p.evaluate('editGridMore.textContent'),'透视布局');
      await p.click('#editGridMore');
      await p.wait('editCopyDialog.open && !editCopyClose.disabled && editCopyTabObjects.getAttribute("aria-selected")==="true"');
      assert.equal(await p.evaluate('editCopyPivot.value'),sample.structure.pivot);
      await p.click('#editCopyClose');report.contextualPage=true;
    }
    await p.click('#editGridUndo'); await revision(3); await verify('undo','before');
    await p.click('#editGridRedo'); await revision(4); await verify('redo','after');
    if (sample.locked) {
      await sheet(sample.locked.sheet); await select(sample.locked.ref);
      await p.evaluate('document.querySelector(".grid-cell.selected").dispatchEvent(new MouseEvent("dblclick",{bubbles:true}))');
      await p.wait('editGridStatus.textContent.includes("保护") && !editGridInput.classList.contains("writing")');
      report.stages.push('locked');
    }
    if (sample.inline || sample.structure) {
      await p.click('#editGridSave');
      await p.wait('!editGridDownload.hidden && editGridDownload.hasAttribute("href") && !editGridMore.disabled');
      await p.click('#editGridMore');
      await p.wait('editCopyDialog.open && !editCopyClose.disabled && editCopyOutputs.querySelector("button:enabled")');
      await p.click('#editCopyOutputs button');
      await p.wait('!editCopyDialog.open && !document.body.classList.contains("editing-copy") && '+ready);
      await p.click('#editCopyOpen');
      await revision(1);
      await verify('saveReopen', 'after');
    }
    assert.deepEqual(report.errors, []);
    report.result='PASS';
  } catch (error) {
    report.failure = error.message;
    report.view = await p.evaluate('({dialog:editCopyDialog.open, editing:document.body.classList.contains("editing-copy"), status:editCopyStatus.textContent, grid:editGridStatus.textContent})').catch(() => null);
    throw error;
  } finally {
    try {
      await h.close();
    } catch (cleanup) {
      report.cleanupFailure = cleanup.message;
      if (!report.failure) throw cleanup;
    } finally {
      fs.writeFileSync(output,JSON.stringify(report,null,2)+'\n');
    }
  }
}

main().catch(error=>{console.error(error);process.exitCode=1;});
