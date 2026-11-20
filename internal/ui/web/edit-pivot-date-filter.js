(() => {
  'use strict';
  const el=id=>document.getElementById('editCopyPivotDateFilter'+id);
  const fixed={dateEqual:'等于',dateNotEqual:'不等于',dateOlderThan:'早于',dateOlderThanOrEqual:'早于或等于',dateNewerThan:'晚于',dateNewerThanOrEqual:'晚于或等于',dateBetween:'介于',dateNotBetween:'不介于'};
  const relative={today:'今天',yesterday:'昨天',tomorrow:'明天',thisWeek:'本周',lastWeek:'上周',nextWeek:'下周',thisMonth:'本月',lastMonth:'上月',nextMonth:'下月',thisQuarter:'本季度',lastQuarter:'上季度',nextQuarter:'下季度',thisYear:'今年',lastYear:'去年',nextYear:'明年',yearToDate:'年初至今'};
  const annual={};
  for(let i=1;i<=4;i++)annual['Q'+i]='第'+i+'季度';
  for(let i=1;i<=12;i++)annual['M'+i]=i+'月';
  const names={...fixed,...relative,...annual};
  function iso(value,withTime) {
    if(!value)return '';
    if(!withTime)return value.slice(0,10);
    if(value.length===10)return value+'T00:00:00';
    return value.length===16?value+':00':value;
  }
  function describe(f){
    const bounds=[f.start,f.end].filter(Boolean).map(v=>v.replace('T',' '));
    return [f.field,names[f.kind],bounds.join(' 至 ')].filter(Boolean).join(' ');
  }
  class PivotDateFilter {
    constructor(apply) {
      this.panel=document.getElementById('editCopyPivotDateFilters');
      this.field=el('Field');this.kind=el('Kind');this.start=el('Start');this.end=el('End');this.time=el('Time');
      for(const [name,items] of [['相对日期',relative],['指定日期',fixed],['每年的指定时段',annual]]) {
        const group=document.createElement('optgroup');group.label=name;
        group.append(...Object.entries(items).map(([key,text])=>new Option(text,key)));this.kind.append(group);
      }
      this.field.onchange=()=>this.load();
      this.kind.onchange=()=>this.controls();
      this.time.onchange=()=>{this.storedTime=false;this.precision();this.controls();};
      this.start.oninput=this.end.oninput=()=>this.controls();
      el('Apply').onclick=()=>{if(!el('Apply').disabled)apply(this.read());};
      el('Clear').onclick=()=>{if(this.current)apply({field:this.field.value,kind:'clear'});};
      this.panel.addEventListener('toggle',()=>this.activate(this.panel.open));
      document.getElementById('editCopyPivotFields').addEventListener('focusin',e=>this.activate(this.panel.open&&this.panel.contains(e.target)));
    }
    bind(info,fields){
      const binding=JSON.stringify([info?.part,fields,info?.dateFilters]);
      if(binding===this.binding)return;
      this.binding=binding;
      const same=this.info?.part===info?.part,previous=same?this.field.value:'';
      this.info=info;
      const box=this.panel;box.hidden=!fields.length;
      const active=(info?.dateFilters||[]).filter(f=>fields.includes(f.field));
      this.field.replaceChildren(...fields.map(name=>new Option(active.some(f=>f.field===name)?name+' · 已筛选':name,name)));
      this.field.value=fields.includes(previous)?previous:active[0]?.field||fields[0]||'';
      box.querySelector('summary').textContent=active.length===1?'日期筛选 · '+describe(active[0]):active.length?'日期筛选 · '+active.length+' 项':'日期筛选';
      this.load();
    }
    get current(){return this.info?.dateFilters?.find(f=>f.field===this.field.value);}
    activate(editing){
      this.editing=editing;
      this.footer(this.available,this.blocked);
    }
    footer(available,blocked){
      this.available=available;this.blocked=blocked;
      const editing=available&&!this.panel.hidden&&this.panel.open&&this.editing;
      el('Apply').hidden=!editing;
      el('Apply').disabled=blocked||!this.valid;
      document.getElementById('editCopyPivotApply').hidden=!available||editing;
    }
    load(){
      const filter=this.current;
      this.kind.value=filter?.kind||'thisMonth';
      this.time.checked=Boolean(filter?.start&&!filter.wholeDay);
      this.storedTime=!!filter?.wholeDay&&[filter.start,filter.end].some(v=>v?.includes('T'));
      this.precision();
      this.start.value=iso(filter?.start,this.withTime);this.end.value=iso(filter?.end,this.withTime);
      this.controls();
    }
    get withTime(){return this.time.checked||this.storedTime;}
    precision(){
      for(const input of [this.start,this.end]){
        const text=input.value;
        input.type=this.withTime?'datetime-local':'date';
        input.step='1';input.value=iso(text,this.withTime);
      }
    }
    read(){
      const kind=this.kind.value,spec={field:this.field.value,kind};
      if(fixed[kind]){
        spec.start=iso(this.start.value,this.withTime);
        if(!this.time.checked)spec.wholeDay=true;
        if(kind.endsWith('Between'))spec.end=iso(this.end.value,this.withTime);
      }
      return spec;
    }
    controls(){
      const spec=this.read(),bounded=!!fixed[spec.kind],range=spec.end!==undefined;
      el('Bounds').hidden=!bounded;this.end.parentElement.hidden=!range;
      el('StartLabel').textContent=range?'开始日期':'日期';
      const reversed=range&&!!spec.start&&!!spec.end&&spec.end<spec.start;
      const first=iso('0001-01-01',this.withTime), last=this.withTime?'9999-12-31T23:59:59':'9999-12-31';
      this.start.min=first;this.end.max=last;
      this.end.min=range&&this.start.value?this.start.value:first;
      this.start.max=range&&this.end.value?this.end.value:last;
      const outOfBounds=input=>input.validity.rangeUnderflow||input.validity.rangeOverflow;
      const outside=outOfBounds(this.start)||(range&&outOfBounds(this.end));
      el('Error').textContent=!bounded?'':reversed?'结束日期不能早于开始日期':outside?'请输入 1 至 9999 年之间的日期':'';
      this.valid=!!spec.field&&(!bounded||!!spec.start&&this.start.validity.valid&&(!range||!!spec.end&&this.end.validity.valid));
      this.footer(this.available,this.blocked);
      el('Clear').hidden=!this.current;
    }
  }
  window.ExcelLensPivotDateFilter=PivotDateFilter;
})();
