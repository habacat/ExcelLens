// Default to the offline clipboard regressions. The native runner uses the
// browser mode and acknowledges each copy before advancing to the next one.
const path = require('node:path');
const readline = require('node:readline');
const {launch} = require('./browser_harness.cjs');

async function key(page, key, code, vk, modifiers=0) {
  await page.call('Input.dispatchKeyEvent', {type:'keyDown',key,code,windowsVirtualKeyCode:vk,modifiers});
  await page.call('Input.dispatchKeyEvent', {type:'keyUp',key,code,windowsVirtualKeyCode:vk});
}

async function select(page, address) {
  await page.run(address => {
    const box=document.getElementById('nameBox');
    box.focus(); box.value=address;
  }, address);
  await key(page,'Enter','Enter',13);
  await page.wait('document.activeElement===grid || document.activeElement===editGridInput');
}

async function main() {
  const errors=[], harness=await launch({errors,systemClipboard:true}), page=harness.page;
  let source;
  try {
    await page.call('Emulation.setFocusEmulationEnabled',{enabled:true});
    for await (const line of readline.createInterface({input:process.stdin,crlfDelay:Infinity})) {
      const request=JSON.parse(line);
      if (request.action==='stop') break;
      if (request.action==='select') {
        if (source!==request.file) {
          if (await page.evaluate('document.body.classList.contains("editing-copy")')) {
            await page.click('#editGridExit');
            await page.wait('!document.body.classList.contains("editing-copy")');
          }
          source=request.file;
          await page.file('#fileInput',source);
          await page.wait('fileBadge.textContent==='+JSON.stringify(path.basename(source))+
            ' && window.ExcelLensPerformance().hasVisibleCoverage && !window.ExcelLensPerformance().sheetLoading');
        }
        if (request.edit && !await page.evaluate('document.body.classList.contains("editing-copy")')) {
          await page.click('#editCopyOpen');
          await page.wait('document.body.classList.contains("editing-copy") && !editGridSave.disabled');
        }
        await select(page,request.sheet+'!'+request.address);
        if (request.value!==undefined) {
          await key(page,'F2','F2',113);
          await page.wait('editGridInput.classList.contains("writing")');
          await page.evaluate('editGridInput.select()');
          await page.call('Input.insertText',{text:request.value});
          await key(page,'Enter','Enter',13);
          await page.wait('!editGridInput.classList.contains("writing") && editCopyRevision.textContent.startsWith("修订 2 ")');
          await select(page,request.sheet+'!'+request.address);
        }
        if (request.history) {
          await page.click(request.history==='undo'?'#editGridUndo':'#editGridRedo');
          await page.wait('editCopyRevision.textContent.startsWith('+JSON.stringify('修订 '+request.revision+' ')+') && !editGridSave.disabled');
          await select(page,request.sheet+'!'+request.address);
        }
        if (request.mode==='special') {
          await key(page,'C','KeyC',67,11);
          await page.wait('copySpecialDialog.open&&!copySpecialWrite.disabled');
        } else if (request.mode==='menu') {
          const point=await page.run(ref=>{
            const node=[...document.querySelectorAll('.grid-cell[data-ref="'+ref+'"]')].find(n=>n.offsetWidth);
            const rect=node.getBoundingClientRect();
            return {x:rect.left+rect.width/2,y:rect.top+rect.height/2};
          },request.address.split(':')[0]);
          for (const type of ['mousePressed','mouseReleased']) {
            await page.call('Input.dispatchMouseEvent',{type,...point,button:'right',buttons:type==='mousePressed'?2:0,clickCount:1});
          }
          await page.wait('!cellMenu.hidden');
        }
        console.log(JSON.stringify({address:await page.evaluate('nameBox.value')}));
        continue;
      }
      if (request.action!=='copy') throw Error('Unknown probe command');
      await page.run(mode=>{
        if (mode==='fallback') Object.defineProperty(navigator.clipboard,'write',{configurable:true,value:undefined});
        window.qaCopyDone=false;
        const node=document.getElementById('toast');
        const observer=new MutationObserver(()=>{window.qaCopyDone=node.textContent;observer.disconnect();});
        observer.observe(node,{childList:true});
      },request.mode);
      if (request.mode==='special') await page.click('#copySpecialWrite');
      else if (request.mode==='menu') await page.click('#menuCopyValue');
      else await key(page,'c','KeyC',67,2);
      await page.wait('window.qaCopyDone');
      const copied=await page.run(mode=>{
        if (mode==='fallback') delete navigator.clipboard.write;
        return {toast:window.qaCopyDone};
      },request.mode);
      if (errors.length) throw Error(JSON.stringify(errors));
      console.log(JSON.stringify(copied));
    }
  } catch (error) {
    const state=await page.evaluate('({file:fileBadge.textContent,selection:nameBox.value,focus:document.activeElement.id,toast:toast.textContent})').catch(()=>null);
    console.log(JSON.stringify({probeError:error.message,state}));
    throw error;
  } finally {
    process.stdin.pause();
    await harness.close();
  }
}
if (process.argv.includes('--excel')) {
  main().catch(error=>{console.error(error);process.exit(1);});
} else {
  const tests=['clipboard','merges','clipboard-special','clipboard-panels','richtext'];
  const paths=tests.map(name=>path.resolve(__dirname,'../../internal/ui/web',name+'.test.cjs'));
  const result=require('node:child_process').spawnSync(process.execPath,['--test',...paths],{stdio:'inherit'});
  process.exitCode=result.status??1;
}
