// Current production toolbar, actual browser geometry and command actions.
const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict'),crypto=require('node:crypto');
const config=require('./project_config.cjs'),{launch}=require('./browser_harness.cjs'),{zip}=require('./generated_workbook.cjs');
const root=path.resolve(__dirname,'../..'),base=path.join(root,'validation',config.validationVersion()),phase=process.env.QA_TOOLBAR_PHASE||'after';assert(['before','after'].includes(phase));
const out=path.join(base,'private','toolbar',phase);fs.mkdirSync(out,{recursive:true});
const source=path.join(out,'较长的工作簿名称用于验证窄屏文件标识.xlsx'),ns='http://schemas.openxmlformats.org/spreadsheetml/2006/main',rel='http://schemas.openxmlformats.org/officeDocument/2006/relationships';
const sheets=Array.from({length:80},(_,i)=>'工具测试'+String(i+1).padStart(2,'0'));
let rows='';for(let row=1;row<=40;row++)rows+=`<row r="${row}"><c r="A${row}" t="inlineStr"><is><t>记录${row}</t></is></c><c r="B${row}"><v>${row}.25</v></c></row>`;
const files={'xl/workbook.xml':`<workbook xmlns="${ns}" xmlns:r="${rel}"><sheets>${sheets.map((s,i)=>`<sheet name="${s}" sheetId="${i+1}" r:id="s${i+1}"/>`).join('')}</sheets></workbook>`,
  'xl/_rels/workbook.xml.rels':`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">${sheets.map((_,i)=>`<Relationship Id="s${i+1}" Type="${rel}/worksheet" Target="worksheets/sheet${i+1}.xml"/>`).join('')}</Relationships>`};
