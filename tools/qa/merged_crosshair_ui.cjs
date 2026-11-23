// Compare the actual worksheet pixels with the crosshair on and off.
// Run with an output directory and optionally "baseline" or a real October report.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {launch} = require('./browser_harness.cjs');
const {zip} = require('./generated_workbook.cjs');

function fixture() {
  const ns = 'http://schemas.openxmlformats.org/spreadsheetml/2006/main';
  const rel = 'http://schemas.openxmlformats.org/officeDocument/2006/relationships';
  const row = (r,height,content='') => `<row r="${r}" ht="${height}" customHeight="1">${content}</row>`;
  const inline = (ref,value) => `<c r="${ref}" t="inlineStr"><is><t>${value}</t></is></c>`;
  return zip({
    '[Content_Types].xml':'<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/></Types>',
    '_rels/.rels':`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="${rel}/officeDocument" Target="xl/workbook.xml"/></Relationships>`,
    'xl/workbook.xml':`<workbook xmlns="${ns}" xmlns:r="${rel}"><sheets><sheet name="合并表头" sheetId="1" r:id="rId1"/></sheets></workbook>`,
    'xl/_rels/workbook.xml.rels':`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="${rel}/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
    'xl/worksheets/sheet1.xml':`<worksheet xmlns="${ns}"><cols><col min="2" max="2" width="8" customWidth="1"/><col min="3" max="3" width="22" customWidth="1"/></cols><sheetData>${row(2,20,inline('B2','合并标题'))}${row(3,35)}${row(4,24)}${row(9,22,inline('H9','末格'))}</sheetData><mergeCells count="1"><mergeCell ref="B2:D4"/></mergeCells></worksheet>`
  });
}

// Measure the constituent rows and columns independently of the merged DOM cell.
function boxFor(ref) {
  const [,letter,number] = /^([A-Z])(\d+)$/.exec(ref);
  function header(selector) {
    for (const node of document.querySelectorAll(selector)) {
      const r=node.getBoundingClientRect();
      if (r.width && r.height && node.contains(document.elementFromPoint(r.left+r.width/2,r.top+r.height/2))) return r;
    }
    throw Error('Missing visible header for '+ref+' '+selector);
  }
  const col=header('.column-head[data-col="'+(letter.charCodeAt(0)-64)+'"]');
  const row=header('.pinned-row-head[data-row="'+number+'"],.grid-row[data-row="'+number+'"] .row-head');
  return {ref,x:col.left,y:row.top,w:col.width,h:row.height};
}

async function changedFractions(on,off,boxes) {
  async function decode(data) {
    const img=new Image();img.src='data:image/png;base64,'+data;await img.decode();
    const ctx=new OffscreenCanvas(img.width,img.height).getContext('2d');
    ctx.drawImage(img,0,0);
    return ctx.getImageData(0,0,img.width,img.height);
  }
  const [a,b]=await Promise.all([decode(on),decode(off)]),scale=a.width/innerWidth;
  return Object.fromEntries(boxes.map(box=>{
    const inset=4;
    let changed=0,count=0;
    for(let y=Math.ceil((box.y+inset)*scale);y<Math.floor((box.y+box.h-inset)*scale);y++){
      for(let x=Math.ceil((box.x+inset)*scale);x<Math.floor((box.x+box.w-inset)*scale);x++){
        const i=(y*a.width+x)*4;
        if([0,1,2].some(k=>Math.abs(a.data[i+k]-b.data[i+k])>3))changed++;
        count++;
      }
    }
    if(!count)throw Error('Empty pixel sample for '+box.ref);
    return [box.ref,changed/count];
  }));
}

async function main() {
  const [directory,mode]=process.argv.slice(2),out=path.resolve(directory);
  const baseline=mode==='baseline',report=mode&&!baseline;
  fs.mkdirSync(out,{recursive:true});
  const file=report?path.resolve(mode):path.join(out,'merged.xlsx');
  if(!report)fs.writeFileSync(file,fixture());
  const errors=[],cases=[];
  const h=await launch({errors}),p=h.page;
  const ready='window.ExcelLensPerformance().hasVisibleCoverage && !window.ExcelLensPerformance().sheetLoading';
  const settle=()=>p.evaluate('new Promise(done=>requestAnimationFrame(()=>requestAnimationFrame(done)))');
  const snap=async()=>(await p.call('Page.captureScreenshot',{format:'png',captureBeyondViewport:false})).data;
  const anchor=report?'J2':'B2';
  const merge=report?[...'JKLMNOPQ'].map(c=>c+'2'):[2,3,4].flatMap(r=>[...'BCD'].map(c=>c+r));
  const refs=report?[...merge,'J7','K7','L7']:[...merge,'B6','C6','D6','F2','F3','F4'];
  async function measure(label,ref) {
    const hit=await p.run(boxFor,ref),point={x:hit.x+hit.w*.63,y:hit.y+hit.h*.6};
    for(const type of ['mousePressed','mouseReleased'])await p.call('Input.dispatchMouseEvent',{type,...point,button:'left',clickCount:1});
    await p.call('Input.dispatchMouseEvent',{type:'mouseMoved',x:5,y:5});
    await settle();
    const expectedRef=merge.includes(ref)?anchor:ref;
    assert.equal(await p.evaluate('nameBox.value'),expectedRef,'source address for '+ref);
    const boxes=[];
    for(const r of refs)boxes.push(await p.run(boxFor,r));
    const on=await snap();
    await p.click('#crosshairToggle');await settle();
    const off=await snap();
    await p.click('#crosshairToggle');await settle();
    const fractions=await p.run(changedFractions,on,off,boxes);
    const mismatches=refs.filter(r=>{
      const onAxis=r[0]===ref[0]||r.slice(1)===ref.slice(1);
      const tint=onAxis && (r!==ref||merge.includes(ref));
      return tint?fractions[r]<.78:fractions[r]>.015;
    });
    cases.push({label,ref,sourceAddress:expectedRef,fractions,mismatches});
    if(!baseline)assert.deepEqual(mismatches,[],label+' '+ref+' '+JSON.stringify(fractions));
  }
  try {
    await p.file('#fileInput',file);await p.wait(ready);
    if(report){
      await p.run(()=>[...document.querySelectorAll('.sheet-tab')].find(n=>n.textContent.trim()==='销售费用').click());
      await p.wait(ready+' && !!document.querySelector(\'.merged-cell[data-ref="J2"]\')');
    }
    const primary=report?['J7','K7','L7','K2']:['B6','C6','D6','F2','F3','F4','B2','C3','D4','H9'];
    for(const ref of primary)await measure('reading',ref);
    if(baseline){
      for(const ref of ['B6','C6','F2','F3','C3'])assert.ok(cases.find(c=>c.ref===ref).mismatches.length,'old defect for '+ref);
    }else if(!report){
      await p.click('.grid-row .grid-cell[data-ref="A1"]');
      await p.click('#zoomInBtn');
      await p.call('Emulation.setDeviceMetricsOverride',{width:1600,height:1050,deviceScaleFactor:1.25,mobile:false});
      await p.run(()=>{grid.scrollTo(0,0);});await p.wait(ready);await settle();
      await measure('zoom', 'C3');
      for(const [rows,cols,pane] of [[4,0,'freezeTopCells'],[0,4,'freezeLeftCells'],[4,4,'freezeCorner']]){
        await p.click('#freezeBtn');await p.run((r,c)=>{freezeRowsInput.value=r;freezeColsInput.value=c;freezeApplyBtn.click();},rows,cols);
        await p.wait('!freezeDialog.open && !!document.querySelector(\'#'+pane+' .merged-cell[data-ref="B2"]\')');
        await measure(pane,'C6');await measure(pane,'F3');await measure(pane,'D4');
      }
      await p.click('#editCopyOpen');await p.wait('document.body.classList.contains("editing-copy") && !editGridSave.disabled');
      await measure('editing','C3');
      await p.run(()=>{document.querySelector('input[name="readingPreset"][value="navy"]').click();document.querySelector('input[name="crosshairColor"][value="purple"]').click();});
      await measure('dark editing','D4');await measure('dark editing','C6');
    }
    assert.deepEqual(errors,[]);
  }finally{
    fs.writeFileSync(path.join(out,'pixels.json'),JSON.stringify({mode:baseline?'baseline':report?'report':'fixed',cases,errors},null,2));
    await h.close();
  }
  console.log(cases.length+' pixel comparisons completed');
}
main().catch(e=>{console.error(e);process.exitCode=1;});
