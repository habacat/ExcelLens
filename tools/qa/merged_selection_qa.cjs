// Paint regression through the real Go service and an isolated headless browser.
// Compares merged anchors while selecting the covered row and a following row.
const fs=require('node:fs'),path=require('node:path'),os=require('node:os');
const {spawn}=require('node:child_process'),{createHash}=require('node:crypto');
const assert=require('node:assert/strict');
const [address,file,...options]=process.argv.slice(2);
if(!address||!file)throw Error('Usage: node merged_selection_qa.cjs SERVER_URL FILE [--out DIR] [--legacy-row-fill] [--shutdown]');
const root=path.resolve(__dirname,'../..');
const option=name=>options.includes(name)?options[options.indexOf(name)+1]:'';
const output=path.resolve(option('--out')||path.join(root,'validation',require('./project_config.cjs').version(),'private','merged-selection'));
fs.mkdirSync(output,{recursive:true});
const sourceHash=()=>createHash('sha256').update(fs.readFileSync(file)).digest('hex'),before=sourceHash();
const profile=fs.mkdtempSync(path.join(os.tmpdir(),'excellens-merged-qa-'));
const child=spawn(process.env.QA_CHROME||'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe',['--headless=new','--no-first-run','--no-default-browser-check','--disable-extensions','--disable-background-networking','--remote-debugging-port=0','--user-data-dir='+profile,'about:blank'],{windowsHide:true,stdio:'ignore'});
const pause=ms=>new Promise(resolve=>setTimeout(resolve,ms));
const viewport={width:3200,height:1200};
let socket,sequence=0;const pending=new Map(),errors=[];
function cdp(method,params={}){return new Promise((resolve,reject)=>{const id=++sequence,timer=setTimeout(()=>{pending.delete(id);reject(Error(method+' timeout'));},30000);pending.set(id,{resolve,reject,timer});socket.send(JSON.stringify({id,method,params}));});}
async function evaluate(expression){const response=await cdp('Runtime.evaluate',{expression,awaitPromise:true,returnByValue:true});if(response.exceptionDetails)throw Error(JSON.stringify(response.exceptionDetails));return response.result.value;}
async function waitFor(expression){for(let i=0;i<300;i++){const value=await evaluate(expression);if(value)return value;await pause(100);}throw Error('Timeout: '+expression);}
const settle=()=>evaluate('new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))');
const letter=number=>{let text='';for(;number;number=Math.floor((number-1)/26))text=String.fromCharCode(65+(number-1)%26)+text;return text;};
async function capture(filename,clip){const shot=await cdp('Page.captureScreenshot',{format:'png',captureBeyondViewport:false,clip:{...clip,scale:1}});fs.writeFileSync(path.join(output,filename),Buffer.from(shot.data,'base64'));}

