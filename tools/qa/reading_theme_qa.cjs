// Optional real-service theme coverage. Uses a synthetic XLSX, an isolated
// headless profile and the production page; never opens a user's browser.
const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict'),{spawn}=require('node:child_process'),{createHash}=require('node:crypto');
const address=process.argv[2];if(!address)throw Error('Usage: node reading_theme_qa.cjs SERVER_URL');
const root=path.resolve(__dirname,'../..'),config=require('./project_config.cjs'),version=config.version(),receipts=path.join(root,'validation',config.validationVersion());
const output=path.join(receipts,'private','reading-themes');fs.mkdirSync(output,{recursive:true});
const baseline=process.env.QA_THEME_BASELINE_CSS,homeOnly=process.argv.includes('--home-only'),failures=[],phases=[],homePreview=[],errors=[],assetDigests={};
const pause=ms=>new Promise(r=>setTimeout(r,ms)),digest=data=>createHash('sha256').update(data).digest('hex');
function zip(parts){
  const table=Array.from({length:256},(_,n)=>{for(let i=0;i<8;i++)n=n&1?0xedb88320^(n>>>1):n>>>1;return n>>>0;}),chunks=[],directory=[];let offset=0;
  for(const [name,value]of Object.entries(parts)){
    const data=Buffer.isBuffer(value)?value:Buffer.from(value),file=Buffer.from(name);let crc=0xffffffff;for(const b of data)crc=table[(crc^b)&255]^(crc>>>8);crc=(crc^0xffffffff)>>>0;
    const h=Buffer.alloc(30);h.writeUInt32LE(0x04034b50);h.writeUInt16LE(20,4);h.writeUInt32LE(crc,14);h.writeUInt32LE(data.length,18);h.writeUInt32LE(data.length,22);h.writeUInt16LE(file.length,26);chunks.push(h,file,data);
    const e=Buffer.alloc(46);e.writeUInt32LE(0x02014b50);e.writeUInt16LE(20,4);e.writeUInt16LE(20,6);e.writeUInt32LE(crc,16);e.writeUInt32LE(data.length,20);e.writeUInt32LE(data.length,24);e.writeUInt16LE(file.length,28);e.writeUInt32LE(offset,42);directory.push(e,file);offset+=h.length+file.length+data.length;
  }
  const central=Buffer.concat(directory),end=Buffer.alloc(22);end.writeUInt32LE(0x06054b50);end.writeUInt16LE(Object.keys(parts).length,8);end.writeUInt16LE(Object.keys(parts).length,10);end.writeUInt32LE(central.length,12);end.writeUInt32LE(offset,16);return Buffer.concat([...chunks,central,end]);
}
const ns='http://schemas.openxmlformats.org/spreadsheetml/2006/main',names=Array.from({length:24},(_,i)=>'主题核查工作表'+(i+1));
const parts={
  '[Content_Types].xml':'<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>'+names.map((_,i)=>`<Override PartName="/xl/worksheets/sheet${i+1}.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`).join('')+'</Types>',
  '_rels/.rels':'<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="w" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>',
  'xl/workbook.xml':`<workbook xmlns="${ns}" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>${names.map((name,i)=>`<sheet name="${name}" sheetId="${i+1}" r:id="r${i+1}"/>`).join('')}</sheets></workbook>`,
  'xl/_rels/workbook.xml.rels':'<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">'+names.map((_,i)=>`<Relationship Id="r${i+1}" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet${i+1}.xml"/>`).join('')+'<Relationship Id="s" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/></Relationships>',
  'xl/styles.xml':`<styleSheet xmlns="${ns}"><fonts count="2"><font><sz val="11"/><color rgb="FF304355"/></font><font><sz val="11"/><color rgb="FFFFFFFF"/></font></fonts><fills count="3"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill><fill><patternFill patternType="solid"><fgColor rgb="FF2F79CF"/></patternFill></fill></fills><borders count="1"><border/></borders><cellStyleXfs count="1"><xf/></cellStyleXfs><cellXfs count="2"><xf fontId="0" fillId="0" borderId="0"/><xf fontId="1" fillId="2" borderId="0" applyFill="1" applyFont="1"/></cellXfs></styleSheet>`
};
for(let i=0;i<names.length;i++){
  const rows=Array.from({length:80},(_,j)=>{const n=j+1;return `<row r="${n}"><c r="A${n}" t="inlineStr"><is><t>${j?'核查条目'+n:'项目'}</t></is></c><c r="B${n}"${j?'':' t="inlineStr"'}>${j?`<v>${n}.25</v>`:'<is><t>金额</t></is>'}</c>${n===2?'<c r="C2" s="1"><f>B2*2</f><v>4.5</v></c><c r="D2" t="e"><v>#DIV/0!</v></c>':''}${n===3?'<c r="D3" t="inlineStr"><is><t>00001</t></is></c>':''}</row>`;}).join('');
  parts[`xl/worksheets/sheet${i+1}.xml`]=`<worksheet xmlns="${ns}"><sheetPr><tabColor rgb="FFCC7944"/></sheetPr><dimension ref="A1:AD80"/><cols><col min="1" max="30" width="17" customWidth="1"/></cols><sheetData>${rows}</sheetData></worksheet>`;
}
const file=path.join(output,'themes.xlsx');fs.writeFileSync(file,zip(parts));const sourceSHA256=digest(fs.readFileSync(file));
const profile=fs.mkdtempSync(path.join(output,'chrome-')),child=spawn(process.env.QA_CHROME||'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe',['--headless=new','--no-first-run','--no-default-browser-check','--disable-extensions','--disable-component-update','--disable-sync','--disable-background-networking','--remote-debugging-port=0','--user-data-dir='+profile,'about:blank'],{windowsHide:true,stdio:'ignore'});
let socket,sequence=0,browser,presets,profileRemoved=false;const pending=new Map();
function cdp(method,params={}){return new Promise((resolve,reject)=>{const id=++sequence,timer=setTimeout(()=>{pending.delete(id);reject(Error(method+' timeout'));},20000);pending.set(id,{resolve,reject,timer});socket.send(JSON.stringify({id,method,params}));});}
async function evaluate(expression){const r=await cdp('Runtime.evaluate',{expression,returnByValue:true,awaitPromise:true});if(r.exceptionDetails)throw Error(JSON.stringify(r.exceptionDetails));return r.result.value;}
async function waitFor(expression){for(let i=0;i<250;i++){if(await evaluate(expression))return;await pause(80);}throw Error('Timeout: '+expression);}
async function settle(){await evaluate('Promise.all(document.getAnimations().filter(a=>a.effect?.getComputedTiming().endTime<=300).map(a=>a.finished.catch(()=>{})))');}
async function apply(preset){await evaluate(`document.querySelector('input[name="readingPreset"][value="${preset}"]').click()`);await settle();}
async function capture(name){const r=await cdp('Page.captureScreenshot',{format:'png',captureBeyondViewport:false});fs.writeFileSync(path.join(output,name+'.png'),Buffer.from(r.data,'base64'));}
async function snapshot(selectors){return evaluate(`(${function(selectors){
  return selectors.map(selector=>{const n=document.querySelector(selector);if(!n)throw Error('Missing coverage target: '+selector);const s=getComputedStyle(n);return {selector,background:s.backgroundColor,color:s.color,borders:[s.borderTopColor,s.borderRightColor,s.borderBottomColor,s.borderLeftColor],image:s.backgroundImage};});
}.toString()})(${JSON.stringify(selectors)})`);}
async function audit(){return evaluate(`(${function(){
  // Mixed palette colours compute to color(srgb r g b / a) with 0..1 channels.
  const color=value=>{const n=(value.match(/[\d.]+/g)||[]).map(Number);return value.startsWith('color(')?n.map((x,i)=>i<3?Math.round(x*255):x):n;},opaque=c=>c.length===3||c[3]===1;
  const styles=getComputedStyle(document.documentElement),accent=styles.getPropertyValue('--accent');
  const probe=document.createElement('span');probe.style.color=accent;document.body.append(probe);const primary=getComputedStyle(probe).color;probe.remove();
  const skip='.grid-region,.reading-swatch,.crosshair-current-color,.brand-mark,.landing-product img,.picture-stage,.analysis-brand-icon';
  const light=[],low=[];const luminance=c=>c.reduce((sum,n,i)=>{n/=255;return sum+[.2126,.7152,.0722][i]*(n<=.04045?n/12.92:((n+.055)/1.055)**2.4);},0);
  for(const n of document.querySelectorAll('body *')){
    if(!(n instanceof HTMLElement)||n.closest(skip))continue;
    const s=getComputedStyle(n),bg=color(s.backgroundColor),fg=color(s.color),id=n.id?'#'+n.id:n.tagName.toLowerCase()+'.'+[...n.classList].join('.');
    if(opaque(bg)&&Math.min(...bg.slice(0,3))>160&&s.backgroundColor!==primary&&!n.closest('.export-result'))light.push({id,background:s.backgroundColor});
    if(n.matches(':disabled')||n.closest(':disabled')||!opaque(fg)||!Array.from(n.childNodes).some(c=>c.nodeType===3&&c.textContent.trim()))continue;
    let base=n,under=bg;while(base.parentElement&&!opaque(under)){base=base.parentElement;under=color(getComputedStyle(base).backgroundColor);}
    if(!opaque(under))continue;
    const a=luminance(fg.slice(0,3)),b=luminance(under.slice(0,3)),contrast=(Math.max(a,b)+.05)/(Math.min(a,b)+.05);
    if(contrast<4.5)low.push({id,color:s.color,background:under.slice(0,3),contrast:Number(contrast.toFixed(2))});
  }
  return {light,low};
}.toString()})()`);}
async function force(selector,state){const doc=await cdp('DOM.getDocument'),node=await cdp('DOM.querySelector',{nodeId:doc.root.nodeId,selector});assert(node.nodeId,'Missing pseudo target '+selector);await cdp('CSS.forcePseudoState',{nodeId:node.nodeId,forcedPseudoClasses:state});await settle();}
async function matrix(name,selectors,{surface=[],inset=[],paper=[],active=[],canvas=false,bottom=false}={}){
  await pause(650);await waitFor('window.__themePending===0');
  await apply('off');const original=await snapshot(selectors),source=await evaluate(`Array.from(document.querySelectorAll('.grid-cell[data-ref="C2"]'),n=>n.style.cssText)`),cases=[];
  for(const preset of presets.filter(x=>x!=='off')){
    await evaluate('window.__themeReads=[]');await apply(preset);
    const state=await evaluate(`(()=>{const s=getComputedStyle(document.documentElement),probe=document.createElement('i'),v={};document.body.append(probe);for(const key of ['surface','inset','paper','ink','muted','line','active','accent']){probe.style.color='var(--'+key+')';v[key]=getComputedStyle(probe).color;}probe.remove();return {tone:document.documentElement.dataset.readingTone,palette:v,reads:window.__themeReads,source:Array.from(document.querySelectorAll('.grid-cell[data-ref="C2"]'),n=>n.style.cssText)};})()`);
    const values=await snapshot(selectors);for(const [token,list]of Object.entries({surface,inset,paper,active}))for(const selector of list){const value=values.find(x=>x.selector===selector);if(value.background!==state.palette[token])failures.push({phase:name,preset,selector,expected:state.palette[token],actual:value.background});}
    if(state.reads.length)failures.push({phase:name,preset,unexpectedReads:state.reads});
    if(JSON.stringify(state.source)!==JSON.stringify(source))failures.push({phase:name,preset,sourcePresentationChanged:true});
    if(bottom){
      for(const [selector,disabled]of [['#sheetNextBtn',false],['#sheetPrevBtn',true]]){const rest=values.find(x=>x.selector===selector);await force(selector,['hover']);const [v]=await snapshot([selector]);if((v.background===rest.background)!==disabled)failures.push({phase:name,preset,selector,hover:v.background});await force(selector,[]);}
      await evaluate(`(()=>{const n=document.querySelector('#sheetTabs');n.scrollTo({left:n.scrollWidth,behavior:'instant'});n.dispatchEvent(new Event('scroll'));})()`);
      const end=await evaluate(`({prev:document.querySelector('#sheetPrevBtn').disabled,next:document.querySelector('#sheetNextBtn').disabled,corner:getComputedStyle(document.querySelector('#grid'),'::-webkit-scrollbar-corner').backgroundColor,minimum:getComputedStyle(document.querySelector('#grid'),'::-webkit-scrollbar-thumb').minHeight})`);
      if(end.prev||!end.next||end.corner!==state.palette.inset||end.minimum!=='40px')failures.push({phase:name,preset,pagerEnd:end});
      for(const [selector,disabled]of [['#sheetPrevBtn',false],['#sheetNextBtn',true]]){const [rest]=await snapshot([selector]);await force(selector,['hover']);const [v]=await snapshot([selector]);if((v.background===rest.background)!==disabled)failures.push({phase:name,preset,selector,endHover:v.background});if((v.color===state.palette.ink)===disabled)failures.push({phase:name,preset,selector,glyph:v.color});await force(selector,[]);}
      await evaluate(`(()=>{const n=document.querySelector('#sheetTabs');n.scrollTo({left:0,behavior:'instant'});n.dispatchEvent(new Event('scroll'));})()`);
    }
    let scan;if(state.tone==='dark'){scan=await audit();if(scan.light.length||scan.low.length)failures.push({phase:name,preset,audit:scan});}
    let pixel;if(canvas){pixel=await evaluate(`(()=>{const n=document.querySelector(${JSON.stringify(canvas)}),p=n.getContext('2d').getImageData(1,1,1,1).data;return 'rgb('+Array.from(p).slice(0,3).join(', ')+')';})()`);if(pixel!==state.palette.surface)failures.push({phase:name,preset,canvasPixel:pixel,expected:state.palette.surface});}
    if(preset==='graphite'||preset==='warm')await capture(name+'-'+preset);
    cases.push({preset,tone:state.tone,controls:values,canvasPixel:pixel,audit:scan});
  }
  await apply('off');const restored=await snapshot(selectors);if(JSON.stringify(restored)!==JSON.stringify(original))failures.push({phase:name,restore:{original,restored}});
  phases.push({name,controls:selectors.length,cases});console.log(name+': '+selectors.length+' controls × 12 presets; findings '+failures.length);
}
async function closeDialog(id){await evaluate(`document.querySelector('#${id}').close()`);await settle();}
async function run(){
  try{
    let port;for(let i=0;i<150;i++){try{port=Number(fs.readFileSync(path.join(profile,'DevToolsActivePort'),'utf8').split('\n')[0]);break;}catch{}await pause(100);}assert(port,'Chrome did not start');
    const pages=await(await fetch('http://127.0.0.1:'+port+'/json')).json();socket=new WebSocket(pages.find(x=>x.type==='page').webSocketDebuggerUrl);await new Promise((ok,fail)=>{socket.addEventListener('open',ok,{once:true});socket.addEventListener('error',fail,{once:true});});
    socket.addEventListener('message',event=>{const r=JSON.parse(event.data);if(r.method==='Runtime.exceptionThrown')errors.push(r.params.exceptionDetails.exception?.description||r.params.exceptionDetails.text);const task=pending.get(r.id);if(task){clearTimeout(task.timer);pending.delete(r.id);r.error?task.reject(Error(r.error.message)):task.resolve(r.result);}});
    await cdp('Page.enable');await cdp('Runtime.enable');await cdp('DOM.enable');await cdp('CSS.enable');await cdp('Emulation.setDeviceMetricsOverride',{width:1900,height:1000,deviceScaleFactor:1,mobile:false});
    await cdp('Page.navigate',{url:address});await waitFor('document.readyState==="complete"&&!!window.ExcelLensSession');await evaluate('window.ExcelLensSession.ready');browser=(await cdp('Browser.getVersion')).product;
    for(const asset of ['style.css','themes.css','calculator.css','analytics.css','analytics.js','analytics-charts.js','readingmode.js','home.css','home-texture.svg']){const data=Buffer.from(await(await fetch(new URL('/assets/'+asset,address))).arrayBuffer());assert.equal(digest(data),digest(fs.readFileSync(path.join(root,'internal/ui/web',asset))),asset+' differs from production assets');assetDigests[asset]=digest(data);}
    if(baseline)await evaluate(`(()=>{document.querySelector('link[href="/assets/themes.css"]').disabled=true;const n=document.createElement('style');n.textContent=${JSON.stringify(fs.readFileSync(baseline,'utf8'))};document.head.append(n);})()`);
    if(homeOnly){
      presets=await evaluate('Array.from(document.querySelectorAll("input[name=readingPreset]"),n=>n.value)');
      for(const width of [1900,1000,760]){
        await cdp('Emulation.setDeviceMetricsOverride',{width,height:width===1900?1000:820,deviceScaleFactor:1,mobile:false});
        for(const preset of width===1900?presets:['off','warm','navy']){
          await apply(preset);
          const state=await evaluate(`(()=>{const n=document.querySelector('#chooseFileBtn'),r=n.getBoundingClientRect(),p=getComputedStyle(document.querySelector('#dropZone'),'::before');return {width:innerWidth,preset:document.documentElement.dataset.readingPreset||'off',overflow:document.documentElement.scrollWidth>innerWidth,hit:document.elementFromPoint(r.x+r.width/2,r.y+r.height/2)?.closest('button')?.id,mask:p.maskImage,opacity:p.opacity,animation:p.animationName};})()`);
          assert.equal(state.overflow,false);assert.equal(state.hit,'chooseFileBtn');assert(state.mask.includes('home-texture.svg'));assert.equal(state.animation,'none');homePreview.push(state);await capture('home-preview-'+width+'-'+preset);
        }
      }
      return;
    }
    const input=await cdp('Runtime.evaluate',{expression:'document.querySelector("#fileInput")'});await cdp('DOM.setFileInputFiles',{objectId:input.result.objectId,files:[file]});await waitFor('document.querySelector("#indexOverlay").hidden&&window.ExcelLensPerformance().hasVisibleCoverage&&document.querySelector(".grid-cell[data-ref=C2]")');
    presets=await evaluate('Array.from(document.querySelectorAll("input[name=readingPreset]"),n=>n.value)');assert.equal(presets.length,13);
    await evaluate(`(()=>{const actual=window.fetch;window.__themeReads=[];window.__themePending=0;window.fetch=(url,options)=>{const route=new URL(url,location.href).pathname;if(route.startsWith('/api/'))window.__themeReads.push(route);window.__themePending++;return actual(url,options).finally(()=>window.__themePending--);};})()`);
    const bottom=['.bottom-bar','.sheet-controls','.status-strip','#sheetPrevBtn','#sheetNextBtn','#sheetListBtn','.sheet-tab','.sheet-tab.has-tab-color','#calculatorDock','#calculatorHomeDock','#zoomOutBtn','#zoomInBtn','#readingModeBtn','#crosshairColorBtn','#zoomValue'];
    await evaluate(`(()=>{const n=document.querySelector('#sheetTabs');n.scrollTo({left:0,behavior:'instant'});n.dispatchEvent(new Event('scroll'));})()`);
    const pager=await evaluate('({prev:document.querySelector("#sheetPrevBtn").disabled,next:document.querySelector("#sheetNextBtn").disabled,left:document.querySelector("#sheetTabs").scrollLeft,width:document.querySelector("#sheetTabs").clientWidth,total:document.querySelector("#sheetTabs").scrollWidth})');
    assert(pager.prev&&!pager.next,'fixture must overflow tab strip: '+JSON.stringify(pager));
    await matrix('bottom',bottom,{inset:['.bottom-bar'],active:['#calculatorDock','#calculatorHomeDock'],bottom:true});
    if(baseline)return;
    await evaluate(`document.querySelector('#searchOpenBtn').click();document.querySelector('#searchInput').value='核查';document.querySelector('#searchBtn').click()`);await waitFor('document.querySelectorAll(".result-item").length>0&&document.querySelectorAll(".history-item").length>0');
    await evaluate(`document.querySelector('#searchMode').value='advanced';document.querySelector('#searchMode').dispatchEvent(new Event('change',{bubbles:true}));document.querySelector('#searchSyntaxHelp').click()`);
    await matrix('search',['#searchDialog','.result-item','.result-ref','.result-snippet','.history-panel','.history-item','.history-meta','.search-query-snippets button','.search-query-fields button','.search-advanced-help code'],{surface:['#searchDialog','.history-panel','.search-query-snippets button','.search-query-fields button','.search-advanced-help code']});await closeDialog('searchDialog');
    await evaluate(`document.querySelector('#inspectOpenBtn').click();document.querySelector('#inspectMode').value='constants';document.querySelector('#inspectMode').dispatchEvent(new Event('change',{bubbles:true}));document.querySelector('#inspectScope').value='used';document.querySelector('#inspectRun').click()`);await waitFor('document.querySelectorAll(".inspect-hit").length>0');
    await matrix('inspector',['.inspect-panel','.inspect-titlebar','.inspect-list-header','#inspectPrev','#inspectNext','.inspect-hit','.inspect-hit strong','.inspect-more'],{surface:['.inspect-panel','#inspectPrev','#inspectNext'],inset:['.inspect-list-header','.inspect-more']});await evaluate(`document.querySelector('#inspectClose').click()`);
    await evaluate(`document.querySelector('#exportOpenBtn').click();document.querySelector('#exportStartBtn').click()`);await waitFor('!document.querySelector("#exportResult").hidden');
    await matrix('export',['#exportDialog','.export-options','.export-range-value','.export-progress','.export-result','.export-success','.export-download'],{inset:['.export-range-value','.export-progress']});await closeDialog('exportDialog');
    await evaluate(`document.querySelector('#profileOpenBtn').click();document.querySelector('#profileStart').value='A1';document.querySelector('#profileEnd').value='D6';document.querySelector('#profileAnalyzeBtn').click()`);await waitFor('!document.querySelector("#profileSummary").hidden&&!!document.querySelector(".profile-ref")');
    await matrix('statistics',['#profileDialog','.profile-stat','.profile-number','.profile-caption','.profile-detail','.profile-detail h4','.profile-data-line code','.profile-ref'],{inset:['.profile-stat'],surface:['.profile-ref']});await closeDialog('profileDialog');
    await evaluate(`document.querySelector('#bookmarkOpenBtn').click();document.querySelector('#bookmarkNote').value='主题自动核查';document.querySelector('#bookmarkSaveBtn').click()`);await waitFor('!!document.querySelector(".bookmark-tag")');
    await matrix('bookmarks',['#bookmarkDialog','.bookmark-target','.bookmark-head','.bookmark-tag','.bookmark-desc'],{inset:['.bookmark-target']});await closeDialog('bookmarkDialog');
    await evaluate(`document.querySelector('#commandOpenBtn').click()`);await waitFor('!!document.querySelector(".command-entry")');
    await matrix('commands',['#commandDialog','.command-entry','.command-entry:not(.active)','.command-shortcut'],{surface:['#commandDialog']});await closeDialog('commandDialog');
    await evaluate(`document.querySelector('#calculatorOpenBtn').click();document.querySelector('#calculatorNotes').click()`);await waitFor('!!document.querySelector(".calculator-note")');
    await evaluate(`document.querySelector('.calculator-note').focus()`);
    await matrix('calculator',['#calculatorWindow','.calculator-head','.calculator-columns','.calculator-row-number','.calculator-formula','.calculator-note','.calculator-value','#calculatorNotes'],{surface:['#calculatorWindow'],inset:['.calculator-head','.calculator-columns']});
    await evaluate(`document.querySelector('#calculatorMinimize').click()`);await waitFor('!document.querySelector("#calculatorDock").hidden');
    await matrix('calculator-dock',['#calculatorDock','#zoomOutBtn','#zoomInBtn'],{active:['#calculatorDock']});
    await evaluate(`document.querySelector('#analysisOpenBtn').click()`);await waitFor('!document.querySelector("#analysisRoot").hidden&&!!document.querySelector(".analysis-field-chip")');await evaluate(`document.querySelector('.analysis-field-chip[data-field="C1"]').click();document.querySelector('#analysisRun').click()`);await waitFor('!!document.querySelector("#analysisPivotTable td")');
    await matrix('analysis-chart',['.analysis-topbar','.analysis-builder','.analysis-chart-card','.analysis-data-table th','.analysis-data-table td','.analysis-symbol-button','.analysis-quiet-button'],{surface:['.analysis-topbar','.analysis-builder','.analysis-chart-card','.analysis-symbol-button'],inset:['.analysis-data-table th'],canvas:'#analysisChart'});
    await evaluate(`document.querySelector('#analysisSaveReport').click()`);await waitFor('!!document.querySelector(".analysis-modal")');
    await matrix('analysis-dialog',['.analysis-modal','.analysis-modal h3','.analysis-modal input','.analysis-modal .analysis-outline-button'],{surface:['.analysis-modal','.analysis-modal input','.analysis-modal .analysis-outline-button']});
    await evaluate(`document.querySelector('.analysis-modal input').value='主题核查报表';document.querySelector('.analysis-modal [data-yes]').click()`);await waitFor('!document.querySelector(".analysis-modal")&&document.querySelector("#analysisSaveReport").textContent==="更新报表"&&window.__themePending===0');await evaluate(`document.querySelector('[data-analysis-tab="dashboard"]').click()`);await waitFor('!!document.querySelector(".analysis-dashboard-card canvas")&&!document.querySelector(".analysis-card-loading")');
    await matrix('dashboard',['.analysis-dashboard-card','.analysis-dashboard-card-head','.analysis-dashboard-card-head button'],{surface:['.analysis-dashboard-card'],canvas:'.analysis-dashboard-card canvas'});
    await evaluate(`document.querySelector('[data-analysis-tab="pivot"]').click();document.querySelector('#analysisAddFilter').click();document.querySelector('#analysisAddConversion').click();document.querySelector('#analysisAddMetric').click()`);
    await matrix('analysis-rules',['.analysis-rule','.analysis-rule-remove','.analysis-metric-row button','.analysis-field-chip','.analysis-field-info-btn','.analysis-rule input','.analysis-rule select'],{inset:['.analysis-rule','.analysis-field-info-btn']});
    await evaluate(`document.querySelector('[data-analysis-tab="sheets"]').click();document.querySelector('#analysisCreateSheet').click()`);await waitFor('!!document.querySelector(".analysis-modal")');await evaluate(`document.querySelector('.analysis-modal input').value='主题核查编辑表';document.querySelector('.analysis-modal [data-yes]').click()`);await waitFor('!!document.querySelector(".analysis-edit-grid textarea")');await evaluate(`document.querySelector('.analysis-edit-grid textarea').focus()`);
    await matrix('analysis-sheet',['.analysis-sheet-list','.analysis-sheet-list button','.analysis-sheet-tools strong','.analysis-edit-grid th','.analysis-edit-grid td textarea','.analysis-pagination'],{inset:['.analysis-sheet-list','.analysis-edit-grid th']});
    await evaluate(`document.querySelector('[data-analysis-tab="templates"]').click()`);await waitFor('!!document.querySelector(".analysis-template-card")');
    await matrix('templates',['.analysis-template-card','.analysis-template-card strong','.analysis-template-card small','.analysis-category-list button'],{surface:['.analysis-template-card']});
    await evaluate(`document.querySelector('#analysisClose').click();document.querySelector('#homeBtn').click()`);await waitFor('document.body.classList.contains("home-view")&&!!document.querySelector(".recent-item")');
    await matrix('home',['.appbar','.landing-shell','.landing-open-zone','.landing-content h1','.landing-subline','.landing-open-button','.recent-item','.recent-name','.recent-meta','.recent-file-icon','.landing-footer','#calculatorHomeDock'],{surface:['.appbar','.landing-shell'],inset:['.landing-open-zone','.landing-footer'],active:['#calculatorHomeDock']});
    for(const preset of ['off','warm','green','graphite','navy']){await apply(preset);await capture('home-texture-'+preset);}
    await apply('off');
    assert.equal(digest(fs.readFileSync(file)),sourceSHA256,'source XLSX changed');assert.deepEqual(errors,[]);
  }catch(error){failures.push({error:error.stack||String(error)});
  }finally{
    if(socket?.readyState===1){try{await cdp('Browser.close');}catch{}socket.close();}
    if(child.exitCode===null&&child.signalCode===null)await Promise.race([new Promise(r=>child.once('exit',r)),pause(1500)]);if(child.exitCode===null&&child.signalCode===null)child.kill();
    // Keep the task-owned profile beside the ignored private diagnostics.
    profileRemoved=!fs.existsSync(profile);
  }
}
run().catch(error=>{failures.push({error:error.stack||String(error)});}).then(()=>{
  const report={version,result:failures.length?'FAIL':'PASS',browser,baseline:Boolean(baseline),desktopManualAcceptance:false,presets:presets?.filter(x=>x!=='off'),sourceSHA256,sourceUnchanged:digest(fs.readFileSync(file))===sourceSHA256,assetDigests,profileRemoved,profileRetention:'Private profile retained with diagnostic files',errors,failures,phases,homePreview};
  fs.writeFileSync(path.join(output,baseline?'baseline.json':homeOnly?'home-preview.json':'result.json'),JSON.stringify(report,null,2)+'\n');
  if(!baseline)fs.writeFileSync(path.join(receipts,homeOnly?'home-texture.json':'reading-themes.json'),JSON.stringify({...report,phases:phases.map(({name,controls,cases})=>({name,controls,presets:cases.length})),failures:failures.slice(0,30)},null,2)+'\n');
  console.log(JSON.stringify({result:report.result,phases:phases.length,failures:failures.slice(0,8),profileRemoved},null,2));if(failures.length)process.exitCode=1;
});
