// Optional visual and boundary checks against an isolated production service.
// All inputs are synthetic; no desktop browser or user workbook is opened.
const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict');
const {spawn}=require('node:child_process'),{createHash}=require('node:crypto');
const root=path.resolve(__dirname,'../..'),version=require('./project_config.cjs').version();
const out=path.join(root,'validation',version,'private','background-details');fs.mkdirSync(out,{recursive:true});
const exe=process.argv[2];if(!exe)throw Error('Usage: node tools/qa/background_details_qa.cjs QA_SERVER_EXE');
const pause=ms=>new Promise(r=>setTimeout(r,ms)),digest=b=>createHash('sha256').update(b).digest('hex');
const csv=path.join(out,'background.csv');
fs.writeFileSync(csv,'项目,分组,金额,比例,日期,编号,备注\n'+Array.from({length:14},(_,i)=>`核查条目${i+1},${i%2?'保留':'其他'},${i+1}.25,25%,2026-11-14,000${i+1},源值只读`).join('\n')+'\n');
const inputs=new Map([[csv,digest(fs.readFileSync(csv))]]);
const profile=fs.mkdtempSync(path.join(out,'chrome-'));
const browser=spawn(process.env.QA_CHROME||'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe',[
  '--headless=new','--no-first-run','--no-default-browser-check','--disable-extensions','--disable-component-update',
  '--disable-sync','--disable-background-networking','--remote-debugging-port=0','--user-data-dir='+profile,'about:blank'
],{windowsHide:true,stdio:'ignore'});
let socket,server,seq=0;const pending=new Map(),requests=[];
const report={version,result:'FAIL',desktopManualAcceptance:false,home:[],grid:[],checks:[],errors:[],assetDigests:{}};
function cdp(method,params={}){return new Promise((resolve,reject)=>{const id=++seq,timer=setTimeout(()=>{pending.delete(id);reject(Error(method+' timeout'));},20000);pending.set(id,{resolve,reject,timer});socket.send(JSON.stringify({id,method,params}));});}
async function evaluate(expression){const r=await cdp('Runtime.evaluate',{expression,returnByValue:true,awaitPromise:true});if(r.exceptionDetails)throw Error(JSON.stringify(r.exceptionDetails));return r.result.value;}
async function waitFor(expression){for(let i=0;i<250;i++){if(await evaluate(expression))return;await pause(80);}throw Error('Timeout: '+expression);}
async function settle(){await evaluate('new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)))');}
async function apply(preset){await evaluate(`document.querySelector('input[name="readingPreset"][value="${preset}"]').click()`);await settle();}
async function click(selector){const pos=await evaluate(`(()=>{const r=document.querySelector(${JSON.stringify(selector)}).getBoundingClientRect();return {x:r.x+r.width/2,y:r.y+r.height/2};})()`);for(const type of ['mouseMoved','mousePressed','mouseReleased'])await cdp('Input.dispatchMouseEvent',{type,...pos,...(type==='mouseMoved'?{}:{button:'left',clickCount:1})});}
async function key(key,code,vk){await cdp('Input.dispatchKeyEvent',{type:'keyDown',key,code,windowsVirtualKeyCode:vk});await cdp('Input.dispatchKeyEvent',{type:'keyUp',key,code,windowsVirtualKeyCode:vk});}
async function capture(name,selector){const clip=selector?await evaluate(`(()=>{const r=document.querySelector(${JSON.stringify(selector)}).getBoundingClientRect();return {x:r.x,y:r.y,width:r.width,height:r.height,scale:2};})()`):null;const r=await cdp('Page.captureScreenshot',{format:'png',captureBeyondViewport:false,...(clip?{clip}:{})});fs.writeFileSync(path.join(out,name+'.png'),Buffer.from(r.data,'base64'));}
async function upload(file){const r=await cdp('Runtime.evaluate',{expression:'document.querySelector("#fileInput")'});await cdp('DOM.setFileInputFiles',{objectId:r.result.objectId,files:[file]});await waitFor('document.querySelector("#fileBadge").textContent==='+JSON.stringify(path.basename(file))+'&&!document.querySelector("#workspace").hidden&&document.querySelector("#indexOverlay").hidden&&window.ExcelLensPerformance().hasVisibleCoverage&&!window.ExcelLensPerformance().sheetLoading');await settle();}
async function boundary(ref,{filtered=false}={}){
  const m=await evaluate(`(${function(ref,filtered){
    const grid=document.querySelector('#grid'),layer=document.querySelector('#gridBackdrop'),s=getComputedStyle(layer);
    const g=grid.getBoundingClientRect(),r=layer.getBoundingClientRect(),cell=document.querySelector('.grid-cell[data-ref="'+ref+'"]'),c=cell?.getBoundingClientRect();
    const right=parseFloat(s.getPropertyValue('--backdrop-data-right')),bottom=parseFloat(s.getPropertyValue('--backdrop-data-bottom'));
    return {ref,filtered,hidden:layer.hidden,width:r.width,height:r.height,viewportWidth:grid.clientWidth,viewportHeight:grid.clientHeight,right,bottom,cellRight:c?.right-g.x,cellBottom:c?.bottom-g.y,pointer:s.pointerEvents,mask:s.maskImage,clip:s.clipPath,position:s.position,background:s.backgroundColor};
  }.toString()})(${JSON.stringify(ref)},${filtered})`);
  assert.equal(m.hidden,false);assert.equal(m.width,m.viewportWidth);assert.equal(m.height,m.viewportHeight);assert.equal(m.pointer,'none');assert.equal(m.position,'absolute');assert(m.mask.includes('linear-gradient'));
  if(Number.isFinite(m.cellRight)&&m.cellRight>=-96&&m.cellRight<=m.viewportWidth)assert(Math.abs(m.right-m.cellRight)<1,'right boundary mismatch '+JSON.stringify(m));
  if(Number.isFinite(m.cellBottom)&&m.cellBottom>=-80&&m.cellBottom<=m.viewportHeight)assert(Math.abs(m.bottom-m.cellBottom)<1,'bottom boundary mismatch '+JSON.stringify(m));
  return m;
}
function zip(parts){
  const table=Array.from({length:256},(_,n)=>{for(let i=0;i<8;i++)n=n&1?0xedb88320^(n>>>1):n>>>1;return n>>>0;}),chunks=[],dir=[];let offset=0;
  for(const [name,value]of Object.entries(parts)){const data=Buffer.from(value),file=Buffer.from(name);let crc=0xffffffff;for(const b of data)crc=table[(crc^b)&255]^(crc>>>8);crc=(crc^0xffffffff)>>>0;
    const h=Buffer.alloc(30);h.writeUInt32LE(0x04034b50);h.writeUInt16LE(20,4);h.writeUInt32LE(crc,14);h.writeUInt32LE(data.length,18);h.writeUInt32LE(data.length,22);h.writeUInt16LE(file.length,26);chunks.push(h,file,data);
    const e=Buffer.alloc(46);e.writeUInt32LE(0x02014b50);e.writeUInt16LE(20,4);e.writeUInt16LE(20,6);e.writeUInt32LE(crc,16);e.writeUInt32LE(data.length,20);e.writeUInt32LE(data.length,24);e.writeUInt16LE(file.length,28);e.writeUInt32LE(offset,42);dir.push(e,file);offset+=h.length+file.length+data.length;
  }const central=Buffer.concat(dir),end=Buffer.alloc(22);end.writeUInt32LE(0x06054b50);end.writeUInt16LE(Object.keys(parts).length,8);end.writeUInt16LE(Object.keys(parts).length,10);end.writeUInt32LE(central.length,12);end.writeUInt32LE(offset,16);return Buffer.concat([...chunks,central,end]);
}
async function start(){
  server=spawn(path.resolve(exe),[],{cwd:root,windowsHide:true,env:{...process.env,LOCALAPPDATA:path.join(profile,'server-storage','cache'),APPDATA:path.join(profile,'server-storage','config')},stdio:['ignore','pipe','pipe']});server.stderr.on('data',b=>fs.appendFileSync(path.join(out,'server.log'),b));
  return new Promise((resolve,reject)=>{let output='';const timer=setTimeout(()=>reject(Error('Service timeout')),10000);server.stdout.on('data',b=>{output+=b;const address=output.match(/http:\/\/127\.0\.0\.1:\d+\//)?.[0];if(address){clearTimeout(timer);resolve(address);}});server.once('exit',code=>{clearTimeout(timer);reject(Error('Service exited: '+code));});});
}
(async()=>{
  try{
    let port;for(let i=0;i<150;i++){try{port=Number(fs.readFileSync(path.join(profile,'DevToolsActivePort'),'utf8').split('\n')[0]);break;}catch{}await pause(100);}assert(port,'Headless Chrome did not start');
    const pages=await(await fetch('http://127.0.0.1:'+port+'/json')).json();socket=new WebSocket(pages.find(p=>p.type==='page').webSocketDebuggerUrl);await new Promise((ok,fail)=>{socket.addEventListener('open',ok,{once:true});socket.addEventListener('error',fail,{once:true});});
    socket.addEventListener('message',event=>{const r=JSON.parse(event.data);if(r.method==='Runtime.exceptionThrown')report.errors.push(r.params.exceptionDetails.exception?.description||r.params.exceptionDetails.text);if(r.method==='Network.requestWillBeSent')requests.push(new URL(r.params.request.url).pathname);const task=pending.get(r.id);if(task){clearTimeout(task.timer);pending.delete(r.id);r.error?task.reject(Error(r.error.message)):task.resolve(r.result);}});
    await cdp('Page.enable');await cdp('Runtime.enable');await cdp('Network.enable');report.browser=(await cdp('Browser.getVersion')).product;
    const address=await start();await cdp('Page.navigate',{url:address});await waitFor('document.readyState==="complete"&&!!window.ExcelLensSession');await evaluate('window.ExcelLensSession.ready');
    assert.equal(await evaluate('document.querySelector("meta[name=app-version]").content'),version);
    for(const name of ['home.css','home-texture.svg','home-opening-texture.svg','themes.css','readingmode.js','app.js','geometry.js','formats.js','merges.js','inspect.js','analytics.js','multisort.js','autofilter.js']){const data=Buffer.from(await(await fetch(new URL('/assets/'+name,address))).arrayBuffer());assert.equal(digest(data),digest(fs.readFileSync(path.join(root,'internal/ui/web',name))));report.assetDigests[name]=digest(data);}
    assert.equal(await evaluate('["sortHeaderRow","textHeaderRow","analysisHeaderRow"].every(id=>document.getElementById(id).max==="1000000000")'),true);
    for(const width of [1900,1000,760,390])for(const preset of ['off','warm','graphite']){
      await cdp('Emulation.setDeviceMetricsOverride',{width,height:1000,deviceScaleFactor:1,mobile:false});await apply(preset);
      const m=await evaluate(`(()=>{const zone=document.querySelector('.landing-open-zone'),s=getComputedStyle(zone,'::before'),b=document.querySelector('#chooseFileBtn').getBoundingClientRect(),art=zone.querySelector('.landing-grid-art');return {size:s.maskSize,mask:s.maskImage,animation:s.animationName,overflow:document.documentElement.scrollWidth>innerWidth,hit:document.elementFromPoint(b.x+b.width/2,b.y+b.height/2)?.closest('button')?.id,rows:[...art.children].map(n=>n.children.length),cellWidth:getComputedStyle(art.querySelector('span')).width,cellHeight:getComputedStyle(art.querySelector('span')).height,artTransform:getComputedStyle(art).transform,artPointer:getComputedStyle(art).pointerEvents};})()`);
      if(m.overflow)await capture('home-overflow-'+width);
      assert.equal(m.size,'900px 810px, 100% 100%');assert(m.mask.includes('home-opening-texture.svg'));assert.equal(m.hit,'chooseFileBtn');assert.equal(m.overflow,false,'home overflow '+JSON.stringify({width,preset,...m}));assert.equal(m.animation,'none');report.home.push({width,preset,...m});
      assert.deepEqual(m.rows,[8,6]);assert.equal(m.cellWidth,'54.375px');assert.equal(m.cellHeight,'70px');assert.equal(m.artPointer,'none');assert.notEqual(m.artTransform,'none');
      if(width===1900)await capture('home-'+preset,'.landing-open-zone');
      if(width!==1900&&preset==='off')await capture('home-layout-'+width);
    }
    const svg=fs.readFileSync(path.join(root,'internal/ui/web/home-texture.svg'),'utf8');
    const ids=[...svg.matchAll(/<g id="([^"]+)"/g)].map(m=>m[1]),uses=[...svg.matchAll(/<use href="#([^"]+)" transform="([^"]+)"/g)];
    assert.equal(ids.length,28);assert.equal(uses.length,88);assert(uses.every(m=>ids.includes(m[1])&&m[2].includes('scale')));assert(svg.includes('width="840" height="720"'));assert(svg.includes('stroke-width="1.15"'));
    const parsed=await evaluate(`(()=>{const d=new DOMParser().parseFromString(${JSON.stringify(svg)},'image/svg+xml');return {error:!!d.querySelector('parsererror'),looseMarks:[...d.documentElement.children].filter(n=>!['desc','defs','use'].includes(n.localName)).length};})()`);assert.equal(parsed.error,false);assert.equal(parsed.looseMarks,0);
    const placements=uses.map(m=>m[2].match(/^translate\(([-\d.]+) ([-\d.]+)\) rotate\(([-\d.]+) 22 22\) translate\(22 22\) scale\(([-\d.]+)\) translate\(-22 -22\)$/));assert(placements.every(Boolean));
    const uniqueY=new Set(placements.map(m=>m[2])).size,angles=new Set(placements.map(m=>m[3])).size;assert(uniqueY>70);assert(angles>30);
    report.homeMotifs={tile:[840,720],unique:ids.length,beforePerFullTile:56,afterPerFullTile:uses.length,placement:'irregular',uniqueY,angles,scaleRange:[Math.min(...placements.map(m=>Number(m[4]))),Math.max(...placements.map(m=>Number(m[4])))],looseMarks:parsed.looseMarks};
    const opening=fs.readFileSync(path.join(root,'internal/ui/web/home-opening-texture.svg'),'utf8'),openingUses=[...opening.matchAll(/<use href="#([^"]+)" transform="translate\(([-\d.]+) ([-\d.]+)\) rotate\(([-\d.]+)\) scale\(([-\d.]+)\) translate\(-22 -22\)"/g)];assert.equal(openingUses.length,200);assert(openingUses.every(m=>ids.includes(m[1])));
    assert.equal(await evaluate(`!!new DOMParser().parseFromString(${JSON.stringify(opening)},'image/svg+xml').querySelector('parsererror')`),false);
    const openingPoints=openingUses.map(m=>({x:Number(m[2]),y:Number(m[3]),scale:Number(m[5]),distance:Math.hypot((Number(m[2])-800)*0.7,Number(m[3])-95)}));
    const bands=[{name:'near',min:0,max:280},{name:'middle',min:280,max:540},{name:'far',min:540,max:Infinity}];
    for(const b of bands){b.count=openingPoints.filter(p=>p.distance>=b.min&&p.distance<b.max).length;b.small=openingPoints.filter(p=>p.distance>=b.min&&p.distance<b.max&&p.scale<0.7).length;b.area=0;for(let y=2.5;y<900;y+=5)for(let x=2.5;x<1000;x+=5){const d=Math.hypot((x-800)*0.7,y-95);if(d>=b.min&&d<b.max)b.area+=25;}b.per100k=b.count/b.area*100000;}
    assert(bands[0].per100k<bands[1].per100k&&bands[1].per100k<bands[2].per100k);assert.equal(bands[0].small,0);assert.equal(openingPoints.filter(p=>p.scale<0.7).length,90);
    report.openingMotifs={tile:[1000,900],count:200,primary:110,small:90,scaleRange:[Math.min(...openingPoints.map(p=>p.scale)),Math.max(...openingPoints.map(p=>p.scale))],bands};
    await cdp('Emulation.setDeviceMetricsOverride',{width:1900,height:1000,deviceScaleFactor:1,mobile:false});await apply('off');await upload(csv);await waitFor('!!document.querySelector(".grid-cell[data-ref=G15]")');
    assert.equal(await evaluate('document.querySelector("#gridBackdrop").hidden'),true);assert.equal(await evaluate('document.querySelector("#gridTextureToggle").checked'),false);assert.equal(await evaluate('localStorage.getItem("ExcelLens.gridTexture")'),null);
    await capture('grid-default-off');await click('#readingModeBtn');await capture('reading-settings-default-off','#readingModePanel');
    const sourceReads=()=>requests.filter(p=>/\/api\/(sheet|layout|index|preview)/.test(p)).length;
    await pause(200);const toggleReads=sourceReads(),selection=await evaluate('document.querySelector("#nameBox").value');
    await click('#gridTextureToggle');assert.equal(await evaluate('document.querySelector("#gridBackdrop").hidden'),false);
    await key(' ','Space',32);assert.equal(await evaluate('document.querySelector("#gridBackdrop").hidden'),true);
    await key(' ','Space',32);assert.equal(await evaluate('document.querySelector("#gridBackdrop").hidden'),false);
    assert.equal(await evaluate('document.activeElement.id'),'gridTextureToggle');assert.equal(await evaluate('getComputedStyle(document.querySelector("#gridTextureToggle")).outlineStyle'),'solid');
    assert.equal(sourceReads(),toggleReads);assert.equal(await evaluate('document.querySelector("#nameBox").value'),selection);assert.equal(await evaluate('localStorage.getItem("ExcelLens.gridTexture")'),'on');
    await capture('reading-settings-on','#readingModePanel');await key('Escape','Escape',27);assert.equal(await evaluate('document.querySelector("#readingModePanel").hidden'),true);
    report.checks.push('worksheet texture defaults off; mouse and Space toggle without source reads or selection changes; keyboard focus and Escape');
    await cdp('Page.reload',{ignoreCache:true});await pause(300);await waitFor('document.readyState==="complete"&&!!window.ExcelLensSession');await evaluate('window.ExcelLensSession.ready');
    assert.equal(await evaluate('document.querySelector("#gridTextureToggle").checked'),true);await waitFor('window.ExcelLensPerformance().hasVisibleCoverage&&!window.ExcelLensPerformance().sheetLoading&&!!document.querySelector(".grid-cell[data-ref=G15]")');await settle();
    assert.equal(await evaluate('document.querySelector("#gridBackdrop").hidden'),false);report.checks.push('enabled texture preference survives page reload and file opening');
    const presets=await evaluate('[...document.querySelectorAll("input[name=readingPreset]")].map(n=>n.value)');
    for(const preset of presets){await apply(preset);assert.equal(await evaluate('document.querySelector("#gridTextureToggle").checked'),true);report.grid.push({width:1900,dpr:1,preset,...await boundary('G15')});if(['off','warm','graphite'].includes(preset)){await capture('grid-'+preset);await capture('checkbox-'+preset,'.crosshair-controls');await click('#readingModeBtn');await capture('reading-settings-'+preset,'#readingModePanel');await key('Escape','Escape',27);}}
    for(const width of [1000,760])for(const dpr of [1.25,2])for(const preset of ['off','warm','graphite']){
      await cdp('Emulation.setDeviceMetricsOverride',{width,height:1000,deviceScaleFactor:dpr,mobile:false});await apply(preset);report.grid.push({width,dpr,preset,...await boundary('G15')});
    }
    await cdp('Emulation.setDeviceMetricsOverride',{width:1900,height:1000,deviceScaleFactor:1,mobile:false});await apply('off');
    const reads=()=>requests.filter(p=>/\/api\/(sheet|layout|index|preview)/.test(p)).length;
    await pause(200);const before=reads();await click('.grid-cell[data-ref="I18"]');assert.equal(await evaluate('document.querySelector("#nameBox").value'),'I18');
    await click('#crosshairToggle');assert.equal(await evaluate('document.querySelector("#crosshairToggle").checked'),false);await key(' ','Space',32);assert.equal(await evaluate('document.querySelector("#crosshairToggle").checked'),true);
    const focus=await evaluate('(()=>{const n=document.querySelector("#crosshairToggle"),s=getComputedStyle(n);return {id:document.activeElement.id,outline:s.outlineStyle,appearance:s.appearance,tick:getComputedStyle(n,"::before").opacity,width:n.getBoundingClientRect().width};})()');assert.equal(focus.id,'crosshairToggle');assert.equal(focus.outline,'solid');assert.equal(focus.appearance,'none');assert.equal(focus.tick,'1');assert.equal(focus.width,10);
    assert.equal(await evaluate('document.querySelector("#nameBox").value'),'I18');assert.equal(reads(),before);report.checks.push('empty-cell pointer pass-through; mouse and Space toggle without source reads or selection changes');
    await click('#crosshairColorBtn');await click('input[name="crosshairColor"][value="purple"]');await key('Escape','Escape',27);assert.equal(await evaluate('document.querySelector("#crosshairColorBtn").getAttribute("aria-expanded")'),'false');
    await cdp('Emulation.setEmulatedMedia',{features:[{name:'forced-colors',value:'active'}]});assert.equal(await evaluate('getComputedStyle(document.querySelector("#crosshairToggle")).appearance'),'auto');await capture('checkbox-forced-colors','.crosshair-controls');await cdp('Emulation.setEmulatedMedia',{features:[]});
    await click('.grid-cell[data-ref="A1"]');await evaluate('for(let i=0;i<5;i++)document.querySelector("#zoomInBtn").click()');await waitFor('window.ExcelLensPerformance().hasVisibleCoverage&&!window.ExcelLensPerformance().hasPendingRequest&&!!document.querySelector(".grid-cell[data-ref=G15]")');await settle();assert.equal(await evaluate('document.querySelector("#zoomValue").textContent'),'125%');report.zoom125=await boundary('G15');await capture('grid-zoom125');
    await click('#freezeBtn');await evaluate('document.querySelector("#freezeRowsInput").value=1;document.querySelector("#freezeColsInput").value=2');await click('#freezeApplyBtn');await waitFor('!document.querySelector("#freezeTop").hidden');
    await evaluate('document.querySelector("#grid").scrollTop=120;document.querySelector("#grid").scrollLeft=70');await settle();report.frozenScrolled=await boundary('G15');
    report.frozenTexture=await evaluate(`(()=>{const n=document.querySelector('#gridBackdrop'),s=getComputedStyle(n),p=getComputedStyle(n,'::before');return {layers:document.querySelectorAll('.grid-backdrop').length,z:Number(s.zIndex),paneZ:['freezeTop','freezeLeft','freezeCorner'].map(id=>Number(getComputedStyle(document.getElementById(id)).zIndex)),pattern:p.maskImage,size:p.maskSize,phase:p.maskPosition,masks:s.maskImage};})()`);
    assert.equal(report.frozenTexture.layers,1);assert(report.frozenTexture.paneZ.every(z=>z<report.frozenTexture.z));assert(report.frozenTexture.pattern.includes('home-texture.svg'));assert.equal(report.frozenTexture.size,'504px 432px');assert.equal(report.frozenTexture.phase,'19px 11px');
    await capture('grid-frozen-scrolled');await capture('frozen-top-texture','#freezeTop');await capture('frozen-left-texture','#freezeLeft');
    const frozenReads=sourceReads();await click('#readingModeBtn');await click('#gridTextureToggle');assert.equal(await evaluate('document.querySelector("#gridBackdrop").hidden'),true);await capture('grid-frozen-off');
    await click('#gridTextureToggle');await key('Escape','Escape',27);assert.equal(sourceReads(),frozenReads);await boundary('G15');report.checks.push('frozen and normal blank areas switch together without source reads; one shared texture phase');
    await click('#advancedFilterBtn');await evaluate('document.querySelector(".filter-column").value="B";document.querySelector(".filter-operator").value="equals";document.querySelector(".filter-value").value="保留"');await click('#applyFilterBtn');await waitFor('!document.querySelector("#filterStatus").hidden&&!document.querySelector("#applyFilterBtn").disabled');await click('#filterDialog .dialog-close');await settle();
    report.filtered=await boundary('G15',{filtered:true});assert(report.filtered.bottom<report.zoom125.bottom);await capture('grid-filtered');await click('#clearFilterBtn');await waitFor('document.querySelector("#filterStatus").hidden');
    await click('#nameBox');await evaluate('document.querySelector("#nameBox").value="G1000000000"');await key('Enter','Enter',13);await waitFor('document.querySelector(".grid-cell.selected")?.dataset.ref==="G1000000000"&&window.ExcelLensPerformance().hasVisibleCoverage');
    report.billionCoordinates=await boundary(null);report.billionCoordinates.performance=await evaluate('window.ExcelLensPerformance()');assert(report.billionCoordinates.performance.visibleNodes<10000);assert.equal(await evaluate('getComputedStyle(document.querySelector("#gridCanvas")).getPropertyValue("--row-header-width")'),'72px');await capture('grid-billion-coordinates');
    await evaluate('document.querySelector("#nameBox").value="G1000000001"');await key('Enter','Enter',13);assert.equal(await evaluate('document.querySelector(".grid-cell.selected").dataset.ref'),'G1000000000');
    // One sparse cell at Excel's final row exercises compressed logical scroll.
    const ns='http://schemas.openxmlformats.org/spreadsheetml/2006/main',tall=path.join(out,'sparse-tall.xlsx');
    fs.writeFileSync(tall,zip({'[Content_Types].xml':'<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/></Types>',
      '_rels/.rels':'<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="w" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>',
      'xl/workbook.xml':`<workbook xmlns="${ns}" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="稀疏高表" sheetId="1" r:id="s"/></sheets></workbook>`,
      'xl/_rels/workbook.xml.rels':'<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="s" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>',
      'xl/worksheets/sheet1.xml':`<worksheet xmlns="${ns}"><dimension ref="A1:G1048576"/><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>起点</t></is></c></row><row r="1048576"><c r="G1048576" t="inlineStr"><is><t>边界</t></is></c></row></sheetData></worksheet>`}));inputs.set(tall,digest(fs.readFileSync(tall)));
    await upload(tall);await click('#nameBox');await evaluate('document.querySelector("#nameBox").value="G1048576"');await key('Enter','Enter',13);await waitFor('document.querySelector(".grid-cell.selected")?.dataset.ref==="G1048576"');await settle();report.tall=await boundary('G1048576');report.tall.performance=await evaluate('window.ExcelLensPerformance()');await capture('grid-tall');
    if(process.env.QA_LARGE_SOURCE){
      const file=path.resolve(process.env.QA_LARGE_SOURCE);await upload(file);await waitFor('!document.querySelector("#largeDataStatus").hidden');
      assert.equal(await evaluate('document.querySelector("#multiSortBtn").disabled'),true);assert.equal(await evaluate('document.querySelector("#autoFilterSortAsc").disabled'),true);
      const sheet=await evaluate('document.querySelector(".sheet-tab.active").textContent.trim()');
      async function guard(url,method='GET',body){return evaluate(`(async()=>{const h=new Headers({'X-ExcelLens-Token':document.querySelector('meta[name="app-token"]').content});window.ExcelLensSession.apply(h);if(${JSON.stringify(method)}==='POST')h.set('Content-Type','application/json');const start=performance.now();const r=await fetch(${JSON.stringify(url)},{method:${JSON.stringify(method)},headers:h,${body?'body:'+JSON.stringify(JSON.stringify(body))+',':''}cache:'no-store'});return {status:r.status,body:await r.text(),ms:performance.now()-start};})()`);}
      report.largeMode={sheet,sort:await guard('/api/filter/start','POST',{sheet,sortColumn:1,headerRow:1,rules:[]}),navigate:await guard('/api/navigate?sheet='+encodeURIComponent(sheet)+'&ref=A2&direction=down'),duplicates:await guard('/api/inspect','POST',{sheet,mode:'duplicates',start:'A1',end:'A1000000000',limit:10}),profile:await guard('/api/profile','POST',{sheet,start:'A1',end:'A1000000000'}),cache:await guard('/api/cache/clear','POST',{})};
      for(const name of ['sort','navigate','duplicates','profile'])assert.equal(report.largeMode[name].status,400,name+' guard');assert.equal(report.largeMode.cache.status,409);
      await click('#grid');await cdp('Input.dispatchKeyEvent',{type:'keyDown',key:'End',code:'End',windowsVirtualKeyCode:35,modifiers:2});await cdp('Input.dispatchKeyEvent',{type:'keyUp',key:'End',code:'End',windowsVirtualKeyCode:35,modifiers:2});
      await waitFor('document.querySelector(".grid-cell.selected")?.dataset.ref==="C5000002"&&window.ExcelLensPerformance().hasVisibleCoverage');await capture('large-mode-tail');
      report.largeMode.performance=await evaluate('window.ExcelLensPerformance()');assert(report.largeMode.performance.visibleNodes<10000);
    }
    assert.equal(await evaluate('localStorage.getItem("ExcelLens.crosshairColor")'),'purple');
    await click('#readingModeBtn');await click('#gridTextureToggle');await key('Escape','Escape',27);assert.equal(await evaluate('localStorage.getItem("ExcelLens.gridTexture")'),'off');
    await cdp('Page.reload',{ignoreCache:true});await pause(300);await waitFor('document.readyState==="complete"&&!!window.ExcelLensSession');await evaluate('window.ExcelLensSession.ready');assert.equal(await evaluate('document.querySelector("#gridTextureToggle").checked'),false);assert.equal(await evaluate('document.querySelector("#gridBackdrop").hidden'),true);report.checks.push('disabled texture preference survives page reload');
    assert.deepEqual(report.errors,[]);report.result='PASS';
  }catch(error){report.failure=error.stack||String(error);process.exitCode=1;
  }finally{
    if(server?.exitCode===null)server.kill();if(socket?.readyState===1){await cdp('Browser.close').catch(()=>{});socket.close();}if(browser.exitCode===null&&browser.signalCode===null){await Promise.race([new Promise(r=>browser.once('exit',r)),pause(1500)]);if(browser.exitCode===null&&browser.signalCode===null)browser.kill();}
    report.sourceUnchanged=[...inputs].every(([file,hash])=>digest(fs.readFileSync(file))===hash);assert(report.sourceUnchanged);report.sources=[...inputs].map(([file,sha256])=>({file:path.basename(file),sha256}));
    fs.writeFileSync(path.join(out,'result.json'),JSON.stringify(report,null,2)+'\n');const receipt={...report,privateEvidence:'private/background-details/result.json'};delete receipt.tall?.performance;
    fs.writeFileSync(path.join(root,'validation',version,'background-details.json'),JSON.stringify(receipt,null,2)+'\n');console.log(JSON.stringify({result:report.result,home:report.home.length,grid:report.grid.length,checks:report.checks,failure:report.failure},null,2));
  }
})();
