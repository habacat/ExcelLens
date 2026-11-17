const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs'),vm=require('node:vm');
const source=fs.readFileSync(__dirname+'/../../internal/ui/web/app.js','utf8');
function harness(api){
  const S={indexReady:true,source:{format:'txt',tableHeader:{row:1,columns:['账号','金额'],detected:true}},sheet:'Data',sheetEpoch:7,textHeaderRevision:0,usedMaxRow:100,filterId:'',filterJob:null,
    sortKeysBySheet:new Map(),sortHeaderBySheet:new Map(),lastRulesBySheet:new Map(),autoFiltersBySheet:new Map()};
  const sandbox={S,window:{},URLSearchParams,api,autoFilterUI:{hide(){}},cancelFilter:async()=>{},clearFilter:async()=>{},updateAutoFilterButton(){},refreshAutoFilterHeaders(){},closeDialog(){},toast(){}};
  vm.runInNewContext(fs.readFileSync(__dirname+'/../../internal/ui/web/tableheaders.js','utf8'),sandbox);
  const start=source.indexOf('  async function applyTextTableHeader('),end=source.indexOf('  async function toggleAutoFilter(',start);
  vm.runInNewContext(source.slice(start,end)+';this.applyHeader=applyTextTableHeader;this.plain=disableTextTableHeader;',sandbox);
  return {S,...sandbox};
}

test('late manual header cannot override a newer choice or a switch to ordinary text',async()=>{
  let resolve;
  const h=harness(()=>new Promise(r=>{resolve=r;}));
  const pending=h.applyHeader(3);
  await h.plain();
  resolve({row:3,columns:['old','stale']});await pending;
  assert.equal(h.S.autoFilter,null);assert.equal(h.S.autoFiltersBySheet.has('Data'),true);
  const next=h.applyHeader(5);
  await h.applyHeader(0,true);
  resolve({row:5,columns:['old','stale']});await next;
  assert.equal(h.S.autoFilter.row,1);assert.equal(h.S.autoFilter.names[0],'账号');
});

test('a previous file header cannot alter the next workbook',async()=>{
  let resolve;const h=harness(()=>new Promise(r=>{resolve=r;}));
  const pending=h.applyHeader(4);h.S.sheetEpoch++;h.S.sheet='Next';h.S.autoFilter=null;
  resolve({row:4,columns:['old']});await pending;
  assert.equal(h.S.autoFilter,null);assert.equal(h.S.autoFiltersBySheet.has('Next'),false);
});

test('a cancelled filter finishing late cannot replace the current table view',async()=>{
  let release;
  const S={filterJob:'old-job',sheet:'Data',sheetEpoch:2,filterId:'keep-current'};
  const sandbox={S,delay:async()=>{},api:()=>new Promise(r=>{release=r;}),$:()=>({style:{}}),setStatus(){},toast(){}};
  const start=source.indexOf('  async function pollFilter('),end=source.indexOf('  async function cancelFilter(',start);
  vm.runInNewContext(source.slice(start,end)+';this.poll=pollFilter;',sandbox);
  const pending=sandbox.poll('old-job','Data',2);await new Promise(r=>setImmediate(r));
  S.filterJob='new-job';release({status:'done',filterId:'old-result',matchedRows:99});await pending;
  assert.equal(S.filterId,'keep-current');assert.equal(S.filterJob,'new-job');
});

test('a late filter start after a sheet switch is cancelled without publishing or polling',async()=>{
  let release;const cancelled=[];
  const S={sheet:'Data',sheetEpoch:2,workbookKey:'book',filterJob:null,filterRequest:0};
  const sandbox={S,api:(url,options)=>url.endsWith('/start')?new Promise(resolve=>release=resolve):(cancelled.push(JSON.parse(options.body).id),Promise.resolve({})),$:()=>({style:{}}),setStatus(){},toast(){},cancelFilter:async()=>{},pollFilter:()=>{throw Error('old source must not poll');}};
  const start=source.indexOf('  async function submitFilterRules('),end=source.indexOf('  async function adoptFilterResult(',start);
  vm.runInNewContext(source.slice(start,end)+';this.submit=submitFilterRules;',sandbox);
  const pending=sandbox.submit([{column:1,operator:'notBlank'}]);assert.match(S.filterJob,/pending:/);S.sheet='Next';S.sheetEpoch++;release({jobId:'late'});assert.equal(await pending,false);
  await new Promise(resolve=>setImmediate(resolve));assert.deepEqual(cancelled,['late']);assert.equal(S.filterJob,null);
});
