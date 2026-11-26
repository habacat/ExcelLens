// A small worksheet leaves real unallocated space on both sides of the last cell.
const fs=require('node:fs'), path=require('node:path'), assert=require('node:assert/strict');
const {launch}=require('./browser_harness.cjs');
async function main(){
  const [file,output,mode]=process.argv.slice(2),out=path.resolve(output),baseline=mode==='baseline';
  fs.mkdirSync(out,{recursive:true});
  const errors=[],states={},h=await launch({errors}),p=h.page;
  async function select(ref){
    await p.run(ref=>{nameBox.focus();nameBox.value=ref;nameBox.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true}));},ref);
    await p.wait('nameBox.value==='+JSON.stringify(ref)+' && document.querySelector(".grid-cell.selected")');
  }
  async function measure(name,ref,enabled=true,endRef=ref){
    await p.run(()=>new Promise(done=>requestAnimationFrame(()=>requestAnimationFrame(done))));
    const obs=await p.run((ref,endRef)=>{
      const grid=document.getElementById('grid'),g=grid.getBoundingClientRect(),box=document.getElementById('crosshairTails');
      const dims=r=>({x:r.left-g.left,y:r.top-g.top,w:r.width,h:r.height});
      const cells=[...document.querySelectorAll('.grid-cell')].filter(n=>n.offsetWidth&&n.offsetHeight);
      const rects=cells.map(n=>n.getBoundingClientRect());
      const find=r=>cells.find(n=>n.dataset.ref===r && n.classList.contains('pinned-cell')) || cells.find(n=>n.dataset.ref===r);
      const selected=find(ref),last=find(endRef)||selected;
      const s=selected.getBoundingClientRect(),e=last.getBoundingClientRect();
      const boxSpan={left:Math.min(s.left,e.left),top:Math.min(s.top,e.top),width:Math.max(s.right,e.right)-Math.min(s.left,e.left),height:Math.max(s.bottom,e.bottom)-Math.min(s.top,e.top)};
      const row=box?.firstElementChild,col=box?.lastElementChild;
      return {w:grid.clientWidth,h:grid.clientHeight,x:g.left,y:g.top,scale:devicePixelRatio,selected:dims(boxSpan),pinned:selected.classList.contains('pinned-cell'),
        lastRow:Math.max(...rects.map(r=>r.bottom-g.top)),lastCol:Math.max(...rects.map(r=>r.right-g.left)),
        row:row&&dims(row.getBoundingClientRect()),col:col&&dims(col.getBoundingClientRect()),
        through:!box || getComputedStyle(box).pointerEvents==='none'};
    },ref,endRef);
    states[name]=obs;
    if(enabled){
      assert.ok(obs.w-obs.lastCol>8 && obs.h-obs.lastRow>8,'fixture must expose both margins: '+JSON.stringify(obs));
      if(!baseline){
        const close=(a,b)=>assert.ok(Math.abs(a-b)<1.1,JSON.stringify({a,b,obs}));
        close(obs.row.x,obs.lastCol);close(obs.row.x+obs.row.w,obs.w);close(obs.row.y,obs.selected.y);close(obs.row.h,obs.selected.h);
        close(obs.col.y,obs.lastRow);close(obs.col.y+obs.col.h,obs.h);close(obs.col.x,obs.selected.x);close(obs.col.w,obs.selected.w);
      }
    }else if(!baseline)assert.ok(!obs.row.w && !obs.col.h);
    assert.ok(obs.through);
    await p.shot(path.join(out,name+'.png'));
  }
  try{
    await p.call('Emulation.setDeviceMetricsOverride',{width:mode==='reading'?780:1600,height:mode==='reading'?650:1120,deviceScaleFactor:1,mobile:false});
    await p.file('#fileInput',path.resolve(file));
    await p.wait('window.ExcelLensPerformance().hasVisibleCoverage && !window.ExcelLensPerformance().sheetLoading');
    await select('D4');
    if(mode==='reading'){
      await measure('covered','D4',false);
      await p.call('Emulation.setDeviceMetricsOverride',{width:1600,height:1120,deviceScaleFactor:1,mobile:false});
    }
    await measure('reading','D4');
    if(!baseline && mode!=='reading'){
      await p.click('#crosshairToggle');await measure('off','D4',false);await p.click('#crosshairToggle');
      await p.click('#zoomOutBtn');await measure('zoom','D4');
      await p.click('#zoomInBtn');
      await p.call('Emulation.setDeviceMetricsOverride',{width:1760,height:1220,deviceScaleFactor:1.25,mobile:false});
      await measure('resized','D4');
      if(await p.evaluate('freezeCorner.hidden')) {
        await p.click('#freezeBtn');
        await p.run(()=>{freezeRowsInput.value='1';freezeColsInput.value='1';freezeApplyBtn.click();});
        await p.wait('!freezeDialog.open && !freezeCorner.hidden');
      }
      await select('A1');await measure('frozen','A1');assert.ok(states.frozen.pinned);
      await p.click('#editCopyOpen');await p.wait('document.body.classList.contains("editing-copy") && !editGridSave.disabled');
      await select('D4');await measure('editing','D4');
      await p.run(()=>{document.querySelector('input[name="readingPreset"][value="navy"]').click();document.querySelector('input[name="crosshairColor"][value="purple"]').click();});
      await measure('dark','D4');
      await p.run(()=>{nameBox.focus();nameBox.value='B3:D4';nameBox.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true}));});
      await p.wait('document.querySelector(".grid-region.has-range")');
      await measure('range','B3',true,'D4');
    }
    assert.deepEqual(errors,[]);
  }finally{
    fs.writeFileSync(path.join(out,'geometry.json'),JSON.stringify({states,errors},null,2));
    try{await h.close();}catch(e){if(e.code!=='EPERM')throw e;}
  }
}
main().catch(e=>{console.error(e);process.exitCode=1;});
