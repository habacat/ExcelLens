const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const tick=()=>new Promise(resolve=>setImmediate(resolve));
const limits={types:['png','svg'],scales:[1,2],minWidth:640,maxWidth:2400,minHeight:480,maxHeight:1600,defaultWidth:1200,defaultHeight:800,pixels:20000000,dataBytes:16<<20,imageBytes:20<<20,svgBytes:4<<20,svgNodes:20000,packageBytes:40<<20};
const spec={type:'svg',width:1200,height:800,scale:1,background:'captured'};
function setup(){
  const canvases=[],callbacks=[];
  const context=()=>({font:'11px "Segoe UI"',measureText(value){return {width:Array.from(String(value)).length*(Number.parseFloat(this.font.replace('bold ',''))||11)*.55};},setTransform(){},save(){},restore(){},translate(){},fillRect(){},fillText(){},beginPath(){},moveTo(){},lineTo(){},arc(){},closePath(){},fill(){},stroke(){}});
  const env={window:{},TextEncoder,TextDecoder,Uint8Array,Uint32Array,DataView,Blob,AbortController,setTimeout,clearTimeout,
    document:{documentElement:{hasAttribute:()=>false},addEventListener(){},querySelectorAll:()=>[],createElement(tag){assert.equal(tag,'canvas');const ctx=context(),canvas={width:300,height:150,getContext:()=>ctx,toBlob:callback=>callbacks.push(callback)};canvases.push(canvas);return canvas;}}};
  for(const file of ['analysis-numbers.js','analysis-comparisons.js','analysis-results.js','selection-quick-model.js','analytics-charts.js','chart-export-model.js','chart-export-surface.js'])vm.runInNewContext(fs.readFileSync(__dirname+'/'+file,'utf8'),env);
  const result={columnKeys:['金额','次列'],rows:[{keys:['<img src=x> 中😀'],values:[{exact:'9007199254740993.31',display:'9007199254740993.31'},{exact:'1/3',display:'0.33'}],metricValues:[[{exact:'9007199254740993.31',display:'9007199254740993.31'},{exact:'1/3',display:'0.33'}],[{exact:'1/3',display:'0.33'},{exact:'2/3',display:'0.67'}]]},{keys:['001'],values:[{exact:'',display:''},{exact:'0',display:'0'}],metricValues:[[{exact:'',display:''},{exact:'2/3',display:'0.67'}],[{exact:'0',display:'0'},{exact:'0',display:'0'}]]}],included:2};
  const input={kind:'analysis',origin:{workbookKey:'content',book:'path',job:'completed'},query:{source:'wb:明细',sourceScope:{version:1,mode:'selection'},rows:['C1']},fields:[{id:'C1',name:'原字段'}],result,
    mode:'bar',metricIndex:0,title:'<script>alert(1)</script> 中😀',source:'源文件.xlsx / 明细',scope:'当前画面选区',metric:'金额',unit:'源指标原单位',theme:{themed:true,paper:'#272b30',ink:'#dce2e8',muted:'#abb5bf',grid:'#39424c',error:'#ef9999'},numberSettings:env.window.ExcelLensAnalysisNumbers.get(),applicationVersion:'test'};
  return {env,canvases,callbacks,measure:context(),input,result,model:env.window.ExcelLensChartExportModel,surface:env.window.ExcelLensChartExportSurface,charts:env.window.ExcelLensAnalyticsCharts};
}

