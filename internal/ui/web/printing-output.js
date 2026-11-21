(() => {
  'use strict';
  function pageRange(text,total,limit){
    const selected=new Set();
    for(const part of text.trim().split(/[,，]/)){
      const match=/^\s*(\d+)(?:\s*[-–]\s*(\d+))?\s*$/.exec(part);
      if(!match)throw Error('页段可填写 1-3,5');
      const first=Number(match[1]),last=Number(match[2]||match[1]);
      if(!Number.isSafeInteger(last)||first<1||last<first||last>total)throw Error('输出页段超出已确认的分页范围');
      if(last-first+1>limit)throw Error('输出页数超出容量，请分段导出');
      for(let page=first;page<=last;page++){selected.add(page-1);if(selected.size>limit)throw Error('输出页数超出容量，请分段导出');}
    }
    return [...selected].sort((a,b)=>a-b);
  }
  function paperCSS(){
    const rules=[];
    for(const sheet of document.styleSheets){
      if(!sheet.href||new URL(sheet.href).origin!==location.origin)continue;
      for(const rule of sheet.cssRules){
        if(rule.selectorText&&rule.selectorText.split(',').some(s=>/^\.(print-(paper|data|edges?|body|cell|source-heading)|cell-rich)/.test(s.trim())))rules.push(rule.cssText);
      }
    }
    return rules.join('\n');
  }
  class Output {
    constructor(title,limits){
      this.title=title;this.limits=limits;this.parts=[];this.sizes=new Map();this.notes=new Set();this.bytes=0;this.encoder=new TextEncoder();
    }
    add(rendered,source,page,notes){
      if(this.parts.length>=this.limits.outputPages)throw Error('输出页数超出容量，请分段导出');
      const paper=rendered.node;paper.style.transform='none';
      const size=rendered.width+'px '+rendered.height+'px';
      if(!this.sizes.has(size))this.sizes.set(size,'paper'+this.sizes.size);
      paper.style.page=this.sizes.get(size);
      paper.setAttribute('aria-label',source+' · 第 '+page+' 页');
      for(const cell of paper.querySelectorAll('[data-missing-cache]')){if(!cell.textContent.trim())cell.textContent=cell.getAttribute('data-missing-cache');}
      const html=paper.outerHTML;
      this.bytes+=this.encoder.encode(html).length;
      if(this.bytes>this.limits.outputBytes)throw Error('底稿内容超过容量，请分段导出');
      for(const note of [...notes,...rendered.warnings])if(note)this.notes.add(note);
      this.parts.push(html);
    }
    file(){
      if(!this.parts.length)throw Error('所选范围没有可打印页面');
      const safe=text=>{const el=document.createElement('span');el.textContent=text;return el.innerHTML;};
      const pages=[...this.sizes].map(([size,name])=>'@page '+name+'{size:'+size+';margin:0}').join('\n');
      const css=paperCSS()+'\n'+pages+
        '\n*{box-sizing:border-box}html,body{margin:0;padding:0}body{background:#eee}.print-paper{position:relative;left:auto;top:auto;margin:12px auto;break-after:page;break-inside:avoid;print-color-adjust:exact;-webkit-print-color-adjust:exact}.print-paper:last-child{break-after:auto}.notes{font:14px sans-serif;padding:12px;color:#222}.print-paper [data-missing-cache]{color:#765200}@media print{body{background:white}.notes{display:none}.print-paper{margin:0;box-shadow:none}}';
      const notes=[...this.notes,'字体由打开底稿的系统提供，缺少原字体时使用替代字体。'];
      const head='<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="default-src \'none\'; style-src \'unsafe-inline\'"><title>'+safe(this.title)+'</title><style>'+css+'</style><body><section class="notes"><strong>'+safe(this.title)+'</strong><p>'+this.parts.length+' 页 · 可打印或另存为 PDF</p>'+notes.map(n=>'<p>'+safe(n)+'</p>').join('')+'</section>';
      const blob=new Blob([head,...this.parts,'</body></html>'],{type:'text/html;charset=utf-8'});
      if(blob.size>this.limits.outputBytes)throw Error('底稿内容超过容量，请分段导出');return blob;
    }
  }
  window.ExcelLensPrintingOutput={pageRange,Output};
})();
