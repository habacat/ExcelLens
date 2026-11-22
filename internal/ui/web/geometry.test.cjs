const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const create=()=>{const c={window:{}};vm.runInNewContext(fs.readFileSync(__dirname+'/geometry.js','utf8'),c);return new c.window.ExcelLensGeometry(1000000000,16384);};

test('row header follows the largest rendered row number and aligns to device pixels',()=>{
  const Geometry=create().constructor;
  for(const [row,expected]of [[1,26],[18,26],[99,26],[100,30],[1000,36],[9000,36],[90000,42],[1000000,54],[50000000,60],[1000000000,72]]){
    assert.equal(Geometry.rowHeaderWidth(row),expected);
    for(const dpr of [1,1.25,1.5,2]){const width=Geometry.rowHeaderWidth(row,dpr);assert(width>=expected);assert(width<expected+1/dpr+.0001);assert(Math.abs(width*dpr-Math.round(width*dpr))<.00001);}
  }
});

test('billion-row geometry keeps sparse runs, exact tail positions and the injected ceiling',()=>{
  const Geometry=create().constructor,g=new Geometry(1000000000,16384);
  g.rows.load({500000000:40},[{min:1,max:1000000000,size:17.5}]);
  assert.equal(g.rows.source.length,1);assert.equal(g.rows.values.size,1);
  assert.equal(g.rows.total(1000000000),17500000022.5);
  assert.equal(g.rows.at(g.rows.offset(1000000000)),1000000000);
  assert.equal(g.rows.set(1000000001,30),false);
});

test('source row runs preserve fractional heights, local overrides and inverse positions',()=>{
  const g=create(),runs=[{min:1,max:2,size:17.5},{min:4,max:7,size:31.25},{min:9,max:10,size:14.125}],overrides={2:44,5:18};
  g.rows.load(overrides,runs);let top=0;
  for(let i=1;i<=30;i++){
    const size=Number(overrides[i]??runs.find(r=>r.min<=i&&r.max>=i)?.size??25);
    assert.equal(g.rows.measure(i),size);assert.equal(g.rows.offset(i),top);
    for(const y of [top,top+.01,top+size/2,top+size-.01])assert.equal(g.rows.at(y),i,'row '+i+' at '+y);
    top+=size;
  }
  assert.equal(g.rows.total(30),top);g.rows.set(2,0);assert.equal(g.rows.measure(2),17.5);
  g.rows.load({});assert.equal(g.rows.measure(1),25);assert.equal(g.rows.offset(3),50);
});

test('fractional source columns share the same device-pixel edge at every zoom',()=>{
  const axis=create().cols;axis.load({1:47.263,2:85.057,3:151.189,4:160.658,5:56.732,6:103.927,7:103.927,8:94.526,9:94.457});
  for(const ratio of [1,1.25,1.5,2])for(const scale of [.8,1,1.1,1.25,1.5])for(let col=1;col<=12;col++){
    const left=axis.paintOffset(col,scale,ratio),width=axis.paintSize(col,scale,ratio),right=axis.paintOffset(col+1,scale,ratio);
    assert(Math.abs(left+width-right)<1e-8);assert(Math.abs(right*ratio-Math.round(right*ratio))<1e-8);
  }
});

test('fifty million source rows require one size run and sparse user overrides',()=>{
  const g=create();g.rows.load({25000000:40},[{min:1,max:50000000,size:17.5}]);
  assert.equal(g.rows.source.length,1);assert.equal(g.rows.values.size,1);
  assert.equal(g.rows.offset(50000000),49999999*17.5+22.5);
  assert.equal(g.rows.at(g.rows.offset(50000000)),50000000);
  assert.equal(g.rows.at(g.rows.offset(25000000)+39),25000000);
  assert.equal(g.rows.at(g.rows.offset(25000001)),25000001);
  g.rows.setBase(30);assert.equal(g.rows.measure(1),17.5);assert.equal(g.rows.total(50000000),50000000*17.5+22.5);
});

test('shared resize resets source sizes and preserves source widths when removing an override',async()=>{
  const geometry=create();geometry.rows.load({2:55},[{min:2,max:2,size:40}]);geometry.cols.load({1:220});
  const S={indexReady:true,filtered:false,sheet:'data',sheetEpoch:1,hiddenColumns:new Set(),sourceColumnWidths:{1:145},columnWidths:{1:220},rowSizes:{2:55}},requests=[],no=()=>{};let relayouts=0;
  const context={S,geometry,api:async(url,options)=>requests.push(JSON.parse(options.body)),toast:no,relayoutGrid:()=>relayouts++};
  const app=fs.readFileSync(__dirname+'/app.js','utf8'),a=app.indexOf('  async function commitResize('),b=app.indexOf('  async function promptResize(',a);assert(a>0&&b>a);vm.runInNewContext(app.slice(a,b)+';this.resize=commitResize;',context);
  await context.resize('col',1,0);assert.equal(geometry.cols.measure(1),145);assert.equal(S.columnWidths[1],undefined);assert.equal(requests[0].size,0);
  await context.resize('row',2,0);assert.equal(geometry.rows.measure(2),40);assert.equal(S.rowSizes[2],undefined);
  // Each resize lays the grid out again, which also puts the selection frame back.
  assert.equal(relayouts,2);
});

test('recycled preview cells adopt source column positions without collapsing merged widths',()=>{
  const app=fs.readFileSync(__dirname+'/app.js','utf8'),a=app.indexOf('  function updateGridCellGeometry('),b=app.indexOf('  // Only resolved,',a),context={};vm.runInNewContext(app.slice(a,b)+';this.position=updateGridCellGeometry;',context);
  const cell={style:{}};context.position(cell,{left:52,width:132});context.position(cell,{left:99,width:145});assert.equal(cell.style.width,'145px');assert.equal(cell.style.left,'99px');
  cell.style.width='290px';context.position(cell,{left:99,width:145});assert.equal(cell.style.width,'290px');
});
