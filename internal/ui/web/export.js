/* ExcelLens Export Center. A bounded progress API controls a streaming Go job.
 * No workbook cell data is copied into the browser to build the export. */
(() => {
  'use strict';
  const $=id=>document.getElementById(id);
  const controls=['exportScope','exportFormat','exportMode','exportStyles','exportBOM','exportCSVGuard','exportHidden','exportRecoverable'];
  class ExportCenter {
    constructor({state,api,toast,colName,openDialog,excludeThroughRow,sourceCapabilities=()=>({styled:false})}) {
      this.s=state;this.api=api;this.toast=toast;this.colName=colName;this.openDialog=openDialog;
      this.excludeThroughRow=excludeThroughRow||(()=>0);
      this.sourceCapabilities=sourceCapabilities;
      this.job='';this.running=false;this.timer=0;this.busy=false;this.serial=0;this.failures=0;this.snapshot='';this.resultSource='';this.pending=null;
      for(const id of controls)$(id).addEventListener('change',()=>this.refresh());
      $('exportStartBtn').addEventListener('click',()=>this.start());
      $('exportCancelBtn').addEventListener('click',()=>this.cancel());
      $('exportFolderBtn').addEventListener('click',()=>this.folder());
      $('exportRetryBtn').addEventListener('click',()=>{this.failures=0;this.poll();});
    }
    clearResult(){
      $('exportResult').hidden=true;$('exportDownload').removeAttribute('href');$('exportDownload').removeAttribute('download');$('exportFileDetails').textContent='';$('exportSnapshot').textContent='';
    }
    resetBook(){
      if(this.job&&this.running)this.cancelOwned(this.job);
      ++this.serial;clearTimeout(this.timer);this.pending?.abort();this.pending=null;
      this.job='';this.running=false;this.busy=false;this.snapshot='';this.resultSource='';this.failures=0;
      this.generated=null;
      this.clearResult();$('exportProgress').hidden=true;$('exportCancelBtn').hidden=true;$('exportRetryBtn').hidden=true;
      $('exportDialog').close();
    }
    range() {
      const model=window.ExcelLensMultiSelection,area=model.areas(this.s,true).at(-1);
      return area?model.points(area,this.colName):{start:'A1',end:'A1'};
    }
    open(scope='') {
      if(!this.s.opened || !this.s.sheet) {this.toast('请先打开工作簿','warning');return;}
      const hasRange=this.s.range && (this.s.range.row1!==this.s.range.row2||this.s.range.col1!==this.s.range.col2);
      if(!this.running&&!this.busy)this.generated=null;
      if(scope&&!this.running&&!this.busy) $('exportScope').value=scope;
      else if(!this.running) $('exportScope').value=hasRange?'selection':'sheet';
      this.refresh();this.openDialog('exportDialog');
    }
    refresh() {
      const scope=$('exportScope').value, format=$('exportFormat').value;
      const filtered=Boolean(this.s.filtered&&this.s.filterId);
      const filteredOption=$('exportScope').querySelector('[value="filtered"]');
      const visibleOption=$('exportScope').querySelector('[value="visible"]');if(visibleOption)visibleOption.disabled=!this.s.rowView;
      filteredOption.disabled=!filtered;
      if(scope==='filtered'&&!filtered) $('exportScope').value='sheet';
      if(scope==='visible'&&!this.s.rowView)$('exportScope').value='sheet';
      const activeScope=$('exportScope').value;
      if(activeScope==='all'&&!['xlsx','zipcsv'].includes($('exportFormat').value)) $('exportFormat').value='zipcsv';
      if(!this.generated&&activeScope!=='all' && $('exportFormat').value==='zipcsv') $('exportFormat').value='csv';
      const fmt=$('exportFormat').value;
      const range=this.range();
      const filteredRows=this.s.filterTotalsBySheet?.get(this.s.sheet)??this.s.filterTotal??0;
      $('exportRangeLabel').textContent= activeScope==='all'?`${this.s.sheets.length} 个 Sheet`:
        activeScope==='filtered'?`已筛选：${Number(filteredRows).toLocaleString()} 行`:
        activeScope==='visible'?`${this.s.sheet} · 当前可见行`:
        activeScope==='selection'?`${this.s.sheet} · ${range.start}:${range.end}`:
        `${this.s.sheet} · ${this.s.dimension||'已使用区域'}`;
      const styleOK=(fmt==='xlsx'||fmt==='html') && this.sourceCapabilities().styled;
      $('exportStyles').disabled=!styleOK;
      if(!styleOK)$('exportStyles').checked=false;
      $('exportBOM').disabled=!['csv','zipcsv'].includes(fmt);
      $('exportCSVGuard').disabled=!['csv','tsv','zipcsv'].includes(fmt);
      $('exportHidden').disabled=activeScope!=='all';
      const formulaUnsafe=fmt==='xlsx'&&['selection','filtered','visible'].includes(activeScope);
      $('exportMode').querySelector('[value="formula"]').disabled=formulaUnsafe;
      if(formulaUnsafe&&$('exportMode').value==='formula')$('exportMode').value='values';
      let advisory='';
      if(activeScope==='all')advisory='导出整本工作簿时，尚未建立索引的 Sheet 会先建立索引；大表可能产生较大的文件。';
      else if(activeScope==='filtered')advisory='导出全部筛选匹配行，包含手动隐藏的行，保持排序。';
      else if(activeScope==='visible')advisory='导出当前可见行，包含可见的冻结标题，保持排序。';
      else if(activeScope==='selection')advisory='选区按左上角开始重新排布，不会改变原始工作簿。';
      if(formulaUnsafe)advisory+=' 为避免公式引用错位，该范围的 XLSX 只允许导出缓存值。';
      if(fmt==='html')advisory+=' HTML 包含单元格颜色等基础样式，限制15万格；完整格式建议使用 XLSX。';
      if(fmt==='jsonl')advisory+=' JSONL 每行保留来源行号、单元格地址、公式、缓存值及类型，适合程序审计。';
      if($('exportMode').value==='formula'&&fmt==='xlsx')advisory+=' 仅导出指定 Sheet 时，跨表引用可能指向未导出的 Sheet；如需保留依赖，请选择整个工作簿。';
      $('exportAdvisory').textContent=this.generated?'':advisory;
      const locked=this.running||this.busy;
      for(const id of controls)$(id).disabled=locked||!!this.generated||(
        id==='exportStyles'&&!styleOK||id==='exportBOM'&&!['csv','zipcsv'].includes(fmt)||
        id==='exportCSVGuard'&&!['csv','tsv','zipcsv'].includes(fmt)||id==='exportHidden'&&activeScope!=='all');
      if((locked||this.generated)&&this.snapshot)$('exportRangeLabel').textContent=this.snapshot;
      $('exportStartBtn').disabled=locked;
      $('exportStartBtn').textContent=this.generated?'重新导出':'开始导出';
    }
    async start() {
      if(this.running ||this.busy)return;
      if(this.generated)return this.begin(this.generated.path,this.generated.payload,this.generated.label);
      if($('exportScope').value==='selection'&&this.s.multiRanges?.length){this.toast('导出选区请保留一个连续区域','warning');return;}
      const range=this.range();const payload={
        scope:$('exportScope').value,sheet:this.s.sheet,workbookKey:this.s.workbookKey,...range,filterId:this.s.filtered?this.s.filterId:'',
        excludeThroughRow:this.s.filtered||this.s.rowView?this.excludeThroughRow():0,
        format:$('exportFormat').value,mode:$('exportMode').value,styles:$('exportStyles').checked,recoverable:$('exportRecoverable').checked,
        bom:$('exportBOM').checked,protectCSV:$('exportCSVGuard').checked,includeHidden:$('exportHidden').checked
      };
      if(this.s.rowView&&['selection','filtered','visible'].includes(payload.scope))payload.viewId=this.s.rowView.id;
      if(this.s.book)payload.book=this.s.book;
      const ticket=++this.serial,book=this.s.workbookKey,ctrl=new AbortController();this.pending=ctrl;
      return this.begin('/api/export/start',payload,$('exportRangeLabel').textContent,{ticket,book,ctrl});
    }
    async startGenerated(path,payload,label){
      if(this.running||this.busy){this.toast('已有导出正在处理，请完成或取消后重试','warning');this.openDialog('exportDialog');return false;}
      this.generated={path,payload,label};$('exportFormat').value=payload.format||'csv';$('exportScope').value='sheet';$('exportMode').value='values';
      this.snapshot=label;this.refresh();this.openDialog('exportDialog');
      return this.begin(path,payload,label);
    }
    async begin(path,payload,label,request){
      const {ticket,book,ctrl}=request||{ticket:++this.serial,book:this.s.workbookKey,ctrl:new AbortController()};this.pending=ctrl;
      const current=()=>ticket===this.serial&&book===this.s.workbookKey&&!ctrl.signal.aborted;
      this.snapshot=label;this.failures=0;this.busy=true;this.clearResult();this.refresh();
      this.resultSource=[this.s.fileName,payload.sheet,label].filter(Boolean).join(' · ');
      try {
        const started=await this.api(path,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(payload)});
        if(!current()){this.cancelOwned(started.jobId);return;}
        this.job=started.jobId;this.running=true;
        $('exportProgress').hidden=false;$('exportCancelBtn').hidden=false;
        await this.poll();
      }catch(e){if(current()){this.toast(`导出失败：${e.message}`,'error');$('exportStage').textContent=`导出失败：${e.message}`;}}
      finally{if(ticket===this.serial){this.busy=false;this.refresh();if(this.pending===ctrl)this.pending=null;}}
    }
    async cancelOwned(id){
      if(!id)return;
      try{await this.api('/api/export/cancel',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({id})});}catch{/* The task may have completed or been removed by a source switch. */}
    }
    adoptRecovered(start,item){
      if(this.running||this.busy||item.book!==this.s.book||item.workbookKey!==this.s.workbookKey)return false;
      ++this.serial;this.pending?.abort();clearTimeout(this.timer);this.generated=null;this.failures=0;
      this.job=start.jobId;this.running=true;this.snapshot=item.file+(item.sheet?' · '+item.sheet:'');this.resultSource=this.snapshot;
      $('exportFormat').value=item.mode;$('exportScope').value=item.scope;$('exportRecoverable').checked=true;
      this.clearResult();$('exportProgress').hidden=false;$('exportCancelBtn').hidden=false;this.refresh();this.openDialog('exportDialog');
      this.poll();return true;
    }
    async poll() {
      if(!this.job||!this.running)return;
      clearTimeout(this.timer);
      this.pending?.abort();const ctrl=new AbortController();this.pending=ctrl;
      const ticket=this.serial,job=this.job,book=this.s.workbookKey;
      const current=()=>ticket===this.serial&&job===this.job&&book===this.s.workbookKey&&!ctrl.signal.aborted;
      try {
        const data=await this.api(`/api/export/status?id=${encodeURIComponent(job)}`,{signal:ctrl.signal});
        if(!current())return;
        this.failures=0;$('exportRetryBtn').hidden=true;
        const done=Number(data.doneRows||0),total=Number(data.totalRows||0);
        const pct=Math.max(0,Math.min(100,Number(data.progress||0)));
        $('exportBar').style.width=`${pct}%`;
        $('exportStage').textContent=data.stage||'处理中';
        $('exportPercent').textContent=`${pct.toFixed(1)}%`;
        $('exportDetails').textContent=`${done.toLocaleString()} / ${total.toLocaleString()} 行 · 已用 ${(Number(data.elapsedMs||0)/1000).toFixed(1)} 秒`;
        if(data.status==='done') {
          this.running=false;$('exportCancelBtn').hidden=true;
          $('exportResult').hidden=false;
          $('exportDownload').href=data.downloadUrl;
          $('exportDownload').setAttribute('download',data.fileName||'ExcelLens-export');
          $('exportFileDetails').textContent=`${data.fileName} · ${this.formatBytes(data.size)}`;
          $('exportSnapshot').textContent=this.resultSource?`来源：${this.resultSource}`:'';
          this.refresh();return;
        }
        if(data.status==='failed'||data.status==='cancelled') {
          this.running=false;$('exportCancelBtn').hidden=true;$('exportStage').textContent=data.status==='cancelled'?'导出已取消':`导出失败：${data.error||'未知错误'}`;
          this.refresh();return;
        }
        this.timer=setTimeout(()=>this.poll(),340);
      }catch(err){
        if(!current())return;
        if(err.status===404){this.running=false;$('exportCancelBtn').hidden=true;$('exportRetryBtn').hidden=true;$('exportStage').textContent='导出任务已失效，请重新导出';this.refresh();return;}
        $('exportStage').textContent=`状态读取失败：${err.message}`;$('exportRetryBtn').hidden=false;
        // A transport failure says nothing about the server job's completion.
        // Keep cancellation and the task identity, with bounded automatic retries.
        if(++this.failures<=3)this.timer=setTimeout(()=>this.poll(),Math.min(8000,1000*2**(this.failures-1)));
        this.refresh();
      }finally{if(this.pending===ctrl)this.pending=null;}
    }
    formatBytes(value) {return window.ExcelLensDisplayFormat.bytes(value);}
    async cancel() {
      if(!this.job||!this.running)return;
      const ticket=this.serial,job=this.job;
      try{await this.api('/api/export/cancel',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({id:job})});if(ticket===this.serial&&job===this.job){$('exportStage').textContent='正在取消...';this.failures=0;this.poll();}}
      catch(e){if(ticket===this.serial&&job===this.job)this.toast(e.message,'error');}
    }
    async folder(){try {await this.api('/api/export/folder',{method:'POST'});}catch(e){this.toast(e.message,'error');}}
  }
  window.ExcelLensExportCenter=ExportCenter;
})();
