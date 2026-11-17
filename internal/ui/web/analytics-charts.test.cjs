const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');

function setup({observe=false}={}){
  let themed=false;const events={},paint=[],text=[],lines=[],variables={},rects=[],paths=[],arcs=[];
  let current=[];
  const context={setTransform(){},clearRect(){},fillRect(x,y,w,h){paint.push(this.fillStyle);rects.push({color:this.fillStyle,x,y,w,h});},fillText(value){text.push({value,color:this.fillStyle});},beginPath(){current=[];},moveTo(x,y){current.push(['move',x,y]);},lineTo(x,y){current.push(['line',x,y]);},stroke(){lines.push(this.strokeStyle);paths.push({color:this.strokeStyle,points:current});},arc(...args){arcs.push(args);},closePath(){},fill(){}};
  const tip={hidden:true,style:{},getBoundingClientRect(){return {width:220};}};
  const canvas={width:0,height:0,style:{},parentElement:{querySelector(){return tip;},getBoundingClientRect(){return {left:0,top:0,width:640,height:300};}},getBoundingClientRect(){return {left:0,top:0,width:640,height:300};},getClientRects(){return [{}];},getContext(){return context;}};
  const frames=[],panels=[{}],observers=[];
  const env={window:{devicePixelRatio:1},WeakMap,requestAnimationFrame:fn=>(frames.push(fn),frames.length),document:{documentElement:{hasAttribute(){return themed;}},addEventListener(type,fn){events[type]=fn;},querySelectorAll:selector=>selector.endsWith('.analysis-panel')?panels:[canvas]},getComputedStyle(){return {getPropertyValue:name=>variables[name]||''};}};
  if(observe)env.window.ResizeObserver=class{constructor(callback){this.callback=callback;this.observed=[];observers.push(this);}observe(panel){this.observed.push(panel);}};
  vm.runInNewContext(fs.readFileSync(__dirname+'/analysis-numbers.js','utf8'),env);
  vm.runInNewContext(fs.readFileSync(__dirname+'/analysis-comparisons.js','utf8'),env);
  vm.runInNewContext(fs.readFileSync(__dirname+'/analytics-charts.js','utf8'),env);
  const result={columnKeys:['金额'],rows:[{keys:['样本'],values:[{exact:'10',display:'10'}]}]},original=JSON.stringify(result);
  return {canvas,result,original,paint,text,lines,variables,rects,paths,arcs,tip,frames,observers,panels,env,charts:env.window.ExcelLensAnalyticsCharts,numbers:env.window.ExcelLensAnalysisNumbers,draw:env.window.ExcelLensAnalyticsCharts.draw,change(value){themed=value;events['excellens:readingchange']();},changeNumbers(options){env.window.ExcelLensAnalysisNumbers.set(options);events['excellens:analysisnumberchange']();}};
}

test('snapshot mini trends preserve gaps and use a shared normalized range without overflowing the canvas',()=>{
  const h=setup(),c=exact=>({exact,display:exact}),domain=[c('-1e308'),c('1e308')];h.charts.sparkline(h.canvas,[c('-1e308'),null,c('1e308')],domain);
  const path=h.paths.at(-1);assert.equal(path.points.length,2);assert(path.points.every(p=>p[0]==='move'));assert(path.points.flatMap(p=>p.slice(1)).every(Number.isFinite));
  assert.equal(h.arcs.length,2,'isolated finite points must remain visible across gaps');
  h.charts.sparkline(h.canvas,[c('1/3')],[c('1/3'),c('2/3')]);assert(h.arcs.length>0);assert.equal(h.text.length,0);
});

test('snapshot mini trends show a range failure for underflow or overflow instead of fabricating zero',()=>{
  const h=setup();h.charts.sparkline(h.canvas,[{exact:'1e999',display:'99'}]);assert(h.text.some(t=>/绘图范围/.test(t.value)));assert.equal(h.paths.length,0);
});

