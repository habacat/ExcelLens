/* Crosshair colors are display-only; changing them never reloads a sheet. */
(() => {
  'use strict';
  const colors=Object.freeze({blue:{name:'蓝色',color:'#3c7dc3'},green:{name:'绿色',color:'#109968'},amber:{name:'琥珀',color:'#b77a19'},purple:{name:'紫色',color:'#8654b6'},rose:{name:'玫红',color:'#b95179'}});
  const enabledKey='ExcelLens.crosshairEnabled',colorKey='ExcelLens.crosshairColor';
  class HighlightMode {
    constructor({toggle,button,panel,onChange}){
      this.toggle=toggle;this.button=button;this.panel=panel;this.root=document.documentElement;this.onChange=onChange;
      let enabled=true,color='green';try{enabled=localStorage.getItem(enabledKey)!=='false';color=localStorage.getItem(colorKey)||'green';}catch{}
      this.apply(enabled,color,false);
      toggle.addEventListener('change',()=>this.apply(toggle.checked,this.color));
      button.addEventListener('click',()=>{if(!panel.hidden)this.close();else{panel.hidden=false;button.setAttribute('aria-expanded','true');panel.querySelector('input:checked')?.focus();}});
      panel.addEventListener('change',event=>{if(event.target.name==='crosshairColor')this.apply(this.enabled,event.target.value);});
      panel.querySelector('[data-highlight-close]').addEventListener('click',()=>this.close());
      panel.addEventListener('keydown',event=>{if(event.key==='Escape'){event.preventDefault();event.stopPropagation();this.close();}});
      document.addEventListener('pointerdown',event=>{if(!panel.hidden&&!panel.contains(event.target)&&!button.contains(event.target))this.close(false);});
    }
    apply(enabled,color,persist=true){
      this.enabled=Boolean(enabled);this.color=Object.hasOwn(colors,color)?color:'green';
      const palette=colors[this.color];
      this.toggle.checked=this.enabled;this.root?.setAttribute('data-crosshair-enabled',String(this.enabled));
      this.root?.style.setProperty('--crosshair-color',colors[this.color].color);
      this.root?.style.setProperty('--selection-color',palette.color);
      this.root?.style.setProperty('--selection-active-fill',palette.color+'1f');
      this.button.title='行列与选区颜色：'+colors[this.color].name;this.button.setAttribute('aria-label',this.button.title);
      for(const input of this.panel.querySelectorAll('input[name="crosshairColor"]'))input.checked=input.value===this.color;
      if(persist)try{localStorage.setItem(enabledKey,String(this.enabled));localStorage.setItem(colorKey,this.color);}catch{}
      if(persist)this.onChange?.();
    }
    close(focus=true){if(this.panel.hidden)return;this.panel.hidden=true;this.button.setAttribute('aria-expanded','false');if(focus)this.button.focus({preventScroll:true});}
  }
  window.ExcelLensHighlightMode=HighlightMode;
})();
