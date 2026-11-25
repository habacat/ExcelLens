/* One presentation contract for every source format and both grid painters. */
(() => {
  'use strict';
  const value=(cell,formulas=false,styles=true)=>window.ExcelLensConditional?.value(cell,formulas,styles)??(cell?(formulas&&cell.hasFormula&&cell.formula?cell.formula:String(cell.display??cell.raw??'')):'');
  const occupied=cell=>!!cell&&(!!cell.hasFormula||String(cell.raw??'')!==''||String(cell.display??'')!=='');
  const textTypes=new Set(['s','str','inlineStr','text','string']);
  const numeric=/^[+-]?(?:\d+\.?\d*|\.\d+)(?:[eE][+-]?\d+)?$/;
  // General reduces visible precision as the column narrows. A fixed number
  // format has to keep its specified decimal places, currency and percent sign.
  function fitGeneralNumber(text, fits) {
    const n=Number(text);
    if(!Number.isFinite(n)||n===0)return '';
    const x=Math.abs(n), integerDigits=x<1?1:Math.floor(Math.log10(x))+1;
    if(x<1e11 && x>=1e-9) {
      for(let places=Math.min(14,15-integerDigits);places>=0;places--) {
        const candidate=n.toFixed(places).replace(/(\.\d*?[1-9])0+$|\.0+$/,'$1');
        if(Number(candidate)!==0 && fits(candidate))return candidate;
      }
    }
    for(let places=6;places>=0;places--) {
      const [mantissa,exponent]=n.toExponential(places).split('e');
      const candidate=mantissa.replace(/(\.\d*?[1-9])0+$|\.0+$/,'$1')+'E'+exponent[0]+exponent.slice(1).padStart(2,'0');
      if(fits(candidate))return candidate;
    }
    return '';
  }
  const selectionFills=new Map();
  const rgb=(value,fallback)=>{
    const hex=/^#([\da-f]{6})$/i.exec(value||'');
    return hex?[0,2,4].map(i=>parseInt(hex[1].slice(i,i+2),16)):fallback;
  };
  const luminance=color=>color.reduce((sum,c,i)=>{const v=c/255;return sum+[.2126,.7152,.0722][i]*(v<=.04045?v/12.92:((v+.055)/1.055)**2.4);},0);
  const contrast=(a,b)=>(Math.max(a,b)+.05)/(Math.min(a,b)+.05);
  function selectionFill(background,foreground){
    const key=(background||'')+'|'+(foreground||'');
    if(selectionFills.has(key))return selectionFills.get(key);
    const base=rgb(background,[255,255,255]),ink=luminance(rgb(foreground,[48,67,85]));
    const required=Math.min(4.5,contrast(ink,luminance(base))*.9);
    const mean=base.reduce((sum,c)=>sum+c,0)/3;
    const preferred=mean>=128?0:255;
    let result='rgba(0,0,0,0)';
    for(const gray of [preferred,255-preferred]){
      const distance=Math.abs(gray-mean);
      if(distance<20)continue;
      const alpha=Math.min(1,25/distance);
      const painted=base.map(c=>c+(gray-c)*alpha);
      if(contrast(ink,luminance(painted))>=required){
        result=`rgba(${gray},${gray},${gray},${alpha})`;break;
      }
    }
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
    constructor(options){this.o=options;this.measure=null;this.widths=new Map();this.richWidths=new WeakMap();this.numberWidths=new WeakMap();this.numberMeasures=new Map();this.hashWidths=new Map();this.templates=new Map();this.richPlan=null;}
    prepare(rows){
      this.richPlan=new WeakMap();let remaining=this.o.richLimits?.viewRuns||0;
      for(const row of rows||[])for(const cell of row.cells||[]){
        if((!cell.richText&&!window.ExcelLensConditional?.needsInline(cell,this.style(cell,row.style)))||this.richPlan.has(cell))continue;
        const count=cell.richText?(cell.richText.runs?.length||0)+1:2,allowed=count<=remaining;
        this.richPlan.set(cell,allowed);if(allowed)remaining-=count;
      }
    }
    allowed(cell){return (!cell?.richText&&!window.ExcelLensConditional?.needsInline(cell,this.style(cell)))||!this.richPlan||this.richPlan.get(cell)===true;}
    paintKey(){
      const s=this.o.state,scale=this.o.scale(),p=this.paintState;
      if(!p||p.styles!==s.showStyles||p.formulas!==s.showFormulas||p.font!==s.fontOverride||p.scale!==scale){
        this.paintState={styles:s.showStyles,formulas:s.showFormulas,font:s.fontOverride,scale,key:[s.showStyles,s.showFormulas,s.fontOverride,scale].join('|')};
      }
      return this.paintState.key;
    }
    needsPaint(node,cell,row){return node._xlPaintKey!==this.paintKey()||node._xlRichAllowed!==this.allowed(cell)||node._xlRowPresentationNotice!==(row?.presentationNotice||'');}
    value(cell){
      const s=this.o.state,text=value(cell,s.showFormulas,s.showStyles);
      if(!cell || s.showFormulas&&cell.hasFormula || cell.conditional?.display!=null)return text;
      const pattern=cell.displayNumFmt==null?s.styles?.[cell.style]?.shortDate:cell.shortDate;
      if(!pattern || (cell.type && cell.type!=='n') || !numeric.test(String(cell.raw)))return text;
      const parts=/^(\d{4})-(\d\d)-(\d\d)(.*)$/.exec(text);
      if(!parts)return text;
      const fields={y:parts[1].slice(-2),yy:parts[1].slice(-2),yyyy:parts[1],m:String(+parts[2]),mm:parts[2],d:String(+parts[3]),dd:parts[3]};
      return pattern.replace(/y+|m+|d+/g,t=>fields[t]??t)+parts[4];
    }
    style(cell,rowStyle=0){const s=this.o.state;return window.ExcelLensConditional?.visual(cell,s.styles,rowStyle,s.showStyles)??(s.showStyles?s.styles?.[Number(cell?.style??rowStyle)||0]:null);}
    create(cell,row,rowNumber,col){
      const s=this.o.state,filter=s.autoFilter;
      const ordinary=!cell?.richText&&!cell?.conditional&&!cell?.hyperlink&&!row?.presentationNotice&&
        !(filter&&rowNumber===filter.row&&col>=filter.startCol&&col<=filter.endCol);
      const key=ordinary?[this.paintKey(),cell?.style??row?.style??0,cell?1:0,cell?.hasFormula?1:0,
        cell?.type==='n'||(!cell?.type&&cell?.raw&&Number.isFinite(Number(cell.raw)))?1:0,cell?.valueColor||''].join('|'):null;
      if(this.templatePalette!==s.styles){this.templates.clear();this.templatePalette=s.styles;}
      const saved=key&&this.templates.get(key);
      if(saved){
        const node=saved.node.cloneNode(false);
        node._xlPaintKey=saved.paintKey;node._xlRowStyle=row?.style||0;node._xlModel=cell||null;
        node._xlRichAllowed=true;node._xlRowPresentationNotice='';
        node.textContent=this.value(cell);
        return node;
      }
      const node=document.createElement('div');node.className='grid-cell';
      // Filter controls need their actual address while painting.
      node.dataset.row=String(rowNumber);node.dataset.col=String(col);
      this.paint(node,cell,row);
      if(key&&node.cloneNode&&!node.childElementCount&&!node.title&&node._xlRichAllowed){
        const template=node.cloneNode(false);
        if(this.templates.size>=384)this.templates.clear();
        this.templates.set(key,{node:template,paintKey:node._xlPaintKey});
      }
      return node;
    }
    paint(node,cell,row){
      const fresh=node._xlPaintKey===undefined;
      if(node._xlNumericOriginal)node.title=node._xlNumericOriginalTitle||'';
      node._xlNumericOriginal=node._xlNumericSpan=null;
      const s=this.o.state;node._xlModel=cell||null;node._xlRowStyle=row?.style||0;
      node._xlPaintKey=this.paintKey();node._xlRichAllowed=this.allowed(cell);
      node._xlRowPresentationNotice=row?.presentationNotice||'';
      if(node._xlRichNotice){node.title='';node._xlRichNotice='';}
      if(node._xlPresentationNotice){node.title='';node._xlPresentationNotice='';}
      node._xlRichNode=null;if(!fresh)node.classList.remove('rich-text-cell','rich-text-notice');
      if(node._xlOverflowSpan){node._xlOverflowSpan=null;node.classList.remove('cell-overflow');node.style.removeProperty('--overflow-width');}
      if(!fresh)node.classList.remove('formula-cell','hyperlink-cell','formula-view-cell','auto-filter-header');
      node.textContent=this.value(cell);
      if(cell){const styled=cell.style==null&&row?.style?{...cell,style:row.style}:cell;this.o.applyStyle(node,styled);
        if(cell.hasFormula)node.classList.add('formula-cell');
        if(cell.hyperlink)node.classList.add('hyperlink-cell');
        if(s.showFormulas&&cell.hasFormula)node.classList.add('formula-view-cell');
      }else if(row?.style)this.o.applyStyle(node,{style:row.style});
      const base=this.style(cell,row?.style)||{};
      if(s.showStyles&&(cell?.richText||window.ExcelLensConditional?.needsInline(cell,base))&&!(s.showFormulas&&cell?.hasFormula))window.ExcelLensRichText?.paint(node,cell,base,
        {limits:this.o.richLimits,allowed:this.allowed(cell),fontOverride:s.fontOverride,fontStack:this.o.fontStack,scale:this.o.scale()});
      if(!fresh||cell?.conditional||row?.presentationNotice||/Accounting$/.test(base.underlineStyle||''))window.ExcelLensConditional?.notice(node,cell,row,s.showStyles,base);
      const r=Number(node.dataset.row),c=Number(node.dataset.col),filter=s.autoFilter;
      if(filter&&r===filter.row&&c>=filter.startCol&&c<=filter.endCol){
        node.classList.add('auto-filter-header');const button=document.createElement('button');button.type='button';
        button.className='auto-filter-button'+(filter.rules.has(c)?' is-filtered':'');button.dataset.column=String(c);
        button.textContent='▾';button.title=this.o.colName(c)+' 列：筛选菜单';button.setAttribute('aria-label',button.title);button.disabled=!s.indexReady;node.appendChild(button);
      }
    }
    measureFont(font){if(this.currentFont!==font){this.measure.font=font;this.currentFont=font;}}
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
            this.measureFont(((run.italic??style?.italic)?'italic ':'')+((run.bold??style?.bold)?'700 ':'')+(size*4/3*this.o.scale())+'px '+this.o.fontStack(s.fontOverride||run.name||style?.fontName||'Segoe UI'));
            needed+=this.measure.measureText(part).width;
          });
          this.richWidths.set(cell.richText,{font:fontKey,text,width:needed});
        }
      }else if(needed===undefined){this.measureFont(font);needed=text.length>2048?Infinity:this.measure.measureText(prefix).width+14;this.widths.set(key,needed);if(this.widths.size>2048)this.widths.delete(this.widths.keys().next().value);}
      if(needed<=current.width-2)return 0;
      let available=current.width;
      for(let i=position+1;i<layouts.length;i++){if(occupied(values?.[layouts[i].col])||(row&&this.o.merge?.(row,layouts[i].col)))break;available+=layouts[i].width;if(available>=needed)break;}
      return available>current.width+1?Math.max(current.width,Math.min(available-6,needed)):0;
    }
    numberMetrics(cell,rowStyle){
      const s=this.o.state;
      if(!cell||cell.cacheMissing||s.showFormulas&&cell.hasFormula||!((!cell.type||cell.type==='n')&&numeric.test(String(cell.raw??'').trim())||cell.type==='d'))return null;
      const text=this.value(cell),style=this.style(cell,rowStyle)||{};
      if(!text||style.wrap)return null;
      if(!this.measure){const canvas=typeof document!=='undefined'?document.createElement('canvas'):null;this.measure=canvas?.getContext?.('2d');}if(!this.measure)return null;
      const raised=['superscript','subscript'].includes(cell.conditional?.style?.font?.vertical||style.fontVertical),font=(style.italic?'italic ':'')+(style.bold?'700 ':'')+((style.fontSize||8.25)*(raised ? .65 : 1)*4/3*this.o.scale())+'px '+this.o.fontStack(s.fontOverride||style.fontName||'Segoe UI');
      let measured=this.numberWidths.get(cell);if(!measured||measured.text!==text||measured.font!==font){const key=text.length<=256?font+'\0'+text:null;measured=key&&this.numberMeasures.get(key);if(!measured){this.measureFont(font);let hash=this.hashWidths.get(font);
          if(hash===undefined){hash=this.measure.measureText('#').width;if(this.hashWidths.size>=128)this.hashWidths.clear();this.hashWidths.set(font,hash);}
          measured={text,font,width:this.measure.measureText(text).width,hash};if(key){if(this.numberMeasures.size>=512)this.numberMeasures.delete(this.numberMeasures.keys().next().value);this.numberMeasures.set(key,measured);}}this.numberWidths.set(cell,measured);}
      return measured;
    }
    fitNumber(node,cell,width,padding){
      const restore=()=>{if(node._xlNumericSpan){node._xlNumericSpan.replaceWith(node._xlNumericOriginal);node.title=node._xlNumericOriginalTitle||'';node._xlNumericOriginal=node._xlNumericSpan=null;}return false;};
      const measured=this.numberMetrics(cell,node._xlRowStyle);
      if(!measured)return restore();
      const {text,font}=measured,s=this.o.state;
      padding=padding??(this.o.numericPadding?this.o.numericPadding(node):node.classList.contains('auto-filter-header')?32:14);const available=Math.max(0,width-padding);
      if(!(width>0)||!Number.isFinite(measured.width)||measured.width<=available+.25)return restore();
      if(!node._xlNumericSpan){const original=node._xlRichNode||[...node.childNodes].find(n=>n.nodeType===3);if(!original)return false;const span=document.createElement('span');span.className='cell-number-overflow';node._xlNumericOriginal=original;node._xlNumericOriginalTitle=node.title||'';node._xlNumericSpan=span;original.replaceWith(span);}
      let fitted='';
      if(text===String(cell.raw) && !cell.conditional?.display && !(cell.displayNumFmt??s.styles?.[cell.style]?.numFmtId)) {
        if(measured.fitWidth!==available) {
          this.measureFont(font);
          measured.fitted=fitGeneralNumber(text, candidate=>this.measure.measureText(candidate).width<=available+.25);
          measured.fitWidth=available;
        }
        fitted=measured.fitted;
      }
      node._xlNumericSpan.textContent=fitted||'#'.repeat(Math.min(128,Math.max(1,Math.floor(available/Math.max(1,measured.hash)))));node.title=[node._xlNumericOriginalTitle,'宽度不足，完整显示值：'+text].filter(Boolean).join('；');return true;
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
  const sourceType=cell=>cell.cacheMissing?'公式无保存结果':cell.presentationOnly?'源为空白':cell.sourceType?'ODS · '+cell.sourceType+(cell.currency?' '+cell.currency:''):cell.raw===''&&!cell.hasFormula?'空白':types.get(cell.type)||(cell.type?'源类型 '+cell.type:'数值');
  const formula=cell=>cell.hasFormula&&typeof cell.formula==='string'&&cell.formula.startsWith('=')&&cell.formula.length>1?cell.formula:null;
  window.ExcelLensCellView={value,occupied,isText,Presenter,selectionFill,sourceType,formula};
})();
