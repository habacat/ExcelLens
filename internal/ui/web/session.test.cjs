const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const source=fs.readFileSync(__dirname+'/session.js','utf8');
function bus(){
  const channels=[],queue=[],timers=new Map();let serial=0,timer=0;
  function page({launch='',stored='',blocked=false}={}){
    const events={},values=new Map([['ExcelLens.tabSession.v1',stored]]);
    class Channel {
      constructor(){if(blocked)throw Error('blocked');channels.push(this);}
      postMessage(data){for(const peer of channels)if(peer!==this&&!peer.closed)queue.push(()=>peer.onmessage?.({data}));}
      close(){this.closed=true;}
    }
    const ctx={window:{location:{href:'http://localhost/'+(launch?'?session='+launch:'')},addEventListener:(name,fn)=>events[name]=fn},
      crypto:{randomUUID:()=>String(++serial).padStart(24,'0')},BroadcastChannel:Channel,URL,history:{replaceState(){}},
      sessionStorage:{getItem:key=>values.get(key),setItem:(key,value)=>values.set(key,value)},
      setTimeout:fn=>{timers.set(++timer,fn);return timer;},clearTimeout:id=>timers.delete(id)};
    vm.runInNewContext(source,ctx);return {session:ctx.window.ExcelLensSession,events,values};
  }
  function flush(){let count=0;while(queue.length){if(++count>1000)throw Error('unstable session negotiation');queue.shift()();}for(const [id,fn]of [...timers]){if(timers.delete(id))fn();}}
  return {page,flush};
}
test('native launch owns its workbook and a duplicated tab negotiates a different session',async()=>{
  const b=bus(),id='native-session-1234567890',native=b.page({launch:id});await native.session.ready;
  const duplicate=b.page({stored:id});b.flush();await duplicate.session.ready;
  assert.equal(native.session.id,id);assert.notEqual(duplicate.session.id,id);
  const headers=new Headers();duplicate.session.apply(headers);assert.equal(headers.get('X-ExcelLens-Session'),duplicate.session.id);
});
test('simultaneous normal duplicates separate while a reload after pagehide preserves its session',async()=>{
  const b=bus(),id='stored-session-1234567890',a=b.page({stored:id}),c=b.page({stored:id});b.flush();await Promise.all([a.session.ready,c.session.ready]);
  assert.notEqual(a.session.id,c.session.id);
  a.events.pagehide();const reload=b.page({stored:a.session.id});b.flush();await reload.session.ready;assert.equal(reload.session.id,a.session.id);
});
test('an unavailable BroadcastChannel does not prevent the page bootstrap',async()=>{
  const b=bus(),id='stored-session-1234567890',a=b.page({stored:id,blocked:true});await a.session.ready;assert.equal(a.session.id,id);
});
