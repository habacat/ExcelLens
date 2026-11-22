// Headless regression against the real Go server. Uses an isolated browser
// profile; screenshots of private source data stay in an ignored directory.
const fs=require('node:fs'),path=require('node:path'),os=require('node:os'),assert=require('node:assert/strict');
const {createHash}=require('node:crypto');
const {spawn}=require('node:child_process');
const address=process.argv[2],sample=process.argv[3];
if(!address||!sample)throw Error('Usage: node calculation_qa.cjs SERVER_URL LEDGER_7Z [DIMENSIONS_XLSX] [PLAIN_CSV]');
const version=process.env.QA_VERSION||require('./project_config.cjs').version();
assert.match(version,/^\d+\.\d+\.\d+$/);
const root=path.resolve(__dirname,'../..'),out=path.join(root,'validation',version),privateOut=path.join(out,'private');
const sourceHash=()=>createHash('sha256').update(fs.readFileSync(path.resolve(sample))).digest('hex'),sourceBefore=sourceHash();
fs.mkdirSync(privateOut,{recursive:true});
const sleep=ms=>new Promise(resolve=>setTimeout(resolve,ms));
const profile=fs.mkdtempSync(path.join(os.tmpdir(),'excellens-chrome-qa-'));
const chrome=process.env.QA_CHROME||'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const child=spawn(chrome,['--headless=new','--no-first-run','--no-default-browser-check','--disable-extensions','--disable-background-networking','--remote-debugging-port=0','--user-data-dir='+profile,'about:blank'],{windowsHide:true,stdio:'ignore'});
let socket,seq=0;const pending=new Map(),errors=[],checks=[];
function cdp(method,params={}){return new Promise((resolve,reject)=>{const id=++seq,timer=setTimeout(()=>{pending.delete(id);reject(Error(method+' timed out'));},30000);pending.set(id,{resolve,reject,timer});socket.send(JSON.stringify({id,method,params}));});}
async function evaluate(expression){const r=await cdp('Runtime.evaluate',{expression,awaitPromise:true,returnByValue:true});if(r.exceptionDetails)throw Error(JSON.stringify(r.exceptionDetails));return r.result.value;}
async function waitFor(expression,limit=30000){const start=Date.now();while(Date.now()-start<limit){const result=await evaluate(expression);if(result)return result;await sleep(100);}throw Error('Timed out: '+expression);}
async function click(selector){return evaluate(`document.querySelector(${JSON.stringify(selector)}).click();true`);}
async function pointerPosition(selector){await waitFor(`!!document.querySelector(${JSON.stringify(selector)})`);return evaluate(`(()=>{const r=document.querySelector(${JSON.stringify(selector)}).getBoundingClientRect();return {x:r.left+Math.min(20,r.width/2),y:r.top+r.height/2};})()`);}
async function pointerClick(selector,shift=false){const p=await pointerPosition(selector);for(const type of ['mousePressed','mouseReleased'])await cdp('Input.dispatchMouseEvent',{type,...p,button:'left',clickCount:1,modifiers:shift?8:0});await settled();}
async function pointerDrag(from,to){const a=await pointerPosition(from),b=await pointerPosition(to);await cdp('Input.dispatchMouseEvent',{type:'mousePressed',...a,button:'left',clickCount:1});await cdp('Input.dispatchMouseEvent',{type:'mouseMoved',x:(a.x+b.x)/2,y:(a.y+b.y)/2,buttons:1});await cdp('Input.dispatchMouseEvent',{type:'mouseMoved',...b,buttons:1});await cdp('Input.dispatchMouseEvent',{type:'mouseReleased',...b,button:'left',clickCount:1});await settled();}
async function type(index,text){return evaluate(`(()=>{const input=document.querySelector('[data-formula-row="${index}"]');input.focus();input.value=${JSON.stringify(text)};input.dispatchEvent(new Event('input',{bubbles:true}));return true;})()`);}
async function enter(index){return evaluate(`document.querySelector('[data-formula-row="${index}"]').dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true,cancelable:true}));true`);}
async function screenshot(name){await evaluate('Promise.all(document.getAnimations().filter(a=>a.effect.getTiming().iterations!==Infinity).map(a=>a.finished.catch(()=>{})))');const r=await cdp('Page.captureScreenshot',{format:'png',captureBeyondViewport:false});fs.writeFileSync(path.join(privateOut,name+'.png'),Buffer.from(r.data,'base64'));}
const borderScenarios=[];
async function settled(){await evaluate('new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)))');}
async function borderMatrix(cellClass,mode){
 await evaluate(`(()=>{const box=document.querySelector('#nameBox');box.focus();box.value='A1';box.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true,cancelable:true}));return true;})()`);
 await waitFor(`!!document.querySelector('.${cellClass}[data-ref="E8"]')`);
 const baseHeight=await evaluate(`document.querySelector('.${cellClass}[data-ref="E8"]').getBoundingClientRect().height`);
 for(const dpr of [1,1.25,1.5,2]){
  await cdp('Emulation.setDeviceMetricsOverride',{width:1600,height:950,deviceScaleFactor:dpr,mobile:false});
  for(const zoom of [80,100,125]){
   await evaluate(`(()=>{const current=parseInt(document.querySelector('#zoomValue').textContent),id=${zoom}>current?'zoomInBtn':'zoomOutBtn';for(let i=0;i<Math.abs(${zoom}-current)/5;i++)document.getElementById(id).click();return true;})()`);await settled();
   await waitFor(`['E8','J8'].every(ref=>{const n=document.querySelector('.${cellClass}[data-ref="'+ref+'"]');return n&&Math.abs(n.getBoundingClientRect().height-${baseHeight*zoom/100})<0.15;})`);await settled();
   const edges=await evaluate(`['E8','J8'].map(ref=>{const node=document.querySelector('.${cellClass}[data-ref="'+ref+'"]'),r=node.getBoundingClientRect(),s=getComputedStyle(node);return {ref,x:r.right,y:r.top,height:r.height,border:s.borderRightWidth,color:s.borderRightColor};})`);
   for(const edge of edges){assert(parseFloat(edge.border)>0,mode+' '+edge.ref+' right border is missing');assert(edge.height>15);}
   const name='borders-'+mode+'-'+dpr+'-'+zoom;await screenshot(name);borderScenarios.push({mode,dpr,zoom,file:name+'.png',viewportWidth:1600,edges});
  }
 }
 await cdp('Emulation.setDeviceMetricsOverride',{width:1600,height:950,deviceScaleFactor:1,mobile:false});
 await evaluate(`(()=>{let current=parseInt(document.querySelector('#zoomValue').textContent);while(current!==100){document.getElementById(current<100?'zoomInBtn':'zoomOutBtn').click();current=parseInt(document.querySelector('#zoomValue').textContent);}return true;})()`);await settled();
 fs.writeFileSync(path.join(privateOut,'border-scenarios.json'),JSON.stringify(borderScenarios,null,2));
 checks.push(mode+' merged E8:E9 and J8:J9 retain their complete right borders at 80/100/125% zoom and DPR 1/1.25/1.5/2');
}
async function windowBounds(){return evaluate(`(()=>{const r=document.querySelector('#calculatorWindow').getBoundingClientRect();return {left:r.left,top:r.top,width:r.width};})()`);}
async function layoutSize(kind,size){return evaluate(`(async()=>{const headers=new Headers({'Content-Type':'application/json','X-ExcelLens-Token':document.querySelector('meta[name="app-token"]').content});window.ExcelLensSession.apply(headers);const r=await fetch('/api/layout',{method:'POST',headers,body:JSON.stringify({sheet:'Dimensions',kind:${JSON.stringify(kind)},index:${kind==='row'?2:1},size:${size}})});if(!r.ok)throw Error(await r.text());return true;})()`);}
async function dragWindow(dx,dy){const p=await evaluate(`(()=>{const r=document.querySelector('#calculatorDrag').getBoundingClientRect();return {x:r.left+50,y:r.top+15};})()`);await cdp('Input.dispatchMouseEvent',{type:'mousePressed',...p,button:'left',clickCount:1});await cdp('Input.dispatchMouseEvent',{type:'mouseMoved',x:p.x+dx,y:p.y+dy,button:'left',buttons:1});await cdp('Input.dispatchMouseEvent',{type:'mouseReleased',x:p.x+dx,y:p.y+dy,button:'left',clickCount:1});await settled();}
(async()=>{
 try{
  let port;
  for(let i=0;i<150;i++){try{port=Number(fs.readFileSync(path.join(profile,'DevToolsActivePort'),'utf8').split('\n')[0]);break;}catch{}await sleep(100);}
  if(!port)throw Error('Headless Chrome did not start');
  const pages=await(await fetch('http://127.0.0.1:'+port+'/json')).json(),page=pages.find(p=>p.type==='page');
  socket=new WebSocket(page.webSocketDebuggerUrl);await new Promise((resolve,reject)=>{socket.addEventListener('open',resolve,{once:true});socket.addEventListener('error',reject,{once:true});});
  socket.addEventListener('message',event=>{const r=JSON.parse(event.data);if(r.method==='Runtime.exceptionThrown')errors.push(r.params.exceptionDetails.text);const task=pending.get(r.id);if(task){pending.delete(r.id);clearTimeout(task.timer);r.error?task.reject(Error(r.error.message)):task.resolve(r.result);}});
  await cdp('Page.enable');await cdp('Runtime.enable');await cdp('Emulation.setDeviceMetricsOverride',{width:1600,height:950,deviceScaleFactor:1,mobile:false});
  await cdp('Page.navigate',{url:address});await waitFor('!!document.querySelector("#fileInput")');
  const home=await evaluate(`({title:document.querySelector('.landing-content h1').textContent,subtitle:document.querySelector('.landing-subline').textContent,hint:!!document.querySelector('.drop-hint'),oldText:document.body.textContent.includes('按最近打开时间排列，置顶优先')})`);
  assert.equal(home.title.trim(),'浏览数据');assert.equal(home.subtitle.trim(),'浏览Excel、CSV、TXT与SQLite');assert(!home.hint&&!home.oldText);checks.push('Home copy matches the requested wording');await screenshot('home');
  const doc=await cdp('DOM.getDocument'),input=await cdp('DOM.querySelector',{nodeId:doc.root.nodeId,selector:'#fileInput'});
  await cdp('DOM.setFileInputFiles',{nodeId:input.nodeId,files:[path.resolve(sample)]});
  await waitFor('!!document.querySelector(".archive-member")',60000);await click('.archive-member');await click('#archiveOpen');
  await waitFor('document.querySelector("#indexOverlay").hidden && document.querySelector(\'.grid-cell[data-ref="F12"]\')?.getBoundingClientRect().height<22',60000);
  const grid=await evaluate(`(()=>{const b=document.querySelector('.grid-cell[data-ref="D3"]'),d=document.querySelector('.grid-cell[data-ref="F12"]'),h=document.querySelector('.grid-cell[data-ref="B8"]');return {blankRight:getComputedStyle(b).borderRightWidth,blankBottom:getComputedStyle(b).borderBottomWidth,dataBorder:getComputedStyle(d).borderBottomWidth,vertical:getComputedStyle(d).alignItems,headerVertical:getComputedStyle(h).alignItems,rowHeight:d.getBoundingClientRect().height,raw:d.textContent,filename:document.querySelector('#fileBadge').title};})()`);
  const geometryDebug=await evaluate(`(()=>{const cell=document.querySelector('.grid-cell[data-ref="F12"]');return {cellHeight:cell.getBoundingClientRect().height,parentHeight:cell.parentNode.getBoundingClientRect().height,rowVariable:getComputedStyle(cell).getPropertyValue('--row-height'),inline:cell.getAttribute('style'),parent:cell.parentNode.getAttribute('style')};})()`);fs.writeFileSync(path.join(privateOut,'geometry-debug.json'),JSON.stringify(geometryDebug,null,2));await screenshot('ledger');
  assert.equal(grid.blankRight,'0px');assert.equal(grid.blankBottom,'0px');assert(parseFloat(grid.dataBorder)>0);assert.equal(grid.vertical,'flex-start');assert.equal(grid.headerVertical,'center');assert(grid.rowHeight<22);assert.equal(grid.raw,'38,403.75');assert.equal(grid.filename,path.basename(sample,'.7z')+'.xls');assert.equal(await evaluate('document.querySelector("#fileBadge").textContent'),grid.filename);checks.push('Real HTML ledger has no blank-area gridlines and keeps source borders, row height, alignment and values');await screenshot('ledger');
  await click('#formatBtn');assert(parseFloat(await evaluate('getComputedStyle(document.querySelector(\'.grid-cell[data-ref="D3"]\')).borderRightWidth'))>0);await click('#formatBtn');checks.push('Original-format toggle restores plain gridlines and returns to source borders');
  await evaluate(`(()=>{const box=document.querySelector('#nameBox');box.focus();box.value='F12:G12';box.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true,cancelable:true}));return true;})()`);
  await waitFor('document.querySelector("#quickStats").textContent.includes("求和=7万6807.5")');
  assert.equal(await evaluate('document.querySelector("#quickStats").textContent'),'平均值=3万8403.75计数=2求和=7万6807.5');checks.push('Selection statistics use the supplied native order and retain exact copy values');
  await borderMatrix('grid-cell','main');
  await click('#calculatorOpenBtn');
  assert.equal(await evaluate('document.querySelector("#calculatorScroll").getBoundingClientRect().height'),156);
  for(const [index,formula,want] of [[0,'=1=1','TRUE'],[1,'=IF(1=1,"一致","差异")','一致'],[2,'=LEFT("000123",3)','000'],[3,'=F12-G12','0']]){
    await type(index,formula);await enter(index);await waitFor(`document.querySelector('[data-result-row="${index}"]')?.textContent===${JSON.stringify(want)}`);
  }
  checks.push('Real formula endpoint calculates comparisons, text, leading zeroes and ledger references');
  for(const [formula,want] of [['=SUMIF(F12:G12,">0")','76807.5'],['=COUNTIF(F12:G12,">0")','2'],['=SUMIFS(F12:G12,F12:G12,">0",F12:G12,"<50000")','76807.5'],['=COUNTIFS(F12:G12,">0",F12:G12,"<50000")','2'],['=XLOOKUP(F12,F12:G12,F12:G12)','38,403.75'],['=SUMPRODUCT((F12:G12>0)*F12:G12)','76807.5'],['=SUM(IFERROR(F12:G12,0))','76807.5'],['=SEQUENCE(2,3)','2行 × 3列']]){await type(0,formula);await enter(0);await waitFor(`document.querySelector('[data-result-row="0"]')?.textContent===${JSON.stringify(want)}`);}
  await evaluate(`Object.defineProperty(navigator.clipboard,'writeText',{configurable:true,value:async text=>{window.__qaCopied=text;}});const output=document.querySelector('[data-result-row="0"]');output.focus();output.dispatchEvent(new KeyboardEvent('keydown',{key:'c',ctrlKey:true,bubbles:true,cancelable:true}));true`);
  await waitFor('window.__qaCopied==="1\\t2\\t3\\n4\\t5\\t6"');checks.push('Native conditional, lookup and array expressions calculate against the real ledger; Ctrl+C copies the entire array TSV');
  await type(0,'=norm.s.d');await waitFor('!document.querySelector("#calculatorCompletion").hidden && !!document.querySelector(\'[data-calc-function="NORM.S.DIST"]\')');await evaluate(`document.querySelector('[data-calc-function="NORM.S.DIST"]').dispatchEvent(new PointerEvent('pointerdown',{bubbles:true}));true`);assert.equal(await evaluate('document.querySelector("#calculatorFormula0").value'),'=NORM.S.DIST(');await type(0,'=NORM.S.DIST(0,TRUE)');await enter(0);await waitFor('document.querySelector(\'[data-result-row="0"]\')?.textContent==="0.5 ≈"');checks.push('Dotted native function names complete and calculate through the shared catalog');
  const compact=await windowBounds();for(let i=0;i<8;i++){await click('#calculatorNotes');await click('#calculatorNotes');await settled();assert.deepEqual(await windowBounds(),compact);}
  await click('#calculatorNotes');await dragWindow(-40,-20);await click('#calculatorNotes');const moved=await windowBounds();assert.equal(moved.left,compact.left-40);assert.equal(moved.top,compact.top-20);assert.equal(moved.width,compact.width);
  await dragWindow(-2000,0);const atEdge=await windowBounds();assert.equal(atEdge.left,8);await click('#calculatorNotes');await click('#calculatorNotes');assert.deepEqual(await windowBounds(),atEdge);await dragWindow(compact.left-8,0);checks.push('Eight note round-trips restore exact window bounds; intentional dragging and left-edge clamping preserve the correct collapsed position');
  await type(0,'=1=1');await enter(0);await waitFor('document.querySelector(\'[data-result-row="0"]\')?.textContent==="TRUE"');
  await type(4,'=su');await waitFor('!document.querySelector("#calculatorCompletion").hidden && [...document.querySelectorAll("[data-calc-function]")].some(n=>n.dataset.calcFunction==="SUM")');
  await evaluate(`document.querySelector('[data-calc-function="SUM"]').dispatchEvent(new PointerEvent('pointerdown',{bubbles:true}));true`);
  assert.equal(await evaluate('document.querySelector("#calculatorFormula4").value'),'=SUM(');await type(4,'=SUM(F12:G12)');await enter(4);await waitFor('document.querySelector(\'[data-result-row="4"]\')?.textContent==="76807.5"');checks.push('Function completion inserts the shared catalog item and missing parentheses close on commit');
  await click('#calculatorNotes');await evaluate(`(()=>{const n=document.querySelector('[data-note-row="0"]');n.focus();n.value='待复核';n.dispatchEvent(new Event('input',{bubbles:true}));return true;})()`);
  assert.equal(await evaluate('document.querySelector(\'[data-result-row="0"]\').textContent'),'TRUE');await click('#calculatorNotes');await click('#calculatorNotes');assert.equal(await evaluate('document.querySelector(\'[data-note-row="0"]\').value'),'待复核');checks.push('Notes survive hiding and leave completed results unchanged');await screenshot('calculation');
  await click('#calculatorMinimize');assert.equal(await evaluate('document.querySelector("#calculatorWindow").getBoundingClientRect().width'),0);assert(await evaluate('document.querySelector("#calculatorDock").closest(".status-strip")!==null&&!document.querySelector("#calculatorDock").hidden'));await screenshot('docked');await click('#calculatorDock');assert.equal(await evaluate('document.querySelector(\'[data-note-row="0"]\').value'),'待复核');checks.push('Minimize removes the floating window and restores from the bottom status bar');
  await type(0,'=1=1');const pasted=JSON.stringify(Array.from({length:30},()=>'=1=1').join('\n'));await evaluate(`(()=>{const input=document.querySelector('[data-formula-row="0"]'),data=new DataTransfer();data.setData('text/plain',${pasted});input.dispatchEvent(new ClipboardEvent('paste',{bubbles:true,cancelable:true,clipboardData:data}));return true;})()`);
  await waitFor('document.querySelector(\'[data-result-row="29"]\')?.textContent==="TRUE"');
  const virtual=await evaluate(`({height:document.querySelector('#calculatorScroll').getBoundingClientRect().height,rows:document.querySelector('#calculatorScroll').getAttribute('aria-rowcount'),nodes:document.querySelector('#calculatorRows').children.length})`);
  assert.equal(virtual.height,338);assert(Number(virtual.rows)>30);assert(virtual.nodes<=21);checks.push('Calculation viewport grows to twelve rows and scrolls beyond thirty with bounded DOM');
  await click('#calculatorMinimize');await click('#freezeBtn');await evaluate(`document.querySelector('#freezeRowsInput').value='9';document.querySelector('#freezeColsInput').value='1';true`);await click('#freezeApplyBtn');
  await waitFor('!!document.querySelector(\'.pinned-cell[data-ref="B8"]\')');
  const frozen=await evaluate(`(()=>{const h=document.querySelector('.pinned-cell[data-ref="B8"]'),b=document.querySelector('.pinned-cell[data-ref="D3"]');return {align:getComputedStyle(h).alignItems,border:getComputedStyle(h).borderBottomWidth,blank:getComputedStyle(b).borderRightWidth};})()`);
  assert.equal(frozen.align,'center');assert(parseFloat(frozen.border)>0);assert.equal(frozen.blank,'0px');checks.push('Frozen panes use the same source borders and vertical alignment');await screenshot('frozen');
  await borderMatrix('pinned-cell','frozen');
  const header=await evaluate(`(()=>{const a=document.querySelector('.pinned-cell[data-ref="B8"]').getBoundingClientRect(),b=document.querySelector('.pinned-cell[data-ref="L9"]').getBoundingClientRect();return {x:a.left,y:a.top,width:Math.min(innerWidth,b.right)-a.left,height:b.bottom-a.top};})()`);
  const headerImage=await cdp('Page.captureScreenshot',{format:'png',clip:{...header,scale:1}});fs.writeFileSync(path.join(privateOut,'header-borders.png'),Buffer.from(headerImage.data,'base64'));
  for(const ref of ['F8','H8','K8']){assert(parseFloat(await evaluate(`getComputedStyle(document.querySelector('.pinned-cell[data-ref="${ref}"]')).borderTopWidth`))>0);}
  checks.push('Merged debit, credit and balance headers paint the full top border across covered columns');
  if(process.argv[4]){
    const upload=async file=>{const doc=await cdp('DOM.getDocument'),input=await cdp('DOM.querySelector',{nodeId:doc.root.nodeId,selector:'#fileInput'});await cdp('DOM.setFileInputFiles',{nodeId:input.nodeId,files:[path.resolve(file)]});};
    await upload(process.argv[4]);await waitFor('document.querySelector("#indexOverlay").hidden && document.querySelector(\'.grid-cell[data-ref="A2"]\')?.textContent==="12"');
    await layoutSize('row',0);await cdp('Page.reload');await waitFor('document.querySelector("#indexOverlay").hidden && document.querySelector(\'.grid-cell[data-ref="A2"]\')?.getBoundingClientRect().height===40');
    let native=await evaluate(`(()=>{const cell=document.querySelector('.grid-cell[data-ref="A2"]');return {height:cell.getBoundingClientRect().height,width:cell.getBoundingClientRect().width,border:getComputedStyle(cell).borderRightWidth};})()`);
    assert.equal(native.height,40);assert.equal(native.width,140);assert.equal(native.border,'0px');
    await layoutSize('row',55);
    await cdp('Page.reload');await waitFor('document.querySelector("#indexOverlay").hidden && document.querySelector(\'.grid-cell[data-ref="A2"]\')?.getBoundingClientRect().height===55');
    checks.push('OOXML uses the shared source sizes and gridline setting; saved user row override wins');await screenshot('xlsx-source-sizes');
    if(process.argv[5]){await upload(process.argv[5]);await waitFor('document.querySelector("#indexOverlay").hidden && document.querySelector(\'.grid-cell[data-ref="A2"]\')?.textContent==="plain"');
      const plain=await evaluate(`(()=>{const cell=document.querySelector('.grid-cell[data-ref="A2"]');return {height:cell.getBoundingClientRect().height,width:cell.getBoundingClientRect().width,border:getComputedStyle(cell).borderRightWidth};})()`);
      assert.equal(plain.height,25);assert.equal(plain.width,132);assert(parseFloat(plain.border)>0);checks.push('CSV restores default geometry and gridlines without leaking prior workbook source dimensions');
    }
  }
  await cdp('Emulation.setEmulatedMedia',{features:[{name:'prefers-reduced-motion',value:'reduce'}]});await click('#calculatorDock');
  assert.equal(await evaluate('document.querySelector("#calculatorWindow").getAnimations().length'),0);assert.equal(await evaluate('getComputedStyle(document.querySelector("#calculatorDock")).transitionDuration'),'0s');
  await click('#calculatorClose');await cdp('Emulation.setEmulatedMedia',{features:[{name:'prefers-reduced-motion',value:'no-preference'}]});await click('#calculatorOpenBtn');assert(await evaluate('document.querySelector("#calculatorWindow").getAnimations().length>0'));checks.push('Controls share cancellable microanimations and reduced-motion disables them');
  const axisFixture=path.join(privateOut,'header-picks.csv');fs.writeFileSync(axisFixture,'A,B,C\n1,10,100\n2,20,200\n3,30,300\n');
  const axisDoc=await cdp('DOM.getDocument'),axisInput=await cdp('DOM.querySelector',{nodeId:axisDoc.root.nodeId,selector:'#fileInput'});await cdp('DOM.setFileInputFiles',{nodeId:axisInput.nodeId,files:[axisFixture]});await waitFor('document.querySelector("#indexOverlay").hidden && document.querySelector(\'.grid-cell[data-ref="C2"]\')?.textContent==="100"');await cdp('Page.reload');await waitFor('document.querySelector("#indexOverlay").hidden && document.querySelector(\'.grid-cell[data-ref="C2"]\')?.textContent==="100"');await click('#calculatorOpenBtn');
  const checkPick=async(expression,want)=>{assert.equal(await evaluate('document.querySelector("#calculatorFormula0").value'),expression);assert.equal(await evaluate('document.activeElement.id'),'calculatorFormula0');await enter(0);await waitFor(`document.querySelector('[data-result-row="0"]')?.textContent===${JSON.stringify(want)}`);};
  await type(0,'=SUM(');await pointerClick('#columnHeaders .column-head[data-col="2"]');await checkPick('=SUM(B:B','60');
  await type(0,'=SUM(');await pointerClick('.grid-row[data-row="2"] .row-head');await checkPick('=SUM(2:2','111');
  await type(0,'=SUM(');await pointerDrag('#columnHeaders .column-head[data-col="1"]','#columnHeaders .column-head[data-col="3"]');await checkPick('=SUM(A:C','666');
  await type(0,'=SUM(');await pointerClick('#columnHeaders .column-head[data-col="1"]');await pointerClick('#columnHeaders .column-head[data-col="3"]',true);await checkPick('=SUM(A:C','666');
  await click('#calculatorMinimize');await click('#freezeBtn');await evaluate(`document.querySelector('#freezeRowsInput').value='2';document.querySelector('#freezeColsInput').value='1';true`);await click('#freezeApplyBtn');await waitFor('!!document.querySelector(\'.pinned-row-head[data-row="2"]\')');await click('#calculatorDock');
  await type(0,'=SUM(');await pointerClick('.pinned-head[data-col="1"]');await checkPick('=SUM(A:A','6');
  await type(0,'=SUM(');await pointerClick('.pinned-row-head[data-row="2"]');await checkPick('=SUM(2:2','111');
  await type(0,'=SUM(');await pointerClick('.grid-row[data-row="3"] .row-head');await checkPick('=SUM(3:3','222');
  await type(0,'=SUM(');const grip=await pointerPosition('.pinned-head[data-col="1"] .column-resize-grip');await cdp('Input.dispatchMouseEvent',{type:'mousePressed',...grip,button:'left',clickCount:1});await cdp('Input.dispatchMouseEvent',{type:'mouseMoved',x:grip.x+20,y:grip.y,buttons:1});await cdp('Input.dispatchMouseEvent',{type:'mouseReleased',x:grip.x+20,y:grip.y,button:'left',clickCount:1});assert.equal(await evaluate('document.querySelector("#calculatorFormula0").value'),'=SUM(');
  checks.push('Actual mouse column/row headers insert B:B and 2:2 without losing formula focus; drag and Shift select A:C; ordinary and frozen headers calculate correct source sums; resize grips do not insert references');await screenshot('whole-axis-picks');
  assert.deepEqual(errors,[]);assert.equal(sourceHash(),sourceBefore);const result={version,result:'PASS',browser:'isolated headless Chrome',server:'real Go application HTTP API',checks,sourceSHA256:sourceBefore,sourceHashUnchanged:true,grid:{rowHeight:grid.rowHeight,blankGridlinePixels:0,headerVertical:grid.headerVertical,dataVertical:grid.vertical},borderScenarios:borderScenarios.length,virtual,privateScreenshotsExcluded:true};
  fs.writeFileSync(path.join(out,'browser-qa.json'),JSON.stringify(result,null,2)+'\n');console.log(JSON.stringify(result,null,2));
 } catch(error){try{await screenshot('failure');fs.writeFileSync(path.join(privateOut,'failure.json'),JSON.stringify(await evaluate(`({rows:[...document.querySelectorAll('[data-formula-row]')].map(n=>({text:n.value,result:n.closest('.calculator-row').querySelector('.calculator-value').textContent})),zoom:document.querySelector('#zoomValue').textContent})`),null,2));}catch{}throw error;} finally {
  if(socket?.readyState===1){try{await cdp('Browser.close');}catch{}socket.close();}
  child.kill();await sleep(600);
  if(path.dirname(profile)===path.resolve(os.tmpdir())){try{fs.rmSync(profile,{recursive:true,force:true,maxRetries:3,retryDelay:100});}catch{}}
 }
})().catch(error=>{console.error(error);process.exitCode=1;});
