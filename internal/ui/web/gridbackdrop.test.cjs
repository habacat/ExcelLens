const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const source=fs.readFileSync(__dirname+'/app.js','utf8'),start=source.indexOf('  function gridBackdropPanes('),end=source.indexOf('  function syncGridBackdrop(',start);
const sandbox={};vm.runInNewContext(source.slice(start,end)+';this.panes=gridBackdropPanes;',sandbox);
test('one viewport texture covers frozen blank strips while protecting frozen source cells after scrolling',()=>{
  const bounds={width:1200,height:800,rowHead:42,header:27,pinWidth:260,pinHeight:50,right:100,bottom:100,fixedRight:700,fixedBottom:400};
  const panes=sandbox.panes(bounds);assert.equal(panes.length,4);
  const alpha=(x,y)=>{
    const p=panes.find(p=>x>=p.x&&x<p.x+p.width&&y>=p.y&&y<p.y+p.height);if(!p)return 0;
    const a=Math.max(0,Math.min(1,(x-p.x-p.right)/96)),b=Math.max(0,Math.min(1,(y-p.y-p.bottom)/80));return 1-(1-a)*(1-b);
  };
  assert.equal(alpha(200,50),0,'frozen source corner must stay clear');
  assert.equal(alpha(200,250),1,'bottom blank strip in frozen columns must be decorated');
  assert.equal(alpha(900,50),1,'right blank strip in frozen rows must be decorated');
  assert.equal(alpha(900,400),1,'scrolling blank area must join the frozen strips');
  assert.equal(alpha(20,400),0);assert.equal(alpha(900,10),0);
  assert.equal(panes.reduce((n,p)=>n+p.width*p.height,0),(bounds.width-bounds.rowHead)*(bounds.height-bounds.header));
});
test('freezing beyond the used extent and billion-row scroll positions keep all masks viewport bounded',()=>{
  const panes=sandbox.panes({width:1900,height:900,rowHead:72,header:33.75,pinWidth:700,pinHeight:400,right:-25000000000,bottom:-25000000000,fixedRight:400,fixedBottom:200});
  const corner=panes.find(p=>p.x===72&&p.y===33.75);assert.equal(corner.right,328);assert.equal(corner.bottom,166.25);
  for(const p of panes){assert(p.width<=1900&&p.height<=900);assert(p.right>=-96);assert(p.bottom>=-80);}
});

test('disabled texture and sheets still indexing skip all geometry and mask work',()=>{
  const backdrop={hidden:false},state={indexReady:true},mode={textureEnabled:false};let lookups=0;
  const context={S:state,readingMode:mode,$:id=>{assert.equal(id,'gridBackdrop','hidden decoration must not query grid geometry');lookups++;return backdrop;}};
  const finish=source.indexOf('  function decorateTableColumnHeader(',end);
  vm.runInNewContext(source.slice(end,finish)+';this.sync=syncGridBackdrop;',context);
  context.sync();assert.equal(backdrop.hidden,true);assert.equal(lookups,1);
  mode.textureEnabled=true;state.indexReady=false;backdrop.hidden=false;context.sync();assert.equal(backdrop.hidden,true);assert.equal(lookups,2);
});
