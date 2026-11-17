/* A page's input journal is separate from source files and saved analysis data.
 * Only a synced server receipt can clear its matching pending input. */
(() => {
  'use strict';
  const bytes=text=>new TextEncoder().encode(text).length;
  const makeID=()=>crypto.randomUUID?.()||Array.from(crypto.getRandomValues(new Uint8Array(20)),n=>n.toString(16).padStart(2,'0')).join('');
  const validID=id=>/^[A-Za-z0-9_-]{20,64}$/.test(id||'');
  class Journal {
    constructor({kind,api,onRestore=()=>{},onStatus=()=>{},prepareCopy=records=>records}){
      Object.assign(this,{kind,api,onRestore,onStatus,prepareCopy});
      this.records=new Map();this.encoded=new Map();this.pending=new Map();this.revision=0;this.owner='';this.inflight=null;this.running=null;this.timer=0;this.error='';this.blocked=false;this.initialized=false;this.loading=true;this.failures=0;
      this.ready=this.initialize();
      window.addEventListener('beforeunload',event=>{if(this.dirty){event.preventDefault();event.returnValue='';this.flush(true);}});
      window.addEventListener('pagehide',()=>{this.flush(true);});
      window.addEventListener('online',()=>{if(this.dirty&&!this.blocked)this.flush();});
    }
    get dirty(){return this.pending.size>0||!!this.inflight;}
    state(){return {dirty:this.dirty,loading:this.loading,saving:!!this.running,error:this.error,blocked:this.blocked,count:this.records.size,revision:this.revision};}
    notify(){this.onStatus(this.state());}
    rememberOwner(){try{sessionStorage.setItem(this.ownerKey,this.owner);}catch{}}
    async initialize(){
      try{
        await window.ExcelLensSession?.ready;
        const session=window.ExcelLensSession?.id||makeID();this.ownerKey='ExcelLens.recovery.'+this.kind+'.'+session;
        let saved='';try{saved=sessionStorage.getItem(this.ownerKey)||'';}catch{}
        this.owner=validID(saved)?saved:session;
        const snapshot=await this.api('/api/recovery?kind='+this.kind+'&owner='+encodeURIComponent(this.owner));
        this.validateSnapshot(snapshot);
        const restored={};
        for(const [key,data] of Object.entries(snapshot.records))if(!this.pending.has(key)){
          this.records.set(key,data);this.encoded.set(key,JSON.stringify(data));restored[key]=data;
        }
        this.onRestore(restored,{automatic:true,recovered:!!snapshot.recovered});
        this.revision=snapshot.revision;this.initialized=true;this.loading=false;this.notify();
        if(this.dirty)this.schedule();
        return true;
      }catch(error){this.loading=false;this.blocked=true;this.error='恢复副本未载入：'+error.message;this.notify();return false;}
    }
    validateSnapshot(snapshot){
      if(!snapshot||!Number.isSafeInteger(snapshot.revision)||snapshot.revision<0||!snapshot.records||Array.isArray(snapshot.records)||typeof snapshot.records!=='object'||Object.keys(snapshot.records).length>20000)throw Error('恢复副本结构无效');
    }
    set(key,data){
      key=String(key);const encoded=data==null?null:JSON.stringify(data);
      if(encoded===(this.encoded.get(key)??null))return;
      const copy=encoded===null?null:JSON.parse(encoded);
      if(copy===null){this.records.delete(key);this.encoded.delete(key);}else{this.records.set(key,copy);this.encoded.set(key,encoded);}
      this.pending.set(key,{key,data:copy});this.notify();this.schedule();
      if(this.capacityBlocked){this.capacityBlocked=false;this.blocked=false;this.error='';this.notify();this.schedule();}
    }
    schedule(delay=400){
      clearTimeout(this.timer);this.timer=0;
      if(!this.initialized||this.blocked||this.running||!this.dirty)return;
      this.timer=setTimeout(()=>{this.timer=0;this.flush();},delay);
    }
    batch(){
      const entries=[];let size=512;
      // Free acknowledged space before adding rows after a capacity rejection.
      const ordered=function*(pending){for(const item of pending.values())if(item.data===null)yield item;for(const item of pending.values())if(item.data!==null)yield item;};
      for(const entry of ordered(this.pending)){
        const n=bytes(JSON.stringify(entry))+1;
        if(entries.length&&(size+n>48*1024||entries.length>=128))break;
        if(n>64*1024){const error=Error('单条恢复内容过长，请缩短公式或备注');error.status=413;throw error;}
        entries.push(entry);size+=n;
      }
      return {entries,request:{owner:this.owner,kind:this.kind,revision:this.revision,requestId:makeID(),changes:entries.map(({key,data})=>({key,data}))}};
    }
    flush(keepalive=false){
      clearTimeout(this.timer);this.timer=0;
      if(this.running)return this.running;
      const work=async()=>{
        await this.ready;
        if(!this.initialized||this.blocked)return false;
        try{
          while(this.pending.size||this.inflight){
            if(!this.inflight)this.inflight=this.batch();
            const sent=this.inflight,body=JSON.stringify(sent.request);
            if(keepalive&&bytes(body)>60*1024)return false;
            const controller=new AbortController(),timeout=setTimeout(()=>controller.abort(),15000);
            let meta;
            try{meta=await this.api('/api/recovery',{method:'POST',headers:{'Content-Type':'application/json'},body,keepalive,signal:controller.signal});}
            finally{clearTimeout(timeout);}
            if(!Number.isSafeInteger(meta.revision)||meta.revision!==sent.request.revision+1)throw Error('恢复保存确认版本无效');
            this.revision=meta.revision;this.failures=0;this.error='';
            for(const entry of sent.entries)if(this.pending.get(entry.key)===entry)this.pending.delete(entry.key);
            this.inflight=null;this.notify();
            if(keepalive)break;
          }
          return !this.dirty;
        }catch(error){
          this.error='恢复未保存：'+error.message;this.failures++;
          this.blocked=[400,409,413,422].includes(error.status);
          if(error.status===413){this.inflight=null;this.capacityBlocked=true;}
          return false;
        }
      };
      this.running=work().finally(()=>{
        this.running=null;this.notify();
        if(this.dirty&&!this.blocked)this.schedule(Math.min(30000,1000*2**Math.min(this.failures,5)));
      });
      this.notify();return this.running;
    }
    async retry(){
      if(!this.initialized){this.blocked=false;this.loading=true;this.ready=this.initialize();return this.ready;}
      if(this.blocked)return false;
      return this.flush();
    }
    async fork(){
      await this.ready;if(this.running)await this.running;
      this.owner=makeID();this.revision=0;this.inflight=null;this.initialized=true;this.loading=false;this.blocked=false;this.capacityBlocked=false;this.error='';this.failures=0;
      this.pending=new Map([...this.records].map(([key,data])=>[key,{key,data}]));this.rememberOwner();this.notify();
      return this.flush();
    }
    async importCopy(entry){
      await this.ready;if(this.running)await this.running;
      const snapshot=await this.api('/api/recovery?kind='+this.kind+'&owner='+encodeURIComponent(entry.owner)+(entry.backup?'&backup=1':''));
      this.validateSnapshot(snapshot);
      const incoming=this.prepareCopy(snapshot.records,this.records),added={};let skipped=0;
      for(const [key,data] of Object.entries(incoming)){
        if(this.records.has(key)){skipped++;continue;}
        added[key]=data;
      }
      // Consumers validate the whole batch before changing their visible model.
      this.onRestore(added,{automatic:false,recovered:!!snapshot.recovered});
      for(const [key,data] of Object.entries(added)){this.records.set(key,data);this.encoded.set(key,JSON.stringify(data));}
      const saved=await this.fork();return {count:Object.keys(added).length,skipped,saved};
    }
  }

  // One dialog for both input surfaces; it uses the app's existing typography
  // and theme variables and shows a preview before the user loads a copy.
  let dialog;
  function show(journal,{title,explanation}){
    if(dialog?.open){dialog.focus();return;}
    const previous=document.activeElement;
    const el=document.createElement('dialog');dialog=el;el.className='recovery-dialog';el.setAttribute('aria-label',title);
    const head=document.createElement('div');head.className='recovery-head';
    const heading=document.createElement('strong');heading.textContent=title;
    const close=document.createElement('button');close.type='button';close.textContent='关闭';close.onclick=()=>el.close();head.append(heading,close);
    const info=document.createElement('p');info.textContent=explanation;
    const status=document.createElement('p');status.className='recovery-status';status.setAttribute('role','status');
    const tools=document.createElement('div');tools.className='recovery-tools';
    const retry=document.createElement('button');retry.type='button';retry.textContent='重试保存';
    const fork=document.createElement('button');fork.type='button';fork.textContent='另存本页副本';tools.append(retry,fork);
    const list=document.createElement('div');list.className='recovery-list';list.textContent='正在读取恢复副本…';
    const preview=document.createElement('pre');preview.className='recovery-preview';preview.hidden=true;
    const load=document.createElement('button');load.type='button';load.className='recovery-load';load.textContent='载入副本';load.hidden=true;
    let selected=null,busy=false,serial=0;
    const message=()=>{status.textContent=journal.error||(journal.dirty?'本页还有未确认输入':journal.records.size?'本页恢复副本已保存':'本页暂无输入');retry.disabled=busy;fork.disabled=busy||!journal.records.size;load.disabled=busy||!selected;};
    const run=async fn=>{if(busy)return;busy=true;message();try{await fn();}catch(error){status.textContent=error.message;}finally{busy=false;retry.disabled=false;fork.disabled=!journal.records.size;load.disabled=!selected;}};
    retry.onclick=()=>run(async()=>{await journal.retry();message();});
    fork.onclick=()=>run(async()=>{await journal.fork();message();});
    load.onclick=()=>run(async()=>{const result=await journal.importCopy(selected);status.textContent='已载入'+result.count+'条'+(result.skipped?'；保留本页同格草稿'+result.skipped+'条':'')+(result.saved?'；恢复副本已保存':'；'+journal.error);});
    el.append(head,info,status,tools,list,preview,load);document.body.append(el);
    el.addEventListener('close',()=>{++serial;el.remove();if(dialog===el)dialog=null;previous?.focus?.({preventScroll:true});},{once:true});
    el.showModal();message();
    journal.ready.then(()=>journal.api('/api/recovery/list?kind='+journal.kind)).then(data=>{
      if(!el.open)return;list.replaceChildren();
      if(!data.entries?.length){list.textContent='暂无恢复副本';return;}
      for(const entry of data.entries){
        const button=document.createElement('button');button.type='button';button.className='recovery-entry';
        button.textContent=new Date(entry.updated).toLocaleString()+(entry.backup?'　备份':'')+(entry.owner===journal.owner?'　本页':'')+'　'+Math.ceil(entry.bytes/1024)+' KiB';
        button.onclick=()=>run(async()=>{
          const ticket=++serial;selected=null;load.hidden=true;preview.hidden=false;preview.textContent='正在读取…';
          const copy=await journal.api('/api/recovery?kind='+journal.kind+'&owner='+encodeURIComponent(entry.owner)+(entry.backup?'&backup=1':''));
          if(ticket!==serial||!el.open)return;journal.validateSnapshot(copy);selected=entry;
          const items=Object.entries(copy.records);preview.textContent=items.length+'条输入'+(copy.recovered?'（已找回中断前的完整记录）':'')+'\n'+items.slice(0,5).map(([key,v])=>journal.kind==='calculator'?'第'+(Number(key)+1)+'行　'+(v.text||'')+(v.note?'　备注：'+v.note:''):(v.book||'')+' / '+(v.id||'')+' / '+(Number(v.row)+1)+','+(Number(v.col)+1)+'　'+(v.value||'')).join('\n').slice(0,2400);
          load.hidden=false;load.disabled=false;message();
        });list.append(button);
      }
    }).catch(error=>{if(el.open)list.textContent=error.message;});
  }
  window.ExcelLensRecovery={Journal,show};
})();
