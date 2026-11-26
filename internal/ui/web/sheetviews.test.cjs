const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const source=fs.readFileSync(__dirname+'/app.js','utf8'),tick=()=>new Promise(resolve=>setImmediate(resolve));
function harness(override){
  const nodes=new Map(),calls=[],notices=[],cardResets=[],quickResets=[],detailResets=[];let timer=0;
  const node=id=>{if(!nodes.has(id)){const classes=new Set();nodes.set(id,{hidden:true,disabled:false,inert:false,scrollTop:0,scrollLeft:0,clientHeight:625,clientWidth:1200,style:{},classList:{add:x=>classes.add(x),remove:x=>classes.delete(x),contains:x=>classes.has(x)},focus(){}});}return nodes.get(id);};
  node('grid').parentElement=node('region');
  node('fontSelect').selectedOptions=[{text:'原表字体'}];
  const ctx={paintRangeFrames(){},transposeCenter:{resetBook(){},changed(){}},profileCenter:{resetBook(){},changed(){}},window:{ExcelLensTableHeaders:{fromHeader:()=>null}},traceUI:null,rowViews:null,cellExplanation:{resetBook:()=>detailResets.push(ctx.S.sheet),changed(){}},selectionQuick:{resetBook:()=>quickResets.push(ctx.S.sheet),changed(){}},recordCardCenter:{resetBook:()=>cardResets.push(ctx.S.sheet),controls(){}},geometry:{rows:{setHidden(){}}},Map,Set,Math,AbortController,URLSearchParams,encodeURIComponent,
    $:node,editCopy:null,setTimeout:()=>++timer,clearTimeout(){},cancelAnimationFrame(){},FONT_STACKS:{},
    sourceRowLimit:()=>50000000,sourceColLimit:()=>16384,rowVisible:()=>true,colName:n=>String.fromCharCode(64+n),getVirtualScrollTop:()=>node('grid').scrollTop,setVirtualScrollTop:v=>node('grid').scrollTop=v,
    frozenPanes:{cache:new Map(),cachePart(key,data){this.cache.set(key,data);},reset(){this.cache.clear();},schedule(){},async refresh(){}},pictures:{reset(){},load:async()=>{},schedule(){}},mergedCells:{load(){}},
    selectionStats:{clear(){}},formulaInspector:{clear(){}},cancelIndex(){},cancelConditionalStats(){},startConditionalStats(){},updateSourceAppearance(){},updateAutoFilterButton(){},updateFreezeUI(){},updateGeometryBase(){},updateCanvasSize(){},applyColumnVisibility(){},renderSheetTabs(){},setStatus(){},toast:(...args)=>notices.push(args),
    invalidateViewport(options){assert.equal(options.preserveDOM,true);ctx.tiles.clear();ctx.S.viewportData=null;ctx.S.window=null;},
    viewportKey:()=>ctx.S.sheet,
    async api(path,options={}){const url=new URL(path,'http://qa'),sheet=options.body?JSON.parse(options.body).sheet:url.searchParams.get('sheet');calls.push({path:url.pathname,sheet});const custom=override?.(url.pathname,sheet);if(custom!==undefined)return custom;
      if(url.pathname==='/api/index/start')return {ready:true,maxRow:80,maxCol:4,sheet};
      if(url.pathname==='/api/layout')return {rows:{2:sheet==='A'?40:55},cols:{1:110},font:'',sourceMerges:[],sourceRows:[],sourceCols:{}};
      if(url.pathname==='/api/view')return {rows:0,cols:0};
      if(url.pathname==='/api/sheet')return {sheet,rows:[{number:1,cells:[{col:1,ref:'A1',raw:sheet,display:sheet}]}]};
      throw Error('unexpected API '+path);
    },
    async loadViewport(force,epoch,strict){assert.equal(strict,true);const key=ctx.S.sheet;let entry=ctx.tiles.entries.get(key);if(!entry){const data=await ctx.api('/api/sheet?sheet='+key);entry={data,request:{},bounds:{}};ctx.tiles.entries.set(key,entry);}ctx.S.viewportData=entry;ctx.S.cellMap.set('A1',entry.data.rows[0].cells[0]);},
    selectInitialCell(){ctx.S.selected={...ctx.S.cellMap.get('A1'),row:1,col:1,sheet:ctx.S.sheet};},selectCell(cell,row,col,ref){ctx.S.selected={...cell,row,col,ref,sheet:ctx.S.sheet};},async getCell(sheet){return {raw:sheet,ref:'A1'};},cellAt:()=>({}),parseRef:()=>({row:1,col:1,ref:'A1'}),navigateToCell:async()=>{}
  };
  vm.runInNewContext(fs.readFileSync(__dirname+'/richtext.js','utf8'),ctx);
  const stateBegin=source.indexOf('  const S = {'),stateEnd=source.indexOf('\n  const cellPresenter',stateBegin);
  vm.runInNewContext(source.slice(stateBegin,stateEnd)+';this.S=S;',ctx);ctx.S.opened=true;ctx.S.source={format:'xlsx'};ctx.S.workbookKey='qa-book';
  vm.runInNewContext(source.slice(source.indexOf('  function setDisplayFontSelection('),source.indexOf('  async function changeDisplayFont(')),ctx);
  vm.runInNewContext(fs.readFileSync(__dirname+'/viewport-cache.js','utf8'),ctx);
  ctx.estimateViewportBytes=ctx.window.ExcelLensViewport.estimateViewportBytes;
  ctx.tiles=new ctx.window.ExcelLensViewport.TileCache(24*1024*1024);
  const start=source.indexOf('  function rememberSheetView('),end=source.indexOf('  async function paintInitialPreview(',start);
  ctx.sheetPresentationKey=()=>[ctx.S.zoom,ctx.S.showStyles,ctx.S.showFormulas,1].join('|');
  vm.runInNewContext(source.slice(start,end)+';this.open=selectSheet;this.remember=rememberSheetView;',ctx);
  return {ctx,S:ctx.S,calls,nodes,notices,cardResets,quickResets,detailResets,open:ctx.open};
}

