/* Runtime owners are separate from durable query scopes. A late response is
 * still observed so its own ID can be released, never the newer owner's ID. */
(() => {
  'use strict';
  const clone=value=>value==null?null:JSON.parse(JSON.stringify(value));
  const key=scope=>JSON.stringify(scope||null);
  const cancelled=()=>Object.assign(Error('来源选择已取消'),{name:'AbortError'});
  const labels={full:'完整源表',filtered:'完整筛选结果',visible:'当前可见行',selection:'画面选区'};
  function caption(scope){
    if(!scope)return labels.full;
    const area=scope.selection;
    return (labels[scope.mode]||'保存的范围')+(area?` · 第${area.startColumn}–${area.endColumn}列 · 源行${area.startRow}至${area.endRow}`:'');
  }
  class AnalysisSources{
    constructor({api,context,grid,tasks}){Object.assign(this,{api,context,grid,tasks});this.serial=0;this.binding=null;this.candidates=new Set();}
    identity(){const c=this.context();return JSON.stringify([c.bookKey,c.book,c.opened]);}
    post(path,data){return this.api(path,{method:'POST',body:JSON.stringify(data)});}
    clear(record){if(record?.id)this.post(record.kind==='composition'||record.composition?'/api/composition/clear':'/api/analysis/source/clear',{id:record.id}).catch(()=>{});}
    release(record){if(!record||record===this.binding)return;if(this.candidates.delete(record))this.clear(record);}
    cancel(){++this.serial;for(const record of this.candidates)this.release(record);}
    reset(){this.cancel();const old=this.binding;this.binding=null;this.clear(old);}
    matches(source,scope){return this.binding?.source===source&&this.binding.identity===this.identity()&&key(this.binding.sourceScope)===key(scope);}
    async prepare({source,sourceScope=null,composition=null,prepared=null,mode},guard=()=>true){
      this.cancel();const serial=this.serial,identity=this.identity(),c=this.context();
      const current=()=>serial===this.serial&&identity===this.identity()&&c.opened&&guard();
      if(!current())throw cancelled();
      if(!mode&&!prepared&&this.matches(source,sourceScope))return this.binding;
      let data;
      if(source.startsWith('append:')){
        if(!composition||sourceScope||mode&&mode!=='full'||!this.tasks)throw Error('联合来源定义无效，请重新选择');
        data=prepared||await this.tasks.run({mode:'append',definition:clone(composition),...(this.binding?.composition?{keepId:this.binding.id}:{})},current);
        data={...data,composition:data.definition,sourceScope:null};
      }else if(mode&&mode!=='full'){
        const g=this.grid(mode);
        if(g.workbookKey!==c.bookKey||g.book!==c.book||g.source!==source)throw Error('请返回工作簿，在此来源工作表选择范围');
        data=await this.post('/api/analysis/source/capture',{...g,mode});
      }else if(!mode&&sourceScope){
        data=await this.post('/api/analysis/source/restore',{workbookKey:c.bookKey,book:c.book,source,sourceScope:clone(sourceScope)});
      }else data={source,sourceScope:null,workbookKey:c.bookKey,book:c.book,id:''};
      const record={...data,kind:source.startsWith('append:')?'composition':'scope',identity,sourceScope:clone(data.sourceScope),current};this.candidates.add(record);
      if(!current()){this.release(record);throw cancelled();}
      if(data.source!==source||data.workbookKey!==c.bookKey||data.book!==c.book||((mode&&mode!=='full'||!mode&&sourceScope)&&(!data.id||!data.sourceScope))){this.release(record);throw Error('来源响应与所选范围不一致');}
      return record;
    }
    commit(record){
      if(record===this.binding)return record;
      if(!this.candidates.has(record)||!record.current()){this.release(record);throw cancelled();}
      const old=this.binding;this.binding=record;this.candidates.delete(record);
      this.clear(old);
      return record;
    }
    query(query){const q=clone(query);if(this.matches(q.source,q.sourceScope)&&this.binding.id)q.sourceId=this.binding.id;return q;}
  }
  window.ExcelLensAnalysisSources=AnalysisSources;
  window.ExcelLensAnalysisSourceModel={caption,key};
})();
