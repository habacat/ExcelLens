/* ExcelLens: non-modal Excel-style header checklist. This module owns only the
 * dropdown UI. The spreadsheet parser and filter engine remain in Go. */
(() => {
  'use strict';
  class AutoFilterDropdown {
    constructor({state,api,colName,toast,onApply}) {
      this.s=state;
      this.api=api;
      this.colName=colName;
      this.toast=toast;
      this.onApply=onApply;
      this.panel=document.getElementById('autoFilterPopover');
      this.records=[];
      this.selected=new Set();
      this.sheet='';
      this.column=0;
      this.truncated=false;
      this.pending=null;
      this.request=0;
      this.bind();
    }
    get config() {return this.s.autoFilter;}
    isHeader(row,col) {
      const c=this.config;
      return Boolean(c&&c.row===row&&col>=c.startCol&&col<=c.endCol);
    }
    hasFilter(col) {return this.config?.rules.has(col) || false;}
    bind() {
      // Capture prevents grid selection from stealing a click on the filter
      // arrow, including when it belongs to a frozen header cell.
      document.addEventListener('click',e=>{
        const arrow=e.target.closest?.('.auto-filter-button');
        if(arrow) {
          e.preventDefault(); e.stopPropagation();
          const col=Number(arrow.dataset.column);
          if(this.panel.hidden || this.column!==col || this.sheet!==this.s.sheet) this.open(col,arrow);
          else this.hide();
          return;
        }
        if(!this.panel.hidden && !e.target.closest?.('#autoFilterPopover')) this.hide();
      },true);
      document.getElementById('autoFilterClose').addEventListener('click',()=>this.hide());
      document.getElementById('autoFilterCancel').addEventListener('click',()=>this.hide());
      document.getElementById('autoFilterApply').addEventListener('click',()=>this.apply());
      document.getElementById('autoFilterClear').addEventListener('click',()=>this.clearColumn());
      document.getElementById('autoFilterSortAsc').addEventListener('click',()=>this.sort(false));
      document.getElementById('autoFilterSortDesc').addEventListener('click',()=>this.sort(true));
      document.getElementById('autoFilterFind').addEventListener('input',()=>{
        const q=this.needle();
        if(q) this.selected=new Set(this.records.filter(v=>v.value.toLocaleLowerCase().includes(q)).map(v=>v.value));
        this.renderOptions();
      });
      document.getElementById('autoFilterAll').addEventListener('change',e=>{
        if(!e.target.checked&&!this.needle())this.selected.clear();
        for(const item of this.filteredRecords()) {
          if(e.target.checked) this.selected.add(item.value);
          else this.selected.delete(item.value);
        }
        this.renderOptions();
      });
      document.getElementById('autoFilterMore').addEventListener('click',()=>{
        this.displayLimit=Math.min(this.records.length,this.displayLimit+200);
        this.renderOptions();
      });
      document.addEventListener('keydown',e=>{
        if(this.panel.hidden) return;
        if(e.key==='Escape') {e.preventDefault();this.hide();}
        if(e.key==='Enter'&&this.panel.contains(document.activeElement)) {e.preventDefault();this.apply();}
      },true);
      window.addEventListener('resize',()=>this.hide());
      document.getElementById('grid').addEventListener('scroll',()=>this.hide(),{passive:true});
    }
    hide() {
      if(this.pending) {this.pending.abort();this.pending=null;}
      this.panel.hidden=true;
      this.column=0;
      this.configAtOpen=null;
      this.records=[];
      ++this.request;
    }
    needle() {return document.getElementById('autoFilterFind').value.trim().toLocaleLowerCase();}
    filteredRecords() {
      const q=this.needle();
      return q?this.records.filter(v=>v.value.toLocaleLowerCase().includes(q)):this.records;
    }
    position(anchor) {
      const r=anchor.getBoundingClientRect();
      const width=300, height=430;
      this.panel.style.left=`${Math.max(6,Math.min(window.innerWidth-width-8,r.left))}px`;
      this.panel.style.top=`${Math.max(6,Math.min(window.innerHeight-height-8,r.bottom+4))}px`;
    }
    async open(col,anchor) {
      const c=this.config;
      if(!this.s.indexReady||!c||!this.isHeader(c.row,col)) return;
      this.hide();
      this.column=col;
      this.sheet=this.s.sheet;
      this.configAtOpen=c;
      this.displayLimit=180;
      const ticket=++this.request;
      this.panel.hidden=false;
      this.position(anchor);
      document.getElementById('autoFilterTitle').textContent=c.table&&c.names?.[col-1]?c.names[col-1]:`${this.colName(col)} 列筛选`;
      document.getElementById('autoFilterFind').value='';
      document.getElementById('autoFilterOptions').replaceChildren();
      document.getElementById('autoFilterSummary').textContent='正在读取本列选项…';
      document.getElementById('autoFilterApply').disabled=true;
      document.getElementById('autoFilterMore').hidden=true;
      const abort=new AbortController();
      this.pending=abort;
      try {
        const query=new URLSearchParams({sheet:this.sheet,column:String(col),headerRow:String(c.row)});
        const response=await this.api(`/api/filter/distinct?${query}`,{signal:abort.signal});
        if(ticket!==this.request||abort.signal.aborted||this.sheet!==this.s.sheet||c!==this.config) return;
        this.pending=null;
        this.records=(response.values||[]).slice().sort((a,b)=>a.value.localeCompare(b.value,'zh-CN',{numeric:true}));
        this.truncated=Boolean(response.truncated);
        const active=c.rules.get(col);
        this.selected=active?.operator==='in'?new Set(active.values):new Set(this.records.map(v=>v.value));
        this.initialSelected=new Set(this.selected);
        this.renderOptions();
        document.getElementById('autoFilterApply').disabled=false;
        document.getElementById('autoFilterFind').focus({preventScroll:true});
      }catch(err){
        if(abort.signal.aborted) return;
        this.pending=null;
        document.getElementById('autoFilterSummary').textContent=`读取失败：${err.message}`;
        document.getElementById('autoFilterApply').disabled=true;
      }
    }
    renderOptions(){
      const visible=this.filteredRecords();
      const holder=document.getElementById('autoFilterOptions');
      const frag=document.createDocumentFragment();
      for(const item of visible.slice(0,this.displayLimit)) {
        const line=document.createElement('label');
        line.className='auto-filter-item';
        const check=document.createElement('input');check.type='checkbox';
        check.checked=this.selected.has(item.value);
        check.addEventListener('change',()=>{
          if(check.checked)this.selected.add(item.value);else this.selected.delete(item.value);
          this.updateAllCheckbox();
        });
        const name=document.createElement('span');name.className='auto-filter-value';
        name.textContent=item.value===''?'（空白）':item.value;
        name.title=name.textContent;
        const count=document.createElement('span');count.className='auto-filter-count';
        count.textContent=String(item.count);
        line.append(check,name,count);frag.append(line);
      }
      holder.replaceChildren(frag);
      const more=document.getElementById('autoFilterMore');
      more.hidden=visible.length<=this.displayLimit;
      more.textContent=`显示更多（剩余 ${Math.max(0,visible.length-this.displayLimit)} 项）`;
      document.getElementById('autoFilterSummary').textContent=this.truncated?
        `选项未全部列出；请使用条件筛选缩小范围`:
        `${visible.length.toLocaleString()} 个选项 · ${this.selected.size.toLocaleString()} 已勾选`;
      this.updateAllCheckbox();
    }
    updateAllCheckbox(){
      const rows=this.filteredRecords();
      const count=rows.filter(v=>this.selected.has(v.value)).length;
      const all=document.getElementById('autoFilterAll');
      all.checked=rows.length>0&&count===rows.length;
      all.indeterminate=count>0&&count<rows.length;
    }
    async apply(){
      if(!this.config||this.config!==this.configAtOpen||this.sheet!==this.s.sheet)return;
      const col=this.column;
      const unchanged=this.initialSelected&&this.selected.size===this.initialSelected.size&&[...this.selected].every(value=>this.initialSelected.has(value));
      if(unchanged){this.hide();return;}
      if(!this.selected.size){this.toast('至少勾选一个值；要清除条件，请点击「清除此列」','warning');return;}
      if(this.selected.size>2500){this.toast('勾选值超过2500项，请先清除此列或缩小选择范围','warning');return;}
      const full=!this.truncated&&this.selected.size===this.records.length&&this.records.every(item=>this.selected.has(item.value));
      if(full) {
        this.config.rules.delete(col);
      } else {
        this.config.rules.set(col,{column:col,operator:'in',values:[...this.selected]});
      }
      this.hide();
      await this.onApply({recoverable:!!document.getElementById('autoFilterRecoverable')?.checked});
    }
    async clearColumn(){
      if(!this.config||this.config!==this.configAtOpen||this.sheet!==this.s.sheet)return;
      this.config.rules.delete(this.column);
      this.hide();
      await this.onApply({recoverable:!!document.getElementById('autoFilterRecoverable')?.checked});
    }
    async sort(descending){
      if(!this.config||this.config!==this.configAtOpen||this.sheet!==this.s.sheet)return;
      // A direct header sort replaces, rather than silently combines with,
      // multi-level sorting. The user can explicitly configure multi-sort again.
      this.s.sortKeysBySheet?.delete(this.s.sheet);
      this.s.sortHeaderBySheet?.delete(this.s.sheet);
      this.config.sortColumn=this.column;
      this.config.descending=descending;
      this.hide();
      await this.onApply({preserveRules:true,recoverable:!!document.getElementById('autoFilterRecoverable')?.checked});
    }
  }
  window.ExcelLensAutoFilterDropdown=AutoFilterDropdown;
})();
