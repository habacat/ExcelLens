// Compare complete repeated scroll measurements from render_perf_qa.cjs.
const fs=require('node:fs'),crypto=require('node:crypto'),assert=require('node:assert/strict');
const [beforeFile,afterFile,out]=process.argv.slice(2);
if(!out)throw Error('Usage: node compare_scroll.cjs before.json after.json comparison.json');
const read=f=>JSON.parse(fs.readFileSync(f,'utf8'));
const hash=f=>crypto.createHash('sha256').update(fs.readFileSync(f)).digest('hex');
const before=read(beforeFile),after=read(afterFile);
for(const key of ['sourceSHA256','size','cpu','ram','platform','node'])assert.deepEqual(before[key],after[key],key);
assert.ok(before.rounds.length>=3);assert.equal(before.rounds.length,after.rounds.length);
const median=values=>{const a=values.toSorted((x,y)=>x-y);return (a[(a.length-1)>>1]+a[a.length>>1])/2;};
const round=n=>Math.round(n*1000)/1000;
function summarize(timings,images){
  const names=timings[0].phases.map(p=>p.phase);
  const phases=Object.fromEntries(names.map(name=>{
    const rows=timings.flatMap(t=>t.phases.filter(p=>p.phase===name));
    const frames=images.flatMap(t=>t.pixels.filter(p=>p.phase===name));
    assert.equal(rows.length,timings.length);assert.ok(frames.length>=images.length);
    const sum=(rs,k)=>rs.reduce((v,r)=>v+r[k],0),pixels=sum(frames,'eligible'),checked=sum(rows,'checked');
    // FPS combines elapsed time. Percentiles summarize the separately measured
    // session percentiles; a jump is a single input, so only its latency is reported.
    const duration=rows.reduce((s,r)=>s+r.frames/r.fps,0);
    return [name,{samples:frames.length,sourceStylePixels:pixels,matchingPixels:sum(frames,'correct'),
      correctStylePct:round(100*sum(frames,'correct')/pixels),whitePixels:sum(frames,'white'),maxWhitePixels:Math.max(...frames.map(f=>f.white)),
      whitePct:round(100*sum(frames,'white')/pixels),fps:name==='jump'?null:round(sum(rows,'frames')/duration),
      medianSessionP95Ms:name==='jump'?null:round(median(rows.map(r=>r.p95))),medianSessionP99Ms:name==='jump'?null:round(median(rows.map(r=>r.p99))),
      medianSettleMs:round(median(rows.map(r=>r.settleMs))),maxSettleMs:round(Math.max(...rows.map(r=>r.settleMs))),
      sampledFrames:checked,completeContentPct:checked?round(100*sum(rows,'covered')/checked):null,
      foregroundLookups:sum(rows,'foregroundLookups'),foregroundHits:sum(rows,'foregroundHits')}];
  }));
  const rows=timings.flatMap(t=>t.phases),net=timings.reduce((v,t)=>[v[0]+t.network.calls,v[1]+t.network.cancels],[0,0]);
  const hits=rows.reduce((s,r)=>s+r.foregroundHits,0),lookups=rows.reduce((s,r)=>s+r.foregroundLookups,0);
  return {phases,requests:net[0],cancelled:net[1],cancelPct:round(100*net[1]/(net[0]||1)),foregroundLookups:lookups,foregroundHits:hits,
    foregroundHitPct:round(100*hits/(lookups||1)),maxMountedCells:Math.max(...rows.map(p=>p.nodes)),
    maxTileBytes:Math.max(...rows.map(p=>p.cacheBytes)),sampledPeakHeapBytes:Math.max(...rows.map(p=>p.heapBytes)),
    servicePeakWorkingSetBytes:Math.max(...timings.map(t=>t.workingSetPeak.serviceBytes)),
    rendererPeakWorkingSetBytes:Math.max(...timings.map(t=>t.workingSetPeak.largestRendererBytes))};
}
for(let i=0;i<before.rounds.length;i++){
  const b=before.rounds[i],a=after.rounds[i];assert.ok(b.timing.length>=2);
  for(const part of ['timing','visual'])assert.deepEqual(a[part].map(z=>z.zoom),b[part].map(z=>z.zoom));
  for(let j=0;j<b.timing.length;j++){
    assert.equal(a.timing[j].browser,b.timing[j].browser);
    assert.deepEqual(a.timing[j].phases.map(p=>p.end),b.timing[j].phases.map(p=>p.end));
    assert.deepEqual(a.visual[j].rect,b.visual[j].rect);
  }
}
const pack=r=>({executableSHA256:r.executableSHA256,all:summarize(r.rounds.flatMap(x=>x.timing),r.rounds.flatMap(x=>x.visual)),
  rounds:r.rounds.map(x=>x.timing.map((t,i)=>({zoom:t.zoom,...summarize([t],[x.visual[i]])})))});
const report={sourceSHA256:before.sourceSHA256,size:before.size,cpu:before.cpu,ram:before.ram,node:before.node,
  browser:before.rounds[0].timing[0].browser,platform:before.platform,
  rawSHA256:{before:hash(beforeFile),after:hash(afterFile)},before:pack(before),after:pack(after)};
fs.writeFileSync(out,JSON.stringify(report,null,2)+'\n');
console.log('Compared '+before.rounds.length+' rounds at '+before.rounds[0].timing.map(x=>x.zoom).join(', '));
