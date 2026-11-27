// Production command bar: tabs, overflow, folding and command actions,
// measured in a real browser. This is not manual desktop acceptance.
const fs = require('node:fs'), path = require('node:path'), assert = require('node:assert/strict'), crypto = require('node:crypto');
const config = require('./project_config.cjs'), {launch} = require('./browser_harness.cjs'), {zip} = require('./generated_workbook.cjs');
const root = path.resolve(__dirname, '../..'), phase = process.env.QA_TOOLBAR_PHASE || 'after';
assert(['before', 'after'].includes(phase));
const out = path.join(root, 'validation', config.validationVersion(), 'private', 'toolbar', phase);
fs.mkdirSync(out, {recursive: true});

const source = path.join(out, '较长的工作簿名称用于验证窄屏文件标识.xlsx');
const ns = 'http://schemas.openxmlformats.org/spreadsheetml/2006/main', rel = 'http://schemas.openxmlformats.org/officeDocument/2006/relationships';
const sheets = Array.from({length: 80}, (_, i) => '工具测试' + String(i + 1).padStart(2, '0'));
let rows = '';
for (let row = 1; row <= 40; row++) rows += `<row r="${row}"><c r="A${row}" t="inlineStr"><is><t>记录${row}</t></is></c><c r="B${row}"><v>${row}.25</v></c></row>`;
const files = {
  'xl/workbook.xml': `<workbook xmlns="${ns}" xmlns:r="${rel}"><sheets>${sheets.map((s, i) => `<sheet name="${s}" sheetId="${i + 1}" r:id="s${i + 1}"/>`).join('')}</sheets></workbook>`,
  'xl/_rels/workbook.xml.rels': `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">${sheets.map((_, i) => `<Relationship Id="s${i + 1}" Type="${rel}/worksheet" Target="worksheets/sheet${i + 1}.xml"/>`).join('')}</Relationships>`
};
for (let i = 0; i < sheets.length; i++) files[`xl/worksheets/sheet${i + 1}.xml`] = `<worksheet xmlns="${ns}"><sheetData>${rows}</sheetData></worksheet>`;
fs.writeFileSync(source, zip(files));
const hash = file => crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex'), before = hash(source);

const report = {version: config.version(), phase, result: 'FAIL', checks: [], cases: [], errors: [], screenshots: [], desktopManualAcceptance: false, officeWpsAcceptance: false};
const ready = 'window.ExcelLensPerformance?.().hasVisibleCoverage&&!window.ExcelLensPerformance().sheetLoading';
const core = ['openBtn', 'searchOpenBtn', 'filterOpenBtn', 'multiSortBtn', 'exportOpenBtn', 'analysisOpenBtn'];
let h;
const settled = () => h.page.evaluate('new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))');
async function shot(name) {
  const output = path.join(out, name + '.png');
  await h.page.shot(output);
  report.screenshots.push(path.relative(root, output).replaceAll('\\', '/'));
}
async function key(key, code, number, modifiers = 0) {
  await h.page.call('Input.dispatchKeyEvent', {type: 'keyDown', key, code, windowsVirtualKeyCode: number, modifiers});
  await h.page.call('Input.dispatchKeyEvent', {type: 'keyUp', key, code, windowsVirtualKeyCode: number, modifiers});
}
async function palette() {
  await h.page.click('#commandOpenBtn');
  await h.page.wait('document.getElementById("commandDialog").open');
}
async function command(button) {
  await palette();
  await h.page.run(button => {
    const action = [...document.querySelectorAll('#commandList .command-entry')].find(n => n.dataset.commandButton === button);
    if (!action || action.disabled) throw Error('Unavailable command ' + button);
    action.click();
  }, button);
}
const resize = (width, height, dpr) => h.page.call('Emulation.setDeviceMetricsOverride', {width, height, deviceScaleFactor: dpr, mobile: false});
// Geometry of everything a pointer can reach in the bar at this moment.
const measure = () => h.page.run(() => {
  const bar = document.querySelector('.appbar'), bounds = bar.getBoundingClientRect();
  const box = node => {
    const r = node.getBoundingClientRect(), hit = document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2), icon = node.querySelector('.icon')?.getBoundingClientRect();
    return {id: node.id, left: r.left, right: r.right, top: r.top, bottom: r.bottom, height: r.height, font: parseFloat(getComputedStyle(node).fontSize), icon: icon?.width || 0, hit: hit?.closest('button')?.id};
  };
  const shown = selector => [...bar.querySelectorAll(selector)].filter(n => n.getBoundingClientRect().width > 0).map(box);
  const formula = document.querySelector('.formula-toolbar');
  const controls = [...formula.querySelectorAll('input,select,button,[role="textbox"]')].filter(n => n.getBoundingClientRect().width > 0).map(n => { const r = n.getBoundingClientRect(); return {id: n.id, left: r.left, right: r.right}; });
  const name = document.getElementById('fileBadge').getBoundingClientRect();
  return {
    width: innerWidth, bar: {left: bounds.left, right: bounds.right, height: bounds.height},
    commands: shown('.app-actions .ribbon-panel:not([hidden]) > button, #ribbonMoreBtn'), tabs: shown('.ribbon-tabs button'), title: shown('.titlebar > button'),
    folded: [...bar.querySelectorAll('.ribbon-panel:not([hidden]) > [data-toolbar-overflow]')].map(n => n.id),
    fileName: {width: name.width, left: name.left, right: name.right}, formulaControls: controls, reads: window.__toolbarReads
  };
});

