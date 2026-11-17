/* Explicit, source-bound inspection. Exact data stay in the accepted model;
 * display preferences and responsive layout never trigger a source query. */
(() => {
  'use strict';
  const $=id=>document.getElementById(id),make=(tag,cls='',text='')=>{const node=document.createElement(tag);node.className=cls;node.textContent=text;return node;};
  class ProfileCenter {
    constructor({state,api,toast,openDialog,navigateToCell,colName,limits,source,canRead=()=>true,excludeThroughRow=()=>0}){
      Object.assign(this,{s:state,api,toast,openDialog,navigate:navigateToCell,colName,limits,source,canRead,excludeThroughRow});
      this.pending=null;this.serial=0;this.selection=null;this.bound=null;this.page=null;
      $('profileAnalyzeBtn').addEventListener('click',()=>this.analyze());
      $('profileCancelBtn').addEventListener('click',()=>{this.cancel();this.status('已取消分析');});
      $('profileDialog').addEventListener('close',()=>{if(!$('profileDialog').open)this.clear();});
      $('profileDialog').addEventListener('cancel',()=>this.cancel());
      for(const id of ['profileStart','profileEnd','profileFiltered','profileDistribution'])$(id).addEventListener(id==='profileStart'||id==='profileEnd'?'input':'change',()=>this.inputsChanged());
      document.addEventListener('excellens:analysisnumberchange',()=>{if(this.page&&this.same(this.bound)&&$('profileDialog').open)this.render(this.page,this.accepted);});
    }
    binding(){return {key:JSON.stringify([this.s.workbookKey,this.s.book,this.s.sheet,this.s.sheetEpoch,this.s.rowView?.id||'',this.s.filtered?this.s.filterId||'':'',this.s.indexReady,this.excludeThroughRow()]),index:this.s.index};}
    same(bound){const now=this.binding();return !!bound&&now.key===bound.key&&now.index===bound.index;}
    changed(){if(this.bound&&!this.same(this.bound))this.resetBook();}
    cancel(){++this.serial;this.pending?.abort();this.pending=null;$('profileAnalyzeBtn').disabled=false;$('profileCancelBtn').hidden=true;$('profileDetail').setAttribute('aria-busy','false');}
    clear(){this.cancel();this.bound=null;this.selection=null;this.page=null;this.accepted=null;for(const id of ['profileSummary','profileDetail']){$(id).replaceChildren();$(id).hidden=true;}this.status('');}
    resetBook(){this.clear();if($('profileDialog').open)$('profileDialog').close();}
    inputsChanged(){this.cancel();this.page=null;this.accepted=null;for(const id of ['profileSummary','profileDetail']){$(id).replaceChildren();$(id).hidden=true;}this.status('');}
    status(text,error=false){$('profilePending').textContent=text;$('profilePending').hidden=!text;$('profilePending').setAttribute('role',error?'alert':'status');}
    enabled(){return this.s.opened&&this.s.sheet&&this.s.book&&this.s.rowView?.id&&this.s.indexReady&&!this.s.opening&&!this.s.sheetLoadingEpoch&&!this.s.rowViewFailure&&this.canRead();}
    open(){
      if(!this.enabled()){this.toast('请先完成工作表加载','warning');return;}
      if(this.s.multiRanges?.length){this.toast('详细分析请选择一个连续区域','warning');return;}
      this.clear();this.bound=this.binding();const r=this.s.range;
      if(r){
        const points=window.ExcelLensMultiSelection.points(r,this.colName);$('profileStart').value=points.start;$('profileEnd').value=points.end;
        if(this.s.filtered||this.s.rowView)this.selection={...points,filterId:this.s.filtered?this.s.filterId:'',viewId:this.s.rowView?.id||'',excludeThroughRow:this.excludeThroughRow(),epoch:this.s.sheetEpoch};
      }else{const col=this.colName(this.s.selected?.col||1);$('profileStart').value=col+'1';$('profileEnd').value=col+(this.s.usedMaxRow||1);}
      $('profileFiltered').checked=!!(this.s.filtered&&this.s.filterId);$('profileFiltered').disabled=!this.s.filtered;
      this.openDialog('profileDialog');
    }
    input(){
      const input={workbookKey:this.s.workbookKey,book:this.s.book,sheet:this.s.sheet,bindingViewId:this.s.rowView?.id||'',bindingFilterId:this.s.filtered?this.s.filterId||'':'',start:$('profileStart').value.trim(),end:$('profileEnd').value.trim(),filterId:$('profileFiltered').checked?this.s.filterId||'':'',includeDistribution:!!$('profileDistribution').checked};
      const selected=this.selection;
      if(selected&&selected.epoch===this.s.sheetEpoch&&selected.start===input.start&&selected.end===input.end&&selected.filterId===input.filterId){input.visualStart=selected.visualStart;input.visualEnd=selected.visualEnd;input.excludeThroughRow=selected.excludeThroughRow;if(selected.viewId)input.viewId=selected.viewId;}
      return input;
    }
    async analyze(){
      this.changed();if(!this.enabled()||!$('profileDialog').open||!this.bound)return;
      this.cancel();const ctrl=new AbortController(),ticket=this.serial,bound=this.bound,input=this.input(),inputKey=JSON.stringify(input);this.pending=ctrl;
      const current=()=>!ctrl.signal.aborted&&ticket===this.serial&&this.same(bound)&&$('profileDialog').open&&inputKey===JSON.stringify(this.input());
      $('profileAnalyzeBtn').disabled=true;$('profileCancelBtn').hidden=false;$('profileDetail').setAttribute('aria-busy','true');this.status(input.includeDistribution?'正在计算分布…':'正在分析…');
      try{
        const body=JSON.stringify(input);if(new TextEncoder().encode(body).length>this.limits.requestBytes)throw Error('分析请求超过容量');
        const data=await this.api('/api/profile',{method:'POST',headers:{'Content-Type':'application/json'},body,signal:ctrl.signal});
        if(current()){this.render(data,input);this.status('');}
      }catch(error){if(current()&&error.name!=='AbortError')this.status(error.message,true);}
      finally{if(this.pending===ctrl){this.pending=null;$('profileAnalyzeBtn').disabled=false;$('profileCancelBtn').hidden=true;$('profileDetail').setAttribute('aria-busy','false');}}
    }
    format(exact){return exact===''?'—':window.ExcelLensAnalysisNumbers.format({exact,display:exact});}
    render(data,request){
      const d=window.ExcelLensProfileModel.validate(data,request,this.limits,this.source()),summary=document.createDocumentFragment(),detail=document.createDocumentFragment();
      const fields=[['总格数',d.total],['数值',d.number],['空白',d.blank],['不同值',(d.uniqueTruncated?'≥ ':'')+d.unique.toLocaleString('zh-CN')]];
      for(const [label,value]of [['其他值',d.text],['公式',d.formulas],['缺少公式结果',d.cacheMissing],['错误',d.errors]])if(value)fields.push([label,value]);
      for(const [label,value]of fields){const box=make('div','profile-stat');box.append(make('div','profile-number',typeof value==='number'?value.toLocaleString('zh-CN'):value),make('div','profile-caption',label));summary.append(box);}
      const heading=title=>detail.append(make('h4','',title));
      const line=(label,value,numeric=false)=>{const node=make('div','profile-data-line'),text=numeric?this.format(value):String(value),code=make('code','',text);if(numeric)code.title=value;node.append(make('span','',label),code);detail.append(node);};
      heading('数值统计');for(const [label,value]of [['合计',d.sumExact],['平均',d.averageExact],['最小',d.minimumExact],['最大',d.maximumExact]])line(label,value,true);
      if(d.distribution){
        heading('分位数');
        if(!d.number)detail.append(make('p','','无可用数值'));
        for(const q of d.distribution.quantiles)line(q.percent===50?'中位数':q.percent+'%分位',q.value,true);
        if(d.distribution.bins.length){heading('数值分布');const bins=make('div','profile-bins');
          for(const bin of d.distribution.bins){
            let lower=this.format(bin.lower),upper=this.format(bin.upper);
            if(lower===upper&&bin.lower!==bin.upper){const settings={...window.ExcelLensAnalysisNumbers.get(),scientific:false};lower=window.ExcelLensAnalysisNumbers.format({exact:bin.lower,display:bin.lower},settings);upper=window.ExcelLensAnalysisNumbers.format({exact:bin.upper,display:bin.upper},settings);}
            const row=make('div','profile-bin'),label=make('span','profile-bin-range','['+lower+', '+upper+(bin.upperInclusive?']':')'));label.title='['+bin.lower+', '+bin.upper+(bin.upperInclusive?']':')');
            const meter=make('meter');meter.min=0;meter.max=d.number;meter.value=bin.count;meter.setAttribute('aria-label',label.textContent);row.append(label,meter,make('span','profile-bin-count',bin.count.toLocaleString('zh-CN')));bins.append(row);
          }detail.append(bins);
        }
      }
      if(d.topValues.length){heading(d.uniqueTruncated?'常见值（部分）':'常见值');for(const value of d.topValues)line(value.value||'（空白）',value.count.toLocaleString('zh-CN')+' 次');}
      const refs=(title,items)=>{if(!items.length)return;heading(title);const holder=make('div');for(const ref of items){const button=make('button','profile-ref',ref);button.type='button';button.addEventListener('click',()=>{if(!this.same(this.bound))return;const bound=this.bound;$('profileDialog').close();this.navigate(d.sheet,ref,false,()=>this.same(bound));});holder.append(button);}detail.append(holder);};
      refs('错误单元格',d.errorRefs);refs('缺少公式结果',d.missingRefs);
      if(d.warning)detail.append(make('p','profile-warning',d.warning));
      this.page=d;this.accepted=request;$('profileSummary').replaceChildren(summary);$('profileSummary').hidden=false;$('profileDetail').replaceChildren(detail);$('profileDetail').hidden=false;
    }
  }
  window.ExcelLensProfileCenter=ProfileCenter;
})();
