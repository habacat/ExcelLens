(() => {
  'use strict';
  const $=id=>document.getElementById(id),make=(tag,cls='',text='')=>{const node=document.createElement(tag);node.className=cls;node.textContent=text;return node;};
  class SelectionQuick {
    constructor({state,api,toast,colName,limits,pinnedThroughRow,canRead=()=>true,navigate}){
      Object.assign(this,{s:state,api,toast,colName,limits,pinnedThroughRow,canRead,navigate});
      if(!limits?.cells||!limits.columns||!limits.segments||!limits.bytes||!limits.requestBytes)throw Error('快速分析容量配置不可用');
      this.model=window.ExcelLensSelectionQuickModel;this.serial=0;this.busy=false;this.dialog=this.create();
      $('selectionQuickClose').addEventListener('click',()=>this.close());this.dialog.addEventListener('cancel',event=>{event.preventDefault();this.close();});
      this.dialog.addEventListener('close',()=>{if(!this.dialog.open)this.clear();});
      $('selectionQuickRun').addEventListener('click',()=>this.run());$('selectionQuickCancel').addEventListener('click',()=>this.cancel());
      for(const id of ['selectionQuickSkip','selectionQuickMinuend','selectionQuickSubtrahend'])$(id).addEventListener('change',()=>{if(this.page)this.status('设置已更改，请重新运行。');});
      for(const event of ['excellens:readingchange','excellens:analysisnumberchange'])document.addEventListener(event,()=>{if(this.page&&this.dialog.open)this.renderSeries();});
      window.addEventListener('resize',()=>{if(this.page&&this.dialog.open)this.draw();});
      this.navigation=0;for(const type of ['pointerdown','keydown'])document.addEventListener(type,()=>{++this.navigation;},true);
    }
    create(){
      const d=make('dialog','float-dialog selection-quick-dialog');d.id='selectionQuickDialog';d.setAttribute('aria-labelledby','selectionQuickTitle');
      // Static markup only; every source string below uses textContent.
      d.innerHTML=`<div class="dialog-head"><div id="selectionQuickTitle" class="dialog-title">选区快速分析</div><button id="selectionQuickClose" type="button" class="dialog-close" aria-label="关闭快速分析">×</button></div>
        <p id="selectionQuickSource" class="selection-quick-source"></p><div class="selection-quick-tools"><label><input id="selectionQuickSkip" type="checkbox">跳过首行</label><label>被减列<select id="selectionQuickMinuend" class="field-control"></select></label><span aria-hidden="true">−</span><label>减列<select id="selectionQuickSubtrahend" class="field-control"></select></label><button id="selectionQuickRun" type="button" class="mini-button">运行分析</button></div>
        <div id="selectionQuickBody" class="selection-quick-body"></div>
        <div class="selection-quick-footer"><p id="selectionQuickStatus" role="status" aria-live="polite"></p><button id="selectionQuickCancel" type="button" class="mini-button" hidden>取消分析</button></div>`;
      document.body.append(d);return d;
    }
    binding(){
      const areas=window.ExcelLensMultiSelection.areas(this.s,true),area=areas.length===1?areas[0]:null;
      const points=area?window.ExcelLensMultiSelection.points(area,this.colName):null,through=this.pinnedThroughRow();
      const request={workbookKey:this.s.workbookKey,book:this.s.book,sheet:this.s.sheet,viewId:this.s.rowView?.id||'',filterId:this.s.filtered?this.s.filterId||'':'',excludeThroughRow:through,...points};
      const first=area?Math.min(area.col1,area.col2):0,last=area?Math.max(area.col1,area.col2):0;
      const rows=area?(this.s.rowVisibility?this.s.rowVisibility.selectionCount(area,this.s.filtered,through):window.ExcelLensMultiSelection.rowCount(area,this.s.filtered,through)):0;
      const sourceKey=JSON.stringify([request.workbookKey,request.book,request.sheet,request.viewId,request.filterId,through,this.s.sheetEpoch,this.s.opened,this.s.indexReady,this.s.filtered]);
      return {request,area,first,last,rows,index:this.s.index,sourceKey,key:JSON.stringify([request,this.s.sheetEpoch,this.s.opened,this.s.indexReady,this.s.filtered,areas])};
    }
    same(bound){const now=this.binding();return !!bound&&bound.key===now.key&&bound.index===now.index;}
    ready(){return !!this.s.opened&&!!this.s.indexReady&&!this.s.opening&&!this.s.sheetLoadingEpoch&&!this.s.rowViewFailure&&!!this.s.rowView?.id&&this.canRead();}
    changed(){if(this.bound&&(!this.same(this.bound)||!this.ready()))this.close();window.ExcelLensChartExport?.revalidate();}
    status(text,error=false){const p=$('selectionQuickStatus');p.textContent=text;p.classList.toggle('is-error',error);p.setAttribute('role',error?'alert':'status');}
    controls(){
      $('selectionQuickCancel').hidden=!this.busy;$('selectionQuickRun').disabled=this.busy;
      for(const id of ['selectionQuickSkip','selectionQuickMinuend','selectionQuickSubtrahend'])$(id).disabled=this.busy;
      $('selectionQuickBody').setAttribute('aria-busy',String(this.busy));
	  if($('selectionQuickImage'))$('selectionQuickImage').disabled=this.busy||!this.page;
    }
    open(){
      if(!this.ready()){this.toast('请先在已完成加载的工作表中选择连续区域','warning');return;}
      if(this.dialog.open)return;
      const bound=this.binding(),{first,last,rows}=bound;
      if(!bound.area||first<1||last>this.s.index.maxCol||rows<1){this.toast('快速分析请选择已用范围内的一个连续区域','warning');return;}
      if(last-first+1>this.limits.columns||rows*(last-first+1)>this.limits.cells){this.toast(`快速分析最多 ${this.limits.columns} 列、${this.limits.cells.toLocaleString()} 格，请缩小选区`,'warning');return;}
      this.clear();++this.navigation;this.bound=bound;
      $('selectionQuickSource').textContent=`${this.s.sheet}!${bound.request.start}:${bound.request.end} · ${rows.toLocaleString()} 行`;
      $('selectionQuickSkip').checked=false;
      const left=$('selectionQuickMinuend'),right=$('selectionQuickSubtrahend');left.replaceChildren(new Option('不比较','0'));right.replaceChildren();
      for(let col=first;col<=last;col++){left.append(new Option(this.colName(col),String(col)));right.append(new Option(this.colName(col),String(col)));}
      left.value='0';right.value=String(first);this.controls();this.status('');this.dialog.showModal();$('selectionQuickRun').focus();
    }
    stop(){++this.serial;this.abort?.abort();this.abort=null;this.busy=false;}
    clear(){this.stop();this.bound=this.page=null;this.series=this.drawn=null;window.ExcelLensChartExport?.revalidate();$('selectionQuickBody').replaceChildren();this.status('');}
    close(){this.clear();if(this.dialog.open)this.dialog.close();}
    resetBook(){this.close();}
    cancel(){this.stop();this.status(this.page?'已取消，保留上次结果。':'已取消');this.controls();}
    async run(){
      this.changed();if(!this.dialog.open||!this.bound||this.busy||!this.ready())return;
      const bound=this.bound,minuend=Number($('selectionQuickMinuend').value),subtrahend=minuend?Number($('selectionQuickSubtrahend').value):0;
      if(minuend&&(minuend===subtrahend||minuend<bound.first||minuend>bound.last||subtrahend<bound.first||subtrahend>bound.last)){this.status('差额请选择选区内两个不同的源列。',true);return;}
      const request={...bound.request,skipFirstRow:$('selectionQuickSkip').checked,minuend,subtrahend};
      this.stop();const serial=this.serial,ctrl=new AbortController();this.abort=ctrl;this.busy=true;this.controls();this.status('正在分析…');
      const current=()=>this.serial===serial&&!ctrl.signal.aborted&&this.dialog.open&&this.same(bound)&&this.ready();
      try{
        const body=JSON.stringify(request);if(new TextEncoder().encode(body).length>this.limits.requestBytes)throw Error('请求超过快速分析容量');
        const page=await this.api('/api/selection/quick',{method:'POST',body,signal:ctrl.signal});if(!current())return;
        this.model.validate(page,request,this.limits,bound.rows,bound.first,bound.last,bound.index.maxRow);
        const view=this.build(page);if(!current())return;
        this.page=page;$('selectionQuickBody').replaceChildren(view);this.status(`${page.rows.toLocaleString()} 行 × ${page.columns.length} 列${page.skipFirstRow?'（跳过首行）':''}`);this.renderSeries();
      }catch(error){if(current()&&error.name!=='AbortError')this.status(error.message+(this.page?'（保留上次结果）':''),true);}
      finally{if(this.serial===serial){this.abort=null;this.busy=false;this.controls();}}
    }
    table(headers,rows,cls=''){
      const wrap=make('div','selection-quick-table-wrap '+cls),table=make('table'),head=make('thead'),tr=make('tr');
      for(const label of headers){const th=make('th','',label);th.scope='col';tr.append(th);}head.append(tr);table.append(head);const body=make('tbody');
      for(const row of rows){const line=make('tr');for(const value of row){const td=make('td');if(typeof value==='object')td.append(value);else td.textContent=String(value);line.append(td);}body.append(line);}table.append(body);wrap.append(table);return wrap;
    }
    build(page){
      const fragment=document.createDocumentFragment();fragment.append(make('h4','','逐列分布'));
      fragment.append(this.table(['列','负数','零','正数','空白','其他值','错误','公式无结果','无效数值'],page.columns.map(c=>[this.colName(c.column),c.counts.negative,c.counts.zero,c.counts.positive,c.counts.blank,c.counts.other,c.counts.errors,c.counts.missing,c.counts.invalid])));
      const tools=make('div','selection-quick-series-tools'),label=make('label','','查看统计与趋势 '),select=make('select','field-control');select.id='selectionQuickSeries';
      for(const col of page.columns)select.append(new Option('源列 '+this.colName(col.column),String(col.column)));
      if(page.difference)select.append(new Option(`差额 ${this.colName(page.minuend)} − ${this.colName(page.subtrahend)}`,'difference'));
      select.value=page.difference?'difference':String(page.firstColumn);select.addEventListener('change',()=>this.renderSeries());label.append(select);tools.append(label);
	  const image=make('button','mini-button','导出趋势图片');image.id='selectionQuickImage';image.type='button';image.addEventListener('click',()=>this.exportImage());tools.append(image);fragment.append(tools);
      const summary=make('div','selection-quick-summary');summary.id='selectionQuickSummary';fragment.append(summary);
      const note=make('p','selection-quick-note');note.id='selectionQuickPairing';fragment.append(note);
      fragment.append(make('h4','','分段平均趋势'));
      const chart=make('div','selection-quick-chart'),canvas=make('canvas');canvas.id='selectionQuickChart';canvas.setAttribute('role','img');canvas.setAttribute('aria-label','分段平均趋势');
      chart.append(canvas,make('div','analysis-chart-tooltip'));fragment.append(chart);
      const details=make('details');details.id='selectionQuickSegments';details.append(make('summary','','分段明细'));const holder=make('div');holder.id='selectionQuickSegmentTable';details.append(holder);fragment.append(details);return fragment;
    }
    format(exact){return exact===''?'—':window.ExcelLensAnalysisNumbers.format({exact,display:exact});}
    numeric(exact){const span=make('span','selection-quick-value',this.format(exact));span.title=exact===''?'无有效数值':'精确值 '+exact;return span;}
    sourceButton(row,col){
      const ref=this.colName(col)+row,button=make('button','selection-quick-ref',ref);button.type='button';button.title='定位源单元格 '+ref;button.addEventListener('click',()=>this.locate(ref));return button;
    }
    renderSeries(){
      this.changed();if(!this.page||!this.dialog.open)return;
      const page=this.page,key=$('selectionQuickSeries').value,delta=key==='difference';
      const series=delta?page.difference:page.columns.find(c=>String(c.column)===key);if(!series)return;
      this.series=series;const summary=series.summary,holder=$('selectionQuickSummary');holder.replaceChildren();
      const col=delta?page.minuend:Number(key);
      for(const [title,exact,row] of [['数值数量',String(summary.count)],['合计',summary.sum],['平均',summary.average],['最小',summary.minimum,summary.minimumRow],['最大',summary.maximum,summary.maximumRow]]){
        const card=make('div');card.append(make('small','',title),this.numeric(exact));if(row)card.append(this.sourceButton(row,col));holder.append(card);
      }
      $('selectionQuickPairing').textContent=delta?`配对 ${series.paired.toLocaleString()} 行`+(series.unpaired?` · 未配对 ${series.unpaired.toLocaleString()} 行`:''):'';
      $('selectionQuickSegmentTable').replaceChildren(this.table(['段','选中行序','源首行→末行','数值数','合计','平均','最小','最大'],series.segments.map((p,i)=>[
        i+1,`${p.first}–${p.last}`,`${p.firstRow} → ${p.lastRow}`,p.summary.count,this.numeric(p.summary.sum),this.numeric(p.summary.average),this.numeric(p.summary.minimum),this.numeric(p.summary.maximum)
      ])));
      this.draw();
    }
    draw(){if(this.dialog.open&&this.page&&this.series){const series=this.series,plot=this.model.chart(series);window.ExcelLensAnalyticsCharts.draw($('selectionQuickChart'),plot,'line',keys=>{const at=Number(keys[0])-1,p=series.segments[at];if(Number.isInteger(at)&&p)this.status(`第 ${at+1} 段 · 选中行 ${p.first}–${p.last}；精确平均 ${p.summary.average}，源行 ${p.firstRow} → ${p.lastRow}。`);});this.drawn={plot,page:this.page,series};window.ExcelLensChartExport?.revalidate();}}
	 exportImage(){
	   if(!this.page||!this.drawn||this.busy||!this.ready()||!this.same(this.bound))return;
	   const page=this.page,bound=this.bound,series=this.series,key=$('selectionQuickSeries').value,expectedResult=this.drawn.plot;
	   const current=()=>this.dialog.open&&this.page===page&&this.series===series&&!this.busy&&this.ready()&&this.same(bound)&&$('selectionQuickSeries').value===key;
	   window.ExcelLensChartExport?.open({kind:'selection-quick',canvas:$('selectionQuickChart'),expectedResult,mode:'line',seriesKey:key,result:page,
	     origin:{workbookKey:page.workbookKey,book:page.book,sheet:page.sheet,viewId:page.viewId},title:'选区分段趋势',source:(this.s.fileName||'当前工作簿')+' / '+page.sheet,
       scope:page.start+':'+page.end+' · '+page.rows+'行'+(page.skipFirstRow?'（跳过首行）':''),
	     metric:key==='difference'?'差额 '+this.colName(page.minuend)+' − '+this.colName(page.subtrahend)+' · 分段平均':'源列 '+this.colName(Number(key))+' · 分段平均',unit:'源指标原单位（未换算）',current,notify:this.toast});
	 }
    async locate(ref){
      if(!this.page||!this.same(this.bound)||this.busy||!this.ready())return;
      const bound=this.bound,sheet=this.page.sheet;this.close();const ticket=++this.navigation;
      const guard=()=>{const now=this.binding();return !this.dialog.open&&this.navigation===ticket&&now.sourceKey===bound.sourceKey&&now.index===bound.index&&this.ready();};
      try{await this.navigate(sheet,ref,guard);}catch(error){if(guard())this.toast(error.message,'error');}
    }
  }
  window.ExcelLensSelectionQuick=SelectionQuick;
})();
