const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');

const app=fs.readFileSync(__dirname+'/app.js','utf8');
function section(first,last){
  const start=app.indexOf('  '+first),end=app.indexOf('  '+last,start+1);
  assert.ok(start>=0&&end>start);
  return app.slice(start,end);
}

test('a clickable first preview paints the same single-cell frame as the indexed sheet',()=>{
  const state={previewOnly:true,indexReady:false,selected:{row:3,col:2},selectedVisualIndex:3};
  const classes=new Set(),frames=[];
  const layer={childElementCount:1,parentElement:{classList:{toggle(name,on){if(on)classes.add(name);else classes.delete(name);}}}};
  const grid={clientWidth:500,clientHeight:300,scrollLeft:0};
  const context={S:state,$:id=>id==='grid'?grid:id==='rangeFrames'?layer:{childElementCount:0},
    editCopy:null,colTotal:()=>260,sheetHeight:()=>180,
    window:{ExcelLensMultiSelection:{areas:()=>[]},ExcelLensRangeFrame:{paintCrosshair(){},paint:(_,areas)=>frames.push(JSON.stringify(areas))}},
    activeMerge:()=>null,crosshairCell:()=>state.selected,frozenPanes:{effective:()=>({rows:0,cols:0})},getVirtualScrollTop:()=>0,headerHeight:()=>20,
    ROW_HEAD:28,colLeft:col=>(col-1)*64,sourceRowTop:row=>(row-1)*20};
  const paint=vm.runInNewContext(section('function paintRangeFrames(', 'function decorateGridCell(')+';paintRangeFrames',context);
  paint();
  assert.ok(classes.has('has-cell-selection'));
  assert.deepEqual(JSON.parse(frames[0]),[{row1:3,col1:2,row2:3,col2:2,visual1:3,visual2:3}]);
  state.previewOnly=false;state.indexReady=true;paint();
  assert.equal(frames[1],frames[0]);
  state.indexReady=false;paint();
  assert.equal(frames[2],'[]');assert.ok(!classes.has('has-cell-selection'));
});

test('navigation keeps wide cells and tall rows visible between frozen headers and the viewport edge',()=>{
  const reveal=vm.runInNewContext(section('function revealCellOffset(', 'function scrollCellIntoView(')+';revealCellOffset');
  for(const offset of [0,578]) {
    assert.equal(reveal(offset,352,26,162,384,384),136);
    assert.equal(reveal(offset,420,120,200,500,1000),80);
  }
  assert.equal(reveal(0,360,80,300,200,200),220);
  assert.equal(reveal(75,100,100,140,64,64),75);
});

