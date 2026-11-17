/* Browser isolation: reload restores the SAME workbook, a new tab opens a
 * fresh workspace. Duplicated tabs inherit sessionStorage in some browsers;
 * the BroadcastChannel ownership handshake detects that otherwise subtle
 * collision before the first workbook request is issued. */
(() => {
  'use strict';
  const key='ExcelLens.tabSession.v1';
  const valid=value=>/^[A-Za-z0-9_-]{20,64}$/.test(value||'');
  const makeID=()=>crypto.randomUUID?.()||Array.from(crypto.getRandomValues(new Uint8Array(20)),x=>x.toString(16).padStart(2,'0')).join('');
  const url=new URL(window.location.href);
  const launch=url.searchParams.get('session');
  let stored='';
  try{stored=sessionStorage.getItem(key)||''}catch{}
  let id=valid(launch)?launch:(valid(stored)?stored:makeID());
  const nonce=makeID();
  const remember=()=>{try{sessionStorage.setItem(key,id)}catch{}};
  remember();
  if(valid(launch)){
    url.searchParams.delete('session');
    history.replaceState(history.state,'',url.pathname+url.search+url.hash);
  }
  let ready=Promise.resolve();
  let channel;
  try{if(typeof BroadcastChannel==='function')channel=new BroadcastChannel('ExcelLens.workbookTabs.v1');}catch{}
  if(channel){
    ready=new Promise(resolve=>{
      // A native launch owns its pre-created server session, but must still
      // answer probes from duplicated tabs inheriting sessionStorage.
      let claimed=valid(launch),timer=null;
      const settle=()=>{claimed=true;resolve()};
      const probe=()=>{
        channel.postMessage({kind:'probe',id,nonce});
        clearTimeout(timer);timer=setTimeout(settle,125);
      };
      const rotate=()=>{if(claimed)return;id=makeID();remember();probe()};
      channel.onmessage=event=>{
        const msg=event.data;
        if(!msg||msg.id!==id||msg.nonce===nonce)return;
        if(msg.kind==='probe'){
          if(claimed||nonce<msg.nonce){
            channel.postMessage({kind:'occupied',id,nonce,target:msg.nonce});
          }else rotate();
        }else if(msg.kind==='occupied'&&msg.target===nonce&&!claimed)rotate();
      };
      if(claimed)resolve();else probe();
      window.addEventListener('pagehide',()=>{clearTimeout(timer);channel.close()},{once:true});
    });
  }
  window.ExcelLensSession=Object.freeze({
    get id(){return id},
    ready,
    apply(headers){headers.set('X-ExcelLens-Session',id);return headers;}
  });
})();
