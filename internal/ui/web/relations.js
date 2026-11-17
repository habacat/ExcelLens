(() => {
  'use strict';
  const m=window.ExcelLensRelationsModel,fieldsModel=window.ExcelLensCompositionModel,$=id=>document.getElementById(id);
  const node=(tag,text,cls)=>{const value=document.createElement(tag);if(text!==undefined)value.textContent=text;if(cls)value.className=cls;return value;};
  const button=(text,action,cls='mini-button')=>{const value=node('button',text,cls);value.type='button';value.onclick=action;return value;};
  const label=fieldsModel.label;
  class Relations{
    constructor({api,context,formats,fieldLimits,onExport,navigate,toast,canRun=()=>true}){
      Object.assign(this,{api,context,formats,fieldLimits,onExport,navigate,toast,canRun});
      this.serial=0;this.navSerial=0;this.books=[];this.inputs=[];this.keys=[];this.rowPage=0;this.columnPage=0;this.visibleColumns=1;this.accepted=null;
      this.fieldsTasks=new window.ExcelLensSourceTasks({api,context});this.tasks=new window.ExcelLensSourceTasks({api,context,endpoint:'/api/relation',label:'关联'});
      this.dialog=node('dialog',undefined,'float-dialog relations-dialog');this.dialog.id='relationsDialog';this.dialog.setAttribute('aria-labelledby','relationsTitle');
      this.dialog.innerHTML=`<form method="dialog" class="dialog-head"><div id="relationsTitle" class="dialog-title">关联与提取</div><button class="dialog-close" value="cancel" aria-label="关闭关联与提取">×</button></form>
        <div class="relations-body"><fieldset id="relationsConfig"><div class="relations-toolbar"><button id="relationsRefresh" class="mini-button" type="button">刷新来源</button><button id="relationsUpload" class="mini-button" type="button">添加文件</button><input id="relationsFile" type="file" hidden><button id="relationsSwap" class="mini-button" type="button">交换左右表</button><button id="relationsRead" class="mini-button" type="button">读取字段</button></div>
        <div id="relationsSources" class="relations-sources"></div><div class="relations-key-heading"><strong>关联键</strong><button id="relationsAddKey" class="mini-button" type="button">添加键</button></div><div id="relationsKeys"></div></fieldset>
        <section id="relationsResults" hidden><div class="relations-result-heading"><strong>结果</strong><span id="relationsPrevious" class="relations-previous" hidden>上次结果</span><span id="relationsResultSources"></span><button id="relationsClear" type="button" class="mini-button">清除</button></div>
        <div id="relationsCounts" class="relations-counts"></div><div class="relations-results-tools"><label>提取<select id="relationsMode" class="field-control"></select></label><span id="relationsTotal"></span></div>
        <div id="relationsTableWrap" class="relations-table-wrap"><table id="relationsTable" class="relations-table"></table></div><div class="relations-paging"><div id="relationsRowPaging"></div><div id="relationsColumnPaging"></div></div>
        <div class="relations-export"><label>导出上限<input id="relationsMaximum" class="field-control" inputmode="numeric" aria-label="导出记录上限"></label><label>格式<select id="relationsFormat" class="field-control" aria-label="关联导出格式"></select></label><label><input id="relationsProtect" type="checkbox" checked>文本保护</label><button id="relationsExport" type="button" class="mini-button">导出结果</button></div></section></div>
        <div class="relations-footer"><div id="relationsStatus" role="status" aria-live="polite"></div><button id="relationsCancel" class="mini-button" type="button" hidden>取消任务</button><button id="relationsRun" class="primary-button compact" type="button">运行关联</button></div>`;
      document.body.append(this.dialog);this.picker=new window.ExcelLensFieldPicker({id:'relationsPicker',pageRows:fieldLimits.pickerRows});
      this.exportOptions=formats.relationExports||[];this.exportFormatTouched=false;
      for(const option of this.exportOptions)$('relationsFormat').append(new Option(option.name,option.format));
      $('relationsFormat').value=this.exportOptions[0]?.format||'';
      $('relationsFormat').addEventListener('change',()=>{this.exportFormatTouched=true;});
      this.bind();
      this.observer=new ResizeObserver(entries=>{const width=entries[0]?.contentRect.width||0;const count=Math.max(1,Math.min(this.limits?.viewColumns||1,Math.floor(Math.max(1,width-126)/150)));if(count!==this.visibleColumns){this.visibleColumns=count;this.columnPage=0;if(this.accepted)this.renderTable();}});this.observer.observe($('relationsTableWrap'));
    }
    identity(){const c=this.context();return JSON.stringify([c.bookKey,c.book]);}
    post(path,value,options={}){return this.api(path,{method:'POST',body:JSON.stringify(value),...options});}
    bind(){
      this.dialog.addEventListener('keydown',event=>event.stopPropagation());
      this.dialog.addEventListener('close',()=>{if(!this.dialog.open)this.cancel(false);});
      $('relationsOpenBtn').addEventListener('click',()=>this.open());
      $('relationsRefresh').onclick=()=>this.action(current=>this.refresh(current));
      $('relationsRead').onclick=()=>this.action(async current=>{for(let i=0;i<2&&current();i++)await this.read(i,current);});
      $('relationsUpload').onclick=()=>$('relationsFile').click();$('relationsFile').accept=JSON.parse($('fileFormats').textContent).extensions.join(',');
      $('relationsFile').onchange=event=>{const file=event.target.files?.[0];event.target.value='';if(file)this.action(current=>this.upload(file,current));};
      $('relationsSwap').onclick=()=>this.swap();$('relationsAddKey').onclick=()=>{this.keys.push({left:null,right:null,mode:'',trim:false,ignoreCase:false});this.changed();this.renderKeys();};
      $('relationsRun').onclick=()=>this.action(current=>this.run(current));$('relationsCancel').onclick=()=>this.cancel(true);
      $('relationsMode').onchange=()=>{const selected=$('relationsMode').value;this.action(current=>this.changeMode(selected,current));};
      $('relationsClear').onclick=()=>this.clear();$('relationsExport').onclick=()=>this.export();
    }
    message(text,error=false){$('relationsStatus').textContent=text;$('relationsStatus').setAttribute('role',error?'alert':'status');}
    controls(){
      $('relationsConfig').disabled=!!this.busy;$('relationsCancel').hidden=!this.busy;$('relationsRun').disabled=!!this.busy||!this.inputs.length||this.inputs.some(input=>!input.info);$('relationsAddKey').disabled=!!this.busy||!this.limits||this.keys.length>=this.limits.keys;$('relationsMode').disabled=!!this.busy||!this.accepted;$('relationsClear').disabled=!!this.busy||!this.accepted;$('relationsExport').disabled=!!this.busy||!this.accepted||m.count(this.accepted.data.preview.total)>BigInt(this.limits.maximumRows);
    }
    async action(operation){
      if(this.busy)return;const ticket=++this.serial,identity=this.identity(),current=()=>ticket===this.serial&&identity===this.identity()&&this.context().opened&&this.dialog.open;this.busy=true;this.controls();
      try{await operation(current);}catch(error){if(current()&&error.name!=='AbortError')this.message(error.message,true);}finally{if(ticket===this.serial){this.busy=false;this.controls();}}
    }
    cancel(report){const pending=this.busy;++this.serial;++this.navSerial;this.fieldsTasks.reset();this.tasks.reset();this.controller?.abort();this.controller=null;this.picker.close();this.busy=false;if(this.accepted)$('relationsMode').value=this.accepted.data.outputMode;if(report&&pending)this.message('已取消');this.controls();}
    resetBook(){this.cancel(false);if(this.dialog.open)this.dialog.close();this.releaseAccepted();this.inputs=[];this.keys=[];this.books=[];this.identityKey='';for(const id of ['relationsSources','relationsKeys','relationsTable','relationsCounts','relationsMode'])$(id).replaceChildren();$('relationsResults').hidden=true;this.message('');}
    releaseAccepted(){if(this.accepted){this.tasks.clear(this.accepted.data.id);this.accepted=null;}}
    async open(){
      if(!this.context().opened){this.toast('请先打开数据文件','warning');return;}
      if(this.identityKey!==this.identity()){this.resetBook();this.identityKey=this.identity();}
      this.cancel(false);if(!this.dialog.open)this.dialog.showModal();
      await this.action(async current=>{
        if(!this.limits){this.limits=m.validateLimits(await this.api('/api/relation/limits'));if(!current())return;$('relationsMaximum').value=String(this.limits.defaultRows);}
        await this.refresh(current);if(!current())return;
        if(!this.inputs.length){const c=this.context(),other=this.books.find(book=>book.id!==c.book),primary=this.books.find(book=>book.id===c.book),second=other?.sheets?.[0]||primary?.sheets?.find(sheet=>'wb:'+sheet.name!==c.source);this.inputs=[{book:c.book,source:c.source||'',headerRow:1,info:null,columns:[]},{book:other?.id||c.book,source:second?'wb:'+second.name:'',headerRow:1,info:null,columns:[]}];this.renderSources();this.renderKeys();}
        this.message('');
      });
    }
    async refresh(current,focus=''){
      this.controller?.abort();const controller=new AbortController();this.controller=controller;
      try{const data=await this.api('/api/relation/sources'+(focus?'?book='+encodeURIComponent(focus):''),{signal:controller.signal});if(!current())return;if(data.currentBook!==this.context().book||!Array.isArray(data.books))throw Error('来源列表已变化，请刷新');
        const previous=new Map(this.books.map(book=>[book.id,book]));this.books=data.books.map(book=>!book.scratchLoaded&&previous.get(book.id)?.scratchLoaded?{...book,scratch:previous.get(book.id).scratch,scratchLoaded:true}:book);
        for(let side=0;side<this.inputs.length;side++){const input=this.inputs[side],book=this.books.find(book=>book.id===input.book),exists=book&&((book.sheets||[]).some(sheet=>'wb:'+sheet.name===input.source)||(book.scratch||[]).some(sheet=>'scratch:'+sheet.id===input.source)||!book.scratchLoaded&&input.source.startsWith('scratch:'));if(!exists){input.info=null;input.columns=[];}}
        this.renderSources();
      }finally{if(this.controller===controller)this.controller=null;}
    }
    changed(){if(this.accepted)$('relationsPrevious').hidden=false;this.message('');this.controls();}
    invalidate(side){const input=this.inputs[side];input.info=null;input.columns=[];for(const key of this.keys)key[side?'right':'left']=null;this.changed();this.renderSources();this.renderKeys();}
    renderSources(){
      const host=$('relationsSources');host.replaceChildren();
      this.inputs.forEach((input,side)=>{
        const pane=node('section',undefined,'relations-source'),head=node('div',undefined,'relations-source-heading'),title=node('strong',side?'右表':'左表');head.append(title);const meta=this.books.find(book=>book.id===input.book);
        if(meta?.attached)head.append(button('移除文件',()=>this.action(async current=>{await this.post('/api/reconcile/source/remove',{book:input.book});if(!current())return;input.source='';this.invalidate(side);await this.refresh(current);})));
        const book=node('select',undefined,'field-control');book.setAttribute('aria-label',(side?'右':'左')+'表文件');book.dataset.side=String(side);book.dataset.choice='book';for(const item of this.books){const option=new Option(item.name,item.id);option.disabled=!!item.incomplete;book.append(option);}book.value=input.book;
        const sheet=node('select',undefined,'field-control');sheet.setAttribute('aria-label',(side?'右':'左')+'表工作表');sheet.dataset.side=String(side);sheet.dataset.choice='source';sheet.append(new Option('选择工作表',''));for(const item of meta?.sheets||[])sheet.append(new Option(item.name,'wb:'+item.name));for(const item of meta?.scratch||[])sheet.append(new Option(item.name+'（分析表）','scratch:'+item.id));sheet.value=input.source;
        book.onchange=()=>{input.book=book.value;input.source='';input.headerRow=1;this.invalidate(side);const chosen=this.books.find(item=>item.id===input.book);if(chosen&&!chosen.scratchLoaded)this.action(current=>this.refresh(current,input.book));};
        sheet.onchange=()=>{input.source=sheet.value;input.headerRow=input.source.startsWith('scratch:')?0:1;this.invalidate(side);};
        const bottom=node('div',undefined,'relations-source-tools'),header=node('label','表头行'),number=node('input',undefined,'field-control');number.type='number';number.min='0';number.max=String(this.formats.maxRows);number.value=String(input.headerRow);number.title='0：无表头';number.disabled=input.source.startsWith('scratch:');number.dataset.side=String(side);number.dataset.choice='header';number.setAttribute('aria-label',(side?'右':'左')+'表表头行');number.onchange=()=>{input.headerRow=Number(number.value);this.invalidate(side);};header.append(number);
        const selected=button('结果字段 '+input.columns.length,()=>this.pickColumns(side));selected.disabled=!input.info;selected.dataset.side=String(side);selected.dataset.choice='columns';const info=node('span',input.info?m.formatCount(input.info.rows)+' 条 / '+input.info.fields.length+' 列':'未读取字段','relations-source-state');bottom.append(header,selected,info);pane.append(head,book,sheet,bottom);host.append(pane);
      });this.controls();
    }
    async read(side,current){
      const input=this.inputs[side];if(!input?.book||!input.source)throw Error('请选择两侧文件和工作表');if(!Number.isSafeInteger(input.headerRow)||input.headerRow<0||input.headerRow>this.formats.maxRows)throw Error('表头行号无效');
      const choice={book:input.book,source:input.source,headerRow:input.headerRow},identity={...this.context()};let result;
      try{this.message('正在读取'+(side?'右':'左')+'表字段…');result=await this.fieldsTasks.run({mode:'fields',choice},current);if(!current()||this.inputs[side]!==input)return;
        const info=m.validateFields(result,choice,identity,this.formats),available=new Set(info.fields.map(field=>field.column));input.info=info;input.columns=input.columns.filter(column=>available.has(column));if(!input.columns.length)input.columns=info.fields.slice(0,this.limits.initialFields).map(field=>field.column);
        if(this.inputs.every(input=>input.info)){
          if(!this.keys.length)this.keys=[{left:null,right:null,mode:'',trim:false,ignoreCase:false}];
          const suggestions=fieldsModel.suggest(this.inputs[0].info.fields,this.inputs[1].info.fields),first=suggestions.findIndex(column=>column>0);
          for(const key of this.keys){if(!key.left&&!key.right){if(first>=0){key.left=this.inputs[0].info.fields[first].column;key.right=suggestions[first];}}else for(let missing=0;missing<2;missing++){const prop=missing?'right':'left',other=missing?'left':'right';if(!key[prop]&&key[other]){const field=this.inputs[1-missing].info.fields.find(field=>field.column===key[other]);if(field)key[prop]=fieldsModel.suggest([field],this.inputs[missing].info.fields)[0];}}}
        }
        this.changed();this.renderSources();this.renderKeys();
      }finally{if(result)await this.fieldsTasks.clear(result.id);}
    }
    pickColumns(side){const input=this.inputs[side],info=input.info;if(!info)return;this.picker.open({title:(side?'右':'左')+'表结果字段',fields:info.fields,selected:input.columns,multiple:true,limit:this.limits.columns,current:()=>this.dialog.open&&this.inputs[side]===input&&input.info===info,onChoose:columns=>{input.columns=columns;this.changed();this.renderSources();}});}
    pickKey(side,index){const input=this.inputs[side],info=input.info;if(!info)return;const prop=side?'right':'left',pair=this.keys[index];this.picker.open({title:(side?'右':'左')+'表关联键',fields:info.fields,disabled:this.keys.filter((_,i)=>i!==index).map(key=>key[prop]),current:()=>this.dialog.open&&this.keys[index]===pair&&input.info===info,onChoose:column=>{pair[prop]=column;this.changed();this.renderKeys();}});}
    renderKeys(){
      const host=$('relationsKeys');host.replaceChildren();
      if(!this.keys.length){host.append(node('span','读取字段后选择关联键','relations-source-state'));return;}
      this.keys.forEach((pair,index)=>{
        const row=node('div',undefined,'relations-key');
        for(let side=0;side<2;side++){const prop=side?'right':'left',field=this.inputs[side]?.info?.fields.find(field=>field.column===pair[prop]),pick=button(field?label(field.column)+' '+field.name:(side?'右':'左')+'表字段',()=>this.pickKey(side,index),'relations-field-button');pick.disabled=!this.inputs[side]?.info;pick.setAttribute('aria-label',(side?'右':'左')+'表第'+(index+1)+'个关联键');row.append(pick);}
        const mode=node('select',undefined,'field-control');mode.setAttribute('aria-label','第'+(index+1)+'个键的匹配方式');for(const [value,text]of [['','保留源类型'],['text','按显示文本'],['number','按精确数值']])mode.append(new Option(text,value));mode.value=pair.mode;mode.onchange=()=>{pair.mode=mode.value;if(pair.mode==='number'){pair.trim=false;pair.ignoreCase=false;}this.changed();this.renderKeys();};row.append(mode);
        const flags=node('div',undefined,'relations-key-flags');for(const [key,text]of [['trim','去空格'],['ignoreCase','忽略大小写']]){const wrap=node('label'),check=node('input');check.type='checkbox';check.checked=pair[key];check.disabled=pair.mode==='number';check.onchange=()=>{pair[key]=check.checked;this.changed();};wrap.append(check,document.createTextNode(text));flags.append(wrap);}row.append(flags);
        const remove=button('×',()=>{this.keys.splice(index,1);this.changed();this.renderKeys();});remove.title='移除关联键';remove.disabled=this.keys.length===1;row.append(remove);host.append(row);
      });this.controls();
    }
    query(){if(this.inputs.some(input=>!input.info))throw Error('请先读取两侧字段');return m.validateQuery({keys:m.copy(this.keys),leftColumns:[...this.inputs[0].columns],rightColumns:[...this.inputs[1].columns]},this.inputs.map(input=>input.info),this.limits);}
    async run(current){
      if(!this.canRun())return;const query=this.query(),infos=this.inputs.map(input=>m.copy(input.info)),identity={...this.context()};
      await this.prepare({mode:'prepare',left:m.fieldRef(infos[0]),right:m.fieldRef(infos[1]),query,outputMode:'left'}, {query,infos,identity,mode:'left',operation:'prepare'},current);
    }
    async changeMode(id,current){
      const accepted=this.accepted;if(!accepted)return;await this.prepare({mode:'preview',parentId:accepted.data.id,outputMode:id},{query:accepted.data.query,infos:accepted.infos,identity:{...this.context()},mode:id,operation:'preview',parentId:accepted.data.id},current);
    }
    async prepare(request,expected,current){
      let response,published=false;try{
        this.message('正在关联…');response=await this.tasks.run({...request,...(this.accepted?{keepId:this.accepted.data.id}:{})},current);if(!current())return;
        const data=m.validate(response,{...expected,id:response.id},this.limits,this.formats),candidate={data,infos:m.copy(expected.infos)};
        const view=this.buildResult(candidate);if(!current())return;
        const old=this.accepted;this.accepted=candidate;this.rowPage=this.columnPage=0;this.chooseExportFormat();this.renderResult(view);published=true;this.message('');if(old)await this.tasks.clear(old.data.id);
      }finally{if(response&&!published)await this.tasks.clear(response.id);if(!published&&current()&&this.accepted)$('relationsMode').value=this.accepted.data.outputMode;}
    }
    buildResult(candidate){
      const data=candidate.data,counts=document.createDocumentFragment(),options=document.createDocumentFragment();for(const [key,text]of [['leftMatched','左匹配'],['rightMatched','右匹配'],['leftOnly','左未匹配'],['rightOnly','右未匹配'],['leftInvalid','左无效键'],['rightInvalid','右无效键']]){const item=node('span',text),value=node('b',m.formatCount(data.counts[key]));item.append(value);counts.append(item);}if(m.count(data.counts.manyToManyGroups)>0n)counts.append(node('span','多对多 '+m.formatCount(data.counts.manyToManyGroups)+' 组','relations-multiple'));for(const item of m.modes)options.append(new Option(item.label,item.id));return {counts,options,source:data.left.name+' ↔ '+data.right.name,total:'共 '+m.formatCount(data.preview.total)+' 条',table:this.buildTable(candidate,0,0)};
    }
    renderResult(view){
      $('relationsResults').hidden=false;$('relationsPrevious').hidden=true;$('relationsResultSources').textContent=view.source;$('relationsCounts').replaceChildren(view.counts);$('relationsMode').replaceChildren(view.options);$('relationsMode').value=this.accepted.data.outputMode;$('relationsTotal').textContent=view.total;this.commitTable(view.table);this.controls();
    }
    buildTable(accepted,rowPage,columnPage){
      const data=accepted.data,all=m.columns(data),width=Math.max(1,this.visibleColumns),begin=columnPage*width,columns=all.slice(begin,begin+width),rows=data.preview.rows.slice(rowPage*this.limits.viewRows,(rowPage+1)*this.limits.viewRows),fragment=document.createDocumentFragment(),head=node('thead'),hr=node('tr');hr.append(node('th','源行'),node('th','状态'));
      for(const item of columns)hr.append(node('th',(item.side?'右 ':'左 ')+label(item.column)+' '+item.field.name));head.append(hr);fragment.append(head);const body=node('tbody');
      for(const row of rows){const tr=node('tr'),origin=node('td',undefined,'relations-origins');for(const side of m.mode(data.outputMode).sides){const record=row[side?'right':'left'];if(record)origin.append(button((side?'右 ':'左 ')+record.number,()=>this.goSource(accepted,side,record.number,data.query.keys[0][side?'right':'left']),'relations-source-link'));}tr.append(origin);const state=node('td',row.status==='matched'?'已匹配':row.status==='invalid_key'?'无效键':'未匹配','relations-row-state');state.dataset.status=row.status;if(row.reason)state.title=window.ExcelLensRecordCardModel.preview(row.reason,this.limits.snippetUnits);tr.append(state);
        for(const item of columns){const record=row[item.side?'right':'left'],value=record?.values?.[item.column],cell=node('td');if(record){const raw=value?.missingCache?'缺缓存':value?.exists?window.ExcelLensCellView.value(m.cell(value),false,true):'',snippet=window.ExcelLensRecordCardModel.preview(raw,this.limits.snippetUnits),link=button(snippet+(snippet.length<raw.length||value?.truncated?'…':''),()=>{if(!window.getSelection()?.toString())this.goSource(accepted,item.side,record.number,item.column);},'relations-value');link.title=(item.side?'右 ':'左 ')+label(item.column)+record.number;if(value?.typeMissing||value?.uncertain||value?.truncated)link.classList.add('relations-value-notice');cell.append(link);}tr.append(cell);}body.append(tr);
      }
      if(!rows.length){const tr=node('tr'),empty=node('td',m.count(data.preview.total)===0n?'没有此类记录':'预览内容过大，请导出查看','relations-empty');empty.colSpan=columns.length+2;tr.append(empty);body.append(tr);}fragment.append(body);return {fragment,all:all.length,columnPage,rowPage};
    }
    commitTable(table){$('relationsTable').replaceChildren(table.fragment);this.paging($('relationsRowPaging'),this.accepted.data.preview.rows.length,this.rowPage,this.limits.viewRows,page=>{this.rowPage=page;this.renderTable();},'预览 '+this.accepted.data.preview.rows.length+' 条');this.paging($('relationsColumnPaging'),table.all,this.columnPage,this.visibleColumns,page=>{this.columnPage=page;this.renderTable();},'字段');}
    renderTable(){if(!this.accepted)return;const all=m.columns(this.accepted.data).length;this.columnPage=Math.min(this.columnPage,Math.max(0,Math.ceil(all/this.visibleColumns)-1));this.rowPage=Math.min(this.rowPage,Math.max(0,Math.ceil(this.accepted.data.preview.rows.length/this.limits.viewRows)-1));this.commitTable(this.buildTable(this.accepted,this.rowPage,this.columnPage));}
    paging(host,count,page,size,change,text){host.replaceChildren();const info=node('span',text+(count>size?'　'+(page+1)+' / '+Math.ceil(count/size):''));host.append(info);if(count<=size)return;const previous=button('上一页',()=>change(page-1)),next=button('下一页',()=>change(page+1));previous.disabled=page===0;next.disabled=(page+1)*size>=count;host.append(previous,next);}
    swap(){if(this.busy)return;this.inputs.reverse();for(const key of this.keys)[key.left,key.right]=[key.right,key.left];this.changed();this.renderSources();this.renderKeys();}
    async upload(file,current){const c={...this.context()},form=new FormData();form.append('file',file,file.name);form.append('reconcileSource','1');form.append('workbookKey',c.bookKey);form.append('primaryBook',c.book);const data=await this.api('/api/open',{method:'POST',body:form});if(!current())return;await this.refresh(current,data.book);if(!current())return;const book=this.books.find(item=>item.id===data.book);if(!book?.sheets?.length)throw Error('新文件没有可用工作表');this.inputs[1]={book:data.book,source:'wb:'+book.sheets[0].name,headerRow:1,info:null,columns:[]};for(const key of this.keys)key.right=null;this.changed();this.renderSources();this.renderKeys();}
    async clear(){if(this.busy||!this.accepted)return;const accepted=this.accepted;return this.action(async current=>{await this.post('/api/relation/clear',{id:accepted.data.id});if(!current()||this.accepted!==accepted)return;this.accepted=null;$('relationsResults').hidden=true;this.message('');this.controls();});}
    async export(){
      const accepted=this.accepted;if(!accepted||this.busy)return;try{const maximum=$('relationsMaximum').value.replace(/,/g,'').trim();if(!/^[1-9]\d*$/.test(maximum)||BigInt(maximum)>BigInt(this.limits.maximumRows))throw Error('导出上限无效');if(m.count(accepted.data.preview.total)>BigInt(maximum))throw Error('结果 '+m.formatCount(accepted.data.preview.total)+' 条，超过导出上限');
        const payload={jobId:accepted.data.id,mode:accepted.data.outputMode,maximum,protectCSV:$('relationsProtect').checked},format=$('relationsFormat').value;
        if(format){if(!this.exportOptions.some(option=>option.format===format))throw Error('关联导出格式无效');payload.format=format;}
        const name=accepted.data.left.name+' ↔ '+accepted.data.right.name;this.dialog.close();await this.onExport(payload,name);
      }catch(error){this.message(error.message,true);}
    }
    chooseExportFormat(){
      if(this.exportFormatTouched||!this.accepted||!this.exportOptions.length)return;
      const compressed=!!this.limits.zipRows&&m.count(this.accepted.data.preview.total)>=BigInt(this.limits.zipRows),option=this.exportOptions.find(option=>option.compressed===compressed)||this.exportOptions[0];
      $('relationsFormat').value=option.format;
    }
    async goSource(accepted,side,row,col){
      if(this.accepted!==accepted)return;const ticket=++this.navSerial,identity=this.identity(),book=accepted.infos[side].ref.book,foreign=book!==this.context().book,popup=foreign?window.open('about:blank','_blank'):null;if(popup)popup.opener=null;let handled=false;
      try{const target=await this.post('/api/relation/navigate',{jobId:accepted.data.id,book,side,row,col});if(ticket!==this.navSerial||identity!==this.identity()||this.accepted!==accepted||!this.dialog.open)return;
        if(target.sameBook){popup?.close();handled=true;this.dialog.close();if(target.kind==='analysis')await window.ExcelLensAnalytics.navigateSheet(target.analysisSheet,row,col);else{window.ExcelLensAnalytics?.close();await this.navigate(target.sheet,label(col)+row);}}
        else if(popup){popup.location.assign(target.url);handled=true;}else this.toast('请允许弹出窗口后重试','warning');
      }catch(error){if(identity===this.identity()&&ticket===this.navSerial)this.message(error.message,true);}finally{if(popup&&!handled)popup.close();}
    }
  }
  window.ExcelLensRelations=Relations;
})();
