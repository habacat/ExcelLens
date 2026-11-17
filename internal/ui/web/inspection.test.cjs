const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
function setup(){
  const nodes=new Map(),node=id=>{if(!nodes.has(id))nodes.set(id,{value:'',checked:false,hidden:true,textContent:'',addEventListener(){},setAttribute(){},replaceChildren(){},append(){},focus(){},close(){this.open=false;}});return nodes.get(id);};
  const ctx={window:{},document:{getElementById:node,addEventListener(){},createDocumentFragment:()=>({append(){}})},AbortController,TextEncoder};
  for(const file of ['multiselection.js','profile.js','inspect.js'])vm.runInNewContext(fs.readFileSync(__dirname+'/'+file,'utf8'),ctx);
  const state={opened:true,sheet:'Data',book:'path',rowView:{id:'view'},workbookKey:'one',sheetEpoch:1,indexReady:true,range:{row1:4,row2:2,col1:2,col2:2,visual1:1,visual2:2},filtered:true,filterId:'f',usedMaxRow:4,usedMaxCol:2};
  const pending=[],calls=[],options={state,limits:{requestBytes:8192},colName:c=>String.fromCharCode(64+c),parseColumn:()=>2,toast(){},openDialog(id){node(id).open=true;},onChange(){},excludeThroughRow:()=>1,
    api:async(path,options)=>{calls.push({path,body:JSON.parse(options.body)});return new Promise(resolve=>pending.push(resolve));}};
  const profile=new ctx.window.ExcelLensProfileCenter(options),inspector=new ctx.window.ExcelLensSelectionInspector(options);
  return {state,node,profile,inspector,pending,calls};
}
test('profile and inspection send paired visual endpoints and keep edited source addresses explicit',async()=>{
  const h=setup();h.profile.render=()=>{};h.profile.open();const p=h.profile.analyze();
  assert.equal(h.calls[0].body.start,'B4');assert.equal(h.calls[0].body.end,'B2');assert.equal(h.calls[0].body.visualStart,1);assert.equal(h.calls[0].body.excludeThroughRow,1);
  h.pending.shift()({});await p;
  h.node('profileStart').value='B1';const manual=h.profile.analyze();assert.equal(h.calls[1].body.visualStart,undefined);h.pending.shift()({});await manual;
  h.node('inspectMode').value='formulas';h.node('inspectScope').value='selection';const opt=h.inspector.options();
  assert.equal(opt.start,'B4');assert.equal(opt.end,'B2');assert.equal(opt.visualStart,1);assert.equal(opt.visualEnd,2);assert.equal(opt.excludeThroughRow,1);
});
test('late profile responses cannot render a different workbook or a reopened dialog',async()=>{
  const h=setup(),rendered=[];h.profile.render=x=>rendered.push(x);h.profile.open();const old=h.profile.analyze();
  h.state.workbookKey='two';h.profile.resetBook();h.profile.open();const fresh=h.profile.analyze();
  h.pending[1]({id:'new'});await fresh;h.pending[0]({id:'old'});await old;
  assert.deepEqual(rendered,[{id:'new'}]);assert.equal(h.node('profileAnalyzeBtn').disabled,false);
});
test('a cancelled inspector response cannot repopulate caches after a workbook reset',async()=>{
  const h=setup();h.node('inspectMode').value='formulas';h.node('inspectScope').value='selection';
  const old=h.inspector.run();h.inspector.resetWorkbook();h.state.workbookKey='two';h.state.filtered=false;
  h.pending[0]({matches:[{ref:'B3'}],matched:1});await old;
  assert.equal(h.inspector.cache.size,0);assert.equal(h.inspector.matches.length,0);
  const fresh=h.inspector.run();assert.equal(h.calls.length,2);h.pending[1]({matches:[],matched:0});await fresh;
  assert.equal(h.inspector.cache.size,1);assert.equal(h.inspector.matches.length,0);
});
