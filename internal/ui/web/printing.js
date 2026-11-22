(() => {
  'use strict';
  const $=id=>document.getElementById(id),make=(tag,cls='',text='')=>{const n=document.createElement(tag);n.className=cls;n.textContent=text;return n;};
  class Printing {
    constructor({state,api,toast,source,limits,richLimits,fontStack,sleep=ms=>new Promise(resolve=>setTimeout(resolve,ms))}){
      Object.assign(this,{s:state,api,toast,source,limits});if(!limits?.pages||!limits.domPixels)throw Error('分页预览容量不可用');
      this.epoch=0;this.request=0;this.pageRequest=0;this.job='';this.index=0;this.busy=false;this.loading=false;this.dirty=false;
      this.renderer=new window.ExcelLensPrintingRenderer({limits,richLimits,fontStack});this.dialog=this.create();
      this.jobs=new window.ExcelLensAnalysisJobs({api,identity:()=>this.identity(),requestIdentity:()=>this.s.workbookKey,sleep,endpoint:'/api/printing',label:'分页预览',maxPolls:0,resultFromStatus:status=>status,onProgress:status=>{this.validSource(status);this.status(status.stage||'正在准备预览');}});
      $('printPreviewOpenBtn').addEventListener('click',()=>this.open());$('printPreviewClose').addEventListener('click',()=>this.close());
      this.dialog.addEventListener('cancel',event=>{event.preventDefault();this.close();});this.dialog.addEventListener('close',()=>{if(!this.dialog.open)this.stop(true);});
      $('printPreviewUpdate').addEventListener('click',()=>this.refresh());$('printPreviewCancel').addEventListener('click',()=>this.cancel());
      $('printSettingsForm').addEventListener('submit',event=>{event.preventDefault();this.refresh();});$('printSettingsForm').addEventListener('input',()=>this.changed());$('printSettingsForm').addEventListener('change',()=>this.changed());
      $('printPreviewPrev').addEventListener('click',()=>this.go(this.index-1));$('printPreviewNext').addEventListener('click',()=>this.go(this.index+1));
      $('printPreviewPage').addEventListener('change',()=>this.goInput());$('printPreviewPage').addEventListener('keydown',event=>{if(event.key==='Enter'){event.preventDefault();this.goInput();}});
      $('printOutputMode').addEventListener('change',()=>this.controls());
      $('printOutput').addEventListener('click',()=>this.exportPaper());
      $('printScreenZoom').addEventListener('change',()=>this.zoom());
      this.dialog.addEventListener('keydown',event=>{if(event.target.closest?.('input,textarea,select')||event.ctrlKey||event.altKey||event.metaKey)return;const page=event.key==='PageDown'?this.index+1:event.key==='PageUp'?this.index-1:event.key==='Home'?0:event.key==='End'?(this.summary?.summary.pages||1)-1:null;if(page!==null){event.preventDefault();this.go(page);}});
      document.addEventListener('keydown',event=>{if((event.ctrlKey||event.metaKey)&&!event.altKey&&event.key.toLowerCase()==='p'&&this.s.opened){event.preventDefault();this.open();}});
      if(typeof ResizeObserver!=='undefined'){this.resize=new ResizeObserver(()=>this.zoom());this.resize.observe($('printPreviewCanvas'));}
      this.fields();this.controls();
    }
    create(){
      const d=make('dialog','float-dialog printing-dialog');d.id='printPreviewDialog';d.setAttribute('aria-labelledby','printPreviewTitle');
      d.innerHTML=`<div class="dialog-head"><div id="printPreviewTitle" class="dialog-title">分页预览</div><button id="printPreviewClose" class="dialog-close" type="button" aria-label="关闭分页预览">×</button></div>
        <p id="printPreviewSource" class="print-source"></p><div class="print-main"><details id="printSettings" class="print-settings" open><summary>页面设置</summary><form id="printSettingsForm">
        <label>纸张<select id="printPaper" class="field-control"><option value="source">按源设置</option><option value="a4">A4</option><option value="a3">A3</option><option value="letter">Letter</option><option value="legal">Legal</option><option value="custom">自定义尺寸</option></select></label>
        <div id="printCustomPaper" class="print-pair" hidden><label>宽（mm）<input id="printPaperWidth" class="field-control" value="210" inputmode="decimal"></label><label>高（mm）<input id="printPaperHeight" class="field-control" value="297" inputmode="decimal"></label></div>
        <label>方向<select id="printOrientation" class="field-control"><option value="">按源设置</option><option value="portrait">纵向</option><option value="landscape">横向</option></select></label>
        <label>打印缩放<select id="printScaleMode" class="field-control"><option value="source">按源设置</option><option value="percent">指定比例</option><option value="fit">适配页数</option></select></label>
        <label id="printPercentRow" hidden>比例（%）<input id="printPercent" class="field-control" value="100" inputmode="decimal"></label>
        <div id="printFitRow" class="print-pair" hidden><label>横向页数<input id="printFitWidth" class="field-control" value="1" inputmode="numeric"></label><label>纵向页数<input id="printFitHeight" class="field-control" value="0" inputmode="numeric"></label><small>0 表示该方向不限。</small></div>
        <label>打印区域<input id="printArea" class="field-control" placeholder="A1:H50, J1:L50（留空按原表）" autocomplete="off"></label>
        <details class="print-advanced"><summary>更多页面设置</summary><label>重复行<input id="printRepeatRows" class="field-control" placeholder="留空按源，如 1:2"></label><label class="print-toggle"><input id="printClearRepeatRows" type="checkbox">不重复行</label><label>重复列<input id="printRepeatColumns" class="field-control" placeholder="留空按源，如 A:B"></label><label class="print-toggle"><input id="printClearRepeatColumns" type="checkbox">不重复列</label>
        <label>页边距<select id="printMarginMode" class="field-control"><option value="source">按源设置</option><option value="custom">自定义（mm）</option></select></label><div id="printMargins" class="print-pair" hidden></div>
        <label>页序<select id="printPageOrder" class="field-control"><option value="">按源设置</option><option value="downThenOver">先向下，后向右</option><option value="overThenDown">先向右，后向下</option></select></label><label>起始页码<input id="printFirstPage" class="field-control" placeholder="留空按源" inputmode="numeric"></label>
        <div class="print-pair"><label>缺省行高（磅）<input id="printRowHeight" class="field-control" placeholder="留空按源" inputmode="decimal"></label><label>缺省列宽（磅）<input id="printColumnWidth" class="field-control" placeholder="留空按源" inputmode="decimal"></label></div><label class="print-toggle"><input id="printIgnoreBreaks" type="checkbox">忽略源手工分页</label></details>
        <label class="print-toggle"><input id="printUseDefaults" type="checkbox" checked>补足缺失的页面设置</label><button id="printPreviewUpdate" class="primary-button" type="button">更新预览</button><p id="printPendingNote" class="print-pending" hidden>设置已更改，点击更新预览。</p></form></details>
        <section class="print-view"><div class="print-page-tools"><button id="printPreviewPrev" class="mini-button" type="button" aria-label="上一页">上一页</button><label class="print-page-number">第<input id="printPreviewPage" class="field-control" type="number" min="1" value="1" aria-label="预览页序">页<span id="printPreviewCount">/ 0</span></label><button id="printPreviewNext" class="mini-button" type="button" aria-label="下一页">下一页</button><label class="print-screen-zoom">屏幕缩放<select id="printScreenZoom" class="field-control"><option value="fit">适合页面</option><option value="width">适合宽度</option><option value="50">50%</option><option value="75">75%</option><option value="100">100%</option><option value="125">125%</option><option value="150">150%</option><option value="200">200%</option></select></label></div><p id="printEffective" class="print-effective"></p><div id="printPreviewCanvas" class="print-canvas" tabindex="0" aria-label="纸张预览，PageUp和PageDown翻页"><div id="printPaperWrap" class="print-paper-wrap"></div><p id="printPreviewEmpty" class="print-empty">正在准备预览…</p></div><p id="printPageCaption" class="print-caption"></p></section></div>
        <details id="printSourceDetails" class="print-source-details"><summary>页面详情</summary><ul id="printWarnings"></ul><div id="printRawSettings"></div></details><div class="print-footer"><select id="printOutputMode" class="field-control" aria-label="底稿页段"><option value="current">当前页</option><option value="range">指定页段</option><option value="sheet">本表全部页</option><option value="book">整本可见表</option></select><input id="printOutputRange" class="field-control" placeholder="1-3,5" aria-label="底稿页段范围" hidden><button id="printOutput" class="mini-button" type="button">导出打印底稿</button><p id="printPreviewStatus" role="status"></p><button id="printPreviewCancel" class="mini-button" type="button" hidden>取消</button></div>`;
      document.body.append(d);const names={left:'左',right:'右',top:'上',bottom:'下',header:'页眉',footer:'页脚'};
      for(const [key,label] of Object.entries(names)){const node=make('label','',label+'（mm）'),input=make('input','field-control');input.id='printMargin'+key[0].toUpperCase()+key.slice(1);input.inputMode='decimal';input.value=input.defaultValue=['header','footer'].includes(key)?'8':'20';node.append(input);$('printMargins').append(node);}return d;
    }
    identity(){return JSON.stringify([this.s.workbookKey,this.s.book,this.s.sheet,this.s.sheetEpoch,this.epoch]);}
    binding(){return {workbookKey:this.s.workbookKey,book:this.s.book,sheet:this.s.sheet};}
    enabled(){return !!this.s.opened&&!!this.s.indexReady&&!!this.s.sheet&&!this.s.opening&&!this.s.sheetLoadingEpoch&&!!this.source().printMetadata;}
    validSource(value){if(value?.workbookKey!==this.s.workbookKey||value?.book!==this.s.book||value?.sheet!==this.s.sheet)throw Error('预览来源已切换，请重新准备');}
    release(id){if(id)this.api('/api/printing/clear',{method:'POST',body:JSON.stringify({id})}).catch(()=>{});}
    status(text,error=false){$('printPreviewStatus').textContent=text||'';$('printPreviewStatus').classList.toggle('is-error',error);$('printPreviewStatus').setAttribute('role',error?'alert':'status');}
    controls(){
      const enabled=this.enabled(),pending=this.busy||this.loading||!!this.outputController,pages=this.summary?.summary.pages||0;
      $('printPreviewOpenBtn').disabled=!enabled;if($('menuPrintPreview'))$('menuPrintPreview').disabled=!enabled;
      $('printPreviewUpdate').disabled=!enabled||this.busy;$('printPreviewCancel').hidden=!pending;
      $('printPreviewPrev').disabled=pending||!this.job||this.index===0;$('printPreviewNext').disabled=pending||!this.job||this.index+1>=pages;
      $('printPreviewPage').disabled=this.busy||!pages;$('printPreviewPage').max=String(pages);$('printPreviewCount').textContent='/ '+pages;
      $('printOutput').disabled=pending||this.dirty||!this.job||!pages;
      $('printOutputRange').hidden=$('printOutputMode').value!=='range';
      $('printOutputMode').disabled=!!this.outputController;
      $('printOutputRange').disabled=!!this.outputController;
      $('printPendingNote').hidden=!this.dirty;$('printScreenZoom').disabled=!this.rendered;
    }
    fields(){
      $('printCustomPaper').hidden=$('printPaper').value!=='custom';$('printPercentRow').hidden=$('printScaleMode').value!=='percent';$('printFitRow').hidden=$('printScaleMode').value!=='fit';$('printMargins').hidden=$('printMarginMode').value!=='custom';
      $('printRepeatRows').disabled=$('printClearRepeatRows').checked;$('printRepeatColumns').disabled=$('printClearRepeatColumns').checked;
    }
    input(){
      const value=id=>$(id).value,out={defaults:$('printUseDefaults').checked,paper:value('printPaper'),width:value('printPaperWidth'),height:value('printPaperHeight'),orientation:value('printOrientation'),scaleMode:value('printScaleMode'),percent:value('printPercent'),fitWidth:value('printFitWidth'),fitHeight:value('printFitHeight'),area:value('printArea'),repeatRows:value('printRepeatRows'),repeatColumns:value('printRepeatColumns'),clearRepeatRows:$('printClearRepeatRows').checked,clearRepeatColumns:$('printClearRepeatColumns').checked,marginMode:value('printMarginMode'),pageOrder:value('printPageOrder'),firstPage:value('printFirstPage'),rowHeight:value('printRowHeight'),columnWidth:value('printColumnWidth'),ignoreBreaks:$('printIgnoreBreaks').checked};
      for(const key of ['left','right','top','bottom','header','footer'])out[key]=value('printMargin'+key[0].toUpperCase()+key.slice(1));return out;
    }
    changed(){this.stop(false);this.dirty=true;this.fields();this.controls();this.status('设置已更改，点击更新预览。');}
    stop(clear=false){this.outputController?.abort();this.outputJobs?.reset();this.outputController=this.outputJobs=null;++this.epoch;++this.request;++this.pageRequest;this.jobs?.reset();this.controller?.abort();this.pageController?.abort();this.busy=this.loading=false;if(clear){this.release(this.job);this.job='';this.summary=this.page=this.rendered=null;$('printPaperWrap').replaceChildren();$('printPageCaption').textContent='';$('printEffective').textContent='';$('printWarnings').replaceChildren();$('printRawSettings').replaceChildren();$('printPreviewEmpty').hidden=false;$('printPreviewEmpty').textContent='点击更新预览，准备本表分页。';}this.controls();}
    close(){this.stop(true);if(this.dialog.open)this.dialog.close();}
    resetBook(){this.stop(true);this.bound='';this.dirty=false;$('printSettingsForm').reset();$('printScreenZoom').value='fit';this.fields();this.status('');if(this.dialog.open)this.dialog.close();this.controls();}
    cancel(){this.stop(false);this.status(this.job?'已取消更新，保留上一次预览。':'已取消，可重新准备预览。');}
    async open(){
      if(!this.enabled()){this.toast('请先打开并完成支持打印设置的工作表加载','warning');return;}
      const bound=JSON.stringify(this.binding());if(this.bound&&this.bound!==bound)this.resetBook();this.bound=bound;
      $('printPreviewSource').textContent=`${this.s.fileName||'当前工作簿'} / ${this.s.sheet}`;
      if(!this.dialog.open){$('printSettings').open=typeof matchMedia==='undefined'||!matchMedia('(max-width:680px)').matches;this.dialog.showModal();}
      this.controls();if(!this.job&&!this.busy)await this.refresh();if(this.dialog.open)$('printPreviewCanvas').focus({preventScroll:true});
    }
    async refresh(){
      if(this.busy||!this.enabled()||!this.dialog.open)return;let options;try{options=window.ExcelLensPrintingModel.options(this.input(),this.source(),this.limits);}catch(error){this.status(error.message,true);return;}
      this.stop(false);const request=++this.request,identity=this.identity(),binding=this.binding();this.busy=true;this.controls();this.status('正在准备纸张分页…');let owned='',adopted=false;
      const current=()=>request===this.request&&identity===this.identity()&&this.dialog.open;
      try{
        const done=await this.jobs.execute({...binding,options},{main:true});owned=done.job;if(!current()||!done.current())return;
        const controller=new AbortController();this.controller=controller;
        const summary=await this.api('/api/printing/summary?'+new URLSearchParams({id:owned}),{signal:controller.signal});if(!current()||controller.signal.aborted)return;
        window.ExcelLensPrintingModel.summary(summary,binding,owned,this.limits,this.source());
        let page=null,rendered=null;if(summary.summary.pages){page=await this.readPage(owned,0,summary,binding,controller.signal);if(!current()||controller.signal.aborted)return;rendered=this.renderer.build(summary,page);}
        if(!current())return;const old=this.job;this.job=owned;this.summary=summary;this.index=0;this.page=page;this.rendered=rendered;adopted=true;this.dirty=false;this.commit();this.release(old);
      }catch(error){if(current()&&error.name!=='AbortError')this.status((this.job?'保留上一次预览。':'')+error.message,true);}
      finally{if(owned&&!adopted)this.release(owned);if(current()){this.busy=false;this.controls();}}
    }
    async readPage(job,index,summary,binding,signal){const value=await this.api('/api/printing/page?'+new URLSearchParams({id:job,page:String(index)}),{signal});window.ExcelLensPrintingModel.page(value,binding,job,index,summary,this.limits,this.source());return value;}
    async go(index){
      const pages=this.summary?.summary.pages||0;if(this.busy||!this.job||!this.dialog.open||!Number.isSafeInteger(index)||index<0||index>=pages||!this.loading&&index===this.index)return;
      const serial=++this.pageRequest,identity=this.identity(),job=this.job,summary=this.summary,binding=this.binding();this.pageController?.abort();const controller=new AbortController();this.pageController=controller;this.loading=true;this.controls();this.status('正在读取第 '+(index+1)+' 页…');
      const current=()=>serial===this.pageRequest&&identity===this.identity()&&job===this.job&&this.dialog.open;
      try{const page=await this.readPage(job,index,summary,binding,controller.signal);if(!current()||controller.signal.aborted)return;const rendered=this.renderer.build(summary,page);if(!current())return;this.page=page;this.index=index;this.rendered=rendered;this.commit();}
      catch(error){if(current()&&error.name!=='AbortError'){this.status('保留上一次预览。'+error.message,true);$('printPreviewPage').value=String(this.index+1);}}
      finally{if(current()){this.loading=false;this.controls();}}
    }
    async exportPaper(){
      if(this.dirty||this.busy||this.loading||this.outputController||!this.summary)return;
      const controller=new AbortController(),identity=this.identity(),binding=this.binding(),base=this.job;
      const current=()=>this.outputController===controller&&!controller.signal.aborted&&this.dialog.open&&this.identity()===identity&&this.job===base;
      const jobs=new window.ExcelLensAnalysisJobs({api:this.api,identity:()=>this.identity(),requestIdentity:()=>binding.workbookKey,sleep:ms=>new Promise(resolve=>setTimeout(resolve,ms)),endpoint:'/api/printing',label:'底稿分页',maxPolls:0,resultFromStatus:status=>status});
      this.outputController=controller;this.outputJobs=jobs;this.controls();
      try{
        const mode=$('printOutputMode').value,model=window.ExcelLensPrintingOutput;
        const sheets=mode==='book'?this.s.sheets.filter(s=>!s.state||s.state==='visible').map(s=>s.name):[binding.sheet];
        const title=this.s.fileName+' · '+(mode==='book'?'可见工作表':binding.sheet);
        const output=new model.Output(title,this.limits);
        for(const sheet of sheets){
          const source={...binding,sheet};
          let id=base,summary=this.summary,temporary='';
          try{
            if(sheet!==binding.sheet){
              const task=await jobs.execute({...source,indexIfNeeded:true,options:{usePreviewDefaults:$('printUseDefaults').checked}},{main:true});
              id=temporary=task.job;if(!current())return;
              summary=await this.api('/api/printing/summary?'+new URLSearchParams({id}),{signal:controller.signal});
              window.ExcelLensPrintingModel.summary(summary,source,id,this.limits,this.source());
            }
            if(!current())return;
            const total=summary.summary.pages;if(!total)continue;
            const pages=mode==='current'?[this.index]:model.pageRange(mode==='range'?$('printOutputRange').value:'1-'+total,total,this.limits.outputPages-output.parts.length);
            for(const index of pages){
              this.status(sheet+' · 正在输出第 '+(index+1)+' 页');
              const page=await this.readPage(id,index,summary,source,controller.signal);if(!current())return;
              const rendered=this.renderer.build(summary,page);
              const notes=[...(summary.summary.notices||[]),...(summary.sourceNotices||[])];
              if(!summary.sourceComplete||summary.summary.approximate)notes.push('部分页面设置采用缺省或近似值。');
              if(Object.values(summary.sourceProperties.headerFooter||{}).some(v=>v.type==='text'&&v.value))notes.push('源页眉页脚字段未绘制。');
              output.add(rendered,sheet,index+1,notes);
            }
          }finally{if(temporary)await this.api('/api/printing/clear',{method:'POST',body:JSON.stringify({id:temporary})}).catch(()=>{});}
        }
        if(!current())return;
        const url=URL.createObjectURL(output.file()),link=document.createElement('a');
        link.href=url;link.download=title.replace(/[\\/:*?"<>|]/g,'_')+'_分页底稿.html';link.click();setTimeout(()=>URL.revokeObjectURL(url),60000);
        this.status('已导出 '+output.parts.length+' 页，可打开底稿打印或另存为 PDF');
      }catch(error){if(current())this.status(error.message,true);}
      finally{if(this.outputController===controller){this.outputController=this.outputJobs=null;this.controls();}}
    }
    goInput(){const value=Number($('printPreviewPage').value),pages=this.summary?.summary.pages||0;if(!Number.isSafeInteger(value)||value<1||value>pages){this.status('页序请输入1到'+pages+'之间的整数',true);$('printPreviewPage').value=String(this.index+1);return;}return this.go(value-1);}
    commit(){
      $('printPaperWrap').replaceChildren(...(this.rendered?[this.rendered.node]:[]));$('printPreviewEmpty').hidden=!!this.rendered;$('printPreviewEmpty').textContent='当前打印范围没有可见内容，可调整区域或重复标题。';$('printPreviewPage').value=String(this.index+1);
      const s=this.summary.summary,mm=value=>(Number(window.ExcelLensPrintingModel.units(value))/this.limits.unitsPerPoint*25.4/72).toFixed(1);
      const percent=s.pages?Number(s.scale.numerator)/Number(s.scale.denominator)*100:0;
      $('printEffective').textContent=s.pages?`${mm(s.paper.width)} × ${mm(s.paper.height)} mm · 打印缩放 ${percent<.0001?percent.toExponential(3):Number(percent.toPrecision(6))}%`:'没有可预览的页面';
      $('printPageCaption').textContent=this.page?`第 ${this.index+1} / ${s.pages} 页`+(this.page.content.page.number===this.index+1?'':` · 源页码 ${this.page.content.page.number}`)+` · 区域 ${this.page.content.page.area}`:'';
      this.explain();this.zoom();this.status(this.dirty?'设置已更改，请更新预览。':s.pages?(this.noticeCount?this.noticeCount+' 项提示，见页面详情':''):'没有可预览的页面');this.controls();
    }
    explain(){
      const s=this.summary,notes=new Set([...(s.summary.notices||[]),...(s.sourceNotices||[]),...(this.rendered?.warnings||[])]);
      if(!s.sourceComplete)notes.add('源打印设置有未完整解释的项目，原始属性保留。');
      if(s.summary.approximate)notes.add('此预览包含推导或近似设置，请核对源文件的实际分页。');
      if(Object.values(s.sourceProperties.headerFooter||{}).some(value=>value.type==='text'&&value.value))notes.add('保存的页眉或页脚含文字/字段，当前纸张中未绘制，可在源定义中核对。');
      this.noticeCount=notes.size;
      notes.add('本视图显示单元格分页；图形、页眉页脚字段和打印机效果未绘制，原定义在下方列示。');
      const list=$('printWarnings');list.replaceChildren();for(const text of notes)list.append(make('li','',text));
      const raw=$('printRawSettings');raw.replaceChildren();const origins={source:'源设置','preview-override':'本次覆盖','preview-default':'预览缺省','schema-default':'格式缺省','indexed-data-range':'源数据范围','office-compatible-source':'源手工分页'};
      const labels={paper:'纸张',orientation:'方向',areas:'打印区域',repeatRows:'重复行',repeatColumns:'重复列',firstPageNumber:'起始页码'};
      for(const [key,label] of Object.entries(labels))if(s.summary.origins?.[key])raw.append(make('p','print-setting-origin',label+'：'+(origins[s.summary.origins[key]]||s.summary.origins[key])));
      const groups={pageSetup:'源页面设置',pageMargins:'源页边距',pageSetUpPr:'源适配设置',printOptions:'源打印选项',headerFooter:'源页眉页脚定义'};
      for(const [group,fields] of Object.entries(s.sourceProperties)){const detail=make('details','print-source-group');detail.append(make('summary','',groups[group]||group));for(const [name,value] of Object.entries(fields)){const line=make('p','',name+'：'),definition=make('span','',String(value.value??''));line.append(definition);if(value.status!=='stored')line.append(make('small','',value.reason||'未确认'));detail.append(line);}raw.append(detail);}
    }
    zoom(){
      if(!this.rendered||!this.dialog.open)return;const canvas=$('printPreviewCanvas'),mode=$('printScreenZoom').value,w=this.rendered.width,h=this.rendered.height;
      let scale=mode==='fit'?Math.min((canvas.clientWidth-36)/w,(canvas.clientHeight-36)/h,1.5):mode==='width'?(canvas.clientWidth-36)/w:Number(mode)/100;
      if(!Number.isFinite(scale)||scale<=0)return;scale=Math.min(4,scale);this.rendered.node.style.transform=`scale(${scale})`;$('printPaperWrap').style.width=w*scale+'px';$('printPaperWrap').style.height=h*scale+'px';
    }
  }
  window.ExcelLensPrinting=Printing;
})();
