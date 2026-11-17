// Real TXT adapter/service and isolated headless Chromium. This checks actual
// row geometry/hit testing after cached scrolls, not just payload coverage.
const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict');
const {createHash}=require('node:crypto'),{launch}=require('./browser_harness.cjs'),config=require('./project_config.cjs');
const label=process.argv[2]||'fixed';
assert(/^[a-z0-9-]+$/.test(label),'Use a simple receipt label');
const root=path.resolve(__dirname,'../..'),base=path.join(root,'validation',config.validationVersion());
const out=path.join(base,'private','long-txt-scroll-'+label),fixture=path.join(base,'private','long-txt-scroll-fixture');
fs.mkdirSync(out,{recursive:true});fs.mkdirSync(fixture,{recursive:true});
const file=path.join(fixture,'large.txt'),rowCount=2907951;
if(!fs.existsSync(file)){
  const fd=fs.openSync(file,'w');
  try{
    fs.writeSync(fd,'行号@凭证来源@编号@金额\n');
    for(let first=2;first<=rowCount;first+=8192){
      const rows=[];
      for(let row=first;row<Math.min(first+8192,rowCount+1);row++)rows.push(`${row}@电子表格@0000123@-2805.93\n`);
      fs.writeSync(fd,rows.join(''));
    }
  }finally{fs.closeSync(fd);}
}
const hashFile=async file=>{const hash=createHash('sha256');for await(const chunk of fs.createReadStream(file))hash.update(chunk);return hash.digest('hex');};
const ready='window.ExcelLensPerformance?.().hasVisibleCoverage&&!window.ExcelLensPerformance().sheetLoading';
const report={version:config.version(),label,result:'FAIL',rowCount,checks:[],errors:[],screenshots:[],desktopManualAcceptance:false,officeWpsAcceptance:false};
let h;
const settle=p=>p.evaluate('new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))');
async function shot(p,name){await p.shot(path.join(out,name+'.png'));report.screenshots.push('private/long-txt-scroll-'+label+'/'+name+'.png');}
async function snapshot(p){
  return p.run(rowCount=>{
    const grid=document.querySelector('#grid'),box=grid.getBoundingClientRect();
    const rows=[...document.querySelectorAll('#rowsLayer .grid-row')].map(node=>{
      const rect=node.getBoundingClientRect();
      return {row:Number(node.dataset.row),top:rect.top,bottom:rect.bottom,height:rect.height,serialized:node.style.top,
        cells:[...node.querySelectorAll('.grid-cell')].map(cell=>({col:Number(cell.dataset.col),raw:cell._xlModel?.raw,type:cell._xlModel?.type||''}))};
    }).sort((a,b)=>a.row-b.row);
    const visible=rows.filter(row=>row.top>=box.top+120&&row.bottom<=box.bottom-2);
    const gaps=visible.slice(1).map((row,i)=>row.top-visible[i].bottom);
    const hitMismatches=visible.filter(row=>Number(document.elementFromPoint(box.left+300,(row.top+row.bottom)/2)?.closest('.grid-row')?.dataset.row)!==row.row).map(row=>row.row);
    const valueMismatches=rows.filter(row=>row.row>1&&(row.row<=rowCount?
      row.cells.filter(cell=>cell.col<=4).length!==4||row.cells.some(cell=>cell.col<=4?
        cell.raw!==({1:String(row.row),2:'电子表格',3:'0000123',4:'-2805.93'})[cell.col]||cell.type!=='inlineStr':cell.raw!==undefined||cell.type!==''):
      row.cells.some(cell=>cell.raw!==undefined||cell.type!==''))).map(row=>row.row);
    return {physical:grid.scrollTop,maxPhysical:grid.scrollHeight-grid.clientHeight,coverage:window.ExcelLensPerformance().hasVisibleCoverage,
      mounted:rows.length,first:visible[0]?.row,last:visible.at(-1)?.row,visible:visible.length,minGap:Math.min(...gaps),maxGap:Math.max(...gaps),
      hitMismatches,valueMismatches,requests:window.qaLongTxtRequests,renderCommits:window.ExcelLensPerformance().commits,
      firstCells:rows[0]?.cells.slice(0,5),coordinates:visible.slice(0,8).map(({row,top,height,serialized})=>({row,top,height,serialized}))};
  },rowCount);
}
function valid(sample){
  assert(sample.coverage&&sample.visible>=5,JSON.stringify(sample));
  assert(Math.abs(sample.minGap)<2&&Math.abs(sample.maxGap)<2,'Rows overlapped or left blank gaps: '+JSON.stringify(sample));
  assert.deepEqual(sample.hitMismatches,[],'A visible row was covered by another row');
  assert.deepEqual(sample.valueMismatches,[],'Source values/types changed');
}
async function freeze(p,rows,cols){
  await p.click('#freezeBtn');
  await p.run((rows,cols)=>{document.querySelector('#freezeRowsInput').value=String(rows);document.querySelector('#freezeColsInput').value=String(cols);},rows,cols);
  await p.click('#freezeApplyBtn');await p.wait('!document.querySelector("#freezeDialog").open&&'+ready);await settle(p);
}
(async()=>{
  try{
    report.sourceSHA256Before=await hashFile(file);report.fileBytes=fs.statSync(file).size;
    h=await launch({out,errors:report.errors,duration:'12m'});const p=h.page;
    report.browser=await p.call('Browser.getVersion');report.assetSHA256={};
    for(const name of ['app.js','virtualscroll.js','style.css']){
      const served=await p.run(async name=>await(await fetch('/assets/'+name)).text(),name);
      const digest=createHash('sha256').update(served).digest('hex');
      assert.equal(digest,await hashFile(path.join(root,'internal/ui/web',name)),'Stale embedded asset '+name);report.assetSHA256[name]=digest;
    }
    await p.run(()=>{const fetchSource=window.fetch;window.qaLongTxtRequests=0;window.fetch=function(url,...args){if(String(url).startsWith('/api/sheet?'))window.qaLongTxtRequests++;return fetchSource.call(this,url,...args);};});
    await p.file('#fileInput',file);await p.wait(ready);
    const status=await p.api('/api/status');assert.equal(status.source.format,'txt');report.sourceFormat=status.source.format;
    for(const zoom of [100,70,125])for(const frozen of [false,true]){
      await freeze(p,frozen?2:0,frozen?1:0);
      await p.run(zoom=>{let current=Number(document.querySelector('#zoomValue').textContent.replace('%',''));while(current!==zoom){document.querySelector(current<zoom?'#zoomInBtn':'#zoomOutBtn').click();current=Number(document.querySelector('#zoomValue').textContent.replace('%',''));}},zoom);
      await p.wait(ready);
      await p.run(()=>{const grid=document.querySelector('#grid');grid.scrollTop=(grid.scrollHeight-grid.clientHeight)*.99997;});
      await p.wait(ready);await settle(p);
      const initial=await snapshot(p);report.checks.push({name:'deep-initial',zoom,frozen,sample:initial});
      assert(initial.first>2907000,'Did not reach deep source rows');valid(initial);
      if(zoom===100&&!frozen)await shot(p,'deep-before-wheel');
      const samples=[];
      for(let step=0;step<60;step++){
        await p.run(delta=>document.querySelector('#grid').dispatchEvent(new WheelEvent('wheel',{deltaY:delta,deltaMode:0,bubbles:true,cancelable:true})),(step%2?-1:1)*[.125,7.25,20.5][Math.floor(step/2)%3]);
        await settle(p);const sample=await snapshot(p);samples.push(sample);
        report.current={zoom,frozen,step,sample};valid(sample);
      }
      assert.equal(samples.at(-1).requests,initial.requests,'Cached fine scrolling fetched a new data tile');
      report.checks.push({name:'60-cached-reversals',zoom,frozen,steps:samples.length,requests:initial.requests,first:samples[0],last:samples.at(-1)});
      if(zoom===100&&!frozen)await shot(p,'deep-after-wheel');
      await p.run(()=>{const grid=document.querySelector('#grid');grid.scrollTop=grid.scrollHeight-grid.clientHeight;});await p.wait(ready);await settle(p);
      const end=await snapshot(p);valid(end);assert.equal(end.physical,end.maxPhysical);report.checks.push({name:'last-row',zoom,frozen,sample:end});
      await p.run(()=>{document.querySelector('#grid').scrollTop=0;});await p.wait(ready);await settle(p);
      const top=await p.run(()=>({physical:document.querySelector('#grid').scrollTop,first:document.querySelector('#rowsLayer .grid-row')?.dataset.row}));
      assert.equal(top.physical,0);assert(Number(top.first)<=5);report.checks.push({name:'return-to-first-row',zoom,frozen,...top});
    }
    assert.deepEqual(report.errors,[]);report.result='PASS';delete report.current;
  }catch(error){report.error=error.stack||String(error);if(h)try{await shot(h.page,'failure');}catch{}process.exitCode=1;
  }finally{
    report.sourceSHA256After=await hashFile(file);report.sourceUnchanged=report.sourceSHA256After===report.sourceSHA256Before;
    if(!report.sourceUnchanged){report.result='FAIL';process.exitCode=1;}
    if(h)await h.close();report.processesClosed=true;
    fs.writeFileSync(path.join(base,'long-txt-scroll-'+label+'-browser.json'),JSON.stringify(report,null,2)+'\n');
    console.log(JSON.stringify({result:report.result,label,checks:report.checks.length,current:report.current,error:report.error,sourceUnchanged:report.sourceUnchanged}));
  }
})();
