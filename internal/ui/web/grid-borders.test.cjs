const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const read=name=>fs.readFileSync(__dirname+'/'+name,'utf8');
const element=()=>({
  style:{removeProperty(key){delete this[key.replace(/-([a-z])/g,(_,c)=>c.toUpperCase())];}},
  children:[],setAttribute(){},appendChild(child){child.parentNode=this;this.children.push(child);return child;},
  remove(){this.parentNode.children.splice(this.parentNode.children.indexOf(this),1);},
  replaceChildren(){this.children.length=0;},get lastChild(){return this.children.at(-1);}
});
function setup(){
  const context={window:{},document:{createElement:element}};for(const name of ['conditional.js','grid-borders.js','merges.js'])vm.runInNewContext(read(name),context);
  const merge=new context.window.ExcelLensMergedCells();merge.load([{row:8,col:5,endRow:9,endCol:5},{row:8,col:6,endRow:8,endCol:7},{row:8,col:8,endRow:8,endCol:9},{row:8,col:10,endRow:9,endCol:10},{row:8,col:11,endRow:8,endCol:12}]);
  const black='0.667px solid #000000',style={borderTop:black,borderBottom:black,borderLeft:black,borderRight:black};
  const S={showStyles:true,styles:[{},style],cellMap:new Map(),frozenCellMap:new Map()},colName=c=>String.fromCharCode(64+c);
  const source=read('app.js'),a=source.indexOf('  const visualForBorder='),b=source.indexOf('  function toggleOriginalFormat(',a);
  Object.assign(context,{S,activeMerge:(r,c)=>merge.at(r,c),colName,nextVisibleRow:r=>r,rowTop:r=>(r-1)*25,colLeft:c=>(c-1)*80});vm.runInNewContext(source.slice(a,b)+';this.paint=updateSharedBorders;',context);
  const node=(row,col)=>Object.assign(element(),{dataset:{row:String(row),col:String(col)}});
  const row={number:8,cells:[{col:6,style:1},{col:8,style:1},{col:10,style:1},{col:5,style:1},{col:11,style:1}]};
  return {context,S,node,row,black};
}
test('all preceding cells draw a merged header edge at the same trailing pixel as ordinary cells',()=>{
  const {context,node,row,black}=setup(),anchor=node(8,6);context.paint(anchor,row.cells[0],row,null,row,{style:1},{number:9});
  assert.equal(anchor.style.borderTop,undefined);assert.equal(anchor.style.borderLeft,undefined);
  for(const col of [6,7]){const above=node(7,col);context.paint(above,null,{number:7},null,null,col===6?row.cells[0]:null,row);assert.equal(above.style.borderBottom,black,'covered column '+col+' must borrow the merged border');}
});
test('adjacent merged headers choose their real outer seam and frozen anchors resolve identically',()=>{
  const {context,S,node,row,black}=setup();
  const left=node(8,6);context.paint(left,row.cells[0],row,null,row,null,{number:9});assert.equal(left.style.borderRight,black);
  S.frozenCellMap.set('J8',row.cells[2]);const below=node(9,9);context.paint(below,null,{number:9},null,{number:9},null,null);assert.equal(below.style.borderRight,black);
  const frozen=node(8,10);context.paint(frozen,row.cells[2],row,null,row,null,{number:9});assert.equal(frozen.style.borderLeft,undefined);assert.equal(frozen.style.borderTop,undefined);
});

test('two-row currency and direction headers retain the entire right border next to one-row merges',()=>{
  const {context,S,node,row,black}=setup();for(const [col,neighbor] of [[5,6],[10,11]]){
    const tall=row.cells.find(c=>c.col===col),short=row.cells.find(c=>c.col===neighbor);S.cellMap.set(String.fromCharCode(64+col)+'8',tall);
    const a=node(8,col);context.paint(a,tall,row,short,row,null,{number:9});assert.equal(a.style.borderRight,black);
    const b=node(8,neighbor);context.paint(b,short,row,null,row,null,{number:9});assert.equal(b.style.borderLeft,undefined);
  }
});

