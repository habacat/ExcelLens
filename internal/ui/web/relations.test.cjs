const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const deferred=()=>{let resolve;const promise=new Promise(r=>resolve=r);return {promise,resolve};};
function setup(){
  const nodes=new Map();
  class Node{
    constructor(){this.children=[];this.attrs={};this.dataset={};this.listeners={};this.open=false;this.value='';this.textContent='';this.classList={add(){},toggle(){}};}
    setAttribute(k,v){this.attrs[k]=v;}append(...children){this.children.push(...children);}replaceChildren(...children){this.children=children;}focus(){}
    addEventListener(k,f){(this.listeners[k]??=[]).push(f);}showModal(){this.open=true;}
    close(){this.open=false;queueMicrotask(()=>{for(const f of this.listeners.close||[])f();});}
  }
  const get=id=>{if(!nodes.has(id))nodes.set(id,new Node());return nodes.get(id);};
  get('fileFormats').textContent=JSON.stringify({extensions:['.csv','.xlsx']});
  const c={bookKey:'primary-fingerprint',book:'primary-book',opened:true,source:'wb:数据'},calls=[],exports=[],navigations=[],popups=[];
  let next=0;
  const runtime={TextEncoder,AbortController,FormData,setTimeout,Option:class extends Node{constructor(text,value){super();this.textContent=text;this.value=value;}},ResizeObserver:class{constructor(fn){this.fn=fn;}observe(){}},window:{open(){const popup={opener:{},closed:false,urls:[],close(){this.closed=true;},location:{assign(url){popup.urls.push(url);}}};popups.push(popup);return popup;}},document:{getElementById:get,createElement:()=>new Node(),createTextNode:value=>value,createDocumentFragment:()=>new Node(),body:new Node()}};
  for(const file of ['composition-model.js','relations-model.js','field-picker.js','composition-tasks.js','relations.js'])vm.runInNewContext(fs.readFileSync(__dirname+'/'+file,'utf8'),runtime);
  const limits={keys:4,columns:64,previewRows:100,defaultRows:1000000,maximumRows:1000000000,requestBytes:16384,resultBytes:8388608,viewRows:20,viewColumns:12,initialFields:8,snippetUnits:256};
  const formats={maxRows:1000000000,maxColumns:16384},fields=[{column:1,name:'编号',headerHash:'id',headerLoaded:true,headerUnique:true},{column:2,name:'金额',headerHash:'amount',headerLoaded:true,headerUnique:true}];
  const infos=['left','right'].map(side=>({ref:{book:side==='left'?c.book:'attached-book',source:'wb:数据',headerRow:1,fingerprint:side+'-fp',stamp:side+'-stamp'},name:side+'.csv',sheet:'数据',kind:'workbook',format:'csv',rows:1,fields:structuredClone(fields)}));
  const query={keys:[{left:1,right:1,mode:'',trim:false,ignoreCase:false}],leftColumns:[1,2],rightColumns:[1,2]};
  const data=id=>({id,mode:'prepare',outputMode:'left',parentId:id,workbookKey:c.bookKey,book:c.book,query:structuredClone(query),left:{...structuredClone(infos[0]),id:infos[0].ref.book+'/wb:数据',fingerprint:infos[0].ref.fingerprint,stamp:infos[0].ref.stamp,headerRow:1,total:1},right:{...structuredClone(infos[1]),id:infos[1].ref.book+'/wb:数据',fingerprint:infos[1].ref.fingerprint,stamp:infos[1].ref.stamp,headerRow:1,total:1},counts:{leftRecords:'1',rightRecords:'1',leftInvalid:'0',rightInvalid:'0',leftDuplicateGroups:'0',rightDuplicateGroups:'0',manyToManyGroups:'0',matchingGroups:'1',leftMatched:'1',rightMatched:'1',leftOnly:'0',rightOnly:'0',innerRows:'1',leftJoinRows:'1'},preview:{total:'1',truncated:false,rows:[{sequence:'1',left:{position:0,number:2,values:{1:{raw:'001',exists:true,type:'s'},2:{raw:'9007199254740993.31',exists:true,type:'n'}}},right:{position:0,number:2,values:{1:{raw:'001',exists:true,type:'s'},2:{raw:'0.1',exists:true,type:'n'}}},status:'matched',rightMatches:'1'}]}});
  const api=async(path,options)=>{const request=options?.body&&typeof options.body==='string'?JSON.parse(options.body):null;calls.push({path,request});if(path.endsWith('/start'))return {jobId:'new-'+(++next)};if(path.includes('/status?'))return {status:'done'};if(path.includes('/result?'))return data(new URL(path,'http://test').searchParams.get('id'));if(path.endsWith('/limits'))return limits;if(path.startsWith('/api/relation/sources'))return {currentBook:c.book,books:[]};return {};};
  const h=new runtime.window.ExcelLensRelations({api,context:()=>c,formats,fieldLimits:{pickerRows:3},onExport:async(...args)=>exports.push(args),navigate:async(...args)=>navigations.push(args),toast(){}});
  h.identityKey=h.identity();h.limits=limits;h.dialog.showModal();h.inputs=infos.map(info=>({book:info.ref.book,source:info.ref.source,headerRow:1,info,columns:[1,2]}));h.keys=structuredClone(query.keys);
  h.renderSources=()=>{};h.renderKeys=()=>{};h.buildResult=candidate=>({candidate});h.renderResult=view=>{h.rendered=view;h.controls();};h.accepted={data:data('accepted'),infos:structuredClone(infos)};
  const expected={id:'new',query:structuredClone(query),infos:structuredClone(infos),identity:{...c},mode:'left',operation:'prepare'};
  return {h,c,calls,exports,navigations,popups,get,data,infos,query,expected};
}

