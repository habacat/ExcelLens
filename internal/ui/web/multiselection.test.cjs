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
  assert.ok(frames[0].style.cssText.includes('border-width:3px 0 0 3px'));
  assert.ok(frames[0].style.cssText.startsWith('left:20px;top:20px;'));
  assert.ok(frames[3].style.cssText.includes('border-width:0 3px 3px 0'));
  assert.ok(layer.lastElementChild.style.cssText.startsWith('left:158px;top:103px;width:7px;height:7px'));
  const single=S.rectangle(S.cell(3,3),S.cell(3,3));
  paint(layer,[single],view);
  assert.ok(layer.children[0].style.cssText.startsWith('left:85px;top:50px;width:40px;height:20px;'));
  assert.ok(layer.lastElementChild.style.cssText.startsWith('left:118px;top:63px;width:7px;height:7px'));
  paint(layer,[single],{...view,left:0,top:0,frozenWidth:0,frozenHeight:0,colLeft:c=>(c-1)*2,rowBounds:r=>[(r-1)*4,r*4]});
  assert.ok(layer.children[0].style.cssText.includes('width:2px;height:4px;border-width:2px 1px 2px 1px'));
  assert.ok(layer.lastElementChild.style.cssText.startsWith('left:24px;top:28px;width:2px;height:4px'));
  paint(layer,[area],{...view, width:110, height:80});
  assert.ok(layer.children.every(n=>n.className==='range-frame'));
  paint(layer,[],view);assert.equal(layer.children.length,0);
});
const a=S.cell(1,2),b=S.cell(3,4),r=S.rectangle(a,b);
test('source thickness moves the centre of the selection without changing its pen',()=>{
  function element(){
    return {style:{},children:[],attrs:{},appendChild(n){n.parent=this;this.children.push(n);return n;},
      setAttribute(k,v){this.attrs[k]=Number(v);},remove(){this.parent.children.splice(this.parent.children.indexOf(this),1);},
      get firstElementChild(){return this.children[0];},get lastElementChild(){return this.children.at(-1);}};
  }
  const document={createElement:element,createElementNS:element},layer=element();
  const paint=vm.runInNewContext(fs.readFileSync(__dirname+'/range-frame.js','utf8')+';window.ExcelLensRangeFrame.paint',{window:{},document});
  let offset=-2;
  const view={width:250,height:150,top:0,left:0,frozenWidth:0,frozenHeight:0,header:20,rowHead:20,
    colLeft:c=>(c-1)*40,rowBounds:r=>[(r-1)*20,r*20],
    seams:(x1,x2,y1,y2)=>({top:[{from:x1,to:x2,offset:-1}],bottom:[{from:x1,to:x2,offset:-.5}],
      left:[{from:y1,to:y2,offset}],right:[{from:y1,to:y2,offset:0}]})};
  for(offset of [-2,-1,-.5,0]){
    paint(layer,[S.rectangle(S.cell(2,2),S.cell(2,2))],view);
    const [top,bottom,left,right]=layer.firstElementChild.firstElementChild.children.map(n=>n.attrs);
    assert.deepEqual([top.height,bottom.height,left.width,right.width],[3,3,3,3]);
    assert.deepEqual([top.y+1.5,bottom.y+1.5,left.x+1.5,right.x+1.5],[-1,19.5,offset,40]);
  }
});
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