for(let i=0;i<sheets.length;i++)files[`xl/worksheets/sheet${i+1}.xml`]=`<worksheet xmlns="${ns}"><sheetData>${rows}</sheetData></worksheet>`;
fs.writeFileSync(source,zip(files));const hash=p=>crypto.createHash('sha256').update(fs.readFileSync(p)).digest('hex'),before=hash(source);
const report={version:config.version(),phase,result:'FAIL',checks:[],cases:[],errors:[],screenshots:[],desktopManualAcceptance:false,officeWpsAcceptance:false};let h;
const ready='window.ExcelLensPerformance?.().hasVisibleCoverage&&!window.ExcelLensPerformance().sheetLoading';
async function settled(){await h.page.evaluate('new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))');}
async function shot(name){const output=path.join(out,name+'.png');await h.page.shot(output);report.screenshots.push(path.relative(root,output).replaceAll('\\','/'));}
async function key(key,code,number,modifiers=0){await h.page.call('Input.dispatchKeyEvent',{type:'keyDown',key,code,windowsVirtualKeyCode:number,modifiers});await h.page.call('Input.dispatchKeyEvent',{type:'keyUp',key,code,windowsVirtualKeyCode:number,modifiers});}
async function palette(){await h.page.click('#commandOpenBtn');await h.page.wait('document.getElementById("commandDialog").open');}
async function command(button){await palette();await h.page.run(button=>{const action=[...document.querySelectorAll('#commandList .command-entry')].find(n=>n.dataset.commandButton===button);if(!action||action.disabled)throw Error('Unavailable command '+button);action.click();},button);}
(async()=>{try{
  h=await launch({out,errors:report.errors,duration:'12m'});const p=h.page;await p.file('#fileInput',source);await p.wait(ready);await settled();
  await p.run(()=>{const fetch=window.fetch.bind(window);window.__toolbarReads=0;window.fetch=(url,...args)=>{if(/\/api\/(?:index\/start|analysis\/start|row-view\/start|profile)/.test(String(url)))window.__toolbarReads++;return fetch(url,...args);};});
  for(const [width,height,dpr] of [[1600,1000,1],[1366,820,1.25],[1024,768,1.5],[900,760,1],[760,760,2],[640,760,1.25],[420,760,2],[320,760,1]]){
    await p.call('Emulation.setDeviceMetricsOverride',{width,height,deviceScaleFactor:dpr,mobile:false});await settled();await p.wait(ready);
    const result=await p.run(()=>{
      const bar=document.querySelector('.appbar'),bounds=bar.getBoundingClientRect();
      const visible=[...bar.querySelectorAll('.app-actions > button')].filter(n=>n.getBoundingClientRect().width>0).map(n=>{const r=n.getBoundingClientRect(),hit=document.elementFromPoint(r.x+r.width/2,r.y+r.height/2);return {id:n.id,left:r.left,right:r.right,top:r.top,bottom:r.bottom,height:r.height,font:parseFloat(getComputedStyle(n).fontSize),hit:hit?.closest('button')?.id};});
      const formula=document.querySelector('.formula-toolbar'),formulaBounds=formula.getBoundingClientRect(),formulaControls=[...formula.querySelectorAll('input,select,button,[role="textbox"]')].filter(n=>n.getBoundingClientRect().width>0).map(n=>{const r=n.getBoundingClientRect();return {id:n.id,left:r.left,right:r.right};});
      return {width:innerWidth,bar:{left:bounds.left,right:bounds.right,height:bounds.height},visible,formulaControls,formulaBounds:{left:formulaBounds.left,right:formulaBounds.right},reads:window.__toolbarReads};
    });report.cases.push({...result,dpr});
    const common=['openBtn','searchOpenBtn','filterOpenBtn','multiSortBtn','exportOpenBtn','analysisOpenBtn','commandOpenBtn'];
    for(const id of common)assert(result.visible.some(n=>n.id===id),'Missing common action '+id+' at '+width);
    for(const node of result.visible){assert(node.left>=-0.5&&node.right<=width+0.5,`${node.id} clipped at ${width}`);assert(node.top>=-0.5&&node.bottom<=result.bar.height+0.5);assert.equal(node.hit,node.id,'Action must receive pointer input');assert(node.height>=26&&node.font>=11);}
    for(const node of result.formulaControls)assert(node.left>=-0.5&&node.right<=width+0.5,'Formula control clipped: '+node.id+' at '+width);
    assert.equal(result.reads,0,'Toolbar resizing must not restart indexing or analysis');
    if([1024,420,320].includes(width))await shot('toolbar-'+width);
  }
  report.checks.push('Eight screen widths and four DPR values: common actions and formula controls fit without smaller text or new source tasks');
  await p.call('Emulation.setDeviceMetricsOverride',{width:1600,height:1000,deviceScaleFactor:1,mobile:false});await p.run(()=>document.querySelector('.appbar').style.width='620px');await settled();
  assert(await p.evaluate('[...document.querySelectorAll(".appbar .app-actions > button")].filter(n=>n.getBoundingClientRect().width).every(n=>n.getBoundingClientRect().right<=620.5)'));
  await p.run(()=>document.querySelector('.appbar').style.removeProperty('width'));await settled();report.checks.push('Actual container resize independently of the window recomputes toolbar visibility');
  await palette();
  const coverage=await p.run(()=>({low:[...document.querySelectorAll('.appbar [data-command-only]')].map(n=>n.id),commands:[...document.querySelectorAll('#commandList .command-entry')].map(n=>({button:n.dataset.commandButton,disabled:n.disabled}))}));
  for(const id of coverage.low)assert(coverage.commands.some(n=>n.button===id),'Missing folded command '+id);
  assert(coverage.commands.find(n=>n.button==='imagesBtn').disabled);await p.run(()=>document.getElementById('commandDialog').close());
  for(const [button,dialog] of [['reconcileOpenBtn','reconcileDialog'],['relationsOpenBtn','relationsDialog'],['namedViewOpenBtn','namedViewDialog'],['rowsBtn','rowsDialog'],['freezeBtn','freezeDialog'],['recordCardOpenBtn','recordCardDialog'],['cellDetailBtn','detailDialog']]){
    await command(button);await p.wait(`document.getElementById('${dialog}')?.open`);await p.run(id=>document.getElementById(id).close(),dialog);
  }
  report.checks.push('All sixteen folded actions remain registered; source-unavailable commands stay disabled; seven commands open their actual controls');
  await palette();await p.run(()=>{const input=document.getElementById('commandSearch');input.value='工具测试';input.dispatchEvent(new Event('input',{bubbles:true}));});
  for(let i=0;i<79;i++)await key('ArrowDown','ArrowDown',40);
  assert.equal(await p.evaluate('document.querySelector("#commandList .active").textContent'),'打开 Sheet：工具测试80');await key('Enter','Enter',13);await p.wait(ready+'&&document.querySelector(".sheet-tab.active").textContent==="工具测试80"');
  report.checks.push('Keyboard browsing beyond the first thirty and sixty entries visibly selects and opens the eightieth worksheet');
  report.sourceUnchanged=hash(source)===before;assert(report.sourceUnchanged);report.sourceSHA256=before;report.assets={};for(const name of ['toolbar.js','style.css','app.js','index.html'])report.assets[name]=hash(path.join(root,'internal/ui/web',name));assert.deepEqual(report.errors,[]);report.result='PASS';
}catch(error){report.errors.push(error.stack||String(error));if(h)try{await shot('failure');}catch{}}
finally{if(h)await h.close();fs.writeFileSync(path.join(base,`toolbar-ui-${phase}.json`),JSON.stringify(report,null,2)+'\n');console.log(JSON.stringify({result:report.result,phase,checks:report.checks,lastCase:report.cases.at(-1),errors:report.errors},null,2));if(report.result!=='PASS')process.exitCode=1;}})();
