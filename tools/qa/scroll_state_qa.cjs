// Exercise sparse merge anchors, true empty/edit state and revision changes through the real grid.
const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict'),crypto=require('node:crypto');
const {launch}=require('./browser_harness.cjs'),{fixture}=require('./scroll_fixture.cjs'),{workbook}=require('./generated_workbook.cjs');
async function coldStyles(p,ready){
  const measured=await p.run(()=>{
    const a=document.querySelector('#rowsLayer [data-ref="A1"]').getBoundingClientRect();
    const fetchSource=window.fetch;
    window.fetch=async function(url,...rest){
      const hold=window.scrollHold;
      if(hold&&new URL(url,location.href).pathname==='/api/sheet'){hold.waiting=true;await hold.promise;}
      return fetchSource.call(this,url,...rest);
    };
    return {x:a.left,y:a.top,w:a.width,h:a.height};
  });
  const receipts=[];
  for(const [preset,row,col] of [['off',1230,65],['navy',440,18]]){
    await p.click('input[name=readingPreset][value='+preset+']');
    try{
      await p.run((m,row,col)=>{
        const hold={waiting:false};hold.promise=new Promise(ok=>{hold.release=ok;});window.scrollHold=hold;
        document.getElementById('grid').scrollTo(col*m.w,row*m.h);
      },measured,row,col);
      await p.wait('window.scrollHold.waiting && !tabActivity.hidden && viewportActivitySlot.contains(tabActivity)');
      const outside=await p.run(()=>tabActivity.getBoundingClientRect().top>=document.getElementById('grid').getBoundingClientRect().bottom);
      assert.ok(outside,'Visible loading feedback must leave the worksheet unobscured');
      const shot=await p.call('Page.captureScreenshot',{format:'png',captureBeyondViewport:false});
      const sample=await p.run(async (encoded,m)=>{
        const img=new Image();img.src='data:image/png;base64,'+encoded;await img.decode();
        const ctx=new OffscreenCanvas(img.width,img.height).getContext('2d',{willReadFrequently:true});ctx.drawImage(img,0,0);
        const ratio=img.width/innerWidth,colors=[];
        for(const x of [1.5,3.5,5.5])for(const y of [2.5,6.5,10.5]){
          colors.push(Array.from(ctx.getImageData(Math.round((m.x+x*m.w)*ratio),Math.round((m.y+y*m.h)*ratio),1,1).data).slice(0,3).join(','));
        }
        ctx.fillStyle=getComputedStyle(document.getElementById('gridCanvas')).backgroundColor;ctx.fillRect(0,0,1,1);
        return {expected:Array.from(ctx.getImageData(0,0,1,1).data).slice(0,3).join(','),colors};
      },shot.data,measured);
      // CSS and Canvas can round a half-channel differently on 8-bit output.
      const expected=sample.expected.split(',').map(Number);
      assert.equal(new Set(sample.colors).size,1,JSON.stringify({preset,...sample}));
      assert.ok(sample.colors[0].split(',').every((v,i)=>Math.abs(Number(v)-expected[i])<=1),JSON.stringify({preset,...sample}));
      const point={x:measured.x+3.5*measured.w,y:measured.y+6.5*measured.h};
      const selected=()=>p.evaluate('({address:nameBox.value,value:formulaText.textContent})');
      const before=await selected();
      await p.call('Input.dispatchMouseEvent',{type:'mousePressed',button:'left',buttons:1,clickCount:1,...point});
      await p.call('Input.dispatchMouseEvent',{type:'mouseReleased',button:'left',clickCount:1,...point});
      assert.deepEqual(await selected(),before,'An unknown cell cannot supply a value');
      receipts.push({preset,expected:sample.expected,observed:sample.colors[0],matchingPoints:sample.colors.length,statusBelowGrid:outside,unknownPressPreservedSelection:true});
    }finally{await p.run(()=>{window.scrollHold?.release();window.scrollHold=null;});}
    await p.wait(ready);
  }
  await p.click('input[name=readingPreset][value=off]');
  await p.run(()=>document.getElementById('grid').scrollTo(0,0));await p.wait(ready+' && !!document.querySelector("#rowsLayer [data-ref=A1]")');
  return receipts;
}
async function clickDuringScroll(page){
  const hits=[];
  for(const direction of [1,-1]){
    const point=await page.run(direction=>new Promise(resolve=>{
      const g=document.getElementById('grid'),first=g.querySelector('.grid-cell[data-ref=A1]')||g.querySelector('.grid-cell[data-col="1"]');
      const a=first.getBoundingClientRect(),port=g.getBoundingClientRect(),origin=g.querySelector('.rows-layer').getBoundingClientRect();
      const head=g.querySelector('.column-headers').getBoundingClientRect().height;
      const x=port.left+parseFloat(getComputedStyle(g).getPropertyValue('--row-header-width'))+2.5*a.width;
      const y=port.top+head+g.clientHeight*(direction>0?.75:.2);
      const q={events:[],running:true};window.clickQA=q;
      const top=g.scrollTop,started=performance.now(),rowZero=origin.top+top;
      function clicked(e){
        const target=e.target.closest('.grid-cell');
        const r=Math.floor((e.clientY-rowZero+g.scrollTop)/a.height)+1;
        q.pressedAt=g.scrollTop;
        q.events.push({direction,trusted:e.isTrusted,moving:q.running,expected:'C'+r,ref:target?.dataset.ref,
          expectedValue:String(r*1000+3),value:target?.textContent,target:e.target.className,
          x:e.clientX,y:e.clientY,scrollTop:g.scrollTop});
      }
      document.addEventListener('pointerdown',clicked,{capture:true,once:true});
      function move(t){
        const f=Math.max(0,Math.min(1,(t-started)/950));g.scrollTop=top+direction*Math.round(4500*f);
        if(f>=.15)resolve({x,y});
        if(f<1)requestAnimationFrame(move);
        else q.running=false;
      }
      requestAnimationFrame(move);
    }),direction);
    await page.call('Input.dispatchMouseEvent',{type:'mousePressed',button:'left',buttons:1,clickCount:1,...point});
    const selection=await page.evaluate('({selected:nameBox.value,bar:formulaText.textContent.trim()})');
    await page.wait('Math.abs(grid.scrollTop-clickQA.pressedAt)>30 || !clickQA.running');
    await page.call('Input.dispatchMouseEvent',{type:'mouseReleased',button:'left',clickCount:1,...point});
    await page.wait('!clickQA.running');
    const events=await page.evaluate('clickQA.events');assert.equal(events.length,1);
    const hit=Object.assign(events[0],selection);
    assert.ok(hit.trusted&&hit.moving&&Math.abs(await page.evaluate('grid.scrollTop')-hit.scrollTop)>30,JSON.stringify(hit));
    if(hit.ref){assert.equal(hit.ref,hit.expected,JSON.stringify(hit));assert.equal(hit.value,hit.expectedValue);}
    assert.equal(hit.selected,hit.expected);assert.equal(hit.bar,hit.expectedValue);
    assert.equal(await page.evaluate('nameBox.value'),hit.expected);hits.push(hit);
  }
  await page.run(()=>document.getElementById('grid').scrollTo(0,0));
  return hits;
}
(async()=>{
  const output=path.resolve(process.argv[2]||'artifacts/qa/scroll-state.json');
  fs.mkdirSync(path.dirname(output),{recursive:true});
  const file=path.join(path.dirname(output),'long-merge.xlsx');fs.writeFileSync(file,fixture(['AR50:BI1400']));
  const digest=()=>crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex'),before=digest(),errors=[];
  const h=await launch({errors,launchFile:file}),p=h.page;
  const ready='window.ExcelLensPerformance?.().hasVisibleCoverage&&!window.ExcelLensPerformance().sheetLoading';
  try{
    await p.wait(ready);
    const cold=await coldStyles(p,ready),hits=await clickDuringScroll(p);
    await p.wait(ready+' && !!document.querySelector("#rowsLayer [data-ref=A1]")');
    const point=await p.run(()=>{
      const a=document.querySelector('[data-ref="A1"]').getBoundingClientRect(),g=document.getElementById('grid');
      g.scrollTo(46*a.width,680*a.height);
      return {x:a.x+2*a.width+8,y:a.y+3*a.height+4};
    });
    await p.wait(ready+' && !!document.querySelector("#rowsLayer [data-ref=AR50]")');
    const anchor=await p.run(({x,y})=>{const el=document.elementFromPoint(x,y)?.closest('.grid-cell');return [el?.dataset.ref,el?.textContent];},point);
    assert.deepEqual(anchor,['AR50','50044']);const maxNodes=await p.evaluate('window.ExcelLensPerformance().visibleNodes');assert.ok(maxNodes<4000);
    const go=async ref=>{await p.run(ref=>{nameBox.focus();nameBox.value=ref;nameBox.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true}));},ref);
      await p.wait(ready+' && document.querySelector(".grid-cell.selected")?.dataset.ref==='+JSON.stringify(ref));};
    // The rich-text scroll fixture exercises read-only presentation. Use a plain
    // editable workbook for the revision check, through the ordinary preflight.
    const editable=path.join(path.dirname(output),'editable-scroll.xlsx');
    const original=workbook([{name:'Edit',rows:Array.from({length:1000},(_,i)=>['row '+(i+1),'B'+(i+1),'C','D'])}]);
    fs.writeFileSync(editable,original);await p.file('#fileInput',editable);
    await p.wait(ready+' && document.querySelector(".sheet-tab.active")?.textContent.trim()==="Edit"');
    await go('B3');await p.click('#editCopyOpen');await p.wait('document.body.classList.contains("editing-copy")&&!editGridSave.disabled');
    await go('B3');await p.run(()=>{editGridFormula.focus();editGridFormula.select();});
    await p.call('Input.insertText',{text:'changed-at-B3'});
    for(const type of ['rawKeyDown','keyUp'])await p.call('Input.dispatchKeyEvent',{type,key:'Enter',windowsVirtualKeyCode:13});
    await p.wait('editCopyRevision.textContent.startsWith("修订 1")');
    await p.wait('document.querySelector("#rowsLayer [data-ref=B3]")?.textContent==="changed-at-B3"');
    await go('D950');await go('B3');
    assert.equal(await p.evaluate('document.querySelector(".grid-cell.selected").textContent'),'changed-at-B3');
    assert.deepEqual(fs.readFileSync(editable),original);assert.equal(digest(),before);assert.deepEqual(errors,[]);
    fs.writeFileSync(output,JSON.stringify({result:'PASS',cold,hits,anchor,maxNodes,revision:1,sourceSHA256:before,sourceUnchanged:true,errors},null,2)+'\n');
    console.log('PASS sparse merged anchor and edited value after a distant jump and return');
  }catch(error){
    const state=await p.evaluate('({address:nameBox.value,focus:document.activeElement.id,revision:editCopyRevision.textContent,edit:editGridStatus.textContent,cell:document.querySelector(".grid-cell.selected")?.textContent,sourceB3:document.querySelector("#rowsLayer [data-ref=B3]")?.textContent,perf:window.ExcelLensPerformance()})');
    throw new Error('Scroll state at failure '+JSON.stringify(state),{cause:error});
  }finally{
    try{await h.close();}catch(error){if(error.code!=='EPERM'||process.platform!=='win32')throw error;console.warn('Locked test profile',h.storage);}
  }
})().catch(e=>{console.error(e);process.exitCode=1;});
