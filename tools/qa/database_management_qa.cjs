// Run against a disposable copy of a retained fixture supplied by the caller.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
const {launch, pause} = require('./browser_harness.cjs');

async function main() {
  const source = path.resolve(process.argv[2]), key = path.basename(source);
  assert.match(key, /^[a-f0-9]{64}$/);
  const errors = [], h = await launch({errors}), p = h.page;
  try {
    const dest = path.join(h.storage,'ExcelLens','database-snapshots',key);
    fs.cpSync(source,dest,{recursive:true});
    const manifest = JSON.parse(fs.readFileSync(path.join(dest,'snapshot.elndb'),'utf8'));
    const shot = async name => {
      if (process.env.QA_SCREENSHOT_DIR) await p.shot(path.join(process.env.QA_SCREENSHOT_DIR,name));
    };
    const activateKey = async (id, key, code) => {
      await p.run(id=>document.getElementById(id).focus(),id);
      for (const type of ['rawKeyDown','keyUp']) await p.call('Input.dispatchKeyEvent',{type,key,code,windowsVirtualKeyCode:key==='Enter'?13:32});
    };
    const homeMenu = async () => {
      await p.click('#homeDataMenu summary');
      await p.wait('homeDataMenu.open && databaseSnapshotsHome.getBoundingClientRect().width > 0');
    };
    await homeMenu();
    await shot('snapshot-management-home.png');
    await activateKey('openDatabaseHome','Enter','Enter');
    await p.wait('databaseDialog.open && !homeDataMenu.open');
    await p.click('#databaseCancel');
    await homeMenu();
    await activateKey('storageHome',' ','Space');
    await p.wait('storageDialog.open && storageRows.textContent.includes("数据库快照暂存")');
    await p.evaluate('storageDialog.close()');

    const csv = path.join(h.storage,'clipboard.csv');
    fs.writeFileSync(csv,'account,amount\n000123,10\n');
    await p.file('#fileInput',csv);
    await p.wait('window.ExcelLensPerformance?.().hasVisibleCoverage && !window.ExcelLensPerformance().sheetLoading');
    await p.call('Browser.grantPermissions',{origin:new URL(h.address).origin,permissions:['clipboardReadWrite','clipboardSanitizedWrite']});
    const copy = async () => {
      await p.call('Input.dispatchKeyEvent',{type:'rawKeyDown',key:'c',code:'KeyC',modifiers:2,windowsVirtualKeyCode:67});
      await p.call('Input.dispatchKeyEvent',{type:'keyUp',key:'c',code:'KeyC',windowsVirtualKeyCode:67});
    };
    await p.evaluate("nameBox.focus();nameBox.value='A2'");
    await p.call('Input.dispatchKeyEvent',{type:'keyDown',key:'Enter',code:'Enter',windowsVirtualKeyCode:13});
    await p.call('Input.dispatchKeyEvent',{type:'keyUp',key:'Enter',code:'Enter',windowsVirtualKeyCode:13});
    await p.wait('document.activeElement===grid');
    await copy();
    await p.wait('navigator.clipboard.readText().then(text=>text.trim()==="000123")');
    await p.click('#homeBtn');
    await homeMenu();
    const bounds = await p.evaluate(`(() => {
      const range=document.createRange(); range.selectNodeContents(openDatabaseHome);
      const r=range.getBoundingClientRect(); return {x:r.left-1,end:r.right+1,y:r.top+r.height/2};
    })()`);
    await p.call('Input.dispatchMouseEvent',{type:'mousePressed',x:bounds.x,y:bounds.y,button:'left',buttons:1,clickCount:1});
    for(let i=1;i<=8;i++) await p.call('Input.dispatchMouseEvent',{type:'mouseMoved',x:bounds.x+(bounds.end-bounds.x)*i/8,y:bounds.y,button:'left',buttons:1});
    await p.call('Input.dispatchMouseEvent',{type:'mouseReleased',x:bounds.end,y:bounds.y,button:'left',clickCount:1});
    assert.equal(await p.evaluate('getSelection().toString()'),'连接在线数据库');
    assert(await p.evaluate('homeDataMenu.open && !databaseDialog.open'));
    await copy();
    await p.wait('navigator.clipboard.readText().then(text=>text==="连接在线数据库")');
    await p.evaluate('getSelection().removeAllRanges()');
    await p.click('#databaseSnapshotsHome');
    await p.wait('databaseSnapshotsDialog.open && !databaseSnapshotsList.inert && document.querySelector(".database-snapshot-entry")');
    await p.click('#dbSnapshotControls details:nth-of-type(1) summary');
    const filter = (field,value) => p.run((field,value) => {
      const input = dbSnapshotFilters.elements[field];
      input.value = value;
      input.dispatchEvent(new Event('input',{bubbles:true}));
      return document.querySelectorAll('.database-snapshot-entry').length;
    },field,value);
    for (const [field,value] of [['source','absent host'],['table','absent table'],['min','999999'],['until','2000-01-01']]) {
      assert.equal(await filter(field,value),0,field);
      assert.equal(await filter(field,''),1,field);
    }
    assert.equal(await filter('source',manifest.source.host),1);
    assert.equal(await filter('table',manifest.tables[0].name),1);
    await p.click('#dbSnapshotControls details:nth-of-type(2) summary');
    await p.evaluate("dbSnapshotPolicy.elements.budget.value='0.0000001';dbSnapshotPolicy.elements.days.value='30';dbSnapshotPolicy.requestSubmit()");
    await p.wait('dbSnapshotUsage.textContent.includes("已达容量提醒") && !databaseSnapshotsList.inert');
    await p.click('.database-snapshot-entry button');
    await p.wait('dbSnapshotEvidence.open && dbEvidenceOrigin.textContent.includes("SHA-256")');
    for (let i=0; i<manifest.tables.length; i++) {
      const e = manifest.tables[i].evidence;
      const text = await p.run(i=>{
        dbEvidenceTable.selectedIndex=i;dbEvidenceTable.dispatchEvent(new Event('change'));
        return dbEvidenceTableFacts.textContent + dbEvidenceColumns.textContent;
      },i);
      for (const value of [e.query,e.index.sha256,e.rows.sha256,e.columns[0].name]) assert(text.includes(value));
    }
    await p.call('Emulation.setDeviceMetricsOverride',{width:390,height:740,mobile:false,deviceScaleFactor:1});
    assert(await p.evaluate('dbSnapshotEvidence.getBoundingClientRect().right<=390 && dbSnapshotEvidence.scrollWidth<=dbSnapshotEvidence.clientWidth+1'));
    await shot('snapshot-management-evidence.png');
    const download = path.join(h.storage,'downloads');
    fs.mkdirSync(download);
    await p.call('Browser.setDownloadBehavior',{behavior:'allow',downloadPath:download});
    await p.click('#dbEvidenceDownload');
    await p.wait('dbEvidenceStatus.textContent.includes("已导出")');
    const exported = path.join(download,'ExcelLens-snapshot-'+key+'.json');
    for (let turn=0; !fs.existsSync(exported) && turn<100; turn++) await pause(50);
    assert.equal(crypto.createHash('sha256').update(fs.readFileSync(exported)).digest('hex'),key);
    await p.click('#dbEvidenceClose');
    await p.click('.database-snapshot-entry button:last-child');
    await p.wait('!dbSnapshotRemoval.hidden && !dbSnapshotRemovalConfirm.hidden && !dbSnapshotRemovalConfirm.disabled');
    await p.click('#dbSnapshotRemovalBack');
    assert(fs.existsSync(dest));
    await p.click('.database-snapshot-entry button:last-child');
    await p.wait('!dbSnapshotRemovalConfirm.hidden && !dbSnapshotRemovalConfirm.disabled');
    await p.click('#dbSnapshotRemovalConfirm');
    await p.wait('dbSnapshotUsage.textContent.includes("共 0 份") && !databaseSnapshotsList.inert');
    assert(!fs.existsSync(dest));
    assert.deepEqual(errors,[]);
    console.log('PASS: menu routes, native clipboard with a background grid, four filters, capacity reminder, per-table evidence, exact JSON download, 390px layout, cancelled and confirmed deletion.');
  } finally { await h.close(); }
}
main().catch(error=>{console.error(error);process.exitCode=1;});
