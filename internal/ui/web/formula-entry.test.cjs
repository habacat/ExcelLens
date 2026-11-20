const {test}=require('node:test');
const assert=require('node:assert/strict'), fs=require('node:fs'), vm=require('node:vm');
const ctx={window:{}};
for (const file of ['cell-address.js','formula_inspector.js','formula-entry-model.js','edit-reference-view.js'])
  vm.runInNewContext(fs.readFileSync(__dirname+'/'+file,'utf8'),ctx);
const model=ctx.window.ExcelLensFormulaInput;

test('the active function argument ignores nested commas, quoted names and table fields',()=>{
  for (const [s,fn,arg] of [
    ['=IF(SUM(A1,B2),','IF',1], ['=IF(A1,"a,""b",','IF',2],
    ["=SUM('O''Brien, ('!B1,", 'SUM',1], ['=SUM(T[[a,b]],','SUM',1],
    ['=SUM({1,2;3,4},','SUM',1], ['=IF((A1+B1),SU','IF',1]
  ]) { const c=model.context(s,s.length);assert.equal(c.fn,fn,s);assert.equal(c.arg,arg,s); }
  for (const s of ['="a,SU',"='O''Brien", '=Table[[a,SU']) assert.equal(model.context(s,s.length),null,s);
  assert.equal(model.close('=SUM(Table[Turnover (2026)]'),'=SUM(Table[Turnover (2026)])');
  assert.equal(model.close('=)('),'=)(');
});

test('pointed ranges retain their sheet and lock mode, and F4 can target a single endpoint',()=>{
  const a={kind:'cell',row:4,col:3}, b={kind:'cell',row:2,col:2};
  const ref=model.reference(a,b,"O'Brien 明细",'原表',"'前表'!$D4");
  assert.equal(ref,"'O''Brien 明细'!$B2:$C4");
  const s='=SUM('+ref+')',from=s.indexOf("'"),end=s.indexOf(')');
  const all=model.absolute(s,from,end,'原表');assert.equal(all.value,'B2:C4');
  const last=model.absolute(s,end,end,'原表');assert.equal(last.value,'C4');
  assert.equal(model.reference({kind:'column',col:4},{kind:'column',col:1},'手续费','原表'),'手续费!A:D');
  assert.equal(model.absolute('="B3"',4,4,'原表'),null);
});

test('date-sorted or filtered worksheet references paint only the visible source members',()=>{
  const ref={row1:5,row2:8,col1:2,col2:3,color:'blue'};
  const areas=ctx.window.ExcelLensEditReferenceView.visibleAreas([ref],true,1,new Map([[1,8],[2,6],[3,2],[4,7]]),()=>null);
  assert.equal(JSON.stringify(areas.map(a=>[a.visual1,a.visual2])),JSON.stringify([[1,2],[4,4]]));
});

test('a delayed reference lookup cannot overwrite newer typing; repeated arrows follow in order',async()=>{
  vm.runInNewContext(fs.readFileSync(__dirname+'/edit-formula-entry.js','utf8'),ctx);
  let release, calls=0;
  const entry=Object.create(ctx.window.ExcelLensWorksheetFormulaEntry.prototype);
  const written=[],grid={draft:{},selected:{row:1,col:1},owner:{context:()=>({sheet:'A'})},
    view:{referenceStep(base){calls++;const next={kind:'cell',row:base.row+1,col:1};
      if(calls===1)return new Promise(resolve=>{release=()=>resolve(next);});return next;}}};
  Object.assign(entry,{grid,sequence:0,currentPick(){return this.pick;},writePoint(anchor,point){
    this.pick={anchor,point,sheet:'A'};written.push(point.row);
  }});
  entry.step('ArrowDown',false);entry.step('ArrowDown',false);
  release();await entry.pendingStep;assert.deepEqual(written,[2,3]);
  calls=0;entry.step('ArrowDown',false);entry.sequence++;release();await entry.pendingStep;
  assert.deepEqual(written,[2,3]);
});
