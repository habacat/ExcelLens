// Exercise both layouts and the native callbacks using isolated storage and
// the production page. The Windows notification-area menu itself is not shown.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {createHash} = require('node:crypto');
const {launch} = require('./browser_harness.cjs');
const {workbook} = require('./generated_workbook.cjs');
const config = require('./project_config.cjs');
const out = path.resolve('validation', config.validationVersion(), 'private', 'toolbar-layouts');
fs.mkdirSync(out, {recursive:true});
const source = path.join(out, '月度费用台账.xlsx');
const formula = 'SUMIFS(月度费用明细!$C$2:$C$3,月度费用明细!$A$2:$A$3,A2,月度费用明细!$B$2:$B$3,B2)';
fs.writeFileSync(source, workbook([{name:'月度费用明细',rows:[
  ['编号','项目','金额','汇总'], ['001','差旅费',{raw:'160.25'},{formula,raw:'160.25'}], ['002','交通费',{raw:'28.50'}]
]}]));
const hash = file => createHash('sha256').update(fs.readFileSync(file)).digest('hex');
const original = hash(source), errors = [];
const ready = 'window.ExcelLensPerformance?.().hasVisibleCoverage && !window.ExcelLensPerformance().sheetLoading';
const report = {version:config.version(),result:'FAIL',widths:[],checks:[],errors};
async function pointer(page, selector, button='left') {
  const point = await page.run(selector=>{
    const el=document.querySelector(selector), box=el.getBoundingClientRect();
    const x=box.left+box.width/2, y=box.top+box.height/2;
    if(!box.width || !el.contains(document.elementFromPoint(x,y))) throw Error('Control not reachable: '+selector);
    return {x,y};
  }, selector);
  for (const type of ['mousePressed','mouseReleased']) await page.call('Input.dispatchMouseEvent',{type,button,clickCount:1,...point});
}
const settle = p => p.evaluate('new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))');
async function main() {
  const h = await launch({errors,trayInput:true});
  try {
    const p = h.page;
    await p.file('#fileInput',source); await p.wait(ready);
    await p.click('.grid-cell[data-ref="D2"]');
    await p.wait('formulaText.textContent === '+JSON.stringify('='+formula));
    const folder = path.join(h.storage,'ExcelLens','cache');
    assert(fs.readdirSync(folder).length>0);
    await p.run(()=>{
      window.__toolbarNodes=['openBtn','fontSelect','tracePrecedentsBtn','copyValueBtn'].map(id=>document.getElementById(id));
      const original=window.fetch; window.__layoutJobs=[];
      window.fetch=function(url,...args){
        if (/\/api\/(index\/start|analysis\/start|row-view\/start|filter\/start|search\/start)/.test(String(url))) window.__layoutJobs.push(String(url));
        return original.call(this,url,...args);
      };
    });
    for(const [width,dpr] of [[1920,1],[1600,1],[1536,1.25],[1391,1],[1280,1.25],[1024,1.5],[901,1],[837,1],[836,1],[760,1],[671,1],[640,1.25],[537,1],[536,1],[420,2],[320,1]]) {
      await p.call('Emulation.setDeviceMetricsOverride',{width,height:840,deviceScaleFactor:dpr,mobile:false});
      await settle(p); await settle(p);
      const sample = await p.run(()=>{
        const box=el=>el.getBoundingClientRect(),visible=el=>box(el).width>0;
        const header=document.querySelector('.appbar'), formula=document.querySelector('.formula-toolbar');
        const bad=[...document.querySelectorAll('.appbar button,.formula-toolbar button,.formula-toolbar select,#nameBox,#formulaText')].filter(visible).filter(el=>{
          const r=box(el); return r.left<-.5||r.right>innerWidth+.5||!el.contains(document.elementFromPoint(r.left+r.width/2,r.top+r.height/2));
        }).map(el=>el.id);
        const tools=[...formula.querySelectorAll('.classic-before button,.classic-after button,.classic-after select')].filter(visible).map(el=>{
          const r=box(el), icon=box((el.closest('label') || el).querySelector('.icon'));
          return {id:el.id,width:r.width,height:r.height,icon:icon.width,dx:icon.x+icon.width/2-r.x-r.width/2,
            name:el.getAttribute('aria-label'),title:el.title};
        });
        return {width:innerWidth,top:box(header).height,formula:box(formula).height,gridTop:box(document.querySelector('.grid-region')).top,
          tools,formulaFits:formulaText.scrollWidth<=formulaText.clientWidth,
          searchWidth:box(document.getElementById('commandOpenBtn')).width,fontWidth:box(document.getElementById('fontSelect')).width,
          formulaWidth:box(document.getElementById('formulaText')).width,bad,
          topButtons:[...document.querySelectorAll('.classic-actions button')].filter(visible).map(el=>el.id),
          formulaButtons:[...formula.querySelectorAll('button,select')].filter(visible).map(el=>el.id),
          tabWidth:box(document.getElementById('ribbonTabs')).width};
      });
      report.widths.push({...sample,dpr});
      assert(sample.top<=40 && sample.formula<=39 && sample.tabWidth===0,JSON.stringify(sample));
      assert.deepEqual(sample.bad,[],JSON.stringify(sample));
      for(const id of ['openBtn','searchOpenBtn','filterOpenBtn','multiSortBtn','exportOpenBtn','analysisOpenBtn'])assert(sample.topButtons.includes(id),JSON.stringify(sample));
      assert(sample.formulaWidth>=width*(width>=1024?.5:.4) && sample.searchWidth>=28,JSON.stringify(sample));
      for(const tool of sample.tools) {
        assert(tool.width>=24 && tool.width<=28 && tool.height===tool.width && tool.icon>=14 && Math.abs(tool.dx)<.5,JSON.stringify(tool));
        assert(tool.name && tool.title.startsWith(tool.name),tool.id);
      }
      assert(sample.tools.some(tool=>tool.id==='fontSelect'));
      if(width>=901) assert.equal(sample.tools.length,8);
      if(width>=1280) assert(sample.formulaFits,'Long SUMIFS is clipped at '+width);
      if(width===1920) {
        for (const id of ['catalogueOpenBtn','dataRulesOpenBtn','recordCardOpenBtn','copyFormulaBtn','copyValueBtn','tracePrecedentsBtn','traceDependentsBtn','cellDetailBtn']) assert(sample.formulaButtons.includes(id),id);
        assert(sample.searchWidth>=200);
        const snap=await p.call('Page.captureScreenshot',{format:'png',clip:{x:0,y:0,width,height:148,scale:1}});
        fs.writeFileSync(path.join(out,'classic-top.png'),Buffer.from(snap.data,'base64'));
      }
    }
    assert(report.widths.at(-1).searchWidth < report.widths[0].searchWidth);
    report.checks.push('Classic is the default; desktop, narrow and breakpoint widths preserve one top row, working font and search controls, hit targets and formula space');
    await pointer(p,'#fontSelect');
    for(const [key,code] of [['End',35],['Enter',13]]) {
      for(const type of ['keyDown','keyUp']) await p.call('Input.dispatchKeyEvent',{type,key,code:key,windowsVirtualKeyCode:code});
    }
    await p.wait('fontSelect.value==="Consolas" && fontSelect.title.includes("Consolas") && getComputedStyle(document.querySelector("#rowsLayer .grid-cell")).fontFamily.includes("Consolas")');
    await p.call('Emulation.setDeviceMetricsOverride',{width:1280,height:840,deviceScaleFactor:1,mobile:false}); await settle(p);
    await p.run(()=>{window.__copied=[];Object.defineProperty(navigator,'clipboard',{configurable:true,value:{writeText:async text=>window.__copied.push(text)}});});
    await pointer(p,'#copyFormulaBtn'); await pointer(p,'#copyValueBtn');
    assert.deepEqual(await p.evaluate('window.__copied'),['='+formula,'160.25']);
    report.checks.push('Icon hit targets, hover names, native font selection and separate formula/value clipboard commands work');
    await pointer(p,'#openBtn','right'); await p.wait('!toolbarLayoutMenu.hidden');
    await pointer(p,'#toolbarLayoutToggle'); await p.wait('document.documentElement.dataset.ribbonLayout==="true"');
    const tabbedTop=await p.evaluate('document.querySelector(".appbar").getBoundingClientRect().height');
    assert(tabbedTop>report.widths[0].top+30); report.tabbedHeight=tabbedTop;
    await pointer(p,'#ribbonTabFormula');
    assert(await p.evaluate('document.querySelector("#ribbonFormula #tracePrecedentsBtn").getClientRects().length>0'));
    await p.run(()=>{window.__oldLayoutPage=true;});
    await p.call('Page.reload'); await p.wait('!window.__oldLayoutPage && '+ready+' && document.documentElement.dataset.ribbonLayout==="true"');
    h.tray('classic'); await p.wait('document.documentElement.dataset.ribbonLayout==="false"');
    await p.run(()=>{
      window.__toolbarNodes=['openBtn','fontSelect','tracePrecedentsBtn','copyValueBtn'].map(id=>document.getElementById(id));
      window.__layoutJobs=[];const original=window.fetch;
      window.fetch=function(url,...args){if(/\/api\/(index|filter|analysis|search)\/start/.test(String(url)))window.__layoutJobs.push(String(url));return original.call(this,url,...args);};
    });
    await pointer(p,'#commandOpenBtn'); await p.wait('commandDialog.open');
    await p.run(()=>{commandSearch.value='列 显隐';commandSearch.dispatchEvent(new Event('input',{bubbles:true}));});
    await pointer(p,'[data-command-button="columnsBtn"]'); await p.wait('!columnsMenu.hidden');
    await p.call('Input.dispatchKeyEvent',{type:'keyDown',key:'Escape',code:'Escape',windowsVirtualKeyCode:27});
    await p.wait('columnsMenu.hidden');
    await pointer(p,'#openBtn','right'); await pointer(p,'#toolbarLayoutToggle'); await p.wait('document.documentElement.dataset.ribbonLayout==="true"');
    h.tray('classic'); await p.wait('document.documentElement.dataset.ribbonLayout==="false"');
    assert(await p.evaluate('window.__toolbarNodes.every(n=>document.querySelectorAll("#"+n.id).length===1 && document.getElementById(n.id)===n)'));
    assert.deepEqual(await p.evaluate('window.__layoutJobs'),[]);
    report.checks.push('Header context menu and tray callbacks both switch the same persisted preference; control identity, command search and workbook binding survive the switch without new source jobs');
    await pointer(p,'#editCopyOpen');
    await p.wait('document.body.classList.contains("editing-copy") && !editGridSave.disabled');
    await p.click('.grid-cell[data-ref="A2"]');
    await p.run(()=>{
      const field=document.getElementById('editGridFormula');
      field.focus(); field.value='尚未确认'; field.dispatchEvent(new InputEvent('input',{bubbles:true}));
    });
    h.tray('ribbon'); await p.wait('document.documentElement.dataset.ribbonLayout==="true"');
    assert(await p.evaluate('document.activeElement===editGridFormula && editGridFormula.value==="尚未确认"'));
    await p.call('Input.dispatchKeyEvent',{type:'keyDown',key:'Escape',code:'Escape',windowsVirtualKeyCode:27});
    await p.click('#editGridExit'); await p.wait('!document.body.classList.contains("editing-copy")');
    h.tray('classic'); await p.wait('document.documentElement.dataset.ribbonLayout==="false"');
    report.checks.push('A tray layout change keeps an in-progress edit and the active formula field');
    const {targetId}=await p.call('Target.createTarget',{url:h.address});
    const second=await h.connect((await h.tabs()).find(tab=>tab.id===targetId));
    await second.call('Emulation.setDeviceMetricsOverride',{width:1600,height:840,deviceScaleFactor:1,mobile:false});
    await second.wait('document.readyState==="complete" && document.documentElement.dataset.ribbonLayout==="false"');
    await second.file('#fileInput',source); await second.wait(ready);
    await second.call('Page.bringToFront');
    await second.wait('!editCopyOpen.disabled && editCopyOpen.getClientRects().length>0'); await settle(second);
    await pointer(second,'#editCopyOpen');
    await second.wait('document.body.classList.contains("editing-copy") && !editGridSave.disabled');
    await second.run(()=>{
      editGridFormula.focus(); editGridFormula.value='待确认'; editGridFormula.dispatchEvent(new InputEvent('input',{bubbles:true}));
      const leaving=new Event('beforeunload',{cancelable:true});
      window.dispatchEvent(leaving);
      if (!leaving.defaultPrevented) throw Error('An ordinary navigation must still protect the edit');
    });
    h.tray('clear-all');
    const home='document.documentElement?.dataset.cacheEpoch === "1" && document.readyState === "complete" && window.ExcelLensPerformance && document.body.classList.contains("home-view") && !document.getElementById("welcome").hidden';
    await second.call('Page.bringToFront'); await second.wait(home);
    await p.call('Page.bringToFront'); await p.wait(home);
    assert.deepEqual(fs.readdirSync(folder),[]);
    await p.file('#fileInput',source); await p.wait(ready);
    await p.wait('document.querySelector("#rowsLayer [data-ref=A2]")?.textContent==="001"');
    assert(fs.readdirSync(folder).length>0); assert.equal(hash(source),original);
    report.checks.push('Clear all caches returns both tabs to home, empties disk cache, preserves source bytes, and reopens the workbook with its leading-zero ID');
    assert.deepEqual(errors,[]);
  } catch (err) {
    report.businessFailure=err.stack;
    throw err;
  } finally { await h.close(); }
}
main().then(()=>{report.result='PASS';}).catch(err=>{report.failure=err.stack;process.exitCode=1;}).finally(()=>{
  fs.writeFileSync(path.join(out,'report.json'), JSON.stringify(report,null,2)+'\n');
  console.log(JSON.stringify({result:report.result,widths:report.widths.length,checks:report.checks,failure:report.failure}));
});
