/* Multi-level sort UI: collects at most five keys and delegates scanning plus
 * stable sorting to Go. Never loads a whole sheet into browser memory. */
(() => {
  'use strict';
  class MultiSort {
    constructor({state, parseColumn, colName, toast, openDialog, submit, clear}) {
      this.s=state;this.parseColumn=parseColumn;this.colName=colName;this.toast=toast;
      this.openDialog=openDialog;this.submit=submit;this.clear=clear;
      this.holder=document.getElementById('sortLevels');
      document.getElementById('sortAddLevel').addEventListener('click',()=>this.add());
      document.getElementById('sortApply').addEventListener('click',()=>this.apply());
      document.getElementById('sortReset').addEventListener('click',()=>this.reset());
    }
    show() {
      if(!this.s.sheet||!this.s.indexReady)return;
      const header=this.s.autoFilter?.row??Math.min(1,this.s.usedMaxRow);
      document.getElementById('sortHeaderRow').value=String(this.s.sortHeaderBySheet.get(this.s.sheet)??header);
      this.holder.replaceChildren();
      const keys=this.s.sortKeysBySheet.get(this.s.sheet)||[{column:this.s.selected?.col||1,descending:false}];
      for(const k of keys)this.add(k);
      this.openDialog('multiSortDialog');
    }
    add(key={column:1,descending:false}) {
      if(this.holder.children.length>=5){this.toast('最多5级排序','warning');return}
      const row=document.createElement('div');row.className='sort-level';
      const number=document.createElement('span');number.className='sort-level-number';
      const col=document.createElement('input');col.className='field-control sort-column';
      col.placeholder='列字母 / 序号';col.title='例如 A、AB 或 28';col.value=this.colName(key.column||1);
      col.setAttribute('aria-label','排序列');col.spellcheck=false;
      const dir=document.createElement('select');dir.className='field-control sort-direction';dir.setAttribute('aria-label','排序方向');
      for(const [value,label] of [['asc','从小到大 ↑'],['desc','从大到小 ↓']]){
        const opt=document.createElement('option');opt.value=value;opt.textContent=label;dir.appendChild(opt);
      }
      dir.value=key.descending?'desc':'asc';
      const mode=document.createElement('select');mode.className='field-control sort-mode';mode.setAttribute('aria-label','排序方式');
      for(const [value,label] of [['','按原始类型'],['number','按数值（金额）'],['text','按显示文本'],['natural','自然顺序（文本数字）']]){
        const opt=document.createElement('option');opt.value=value;opt.textContent=label;mode.appendChild(opt);
      }
      mode.value=key.mode||'';
      const remove=document.createElement('button');remove.type='button';remove.className='sort-level-remove';remove.textContent='×';remove.title='删除排序条件';
      remove.addEventListener('click',()=>{if(this.holder.children.length===1){this.toast('请至少保留一个排序条件','warning');return;}row.remove();this.relabel();});
      row.append(number,col,mode,dir,remove);this.holder.appendChild(row);this.relabel();
    }
    relabel(){[...this.holder.children].forEach((r,i)=>r.querySelector('.sort-level-number').textContent=String(i+1).padStart(2,'0'));}
    values(){
      const keys=[],seen=new Set();
      for(const row of this.holder.children){
        const raw=row.querySelector('.sort-column').value;
        const column=this.parseColumn(raw);
        if(!column||column>this.s.usedMaxCol)throw Error(`排序列「${raw}」无效或超出有效范围`);
        if(seen.has(column))throw Error(`排序列 ${this.colName(column)} 重复`);
        seen.add(column);keys.push({column,mode:row.querySelector('.sort-mode').value,descending:row.querySelector('.sort-direction').value==='desc'});
      }
      const header=Number(document.getElementById('sortHeaderRow').value);
      if(!Number.isInteger(header)||header<0||header>=this.s.usedMaxRow)throw Error('表头行必须小于数据区域最后一行，可填0表示无表头');
      return {keys,header};
    }
    async apply(){
      let args;
      try{args=this.values();}catch(err){this.toast(err.message,'warning');return}
      document.getElementById('multiSortDialog').close();
      this.s.sortKeysBySheet.set(this.s.sheet,args.keys);
      this.s.sortHeaderBySheet.set(this.s.sheet,args.header);
      await this.submit(args.keys,args.header);
    }
    async reset(){
      document.getElementById('multiSortDialog').close();
      this.s.sortKeysBySheet.delete(this.s.sheet);
      this.s.sortHeaderBySheet.delete(this.s.sheet);
      await this.clear();
    }
  }
  window.ExcelLensMultiSort=MultiSort;
})();
