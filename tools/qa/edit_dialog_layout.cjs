// Exercise the production editing dialog with a real workbook and reading presets.
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const {launch} = require('./browser_harness.cjs');

async function main() {
  const [source, output, sheetName] = process.argv.slice(2);
  const errors = [], cases = [];
  const h = await launch({errors});
  const p = h.page;
  async function settled(id) {
    await p.run(async id => {
      const animations = document.getElementById(id).getAnimations();
      await Promise.all(animations.map(animation => animation.finished));
    }, id);
  }
  async function captureDialog(id, label) {
    const clip = await p.run(id => {
      const {x,y,width,height} = document.getElementById(id).getBoundingClientRect();
      return {x,y,width,height,scale:1};
    }, id);
    const shot = await p.call('Page.captureScreenshot', {format:'png', clip});
    fs.writeFileSync(output.replace(/\.json$/, '-'+label+'.png'), Buffer.from(shot.data, 'base64'));
  }
  async function choose(tool) {
    if (await p.evaluate('editCopyToolSelect.getClientRects().length > 0')) {
      await p.run(key => {
        editCopyToolSelect.value = key;
        editCopyToolSelect.dispatchEvent(new Event('change', {bubbles:true}));
      }, tool);
    } else {
      await p.click('#editCopyTab'+tool);
    }
  }
  try {
    await p.file('#fileInput', path.resolve(source));
    await p.wait('window.ExcelLensPerformance().hasVisibleCoverage && !editCopyOpen.disabled');
    await p.click('#editCopyOpen');
    await p.wait('document.body.classList.contains("editing-copy") && !editGridMore.disabled');
    if (sheetName) {
      const prematurelyEnabled = await p.run(name => {
        const tab = [...document.querySelectorAll('.sheet-tab')].find(n => n.textContent.trim() === name);
        if (tab.classList.contains('active')) return false;
        tab.click();
        return !editGridMore.disabled;
      }, sheetName);
      assert.equal(prematurelyEnabled, false, 'advanced editing enabled during a sheet change');
      await p.wait('document.querySelector(".sheet-tab.active")?.textContent.trim() === '+JSON.stringify(sheetName)+' && !editGridMore.disabled && ExcelLensPerformance().hasVisibleCoverage');
    }
    const origin = await p.evaluate('document.querySelector(".sheet-tab.active").textContent.trim()+"!"+nameBox.value');
    await p.click('#editGridMore');
    await p.wait('editCopyDialog.open && !editCopyBatch.hidden && !editCopyClose.disabled');
    await settled('editCopyDialog');
    const tools = await p.evaluate('[...editCopyTools.querySelectorAll("[role=tab]:not([hidden])")].map(t=>t.id.slice("editCopyTab".length))');
    for (const [width, height, scale] of [[850,750,1],[560,540,1.25],[360,640,1.5],[320,390,1]]) {
      await p.call('Emulation.setDeviceMetricsOverride', {width, height, deviceScaleFactor:scale, mobile:false});
      for (const preset of ['off','warm','graphite']) {
        await p.run(preset => document.querySelector('input[name="readingPreset"][value="'+preset+'"]').click(), preset);
        for (const tab of tools) {
          await choose(tab);
          const result = await p.run(tab => {
            const pane = document.getElementById(document.getElementById('editCopyTab'+tab).getAttribute('aria-controls'));
            const rect = node => {
              const {x,y,right,bottom,width,height} = node.getBoundingClientRect();
              return {x,y,right,bottom,width,height};
            };
            const scroll = document.querySelector('.edit-copy-scroll');
            const nodes = [editCopyRefresh,editCopyUndo,editCopyRedo,editCopyHistory,editCopySave,
              ...[...pane.querySelectorAll('input:not([type="checkbox"]), select, button')].filter(n => n.getClientRects().length)];
            const actions = nodes.map(node => ({id:node.id, ...rect(node), font:getComputedStyle(node).fontSize}));
            return {active:editCopyTools.querySelector('[aria-selected="true"]').id, panes:[...editCopyBatch.querySelectorAll('[role="tabpanel"]')].filter(n => n.getClientRects().length).length,
              origin:editCopyOriginSheet.textContent+'!'+editCopyOriginRef.textContent, overflow:scroll.scrollWidth-scroll.clientWidth,
              panel:rect(editCopyDialog), footer:rect(document.querySelector('.edit-copy-status')), tone:document.documentElement.dataset.readingTone,
              compact:!!editCopyToolSelect.getClientRects().length, actions};
          }, tab);
          assert.equal(result.active, 'editCopyTab'+tab);
          assert.equal(result.panes, 1);
          assert.equal(result.compact, width <= 360);
          assert.equal(result.origin, origin);
          assert.ok(result.origin.split('!').at(-1));
          assert.ok(result.overflow <= 1 && result.footer.bottom <= height && result.panel.height <= height);
          for (const n of result.actions) {
            assert.ok(n.x >= result.panel.x && n.right <= result.panel.right, n.id+' bounds');
            assert.equal(n.height, 32, n.id+' height');
            assert.equal(n.font,'13px', n.id+' font');
          }
          if (preset === 'graphite') assert.equal(result.tone, 'dark');
          if (tab === 'Paste' && ((width === 850 && preset !== 'warm') || (width === 360 && preset === 'off'))) {
            await p.shot(output.replace(/\.json$/, '-'+width+'-'+preset+'.png'));
          }
          if (width === 850 && preset === 'off' && ['Paste','Structure','Objects','Rules'].includes(tab)) {
            await captureDialog('editCopyDialog', tab);
          }
          cases.push({width,height,scale,preset,tab,...result});
        }
      }
    }
    await p.call('Emulation.setDeviceMetricsOverride',{width:850,height:660,deviceScaleFactor:1,mobile:false});
    await p.evaluate('editCopyTabFill.focus()');
    for (const [key,tab] of [['Home','Copy'],['ArrowRight','Paste'],['End',tools.at(-1)],['ArrowRight','Copy']]) {
      await p.call('Input.dispatchKeyEvent',{type:'keyDown',key});
      await p.call('Input.dispatchKeyEvent',{type:'keyUp',key});
      assert.equal(await p.evaluate('document.activeElement.id'), 'editCopyTab'+tab);
      assert.ok(await p.evaluate('document.activeElement.matches(":focus-visible")'));
    }
    await p.click('#editCopyTabPaste');
    await p.evaluate('editCopyBatchText.value="样本";editCopyBatchText.dispatchEvent(new Event("input"))');
    await p.click('#editCopyPreviewPaste');
    await p.wait('!editCopyBatchPreview.hidden && !editCopyClose.disabled');
    await p.click('#editCopyTabFill');
    assert.ok(await p.evaluate('editCopyBatchPreview.hidden && editCopyPanePaste.hidden'));
    await p.click('#editCopyTabPaste');
    assert.equal(await p.evaluate('editCopyBatchText.value'), '样本');
    await p.click('#editCopyHistory');
    await p.wait('!editCopyListView.hidden && !editCopyClose.disabled');
    assert.equal(await p.evaluate('editCopySave.getClientRects().length'),0);
    await p.click('#editCopyClose');
    await p.click('#editGridExit');
    await p.wait('!document.body.classList.contains("editing-copy")');
    const related = [];
    for (const width of [850,360,320]) {
      await p.call('Emulation.setDeviceMetricsOverride',{width,height:650,deviceScaleFactor:1,mobile:false});
      for (const [trigger,dialog,ids] of [
        ['searchOpenBtn','searchDialog',['#searchBtn','#searchInput','#searchMode','#searchScope','#searchHistoryBtn']],
        ['advancedFilterBtn','filterDialog',['#applyFilterBtn','#addFilterRuleBtn','.filter-column','.filter-operator','.filter-value','.remove-rule']]
      ]) {
        await p.click('#'+trigger);
        await p.wait(dialog+'.open');
        await settled(dialog);
        const items = await p.run((dialog, controls) => controls.map(selector => {
          const n=document.getElementById(dialog).querySelector(selector), r=n.getBoundingClientRect(), css=getComputedStyle(n);
          return {selector,height:r.height,font:css.fontSize,left:r.left,right:r.right};
        }),dialog,ids);
        assert.ok(items.every(v => v.height === 32), dialog+' control height');
        assert.ok(items.filter(v => !['.remove-rule','#searchHistoryBtn'].includes(v.selector)).every(v=>v.font==='13px'), dialog+' control font');
        assert.ok(items.every(v => v.left>=0 && v.right<=width),dialog+' bounds');
        if (dialog === 'filterDialog') {
          assert.ok(await p.evaluate('filterProgressChoice.getBoundingClientRect().right < applyFilterBtn.getBoundingClientRect().left'));
          await p.click('#addFilterRuleBtn');
          assert.ok(await p.evaluate('filterRules.children.length === 2 && filterHelp.getClientRects().length > 0'));
          await p.click('#filterRules .filter-rule:last-child .remove-rule');
          assert.ok(await p.evaluate('filterRules.children.length === 1 && !filterHelp.getClientRects().length'));
        }
        if (width === 850) await captureDialog(dialog, dialog);
        related.push({dialog,width,items});
        await p.click('#'+dialog+' .dialog-close');
      }
    }
    assert.equal(errors.length,0);
    fs.writeFileSync(output, JSON.stringify({result:'PASS', cases, keyboard:true, preview:true, related, errors},null,2)+'\n');
  } finally { await h.close(); }
}
main().catch(error => {console.error(error);process.exitCode=1;});
