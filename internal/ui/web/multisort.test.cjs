const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs'),vm=require('node:vm');

function setup() {
  class Element {
    constructor(){this.children=[];this.value='';this.handlers={};}
    addEventListener(name,fn){this.handlers[name]=fn;}
    setAttribute(){}
    appendChild(node){this.children.push(node);node.parent=this;}
    append(...nodes){nodes.forEach(n=>this.appendChild(n));}
    replaceChildren(){this.children=[];}
    querySelector(selector){return this.children.find(n=>n.className?.split(' ').includes(selector.slice(1)));}
    remove(){this.parent.children=this.parent.children.filter(n=>n!==this);}
    close(){}
  }
  const nodes=new Map();
  const document={getElementById(id){if(!nodes.has(id))nodes.set(id,new Element());return nodes.get(id);},createElement(){return new Element();}};
  const context={document,window:{}};
  vm.runInNewContext(fs.readFileSync(__dirname+'/multisort.js','utf8'),context);
  const state={sheet:'Data',indexReady:true,selected:{col:1},usedMaxRow:100,usedMaxCol:20,sortHeaderBySheet:new Map(),sortKeysBySheet:new Map()};
  const submitted=[];
  const instance=new context.window.ExcelLensMultiSort({state,parseColumn:v=>Number(v)||v.toUpperCase().charCodeAt(0)-64,colName:c=>String.fromCharCode(c+64),toast(){},openDialog(){},submit:async(keys,header)=>submitted.push({keys,header}),clear:async()=>{}});
  return {state,nodes,instance,submitted};
}

test('numeric sort mode survives dialog reopen and reaches the server request',async()=>{
  const h=setup();h.state.sortKeysBySheet.set('Data',[{column:2,mode:'number',descending:true}]);
  h.instance.show();
  const row=h.nodes.get('sortLevels').children[0];
  assert.equal(row.querySelector('.sort-mode').value,'number');
  row.querySelector('.sort-column').value='3';
  await h.instance.apply();
  assert.deepEqual(JSON.parse(JSON.stringify(h.submitted)),[{keys:[{column:3,mode:'number',descending:true}],header:1}]);
  h.instance.show();assert.equal(h.nodes.get('sortLevels').children[0].querySelector('.sort-mode').value,'number');
});

test('source-type sorting stays the default and each level chooses its own mode',()=>{
  const h=setup();h.instance.show();h.instance.add({column:2,mode:'text'});
  assert.equal(h.instance.values().keys[0].mode,'');assert.equal(h.instance.values().keys[1].mode,'text');
  h.nodes.get('sortLevels').children[1].querySelector('.sort-column').value='A';
  assert.throws(()=>h.instance.values(),/重复/);
});

test('natural text sorting is explicit and remains selected when reopening',async()=>{
  const h=setup();h.state.largeData=true;h.instance.show();
  const mode=h.nodes.get('sortLevels').children[0].querySelector('.sort-mode');
  assert(mode.children.some(option=>option.value==='natural'));
  mode.value='natural';await h.instance.apply();
  assert.equal(h.submitted[0].keys[0].mode,'natural');
  h.instance.show();assert.equal(h.nodes.get('sortLevels').children[0].querySelector('.sort-mode').value,'natural');
});
