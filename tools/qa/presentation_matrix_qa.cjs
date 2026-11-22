// Checks newly uncovered combinations; does not rerun the feature workflows.
// Only isolated developer browser/service processes and generated sources.
const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict'),{createHash}=require('node:crypto');
const config=require('./project_config.cjs'),{launch}=require('./browser_harness.cjs'),{presentation}=require('./presentation_fixtures.cjs'),{biff}=require('./catalogue_fixtures.cjs');
const root=path.resolve(__dirname,'../..'),base=path.join(root,'validation',config.validationVersion()),out=path.join(base,'private','presentation-matrix');fs.mkdirSync(out,{recursive:true});
const files=[path.join(out,'独立呈现矩阵.xlsx'),path.join(out,'原生默认字体.xls')];fs.writeFileSync(files[0],presentation());fs.writeFileSync(files[1],biff({formulas:true}));
const hash=file=>createHash('sha256').update(fs.readFileSync(file)).digest('hex'),hashes=files.map(hash);
const report={version:config.version(),result:'FAIL',startedAt:new Date().toISOString(),desktopManualAcceptance:false,officeWpsAcceptance:false,checks:[],cases:[],screenshots:[],errors:[]};let h;
const ready='window.ExcelLensPerformance?.().hasVisibleCoverage&&!window.ExcelLensPerformance().sheetLoading&&!window.ExcelLensPerformance().hasPendingRequest';
async function settle(p){await p.wait(ready);await p.run(async()=>{await document.fonts.ready;await new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)));});}
async function choose(p,id,value){await p.run((id,value)=>{const n=document.getElementById(id);n.value=value;n.dispatchEvent(new Event('change',{bubbles:true}));},id,value);await settle(p);}
async function open(p,file){await p.file('#fileInput',file);await p.wait('document.querySelector("#fileBadge").textContent==='+JSON.stringify(path.basename(file))+'&&'+ready);}
async function freeze(p,rows,columns){await p.click('#freezeBtn');await p.run((rows,columns)=>{document.querySelector('#freezeRowsInput').value=String(rows);document.querySelector('#freezeColsInput').value=String(columns);},rows,columns);await p.click('#freezeApplyBtn');await p.wait('!!document.querySelector("#freezeCorner .pinned-cell")');await settle(p);}
async function zoom(p,value){await p.run(value=>{const current=Number(document.querySelector('#zoomValue').textContent.replace('%','')),button=document.getElementById(value>current?'zoomInBtn':'zoomOutBtn');for(let i=0;i<Math.abs(value-current)/5;i++)button.click();},value);await p.wait('document.querySelector("#zoomValue").textContent==='+JSON.stringify(value+'%')+'&&'+ready);await settle(p);}
async function source(p,sheet){const value=await p.api('/api/sheet?'+new URLSearchParams({sheet,row:'1',rowWindow:'1',limit:'12',cols:'6'}));return value.rows.map(row=>({number:row.number,style:row.style||0,cells:(row.cells||[]).map(cell=>({ref:cell.ref,raw:cell.raw,display:cell.display,type:cell.type||'',style:cell.style||0,hasFormula:cell.hasFormula,cacheMissing:!!cell.cacheMissing,richText:cell.richText,conditional:cell.conditional}))}));}
async function fonts(p,selector){const doc=await p.call('DOM.getDocument'),node=await p.call('DOM.querySelector',{nodeId:doc.root.nodeId,selector});if(!node.nodeId)throw Error('Missing font probe '+selector);const value=(await p.call('CSS.getPlatformFontsForNode',{nodeId:node.nodeId})).fonts;assert(value.length&&value.some(font=>font.glyphCount>0),'No actual platform glyph fonts: '+selector);return value;}
async function shot(p,name){const file=path.join(out,name+'.png');await p.shot(file);report.screenshots.push(path.relative(root,file));}
let fixtureDate;
async function sample(p,native=false){return p.run((native,fixtureDate)=>{
  const context=document.createElement('canvas').getContext('2d');
  const refs=native?['B2','B3','B4']:['B3','B4','E3','D7','B8','D4','C4'];
  const cells=refs.map(ref=>{
    const node=document.querySelector((ref.startsWith('B')?'#freezeLeftCells':'#rowsLayer')+' .grid-cell[data-ref='+ref+']');if(!node)throw Error('Missing painted '+ref);
    const s=getComputedStyle(node),model=node._xlModel;
    const text=!native&&ref==='D4'?fixtureDate:(model.conditional?.display??model.display??model.raw);
    context.font=s.fontStyle+' '+s.fontWeight+' '+s.fontSize+' '+s.fontFamily;
    const measured=context.measureText(text).width,available=parseFloat(s.width)-parseFloat(s.paddingLeft)-parseFloat(s.paddingRight)-parseFloat(s.borderLeftWidth)-parseFloat(s.borderRightWidth);
    return {ref,raw:model.raw,type:model.type||'',display:text,text:node.textContent,marker:!!node.querySelector('.cell-number-overflow'),font:s.fontFamily,fontSize:s.fontSize,measured,available,width:node.getBoundingClientRect().width,styleWidth:node.style.width};
  });
  const out={dpi:devicePixelRatio,zoom:Number(document.querySelector('#zoomValue').textContent.replace('%','')),font:document.querySelector('#fontSelect').value,cells,nodes:document.querySelectorAll('.grid-cell').length};
  if(!native){
    const rich=document.querySelector('#rowsLayer .grid-cell[data-ref=C3]'),title=document.querySelector('#freezeCorner [data-ref=A1]');
    out.rich={text:rich.textContent,spans:[...rich.querySelectorAll('.cell-rich-text>span')].map(n=>({text:n.textContent,font:getComputedStyle(n).fontFamily,size:getComputedStyle(n).fontSize,weight:getComputedStyle(n).fontWeight,vertical:getComputedStyle(n).verticalAlign}))};
    out.title={text:title.textContent,width:title.getBoundingClientRect().width,expectedWidth:[1,2].reduce((sum,col)=>sum+document.querySelector('#freezeHeader [data-col="'+col+'"]').getBoundingClientRect().width,0)};
    out.mergeWidth=[4,5].reduce((sum,col)=>sum+document.querySelector('#columnHeaders [data-col="'+col+'"]').getBoundingClientRect().width,0);
    out.leading=document.querySelector('#freezeLeftCells [data-ref=A4]').textContent;out.hidden=!!document.querySelector('.grid-cell[data-row="6"]');out.missingCache=!!document.querySelector('#rowsLayer [data-ref=E4]')?._xlModel?.cacheMissing;out.executed=!!window.badPresentation;
  }else out.leading=document.querySelector('#freezeLeftCells [data-ref=A2]').textContent;
  return out;
},native,fixtureDate);}
function verify(value,expected,sourceRows){
  assert.equal(value.dpi,expected.dpi);assert.equal(value.zoom,expected.zoom);assert.equal(value.font,expected.font);assert(value.nodes<3000,'unbounded viewport nodes');
  const originals=new Map(sourceRows.flatMap(row=>row.cells).map(cell=>[cell.ref,cell]));
  for(const cell of value.cells){const context=JSON.stringify({expected,cell});assert(Number.isFinite(cell.measured)&&cell.available>0,context);
    assert.equal(cell.raw,originals.get(cell.ref)?.raw,context);assert.equal(cell.type,originals.get(cell.ref)?.type,context);
    if(Math.abs(cell.measured-cell.available)>1)assert.equal(cell.marker,cell.measured>cell.available,context);
    assert.equal(cell.text,cell.marker?cell.text:cell.display,context);if(cell.marker)assert.match(cell.text,/^#+$/,context);
  }
  if(expected.native){assert.equal(value.leading,'001');return;}
  assert.equal(value.leading,'0000001234567890123');assert.equal(value.hidden,false);assert.equal(value.missingCache,true);assert.equal(value.executed,false);
  assert.equal(value.rich.text,'中😀字体对照');assert.equal(value.rich.spans.length,2);assert.equal(value.rich.spans[0].weight,'400');assert.equal(value.rich.spans[1].vertical,'super');
  if(expected.font)for(const span of value.rich.spans)assert(span.font.includes(expected.font),JSON.stringify({expected,span}));
  assert(Math.abs(value.title.width-value.title.expectedWidth)<.75,JSON.stringify(value.title));assert(Math.abs(value.cells.find(c=>c.ref==='D7').width-value.mergeWidth)<.75,JSON.stringify(value));
  assert.equal(value.cells.find(c=>c.ref==='E3').raw,'9007199254740993.31');assert.equal(value.cells.find(c=>c.ref==='C4').display,'1.24');
}
(async()=>{try{
  h=await launch({out,errors:report.errors,duration:'12m'});const p=h.page;await p.call('DOM.enable');await p.call('CSS.enable');
  report.assetSHA256={};for(const name of ['app.js','cellview.js','freeze.js','richtext.js','conditional.js','merges.js','geometry.js','style.css','printing-model.js','printing-render.js','printing.js','printing.css']){const served=await p.run(async name=>await(await fetch('/assets/'+name)).text(),name),sha=createHash('sha256').update(served).digest('hex');assert.equal(sha,hash(path.join(root,'internal','ui','web',name)),name);report.assetSHA256[name]=sha;}
  await p.run(()=>{window.__presentationCalls=[];const original=window.fetch;window.fetch=function(input,options){window.__presentationCalls.push(new URL(typeof input==='string'?input:input.url,location.href).pathname);return original(input,options);};});
  await open(p,files[0]);const original=await source(p,'呈现');await freeze(p,2,2);
  // D4 is serial 45292 in built-in format 14. Measure its expected regional
  // spelling independently of the grid text, which may already be hashes.
  const dateCell=original.flatMap(row=>row.cells).find(cell=>cell.ref==='D4');
  assert.equal(dateCell.raw,'45292');assert.equal(dateCell.display,'2024-01-01');
  const pattern=(await p.api('/api/styles')).styles[dateCell.style].shortDate||'yyyy-mm-dd';
  const dateParts={yyyy:'2024',yy:'24',mm:'01',m:'1',dd:'01',d:'1'};
  fixtureDate=pattern.replace(/y+|m+|d+/g,token=>dateParts[token]);
  for(const dpi of [1,1.25,1.5,2])for(const scale of [70,100,125])for(const font of ['', '宋体','Arial','Consolas']){
    report.current={source:'xlsx',dpi,zoom:scale,font};await p.call('Emulation.setDeviceMetricsOverride',{width:1250,height:900,deviceScaleFactor:dpi,mobile:false});await zoom(p,scale);await choose(p,'fontSelect',font);
    const value=await sample(p);verify(value,report.current,original);report.cases.push({source:'xlsx',...value});
    if(dpi===2&&scale===70&&font==='宋体')await shot(p,'grid-70-dpr2-simsun');
    if(dpi===2&&scale===125&&font==='Consolas'){report.platformFonts={numeric:await fonts(p,'#freezeLeftCells [data-ref=B3]'),mixedFirst:await fonts(p,'#rowsLayer [data-ref=C3] .cell-rich-text>span:first-child'),mixedLast:await fonts(p,'#rowsLayer [data-ref=C3] .cell-rich-text>span:last-child')};await shot(p,'grid-125-dpr2-consolas');}
  }
  assert.deepEqual(await source(p,'呈现'),original);report.checks.push('48 independent OOXML DPI/zoom/font combinations preserve exact source models, inherited row style, conditional display, Unicode rich runs, hidden rows, merged geometry and main/frozen numeric width decisions measured independently from computed fonts');
  await p.click('#printPreviewOpenBtn');await p.wait('!!document.querySelector("#printPaperWrap .print-paper")&&document.querySelector("#printPreviewCancel").hidden');
  report.printSourceFonts={first:await fonts(p,'#printPreviewDialog .print-cell[data-ref=C3] .cell-rich-text>span:first-child'),last:await fonts(p,'#printPreviewDialog .print-cell[data-ref=C3] .cell-rich-text>span:last-child')};
  const paper=await p.run(()=>{const n=document.querySelector('#printPreviewDialog .print-paper'),c=document.querySelector('#printPreviewDialog .print-cell[data-ref=C3]');return {width:n.style.width,height:n.style.height,raw:document.querySelector('#printPreviewDialog .print-cell[data-ref=E3]')._xlSourceCell.raw,fonts:[...c.querySelectorAll('.cell-rich-text>span')].map(n=>getComputedStyle(n).fontFamily)};});
  assert(paper.fonts[0].includes('宋体')&&paper.fonts[1].includes('Arial'),JSON.stringify(paper));assert.equal(paper.raw,'9007199254740993.31');
  const reads=await p.evaluate('window.__presentationCalls.filter(x=>x.startsWith("/api/printing/")).length');
  for(const mode of ['50','150','fit']){await choose(p,'printScreenZoom',mode);assert.equal(await p.evaluate('window.__presentationCalls.filter(x=>x.startsWith("/api/printing/")).length'),reads);assert.deepEqual(await p.run(()=>{const n=document.querySelector('#printPreviewDialog .print-paper');return [n.style.width,n.style.height];}),[paper.width,paper.height]);}
  await shot(p,'paper-dpr2-source-fonts');await p.click('#printPreviewClose');report.checks.push('DPR2 preview retains original source fonts and physical paper despite Consolas screen override and 125 percent grid zoom; 50/150/fit screen zoom changes no paper dimensions or print reads');
  await open(p,files[1]);const legacy=await source(p,'旧表');await freeze(p,1,2);
  for(const dpi of [1.5,2])for(const scale of [70,125])for(const font of ['', '宋体','Consolas']){
    report.current={source:'biff8',native:true,dpi,zoom:scale,font};await p.call('Emulation.setDeviceMetricsOverride',{width:1250,height:900,deviceScaleFactor:dpi,mobile:false});await zoom(p,scale);await choose(p,'fontSelect',font);const value=await sample(p,true);verify(value,report.current,legacy);report.cases.push({source:'biff8',...value});
  }
  assert.deepEqual(await source(p,'旧表'),legacy);await shot(p,'biff-125-dpr2');report.checks.push('12 native BIFF8 default-font boundary combinations use the shared main/frozen presenter without changing number/text types, source coordinates, formula caches or leading-zero identifiers');
  assert.deepEqual(report.errors,[]);files.forEach((file,i)=>assert.equal(hash(file),hashes[i]));report.sourceSHA256=Object.fromEntries(files.map((file,i)=>[path.basename(file),hashes[i]]));report.sourceUnchanged=true;report.result='PASS';
}catch(error){report.failure=error.stack||String(error);if(h)try{report.diagnostic=await sample(h.page,report.current?.native);await shot(h.page,'failure');}catch(e){report.diagnosticError=e.message;}process.exitCode=1;
}finally{report.finishedAt=new Date().toISOString();if(h)await h.close();fs.writeFileSync(path.join(base,'presentation-matrix-browser.json'),JSON.stringify(report,null,2)+'\n');console.log(JSON.stringify({result:report.result,cases:report.cases.length,current:report.current,checks:report.checks,failure:report.failure,diagnostic:report.diagnostic},null,2));}})();
