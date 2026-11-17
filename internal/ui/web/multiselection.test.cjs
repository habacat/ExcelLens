'use strict';
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const context={window:{}};vm.createContext(context);
vm.runInContext(fs.readFileSync(__dirname+'/multiselection.js','utf8'),context);
const S=context.window.ExcelLensMultiSelection;
const {test}=require('node:test');
test('range seams use source or sorted visual positions and include a selected merged anchor',()=>{
  const range=S.rectangle(S.cell(2,2),S.cell(4,4));
  for(const [row,col,expected]of [[2,2,25],[2,3,17],[3,3,16],[4,4,22],[1,3,0]])assert.equal(S.borderMask([range],row,col,row),expected);
  const sorted=S.rectangle(S.cell(100,2,7),S.cell(5,4,9),true);
  assert.equal(S.borderMask([sorted],998,3,8,true),16);assert.equal(S.borderMask([sorted],5,4,9,true),22);
  assert.equal(S.borderMask([range],2,2,2,false,{row:2,col:2,endRow:4,endCol:4}),31);
  assert.equal(S.borderMask([],2,2,2),0);
});
const a=S.cell(1,2),b=S.cell(3,4),r=S.rectangle(a,b);
assert.equal(S.contains(r,2,3,0),true);
assert.equal(S.contains(r,4,3,0),false);
const filtered=S.rectangle(S.cell(100,2,7),S.cell(5,4,9),true);
assert.equal(S.contains(filtered,998,3,8,true),true);
assert.equal(S.contains(filtered,5,3,10,true),false);
const selected={multiRanges:[r],range:filtered,selected:{row:5,col:4},selectedVisualIndex:9};
assert.equal(S.areas(selected).length,2);
assert.equal(S.areas({selected:{row:3,col:1},selectedVisualIndex:3,filtered:false},true).length,1);
assert.deepEqual(JSON.parse(JSON.stringify(S.removeSingleton([r,S.rectangle(a,a)],1,2,1,false))),JSON.parse(JSON.stringify([r])));
assert.equal(S.removeSingleton([r],2,3,2,false),null);
assert.deepEqual(JSON.parse(JSON.stringify(S.points(r,c=>String.fromCharCode(64+c)))),{start:'B1',end:'D3'});
console.log('MultiSelection invariants OK');

test('selection row counts include frozen headers and retain reversed drag endpoint pairs',()=>{
  for(const area of [
    {row1:1,row2:8,visual1:1,visual2:2},
    {row1:8,row2:1,visual1:2,visual2:1}
  ])assert.equal(S.rowCount(area,true,3),5);
  assert.equal(S.rowCount({row1:8,row2:4,visual1:1,visual2:2},true,3),2);
  assert.equal(S.rowCount({row1:3,row2:1,visual1:3,visual2:1},true,3),3);
});
