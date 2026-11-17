const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
function setup(values,truncated,rule){
  const nodes=new Map(),node=id=>{if(!nodes.has(id))nodes.set(id,{hidden:true,value:'',style:{},replaceChildren(){},focus(){}});return nodes.get(id);};
  const ctx={window:{},document:{getElementById:node},AbortController,URLSearchParams};
  vm.runInNewContext(fs.readFileSync(__dirname+'/autofilter.js','utf8'),ctx);
  const Class=ctx.window.ExcelLensAutoFilterDropdown;Class.prototype.bind=function(){};Class.prototype.position=function(){};Class.prototype.renderOptions=function(){};
  const rules=new Map(rule?[[1,rule]]:[]),s={sheet:'Data',indexReady:true,autoFilter:{row:1,startCol:1,endCol:1,rules}};let applied=0;
  const dropdown=new Class({state:s,api:async()=>({values:values.map(value=>({value,count:1})),truncated}),colName:()=> 'A',toast(){},onApply:async()=>applied++});
  return {dropdown,rules,node,get applied(){return applied;}};
}
test('confirming an incomplete list preserves the active whitelist and does not repeat the scan',async()=>{
  const rule={column:1,operator:'in',values:['a','b']},h=setup(['a','b'],true,rule);await h.dropdown.open(1,{});await h.dropdown.apply();
  assert.equal(h.rules.get(1),rule);assert.equal(h.applied,0);
  await h.dropdown.open(1,{});await h.dropdown.clearColumn();assert.equal(h.rules.size,0);assert.equal(h.applied,1);
});
test('equal-sized selections containing unlisted values are not mistaken for selecting every value',async()=>{
  const h=setup(['a','b'],false,{column:1,operator:'in',values:['a','unlisted']});await h.dropdown.open(1,{});
  h.dropdown.selected=new Set(['b','unlisted']);await h.dropdown.apply();
  assert.deepEqual([...h.rules.get(1).values],['b','unlisted']);assert.equal(h.applied,1);
});
test('an explicitly changed complete selection may clear a rule, while an incomplete one remains a whitelist',async()=>{
  for(const truncated of [false,true]){
    const h=setup(['a','b'],truncated,{column:1,operator:'in',values:['a']});await h.dropdown.open(1,{});
    h.dropdown.selected.add('b');await h.dropdown.apply();assert.equal(h.rules.has(1),truncated);assert.equal(h.applied,1);
  }
});

test('large worksheets can use the header sort without discarding predicates',async()=>{
  const rule={column:1,operator:'in',values:['a']},h=setup(['a','b'],false,rule);
  h.dropdown.s.largeData=true;await h.dropdown.open(1,{});await h.dropdown.sort(true);
  assert.equal(h.applied,1);assert.equal(h.dropdown.config.sortColumn,1);assert.equal(h.dropdown.config.descending,true);
  assert.equal(h.rules.get(1),rule);
});
