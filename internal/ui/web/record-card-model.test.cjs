const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),vm=require('node:vm');
const context={window:{},TextEncoder};for(const file of ['cellview.js','record-card-model.js'])vm.runInNewContext(fs.readFileSync(path.join(__dirname,file),'utf8'),context);
const model=context.window.ExcelLensRecordCardModel,colName=col=>{let name='';for(;col;col=Math.floor((col-1)/26))name=String.fromCharCode(65+(col-1)%26)+name;return name;};
const source={maxRows:1000000,maxColumns:16384},limits={columns:32,bytes:8<<20};
const request={workbookKey:'content',book:'path',sheet:'明细',viewId:'view',row:7,headerRow:1,firstColumn:33,columns:32,pinnedThroughRow:2};
function page(){return {...request,columns:3,position:3,totalRecords:5,previousRow:9,nextRow:2,totalColumns:35,rowStyle:0,headerStyle:0,rowIndexed:true,notices:[],fields:[33,34,35].map(column=>({column,header:{ref:colName(column)+'1',col:column,raw:'同名',display:'同名',hasFormula:false,style:0,type:'s'},cell:{ref:colName(column)+'7',col:column,raw:'9007199254740993.31',display:'9,007,199,254,740,993.31',hasFormula:false,style:0}}))};}
test('record model accepts the real short last page and optional CellData properties without converting source strings',()=>{
  const p=page();delete p.filterId;assert.equal(model.validate(p,request,limits,source,colName,35),p);
  assert.equal(model.value(p.fields[0].cell,'raw'),'9007199254740993.31');assert.equal(model.value(p.fields[0].cell,'display'),'9,007,199,254,740,993.31');
  const text={raw:'0000123',display:'0000123',type:'s'};assert.equal(model.value(text,'raw'),'0000123');assert.equal(model.type(text),'文本');
  const conditional={raw:'12.34',display:'12.34',conditional:{display:'12.3元'}};context.window.ExcelLensCellView.value=(cell,formulas,styles)=>styles?cell.conditional?.display??cell.display:cell.display;assert.equal(model.value(conditional,'display'),'12.3元');assert.equal(model.value(conditional,'raw'),'12.34');
});
test('foreign sources, wrong columns, forged coordinates and inconsistent record neighbors are rejected',()=>{
  for(const mutate of [p=>p.book='foreign',p=>p.workbookKey='foreign',p=>p.sheet='foreign',p=>p.viewId='foreign',p=>p.filterId='foreign',p=>p.pinnedThroughRow=0,p=>p.headerRow=0,p=>p.columns=32,p=>p.totalColumns=36,p=>p.fields.reverse(),p=>p.fields[0].cell.ref='AG8',p=>p.fields[0].cell.col=1,p=>p.fields[0].header.ref='AG2',p=>p.fields[0].cell.raw=9007199254740992,p=>p.fields[0].cell.hasFormula=undefined,p=>p.fields[0].cell.cacheMissing='true',p=>p.previousRow=7,p=>p.previousRow=0,p=>p.nextRow=9,p=>p.position=0,p=>p.totalRecords=source.maxRows+1]){
    const p=page();mutate(p);assert.throws(()=>model.validate(p,request,limits,source,colName,35));
  }
});
test('header row zero preserves column identity and refuses a silently substituted header',()=>{
  const req={...request,headerRow:0},p=page();p.headerRow=0;for(const field of p.fields)delete field.header;
  assert.equal(model.validate(p,req,limits,source,colName,35),p);p.fields[0].header={};assert.throws(()=>model.validate(p,req,limits,source,colName,35));
});
test('missing formula caches remain unavailable; unrecoverable formulas never become fabricated expressions',()=>{
  const missing={raw:'',display:'#N/A',hasFormula:true,cacheMissing:true};assert.equal(model.value(missing,'raw'),null);assert.equal(model.value(missing,'display'),null);assert.match(model.type(missing),/无保存结果/);
  const broken={raw:'17',display:'17',hasFormula:true,sharedFormula:true,formula:'[共享公式组 si=9：未找到主公式]'};assert.equal(model.value(broken,'raw'),'17');assert.equal(model.formula(broken),null);assert.equal(model.formula({...broken,formula:'=1+1'}),'=1+1');assert.equal(model.formula({...broken,formula:''}),null);
});
test('all Unicode segments reassemble exact source text and never split a surrogate pair',()=>{
  for(const units of [2,3,7,4096])for(const text of ['', '中😀文'.repeat(2500), 'x'.repeat(units-1)+'😀'+'尾'.repeat(units+3), '\r\n\t<&>"\u2028'.repeat(1800)]){
    const positions=model.offsets(text,units),parts=[];for(let i=0;i<positions.length-1;i++){const part=text.slice(positions[i],positions[i+1]);assert(part.length<=units);assert(!/^[\uDC00-\uDFFF]|[\uD800-\uDBFF]$/.test(part));parts.push(part);}
    assert.equal(parts.join(''),text);assert.equal(model.preview(text,units),parts[0]);
  }
});
test('page byte limits use the serialized strings and reject excess as a whole',()=>{
  const p=page(),size=new TextEncoder().encode(JSON.stringify(p)).length;assert.equal(model.validate(p,request,{...limits,bytes:size},source,colName,35),p);assert.throws(()=>model.validate(p,request,{...limits,bytes:size-1},source,colName,35),/超过/);
});
test('unknown source types remain literal labels even when their names match JavaScript prototype properties',()=>{
  for(const type of ['constructor','__proto__','toString','unknown'])assert.equal(model.type({type,raw:'原文'}),'源类型 '+type);
});