test('panel and window resizing coalesce one repaint, adapt backing pixels and preserve exact results and drill keys',()=>{
  const h=setup({observe:true}),picked=[];h.draw(h.canvas,h.result,'bar',keys=>picked.push(keys));assert.equal(h.observers.length,1);assert.deepEqual(h.observers[0].observed,h.panels);
  h.paint.length=0;h.rects.length=0;h.env.window.devicePixelRatio=2;h.canvas.getBoundingClientRect=()=>({left:0,top:0,width:320,height:200});
  for(let i=0;i<20;i++){h.charts.resize();h.observers[0].callback();}assert.equal(h.frames.length,1);assert.equal(h.paint.length,0);h.frames.shift()();assert.equal(h.canvas.width,640);assert.equal(h.canvas.height,400);assert.equal(h.paint.length,2);
  const bar=h.rects.find(r=>r.color==='#287b78');h.canvas.onclick({clientX:bar.x+bar.w/2,clientY:bar.y+bar.h/2});assert.deepEqual(picked,[['样本']]);assert.equal(JSON.stringify(h.result),h.original);
  h.canvas.getClientRects=()=>[];h.paint.length=0;h.observers[0].callback();h.frames.shift()();assert.equal(h.paint.length,0);assert.equal(h.canvas.width,640);
});

test('comparison charts plot exact derived ratios with percentage axes and never turn undefined ratios into zero',()=>{
  const h=setup(),selected=[],result={columnKeys:['金额'],rows:[
    {keys:['A'],values:[{exact:'9007199254740993.31',display:'9007199254740993.31',comparison:{mode:'percent_grand',exact:'1/4',display:'25.00%',percent:true,numerator:'1',denominator:'4'}}]},
    {keys:['B'],values:[{exact:'0',display:'0.00',comparison:{mode:'percent_grand',exact:'',display:'分母为0',status:'zero_denominator',percent:true}}]},
    {keys:['C'],values:[{exact:'9',display:'9.00',comparison:{mode:'percent_grand',exact:'3/4',display:'75.00%',percent:true,numerator:'3',denominator:'4'}}]}
  ]},original=JSON.stringify(result);
  h.draw(h.canvas,result,'line',keys=>selected.push(keys));assert.equal(h.arcs.length,2);assert(h.text.some(item=>/%$/.test(item.value)));assert(!h.text.some(item=>String(item.value).includes('9.01e+15')));
  const [x,y]=h.arcs[0],event={clientX:x,clientY:y};h.canvas.onmousemove(event);assert.match(h.tip.textContent,/25.00%/);assert.match(h.tip.textContent,/精确比值：1\/4/);assert.match(h.tip.textContent,/本分组原始聚合：9007199254740993.31/);h.canvas.onclick(event);assert.equal(selected[0][0],'A');assert.equal(JSON.stringify(result),original);
});

test('existing charts repaint paper and labels on preset changes and restore the original palette without changing results',()=>{
  const h=setup();h.draw(h.canvas,h.result);
  assert.equal(h.paint[0],'#fff');assert(h.text.some(t=>t.color==='#92a0ac'));assert(h.lines.includes('#e8eef1'));
  Object.assign(h.variables,{'--reading-surface':'#2e333a','--reading-ink':'#dce2e8','--reading-muted':'#abb5bf','--reading-grid-line':'#39424c'});
  h.paint.length=0;h.text.length=0;h.lines.length=0;h.change(true);
  assert.equal(h.paint[0],'#2e333a');assert(h.text.every(t=>t.color==='#abb5bf'));assert(h.lines.every(c=>c==='#39424c'));
  assert(h.paint.includes('#287b78'),'series colors must remain distinct from page chrome');
  h.paint.length=0;h.text.length=0;h.lines.length=0;Object.keys(h.variables).forEach(k=>delete h.variables[k]);h.change(false);
  assert.equal(h.paint[0],'#fff');assert(h.text.some(t=>t.color==='#92a0ac'));assert.equal(JSON.stringify(h.result),h.original);
});

test('a hidden chart retains its drawable data but theme changes do not resize it',()=>{
  const h=setup();h.draw(h.canvas,h.result);h.canvas.getClientRects=()=>[];h.paint.length=0;
  h.change(true);assert.deepEqual(h.paint,[]);assert.equal(h.canvas.width,640);assert.equal(h.canvas.height,300);
});

