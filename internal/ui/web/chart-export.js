(() => {
  'use strict';
  const $=id=>document.getElementById(id),abortError=()=>Object.assign(Error('图表导出已取消'),{name:'AbortError'});
  class ChartExport {
    constructor(limits){
      this.limits=limits;this.model=window.ExcelLensChartExportModel;this.surface=window.ExcelLensChartExportSurface;this.serial=0;this.busy=false;
      if(!limits?.types?.length||!limits.scales?.length||!limits.dataBytes||!limits.packageBytes)throw Error('图表导出容量不可用');
      const dialog=document.createElement('dialog');dialog.id='chartExportDialog';dialog.className='chart-export-dialog';dialog.setAttribute('aria-labelledby','chartExportTitle');
      // Static template only. Captured source labels below use textContent.
      dialog.innerHTML=`<div class="chart-export-head"><h3 id="chartExportTitle">导出图片</h3><button id="chartExportClose" type="button" class="mini-button" aria-label="关闭图表导出">关闭</button></div>
        <form id="chartExportForm"><fieldset id="chartExportOptions"><label>格式<select id="chartExportType" class="field-control"></select></label><label>宽度（px）<input id="chartExportWidth" class="field-control" type="number" step="1"></label><label>高度（px）<input id="chartExportHeight" class="field-control" type="number" step="1"></label><label>PNG倍率<select id="chartExportScale" class="field-control"></select></label><label>背景<select id="chartExportBackground" class="field-control"><option value="captured">当前配色</option><option value="white">白底</option></select></label></fieldset>
        <div class="chart-export-actions"><button id="chartExportBuild" class="mini-button" type="submit">生成图片</button><button id="chartExportCancel" class="mini-button" type="button" hidden>取消</button></div></form>
        <p id="chartExportStatus" class="chart-export-status" role="status" aria-live="polite"></p><div id="chartExportResult" class="chart-export-result" hidden><p id="chartExportPackageInfo"></p><div class="chart-export-actions"><a id="chartExportImageDownload" class="mini-button">下载图片</a><a id="chartExportDownload" class="mini-button" download="ExcelLens-Chart.zip">下载ZIP</a></div><div class="chart-export-preview"><img id="chartExportPreview" alt="待导出图表预览"></div></div>`;
      document.body.append(dialog);this.dialog=dialog;
      for(const kind of limits.types)$('chartExportType').append(new Option(kind.toUpperCase(),kind));
      for(const scale of limits.scales)$('chartExportScale').append(new Option(scale+'×',String(scale)));
      for(const [id,min,max]of [['chartExportWidth',limits.minWidth,limits.maxWidth],['chartExportHeight',limits.minHeight,limits.maxHeight]]){$(id).min=String(min);$(id).max=String(max);}
      $('chartExportClose').addEventListener('click',()=>this.close());dialog.addEventListener('cancel',event=>{event.preventDefault();this.close();});dialog.addEventListener('close',()=>{if(!dialog.open)this.clear();});
      $('chartExportForm').addEventListener('submit',event=>{event.preventDefault();this.generate();});$('chartExportCancel').addEventListener('click',()=>this.cancel());
      $('chartExportOptions').addEventListener('change',()=>{this.controls();if(this.accepted)this.status('设置已更改，下载仍为上次结果。');});
      for(const id of ['chartExportImageDownload','chartExportDownload'])$(id).addEventListener('click',event=>{if(!this.revalidate()||this.busy||!this.accepted)event.preventDefault();});
    }
    status(text,error=false){const p=$('chartExportStatus');p.textContent=text;p.classList.toggle('is-error',error);p.setAttribute('role',error?'alert':'status');}
    controls(){
      $('chartExportOptions').disabled=this.busy;$('chartExportBuild').disabled=this.busy;$('chartExportCancel').hidden=!this.busy;
      $('chartExportScale').disabled=this.busy||$('chartExportType').value==='svg';
      for(const id of ['chartExportImageDownload','chartExportDownload']){$(id).setAttribute('aria-disabled',String(this.busy||!this.accepted));$(id).setAttribute('tabindex',this.busy||!this.accepted?'-1':'0');}
    }
    revalidate(){if(this.bound&&!this.bound.current()){this.close();return false;}return !!this.bound;}
    open(input){
      if(this.dialog.open)return false;
      try{
        if(typeof input.current!=='function'||!input.current())throw Error('图表结果已变化，请重新打开导出');
        const drawing=window.ExcelLensAnalyticsCharts.snapshot(input.canvas);
        if(!drawing||drawing.result!==input.expectedResult||drawing.mode!==input.mode)throw Error('图表尚未完成更新，请稍后重试');
        const payload=this.model.capture({...input,theme:drawing.theme,numberSettings:drawing.numberSettings,applicationVersion:document.querySelector('meta[name="app-version"]')?.content||''},this.limits);
        if(!input.current())throw abortError();
        this.clear();this.bound={payload,current:input.current};
        $('chartExportWidth').value=String(this.limits.defaultWidth);$('chartExportHeight').value=String(this.limits.defaultHeight);$('chartExportType').value='png';$('chartExportScale').value=String(this.limits.scales.at(-1));$('chartExportBackground').value='captured';
        this.controls();this.status('');this.dialog.showModal();$('chartExportBuild').focus();return true;
      }catch(error){if(error.name!=='AbortError')input.notify?.(error.message,'error');return false;}
    }
    stop(){++this.serial;this.abort?.abort();this.abort=null;this.busy=false;}
    revoke(result){if(result)for(const url of [result.imageURL,result.packageURL])if(url)URL.revokeObjectURL(url);}
    clear(){this.stop();this.revoke(this.accepted);this.accepted=this.bound=null;$('chartExportResult').hidden=true;for(const id of ['chartExportImageDownload','chartExportDownload'])$(id).removeAttribute('href');$('chartExportPreview').removeAttribute('src');this.status('');}
    close(){this.clear();if(this.dialog.open)this.dialog.close();}
    cancel(){this.stop();this.status(this.accepted?'已取消，保留上次结果。':'已取消');this.controls();}
    specification(){
      const digits=id=>{const value=$(id).value.trim();if(!/^\d+$/.test(value))throw Error('请输入整数图片尺寸');return Number(value);};
      const type=$('chartExportType').value,spec={type,width:digits('chartExportWidth'),height:digits('chartExportHeight'),scale:type==='svg'?1:Number($('chartExportScale').value),background:$('chartExportBackground').value};
      this.model.dimensions(spec,this.limits);return spec;
    }
    async generate(){
      if(!this.revalidate()||!this.dialog.open||this.busy)return;
      let spec;try{spec=this.specification();}catch(error){this.status(error.message,true);return;}
      this.stop();const serial=this.serial,ctrl=new AbortController(),bound=this.bound;this.abort=ctrl;this.busy=true;this.controls();this.status('正在生成…');
      const activity=globalThis.ExcelLensActivity?.begin('正在生成图表文件',{signal:ctrl.signal});
      const current=()=>serial===this.serial&&!ctrl.signal.aborted&&this.bound===bound&&this.dialog.open&&bound.current();
      const checkpoint=async()=>{if(!current())throw abortError();await new Promise(resolve=>setTimeout(resolve,0));if(!current())throw abortError();};
      let candidate;
      try{
        await checkpoint();const dimensions=this.model.dimensions(spec,this.limits),payload=bound.payload;
        const data=this.model.stringify({...payload,output:{...spec,pixelWidth:dimensions.width,pixelHeight:dimensions.height,pixels:dimensions.pixels,generatedAt:new Date().toISOString()}},this.limits.dataBytes);
        const image=await this.surface.image(payload,spec,this.limits,checkpoint,ctrl.signal);await checkpoint();
        if(image.size>(spec.type==='svg'?this.limits.svgBytes:this.limits.imageBytes))throw this.model.capacity();
        const text='ExcelLens 图表导出包\n\n'+payload.chart.title+'\n来源：'+payload.chart.source+'\n指标：'+payload.chart.metric+'\n单位：'+payload.chart.unit+'\n\n'+this.surface.descriptions(payload).join('\n')+'\n\nchart.'+spec.type+'：可嵌入报告的图片。\nexact-data.json：版本1；保留源身份/查询/字段、完整已接受结果、原始和派生精确字符串、缺值/错误状态、图形选择索引及显示/输出设置。不要把近似绘图值替代此文件中的exact。\nJSON数值字符串与前导零编号保持原样，分数不转换为浮点。\nREADME.txt：范围及说明。\n\n本包不重新查询源表；Top-N、首层或选区限制仍存在。SVG使用系统字体，不携带外部资源；图片长标签可能截短或以替代符呈现控制字符，JSON保留完整原文。\n';
        const encoder=new TextEncoder(),bytes=new Uint8Array(await image.arrayBuffer());await checkpoint();
        const blob=await this.model.zip([{name:'chart.'+spec.type,bytes},{name:'exact-data.json',bytes:encoder.encode(data)},{name:'README.txt',bytes:encoder.encode(text)}],this.limits.packageBytes,checkpoint);await checkpoint();
        candidate={spec,image,blob,imageURL:URL.createObjectURL(image),packageURL:null};candidate.packageURL=URL.createObjectURL(blob);
        if(!current())throw abortError();
        const previous=this.accepted;this.accepted=candidate;candidate=null;
        $('chartExportPackageInfo').textContent=spec.type.toUpperCase()+' · '+dimensions.width+' × '+dimensions.height+' · '+window.ExcelLensDisplayFormat.bytes(image.size);
        $('chartExportImageDownload').href=this.accepted.imageURL;$('chartExportImageDownload').download='ExcelLens-Chart.'+spec.type;
        $('chartExportDownload').href=this.accepted.packageURL;$('chartExportPreview').src=this.accepted.imageURL;$('chartExportPreview').alt=payload.chart.title+'（导出预览）';$('chartExportResult').hidden=false;
        this.revoke(previous);this.status('');
      }catch(error){if(current()&&error.name!=='AbortError')this.status(error.message+(this.accepted?'（保留上次结果）':''),true);}
      finally{activity?.end();this.revoke(candidate);if(serial===this.serial){this.abort=null;this.busy=false;this.controls();if(!bound.current())this.close();}}
    }
  }
  let singleton;
  window.ExcelLensChartExport={Controller:ChartExport,open:input=>{singleton??=new ChartExport(JSON.parse($('fileFormats').textContent).chartExport);return singleton.open(input);},revalidate:()=>singleton?.revalidate(),close:()=>singleton?.close()};
})();
