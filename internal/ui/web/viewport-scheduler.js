/* Reconcile the latest demand with outstanding reads. Only a change of data identity
 * invalidates every read. Off-screen completions can still populate the bounded cache. */
(() => {
  'use strict';
  class ViewportScheduler {
    constructor(read,limit=3){
      this.read=read;
      this.limit=limit;
      this.active=new Map();
      this.wanted=new Map();
      this.stats={started:0,cancelled:0,completed:0,peak:0};
    }
    reset(){
      for(const job of this.active.values())this.cancel(job);
      for(const job of this.wanted.values())job.resolve();
      this.wanted.clear();
    }
    cancel(job){
      if(!job.controller.signal.aborted){this.stats.cancelled++;job.controller.abort();}
    }
    plan(current,predicted){
      const wanted=new Map(),required=[];
      const add=(item,foreground)=>{
        if(wanted.has(item.key))return;
        let job=this.active.get(item.key);
        if(job?.controller.signal.aborted)job=null;
        job||=this.wanted.get(item.key);
        if(!job||job.controller?.signal.aborted){
          let resolve,reject;
          const promise=new Promise((ok,fail)=>{resolve=ok;reject=fail;});
          job={...item,promise,resolve,reject};
          // Speculative errors are retried if the tile later becomes current.
          promise.catch(()=>{});
        }
        job.foreground=foreground;wanted.set(job.key,job);
        if(foreground)required.push(job.promise);
      };
      for(const item of current)add(item,true);
      for(const item of predicted)add(item,false);
      for(const old of this.wanted.values())if(!wanted.has(old.key)&&!old.controller)old.resolve();
      this.wanted=wanted;
      this.pump();
      return Promise.all(required);
    }
    pump(){
      for(const job of this.wanted.values()){
        if(job.controller||this.active.has(job.key))continue;
        if(this.active.size>=this.limit){
          // Reserve progress for a thumb jump. Keep neighbouring or promoted reads alive.
          if(job.foreground&&![...this.active.values()].some(j=>j.controller.signal.aborted)){
            const obsolete=[...this.active.values()].find(j=>!this.wanted.has(j.key));
            if(obsolete)this.cancel(obsolete);
          }
          break;
        }
        if(!job.foreground&&this.active.size>=this.limit-1)break;
        job.controller=new AbortController();
        this.active.set(job.key,job);this.stats.started++;
        this.stats.peak=Math.max(this.stats.peak,this.active.size);
        Promise.resolve().then(()=>this.read(job,job.controller.signal)).then(
          result=>{if(!job.controller.signal.aborted)this.stats.completed++;job.resolve(result);},
          err=>job.controller.signal.aborted?job.resolve():job.reject(err)
        ).finally(()=>{
          this.active.delete(job.key);
          if(this.wanted.get(job.key)===job)this.wanted.delete(job.key);
          this.pump();
        });
      }
    }
  }
  window.ExcelLensViewportScheduler=ViewportScheduler;
})();
