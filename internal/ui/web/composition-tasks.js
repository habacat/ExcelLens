/* Each preparation owns exactly its own runtime token, including late starts. */
(() => {
  'use strict';
  const abort=()=>Object.assign(Error('联合来源准备已取消'),{name:'AbortError'});
  class Tasks{
    constructor({api,context,sleep=ms=>new Promise(r=>setTimeout(r,ms)),endpoint='/api/composition',label='来源'}){Object.assign(this,{api,context,sleep,endpoint,label});this.pending=new Set();}
    post(path,data){return this.api(path,{method:'POST',body:JSON.stringify(data)});}
    clear(id){if(id)return this.post(this.endpoint+'/clear',{id}).catch(()=>{});return Promise.resolve();}
    cancel(record){record.cancelled=true;record.activity?.end();if(record.id)this.clear(record.id);}
    reset(){for(const record of this.pending)this.cancel(record);}
    async run(request,guard=()=>true){
      const c=this.context(),identity=JSON.stringify([c.bookKey,c.book]),record={id:'',cancelled:false};this.pending.add(record);
      record.activity=globalThis.ExcelLensActivity?.begin('正在准备'+this.label);
      const current=()=>!record.cancelled&&guard()&&this.context().opened&&JSON.stringify([this.context().bookKey,this.context().book])===identity;
      let accepted=false;
      try{
        if(!current())throw abort();
        const start=await this.post(this.endpoint+'/start',{...request,workbookKey:c.bookKey,book:c.book});record.id=start.jobId;
        if(typeof record.id!=='string'||!/^[a-zA-Z0-9_-]+$/.test(record.id)) {record.id='';throw Error(this.label+'任务未创建，请重试');}
        if(!current())throw abort();
        for(;;){
          const status=await this.api(this.endpoint+'/status?id='+encodeURIComponent(record.id));
          if(!current())throw abort();
          record.activity?.job(status);
          if(!['queued','running','done','error','cancelled'].includes(status?.status)||status.id!==undefined&&status.id!==record.id||status.jobId!==undefined&&status.jobId!==record.id)throw Error(this.label+'任务状态无效，请重试');
          if(status.status==='done')break;
          if(status.status==='error'||status.status==='cancelled')throw Error(status.error||'来源准备未完成');
          await this.sleep(250);if(!current())throw abort();
        }
        const result=await this.api(this.endpoint+'/result?id='+encodeURIComponent(record.id));
        if(!current())throw abort();
        if(result.id!==record.id||result.workbookKey!==c.bookKey||result.book!==c.book)throw Error('来源响应已变化，请重新准备');
        accepted=true;return {...result,id:record.id};
      }finally{record.activity?.end();this.pending.delete(record);if(!accepted)await this.clear(record.id);}
    }
  }
  window.ExcelLensCompositionTasks=Tasks;
  window.ExcelLensSourceTasks=Tasks;
})();
