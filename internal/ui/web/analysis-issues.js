(() => {
  'use strict';
  const $=id=>document.getElementById(id);
  const kinds={conversion:'转换失败',number:'未计入数值聚合',missing_cache:'公式缺缓存'};
  const colName=index=>{let text='';while(index>0){index--;text=String.fromCharCode(65+index%26)+text;index=Math.floor(index/26);}return text;};
  class Issues {
    constructor({api,identity,toast,navigate,askName,onMaterialized,startExport}){
      Object.assign(this,{api,identity,toast,navigate,askName,onMaterialized,startExport});this.snapshot=null;this.dialog=$('analysisIssuesDialog');this.busy=false;
      $('analysisIssuesOpen').addEventListener('click',()=>this.open());$('analysisIssuesClose').addEventListener('click',()=>this.close());
      this.dialog.addEventListener('keydown',event=>{if(event.key==='Escape'){event.stopPropagation();}});
      $('analysisIssuesExtract').addEventListener('click',()=>this.extract());
      $('analysisIssuesExport').addEventListener('click',()=>{const snapshot=this.snapshot;if(!this.current(snapshot))return;const protect=$('analysisIssuesCSVGuard').checked;this.close();this.startExport(snapshot.job,protect);});
    }
    current(snapshot){return snapshot&&snapshot===this.snapshot&&snapshot.book===this.identity();}
    close(){if(this.dialog.open)this.dialog.close();}
    reset(){this.close();this.snapshot=null;$('analysisIssuesOpen').hidden=true;}
    setSnapshot(snapshot){
      if(snapshot?.job!==this.snapshot?.job)this.close();this.snapshot=snapshot;
      const total=this.total(snapshot?.result);const button=$('analysisIssuesOpen');button.hidden=!total;button.textContent='核对项目 '+total.toLocaleString();button.disabled=!total;
    }
    total(result){return (result?.invalidNumbers||0)+(result?.invalidConversions||0)+(result?.missingFormulaCaches||0);}
    open(){
      const snapshot=this.snapshot;if(!this.current(snapshot))return;
      const result=snapshot.result,issues=result.issues||[],total=this.total(result);
      $('analysisIssuesSummary').textContent='共 '+total.toLocaleString()+' 项；显示 '+issues.length+' 项'+(result.issuesTruncated?'（样例）':'')+'。同一源行可能有多项。';
      $('analysisIssuesTextNote').textContent='原文最多显示512字符；编辑表最多10,000项，完整原文可导出CSV。';
      const table=$('analysisIssuesTable');table.replaceChildren();
      const head=document.createElement('thead'),tr=document.createElement('tr');
      for(const label of ['源位置','参与字段','类别','原文片段','步骤输入','当前值','原因']){const th=document.createElement('th');th.textContent=label;tr.append(th);}head.append(tr);table.append(head);
      const body=document.createElement('tbody');
      for(const issue of issues){
        const row=document.createElement('tr'),position=document.createElement('td'),jump=document.createElement('button');jump.type='button';jump.className='analysis-drill-link';jump.textContent=(issue.sourceColumn?colName(issue.sourceColumn):'行 ')+issue.row;jump.title='转到来源位置';jump.onclick=()=>{if(this.current(snapshot)){this.close();this.navigate(snapshot.query,issue,colName(issue.sourceColumn||1)+issue.row);}};if(issue.origin){const source=document.createElement('div');source.className='composition-origin';source.textContent=issue.origin.file+' / '+issue.origin.sheet;position.append(source);}position.append(jump);row.append(position);
        for(const text of [issue.fieldName||issue.field,kinds[issue.kind]||issue.kind,issue.original,issue.input,issue.value,issue.reason+(issue.textTruncated?'（内容已截取）':'')]){const td=document.createElement('td');td.textContent=text;row.append(td);}body.append(row);
      }
      table.append(body);$('analysisIssuesExtract').disabled=this.busy;$('analysisIssuesExport').disabled=this.busy;
      if(!this.dialog.open)this.dialog.showModal();
    }
    async extract(){
      const snapshot=this.snapshot;if(this.busy||!this.current(snapshot))return;
      // Close the native modal before opening the app's naming prompt.
      this.close();const name=await this.askName('提取异常核对表','异常核对 '+new Date().toLocaleDateString('zh-CN'));
      if(!name||!this.current(snapshot))return;
      this.busy=true;
      try{
        const data=await this.api('/api/analysis/issues/materialize',{method:'POST',body:JSON.stringify({jobId:snapshot.job,name,limit:10000})});
        if(!this.current(snapshot))return;
        await this.onMaterialized(data);
        this.toast('已提取 '+data.rows.toLocaleString()+' / '+data.total.toLocaleString()+' 项'+(data.truncated?'；达到容量上限，完整清单请导出CSV':''),data.truncated?'warning':'success');
      }catch(error){if(this.current(snapshot))this.toast(error.message,'error');}
      finally{this.busy=false;}
    }
  }
  window.ExcelLensAnalysisIssues=Issues;
})();
