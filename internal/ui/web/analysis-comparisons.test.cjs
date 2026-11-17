const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),vm=require('node:vm');
const read=name=>fs.readFileSync(path.join(__dirname,name),'utf8'),context={window:{}};
vm.runInNewContext(read('analysis-comparisons.js'),context);const model=context.window.ExcelLensAnalysisComparisons;
test('comparison explanations preserve the exact ratio, original aggregate and current-layer scope',()=>{
  const cell={exact:'9007199254740993.31',display:'9007199254740993',comparison:{mode:'running_percent',axis:'rows',exact:'1/3',display:'33.33%',percent:true,numerator:'3002399751580331.103333',denominator:'9007199254740993.31'}};
  const before=JSON.stringify(cell);assert.equal(model.selected(cell),cell.comparison);assert.match(model.title(cell),/精确比值：1\/3（1=100%）/);assert.match(model.title(cell),/本分组原始聚合：9007199254740993.31/);assert.match(model.title(cell),/明细仅查看当前分组/);assert.equal(model.label({comparison:'running_percent',comparisonAxis:'columns'}),'沿列累计占比');assert.equal(JSON.stringify(cell),before);
  const scope=model.scope({layout:'tree',metrics:[{aggregate:'average',comparison:'running_percent'}]});assert.match(scope,/每层范围为该父分组/);assert.match(scope,/再截取前 N 项/);assert.match(scope,/不保证相加为100%/);assert.equal(model.scope({metrics:[{aggregate:'sum'}]}),'');
});
test('comparison states remain nonnumeric and unknown names cannot resolve prototype properties',()=>{
  for(const [status,label] of [['zero_denominator','分母为0'],['empty','无有效值'],['empty_denominator','分母无有效值'],['constructor','不可用']]){
    const cell={exact:'0',comparison:{mode:'percent_grand',status,exact:'',display:label,numerator:'0',denominator:'0'}};assert.match(model.title(cell),new RegExp(label));assert.equal(model.selected(cell).exact,'');
  }
  for(const name of ['constructor','__proto__','toString'])assert.equal(model.label({comparison:name}),'未知比较方式');
});
function editor(query){
  class Node{constructor(tag='div'){this.tagName=tag;this.children=[];this.value='';this.className='';this.attributes={};}append(...children){this.children.push(...children);}replaceChildren(...children){this.children=children;}setAttribute(key,value){this.attributes[key]=value;}}
  const root=new Node(),state={query},document={createElement:tag=>new Node(tag)},ctx={document,state,$:()=>root,window:{ExcelLensAnalysisComparisons:model},fieldSelect:value=>Object.assign(new Node('select'),{value}),aggregations:[['sum','求和'],['count','计数'],['average','平均']],renderBuilder(){},syncFirstMetric(){if(state.query.metrics?.length){state.query.value=state.query.metrics[0].field;state.query.aggregate=state.query.metrics[0].aggregate;}},Option:function(text,value){this.text=text;this.value=value;}};
  const source=read('analytics.js'),start=source.indexOf('  function renderMetricEditor('),end=source.indexOf('  function setTab(',start);assert(start>0&&end>start);vm.runInNewContext(source.slice(start,end)+';this.render=renderMetricEditor',ctx);
  const find=(node,cls)=>node.className===cls?node:(node.children||[]).map(child=>find(child,cls)).find(Boolean);ctx.render();return {root,ctx,find};
}
test('legacy single metrics gain saved comparison settings only on explicit user choice',()=>{
  const q={value:'C3',aggregate:'sum',metrics:[]},complete=JSON.parse(JSON.stringify(q)),h=editor(q);assert.equal(q.metrics.length,0);
  const select=h.find(h.root,'analysis-control analysis-comparison-mode');select.value='running_percent';select.onchange();assert.equal(q.metrics[0].comparison,'running_percent');assert.equal(q.metrics[0].field,'C3');assert.deepEqual(complete,{value:'C3',aggregate:'sum',metrics:[]});
  h.ctx.render();const axis=h.find(h.root,'analysis-control analysis-comparison-axis');assert.equal(axis.value,'rows');axis.value='columns';axis.onchange();assert.equal(q.metrics[0].comparisonAxis,'columns');
  const mode=h.find(h.root,'analysis-control analysis-comparison-mode');mode.value='percent_row';mode.onchange();assert.equal(q.metrics[0].comparisonAxis,undefined);assert.equal(q.metrics[0].aggregate,'sum');assert.equal(q.aggregate,'sum');
});
test('record counts support comparison without requiring an arbitrary numeric field',()=>{
  const q={value:'',aggregate:'count',metrics:[]},h=editor(q),select=h.find(h.root,'analysis-control analysis-comparison-mode');select.value='percent_grand';select.onchange();assert.equal(q.metrics[0].aggregate,'count');assert.equal(q.metrics[0].field,'');assert.equal(q.metrics[0].comparison,'percent_grand');
});
