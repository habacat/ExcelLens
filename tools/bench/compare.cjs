const fs=require('node:fs'),path=require('node:path');
// Only compare equivalent fixtures, host, browser, toolchain and measurement code.
function compareBaseline(current,root,output){
  const dir=path.join(root,'validation'),candidates=[];
  for(const entry of fs.readdirSync(dir,{withFileTypes:true})){
    if(!entry.isDirectory())continue;
    const file=path.join(dir,entry.name,'performance-baseline.json');
    if(file===output||!fs.existsSync(file))continue;
    const old=JSON.parse(fs.readFileSync(file,'utf8'));
    if(old.profile!=='release'||old.result!=='PASS'||old.schema!==current.schema||old.suiteSHA256!==current.suiteSHA256)continue;
    if(['platform','os','cpu','logicalCPUs','ramBytes','browser','go','node'].some(k=>old.host[k]!==current.host[k]))continue;
    if(JSON.stringify(old.samples.map(x=>x.sha256))!==JSON.stringify(current.samples.map(x=>x.sha256)))continue;
    if(current.conditional){
      const identities=c=>[...c.statistics.map(x=>[x.rows,x.distinctKeys,x.repetitions,x.sourceSHA256]),...c.browser.map(x=>[x.mode,x.viewport,x.sourceSHA256])];
      if(!old.conditional||JSON.stringify(identities(old.conditional))!==JSON.stringify(identities(current.conditional)))continue;
    }
    candidates.push({old,file});
  }
  candidates.sort((a,b)=>b.old.finished.localeCompare(a.old.finished));
  if(!candidates.length)return {comparable:false};
  const {old,file}=candidates[0],deltas={};
  function delta(key,a,b){deltas[key]={previous:a,current:b,change:b-a,changePercent:a>0?Number((100*(b/a-1)).toFixed(2)):null};}
  for(const item of current.readings){
    const base=old.readings.find(x=>x.format===item.format&&x.rows===item.rows);
    for(const k of ['firstVisibleMs','visibleCoverageMs','indexRequestToReadyMs','peakServiceRSSBytes'])delta(item.format+'.'+item.rows+'.'+k,base[k],item[k]);
  }
  for(const [section,keys] of Object.entries({scroll:['fps','p95FrameMs'],reconcile:['elapsedMs','peakAdditionalDataBytes'],fullSearch:['searchMs','csvStreamAndVerifyMs'],edit:['saveAndVerifyMs'],lifecycle:['deltaRSSBytes','deltaHandles']}))
    for(const key of keys)delta(section+'.'+key,old[section][key],current[section][key]);
  for(const item of current.tabs){
    const base=old.tabs.find(x=>x.pages===item.pages);
    for(const k of ['p95StatusMs','serviceRSSBytes','serviceHandles'])delta('tabs.'+item.pages+'.'+k,base[k],item[k]);
    for(const k of ['sumWorkingSetBytes','sumHandles'])delta('tabs.'+item.pages+'.browser.'+k,base.browser[k],item.browser[k]);
  }
  if(current.conditional){
    for(const [i,item] of current.conditional.statistics.entries()){
      for(const k of ['sourceIndexMedianMs','preparationMedianMs','firstViewportMedianMs','warmViewportMedianMs','warmViewportP95Ms','reopenAndIndexMedianMs','reopenedViewportMedianMs','sharedIndexBytes']){
        delta('conditional.'+item.distinctKeys+'.'+k,old.conditional.statistics[i][k],item[k]);
      }
    }
    for(const k of ['medianNs','medianBytes','medianAllocs'])delta('conditional.cell.'+k,old.conditional.perCell[k],current.conditional.perCell[k]);
    for(const [i,view] of current.conditional.browser.entries()){
      const base=old.conditional.browser[i];
      for(const k of ['openToInteractiveGridMs','openToStyledGridMs'])delta('conditional.'+view.mode+'.'+k,base[k],view[k]);
      for(const k of ['peakServiceRSSBytes','peakDataBytes','peakHandles'])delta('conditional.'+view.mode+'.'+k,base.resources[k],view.resources[k]);
      for(const [p,pass] of view.passes.entries())for(const phase of pass.phases){
        const prev=base.passes[p].phases.find(x=>x.phase===phase.phase);
        if(!prev)continue;
        for(const k of ['fps','p95FrameMs','textReadyPct','conditionalReadyPct','settleMs']){
          if(Number.isFinite(prev[k])&&Number.isFinite(phase[k]))delta(['conditional',view.mode,p,phase.phase,k].join('.'),prev[k],phase[k]);
        }
      }
    }
  }
  return {comparable:true,baseline:path.relative(root,file).split(path.sep).join('/'),deltas};
}
module.exports={compareBaseline};
