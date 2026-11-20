(() => {
  'use strict';
  const el=id=>document.getElementById('editCopyPivot'+id);
  const criteria={captionEqual:'等于',captionNotEqual:'不等于',captionBeginsWith:'开头是',captionNotBeginsWith:'开头不是',captionEndsWith:'结尾是',captionNotEndsWith:'结尾不是',captionContains:'包含',captionNotContains:'不包含',captionGreaterThan:'大于',captionGreaterThanOrEqual:'大于或等于',captionLessThan:'小于',captionLessThanOrEqual:'小于或等于',captionBetween:'介于',captionNotBetween:'不介于',clear:'清除筛选'};
  const options=names=>names.map(n=>new Option(n,n));
  class PivotCriteria {
    constructor(commit) {
      this.commit=commit;
      this.dateFilter=new window.ExcelLensPivotDateFilter(spec=>commit('pivotDateFilter','dateFilter',spec));
      el('LabelKind').replaceChildren(...Object.entries(criteria).map(([k,v])=>new Option(v,k)));
      el('LabelField').onchange=()=>this.label();
      el('LabelKind').onchange=el('LabelStart').oninput=el('LabelEnd').oninput=()=>this.controls();
      el('DateField').onchange=()=>this.date();
      el('DateUnits').onchange=el('Interval').oninput=()=>this.controls();
      el('DateStart').oninput=el('DateEnd').oninput=()=>this.controls();
      el('LabelApply').onclick=()=>this.commit('pivotLabelFilter','labelFilter',this.labelSpec());
      el('DateApply').onclick=()=>this.commit('pivotDateGroup','dateGroup',this.dateSpec());
    }
    bind(info) {
      this.info=info;
      const axis=[...(info?.axes?.rows||[]),...(info?.axes?.columns||[])];
      el('Labels').hidden=!axis.length;el('LabelField').replaceChildren(...options(axis));
      const dates=[...new Set(axis.flatMap(field=>{
        const base=info.groupFields?.[field]?.source;
        return base ? [base] : info.dateFields?.includes(field) ? [field] : [];
      }))];
      el('Dates').hidden=!dates.length;el('DateField').replaceChildren(...options(dates));
      this.dateFilter.bind(info,dates);
      this.label();this.date();
    }
    label(){
      const s=this.info?.labelFilters?.find(f=>f.field===el('LabelField').value);
      el('LabelKind').value=s?.kind||'captionContains';el('LabelStart').value=s?.value||'';el('LabelEnd').value=s?.second||'';this.controls();
    }
    labelSpec(){
      const field=el('LabelField').value,kind=el('LabelKind').value;
      if(kind==='clear')return {field,kind};
      const spec={field,kind,value:el('LabelStart').value};if(kind.endsWith('Between'))spec.second=el('LabelEnd').value;return spec;
    }
    date(){
      const s=this.info?.dateGroups?.find(g=>g.field===el('DateField').value);
      for(const input of el('DateUnits').querySelectorAll('input'))input.checked=(s?.units||['years','months']).includes(input.value);
      el('DateStart').value=s?.start||'';el('DateEnd').value=s?.end||'';
      el('Bounds').open=Boolean(s?.start||s?.end);
      el('Interval').value=s?.interval||1;this.controls();
    }
    dateSpec(){
      const field=el('DateField').value,units=[...el('DateUnits').querySelectorAll('input:checked')].map(n=>n.value);
      const s={field,units};
      if(units.length===1 && units[0]==='days')s.interval=el('Interval').valueAsNumber;
      if(units.length)for(const [key,id] of [['start','DateStart'],['end','DateEnd']]){
        const value=el(id).value;
        if(value)s[key]=value.length===16 ? value+':00' : value;
      }
      return s;
    }
    controls(){
      const label=this.labelSpec(),date=this.dateSpec();
      el('LabelStart').parentElement.hidden=label.kind==='clear';el('LabelEnd').parentElement.hidden=(!label.kind.endsWith('Between'));
      el('LabelApply').disabled=!label.field || (label.kind==='clear' ? !this.info?.labelFilters?.some(f=>f.field===label.field) : !label.value || label.second==='');
      el('Interval').parentElement.hidden=date.interval===undefined;
      const bounds=el('Bounds');bounds.hidden=!date.units.length;
      bounds.querySelector('summary').textContent=date.start||date.end?'起止时间（自定义）':'起止时间（自动）';
      el('DateStart').max=date.end||'';el('DateEnd').min=date.start||'';
      const invalidBounds=date.units.length&&(!el('DateStart').validity.valid||!el('DateEnd').validity.valid);
      el('DateApply').textContent=date.units.length?'应用分组':'取消分组';
      el('DateApply').disabled=!date.field || invalidBounds || (!date.units.length&&!this.info?.dateGroups?.some(g=>g.field===date.field)) || (date.interval!==undefined&&!(Number.isInteger(date.interval)&&date.interval>=1&&date.interval<=32767));
    }
  }
  window.ExcelLensPivotCriteria=PivotCriteria;
})();
