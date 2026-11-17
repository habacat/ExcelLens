/* ExcelLens audit bookmarks: stored outside disposable caches. No changes are
 * made to the original workbook. */
(() => {
  'use strict';
  const $=id=>document.getElementById(id);
  const quote=value=>'"'+String(value??'').replaceAll('"','""')+'"';
  class Bookmarks {
    constructor({state,api,toast,openDialog,navigateToCell,onChange}){
      this.s=state;this.api=api;this.toast=toast;this.openDialog=openDialog;this.navigate=navigateToCell;this.onChange=onChange;
      this.items=[];this.keys=new Set();this.loadedBook='';this.request=0;this.pending=null;this.saving=false;this.targetBook='';
      $('bookmarkOpenBtn').addEventListener('click',()=>this.open());
      $('bookmarkSaveBtn').addEventListener('click',()=>this.save());
      $('bookmarkExportBtn').addEventListener('click',()=>this.exportCSV());
      $('bookmarkSearch').addEventListener('input',()=>this.render());
      $('bookmarkRefreshBtn').addEventListener('click',()=>this.load(true));
    }
    resetBook(){
      ++this.request;this.pending?.abort();this.pending=null;this.saving=false;
      this.loadedBook='';this.targetBook='';this.items=[];this.keys.clear();
      $('bookmarkSaveBtn').disabled=false;
      if($('bookmarkDialog').open)$('bookmarkDialog').close();
    }
    accept(data,book){
      if(book!==this.s.workbookKey)return false;
      this.items=Array.isArray(data.items)?data.items:[];this.loadedBook=book;
      this.keys=new Set(this.items.map(x=>`${x.sheet}!${x.ref}`));
      $('bookmarkOpenBtn').title=`审计标记（${this.items.length}） · 不写入源文件`;
      this.onChange();this.render();return true;
    }
    async load(force=false) {
      if(!this.s.opened||this.saving)return false;
      const book=this.s.workbookKey;
      if(!force && this.loadedBook===book)return true;
      this.pending?.abort();const ctrl=new AbortController();this.pending=ctrl;const ticket=++this.request;
      try {
        const response=await this.api('/api/bookmarks',{signal:ctrl.signal});
        if(ctrl.signal.aborted||ticket!==this.request)return false;
        return this.accept(response,book);
      }catch(e){if(!ctrl.signal.aborted&&ticket===this.request&&book===this.s.workbookKey)this.toast(`读取审计标记失败：${e.message}`,'error');return false;}
      finally{if(this.pending===ctrl)this.pending=null;}
    }
    has(sheet,ref){return this.loadedBook===this.s.workbookKey&&this.keys.has(`${sheet}!${ref}`)}
    open() {
      if(!this.s.sheet||!this.s.selected){this.toast('请先选中单元格','warning');return;}
      this.prepareCurrent();this.render();this.openDialog('bookmarkDialog');
      const book=this.s.workbookKey,target=`${this.s.sheet}!${this.s.selected.ref}`,note=$('bookmarkNote').value,tag=$('bookmarkTag').value;
      this.load(true).then(accepted=>{
        if(accepted&&book===this.s.workbookKey&&target===`${this.s.sheet}!${this.s.selected?.ref}`){
          this.refreshCurrent(note,tag);
        }
      });
    }
    prepareCurrent() {
      const cell=this.s.selected;if(!cell)return;
      this.targetBook=this.s.workbookKey;
      $('bookmarkTarget').textContent=`${this.s.sheet}!${cell.ref} · ${String(cell.display??'').slice(0,90)}`;
      const existing=this.items.find(x=>x.sheet===this.s.sheet&&x.ref===cell.ref);
      $('bookmarkNote').dataset.id=existing?.id||'';
      $('bookmarkNote').value=existing?.note||'';
      $('bookmarkTag').value=existing?.tag||'待复核';
      $('bookmarkSaveBtn').textContent=existing?'更新标记':'添加标记';
    }
    refreshCurrent(note,tag){
      const draft=$('bookmarkNote').value,category=$('bookmarkTag').value,edited=draft!==note||category!==tag;
      this.prepareCurrent();
      if(edited){$('bookmarkNote').value=draft;$('bookmarkTag').value=category;}
      return edited;
    }
    async save() {
      if(this.saving)return;
      if(this.targetBook!==this.s.workbookKey){this.toast('工作簿已切换，请重新打开标记','warning');return;}
      const cell=this.s.selected;if(!cell)return;
      const note=$('bookmarkNote').value.trim();
      if([...note].length>800){this.toast('标记说明不能超过800字','warning');return;}
      const request={action:'save',id:$('bookmarkNote').dataset.id||'',sheet:this.s.sheet,ref:cell.ref,tag:$('bookmarkTag').value,note,value:String(cell.display??'').slice(0,1500),formula:String(cell.formula??'').slice(0,6000)};
      const book=this.s.workbookKey,ticket=++this.request;this.pending?.abort();this.saving=true;$('bookmarkSaveBtn').disabled=true;
      try{const data=await this.api('/api/bookmarks',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(request)});if(ticket!==this.request||!this.accept(data,book))return;const draftPending=request.sheet===this.s.sheet&&request.ref===this.s.selected?.ref&&this.refreshCurrent(note,request.tag);this.toast(draftPending?'已保存先前备注；当前修改尚未保存':'已保存本机审计标记',draftPending?'warning':'success');}
      catch(err){if(ticket===this.request&&book===this.s.workbookKey)this.toast(err.message,'error')}
      finally{if(ticket===this.request){this.saving=false;$('bookmarkSaveBtn').disabled=false;}}
    }
    async remove(bookmark){
      if(this.saving||this.loadedBook!==this.s.workbookKey)return;
      if(!confirm(`删除 ${bookmark.sheet}!${bookmark.ref} 的标记？原始 Excel 不受影响。`))return;
      const note=$('bookmarkNote').value,tag=$('bookmarkTag').value;
      const book=this.s.workbookKey,ticket=++this.request;this.pending?.abort();this.saving=true;$('bookmarkSaveBtn').disabled=true;
      try{const data=await this.api('/api/bookmarks',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({action:'delete',id:bookmark.id})});if(ticket!==this.request||!this.accept(data,book))return;this.refreshCurrent(note,tag);}
      catch(e){if(ticket===this.request&&book===this.s.workbookKey)this.toast(e.message,'error')}
      finally{if(ticket===this.request){this.saving=false;$('bookmarkSaveBtn').disabled=false;}}
    }
    render(){
      const q=$('bookmarkSearch').value.trim().toLocaleLowerCase();
      const list=$('bookmarkList');const frag=document.createDocumentFragment();
      let count=0;
      for(const b of this.items){
        if(q&&!`${b.sheet} ${b.ref} ${b.tag} ${b.note} ${b.value}`.toLocaleLowerCase().includes(q))continue;
        const row=document.createElement('div');row.className='bookmark-item';
        const main=document.createElement('button');main.type='button';main.className='bookmark-visit';
        const head=document.createElement('span');head.className='bookmark-head';head.textContent=`${b.sheet}!${b.ref}`;
        const tag=document.createElement('span');tag.className='bookmark-tag';tag.textContent=b.tag;
        const desc=document.createElement('span');desc.className='bookmark-desc';desc.textContent=b.note||b.value||'（无说明）';
        main.append(head,tag,desc);
        main.addEventListener('click',async()=>{$('bookmarkDialog').close();try{await this.navigate(b.sheet,b.ref)}catch(e){this.toast(e.message,'error')}});
        const del=document.createElement('button');del.type='button';del.className='bookmark-remove';del.textContent='×';del.title='删除此标记';del.addEventListener('click',()=>this.remove(b));
        row.append(main,del);frag.append(row);count++;
      }
      if(!count){const empty=document.createElement('div');empty.className='dialog-summary';empty.textContent='暂无匹配的标记';frag.append(empty)}
      list.replaceChildren(frag);
      $('bookmarkCount').textContent=`已保存 ${this.items.length} 个标记 · 数据仅在本机`;
    }
    exportCSV(){
      const headers=['工作表','单元格','分类','备注','保存值快照','公式快照','创建时间','更新时间'];
      const rows=this.items.map(b=>[b.sheet,b.ref,b.tag,b.note,b.value,b.formula,b.created,b.updated]);
      const content='\ufeff'+[headers,...rows].map(row=>row.map(value=>{let s=String(value??'');if(/^[\s]*[=+\-@]/.test(s))s="'"+s;return quote(s)}).join(',')).join('\r\n');
      const url=URL.createObjectURL(new Blob([content],{type:'text/csv;charset=utf-8'}));
      const link=document.createElement('a');link.href=url;link.download='ExcelLens_审计标记.csv';link.click();setTimeout(()=>URL.revokeObjectURL(url),30000);
      this.toast('标记清单已导出','success');
    }
  }
  window.ExcelLensBookmarks=Bookmarks;
})();
