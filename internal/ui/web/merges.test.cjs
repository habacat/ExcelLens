const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs'),vm=require('node:vm');
const context={window:{}};vm.runInNewContext(fs.readFileSync(__dirname+'/merges.js','utf8'),context);
const create=()=>new context.window.ExcelLensMergedCells();
test('HTML title/header merged anchors and covered positions keep their source coordinates',()=>{
  const m=create();m.load([{row:2,col:1,endRow:2,endCol:13},{row:8,col:2,endRow:9,endCol:2}]);
  assert.equal(m.at(2,13).col,1);assert.equal(m.at(9,2).row,8);assert.equal(m.at(7,2),null);assert.equal(m.at(2,14),null);
  const node={style:{},classList:{toggle(){}}};
  m.decorate(node,m.at(2,1),2,1,c=>(c-1)*132,r=>(r-1)*25,()=>132);
  assert.equal(node.style.width,'1716px');assert.equal(node.style.height,'25px');assert.equal(node.style.zIndex,'2');
  m.decorate(node,m.at(9,2),9,2,c=>(c-1)*132,r=>(r-1)*25,()=>132);assert.equal(node.style.display,'none');
  m.load([]);m.decorate(node,null,9,2,c=>(c-1)*132,r=>(r-1)*25,()=>132);assert.equal(node.style.display,'');assert.equal(node.style.width,'132px');
});
test('merged lookup agrees with a source-range oracle and does not touch ordinary cell styles',()=>{
  const m=create(),ranges=[];
  for(let i=0;i<1000;i++)ranges.push({row:i*20+1,col:2,endRow:i*20+3,endCol:5});
  m.load(ranges);
  for(let row=1;row<20000;row+=7)for(let col=1;col<8;col++){
    const want=ranges.find(r=>row>=r.row&&row<=r.endRow&&col>=r.col&&col<=r.endCol);
    assert.equal(m.at(row,col)?.row,want?.row);
  }
  const node={style:new Proxy({},{set(){throw Error('ordinary cell incurred a style write')}})};
  m.decorate(node,null,1,1,()=>0,()=>0,()=>132);
});

test('actual grid helpers disable source merges during filtering or across a frozen boundary',()=>{
  const source=fs.readFileSync(__dirname+'/app.js','utf8');
  const start=source.indexOf('  function activeMerge('),end=source.indexOf('  // Updating the text node',start);
  assert(start>0&&end>start);
  const m=create();m.load([{row:2,col:1,endRow:2,endCol:13}]);
  const S={indexReady:true,filtered:false,freeze:{rows:0,cols:0}};
  const sandbox={S,mergedCells:m,colLeft:c=>(c-1)*132,rowTop:r=>(r-1)*25,colWidth:()=>132};
  vm.runInNewContext(source.slice(start,end)+';this.active=activeMerge;',sandbox);
  assert.equal(sandbox.active(2,1).endCol,13);
  S.filtered=true;assert.equal(sandbox.active(2,1),null);
  S.filtered=false;S.freeze.cols=1;assert.equal(sandbox.active(2,1),null);
  S.freeze.cols=0;S.freeze.rows=2;assert.equal(sandbox.active(2,1).endCol,13);
  S.rowVisibility={visible:row=>row!==2};assert.equal(sandbox.active(2,1),null);
  S.rowVisibility=null;assert.equal(sandbox.active(2,1).endCol,13);
  S.indexReady=false;assert.equal(sandbox.active(2,1),null);
});