const resultOf=values=>({columnKeys:['金额'],rows:values.map((exact,i)=>({keys:[String(i)],values:[{exact,display:'0'}]}))});
test('fractions draw from exact values; missing data leaves holes in bars, points and line segments',()=>{
  const h=setup(),result=resultOf(['1/3','','2/3']);
  h.draw(h.canvas,result,'bar');const bars=h.rects.filter(x=>x.color==='#287b78');
  assert.equal(bars.length,2);assert(Math.abs(bars[1].h/bars[0].h-2)<1e-12);
  h.draw(h.canvas,result,'line');assert.equal(h.arcs.length,2);
  const strokes=h.paths.filter(x=>x.color==='#287b78');assert.equal(strokes.length,2);assert(strokes.every(p=>p.points.length===1&&p.points[0][0]==='move'));
  const pick=h.draw(h.canvas,result,'scatter');assert.equal(pick({clientX:340,clientY:150}),null);
  assert.equal(result.rows[0].values[0].exact,'1/3');
});
test('tiny negative values keep their scale; long rational components are approximated without Infinity divided by Infinity',()=>{
  const h=setup();h.draw(h.canvas,resultOf(['-0.0000000000001']),'bar');
  const bar=h.rects.find(x=>x.color==='#287b78');assert(bar.h>200);assert(h.text.some(t=>t.value.includes('e-13')));
  h.rects.length=0;h.draw(h.canvas,resultOf(['1'+'0'.repeat(400)+'/3'+'0'.repeat(400)]),'bar');
  assert(h.rects.some(x=>x.color==='#287b78'&&x.h>200));
});
test('unrepresentable values refuse drawing and remove old interactions; missing heatmap cells remain blank',()=>{
  const h=setup();h.draw(h.canvas,h.result);h.rects.length=0;
  h.draw(h.canvas,resultOf(['1e400']));assert.equal(h.canvas.onclick,null);assert(h.text.some(t=>t.value.includes('超出图表范围')));assert.equal(h.rects.length,1);
  h.rects.length=0;
  h.draw(h.canvas,{columnKeys:['a','b'],rows:[{keys:['一'],values:[{exact:'',display:''},{exact:'0',display:'0'}]}]},'heatmap');
  assert.equal(h.rects.length,2,'only background and the real zero are painted');
});
test('limited donut totals and chart series show their actual plotted scope',()=>{
  const h=setup(),result=resultOf(Array(25).fill('1'));result.columnKeys=['金额','数量'];
  h.draw(h.canvas,result,'donut');assert(h.text.some(t=>t.value==='已绘制合计'));assert(h.text.some(t=>t.value.includes('16 个正值分类')&&t.value.includes('第1系列')));
  assert(!h.text.some(t=>t.value==='汇总合计'));
  h.text.length=0;h.draw(h.canvas,result,'horizontal');assert(h.text.some(t=>t.value.includes('24/25')&&t.value.includes('1/2')));
});

test('empty groups, literal labels and totals stay distinct; exact tooltips repaint preferences without changing drill keys',()=>{
  const h=setup(),value={exact:'9007199254741015.11',display:'9007199254741015.11'};
  const result={columnField:'C2',columnKeys:['','（空白）'],rows:[[''],['（空白）'],[]].map(keys=>({keys,values:[value,{exact:'1',display:'1.00'}]}))},before=JSON.stringify(result),picked=[];
  h.draw(h.canvas,result,'bar',(keys,column)=>picked.push({keys,column}));
  for(const label of ['空白值','（空白）','总计','空白列'])assert(h.text.some(t=>t.value===label),label);
  const bar=h.rects.find(rect=>rect.color==='#287b78'),event={clientX:bar.x+bar.w/2,clientY:bar.y+bar.h/2};
  h.canvas.onmousemove(event);assert(h.tip.textContent.startsWith('空白值 · 空白列：9.01e+15'));assert(h.tip.textContent.includes(value.exact));
  h.canvas.onclick(event);assert.deepEqual(picked,[{keys:[''],column:''}]);
  h.changeNumbers({...h.numbers.defaults,scientific:false});h.canvas.onmousemove(event);assert(h.tip.textContent.includes('9,007,199,254,741,015.11'));assert.equal(JSON.stringify(result),before);
  h.canvas.onclick(event);assert.deepEqual(picked[1],{keys:[''],column:''});
});
