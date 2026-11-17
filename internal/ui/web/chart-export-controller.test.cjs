const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const read=file=>fs.readFileSync(__dirname+'/'+file,'utf8'),tick=()=>new Promise(resolve=>setTimeout(resolve,2));
const limits={types:['png','svg'],scales:[1,2],minWidth:640,maxWidth:2400,minHeight:480,maxHeight:1600,defaultWidth:1200,defaultHeight:800,pixels:20000000,dataBytes:16<<20,imageBytes:20<<20,svgBytes:4<<20,svgNodes:20000,packageBytes:40<<20};
function harness(override){
  const nodes=new Map(),urls=[],revoked=[],messages=[],html=[],images=[];let failURL=0;
  class Node{
    constructor(tag='div'){this.tagName=tag;this.children=[];this.listeners={};this.attributes={};this.value='';this.open=false;this.hidden=false;this.classes=new Set();this.classList={toggle:(key,on)=>on?this.classes.add(key):this.classes.delete(key)};}
    set id(id){this._id=id;nodes.set(id,this);}get id(){return this._id;}
    set innerHTML(value){html.push(value);for(const m of value.matchAll(/<([a-z]+)\b[^>]*\bid="([^"]+)"[^>]*>/g)){const node=new Node(m[1]);node.id=m[2];}}
    append(...items){this.children.push(...items);}setAttribute(key,value){this.attributes[key]=value;}removeAttribute(key){delete this.attributes[key];delete this[key];}getAttribute(key){return this.attributes[key];}
    addEventListener(type,fn){(this.listeners[type]??=[]).push(fn);}dispatch(type,event={}){for(const fn of this.listeners[type]||[])fn(event);}
    showModal(){this.open=true;}close(){this.open=false;queueMicrotask(()=>this.dispatch('close'));}focus(){}
  }
  const env={window:{},Blob,TextEncoder,TextDecoder,Uint8Array,Uint32Array,DataView,AbortController,setTimeout,clearTimeout,queueMicrotask,
    URL:{createObjectURL(blob){if(failURL&&urls.length+1===failURL)throw Error('URL allocation failed');const item={url:'blob:local/'+(urls.length+1),blob};urls.push(item);return item.url;},revokeObjectURL:url=>revoked.push(url)},
    Option:function(text,value){this.textContent=text;this.value=value;},
    document:{body:new Node(),createElement:tag=>new Node(tag),getElementById:id=>nodes.get(id),querySelector:()=>({content:'test'})}};
  vm.runInNewContext(read('display-format.js'),env);vm.runInNewContext(read('analysis-results.js'),env);
  const drawing={result:{},mode:'bar',theme:{paper:'#fff',ink:'#263b55',muted:'#627184',grid:'#ccc'},numberSettings:{groupThousands:true,scientific:true,scientificExponent:12}};
  env.window.ExcelLensAnalyticsCharts={snapshot:()=>drawing,describe:()=>({totalRows:1,totalSeries:1,rowIndices:[0],seriesIndices:[0],drawableCells:1})};
  vm.runInNewContext(read('chart-export-model.js'),env);
  env.window.ExcelLensChartExportSurface={descriptions:()=>['完整已接受结果'],async image(payload,spec,...rest){images.push({payload,spec,rest});const custom=override?.(payload,spec,...rest);return custom===undefined?new Blob(['image'],{type:spec.type==='png'?'image/png':'image/svg+xml'}):custom;}};
  vm.runInNewContext(read('chart-export.js'),env);
  const state={id:'one'},input={kind:'analysis',origin:{workbookKey:'content',book:'path',job:'accepted'},query:{source:'wb:原表',rows:['C1']},fields:[{id:'C1',name:'原字段'}],result:{columnKeys:['金额'],rows:[{keys:['001'],values:[{exact:'9007199254740993.31',display:'9007199254740993.31'}]}]},canvas:{},expectedResult:drawing.result,mode:'bar',metricIndex:0,title:'<img src=x>',source:'源<原表>.xlsx',scope:'当前选区',metric:'金额',unit:'元',current:()=>state.id==='one',notify:(...m)=>messages.push(m)};
  const center=new env.window.ExcelLensChartExport.Controller(limits);
  return {center,state,input,drawing,nodes,urls,revoked,messages,images,html,env,failURL:n=>failURL=n};
}
function localFiles(blob){return blob.arrayBuffer().then(buffer=>{const data=Buffer.from(buffer),files={};let at=0;while(data.readUInt32LE(at)===0x04034b50){const size=data.readUInt32LE(at+18),n=data.readUInt16LE(at+26),name=data.subarray(at+30,at+30+n).toString(),start=at+30+n;files[name]=data.subarray(start,start+size);at=start+size;}return files;});}

test('opening performs no encoding; explicit generation packages exact accepted strings and safe source labels',async()=>{
  const h=harness();assert(h.center.open(h.input));assert.equal(h.images.length,0);assert(h.html.every(s=>!s.includes('<原表>')));
  h.input.result.rows[0].values[0].exact='99';h.input.query.source='wb:later';await h.center.generate();assert(h.center.accepted);
  const files=await localFiles(h.center.accepted.blob),data=JSON.parse(files['exact-data.json']);assert.equal(data.result.rows[0].values[0].exact,'9007199254740993.31');assert.equal(data.query.source,'wb:原表');assert.equal(data.output.width,1200);assert.equal(data.output.height,800);assert.equal(data.output.pixelWidth,2400);assert.equal(data.output.pixelHeight,1600);assert.equal(data.output.scale,2);assert.equal(data.output.type,'png');assert.equal(data.origin.job,'accepted');
  assert.deepEqual(Object.keys(files),['chart.png','exact-data.json','README.txt']);assert.equal(h.urls.length,2);assert.equal(h.center.busy,false);
});
test('cancel followed by retry preserves the new complete package against a late ignored-abort image',async()=>{
  let hold=true,release;const h=harness(()=>hold?new Promise(resolve=>release=()=>resolve(new Blob(['late'],{type:'image/png'}))):undefined);h.center.open(h.input);
  const first=h.center.generate();for(let i=0;i<30&&!release;i++)await tick();assert(release);h.center.cancel();assert.equal(h.center.busy,false);hold=false;await h.center.generate();const accepted=h.center.accepted;release();await first;
  assert.equal(h.center.accepted,accepted);assert.equal(h.urls.length,2);assert.equal(h.revoked.length,0);
});
test('failed regeneration and failed URL publication retain the previous complete package and revoke new resources',async()=>{
  let fail=false;const h=harness(()=>{if(fail)throw Error('image failed');});h.center.open(h.input);await h.center.generate();const accepted=h.center.accepted;
  fail=true;await h.center.generate();assert.equal(h.center.accepted,accepted);assert.match(h.nodes.get('chartExportStatus').textContent,/image failed.*保留上次结果/);assert.equal(h.revoked.length,0);
  fail=false;h.failURL(4);await h.center.generate();assert.equal(h.center.accepted,accepted);assert.deepEqual(h.revoked,['blob:local/3']);assert.match(h.nodes.get('chartExportStatus').textContent,/URL allocation failed/);
});
test('source/result guard invalidation closes the export, revokes accepted URLs, and rejects stale downloads',async()=>{
  const h=harness();h.center.open(h.input);await h.center.generate();h.state.id='same-content-other-path';assert.equal(h.center.revalidate(),false);assert.equal(h.center.dialog.open,false);assert.equal(h.center.accepted,null);assert.equal(h.revoked.length,2);
  for(const id of ['chartExportImageDownload','chartExportDownload']){let prevented=false;h.nodes.get(id).dispatch('click',{preventDefault:()=>prevented=true});assert(prevented);assert.equal(h.nodes.get(id).href,undefined);}
});

test('direct image and ZIP share the complete candidate, retain its filename after option edits, and block both downloads during replacement',async()=>{
  let hold=false,release;const h=harness(()=>hold?new Promise(resolve=>release=()=>resolve(new Blob(['replacement'],{type:'image/svg+xml'}))):undefined);
  h.center.open(h.input);await h.center.generate();const image=h.nodes.get('chartExportImageDownload'),zip=h.nodes.get('chartExportDownload'),accepted=h.center.accepted;
  assert.equal(image.href,accepted.imageURL);assert.equal(image.download,'ExcelLens-Chart.png');assert.equal(zip.href,accepted.packageURL);assert.equal(h.urls.find(x=>x.url===image.href).blob,accepted.image);
  const files=await localFiles(accepted.blob);assert.equal(files['chart.png'].toString(),await accepted.image.text());assert.equal(h.urls.length,2);
  h.nodes.get('chartExportType').value='svg';h.nodes.get('chartExportOptions').dispatch('change');assert.equal(image.download,'ExcelLens-Chart.png');assert.equal(image.href,accepted.imageURL);
  hold=true;const pending=h.center.generate();for(let i=0;i<30&&!release;i++)await tick();assert(release);
  for(const node of [image,zip]){assert.equal(node.getAttribute('aria-disabled'),'true');assert.equal(node.getAttribute('tabindex'),'-1');let prevented=false;node.dispatch('click',{preventDefault:()=>prevented=true});assert(prevented);}
  release();await pending;assert.equal(image.download,'ExcelLens-Chart.svg');assert.equal(image.href,h.center.accepted.imageURL);assert.equal(zip.href,h.center.accepted.packageURL);assert.deepEqual(h.revoked,[accepted.imageURL,accepted.packageURL]);
  for(const node of [image,zip]){assert.equal(node.getAttribute('aria-disabled'),'false');assert.equal(node.getAttribute('tabindex'),'0');let prevented=false;node.dispatch('click',{preventDefault:()=>prevented=true});assert(!prevented);}
  h.center.close();assert.equal(image.href,undefined);assert.equal(zip.href,undefined);assert.equal(h.revoked.length,4);
});
test('late generation after source change produces no public URL and leaves no retained model',async()=>{
  let release;const h=harness(()=>new Promise(resolve=>release=()=>resolve(new Blob(['late'],{type:'image/png'}))));h.center.open(h.input);const pending=h.center.generate();for(let i=0;i<30&&!release;i++)await tick();h.state.id='new-result';release();await pending;
  assert.equal(h.urls.length,0);assert.equal(h.center.dialog.open,false);assert.equal(h.center.bound,null);assert.equal(h.center.busy,false);
});
test('invalid sizes refuse before encoding, SVG uses logical dimensions, and option edits never relabel an old package',async()=>{
  const h=harness();h.center.open(h.input);h.nodes.get('chartExportWidth').value='1e3';await h.center.generate();assert.equal(h.images.length,0);assert.match(h.nodes.get('chartExportStatus').textContent,/整数/);
  h.nodes.get('chartExportWidth').value='1200';h.nodes.get('chartExportType').value='svg';await h.center.generate();assert.equal(h.images[0].spec.scale,1);const accepted=h.center.accepted;
  h.nodes.get('chartExportWidth').value='1000';h.nodes.get('chartExportOptions').dispatch('change');assert.equal(h.center.accepted,accepted);assert.match(h.nodes.get('chartExportStatus').textContent,/上次结果/);assert.equal(h.center.accepted.spec.width,1200);
});
test('native queued close events cannot clear an immediate reopen; unpainted or foreign draw snapshots refuse opening',async()=>{
  const h=harness();h.center.open(h.input);h.center.close();assert(h.center.open(h.input));await tick();assert(h.center.bound);await h.center.generate();assert(h.center.accepted);
  h.center.close();h.drawing.result={};assert.equal(h.center.open(h.input),false);assert.equal(h.messages.length,1);assert.equal(h.center.bound,null);assert.equal(h.revoked.length,2);
});

function chartHarness(){
  const frames=[],painted=[],exports=[],nodes=new Map(),node=id=>{if(!nodes.has(id))nodes.set(id,{value:'',textContent:''});return nodes.get(id);};
  const result={columnKeys:['金额'],rows:[{keys:['原分类'],values:[{exact:'1',display:'1'}],metricValues:[[{exact:'1',display:'1'},{exact:'9007199254740993.31',display:'9007199254740993.31'}]]}]};
  const q={source:'wb:原表',metrics:[{field:'C2',aggregate:'sum'},{field:'C3',aggregate:'sum'}]},state={opened:true,tab:'pivot',activeResult:result,activeResultQuery:q,fingerprint:'content',book:'path',openSerial:1,jobID:'done',reportOpenSerial:2,activeResultFields:[{id:'C3',name:'完成时金额'}],workspace:{workbook:'源.xlsx'}};
  node('analysisChartSelect').value='bar';node('analysisChartMetric').value='0';
  const ctx={window:{ExcelLensChartExport:{revalidate(){},open:input=>exports.push(input)}},snapshots:null,state,$:node,requestAnimationFrame:fn=>frames.push(fn),sourceModel:{caption:()=> '原选区'},toast(){},drill(){},effectiveMetrics:q=>q.metrics,metricLabel:(m,lookup)=>lookup(m.field)};
  vm.runInNewContext(read('analysis-results.js'),ctx);
  ctx.charts={draw(canvas,result,mode){painted.push({canvas,result,mode});}};
  const source=read('analytics.js'),section=(a,b)=>source.slice(source.indexOf(a),source.indexOf(b,source.indexOf(a)));
  vm.runInNewContext(section('  function queryScope(','  async function adoptComposition(')+section('  function metricCell(','  function renderMetrics(')+section('  function renderChart(){','  async function drill(')+';this.render=renderChart;this.exportImage=exportChartImage;this.setChartMode=setChartMode;',ctx);
  return {ctx,state,q,result,frames,painted,exports,nodes,node};
}
test('old animation frames cannot draw a new metric with an old projection or enable export for a foreign source',()=>{
  const h=chartHarness();h.ctx.render();h.node('analysisChartMetric').value='1';h.ctx.render();h.frames.shift()();assert.equal(h.painted.length,0);h.frames.shift()();assert.equal(h.painted.length,1);assert.equal(h.painted[0].result.rows[0].values[0].exact,'9007199254740993.31');
  h.state.query={source:'wb:new'};h.state.fields=[{id:'C3',name:'新字段'}];h.ctx.exportImage();assert.equal(h.exports[0].metric,'完成时金额');assert.equal(h.exports[0].query.source,'wb:原表');assert(h.exports[0].current());h.state.book='other-path';assert(!h.exports[0].current());
  h.ctx.render();h.state.activeResult={...h.result};h.frames.shift()();assert.equal(h.painted.length,1);
});
test('chart type changes persist the displayed presentation on the accepted report without changing its data source',()=>{
  const h=chartHarness();h.state.query={source:'wb:builder'};h.node('analysisChartSelect').value='donut';h.ctx.setChartMode();assert.equal(h.q.chart,'donut');assert.equal(h.state.query.chart,'donut');assert.equal(h.q.source,'wb:原表');assert.equal(h.state.query.source,'wb:builder');h.frames.shift()();h.ctx.exportImage();assert.equal(h.exports[0].mode,'donut');assert.equal(h.exports[0].query.chart,'donut');
});

test('composition chart exports retain the completed definition and source count after builder edits',()=>{
  const h=chartHarness();h.q.source='append:accepted';h.q.composition={version:1,name:'月度联合',inputs:[{ref:{book:'left'}},{ref:{book:'right'}}]};h.q.filters=[{field:'C2',operator:'gt',value:'0'}];
  h.ctx.render();h.frames.shift()();h.state.query={source:'wb:builder'};h.ctx.exportImage();
  assert.equal(h.exports[0].source,'源.xlsx / 月度联合');assert.equal(h.exports[0].scope,'2 个来源 · 已筛选');assert.equal(h.exports[0].query.source,'append:accepted');assert.equal(h.exports[0].query.composition.name,'月度联合');assert.equal(h.exports[0].origin.job,'done');
});
