// Native monitor and emulated output scales exercise different rounding paths.
// Measure the screenshot against an independently painted selection in both.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {createHash} = require('node:crypto');
const {launch, pause} = require('./browser_harness.cjs');
const {fixture} = require('./grid_seam_pixels.cjs');
const {validationVersion} = require('./project_config.cjs');

const real = process.env.QA_GRID_SOURCE;
const nativeScale = Number(process.env.QA_NATIVE_SCALE || 1.25);
const emulatedScale = Number(process.env.QA_EMULATED_SCALE || 0);
const scale = emulatedScale || nativeScale;
const scaleLabel = emulatedScale ? `${nativeScale}-to-${emulatedScale}` : String(nativeScale);
const ledger=process.env.QA_GRID_LEDGER==='1';
const varied=process.argv.includes('--pens');
const out = path.resolve('validation', validationVersion(), 'private', 'selection-centres',ledger?'ledger':real?'report':varied?'pens':'fixture',scaleLabel);
const ready = 'window.ExcelLensPerformance?.().hasVisibleCoverage && !window.ExcelLensPerformance().sheetLoading';
const spec = real ? {
  single:'K7', merged:'S2', drag:['I6','G5'], rows:5, cols:8,
  seams:[['B2','S2','top'], ['J2','R2','bottom'], ['D2','S2','bottom'], ['O3','O3','bottom'], ['S2','S2','bottom'], ['B2','U4','bottom'], ['B2','B7','left'], ['S2','U7','right']]
} : {
  single:'F6', merged:'B3', drag:['E6','C5'], rows:5, cols:4,
  seams:[['B3','G3','top'], ['D3','G3','bottom'], ['B3','G4','bottom'], ['B7','G7','bottom'], ['B3','B8','left']]
};

function visibleBoxes(refs) {
  const rect = el => {
    const {left,top,right,bottom} = el.getBoundingClientRect();
    return {left,top,right,bottom};
  };
  const boxes = {}, selected = [];
  for (const ref of refs) {
    const cells = document.querySelectorAll('.grid-cell[data-ref="'+ref+'"]');
    const node = [...cells].find(el => {
      const r=el.getBoundingClientRect();
      return r.width && el.contains(document.elementFromPoint(r.left+r.width*.4,r.top+r.height*.4));
    });
    if (!node) throw Error('No visible cell for '+ref);
    boxes[ref]=rect(node);
    if(node.matches('.selected,.range-cell'))selected.push(ref);
  }
  const frames=[...document.querySelectorAll('#rangeFrames .range-frame')].map(rect);
  const handle=document.querySelector('#rangeFrames .range-corner');
  return {boxes,selected,frames,handle:handle&&rect(handle),ratio:devicePixelRatio,rasterStep:getComputedStyle(rangeFrames,'::before').borderTopWidth};
}

function markCellBounds(refs) {
  const base='#workspace .grid-region .grid-cell', style=document.createElement('style');
  style.id='qaCellMask';
  function rule(selector,green) {
    const color='rgb(252,'+green+',219)';
    return selector+'{background:'+color+' !important;border-color:'+color+' !important;color:'+color+' !important;outline:0 !important;box-shadow:none !important;}'+
      selector+' > * {visibility:hidden !important;}';
  }
  style.textContent=rule(base+':is(.selected,.range-cell)',0)+refs.map((ref,i)=>
    rule(base+'[data-ref="'+ref+'"]:not(.selected):not(.range-cell)',(i+1)*16)).join('');
  document.head.appendChild(style);
}

