const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const deferred=()=>{let resolve,reject;const promise=new Promise((a,b)=>{resolve=a;reject=b;});return {resolve,reject,promise};};
function setup(){
  const nodes=new Map();const get=id=>{if(!nodes.has(id))nodes.set(id,{textContent:''});return nodes.get(id);};
  const ctx={window:{},document:{getElementById:get},AbortController};
  vm.runInNewContext(fs.readFileSync(__dirname+'/cell-address.js','utf8'),ctx);
  vm.runInNewContext(fs.readFileSync(__dirname+'/reconcile.js','utf8'),ctx);
  return {ctx,model:ctx.window.ExcelLensReconcileModel,prototype:ctx.window.ExcelLensReconcile.prototype,get};
}
const field=(column,rawName,extras={})=>({column,rawName,name:rawName,headerLoaded:true,headerHash:rawName,...extras});
test('saved comparison rules rebind both sides by unique headers without guessing',()=>{
  const {model}=setup(),digest='b'.repeat(64),oldStamp='a'.repeat(64);
  const fields=[field(1,'编号',{uniqueHeader:true}),field(2,'金额',{uniqueHeader:true})];
  const input={kind:'workbook',name:'基线.csv',stamp:oldStamp,fileSHA256:digest,fields};
  const target={kind:'workbook',stamp:'c'.repeat(64),fields:[{...fields[1],column:1},{...fields[0],column:2}]};
  const record={kind:'ExcelLens.Reconcile',version:1,ruleVersion:1,scope:'all-data-rows',exceptionGroupsSHA256:digest,inputs:[input,input],
    query:{keys:[{left:1,right:1,mode:'text',trim:true}],fields:[{left:2,right:2,mode:'number'}]},
    counts:{equal:1,different:1,leftOnly:0,rightOnly:0,duplicate:0,review:0,invalidKeys:0},
    detail:{status:'different',sha256:digest,protectCSV:true}};
  const mapped=model.rebindRecord(record,[input,target]);
  assert.deepEqual(JSON.parse(JSON.stringify(mapped)),{keys:[{left:1,right:2,mode:'text',trim:true,ignoreCase:false}],fields:[{left:2,right:1,mode:'number',trim:false,ignoreCase:false}],includeEqual:false});
  assert.throws(()=>model.rebindRecord(record,[target,{...target,fields:[...target.fields,{...fields[0],column:3}]}]),/唯一表头/);
  assert.throws(()=>model.rebindRecord(record,[target,{...target,fields:[target.fields[0]]}]),/唯一表头/);
  const withTolerance={...record,ruleVersion:2,counts:{...record.counts,withinTolerance:1},query:{...record.query,fields:[{...record.query.fields[0],tolerance:'0.010'}]}};
  assert.equal(model.rebindRecord(withTolerance,[input,target]).fields[0].tolerance,'0.010');
  assert.throws(()=>model.rebindRecord({...withTolerance,ruleVersion:1},[input,target]),/容差/);
  assert.throws(()=>model.rebindRecord({...record,ruleVersion:3},[input,target]),/版本/);
  assert.throws(()=>model.rebindRecord({...record,counts:{duplicate:-1}},[input,target]),/计数/);
});
test('automatic field suggestions require complete unique names on both sides',()=>{
  const {model}=setup(),left=[field(1,'编号'),field(2,'金额'),field(3,'金额'),field(4,'未读',{headerLoaded:false}),field(5,'截取',{truncated:true}),field(6,'备注')],right=[field(2,'编号'),field(3,'金额'),field(4,'未读'),field(5,'截取'),field(7,'备注')];
  const result=JSON.parse(JSON.stringify(model.suggest(left,right)));
  assert.deepEqual(result,{keys:[{left:1,right:2,mode:''}],fields:[{left:6,right:7,mode:''}]});
});
test('column selection refuses duplicate, truncated and ambiguous names while retaining explicit column access',()=>{
  const {model}=setup(),fields=[field(1,'编号'),field(2,'金额'),field(3,'金额'),field(4,'A'),field(5,'长名',{truncated:true})];
  assert.equal(model.resolveColumn(' 1 ',fields),1);assert.equal(model.resolveColumn('e',fields),5);assert.equal(model.resolveColumn('编号',fields),1);
  assert.throws(()=>model.resolveColumn('金额',fields),/重复/);assert.throws(()=>model.resolveColumn('A',fields),/歧义/);assert.throws(()=>model.resolveColumn('长名',fields),/无效/);
});
test('late field requests cannot overwrite a changed side or a replaced workbook',async()=>{
  const {prototype}=setup(),requests=[],infos=[],sides={left:{serial:0,info:null},right:{serial:0,info:null}};
  const h=Object.assign(Object.create(prototype),{sides,epoch:0,controls(){},ref:()=>({book:'book',source:'wb:sheet'}),node:()=>({textContent:''}),post:()=>{const d=deferred();requests.push(d);return d.promise;},options:(side,info)=>infos.push(info),suggestMappings(){}});
  const old=h.loadSide('left'),fresh=h.loadSide('left');requests[1].resolve({stamp:'fresh',total:2,fields:[]});await fresh;
  requests[0].resolve({stamp:'old',total:1,fields:[]});await old;assert.equal(sides.left.info.stamp,'fresh');assert.equal(infos.length,1);
  const replaced=h.loadSide('left');h.epoch++;h.sides.left={serial:0,info:null};requests[2].resolve({stamp:'replaced',total:3,fields:[]});await replaced;assert.equal(h.sides.left.info,null);assert.equal(infos.length,1);
});
test('source navigation closes blank popups after stale results, errors or same-book responses',async()=>{
  const {ctx,prototype}=setup();let opened=0,closed=0,navigated=0,assigned=0;
  ctx.window.open=()=>{opened++;return {close:()=>closed++,location:{assign:()=>assigned++}};};ctx.window.ExcelLensAnalytics={close(){}};
  const h=Object.assign(Object.create(prototype),{job:'one',epoch:0,result:{right:{id:'foreign/wb:sheet'}},primaryBook:'primary',dialog:{close(){}},colName:()=> 'B',toast(){},navigate:async()=>navigated++});
  const gate=deferred();h.post=()=>gate.promise;const stale=h.goSource('right',2,2);h.job='two';gate.resolve({sameBook:false,url:'/?old'});await stale;
  assert.equal(closed,1);assert.equal(assigned,0);
  h.post=async()=>{throw Error('changed source');};await h.goSource('right',2,2);assert.equal(closed,2);
  h.post=async()=>({sameBook:true,kind:'workbook',sheet:'sheet'});await h.goSource('right',2,2);assert.equal(closed,3);assert.equal(navigated,1);
  h.post=async()=>({sameBook:false,url:'/?fresh'});await h.goSource('right',2,2);assert.equal(assigned,1);assert.equal(closed,3);assert.equal(opened,4);
});
test('downloads keep the completed result source after the editable configuration changes',async()=>{
  const {ctx,prototype,get}=setup();get('reconcileProtectCSV').checked=true;let args;
  ctx.window.ExcelLensExportReconcile=async(...value)=>args=value;
  const h=Object.assign(Object.create(prototype),{job:'completed',result:{left:{name:'左.csv',sheet:'旧左'},right:{name:'右.csv',sheet:'旧右'}},configChanged:true,busy:false,dialog:{close(){}},sides:{left:{info:{name:'新左'}}}});
  await h.export();assert.equal(args[0],'completed');assert.equal(args[1],true);assert.match(args[2],/旧左.*旧右/);assert.doesNotMatch(args[2],/新左/);
});
