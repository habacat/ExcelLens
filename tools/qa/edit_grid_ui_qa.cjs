// Exercise the embedded reader and editor against the same workbook and palette.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {launch} = require('./browser_harness.cjs');
const {workbook} = require('./generated_workbook.cjs');
const {validationVersion} = require('./project_config.cjs');

const out = path.resolve('validation', validationVersion());
const screenshots = path.join(out, 'private', 'edit-toolbar');
const report = {result: 'FAIL', layouts: 0, errors: []};
const frames = 'new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))';

async function checkEditingColors(p) {
  const colors = await p.evaluate('[...document.querySelectorAll("input[name=crosshairColor]")].map(n=>n.value)');
  const cases = [];
  for (const preset of ['off', 'navy']) {
    await p.click('input[name=readingPreset][value='+preset+']');
    for (const color of colors) {
      await p.click('#crosshairColorBtn');
      await p.click('input[name=crosshairColor][value='+color+']');
      const colorsInView = await p.run(() => {
        const selected = document.querySelector('#crosshairPanel input:checked').closest('label');
        return {palette: getComputedStyle(selected.querySelector('.reading-swatch')).backgroundColor,
          checkbox: getComputedStyle(crosshairToggle).accentColor, dot: getComputedStyle(document.querySelector('.crosshair-current-color')).backgroundColor,
          option: getComputedStyle(selected).borderColor, selection: getComputedStyle(document.querySelector('#rangeFrames rect')).fill};
      });
      await p.click('[data-highlight-close]');
      const point = await p.run(() => {
        const r = document.querySelector('.grid-cell[data-ref="A2"]').getBoundingClientRect();
        return {x:r.x+r.width/2, y:r.y+r.height/2};
      });
      for (const clickCount of [1,2]) for (const type of ['mousePressed','mouseReleased']) {
        await p.call('Input.dispatchMouseEvent',{type,button:'left',clickCount,...point});
      }
      await p.wait('editGridInput.classList.contains("writing") && document.activeElement===editGridInput');
      colorsInView.editor = await p.evaluate('getComputedStyle(editGridInput).borderColor');
      assert.equal(new Set(Object.values(colorsInView)).size,1,JSON.stringify({preset,color,colorsInView}));
      cases.push({preset,color,paint:colorsInView.editor});
      for (const type of ['keyDown','keyUp']) await p.call('Input.dispatchKeyEvent',{type,key:'Escape',code:'Escape',windowsVirtualKeyCode:27});
      await p.wait('!editGridInput.classList.contains("writing")');
    }
  }
  return cases;
}

function geometry() {
  const rect = node => {
    const {x, y, width, height, right, bottom} = node.getBoundingClientRect();
    return {x, y, width, height, right, bottom};
  };
  const style = el => {
    const s = getComputedStyle(el);
    return {height: s.height, font: s.fontSize, color: s.color, background: s.backgroundColor,
      border: s.borderTop, radius: s.borderRadius};
  };
  const edit = document.body.classList.contains('editing-copy');
  const byID = id => document.getElementById(id);
  const header = document.querySelector('.appbar');
  const title = document.querySelector('.edit-grid-state > span');
  const field = byID(edit ? 'editGridFormula' : 'formulaText');
  const kind = byID(edit ? 'editGridKind' : 'fontSelect');
  const buttons = [...document.querySelectorAll('.edit-grid-actions > :not([hidden])')];
  return {address: style(byID('nameBox')), field: style(field), kind: style(kind),
    fieldFont: getComputedStyle(field).fontFamily, kindFont: getComputedStyle(kind).fontFamily,
    fields: [byID('nameBox'), field, byID('formulaExpandBtn'), kind].map(rect),
    statusFont: getComputedStyle(byID('editGridStatus')).fontSize, titleFont: getComputedStyle(title).fontSize,
    buttons: buttons.map(rect), actionFont: getComputedStyle(byID('editGridSave')).fontSize,
    status: rect(byID('editGridStatus')), brand: rect(byID('fileBadge')), title: rect(title),
    header: rect(header), inspector: rect(byID('formulaText')), expanded: byID('formulaExpandBtn').ariaExpanded,
    width: innerWidth, documentWidth: document.body.scrollWidth};
}

