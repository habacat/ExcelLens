const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs'),vm=require('node:vm');
const context={window:{}};
vm.runInNewContext(fs.readFileSync(__dirname+'/virtualscroll.js','utf8'),context);
const VirtualScroll=context.window.ExcelLensVirtualScroll;
const app=fs.readFileSync(__dirname+'/app.js','utf8');

test('repeated wheel/trackpad down and back lands exactly at row one, at every supported zoom',()=>{
  for(const count of [100,1048576,2907951,50000000,1000000000])for(const zoom of [.7,1,1.25]){
    const s=new VirtualScroll(),height=27*zoom+count*25*zoom,view=763;
    const physicalHeight=s.configure(height,view);
    assert.ok(physicalHeight<=15000000);
    let top=0;
    for(let cycle=0;cycle<25;cycle++){
      for(let i=0;i<400;i++)top=s.wheel({deltaY:19.75,deltaMode:0},top,25*zoom,view).top;
      assert.ok(Math.abs(s.logical(top)-7900)<.0001||top===s.physicalMax);
      for(let i=0;i<401;i++)top=s.wheel({deltaY:-19.75,deltaMode:0},top,25*zoom,view).top;
      assert.equal(top,0,`rows=${count} zoom=${zoom} cycle=${cycle}`);
      assert.equal(s.logical(top),0);
    }
  }
});

test('native scrollbar endpoints cover the complete source and logical positions round trip',()=>{
  const s=new VirtualScroll();s.configure(25000000027,630);
  for(const value of [0,.01,25,50000,100000000,s.logicalMax])assert.ok(Math.abs(s.logical(s.physical(value))-value)<.00001);
  assert.equal(s.logical(s.physicalMax),s.logicalMax);
  assert.equal(s.physical(-100),0);assert.equal(s.physical(Infinity),s.physicalMax);
  assert.equal(s.logical(-1),0);
});

test('fractional touchpad input accumulates despite native subpixel quantization',()=>{
  const s=new VirtualScroll();s.configure(1250000027,700);
  let top=0;
  const step=delta=>{top=Math.round(s.wheel({deltaY:delta,deltaMode:0},top,25,673).top*64)/64;};
  for(let i=0;i<10000;i++)step(.1);
  assert.ok(Math.abs(s.logical(top)-1000)<2,'tiny deltas disappeared at the physical scrollbar');
  for(let i=0;i<10001;i++)step(-.1);
  assert.equal(top,0);
  top=s.physical(10000000);step(-25);
  assert.ok(Math.abs(s.logical(top)-9999975)<2,'native thumb drag reused the old wheel origin');
});

test('line/page wheels retain logical pixel distance and horizontal wheel does not jump rows',()=>{
  const s=new VirtualScroll();s.configure(73000000,700);
  const top=s.physical(10000);
  assert.ok(Math.abs(s.logical(s.wheel({deltaY:-3,deltaMode:1},top,25,673).top)-9925)<1e-8);
  assert.ok(Math.abs(s.logical(s.wheel({deltaY:1,deltaMode:2},top,25,673).top)-10673)<1e-8);
  const horizontal=s.wheel({deltaY:80,deltaX:0,deltaMode:0,shiftKey:true},top,25,673);
  assert.equal(horizontal.top,top);assert.equal(horizontal.left,80);
});

test('actual cached-row scroll follower keeps row screen positions correct without repaint or fetch',()=>{
  const s=new VirtualScroll();s.configure(73000000,700);
  const grid={scrollTop:0};
  const S={renderedScrollTop:0,renderedRows:new Map(Array.from({length:80},(_,i)=>[i,{style:{top:`${i*25}px`}}]))};
  const sandbox={S,virtualScroll:s,$:()=>grid,getVirtualScrollTop:()=>s.logical(grid.scrollTop)};
  const start=app.indexOf('  function followGridScroll() {'),end=app.indexOf('  function displayCellValue(',start);
  assert.ok(start>0&&end>start);vm.runInNewContext(app.slice(start,end)+';this.follow=followGridScroll;',sandbox);
  for(const logical of [300,350,275,5000,10000000,50,0,250,0]){
    grid.scrollTop=s.physical(logical);sandbox.follow();
    for(const [i,row]of S.renderedRows)assert.ok(Math.abs(parseFloat(row.style.top)-grid.scrollTop-(i*25-logical))<.00001);
  }
  assert.ok(Math.abs(parseFloat(S.renderedRows.get(0).style.top))<.00001);
});

test('native anchoring is disabled and overscan cannot extend the authoritative scroll range',()=>{
  const css=fs.readFileSync(__dirname+'/style.css','utf8');
  assert.match(css,/\.grid-scroll\s*\{[^}]*overflow-anchor:\s*none/);
  assert.match(css,/\.rows-layer\s*\{[^}]*bottom:\s*0[^}]*overflow:\s*clip/);
});
