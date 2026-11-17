// Adapt the existing controls to their actual container; no source reads.
(() => {
  'use strict';
  class AdaptiveToolbar {
    static choose(items,width,gap){
      const shown=new Set(),ordered=items.map((item,index)=>({...item,index})).sort((a,b)=>a.priority-b.priority||a.index-b.index);
      let used=0;
      for(const item of ordered){const next=used+(shown.size?gap:0)+item.width;if(item.priority===0||next<=width){shown.add(item.node);used=next;}}
      return shown;
    }
    constructor(root){
      this.root=root;this.actions=root.querySelector('.app-actions');this.brand=root.querySelector('.brand-area');this.frame=0;
      this.schedule=()=>{if(!this.frame)this.frame=requestAnimationFrame(()=>{this.frame=0;this.fit();});};
      this.resize=new ResizeObserver(entries=>{const width=entries[0]?.contentRect.width,height=root.getBoundingClientRect().height;if(height!==this.height){this.height=height;document.documentElement.style.setProperty('--appbar-height',height+'px');}if(width!==this.width){this.width=width;this.schedule();}});
      this.resize.observe(root);
      this.changes=new MutationObserver(this.schedule);this.changes.observe(this.actions,{subtree:true,childList:true,characterData:true,attributes:true,attributeFilter:['hidden','disabled']});
      this.visibility=new MutationObserver(this.schedule);this.visibility.observe(document.body,{attributes:true,attributeFilter:['class']});
      window.addEventListener('resize',this.schedule);document.fonts?.ready.then(this.schedule);this.schedule();
    }
    fit(){
      const root=this.root,actions=this.actions;if(!root.clientWidth)return;
      const nodes=[...actions.querySelectorAll(':scope > [data-toolbar-priority]')];
      for(const node of nodes)node.removeAttribute('data-toolbar-overflow');
      const items=nodes.filter(node=>!node.hidden&&(!node.hasAttribute('data-toolbar-when-enabled')||!node.disabled)).map(node=>({node,width:node.getBoundingClientRect().width,priority:Number(node.dataset.toolbarPriority)||0})).filter(item=>item.width>0);
      const style=getComputedStyle(root),gap=parseFloat(getComputedStyle(actions).columnGap)||0;
      const content=root.clientWidth-(parseFloat(style.paddingLeft)||0)-(parseFloat(style.paddingRight)||0);
      const brand=parseFloat(getComputedStyle(this.brand).minWidth)||0,between=parseFloat(style.columnGap)||0;
      const essential=items.filter(item=>item.priority===0),minimum=essential.reduce((sum,item)=>sum+item.width,0)+Math.max(0,essential.length-1)*gap;
      const rows=minimum>content-brand-between?2:1;root.dataset.toolbarRows=String(rows);
      const shown=AdaptiveToolbar.choose(items,Math.max(0,content-(rows===1?brand+between:0))-1,gap);
      const focused=document.activeElement;
      for(const item of items)if(!shown.has(item.node))item.node.setAttribute('data-toolbar-overflow','');
      if(items.some(item=>item.node===focused&&!shown.has(item.node)))root.querySelector('#commandOpenBtn')?.focus({preventScroll:true});
    }
    dispose(){cancelAnimationFrame(this.frame);this.resize.disconnect();this.changes.disconnect();this.visibility.disconnect();window.removeEventListener('resize',this.schedule);}
  }
  window.ExcelLensAdaptiveToolbar=AdaptiveToolbar;
  const root=document.querySelector('.appbar');if(root)new AdaptiveToolbar(root);
})();