test('failed or malformed candidates release only their token and preserve the accepted result',async()=>{
  for(const invalid of ['response','render']){const s=setup(),old=s.h.accepted;s.h.tasks.run=async()=>{const value=s.data('candidate');if(invalid==='response')value.counts.leftOnly='1';return value;};if(invalid==='render')s.h.buildResult=()=>{throw Error('渲染失败');};
    await s.h.action(current=>s.h.run(current));assert.equal(s.h.accepted,old);assert.equal(s.calls.filter(c=>c.path.endsWith('/clear')).length,1);assert.equal(s.calls.at(-1).request.id,'candidate');assert.equal(s.get('relationsStatus').attrs.role,'alert');assert.equal(s.h.busy,false);
  }
});
test('publishing a fully built candidate atomically replaces and then releases the old result',async()=>{
  const s=setup(),old=s.h.accepted;await s.h.action(current=>s.h.run(current));assert.notEqual(s.h.accepted,old);assert.equal(s.h.accepted.data.id,'new-1');assert.equal(s.h.rendered.candidate,s.h.accepted);assert.equal(s.calls.at(-1).request.id,'accepted');assert.equal(s.calls[0].request.keepId,'accepted');
});
test('cancelling a late real start cleans its own backend job and keeps the accepted result',async()=>{
  const s=setup(),old=s.h.accepted,gate=deferred(),api=s.h.tasks.api;s.h.tasks.api=(path,...rest)=>path.endsWith('/start')?gate.promise:api(path,...rest);
  const pending=s.h.action(current=>s.h.run(current));s.h.cancel(true);gate.resolve({jobId:'late'});await pending;assert.equal(s.h.accepted,old);assert.deepEqual(s.calls.filter(c=>c.path.endsWith('/clear')).map(c=>c.request.id),['late']);assert.equal(s.get('relationsStatus').textContent,'已取消');
});
test('a late candidate body cannot replace a new accepted result after cancellation and retry',async()=>{
  const s=setup(),gate=deferred(),api=s.h.tasks.api,entered=deferred();s.h.tasks.api=(path,...rest)=>{if(path.includes('/result?')&&path.endsWith('new-1')){entered.resolve();return gate.promise;}return api(path,...rest);};
  const first=s.h.action(current=>s.h.run(current));await entered.promise;s.h.cancel(true);await s.h.action(current=>s.h.run(current));const accepted=s.h.accepted;gate.resolve(s.data('new-1'));await first;assert.equal(s.h.accepted,accepted);assert.equal(accepted.data.id,'new-2');assert.equal(s.h.busy,false);assert(!s.calls.some(c=>c.path.endsWith('/clear')&&c.request.id==='new-2'));
});
test('an older finally cannot enable controls while a newer operation is pending',async()=>{
  const s=setup(),first=deferred(),second=deferred(),p=s.h.action(()=>first.promise);s.h.cancel(false);const q=s.h.action(()=>second.promise);first.resolve();await p;assert.equal(s.h.busy,true);assert.equal(s.get('relationsConfig').disabled,true);second.resolve();await q;assert.equal(s.h.busy,false);
});
test('changing book resets the result and late field capture releases without populating the new book',async()=>{
  const s=setup(),gate=deferred();s.h.fieldsTasks.run=()=>gate.promise;const pending=s.h.action(current=>s.h.read(0,current));s.c.book='other-book';s.h.resetBook();gate.resolve({id:'late-fields',mode:'fields',book:'primary-book',workbookKey:s.c.bookKey,info:s.infos[0]});await pending;assert.equal(s.h.accepted,null);assert.equal(s.h.inputs.length,0);assert(s.calls.some(c=>c.path==='/api/composition/clear'&&c.request.id==='late-fields'));assert(s.calls.some(c=>c.path==='/api/relation/clear'&&c.request.id==='accepted'));
});
test('failed mode switch restores the accepted selector and does not discard its parent',async()=>{
  const s=setup(),old=s.h.accepted;s.h.tasks.run=async()=>{const value=s.data('candidate');value.mode='preview';value.parentId='accepted';value.outputMode='right_unmatched';return value;};s.get('relationsMode').value='right_unmatched';await s.h.action(current=>s.h.changeMode('right_unmatched',current));assert.equal(s.h.accepted,old);assert.equal(s.get('relationsMode').value,'left');assert.equal(s.calls.at(-1).request.id,'candidate');
});
test('cancelled initial source-list refresh cannot initialize a reopened configuration',async()=>{
  const s=setup(),gate=deferred(),entered=deferred();s.h.inputs=[];s.h.refresh=()=>{entered.resolve();return gate.promise;};const pending=s.h.open();await entered.promise;s.h.cancel(false);s.h.dialog.showModal();gate.resolve();await pending;assert.equal(s.h.inputs.length,0);
});
test('late upload refresh cannot overwrite newly selected sources after reopening',async()=>{
  const s=setup(),gate=deferred(),entered=deferred();s.h.api=async()=>({book:'uploaded'});s.h.refresh=()=>{entered.resolve();return gate.promise;};let current=true;const pending=s.h.upload(new Blob(['a,b']),()=>current);await entered.promise;current=false;const inputs=s.h.inputs=[{book:'chosen-left'},{book:'chosen-right'}];gate.resolve();await pending;assert.equal(s.h.inputs,inputs);assert.equal(s.h.inputs[1].book,'chosen-right');
});
test('queued native close cannot cancel an immediately reopened operation',async()=>{
  const s=setup();let resets=0;s.h.tasks.reset=()=>resets++;s.h.dialog.close();s.h.dialog.showModal();const serial=s.h.serial;await Promise.resolve();assert.equal(s.h.serial,serial);assert.equal(resets,0);s.h.dialog.close();await Promise.resolve();assert.equal(resets,1);
});
test('late foreign navigation closes only its empty popup and cannot move a new result',async()=>{
  const s=setup(),gate=deferred();s.h.post=()=>gate.promise;const pending=s.h.goSource(s.h.accepted,1,2,2);assert.equal(s.popups.length,1);s.h.cancel(false);gate.resolve({sameBook:false,url:'http://source/B2'});await pending;assert.equal(s.popups[0].closed,true);assert.deepEqual(s.popups[0].urls,[]);assert.equal(s.navigations.length,0);
});
test('export uses the accepted snapshot rather than changed inputs and rejects an exact over-limit count',async()=>{
  const s=setup();s.h.keys[0].mode='number';s.h.inputs[0].book='new-selection';s.get('relationsMaximum').value='1';s.get('relationsProtect').checked=true;await s.h.export();assert.equal(s.exports.length,1);assert.equal(s.exports[0][0].jobId,'accepted');assert.equal(s.exports[0][0].maximum,'1');assert.equal(s.exports[0][0].protectCSV,true);
  s.h.dialog.showModal();s.h.accepted.data.preview.total='1000000000000000000';s.get('relationsMaximum').value='1000000000';await s.h.export();assert.equal(s.exports.length,1);assert.match(s.get('relationsStatus').textContent,/1,000,000,000,000,000,000/);assert.equal(s.h.dialog.open,true);
});

