// Real local pages. All workbooks, transfer receipts and downloads are synthetic.
const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path');
const {launch,pause}=require('./browser_harness.cjs');
(async()=>{
  const errors=[],h=await launch({errors}),p=h.page;
  try{
    const {roots}=await p.api('/api/storage'),history=path.join(roots.config,'transfer-history.json');
    fs.mkdirSync(roots.config,{recursive:true});fs.writeFileSync(history,'broken-history');
    const parent=path.join(h.storage,'backups');fs.mkdirSync(parent);
    await p.click('#homeDataMenu summary');await p.click('#storageHome');await p.wait('!storageHistoryIssue.hidden&&storageDialog.getAttribute("aria-busy")==="false"');
    await p.run(dir=>{storageDirectory.value=dir;},parent);
    await p.click('[data-storage="backup"]');await p.wait('storageStatus.textContent.includes("备份已核验")');
    assert.equal(fs.readFileSync(history,'utf8'),'broken-history');
    await p.click('[data-storage="repair-history"]');await p.wait('!!document.querySelector(".ask-dialog[open] [data-no]")');
    await p.click('.ask-dialog[open] [data-no]');assert.equal(fs.readFileSync(history,'utf8'),'broken-history');
    await p.click('[data-storage="repair-history"]');await p.click('.ask-dialog[open] [data-yes]');
    await p.wait('storageHistoryIssue.hidden&&storageStatus.textContent.includes("原传输历史已保存在")');
    const evidence=fs.readdirSync(roots.config).filter(x=>x.startsWith('transfer-history-damaged-'));
    assert.equal(evidence.length,1);assert.equal(fs.readFileSync(path.join(roots.config,evidence[0]),'utf8'),'broken-history');
    await p.evaluate('storageDialog.close()');
    const file=path.join(h.storage,'results.csv');
    fs.writeFileSync(file,'id,value\n'+Array.from({length:1503},(_,i)=>i+',needle-office-'+String(i+1).padStart(7,'0')).join('\n')+'\n');
    await p.file('#fileInput',file);await p.wait('window.ExcelLensPerformance?.().hasVisibleCoverage&&!window.ExcelLensPerformance().sheetLoading');
    await p.click('#searchOpenBtn');await p.run(()=>{searchMode.value='value';searchScope.value='all';searchFull.checked=true;searchRecoverable.checked=true;searchInput.value='needle-office';});
    await p.click('#searchBtn');await p.wait('!searchPaging.hidden&&searchSummary.textContent.includes("1503")&&searchResults.children.length===100');
    await p.click('#searchPageNext');await p.wait('searchPageCount.textContent.includes("第 2 页")');
    assert.equal(await p.evaluate('searchResults.querySelector(".result-ref").textContent'),'B102');
    await p.click('#searchPagePrev');await p.wait('searchResults.querySelector(".result-ref").textContent==="B2"');
    await p.run(()=>{searchResultFilter.value='0001503';searchResultFilter.dispatchEvent(new Event('input',{bubbles:true}));searchPageNext.click();});
    await p.wait('searchResults.children.length===1&&searchResults.firstChild.textContent.includes("B1504")');
    const downloads=path.join(h.storage,'download');fs.mkdirSync(downloads);
    await p.call('Browser.setDownloadBehavior',{behavior:'allow',downloadPath:downloads});
    await p.click('#saveSearchResultsBtn');
    let name;for(let n=0;n<200&&!name;n++){name=fs.readdirSync(downloads).find(x=>x.endsWith('.csv'));if(!name)await pause(50);}
    assert.ok(name);const out=fs.readFileSync(path.join(downloads,name),'utf8');assert.ok(out.includes('B1504'));assert.equal(out.trimEnd().split('\n').length,2);
    const wb=await p.api('/api/status');const filter=await p.api('/api/filter/start',{sheet:wb.sheets[0].name,headerRow:1,rules:[{column:2,operator:'equals',value:'needle-office-0001503'}]});
    let status;for(let i=0;i<100;i++){status=await p.api('/api/filter/status?id='+filter.jobId);if(status.status==='done')break;await pause(30);}
    assert.equal(status.matchedRows,2); // The filter keeps the header and one data row.
    const {targetId}=await p.call('Target.createTarget',{url:h.address});
    const other=await h.connect((await h.tabs()).find(t=>t.id===targetId));await other.wait('!!window.ExcelLensSession');await other.evaluate('ExcelLensSession.ready');
    await other.click('#homeDataMenu summary');await other.click('#storageHome');await other.evaluate('storageBackground.open=true');
    await other.wait('storageWork.textContent.includes("活动页面使用中")');
    const items=await other.api('/api/background');const item=items.items.find(x=>x.id===filter.jobId);
    assert.equal(item.protected,true);
    await assert.rejects(other.api('/api/background',{action:'discard',kind:'filter',session:item.session,id:item.id}),/活动页面/);
    const visible=await p.api('/api/sheet?'+new URLSearchParams({sheet:wb.sheets[0].name,filterId:status.filterId,limit:2}));
    assert.deepEqual(visible.rows.map(row=>row.number),[1,1504]);
    const search=items.items.find(x=>x.kind==='search');
    assert.equal(search.protected,true);
    await assert.rejects(other.api('/api/background',{action:'discard',kind:'search',session:search.session,id:search.id}),/活动页面/);
    assert.equal((await p.api('/api/search/page?id='+search.id)).matches.length,100);
    assert.deepEqual(errors,[]);console.log(JSON.stringify({result:'PASS',historyBackupBeforeRepair:true,confirmedQuarantine:true,fullHits:1503,filteredCSVRows:1,protectedActiveFilter:true}));
  }catch(error){console.error(error);throw error;}finally{await h.close();}
})().catch(()=>process.exitCode=1);
