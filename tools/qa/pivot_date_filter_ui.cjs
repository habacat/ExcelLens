// Drive an existing workbook through the production grid and its pivot dialog.
const fs=require('node:fs'), path=require('node:path'), assert=require('node:assert/strict');
const {launch}=require('./browser_harness.cjs');

async function run(spec,out){
  fs.mkdirSync(out,{recursive:true});
  const errors=[],steps=[],h=await launch({errors,duration:'20m'}), p=h.page;
  const id='editCopyPivotDateFilter', ready='!editGridSave.disabled && document.body.classList.contains("editing-copy")';
  async function change(suffix,value,event='change'){
    await p.run((id,value,event)=>{const n=document.getElementById(id);n.value=value;n.dispatchEvent(new Event(event,{bubbles:true}));},id+suffix,value,event);
  }
  async function sheet(){
    await p.run(s=>[...document.querySelectorAll('.sheet-tab')].find(t=>t.textContent.trim()===s).click(),spec.sheet);
    await p.wait('!window.ExcelLensPerformance().sheetLoading && document.querySelector(".sheet-tab.active").textContent.trim()==='+JSON.stringify(spec.sheet));
  }
  async function open(){
    await p.run(ref=>{nameBox.focus();nameBox.value=ref;nameBox.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true}));},spec.ref);
    await p.wait('editGridMore.textContent==="透视布局" && !editGridMore.disabled');await p.click('#editGridMore');
    await p.wait('editCopyDialog.open && !editCopyPivotDateFilters.hidden && !editCopyPivotFields.hidden && !editCopyPivotApply.disabled');
    if(!await p.evaluate('editCopyPivotDateFilters.open'))await p.click('#editCopyPivotDateFilters > summary');
    await change('Field',spec.field);
    assert.equal(await p.evaluate('editCopyPivotDateFilterField.value'),spec.field);
  }
  async function inspect(){
    const filter=await p.evaluate('({kind:editCopyPivotDateFilterKind.value,field:editCopyPivotDateFilterField.value,start:editCopyPivotDateFilterStart.value,end:editCopyPivotDateFilterEnd.value,time:editCopyPivotDateFilterTime.checked,clear:!editCopyPivotDateFilterClear.hidden,summary:editCopyPivotDateFilters.querySelector("summary").textContent})');
    if(spec.kind==='clear'){assert.equal(filter.clear,false);return;}
    assert.equal(filter.kind,spec.kind);assert.equal(filter.clear,true);
    assert.ok(filter.summary.includes(spec.field));
    for(const key of ['start','end'])if(spec[key])assert.equal(filter[key].replace(/T(\d\d:\d\d)$/,'T$1:00'),spec[key]);
    if(spec.start)assert.equal(filter.time,!spec.wholeDay);
  }
  async function save(revision,label){
    await p.wait(ready+' && editCopyRevision.textContent.startsWith("修订 '+revision+' ·")');
    await p.click('#editGridSave');await p.wait('!editGridDownload.hidden && !editGridSave.disabled');
    const url=await p.evaluate('editGridDownload.href');
    const headers=await p.run(()=>Object.fromEntries(window.ExcelLensSession.apply(new Headers({'X-ExcelLens-Token':document.querySelector('meta[name=app-token]').content}))));
    const r=await fetch(url,{headers});assert.equal(r.status,200);
    fs.writeFileSync(path.join(out,label+'.xlsx'),Buffer.from(await r.arrayBuffer()));steps.push(label);
  }
  try{
    if(spec.width)await p.call('Emulation.setDeviceMetricsOverride',{width:spec.width,height:780,deviceScaleFactor:1,mobile:false});
    await p.file('#fileInput',spec.source);await p.wait('window.ExcelLensPerformance().hasVisibleCoverage');await sheet();
    await p.click('#editCopyOpen');await p.wait(ready);await open();
    if(spec.kind==='clear'){
      await p.wait('!editCopyPivotDateFilterClear.hidden');await p.click('#'+id+'Clear');
    }else{
      await change('Kind',spec.kind);
      assert.equal(await p.evaluate('editCopyPivotDateFilterBounds.hidden'),!spec.start);
      if(spec.start){
        if(!spec.wholeDay)await p.click('#'+id+'Time');
        await change('Start',spec.start,'input');
        if(spec.end){
          await change('End',spec.wholeDay?'1900-01-01':'1900-01-01T00:00:00','input');
          assert.equal(await p.evaluate('editCopyPivotDateFilterApply.disabled && editCopyPivotDateFilterError.textContent.length>0'),true);
          await change('End',spec.end,'input');steps.push('corrected interval');
        }
      }
      await p.wait('!editCopyPivotDateFilterApply.disabled');
      const inside=await p.evaluate('(()=>{const box=editCopyPivotDateFilters;box.scrollIntoView({block:"start"});return [editCopyPivotDateFilterApply,...box.querySelectorAll("button,select,input")].filter(v=>v.offsetWidth).every(v=>{const r=v.getBoundingClientRect();return r.width>0&&r.left>=0&&r.right<=innerWidth+1&&r.top>=0&&r.bottom<=innerHeight+1})})()');
      assert.ok(inside,'filter controls fit the window');
      if(spec.shot)await p.shot(path.join(out,'date-filter.png'));
      await p.click('#'+id+'Apply');
    }
    await save(2,'changed');await open();await inspect();steps.push('current condition');
    await p.click('#editCopyPivotRefresh');await save(3,'refresh');
    for(const [revision,button,label] of [[4,'Undo','undo-refresh'],[5,'Undo','undo'],[6,'Redo','redo'],[7,'Redo','redo-refresh']]){
      await p.click('#editGrid'+button);await save(revision,label);
    }
    await p.file('#fileInput',path.join(out,'redo-refresh.xlsx'));await p.wait('!document.body.classList.contains("editing-copy") && window.ExcelLensPerformance().hasVisibleCoverage');
    await sheet();await p.click('#editCopyOpen');await p.wait(ready);await open();await inspect();steps.push('reopened condition');
    assert.deepEqual(errors,[]);
  }finally{
    fs.writeFileSync(path.join(out,'browser.json'),JSON.stringify({steps,errors},null,2));
    try{await h.close();}catch(e){if(e.code!=='EPERM')throw e;}
  }
}
run(JSON.parse(fs.readFileSync(process.argv[2],'utf8')),path.resolve(process.argv[3])).catch(e=>{console.error(e);process.exitCode=1;});