test('large complete results recommend injected compressed format and preserve an explicit user choice',()=>{
  const s=setup();s.h.exportOptions=[{format:'csv',name:'CSV',compressed:false},{format:'zipcsv',name:'ZIP',compressed:true}];s.h.limits.zipRows=100000;
  s.h.accepted.data.preview.total='99999';s.h.chooseExportFormat();assert.equal(s.get('relationsFormat').value,'csv');
  s.h.accepted.data.preview.total='100000';s.h.chooseExportFormat();assert.equal(s.get('relationsFormat').value,'zipcsv');
  s.get('relationsFormat').value='csv';for(const callback of s.get('relationsFormat').listeners.change)callback();s.h.accepted.data.preview.total='1000000000';s.h.chooseExportFormat();assert.equal(s.get('relationsFormat').value,'csv');
});

test('compressed format belongs to the accepted export and forged formats do not start a download',async()=>{
  const s=setup();s.h.exportOptions=[{format:'csv',compressed:false},{format:'zipcsv',compressed:true}];s.get('relationsFormat').value='zipcsv';s.get('relationsMaximum').value='10';s.get('relationsProtect').checked=true;
  await s.h.export();assert.equal(s.exports.length,1);assert.equal(s.exports[0][0].format,'zipcsv');assert.equal(s.exports[0][0].jobId,'accepted');assert.equal(s.exports[0][0].protectCSV,true);
  s.h.dialog.showModal();s.get('relationsFormat').value='xlsx';await s.h.export();assert.equal(s.exports.length,1);assert.match(s.get('relationsStatus').textContent,/格式无效/);
});
