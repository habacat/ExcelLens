const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const source=fs.readFileSync(__dirname+'/app.js','utf8'),tick=()=>new Promise(resolve=>setImmediate(resolve));
function fixture(){
  const nodes=new Map(),closed=[],errors=[],context={$:id=>nodes.get(id),Boolean,Math,Promise,closeDialog:id=>closed.push(id),toast:(...args)=>errors.push(args)};
  const helpers=source.slice(source.indexOf('  function buttonCommand('),source.indexOf('  const ACTIONS = ['));
  const run=source.slice(source.indexOf('  function runCommand('),source.indexOf('  function openCommandPalette('));
  vm.runInNewContext(helpers+run+';this.commands={buttonCommand,commandUnavailable,commandWindow,runCommand};',context);
  return {...context.commands,nodes,closed,errors};
}
test('a control folded out of the toolbar retains its original command action',()=>{
  const h=fixture();let calls=0;h.nodes.set('rowsBtn',{hidden:false,disabled:false,dataset:{commandOnly:'',toolbarOverflow:''},click(){calls++;}});
  const action=h.buttonCommand('行与大纲','rowsBtn');assert.equal(h.commandUnavailable(action),false);action.run();assert.equal(calls,1);
});
test('commands preserve hidden and disabled source-capability guards',()=>{
  for(const unavailable of [{hidden:true,disabled:false},{hidden:false,disabled:true}]){
    const h=fixture();let calls=0;h.nodes.set('source', {...unavailable,click(){calls++;}});const action=h.buttonCommand('源操作','source');
    assert(h.commandUnavailable(action));h.runCommand(action);action.run();assert.equal(calls,0);assert.equal(h.closed.length,0);
  }
});
test('command browsing always renders the selected result beyond the first thirty',()=>{
  const h=fixture();for(const total of [0,1,30,60,80,32768])for(const index of [0,1,29,30,59,60,total-1].filter(i=>i>=0&&i<total)){
    const start=h.commandWindow(total,index,60);assert(start>=0);assert(start<=index&&index<start+60);assert(start<=Math.max(0,total-60));
  }
});
test('synchronous command failures are reported without an uncaught UI exception',async()=>{
  const h=fixture();h.runCommand({run(){throw Error('read failed');}});await tick();assert.deepEqual(h.closed,['commandDialog']);assert.deepEqual(h.errors,[['read failed','error']]);
});
