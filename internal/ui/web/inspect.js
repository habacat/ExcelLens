/* Selection inspector. One read-only Go scan, bounded result preview and
 * O(1) decoration of only mounted grid cells. Results remain navigable after
 * switching sheets; the inspector never changes the source workbook. */
(() => {
  'use strict';
  const $=id=>document.getElementById(id);
  class SelectionInspector {
    constructor({state,api,colName,parseColumn,toast,navigate,onChange,copy,rowLimit=()=>Number.MAX_SAFE_INTEGER,colLimit=()=>window.ExcelLensFileFormats?.current?.()?.maxColumns||16384,largeDataRows=Number.MAX_SAFE_INTEGER,excludeThroughRow=()=>0}) {
      this.state=state;this.api=api;this.colName=colName;this.parseColumn=parseColumn;
      this.toast=toast;this.navigate=navigate;this.onChange=onChange;this.copy=copy;
      this.rowLimit=rowLimit;
      this.colLimit=colLimit;
      this.largeDataRows=largeDataRows;
      this.excludeThroughRow=excludeThroughRow;
      this.matches=[];this.hits=new Set();this.sheet='';this.abort=null;
      this.serial=0;this.showCount=0;this.cache=new Map();this.current=-1;
      this.attach();
    }
    attach(){
      $('inspectClose').addEventListener('click',()=>this.close());
      $('inspectRun').addEventListener('click',()=>this.run());
      $('inspectCancel').addEventListener('click',()=>this.cancel());
      $('inspectMode').addEventListener('change',()=>this.updateFields());
      $('inspectScope').addEventListener('change',()=>this.updateFields());
      $('inspectMore').addEventListener('click',()=>this.appendMore());
      $('inspectPrev').addEventListener('click',()=>this.step(-1));
      $('inspectNext').addEventListener('click',()=>this.step(1));
      $('inspectClear').addEventListener('click',()=>this.clear());
      $('inspectCopy').addEventListener('click',()=>this.copyResults());
      $('inspectPanel').addEventListener('keydown',e=>{if(e.key==='Escape'){e.preventDefault();this.close();}});
    }
    open(mode='formulas'){
      if(!this.state.indexReady)return this.toast('请先打开工作表','warning');
      if(this.state.multiRanges?.length)return this.toast('定位检查请选择一个连续区域','warning');
      const range=this.state.range;
      if(mode==='duplicates'&&this.state.largeData&&(!range||range.col1!==range.col2||Math.abs(range.row2-range.row1)+1>this.largeDataRows))return this.toast(`超大表请先选取不超过${this.largeDataRows.toLocaleString('zh-CN')}行的单列区域，再运行查重`,'warning');
      $('inspectMode').value=mode;
      const s=this.state;
      $('inspectScope').value=s.range?'selection':'used';
      $('inspectColumn').value=this.colName(s.selected?.col||1);
      $('inspectSkipHeader').checked=Boolean(s.autoFilter?.row || (s.usedMaxRow>1));
      this.updateFields();
      $('inspectPanel').hidden=false;
      $('inspectRun').focus({preventScroll:true});
    }
    close(){this.cancel();$('inspectPanel').hidden=true;}
    updateFields(){
      const duplicates=$('inspectMode').value==='duplicates';
      $('inspectScopeWrap').hidden=duplicates;
      $('inspectColumnWrap').hidden=!duplicates;
      $('inspectSkipHeaderWrap').hidden=!duplicates;
      $('inspectHint').textContent=duplicates?'按原始值判重：数字不看显示格式，文本不分大小写，空值不参与。':'';
    }
    options(){
      const s=this.state,mode=$('inspectMode').value;
      let row1=1,col1=1,row2=Math.max(1,s.usedMaxRow),col2=Math.max(1,s.usedMaxCol);
      if(mode==='duplicates'){
        const col=this.parseColumn($('inspectColumn').value);
        if(!col)throw new Error('请输入有效列名，例如 A 或 AGZ');
        col1=col2=col;
        if(s.range && s.range.col1===s.range.col2 && s.range.col1===col){
          row1=Math.min(s.range.row1,s.range.row2);row2=Math.max(s.range.row1,s.range.row2);
        }
        if($('inspectSkipHeader').checked)row1=Math.max(row1,Math.min(row2,Math.max(2,(s.autoFilter?.row||1)+1)));
      }else if($('inspectScope').value==='selection'){
        if(s.range){
          row1=Math.min(s.range.row1,s.range.row2);row2=Math.max(s.range.row1,s.range.row2);
          col1=Math.min(s.range.col1,s.range.col2);col2=Math.max(s.range.col1,s.range.col2);
        }else if(s.selected){
          // Excel's Go To Special on a single active cell considers the
          // worksheet's used range, not just that one cell.
          $('inspectScope').value='used';
        }
      }
      // Clamp to actual content extents, not the grid's optional blank margin.
      row2=Math.min(row2,this.rowLimit());col2=Math.min(col2,this.colLimit());
      const opt={sheet:s.sheet,mode,start:this.colName(col1)+row1,end:this.colName(col2)+row2,
        filterId:s.filtered?s.filterId:'',limit:1200};
      const selected=s.range&&(mode==='duplicates'?s.range.col1===col1&&s.range.col2===col1:$('inspectScope').value==='selection');
      if((s.filtered||s.rowView)&&selected){
        Object.assign(opt,window.ExcelLensMultiSelection.points(s.range,this.colName));
        opt.start=this.colName(col1)+s.range.row1;opt.end=this.colName(col2)+s.range.row2;
        opt.excludeThroughRow=this.excludeThroughRow();
        if(s.rowView)opt.viewId=s.rowView.id;
        if(mode==='duplicates'&&$('inspectSkipHeader').checked)opt.skipThroughRow=Math.max(1,s.autoFilter?.row||1);
      }
      return opt;
    }
    cancel(){this.serial++;this.abort?.abort();this.abort=null;
      $('inspectCancel').hidden=true;$('inspectRun').disabled=false;
      if($('inspectProgress').textContent.includes('正在'))$('inspectProgress').textContent='已取消本次检查';
    }
    async run(){
      if(!this.state.indexReady)return;
      let opt;try{opt=this.options();}catch(e){this.toast(e.message,'warning');return;}
      this.cancel();
      const ticket=++this.serial;const abort=new AbortController();this.abort=abort;
      const book=this.state.workbookKey;
      const current=()=>ticket===this.serial&&!abort.signal.aborted&&book===this.state.workbookKey;
      $('inspectRun').disabled=true;$('inspectCancel').hidden=false;
      $('inspectProgress').textContent='正在检查源数据…';
      try{
        const key=JSON.stringify([book,opt]);
        let res=this.cache.get(key);
        if(!res){
          res=await this.api('/api/inspect',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(opt),signal:abort.signal});
          if(!current())return;
          if(this.cache.size>=10)this.cache.delete(this.cache.keys().next().value);
          this.cache.set(key,res);
        }
        if(!current())return;
        this.sheet=opt.sheet;this.matches=Array.isArray(res.matches)?res.matches:[];
        this.hits=new Set(this.matches.map(x=>x.ref));this.current=-1;this.showCount=0;
        $('inspectProgress').textContent=`${opt.start}:${opt.end} · 共 ${Number(res.matched||0).toLocaleString()} 格 · ${res.mode==='duplicates'?`${res.duplicateGroups||0} 组重复 · `:''}${res.durationMs||0} ms${res.truncated?' · 仅列出前 '+this.matches.length+' 条':''}`;
        $('inspectResultList').replaceChildren();this.appendMore();
        this.onChange();
      }catch(e){if(e.name!=='AbortError'&&ticket===this.serial){$('inspectProgress').textContent=e.message;this.toast(e.message,'error');}}
      finally{if(ticket===this.serial){this.abort=null;$('inspectRun').disabled=false;$('inspectCancel').hidden=true;}}
    }
    appendMore(){
      const end=Math.min(this.matches.length,this.showCount+100),frag=document.createDocumentFragment();
      for(let i=this.showCount;i<end;i++){
        const hit=this.matches[i];
        const btn=document.createElement('button');btn.type='button';btn.className='inspect-hit';
        btn.dataset.index=String(i);
        const ref=document.createElement('strong');ref.textContent=hit.ref;
        const preview=document.createElement('span');preview.textContent=hit.formula||hit.value||'（空白）';
        btn.append(ref,preview);
        btn.title=hit.formula||hit.value||hit.ref;
        btn.addEventListener('click',()=>this.navigateHit(i));
        frag.append(btn);
      }
      $('inspectResultList').append(frag);this.showCount=end;
      $('inspectMore').hidden=end>=this.matches.length;
      $('inspectPrev').disabled=this.matches.length===0;
      $('inspectNext').disabled=this.matches.length===0;
      if(!this.matches.length){
        const empty=document.createElement('div');empty.className='inspect-empty';empty.textContent='没有找到符合条件的单元格';
        $('inspectResultList').append(empty);
      }
    }
    async copyResults(){
      if(!this.matches.length){this.toast('没有可复制的匹配结果','warning');return;}
      const text=this.matches.map(hit=>`${this.sheet}!${hit.ref}\t${String(hit.value||'').replace(/[\t\r\n]/g,' ')}`).join('\n');
      if(await this.copy(text)===false)return;
      this.toast(`已复制 ${this.matches.length} 条结果（制表符分隔）`,'success');
    }
    async navigateHit(index){
      const hit=this.matches[index];if(!hit)return;
      this.current=index;
      $('inspectResultList').querySelectorAll('.inspect-hit.current').forEach(x=>x.classList.remove('current'));
      while(index>=this.showCount)this.appendMore();
      const element=$('inspectResultList').querySelector(`.inspect-hit[data-index="${index}"]`);
      if(element){element.classList.add('current');element.scrollIntoView({block:'nearest'});}
      try{await this.navigate(this.sheet,hit.ref);}catch(e){this.toast(e.message,'error');}
    }
    step(step){if(!this.matches.length)return;this.navigateHit((this.current+step+this.matches.length)%this.matches.length);}
    clear(){this.cancel();this.sheet='';this.matches=[];this.hits.clear();this.showCount=0;this.current=-1;
      $('inspectProgress').textContent='';$('inspectResultList').replaceChildren();
      $('inspectMore').hidden=true;$('inspectNext').disabled=$('inspectPrev').disabled=true;
      this.onChange();
    }
    resetWorkbook(){this.cancel();this.clear();this.cache.clear();}
    decorateCell(element,ref,sheet){
      element.classList.toggle('inspect-highlight',this.sheet===sheet&&this.hits.has(ref));
    }
  }
  window.ExcelLensSelectionInspector=SelectionInspector;
})();
