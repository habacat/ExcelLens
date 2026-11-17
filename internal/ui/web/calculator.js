(() => {
  'use strict';
  const $=id=>document.getElementById(id),ROW=26,HEADER=26;
  const quote=sheet=>"'"+sheet.replace(/'/g,"''")+"'!";
  function formulaContext(text,caret){
    const before=text.slice(0,caret),stack=[];let quoted='',word='';
    for(let i=0;i<before.length;i++){const c=before[i];if(quoted){if(c===quoted){if(before[i+1]===quoted){i++;continue;}quoted='';}continue;}if(c==='"'||c==="'"){quoted=c;word='';continue;}if(/[A-Za-z_]/.test(c)||word&&/[0-9.]/.test(c)){word+=c;continue;}if(c==='('){stack.push(word.toUpperCase());}else if(c===')')stack.pop();word='';}
    if(quoted||!text.trimStart().startsWith('='))return null;
    const match=before.match(/[A-Za-z_][A-Za-z0-9_.]*$/);
    const rangeEnd=match&&/:\s*$/.test(before.slice(0,match.index));
    return {prefix:rangeEnd?'':match?.[0]||'',start:match&&!rangeEnd?caret-match[0].length:caret,end:caret,fn:stack.at(-1)||''};
  }
  function closeFormula(text){let depth=0,quoted='';for(let i=0;i<text.length;i++){const c=text[i];if(quoted){if(c===quoted){if(text[i+1]===quoted){i++;continue;}quoted='';}continue;}if(c==='"'||c==="'")quoted=c;else if(c==='(')depth++;else if(c===')')depth--;}return !quoted&&depth>0&&depth<=40?text+')'.repeat(depth):text;}
  class Calculator {
    constructor(options){
      this.o=options;this.model=new window.ExcelLensCalculatorModel.FormulaTable();this.views=new Map();this.pool=[];this.jobs=new Map();this.waiting=new Map();this.running=0;this.editing=null;this.pick=null;this.notes=false;this.noteEdit=null;
      this.host=$('calculatorWindow');this.body=$('calculatorRows');this.scroll=$('calculatorScroll');this.popup=$('calculatorCompletion');this.virtual=new window.ExcelLensVirtualScroll();this.catalog=[];this.suggestions=[];this.suggestion=0;
      $('calculatorOpenBtn').addEventListener('click',()=>this.toggle());$('calculatorClose').addEventListener('click',()=>this.hide());$('calculatorMinimize').addEventListener('click',()=>this.minimize());
      $('calculatorNotes').addEventListener('click',()=>this.toggleNotes());
      for(const id of ['calculatorDock','calculatorHomeDock'])$(id).addEventListener('click',()=>this.open());
      this.body.addEventListener('focusin',event=>{const note=event.target.closest('[data-note-row]');if(note){this.activateNote(Number(note.dataset.noteRow));return;}const input=event.target.closest('[data-formula-row]');if(input){this.activate(Number(input.dataset.formulaRow));return;}if(event.target.closest('[data-result-row]')){if(this.editing!==null)this.commit(this.editing);this.editing=null;this.noteEdit=null;this.pick=null;this.hideCompletion();}});
      this.body.addEventListener('input',event=>{const note=event.target.closest('[data-note-row]');if(note){const index=Number(note.dataset.noteRow);if(this.noteEdit?.index!==index)this.activateNote(index);this.model.setNote(index,note.value);note.title=note.value;this.render();return;}const input=event.target.closest('[data-formula-row]');if(!input)return;const index=Number(input.dataset.formulaRow);this.activate(index);this.edit(index,input.value);this.updateCompletion();});
      this.body.addEventListener('paste',event=>this.paste(event));
      this.scroll.addEventListener('scroll',()=>{this.hideCompletion();this.render();},{passive:true});
      this.scroll.addEventListener('wheel',event=>{if(this.virtual.scale===1||!event.cancelable)return;event.preventDefault();const next=this.virtual.wheel(event,this.scroll.scrollTop,ROW,this.viewport());this.scroll.scrollTop=next.top;this.render();},{passive:false});
      this.popup.addEventListener('pointerdown',event=>{const target=event.target.closest('[data-calc-function]');if(!target)return;event.preventDefault();this.complete(target.dataset.calcFunction);});
      document.addEventListener('pointerdown',event=>{
        if(this.host.hidden||this.editing===null||event.target.closest?.('#calculatorCompletion'))return;
        if(this.picking&&event.target.closest?.('.grid-cell,.column-head[data-col],.row-head,.pinned-row-head,.sheet-tab')&&!event.target.closest?.('.row-resize-grip,.column-resize-grip,.column-filter,.auto-filter-button')){event.preventDefault();return;}
        if(!event.target.closest?.('#calculatorWindow,.grid-region,.sheet-tab')){const index=this.editing;this.editing=null;this.commit(index);this.hideCompletion();}
      },true);
      this.bindDrag();window.addEventListener('resize',()=>{this.hideCompletion();this.render();this.clamp();});
      if(window.ResizeObserver)new ResizeObserver(()=>{this.render();this.clamp();}).observe(this.host);
      this.render();
      if(window.ExcelLensRecovery){
        this.recovery=new window.ExcelLensRecovery.Journal({kind:'calculator',api:(...args)=>this.o.api(...args),
          onRestore:records=>{this.model.restore(records);this.render();if(Object.keys(records).length)this.dock(this.host.hidden);},
          prepareCopy:(records,current)=>{let last=-2;for(const key of current.keys())last=Math.max(last,Number(key));const offset=last+2,result={};for(const [key,value] of Object.entries(records)){const index=Number(key)+offset;if(!Number.isSafeInteger(index)||index>Number.MAX_SAFE_INTEGER-2)throw Error('追加恢复后的行号超出范围');result[String(index)]=value;}return result;},
          onStatus:status=>{const el=$('calculatorSaveStatus');el.textContent=status.error?'恢复未保存':status.loading?'读取恢复副本…':status.dirty?'恢复保存中…':status.count?'已保存':'';el.title=status.error||'公式和备注的恢复副本';el.classList.toggle('has-error',!!status.error);}});
        this.model.onChange=(key,data)=>this.recovery.set(key,data);
        $('calculatorRecovery').addEventListener('click',()=>window.ExcelLensRecovery.show(this.recovery,{title:'恢复计算表',explanation:'载入的公式等待主动计算。其他副本追加到本页末尾，原副本保留。'}));
      }
    }
    source(){return {workbookKey:this.o.state.workbookKey||'',sheet:this.o.state.sheet||''};}
    get picking(){
      if(this.host.hidden||this.editing===null)return false;const row=this.model.get(this.editing),input=this.views.get(this.editing)?.input;if(!input||!formulaContext(row.text,input.selectionStart))return false;
      if(input.selectionStart!==input.selectionEnd)return true;
      if(this.pick?.index===this.editing&&input.selectionStart===this.pick.end&&row.text.slice(this.pick.start,this.pick.end)===this.pick.token)return true;
      return /[=+\-*/^&(<>,:]\s*$/.test(row.text.slice(0,input.selectionStart));
    }
    toggle(){this.host.hidden||this.leaving?this.open():this.hide();}
    dock(value){for(const id of ['calculatorDock','calculatorHomeDock']){$(id).hidden=!value;if(value)window.ExcelLensMotion?.enter($(id),{distance:3,duration:140});}}
    toggleNotes(){
      const index=this.model.active,noteFocused=document.activeElement===this.views.get(index)?.note,r=this.host.getBoundingClientRect();this.notes=!this.notes;
      if(this.notes){this.notesBounds={width:this.host.style.width||r.width+'px',left:r.left,top:r.top};const width=Math.min(window.innerWidth-16,Math.max(580,r.width+180));this.host.style.width=width+'px';this.host.style.left=r.left+r.width-width+'px';}
      else{const old=this.notesBounds;this.host.style.width=old?.width||'440px';if(old){this.host.style.left=old.left+r.left-old.expandedLeft+'px';this.host.style.top=old.top+r.top-old.expandedTop+'px';}}
      this.host.classList.toggle('has-notes',this.notes);$('calculatorNotes').setAttribute('aria-pressed',String(this.notes));this.scroll.setAttribute('aria-colcount',this.notes?'4':'3');this.render();this.clamp();
      if(this.notes){const expanded=this.host.getBoundingClientRect();Object.assign(this.notesBounds,{expandedLeft:expanded.left,expandedTop:expanded.top});}else this.notesBounds=null;
      window.ExcelLensMotion?.enter(this.scroll,{duration:120});if(noteFocused&&!this.notes)this.focus(index);
    }
    open(){this.visibility=(this.visibility||0)+1;this.minimizing=false;this.leaving=false;this.host.hidden=false;this.dock(false);this.loadFunctions();this.render();this.clamp();this.focus(this.model.active);window.ExcelLensMotion?.enter(this.host,{distance:5});}
    leave(minimized){
      if(this.editing!==null)this.commit(this.editing);this.editing=null;this.pick=null;this.hideCompletion();this.minimizing=minimized;this.leaving=true;
      const ticket=this.visibility=(this.visibility||0)+1;this.dock(minimized);
      const finish=()=>{if(ticket!==this.visibility)return;const returnFocus=this.host.contains(document.activeElement);this.host.hidden=true;this.minimizing=false;this.leaving=false;if(returnFocus&&this.o.state.indexReady)this.o.grid.focus({preventScroll:true});};
      if(this.host.animate&&window.ExcelLensMotion?.exit)window.ExcelLensMotion.exit(this.host).then(done=>{if(done)finish();});else finish();
    }
    hide(){this.leave(false);}
    minimize(){this.leave(true);}
    cancelJob(index){const job=this.jobs.get(index);if(!job)return;job.controller.abort();this.jobs.delete(index);this.waiting.delete(index);job.finish();}
    bookChanged(){this.editing=null;this.pick=null;this.hideCompletion();for(const [index,job] of this.jobs)if(job.key&&job.key!==this.o.state.workbookKey){this.cancelJob(index);const row=this.model.ensure(index);row.result=null;row.error='#REF! 工作簿已切换';this.paint(index);}}
    activate(index){if(this.editing===index&&this.model.active===index)return;if(this.editing!==null&&this.editing!==index)this.commit(this.editing);this.noteEdit=null;this.hideCompletion();this.pick=null;this.model.active=index;this.model.fit();this.editing=index;for(const [i,v] of this.views)v.tr.classList.toggle('is-active',i===index);}
    activateNote(index){if(this.editing!==null)this.commit(this.editing);this.editing=null;this.pick=null;this.hideCompletion();if(this.noteEdit?.index!==index)this.noteEdit={index,before:this.model.get(index).note};this.model.active=index;this.model.fit();for(const [i,v] of this.views)v.tr.classList.toggle('is-active',i===index);}
    viewport(){return Math.max(ROW,(this.scroll.clientHeight||HEADER+Math.min(12,this.model.rowCount)*ROW)-HEADER);}
    layout(){this.scroll.style.height=HEADER+Math.min(12,Math.max(5,this.model.rowCount))*ROW+'px';const height=this.virtual.configure(this.model.rowCount*ROW,this.viewport());this.body.style.height=height+'px';}
    focus(index,field='formula'){
      if(index<0||!Number.isSafeInteger(index))return;
      this.model.ensure(index);this.layout();const y=this.virtual.logical(this.scroll.scrollTop||0),top=index*ROW,view=this.viewport();
      if(top<y)this.scroll.scrollTop=this.virtual.physical(top);else if(top+ROW>y+view)this.scroll.scrollTop=this.virtual.physical(top+ROW-view);
      if(field==='note')this.activateNote(index);else this.activate(index);this.render();(field==='note'?this.views.get(index)?.note:this.views.get(index)?.input)?.focus({preventScroll:true});
    }
    edit(index,text){this.cancelJob(index);this.pick=null;try{this.model.edit(index,text,this.source());}catch(error){this.model.ensure(index).error=error.message;}this.render();}
    makeView(){
      const tr=document.createElement('div');tr.className='calculator-row';tr.setAttribute('role','row');
      const number=document.createElement('div');number.className='calculator-row-number';number.setAttribute('role','rowheader');
      const cell=document.createElement('div'),input=document.createElement('input');input.type='text';input.className='calculator-formula';input.maxLength=4096;input.spellcheck=false;input.autocomplete='off';input.setAttribute('aria-autocomplete','list');input.setAttribute('aria-controls','calculatorCompletion');cell.append(input);
      const value=document.createElement('div');value.className='calculator-value';value.tabIndex=0;value.setAttribute('role','gridcell');
      const noteCell=document.createElement('div');noteCell.className='calculator-note-cell';const note=document.createElement('input');note.type='text';note.className='calculator-note';note.maxLength=4096;note.autocomplete='off';noteCell.append(note);
      tr.append(number,cell,value,noteCell);this.body.append(tr);const view={tr,number,input,value,note,index:-1};this.pool.push(view);return view;
    }
    render(){
      this.layout();const y=this.virtual.logical(this.scroll.scrollTop||0),first=Math.max(0,Math.floor(y/ROW)-3),last=Math.min(this.model.rowCount,first+Math.ceil(this.viewport()/ROW)+7),desired=new Set();
      for(let i=first;i<last;i++)desired.add(i);if(this.editing!==null&&document.activeElement===this.views.get(this.editing)?.input)desired.add(this.editing);if(this.noteEdit&&document.activeElement===this.views.get(this.noteEdit.index)?.note)desired.add(this.noteEdit.index);
      const next=new Map();for(const i of desired)if(this.views.has(i))next.set(i,this.views.get(i));
      const used=new Set(next.values()),free=this.pool.filter(v=>!used.has(v));
      for(const i of desired)if(!next.has(i)){const v=free.pop()||this.makeView();v.index=i;v.input.dataset.formulaRow=String(i);v.value.dataset.resultRow=String(i);v.note.dataset.noteRow=String(i);v.input.id='calculatorFormula'+i;v.input.placeholder=i===0?'=':'';v.input.setAttribute('aria-label','第'+(i+1)+'行公式');v.value.setAttribute('aria-label','第'+(i+1)+'行结果');v.note.setAttribute('aria-label','第'+(i+1)+'行备注');v.input.setAttribute('aria-expanded','false');v.tr.setAttribute('aria-rowindex',String(i+1));v.number.textContent=String(i+1);next.set(i,v);}
      this.views=next;const bound=new Set(next.values());for(const v of this.pool)v.tr.hidden=!bound.has(v);
      for(const [i,v] of next){const row=this.model.get(i);v.tr.hidden=false;v.tr.style.top=this.virtual.rowPosition(i*ROW,this.scroll.scrollTop||0)+'px';v.tr.classList.toggle('is-active',i===this.model.active);if(v.input.value!==row.text)v.input.value=row.text;if(v.note.value!==row.note)v.note.value=row.note;v.note.title=row.note;this.paint(i);}
      this.scroll.setAttribute('aria-rowcount',String(this.model.rowCount));
    }
    paint(index){const row=this.model.get(index),v=this.views.get(index);if(!v)return;v.value.textContent=row.error?this.errorCode(row.error):row.result?.display??(this.jobs.has(index)?'…':row.restored?'待计算':'');v.value.title=row.error||row.result?.exact||(row.restored?'按Enter计算恢复的公式':'');v.value.classList.toggle('is-error',!!row.error);v.value.classList.toggle('is-pending',row.restored&&!row.result);v.value.classList.toggle('is-text',row.result?.kind==='text');v.value.classList.toggle('is-boolean',row.result?.kind==='boolean');
      const suggestion=row.result?.suggestions?.[0];
      if(!row.error&&suggestion){const text=document.createElement('span');text.className='calculator-conversion-value';text.textContent=v.value.textContent;v.value.textContent='';const button=document.createElement('button');button.type='button';button.className='calculator-convert';button.textContent=suggestion.label;button.title=suggestion.preview+'；点击在本行公式中明确转换，原数据保持不变';button.onclick=event=>{event.preventDefault();event.stopPropagation();this.applyConversion(index,suggestion)};v.value.append(text,button);}
    }
    applyConversion(index,suggestion){
      const row=this.model.get(index);if(row.result?.suggestions?.[0]!==suggestion)return;
      const text=row.text.trim(),body=text.startsWith('=')?text.slice(1):'"'+text.replace(/"/g,'""')+'"';let formula;
      if(suggestion.kind==='date')formula='=DATEVALUE('+body+')';
      else if(suggestion.kind==='time')formula='=TIMEVALUE('+body+')';
      else if(suggestion.kind==='datetime')formula='=DATEVALUE('+body+')+TIMEVALUE('+body+')';else return;
      if(formula.length>4096){this.o.toast('转换后的公式过长，请拆分本行公式','warning');return;}
      this.edit(index,formula);this.commit(index,true);this.focus(index);
    }
    errorCode(error){return error.match(/#(?:DIV\/0!|REF!|VALUE!|NUM!|NAME\?|N\/A|NULL!|CALC!|SPILL!)/)?.[0]||(/除数|除以0/.test(error)?'#DIV/0!':/引用|工作簿|工作表|索引/.test(error)?'#REF!':'#VALUE!');}
    commit(index,force=false){
      const row=this.model.ensure(index);let text=row.text.trim();
      if(row.restored&&!force)return Promise.resolve();
      row.restored=false;
      if(text.startsWith('=')){const completed=closeFormula(row.text);if(completed!==row.text){this.cancelJob(index);this.model.edit(index,completed,this.source());const input=this.views.get(index)?.input;if(input)input.value=completed;text=completed.trim();}}
      if(!text||!text.startsWith('=')){this.cancelJob(index);const kind=/^[+-]?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?$/.test(text)?'number':'text';row.result=text?{kind,display:row.text,copy:row.text,exact:row.text}:null;row.committed=row.text;row.savedResult=row.result;row.committedRestored=false;this.paint(index);return Promise.resolve();}
      if(!force&&row.text===row.committed&&row.result&&!row.error)return Promise.resolve();
      if(this.jobs.get(index)?.revision===row.revision)return this.jobs.get(index).promise;this.cancelJob(index);
      const source={...(row.source||this.source())},job={index,revision:row.revision,controller:new AbortController(),key:source.workbookKey,body:JSON.stringify({expression:row.text,...source})};
      job.promise=new Promise(resolve=>{job.finish=resolve;});this.jobs.set(index,job);this.waiting.set(index,job);row.result=null;row.error='';this.paint(index);this.pump();return job.promise;
    }
    pump(){while(this.running<4&&this.waiting.size){const [index,job]=this.waiting.entries().next().value;this.waiting.delete(index);if(job.controller.signal.aborted){job.finish();continue;}this.running++;this.run(job).finally(()=>{this.running--;job.finish();this.pump();});}}
    async run(job){
      const {index,revision,controller}=job,row=this.model.ensure(index);
      try{const result=await this.o.api('/api/calculator/formula',{method:'POST',headers:{'Content-Type':'application/json'},body:job.body,signal:controller.signal});if(controller.signal.aborted||row.revision!==revision)return;row.result=result;row.error='';row.committed=row.text;row.savedResult=result;row.committedRestored=false;}
      catch(error){if(!controller.signal.aborted&&row.revision===revision){row.result=null;row.error=error.message;}}
      finally{if(this.jobs.get(index)===job)this.jobs.delete(index);this.paint(index);}
    }
    captureCell(ref,event){if(!this.picking||event?.detail>1)return false;return this.captureRange(ref);}
    captureRange(start,end){
      if(!this.picking)return false;this.hideCompletion();const index=this.editing,row=this.model.ensure(index),input=this.views.get(index)?.input,current=this.source();if(!input)return false;
      if(!this.o.state.indexReady){row.error='#REF! 工作表索引尚未完成';this.paint(index);return true;}
      if(row.source?.workbookKey&&row.source.workbookKey!==current.workbookKey){row.error='#REF! 工作簿已切换';this.paint(index);return true;}if(!row.source?.workbookKey)row.source=current;
      const token=(current.sheet===row.source.sheet?'':quote(current.sheet))+start+(end&&end!==start?':'+end:'');let a=input.selectionStart??row.text.length,b=input.selectionEnd??a;
      const equal=row.text.indexOf('=');if(a<=equal&&b>equal)a=equal+1;
      if(this.pick?.index===index&&a===b&&a===this.pick.end&&row.text.slice(this.pick.start,this.pick.end)===this.pick.token){a=this.pick.start;b=this.pick.end;}
      const updated=row.text.slice(0,a)+token+row.text.slice(b);input.focus({preventScroll:true});input.setSelectionRange(a,b);try{document.execCommand?.('insertText',false,token);}catch{}
      this.edit(index,updated);if(input.value!==row.text)input.value=row.text;const caret=a+token.length;input.setSelectionRange(caret,caret);this.pick={index,start:a,end:caret,token};return true;
    }
    paste(event){
      const note=event.target.closest('[data-note-row]');
      const input=event.target.closest('[data-formula-row]');if(!note&&!input)return;
      const text=event.clipboardData?.getData('text/plain')||'';if(!/[\t\r\n]/.test(text))return;
      event.preventDefault();let lines;
      try{lines=window.ExcelLensClipboard.parse(text,{maxCols:1,maxCells:2000,maxCellLength:4096}).map(row=>row[0]);}
      catch(error){this.o.toast(error.message,'warning');return;}
      const start=Number(note?note.dataset.noteRow:input.dataset.formulaRow);
      if(note){for(let offset=0;offset<lines.length;offset++)this.model.setNote(start+offset,lines[offset]);this.render();this.focus(start+lines.length,'note');return;}
      this.hideCompletion();
      for(let offset=0;offset<lines.length;offset++){const i=start+offset;this.cancelJob(i);try{this.model.edit(i,lines[offset],this.source());this.commit(i);}catch(error){this.model.ensure(i).error=error.message;}}
      this.render();this.focus(start+lines.length);
    }
    loadFunctions(){if(!this.catalogPromise)this.catalogPromise=Promise.resolve().then(()=>this.o.api('/api/calculator/functions')).then(data=>{this.catalog=data.items||[];}).catch(()=>{this.catalogPromise=null;});return this.catalogPromise;}
    async updateCompletion(){
      const index=this.editing,input=this.views.get(index)?.input;if(!input||this.host.hidden)return this.hideCompletion();const context=formulaContext(input.value,input.selectionStart);if(!context||!context.prefix&&!context.fn)return this.hideCompletion();
      await this.loadFunctions();if(this.editing!==index||document.activeElement!==input)return;
      const now=formulaContext(input.value,input.selectionStart);if(!now||now.prefix!==context.prefix||now.end!==context.end||now.fn!==context.fn)return;
      this.completionContext=context;this.suggestions=context.prefix?this.catalog.filter(fn=>fn.name.startsWith(context.prefix.toUpperCase())).slice(0,8):[];this.suggestion=0;
      if(!this.suggestions.length){const fn=this.catalog.find(fn=>fn.name===context.fn);if(!fn)return this.hideCompletion();const hint=document.createElement('div');hint.className='calculator-signature';hint.textContent=fn.signature;this.popup.replaceChildren(hint);this.completionMode='hint';}
      else{this.completionMode='list';this.renderSuggestions();}
      const entering=this.popup.hidden;this.popup.hidden=false;input.setAttribute('aria-expanded',String(this.completionMode==='list'));this.placeCompletion(input);if(entering)window.ExcelLensMotion?.enter(this.popup,{distance:3,duration:130});
    }
    renderSuggestions(){const nodes=this.suggestions.map((fn,i)=>{const button=document.createElement('button');button.type='button';button.dataset.calcFunction=fn.name;button.id='calculatorSuggest'+i;button.setAttribute('role','option');button.setAttribute('aria-selected',String(i===this.suggestion));button.className=i===this.suggestion?'is-active':'';button.textContent=fn.name;const signature=document.createElement('span');signature.textContent=fn.signature.slice(fn.name.length);button.append(signature);return button;});this.popup.replaceChildren(...nodes);this.views.get(this.editing)?.input.setAttribute('aria-activedescendant','calculatorSuggest'+this.suggestion);}
    placeCompletion(input){const r=input.getBoundingClientRect(),height=this.completionMode==='list'?Math.min(174,this.suggestions.length*28):30,width=Math.min(340,Math.max(220,r.width||260));this.popup.style.width=width+'px';this.popup.style.left=Math.max(8,Math.min(r.left,window.innerWidth-width-8))+'px';this.popup.style.top=(r.bottom+height+2<window.innerHeight?r.bottom+2:Math.max(8,r.top-height-2))+'px';}
    hideCompletion(){this.popup.hidden=true;this.completionMode='';this.views.get(this.editing)?.input.setAttribute('aria-expanded','false');this.views.get(this.editing)?.input.removeAttribute('aria-activedescendant');}
    complete(name){const index=this.editing,input=this.views.get(index)?.input;if(!input)return;const context=formulaContext(input.value,input.selectionStart);if(!context)return;const suffix=input.value.slice(context.end),insert=name+(suffix.startsWith('(')?'':'('),text=input.value.slice(0,context.start)+insert+suffix;this.edit(index,text);input.value=text;const caret=context.start+insert.length+(suffix.startsWith('(')?1:0);input.focus({preventScroll:true});input.setSelectionRange(caret,caret);this.hideCompletion();this.updateCompletion();}
    handleKey(event){
      if(this.host.hidden)return false;const inside=event.target?.closest?.('#calculatorWindow');
      if(!inside){if(!this.picking||!event.target?.closest?.('.grid-region'))return false;if(event.key==='Enter'){event.preventDefault();this.finish(false);return true;}if(event.key==='Escape'){event.preventDefault();this.cancel();return true;}return false;}
      const input=event.target.closest?.('[data-formula-row]');
      const note=event.target.closest?.('[data-note-row]');
      if(note&&!event.isComposing){const index=Number(note.dataset.noteRow);if(event.key==='Enter'||event.key==='ArrowDown'||event.key==='ArrowUp'||event.key==='Tab'){event.preventDefault();const direction=event.shiftKey||event.key==='ArrowUp'?-1:1;this.focus(index+direction,event.key==='Tab'?'formula':'note');}else if(event.key==='Escape'&&this.noteEdit){event.preventDefault();this.model.setNote(index,this.noteEdit.before);this.noteEdit=null;this.render();note.select();}return true;}
      if(input&&!event.isComposing){
        if(!this.popup.hidden&&this.completionMode==='list'&&['ArrowDown','ArrowUp','Enter','Tab'].includes(event.key)){event.preventDefault();if(event.key==='ArrowDown'||event.key==='ArrowUp'){this.suggestion=(this.suggestion+(event.key==='ArrowDown'?1:-1)+this.suggestions.length)%this.suggestions.length;this.renderSuggestions();}else this.complete(this.suggestions[this.suggestion].name);return true;}
        if(event.key==='Enter'||event.key==='Tab'){event.preventDefault();this.finish(event.shiftKey);}
        else if(event.key==='Escape'){event.preventDefault();if(!this.popup.hidden)this.hideCompletion();else this.cancel();}
        else if((event.key==='ArrowUp'||event.key==='ArrowDown')&&!event.altKey&&!event.ctrlKey&&!event.metaKey){event.preventDefault();this.commit(this.model.active);this.focus(this.model.active+(event.key==='ArrowUp'?-1:1));}
      }
      const output=event.target.closest?.('[data-result-row]');if(output&&(event.ctrlKey||event.metaKey)&&event.key.toLowerCase()==='c'){const row=this.model.get(Number(output.dataset.resultRow));if(row.result){event.preventDefault();Promise.resolve(this.o.copy(row.result.copy)).catch(error=>this.o.toast(error.message,'error'));}}
      return true;
    }
    finish(reverse){this.hideCompletion();const index=this.editing??this.model.active;this.commit(index,true);this.editing=null;this.pick=null;this.focus(index+(reverse?-1:1));}
    cancel(){const index=this.editing??this.model.active;this.cancelJob(index);this.model.cancel(index);this.editing=null;this.pick=null;this.hideCompletion();this.render();this.views.get(index)?.input.select();}
    clamp(){if(this.host.hidden)return;const r=this.host.getBoundingClientRect(),left=Math.max(8,Math.min(r.left,window.innerWidth-r.width-8)),top=Math.max(8,Math.min(r.top,window.innerHeight-r.height-8));if(this.host.style.left!==left+'px')this.host.style.left=left+'px';if(this.host.style.top!==top+'px')this.host.style.top=top+'px';this.host.style.right=this.host.style.bottom='auto';}
    bindDrag(){const handle=$('calculatorDrag');let drag=null,frame=0,pending;const move=()=>{frame=0;if(!drag||!pending)return;this.host.style.left=drag.left+pending.x-drag.x+'px';this.host.style.top=drag.top+pending.y-drag.y+'px';this.clamp();};handle.addEventListener('pointerdown',event=>{if(event.button!==0||event.target.closest('button'))return;this.hideCompletion();const r=this.host.getBoundingClientRect();drag={id:event.pointerId,x:event.clientX,y:event.clientY,left:r.left,top:r.top};handle.setPointerCapture(event.pointerId);event.preventDefault();});handle.addEventListener('pointermove',event=>{if(!drag||event.pointerId!==drag.id)return;pending={x:event.clientX,y:event.clientY};if(!frame)frame=requestAnimationFrame(move);});const end=event=>{if(drag&&event.pointerId===drag.id){if(frame){cancelAnimationFrame(frame);move();}drag=null;pending=null;}};handle.addEventListener('pointerup',end);handle.addEventListener('pointercancel',end);}
  }
  window.ExcelLensCalculator=Calculator;
  window.ExcelLensFormulaInput={context:formulaContext,close:closeFormula};
})();
