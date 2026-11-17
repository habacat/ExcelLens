/* Simulate Chrome's duplicated-tab sessionStorage cloning and a native
 * ?session= launch, independently of HTTP/OS-specific browser policies. */
const fs=require('node:fs'),vm=require('node:vm'),assert=require('node:assert/strict'),path=require('node:path');
const source=fs.readFileSync(path.resolve(__dirname,'../../internal/ui/web/session.js'),'utf8');
let serial=0;
const channels=new Set();
class Channel {
  constructor(name){this.name=name;this.onmessage=null;channels.add(this)}
  postMessage(data){for(const c of channels){if(c!==this&&c.name===this.name)setTimeout(()=>c.onmessage?.({data}),0)}}
  close(){channels.delete(this)}
}
function page(stored='',launch=''){
  const storage=new Map(stored?[['ExcelLens.tabSession.v1',stored]]:[]);
  const listeners={};const history={replaceState(_state,_unused,url){this.last=url}};
  const window={location:{href:'http://127.0.0.1:22222/'+(launch?'?session='+launch:'')},addEventListener(event,fn){listeners[event]=fn}};
  const context={window,sessionStorage:{getItem:key=>storage.get(key)||null,setItem:(key,val)=>storage.set(key,val)},BroadcastChannel:Channel,crypto:{randomUUID:()=>`40000000-0000-4000-8000-${String(++serial).padStart(12,'0')}`},history,URL,setTimeout,clearTimeout};
  vm.runInNewContext(source,context);
  return {window,storage,listeners,history};
}
(async()=>{
  const original=page();await original.window.ExcelLensSession.ready;
  const id=original.window.ExcelLensSession.id;
  const duplicate=page(id);await duplicate.window.ExcelLensSession.ready;
  assert.notEqual(duplicate.window.ExcelLensSession.id,id,'cloned session must rotate');
  assert.equal(original.window.ExcelLensSession.id,id,'original session must be stable');
  const explicit=page(id,'70000000-0000-4000-8000-123456789012');await explicit.window.ExcelLensSession.ready;
  assert.equal(explicit.window.ExcelLensSession.id,'70000000-0000-4000-8000-123456789012');
  assert.equal(explicit.history.last,'/');
  original.listeners.pagehide?.();duplicate.listeners.pagehide?.();
  console.log('PASS: independent tabs, duplicate collision handshake, native explicit launch');
})().catch(e=>{console.error(e);process.exit(1)});
