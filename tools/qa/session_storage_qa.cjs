// Exercise reload, browser-initiated close and storage actions on an isolated service.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {launch,pause} = require('./browser_harness.cjs');

async function main() {
  const report = {}, errors = [];
  const h = await launch({errors}), home = h.page;
  try {
    await home.click('#homeDataMenu summary');await home.click('#storageHome');
    await home.wait('storageDialog.open && storageRows.rows.length >= 10 && storageDialog.getAttribute("aria-busy")==="false"');
    assert.ok((await home.evaluate('storageRows.textContent')).includes('上传副本'));
    await home.evaluate('storageDialog.close()');
    const {targetId} = await home.call('Target.createTarget',{url:h.address});
    const tab = await h.connect((await h.tabs()).find(t => t.id === targetId));
    await tab.wait('document.readyState==="complete" && !!window.ExcelLensSession');
    const file = path.join(h.storage,'合成台账.csv');
    fs.writeFileSync(file,'id,amount\n001,12.5\n');
    await tab.file('#fileInput',file);
    const visible = 'window.ExcelLensPerformance?.().hasVisibleCoverage && !window.ExcelLensPerformance().sheetLoading';
    await tab.wait(visible);
    const workbook = await tab.api('/api/status');
    const ids = () => tab.evaluate('({id:ExcelLensSession.id,page:ExcelLensSession.apply(new Headers()).get("X-ExcelLens-Page")})');
    const first = await ids();
    await tab.call('Page.reload');
    await tab.wait('document.readyState==="complete" && !!window.ExcelLensSession && ExcelLensSession.apply(new Headers()).get("X-ExcelLens-Page")!==' + JSON.stringify(first.page));
    await tab.wait(visible);
    const next = await ids(), restored = await tab.api('/api/status');
    assert.equal(next.id,first.id);
    assert.equal(restored.fileName,'合成台账.csv');
    assert.ok(restored.opened && workbook.opened);
    assert.ok(restored.workbookKey && restored.workbookKey===workbook.workbookKey);
    async function lease(owner,action) {
      return home.run(async (owner,action) => {
        const headers = new Headers({'X-ExcelLens-Token':document.querySelector('meta[name="app-token"]').content,
          'X-ExcelLens-Session':owner.id, 'X-ExcelLens-Page':owner.page});
        return (await fetch('/api/session/'+action,{headers,method:'POST'})).status;
      },owner,action);
    }
    assert.equal(await lease(first,'detach'),204);
    assert.equal(await lease(first,'heartbeat'),409);
    assert.equal(await lease(next,'heartbeat'),204);
    report.reloadAndLateClose = true;

    await tab.click('#storageOpenBtn');
    await tab.wait('storageDialog.open && storageDialog.getAttribute("aria-busy")==="false"');
    const parent = path.join(h.storage,'backups');
    fs.mkdirSync(parent);
    await tab.run(folder => { storageDirectory.value = folder; },parent);
    await tab.click('[data-storage="backup"]');
    await tab.wait('storageStatus.textContent.includes("备份已核验")');
    const folders = fs.readdirSync(parent);
    assert.equal(folders.length,1);
    const manifest = JSON.parse(fs.readFileSync(path.join(parent,folders[0],'manifest.json'),'utf8'));
    assert.ok(Object.keys(manifest.files).some(name => name.startsWith('local/temp/ExcelLens-upload-')));
    report.storageBackup = true;
    await tab.evaluate('storageDialog.close()');

    const closed = Date.now();
    await tab.call('Page.close');
    for (let i=0; i<100 && (await h.tabs()).some(t => t.id===targetId); i++) await pause(50);
    assert.ok(!(await h.tabs()).some(t => t.id===targetId));
    assert.equal(await lease(next,'heartbeat'),409,'pagehide detached the actual closing document');
    // No request to this session during the grace period or ensuing sweep.
    await pause(56000);
    assert.equal(await lease(next,'heartbeat'),410);
    report.closedTabExpiredAfterMs = Date.now()-closed;
    await home.click('#homeDataMenu summary');await home.click('#storageHome');
    await home.wait('storageDialog.getAttribute("aria-busy")==="false"');
    await home.run(folder => { storageDirectory.value=folder; },path.join(parent,folders[0]));
    await home.click('[data-storage="restore"]');
    await home.wait('storageStatus.textContent.includes("已核验并迁入备份")');
    report.storageRestore = true;
    assert.ok((await home.api('/api/status')).opened===false);
    assert.deepEqual(errors,[]);
    console.log(JSON.stringify({...report,result:'PASS'}));
  } catch(error) {
    const snapshot=async p=>p?.evaluate('({status:document.getElementById("storageStatus")?.textContent,alert:document.getElementById("welcomeError")?.textContent,open:document.getElementById("storageDialog")?.open,coverage:window.ExcelLensPerformance?.().hasVisibleCoverage})').catch(()=>null);
    console.error(JSON.stringify({report,assertionFailure:error.stack,home:await snapshot(home),errors}));
    throw error;
  } finally { await h.close(); }
}

main().catch(error => { console.error(error); process.exitCode=1; });
