const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const deferred=()=>{let resolve;const promise=new Promise(done=>resolve=done);return {promise,resolve};};
function setup(){
  const nodes=new Map(),node=id=>{if(!nodes.has(id)){const events={};nodes.set(id,{value:'',checked:false,disabled:false,open:false,textContent:'',style:{},classList:{toggle(){}},setAttribute(){},replaceChildren(){},append(){},focus(){},addEventListener(name,fn){events[name]=fn;},emit(name){events[name]?.();},showModal(){this.open=true;},close(){this.open=false;events.close?.();}});}return nodes.get(id);};
  const context={window:{},document:{getElementById:node,createElement:()=>({append(){}})},setTimeout,clearTimeout,AbortController,navigator:{clipboard:{readText:async()=>''}}};
  for(const name of ['cell-address','clipboard','clipboard-special','clipboard-panels'])vm.runInNewContext(fs.readFileSync(__dirname+'/'+name+'.js','utf8'),context);
  context.window.ExcelLensClipboard.configure({rows:10,columns:5,cells:50,textBytes:10000,htmlBytes:20000});
  node('copySpecialFormat').value='tsv';node('copySpecialMode').value='raw';node('pasteSpecialFormat').value='tsv';node('pasteSpecialMode').value='raw';
  return {context,node};
}
const payload=value=>({startCol:1,endCol:1,rows:[{row:1,cells:[{col:1,raw:value,display:value,hasFormula:false,type:'inlineStr'}]}]});

test('cancelled copy reads cannot overwrite a newer dialog, and a changed source cannot write the clipboard',async()=>{
  const h=setup(),first=deferred(),second=deferred(),writes=[];let number=0,current=true;
  const ui=new h.context.window.ExcelLensCopySpecial({capture:()=>{const next=number++?second:first;return {label:'原表',headerRow:1,maxRow:100,styled:false,sheet:'原表',current:()=>current,read:()=>next.promise};},copy:async content=>writes.push(content.text),toast(){},limits:h.context.window.ExcelLensClipboard.copyLimits()});
  ui.open();h.node('copySpecialClose').onclick();ui.open();h.node('copySpecialDialog').emit('close');second.resolve(payload('新的001'));await new Promise(r=>setImmediate(r));first.resolve(payload('旧的999'));await new Promise(r=>setImmediate(r));assert.equal(h.node('copySpecialPreview').value,'新的001');
  current=false;await ui.write();assert.deepEqual(writes,[]);assert(h.node('copySpecialStatus').textContent.includes('变化'));
});

test('a late clipboard write cannot close a newly reopened copy dialog',async()=>{
  const h=setup(),write=deferred();const ui=new h.context.window.ExcelLensCopySpecial({capture:()=>({label:'原表',headerRow:1,maxRow:100,styled:false,sheet:'原表',current:()=>true,read:async()=>payload('001')}),copy:()=>write.promise,toast(){},limits:h.context.window.ExcelLensClipboard.copyLimits()});
  ui.open();await new Promise(r=>setImmediate(r));const pending=ui.write();h.node('copySpecialClose').onclick();ui.open();await new Promise(r=>setImmediate(r));write.resolve(true);await pending;assert.equal(h.node('copySpecialDialog').open,true);assert.equal(h.node('copySpecialPreview').value,'001');
});

test('late clipboard reads do not erase text typed after the read request',async()=>{
  const h=setup(),read=deferred();h.context.navigator.clipboard.readText=()=>read.promise;
  const ui=new h.context.window.ExcelLensPasteSpecial({capture:()=>({label:'目标A1',row:0,col:0,columns:3,current:()=>true}),commit:async()=>{},refresh:async()=>{},toast(){}});
  ui.open();const pending=ui.readClipboard();h.node('pasteSpecialText').value='自己输入001';h.node('pasteSpecialText').oninput();read.resolve('迟到的剪贴板');await pending;assert.equal(h.node('pasteSpecialText').value,'自己输入001');h.node('pasteSpecialClose').onclick();
});

test('paste preview is read-only, a changed target refuses the whole transaction, and a save failure retains input',async()=>{
  const h=setup(),commits=[];let current=true,fail=true;
  const ui=new h.context.window.ExcelLensPasteSpecial({capture:()=>({label:'目标A1',row:0,col:0,columns:3,current:()=>current}),commit:async(context,changes)=>{commits.push(changes);if(fail)throw Error('磁盘未确认');},refresh:async()=>{},toast(){}});
  ui.open();h.node('pasteSpecialText').value='001\t=literal';ui.preview();assert.equal(commits.length,0);current=false;await ui.apply();assert.equal(commits.length,0);
  current=true;ui.preview();await ui.apply();assert.equal(commits.length,1);assert.equal(h.node('pasteSpecialDialog').open,true);assert.equal(h.node('pasteSpecialText').value,'001\t=literal');assert(h.node('pasteSpecialStatus').textContent.includes('未确认'));
  fail=false;ui.preview();await ui.apply();assert.equal(commits.length,2);assert.equal(h.node('pasteSpecialDialog').open,false);assert.equal(commits[1][0].value,'001');
});