function harness({row=6,visual=2,pinned=0,rows=[[1,4],[2,6],[6,3]],total=6}={}){
  const state={book:'book',workbookKey:'key',sheet:'账表',sheetEpoch:1,indexReady:true,
    filtered:true,filterId:'sort',filterTotal:total,rowView:{id:'view'},
    window:{excludeThroughRow:pinned},selected:{row,col:1,ref:'A'+row},selectedVisualIndex:visual,selectedFrozenThrough:pinned,
    rowNumberByVisual:new Map(rows),cellMap:new Map(),frozenCellMap:new Map(),
    rangeAnchor:{row,col:1,visual},multiRanges:[],hiddenColumns:new Set()};
  for(let r=1;r<=6;r++)for(const c of ['A','B','C'])state.cellMap.set(c+r,{raw:c+r});
  const grid={scrollTop:0,scrollLeft:0,clientHeight:300,clientWidth:400,focus(){},
    scrollTo({top,left}){this.scrollTop=top;this.scrollLeft=left;}};
  const selected=[],warnings=[],ranges=[];
  const ctx={
    S:state,$:()=>grid,ROW_HEAD:36,URLSearchParams,
    parseRef:ref=>({ref,row:Number(ref.slice(1)),col:ref.charCodeAt(0)-64}),
    colName:c=>String.fromCharCode(c+64),activeMerge:()=>null,
    frozenPanes:{effective:()=>({rows:pinned,cols:0})},
    advanceVisibleRow:(r,d)=>Math.max(1,r+d),previousVisibleRow:r=>r,nextVisibleRow:r=>r,
    ensureExtent(){},headerHeight:()=>27,sourceRowTop:r=>(r-1)*25,rowTop:r=>(r-1)*25,
    colLeft:c=>(c-1)*80,rowHeight:()=>25,colWidth:()=>80,
    getVirtualScrollTop:()=>grid.scrollTop,scrollTopToPhysical:v=>v,setVirtualScrollTop:v=>{grid.scrollTop=v;},
    cellAt:(r,c)=>state.cellMap.get(String.fromCharCode(c+64)+r)||{raw:''},
    recordVisit(){},getViewportRequest:()=>({}),windowContains:()=>true,
    selectionStats:null,applySelectionClasses(){},
    loadViewport:async()=>{},toast:message=>warnings.push(message),
    api:async url=>{
      const params=new URL(url,'http://qa').searchParams,r=Number(params.get('row'));
      for(const [index,source] of rows)if(source===r)return {found:true,row:r,visualIndex:index};
      return {found:false,row:r};
    },
    selectCell(cell,r,c,ref,v){
      state.selected={...cell,row:r,col:c,ref};
      state.selectedVisualIndex=v;
      selected.push([ref,r,v,cell.raw]);
    },
    extendRange:(anchor,r,c,v)=>ranges.push([anchor.row,anchor.visual,r,c,v])
  };
  vm.runInNewContext(section('function syncFilteredSelection(','// Ctrl+Arrow')+
    ';this.navigate=navigateToCell;this.move=moveSelection;this.rebind=syncFilteredSelection;',ctx);
  return {ctx,state,grid,selected,warnings,ranges};
}

test('horizontal keyboard movement and extension keep the sorted source row and visual index paired',async()=>{
  const h=harness();
  await h.ctx.move(0,1,true);
  assert.deepEqual(h.selected,[['B6',6,2,'B6']]);
  assert.deepEqual(h.ranges,[[6,2,6,2,2]]);
});

test('a filtered arrow waits for an actual source row before selecting a visual position',async()=>{
  const h=harness();
  h.ctx.windowContains=()=>false;
  let reads=0;
  h.ctx.loadViewport=async()=>{reads++;};
  await h.ctx.move(1,0);
  assert.ok(reads>0);
  assert.equal(h.state.selected.ref,'A6');
  assert.equal(h.state.selectedVisualIndex,2);
  assert.deepEqual(h.selected,[]);
});

test('jumping to a frozen cell uses its source row as the selection index',async()=>{
  const h=harness({pinned:1});
  assert.equal(await h.ctx.navigate(h.state.sheet,'B1'),true);
  assert.deepEqual(h.selected,[['B1',1,1,'B1']]);
});

test('a delayed filtered jump cannot replace a newer target or pair an address with another source row',async()=>{
  const h=harness();
  let resume;
  h.ctx.loadViewport=()=>new Promise(resolve=>{resume=resolve;});
  const first=h.ctx.navigate(h.state.sheet,'B6');
  await new Promise(resolve=>setImmediate(resolve));
  assert.equal(typeof resume,'function');
  h.ctx.loadViewport=async()=>{};
  assert.equal(await h.ctx.navigate(h.state.sheet,'C4'),true);
  resume();
  assert.equal(await first,false);
  assert.deepEqual(h.selected,[['C4',4,1,'C4']]);
});

