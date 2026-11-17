(() => {
  'use strict';
  const $=id=>document.getElementById(id),model=window.ExcelLensTaskRecoveryModel;
  class TaskRecovery {
    constructor({state,api,toast,openDialog,maximum,openSource,adopt,canResume,context=()=>''}){
      Object.assign(this,{s:state,api,toast,openDialog,maximum,openSource,adopt,canResume,context});
      this.items=[];this.serial=0;this.listSerial=0;this.busy=false;this.opening=false;
      $('taskRecoveryRefresh').addEventListener('click',()=>this.load());
      $('taskRecoveryClose').addEventListener('click',()=>this.close());
      $('taskRecoveryDialog').addEventListener('cancel',event=>{event.preventDefault();this.close();});
      $('taskRecoveryDialog').addEventListener('close',()=>{if($('taskRecoveryDialog').open)return;++this.serial;++this.listSerial;this.pending?.abort();});
    }
    async open(){this.openDialog('taskRecoveryDialog');return this.load();}
    close(){if(this.opening)return;$('taskRecoveryDialog').close();}
    status(text,error=false){$('taskRecoveryStatus').textContent=text;$('taskRecoveryStatus').classList.toggle('is-error',error);$('taskRecoveryStatus').hidden=!text;}
    controls(){
      $('taskRecoveryRefresh').disabled=this.busy;
      $('taskRecoveryClose').disabled=this.opening;
      for(const button of $('taskRecoveryList').querySelectorAll('button'))button.disabled=this.busy||button.dataset.unavailable==='true';
    }
    async load(){
      if(this.busy)return;
      const ticket=++this.listSerial;this.pending?.abort();const controller=new AbortController();this.pending=controller;
      this.status('正在读取…');
      try{
        const data=await this.api('/api/tasks',{signal:controller.signal});
        if(ticket!==this.listSerial||!$('taskRecoveryDialog').open)return;
        this.items=model.items(data,this.maximum);this.render();this.status('');
      }catch(error){if(ticket===this.listSerial&&error.name!=='AbortError')this.status(error.message,true);}
      finally{if(this.pending===controller)this.pending=null;}
    }
    render(){
      const list=$('taskRecoveryList'),active=document.activeElement,focus=active?.closest?.('[data-task-id]')?.dataset.taskId,action=active?.dataset.action;
      const fragment=document.createDocumentFragment();
      if(!this.items.length){const empty=document.createElement('p');empty.className='task-recovery-empty';empty.textContent='暂无可恢复的任务';fragment.append(empty);}
      for(const item of this.items){
        const row=document.createElement('div');row.className='task-recovery-row';row.dataset.taskId=item.id;
        const main=document.createElement('div');main.className='task-recovery-main';
        const heading=document.createElement('div');heading.className='task-recovery-heading';
        const kind=document.createElement('span');kind.className='task-recovery-kind';kind.textContent=model.label(item);
        const name=document.createElement('strong');name.className='task-recovery-name';name.textContent=item.file||'无法读取的任务';name.title=item.file;
        heading.append(kind,name);
        const meta=document.createElement('div');meta.className='task-recovery-meta';
        const sheet=document.createElement('span');sheet.className='task-recovery-sheet';sheet.textContent=item.sheet;sheet.title=item.sheet;
        const time=document.createElement('time');const date=new Date(item.updated);time.textContent=Number.isNaN(+date)?'':new Intl.DateTimeFormat('zh-CN',{month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit',hour12:false}).format(date);if(!Number.isNaN(+date))time.dateTime=date.toISOString();
        const status=document.createElement('span');status.textContent=item.busy?'运行中':item.error?'不可恢复':item.complete?'已完成':item.backup?'已找回进度':'已暂停';
        meta.append(sheet,time,status);main.append(heading,meta);
        if(item.error){const error=document.createElement('p');error.className='task-recovery-error';error.textContent=item.error;main.append(error);}
        const actions=document.createElement('div');actions.className='task-recovery-actions';
        const next=model.action(item,this.s);
        const add=(action,label,unavailable=false)=>{const button=document.createElement('button');button.type='button';button.className='mini-button';button.dataset.action=action;button.dataset.unavailable=String(unavailable);button.textContent=label;button.addEventListener('click',()=>this.run(item,action));actions.append(button);};
        if(next)add(next,{open:'打开原文件',resume:'继续',download:'下载'}[next]);
        add('remove','移除',item.busy);row.append(main,actions);fragment.append(row);
      }
      const scroll=list.scrollTop;list.replaceChildren(fragment);list.scrollTop=scroll;this.controls();
      if(focus&&action)list.querySelector(`[data-task-id="${focus}"] [data-action="${action}"]`)?.focus({preventScroll:true});
    }
    async cancelOwned(started,item){
      const operation=model.operation(item);
      if(!operation||!started?.jobId)return;
      try{await this.api('/api/'+operation+'/cancel',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({id:started.jobId})});}catch{/* A finished task remains in the persistent directory. */}
    }
    async run(item,action){
      if(this.busy||item.busy||!this.items.includes(item))return;
      if(action!=='remove'&&model.action(item,this.s)!==action)return;
      if(action!=='remove'&&!this.canResume(item,action))return;
      ++this.listSerial;this.pending?.abort();
      const ticket=++this.serial,identity=this.context();
      const current=()=>ticket===this.serial&&identity===this.context()&&$('taskRecoveryDialog').open;
      this.busy=true;this.opening=action==='open';this.controls();this.status(action==='remove'?'正在移除…':action==='open'?'正在打开…':'正在恢复…');
      let started,claimed=false;
      try{
        const payload=model.request(item,this.s);
        if(action==='open'){
          await this.openSource(payload,current); // The shared opener owns its source-switch lifecycle.
          if(ticket!==this.serial||!$('taskRecoveryDialog').open)return;
          this.status('');this.render();return;
        }
        if(action==='remove'){
          await this.api('/api/tasks/discard',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(payload)});
          if(!current())return;
          this.items=this.items.filter(x=>x!==item);this.render();this.status('');return;
        }
        // Keep the mutation response: aborting after server acceptance could
        // hide the returned job ID and prevent ownership-aware cleanup.
        started=await this.api('/api/tasks/resume',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(payload)});
        if(!current())return;
        if(!started?.jobId||started.operation!==model.operation(item))throw Error('恢复响应无效，请刷新任务列表');
        $('taskRecoveryDialog').close();
        claimed=await this.adopt(started,item);
      }catch(error){if(ticket===this.serial&&$('taskRecoveryDialog').open)this.status(error.message,true);else if(started)this.toast(error.message,'error');}
      finally{
        if(started&&!claimed)await this.cancelOwned(started,item);
        this.busy=false;this.opening=false;this.controls();
      }
    }
  }
  window.ExcelLensTaskRecovery=TaskRecovery;
})();
