const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs'),vm=require('node:vm');
const context={window:{}};vm.runInNewContext(fs.readFileSync(__dirname+'/merges.js','utf8'),context);
const create=()=>new context.window.ExcelLensMergedCells();
test('copying one covered cell or dragging backwards includes every intersected Excel merge',()=>{
  const m=create();
  m.load([{row:2,col:2,endRow:3,endCol:4},{row:4,col:1,endRow:5,endCol:2},{row:1,col:6,endRow:7,endCol:6}]);
  const bounds=(r1,c1,r2,c2,visibility)=>JSON.parse(JSON.stringify(m.expand({row1:r1,col1:c1,row2:r2,col2:c2},visibility)));
  assert.deepEqual(bounds(3,3,3,3),{row1:2,col1:2,row2:3,col2:4});
  assert.deepEqual(bounds(5,5,3,3),{row1:5,col1:5,row2:2,col2:1});
  const hidden={visible:r=>r!==3&&r!==4,previous:r=>r===3||r===4?2:r};
  assert.deepEqual(bounds(2,2,2,2,hidden),{row1:2,col1:2,row2:2,col2:4});
  assert.deepEqual(bounds(5,2,5,2,hidden),{row1:5,col1:2,row2:5,col2:2});
});
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

test('merged crosshair hit and paint use the same visible row and column edges',()=>{
  const m=create(),range={row:2,col:2,endRow:4,endCol:5};
  // C is hidden. B, D and E have different widths and row 3 is taller.
  const left=c=>[0,0,50,75,75,148.5,180][c],top=r=>[0,0,17,39.5,85,110][r];
  const props=new Map(),classes=new Set();
  const node={_xlMerge:range,getBoundingClientRect:()=>({left:9.5,top:21}),
    classList:{toggle:(name,on)=>on?classes.add(name):classes.delete(name)},
    style:{setProperty:(k,v)=>props.set(k,v),getPropertyValue:k=>props.get(k)}};
  const click=(x,y,detail=1)=>m.pointerCell({type:'click',detail,target:{closest:()=>node},clientX:x,clientY:y},left,top);
  assert.deepEqual({...click(34.49,43.49)},{row:2,col:2});
  assert.deepEqual({...click(34.5,43.5)},{row:3,col:4});
  assert.deepEqual({...click(138,110)},{row:4,col:5});
  assert.equal(click(0,0,0),null);
  for(const [point,position,size] of [
    [{row:1,col:2},'0px 0, 0 0px','25px 100%, 100% 0px'],
    [{row:6,col:4},'25px 0, 0 0px','73.5px 100%, 100% 0px'],
    [{row:4,col:1},'0px 0, 0 68px','0px 100%, 100% 25px'],
    [click(60,76),'25px 0, 0 22.5px','73.5px 100%, 100% 45.5px']
  ]){
    m.paintCrosshair(node,point,left,top);
    assert.ok(classes.has('merge-crosshair'));
    assert.equal(props.get('--merge-band-position'),position);
    assert.equal(props.get('--merge-band-size'),size);
  }
  m.paintCrosshair(node,{row:7,col:6},left,top);
  assert.ok(!classes.has('merge-crosshair'));
  m.paintCrosshair(node,{row:2,col:2},left,top);
  m.decorate(node,null,2,2,left,top,()=>25);
  assert.ok(!classes.has('merge-crosshair'));
  assert.equal(click(35,45),null);
});

test('merged crosshair supports contiguous and multi-range band spans and clears custom gradient layers',()=>{
  const m=create(),range={row:2,col:2,endRow:4,endCol:5};
  const left=c=>[0,0,50,75,75,148.5,180][c],top=r=>[0,0,17,39.5,85,110][r];
  const props=new Map(),classes=new Set();
  const node={_xlMerge:range,
    classList:{toggle:(name,on)=>on?classes.add(name):classes.delete(name)},
    style:{setProperty:(k,v)=>props.set(k,v),getPropertyValue:k=>props.get(k)||'',removeProperty:k=>props.delete(k)}};
  m.paintCrosshair(node,{rowSpans:[[5,8]],colSpans:[[4,6]]},left,top);
  assert.ok(classes.has('merge-crosshair'));
  assert.equal(props.has('--merge-band-image'),false);
  assert.equal(props.get('--merge-band-position'),'25px 0, 0 0px');
  assert.equal(props.get('--merge-band-size'),'105px 100%, 100% 0px');

  m.paintCrosshair(node,{rowSpans:[[6,7]],colSpans:[[2,2],[5,5]]},left,top);
  assert.ok(classes.has('merge-crosshair'));
  assert.equal(props.get('--merge-band-image'),'linear-gradient(var(--crosshair-column-fill), var(--crosshair-column-fill)), linear-gradient(var(--crosshair-column-fill), var(--crosshair-column-fill))');
  assert.equal(props.get('--merge-band-position'),'0px 0, 98.5px 0');
  assert.equal(props.get('--merge-band-size'),'25px 100%, 31.5px 100%');

  m.paintCrosshair(node,{rowSpans:[[6,7]],colSpans:[[4,4]]},left,top);
  assert.equal(props.has('--merge-band-image'),false);
  assert.equal(props.get('--merge-band-position'),'25px 0, 0 0px');
  assert.equal(props.get('--merge-band-size'),'73.5px 100%, 100% 0px');

  // Unsorted adjacent spans still coalesce into a single band without custom layers.
  m.paintCrosshair(node,{rowSpans:[[6,7]],colSpans:[[4,5],[2,3]]},left,top);
  assert.equal(props.has('--merge-band-image'),false);
  assert.equal(props.get('--merge-band-position'),'0px 0, 0 0px');
  assert.equal(props.get('--merge-band-size'),'130px 100%, 100% 0px');
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
