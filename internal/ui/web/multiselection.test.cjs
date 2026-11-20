'use strict';
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const context={window:{}};vm.createContext(context);
vm.runInContext(fs.readFileSync(__dirname+'/multiselection.js','utf8'),context);
const S=context.window.ExcelLensMultiSelection;
const {test}=require('node:test');
test('a sorted range crossing frozen rows keeps pinned and body coordinates distinct',()=>{
  const range=S.rectangle(S.cell(2,2,2), S.cell(98,4,3),true);
  assert.equal(S.contains(range,1,3,1,true,2),false);
  for(const [row,visual] of [[2,2],[83,1],[98,3]])assert.equal(S.contains(range,row,3,visual,true,2),true);
  assert.equal(S.contains(range,77,3,4,true,2),false);
});
test('frozen and scrolled panes draw only the actual edges and one endpoint',()=>{
  const layer={children:[],appendChild(node){this.children.push(node);return node;},get lastElementChild(){return this.children.at(-1);}};
  const document={createElement:()=>({style:{},remove(){layer.children.splice(layer.children.indexOf(this),1);}})};
  const paint=vm.runInNewContext(fs.readFileSync(__dirname+'/range-frame.js','utf8')+';window.ExcelLensRangeFrame.paint',{window:{},document});
  const view={width:220,height:120,left:15,top:10,rowHead:20,header:20,frozenWidth:40,frozenHeight:20,
    colLeft:c=>(c-1)*40,rowBounds:r=>[(r-1)*20,r*20]};
  const area=S.rectangle(S.cell(1,1),S.cell(5,4));
  paint(layer,[area],view);
  const frames=layer.children.filter(n=>n.className==='range-frame');
  assert.equal(frames.length,4);assert.equal(layer.children.length,5);
  assert.ok(frames[0].style.cssText.includes('border-width:var(--range-stroke) 0 0 var(--range-stroke)'));
  assert.ok(frames[3].style.cssText.includes('border-width:0 var(--range-stroke) var(--range-stroke) 0'));
  assert.ok(layer.lastElementChild.style.cssText.startsWith('left:165px;top:110px;'));
  paint(layer,[area],{...view, width:110, height:80});
  assert.ok(layer.children.every(n=>n.className==='range-frame'));
  paint(layer,[],view);assert.equal(layer.children.length,0);
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