async function comparePixels(black,white,blackGrip,hidden,marked,hPens,vPens,geometry,ends,seams,seamRefs) {
  async function decode(data) {
    const img=new Image();img.src='data:image/png;base64,'+data;await img.decode();
    const ctx=new OffscreenCanvas(img.width,img.height).getContext('2d');
    ctx.drawImage(img,0,0);
    return ctx.getImageData(0,0,img.width,img.height);
  }
  const [on,ink,gripInk,off,mask,horizontalPens,verticalPens]=await Promise.all([black,white,blackGrip,hidden,marked,hPens,vPens].map(decode));
  const {boxes,ratio}=geometry;
  const pixel=css=>Math.round(css*ratio);
  const a=boxes[ends[0]], b=boxes[ends.at(-1)];
  const margin=Math.ceil(8*ratio);
  const x0=Math.max(0,pixel(Math.min(a.left,b.left))-margin),x1=Math.min(on.width,pixel(Math.max(a.right,b.right))+margin);
  const y0=Math.max(0,pixel(Math.min(a.top,b.top))-margin),y1=Math.min(on.height,pixel(Math.max(a.bottom,b.bottom))+margin);
  // A DOM coordinate of e.g. 332.3999939 at 125% is ambiguous at the
  // half-pixel. Read the independently painted cell boxes from the mask.
  let left=x1,right=x0,top=y1,bottom=y0;
  for(let y=y0;y<y1;y++)for(let x=x0;x<x1;x++){
    const i=(y*on.width+x)*4;
    if(mask.data[i]===252 && mask.data[i+1]===0 && mask.data[i+2]===219){
      left=Math.min(left,x);right=Math.max(right,x+1);top=Math.min(top,y);bottom=Math.max(bottom,y+1);
    }
  }
  if(left>=right||top>=bottom)throw Error('Selected cells did not paint the reference mask');
  // At a simulated output scale the compositor can duplicate a layout pixel.
  // Read each endpoint's own rasterized box, including the last physical pixel.
  function cellEdge(ref,side){
    const box=boxes[ref], horizontal=side==='left'||side==='right', leading=side==='left'||side==='top';
    const center=pixel(horizontal?(box.top+box.bottom)/2:(box.left+box.right)/2);
    const expected=pixel(box[side]), green=geometry.selected.includes(ref)?0:(seamRefs.indexOf(ref)+1)*16;
    const low=Math.max(0,expected-margin), high=Math.min((horizontal?mask.width:mask.height)-1,expected+margin);
    for(let step=0;step<=high-low;step++){
      const pos=leading?low+step:high-step;
      const i=4*(horizontal?center*mask.width+pos:pos*mask.width+center);
      if(mask.data[i]===252&&mask.data[i+1]===green&&mask.data[i+2]===219)return pos+(leading?0:1);
    }
    throw Error('No painted '+side+' edge on '+ref);
  }
  // White/black exposures isolate fractional pixel coverage from the fill and
  // text under it. The first moment gives the physical centre of each stroke.
  const profiles={},terminal={};
  const signal=(a,b,i)=>((a.data[i]-b.data[i])+(a.data[i+1]-b.data[i+1])+(a.data[i+2]-b.data[i+2]))/765;
  for(const side of ['left','right','top','bottom']){
    const vertical=side==='left'||side==='right';
    const coordinate={left,right,top,bottom}[side],pad=Math.ceil(6*ratio);
    const low=vertical?top:left,high=vertical?bottom:right,corner=Math.ceil(4*ratio);
    let measured=0,widthSamples=0,maxCenterError=0,maxWidthError=0,first=null;
    for(let along=low+corner;along<high-corner;along++){
      let sourceWeight=0,sourceMoment=0,weight=0,moment=0,crossing=false;
      for(let d=-pad;d<=pad;d++){
        const x=vertical?coordinate+d:along,y=vertical?along:coordinate+d,i=(y*on.width+x)*4;
        const grip=geometry.handle;
        if(grip&&x>=pixel(grip.left)-1&&x<=pixel(grip.right)+1&&y>=pixel(grip.top)-1&&y<=pixel(grip.bottom)+1){crossing=true;break;}
        const source=vertical?signal(verticalPens,horizontalPens,i):signal(horizontalPens,verticalPens,i);
        if(source<-.03){crossing=true;break;}
        if(source>0){sourceWeight+=source;sourceMoment+=(d+.5)*source;}
        const a=Math.max(0,signal(ink,on,i));
        weight+=a;moment+=(d+.5)*a;
      }
      if(crossing)continue;
      widthSamples++;
      const widthDiff=Math.abs(weight-2*ratio);
      maxWidthError=Math.max(maxWidthError,widthDiff);
      if(weight>0)terminal[side]=coordinate+moment/weight;
      const empty=ends.length===1&&ends[0]==='H10';
      if(!sourceWeight&&!empty)continue; // between two dashes
      const expected=sourceWeight?sourceMoment/sourceWeight:0;
      const actual=weight?moment/weight:999;
      const centerDiff=Math.abs(actual-expected);
      measured++;maxCenterError=Math.max(maxCenterError,centerDiff);
      if(!first&&(centerDiff>.1||widthDiff>.1))first={along,coordinate,expected,actual,weight,sourceWeight};
    }
    profiles[side]={measured,widthSamples,maxCenterError,maxWidthError,first};
  }
  // Only the handle changes colour in this exposure. Compare its painted
  // centroid with the measured centres of the two strokes nearest the corner.
  const grip=geometry.handle;
  let mass=0,mx=0,my=0;
  for(let y=Math.max(0,pixel(grip.top)-3);y<Math.min(on.height,pixel(grip.bottom)+3);y++){
    for(let x=Math.max(0,pixel(grip.left)-3);x<Math.min(on.width,pixel(grip.right)+3);x++){
      const weight=Math.max(0,signal(ink,gripInk,(y*on.width+x)*4));
      mass+=weight;mx+=(x+.5)*weight;my+=(y+.5)*weight;
    }
  }
  if(!mass)throw Error('No painted fill handle');
  const handle={x:mx/mass,y:my/mass,cornerX:terminal.right,cornerY:terminal.bottom};
  handle.error=Math.max(Math.abs(handle.x-handle.cornerX),Math.abs(handle.y-handle.cornerY));
  const lines=seams.map(([from,to,side])=>{
    const horizontal=side==='top'||side==='bottom';
    const start=cellEdge(from,horizontal?'left':'top'), end=cellEdge(to,horizontal?'right':'bottom');
    if(start<0||end>(horizontal?off.width:off.height)||end<=start)throw Error('Incomplete span for '+from+' to '+to);
    const edge=cellEdge(from,side);
    let best=end-start;
    // The entire stroke must exist on one pixel row/column, including the
    // junctions between spans. Looking for ink at a few midpoints misses gaps.
    for(let across=edge-Math.ceil(3*ratio);across<=edge+1;across++){
      let missing=0;
      for(let along=start;along<end;along++){
        const x=horizontal?along:across,y=horizontal?across:along,i=(y*off.width+x)*4;
        if(Math.max(off.data[i],off.data[i+1],off.data[i+2])>=160)missing++;
      }
      best=Math.min(best,missing);
    }
    return {from,to,side,length:end-start,missing:best};
  });
  return {paintedBounds:{left,top,right,bottom},profiles,lines,handlePixels:handle};
}