test('returning to a visited sheet restores source layout and cell data without metadata reads',async()=>{
  const h=harness();await h.open('A');h.S.index={...h.S.index,ready:undefined,status:'done'};await h.open('B');h.calls.length=0;await h.open('A');
  assert.deepEqual(h.calls,[]);assert.equal(h.S.rowSizes[2],40);assert.equal(h.S.selected.raw,'A');assert.equal(h.S.sheetLoadingEpoch,0);assert.equal(h.nodes.get('grid').inert,false);
  assert.deepEqual(h.cardResets,[null,'A','B']);
  assert.deepEqual(h.quickResets,[null,'A','B']);
  assert.deepEqual(h.detailResets,[null,'A','B']);
});

test('an older layout failure cannot clear the newly selected sheet or show a stale error',async()=>{
  let reject;const pending=new Promise((_,r)=>reject=r),h=harness((path,sheet)=>path==='/api/layout'&&sheet==='A'?pending:undefined);
  const first=h.open('A');await tick();await h.open('B');reject(Error('late A failure'));await first;
  assert.equal(h.S.sheet,'B');assert.equal(h.S.rowSizes[2],55);assert.equal(h.S.indexReady,true);assert.equal(h.S.selected.raw,'B');assert.equal(h.notices.length,0);
});

test('rapid A to B to A ignores an obsolete layout response even when transport ignores abort',async()=>{
  let release;const pending=new Promise(r=>release=r),h=harness((path,sheet)=>path==='/api/layout'&&sheet==='B'?pending:undefined);
  await h.open('A');const middle=h.open('B');await tick();await h.open('A');release({rows:{2:999},cols:{},sourceMerges:[]});await middle;
  assert.equal(h.S.sheet,'A');assert.equal(h.S.rowSizes[2],40);assert.equal(h.S.selected.raw,'A');assert.equal(h.nodes.get('grid').inert,false);
});

