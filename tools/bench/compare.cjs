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
    candidates.push({old,file});
  }
  candidates.sort((a,b)=>b.old.finished.localeCompare(a.old.finished));
  if(!candidates.length)return {comparable:false};
  const {old,file}=candidates[0],deltas={};
  function delta(key,a,b){deltas[key]={previous:a,current:b,change:b-a,changePercent:a>0?Number((100*(b/a-1)).toFixed(2)):null};}
  for(const item of current.readings){
    const base=old.readings.find(x=>x.format===item.format&&x.rows===item.rows);
    for(const k of ['firstVisibleMs','indexRequestToReadyMs','peakServiceRSSBytes'])delta(item.format+'.'+item.rows+'.'+k,base[k],item[k]);
  }
  for(const [section,keys] of Object.entries({scroll:['fps','p95FrameMs'],reconcile:['elapsedMs','peakAdditionalDataBytes'],fullSearch:['searchMs','csvStreamAndVerifyMs'],edit:['saveAndVerifyMs'],lifecycle:['deltaRSSBytes','deltaHandles']}))
    for(const key of keys)delta(section+'.'+key,old[section][key],current[section][key]);
  for(const item of current.tabs){
    const base=old.tabs.find(x=>x.pages===item.pages);
    for(const k of ['p95StatusMs','serviceRSSBytes','serviceHandles'])delta('tabs.'+item.pages+'.'+k,base[k],item[k]);
    for(const k of ['sumWorkingSetBytes','sumHandles'])delta('tabs.'+item.pages+'.browser.'+k,base.browser[k],item.browser[k]);
  }
  return {comparable:true,baseline:path.relative(root,file).split(path.sep).join('/'),deltas};
}
module.exports={compareBaseline};
