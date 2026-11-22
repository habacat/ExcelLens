// Tabbed command bar. Commands keep their original controls and handlers;
// this module only arranges them: tabs, folding and overflow. No source reads.
(() => {
  'use strict';
  const TAB_KEY='ExcelLens.ribbonTab',COLLAPSE_KEY='ExcelLens.ribbonCollapsed';
  const read=key=>{try{return localStorage.getItem(key);}catch{return null;}};
  const write=(key,value)=>{try{localStorage.setItem(key,value);}catch{}};
  class AdaptiveToolbar {
    static choose(items,width,gap){
      const shown=new Set(),ordered=items.map((item,index)=>({...item,index})).sort((a,b)=>a.priority-b.priority||a.index-b.index);
      let used=0;
      for(const item of ordered){const next=used+(shown.size?gap:0)+item.width;if(item.priority===0||next<=width){shown.add(item.node);used=next;}}
      return shown;
    }
    constructor(root){
      this.root=root;this.actions=root.querySelector('.app-actions');this.frame=0;
      this.tabs=[...root.querySelectorAll('.ribbon-tabs [data-ribbon-tab]')];this.panels=[...root.querySelectorAll('[data-ribbon-panel]')];
      this.more=root.querySelector('#ribbonMoreBtn');this.moreMenu=document.getElementById('ribbonMoreMenu');this.collapse=root.querySelector('#ribbonCollapseBtn');
      this.schedule=()=>{if(!this.frame)this.frame=requestAnimationFrame(()=>{this.frame=0;this.fit();});};
      this.resize=new ResizeObserver(entries=>{const width=entries[0]?.contentRect.width,height=root.getBoundingClientRect().height;if(height!==this.height){this.height=height;document.documentElement.style.setProperty('--appbar-height',height+'px');}if(width!==this.width){this.width=width;this.schedule();}});
      this.resize.observe(root);
      // The overflow button and separators are layout outputs. Observing their
      // own visibility changes queues another layout on every animation frame.
      this.changes = new MutationObserver(records => {
        if (records.some(record => record.attributeName !== 'hidden' ||
            (record.target !== this.more && !record.target.classList.contains('ribbon-sep')))) this.schedule();
      });
      this.changes.observe(this.actions,{subtree:true,childList:true,characterData:true,attributes:true,attributeFilter:['hidden','disabled']});
      this.visibility=new MutationObserver(this.schedule);this.visibility.observe(document.body,{attributes:true,attributeFilter:['class']});
      window.addEventListener('resize',this.schedule);document.fonts?.ready.then(this.schedule);
      for(const tab of this.tabs){
        tab.addEventListener('click',()=>this.activate(tab.dataset.ribbonTab));
        tab.addEventListener('dblclick',()=>this.setCollapsed(!this.collapsed));
        tab.addEventListener('keydown',event=>this.tabKey(event,tab));
      }
      this.collapse?.addEventListener('click',()=>this.setCollapsed(!this.collapsed));
      this.more?.setAttribute('data-menu','ribbonMoreMenu');
      this.moreMenu?.addEventListener('menuopen',()=>this.fillMore());
      // A folded bar opens over the sheet for one command, then steps aside.
      this.actions.addEventListener('click',event=>{if(this.peeking&&event.target.closest('button,a')&&!event.target.closest('[data-menu],#ribbonCollapseBtn,[aria-haspopup="menu"]'))setTimeout(()=>this.peek(false),0);});
      document.addEventListener('pointerdown',event=>{if(this.peeking&&!root.contains(event.target)&&!event.target.closest?.('.menu,.columns-menu'))this.peek(false);},true);
      document.addEventListener('keydown',event=>{
        if(event.key==='F1'&&event.ctrlKey&&!event.altKey&&!event.shiftKey&&this.tabs[0]?.getClientRects().length&&!document.querySelector('dialog:modal')){event.preventDefault();this.setCollapsed(!this.collapsed);}
        else if(event.key==='Escape'&&this.peeking)this.peek(false);
      });
      this.setCollapsed(read(COLLAPSE_KEY)==='1',false);
      this.select(this.tabs.some(tab=>tab.dataset.ribbonTab===read(TAB_KEY))?read(TAB_KEY):this.tabs[0]?.dataset.ribbonTab,false);
    }
    get collapsed(){return this.root.hasAttribute('data-ribbon-collapsed');}
    get peeking(){return this.root.hasAttribute('data-ribbon-peek');}
    activate(name){
      if(this.collapsed){const same=this.current===name&&this.peeking;this.select(name);this.peek(!same);}
      else this.select(name);
    }
    select(name,persist=true){
      if(!name)return;
      this.current=name;
      for(const tab of this.tabs){const on=tab.dataset.ribbonTab===name;tab.setAttribute('aria-selected',String(on));tab.tabIndex=on?0:-1;}
      for(const panel of this.panels)panel.hidden=panel.dataset.ribbonPanel!==name;
      if(persist)write(TAB_KEY,name);
      this.schedule();
    }
    peek(value){this.root.toggleAttribute('data-ribbon-peek',!!value&&this.collapsed);this.schedule();}
    setCollapsed(value,persist=true){
      this.root.toggleAttribute('data-ribbon-collapsed',!!value);this.root.removeAttribute('data-ribbon-peek');
      if(this.collapse){const label=value?'展开功能区':'折叠功能区';this.collapse.setAttribute('aria-pressed',String(!!value));this.collapse.setAttribute('aria-label',label);this.collapse.title=label+'（Ctrl+F1）';}
      if(persist)write(COLLAPSE_KEY,value?'1':'0');
      this.schedule();
    }
    tabKey(event,tab){
      const at=this.tabs.indexOf(tab),next={ArrowRight:at+1,ArrowLeft:at-1,Home:0,End:this.tabs.length-1}[event.key];
      if(next===undefined)return;
      event.preventDefault();
      const target=this.tabs[(next+this.tabs.length)%this.tabs.length];this.activate(target.dataset.ribbonTab);target.focus();
    }
    // Show the tab that owns a control before a command acts on it, so menus
    // anchor to a visible button and the user learns where the command lives.
    reveal(id){
      const node=document.getElementById(id),panel=node?.closest?.('[data-ribbon-panel]');
      if(panel&&panel.dataset.ribbonPanel!==this.current)this.select(panel.dataset.ribbonPanel);
      if(panel){
        if(this.collapsed&&node.matches('[aria-haspopup="menu"]'))this.peek(true);
        this.fit();
      }
      return node;
    }
    fit(){
      const actions=this.actions,panel=this.panels.find(node=>!node.hidden);
      if(!panel||!actions.clientWidth)return;
      const set=(node,value)=>{if(node&&node.hidden!==value)node.hidden=value;};
      const nodes=[...panel.querySelectorAll(':scope > [data-toolbar-priority]')],separators=[...panel.querySelectorAll(':scope > .ribbon-sep')];
      for(const node of nodes)node.removeAttribute('data-toolbar-overflow');
      for(const node of separators)set(node,false);
      const items=nodes.filter(node=>!node.hidden&&(!node.hasAttribute('data-toolbar-when-enabled')||!node.disabled)).map(node=>({node,width:node.getBoundingClientRect().width,priority:Number(node.dataset.toolbarPriority)||0})).filter(item=>item.width>0);
      const style=getComputedStyle(actions),gap=parseFloat(getComputedStyle(panel).columnGap)||0;
      const space=actions.clientWidth-(parseFloat(style.paddingLeft)||0)-(parseFloat(style.paddingRight)||0)-(this.collapse?.getBoundingClientRect().width||0)-gap;
      if(panel.scrollWidth>space+.5){
        set(this.more,false);
        const fixed=separators.reduce((sum,node)=>{const box=getComputedStyle(node);return sum+node.getBoundingClientRect().width+(parseFloat(box.marginLeft)||0)+(parseFloat(box.marginRight)||0)+gap;},0);
        const shown=AdaptiveToolbar.choose(items,Math.max(0,space-this.more.getBoundingClientRect().width-gap-fixed)-1,gap);
        const focused=document.activeElement;
        for(const item of items)if(!shown.has(item.node))item.node.setAttribute('data-toolbar-overflow','');
        if(items.some(item=>item.node.contains(focused)&&!shown.has(item.node)))this.more.focus({preventScroll:true});
        if(!items.some(item=>!shown.has(item.node)))set(this.more,true);
      }else set(this.more,true);
      // A separator needs a visible command on both sides.
      let pending=null,seen=false;
      for(const node of panel.children){
        if(node.classList.contains('ribbon-sep')){if(!seen||pending)set(node,true);else pending=node;}
        else if(node.getClientRects().length){seen=true;pending=null;}
      }
      if(pending)set(pending,true);
    }
    fillMore(){
      const panel=this.panels.find(node=>!node.hidden),fragment=document.createDocumentFragment();
      for(const node of panel?.querySelectorAll(':scope > [data-toolbar-overflow]')||[]){
        const select = node.querySelector('select');
        if (select) {
          // Keep one source of options and use its normal change handler.
          const label = document.createElement('label');
          label.className = 'menu-field';
          const picker = select.cloneNode(true);
          picker.removeAttribute('id');
          picker.value = select.value;
          picker.disabled = select.disabled;
          picker.addEventListener('change', () => {
            select.value = picker.value;
            select.dispatchEvent(new Event('change', {bubbles:true}));
            window.ExcelLensOverlays.menus.close();
            if (this.peeking) this.peek(false);
          });
          label.append(select.getAttribute('aria-label'), picker);
          fragment.append(label);
          continue;
        }
        const item=document.createElement('button'),icon=node.querySelector('.icon:not(.caret)');
        item.type='button';item.setAttribute('role','menuitem');item.disabled=node.disabled;
        if(icon)item.append(icon.cloneNode(true));
        item.append(node.textContent.trim());
        item.addEventListener('click',()=>node.click());
        fragment.append(item);
      }
      this.moreMenu.replaceChildren(fragment);
    }
    dispose(){cancelAnimationFrame(this.frame);this.resize.disconnect();this.changes.disconnect();this.visibility.disconnect();window.removeEventListener('resize',this.schedule);}
  }
  window.ExcelLensAdaptiveToolbar=AdaptiveToolbar;
  const root=document.querySelector('.appbar');if(root)window.ExcelLensRibbon=new AdaptiveToolbar(root);
})();
