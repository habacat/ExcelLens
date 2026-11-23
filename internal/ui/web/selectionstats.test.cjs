const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
function setup(){
  class Element{
    constructor(){this.children=[];this.dataset={};this.attrs={};this.classList={add(){}};this.handlers={};this._text='';}
    set textContent(v){this._text=v;this.children=[];}get textContent(){return this._text+this.children.map(x=>x.textContent).join('');}
    addEventListener(name,fn){this.handlers[name]=fn;}append(...nodes){this.children.push(...nodes);}replaceChildren(...nodes){this._text='';this.children=nodes;}setAttribute(k,v){this.attrs[k]=v;}removeAttribute(k){delete this.attrs[k];}
  }
  const host=new Element(),document={getElementById:id=>id==='quickStats'?host:null,createElement:()=>new Element()},copied=[];
  const sandbox={window:{},document,navigator:{clipboard:{writeText:async value=>copied.push(value)}}};vm.runInNewContext(fs.readFileSync(__dirname+'/selectionstats.js','utf8'),sandbox);
  const stats=new sandbox.window.ExcelLensSelectionStats({state:{},api(){},colName(){},toast(){},profile(){}});
  return {stats,host,copied,format:sandbox.window.ExcelLensSelectionStats.formatNumber};
}
test('selection totals match the supplied Excel order, labels and ten-thousand grouping',()=>{
  const {stats,host}=setup();stats.render({nonEmpty:6,number:6,total:6,sum:'133759.02',average:'22293.17'});
  assert.deepEqual(host.children.map(x=>x.textContent),['平均值=2万2293.17','计数=6','求和=13万3759.02']);assert.equal(host.children[0].children[1].dataset.statValue,'22293.17');assert.equal(host.children[2].children[1].dataset.statValue,'133759.02');
});
test('status formatting never passes monetary values through floating point',()=>{
  const {format,stats,host}=setup();assert.equal(format('-133759.000001'),'-13万3759.000001');assert.equal(format('9007199254740993123.0000001'),'9007199254740993123.0000001');assert.equal(format('0.000000001'),'0.000000001');
  stats.render({nonEmpty:2,number:0,total:3});assert.deepEqual(host.children.map(x=>x.textContent),['计数=2']);
});
test('copying formatted statistics copies the original numeric string',async()=>{
  const {stats,host,copied}=setup();stats.render({nonEmpty:6,number:6,sum:'133759.02',average:'22293.17'});const strong=host.children[2].children[1];
  host.handlers.click({target:{closest:()=>strong}});await new Promise(resolve=>setImmediate(resolve));assert.deepEqual(copied,['133759.02']);
});

test('six metrics distinguish text from numbers and group decimal strings without rounding',()=>{
  const {stats,host,format}=setup();
  stats.metrics=new Set(['average','nonEmpty','number','minimum','maximum','sum']);stats.format='thousands';
  stats.render({nonEmpty:3,number:2,average:'1.75',minimum:'-12345.50',maximum:'12349.00',sum:'3.50'});
  assert.deepEqual(host.children.map(n=>n.textContent),['平均值=1.75','计数=3','数值计数=2','最小值=-12,345.50','最大值=12,349.00','求和=3.50']);
  assert.equal(format('-90071992547409930001.0020 ≈','myriad'),'-9007,1992,5474,0993,0001.0020 ≈');
  assert.equal(format('12345678.500','plain'),'12345678.500');
  stats.render({nonEmpty:1,number:0,sum:'0',average:'0'});
  assert.deepEqual(host.children.map(n=>n.textContent),['计数=1','数值计数=0']);
});
