const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
function setup(){
  const nodes=new Map(),node=id=>{if(!nodes.has(id))nodes.set(id,{hidden:true,clientWidth:900,textContent:'',classList:{toggle(){}},replaceChildren(){},close(){this.open=false;},querySelectorAll:()=>[]});return nodes.get(id);};
  const ctx={window:{},document:{getElementById:node},AbortController,TextEncoder};for(const file of ['cellview.js','transpose-model.js','transpose.js'])vm.runInNewContext(fs.readFileSync(__dirname+'/'+file,'utf8'),ctx);
  let current=true;const calls=[],copied=[],accepted=[],snapshot={book:'book',workbookKey:'hash',sheet:'数据',rows:1,firstColumn:1,lastColumn:1,maxRow:10,styled:false,current:()=>current,sourceCurrent:()=>current,read:async(options)=>{calls.push(options);return {book:'book',workbookKey:'hash',sheet:'数据',startCol:1,endCol:1,rows:[{row:2,cells:[{ref:'A2',col:1,raw:'001',display:'001',style:0,type:'s',hasFormula:false}]}]};}};
  const ui=Object.assign(Object.create(ctx.window.ExcelLensTranspose.prototype),{snapshot,limits:{rows:250,columns:50,cells:5000,bytes:1<<20,pageColumns:16},colName:n=>String.fromCharCode(64+n),serial:0,navigation:0,dialog:node('dialog'),model:ctx.window.ExcelLensTransposeModel,busy:false,controls(){},render(){accepted.push(this.data);},copy:async x=>copied.push(x),toast(){}});ui.dialog.open=true;
  return {ui,node,ctx,calls,copied,accepted,snapshot,setCurrent:value=>current=value};
}
test('one shared source read is forced even for a single cell and mode/page changes reuse accepted data',async()=>{
  const h=setup();await h.ui.load();assert.equal(h.calls.length,1);assert.equal(h.calls[0].force,true);assert.equal(h.accepted.length,1);h.ui.mode='raw';h.ui.page(1);assert.equal(h.calls.length,1);
});
test('closing or cancelling an accepted network request cannot publish its late data',async()=>{
  for(const action of ['close','cancel','switch']){
    const h=setup();let release;h.snapshot.read=()=>new Promise(resolve=>release=resolve);const pending=h.ui.load();
    if(action==='switch')h.setCurrent(false);else h.ui[action]();
    release({});await pending;assert.equal(h.accepted.length,0);assert.equal(h.ui.data??null,null);
  }
});
test('a failed retry preserves the previously accepted source snapshot',async()=>{
  const h=setup();await h.ui.load();const old=h.ui.data;h.snapshot.read=async()=>{throw Error('source unavailable');};await h.ui.load();assert.equal(h.ui.data,old);assert.match(h.node('transposeStatus').textContent,/保留上次内容/);
});
test('copying never turns a missing result into an empty string',async()=>{
  const h=setup();h.ui.mode='raw';h.ui.selectedItem={cell:{cacheMissing:true,hasFormula:true,raw:'',display:''}};await h.ui.copySelected();assert.deepEqual(h.copied,[]);
  h.ui.selectedItem={cell:{raw:'000001',display:'000001'}};await h.ui.copySelected();assert.deepEqual(h.copied,['000001']);
});
test('source navigation keeps its original ref but is rejected after a newer user intent',async()=>{
  const h=setup(),destinations=[];h.ui.selectedItem={ref:'A2',cell:{raw:'001'}};h.ui.navigate=async(sheet,ref,guard)=>{destinations.push({sheet,ref,allowed:guard()});h.ui.navigation++;assert.equal(guard(),false);};await h.ui.locate();assert.deepEqual(destinations,[{sheet:'数据',ref:'A2',allowed:true}]);
});
