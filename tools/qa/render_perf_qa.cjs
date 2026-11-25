// Run against an independently built tools/uiprobe. All inputs use the real scrollport and /api/sheet.
'use strict';
const fs=require('node:fs'),path=require('node:path'),crypto=require('node:crypto'),assert=require('node:assert/strict');
const {launch}=require('./browser_harness.cjs');
const os=require('node:os'),{execFileSync}=require('node:child_process');
const {fixture,colors,border}=require('./scroll_fixture.cjs');
const out=path.resolve(process.argv[2]||'artifacts/qa/scroll-render.json');
const file=path.join(path.dirname(out),'scroll-fixture.xlsx');
const retainedProfiles=[];
const quantile=(list,p)=>list.slice().sort((a,b)=>a-b)[Math.max(0,Math.ceil(list.length*p)-1)];
const hash=b=>crypto.createHash('sha256').update(b).digest('hex');
const settled='window.ExcelLensPerformance?.().hasVisibleCoverage && !window.ExcelLensPerformance().sheetLoading && window.scrollQA?.correct()';
function instruments(){
  const g=document.getElementById('grid'),id=n=>document.getElementById(n);
  const el=ref=>document.querySelector('#rowsLayer .grid-cell[data-ref="'+ref+'"]');
  const a=el('A1').getBoundingClientRect(),grid=g.getBoundingClientRect();
  // The generated workbook uses constant column widths / row heights. Calibrate cumulative
  // snapped edges over several rows and columns, independently of the viewport cache.
  const scale=parseInt(id('zoomValue').textContent)/100;
  const dx=Math.round((el('I1').getBoundingClientRect().left-a.left)/(8*scale))*scale;
  const dy=Math.round((el('A17').getBoundingClientRect().top-a.top)/(16*scale))*scale;
  const box={x:Math.ceil(a.left),y:Math.ceil(a.top),right:Math.floor(grid.left+g.clientWidth),bottom:Math.floor(grid.top+g.clientHeight)};
  const stamp=document.createElement('canvas');stamp.width=stamp.height=8;
  stamp.style.cssText='position:fixed;left:0;top:0;width:8px;height:8px;z-index:2147483647;pointer-events:none';
  document.body.append(stamp);
  const ctx=stamp.getContext('2d'),q={box,stamp,events:[],calls:0,cancels:0};
  // The baseline calls find on foreground misses in its DOM window. New builds
  // expose the equivalent foreground projection counters, including joined tiles.
  if(window.ExcelLensPerformance().foregroundLookups===undefined){
    const proto=window.ExcelLensViewport.TileCache.prototype,find=proto.find;
    q.lookups=q.hits=0;
    proto.find=function(...args){const hit=find.apply(this,args);q.lookups++;q.hits+=Number(!!hit);return hit;};
  }
  q.correct=()=>{
    let complete=true;
    for(const u of [.035,.52,.965])for(const v of [.045,.57,.96]){
      const x=box.x+(box.right-box.x)*u,y=box.y+(box.bottom-box.y)*v;
      const c=Math.floor((x-a.left+g.scrollLeft)/dx)+1,r=Math.floor((y-a.top+g.scrollTop)/dy)+1;
      // Cumulative snapped edges can differ by a pixel from the calibrated slope.
      const xf=(x-a.left+g.scrollLeft)%dx,yf=(y-a.top+g.scrollTop)%dy;
      if(Math.min(xf,dx-xf,yf,dy-yf)<1.5)continue;
      let node=document.elementFromPoint(x,y)?.closest('.grid-cell');
      const mr=r%41===1?r-1:r,merged=(c===8||c===9)&&mr>0&&mr%41===0;
      const rr=merged?mr:r,cc=merged?8:c;
      if(!node){complete=false;continue;}
      if(+node.dataset.row!==rr||+node.dataset.col!==cc||node.textContent!==(cc===31?rr+':'+cc:String(rr*1000+cc))){
        throw Error('Wrong displayed coordinate or value '+JSON.stringify({rr,cc,actual:node.dataset.ref,text:node.textContent,dx,dy,x,y}));
      }
    }
    return complete;
  };
  q.mark=(phase)=>{
    const id=q.events.length+1;ctx.fillStyle='rgb('+(id&255)+','+(id>>8&255)+',73)';ctx.fillRect(0,0,8,8);
    q.events.push({id,phase,x:g.scrollLeft,y:g.scrollTop});
  };
  window.scrollQA=q;
}
async function drive(){
  const q=window.scrollQA,g=document.getElementById('grid'),raf=()=>new Promise(requestAnimationFrame);
  const result=[];
  // Distances per wall-clock second are fixed even on a dropped frame.
  const phases=[['normal',700,0,740],['fastVertical',850,0,5400],['fastHorizontal',750,4600,0],['reverse',800,-3800,-4400],['jump',1,2700,19000]];
  for(const [phase,duration,deltaX,deltaY] of phases){
    let time=await raf(),start=time,last=time,intervals=[],covered=0,cacheReady=0,frames=0,memory=0,nodes=0,bytes=0,lastInput=0,callsAtInput=q.calls;
    const counters=()=>{const m=window.ExcelLensPerformance();return [m.foregroundLookups??q.lookups,m.foregroundHits??q.hits];};
    const [queries,hits]=counters();
    const fromX=g.scrollLeft,fromY=g.scrollTop,firstCalls=q.calls,firstCancels=q.cancels;
    do{
      time=await raf();intervals.push(time-last);last=time;
      if(frames){const ok=q.correct();covered+=Number(ok);cacheReady+=Number(ok&&q.calls===callsAtInput);}
      callsAtInput=q.calls;
      const t=Math.min(1,(time-start)/duration);
      g.scrollTo(fromX+Math.round(deltaX*t),fromY+Math.round(deltaY*t));lastInput=performance.now();q.mark(phase);
      frames++;
      const perf=window.ExcelLensPerformance();nodes=Math.max(nodes,perf.visibleNodes);bytes=Math.max(bytes,perf.bufferedApproxBytes);
      memory=Math.max(memory,performance.memory?.usedJSHeapSize||0);
    }while(time-start<duration);
    const deadline=lastInput+12000;
    while(!q.correct()||!window.ExcelLensPerformance().hasVisibleCoverage){
      if(performance.now()>deadline)throw Error('Final source coordinates/content did not settle: '+phase+' '+g.scrollLeft+','+g.scrollTop+' '+JSON.stringify({perf:window.ExcelLensPerformance(),mismatch:q.mismatch}));
      await raf();
    }
    const latency=performance.now()-lastInput;
    const [queriesEnd,hitsEnd]=counters();
    result.push({phase,foregroundLookups:queriesEnd-queries,foregroundHits:hitsEnd-hits,frames,checked:frames-1,covered,cacheReady,intervals,settleMs:latency,heapBytes:memory,nodes,cacheBytes:bytes,
      requests:q.calls-firstCalls,cancels:q.cancels-firstCancels,end:[g.scrollLeft,g.scrollTop]});
    await raf();await raf();
  }
  return {result,events:q.events,box:q.box};
}
async function imageData(encoded){
  const img=new Image();img.src='data:image/png;base64,'+encoded;await img.decode();
  const ctx=new OffscreenCanvas(img.width,img.height).getContext('2d',{willReadFrequently:true});ctx.drawImage(img,0,0);
  return ctx;
}
async function comparePixels(before,after,rect,palette){
  const a=(await window.scrollImage(before)).getImageData(rect.x,rect.y,rect.right-rect.x,rect.bottom-rect.y).data;
  const b=(await window.scrollImage(after)).getImageData(rect.x,rect.y,rect.right-rect.x,rect.bottom-rect.y).data;
  const styles=new Set(palette.map(s=>parseInt(s,16))),paper=0xffffff;
  let eligible=0,correct=0,white=0;
  for(let i=0;i<b.length;i+=4){
    const reference=b[i]*65536+b[i+1]*256+b[i+2];
    if(!styles.has(reference))continue; // exact settled source fill / border pixels, excluding text and antialiasing
    eligible++;const observed=a[i]*65536+a[i+1]*256+a[i+2];
    if(observed===reference)correct++;
    if(observed===paper)white++;
  }
  return {eligible,correct,white};
}
async function workingSetPeaks(h){
  const {webSocketDebuggerUrl}=await(await fetch(h.debugEndpoint+'/json/version')).json();
  const socket=new WebSocket(webSocketDebuggerUrl);
  try{
    await new Promise((ok,no)=>{socket.onopen=ok;socket.onerror=no;});
    const pending=new Promise((ok,no)=>{const timeout=setTimeout(()=>no(Error('Chrome process inventory timed out')),5000);socket.onmessage=e=>{
      const m=JSON.parse(e.data);if(m.id!==1)return;clearTimeout(timeout);m.error?no(Error(m.error.message)):ok(m.result.processInfo);};});
    socket.send(JSON.stringify({id:1,method:'SystemInfo.getProcessInfo'}));
    const ids=(await pending).filter(p=>p.type==='renderer').map(p=>p.id);
    const all=[h.servicePID,...ids];assert.ok(ids.length&&all.every(Number.isSafeInteger));
    let peaks;
    if(process.platform==='win32'){
      const cmd='Get-Process -Id '+all.join(',')+' | Select-Object Id,PeakWorkingSet64 | ConvertTo-Json -Compress';
      peaks=new Map(JSON.parse(execFileSync('powershell.exe',['-NoProfile','-Command',cmd],{windowsHide:true,encoding:'utf8'})).map(x=>[x.Id,x.PeakWorkingSet64]));
    }else peaks=new Map(all.map(id=>[id,Number(/VmHWM:\s*(\d+)/.exec(fs.readFileSync('/proc/'+id+'/status','utf8'))[1])*1024]));
    return {serviceBytes:peaks.get(h.servicePID),largestRendererBytes:Math.max(...ids.map(id=>peaks.get(id)))};
  }finally{socket.close();}
}
async function measure(zoom,visual){
  const errors=[],h=await launch({errors,launchFile:file}),p=h.page;
  const frames=[];
  try{
    await p.call('Emulation.setDeviceMetricsOverride',{width:1120,height:780,deviceScaleFactor:1,mobile:false});
    await p.wait('window.ExcelLensPerformance?.().hasVisibleCoverage&&!window.ExcelLensPerformance().sheetLoading');
    if(zoom!==100){await p.run(target=>{while(parseInt(document.getElementById('zoomValue').textContent)<target)document.getElementById('zoomInBtn').click();},zoom);await p.wait("document.getElementById('zoomValue').textContent==='"+zoom+"%' && window.ExcelLensPerformance().hasVisibleCoverage");}
    await p.run(instruments);
    await p.wait(settled);
    const actualZoom=await p.evaluate("document.getElementById('zoomValue').textContent");
    await p.call('Network.enable');
    const requests=new Set(),net={calls:0,cancels:0};
    const onEvent=({data})=>{
      const m=JSON.parse(data),v=m.params;
      if(m.method==='Network.requestWillBeSent'&&new URL(v.request.url).pathname==='/api/sheet'){
        requests.add(v.requestId);net.calls++;
      }else if(m.method==='Network.loadingFailed'&&requests.delete(v.requestId)&&v.canceled)net.cancels++;
      else if(m.method==='Network.loadingFinished')requests.delete(v.requestId);
      else if(m.method==='Page.screencastFrame'){
        frames.push(v.data);p.call('Page.screencastFrameAck',{sessionId:v.sessionId}).catch(()=>{});
      }
    };
    p.socket.addEventListener('message',onEvent);
    // This hook observes the same public transport in both builds, including changes of direction.
    await p.evaluate(`(()=>{const real=window.fetch;window.fetch=function(url,opt){if(new URL(url,location.href).pathname==='/api/sheet'){scrollQA.calls++;opt?.signal?.addEventListener('abort',()=>scrollQA.cancels++,{once:true});}return real.apply(this,arguments);};true;})()`);
    if(visual)await p.call('Page.startScreencast',{format:'png',everyNthFrame:1});
    const trace=await p.run(drive);
    if(visual)await p.call('Page.stopScreencast');
    p.socket.removeEventListener('message',onEvent);
    assert.deepEqual(errors,[]);
    if(!visual)return {zoom:actualZoom,browser:(await p.call('Browser.getVersion')).product,workingSetPeak:await workingSetPeaks(h),network:net,phases:trace.result.map(({intervals,...r})=>({...r,fps:1000*intervals.length/intervals.reduce((s,x)=>s+x,0),p95:quantile(intervals,.95),p99:quantile(intervals,.99)}))};
    await p.evaluate('window.scrollImage='+imageData.toString()+';true');
    const byPhase=new Map();
    for(const encoded of frames){
      const id=await p.run(async data=>{const px=(await window.scrollImage(data)).getImageData(3,3,1,1).data;return px[2]===73?px[0]+256*px[1]:0;},encoded);
      if(!id)continue;
      const point=trace.events[id-1];if(!point)continue;
      if(!byPhase.has(point.phase))byPhase.set(point.phase,new Map());
      const group=byPhase.get(point.phase);if(!group.has(id))group.set(id,{point,encoded});
    }
    const pixels=[];
    for(const {phase} of trace.result){
      const group=[...(byPhase.get(phase)?.values()||[])];assert.ok(group.length,'No composited sample for '+phase);
      for(const index of new Set([0,Math.floor((group.length-1)/3),Math.floor((group.length-1)*2/3),group.length-1])){
        const {point,encoded}=group[index];
        await p.run(({x,y})=>document.getElementById('grid').scrollTo(x,y),point);
        await p.wait(settled);await p.evaluate('new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)))');
        const ref=await p.call('Page.captureScreenshot',{format:'png',captureBeyondViewport:false});
        const sample=await p.run(comparePixels,encoded,ref.data,trace.box,[...colors,border]);
        assert.ok(sample.eligible>(trace.box.right-trace.box.x)*(trace.box.bottom-trace.box.y)*.64,'Insufficient source-style reference pixels '+JSON.stringify(point));
        pixels.push({...point,...sample});
      }
    }
    return {zoom:actualZoom,rect:trace.box,pixels};
  }finally{
    try{await h.close();}catch(error){
      if(process.platform!=='win32'||!['EPERM','EBUSY','ENOTEMPTY'].includes(error.code))throw error;
      retainedProfiles.push({profile:path.basename(h.storage),code:error.code});
    }
  }
}
(async()=>{
  fs.mkdirSync(path.dirname(out),{recursive:true});
  const source=fixture();if(!fs.existsSync(file))fs.writeFileSync(file,source);assert.equal(hash(fs.readFileSync(file)),hash(source));
  const report={retainedProfiles,cpu:os.cpus()[0].model,ram:os.totalmem(),size:[1120,780],sourceSHA256:hash(source),executableSHA256:hash(fs.readFileSync(process.env.QA_UIPROBE||'artifacts/qa/uiprobe.exe')),platform:process.platform,node:process.version,rounds:[]};
  for(let i=0;i<Number(process.env.SCROLL_ROUNDS||3);i++){
    const round={timing:[],visual:[]};report.rounds.push(round);
    for(const zoom of [100,125])for(const visual of [false,true]){
      const data=await measure(zoom,visual);round[visual?'visual':'timing'].push(data);
      fs.writeFileSync(out,JSON.stringify(report,null,2)+'\n');console.log('round',i+1,data.zoom,visual?'pixels':'timing');
    }
  }
  assert.equal(hash(fs.readFileSync(file)),report.sourceSHA256);
})().catch(e=>{console.error(e);process.exitCode=1;});
