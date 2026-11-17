const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

test('analysis conversion controls select explicit date order without starting analysis', () => {
  class Element {
    constructor(tag) {this.tagName=tag;this.children=[];this.style={};this.value='';}
    append(...nodes) {this.children.push(...nodes);}
    replaceChildren() {this.children=[];}
    setAttribute(name,value) {this[name]=value;}
  }
  const list = new Element('div');
  const step = {id:'D_date',name:'转换日期',source:'C1',op:'date',arg:'auto'};
  const source = fs.readFileSync(__dirname + '/analytics.js', 'utf8');
  const begin = source.indexOf('  function renderTransforms(){');
  const end = source.indexOf('  async function executeQuery(',begin);
  const context = {
    state:{fields:[{id:'C1',name:'原日期'}],query:{transforms:[step]}},
    document:{createElement:tag=>new Element(tag)},
    $:()=>list,
    Option:function(text,value){this.textContent=text;this.value=value;},
    fieldSelect:()=>new Element('select'),renderFields(){},renderBuilder(){}
  };
  vm.runInNewContext(source.slice(begin,end)+';renderTransforms();',context);
  const box = list.children[0];
  const formats = box.children[2];
  assert.equal(formats.value,'auto');
  assert.deepEqual(formats.children.map(x=>x.value),['auto','compact','dmy','mdy']);
  formats.value='dmy';formats.onchange();assert.equal(step.arg,'dmy');
  const operation = box.children[1].children[1];
  operation.value='time';operation.onchange();assert.equal(step.op,'time');assert.equal(step.arg,'auto');
  // The harness exposes no API or scan function: editing the conversion rule
  // must only update the query until the user presses Run Analysis.
});

test('removing a conversion also removes dependent fields and invalid query references', () => {
  const query={transforms:[{id:'D_date',source:'C1'},{id:'D_year',source:'D_date'},{id:'D_keep',source:'C2'}],filters:[{field:'D_year'},{field:'C1'}],rows:['D_date'],columns:['D_year'],value:'D_year',aggregate:'sum',metrics:[{field:'D_year'},{field:'C2'}]};
  const source=fs.readFileSync(__dirname+'/analytics.js','utf8');
  const begin=source.indexOf('  function removeTransform(id){'),end=source.indexOf('  function renderTransforms(){',begin);
  const context={state:{query},syncFirstMetric(){},renderFields(){},renderBuilder(){}};
  vm.runInNewContext(source.slice(begin,end)+";removeTransform('D_date');",context);
  assert.deepEqual(Array.from(query.transforms,t=>t.id),['D_keep']);assert.deepEqual(Array.from(query.filters,f=>f.field),['C1']);
  assert.equal(query.rows.length,0);assert.equal(query.columns.length,0);assert.equal(query.value,'');assert.equal(query.aggregate,'count');assert.deepEqual(Array.from(query.metrics,m=>m.field),['C2']);
});
