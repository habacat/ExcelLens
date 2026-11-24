const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const source=fs.readFileSync(__dirname+'/session.js','utf8');
function bus(){
  const channels=[],queue=[],timers=new Map();let serial=0,timer=0;
  function page({launch='',stored='',blocked=false,respond}={}){
    const events={},documentEvents={},intervals=new Map(),requests=[],values=new Map([['ExcelLens.tabSession.v1',stored]]);
    class Channel {
      constructor(){if(blocked)throw Error('blocked');channels.push(this);}
      postMessage(data){for(const peer of channels)if(peer!==this&&!peer.closed)queue.push(()=>peer.onmessage?.({data}));}
      close(){this.closed=true;}
    }
    const ctx={window:{location:{replace(){},href:'http://localhost/'+(launch?'?session='+launch:'')},addEventListener:(name,fn)=>events[name]=fn},
      fetch:async(url,options)=>{requests.push({url,options});return respond ? respond(url) : {ok:true,status:204};},Headers,setInterval:fn=>{intervals.set(++timer,fn);return timer;},clearInterval:key=>intervals.delete(key),
      document:{addEventListener:(name,fn)=>documentEvents[name]=fn,querySelector:()=>({content:'token'}),documentElement:{dataset:{cacheEpoch:'3'}}},crypto:{randomUUID:()=>String(++serial).padStart(24,'0')},BroadcastChannel:Channel,URL,history:{replaceState(){}},
      sessionStorage:{getItem:key=>values.get(key),setItem:(key,value)=>values.set(key,value)},
      setTimeout:fn=>{timers.set(++timer,fn);return timer;},clearTimeout:id=>timers.delete(id)};
    vm.runInNewContext(source,ctx);return {session:ctx.window.ExcelLensSession,events,documentEvents,intervals,values,requests};
  }
  function flush(){let count=0;while(queue.length){if(++count>1000)throw Error('unstable session negotiation');queue.shift()();}for(const [id,fn]of [...timers]){if(timers.delete(id))fn();}}
  return {page,flush};
}
test('native launch owns its workbook and a duplicated tab negotiates a different session',async()=>{
  const b=bus(),id='native-session-1234567890',native=b.page({launch:id});await native.session.ready;
  const duplicate=b.page({stored:id});b.flush();await duplicate.session.ready;
  assert.equal(native.session.id,id);assert.notEqual(duplicate.session.id,id);
  const headers=new Headers();duplicate.session.apply(headers);assert.equal(headers.get('X-ExcelLens-Session'),duplicate.session.id);
  assert.equal(headers.get('X-ExcelLens-Cache-Epoch'),'3');
});
test('simultaneous normal duplicates separate while a reload after pagehide preserves its session',async()=>{
  const b=bus(),id='stored-session-1234567890',a=b.page({stored:id}),c=b.page({stored:id});b.flush();await Promise.all([a.session.ready,c.session.ready]);
  assert.notEqual(a.session.id,c.session.id);
  a.events.pagehide();const reload=b.page({stored:a.session.id});b.flush();await reload.session.ready;assert.equal(reload.session.id,a.session.id);
});
test('an unavailable BroadcastChannel does not prevent the page bootstrap',async()=>{
  const b=bus(),id='stored-session-1234567890',a=b.page({stored:id,blocked:true});await a.session.ready;assert.equal(a.session.id,id);
});

test('closing releases only the attached document and keeps the request authenticated',async()=>{
  const b=bus(),a=b.page({blocked:true});await a.session.ready;
  a.events.pagehide();
  assert.deepEqual(a.requests.map(x=>x.url),['/api/session/attach','/api/session/detach']);
  const [attach,detach]=a.requests.map(x=>x.options);
  assert.equal(detach.keepalive,true);
  for(const key of ['Token','Session','Page','Cache-Epoch']) assert.equal(detach.headers.get('X-ExcelLens-'+key),attach.headers.get('X-ExcelLens-'+key));
});

test('heartbeats stop while away and BFCache restoration claims a newer generation',async()=>{
  const b=bus(),a=b.page({blocked:true});await a.session.ready;
  await [...a.intervals.values()][0]();
  a.documentEvents.visibilitychange();
  const initial=a.requests[0].options.headers;
  assert.equal(a.requests.filter(x=>x.url.endsWith('heartbeat')).length,2);
  a.events.pagehide();assert.equal(a.intervals.size,0);
  a.events.pageshow({persisted:true});await a.session.ready;
  const renewed=a.requests.at(-1).options.headers;
  assert.equal(renewed.get('X-ExcelLens-Session'),initial.get('X-ExcelLens-Session'));
  assert.notEqual(renewed.get('X-ExcelLens-Page'),initial.get('X-ExcelLens-Page'));
  assert.equal(renewed.get('X-ExcelLens-Resume'),'1');
  assert.ok(Number(renewed.get('X-ExcelLens-Generation'))>Number(initial.get('X-ExcelLens-Generation')));
});
test('closing while attach is in flight sends a release before the response',async()=>{
  let started,finish;
  const pending=new Promise(resolve=>started=resolve);
  const a=bus().page({blocked:true, respond:url=>{
    if(url.endsWith('attach')){started();return new Promise(resolve=>finish=resolve);}
    return {ok:true,status:204};
  }});
  const ready=a.session.ready;
  await pending;a.events.pagehide();
  assert.equal(a.requests.at(-1).url,'/api/session/detach');
  finish({ok:true,status:204});await assert.rejects(ready,/页面已关闭/);
  assert.equal(a.intervals.size,0);
});

test('a confirmed full reset opens a fresh home instead of resuming the retired tab',async()=>{
  const b=bus(),a=b.page({stored:'stored-session-1234567890',blocked:true});
  await a.session.ready;
  a.session.returnHomeAfterCacheReset();
  const stored=a.values.get('ExcelLens.tabSession.v1'),home=b.page({stored,blocked:true});
  assert.equal(stored,'');assert.equal(home.session.restoring,false);assert.notEqual(home.session.id,a.session.id);
});
