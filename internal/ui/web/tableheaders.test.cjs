const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs'),vm=require('node:vm');
class Element{
  constructor(){this.children=[];this.attrs={};this.dataset={};this.classes=new Set();this.classList={add:v=>this.classes.add(v)};}
  setAttribute(k,v){this.attrs[k]=v;}
  append(...nodes){this.children.push(...nodes);}
}
const context={window:{},document:{createElement:()=>new Element()}};
vm.runInNewContext(fs.readFileSync(__dirname+'/tableheaders.js','utf8'),context);
const T=context.window.ExcelLensTableHeaders,colName=n=>String.fromCharCode(64+n);

test('sticky TXT field labels remain independent of virtual row tiles and use literal text',()=>{
  const original={row:1,detected:true,columns:['凭证来源','<img onerror=bad>','金额']};
  const config=T.fromHeader(original);
  original.columns[0]='changed';
  const head=new Element();assert.equal(T.decorate(head,2,config,colName,true),true);
  assert.equal(head.children[0].textContent,'B');
  assert.equal(head.children[1].textContent,'<img onerror=bad>');
  assert.equal(head.children[2].dataset.column,'2');assert.equal(head.children[2].disabled,false);
  assert.equal(T.columnName(config,1,colName),'凭证来源');
  assert.equal(config.row,1);assert.equal(config.rules.size,0);
});

test('preview header buttons wait for the full index, filtered fields stay marked',()=>{
  const config=T.fromHeader({row:3,columns:['日期','金额','']});config.rules.set(2,{column:2});
  const preview=new Element();T.decorate(preview,2,config,colName,false);
  assert.equal(preview.children[2].disabled,true);assert.match(preview.children[2].className,/is-filtered/);
  assert.equal(T.columnName(config,3,colName),'C 列');
  assert.equal(T.decorate(new Element(),4,config,colName,true),false);
});

test('ordinary views and ambiguous input keep ordinary column letters',()=>{
  assert.equal(T.isTable(null),false);assert.equal(T.decorate(new Element(),1,null,colName,true),false);
  for(const header of [null,{row:0,columns:['x']},{row:1,columns:[]}])assert.equal(T.fromHeader(header),null);
});

test('TXT labels appear only after the source header clears the strip, and reset on return',()=>{
  const config=T.fromHeader({row:1,columns:['账号','金额']});
  for(const [top,expected] of [[0,false],[.5,false],[24.99,false],[25,true],[50000000,true],[24,false],[0,false]]){
    assert.equal(T.useStickyLabels(config,top,25),expected,`scrollTop=${top}`);
  }
  // A manually chosen later header uses its actual bottom, including zoom and
  // resized rows. Frozen source headers keep owning their labels at any offset.
  config.row=3;
  assert.equal(T.useStickyLabels(config,119.99,120),false);
  assert.equal(T.useStickyLabels(config,120,120),true);
  assert.equal(T.useStickyLabels(config,89.99,90,1),false);
  assert.equal(T.useStickyLabels(config,90,90,1),true);
  assert.equal(T.useStickyLabels(config,50000000,0,3),false);
  assert.equal(T.useStickyLabels(null,50000000,0),false);
  assert.equal(T.useStickyLabels({row:1},50000000,25),false);
});
