const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('fs'),vm=require('vm'),path=require('path');
const source=fs.readFileSync(path.join(__dirname,'../../internal/ui/web/app.js'),'utf8');
const start=source.indexOf('  async function paintInitialPreview(');
const end=source.indexOf('  async function pollIndex(',start);

function previewHarness(api){
  const nodes=new Map(),renders=[];
  const S={filtered:false,sheetEpoch:3,indexReady:false,sheet:'台账'};
  const sandbox={S,AbortController,Set,Math,Array,encodeURIComponent,api,getViewportRequest:()=>({visibleStart:1,visibleEnd:20,visibleColStart:1,visibleColEnd:10}),geometry:{rows:{load(){}},cols:{load(){}}},updateCanvasSize(){},renderGrid(data,request){renders.push({data,request});},$(id){if(!nodes.has(id))nodes.set(id,{classList:{add(){},remove(){}},textContent:''});return nodes.get(id);}};
  sandbox.finishSheetSwitch=()=>{};
  const declaration=source.match(/const TEXT_SPILL_LOOKAHEAD = \d+;/)[0];
  vm.runInNewContext(declaration+source.slice(start,end)+';globalThis.preview=paintInitialPreview;globalThis.repaint=renderInitialPreview;',sandbox);
  return {S,nodes,renders,sandbox,preview:sandbox.preview,repaint:sandbox.repaint};
}

test('preview paints a bounded prefix without claiming complete indexing',async()=>{
  const h=previewHarness(async()=>({available:true,partial:true,maxRow:80,maxCol:39,rows:[]}));
  await h.preview('台账',3);
  assert.equal(h.renders.length,1);assert.equal(h.S.indexReady,false);assert.equal(h.S.previewOnly,true);
  assert.equal(h.renders[0].request.displayEnd,28);assert.equal(h.renders[0].data.colCount,22);
  assert.match(h.nodes.get('gridStatus').textContent,/首屏预览/);
});

test('late preview does not overwrite a completed or different sheet',async()=>{
  for(const change of [S=>{S.indexReady=true;},S=>{S.sheetEpoch=4;},S=>{S.previewAbort.abort();}]){
    let release;const response=new Promise(resolve=>{release=resolve;});
    const h=previewHarness(async()=>response);const pending=h.preview('台账',3);change(h.S);
    release({available:true,maxRow:80,maxCol:4,rows:[]});await pending;assert.equal(h.renders.length,0);
  }
});

test('an existing filter never displays unfiltered source rows as its preview',async()=>{
  let called=false;const h=previewHarness(async()=>{called=true;});h.S.filtered=true;
  await h.preview('台账',3);assert.equal(called,false);assert.equal(h.renders.length,0);
});

test('preview scrolling repaints from existing prefix data without another request',async()=>{
  let calls=0;const h=previewHarness(async()=>{calls++;return{available:true,maxRow:80,maxCol:39,rows:[]};});
  await h.preview('台账',3);
  h.sandbox.getViewportRequest=()=>({visibleStart:40,visibleEnd:60,visibleColStart:15,visibleColEnd:24});
  h.repaint();
  assert.equal(calls,1);assert.equal(h.renders[1].request.displayStart,32);assert.equal(h.renders[1].request.displayEnd,68);
  assert.equal(h.renders[1].data.columns.length,34);assert.equal(h.renders[1].data.startCol,3);assert.equal(h.S.indexReady,false);
});