test('viewport tiles with the same filter and view cannot cover a different frozen row prefix',()=>{
  const sandbox={window:{}};
  vm.runInNewContext(fs.readFileSync(__dirname+'/viewport-cache.js','utf8'),sandbox);
  const {viewportBounds:bounds,viewportCoversVisible:visible,viewportCoversRequest:covers,viewportKey:key}=sandbox.window.ExcelLensViewport;
  const request={filtered:true,filterId:'sort',viewId:'view',excludeThroughRow:1,limit:2,
    displayStart:1,displayEnd:2,visibleStart:1,visibleEnd:2,startCol:1,cols:1,visibleColStart:1,visibleColEnd:1};
  const tile=bounds({rows:[{},{}],startCol:1,colCount:1},request);
  const changed={...request,excludeThroughRow:0};
  assert.ok(visible(tile,request)&&covers(tile,request));
  assert.equal(visible(tile,changed),false);
  assert.equal(covers(tile,changed),false);
  assert.notEqual(key(1,request),key(1,changed));
});

test('a sparse tile cannot cover columns inside its bounds that were never fetched',()=>{
  const runtime={window:{}};
  vm.runInNewContext(fs.readFileSync(__dirname+'/viewport-cache.js','utf8'),runtime);
  const v=runtime.window.ExcelLensViewport;
  const request={filtered:false,filterId:'',displayStart:1,displayEnd:8,visibleStart:2,visibleEnd:4,
    startCol:1,cols:3,visibleColStart:1,visibleColEnd:3};
  const tile=v.viewportBounds({rows:[],startCol:1,columns:[1,3]},request);
  assert.equal(v.viewportCoversVisible(tile,request),false);
  assert.equal(v.viewportCoversRequest(tile,request),false);
  assert.equal(v.viewportCoversVisible(tile,{...request,visibleColEnd:1}),true);
  assert.equal(v.viewportHasHeadroom(tile,{...request,visibleColEnd:1},8,3),false);
});

test('changing the frozen prefix preserves the active source cell and rebases its next move',async()=>{
  const h=harness({pinned:2,rows:[[1,12],[2,4],[3,6],[4,3]]});
  h.state.selectedFrozenThrough=14;
  h.state.range={row1:4,row2:6,visual1:1,visual2:2};
  h.ctx.rebind(2);
  assert.equal(h.state.selected.ref,'A6');
  assert.equal(h.state.selectedVisualIndex,3);
  assert.equal(h.state.range,null);
  await h.ctx.move(1,0,true);
  assert.deepEqual(h.selected,[['A3',3,4,'A3']]);
  assert.deepEqual(h.ranges,[[6,3,3,1,4]]);
});

test('setting the active cell updates the selection outline inside the frozen panes',()=>{
  const cell=ref=>{
    const classes=new Set();
    return {classes,dataset:{ref,row:'14',col:String(ref.charCodeAt(0)-64)},
      classList:{toggle(name,on){if(on)classes.add(name);else classes.delete(name);}}};
  };
  const first=cell('A14'),second=cell('B14');
  const S={selected:{ref:'A14',row:14,col:1},lastHighlight:{},renderedRows:new Map()};
  const ctx={S,selectionQuick:null,transposeCenter:null,cellExplanation:null,profileCenter:null,
    paintRangeFrames(){},
    window:{ExcelLensMultiSelection:{areas:()=>[]}},
    crosshairCell:()=>S.selected.crosshair||S.selected,
    document:{getElementsByClassName:()=>[],querySelectorAll:selector=>selector==='.grid-region .pinned-cell'?[first,second]:[]}};
  vm.runInNewContext(section('function applySelectionClasses(','function selectCell(')+
    ';this.paint=applySelectionClasses;',ctx);
  ctx.paint();
  assert.ok(first.classes.has('selected'));
  assert.ok(!second.classes.has('selected'));
  S.selected={ref:'B14',row:14,col:2};
  ctx.paint();
  assert.ok(!first.classes.has('selected'));
  assert.ok(second.classes.has('selected'));
  S.selected.crosshair={row:15,col:1};
  ctx.paint();
  assert.ok(second.classes.has('selected'));
  assert.ok(first.classes.has('active-column'));
  assert.ok(!second.classes.has('active-column'));
  assert.ok(!second.classes.has('crosshair-row'));
});
