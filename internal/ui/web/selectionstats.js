/* Status-bar statistics for any combination of Ctrl-selected cells/rectangles.
 * All values come from Go's exact numeric scan of the original workbook, not
 * from cached visible DOM cells or JavaScript floating-point arithmetic. */
(() => {
  'use strict';
  function formatStatusNumber(value){
    const raw=String(value),match=raw.match(/^([+-]?)(\d+)(\.\d+)?(\s*≈)?$/);
    if(!match||match[2].length<=4||match[2].length>16)return raw;
    const digits=match[2],groups=[];for(let end=digits.length;end>0;end-=4)groups.unshift(digits.slice(Math.max(0,end-4),end));
    const units=['','万','亿','万亿'];let result='';
    groups.forEach((group,i)=>{const power=groups.length-i-1;if(/^0+$/.test(group)&&!match[3])return;result+=group+units[power];});
    return match[1]+result+(match[3]||'')+(match[4]||'');
  }
  class SelectionStats {
    constructor({state,api,colName,toast,profile,excludeThroughRow}) {
      this.s=state;this.api=api;this.colName=colName;this.toast=toast;
      this.profile=profile;this.excludeThroughRow=excludeThroughRow||(()=>0);
      this.host=document.getElementById('quickStats');this.ticket=0;
      this.timer=0;this.abort=null;this.cache=new Map();this.lastKey='';
      this.host.addEventListener('click',event=>{
        const value=event.target.closest('[data-stat-value]')?.dataset.statValue;
        if(value!==undefined){
          if(navigator.clipboard?.writeText){
            navigator.clipboard.writeText(value).then(()=>this.toast('已复制','success')).catch(()=>this.toast('无法复制统计结果','error'));
          } else this.toast('当前环境不支持直接复制，请使用系统剪贴板快捷键','warning');
          return;
        }
        const areas=window.ExcelLensMultiSelection.areas(this.s,true);
        if(areas.length>1){this.toast('多选区精确统计显示在状态栏；详细分析可选择一个连续区域');return;}
        this.profile();
      });
      this.host.setAttribute('role','status');this.host.setAttribute('aria-live','polite');
    }
    clear(){
      ++this.ticket;clearTimeout(this.timer);this.abort?.abort();this.abort=null;
      this.host.replaceChildren();this.lastKey='';this.host.removeAttribute('title');
    }
    resetBook(){this.clear();this.cache.clear();}
    stat(label,value,copyable=false){
      const wrap=document.createElement('span');wrap.className='quick-stat';
      const name=document.createElement('span');name.textContent=label+'=';
      const strong=document.createElement('b');strong.textContent=value;
      if(copyable){strong.dataset.statValue=typeof copyable==='string'?copyable:value;strong.title=label+'='+strong.dataset.statValue;strong.classList.add('quick-stat-copy')}
      wrap.append(name,strong);return wrap;
    }
    show(...pairs){this.host.replaceChildren(...pairs.map(([label,value,copyable])=>this.stat(label,String(value),copyable)))}
    schedule(){
      ++this.ticket;clearTimeout(this.timer);this.abort?.abort();this.abort=null;
      const m=window.ExcelLensMultiSelection;
      const areas=m.areas(this.s,true);
      if(!areas.length||!this.s.sheet||!this.s.indexReady){this.host.replaceChildren();return}
      const ranges=areas.map(area=>m.points(area,this.colName));
      const upper=areas.reduce((sum,a)=>{
        const rows=this.s.rowVisibility?this.s.rowVisibility.selectionCount(a,this.s.filtered,this.excludeThroughRow()):m.rowCount(a,this.s.filtered,this.excludeThroughRow());
        return sum+rows*(Math.abs(a.col2-a.col1)+1);
      },0);
      if(areas.length>m.LIMIT || upper>400000){
        this.show(['选区',areas.length>1?`${areas.length} 处`:`${upper.toLocaleString()} 格`],['统计','超出即时统计限额']);return;
      }
      this.show(['选区',areas.length>1?`${areas.length} 处`: `${upper.toLocaleString()} 格`]);
      const filterId=this.s.filtered?this.s.filterId:'';
	  const viewId=this.s.rowView?.id||'';
      const through=filterId?this.excludeThroughRow():0;
      const key=JSON.stringify([this.s.sheetEpoch,this.s.sheet,filterId,viewId,through,ranges]);
      this.lastKey=key;
      if(this.cache.has(key)){this.render(this.cache.get(key));return}
      const ticket=this.ticket;
      this.timer=setTimeout(async()=>{
        const ctrl=new AbortController();this.abort=ctrl;
        try{
          const result=await this.api('/api/selection/stats',{method:'POST',
            headers:{'Content-Type':'application/json'},
            body:JSON.stringify({sheet:this.s.sheet,filterId,...(viewId?{viewId}:{}),excludeThroughRow:through,ranges}),signal:ctrl.signal});
          if(ctrl.signal.aborted||ticket!==this.ticket||key!==this.lastKey)return;
          this.cache.set(key,result);if(this.cache.size>64)this.cache.delete(this.cache.keys().next().value);
          this.render(result);
        }catch(err){
          if(ctrl.signal.aborted||ticket!==this.ticket)return;
          this.show(['选区',`${areas.length} 处`],['统计','暂不可用']);
        }finally{if(this.abort===ctrl)this.abort=null}
      },upper>6000?420:180);
    }
    render(d){
      if(!d)return;
      const pairs=[];
      if(Number(d.number||0)>0){
        pairs.push(['平均值',formatStatusNumber(d.average),String(d.average)]);
      }
      pairs.push(['计数',String(d.nonEmpty||0)]);
      if(Number(d.number||0)>0)pairs.push(['求和',formatStatusNumber(d.sum),String(d.sum)]);
      this.show(...pairs);
      this.host.removeAttribute('title');
    }
  }
  window.ExcelLensSelectionStats=SelectionStats;
  window.ExcelLensSelectionStats.formatNumber=formatStatusNumber;
})();
