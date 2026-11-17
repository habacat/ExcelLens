const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const ctx={window:{}};vm.runInNewContext(fs.readFileSync(__dirname+'/composition-model.js','utf8'),ctx);const m=ctx.window.ExcelLensCompositionModel;
const field=(column,name,hash)=>({column,name,headerHash:hash,headerLoaded:true,headerUnique:true});
test('only complete unique original headers suggest alignment; display names and positions cannot substitute',()=>{
  const expected=[field(1,'甲','a'),field(2,'乙','b'),{...field(3,'重复','c'),headerUnique:false},field(4,'截取','d')];expected[3].truncated=true;
  const actual=[field(7,'乙','b'),field(8,'甲','a'),field(9,'重复','c'),field(10,'截取','d')];
  assert.deepEqual([...m.suggest(expected,actual)],[8,7,null,null]);actual.push(field(11,'另一个甲','a'));assert.equal(m.suggest(expected,actual)[0],null);
  assert.equal(m.suggest([field(1,'同名','original')],[field(1,'同名','other')])[0],null);
});
test('explicit missing columns differ from unconfirmed mappings and duplicate source bindings are rejected',()=>{
  const inputs=[{info:{ref:{book:'a',source:'wb:甲',stamp:'a'},fields:[field(1,'编号','id'),field(2,'金额','money')]}},{info:{ref:{book:'b',source:'wb:乙',stamp:'b'},fields:[field(7,'金额','money')]}}];
  const rows=[{name:'编号',columns:[1,null]},{name:'金额',columns:[2,7]}],limits={sources:8,columns:256};
  assert.match(m.validate('联合',rows,inputs,limits),/确认/);rows[0].columns[1]=0;assert.equal(m.validate('联合',rows,inputs,limits),'');
  const definition=m.definition(' 联合 ',rows,inputs);assert.equal(definition.name,'联合');assert.deepEqual([...definition.inputs[1].columns],[0,7]);definition.inputs[0].ref.book='changed';assert.equal(inputs[0].info.ref.book,'a');
  rows[0].columns[1]=7;assert.match(m.validate('联合',rows,inputs,limits),/重复/);rows[0].columns[1]=0;rows[1].name='编号';assert.match(m.validate('联合',rows,inputs,limits),/重复/);
});
test('wide source fields are searched and paged without inventing or renumbering source columns',()=>{
  const fields=Array.from({length:16384},(_,i)=>field(i+1,'字段'+(i+1),'h'+i)),rows=m.initialRows(fields,256);
  assert.equal(rows.length,256);assert.equal(m.label(16384),'XFD');const result=m.search(fields,'XFD',0,40);assert.equal(result.count,1);assert.equal(result.items[0].column,16384);
  assert.equal(m.search(fields,'',1,40).items[0].column,41);
});

test('sample type conflicts distinguish numeric-eligible amounts from native text without asserting full-column types',()=>{
  const row={columns:[1,2]},inputs=[{info:{fields:[{column:1,sampleType:'text'}]}},{info:{fields:[{column:2,sampleType:'number'}]}}];
  assert.equal(m.typeConflict(row,inputs),true);inputs[0].info.fields[0].sampleType='number';assert.equal(m.typeConflict(row,inputs),false);inputs[0].info.fields[0].sampleType='unknown';assert.equal(m.typeConflict(row,inputs),false);assert.equal(m.typeWarning(row,inputs),'类型未确认');inputs[0].info.fields[0].sampleType='mixed';assert.equal(m.typeConflict(row,inputs),true);
});
test('wide exact-header matching stays bounded when every field is reversed',()=>{
  const context={window:{}};vm.runInNewContext(fs.readFileSync(__dirname+'/composition-model.js','utf8'),context);const size=4096;let reads=0;
  const actual=Array.from({length:size},(_,i)=>({column:i+1,headerLoaded:true,headerUnique:true,truncated:false,get headerHash(){if(++reads>size*12)throw Error('wide field matching repeated the full candidate scan');return 'hash-'+i;}})),expected=actual.map((_,i)=>({headerHash:'hash-'+(size-i-1),headerLoaded:true,headerUnique:true,truncated:false}));
  const result=context.window.ExcelLensCompositionModel.suggest(expected,actual);assert.equal(result[0],size);assert.equal(result.at(-1),1);assert(reads<=size*12);
});