async function main() {
  fs.mkdirSync(screenshots, {recursive: true});
  const h = await launch({errors: report.errors});
  try {
    const p = h.page;
    const source = path.join(h.storage, '月度费用明细与预算执行情况核对工作表.xlsx');
    fs.writeFileSync(source, workbook([{name: '费用明细', rows: [
      ['机构', '科目', '预算'], ['四川分公司', '业务费用', {raw: '16000'}],
      ['成都分公司', '办公费用', {raw: '2400'}]
    ]}]));
    await p.file('#fileInput', source);
    await p.wait('window.ExcelLensPerformance().hasVisibleCoverage && !window.ExcelLensPerformance().sheetLoading');
    const presets = await p.evaluate('[...document.querySelectorAll("input[name=readingPreset]")].map(input => input.value)');
    assert.ok(presets.includes('off') && presets.includes('warm') && presets.includes('graphite'));
    const palette = value => p.click('input[name=readingPreset][value="' + value + '"]');
    const sizes = [[1600, 1], [960, 1.25], [800, 1], [420, 1.5], [320, 1]];
    const resize = ([width, deviceScaleFactor]) => p.call('Emulation.setDeviceMetricsOverride',
      {width, height: 780, deviceScaleFactor, mobile: false});
    await resize(sizes[0]);
    // Compare the same idle controls. Changing a reading preset animates their
    // borders; a screenshot taken mid-transition is not the settled palette.
    async function settledGeometry(){
      await p.call('Input.dispatchMouseEvent',{type:'mouseMoved',x:0,y:0});
      await p.run(async()=>{
        document.activeElement?.blur();
        await new Promise(requestAnimationFrame);
        const fields=['nameBox','formulaText','editGridFormula','fontSelect','editGridKind'];
        await Promise.allSettled(fields.flatMap(id=>document.getElementById(id).getAnimations()).map(a=>a.finished));
      });
      return p.run(geometry);
    }
    const reading = new Map();
    for (const preset of presets) {
      await palette(preset);
      reading.set(preset,await settledGeometry());
    }
    await palette('off');
    await p.click('#editCopyOpen');
    await p.wait('document.body.classList.contains("editing-copy") && !editGridSave.disabled');
    await p.click('.grid-cell[data-ref="A2"]');
    await p.wait('nameBox.value === "A2" && editGridFormula.value === "四川分公司"');
    report.editingColors = await checkEditingColors(p);
    for (const preset of presets) {
      await palette(preset);
      for (const size of sizes) {
        await resize(size);
        await p.evaluate(frames);
        const ui=await settledGeometry();
        const context = preset + ' at ' + size.join('×');
        const normal = reading.get(preset);
        assert.deepEqual(ui.field, normal.field, context);
        assert.deepEqual(ui.kind, normal.kind, context);
        assert.deepEqual(ui.address, ui.field, context);
        assert.deepEqual(ui.field, ui.kind, context);
        assert.equal(ui.fieldFont, normal.fieldFont, context);
        assert.equal(ui.kindFont, normal.kindFont, context);
        assert.equal(ui.titleFont, ui.statusFont, context);
        assert.equal(ui.titleFont, ui.actionFont, context);
        assert.equal(ui.titleFont, ui.field.font, context);
        if (ui.documentWidth > ui.width) {
          report.overflow = await p.evaluate(`[...document.querySelectorAll("body > *, .workspace > *, .appbar *, .formula-toolbar *, .status-strip *")]
            .filter(el => el.getBoundingClientRect().right > innerWidth + .5)
            .map(el => [el.id || el.className, el.getBoundingClientRect().right])`);
        }
        assert.ok(ui.documentWidth <= ui.width && ui.inspector.height === 0, context + ' width ' + ui.documentWidth);
        assert.ok(ui.brand.width > 0 && ui.status.width > 40, context);
        if (size[0] === 1600) assert.ok(ui.header.height <= 50, context);
        for (const box of [...ui.fields, ...ui.buttons, ui.status, ui.title]) {
          assert.ok(box.width > 0 && box.x >= 0 && box.right <= ui.width + 0.5, context);
        }
        for (const box of ui.buttons) {
          assert.ok(box.height === ui.buttons[0].height && box.bottom <= ui.header.bottom, context);
        }
        for (const [i, box] of ui.fields.entries()) {
          assert.equal(box.y, ui.fields[0].y, context);
          assert.equal(box.height, ui.fields[0].height, context);
          if (i) assert.ok(box.x >= ui.fields[i - 1].right, context);
        }
        assert.ok(ui.fields[1].width >= 72, context);
        report.layouts++;
        if ((preset === 'off' && size[0] === 1600) || (preset === 'graphite' && size[0] === 420)) {
          await p.shot(path.join(screenshots, preset + '-' + size[0] + '.png'));
        }
      }
    }
    await palette('graphite');
    await resize(sizes[0]);
    await p.run(() => document.getElementById('nameBox').focus());
    await p.call('Input.dispatchKeyEvent', {type: 'rawKeyDown', key: 'Tab', code: 'Tab', windowsVirtualKeyCode: 9});
    await p.call('Input.dispatchKeyEvent', {type: 'keyUp', key: 'Tab', code: 'Tab', windowsVirtualKeyCode: 9});
    assert.ok(await p.evaluate('editGridFormula.matches(":focus-visible") && parseFloat(getComputedStyle(editGridFormula).outlineWidth) >= 1'));
    await p.click('#formulaExpandBtn');
    const expanded = await p.run(geometry);
    assert.equal(expanded.expanded, 'true');
    assert.ok(expanded.fields[1].height > expanded.fields[0].height * 2);
    await p.click('#formulaExpandBtn');
    await p.click('#editGridMore');
    await p.wait('editCopyDialog.open && !editCopyClose.disabled');
    assert.ok(await p.evaluate('editCopyDialog.matches(".float-dialog") && parseFloat(getComputedStyle(editCopyDialog).borderRadius) > 0'));
    await p.click('#editCopyClose');
    await p.click('#editGridExit');
    await p.wait('!document.body.classList.contains("editing-copy")');
    assert.equal((await p.run(geometry)).fieldFont, reading.get('graphite').fieldFont);
    assert.equal(await p.evaluate('editGridFormula.hidden && editGridKind.hidden && !editCopyDialog.open'), true);
    assert.deepEqual(report.errors, []);
    Object.assign(report, {result: 'PASS', presets, sizes, focus: 'keyboard', expandCollapse: true,
      advancedDialog: true, returnedToReader: true});
  } finally {
    await h.close();
  }
}

main().catch(error => { report.result = 'FAIL'; report.failure = String(error.stack); process.exitCode = 1; }).finally(() => {
  const target = path.join(out, report.result === 'PASS' ? 'edit-toolbar-browser.json' : 'private/edit-toolbar-failure.json');
  fs.writeFileSync(target, JSON.stringify(report, null, 2) + '\n');
  console.log(JSON.stringify(report));
});
