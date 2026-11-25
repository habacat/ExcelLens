const {test}=require('node:test'),assert=require('node:assert/strict');
const fs=require('node:fs'),vm=require('node:vm');
function modules(){
  const ctx={window:{ExcelLensRichText:{estimate:()=>0}},AbortController};
  for(const file of ['viewport-cache','viewport-scheduler','virtualscroll','merges','grid-borders'])
    vm.runInNewContext(fs.readFileSync(__dirname+'/'+file+'.js','utf8'),ctx);
  return ctx.window;
}
const turn=()=>new Promise(resolve=>setImmediate(resolve));
test('a press on a cached style tile resolves hidden bands, merge anchors and sorted source rows before DOM commit',()=>{
  const {TileCache}=modules().ExcelLensViewport,cache=new TileCache(10000);
  const req={context:'one',displayStart:1,displayEnd:3,startCol:1,cols:3,filtered:false};
  const data={startCol:1,colCount:3,rows:[{number:1,cells:[{col:1,raw:'anchor'}]}]};
  cache.store('plain',data,req);
  // The second row and column are hidden. Their far edge belongs to the third band.
  const edge=i=>[0,0,11,11,25][i],point={x:11,y:11,lastCol:3,lastRow:3,rowTop:edge,colLeft:edge,mergeAt:()=>null};
  const empty=cache.sourceAtPoint(req,point);
  assert.deepEqual([empty.row,empty.col,empty.cell],[3,3,undefined]);
  const merged=cache.sourceAtPoint(req,{...point,mergeAt:()=>({row:1,col:1})});
  assert.deepEqual([merged.row,merged.col,merged.cell.raw,merged.crosshair.row,merged.crosshair.col],[1,1,'anchor',3,3]);
  assert.equal(cache.sourceAtPoint(req,{...point,x:25}),null);
  assert.equal(cache.sourceAtPoint({...req,context:'another revision'},point),null);
  const sorted={...req,filtered:true,filterId:'sort',displayEnd:2};
  cache.store('sorted',{...data,rows:[{number:84,cells:[]},{number:17,cells:[{col:3,raw:'0017'}]}]},sorted);
  const hit=cache.sourceAtPoint(sorted,{...point,y:12,lastRow:2,rowTop:i=>(i-1)*10});
  assert.deepEqual([hit.row,hit.col,hit.visual,hit.cell.raw],[17,3,2,'0017']);
});
test('adjacent row and column tiles join by sorted rank, preserve empty cells and reject gaps or a new revision',()=>{
  const v=modules().ExcelLensViewport,cache=new v.TileCache(100000);
  const base={displayStart:1,displayEnd:3,filtered:true,filterId:'sort',viewId:'visible',context:'rev1',excludeThroughRow:2,startCol:2,cols:2,selectedCols:[2,7]};
  for(const [r1,r2] of [[1,2],[3,3]])for(const c of [2,7]){
    const request={...base,displayStart:r1,displayEnd:r2,startCol:c,cols:1,selectedCols:[c]};
    const rows=[14,9,280].slice(r1-1,r2).map(n=>({number:n,cells:n===9&&c===2?[]:[{col:c,raw:'00'+n,formula:c===7?'B7':'',style:4}]}));
    cache.store(r1+'-'+c,{startCol:c,colCount:1,columns:[c],rows,totalRows:3},request);
  }
  const result=cache.project(base,[1,2,3]);
  assert.deepEqual(Array.from(result.data.rows,r=>[r.number,r.cells.map(c=>c.col).join(',')]),[[14,'2,7'],[9,'7'],[280,'2,7']]);
  assert.equal(result.data.rows[2].cells[0].raw,'00280');assert.equal(result.data.rows[0].cells[1].formula,'B7');
  assert.equal(cache.reader(base)(2,2).cell,undefined);assert.equal(cache.reader(base)(2,4),null);
  assert.equal(cache.project({...base,selectedCols:[2,4,7]},[1,2,3]),null);
  assert.equal(cache.project({...base,context:'rev2'},[1,2,3]),null);
  assert.equal(cache.project({...base,excludeThroughRow:1},[1,2,3]),null);
});
test('reversal promotes an outstanding prediction and distant demand has a reserved slot',async()=>{
  const C=modules().ExcelLensViewportScheduler,reads=[];
  const sched=new C((job,signal)=>new Promise((ok,no)=>{
    reads.push({key:job.key,signal,ok});signal.addEventListener('abort',()=>no(signal.reason),{once:true});
  }));
  const tile=key=>({key});
  await sched.plan([],['ahead','other','queued'].map(tile));await turn();
  assert.deepEqual(reads.map(r=>r.key),['ahead','other']);
  const p=sched.plan(['ahead','jump'].map(tile),[]);await turn();
  assert.equal(reads.length,3);assert(!reads[0].signal.aborted);
  reads[0].ok('retained');reads[2].ok('arrived');assert.deepEqual(Array.from(await p),['retained','arrived']);
  sched.reset();await turn();assert.equal(sched.stats.peak,3);assert.equal(sched.active.size,0);
});
test('queued old-generation work cannot start or strand a new job with the same key after reset',async()=>{
  const C=modules().ExcelLensViewportScheduler,reads=[];
  const s=new C((job,signal)=>new Promise(ok=>reads.push({key:job.key,signal,ok})),1);
  const first=s.plan([{key:'same'},{key:'obsolete'}],[]);await turn();
  s.reset();const next=s.plan([{key:'same'}],[]);reads[0].ok('stale');await first;await turn();
  assert.deepEqual(reads.map(j=>j.key),['same','same']);reads[1].ok('fresh');
  assert.deepEqual(Array.from(await next),['fresh']);
});
test('prediction uses logical scroll distance, reverses immediately and stays capped after slow reads',()=>{
  const V=modules().ExcelLensVirtualScroll,s=new V(1000);s.configure(100000,100);
  const m=new V.Motion();m.sample(0,s.logical(0),0);m.sample(30,s.logical(8),16);
  let p=m.predict(300,200,17);assert(p.y[1]>p.y[0]&&p.x[1]>p.x[0]);
  m.observe('read',4000);m.sample(10,s.logical(1),32);p=m.predict(300,200,33);
  assert(p.y[0]>p.y[1]&&p.x[0]>p.x[1]);assert(p.y[0]<200*4);
  assert.equal(m.predict(300,200,600).y[0],m.predict(300,200,600).y[1]);
});
test('an offscreen merge uses bounded perimeter work regardless of anchor distance',()=>{
  const w=modules(),m=new w.ExcelLensMergedCells();m.load([{row:1,col:1,endRow:900000,endCol:1200}]);
  assert.equal(m.intersect(800000,800020,500,512).length,1);
  let probes=0;const edges=w.ExcelLensGridBorders.runs(1,900000,800020,i=>i,()=>{probes++;return '1px solid red';},'',800000);
  assert.equal(probes,21);assert.equal(edges.length,3);
});
