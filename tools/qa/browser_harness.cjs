// Isolated developer checks against the real app. No browser profile, running
// desktop app or user data is reused. This is not manual desktop acceptance.
const fs=require('node:fs'),os=require('node:os'),path=require('node:path'),{spawn}=require('node:child_process');
const pause=ms=>new Promise(resolve=>setTimeout(resolve,ms));
class Client{
  constructor(socket,errors){this.socket=socket;this.pending=new Map();this.seq=0;socket.addEventListener('message',event=>{const data=JSON.parse(event.data),item=this.pending.get(data.id);if(item){this.pending.delete(data.id);clearTimeout(item.timer);data.error?item.reject(Error(JSON.stringify(data.error))):item.resolve(data.result);}else if(data.method==='Runtime.exceptionThrown')errors.push(data.params.exceptionDetails);});}
  call(method,params={}){return new Promise((resolve,reject)=>{const id=++this.seq,timer=setTimeout(()=>{this.pending.delete(id);reject(Error(method+' timeout'));},30000);this.pending.set(id,{resolve,reject,timer});this.socket.send(JSON.stringify({id,method,params}));});}
  async evaluate(expression){const r=await this.call('Runtime.evaluate',{expression,returnByValue:true,awaitPromise:true,userGesture:true});if(r.exceptionDetails)throw Error(JSON.stringify(r.exceptionDetails));return r.result.value;}
  run(fn,...args){return this.evaluate('('+fn.toString()+')('+args.map(x=>JSON.stringify(x)).join(',')+')');}
  async wait(expression){for(let i=0;i<800;i++){try{if(await this.evaluate(expression))return;}catch(error){if(!/Cannot find context|Execution context was destroyed|Inspected target navigated/.test(error.message))throw error;}await pause(50);}throw Error('Timeout: '+expression);}
  click(selector){return this.run(selector=>{const node=document.querySelector(selector);if(!node)throw Error('Missing '+selector);node.click();},selector);}
  async file(selector,file){const r=await this.call('Runtime.evaluate',{expression:'document.querySelector('+JSON.stringify(selector)+')'});await this.call('DOM.setFileInputFiles',{objectId:r.result.objectId,files:[file]});}
  api(url,data){return this.run(async(url,data)=>{await window.ExcelLensSession.ready;const headers=window.ExcelLensSession.apply(new Headers({'X-ExcelLens-Token':document.querySelector('meta[name="app-token"]').content,'Content-Type':'application/json'}));const response=await fetch(url,{headers,...(data===null?{}:{method:'POST',body:JSON.stringify(data)})}),value=await response.json();if(!response.ok)throw Error(JSON.stringify(value));return value;},url,data===undefined?null:data);}
  async shot(file){await this.call('Page.bringToFront');await this.evaluate('new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))');const r=await this.call('Page.captureScreenshot',{format:'png',captureBeyondViewport:false});fs.writeFileSync(file,Buffer.from(r.data,'base64'));}
  close(){for(const item of this.pending.values()){clearTimeout(item.timer);item.reject(Error('QA browser closed'));}this.pending.clear();this.socket.close();}
}
async function launch({errors=[],duration='8m',exe=process.env.QA_UIPROBE||path.resolve('artifacts/qa/uiprobe.exe'),launchFile,systemClipboard=false}){
  const storage = fs.mkdtempSync(path.join(os.tmpdir(), 'excellens-ui-qa-'));
  const profile = path.join(storage, 'chrome');
  const clients = [];
  let server,browser,port;
  const stop = async child => {
    if (!child?.pid || child.exitCode !== null || child.signalCode !== null) return;
    await new Promise(resolve => {
      child.once('exit', resolve);
      child.kill();
    });
  };
  async function close() {
    // Let Chrome stop its renderer and storage processes before removing the profile.
    if (clients.length && browser?.exitCode === null && browser.signalCode === null) {
      await new Promise(resolve => {
        const done = () => {
          clearTimeout(timer);
          browser.off('exit', done);
          resolve();
        };
        const timer = setTimeout(done, 4000);
        browser.once('exit', done);
        // Chrome can close the CDP socket before its profile has been flushed.
        // Only process exit (or the timeout) ends the graceful shutdown wait.
        clients[0].call('Browser.close').catch(() => {});
      });
    }
    for (const client of clients.splice(0)) client.close();
    await stop(browser);
    await stop(server);
    const resolved = path.resolve(storage);
    if (path.dirname(resolved) === path.resolve(os.tmpdir()) && path.basename(resolved).startsWith('excellens-ui-qa-')) {
      // Retry the whole profile, so a locked file cannot trigger nested retries at every directory level.
      for (let attempt = 0; ; attempt++) {
        try { fs.rmSync(resolved, {recursive:true, force:true}); break; }
        catch (error) {
          if (attempt === 3 || !['EBUSY','ENOTEMPTY','EPERM'].includes(error.code)) throw error;
          await pause(300);
        }
      }
    }
  }
  const harness={storage,close,async tabs(){return(await fetch('http://127.0.0.1:'+port+'/json/list')).json();},async connect(tab){const socket=new WebSocket(tab.webSocketDebuggerUrl);await new Promise((resolve,reject)=>{socket.addEventListener('open',resolve,{once:true});socket.addEventListener('error',reject,{once:true});});const c=new Client(socket,errors);clients.push(c);await c.call('Runtime.enable');await c.call('Page.enable');return c;}};
  const startServer=async()=>{
    const args=['-duration',duration,'-storage',storage];
    if(launchFile)args.push('-launch-file',path.resolve(launchFile));
    server=spawn(exe,args,{cwd:path.resolve(__dirname,'../..'),windowsHide:true,stdio:['ignore','pipe','pipe']});
    harness.address=await new Promise((resolve,reject)=>{let text='',errors='';server.stderr.on('data',x=>errors+=x);server.stdout.on('data',x=>{text+=x;if(text.includes('\n')){try{resolve(JSON.parse(text.split('\n')[0]).url);}catch(e){reject(e);}}});server.once('error',reject);server.once('exit',code=>reject(Error('uiprobe exited '+code+' '+errors)));});
  };
  harness.restart=async()=>{await stop(server);await startServer();return harness.address;};
  try{
    await startServer();
    // Headless Chrome has a private clipboard. A Windows interop check needs
    // the browser's platform clipboard, shared with the separate Excel instance.
    const display=systemClipboard ? ['--incognito','--disable-background-mode','--window-position=-32000,-32000'] : ['--headless=new'];
    browser=spawn(process.env.QA_CHROME||'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe',[...display,'--no-first-run','--no-default-browser-check','--disable-extensions','--disable-component-update','--disable-sync','--disable-background-networking','--disable-popup-blocking','--remote-debugging-port=0','--user-data-dir='+profile,'about:blank'],{windowsHide:true,stdio:'ignore'});
    await new Promise((resolve, reject) => {
      browser.once('spawn', resolve);
      browser.once('error', reject);
    });
    const active = path.join(profile, 'DevToolsActivePort');
    const deadline = Date.now() + 45000;
    while (!port) {
      if (browser.exitCode !== null || browser.signalCode !== null) throw Error('Chrome exited before opening its debugging port');
      try {
        const firstLine = fs.readFileSync(active, 'utf8').split('\n')[0];
        if (/^\d+$/.test(firstLine.trim())) port = Number(firstLine);
      } catch (error) {
        if (error.code !== 'ENOENT') throw error;
      }
      if (port) break;
      if (Date.now() >= deadline) throw Error('Chrome debugging port was unavailable after 45 seconds');
      await pause(100);
    }
    harness.page=await harness.connect((await harness.tabs()).find(tab=>tab.type==='page'));
    await harness.page.call('Emulation.setDeviceMetricsOverride',{width:1600,height:1050,deviceScaleFactor:1,mobile:false});await harness.page.call('Page.navigate',{url:harness.address});
    await harness.page.wait('document.readyState==="complete"&&!!window.ExcelLensSession');await harness.page.evaluate('window.ExcelLensSession.ready');return harness;
  }catch(error){await harness.close();throw error;}
}
// Reopen an actual exported file through a separate real application session.
// The original tab keeps its workbook/result; this checks adapter data/types,
// rather than treating a successful HTTP download as a round trip.
async function reopenRange(harness,file,start,end){
  const target=await harness.page.call('Target.createTarget',{url:harness.address});
  try{
    let tab;for(let i=0;i<50&&!tab;i++){tab=(await harness.tabs()).find(t=>t.id===target.targetId);if(!tab)await pause(50);}
    if(!tab)throw Error('Export reopen tab did not appear');
    const page=await harness.connect(tab);
    await page.wait('document.readyState==="complete"&&!!window.ExcelLensSession');await page.evaluate('window.ExcelLensSession.ready');await page.file('#fileInput',file);
    await page.wait('typeof window.ExcelLensPerformance==="function"&&window.ExcelLensPerformance().hasVisibleCoverage&&!window.ExcelLensPerformance().sheetLoading');
    await page.click('#analysisOpenBtn');await page.wait('window.ExcelLensAnalytics.diagnostics().fieldsReady&&!window.ExcelLensAnalytics.diagnostics().fieldsLoading');
    const workspace=await page.api('/api/analysis/workspace'),sheet=workspace.sourceSheets[0].name;
    return {status:await page.api('/api/status'),workspace,range:await page.api('/api/range?'+new URLSearchParams({sheet,start,end,format:'json'}))};
  }finally{await harness.page.call('Target.closeTarget',{targetId:target.targetId});}
}
module.exports={launch,pause,reopenRange};
