const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const deferred=()=>{let resolve;const promise=new Promise(r=>resolve=r);return {promise,resolve};};
function setup(){
  const nodes=new Map();
  class Node{
    constructor(){this.open=false;this.textContent='';this.listeners={};}
    setAttribute(){} append(){} focus(){}
    addEventListener(type,fn){(this.listeners[type]??=[]).push(fn);}
    showModal(){this.open=true;}
    close(){this.open=false;queueMicrotask(()=>{for(const fn of this.listeners.close||[])fn();});}
  }
  const get=id=>{if(!nodes.has(id))nodes.set(id,new Node());return nodes.get(id);};
  get('fileFormats').textContent=JSON.stringify({extensions:['.xlsx','.csv']});
  const ctx={window:{ExcelLensCompositionModel:{},ExcelLensCompositionTasks:class{reset(){} clear(){return Promise.resolve();}}},document:{getElementById:get,createElement:()=>new Node(),body:new Node()},FormData,encodeURIComponent};
  vm.runInNewContext(fs.readFileSync(__dirname+'/field-picker.js','utf8'),ctx);
  vm.runInNewContext(fs.readFileSync(__dirname+'/composition.js','utf8'),ctx);
  const c={bookKey:'content',book:'path',opened:true,source:'wb:源'},h=new ctx.window.ExcelLensComposition({api:async()=>({book:'upload'}),context:()=>c,limits:{sources:8},toast(){}});
  h.identityKey=h.identity();h.dialog.showModal();h.render=()=>{throw Error('obsolete render');};h.renderSources=()=>{};h.renderLibrary=()=>{};h.controls=()=>{};
  return {h,c,get};
}

test('a cancelled initial source-list refresh cannot populate a reopened empty configuration',async()=>{
  const {h}=setup(),gate=deferred(),entered=deferred();h.refresh=()=>{entered.resolve();return gate.promise;};let adds=0;h.addSource=()=>adds++;
  const pending=h.open();await entered.promise;h.cancel();h.dialog.showModal();gate.resolve();await pending;
  assert.equal(adds,0);assert.equal(h.inputs.length,0);
});

test('a late upload-list refresh cannot replace sources selected after closing and reopening',async()=>{
  const {h}=setup(),gate=deferred(),entered=deferred();let current=true;
  h.refresh=()=>{entered.resolve();return gate.promise;};h.books=[{id:'upload',sheets:[{name:'上传表'}]}];h.inputs=[{book:'main',info:{}},{book:'empty',info:null}];h.rows=[{columns:[1,null]}];
  const pending=h.upload(new Blob(['a,b']),()=>current);await entered.promise;current=false;
  const newInputs=[{book:'new-main',info:{}},{book:'chosen',info:null}],newRows=[{columns:[2,3]}];h.inputs=newInputs;h.rows=newRows;
  gate.resolve();await pending;assert.equal(h.inputs,newInputs);assert.equal(h.inputs[1].book,'chosen');assert.equal(h.rows,newRows);assert.deepEqual(h.rows[0].columns,[2,3]);
});

for(const operation of ['save','removeSaved'])test('a late '+operation+' catalogue refresh cannot overwrite a newer status',async()=>{
  const {h,get}=setup(),gate=deferred(),entered=deferred();let current=true;
  h.preview={id:'prepared'};h.savedID='definition';h.post=async()=>({id:'definition',revision:3});h.refresh=()=>{entered.resolve();return gate.promise;};
  const pending=h[operation](()=>current);await entered.promise;current=false;get('compositionStatus').textContent='新候选读取失败';gate.resolve();await pending;
  assert.equal(get('compositionStatus').textContent,'新候选读取失败');
});

test('a queued native close event cannot cancel an immediately reopened preparation',async()=>{
  const {h}=setup();let resets=0;h.tasks.reset=()=>resets++;h.dialog.close();h.dialog.showModal();const before=h.serial;
  await Promise.resolve();assert.equal(h.serial,before);assert.equal(resets,0);assert.equal(h.dialog.open,true);
  h.dialog.close();await Promise.resolve();assert.equal(resets,1);
});

test('reopening before the native close event cancels the pending upload and starts a fresh list request',async()=>{
  const {h}=setup(),gate=deferred(),entered=deferred();let refreshes=0;
  h.books=[{id:'upload',sheets:[{name:'上传表'}]}];h.inputs=[{book:'main',info:{}},{book:'chosen',info:null}];h.rows=[{columns:[1,2]}];
  h.refresh=()=>{if(++refreshes===1){entered.resolve();return gate.promise;}return Promise.resolve();};
  const pending=h.action(current=>h.upload(new Blob(['a,b']),current),false);await entered.promise;
  h.dialog.close();await h.open();await Promise.resolve();gate.resolve();await pending;
  assert.equal(refreshes,2);assert.equal(h.inputs[1].book,'chosen');assert.equal(h.busy,false);assert.equal(h.dialog.open,true);
});
