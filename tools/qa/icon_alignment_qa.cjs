// Optional geometry and interaction regression using the production local
// service and an isolated headless browser. Node is a development tool only.
const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict');
const {spawn}=require('node:child_process'),{createHash}=require('node:crypto');
const root=path.resolve(__dirname,'../..'),config=require('./project_config.cjs'),version=config.version(),receipts=path.join(root,'validation',config.validationVersion());
const out=path.join(receipts,'private','icon-alignment');fs.mkdirSync(out,{recursive:true});
const serverExe=process.argv[2];if(!serverExe)throw Error('Usage: node tools/qa/icon_alignment_qa.cjs ISOLATED_QA_SERVER_EXE');
const pause=ms=>new Promise(resolve=>setTimeout(resolve,ms)),digest=data=>createHash('sha256').update(data).digest('hex');
const source=path.join(out,'icons.csv');fs.writeFileSync(source,'item,amount\none,1.25\ntwo,2.50\n');
const sourceSHA256=digest(fs.readFileSync(source)),profile=fs.mkdtempSync(path.join(out,'chrome-'));
const browser=spawn(process.env.QA_CHROME||'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe',[
  '--headless=new','--no-first-run','--no-default-browser-check','--disable-extensions','--disable-component-update',
  '--disable-sync','--disable-background-networking','--remote-debugging-port=0','--user-data-dir='+profile,'about:blank'
],{windowsHide:true,stdio:'ignore'});
const report={version,result:'FAIL',desktopManualAcceptance:false,cases:[],interactions:[],errors:[],assetDigests:{}};
let socket,server,sequence=0;const pending=new Map(),requests=[];
function cdp(method,params={}){return new Promise((resolve,reject)=>{const id=++sequence,timer=setTimeout(()=>{pending.delete(id);reject(Error(method+' timeout'));},20000);pending.set(id,{resolve,reject,timer});socket.send(JSON.stringify({id,method,params}));});}
async function evaluate(expression){const r=await cdp('Runtime.evaluate',{expression,returnByValue:true,awaitPromise:true});if(r.exceptionDetails)throw Error(JSON.stringify(r.exceptionDetails));return r.result.value;}
async function waitFor(expression){for(let i=0;i<200;i++){if(await evaluate(expression))return;await pause(80);}throw Error('Timeout: '+expression);}
async function settle(){await evaluate('Promise.all(document.getAnimations().filter(a=>a.effect?.getComputedTiming().endTime<=300).map(a=>a.finished.catch(()=>{})))');}
async function click(selector){const r=await evaluate(`(()=>{const r=document.querySelector(${JSON.stringify(selector)}).getBoundingClientRect();return {x:r.x+r.width/2,y:r.y+r.height/2};})()`);await cdp('Input.dispatchMouseEvent',{type:'mouseMoved',...r});await cdp('Input.dispatchMouseEvent',{type:'mousePressed',button:'left',clickCount:1,...r});await cdp('Input.dispatchMouseEvent',{type:'mouseReleased',button:'left',clickCount:1,...r});}
async function capture(name,selector){const clip=selector?await evaluate(`(()=>{const r=document.querySelector(${JSON.stringify(selector)}).getBoundingClientRect();return {x:r.x,y:r.y,width:r.width,height:r.height,scale:2};})()`):undefined;const r=await cdp('Page.captureScreenshot',{format:'png',captureBeyondViewport:false,...(clip?{clip}:{})});fs.writeFileSync(path.join(out,name+'.png'),Buffer.from(r.data,'base64'));}
async function metrics(selector,{text=false}={}){
  const value=await evaluate(`(${function(selector){
    const n=document.querySelector(selector),svg=n.querySelector('.icon'),r=n.getBoundingClientRect(),i=svg.getBoundingClientRect(),raw=svg.getBBox(),k=svg.viewBox.baseVal?.width?1:24/parseFloat(getComputedStyle(svg).width),b={x:raw.x*k,y:raw.y*k,width:raw.width*k,height:raw.height*k},s=getComputedStyle(n);
    return {selector,width:r.width,height:r.height,dx:i.x+i.width/2-r.x-r.width/2,dy:i.y+i.height/2-r.y-r.height/2,bbox:{x:b.x,y:b.y,width:b.width,height:b.height},stroke:getComputedStyle(svg).stroke,color:s.color,hidden:svg.getAttribute('aria-hidden'),focusable:svg.getAttribute('focusable'),hit:document.elementFromPoint(i.x+i.width/2,i.y+i.height/2)?.closest('button,a')===n};
  }.toString()})(${JSON.stringify(selector)})`);
  assert(Math.abs(value.dy)<.05,selector+' icon vertical center: '+value.dy);
  if(!text)assert(Math.abs(value.dx)<.05,selector+' icon horizontal center: '+value.dx);
  assert(Math.abs(value.bbox.x+value.bbox.width/2-12)<.02,selector+' artwork horizontal center');
  assert(Math.abs(value.bbox.y+value.bbox.height/2-12)<.02,selector+' artwork vertical center');
  assert.equal(value.stroke,value.color);assert.equal(value.hidden,'true');assert.equal(value.focusable,'false');assert(value.hit,selector+' icon hit target');
  return value;
}
function count(pathname){return requests.filter(p=>p===pathname).length;}
async function startServer(){
  server=spawn(path.resolve(serverExe),[],{cwd:root,windowsHide:true,env:{...process.env,LOCALAPPDATA:path.join(out,'storage','cache'),APPDATA:path.join(out,'storage','config')},stdio:['ignore','pipe','pipe']});
  server.stderr.on('data',data=>fs.appendFileSync(path.join(out,'server.log'),data));
  return new Promise((resolve,reject)=>{let text='';const timer=setTimeout(()=>reject(Error('Server startup timeout')),10000);server.stdout.on('data',data=>{text+=data;const address=text.match(/http:\/\/127\.0\.0\.1:\d+\//)?.[0];if(address){clearTimeout(timer);resolve(address);}});server.once('exit',code=>{clearTimeout(timer);reject(Error('Server exited during startup: '+code));});});
}
(async()=>{
  try{
    let port;for(let i=0;i<150;i++){try{port=Number(fs.readFileSync(path.join(profile,'DevToolsActivePort'),'utf8').split('\n')[0]);break;}catch{}await pause(100);}assert(port,'Chrome did not start');
    const pages=await(await fetch('http://127.0.0.1:'+port+'/json')).json();socket=new WebSocket(pages.find(x=>x.type==='page').webSocketDebuggerUrl);
    await new Promise((ok,fail)=>{socket.addEventListener('open',ok,{once:true});socket.addEventListener('error',fail,{once:true});});
    socket.addEventListener('message',event=>{const r=JSON.parse(event.data);if(r.method==='Runtime.exceptionThrown')report.errors.push(r.params.exceptionDetails.exception?.description||r.params.exceptionDetails.text);if(r.method==='Network.requestWillBeSent')requests.push(new URL(r.params.request.url).pathname);const task=pending.get(r.id);if(task){clearTimeout(task.timer);pending.delete(r.id);r.error?task.reject(Error(r.error.message)):task.resolve(r.result);}});
    await cdp('Page.enable');await cdp('Runtime.enable');await cdp('Network.enable');report.browser=(await cdp('Browser.getVersion')).product;
    const address=await startServer();await cdp('Page.navigate',{url:address});await waitFor('document.readyState==="complete"&&!!window.ExcelLensSession');await evaluate('window.ExcelLensSession.ready');
    assert.equal(await evaluate('document.querySelector("meta[name=app-version]").content'),version);
    for(const name of ['style.css','calculator.css','analytics.css','themes.css','app.js','recent.js','images.js','analytics.js']){const data=Buffer.from(await(await fetch(new URL('/assets/'+name,address))).arrayBuffer());assert.equal(digest(data),digest(fs.readFileSync(path.join(root,'internal/ui/web',name))),name+' differs from source');report.assetDigests[name]=digest(data);}
    for(const width of [1900,1000,760])for(const scale of [1,1.25,1.5,2])for(const preset of ['off','warm','graphite']){
      await cdp('Emulation.setDeviceMetricsOverride',{width,height:1000,deviceScaleFactor:scale,mobile:false});
      await evaluate(`document.querySelector('input[name="readingPreset"][value="${preset}"]').click()`);await settle();
      const original=await metrics('#recentRefreshBtn');await evaluate('document.body.style.fontFamily="monospace"');const fallback=await metrics('#recentRefreshBtn');await evaluate('document.body.style.fontFamily=""');
      assert.equal(original.dx,fallback.dx);assert.equal(original.dy,fallback.dy);assert.deepEqual(original.bbox,fallback.bbox);
      report.cases.push({width,scale,preset,original,fallback});
      if(width===1900&&scale===1)await capture('home-'+preset,'.landing-recent');
    }
    await cdp('Emulation.setDeviceMetricsOverride',{width:1900,height:1000,deviceScaleFactor:1,mobile:false});await evaluate('document.querySelector("input[name=readingPreset][value=off]").click()');await settle();
    const before=count('/api/recent');await click('#recentRefreshBtn .icon');await waitFor('!document.querySelector("#recentList").textContent.includes("读取最近文件")');await pause(150);assert.equal(count('/api/recent'),before+1);report.interactions.push('refresh SVG click sends one recent request');
    await cdp('Input.dispatchKeyEvent',{type:'keyDown',key:'Tab',code:'Tab',windowsVirtualKeyCode:9});await cdp('Input.dispatchKeyEvent',{type:'keyUp',key:'Tab',code:'Tab',windowsVirtualKeyCode:9});
    const focus=await evaluate('(()=>{const n=document.querySelector("#recentRefreshBtn");n.focus();return {id:document.activeElement.id,outline:getComputedStyle(n).outlineStyle};})()');assert.equal(focus.id,'recentRefreshBtn');assert.notEqual(focus.outline,'none');report.keyboardFocus=focus;
    const input=await cdp('Runtime.evaluate',{expression:'document.querySelector("#fileInput")'});await cdp('DOM.setFileInputFiles',{objectId:input.result.objectId,files:[source]});
    await waitFor('!document.querySelector("#workspace").hidden&&document.querySelector("#indexOverlay").hidden&&window.ExcelLensPerformance().hasVisibleCoverage');
    await click('.grid-cell[data-ref="A2"]');report.detail=await metrics('#cellDetailBtn');await click('#cellDetailBtn .icon');await waitFor('document.querySelector("#detailDialog").open');await click('#detailDialog .dialog-close');await waitFor('!document.querySelector("#detailDialog").open');report.interactions.push('detail SVG opens panel; centered close button closes it');
    await click('#searchOpenBtn');await waitFor('document.querySelector("#searchDialog").open');report.history=await metrics('#searchHistoryBtn');await click('#searchHistoryBtn .icon');await waitFor('!document.querySelector("#searchHistoryPanel").hidden');assert.equal(await evaluate('document.querySelector("#searchHistoryBtn").getAttribute("aria-expanded")'),'true');report.interactions.push('history SVG toggles search history');
    await evaluate('document.querySelector("#searchInput").value="one"');await click('#searchBtn');await waitFor('!document.querySelector("#copySearchResultsBtn").disabled');report.searchCopy=await metrics('#copySearchResultsBtn');report.searchSave=await metrics('#saveSearchResultsBtn');await capture('search-controls','.search-results-toolbar');await click('#searchDialog .dialog-close');
    await click('#analysisOpenBtn');await waitFor('!document.querySelector("#analysisRoot").hidden&&Number(document.querySelector("#analysisFieldCount").textContent)===2');report.fields=await metrics('#analysisLoadFields',{text:true});
    const fieldsBefore=count('/api/analysis/fields');await click('#analysisLoadFields .icon');await pause(200);assert.equal(count('/api/analysis/fields'),fieldsBefore+1);report.interactions.push('update-fields SVG reloads fields once');await capture('analysis-update-fields','.analysis-source-config');await click('#analysisClose');
    // The empty CSV has no embedded pictures. Reveal only the viewer shell for
    // layout QA; this is not evidence for the workbook image-loading pipeline.
    await evaluate('document.querySelector("#pictureViewer").hidden=false');report.rotation=await metrics('#pictureRotate');report.pictureCopy=await metrics('#pictureCopy');report.pictureSave=await metrics('#pictureDownload');
    await click('#pictureRotate .icon');assert((await evaluate('document.querySelector("#picturePreview").style.transform')).includes('rotate(90deg)'));report.interactions.push('rotation SVG reaches the initialized viewer handler');await capture('picture-controls','.picture-viewer-controls');await click('#pictureClose');
    await click('#homeBtn');await waitFor('!document.querySelector("#welcome").hidden&&!!document.querySelector(".recent-item")');
    report.compact=await evaluate(`(${function(){
      // A labeled rendering fixture exercises hidden/dynamic button families
      // with their production selectors, without claiming their workflows ran.
      const gallery=document.createElement('div');gallery.id='qaIconGallery';gallery.style.cssText='position:fixed;inset:35px auto auto 35px;z-index:1000;display:grid;grid-template-columns:repeat(4,190px);gap:12px;padding:24px;background:#fff;border:1px solid #ccc;color:#344454';
      const cases=[['详情','mini-button icon-mini','i'],['关闭','dialog-close','×'],['最近文件','recent-action','◇'],['缩放','zoom-button','＋'],['工作表列表','sheet-list-button','☷'],['工作表翻页','sheet-edge-button','›'],['计算窗口','calculator-icon','×'],['列菜单','column-filter','▼'],['筛选条件','remove-rule','×'],['排序条件','sort-level-remove','×'],['护眼设置','','×']];
      const values=[];for(const [label,cls,glyph]of cases){const tile=document.createElement('div');tile.style.cssText='display:flex;align-items:center;gap:12px;height:54px';const caption=document.createElement('span');caption.textContent=label;const wrap=document.createElement('div');if(label==='护眼设置')wrap.className='reading-mode-heading';const n=document.createElement('button');n.className=cls;n.type='button';n.textContent=glyph;if(cls==='column-filter')n.classList.add('active');n.style.position='static';n.style.transform='none';wrap.append(n);tile.append(caption,wrap);gallery.append(tile);document.body.append(gallery);const s=getComputedStyle(n);values.push({label,display:s.display,align:s.alignItems,justify:s.justifyContent,padding:[s.paddingTop,s.paddingRight,s.paddingBottom,s.paddingLeft],lineHeight:s.lineHeight,fontSize:s.fontSize});}return values;
    }.toString()})()`);
    // A flex parent blockifies an inline-flex item to computed display:flex.
    for(const value of report.compact){assert(['flex','inline-flex'].includes(value.display),value.label);assert.equal(value.align,'center');assert.equal(value.justify,'center');assert(value.padding.every(p=>p==='0px'),value.label+' padding');assert.equal(value.lineHeight,value.fontSize,value.label+' line height');}
    await capture('compact-controls','#qaIconGallery');await evaluate('document.querySelector("#qaIconGallery").remove()');
    assert.equal(digest(fs.readFileSync(source)),sourceSHA256);assert.deepEqual(report.errors,[]);report.result='PASS';
  }catch(error){report.failure=error.stack||String(error);process.exitCode=1;
  }finally{
    if(server?.exitCode===null)server.kill();if(socket?.readyState===1){await cdp('Browser.close').catch(()=>{});socket.close();}
    if(browser.exitCode===null&&browser.signalCode===null){await Promise.race([new Promise(resolve=>browser.once('exit',resolve)),pause(1500)]);if(browser.exitCode===null&&browser.signalCode===null)browser.kill();}
    Object.assign(report,{sourceSHA256,sourceUnchanged:digest(fs.readFileSync(source))===sourceSHA256});fs.writeFileSync(path.join(out,'result.json'),JSON.stringify(report,null,2)+'\n');
    const receipt={...report,cases:report.cases.map(({width,scale,preset})=>({width,scale,preset})),privateEvidence:'private/icon-alignment/result.json'};
    fs.writeFileSync(path.join(receipts,'icon-alignment.json'),JSON.stringify(receipt,null,2)+'\n');console.log(JSON.stringify({result:report.result,cases:report.cases.length,interactions:report.interactions,failure:report.failure},null,2));
  }
})();