test('bounded JSON preserves exact strings, identifiers, Unicode, control characters, missing states and native JSON omission rules',()=>{
  const h=setup(),value={amount:'9007199254740993.31',id:'0001',fraction:'1/3',missing:{exact:'',cacheMissing:true},text:'中😀\u0000\r\n<&>',array:[null,undefined,0],omitted:undefined};
  const want=JSON.stringify(value),bytes=Buffer.byteLength(want);
  assert.equal(h.model.stringify(value,bytes),want);assert.throws(()=>h.model.stringify(value,bytes-1),/容量/);
  for(const bad of [NaN,Infinity,1n,()=>1])assert.throws(()=>h.model.stringify({bad},1000));
  const cycle={};cycle.self=cycle;assert.throws(()=>h.model.stringify(cycle,1000),/循环/);
  assert.equal(h.model.stringify({a:value,b:value},100000),JSON.stringify({a:value,b:value}));
  assert.throws(()=>h.model.stringify({text:'\u0000'.repeat(300)},1000),/容量/);
});
test('captured results detach the completed metric and source from later builder or original-object edits',()=>{
  const h=setup();h.input.metricIndex=1;const payload=h.model.capture(h.input,limits),plot=h.model.plot(payload);
  assert.equal(plot.rows[0].values[0].exact,'1/3');assert.equal(plot.rows[1].values[0].exact,'2/3');
  h.input.query.source='wb:other';h.input.fields[0].name='changed';h.result.rows[0].metricValues[0][1].exact='99';
  assert.equal(payload.query.source,'wb:明细');assert.equal(payload.fields[0].name,'原字段');assert.equal(h.model.plot(payload).rows[0].values[0].exact,'1/3');assert.equal(payload.origin.job,'completed');
  assert.throws(()=>h.model.capture(h.input,{...limits,dataBytes:100}),/容量/);
});
test('drawing descriptions match the shared plan for every chart and preserve actual pie row indexes',()=>{
  const h=setup();
  const r={columnKeys:Array.from({length:14},(_,i)=>String(i)),rows:Array.from({length:100},(_,i)=>({keys:[String(i)],values:Array.from({length:14},()=>({exact:i===0?'-1':i===1?'0':'1',display:'1'}))}))};
  const expected={bar:[60,12],horizontal:[24,1],line:[60,12],area:[60,12],donut:[16,1],pie:[16,1],scatter:[60,12],stacked:[60,12],heatmap:[28,12]};
  for(const [mode,[rows,series]]of Object.entries(expected)){const d=h.charts.describe(r,mode);assert.equal(d.rowIndices.length,rows,mode);assert.equal(d.seriesIndices.length,series,mode);assert.equal(d.totalRows,100);assert.equal(d.totalSeries,14);if(d.positiveOnly){assert.equal(d.rowIndices[0],2);assert.equal(d.rowIndices.at(-1),17);assert.equal(d.negativeExcluded,true);}}
  r.rows[0].values[0].exact='1e400';assert(h.charts.describe(r).unrepresentable);h.input.result=r;assert.throws(()=>h.model.capture(h.input,limits),/有效绘图/);
});
test('image dimensions and physical pixel admission are exact, typed and checked before creating a bitmap',async()=>{
  const h=setup();assert.deepEqual(JSON.parse(JSON.stringify(h.model.dimensions({...spec,type:'png',scale:2},limits))),{width:2400,height:1600,pixels:3840000});
  for(const bad of [{width:639},{width:'1200'},{height:1601},{type:'jpeg'},{scale:2},{background:'url(x)'}])assert.throws(()=>h.model.dimensions({...spec,...bad},limits));
  const payload=h.model.capture(h.input,limits);await assert.rejects(h.surface.image(payload,{...spec,type:'png',scale:2},{...limits,pixels:1},async()=>{}),/容量/);assert.equal(h.canvases.length,0);
});
test('all nine formats share document paint and produce bounded vectors with safely escaped source text',()=>{
  const h=setup(),original=JSON.stringify(h.input);
  for(const mode of h.charts.modes){
    const payload=h.model.capture({...h.input,mode},limits),ctx=new h.surface.SVGContext(h.measure,limits);h.surface.paintDocument(ctx,payload,spec);const svg=ctx.serialize(spec.width,spec.height,payload.chart.title);
    assert.match(svg,/<svg xmlns="http:\/\/www.w3.org\/2000\/svg"/);assert.match(svg,/&lt;script&gt;alert\(1\)&lt;\/script&gt;/);assert(!/<(?:script|image|foreignObject)\b|\shref=|\sonload=/.test(svg));assert(!/NaN|Infinity/.test(svg));assert.match(svg,/exact-data.json/);assert(h.model.stringify(payload,limits.dataBytes).includes('9007199254740993.31'));
    if(['pie','donut','line','area','scatter'].includes(mode))assert.match(svg,/A[\d.]/,mode);
  }
  assert.equal(JSON.stringify(h.input),original);
});
test('SVG arcs, state, negative rectangles and Unicode replacement obey finite geometry and resource limits',()=>{
  const h=setup(),ctx=new h.surface.SVGContext(h.measure,limits);ctx.beginPath();ctx.arc(20,20,10,0,Math.PI*2);ctx.arc(20,20,5,Math.PI*2,0,true);ctx.closePath();ctx.fill();ctx.fillRect(10,10,-5,-7);
  const svg=ctx.serialize(100,100,'title'),path=/<path d="([^"]+)"/.exec(svg)[1];assert.equal((path.match(/A/g)||[]).length,4);assert.equal((path.match(/ 0 0 1 /g)||[]).length,2);assert.equal((path.match(/ 0 0 0 /g)||[]).length,2);assert.match(svg,/x="5" y="3" width="5" height="7"/);
  for(const action of [()=>ctx.arc(1,1,-1,0,2),()=>ctx.moveTo(NaN,0),()=>ctx.translate(Infinity,0),()=>{ctx.fillStyle='url(https://x)';ctx.fillRect(0,0,1,1);}])assert.throws(action);
  assert.equal(h.surface.xml('中😀\u0000\u000b\uD800<&>'),'中😀\uFFFD\uFFFD\uFFFD&lt;&amp;&gt;');
  const tiny=new h.surface.SVGContext(h.measure,{...limits,svgNodes:1});tiny.fillRect(0,0,1,1);assert.throws(()=>tiny.fillRect(0,0,1,1),/容量/);
  const short=new h.surface.SVGContext(h.measure,{...limits,svgBytes:30});assert.throws(()=>short.fillRect(0,0,1,1),/容量/);
});
function unpack(bytes){
  const buf=Buffer.from(bytes),files=[];let at=0;
  const crc=data=>{let n=0xffffffff;for(const b of data){n^=b;for(let k=0;k<8;k++)n=n&1?(n>>>1)^0xedb88320:n>>>1;}return (n^0xffffffff)>>>0;};
  while(buf.readUInt32LE(at)===0x04034b50){assert.equal(buf.readUInt16LE(at+8),0);assert.equal(buf.readUInt16LE(at+6),0x800);const size=buf.readUInt32LE(at+18),nameLength=buf.readUInt16LE(at+26),name=buf.subarray(at+30,at+30+nameLength).toString('utf8'),data=buf.subarray(at+30+nameLength,at+30+nameLength+size);assert.equal(crc(data),buf.readUInt32LE(at+14));files.push({name,data});at+=30+nameLength+size;}
  const directoryStart=at;
  for(const file of files){assert.equal(buf.readUInt32LE(at),0x02014b50);assert.equal(buf.readUInt32LE(at+24),file.data.length);const n=buf.readUInt16LE(at+28);assert.equal(buf.subarray(at+46,at+46+n).toString(),file.name);at+=46+n;}
  assert.equal(buf.readUInt32LE(at),0x06054b50);assert.equal(buf.readUInt16LE(at+10),files.length);assert.equal(buf.readUInt32LE(at+12),at-directoryStart);assert.equal(buf.readUInt32LE(at+16),directoryStart);assert.equal(buf.length,at+22);return files;
}
test('stored ZIP has independently verified CRC, directory, exact bytes and all-or-nothing byte admission',async()=>{
  const h=setup(),files=[{name:'chart.svg',bytes:new TextEncoder().encode('<svg/>')},{name:'exact-data.json',bytes:new TextEncoder().encode(h.model.stringify({amount:'9007199254740993.31',id:'001',text:'中😀'},1000))},{name:'README.txt',bytes:new Uint8Array()}];
  const blob=await h.model.zip(files,100000),unpacked=unpack(await blob.arrayBuffer());assert.equal(unpacked[1].data.toString(),new TextDecoder().decode(files[1].bytes));assert.equal(unpacked.length,3);
  await assert.rejects(h.model.zip(files,blob.size-1),/容量/);assert.equal((await h.model.zip(files,blob.size)).size,blob.size);
  for(const bad of [[{name:'../source',bytes:new Uint8Array()}],[files[0],files[0]],[{name:'x',bytes:'text'}]])await assert.rejects(h.model.zip(bad,10000));
  let calls=0;await assert.rejects(h.model.zip([{name:'x',bytes:new Uint8Array(3<<20)}],4<<20,async()=>{if(++calls===2)throw Error('cancel');}),/cancel/);
});
test('native PNG cancellation retains one real encoder, one cancellable waiter and releases all canvases on late completion',async()=>{
  const h=setup(),payload=h.model.capture(h.input,limits),png={...spec,type:'png'},control=()=>{const c=new AbortController();return {c,check:async()=>{if(c.signal.aborted)throw Object.assign(Error('cancel'),{name:'AbortError'});}};};
  const a=control(),first=h.surface.image(payload,png,limits,a.check,a.c.signal).then(()=>null,e=>e);for(let i=0;i<20&&!h.callbacks.length;i++)await tick();assert.equal(h.callbacks.length,1);a.c.abort();
  const b=control(),second=h.surface.image(payload,png,limits,b.check,b.c.signal).then(()=>null,e=>e);await tick();assert.equal(h.canvases.length,1);
  const c=control();await assert.rejects(h.surface.image(payload,png,limits,c.check,c.c.signal),/等待编码/);b.c.abort();assert.equal((await second).name,'AbortError');
  const d=control(),last=h.surface.image(payload,png,limits,d.check,d.c.signal);await tick();assert.equal(h.canvases.length,1);
  h.callbacks[0](new Blob([new Uint8Array([137,80,78,71,13,10,26,10])],{type:'image/png'}));assert.equal((await first).name,'AbortError');
  for(let i=0;i<20&&h.callbacks.length<2;i++)await tick();assert.equal(h.callbacks.length,2);h.callbacks[1](new Blob(['png'],{type:'image/png'}));assert.equal((await last).type,'image/png');assert(h.canvases.every(c=>c.width===0&&c.height===0));
});
