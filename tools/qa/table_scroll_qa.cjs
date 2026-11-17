// Real-service, isolated headless Chromium regression for TXT header ownership
// and native scrollbar thumb pixels/dragging. No user files or browser profiles.
const fs=require('node:fs'),path=require('node:path'),os=require('node:os');
const {spawn}=require('node:child_process'),{createHash}=require('node:crypto'),{inflateSync}=require('node:zlib');
const assert=require('node:assert/strict');
const [address,...options]=process.argv.slice(2);
if(!address)throw Error('Usage: node table_scroll_qa.cjs SERVER_URL [--legacy] [--out DIR]');
const root=path.resolve(__dirname,'../..'),legacy=options.includes('--legacy');
const themeIndex=options.indexOf('--theme'),theme=themeIndex>=0?options[themeIndex+1]:'off';
const outIndex=options.indexOf('--out');
const output=path.resolve(outIndex>=0?options[outIndex+1]:path.join(root,'validation',require('./project_config.cjs').version(),'private','table-scroll'+(legacy?'-legacy':'')+(theme!=='off'?'-'+theme:'')));
fs.mkdirSync(output,{recursive:true});
fs.writeFileSync(path.join(output,'result.json'),JSON.stringify({result:'RUNNING',theme})+'\n');
const file=path.join(output,'table.txt'),names=['凭证来源','批名','凭证编号','凭证名','日期','金额',...Array.from({length:42},(_,i)=>'字段'+(i+7))];
const rows=[names.join('@')];
for(let r=1;r<=300;r++)rows.push(['来源'+r,'批次'+r,String(r).padStart(5,'0'),'凭证'+r,'2026-11-12',(r/100).toFixed(2),...Array.from({length:42},(_,i)=>String(r*100+i))].join('@'));
fs.writeFileSync(file,rows.join('\n')+'\n');
const hash=()=>createHash('sha256').update(fs.readFileSync(file)).digest('hex'),before=hash();
const profile=fs.mkdtempSync(path.join(os.tmpdir(),'excellens-table-scroll-qa-'));
const child=spawn(process.env.QA_CHROME||'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe',['--headless=new','--window-size=1500,900','--no-first-run','--no-default-browser-check','--disable-extensions','--disable-background-networking','--remote-debugging-port=0','--user-data-dir='+profile,'about:blank'],{windowsHide:true,stdio:'ignore'});
const pause=ms=>new Promise(resolve=>setTimeout(resolve,ms));
const viewport={width:1500,height:900};
let socket,sequence=0;const pending=new Map(),errors=[],scenarios=[],assetDigests={};
function cdp(method,params={}){return new Promise((resolve,reject)=>{const id=++sequence,timer=setTimeout(()=>{pending.delete(id);reject(Error(method+' timeout'));},30000);pending.set(id,{resolve,reject,timer});socket.send(JSON.stringify({id,method,params}));});}
async function evaluate(expression){const response=await cdp('Runtime.evaluate',{expression,awaitPromise:true,returnByValue:true});if(response.exceptionDetails)throw Error(JSON.stringify(response.exceptionDetails));return response.result.value;}
async function waitFor(expression){for(let i=0;i<250;i++){const value=await evaluate(expression);if(value)return value;await pause(80);}throw Error('Timeout: '+expression);}
const settle=()=>evaluate('new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))');
async function capture(name,clip){const shot=await cdp('Page.captureScreenshot',{format:'png',captureBeyondViewport:false,...(clip?{clip:{...clip,scale:1}}:{})});const buffer=Buffer.from(shot.data,'base64');fs.writeFileSync(path.join(output,name),buffer);return buffer;}
// Decode screenshot PNG scanlines, using only Node's bundled zlib. This checks
// painted native controls rather than merely trusting computed pseudo styles.
function png(buffer){
  const parts=[];let w,h,bpp;
  for(let offset=8;offset<buffer.length;){const size=buffer.readUInt32BE(offset),type=buffer.toString('ascii',offset+4,offset+8),data=buffer.subarray(offset+8,offset+8+size);if(type==='IHDR'){w=data.readUInt32BE(0);h=data.readUInt32BE(4);assert.equal(data[8],8);bpp=data[9]===6?4:data[9]===2?3:0;assert(bpp,'unsupported PNG color type');}if(type==='IDAT')parts.push(data);offset+=size+12;}
  const raw=inflateSync(Buffer.concat(parts)),stride=w*bpp,pixels=Buffer.alloc(stride*h);
  const paeth=(a,b,c)=>{const p=a+b-c,da=Math.abs(p-a),db=Math.abs(p-b),dc=Math.abs(p-c);return da<=db&&da<=dc?a:db<=dc?b:c;};
  for(let y=0;y<h;y++){const filter=raw[y*(stride+1)];assert(filter<=4);for(let x=0;x<stride;x++){const pos=y*stride+x,left=x>=bpp?pixels[pos-bpp]:0,up=y?pixels[pos-stride]:0,corner=y&&x>=bpp?pixels[pos-stride-bpp]:0;pixels[pos]=(raw[y*(stride+1)+1+x]+(filter===1?left:filter===2?up:filter===3?Math.floor((left+up)/2):filter===4?paeth(left,up,corner):0))&255;}}
  return {w,h,at:(x,y)=>[...pixels.subarray((y*w+x)*bpp,(y*w+x)*bpp+3)]};
}
function thumbRun(buffer,vertical){
  const p=png(buffer),length=vertical?p.h:p.w,cross=Math.floor((vertical?p.w:p.h)/2);let best={start:0,length:0},start=0,count=0;
  const samples=Array.from({length},(_,n)=>p.at(vertical?cross:n,vertical?n:cross)),counts=new Map();
  for(const rgb of samples){const k=rgb.join(',');counts.set(k,(counts.get(k)||0)+1);}const track=[...counts].sort((a,b)=>b[1]-a[1])[0][0].split(',').map(Number);
  for(let n=0;n<=length;n++){const thumb=n<length&&samples[n].reduce((sum,c,i)=>sum+Math.abs(c-track[i]),0)/3>8;if(thumb){if(!count)start=n;count++;}else{if(count>best.length)best={start,length:count};count=0;}}
  return {...best,trackRGB:track,thumbRGB:samples[best.start+Math.floor(best.length/2)]};
}
const scroll=async(top,left=0)=>{await evaluate(`(()=>{const g=document.querySelector('#grid');g.scrollTop=${top};g.scrollLeft=${left};g.dispatchEvent(new Event('scroll'));})()`);await settle();};
async function labels(){return evaluate(`(()=>{const head=document.querySelector('#columnHeaders .table-column-head'),label=head?.querySelector('.table-column-name'),arrow=head?.querySelector('.auto-filter-button');return {sticky:document.querySelector('.grid-region').classList.contains('table-header-scrolled'),labelVisible:!!label&&getComputedStyle(label).display!=='none',arrowVisible:!!arrow&&getComputedStyle(arrow).display!=='none',source:document.querySelector('.grid-row[data-row="1"] .grid-cell[data-col="1"]')?.textContent||'',coverage:window.ExcelLensPerformance().hasVisibleCoverage};})()`);}
async function freeze(rows,cols){await evaluate(`document.querySelector('#freezeBtn').click();document.querySelector('#freezeRowsInput').value='${rows}';document.querySelector('#freezeColsInput').value='${cols}';document.querySelector('#freezeApplyBtn').click()`);await waitFor('!document.querySelector("#freezeDialog").open');await settle();}
async function drag(axis,track,run){
  const vertical=axis==='vertical',x=track.x+(vertical?track.width/2:run.start+run.length/2),y=track.y+(vertical?run.start+run.length/2:track.height/2);
  const endX=vertical?x:track.x+track.width-run.length/2,endY=vertical?track.y+track.height-run.length/2:y;
  await cdp('Input.dispatchMouseEvent',{type:'mouseMoved',x,y});await cdp('Input.dispatchMouseEvent',{type:'mousePressed',x,y,button:'left',buttons:1,clickCount:1});
  await pause(50);
  for(let i=1;i<=12;i++){await cdp('Input.dispatchMouseEvent',{type:'mouseMoved',x:x+(endX-x)*i/12,y:y+(endY-y)*i/12,button:'left',buttons:1});await pause(20);}
  await cdp('Input.dispatchMouseEvent',{type:'mouseReleased',x:endX,y:endY,button:'left',buttons:0,clickCount:1});
  console.log('drag-end',axis,await evaluate(`(()=>{const g=document.querySelector('#grid');return {top:g.scrollTop,maxTop:g.scrollHeight-g.clientHeight,left:g.scrollLeft,maxLeft:g.scrollWidth-g.clientWidth};})()`),{track,run,x,y,endX,endY});
  await capture(axis+'-after-drag.png');
  await waitFor(`(()=>{const g=document.querySelector('#grid');return g.${vertical?'scrollTop':'scrollLeft'}>=(g.${vertical?'scrollHeight':'scrollWidth'}-g.${vertical?'clientHeight':'clientWidth'})*.999;})()`);
  // Grab the same thumb at the far end and return it to the first record/column.
  await pause(50);
  await cdp('Input.dispatchMouseEvent',{type:'mousePressed',x:endX,y:endY,button:'left',buttons:1,clickCount:1});
  await pause(50);
  const startX=vertical?x:track.x+1,startY=vertical?track.y+1:y;
  for(let i=1;i<=12;i++){await cdp('Input.dispatchMouseEvent',{type:'mouseMoved',x:endX+(startX-endX)*i/12,y:endY+(startY-endY)*i/12,button:'left',buttons:1});await pause(20);}
  await cdp('Input.dispatchMouseEvent',{type:'mouseReleased',x:startX,y:startY,button:'left',buttons:0,clickCount:1});
  await pause(50);console.log('drag-return',axis,await evaluate(`({top:document.querySelector('#grid').scrollTop,left:document.querySelector('#grid').scrollLeft})`));
  await waitFor(`document.querySelector('#grid').${vertical?'scrollTop':'scrollLeft'}===0`);
  await cdp('Input.dispatchMouseEvent',{type:'mouseMoved',x:10,y:10});await settle();
}
(async()=>{try{
  for(const name of ['app.js','style.css','themes.css','readingmode.js']){const response=await fetch(new URL('/assets/'+name,address));assert(response.ok);assetDigests[name]=createHash('sha256').update(await response.text()).digest('hex');}
  let port;for(let i=0;i<150;i++){try{port=Number(fs.readFileSync(path.join(profile,'DevToolsActivePort'),'utf8').split('\n')[0]);break;}catch{}await pause(100);}assert(port,'headless Chrome did not start');
  const pages=await(await fetch('http://127.0.0.1:'+port+'/json')).json();socket=new WebSocket(pages.find(item=>item.type==='page').webSocketDebuggerUrl);
  await new Promise((resolve,reject)=>{socket.addEventListener('open',resolve,{once:true});socket.addEventListener('error',reject,{once:true});});
  socket.addEventListener('message',event=>{const response=JSON.parse(event.data);if(response.method==='Runtime.exceptionThrown')errors.push(response.params.exceptionDetails.exception?.description||response.params.exceptionDetails.text);const request=pending.get(response.id);if(request){pending.delete(response.id);clearTimeout(request.timer);response.error?request.reject(Error(response.error.message)):request.resolve(response.result);}});
  await cdp('Page.enable');await cdp('Runtime.enable');await cdp('Emulation.setDeviceMetricsOverride',{...viewport,deviceScaleFactor:1,mobile:false});
  await cdp('Page.navigate',{url:address});await waitFor('document.readyState==="complete"&&!!document.querySelector("#fileInput")');await evaluate('window.ExcelLensSession.ready');
  const input=await cdp('Runtime.evaluate',{expression:'document.querySelector("#fileInput")'});await cdp('DOM.setFileInputFiles',{objectId:input.result.objectId,files:[file]});
  await waitFor('document.querySelector("#indexOverlay").hidden&&!!document.querySelector(".table-column-head")&&window.ExcelLensPerformance().hasVisibleCoverage');
  if(theme!=='off'){await evaluate(`document.querySelector('input[name="readingPreset"][value=${JSON.stringify(theme)}]').click()`);await settle();}
  if(legacy){const css=fs.readFileSync(path.join(root,'validation/1.8.4/private/before/style.css'),'utf8');await evaluate(`document.querySelector('link[href="/assets/style.css"]').disabled=true;const legacyStyle=document.createElement('style');legacyStyle.textContent=${JSON.stringify(css)};document.head.append(legacyStyle);window.ExcelLensTableHeaders.useStickyLabels=()=>true;`);}
  await freeze(0,0);await scroll(0);
  const atTop=await labels();assert.equal(atTop.labelVisible,legacy,'source and sticky field labels must not duplicate');assert.equal(atTop.source,'凭证来源▾');await capture('top.png');
  scenarios.push({name:'top',...atTop});
  if(!legacy){
    await evaluate(`document.querySelector('#grid').focus();document.querySelector('#grid').dispatchEvent(new KeyboardEvent('keydown',{key:'ArrowDown',altKey:true,bubbles:true,cancelable:true}))`);
    await waitFor('!document.querySelector("#autoFilterPopover").hidden');
    assert((await evaluate(`document.querySelector('#autoFilterPopover').getBoundingClientRect().top`))>100,'keyboard menu must anchor at the visible source header');
    await evaluate(`document.querySelector('#autoFilterClose').click()`);scenarios.push({name:'visible-keyboard-filter-anchor'});
    for(const zoom of [70,100,125]){
      await evaluate(`(()=>{let n=Number(document.querySelector('#zoomValue').textContent.replace('%',''));while(n!==${zoom}){document.querySelector(n<${zoom}?'#zoomInBtn':'#zoomOutBtn').click();n=Number(document.querySelector('#zoomValue').textContent.replace('%',''));}})()`);
      await scroll(25*zoom/100-1);assert.equal((await labels()).labelVisible,false);
      await scroll(25*zoom/100+1);assert.equal((await labels()).labelVisible,true);
      await scroll(1000);await waitFor('window.ExcelLensPerformance().hasVisibleCoverage');assert.equal((await labels()).labelVisible,true);
      await scroll(0);await waitFor(`!!document.querySelector('.grid-row[data-row="1"]')&&window.ExcelLensPerformance().hasVisibleCoverage`);assert.equal((await labels()).labelVisible,false);scenarios.push({name:'down-and-return',zoom});
    }
    await capture('returned-top.png');
    for(const dpr of [1.25,2]){
      await cdp('Emulation.setDeviceMetricsOverride',{...viewport,deviceScaleFactor:dpr,mobile:false});
      await scroll(1000);assert.equal((await labels()).labelVisible,true);await scroll(0);assert.equal((await labels()).labelVisible,false);scenarios.push({name:'display-density-return',zoom:125,dpr});
    }
    await cdp('Emulation.setDeviceMetricsOverride',{...viewport,deviceScaleFactor:1,mobile:false});
    await scroll(1000);await waitFor('window.ExcelLensPerformance().hasVisibleCoverage');await capture('scrolled.png');
    await freeze(1,2);await scroll(1000);await waitFor('!document.querySelector("#freezeTop").hidden');assert.equal((await labels()).labelVisible,false);
    await evaluate(`document.querySelector('.pinned-cell[data-ref="A1"] .auto-filter-button').click()`);await waitFor('!document.querySelector("#autoFilterPopover").hidden');await evaluate(`document.querySelector('#autoFilterClose').click()`);await capture('frozen.png');scenarios.push({name:'frozen-source-header'});
    await freeze(0,2);await scroll(1000);assert.equal((await labels()).labelVisible,true);await freeze(0,0);
    await scroll(0);await waitFor('window.ExcelLensPerformance().hasVisibleCoverage');
    await evaluate(`document.querySelector('#textHeaderBtn').click();document.querySelector('#textHeaderRow').value='3';document.querySelector('#textHeaderApply').click()`);await waitFor('!document.querySelector("#textHeaderDialog").open');
    await scroll(90);assert.equal((await labels()).labelVisible,false);await scroll(95);assert.equal((await labels()).labelVisible,true);scenarios.push({name:'manual-row-3',zoom:125});
    await evaluate(`document.querySelector('#textHeaderBtn').click();document.querySelector('#textHeaderPlain').click()`);await waitFor('!document.querySelector(".grid-region").classList.contains("text-table-view")');assert.equal((await labels()).labelVisible,false);
    await evaluate(`document.querySelector('#textHeaderBtn').click();document.querySelector('#textHeaderAuto').click()`);await waitFor('!document.querySelector("#textHeaderDialog").open');
    await scroll(0);await waitFor('window.ExcelLensPerformance().hasVisibleCoverage');
    await evaluate(`document.querySelector('.grid-cell[data-ref="A1"] .auto-filter-button').click()`);await waitFor('!document.querySelector("#autoFilterPopover").hidden');await evaluate(`document.querySelector('#autoFilterSortDesc').click()`);await waitFor('document.querySelector("#filterProgressText").textContent.startsWith("筛选完成")&&window.ExcelLensPerformance().hasVisibleCoverage');
    assert.equal((await labels()).labelVisible,false);await scroll(1000);assert.equal((await labels()).labelVisible,true);scenarios.push({name:'sorted-header'});
    await evaluate(`document.querySelector('#clearFilterBtn').click()`);await waitFor('document.querySelector("#clearFilterBtn").disabled');
  }
  // Extend the normal read-only viewport into empty coordinates through the
  // product's name box. This exercises the maximum grid range without a huge
  // fixture, mocked APIs, or modifying internal application state.
  await evaluate(`(()=>{const n=document.querySelector('#nameBox');n.focus();n.value='XFD50000000';n.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true}));})()`);
  await waitFor('document.querySelector("#nameBox").value==="XFD50000000"&&window.ExcelLensPerformance().hasVisibleCoverage');
  await scroll(0);await waitFor('window.ExcelLensPerformance().hasVisibleCoverage');
  // A short-lived sort-complete toast sits over the right-hand track; let the
  // normal product timeout dismiss it before grabbing the thumb a second time.
  await waitFor('document.querySelector("#toast").hidden');
  await cdp('Input.dispatchMouseEvent',{type:'mouseMoved',x:10,y:10});await settle();
  const tracks=await evaluate(`(()=>{const g=document.querySelector('#grid'),r=g.getBoundingClientRect();return {vertical:{x:r.x+g.clientWidth,y:r.y,width:g.offsetWidth-g.clientWidth,height:g.clientHeight},horizontal:{x:r.x,y:r.y+g.clientHeight,width:g.clientWidth,height:g.offsetHeight-g.clientHeight},range:{height:g.scrollHeight,width:g.scrollWidth},standardWidth:getComputedStyle(g).scrollbarWidth,standardColor:getComputedStyle(g).scrollbarColor};})()`);
  console.log('native-tracks',tracks);
  const thumbs={};for(const axis of ['vertical','horizontal']){const buffer=await capture(axis+'-thumb.png',tracks[axis]);thumbs[axis]=thumbRun(buffer,axis==='vertical');console.log('native-thumb',axis,thumbs[axis]);assert(legacy?thumbs[axis].length<36:thumbs[axis].length>=36,axis+' painted thumb minimum: '+JSON.stringify(thumbs[axis]));if(!legacy)await drag(axis,tracks[axis],thumbs[axis]);}
  await capture('large-range-top.png');scenarios.push({name:'maximum-grid-native-thumbs',tracks,thumbs,draggedBothDirections:!legacy});
  assert.deepEqual(errors,[]);assert.equal(hash(),before);
  const report={result:'PASS',theme,assetDigests,browser:await cdp('Browser.getVersion'),legacyDetected:legacy,sourceUnchanged:true,sourceSHA256:before,scenarios};fs.writeFileSync(path.join(output,'result.json'),JSON.stringify(report,null,2)+'\n');console.log(JSON.stringify(report,null,2));
}finally{
  if(socket?.readyState===1){try{await cdp('Browser.close');}catch{}socket.close();}
  if(child.exitCode===null&&child.signalCode===null)await Promise.race([new Promise(resolve=>child.once('exit',resolve)),pause(1500)]);if(child.exitCode===null&&child.signalCode===null)child.kill();
  await pause(300);if(path.dirname(path.resolve(profile))===path.resolve(os.tmpdir())&&path.basename(profile).startsWith('excellens-table-scroll-qa-'))try{fs.rmSync(profile,{recursive:true,force:true,maxRetries:12,retryDelay:500});}catch(error){console.error('Temporary QA profile cleanup deferred: '+profile+' ('+error.code+')');}
}})().catch(error=>{console.error(error);process.exitCode=1;});