test('merged XLS/XLSX perimeter reads borders from styled covered corners',()=>{
  const {context,S,node,row,black}=setup();S.styles.push({borderTop:black,borderLeft:black});S.styles.push({borderBottom:black,borderRight:black});
  S.cellMap.set('E9',{col:5,style:3});const anchor=node(8,5);context.paint(anchor,{col:5,style:2},row,null,row,null,{number:9});assert.equal(anchor.style.borderBottom,black);assert.equal(anchor.style.borderRight,black);
  S.cellMap.clear();S.frozenCellMap.set('G8',{col:7,style:3});const wide=node(8,6);context.paint(wide,{col:6,style:2},row,null,row,null,{number:9});assert.equal(wide.style.borderBottom,black);assert.equal(wide.style.borderRight,black);
});

test('conditional borders use the same seam and merged-perimeter model and clear when absent or disabled',()=>{
  const {context,S,node,row,black}=setup(),red='1.15px solid #FF0033',blue='0.55px solid #1255AA';
  const cell={col:2,style:1,conditional:{visual:{borderRight:red,borderBottom:blue}}},edge=node(2,2),before=JSON.stringify(cell);
  context.paint(edge,cell,{number:2},null,null,null,null);assert.equal(edge.style.borderRight,red);assert.equal(edge.style.borderBottom,blue);assert.equal(JSON.stringify(cell),before);
  context.paint(edge,{...cell,conditional:{visual:{}}},{number:2},null,null,null,null);assert.equal(edge.style.borderRight,undefined);assert.equal(edge.style.borderBottom,undefined);
  S.cellMap.set('E9',{col:5,style:1,conditional:{visual:{borderRight:red,borderBottom:blue}}});S.styles.push({borderTop:black,borderLeft:black});
  const anchor=node(8,5);context.paint(anchor,{col:5,style:2},row,null,null,null,null);assert.equal(anchor.style.borderRight,red);assert.equal(anchor.style.borderBottom,blue);
  S.showStyles=false;context.paint(anchor,{col:5,style:2},row,null,null,null,null);assert.equal(anchor.style.borderRight,undefined);assert.equal(anchor.style.borderBottom,undefined);
});

test('a fill hides the default seam from either side and keeps explicit neighboring borders',()=>{
  const {context,S,node,black}=setup();
  S.styles.push({background:'#DBDBDB'});
  const edge=node(2,2), row={number:2}, below={number:3}, next={nextCol:3};
  context.paint(edge,{style:2},row,null,row,null,below,next);
  assert.equal(edge.style.borderRight,'0px');assert.equal(edge.style.borderBottom,'0px');
  context.paint(edge,{},row,{style:2},row,{style:1},below,next);
  assert.equal(edge.style.borderRight,'0px');assert.equal(edge.style.borderBottom,black);
  context.paint(edge,{},row,null,row,null,below,next);
  assert.equal(edge.style.borderRight,undefined);assert.equal(edge.style.borderBottom,undefined);
});

test('a long shared edge preserves partial neighbor styles and removes segments when its node is reused',()=>{
  const {context,black}=setup(),borders=context.window.ExcelLensGridBorders;
  const red='2px dashed #FF0000';
  const runs=borders.runs(2,6,5,n=>n,n=>n<4?black:n===4?red:'','');
  assert.deepEqual(Array.from(runs,part=>[part.start,part.end,part.style]),[[2,4,black],[4,5,red],[5,7,'']]);
  const cell=element(),offset=c=>c*31;
  borders.paint(cell,'bottom',runs,offset);
  assert.equal(cell.style.borderBottom,'0px');
  assert.deepEqual(cell.children[0].children.map(line=>[line.style.left,line.style.width,line.style.borderBottom]),
    [['0px','62px',black],['62px','31px',red],['93px','62px','var(--grid-default-seam)']]);
  borders.paint(cell,'bottom','',offset,black);
  assert.equal(cell.children[0].className,'grid-default-bottom');
  assert.equal(cell.children[0].children.length,0);
  borders.paint(cell,'bottom',black,offset);
  assert.equal(cell.children.length,0);assert.equal(cell.style.borderBottom,black);
});