async function run() {
  fs.mkdirSync(out,{recursive:true});
  const errors=[],checks=[],h=await launch({errors,nativeScale}),p=h.page;
  const file=real?path.resolve(real):path.join(h.storage,'合并表头.xlsx');
  if(!real)fs.writeFileSync(file,fixture(varied));
  const digest=()=>createHash('sha256').update(fs.readFileSync(file)).digest('hex');
  const original=digest();
  const snap=async()=>(await p.call('Page.captureScreenshot',{format:'png',captureBeyondViewport:false})).data;
  async function setViewport(width,height){
    if(emulatedScale){
      await p.call('Emulation.setDeviceMetricsOverride',{width,height,deviceScaleFactor:emulatedScale,mobile:false});
    }else{
      const {windowId}=await p.call('Browser.getWindowForTarget');
      await p.call('Browser.setWindowBounds',{windowId,bounds:{width,height}});
    }
  }
  async function check(name,ends,seams=[]) {
    await p.call('Input.dispatchMouseEvent',{type:'mouseMoved',x:0,y:0});
    await p.evaluate('new Promise(done=>requestAnimationFrame(()=>requestAnimationFrame(done)))');
    const seamRefs=[...new Set(seams.flatMap(([from,to])=>[from,to]))];
    const refs=[...new Set([...ends,...seamRefs])];
    const geometry=await p.run(visibleBoxes,refs);
    const shown=await snap();
    let black,white,blackGrip,hidden,marked,hPens,vPens;
    try {
      const colourFrame=color=>{for(const key of ['--selection-color','--grid-paper'])rangeFrames.style.setProperty(key,color);};
      await p.run(colourFrame,'black');black=await snap();
      await p.run(colourFrame,'white');white=await snap();
      await p.run(()=>{
        const style=document.createElement('style');style.id='qaGripInk';
        style.textContent='#rangeFrames :is(.range-corner,.range-corner-face){fill:black!important;background:black!important;box-shadow:none!important}';
        document.head.appendChild(style);
      });
      blackGrip=await snap();
      await p.run(()=>document.getElementById('qaGripInk').remove());
      await p.run(()=>{rangeFrames.style.visibility='hidden';});
      hidden=await snap();
      const sourceColours=(h,v)=>{
        const style=document.getElementById('qaSourcePens')||document.head.appendChild(document.createElement('style'));
        style.id='qaSourcePens';
        style.textContent='.grid-region :is(.grid-cell,.grid-border-segments > i,.grid-default-right,.grid-default-bottom,.row-head,.column-headers,.pinned-row-head,.freeze-corner,.freeze-top,.freeze-left,.freeze-header){border-top-color:'+h+'!important;border-bottom-color:'+h+'!important;border-left-color:'+v+'!important;border-right-color:'+v+'!important}';
      };
      await p.run(sourceColours,'white','black');hPens=await snap();
      await p.run(sourceColours,'black','white');vPens=await snap();
      await p.run(()=>document.getElementById('qaSourcePens').remove());
      await p.run(markCellBounds,seamRefs);
      marked=await snap();
    } finally { await p.run(()=>{
      for(const id of ['qaGripInk','qaCellMask','qaSourcePens'])document.getElementById(id)?.remove();
      for(const k of ['visibility','--selection-color','--grid-paper'])rangeFrames.style.removeProperty(k);
    }); }
    const paint=await p.run(comparePixels,black,white,blackGrip,hidden,marked,hPens,vPens,geometry,ends,seams,seamRefs);
    checks.push({name,...geometry,...paint});
    if(name==='read-100' || Object.values(paint.profiles).some(v=>v.maxCenterError>.1||v.maxWidthError>.1) || paint.lines.some(l=>l.missing)) {
      fs.writeFileSync(path.join(out,name+'.png'),Buffer.from(shown,'base64'));
      fs.writeFileSync(path.join(out,name+'-source.png'),Buffer.from(hidden,'base64'));
      if(Object.values(paint.profiles).some(v=>v.maxCenterError>.1||v.maxWidthError>.1)){
        fs.writeFileSync(path.join(out,name+'-pens.png'),Buffer.from(hPens,'base64'));
        fs.writeFileSync(path.join(out,name+'-tint.png'),Buffer.from(white,'base64'));
      }
    }
    assert(Math.abs(geometry.ratio-scale)<.001);
    assert(paint.handlePixels.error<=.1,name+' fill handle misses the intersection '+JSON.stringify(paint.handlePixels));
    for(const [side,profile] of Object.entries(paint.profiles)){
      assert(profile.measured>0,name+' no source samples on '+side);
      assert(profile.maxCenterError<=.1&&profile.maxWidthError<=.1,name+' '+side+' '+JSON.stringify(profile));
    }
    for(const line of paint.lines)assert.equal(line.missing,0,name+' '+JSON.stringify(line));
    const cells=ends.map(ref=>geometry.boxes[ref]), epsilon=.04;
    assert(geometry.handle,name+' visible selection needs its fill handle');
    for(const node of geometry.frames){
      assert(node.left>=Math.min(...cells.map(r=>r.left))-epsilon && node.right<=Math.max(...cells.map(r=>r.right))+epsilon &&
        node.top>=Math.min(...cells.map(r=>r.top))-epsilon && node.bottom<=Math.max(...cells.map(r=>r.bottom))+epsilon,name+' frame outside');
    }
  }
  async function testHandle(){
    const [start,end]=await p.run(ref=>{
      const handle=document.querySelector('#rangeFrames .range-corner');
      const center=el=>{const r=el.getBoundingClientRect();return {x:r.x+r.width/2,y:r.y+r.height/2};};
      const r=handle.getBoundingClientRect(),pt={x:r.right-1,y:r.bottom-1};
      if(document.elementFromPoint(pt.x,pt.y)!==handle)throw Error('Fill handle is not reachable');
      const below=ref.replace(/\d+$/,n=>String(Number(n)+1));
      return [pt,center(document.querySelector('#rowsLayer [data-ref='+below+']'))];
    },spec.single);
    const revision=await p.evaluate('editCopyRevision.textContent');
    await p.call('Input.dispatchMouseEvent',{type:'mousePressed',...start,button:'left',buttons:1,clickCount:1});
    await p.call('Input.dispatchMouseEvent',{type:'mouseMoved',...end,button:'left',buttons:1});
    await p.wait('!!document.querySelector("#formulaEntryFrames .range-frame")');
    for(const type of ['keyDown','keyUp'])await p.call('Input.dispatchKeyEvent',{type,key:'Escape',code:'Escape',windowsVirtualKeyCode:27});
    await p.call('Input.dispatchMouseEvent',{type:'mouseReleased',...end,button:'left',buttons:0,clickCount:1});
    await p.wait('!document.querySelector("#formulaEntryFrames .range-frame")');
    assert.equal(await p.evaluate('editCopyRevision.textContent'),revision);
    if(!real){
      const target='#rowsLayer [data-ref=F7]';
      assert.equal(await p.evaluate('document.querySelector("'+target+'").textContent'),'');
      await p.call('Input.dispatchMouseEvent',{type:'mousePressed',...start,button:'left',buttons:1,clickCount:1});
      await p.call('Input.dispatchMouseEvent',{type:'mouseMoved',...end,button:'left',buttons:1});
      await p.wait('!!document.querySelector("#formulaEntryFrames .range-frame")');
      await p.call('Input.dispatchMouseEvent',{type:'mouseReleased',...end,button:'left',buttons:0,clickCount:1});
      await p.wait('document.querySelector("'+target+'").textContent==="5.54"');
      await p.click('#editGridUndo');
      await p.wait('document.querySelector("'+target+'").textContent===""');
    }
  }
  try{
    if(emulatedScale)await setViewport(1600,800);
    await p.file('#fileInput',file);await p.wait(ready);
    if(varied){
      for(const ref of ['B3','F6','D3','D4','E4','G7','H10']){
        await p.click('#rowsLayer [data-ref='+ref+']');await p.wait('nameBox.value==='+JSON.stringify(ref));
        await check('pen-'+ref,[ref]);
      }
      return;
    }
    if(ledger){
      const ref=await p.run(()=>[...document.querySelectorAll('#rowsLayer .grid-cell')].find(cell=>cell.textContent==='原币')?.dataset.ref);
      assert(ref,'ledger currency header was not found');
      for(const zoom of [100,115]){
        await p.run(z=>{while(parseInt(zoomValue.textContent)<z)zoomInBtn.click();},zoom);
        await p.wait(ready);await pause(150);
        await p.click('#rowsLayer [data-ref='+ref+']');await p.wait('nameBox.value==='+JSON.stringify(ref));
        await check('read-'+zoom,[ref]);
      }
      assert.equal(digest(),original);assert.deepEqual(errors,[]);return;
    }
    if(real && await p.evaluate('document.querySelector(".sheet-tab.active").textContent!=="销售费用"'))await p.click('.sheet-tab[title="销售费用"]');
    await p.wait(ready+' && !!document.querySelector("#rowsLayer [data-ref='+spec.single+']")');
    for(const mode of ['read','edit']){
      if(mode==='edit'){await p.click('#editCopyOpen');await p.wait('document.body.classList.contains("editing-copy") && !editGridSave.disabled');}
      for(const zoom of [100,115]){
        await p.run(zoom=>{
          while(parseInt(zoomValue.textContent)!==zoom)(parseInt(zoomValue.textContent)<zoom?zoomInBtn:zoomOutBtn).click();
          grid.scrollTo(0,0);
        },zoom);
        await p.wait(ready);await pause(200);
        await p.click('#rowsLayer [data-ref='+spec.single+']');await p.wait('nameBox.value==='+JSON.stringify(spec.single));
        await check(mode+'-'+zoom,[spec.single],spec.seams);
        if(mode==='edit' && zoom===100)await testHandle();
      }
      await p.click('#rowsLayer [data-ref='+spec.merged+']');await p.wait('nameBox.value==='+JSON.stringify(spec.merged));
      await check(mode+'-merged',[spec.merged]);
    }
    await p.click('#editGridExit');await p.wait('!document.body.classList.contains("editing-copy")');
    await p.click('#freezeBtn');await p.run((rows,cols)=>{freezeRowsInput.value=rows;freezeColsInput.value=cols;freezeApplyBtn.click();},spec.rows,spec.cols);
    await p.wait('!freezeCorner.hidden && !!freezeCorner.querySelector(".pinned-cell")');
    const cornerRef=String.fromCharCode(64+spec.cols)+spec.rows;
    await p.click('#freezeCorner [data-ref='+cornerRef+']');
    await p.wait('nameBox.value==='+JSON.stringify(cornerRef));
    await check('freeze-divider',[cornerRef]);

    await setViewport(real?900:650,620);
    await p.run(()=>{grid.scrollLeft=18;});await pause(220);
    assert(await p.evaluate('grid.scrollLeft>0'));
    const {boxes}=await p.run(visibleBoxes,spec.drag);
    const [start,end]=spec.drag.map(ref=>{const r=boxes[ref];return {x:(r.left+r.right)/2,y:(r.top+r.bottom)/2};});
    await p.call('Input.dispatchMouseEvent',{type:'mousePressed',...start,button:'left',buttons:1,clickCount:1});
    for(let i=1;i<=6;i++)await p.call('Input.dispatchMouseEvent',{type:'mouseMoved',button:'left',buttons:1,x:start.x+(end.x-start.x)*i/6,y:start.y+(end.y-start.y)*i/6});
    await p.call('Input.dispatchMouseEvent',{type:'mouseReleased',...end,button:'left',buttons:0,clickCount:1});
    await p.wait('document.querySelectorAll("#rangeFrames .range-frame").length===4');
    await check('frozen-reverse-drag',spec.drag);
    assert.equal(digest(),original);assert.deepEqual(errors,[]);
  }finally{
    fs.writeFileSync(path.join(out,'checks.json'),JSON.stringify({original,nativeScale,emulatedScale,errors,checks},null,2)+'\n');
    await h.close();
  }
  console.log(JSON.stringify({scale,cases:checks.length,continuousLines:checks.flatMap(c=>c.lines).length,worstCenterError:Math.max(...checks.flatMap(c=>Object.values(c.profiles).map(p=>p.maxCenterError)))}));
}
run().catch(err=>{console.error(err);process.exitCode=1;});
