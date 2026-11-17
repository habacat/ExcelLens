/* Page-wide reading presets. CSS repaints existing nodes, including cached
 * sheets; source values, styles, coordinates and exports remain untouched. */
(() => {
  'use strict';
  const presets=Object.freeze({off:{name:'原色',color:''},warm:{name:'暖纸',color:'#f4eedf'},green:{name:'浅绿',color:'#e5f0e4'},mist:{name:'柔灰',color:'#e9edf2'},blush:{name:'杏粉',color:'#f4e6e3'},ice:{name:'冰蓝',color:'#e3eef7'},lavender:{name:'淡紫',color:'#eee8f4'},graphite:{name:'石墨',dark:true,color:''},navy:{name:'深蓝',dark:true,color:''},forest:{name:'墨绿',dark:true,color:''},warmdark:{name:'暖灰',dark:true,color:''},plum:{name:'紫夜',dark:true,color:''},brown:{name:'深棕',dark:true,color:''}});
  const key='ExcelLens.readingPreset';
  const textureKey='ExcelLens.gridTexture';
  class ReadingMode {
    constructor({button,panel,veil,textureToggle,onTextureChange}){
      this.button=button;this.panel=panel;this.veil=veil;this.root=document.documentElement;
      this.textureToggle=textureToggle;this.onTextureChange=onTextureChange;
      let saved='off',texture=false;
      try{saved=localStorage.getItem(key)||'off';}catch{}
      try{texture=localStorage.getItem(textureKey)==='on';}catch{}
      this.apply(saved,false);
      this.applyTexture(texture,false);
      button.addEventListener('click',()=>panel.hidden?this.open():this.close());
      panel.addEventListener('change',event=>{
        if(event.target.name==='readingPreset')this.apply(event.target.value);
        else if(event.target===this.textureToggle)this.applyTexture(event.target.checked);
      });
      panel.querySelector('[data-reading-close]').addEventListener('click',()=>this.close());
      panel.addEventListener('keydown',event=>{if(event.key==='Escape'){event.preventDefault();event.stopPropagation();this.close();}});
      document.addEventListener('pointerdown',event=>{if(!panel.hidden&&!panel.contains(event.target)&&!button.contains(event.target))this.close(false);});
    }
    apply(value,persist=true){
      const previous=this.value;
      this.value=Object.hasOwn(presets,value)?value:'off';const preset=presets[this.value];
      if(this.value==='off'){
        this.root?.removeAttribute('data-reading-preset');this.root?.removeAttribute('data-reading-tone');
      }else{
        this.root?.setAttribute('data-reading-preset',this.value);this.root?.setAttribute('data-reading-tone',preset.dark?'dark':'light');
      }
      this.veil.hidden=this.value==='off'||Boolean(preset.dark);this.veil.style.backgroundColor=preset.color;
      this.button.classList.toggle('is-active',this.value!=='off');
      this.button.title='护眼模式：'+preset.name;this.button.setAttribute('aria-label',this.button.title);
      for(const input of this.panel.querySelectorAll('input[name="readingPreset"]'))input.checked=input.value===this.value;
      if(persist)try{localStorage.setItem(key,this.value);}catch{}
      if(previous!==this.value)document.dispatchEvent(new Event('excellens:readingchange'));
    }
    applyTexture(enabled,persist=true){
      const previous=this.textureEnabled;
      this.textureEnabled=enabled===true;
      if(this.textureToggle)this.textureToggle.checked=this.textureEnabled;
      if(persist)try{localStorage.setItem(textureKey,this.textureEnabled?'on':'off');}catch{}
      // Initial restoration precedes the first grid render. Later changes only
      // update the existing viewport decoration, without repainting cell data.
      if(previous!==undefined&&previous!==this.textureEnabled)this.onTextureChange?.(this.textureEnabled);
    }
    open(){this.panel.hidden=false;this.button.setAttribute('aria-expanded','true');this.panel.querySelector('input[name="readingPreset"]:checked')?.focus();}
    close(focus=true){if(this.panel.hidden)return;this.panel.hidden=true;this.button.setAttribute('aria-expanded','false');if(focus)this.button.focus({preventScroll:true});}
  }
  window.ExcelLensReadingMode=ReadingMode;
})();
