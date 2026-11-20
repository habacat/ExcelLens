(() => {
  'use strict';
  class EditSheetTabs {
    constructor(editor) {
      this.editor=editor;
      const tabs=document.getElementById('sheetTabs'), menu=document.getElementById('editSheetMenu');
      document.getElementById('editSheetAdd').addEventListener('click',()=>this.apply('newSheet',editor.context().sheet));
      const show=(event,tab,x,y)=>{
        if(!tab || !editor.grid.active || editor.data.encoding || editor.grid.blocked)return;
        event.preventDefault();
        this.source=tab;
        menu.querySelector('[data-sheet-action="deleteSheet"]').disabled=editor.data.sheets.length<2;
        editor.grid.view.positionMenu(menu,x,y);
        menu.querySelector('button').focus();
      };
      tabs.addEventListener('contextmenu',event=>show(event,event.target.closest('[role="tab"]'),event.clientX,event.clientY));
      tabs.addEventListener('keydown',event=>{
        if(event.key!=='F10'||!event.shiftKey)return;
        const tab=event.target.closest('[role="tab"]');if(!tab)return;
        const box=tab.getBoundingClientRect();show(event,tab,box.left,box.top);
      });
      menu.addEventListener('focusout',e=>{if(!menu.contains(e.relatedTarget))menu.hidden=true;});
      menu.addEventListener('keydown',e=>{
        if(e.key==='Escape'){menu.hidden=true;this.source?.focus();}
        else if(['ArrowUp','ArrowDown'].includes(e.key)){
          const items=[...menu.querySelectorAll('button:enabled')],i=items.indexOf(document.activeElement);
          items[(i+items.length+(e.key==='ArrowDown'?1:-1))%items.length].focus();
        }else return;
        e.preventDefault();e.stopPropagation();
      });
      menu.addEventListener('click',async event=>{
        const action=event.target.closest('button')?.dataset.sheetAction,tab=this.source;
        if(!action||!tab)return;
        menu.hidden=true;const sheet=tab.textContent;
        if(action==='deleteSheet'||action==='renameSheet'){
          if(sheet!==editor.context().sheet)await editor.grid.view.switchSheet(sheet);
          if(sheet===editor.context().sheet)editor.advanced(action);
        }else this.apply(action,sheet);
      });
    }
    nextName(kind,sheet) {
      const names=new Set(this.editor.data.sheets.map(s=>s.name.toLowerCase()));
      for(let n=kind==='newSheet'?1:2;;n++){
        const suffix=kind==='newSheet'?String(n):' ('+n+')';
        const prefix=kind==='newSheet'?'Sheet':sheet.slice(0,31-suffix.length).replace(/[\uD800-\uDBFF]$/,'');
        if(!names.has((prefix+suffix).toLowerCase()))return prefix+suffix;
      }
    }
    apply(kind,sheet) {
      const g=this.editor.grid;
      if(!g.active||g.blocked||this.editor.data.encoding)return;
      g.act(()=>this.editor.commit('structure',undefined,{structure:{kind,sheet,name:this.nextName(kind,sheet)},direct:true}),true,'正在更新工作表');
    }
  }
  window.ExcelLensEditSheetTabs=EditSheetTabs;
})();
