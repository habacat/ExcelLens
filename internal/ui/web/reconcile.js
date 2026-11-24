(() => {
  'use strict';
  const $=id=>document.getElementById(id);
  const sides=['left','right'];
  const labels={equal:'一致',within_tolerance:'容差内',different:'差异',left_only:'仅左侧',right_only:'仅右侧',duplicate:'重复主键',review:'需核对',invalid_key:'无效主键'};
  const countLabels={equal:'一致主键',withinTolerance:'容差内主键',different:'差异主键',leftOnly:'仅左侧主键',rightOnly:'仅右侧主键',duplicate:'重复主键',review:'需核对主键',invalidKeys:'无效主键记录'};
  function rebindRecord(record,targets){
    if(record?.kind!=='ExcelLens.Reconcile'||record.version!==1||![1,2].includes(record.ruleVersion)||record.scope!=='all-data-rows')throw Error('核查记录或规则版本不受支持');
    const {query,inputs,counts,detail}=record,filter=detail?.status;
    const sha=value=>typeof value==='string'&&/^[0-9a-f]{64}$/.test(value);
    if(!Array.isArray(inputs)||inputs.length!==2||!inputs.every(input=>sha(input?.stamp)&&(input.kind==='analysis'||input.kind==='workbook'&&sha(input.fileSHA256)))||
        !sha(record.exceptionGroupsSHA256)||!sha(detail?.sha256)||!Array.isArray(query?.keys)||!query.keys.length||query.keys.length>4||
        !(query.fields===null||Array.isArray(query.fields)&&query.fields.length<=8)||typeof filter!=='string'||(filter!==''&&!Object.hasOwn(labels,filter))||
        (query.includeEqual!==undefined&&typeof query.includeEqual!=='boolean')||typeof detail.protectCSV!=='boolean'||
        !Object.keys(countLabels).every(key=>(key==="withinTolerance"&&record.ruleVersion===1&&counts?.[key]===undefined)||(Number.isSafeInteger(counts?.[key])&&counts[key]>=0)))throw Error('核查记录的字段、条件或计数无效');
    const bindColumn=(side,column)=>{
      const input=inputs[side],target=targets[side];
      if(!Number.isSafeInteger(column)||column<1||!Array.isArray(input?.fields)||typeof input.name!=='string'||!input.stamp)throw Error('核查记录的来源字段无效');
      const saved=input.fields.find(field=>field.column===column);
      if(!saved)throw Error('核查记录缺少已用字段');
      if(input.kind===target.kind&&input.stamp===target.stamp&&target.fields.some(field=>field.column===column))return column;
      const matches=saved.uniqueHeader&&saved.headerHash?target.fields.filter(field=>field.uniqueHeader&&field.headerHash===saved.headerHash):[];
      if(matches.length!==1)throw Error(`${side?'右':'左'}侧「${saved.name}」无法按唯一表头对应，请核对来源和表头`);
      return matches[0].column;
    };
    const bindPair=(pair,isField)=>{
      const mode=pair?.mode??'';
      if(!['','text','number'].includes(mode)||[pair?.trim,pair?.ignoreCase].some(value=>value!==undefined&&typeof value!=='boolean'))throw Error('核对方式无效');
      const mapping={left:bindColumn(0,pair.left),right:bindColumn(1,pair.right),mode,trim:!!pair.trim,ignoreCase:!!pair.ignoreCase};
      if(pair.tolerance!==undefined){
        if(!isField||mode!=='number'||record.ruleVersion===1||typeof pair.tolerance!=='string'||!decimalTolerance(pair.tolerance))throw Error('核查记录包含无效金额容差');
        mapping.tolerance=pair.tolerance;
      }
      return mapping;
    };
    const bindPairs=(pairs,isField)=>{
      const mapped=pairs.map(pair=>bindPair(pair,isField));
      if(sides.some(side=>new Set(mapped.map(pair=>pair[side])).size!==mapped.length))throw Error('核查记录包含重复映射');
      return mapped;
    };
    return {keys:bindPairs(query.keys),fields:bindPairs(query.fields||[],true),includeEqual:!!query.includeEqual};
  }
  function decimalTolerance(text){
    return /^(?:\d+(?:\.\d*)?|\.\d+)$/.test(text);
  }
  function suggest(left,right){
    const unique=fields=>{const map=new Map();for(const field of fields){if(!field.headerLoaded||field.truncated||!field.rawName||!field.headerHash)continue;const key=field.headerHash;map.set(key,map.has(key)?null:field);}return map;};
    const a=unique(left),b=unique(right),pairs=[];
    for(const field of left){const key=field.headerHash;if(key&&a.get(key)===field&&b.get(key))pairs.push({left:field.column,right:b.get(key).column,mode:''});}
    return {keys:pairs.slice(0,1),fields:pairs.slice(1,9)};
  }
  function resolveColumn(raw,fields){
    raw=String(raw).trim();let column=0;
    if(/^\d+$/.test(raw))column=Number(raw);
    else if(/^[A-Za-z]{1,5}$/.test(raw)){column=window.ExcelLensAddress.columnNumber(raw);}
    const exists=fields.some(field=>field.column===column);
    const named=fields.filter(field=>field.headerLoaded&&!field.truncated&&(field.rawName===raw||field.name===raw));
    if(exists&&named.some(field=>field.column!==column)&&!/^\d+$/.test(raw))throw Error('字段名与列标识有歧义，请输入列序号');
    if(exists)return column;
    if(named.length===1)return named[0].column;
    throw Error(named.length>1?'字段名重复，请输入列字母或序号':`字段「${raw}」无效`);
  }
  class Reconcile {
    constructor({state,api,toast,colName,navigate,canRun=()=>true,recordBytes}){
      Object.assign(this,{s:state,api,toast,colName,navigate,canRun,recordBytes});
      this.dialog=$('reconcileDialog');this.epoch=0;this.books=[];this.primaryBook='';this.result=null;this.job='';this.busy=false;this.pendingUpload=null;this.catalogController=null;this.catalogSerial=0;this.mapped=false;this.configChanged=false;
      this.sides={left:{serial:0,info:null,controller:null},right:{serial:0,info:null,controller:null}};
      this.jobs=new window.ExcelLensAnalysisJobs({api,identity:()=>this.s.workbookKey,sleep:ms=>new Promise(r=>setTimeout(r,ms)),endpoint:'/api/reconcile',label:'核对',maxPolls:0,
        resultFromStatus:status=>({...status.result,recordError:status.recordError,exceptionGroupsSHA256:status.exceptionGroupsSHA256}),
        onBusy:value=>{this.busy=value;this.controls();},onProgress:status=>this.progress(`${status.stage||'核对中'}${status.total?'　'+Number(status.done||0).toLocaleString()+' / '+Number(status.total).toLocaleString()+' 条':''}`)});
      this.bind();
    }
    node(side,name){return $('reconcile'+(side==='left'?'Left':'Right')+name);}
    post(path,value,options={}){return this.api(path,{method:'POST',body:JSON.stringify(value),...options});}
    bind(){
      $('reconcileOpenBtn').addEventListener('click',()=>this.open());
      $('reconcileRefresh').addEventListener('click',()=>this.refresh());
      $('reconcileAddFile').addEventListener('click',()=>{if(!this.busy)$('reconcileFile').click();});
      $('reconcileFile').addEventListener('change',event=>{this.upload(event.target.files?.[0]).finally(()=>event.target.value='');});
      $('reconcileSwap').addEventListener('click',()=>this.swap());
      $('reconcileLoadFields').addEventListener('click',()=>this.loadFields());
      $('reconcileMatchFields').addEventListener('click',()=>this.suggestMappings());
      $('reconcileAddKey').addEventListener('click',()=>this.addMapping('keys'));
      $('reconcileAddField').addEventListener('click',()=>this.addMapping('fields'));
      $('reconcileRun').addEventListener('click',()=>this.run());
      $('reconcileCancel').addEventListener('click',()=>{this.jobs.cancelMain();this.progress('已请求取消核对');});
      $('reconcileIncludeEqual').addEventListener('change',()=>this.changed());
      $('reconcileResultFilter').addEventListener('change',()=>this.renderSamples());
      $('reconcileClear').addEventListener('click',()=>this.clear());
      $('reconcileExport').addEventListener('click',()=>this.export());
      $('batchCompare').addEventListener('click',()=>this.compareBatches());
      $('versionCompare').addEventListener('click',()=>this.compareVersions());
      $('reconcileBundle').addEventListener('click',()=>this.export(true));
      $('reconcileImport').addEventListener('click',()=>$('reconcileRecordFile').click());
      $('reconcileRecordFile').addEventListener('change',event=>{
        const file=event.target.files?.[0];event.target.value='';
        if(file)this.loadRecord(file);
      });
      for(const side of sides){
        this.node(side,'Book').addEventListener('change',()=>{this.invalidate(side);this.fillSheets(side);this.loadScratchChoices(this.node(side,'Book').value);});
        this.node(side,'Sheet').addEventListener('change',()=>{this.invalidate(side);const scratch=this.node(side,'Sheet').value.startsWith('scratch:');this.node(side,'Header').disabled=scratch;this.node(side,'Header').value=scratch?'0':'1';});
        this.node(side,'Header').addEventListener('change',()=>this.invalidate(side));
        this.node(side,'Remove').addEventListener('click',()=>this.removeSource(side));
      }
    }
    resetBook(){
      this.baseline=null;this.resultBaseline=null;
      ++this.epoch;this.jobs.reset();this.catalogController?.abort();this.pendingUpload?.abort();this.pendingUpload=null;
      for(const side of sides){this.sides[side].controller?.abort();this.sides[side]={serial:0,info:null,controller:null};this.node(side,'Book').replaceChildren();this.node(side,'Sheet').replaceChildren();this.node(side,'Info').textContent='选择来源后读取字段';}
      this.books=[];this.primaryBook='';this.result=null;this.job='';this.mapped=false;this.configChanged=false;
      $('reconcileKeys').replaceChildren();$('reconcileFields').replaceChildren();$('reconcileResults').hidden=true;$('reconcileProgress').hidden=true;
      if(this.dialog.open)this.dialog.close();this.controls();
    }
    async open(){
      if(!this.s.opened){this.toast('请先打开一个数据文件','warning');return;}
      if(!this.dialog.open)this.dialog.showModal();
      if(!this.primaryBook)await this.refresh();
    }
    async refresh(focus=''){
      if(this.busy)return;
      const epoch=this.epoch,serial=++this.catalogSerial;this.catalogController?.abort();const controller=new AbortController();this.catalogController=controller;
      try{
        $('reconcileSourceNotice').textContent='正在读取来源…';
        const data=await this.api('/api/reconcile/sources'+(focus?'?book='+encodeURIComponent(focus):''),{signal:controller.signal});
        if(epoch!==this.epoch||serial!==this.catalogSerial)return;
        const previous=new Map(this.books.map(book=>[book.id,book]));
        this.books=(data.books||[]).map(book=>{const old=previous.get(book.id);return !book.scratchLoaded&&old?.scratchLoaded?{...book,scratch:old.scratch,scratchLoaded:true}:book;});this.primaryBook=data.currentBook;
        for(const side of sides){
          const node=this.node(side,'Book'),selected=node.value,sheet=this.node(side,'Sheet').value;
          node.replaceChildren();for(const book of this.books){const option=new Option(book.name,book.id);node.append(option);}
          node.value=this.books.some(book=>book.id===selected)?selected:(side==='left'?this.primaryBook:(this.books.find(book=>book.id!==this.primaryBook)?.id||this.primaryBook));
          this.fillSheets(side,sheet);
          if(selected&&(selected!==node.value||sheet!==this.node(side,'Sheet').value))this.invalidate(side);
        }
        $('reconcileSourceNotice').textContent='';
        this.controls();
      }catch(error){if(epoch===this.epoch&&serial===this.catalogSerial&&error.name!=='AbortError'){$('reconcileSourceNotice').textContent=error.message;this.toast(error.message,'error');}}
      finally{if(this.catalogController===controller)this.catalogController=null;}
    }
    async loadScratchChoices(bookID){const book=this.books.find(book=>book.id===bookID);if(book&&!book.scratchLoaded)await this.refresh(bookID);}
    fillSheets(side,previous=''){
      const book=this.books.find(book=>book.id===this.node(side,'Book').value),node=this.node(side,'Sheet');
      const selected=previous||node.value;node.replaceChildren();
      for(const sheet of book?.sheets||[])node.append(new Option(sheet.name,'wb:'+sheet.name));
      for(const sheet of book?.scratch||[])node.append(new Option(sheet.name+'（编辑表，已保存）','scratch:'+sheet.id));
      if([...node.options].some(option=>option.value===selected))node.value=selected;
      const scratch=node.value.startsWith('scratch:');this.node(side,'Header').disabled=scratch||this.busy;if(scratch)this.node(side,'Header').value='0';
      this.node(side,'Remove').hidden=!book?.attached;this.controls();
    }
    invalidate(side){this.sides[side].controller?.abort();this.sides[side].serial++;this.sides[side].info=null;this.mapped=false;this.node(side,'Info').textContent='字段需重新读取';this.changed();this.controls();}
    changed(){this.recordIntent=(this.recordIntent||0)+1;if(this.result){this.configChanged=true;this.renderSource();}}
    async loadRecord(file){
      if(!this.ready()||this.busy)return;
      const targets=sides.map(side=>this.sides[side].info),epoch=this.epoch,intent=this.recordIntent=(this.recordIntent||0)+1;
      const current=()=>epoch===this.epoch&&intent===this.recordIntent&&!this.busy&&sides.every((side,i)=>this.sides[side].info===targets[i]);
      try{
        if(file.size>this.recordBytes)throw Error('核查记录超过容量');
        const record=JSON.parse(await file.text());
        if(!current())return;
        const query=rebindRecord(record,targets);
        for(const kind of ['keys','fields']){
          $(kind==='keys'?'reconcileKeys':'reconcileFields').replaceChildren();
          for(const pair of query[kind])this.addMapping(kind,pair);
        }
        $('reconcileIncludeEqual').checked=query.includeEqual;
        this.mapped=true;this.baseline={record,targets,query};this.changed();
        this.progress('已载入核查规则，核对字段后点击“开始核对”');
        this.controls();
      }catch(error){if(current())this.progress(error.message,true);}
    }
    ref(side){
      const source=this.node(side,'Sheet').value,header=source.startsWith('scratch:')?0:Number(this.node(side,'Header').value);
      if(!Number.isInteger(header)||header<0)throw Error('表头行号无效，0表示无表头');
      return {book:this.node(side,'Book').value,source,headerRow:header,...(this.sides[side].info?{stamp:this.sides[side].info.stamp}:{})};
    }
    async loadFields(){if(this.busy)return;await Promise.all(sides.map(side=>this.loadSide(side)));}
    async loadSide(side){
      const state=this.sides[side],serial=++state.serial,epoch=this.epoch;state.controller?.abort();const controller=new AbortController();state.controller=controller;state.info=null;this.mapped=false;this.controls();
      try{
        const ref=this.ref(side);if(!ref.book||!ref.source)throw Error('请先选择文件和工作表');
        this.node(side,'Info').textContent='正在读取字段…';
        const info=await this.post('/api/reconcile/fields',ref,{signal:controller.signal});
        if(epoch!==this.epoch||serial!==this.sides[side].serial)return;
        state.info=info;this.node(side,'Info').textContent=`${Number(info.total).toLocaleString()} 条数据，${info.fields.length.toLocaleString()} 列`+(info.notice?'；'+info.notice:'');
        this.options(side,info);
        if(this.ready()&&!this.mapped)this.suggestMappings();
      }catch(error){if(epoch===this.epoch&&serial===this.sides[side].serial&&error.name!=='AbortError')this.node(side,'Info').textContent=error.message;}
      finally{if(state.controller===controller)state.controller=null;this.controls();}
    }
    ready(){return sides.every(side=>!!this.sides[side].info);}
    options(side,info){const list=$(side==='left'?'reconcileLeftOptions':'reconcileRightOptions');list.replaceChildren();for(const field of info?.fields.slice(0,512)||[]){const option=document.createElement('option');option.value=this.colName(field.column);option.label=field.name+(field.truncated?'（字段名截取）':'');list.append(option);}}
    suggestMappings(){
      if(!this.ready())return;
      const pairs=suggest(this.sides.left.info.fields,this.sides.right.info.fields);$('reconcileKeys').replaceChildren();$('reconcileFields').replaceChildren();
      for(const pair of pairs.keys.length?pairs.keys:[{}])this.addMapping('keys',pair);
      for(const pair of pairs.fields)this.addMapping('fields',pair);
      this.mapped=true;this.changed();this.controls();
      $('reconcileSourceNotice').textContent=pairs.keys.length?'已按唯一同名字段填入，请核对主键及核对方式。':'没有可唯一匹配的字段，请输入列字母、序号或字段名。';
    }
    addMapping(kind,pair={}){
      const holder=$(kind==='keys'?'reconcileKeys':'reconcileFields'),limit=kind==='keys'?4:8;
      if(holder.children.length>=limit){this.toast(`最多${limit}组${kind==='keys'?'主键':'比较字段'}`,'warning');return;}
      const row=document.createElement('div');row.className='reconcile-mapping';
      for(const side of sides){const input=document.createElement('input');input.type='text';input.className='field-control reconcile-column-'+side;input.setAttribute('list',side==='left'?'reconcileLeftOptions':'reconcileRightOptions');input.setAttribute('aria-label',(side==='left'?'左侧':'右侧')+(kind==='keys'?'主键字段':'比较字段'));input.placeholder=side==='left'?'左侧列或字段名':'右侧列或字段名';input.value=pair[side]?this.colName(pair[side]):'';input.addEventListener('input',()=>this.changed());row.append(input);}
      const mode=document.createElement('select');mode.className='field-control reconcile-mode';mode.setAttribute('aria-label','核对方式');for(const [value,label]of [['','保留源类型'],['text','按显示文本'],['number','按精确数值']])mode.append(new Option(label,value));mode.value=pair.mode||'';row.append(mode);
      for(const [key,label]of [['trim','去首尾空格'],['ignoreCase','忽略大小写']]){const wrap=document.createElement('label'),input=document.createElement('input');input.type='checkbox';input.className='reconcile-'+key;input.checked=!!pair[key];input.addEventListener('change',()=>this.changed());wrap.append(input,document.createTextNode(label));row.append(wrap);}
      let threshold=null;
      if(kind==='fields'){
        threshold=document.createElement('label');
        threshold.className='reconcile-threshold';
        const input=document.createElement('input');
        input.className='field-control reconcile-tolerance';
        input.type='text';
        input.inputMode='decimal';
        input.placeholder='0';
        input.value=pair.tolerance||'';
        input.addEventListener('input',()=>this.changed());
        threshold.append(document.createTextNode('绝对容差'),input);
        row.append(threshold);
      }
      const updateMode=()=>{
        const numeric=mode.value==='number';
        for(const checkbox of row.querySelectorAll('input[type=checkbox]')){
          checkbox.parentElement.hidden=numeric;
          checkbox.disabled=numeric||this.busy;
          if(numeric)checkbox.checked=false;
        }
        if(threshold)threshold.hidden=!numeric;
      };
      updateMode();
      mode.addEventListener('change',()=>{updateMode();this.changed();});
      const remove=document.createElement('button');remove.type='button';remove.textContent='×';remove.title='移除此组字段';remove.addEventListener('click',()=>{if(kind==='keys'&&holder.children.length===1){this.toast('至少保留一组主键','warning');return;}row.remove();this.changed();this.controls();});row.append(remove);holder.append(row);this.controls();
    }
    mappings(kind){
      const rows=[...$(kind==='keys'?'reconcileKeys':'reconcileFields').children],seenLeft=new Set(),seenRight=new Set();
      return rows.map(row=>{const left=resolveColumn(row.querySelector('.reconcile-column-left').value,this.sides.left.info.fields),right=resolveColumn(row.querySelector('.reconcile-column-right').value,this.sides.right.info.fields);
        if(seenLeft.has(left)||seenRight.has(right))throw Error('主键或比较字段重复，请移除重复项');seenLeft.add(left);seenRight.add(right);
        const pair={left,right,mode:row.querySelector('.reconcile-mode').value,trim:row.querySelector('.reconcile-trim').checked,ignoreCase:row.querySelector('.reconcile-ignoreCase').checked};
        if(kind==='fields'&&pair.mode==='number'){
          const text=row.querySelector('.reconcile-tolerance').value.trim();
          if(text&&!decimalTolerance(text))throw Error('金额容差须填写非负十进制数');
          if(text)pair.tolerance=text;
        }
        return pair;});
    }
    controls(){
      if(!this.dialog)return;
      const uploading=!!this.pendingUpload,locked=this.busy||uploading,ready=this.ready();
      for(const id of ['reconcileRefresh','reconcileAddFile','reconcileSwap','reconcileLoadFields','reconcileIncludeEqual'])$(id).disabled=locked;
      $('reconcileMatchFields').disabled=locked||!ready;$('reconcileRun').disabled=locked||!ready;
      $('reconcileImport').disabled=locked||!ready;$('reconcileBundle').disabled=locked||!this.job||!!this.result?.recordError;
      $('reconcileCancel').hidden=!this.busy;$('reconcileClear').disabled=locked;$('reconcileExport').disabled=locked||!this.job;
      for(const side of sides){this.node(side,'Book').disabled=locked;this.node(side,'Sheet').disabled=locked;this.node(side,'Remove').disabled=locked;this.node(side,'Header').disabled=locked||this.node(side,'Sheet').value.startsWith('scratch:');}
      for(const kind of ['keys','fields']){const holder=$(kind==='keys'?'reconcileKeys':'reconcileFields');for(const node of holder.querySelectorAll('input,select,button'))node.disabled=locked;for(const row of holder.children)if(row.querySelector('.reconcile-mode').value==='number')for(const node of row.querySelectorAll('input[type=checkbox]'))node.disabled=true;$(kind==='keys'?'reconcileAddKey':'reconcileAddField').disabled=locked||!ready||holder.children.length>=(kind==='keys'?4:8);}
    }
    progress(text,error=false){$('reconcileProgress').hidden=false;$('reconcileProgress').classList.toggle('is-error',error);$('reconcileProgress').textContent=text;}
    async run(){
      if(this.busy||!this.ready()||!this.canRun('核对'))return;
      let request;try{request={workbookKey:this.s.workbookKey,primaryBook:this.primaryBook,left:this.ref('left'),right:this.ref('right'),query:{keys:this.mappings('keys'),fields:this.mappings('fields'),includeEqual:$('reconcileIncludeEqual').checked}};if(!request.query.keys.length)throw Error('请选择主键');}catch(error){this.toast(error.message,'warning');return;}
      const epoch=this.epoch;this.progress('正在开始核对…');this.changed();
      const base=this.baseline;
      const comparable=base&&sides.every((side,i)=>this.sides[side].info===base.targets[i])&&JSON.stringify(request.query)===JSON.stringify(base.query);
      if(comparable)request.query.ruleVersion=base.record.ruleVersion;
      try{const completed=await this.jobs.execute(request,{main:true});if(!completed.current()||epoch!==this.epoch)return;
        this.resultBaseline=comparable?base.record:null;
        if(comparable){$('reconcileResultFilter').value=base.record.detail.status;$('reconcileProtectCSV').checked=base.record.detail.protectCSV;}
        const previous=this.job;this.job=completed.job;this.result=completed.result;this.configChanged=false;$('reconcileResults').hidden=false;this.progress('核对完成');this.renderResult();if(previous&&previous!==this.job)this.post('/api/reconcile/clear',{id:previous}).catch(()=>{});
      }catch(error){if(epoch===this.epoch&&error.name!=='AbortError')this.progress(error.message,true);}
      finally{this.controls();}
    }
    async reopenJob(job,status){
      await this.open();
      this.job=job;this.result={...status.result,recordError:status.recordError,exceptionGroupsSHA256:status.exceptionGroupsSHA256};
      this.resultBaseline=null;this.configChanged=false;$('reconcileResults').hidden=false;
      this.progress('核对完成');this.renderResult();this.controls();
    }
    async upload(file){
      if(!file||this.busy||this.pendingUpload)return;const epoch=this.epoch,controller=new AbortController();this.pendingUpload=controller;this.controls();
      try{const form=new FormData();form.append('file',file,file.name);form.append('reconcileSource','1');form.append('workbookKey',this.s.workbookKey);form.append('primaryBook',this.primaryBook);$('reconcileSourceNotice').textContent='正在添加核对文件…';
        const result=await this.api('/api/open',{method:'POST',body:form,signal:controller.signal});if(epoch!==this.epoch)return;
        await this.refresh(result.book);if(epoch!==this.epoch)return;this.node('right','Book').value=result.book;this.fillSheets('right');this.invalidate('right');$('reconcileSourceNotice').textContent='已添加 '+result.name+'，请读取字段。';
      }catch(error){if(epoch===this.epoch&&error.name!=='AbortError'){$('reconcileSourceNotice').textContent=error.message;this.toast(error.message,'error');}}
      finally{if(this.pendingUpload===controller)this.pendingUpload=null;this.controls();}
    }
    async removeSource(side){
      if(this.busy)return;const id=this.node(side,'Book').value;try{await this.post('/api/reconcile/source/remove',{book:id});this.invalidate(side);await this.refresh();}catch(error){this.toast(error.message,'error');}
    }
    swap(){
      if(this.busy)return;let pairs;try{pairs=this.ready()?{keys:this.mappings('keys'),fields:this.mappings('fields')}:null;}catch(error){this.toast(error.message,'warning');return;}
      const values=sides.map(side=>({book:this.node(side,'Book').value,sheet:this.node(side,'Sheet').value,header:this.node(side,'Header').value,info:this.sides[side].info}));
      for(let i=0;i<2;i++){const side=sides[i],value=values[1-i];this.sides[side].controller?.abort();this.sides[side].serial++;this.sides[side].info=value.info;this.node(side,'Book').value=value.book;this.fillSheets(side,value.sheet);this.node(side,'Header').value=value.header;this.node(side,'Info').textContent=value.info?`${value.info.total.toLocaleString()} 条数据，${value.info.fields.length} 列`:'字段需重新读取';this.options(side,value.info);}
      if(pairs){$('reconcileKeys').replaceChildren();$('reconcileFields').replaceChildren();for(const kind of ['keys','fields'])for(const pair of pairs[kind])this.addMapping(kind,{...pair,left:pair.right,right:pair.left});}this.changed();this.controls();
    }
    renderSource(){
      if(!this.result)return;
      const r=this.result,parts=[`左：${r.left.name} / ${r.left.sheet}　右：${r.right.name} / ${r.right.sheet}`];
      if(this.resultBaseline){
        const base=this.resultBaseline;
        parts.push('基线：'+base.inputs.map(input=>input.name+' / '+input.sheet).join('、'));
        if(r.exceptionGroupsSHA256)parts.push('有效异常主键、分类及组内笔数'+(r.exceptionGroupsSHA256===base.exceptionGroupsSHA256?'相同':'有变化'));
      }
      if(r.recordError)parts.push('核查记录不可用：'+r.recordError);
      if(this.configChanged)parts.push('配置已更改，以下仍为上次结果。');
      $('reconcileResultSource').textContent=parts.join('　');
    }
    renderResult(){
      this.renderSource();this.resultFields={left:new Map(this.result.left.fields.map(field=>[field.column,field])),right:new Map(this.result.right.fields.map(field=>[field.column,field]))};const holder=$('reconcileCounts');holder.replaceChildren();const counts=this.result.counts;
      for(const [key,label] of Object.entries(countLabels)){
        if(key==='withinTolerance'&&!this.result.query.fields?.some(pair=>pair.tolerance))continue;
        const span=document.createElement('span'),value=document.createElement('b');span.textContent=label;value.textContent=Number(counts[key]||0).toLocaleString();span.append(value);
        if(this.resultBaseline){const delta=(counts[key]||0)-(this.resultBaseline.counts[key]||0);span.append(document.createTextNode(`（较基线 ${delta>0?'+':''}${delta}）`));}
        holder.append(span);
      }
      $('reconcileSampleNote').textContent=`显示${this.result.samples?.length||0}组样例${this.result.samplesTruncated?'，完整结果请导出CSV':''}；长文本仅截取显示。`;this.renderSamples();
    }
    sourceButton(side,row,column,text){const button=document.createElement('button');button.type='button';button.textContent=text;button.title=`定位${side==='left'?'左':'右'}侧 ${this.colName(column)}${row}`;button.addEventListener('click',()=>this.goSource(side,row,column));return button;}
    renderSamples(){
      if(!this.result)return;const table=$('reconcileTable');table.replaceChildren();const head=document.createElement('thead'),hr=document.createElement('tr');for(const label of ['结果','主键','左源行','右源行','字段与读取值','差额 / 说明']){const th=document.createElement('th');th.textContent=label;hr.append(th);}head.append(hr);table.append(head);
      const body=document.createElement('tbody'),filter=$('reconcileResultFilter').value,groups=(this.result.samples||[]).filter(group=>!filter||group.status===filter);
      const query=this.result.query;
      for(const group of groups){const tr=document.createElement('tr');tr.dataset.status=group.status;const cells=Array.from({length:6},()=>document.createElement('td'));const status=document.createElement('span');status.className='reconcile-status';status.dataset.status=group.status;status.textContent=labels[group.status]||group.status;cells[0].append(status);cells[1].textContent=(group.key||[]).join(' / ');
        for(const [index,side]of [[2,'left'],[3,'right']]){const rows=group[side+'Rows']||[],count=Number(group[side+'Count']||0);for(const row of rows){cells[index].append(this.sourceButton(side,row,query.keys[0][side],String(row)),document.createTextNode(' '));}if(count>rows.length)cells[index].append(document.createTextNode(`等${count.toLocaleString()}条`));}
        if(group.status==='duplicate'){cells[4].textContent=`左${group.leftCount}条，右${group.rightCount}条；未自动配对。`;cells[5].textContent=group.reason||'';}
        else{
          const pairs=query.fields.length?query.fields:query.keys.slice(0,1);const reasons=[];
          pairs.forEach((pair,i)=>{const div=document.createElement('div');div.className='reconcile-value-pair';const title=document.createElement('strong');const lf=this.resultFields.left.get(pair.left),rf=this.resultFields.right.get(pair.right);title.textContent=(lf?.name||this.colName(pair.left))+' / '+(rf?.name||this.colName(pair.right));div.append(title);
            for(const side of sides){const record=group[side],value=record?.values?.[pair[side]];if(!record)continue;let text=value?.missingCache?'（公式缺少缓存）':!value?.exists?'（缺失单元格）':value.raw===''?'（空值）':value.raw;const button=this.sourceButton(side,record.number,pair[side],(side==='left'?'左：':'右：')+text+(value?.truncated?'…':''));button.title+='；读取类型：'+(value?.type||'隐式/空白')+(value?.typeMissing?'（源类型未保留）':'');div.append(button,document.createElement('br'));}
            cells[4].append(div);const comparison=group.comparisons?.[i];if(comparison?.delta&&comparison.delta!=='0')reasons.push(`${title.textContent}：${comparison.delta}${comparison.withinTolerance?'（容差内）':''}`);if(comparison?.reason)reasons.push(comparison.reason);
          });
          cells[5].textContent=[group.reason,...reasons,group.textTruncated?'样例文字已截取；CSV保留完整读取值。':''].filter(Boolean).join('\n');
        }
        for(const cell of cells)tr.append(cell);body.append(tr);
      }
      if(!groups.length){const row=document.createElement('tr'),cell=document.createElement('td');cell.colSpan=6;cell.className='reconcile-empty';cell.textContent=filter?'当前样例中没有此类记录。':'没有需要显示的差异样例。';row.append(cell);body.append(row);}table.append(body);
    }
    async goSource(side,row,column){
      if(!this.job)return;const epoch=this.epoch,job=this.job;
      const foreign=this.result?.[side]?.id?.split('/')[0]!==this.primaryBook;
      const popup=foreign?window.open('about:blank','_blank'):null;
      if(popup)popup.opener=null;
      let popupHandled=false;
      try{const target=await this.post('/api/reconcile/navigate',{jobId:job,side,row,col:column});if(epoch!==this.epoch||job!==this.job)return;
        if(target.sameBook){popup?.close();popupHandled=true;this.dialog.close();if(target.kind==='analysis')await window.ExcelLensAnalytics.navigateSheet(target.analysisSheet,row,column);else{window.ExcelLensAnalytics?.close();await this.navigate(target.sheet,this.colName(column)+row);}}
        else if(popup){popup.location.assign(target.url);popupHandled=true;}else this.toast('浏览器阻止了新窗口，请允许弹出窗口后重试','warning');
      }catch(error){if(epoch===this.epoch)this.toast(error.message,'error');}
      finally{if(popup&&!popupHandled)popup.close();}
    }
    async clear(){if(this.busy||!this.job)return;try{await this.post('/api/reconcile/clear',{id:this.job});this.job='';this.result=null;$('reconcileResults').hidden=true;this.controls();}catch(error){this.toast(error.message,'error');}}
    async compareBatches(){
      if(this.busy||this.pendingUpload)return;
      const before=$('batchBaseline').files[0],after=$('batchCurrent').files[0];
      if(!before||!after){this.progress('请选择基线和当期的完整核查包',true);return;}
      const files=new FormData();files.append('baseline',before);files.append('current',after);
      this.dialog.close();
      await window.ExcelLensAuditExport('/api/reconcile/batches?book='+encodeURIComponent(this.primaryBook),files,before.name+' / '+after.name);
    }
    async compareVersions(){
      if(this.busy||this.pendingUpload)return;
      const [left,right]=sides.map(side=>({book:this.node(side,'Book').value,source:this.node(side,'Sheet').value}));
      if(!left.book||!right.book){this.progress('请选择两个来源文件',true);return;}
      const scope=$('versionScope').value;
      const pair=scope==='pair'||scope==='keyed';
      if(pair&&(!left.source.startsWith('wb:')||!right.source.startsWith('wb:'))){this.progress('请选择两侧的源工作表',true);return;}
      const request={primaryBook:this.primaryBook,workbookKey:this.s.workbookKey,leftBook:left.book,rightBook:right.book,
        leftSheet:pair?left.source.slice(3):'',rightSheet:pair?right.source.slice(3):''};
      if(scope==='keyed'){
        if(!this.ready()){this.progress('请先读取两侧字段并确认业务主键',true);return;}
        try{
          const keys=this.mappings('keys');
          if(!keys.length)throw Error('请选择业务主键');
          request.alignment={left:this.ref('left'),right:this.ref('right'),keys};
        }catch(error){this.progress(error.message,true);return;}
      }
      const label=sides.map(side=>this.node(side,'Book').selectedOptions[0]?.textContent).join(' / ');
      this.dialog.close();
      await window.ExcelLensAuditExport('/api/reconcile/versions',request,'版本比较 · '+label);
    }
    async export(bundle=false){
      if(!this.job||!this.result||this.busy)return;
      const r=this.result,status=bundle?$('reconcileResultFilter').value:'';
      if(status==='equal'&&!r.query.includeEqual){this.progress('请勾选“包含一致记录”后重新核对',true);return;}
      this.dialog.close();
      const label=`${r.left.name} / ${r.left.sheet} 与 ${r.right.name} / ${r.right.sheet}（${bundle?'核查包 · '+(labels[status]||'全部类型'):'完整核对'}）`;
      await window.ExcelLensExportReconcile(this.job,$('reconcileProtectCSV').checked,label,bundle?{format:'zipcsv',status}:{});
    }
  }
  window.ExcelLensReconcile=Reconcile;
  window.ExcelLensReconcileModel={suggest,resolveColumn,rebindRecord};
})();
