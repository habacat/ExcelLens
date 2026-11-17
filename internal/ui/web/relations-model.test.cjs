const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
function fixture(){
  const context={window:{},TextEncoder};vm.runInNewContext(fs.readFileSync(__dirname+'/relations-model.js','utf8'),context);const m=context.window.ExcelLensRelationsModel;
  const limits={keys:4,columns:64,previewRows:100,defaultRows:1000000,maximumRows:1000000000,requestBytes:16384,resultBytes:8388608,viewRows:20,viewColumns:12,initialFields:8,snippetUnits:256},formats={maxRows:1000000000,maxColumns:16384};
  const fields=[{column:1,name:'编号',headerHash:'id',headerLoaded:true,headerUnique:true,truncated:false},{column:2,name:'金额',headerHash:'amount',headerLoaded:true,headerUnique:true,truncated:false}];
  const infos=['left','right'].map(side=>({ref:{book:side+'-book',source:'wb:数据',headerRow:1,fingerprint:side+'-fp',stamp:side+'-stamp'},name:side+'.csv',sheet:'数据',kind:'workbook',format:'csv',rows:2,fields:structuredClone(fields)}));
  const query={keys:[{left:1,right:1,mode:'',trim:false,ignoreCase:false}],leftColumns:[1,2],rightColumns:[1,2]};
  const record=(position,number,id)=>({position,number,values:{1:{raw:id,type:'s',exists:true},2:{raw:'9007199254740993.31',display:'9,007,199,254,740,993.31',type:'n',exists:true}}});
  const data={id:'job',mode:'prepare',outputMode:'left',parentId:'job',workbookKey:'main-fp',book:'main-book',query:{keys:[{left:1,right:1}],leftColumns:[1,2],rightColumns:[1,2]},counts:{leftRecords:'2',rightRecords:'2',leftInvalid:'0',rightInvalid:'0',leftDuplicateGroups:'0',rightDuplicateGroups:'0',manyToManyGroups:'0',matchingGroups:'1',leftMatched:'1',rightMatched:'1',leftOnly:'1',rightOnly:'1',innerRows:'1',leftJoinRows:'2'},preview:{total:'2',truncated:false,rows:[{sequence:'1',left:record(1,2,'001'),right:record(1,2,'001'),status:'matched',rightMatches:'1'},{sequence:'2',left:record(2,4,'left-only'),status:'unmatched',rightMatches:'0'}]}};
  for(let i=0;i<2;i++){const info=infos[i];data[i?'right':'left']={id:info.ref.book+'/'+info.ref.source,name:info.name,sheet:info.sheet,kind:info.kind,format:info.format,fingerprint:info.ref.fingerprint,stamp:info.ref.stamp,headerRow:1,fields:structuredClone(fields),total:2};}
  const expected={id:'job',identity:{bookKey:'main-fp',book:'main-book'},mode:'left',operation:'prepare',query,infos};
  return {m,limits,formats,infos,query,data,expected,record};
}
test('Go omitted default key flags retain exact source values/types/coordinates and detached ownership',()=>{
  const h=fixture(),accepted=h.m.validate(h.data,h.expected,h.limits,h.formats);
  assert.equal(accepted.preview.rows[0].left.values[1].raw,'001');assert.equal(accepted.preview.rows[0].left.values[2].raw,'9007199254740993.31');assert.equal(accepted.preview.rows[1].left.number,4);
  h.data.preview.rows[0].left.values[2].raw='mutated';assert.equal(accepted.preview.rows[0].left.values[2].raw,'9007199254740993.31');
});
test('potential quintillion counts are formatted exactly without Number conversion or product expansion',()=>{
  const h=fixture();for(const info of h.infos){info.rows=1000000000;info.ref.headerRow=0;}for(const side of ['left','right']){h.data[side].total=1000000000;h.data[side].headerRow=0;}
  Object.assign(h.data.counts,{leftRecords:'1000000000',rightRecords:'1000000000',leftMatched:'1000000000',rightMatched:'1000000000',leftOnly:'0',rightOnly:'0',leftDuplicateGroups:'1',rightDuplicateGroups:'1',manyToManyGroups:'1',innerRows:'1000000000000000000',leftJoinRows:'1000000000000000000'});
  h.data.preview.rows.length=1;h.data.preview.rows[0].rightMatches='1000000000';h.data.preview.total='1000000000000000000';h.data.preview.truncated=true;
  assert.equal(h.m.validate(h.data,h.expected,h.limits,h.formats).counts.innerRows,'1000000000000000000');assert.equal(h.m.formatCount(h.data.preview.total),'1,000,000,000,000,000,000');
});
for(const mode of ['inner','left_matched','left_unmatched','right_unmatched','left_invalid','right_invalid'])test(mode+' draws only the corresponding source columns and validates its own row shape',()=>{
  const h=fixture();h.data.mode=h.expected.operation='preview';h.data.outputMode=h.expected.mode=mode;h.data.parentId=h.expected.parentId='parent';
  if(mode==='inner')h.data.preview.rows=[h.data.preview.rows[0]];
  if(mode==='left_matched'){h.data.preview.rows=[h.data.preview.rows[0]];delete h.data.preview.rows[0].right;}
  if(mode==='left_unmatched')h.data.preview.rows=[h.data.preview.rows[1]];
  if(mode==='right_unmatched')h.data.preview.rows=[{sequence:'1',right:h.record(2,3,'right-only'),status:'unmatched',rightMatches:'0'}];
  if(mode.endsWith('_invalid'))h.data.preview.rows=[];
  h.data.preview.rows.forEach((row,i)=>row.sequence=String(i+1));h.data.preview.total=h.m.total(h.data.counts,mode);
  const result=h.m.validate(h.data,h.expected,h.limits,h.formats),sides=[...new Set(h.m.columns(result).map(field=>field.side))];assert.deepEqual(sides,[...h.m.mode(mode).sides]);
});
for(const [name,change]of [
  ['number-rounded counter',h=>h.data.counts.innerRows=1],['noncanonical count',h=>h.data.counts.leftOnly='01'],['contradictory totals',h=>h.data.counts.leftMatched='2'],['wrong path identity',h=>h.data.left.id='foreign/wb:数据'],['changed stamp',h=>h.data.right.stamp='other'],['wrong format',h=>h.data.right.format='xls'],['changed header',h=>h.data.left.headerRow=2],['wrong query mode',h=>h.data.query.keys[0].mode='number'],['wrong job parent',h=>h.data.parentId='other'],['source precision coerced to number',h=>h.data.preview.rows[0].left.values[2].raw=9007199254740993.31],['false unmatched count',h=>h.data.preview.rows[1].rightMatches='1'],['false matched without right side',h=>delete h.data.preview.rows[0].right],['reordered positions',h=>h.data.preview.rows[1].left.position=0],['missing source existence',h=>delete h.data.preview.rows[0].left.values[1].exists],['foreign column data',h=>h.data.preview.rows[0].left.values[99]={raw:'x',exists:true}],['partial preview disguised as complete',h=>h.data.preview.truncated=true]
])test('rejects '+name+' before replacing an accepted result',()=>{const h=fixture();change(h);assert.throws(()=>h.m.validate(h.data,h.expected,h.limits,h.formats));});
test('field capture binds path/content/header/full-source signature and tolerates optional empty hashes',()=>{
  const h=fixture(),info=structuredClone(h.infos[0]);delete info.fields[0].headerHash;const data={id:'fields',mode:'fields',workbookKey:h.expected.identity.bookKey,book:h.expected.identity.book,info};
  assert.equal(h.m.validateFields(data,{book:info.ref.book,source:info.ref.source,headerRow:1},h.expected.identity,h.formats).fields.length,2);info.ref.scope={mode:'visible'};assert.throws(()=>h.m.validateFields(data,{book:info.ref.book,source:info.ref.source,headerRow:1},h.expected.identity,h.formats));
});
test('result byte budget and malformed key objects fail closed',()=>{const h=fixture();h.data.preview.rows[0].left.values[2].raw='x'.repeat(2000);assert.throws(()=>h.m.validate(h.data,h.expected,{...h.limits,resultBytes:1024},h.formats));h.query.keys=[null];assert.throws(()=>h.m.validateQuery(h.query,h.infos,h.limits),/关联键/);});
module.exports={fixture};