test('view reuse is bounded and excludes incomplete views, changed filters and oversized tiles',async()=>{
  const h=harness();for(let i=0;i<9;i++)await h.open(String(i));assert(h.S.sheetViews.size<=6);assert(h.S.sheetViewsBytes<=16*1024*1024);
  h.S.filterJob='pending';const count=h.S.sheetViews.size;h.ctx.remember();assert.equal(h.S.sheetViews.size,count);h.S.filterJob=null;
  h.S.viewportData={data:{rows:[{cells:[{raw:'x'.repeat(9000000)}]}]},request:{}};h.ctx.remember();assert(!h.S.sheetViews.has(h.S.sheet));
  h.S.filtersBySheet.set('7','new-filter');h.calls.length=0;await h.open('7');assert(h.calls.some(c=>c.path==='/api/layout'&&c.sheet==='7'));
});

test('a real opening failure keeps the old grid inert and reports the actual error',async()=>{
  const h=harness((path,sheet)=>path==='/api/index/start'&&sheet==='bad'?Promise.reject(Error('source unavailable')):undefined);await h.open('A');await h.open('bad');
  assert.equal(h.S.indexReady,false);assert.equal(h.nodes.get('grid').inert,true);assert.equal(h.nodes.get('indexOverlay').hidden,false);assert.equal(h.notices[0][0],'source unavailable');
});

test('an expired filter during visible-row preparation restores source order while retaining the captured visibility',async()=>{
  const h=harness(),settings={mode:'hide-all',ranges:[{min:1,max:9,hidden:false}]},prepared=[];
  h.S.filtersBySheet.set('A','expired-filter');
  h.ctx.rowViews={cache:new Map([['A',{id:'expired-view',settings}]]),cancel(){},async prepare(options){prepared.push(options);if(prepared.length===1)throw Error('筛选结果已失效，请重新筛选');h.S.rowVisibility={hidden:[]};h.S.rowView={id:'rebuilt'};}};
  await h.open('A');assert.equal(prepared.length,2);assert.equal(prepared[1].settings,settings);assert.equal(h.S.indexReady,true);assert.equal(h.S.filtered,false);assert.equal(h.S.filterId,'');assert.equal(h.S.filtersBySheet.has('A'),false);assert.ok(h.notices.some(x=>x[0].includes('已保留行显隐')));
});

test('a slow trace destination cannot reopen the preparation overlay after the sheet is ready',async()=>{
  const h=harness(),timers=new Map();let id=0,release,started=false;
  h.ctx.setTimeout=fn=>{timers.set(++id,fn);return id;};h.ctx.clearTimeout=key=>timers.delete(key);
  h.ctx.navigateToCell=async()=>{started=true;await new Promise(resolve=>release=resolve);};
  const opening=h.open('B','A60',true);await tick();assert.equal(started,true);
  for(const fn of timers.values())fn();release();await opening;
  assert.equal(h.nodes.get('indexOverlay').hidden,true);assert.equal(h.nodes.get('grid').inert,false);
});

test('first-tile failures propagate during a sheet handoff while ordinary scroll keeps bounded retries',async()=>{
  const start=source.indexOf('  async function loadViewport('),end=source.indexOf('  function resetGridDOM(',start);
  for(const strict of [false,true]){
    let retries=0;const S={sheet:'A',sheetEpoch:1,indexReady:true,viewportRequest:0,viewportRetryAttempt:0,window:null};
    const ctx={S,gridWork:null,foregroundLookups:0,viewport:{viewportHasHeadroom:()=>false},getViewportRequest:()=>({displayStart:1,displayEnd:20,limit:20,startCol:1,cols:4}),projectViewport:()=>null,viewportCoversVisible:()=>false,planViewport:async()=>{throw Error('first tile failed');},clearTimeout(){},setTimeout(){retries++;return 1;}};
    vm.runInNewContext(source.slice(start,end)+';this.load=loadViewport;',ctx);
    if(strict)await assert.rejects(ctx.load(true,1,true),/first tile failed/);else await ctx.load(false,1);
    assert.equal(retries,strict?0:1);assert.equal(S.window,null);
  }
});
