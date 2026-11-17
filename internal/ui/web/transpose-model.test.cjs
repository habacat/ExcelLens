const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const ctx={window:{},TextEncoder};for(const file of ['conditional.js','cellview.js','transpose-model.js'])vm.runInNewContext(fs.readFileSync(__dirname+'/'+file,'utf8'),ctx);
const m=ctx.window.ExcelLensTransposeModel,col=n=>String.fromCharCode(64+n),limits={rows:250,columns:50,cells:5000,bytes:1<<20,pageColumns:16};
const snapshot={book:'path',workbookKey:'content',sheet:'明细',rows:3,firstColumn:1,lastColumn:2,maxRow:1000000000};
const fixture=()=>({...snapshot,startCol:1,endCol:2,rows:[7,2,10].map((row,i)=>({row,style:3,cells:[{ref:'A'+row,col:1,raw:i?'0000123':'9007199254740993.31',display:i?'0000123':'9,007,199,254,740,993.31',type:i?'s':'n',hasFormula:false,style:3}]}))});
test('transpose preserves sorted source order, exact values, cell identity and sparse blanks',()=>{
  const p=fixture(),before=JSON.stringify(p),data=m.validate(p,snapshot,limits,col),first=m.at(data,1,0,col);
  assert.equal(first.ref,'A7');assert.equal(first.cell,p.rows[0].cells[0]);assert.equal(m.value(first,'raw',true),'9007199254740993.31');assert.equal(m.value(first,'display',true),'9,007,199,254,740,993.31');
  assert.equal(m.at(data,1,1,col).ref,'A2');assert.equal(m.value(m.at(data,1,1,col),'raw',true),'0000123');assert.equal(m.at(data,2,1,col).cell,null);assert.equal(m.at(data,2,1,col).style,3);assert.equal(JSON.stringify(p),before);
});
test('foreign bindings, forged coordinates, duplicate rows/cells, rounded values and response excess are rejected',()=>{
  for(const mutate of [p=>p.book='copy',p=>p.workbookKey='new',p=>p.sheet='other',p=>p.startCol=2,p=>p.rows.pop(),p=>p.rows[1].row=7,p=>p.rows[0].cells[0].ref='A2',p=>p.rows[0].cells.push(p.rows[0].cells[0]),p=>p.rows[0].cells[0].raw=9007199254740993,p=>p.rows[0].cells[0].hasFormula='false',p=>p.rows[0].cells[0].cacheMissing='true',p=>p.rows[0].style=-1]){const p=fixture();mutate(p);assert.throws(()=>m.validate(p,snapshot,limits,col));}
  assert.throws(()=>m.validate(fixture(),snapshot,{...limits,bytes:5},col));assert.throws(()=>m.validate(fixture(),snapshot,{...limits,cells:5},col));
});
test('missing formula caches stay unknown and unsupported formula metadata never becomes an expression',()=>{
  const item={cell:{raw:'',display:'',hasFormula:true,cacheMissing:true,formula:'=1+1'}};
  assert.equal(m.value(item,'display',true),null);assert.equal(m.value(item,'raw',true),null);assert.equal(m.value(item,'formula',true),'=1+1');
  item.cell={raw:'17',display:'17',hasFormula:true,formula:'[共享公式组缺失]'};assert.equal(m.value(item,'formula',true),null);assert.equal(m.value(item,'raw',true),'17');
});
test('conditional display is a view choice and never replaces original text or source types',()=>{
  const item={cell:{raw:'12.340',display:'12.34',type:'s',hasFormula:false,conditional:{display:'12.3元'}}};
  assert.equal(m.value(item,'display',true),'12.3元');assert.equal(m.value(item,'display',false),'12.34');assert.equal(m.value(item,'raw',true),'12.340');assert.equal(item.cell.type,'s');
});
test('page size follows the actual container and remains bounded at zero and very large widths',()=>{
  for(const [width,want] of [[0,1],[396,2],[1000,6],[100000,16]])assert.equal(m.pageSize(width,16),want);
});

test('an actual unindexed blank row encoded with null cells stays a sparse source coordinate',()=>{
  const p=fixture();p.rows[0].cells=null;const data=m.validate(p,snapshot,limits,col),blank=m.at(data,2,0,col);
  assert.equal(blank.ref,'B7');assert.equal(blank.cell,null);assert.equal(m.value(blank,'raw',true),'');assert.equal(p.rows[0].cells,null);
});
