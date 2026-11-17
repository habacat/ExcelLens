const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs'),vm=require('node:vm');

function setup(api) {
  const elements=new Map();
  class Element {
    constructor(){this.children=[];this.dataset={};this.handlers={};this.hidden=false;this.value='';this.attrs={};this.classList={toggle(){}};}
    addEventListener(name,fn){this.handlers[name]=fn;}
    setAttribute(name,value){this.attrs[name]=value;}
    removeAttribute(name){delete this.attrs[name];}
    append(...nodes){this.children.push(...nodes);}
    replaceChildren(fragment){this.children=fragment.children;}
    focus(){this.focused=true;}
    scrollIntoView(){}
    close(){this.open=false;this.handlers.close?.();}
    showModal(){this.open=true;}
  }
  const document={getElementById(id){if(!elements.has(id))elements.set(id,new Element());return elements.get(id);},createElement(){return new Element();},createDocumentFragment(){return new Element();}};
  const context={document,window:{},setTimeout,clearTimeout,AbortController,URLSearchParams,encodeURIComponent};
  vm.runInNewContext(fs.readFileSync(__dirname+'/archives.js','utf8'),context);
  const opened=[];
  const instance=new context.window.ExcelLensArchives({api,open:async data=>opened.push(data),toast(){},fmtBytes:n=>String(n)});
  return {instance,elements,opened};
}

test('archive rendering stays bounded and filenames remain literal text',async()=>{
  const {instance,elements}=setup(async()=>({items:Array.from({length:100},(_,id)=>({id,name:`folder/<img onerror=bad>${id}.xlsx`,size:123})),total:100000}));
  instance.use({id:'archive-one',name:'账簿.zip'});await instance.load();
  assert.equal(elements.get('archiveList').children.length,100);
  const first=elements.get('archiveList').children[0];
  assert.equal(first.children[1].children[0].textContent,'<img onerror=bad>0.xlsx');
  assert.equal(elements.get('archiveNext').disabled,false);
  instance.select(33);await instance.load();assert.equal(instance.selected,33);
});

test('search keyboard connects directly to member selection and opening',async()=>{
  const {instance,elements,opened}=setup(async url=>url.startsWith('/api/archive/list')?{items:[{id:5,name:'a.txt',size:2},{id:8,name:'b.txt',size:2}],total:2}:url==='/api/archive/open'?{jobId:'job'}:{status:'done',result:{opened:true}});
  instance.use({id:'one',name:'archive.zip'});await instance.load();
  elements.get('archiveSearch').handlers.keydown({key:'ArrowDown',preventDefault(){}});
  assert.equal(elements.get('archiveList').focused,true);
  instance.keydown({key:'ArrowDown',preventDefault(){}});assert.equal(instance.selected,8);
  await instance.openSelected();assert.equal(opened.length,1);
});

test('an obsolete archive opener cannot replace the next archive or its error state',async()=>{
  let release;const pending=new Promise(resolve=>{release=resolve;});let calls=0;
  const {instance,elements,opened}=setup(async(url,options)=>{
    if(url==='/api/archive/open')return{jobId:'old-job'};
    if(options?.method==='POST')return{status:'cancelled'};
    if(url.startsWith('/api/archive/job')){calls++;return pending;}
    return{items:[{id:0,name:'new.txt',size:1}],total:1};
  });
  instance.use({id:'old',name:'old.zip'});instance.selected=0;
  const opening=instance.openSelected();await new Promise(resolve=>setImmediate(resolve));assert.equal(calls,1);
  instance.use({id:'new',name:'new.zip'});await instance.load();
  release({status:'done',result:{opened:true,fileName:'stale.txt'}});await opening;
  assert.equal(instance.archive.id,'new');assert.equal(instance.items[0].name,'new.txt');assert.equal(opened.length,0);
  assert.equal(elements.get('archiveError').hidden,true);assert.equal(instance.busy,false);
});

test('late directory results cannot overwrite a newer search',async()=>{
  let release;const first=new Promise(resolve=>{release=resolve;});let calls=0;
  const {instance}=setup(async()=>++calls===1?first:{items:[{id:2,name:'latest.csv',size:1}],total:1});
  instance.use({id:'same',name:'source.zip'});const pending=instance.load();await instance.load();
  release({items:[{id:1,name:'stale.csv',size:1}],total:1});await pending;
  assert.equal(instance.items[0].name,'latest.csv');
});

test('incomplete TXT needs a deliberate choice for clicks and keyboard, and resets on another member',async()=>{
  let opens=0,lastBody;
  const notice='TAR 不完整 <img onerror=bad>，仅存 123 / 456 字节';
  const {instance,elements}=setup(async(url,options)=>{
    if(url.startsWith('/api/archive/list'))return {archive:{id:'cut',name:'cut.tar',notice},items:[{id:0,name:'ledger.txt',size:123,declaredSize:456,incomplete:true},{id:1,name:'other.txt',size:3}],total:2};
    if(url==='/api/archive/open'){opens++;lastBody=JSON.parse(options.body);return{jobId:'partial'};}
    return{status:'done',result:{opened:true}};
  });
  instance.use({id:'cut',name:'cut.tar',notice});await instance.load();
  assert.equal(elements.get('archiveNotice').textContent,notice);assert.equal(elements.get('archiveNotice').hidden,false);
  assert.equal(elements.get('archiveOpen').disabled,true);assert.equal(elements.get('archivePartialChoice').hidden,false);
  await instance.openSelected();assert.equal(opens,0);
  instance.keydown({key:'Enter',preventDefault(){}});assert.equal(opens,0);
  elements.get('archiveAllowPartial').checked=true;elements.get('archiveAllowPartial').handlers.change();
  assert.equal(elements.get('archiveOpen').disabled,false);
  await instance.openSelected();assert.equal(opens,1);assert.equal(lastBody.allowPartial,true);
  instance.select(1);assert.equal(elements.get('archiveAllowPartial').checked,false);assert.equal(elements.get('archivePartialChoice').hidden,true);
  instance.select(0);assert.equal(elements.get('archiveOpen').disabled,true);
  instance.use({id:'full',name:'full.zip'});assert.equal(elements.get('archiveNotice').hidden,true);assert.equal(elements.get('archiveAllowPartial').checked,false);
});

test('data integrity notice remains visible on reload and clears for a complete source',()=>{
  const app=fs.readFileSync(__dirname+'/app.js','utf8');
  const start=app.indexOf('  function renderSourceIntegrity() {'),end=app.indexOf('  function showWorkspace() {',start);
  assert(start>0&&end>start);
  const element={};const S={source:{incomplete:true,archiveNotice:'原始压缩包不完整，末条记录未纳入表格'}};
  const sandbox={S,$:()=>element};vm.runInNewContext(app.slice(start,end)+';this.render=renderSourceIntegrity;',sandbox);
  sandbox.render();assert.equal(element.hidden,false);assert.equal(element.textContent,S.source.archiveNotice);
  S.source={format:'xlsx'};sandbox.render();assert.equal(element.hidden,true);assert.equal(element.textContent,'');
  S.source={incomplete:true,notice:'片段'};sandbox.render();assert.equal(element.hidden,false);
});
