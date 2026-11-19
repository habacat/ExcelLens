/* Own server jobs from the start request through completion, including IDs
 * arriving after cancellation. No cancelled task may publish progress/results. */
(() => {
  'use strict';
  const interrupted=(label)=>Object.assign(new Error(label+'已取消'),{name:'AbortError'});
  class AnalysisJobs {
    constructor({api,identity,requestIdentity=identity,sleep,onBusy=()=>{},onProgress=()=>{},endpoint='/api/analysis/query',label='分析',slot,maxPolls=1800,resultFromStatus=status=>status.result}){
      Object.assign(this,{api,identity,requestIdentity,sleep,onBusy,onProgress,endpoint,label,slot,maxPolls,resultFromStatus});
      this.serial=0;this.main=null;this.tasks=new Set();
    }
    current(task){return !task.cancelled&&task.book===this.identity()&&(!task.main||task.serial===this.serial);}
    cancelServer(task){
      if(!task.id||task.cancelSent||task.finished)return;
      task.cancelSent=true;
      this.api(this.endpoint+'/cancel',{method:'POST',body:JSON.stringify({id:task.id})}).catch(()=>{});
    }
    cancelTask(task){task.cancelled=true;task.controller.abort();this.cancelServer(task);}
    cancelMain(){
      ++this.serial;
      if(!this.main)return false;
      const task=this.main;this.main=null;this.cancelTask(task);this.onBusy(false);return true;
    }
    reset(){
      this.cancelMain();
      for(const task of this.tasks)this.cancelTask(task);
    }
    async execute(query,{main=false}={}){
      if(main)this.cancelMain();
      const task={main,serial:this.serial,book:this.identity(),requestBook:this.requestIdentity(),id:'',cancelled:false,cancelSent:false,finished:false,controller:new AbortController()};
      const activity=globalThis.ExcelLensActivity?.begin('正在处理'+this.label,{signal:task.controller.signal,slot:main?this.slot:null});
      this.tasks.add(task);
      if(main){this.main=task;this.onBusy(true);}
      try{
        // Keep the start response observable: if cancelled while it is in
        // flight, learn the new ID and cancel exactly that owned server job.
        const started=await this.api(this.endpoint+'/start',{method:'POST',headers:{'X-ExcelLens-Workbook':task.requestBook},body:JSON.stringify(query)});
		if(typeof started?.jobId!=='string'||!/^[a-zA-Z0-9_-]+$/.test(started.jobId))throw Error(this.label+'任务标识无效，请重试');
        task.id=started.jobId;
        if(!this.current(task))throw interrupted(this.label);
        for(let tick=0;this.maxPolls===0||tick<this.maxPolls;tick++){
          await this.sleep(tick<12?240:420);
          if(!this.current(task))throw interrupted(this.label);
          const status=await this.api(this.endpoint+'/status?id='+encodeURIComponent(task.id),{signal:task.controller.signal});
          if(!this.current(task))throw interrupted(this.label);
		  if(!['queued','running','done','error','cancelled'].includes(status?.status)||(status.id!==undefined&&status.id!==task.id))throw Error(this.label+'任务状态响应无效，请重试');
          activity?.job(status);
          if(main)this.onProgress(status);
          if(status.status==='done'){task.finished=true;return {result:this.resultFromStatus(status),job:task.id,sourceFields:status.sourceFields,query:status.query,current:()=>this.current(task)};}
          if(status.status==='error'||status.status==='cancelled')throw Error(status.error||this.label+'已取消');
        }
        throw Error(this.label+'任务轮询超时，请缩小范围');
      }catch(error){
        if(!this.current(task))throw interrupted(this.label);
        throw error;
      }finally{
        activity?.end();
        this.tasks.delete(task);
        if(!task.finished)this.cancelServer(task);
        if(this.main===task){this.main=null;this.onBusy(false);}
      }
    }
  }
  window.ExcelLensAnalysisJobs=AnalysisJobs;
})();
