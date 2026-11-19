/* Source text stays immutable. DOM and clipboard share validated UTF-16 spans. */
(() => {
  'use strict';
  const own=(value,key)=>Object.prototype.hasOwnProperty.call(value,key);
  const escape=value=>String(value??'').replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;').replace(/'/g,'&#39;');
  const color=value=>/^#[\da-f]{6}$/i.test(value||'');
  const underlines=new Set(['','none','single','double','singleAccounting','doubleAccounting']);
  const verticals=new Set(['','baseline','superscript','subscript']);
  const fontKeys=['name','size','color','autoColor','bold','italic','strike','underline','vertical'];
  function overlayFont(font,overlay){
    if(!overlay||!Object.keys(overlay).length)return font;
    const out={...font};for(const key of fontKeys)if(overlay[key]!=null&&overlay[key]!==''&&overlay[key]!==0)out[key]=overlay[key];
    if(overlay.autoColor)out.color='';if(overlay.color)out.autoColor=false;return out;
  }
  const quoteFont=name=>'"'+String(name).replace(/[\\"]/g,'\\$&').replace(/[\x00-\x1f\x7f<>&;]/g,c=>'\\'+c.charCodeAt(0).toString(16)+' ')+'"';
  function validFont(font){
    if(!font||typeof font!=='object'||Array.isArray(font)||Object.keys(font).some(key=>!fontKeys.includes(key)))return false;
    if(font.name!=null&&(typeof font.name!=='string'||font.name.length>160||[...font.name].length>80||/[\x00\r\n]/.test(font.name)))return false;
    if(font.size!=null&&font.size!==0&&(!Number.isFinite(font.size)||font.size<1||font.size>409))return false;
    if(font.color!=null&&font.color!==''&&!color(font.color))return false;
    if(font.autoColor!=null&&typeof font.autoColor!=='boolean')return false;
    for(const key of ['bold','italic','strike'])if(own(font,key)&&typeof font[key]!=='boolean')return false;
    return underlines.has(font.underline||'')&&verticals.has(font.vertical||'');
  }
  const boundary=(text,end)=>end<=0||end>=text.length||!(text.charCodeAt(end-1)>=0xd800&&text.charCodeAt(end-1)<=0xdbff&&text.charCodeAt(end)>=0xdc00&&text.charCodeAt(end)<=0xdfff);
  function inspect(cell,limits){
    const rich=cell?.richText,text=String(cell?.display??cell?.raw??'');
    if(!rich)return null;
    const invalid=message=>({text,runs:[],notice:message});
    if(text!==String(cell.raw??''))return invalid('显示值与原文不同，富文本片段未应用');
    if(!limits||!Number.isSafeInteger(limits.cellRuns)||limits.cellRuns<1)return invalid('富文本显示容量不可用，原文保留');
    const runs=rich.runs||[],notice=String(rich.notice||'');
    if(!Array.isArray(runs)||runs.length>limits.cellRuns||notice.length>1024)return invalid('富文本样式无效或超限，原文保留');
    let previous=0;
    for(const run of runs){
      if(!Number.isSafeInteger(run?.end)||run.end<=previous||run.end>text.length||!boundary(text,run.end)||!validFont(run.font))return invalid('富文本样式无效，原文保留');
      previous=run.end;
    }
    if(!notice&&previous!==text.length)return invalid('富文本样式未覆盖原文，原文保留');
    const extra=runs.some(run=>/Accounting$/.test(run.font.underline||''))?'会计下划线按片段下划线显示':'';
    return {text,runs,notice:[notice,extra].filter(Boolean).join('；')};
  }
  function properties(font,base={},options={}){
    font=overlayFont(font,options.fontOverlay);
    if(!font.vertical&&base.fontVertical)font={...font,vertical:base.fontVertical};
    const p={},name=options.fontOverride||font.name;
    if(name)p['font-family']=options.fontStack?options.fontStack(name):quoteFont(name);
    if(own(font,'bold'))p['font-weight']=font.bold?'700':'400';
    if(own(font,'italic'))p['font-style']=font.italic?'italic':'normal';
    const u=font.underline||base.underlineStyle||(base.underline?'single':'none'),strike=font.strike??!!base.strike;
    p['text-decoration-line']=[u!=='none'&&'underline',strike&&'line-through'].filter(Boolean).join(' ')||'none';
    if(u==='double'||u==='doubleAccounting')p['text-decoration-style']='double';
    if(/Accounting$/.test(u))p['text-underline-position']='under';
    if(font.color)p.color=font.color;
    else if(font.autoColor)p.color=options.dom?'var(--rich-auto-color,#344454)':'#000000';
    if(options.dom&&p.color){p['--rich-color']=p.color;p.color='var(--rich-ink,var(--rich-color))';}
    const raised=font.vertical==='superscript'||font.vertical==='subscript';
    if(font.size||raised){
      const size=(font.size||base.fontSize||8.25)*(raised ? .65 : 1);
      p['font-size']=options.dom?size*4/3*(options.scale||1)+'px':size+'pt';
    }
    if(font.vertical)p['vertical-align']=raised?(font.vertical==='superscript'?'super':'sub'):'baseline';
    return p;
  }
  function eachPart(info,visit){
    let start=0;
    for(const run of info.runs){visit(info.text.slice(start,run.end),run.font);start=run.end;}
    if(start<info.text.length)visit(info.text.slice(start),{});
  }
  function paint(node,cell,base,options){
    options={...options,fontOverlay:cell?.conditional?.style?.font};
    const info=presentationInfo(cell,options,base);
    if(!info)return false;
    const notice=options.allowed===false?'富文本样式超过当前视口容量，原文完整保留':info.notice;
    if(notice){node.classList.add('rich-text-notice');node.title=notice;node._xlRichNotice=notice;}
    if(!info.runs.length||options.allowed===false)return false;
    const root=document.createElement('span');root.className='cell-rich-text';
    eachPart(info,(text,font)=>{const span=document.createElement('span');span.textContent=text;
      for(const [key,value]of Object.entries(properties(font,base,{...options,dom:true})))span.style.setProperty(key,value);
      root.appendChild(span);
    });
    node.textContent='';node.appendChild(root);node.classList.add('rich-text-cell');
    // Decorations on a flex parent cannot be cleared by a child. Every run
    // therefore owns its complete inherited/explicit decoration instead.
    node.style.textDecoration='none';node._xlRichNode=root;
    return true;
  }
  function html(cell,base,options,encoded=text=>escape(text).replace(/\r\n?|\n/g,'<br>')){
    options={...options,fontOverlay:cell?.conditional?.style?.font};
    const info=presentationInfo(cell,options,base);
    if(!info)return null;
    const parts=[];
    if(!info.runs.length)return {html:encoded(info.text),notice:info.notice,rich:false};
    eachPart(info,(text,font)=>{const css=Object.entries(properties(font,base,options)).map(([k,v])=>k+':'+v).join(';');parts.push('<span style="'+escape(css)+'">'+encoded(text)+'</span>');});
    return {html:parts.join(''),notice:info.notice,rich:true};
  }
  function presentationInfo(cell,options,base={}){
    const display=cell?.conditional?.display,source=display!=null?{...cell,display}:cell;
    let info=inspect(source,options.limits);
    if(!info&&['superscript','subscript'].includes(options.fontOverlay?.vertical||base.fontVertical)){
      const text=String(source?.display??source?.raw??'');info={text,runs:text?[{end:text.length,font:{}}]:[],notice:''};
    }
    return info;
  }
  function estimate(value){
    if(!value)return 0;
    let bytes=96+2*(value.notice?.length||0);
    for(const run of value.runs||[]){bytes+=288;for(const key of fontKeys)if(typeof run.font?.[key]==='string')bytes+=2*run.font[key].length;}
    return bytes;
  }
  function same(a,b){
    if(a===b)return true;if(!a||!b)return false;
    if(a.notice!==b.notice||(a.runs?.length||0)!==(b.runs?.length||0))return false;
    return (a.runs||[]).every((run,i)=>run.end===b.runs[i].end&&fontKeys.every(key=>run.font?.[key]===b.runs[i].font?.[key]));
  }
  window.ExcelLensRichText={inspect,properties,eachPart,paint,html,estimate,same,overlayFont};
})();