(async()=>{try{
  let port;
  for(let i=0;i<150;i++){try{port=Number(fs.readFileSync(path.join(profile,'DevToolsActivePort'),'utf8').split('\n')[0]);break;}catch{}await pause(100);}
  if(!port)throw Error('Headless Chrome did not start');
  const pages=await(await fetch('http://127.0.0.1:'+port+'/json')).json(),page=pages.find(item=>item.type==='page');
  socket=new WebSocket(page.webSocketDebuggerUrl);
  await new Promise((resolve,reject)=>{socket.addEventListener('open',resolve,{once:true});socket.addEventListener('error',reject,{once:true});});
  socket.addEventListener('message',event=>{const response=JSON.parse(event.data);if(response.method==='Runtime.exceptionThrown')errors.push(response.params.exceptionDetails.exception?.description||response.params.exceptionDetails.text);const request=pending.get(response.id);if(request){pending.delete(response.id);clearTimeout(request.timer);response.error?request.reject(Error(response.error.message)):request.resolve(response.result);}});
  await cdp('Page.enable');await cdp('Runtime.enable');
  await cdp('Emulation.setDeviceMetricsOverride',{...viewport,deviceScaleFactor:1,mobile:false});
  await cdp('Page.navigate',{url:address});await waitFor('document.readyState==="complete"&&!!document.querySelector("#fileInput")');
  await evaluate('window.ExcelLensSession.ready');
  const input=await cdp('Runtime.evaluate',{expression:'document.querySelector("#fileInput")'});
  await cdp('DOM.setFileInputFiles',{objectId:input.result.objectId,files:[path.resolve(file)]});
  await waitFor('document.querySelector("#indexOverlay").hidden&&!!document.querySelector(".grid-cell")');
  const layout=await evaluate(`(async()=>{const headers=new Headers({'X-ExcelLens-Token':document.querySelector('meta[name="app-token"]').content});window.ExcelLensSession.apply(headers);const sheet=document.querySelector('.sheet-tab.active').textContent;return await(await fetch('/api/layout?sheet='+encodeURIComponent(sheet),{headers})).json();})()`);
  const groups=new Map();
  for(const merge of layout.sourceMerges||[]){if(merge.endRow<=merge.row||merge.endRow-merge.row>2)continue;const key=merge.row+':'+merge.endRow;if(!groups.has(key))groups.set(key,[]);groups.get(key).push(merge);}
  const anchors=[...groups.values()].filter(group=>group.length>=3).sort((a,b)=>b.length-a.length||a[0].row-b[0].row)[0];
  assert(anchors,'fixture needs several vertical header merges');
  const lower=anchors[0].endRow;
  const lastColumn=Math.max(...(layout.sourceMerges||[]).filter(m=>m.row<=lower&&m.endRow>=anchors[0].row).map(m=>m.endCol));
  const column=letter(Math.max(14,lastColumn+1)),safe=column+(lower+1),covered=column+lower;
  const refs=anchors.map(m=>letter(m.col)+m.row);
  await evaluate(`document.querySelector('#freezeBtn').click();document.querySelector('#freezeRowsInput').value='0';document.querySelector('#freezeColsInput').value='0';document.querySelector('#freezeApplyBtn').click()`);
  await waitFor(`!!document.querySelector('.grid-row .grid-cell[data-ref="${refs[0]}"]')?.classList.contains('merged-cell')`);
  if(options.includes('--legacy-row-fill'))await evaluate(`(()=>{const style=document.createElement('style');style.textContent='.grid-row.active-row{background:#f2f7fd}';document.head.append(style);})()`);
  const scenarios=[];
  const modes=[{mode:'normal',zoom:70,dpr:1},{mode:'normal',zoom:100,dpr:1.25},{mode:'normal',zoom:125,dpr:2},{mode:'plain',zoom:70,dpr:1},{mode:'plain',zoom:125,dpr:1.5},{mode:'frozen',zoom:100,dpr:1},{mode:'frozen',zoom:125,dpr:1.25},{mode:'recycled',zoom:125,dpr:1}];
  let zoom=await evaluate(`Number(document.querySelector('#zoomValue').textContent.replace('%',''))`),formatted=await evaluate(`document.querySelector('#formatBtn').getAttribute('aria-pressed')==='true'`),frozen=false;
  for(let i=0;i<modes.length;i++){
    const scenario=modes[i];
    await cdp('Emulation.setDeviceMetricsOverride',{...viewport,deviceScaleFactor:scenario.dpr,mobile:false});
    while(zoom!==scenario.zoom){const increase=zoom<scenario.zoom;await evaluate(`document.querySelector('#${increase?'zoomInBtn':'zoomOutBtn'}').click()`);const next=await evaluate(`Number(document.querySelector('#zoomValue').textContent.replace('%',''))`);assert.notEqual(next,zoom,'zoom control did not advance');zoom=next;}
    const shouldFormat=scenario.mode!=='plain';if(shouldFormat!==formatted){await evaluate(`document.querySelector('#formatBtn').click()`);formatted=shouldFormat;}
    const shouldFreeze=scenario.mode==='frozen';
    if(shouldFreeze!==frozen){await evaluate(`document.querySelector('#freezeBtn').click();document.querySelector('#freezeRowsInput').value='${shouldFreeze?lower:0}';document.querySelector('#freezeColsInput').value='${shouldFreeze?3:0}';document.querySelector('#freezeApplyBtn').click()`);frozen=shouldFreeze;}
    const selector=frozen?'.pinned-cell':'.grid-row .grid-cell';
    await evaluate(`document.querySelector('#grid').scrollTop=0;document.querySelector('#grid').scrollLeft=0;document.querySelector('#grid').dispatchEvent(new Event('scroll'))`);
    if(scenario.mode==='recycled'){
      await evaluate(`window.__previousMergedAnchor=document.querySelector('.grid-row .grid-cell[data-ref="${refs[0]}"]')`);
      await evaluate(`(()=>{const grid=document.querySelector('#grid');grid.scrollTop=grid.scrollHeight;grid.dispatchEvent(new Event('scroll'))})()`);
      await waitFor(`!document.querySelector('.grid-row .grid-cell[data-ref="${refs[0]}"]')`);
      await evaluate(`(()=>{const grid=document.querySelector('#grid');grid.scrollTop=0;grid.dispatchEvent(new Event('scroll'))})()`);
      await waitFor(`!!document.querySelector('.grid-row .grid-cell[data-ref="${refs[0]}"]')&&document.querySelector('.grid-row .grid-cell[data-ref="${refs[0]}"]')!==window.__previousMergedAnchor`);
    }
    await waitFor(`${JSON.stringify(refs)}.every(ref=>document.querySelector(${JSON.stringify(selector)}+'[data-ref="'+ref+'"]')?.classList.contains('merged-cell'))`);
    await waitFor(`!!document.querySelector('.grid-row .grid-cell[data-ref="${covered}"]')`);await pause(100);await settle();
    const boxes=await evaluate(`${JSON.stringify(refs)}.map(ref=>{const node=document.querySelector(${JSON.stringify(selector)}+'[data-ref="'+ref+'"]'),r=node.getBoundingClientRect();return {ref,text:node.textContent,x:r.x,y:r.y,width:r.width,height:r.height};})`);
    const x=Math.floor(Math.min(...boxes.map(b=>b.x))),y=Math.floor(Math.min(...boxes.map(b=>b.y)));
    const clip={x,y,width:Math.ceil(Math.max(...boxes.map(b=>b.x+b.width)))-x,height:Math.ceil(Math.max(...boxes.map(b=>b.y+b.height)))-y};
    assert(clip.x>=0&&clip.y>=0&&clip.width>0&&clip.x+clip.width<=viewport.width&&clip.y+clip.height<=viewport.height,'headers must fit inside the test viewport: '+JSON.stringify({scenario,clip}));
    const prefix=String(i).padStart(2,'0')+'-'+scenario.mode+'-'+zoom+'-'+scenario.dpr;
    const files=[];
    for(const [step,ref] of [['safe',safe],['covered',covered],['return',safe]]){
      await evaluate(`document.querySelector('.grid-row .grid-cell[data-ref="${ref}"]').click()`);
      await waitFor(`document.querySelector('#nameBox').value==='${ref}'`);await settle();
      const filename=prefix+'-'+step+'.png';await capture(filename,clip);files.push(filename);
    }
    const rowFill=await evaluate(`getComputedStyle(document.querySelector('.grid-row.active-row')).backgroundColor`);
    scenarios.push({...scenario,files,clip,boxes,safe,covered,rowFill});
    console.log('Captured '+scenario.mode+' '+zoom+'% DPR '+scenario.dpr);
  }
  assert.deepEqual(errors,[]);assert.equal(sourceHash(),before);
  const manifest={sourceSHA256:before,sourceUnchanged:true,legacyRowFill:options.includes('--legacy-row-fill'),scenarios};
  fs.writeFileSync(path.join(output,'scenarios.json'),JSON.stringify(manifest,null,2)+'\n');
  console.log(JSON.stringify({capture:'PASS',scenarios:scenarios.length,anchors:anchors.length,selected:[safe,covered],sourceUnchanged:true,output}));
}finally{
  if(socket?.readyState===1){if(options.includes('--shutdown'))try{await evaluate(`(async()=>{const headers=new Headers({'X-ExcelLens-Token':document.querySelector('meta[name="app-token"]').content});window.ExcelLensSession?.apply(headers);await fetch('/api/exit',{method:'POST',headers});})()`);}catch{}try{await cdp('Browser.close');}catch{}socket.close();}
  if(child.exitCode===null&&child.signalCode===null)await Promise.race([new Promise(resolve=>child.once('exit',resolve)),pause(1500)]);
  if(child.exitCode===null&&child.signalCode===null)child.kill();
  await pause(500);
  if(path.dirname(path.resolve(profile))===path.resolve(os.tmpdir())&&path.basename(profile).startsWith('excellens-merged-qa-')){
    try{fs.rmSync(profile,{recursive:true,force:true,maxRetries:8,retryDelay:200});}
    catch(error){console.error('Temporary QA profile cleanup deferred: '+error.code);}
  }
}})().catch(error=>{console.error(error);process.exitCode=1;});
