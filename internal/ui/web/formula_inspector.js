/* ExcelLens formula inspector. Read-only A1-reference lexer and interactive
 * preview. No formula evaluation and no workbook mutation. Uses shared column labels.
 */
(() => {
  'use strict';
  const MAX_ROW = 1048576, MAX_COL = 16384;
  const PALETTE = ['#1d70b6','#b45309','#16805e','#8b46b1','#cc4763','#187e95','#9b6b18','#654fb3'];
  const wordChar = c => Boolean(c) && /[\p{L}\p{N}_.]/u.test(c);
  const letter = c => Boolean(c) && /[A-Za-z]/.test(c);
  const number = c => Boolean(c) && c >= '0' && c <= '9';
  const pretty = window.ExcelLensAddress.columnName;
  const address = (row, col) => `${pretty(col)}${row}`;

  function skipString(s, i) {
    i++;
    while (i < s.length) {
      if (s[i] === '"') {
        if (s[i + 1] === '"') { i += 2; continue; }
        return i + 1;
      }
      i++;
    }
    return i;
  }
  function skipBracket(s, i) {
    let depth=0;
    for (; i<s.length; i++) {
      if (s[i]==='[') depth++;
      else if (s[i]===']' && --depth===0) return i+1;
    }
    return i;
  }
  function sheetPrefix(s, i) {
    if (s[i] === "'") {
      let p=i+1, name='';
      for (;p<s.length;) {
        if (s[p]==="'") {
          if (s[p+1]==="'") {name+="'";p+=2;continue;}
          if (s[p+1]==='!') return {name, after:p+2, external:name.includes('[') || name.includes(']')};
          return null;
        }
        name+=s[p++];
      }
      return null;
    }
    if (!wordChar(s[i]) || number(s[i])) return null;
    let p=i;
    while (p<s.length && wordChar(s[p])) p++;
    if (s[p]==='!' && p>i) return {name:s.slice(i,p),after:p+1,external:false};
    return null;
  }
  function atom(s, from) {
    let p=from;
    if (s[p]==='$') p++;
    const start=p;
    while (letter(s[p]) && p-start < 4) p++;
    const letters=s.slice(start,p);
    if (letters.length > 0 && letters.length <= 3) {
      const col = window.ExcelLensAddress.columnNumber(letters);
      if(col<1 || col>MAX_COL) return null;
      if(s[p]==='$') p++;
      const rs=p;
      while(number(s[p]) && p-rs <= 7) p++;
      if (p>rs) {
        const row=Number(s.slice(rs,p));
        return row>0 && row<=MAX_ROW ? {kind:'cell',row,col,end:p} : null;
      }
      return {kind:'column',col,end:p};
    }
    p=from;
    if(s[p]==='$') p++;
    const rs=p;
    while(number(s[p]) && p-rs <= 7) p++;
    if(p===rs) return null;
    const row=Number(s.slice(rs,p));
    return row>0&&row<=MAX_ROW?{kind:'row',row,end:p}:null;
  }
  function bounds(a,b) {
    if(a.kind==='cell') return {
      kind: a.row===b.row && a.col===b.col ? 'cell' : 'range',
      start: {row:Math.min(a.row,b.row),col:Math.min(a.col,b.col)},
      end: {row:Math.max(a.row,b.row),col:Math.max(a.col,b.col)}
    };
    if(a.kind==='column') return {
      kind:'column',start:{row:1,col:Math.min(a.col,b.col)},
      end:{row:MAX_ROW,col:Math.max(a.col,b.col)}
    };
    return {kind:'row',start:{row:Math.min(a.row,b.row),col:1},
      end:{row:Math.max(a.row,b.row),col:MAX_COL}};
  }

  function parseFormula(formula, currentSheet='', maxRefs=256) {
    const s=String(formula||'');
    const refs=[];
    const colorKeys=new Map();
    for(let i=0;i<s.length && refs.length<maxRefs;) {
      if(s[i]==='"') {i=skipString(s,i);continue;}
      if(s[i]==='[') {i=skipBracket(s,i);continue;}
      if(i>0 && (wordChar(s[i-1]) || s[i-1]==='!' || s[i-1]===']' || s[i-1]==='$')) {i++;continue;}
      const prefix=sheetPrefix(s,i);
      if(s[i]==="'" && !prefix) { // Quoted sheet name that is not followed by a reference.
        let q=i+1;
        while(q<s.length) {if(s[q]==="'"){if(s[q+1]==="'"){q+=2;continue;}q++;break;}q++;}
        i=q;continue;
      }
      // A 3D reference such as Sheet1:Sheet3!A1 is not one local cell.
      if(i>0 && s[i-1]===':' && prefix) {i=prefix.after;continue;}
      const sheet=prefix?.name||currentSheet;
      const startAt=prefix?prefix.after:i;
      const a=atom(s,startAt);
      if(!a) {i=prefix?Math.max(i+1,prefix.after):i+1;continue;}
      let p=a.end, b=a;
      if(s[p]===':') {
        let next=p+1;
        const secondPrefix=sheetPrefix(s,next);
        if(secondPrefix) {
          if(secondPrefix.name.toLocaleLowerCase()!==sheet.toLocaleLowerCase()) {
            i=secondPrefix.after;continue;
          }
          next=secondPrefix.after;
        }
        const second=atom(s,next);
        if(second && second.kind===a.kind) {b=second;p=second.end;}
      }
      if((a.kind!=='cell' && b===a) || wordChar(s[p]) || s[p]==='!' || s[p]==='(' || s[p]==='$') {
        i=prefix?Math.max(i+1,prefix.after):i+1;continue;
      }
      const r=bounds(a,b);
      const external=Boolean(prefix?.external);
      const key=`${sheet.toLocaleLowerCase()}|${r.kind}|${r.start.row},${r.start.col}|${r.end.row},${r.end.col}`;
      if(!colorKeys.has(key))colorKeys.set(key,colorKeys.size%PALETTE.length);
      refs.push({ ...r, sheet, external,
        startIndex:i,endIndex:p,label:s.slice(i,p),
        key,colorIndex:colorKeys.get(key),
        range:r.kind!=='cell'
      });
      i=p;
    }
    return refs;
  }

  class FormulaInspector {
    constructor(o) {
      this.o=o;
      this.el=o.element;
      this.pop=o.tooltip;
      this.expandButton=document.getElementById('formulaExpandBtn');
      this.toolbar=this.el.closest('.formula-toolbar');
      this.workspace=this.el.closest('.workspace');
      this.expanded=false;
      this.active=false;
      this.tokens=[];
      this.tokensBySheet=new Map();
      this.sheet='';
      this.hoverIndex=-1;
      this.pinIndex=-1;
      this.generation=0;
      this.previewID=0;
      this.pendingTimer=0;
      this.refCache=new Map();
      this.indexTasks=new Map();
      this.readySheets=new Set();
      this.makeTooltip();
      this.bind();
    }
    resetWorkbook() {
      this.refCache.clear();this.indexTasks.clear();this.readySheets.clear();
      this.clear();
    }
    clear() {
      this.generation++;this.previewID++;clearTimeout(this.pendingTimer);
      this.active=false;this.tokens=[];this.tokensBySheet.clear();this.sheet='';this.hoverIndex=-1;this.pinIndex=-1;
      this.el.textContent='';this.el.scrollLeft=0;this.el.scrollTop=0;this.el.classList.remove('formula-focused');
      this.pop.hidden=true;
      this.decorateAll();
    }
    show(cell,sheet) {
      this.generation++;this.previewID++;clearTimeout(this.pendingTimer);
      this.active=false;this.hoverIndex=-1;this.pinIndex=-1;
      this.pop.hidden=true;
      this.sheet=sheet||'';
      const text=cell?.hasFormula?(cell.formula||''):String(cell?.raw??'');
      this.tokens=cell?.hasFormula?parseFormula(text,this.sheet):[];
      // Resolve case-insensitive Excel sheet names back to their canonical
      // spelling before making requests or navigating between sheet tabs.
      for(const token of this.tokens){
        const actual=this.o.state.sheets.find(sh=>sh.name.toLocaleLowerCase()===token.sheet.toLocaleLowerCase());
        if(actual)token.sheet=actual.name;
      }
      this.tokensBySheet.clear();
      this.tokens.forEach((token,i)=>{
        token.index=i;
        const lower=token.sheet.toLocaleLowerCase();
        if(!this.tokensBySheet.has(lower))this.tokensBySheet.set(lower,[]);
        if(!token.external)this.tokensBySheet.get(lower).push(token);
      });
      this.el.title=this.tokens.length>=256?'引用较多：目前只交互显示前 256 处引用':'';
      if(!this.tokens.length) {this.el.textContent=text;this.el.classList.remove('formula-focused');this.decorateAll();return;}
      const f=document.createDocumentFragment();
      let pos=0;
      for(let n=0;n<this.tokens.length;n++){
        const token=this.tokens[n];
        if(token.startIndex>pos)f.appendChild(document.createTextNode(text.slice(pos,token.startIndex)));
        const span=document.createElement('span');
        span.className='formula-token'+(token.external?' formula-token-external':'');
        span.dataset.token=String(n);
        span.style.setProperty('--formula-ref-color', PALETTE[token.colorIndex]);
        span.textContent=text.slice(token.startIndex,token.endIndex);
        span.tabIndex=0;
        span.setAttribute('role','button');
        span.setAttribute('aria-label', `${token.label}：悬停查看引用值，回车高亮`);
        f.appendChild(span);
        pos=token.endIndex;
      }
      if(pos<text.length)f.appendChild(document.createTextNode(text.slice(pos)));
      this.el.replaceChildren(f);
      this.el.scrollLeft=0;this.el.scrollTop=0;
      this.el.classList.remove('formula-focused');
      this.decorateAll();
    }
    setExpanded(expanded) {
      this.expanded=Boolean(expanded);
      this.toolbar?.classList.toggle('formula-expanded',this.expanded);
      this.workspace?.classList.toggle('formula-expanded',this.expanded);
      if(this.expandButton){
        const label=this.expanded?'收起公式栏':'展开公式栏';
        this.expandButton.setAttribute('aria-expanded',String(this.expanded));
        this.expandButton.setAttribute('aria-label',label);
        this.expandButton.title=this.expanded?'收起公式栏（单行显示）':'展开公式栏（长公式换行显示）';
      }
      this.el.scrollTop=0;this.el.scrollLeft=0;
      // The spreadsheet viewport becomes taller/shorter when the formula
      // editor expands, so refresh only the currently visible virtual tiles.
      requestAnimationFrame(()=>this.o.onLayoutChange?.());
    }
    bind() {
      this.expandButton?.addEventListener('click',()=>this.setExpanded(!this.expanded));
      // A normal mouse wheel can be used with Shift to reveal text beyond the
      // end of the one-line bar. No visible or overlapping scrollbar is needed.
      this.el.addEventListener('wheel',event=>{
        if(this.expanded||!event.shiftKey||this.el.scrollWidth<=this.el.clientWidth)return;
        if(event.deltaY){event.preventDefault();this.el.scrollLeft+=event.deltaY;}
      },{passive:false});
      this.el.addEventListener('click', e=>{
        if(!this.tokens.length)return;
        this.activate();
        const node=e.target.closest?.('.formula-token');
        if(node){ const n=Number(node.dataset.token);this.pinIndex=n;this.hoverIndex=n;this.preview(n,node,true);}
        else {this.pinIndex=-1;this.hoverIndex=-1;this.pop.hidden=true;this.decorateAll();}
      });
      this.el.addEventListener('dblclick',e=>{
        const node=e.target.closest?.('.formula-token');
        if(!node)return;
        const token=this.tokenAt(Number(node.dataset.token));
        if(!token || token.external || !this.o.sheetExists(token.sheet))return;
        e.preventDefault();
        this.pop.hidden=true;
        this.o.navigate(token.sheet,address(token.start.row,token.start.col),false).catch(err=>this.o.notify(err.message,'error'));
      });
      this.el.addEventListener('pointermove',e=>{
        if(!this.active || !this.tokens.length)return;
        const node=e.target.closest?.('.formula-token');
        const n=node?Number(node.dataset.token):-1;
        if(n===this.hoverIndex)return;
        this.hoverIndex=n;
        if(n>=0) this.preview(n,node,false);
        else if(this.pinIndex<0){this.previewID++;clearTimeout(this.pendingTimer);this.pop.hidden=true;}
        this.decorateAll();
      });
      this.el.addEventListener('pointerleave',()=>{
        this.hoverIndex=-1;
        if(this.pinIndex<0){this.previewID++;clearTimeout(this.pendingTimer);this.pop.hidden=true;}
        this.decorateAll();
      });
      this.el.addEventListener('keydown',e=>{
        if(e.key==='Escape') {
          e.preventDefault();e.stopPropagation();
          this.pinIndex=-1;this.hoverIndex=-1;this.pop.hidden=true;this.active=false;
          this.el.classList.remove('formula-focused');this.decorateAll();
          this.o.grid?.focus({preventScroll:true});
          return;
        }
        if((e.ctrlKey||e.metaKey)&&e.key.toLowerCase()==='c'){
          e.stopPropagation();
          const selected=window.getSelection()?.toString();
          if(!selected){e.preventDefault();this.o.copy(this.el.textContent);}
          return;
        }
        const node=e.target.closest?.('.formula-token');
        if(node && (e.key==='Enter' || e.key===' ')) {
          e.preventDefault();e.stopPropagation();
          this.activate(); const n=Number(node.dataset.token);this.pinIndex=n;this.hoverIndex=n;
          this.preview(n,node,true);this.decorateAll();
        } else if(e.key.startsWith('Arrow')) e.stopPropagation();
      });
      this.pop.addEventListener('pointerenter',()=>{ if(this.pinIndex>=0)clearTimeout(this.pendingTimer); });
      document.addEventListener('pointerdown',e=>{
        if(this.pinIndex<0 || this.el.contains(e.target) || this.pop.contains(e.target))return;
        this.pinIndex=-1;this.hoverIndex=-1;this.pop.hidden=true;this.decorateAll();
      });
      document.addEventListener('keydown',e=>{
        if(e.key!=='Escape' || !this.active || e.target.closest?.('dialog') || this.el.contains(e.target))return;
        this.pinIndex=-1;this.hoverIndex=-1;this.pop.hidden=true;this.active=false;
        this.el.classList.remove('formula-focused');this.decorateAll();
      });
    }
    makeTooltip() {
      const pop=this.pop;
      pop.replaceChildren();
      this.head=document.createElement('div');this.head.className='formula-preview-heading';
      this.content=document.createElement('div');this.content.className='formula-preview-content';
      this.actions=document.createElement('div');this.actions.className='formula-preview-actions';
      this.jump=document.createElement('button');this.jump.textContent='定位引用';this.jump.type='button';
      this.copy=document.createElement('button');this.copy.textContent='复制地址';this.copy.type='button';
      this.actions.append(this.jump,this.copy);
      pop.append(this.head,this.content,this.actions);
      this.jump.addEventListener('click',()=>{
        const t=this.tokens[this.pinIndex>=0?this.pinIndex:this.hoverIndex];
        if(!t || t.external)return;
        this.pop.hidden=true;
        this.o.navigate(t.sheet,address(t.start.row,t.start.col),false).catch(err=>this.o.notify(err.message,'error'));
      });
      this.copy.addEventListener('click',()=>{
        const t=this.tokens[this.pinIndex>=0?this.pinIndex:this.hoverIndex];
        if(t) this.o.copy(t.label);
      });
    }
    activate() {
      if(!this.active){this.active=true;this.el.classList.add('formula-focused');this.decorateAll();}
    }
    tokenAt(n) {return this.tokens[n]||null;}
    currentTarget(){return this.tokenAt(this.hoverIndex>=0?this.hoverIndex:this.pinIndex);}
    matchAt(row,col,sheet) {
      if(!this.active)return null;
      const relevant=this.tokensBySheet.get(sheet.toLocaleLowerCase())||[];
      let candidate=null;
      for(const t of relevant){
        if(row<t.start.row || row>t.end.row || col<t.start.col || col>t.end.col)continue;
        if(this.hoverIndex===t.index || this.pinIndex===t.index)return {token:t,focus:true};
        if(!candidate)candidate={token:t,focus:false};
      }
      return candidate;
    }
    decorateCell(el,row,col,sheet) {
      const match=this.matchAt(row,col,sheet);
      if(!match){
        if(el.classList.contains('formula-referenced')){
          el.classList.remove('formula-referenced','formula-ref-focus');
          el.style.removeProperty('--formula-ref-color');
        }
        return;
      }
      el.classList.add('formula-referenced');
      el.classList.toggle('formula-ref-focus',match.focus);
      const color=PALETTE[match.token.colorIndex];
      if(el.style.getPropertyValue('--formula-ref-color')!==color)el.style.setProperty('--formula-ref-color',color);
    }
    decorateAll(){
      if(this.o?.state){this.o.state.decorVersion++;}
      for(const node of document.querySelectorAll('.grid-region .grid-cell[data-row][data-col]')){
        this.decorateCell(node, Number(node.dataset.row),Number(node.dataset.col),this.o.state.sheet||'');
      }
      for(const tokenNode of this.el.querySelectorAll('.formula-token')){
        const n=Number(tokenNode.dataset.token);
        tokenNode.classList.toggle('is-hovered',n===this.hoverIndex || n===this.pinIndex);
      }
    }
    place(node){
      const rect=node.getBoundingClientRect();
      const width=Math.min(390,window.innerWidth-16);
      const x=Math.max(8,Math.min(window.innerWidth-width-8,rect.left));
      const below=rect.bottom+9;
      const height=188;
      const y=below+height<window.innerHeight?below:Math.max(8,rect.top-height-8);
      this.pop.style.left=`${x}px`;this.pop.style.top=`${y}px`;
    }
    async preview(n,node,pinned) {
      const t=this.tokenAt(n);if(!t)return;
      const request=++this.previewID;
      clearTimeout(this.pendingTimer);
      this.head.textContent=t.label;
      this.content.textContent=t.external?'外部工作簿引用：不读取外部文件':
        (this.o.sheetExists(t.sheet)?'读取引用单元格…':'工作簿中不存在对应工作表');
      this.actions.hidden=!pinned || t.external || !this.o.sheetExists(t.sheet);
      this.pop.classList.toggle('is-pinned',pinned);
      this.pop.hidden=false;
      this.place(node);
      if(t.external || !this.o.sheetExists(t.sheet))return;
      const generation=this.generation;
      const update=(message)=>{
        if(this.generation===generation && this.previewID===request) this.content.textContent=message;
      };
      this.pendingTimer=setTimeout(async()=>{
        try {
          const start=t.start;
          const size=(t.end.row-start.row+1)*(t.end.col-start.col+1);
          const prefix=t.kind==='cell'?'':`${t.kind==='row'?'整行':t.kind==='column'?'整列':'区域'}引用 · ${t.end.row-start.row+1} 行 × ${t.end.col-start.col+1} 列\n`;
          const first=await this.readCell(t.sheet,address(start.row,start.col),update);
          if(this.generation!==generation || this.previewID!==request)return;
          let msg=prefix+this.describe(first,address(start.row,start.col));
          update(msg);
          if(t.kind==='range' && size>1 && size<=10000){
            const endAddress=address(t.end.row,t.end.col);
            const last=await this.readCell(t.sheet,endAddress,update);
            if(this.generation!==generation || this.previewID!==request)return;
            msg+='\n终点 '+this.describe(last,endAddress);
            update(msg);
          } else if(t.kind!=='cell') {
            msg+='\n仅预览起点，不计算区域结果';update(msg);
          }
        } catch(err) {update(`读取失败：${err.message}`);}
      },pinned?0:140);
    }
    describe(cell,ref){
      const value=cell?.cacheMissing?'无缓存（未重算）':(cell?.display??'');
      const raw=cell?.raw??'';
      return `${ref} · 保存值：${value===''?'（空白）':value}` +
        (raw!==''&&String(raw)!==String(value)?`\n原始值：${raw}`:'') +
        (cell?.hasFormula?`\n公式：${cell.formula||'（未解析到公式文本）'}`:'');
    }
    async ensureIndex(sheet, progress) {
      if(sheet===this.o.state.sheet && this.o.state.indexReady)return;
      if(this.readySheets.has(sheet))return;
      if(this.indexTasks.has(sheet))return this.indexTasks.get(sheet);
      const activity=globalThis.ExcelLensActivity?.begin("正在读取引用工作表");
      const task=(async()=>{
        const started=await this.o.api('/api/index/start',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({sheet})});
        if(started.ready) {this.readySheets.add(sheet);return;}
        while(true){
          await new Promise(r=>setTimeout(r,270));
          const result=await this.o.api('/api/index/status?id='+encodeURIComponent(started.jobId));
          activity?.job(result);
          progress(`首次读取「${sheet}」：建立索引 ${Math.round(result.progress||0)}%`);
          if(result.status==='done'){this.readySheets.add(sheet);return;}
          if(result.status==='error'||result.status==='cancelled')throw new Error(result.error||'建立索引失败');
        }
      })();
      this.indexTasks.set(sheet,task);
      try {await task;} finally {activity?.end();this.indexTasks.delete(sheet);}
    }
    async readCell(sheet,ref,progress) {
      const key=sheet+'!'+ref;
      if(this.refCache.has(key))return this.refCache.get(key);
      const inViewport=sheet===this.o.state.sheet && (this.o.state.cellMap.get(ref)||this.o.state.frozenCellMap.get(ref));
      if(inViewport){this.refCache.set(key,Promise.resolve(inViewport));return inViewport;}
      const task=(async()=>{
        await this.ensureIndex(sheet,progress);
        return this.o.getCell(sheet,ref);
      })();
      this.refCache.set(key,task);
      if(this.refCache.size>512)this.refCache.delete(this.refCache.keys().next().value);
      try{return await task;}catch(err){this.refCache.delete(key);throw err;}
    }
  }
  window.ExcelLensFormulaInspector={parseFormula,FormulaInspector,PALETTE};
})();