(async () => {
  try {
    h = await launch({out, errors: report.errors, duration: '12m'});
    const p = h.page;
    await p.file('#fileInput', source); await p.wait(ready); await settled();
    // This suite exercises the optional tabbed layout, including its overflow.
    await p.run(() => document.getElementById('openBtn').dispatchEvent(new MouseEvent('contextmenu', {bubbles:true,cancelable:true})));
    await p.click('#toolbarLayoutToggle');
    await p.wait('document.documentElement.dataset.ribbonLayout === "true"');
    await p.run(() => {
      localStorage.removeItem('ExcelLens.ribbonCollapsed'); if(document.querySelector('.appbar').hasAttribute('data-ribbon-collapsed'))document.getElementById('ribbonCollapseBtn').click(); document.getElementById('ribbonTabHome').click();
      const fetch = window.fetch.bind(window); window.__toolbarReads = 0;
      window.fetch = (url, ...args) => { if (/\/api\/(?:index\/start|analysis\/start|row-view\/start|profile)/.test(String(url))) window.__toolbarReads++; return fetch(url, ...args); };
    });

    for (const [width, height, dpr] of [[1600, 1000, 1], [1366, 820, 1.25], [1024, 768, 1.5], [900, 760, 1], [760, 760, 2], [640, 760, 1.25], [420, 760, 2], [320, 760, 1]]) {
      await resize(width, height, dpr); await settled(); await p.wait(ready); await settled();
      const result = await measure();
      report.cases.push({...result, dpr});
      for (const id of core) assert(result.commands.some(n => n.id === id), `Missing core command ${id} at ${width}`);
      assert(result.title.some(n => n.id === 'commandOpenBtn'), 'Command search must stay reachable at ' + width);
      assert.equal(result.tabs.length, 5, 'All five tabs stay visible at ' + width);
      assert(result.fileName.width >= 24 && result.fileName.left >= 0 && result.fileName.right <= width + .5, 'Workbook name must stay in the bar at ' + width);
      for (const node of [...result.commands, ...result.tabs, ...result.title]) {
        assert(node.left >= -.5 && node.right <= width + .5, `${node.id} clipped at ${width}`);
        assert(node.top >= -.5 && node.bottom <= result.bar.height + .5, `${node.id} leaves the bar at ${width}`);
        assert.equal(node.hit, node.id, `${node.id} must receive pointer input at ${width}`);
        assert(node.height >= 26, `${node.id} is too short at ${width}`);
      }
      // Narrow windows show icons only; wider ones keep readable labels.
      for (const node of result.commands) assert(width <= 560 ? node.icon >= 14 : node.font >= 12, `${node.id} has no readable label or icon at ${width}`);
      for (const node of result.formulaControls) assert(node.left >= -.5 && node.right <= width + .5, `Formula control clipped: ${node.id} at ${width}`);
      assert.equal(result.reads, 0, 'Resizing must not restart indexing or analysis');
      if ([1024, 420, 320].includes(width)) await shot('toolbar-' + width);
    }
    report.checks.push('Eight widths and four pixel ratios: core commands, tabs, command search, workbook name and formula controls fit and receive pointer input without new source tasks');

    await resize(760, 760, 1); await settled(); await settled();
    const narrow = await measure();
    assert(narrow.folded.length > 0 && narrow.commands.some(n => n.id === 'ribbonMoreBtn'), 'Folded commands need the overflow button');
    await p.click('#ribbonMoreBtn'); await settled();
    const listed = await p.run(() => [...document.querySelectorAll('#ribbonMoreMenu button')].map(n => ({text: n.textContent.trim(), disabled: n.disabled})));
    assert.equal(listed.length, narrow.folded.length, 'The overflow menu lists every folded command');
    await p.run(() => [...document.querySelectorAll('#ribbonMoreMenu button')].find(n => n.textContent.trim() === '统计').click());
    await p.wait('document.getElementById("profileDialog").open'); await p.run(() => document.getElementById('profileDialog').close());
    assert(await p.evaluate('document.getElementById("ribbonMoreMenu").hidden'));
    report.checks.push(`At 760 px ${narrow.folded.length} commands fold into the overflow menu and still run`);

    await resize(320, 760, 1);
    await p.click('#ribbonTabView'); await settled();
    assert(await p.evaluate('!document.getElementById("fontSelect").getClientRects().length'));
    await p.click('#ribbonMoreBtn');
    await p.wait('document.querySelector("#ribbonMoreMenu select")?.getClientRects().length');
    await p.run(() => {
      const font = document.querySelector('#ribbonMoreMenu select');
      font.value = 'Consolas';
      font.dispatchEvent(new Event('change', {bubbles:true}));
    });
    await p.wait('document.getElementById("ribbonMoreMenu").hidden && getComputedStyle(document.querySelector("#rowsLayer .grid-cell")).fontFamily.includes("Consolas")');
    await p.click('#ribbonMoreBtn');
    assert.equal(await p.evaluate('document.querySelector("#ribbonMoreMenu select").value'), 'Consolas');
    await key('Escape', 'Escape', 27);
    await p.wait('document.getElementById("ribbonMoreMenu").hidden');
    await settled();
    const idleLayouts = await p.run(async () => {
      let mutations = 0;
      const observer = new MutationObserver(entries => { mutations += entries.length; });
      observer.observe(document.querySelector('.appbar'), {subtree:true, attributes:true, attributeFilter:['data-toolbar-overflow']});
      try {
        for (let frame = 0; frame < 12; frame++) await new Promise(requestAnimationFrame);
        return mutations;
      } finally { observer.disconnect(); }
    });
    assert.equal(idleLayouts, 0, 'An idle overflow layout must settle');
    await p.click('#ribbonTabHome');
    report.checks.push('The 320 px overflow font picker updates the grid and retains the selection; an idle ribbon keeps its overflow state stable over 12 frames');

    await resize(1600, 1000, 1); await p.run(() => { document.querySelector('.appbar').style.width = '620px'; }); await settled(); await settled();
    assert(await p.evaluate('[...document.querySelectorAll(".appbar .app-actions button")].filter(n=>n.getBoundingClientRect().width).every(n=>n.getBoundingClientRect().right<=620.5)'));
    await p.run(() => document.querySelector('.appbar').style.removeProperty('width')); await settled();
    report.checks.push('Resizing the bar itself, independent of the window, recomputes overflow');

    const tabs = await p.run(() => {
      const out = {};
      for (const tab of document.querySelectorAll('.ribbon-tabs button')) {
        tab.click();
        out[tab.dataset.ribbonTab] = {selected: tab.getAttribute('aria-selected'), shown: [...document.querySelectorAll('.ribbon-panel')].filter(n => !n.hidden).map(n => n.dataset.ribbonPanel)};
      }
      document.getElementById('ribbonTabHome').click();
      return out;
    });
    for (const [name, state] of Object.entries(tabs)) { assert.equal(state.selected, 'true'); assert.deepEqual(state.shown, [name]); }
    report.checks.push('Each of the five tabs shows exactly its own panel');

    await palette();
    const coverage = await p.run(() => ({
      controls: [...document.querySelectorAll('.app-actions .ribbon-panel button[id]')].map(n => n.id),
      commands: [...document.querySelectorAll('#commandList .command-entry')].map(n => ({button: n.dataset.commandButton, disabled: n.disabled}))
    }));
    for (const id of coverage.controls.filter(id => id !== 'copyMenuBtn')) assert(coverage.commands.some(n => n.button === (id === 'openBtn' ? 'openLocalMenuBtn' : id)), 'Command search is missing ' + id);
    assert(coverage.commands.find(n => n.button === 'imagesBtn').disabled, 'A command without source support stays disabled');
    await p.run(() => document.getElementById('commandDialog').close());
    for (const [button, dialog] of [['reconcileOpenBtn', 'reconcileDialog'], ['relationsOpenBtn', 'relationsDialog'], ['namedViewOpenBtn', 'namedViewDialog'], ['rowsBtn', 'rowsDialog'], ['freezeBtn', 'freezeDialog'], ['recordCardOpenBtn', 'recordCardDialog'], ['cellDetailBtn', 'detailDialog'], ['taskRecoveryOpenBtn', 'taskRecoveryDialog']]) {
      await command(button); await p.wait(`document.getElementById('${dialog}')?.open`); await p.run(id => document.getElementById(id).close(), dialog);
    }
    await p.run(() => document.getElementById('ribbonTabHome').click());
    await command('columnsBtn'); await settled();
    const anchored = await p.run(() => {
      const menu = document.getElementById('columnsMenu').getBoundingClientRect(), button = document.getElementById('columnsBtn').getBoundingClientRect();
      return {open: !document.getElementById('columnsMenu').hidden, tab: document.querySelector('.ribbon-tabs [aria-selected="true"]').dataset.ribbonTab, gap: menu.top - button.bottom, offset: Math.abs(menu.left - button.left)};
    });
    assert(anchored.open && anchored.tab === 'view' && anchored.gap >= 0 && anchored.gap <= 12 && anchored.offset <= 4, 'The column menu opens under its own button: ' + JSON.stringify(anchored));
    await p.run(() => { document.body.dispatchEvent(new PointerEvent('pointerdown', {bubbles: true})); document.getElementById('ribbonTabHome').click(); });
    report.checks.push('Every tab command is registered in command search; eight open their dialogs; a command on another tab brings its tab forward and anchors its menu to the button');

    const open = await p.evaluate('document.querySelector(".grid-region").getBoundingClientRect().height');
    await key('F1', 'F1', 112, 2); await settled(); await settled();
    const folded = await p.run(() => ({grid: document.querySelector('.grid-region').getBoundingClientRect().height, row: document.querySelector('.app-actions').getBoundingClientRect().height, saved: localStorage.getItem('ExcelLens.ribbonCollapsed')}));
    assert(folded.grid >= open + 30 && folded.row === 0 && folded.saved === '1', 'Folding gives the row back to the sheet: ' + JSON.stringify(folded));
    await p.click('#ribbonTabView'); await settled();
    assert(await p.evaluate('document.querySelector(".appbar").hasAttribute("data-ribbon-peek")&&document.getElementById("rowsBtn").getBoundingClientRect().width>0'));
    assert.equal(await p.evaluate('document.querySelector(".grid-region").getBoundingClientRect().height'), folded.grid, 'A temporary panel floats over the sheet');
    await p.click('#rowsBtn'); await p.wait('document.getElementById("rowsDialog").open'); await p.run(() => document.getElementById('rowsDialog').close()); await settled();
    assert(!await p.evaluate('document.querySelector(".appbar").hasAttribute("data-ribbon-peek")'), 'The temporary panel closes after a command');
    await shot('toolbar-folded');
    await command('columnsBtn'); await settled();
    const columnMenu = await p.run(() => {
      const button = document.getElementById('columnsBtn'), menu = document.getElementById('columnsMenu');
      return {visible: !menu.hidden && button.getBoundingClientRect().width > 0, gap: menu.getBoundingClientRect().top - button.getBoundingClientRect().bottom};
    });
    assert(columnMenu.visible && columnMenu.gap >= 0 && columnMenu.gap <= 12);
    await key('Escape', 'Escape', 27);
    await p.wait('document.getElementById("columnsMenu").hidden && document.activeElement.id === "columnsBtn"');
    report.checks.push('A column menu opened from command search while folded is anchored to its visible button; Escape closes it and restores focus');
    await key('F1', 'F1', 112, 2); await settled();
    assert.equal(await p.evaluate('localStorage.getItem("ExcelLens.ribbonCollapsed")'), '0');
    await p.run(() => document.getElementById('ribbonTabHome').click());
    report.checks.push('Ctrl+F1 folds the bar, a tab opens it over the sheet for one command, and the choice is remembered');

    await palette();
    await p.run(() => { const input = document.getElementById('commandSearch'); input.value = '工具测试'; input.dispatchEvent(new Event('input', {bubbles: true})); });
    for (let i = 0; i < 79; i++) await key('ArrowDown', 'ArrowDown', 40);
    assert.equal(await p.evaluate('document.querySelector("#commandList .active").textContent'), '打开 Sheet：工具测试80');
    await key('Enter', 'Enter', 13); await p.wait(ready + '&&document.querySelector(".sheet-tab.active").textContent==="工具测试80"');
    await p.wait('sheetTabs.scrollLeft>0 && !sheetPrevBtn.disabled');
    const previous = await p.evaluate('sheetTabs.scrollLeft');
    await p.click('#sheetPrevBtn'); await p.wait('sheetTabs.scrollLeft < '+(previous-4));
    const dividers = [];
    for (const preset of ['off','navy']) {
      await p.click('input[name=readingPreset][value='+preset+']'); await settled();
      dividers.push(await p.run(()=>{
        const tabs=document.querySelectorAll('.sheet-tab'), style=getComputedStyle(tabs[1]);
        const line=getComputedStyle(tabs[1],'::before');
        return {first:getComputedStyle(tabs[0],'::before').content, content:line.content,
          width:line.width,color:line.backgroundColor,padding:style.paddingLeft,active:document.querySelector('.sheet-tab.active').textContent};
      }));
    }
    for(const d of dividers) {
      assert.equal(d.first,'none'); assert.equal(d.content,'""'); assert.equal(d.width,'1px');
      assert(parseFloat(d.padding)<=8); assert.equal(d.active,'工具测试80');
    }
    assert.notEqual(dividers[0].color,dividers[1].color);
    report.checks.push('Compact worksheet tabs page through 80 sheets, with a theme-aware divider between adjacent names');
    report.checks.push('Keyboard browsing beyond the first sixty entries selects and opens the eightieth worksheet');

    report.sourceUnchanged = hash(source) === before; assert(report.sourceUnchanged);
    report.sourceSHA256 = before; report.assets = await config.verifyAssets(h.address, 'style.css');
    report.assets['index.html'] = hash(path.join(root, 'internal/ui/web/index.html'));
    assert.deepEqual(report.errors, []);
    report.result = 'PASS';
  } catch (error) {
    report.errors.push(error.stack || String(error));
    if (h) try { await shot('failure'); } catch {}
  } finally {
    if (h) await h.close().catch(error => {
      report.result = 'FAIL';
      report.errors.push('cleanup: ' + (error.message || error));
    });
    fs.writeFileSync(path.join(out, 'report.json'), JSON.stringify(report, null, 2) + '\n');
    console.log(JSON.stringify({result: report.result, phase, checks: report.checks, errors: report.errors}, null, 2));
    if (report.result !== 'PASS') process.exitCode = 1;
  }
})();
