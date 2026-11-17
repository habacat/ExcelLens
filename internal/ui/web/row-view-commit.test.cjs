const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const source=fs.readFileSync(__dirname+'/app.js','utf8'),tick=()=>new Promise(resolve=>setImmediate(resolve));
function deferred(){let resolve;const promise=new Promise(yes=>resolve=yes);return {promise,resolve};}
function harness(){
  const nodes=new Map(),calls=[],cleared=[],installed=[],selected=[],cardResets=[],quickResets=[],detailResets=[];
  const node=id=>{if(!nodes.has(id))nodes.set(id,{style:{},hidden:false,disabled:false,scrollTop:0,scrollLeft:0});return nodes.get(id);};
  const S={sheet:'A',sheetEpoch:1,workbookKey:'book',filterRequest:1,filterId:'old',filterJob:'query',filterTotal:3,filtered:true,rowView:{id:'old-view'},rowVisibility:{hidden:[]},sheetViews:new Map(),filterTotalsBySheet:new Map([['A',3]]),filtersBySheet:new Map([['A','old']]),lastRulesBySheet:new Map(),selected:null,pendingFilterSpec:{sheet:'A',rules:[{column:1,operator:'contains',value:'pending'}]}};
  const ctx={transposeCenter:{resetBook(){},changed(){}},profileCenter:{resetBook(){},changed(){}},S,$:node,AbortController,URLSearchParams,encodeURIComponent,Math,geometry:{rows:{setHidden(){}}},selectionStats:{clear(){}},cellExplanation:{resetBook:()=>detailResets.push(S.rowView?.id)},selectionQuick:{resetBook:()=>quickResets.push(S.rowView?.id)},recordCardCenter:{resetBook:()=>cardResets.push(S.rowView?.id)},
    rowViews:{serial:0,async prepareFilter(filterId){this.serial++;return {id:'prepared',filterId,totalRows:2};},validate(){},install(data){assert.equal(data.filterId,S.filterId);S.rowView=data;installed.push(data.id);},cleanup:id=>cleared.push(id)},
    async api(url,options){calls.push({url,body:options?.body&&JSON.parse(options.body)});return url.startsWith('/api/filter/status')?{status:'done',filterId:'new',matchedRows:2}:{};},
    filteredBodyCount:()=>S.rowView.totalRows,invalidateViewport(){},updateCanvasSize(){},async loadViewport(){},selectInitialCell:()=>selected.push('initial'),frozenPanes:{reset(){},schedule(){},effective:()=>({rows:0})},
    toast(){},setStatus(){},delay:async()=>{},parseRef:()=>null,getVirtualScrollTop:()=>0,rowAtOffset:()=>0,sourceRowTop:()=>0,setVirtualScrollTop(){},nextVisibleRow:row=>row,previousVisibleRow:row=>row,rowVisible:()=>true,navigateToCell:async()=>selected.push('navigation'),refreshAutoFilterHeaders(){}
  };
  for(const [start,end]of [['  async function commitRowView(','  function captureNamedView('],['  async function pollFilter(','  function parseFormulaReferences(']]){
    const a=source.indexOf(start),b=source.indexOf(end,a);assert.ok(a>=0&&b>a);vm.runInNewContext(source.slice(a,b),ctx);
  }
  return {ctx,S,calls,cleared,installed,selected,cardResets,quickResets,detailResets};
}

test('cancelling a filter while its visible projection is pending retains the confirmed view and rules',async()=>{
  const h=harness(),pending=deferred();let signal;
  h.ctx.rowViews.prepareFilter=async(filterId,options)=>{h.ctx.rowViews.serial++;signal=options.signal;return pending.promise;};
  const poll=h.ctx.pollFilter('query');await tick();await h.ctx.cancelFilter('query');assert.equal(signal.aborted,true);
  pending.resolve({id:'late',filterId:'new',totalRows:2});assert.equal(await poll,false);
  assert.equal(h.S.filterId,'old');assert.equal(h.S.rowView.id,'old-view');assert.equal(h.S.lastRulesBySheet.size,0);assert.deepEqual(h.installed,[]);assert.deepEqual(h.cleared,['late']);
  assert.deepEqual(h.cardResets,[]);
});

test('an obsolete filter intention cannot adopt a response even if the projection transport ignores cancellation',async()=>{
  for(const reason of ['request','source','projection']){
    const h=harness(),pending=deferred();h.ctx.rowViews.prepareFilter=async()=>{h.ctx.rowViews.serial++;return pending.promise;};
    const applying=h.ctx.adoptFilterResult({filterId:'new',matchedRows:2});await tick();
    if(reason==='request')h.S.filterRequest++;else if(reason==='source')h.S.sheetEpoch++;else h.ctx.rowViews.serial++;
    pending.resolve({id:'late',filterId:'new',totalRows:2});assert.equal(await applying,false);assert.equal(h.S.filterId,'old');assert.deepEqual(h.installed,[]);assert.deepEqual(h.cleared,['late']);
    assert.equal(h.calls.some(x=>x.url==='/api/filter/clear'),false);
  }
});

test('a delayed clear cannot remove a newly selected filter or overwrite its selection',async()=>{
  const h=harness(),pending=deferred();h.S.filterJob=null;h.ctx.api=async(url)=>url==='/api/filter/clear'?pending.promise:{};
  const clearing=h.ctx.clearFilter();await tick();h.S.filterRequest++;h.S.filterId='latest';h.S.filtersBySheet.set('A','latest');h.S.rowView={id:'latest-view'};pending.resolve({});await clearing;
  assert.equal(h.S.filterId,'latest');assert.equal(h.S.filtersBySheet.get('A'),'latest');assert.equal(h.S.rowView.id,'latest-view');assert.deepEqual(h.selected,[]);assert.deepEqual(h.cleared,['prepared']);
});

test('a row-view commit that finishes loading late cannot select or navigate over a newer view',async()=>{
  const h=harness(),pending=deferred();h.S.filtered=false;h.S.filterId='';h.S.selected={ref:'A1'};h.ctx.parseRef=()=>({row:1});h.ctx.loadViewport=()=>pending.promise;
  const committing=h.ctx.commitRowView({id:'commit',filterId:''});await tick();h.S.rowView={id:'latest'};pending.resolve();assert.equal(await committing,false);assert.deepEqual(h.selected,[]);
  assert.deepEqual(h.cardResets,['old-view']);
  assert.deepEqual(h.quickResets,['old-view']);
  assert.deepEqual(h.detailResets,['old-view']);
});

test('a successful adoption updates the view before selecting while later viewport responses stay guarded',async()=>{
  const h=harness();h.S.filterJob=null;assert.equal(await h.ctx.adoptFilterResult({filterId:'new',matchedRows:2}),true);
  assert.equal(h.S.filterId,'new');assert.equal(h.S.rowView.id,'prepared');assert.equal(h.S.filterTotal,2);assert.deepEqual(h.selected,['initial']);assert.deepEqual(h.cleared,[]);
  assert.deepEqual(h.cardResets,['old-view']);
  assert.deepEqual(h.quickResets,['old-view']);
  assert.deepEqual(h.detailResets,['old-view']);
});
