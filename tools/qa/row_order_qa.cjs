// Real application, generated source and isolated headless Chrome. This is an
// automated browser regression, not manual desktop or Office/WPS acceptance.
const fs=require('node:fs'),os=require('node:os'),path=require('node:path'),assert=require('node:assert/strict');
const {spawn}=require('node:child_process'),{createHash}=require('node:crypto');
const {Readable}=require('node:stream'),{pipeline}=require('node:stream/promises'),readline=require('node:readline');
const config=require('./project_config.cjs'),version=config.version(),outputVersion=config.validationVersion();
const root=path.resolve(__dirname,'../..'),out=path.join(root,'validation',outputVersion,'private','row-order');fs.mkdirSync(out,{recursive:true});
const profile=fs.mkdtempSync(path.join(out,'chrome-')),exe=path.resolve(process.argv[2]||'artifacts/qa/uiprobe.exe');
const count=262145,source=path.join(out,'只读排序.csv');
const rows=['项目,金额,编号'];for(let i=1;i<=count;i++)rows.push(`item${count-i+1},${i}.01,000${String(i).padStart(16,'0')}`);
fs.writeFileSync(source,rows.join('\n')+'\n');rows.length=0;
const hash=()=>createHash('sha256').update(fs.readFileSync(source)).digest('hex'),before=hash();
const report={version,result:'FAIL',desktopManualAcceptance:false,dataRows:count,checks:[],errors:[]};
const pause=ms=>new Promise(resolve=>setTimeout(resolve,ms));
let server,browser,socket,seq=0;const pending=new Map();
function cdp(method,params={}){return new Promise((resolve,reject)=>{const id=++seq,timer=setTimeout(()=>{pending.delete(id);reject(Error(method+' timeout'));},30000);pending.set(id,{resolve,reject,timer});socket.send(JSON.stringify({id,method,params}));});}
async function evaluate(expression){const r=await cdp('Runtime.evaluate',{expression,returnByValue:true,awaitPromise:true});if(r.exceptionDetails)throw Error(JSON.stringify(r.exceptionDetails));return r.result.value;}
const run=(fn,...args)=>evaluate('('+fn.toString()+')('+args.map(x=>JSON.stringify(x)).join(',')+')');
async function waitFor(expression){for(let i=0;i<800;i++){if(await evaluate(expression))return;await pause(50);}throw Error('Timeout: '+expression);}
const click=selector=>run(selector=>{const node=document.querySelector(selector);if(!node)throw Error('Missing '+selector);node.click();},selector);
const api=(url,data)=>run(async(url,data)=>{
  await window.ExcelLensSession.ready;
  const headers=window.ExcelLensSession.apply(new Headers({'X-ExcelLens-Token':document.querySelector('meta[name="app-token"]').content,'Content-Type':'application/json'}));
  const response=await fetch(url,{headers,...(data===null?{}:{method:'POST',body:JSON.stringify(data)})}),value=await response.json();
  if(!response.ok)throw Error(JSON.stringify(value));return value;
},url,data===undefined?null:data);
async function shot(name){await evaluate('new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))');const image=await cdp('Page.captureScreenshot',{format:'png',captureBeyondViewport:false});fs.writeFileSync(path.join(out,name+'.png'),Buffer.from(image.data,'base64'));}
async function drag(first,last){
  const bounds=await run((first,last)=>[first,last].map(selector=>{const r=document.querySelector(selector).getBoundingClientRect();return{x:r.x+r.width*.5,y:r.y+r.height*.5};}),first,last);
  await cdp('Input.dispatchMouseEvent',{type:'mouseMoved',...bounds[0]});await cdp('Input.dispatchMouseEvent',{type:'mousePressed',button:'left',buttons:1,clickCount:1,...bounds[0]});
  await cdp('Input.dispatchMouseEvent',{type:'mouseMoved',button:'left',buttons:1,...bounds[1]});await cdp('Input.dispatchMouseEvent',{type:'mouseReleased',button:'left',buttons:0,clickCount:1,...bounds[1]});
}
(async()=>{
  try{
    server=spawn(exe,['-duration','5m'],{cwd:root,windowsHide:true,stdio:['ignore','pipe','pipe']});
    const address=await new Promise((resolve,reject)=>{let data='',errors='';server.stderr.on('data',x=>errors+=x);server.stdout.on('data',x=>{data+=x;if(data.includes('\n')){try{resolve(JSON.parse(data.split('\n')[0]).url);}catch(e){reject(e);}}});server.once('error',reject);server.once('exit',code=>reject(Error('uiprobe exited '+code+' '+errors)));});
    browser=spawn(process.env.QA_CHROME||'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe',['--headless=new','--no-first-run','--no-default-browser-check','--disable-extensions','--disable-component-update','--disable-sync','--disable-background-networking','--remote-debugging-port=0','--user-data-dir='+profile,'about:blank'],{windowsHide:true,stdio:'ignore'});
    const active=path.join(profile,'DevToolsActivePort');for(let i=0;i<100&&!fs.existsSync(active);i++)await pause(100);
    const port=fs.readFileSync(active,'utf8').split('\n')[0],tabs=await(await fetch('http://127.0.0.1:'+port+'/json/list')).json();
    socket=new WebSocket(tabs.find(tab=>tab.type==='page').webSocketDebuggerUrl);await new Promise((resolve,reject)=>{socket.addEventListener('open',resolve,{once:true});socket.addEventListener('error',reject,{once:true});});
    socket.addEventListener('message',event=>{const data=JSON.parse(event.data);if(data.id){const item=pending.get(data.id);if(item){pending.delete(data.id);clearTimeout(item.timer);data.error?item.reject(Error(JSON.stringify(data.error))):item.resolve(data.result);}}else if(data.method==='Runtime.exceptionThrown')report.errors.push(data.params.exceptionDetails);});
    await cdp('Page.enable');await cdp('Runtime.enable');await cdp('Emulation.setDeviceMetricsOverride',{width:1600,height:1000,deviceScaleFactor:1,mobile:false});await cdp('Page.navigate',{url:address});
    await waitFor('document.readyState==="complete"&&!!window.ExcelLensSession');await evaluate('window.ExcelLensSession.ready');
    await run(()=>{
      window.qaFilters=[];window.qaExportStarts=[];window.qaCopied='';
      Object.defineProperty(navigator,'clipboard',{configurable:true,value:{writeText:async text=>{window.qaCopied=text;}}});
      const original=window.fetch.bind(window);window.qaFetch=original;
      window.fetch=async(url,options)=>{
        const name=String(url),response=await original(url,options);
        if(name==='/api/filter/start')window.qaFilters.push({request:JSON.parse(options.body),...(await response.clone().json())});
        if(name==='/api/export/start'){
          const started=await response.clone().json();window.qaExportStarts.push({request:JSON.parse(options.body),...started});
          if(window.qaHoldExport)return new Promise(resolve=>window.qaReleaseExport=()=>resolve(response));
        }
        return response;
      };
    });
    const input=await cdp('Runtime.evaluate',{expression:'document.querySelector("#fileInput")'});await cdp('DOM.setFileInputFiles',{objectId:input.result.objectId,files:[source]});
    await waitFor('window.ExcelLensPerformance().hasVisibleCoverage&&!window.ExcelLensPerformance().sheetLoading');assert.equal((await api('/api/status')).version,version);
    await click('#freezeBtn');await run(()=>{document.querySelector('#freezeRowsInput').value='1';document.querySelector('#freezeColsInput').value='0';});await click('#freezeApplyBtn');
    await click('#multiSortBtn');await waitFor('document.querySelector("#multiSortDialog").open');
    await run(()=>{document.querySelector('#sortHeaderRow').value='0';document.querySelector('.sort-column').value='A';document.querySelector('.sort-mode').value='natural';document.querySelector('.sort-direction').value='asc';});await click('#sortApply');
    await waitFor(`!!document.querySelector('#rowsLayer .grid-cell[data-ref="A${count+1}"]')&&document.querySelector('#rowsLayer .grid-cell[data-ref="A${count+1}"]').textContent==='item1'&&!document.querySelector('#clearFilterBtn').disabled`);
    const started=await evaluate('window.qaFilters.at(-1)'),filterId=started.jobId,sheet=started.request.sheet;
    assert.equal(started.request.sortKeys[0].mode,'natural');assert.equal(started.request.headerRow,0);
    const page=await api('/api/sheet?'+new URLSearchParams({sheet,filterId,excludeThroughRow:1,offset:0,limit:3,startCol:1,colCount:3}));
    assert.equal(page.totalRows,count);assert.deepEqual(page.rows.map(row=>row.number),[count+1,count,count-1]);
    const frozen=await api('/api/filter/locate?'+new URLSearchParams({sheet,filterId,excludeThroughRow:1,row:1}));assert.equal(frozen.found,false);
    const located=await api('/api/filter/locate?'+new URLSearchParams({sheet,filterId,excludeThroughRow:1,row:count}));assert.equal(located.found,true);assert.equal(located.visualIndex,2);
    assert.equal(await evaluate('document.querySelectorAll("#rowsLayer .grid-cell[data-ref=\\"A1\\"]").length'),0);
    report.checks.push('natural sort of 262145 records uses real row-order routing; a frozen row sorted to the end is excluded from the body and source lookup agrees');
    await drag(`#rowsLayer .grid-cell[data-ref="A${count+1}"]`,`#rowsLayer .grid-cell[data-ref="C${count-1}"]`);
    await cdp('Input.dispatchKeyEvent',{type:'keyDown',key:'c',code:'KeyC',windowsVirtualKeyCode:67,modifiers:2});await cdp('Input.dispatchKeyEvent',{type:'keyUp',key:'c',code:'KeyC',windowsVirtualKeyCode:67,modifiers:2});
    await waitFor('window.qaCopied.length>0');
    assert.equal(await evaluate('window.qaCopied'),[0,1,2].map(i=>`item${i+1}\t${count-i}.01\t000${String(count-i).padStart(16,'0')}`).join('\n'));
    report.checks.push('real drag and Ctrl+C preserve displayed row order, exact text amounts and leading-zero identifiers');
    await shot('natural-sorted-frozen');await click('#multiSortBtn');await waitFor('document.querySelector("#multiSortDialog").open');assert.equal(await evaluate('document.querySelector(".sort-mode").value'),'natural');
    await cdp('Emulation.setDeviceMetricsOverride',{width:1000,height:900,deviceScaleFactor:1,mobile:false});await shot('natural-sort-narrow');await click('#multiSortDialog .dialog-close');
    await cdp('Emulation.setDeviceMetricsOverride',{width:1600,height:1000,deviceScaleFactor:1,mobile:false});
    await click('#exportOpenBtn');await run(()=>{document.querySelector('#exportScope').value='filtered';document.querySelector('#exportFormat').value='csv';document.querySelector('#exportMode').value='values';document.querySelector('#exportBOM').checked=false;document.querySelector('#exportCSVGuard').checked=false;document.querySelector('#exportFormat').dispatchEvent(new Event('change'));window.qaHoldExport=true;});await click('#exportStartBtn');await waitFor('typeof window.qaReleaseExport==="function"');
    assert.equal(await evaluate('window.qaExportStarts.at(-1).request.filterId'),filterId);
    await click('#exportDialog .dialog-close');await click('#clearFilterBtn');await waitFor('document.querySelector("#clearFilterBtn").disabled&&window.ExcelLensPerformance().hasVisibleCoverage');await evaluate('window.qaReleaseExport()');
    await click('#exportOpenBtn');await waitFor('!document.querySelector("#exportResult").hidden');
    const origin=await evaluate('document.querySelector("#exportSnapshot").textContent');assert(origin.includes('只读排序.csv')&&origin.includes('已筛选')&&origin.includes((count+1).toLocaleString()));
    const download=await evaluate('document.querySelector("#exportDownload").getAttribute("href")');
    const headers=await run(()=>Object.fromEntries(window.ExcelLensSession.apply(new Headers({'X-ExcelLens-Token':document.querySelector('meta[name="app-token"]').content})).entries()));
    const response=await fetch(new URL(download,address),{headers});assert.equal(response.status,200);
    const exported=path.join(out,'sorted.csv');await pipeline(Readable.fromWeb(response.body),fs.createWriteStream(exported));
    let position=0;for await(const line of readline.createInterface({input:fs.createReadStream(exported),crlfDelay:Infinity})){
      if(position<count){const source=count-position;assert.equal(line,`item${position+1},${source}.01,000${String(source).padStart(16,'0')}`);}else assert.equal(line,'项目,金额,编号');position++;
    }
    assert.equal(position,count+1);report.checks.push('export started from a mapped filter survives closing its window and clearing the filter; every downloaded CSV record and its visible source label keep the original sorted snapshot');
    await shot('export-after-filter-clear');assert.equal(hash(),before);assert.equal(report.errors.length,0);report.sourceSHA256=before;report.sourceUnchanged=true;report.result='PASS';
  }catch(error){report.error=error.stack;process.exitCode=1;}
  finally{fs.writeFileSync(path.join(root,'validation',outputVersion,'row-order-browser.json'),JSON.stringify(report,null,2)+'\n');for(const item of pending.values())clearTimeout(item.timer);pending.clear();socket?.close();browser?.kill();server?.kill();console.log(JSON.stringify(report,null,2));}
})().catch(error=>{console.error(error);process.exitCode=1;});
