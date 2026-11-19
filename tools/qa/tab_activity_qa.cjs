// Hold selected responses on the browser side. Both tabs keep their real Go
// sessions, DOM, dialogs and file-opening code throughout the checks.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {launch} = require('./browser_harness.cjs');
const {workbook} = require('./generated_workbook.cjs');

async function intercept(page, pattern, handle) {
  let failure;
  const listener = ({data}) => {
    const event = JSON.parse(data);
    if (event.method === 'Fetch.requestPaused') {
      handle(event.params).catch(error => { failure=error; });
    }
  };
  page.socket.addEventListener('message',listener);
  await page.call('Fetch.enable',{patterns:[{urlPattern:pattern}]});
  return async () => {
    await page.call('Fetch.disable');
    page.socket.removeEventListener('message',listener);
    if (failure) throw failure;
  };
}
function reply(page,id,code,body) {
  return page.call('Fetch.fulfillRequest',{requestId:id,responseCode:code,
    responseHeaders:[{name:'Content-Type',value:'application/json'}],body:Buffer.from(JSON.stringify(body)).toString('base64')});
}
async function holdRecent(page) {
  let held;
  const disable=await intercept(page,'*/api/recent',async({requestId,request})=>{
    if(request.method==='POST' && JSON.parse(request.postData).action==='open') held=requestId;
    else await page.call('Fetch.continueRequest',{requestId});
  });
  return async fail=>{
    assert.ok(held,'recent open reached the HTTP boundary');
    if(fail) await reply(page,held,422,{error:'测试读取失败'});
    else await page.call('Fetch.continueRequest',{requestId:held});
    await disable();
  };
}
const ready='!workspace.hidden && !chooseFileBtn.disabled && !ExcelLensPerformance().sheetLoading && ExcelLensPerformance().hasVisibleCoverage';
const visible='!tabActivity.hidden && !tabActivity.hasAttribute("aria-hidden") && tabActivity.checkVisibility({opacityProperty:true,visibilityProperty:true})';
async function openRecent(page) {
  await page.call('Page.bringToFront');
  await page.run(()=>{
    window.activityStart=performance.now();window.activityFirst=null;
    const observer=new MutationObserver(()=>{
      if(!tabActivity.hidden && !tabActivity.hasAttribute('aria-hidden')) {
        window.activityFirst=performance.now()-window.activityStart;observer.disconnect();
      }
    });
    observer.observe(tabActivity,{attributes:true});
    recentList.querySelector('.recent-item').click();
  });
}
async function main() {
  const out=path.resolve(process.argv[2]),errors=[],report={};
  fs.mkdirSync(out,{recursive:true});
  const h=await launch({errors}),a=h.page;
  try {
    const file=path.join(h.storage,'activity.xlsx');
    fs.writeFileSync(file,workbook([{name:'明细',rows:[['项目','金额'],['测试',123],['合计',123]]}]));
    await a.file('#fileInput',file);await a.wait(ready);
    await a.click('#homeBtn');await a.wait('!welcome.hidden && !!recentList.querySelector(".recent-item")');
    const {targetId}=await a.call('Target.createTarget',{url:h.address});
    const b=await h.connect((await h.tabs()).find(t=>t.id===targetId));
    await b.wait('document.readyState==="complete" && !!recentList.querySelector(".recent-item")');
    assert.notEqual(await a.evaluate('ExcelLensSession.id'),await b.evaluate('ExcelLensSession.id'));
    const finishA=await holdRecent(a),finishB=await holdRecent(b);
    await openRecent(a);await a.wait(visible);
    report.homeRevealMs=await a.evaluate('window.activityFirst');
    assert.ok(report.homeRevealMs>=499 && report.homeRevealMs<950,String(report.homeRevealMs));
    assert.equal(await b.evaluate('tabActivity.hidden'),true);
    assert.equal(await a.evaluate('tabActivity.parentElement.classList.contains("recent-meta")'),true);
    await a.shot(path.join(out,'home.png'));
    await openRecent(b);await b.wait(visible);
    await finishA(true);
    await a.wait('tabActivity.hidden && !welcomeError.hidden && !chooseFileBtn.disabled');
    assert.equal(await b.evaluate(visible),true);
    await finishB(false);await b.wait(ready);await b.wait('tabActivity.hidden');
    assert.equal(await a.evaluate('!welcome.hidden && welcomeError.textContent==="测试读取失败"'),true);
    report.independentTabs=true;

    // Short status requests are separated by poll sleeps. The logical
    // search should stay visible and use the measured percentage throughout.
    const disable=await intercept(b,'*/api/search/status*',async({requestId})=>{
      await reply(b,requestId,200,{status:'running',progress:40,scanned:40,total:100,stage:'正在搜索',matches:[]});
    });
    await b.click('#searchOpenBtn');
    await b.run(()=>{searchInput.value='测试';searchBtn.click();});
    await b.wait(visible+' && tabActivity.querySelector(".activity-value").textContent==="40%"');
    assert.equal(await b.evaluate('searchDialog.open && !searchDialog.matches(":modal")'),true);
    const motion=await b.evaluate('commandOpenBtn.click(); ({dialog:getComputedStyle(commandDialog).transitionDuration,button:getComputedStyle(searchBtn).transitionDuration})');
    assert.ok(parseFloat(motion.dialog)>0 && parseFloat(motion.button)>0);
    await b.wait('commandDialog.matches(":modal") && tabActivity.parentElement===commandDialog');
    await b.evaluate('commandDialog.close()');
    await b.wait('tabActivity.parentElement===searchActivity && !tabActivity.hidden');
    await b.run(()=>{
      readingModeBtn.click();
      document.querySelector('[name="readingPreset"][value="navy"]').click();
      document.querySelector('[data-reading-close]').click();
    });
    assert.equal(await b.evaluate('document.documentElement.dataset.readingTone'), 'dark');
    assert.equal(await a.evaluate('tabActivity.hidden'),true);
    await b.call('Emulation.setDeviceMetricsOverride',{width:360,height:720,deviceScaleFactor:1,mobile:false});
    await b.call('Emulation.setEmulatedMedia',{features:[{name:'prefers-reduced-motion',value:'reduce'}]});
    const reduced=await b.evaluate('getComputedStyle(tabActivity.querySelector(".activity-ring")).animationName');
    assert.equal(reduced,'none');
    const bounds=await b.evaluate('(()=>{const r=tabActivity.getBoundingClientRect();return [r.left,r.right,r.bottom,innerWidth,innerHeight]})()');
    assert.ok(bounds[0]>=0 && bounds[1]<=bounds[3] && bounds[2]<=bounds[4]);
    await b.shot(path.join(out,'search-narrow.png'));
    await b.click('#cancelSearchBtn');await b.wait('tabActivity.hidden && cancelSearchBtn.hidden');await disable();
    await b.evaluate('searchDialog.close()');await b.wait('!searchDialog.open');
    report.measuredSearch=true;report.reducedMotion=true;report.narrowBounds=bounds;report.normalMotion=motion;
    // A failed open can be retried on its original page.
    await openRecent(a);await a.wait(ready);await a.wait('tabActivity.hidden');
    assert.equal(await b.evaluate('fileBadge.textContent'), 'activity.xlsx');
    const native=await launch({errors,launchFile:file});
    try {
      await native.page.wait(ready);
      assert.equal(await native.page.evaluate('ExcelLensSession.nativeLaunch && fileBadge.textContent==="activity.xlsx"'),true);
      report.nativeLaunch=true;
    } finally { await native.close(); }
    await a.call('Page.bringToFront');
    report.layoutDeadline = await a.evaluate(`(async () => {
      const task = ExcelLensActivity.begin('正在显示单元格', {priority:2});
      const start = performance.now();
      await new Promise(resolve => setTimeout(resolve, 390));
      while (performance.now() - start < 555) { /* model an indivisible layout */ }
      task.stage('正在显示单元格', 95, 99);
      await task.paintIfDue();
      const painted = !tabActivity.hidden && !tabActivity.hasAttribute('aria-hidden') && tabActivity.checkVisibility({opacityProperty:true,visibilityProperty:true});
      task.end();
      return painted;
    })()`);
    assert.equal(report.layoutDeadline, true);
    assert.equal(await b.evaluate('tabActivity.hidden'), true);
    assert.deepEqual(errors,[]);report.errors=errors;report.result='PASS';
  } finally { fs.writeFileSync(path.join(out,'browser.json'),JSON.stringify(report,null,2));await h.close(); }
}
main().catch(error=>{console.error(error);process.exitCode=1;});
