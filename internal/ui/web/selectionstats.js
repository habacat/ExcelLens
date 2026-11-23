/* Status-bar statistics for any combination of Ctrl-selected cells/rectangles.
 * All values come from Go's exact numeric scan of the original workbook, not
 * from cached visible DOM cells or JavaScript floating-point arithmetic. */
(() => {
  'use strict';
  const metrics = [
    ['average','平均值','A'], ['nonEmpty','计数','C'], ['number','数值计数','O'],
    ['minimum','最小值','I'], ['maximum','最大值','M'], ['sum','求和','S']
  ];
  const formats = [['units','带中文单位分隔','H'], ['thousands','使用千位分隔符','T'], ['myriad','按万位分隔','E']];
  const settingsKey = 'ExcelLens.selectionStats';
  function formatStatusNumber(value, mode='units'){
    const raw=String(value),match=raw.match(/^([+-]?)(\d+)(\.\d+)?(\s*≈)?$/);
    if(!match || mode==='plain')return raw;
    const size=mode==='thousands'?3:4, digits=match[2];
    if(digits.length<=size || mode==='units' && digits.length>16)return raw;
    const groups=[];for(let end=digits.length;end>0;end-=size)groups.unshift(digits.slice(Math.max(0,end-size),end));
    if(mode!=='units')return match[1]+groups.join(',')+(match[3]||'')+(match[4]||'');
    const units=['','万','亿','万亿'];let result='';
    groups.forEach((group,i)=>{const power=groups.length-i-1;if(/^0+$/.test(group)&&!match[3])return;result+=group+units[power];});
    return match[1]+result+(match[3]||'')+(match[4]||'');
  }
  class SelectionStats {
    constructor({state,api,colName,toast,profile,excludeThroughRow,editBinding}) {
      this.s=state;this.api=api;this.colName=colName;this.toast=toast;
      this.profile=profile;this.excludeThroughRow=excludeThroughRow||(()=>0);
      this.host=document.getElementById('quickStats');this.ticket=0;
      this.timer=0;this.abort=null;this.cache=new Map();this.lastKey='';
      this.editBinding=editBinding;
      let saved;
      try { saved=JSON.parse(localStorage.getItem(settingsKey)); } catch {}
      this.metrics=new Set(Array.isArray(saved?.metrics)?saved.metrics.filter(k=>metrics.some(([id])=>id===k)):['average','nonEmpty','sum']);
      this.format=formats.some(([id])=>id===saved?.format)?saved.format:saved?.format==='plain'?'plain':'units';
      this.menu=document.getElementById('quickStatsMenu');
      if(this.menu)this.buildMenu();
      this.host.addEventListener('click',event=>{
        const value=event.target.closest('[data-stat-value]')?.dataset.statValue;
        if(value!==undefined){
          if(navigator.clipboard?.writeText){
            navigator.clipboard.writeText(value).then(()=>this.toast('已复制','success')).catch(()=>this.toast('无法复制统计结果','error'));
          } else this.toast('当前环境不支持直接复制，请使用系统剪贴板快捷键','warning');
          return;
        }
        if(this.editBinding?.()){document.getElementById('quickStatsOptions').click();return;}
        const areas=window.ExcelLensMultiSelection.areas(this.s,true);
        if(areas.length>1){this.toast('多选区精确统计显示在状态栏；详细分析可选择一个连续区域');return;}
        this.profile();
      });
      this.host.setAttribute('role','status');this.host.setAttribute('aria-live','polite');
    }
    buildMenu(){
      this.menuButtons=new Map();
      const add=(options,role)=>{
        for(const [id,label,key] of options){
          const button=document.createElement('button'), name=document.createElement('span');
          button.type='button';button.setAttribute('role',role);button.setAttribute('data-menu-keep','');
          name.textContent=label+' ('+key+')';button.append(name);
          const value=role==='menuitemcheckbox'?document.createElement('span'):null;
          if(value){value.className='stat-result';button.append(value);}
          button.addEventListener('click',()=>{
            if(role==='menuitemcheckbox'){
              if(this.metrics.has(id))this.metrics.delete(id);else this.metrics.add(id);
            }else this.format=this.format===id?'plain':id;
            try{localStorage.setItem(settingsKey,JSON.stringify({metrics:[...this.metrics],format:this.format}));}catch{}
            if(this.current)this.render(this.current);else this.updateMenu();
          });
          this.menuButtons.set(id,{button,value,key});
          this.menu.append(button);
        }
      };
      this.menuStatus=document.createElement('div');this.menuStatus.className='stat-status';this.menu.append(this.menuStatus);
      add(metrics,'menuitemcheckbox');
      const divider=document.createElement('div');divider.className='menu-divider';divider.setAttribute('role','separator');this.menu.append(divider);
      add(formats,'menuitemradio');
      this.menu.addEventListener('menuopen',()=>this.updateMenu());
      this.menu.addEventListener('keydown',event=>{
        if(event.ctrlKey||event.metaKey||event.altKey)return;
        const entry=[...this.menuButtons.values()].find(item=>item.key===event.key.toUpperCase());
        if(entry){event.preventDefault();event.stopPropagation();entry.button.focus();entry.button.click();}
      });
      this.updateMenu();
    }
    updateMenu(){
      if(!this.menuButtons)return;
      for(const [id,{button,value}] of this.menuButtons){
        button.setAttribute('aria-checked',String(this.metrics.has(id)||id===this.format));
        if(value){
          const raw=this.metricValue(id);
          value.textContent=raw==null?'—':formatStatusNumber(raw,this.format);
          value.title=raw==null?'':String(raw);
        }
      }
      this.menuStatus.textContent=this.status||'';this.menuStatus.hidden=!this.status;
    }
    metricValue(id){
      const d=this.current;
      if(!d)return null;
      if(id==='nonEmpty'||id==='number')return d[id]??0;
      return d.number>0?d[id]:null;
    }
    clear(){
      ++this.ticket;clearTimeout(this.timer);this.abort?.abort();this.abort=null;
      this.current=null;this.status='';this.updateMenu();
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
      this.current=null;this.status='';this.lastKey='';
      if(!areas.length||!this.s.sheet||!this.s.indexReady){this.host.replaceChildren();this.updateMenu();return}
      const ranges=areas.map(area=>m.points(area,this.colName));
      const upper=areas.reduce((sum,a)=>{
        const rows=this.s.rowVisibility?this.s.rowVisibility.selectionCount(a,this.s.filtered,this.excludeThroughRow()):m.rowCount(a,this.s.filtered,this.excludeThroughRow());
        return sum+rows*(Math.abs(a.col2-a.col1)+1);
      },0);
      if(areas.length>m.LIMIT || upper>400000){
        this.status='超出即时统计限额';this.updateMenu();
        this.show(['选区',areas.length>1?`${areas.length} 处`:`${upper.toLocaleString()} 格`],['统计',this.status]);return;
      }
      this.show(['选区',areas.length>1?`${areas.length} 处`: `${upper.toLocaleString()} 格`]);
      const filterId=this.s.filtered?this.s.filterId:'';
	  const viewId=this.s.rowView?.id||'';
      const through=filterId?this.excludeThroughRow():0;
      const edit=this.editBinding?.();
      const key=JSON.stringify([this.s.sheetEpoch,this.s.sheet,filterId,viewId,through,ranges,edit]);
      this.lastKey=key;
      if(this.cache.has(key)){this.render(this.cache.get(key));return}
      this.status='正在统计…';this.updateMenu();
      const ticket=this.ticket;
      this.timer=setTimeout(async()=>{
        const ctrl=new AbortController();this.abort=ctrl;
        try{
          const result=await this.api('/api/selection/stats',{method:'POST',
            headers:{'Content-Type':'application/json'},
            body:JSON.stringify({sheet:this.s.sheet,filterId,...(viewId?{viewId}:{}),excludeThroughRow:through,ranges,edit}),signal:ctrl.signal});
          if(ctrl.signal.aborted||ticket!==this.ticket||key!==this.lastKey)return;
          this.cache.set(key,result);if(this.cache.size>64)this.cache.delete(this.cache.keys().next().value);
          this.render(result);
        }catch(err){
          if(ctrl.signal.aborted||ticket!==this.ticket)return;
          this.status=err.message||'统计暂不可用';this.updateMenu();
          this.show(['选区',`${areas.length} 处`],['统计','暂不可用']);
        }finally{if(this.abort===ctrl)this.abort=null}
      },upper>6000?420:180);
    }
    render(d){
      if(!d)return;
      this.current=d;this.status='';this.updateMenu();
      const pairs=[];
      for(const [id,label] of metrics){
        const value=this.metricValue(id);
        if(this.metrics.has(id)&&value!=null)pairs.push([label,formatStatusNumber(value,this.format),String(value)]);
      }
      this.show(...pairs);
      this.host.removeAttribute('title');
    }
  }
  window.ExcelLensSelectionStats=SelectionStats;
  window.ExcelLensSelectionStats.formatNumber=formatStatusNumber;
})();
