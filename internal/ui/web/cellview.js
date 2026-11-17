/* One presentation contract for every source format and both grid painters. */
(() => {
  'use strict';
  const value=(cell,formulas=false,styles=true)=>window.ExcelLensConditional?.value(cell,formulas,styles)??(cell?(formulas&&cell.hasFormula&&cell.formula?cell.formula:String(cell.display??cell.raw??'')):'');
  const occupied=cell=>!!cell&&(!!cell.hasFormula||String(cell.raw??'')!==''||String(cell.display??'')!=='');
  const textTypes=new Set(['s','str','inlineStr','text','string']);
  const numeric=/^[+-]?(?:\d+\.?\d*|\.\d+)(?:[eE][+-]?\d+)?$/;
  const selectionFills=new Map();
  const rgb=(value,fallback)=>{
    const hex=/^#([\da-f]{6})$/i.exec(value||'');
    return hex?[0,2,4].map(i=>parseInt(hex[1].slice(i,i+2),16)):fallback;
  };
  const luminance=color=>color.reduce((sum,c,i)=>{const v=c/255;return sum+[.2126,.7152,.0722][i]*(v<=.04045?v/12.92:((v+.055)/1.055)**2.4);},0);
  const contrast=(a,b)=>(Math.max(a,b)+.05)/(Math.min(a,b)+.05);
  function selectionFill(background,foreground,palette){
    const key=(background||'')+'|'+(foreground||'')+'|'+(palette?.deep||'')+'|'+(palette?.light||'');
    if(selectionFills.has(key))return selectionFills.get(key);
    const base=rgb(background,[255,255,255]),text=rgb(foreground,[48,67,85]),light=luminance(text);
    const required=Math.min(4.5,contrast(light,luminance(base))*.9);
    const candidates=[rgb(palette?.deep,[16,52,105]),rgb(palette?.light,[215,234,255])].map(color=>{
      let alpha=.34,painted,readable;
      do{
        painted=color.map((c,i)=>base[i]*(1-alpha)+c*alpha);
        readable=contrast(light,luminance(painted))>=required;
        if(readable||alpha===0)break;
        alpha=Math.max(0,Number((alpha-.04).toFixed(2)));
      }while(true);
      // A same-hue source fill can need a stronger tint. Increase only while
      // the original text stays readable; never add internal grid borders.
      while(palette&&painted.reduce((sum,c,i)=>sum+Math.abs(c-base[i]),0)/3<22&&alpha<.58){
        const next=Math.min(.58,Number((alpha+.04).toFixed(2))),nextPaint=color.map((c,i)=>base[i]*(1-next)+c*next);
        if(contrast(light,luminance(nextPaint))<required)break;
        alpha=next;painted=nextPaint;
      }
      return {color,alpha,readable,distance:painted.reduce((sum,c,i)=>sum+(c-base[i])**2,0)};
    }).sort((a,b)=>Number(b.readable)-Number(a.readable)||b.distance-a.distance);
    const result=`rgba(${candidates[0].color.join(',')},${candidates[0].alpha})`;
    if(selectionFills.size>=256)selectionFills.delete(selectionFills.keys().next().value);
    selectionFills.set(key,result);return result;
  }
  function isText(cell){
    if(!cell||cell.cacheMissing)return false;
    if(textTypes.has(cell.type))return true;
    if(cell.type)return false;
    const raw=String(cell.raw??cell.display??'');
    return raw!==''&&!numeric.test(raw.trim());
  }
  class Presenter{
    constructor(options){this.o=options;this.measure=null;this.widths=new Map();this.richWidths=new WeakMap();this.numberWidths=new WeakMap();this.numberMeasures=new Map();this.richPlan=null;}
    prepare(rows){
      this.richPlan=new WeakMap();let remaining=this.o.richLimits?.viewRuns||0;
      for(const row of rows||[])for(const cell of row.cells||[]){
        if((!cell.richText&&!window.ExcelLensConditional?.needsInline(cell,this.style(cell,row.style)))||this.richPlan.has(cell))continue;
        const count=cell.richText?(cell.richText.runs?.length||0)+1:2,allowed=count<=remaining;
        this.richPlan.set(cell,allowed);if(allowed)remaining-=count;
      }
    }
    allowed(cell){return (!cell?.richText&&!window.ExcelLensConditional?.needsInline(cell,this.style(cell)))||!this.richPlan||this.richPlan.get(cell)===true;}
    paintKey(){const s=this.o.state;return [s.showStyles,s.showFormulas,s.fontOverride,this.o.scale()].join('|');}
    needsPaint(node,cell,row){return node._xlPaintKey!==this.paintKey()||node._xlRichAllowed!==this.allowed(cell)||node._xlRowPresentationNotice!==(row?.presentationNotice||'');}
    value(cell){return value(cell,this.o.state.showFormulas,this.o.state.showStyles);}
    style(cell,rowStyle=0){const s=this.o.state;return window.ExcelLensConditional?.visual(cell,s.styles,rowStyle,s.showStyles)??(s.showStyles?s.styles?.[Number(cell?.style??rowStyle)||0]:null);}
    paint(node,cell,row){
      if(node._xlNumericOriginal)node.title=node._xlNumericOriginalTitle||'';
      node._xlNumericOriginal=node._xlNumericSpan=null;
      const s=this.o.state;node._xlModel=cell||null;node._xlRowStyle=row?.style||0;
      node._xlPaintKey=this.paintKey();node._xlRichAllowed=this.allowed(cell);
      node._xlRowPresentationNotice=row?.presentationNotice||'';
      if(node._xlRichNotice){node.title='';node._xlRichNotice='';}
      if(node._xlPresentationNotice){node.title='';node._xlPresentationNotice='';}
      node._xlRichNode=null;node.classList.remove('rich-text-cell','rich-text-notice');
      if(node._xlOverflowSpan){node._xlOverflowSpan=null;node.classList.remove('cell-overflow');node.style.removeProperty('--overflow-width');}
      for(const name of ['formula-cell','hyperlink-cell','formula-view-cell','auto-filter-header'])node.classList.remove(name);
      node.textContent=this.value(cell);
      if(cell){const styled=cell.style==null&&row?.style?{...cell,style:row.style}:cell;this.o.applyStyle(node,styled);
        if(cell.hasFormula)node.classList.add('formula-cell');
        if(cell.hyperlink)node.classList.add('hyperlink-cell');
        if(s.showFormulas&&cell.hasFormula)node.classList.add('formula-view-cell');
      }else if(row?.style)this.o.applyStyle(node,{style:row.style});
      if(s.showStyles&&!(s.showFormulas&&cell?.hasFormula))window.ExcelLensRichText?.paint(node,cell,this.style(cell,row?.style)||{},
        {limits:this.o.richLimits,allowed:this.allowed(cell),fontOverride:s.fontOverride,fontStack:this.o.fontStack,scale:this.o.scale()});
      window.ExcelLensConditional?.notice(node,cell,row,s.showStyles,this.style(cell,row?.style));
      const r=Number(node.dataset.row),c=Number(node.dataset.col),filter=s.autoFilter;
      if(filter&&r===filter.row&&c>=filter.startCol&&c<=filter.endCol){
        node.classList.add('auto-filter-header');const button=document.createElement('button');button.type='button';
        button.className='auto-filter-button'+(filter.rules.has(c)?' is-filtered':'');button.dataset.column=String(c);
        button.textContent='▾';button.title=this.o.colName(c)+' 列：筛选菜单';button.setAttribute('aria-label',button.title);button.disabled=!s.indexReady;node.appendChild(button);
      }
    }
    spillWidth(cell,values,layouts,position,rowStyle=0,row=0){
      const s=this.o.state;if(!isText(cell)||s.showFormulas)return 0;
      const text=value(cell,false,s.showStyles);if(!text||/[\r\n]/.test(text))return 0;
      const style=this.style(cell,rowStyle);
      if(style?.wrap||['center','right','justify','distributed','centerContinuous','fill'].includes(style?.horizontal))return 0;
      const current=layouts[position];if(!current||!(current.width>0))return 0;
      if(!this.measure)this.measure=document.createElement('canvas').getContext('2d');if(!this.measure)return 0;
      const raised=['superscript','subscript'].includes(cell?.conditional?.style?.font?.vertical||style?.fontVertical);
      const font=(style?.italic?'italic ':'')+(style?.bold?'700 ':'')+((style?.fontSize||8.25)*(raised ? .65 : 1)*4/3*this.o.scale())+'px '+this.o.fontStack(s.fontOverride||style?.fontName||'Segoe UI');
      // Very long imported text still spills. Only bounded text is measured;
      // the source value and DOM text remain untouched.
      const prefix=text.slice(0,2048),key=font+'\0'+prefix+'\0'+text.length;
      let needed=this.widths.get(key);
      const rich=s.showStyles&&this.allowed(cell)?window.ExcelLensRichText?.inspect(cell,this.o.richLimits):null;
      if(rich?.runs.length){
        // Width depends on every run font. Do not reuse the plain-text cache
        // for cells which happen to have the same text and base font.
        const overlay=window.ExcelLensConditional?.font(cell)||{},fontKey=font+'\0'+JSON.stringify(overlay),cached=this.richWidths.get(cell.richText);
        if(cached?.font===fontKey&&cached.text===text)needed=cached.width;
        else{
          needed=text.length>2048?Infinity:14;
          if(Number.isFinite(needed))window.ExcelLensRichText.eachPart(rich,(part,run)=>{
            run=window.ExcelLensRichText.overlayFont(run,overlay);
            const raised=['superscript','subscript'].includes(run.vertical||style?.fontVertical),size=(run.size||style?.fontSize||8.25)*(raised ? .65 : 1);
            this.measure.font=((run.italic??style?.italic)?'italic ':'')+((run.bold??style?.bold)?'700 ':'')+(size*4/3*this.o.scale())+'px '+this.o.fontStack(s.fontOverride||run.name||style?.fontName||'Segoe UI');
            needed+=this.measure.measureText(part).width;
          });
          this.richWidths.set(cell.richText,{font:fontKey,text,width:needed});
        }
      }else if(needed===undefined){this.measure.font=font;needed=text.length>2048?Infinity:this.measure.measureText(prefix).width+14;this.widths.set(key,needed);if(this.widths.size>2048)this.widths.delete(this.widths.keys().next().value);}
      if(needed<=current.width-2)return 0;
      let available=current.width;
      for(let i=position+1;i<layouts.length;i++){if(occupied(values?.[layouts[i].col])||(row&&this.o.merge?.(row,layouts[i].col)))break;available+=layouts[i].width;if(available>=needed)break;}
      return available>current.width+1?Math.max(current.width,Math.min(available-6,needed)):0;
    }
    fitNumber(node,cell,width,padding){
      const restore=()=>{if(node._xlNumericSpan){node._xlNumericSpan.replaceWith(node._xlNumericOriginal);node.title=node._xlNumericOriginalTitle||'';node._xlNumericOriginal=node._xlNumericSpan=null;}return false;};
      const s=this.o.state;
      if(!cell||cell.cacheMissing||s.showFormulas&&cell.hasFormula||!((!cell.type||cell.type==='n')&&numeric.test(String(cell.raw??'').trim())||cell.type==='d'))return restore();
      const text=this.value(cell),style=this.style(cell,node._xlRowStyle)||{};
      if(!text||style.wrap)return restore();
      if(!this.measure){const canvas=typeof document!=='undefined'?document.createElement('canvas'):null;this.measure=canvas?.getContext?.('2d');}if(!this.measure)return restore();
      const raised=['superscript','subscript'].includes(cell.conditional?.style?.font?.vertical||style.fontVertical),font=(style.italic?'italic ':'')+(style.bold?'700 ':'')+((style.fontSize||8.25)*(raised ? .65 : 1)*4/3*this.o.scale())+'px '+this.o.fontStack(s.fontOverride||style.fontName||'Segoe UI');
      let measured=this.numberWidths.get(cell);if(!measured||measured.text!==text||measured.font!==font){const key=text.length<=256?font+'\0'+text:null;measured=key&&this.numberMeasures.get(key);if(!measured){this.measure.font=font;measured={text,font,width:this.measure.measureText(text).width,hash:this.measure.measureText('#').width};if(key){if(this.numberMeasures.size>=512)this.numberMeasures.delete(this.numberMeasures.keys().next().value);this.numberMeasures.set(key,measured);}}this.numberWidths.set(cell,measured);}
      padding=padding??(this.o.numericPadding?this.o.numericPadding(node):node.classList.contains('auto-filter-header')?32:14);const available=Math.max(0,width-padding);
      if(!(width>0)||!Number.isFinite(measured.width)||measured.width<=available+.25)return restore();
      if(!node._xlNumericSpan){const original=node._xlRichNode||[...node.childNodes].find(n=>n.nodeType===3);if(!original)return false;const span=document.createElement('span');span.className='cell-number-overflow';node._xlNumericOriginal=original;node._xlNumericOriginalTitle=node.title||'';node._xlNumericSpan=span;original.replaceWith(span);}
      node._xlNumericSpan.textContent='#'.repeat(Math.min(128,Math.max(1,Math.floor(available/Math.max(1,measured.hash)))));node.title=[node._xlNumericOriginalTitle,'宽度不足，完整显示值：'+text].filter(Boolean).join('；');return true;
    }
    overflow(node,cell,values,layouts,position){
      if(this.fitNumber(node,cell,parseFloat(node.style.width)||layouts[position]?.width||0))return;
      const merged=this.o.merge?.(Number(node.dataset.row),Number(node.dataset.col));
      const width=merged?0:this.spillWidth(cell,values,layouts,position,node._xlRowStyle,Number(node.dataset.row));
      const previous=node._xlOverflowSpan;
      if(!width){if(previous){previous.replaceWith(node._xlRichNode||document.createTextNode(this.value(cell)));node._xlOverflowSpan=null;node.classList.remove('cell-overflow');node.style.removeProperty('--overflow-width');}return;}
      if(!previous){const span=document.createElement('span');span.className='cell-overflow-content';const old=node._xlRichNode||[...node.childNodes].find(n=>n.nodeType===3);
        if(old){old.replaceWith(span);span.appendChild(old);}else{span.textContent=this.value(cell);node.prepend(span);}node._xlOverflowSpan=span;}
      node.classList.add('cell-overflow');const css=width+'px';if(node.style.getPropertyValue('--overflow-width')!==css)node.style.setProperty('--overflow-width',css);
    }
  }
  const types=new Map([['s','文本'],['str','文本'],['inlineStr','文本'],['text','文本'],['string','文本'],['b','布尔'],['e','错误'],['d','日期'],['n','数值']]);
  const sourceType=cell=>cell.cacheMissing?'公式无保存结果':cell.presentationOnly?'源为空白':cell.raw===''&&!cell.hasFormula?'空白':types.get(cell.type)||(cell.type?'源类型 '+cell.type:'数值');
  const formula=cell=>cell.hasFormula&&typeof cell.formula==='string'&&cell.formula.startsWith('=')&&cell.formula.length>1?cell.formula:null;
  window.ExcelLensCellView={value,occupied,isText,Presenter,selectionFill,sourceType,formula};
})();
