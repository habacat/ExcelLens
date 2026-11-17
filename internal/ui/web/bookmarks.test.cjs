const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const deferred=()=>{let resolve,reject;const promise=new Promise((a,b)=>{resolve=a;reject=b});return {promise,resolve,reject};};
const tick=()=>new Promise(resolve=>setImmediate(resolve));
function setup(api){
  const nodes=new Map(),node=id=>{if(!nodes.has(id))nodes.set(id,{value:'',dataset:{},disabled:false,open:false,addEventListener(){},close(){this.open=false;}});return nodes.get(id);};
  const ctx={window:{},document:{getElementById:node},AbortController,confirm:()=>true};
  vm.runInNewContext(fs.readFileSync(__dirname+'/bookmarks.js','utf8'),ctx);
  const state={opened:true,fileName:'same.xlsx',workbookKey:'A',sheet:'Data',selected:{ref:'A1',display:'42'}},notices=[];
  const marks=new ctx.window.ExcelLensBookmarks({state,api,toast:message=>notices.push(message),openDialog(){},onChange(){}});marks.render=()=>{};
  return {marks,state,node,notices};
}
test('late loads cannot install marks from another workbook even when filenames match and abort is ignored',async()=>{
  const a=deferred(),b=deferred();let calls=0;const h=setup(()=>++calls===1?a.promise:b.promise);
  const first=h.marks.load();h.state.workbookKey='B';h.marks.resetBook();const second=h.marks.load();
  b.resolve({items:[{id:'B',sheet:'Data',ref:'B1'}]});await second;
  a.resolve({items:[{id:'A',sheet:'Data',ref:'A1'}]});await first;
  assert.equal(h.marks.items[0].id,'B');assert.equal(h.marks.loadedBook,'B');assert.equal(h.marks.has('Data','A1'),false);assert.equal(h.notices.length,0);
});
test('loading marks on opening the panel preserves a note being typed while updating its saved ID',async()=>{
  const pending=deferred(),h=setup(()=>pending.promise);h.marks.open();h.node('bookmarkNote').value='new draft';
  pending.resolve({items:[{id:'saved-id',sheet:'Data',ref:'A1',note:'old note',tag:'异常'}]});await tick();
  assert.equal(h.node('bookmarkNote').value,'new draft');assert.equal(h.node('bookmarkNote').dataset.id,'saved-id');
});
test('a save completing after a workbook change cannot paint marks, claim success or disable the new form',async()=>{
  const pending=deferred(),h=setup(()=>pending.promise);h.marks.prepareCurrent();h.node('bookmarkNote').value='A note';
  const save=h.marks.save();h.state.workbookKey='B';h.marks.resetBook();h.marks.prepareCurrent();
  pending.resolve({items:[{id:'A',sheet:'Data',ref:'A1'}]});await save;
  assert.equal(h.marks.items.length,0);assert.equal(h.node('bookmarkSaveBtn').disabled,false);assert.equal(h.notices.length,0);
});
test('typing a newer draft while saving retains the draft and installs the saved mark ID',async()=>{
  const pending=deferred(),h=setup(()=>pending.promise);h.marks.prepareCurrent();h.node('bookmarkNote').value='first draft';
  const save=h.marks.save();h.node('bookmarkNote').value='second draft';
  pending.resolve({items:[{id:'saved',sheet:'Data',ref:'A1',note:'first draft',tag:'待复核'}]});await save;
  assert.equal(h.node('bookmarkNote').value,'second draft');assert.equal(h.node('bookmarkNote').dataset.id,'saved');
  assert.match(h.notices.at(-1),/未保存/);
});
