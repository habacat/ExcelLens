(() => {
  'use strict';
  const $=id=>document.getElementById(id),make=(tag,cls='',text='')=>{const n=document.createElement(tag);n.className=cls;n.textContent=text;return n;};
  class RecordCard {
    constructor({state,api,toast,copy,navigate,colName,parseColumn,pinnedThroughRow,source,limits,canRead=()=>true}){
      Object.assign(this,{s:state,api,toast,copy,navigate,colName,parseColumn,pinnedThroughRow,source,limits,canRead});
      if(!Number.isSafeInteger(limits?.columns)||limits.columns<1||!Number.isSafeInteger(limits?.textUnits)||limits.textUnits<2||!limits.bytes||!limits.requestBytes)throw Error('记录卡容量配置不可用');
      this.model=window.ExcelLensRecordCardModel;this.serial=0;this.nav=0;this.busy=false;this.mode='display';
      this.dialog=this.create();this.detailDialog=this.createDetail();
      $('recordCardOpenBtn').addEventListener('click',()=>this.open());$('recordCardClose').addEventListener('click',()=>this.close());
      this.dialog.addEventListener('cancel',event=>{event.preventDefault();this.close();});
      this.dialog.addEventListener('close',()=>{if(!this.dialog.open)this.clear();});
      $('recordCardForm').addEventListener('submit',event=>{event.preventDefault();this.apply();});
      $('recordCardCancel').addEventListener('click',()=>this.cancel());
      $('recordCardPrevious').addEventListener('click',()=>this.record(-1));$('recordCardNext').addEventListener('click',()=>this.record(1));
      $('recordFieldPrevious').addEventListener('click',()=>this.fields(-1));$('recordFieldNext').addEventListener('click',()=>this.fields(1));
      $('recordCardMode').addEventListener('change',()=>{this.mode=$('recordCardMode').value==='raw'?'raw':'display';this.closeDetail();if(this.page)this.render();});
      $('recordTextClose').addEventListener('click',()=>this.closeDetail());this.detailDialog.addEventListener('cancel',event=>{event.preventDefault();this.closeDetail();});
      this.detailDialog.addEventListener('close',()=>{if(!this.detailDialog.open){this.detail=null;$('recordTextValue').textContent='';}});
      $('recordTextPrevious').addEventListener('click',()=>this.textPart(-1));$('recordTextNext').addEventListener('click',()=>this.textPart(1));$('recordTextCopy').addEventListener('click',()=>this.copyDetail());
      this.dialog.addEventListener('keydown',event=>{
        if(event.target.closest?.('input,select,textarea')||event.ctrlKey||event.altKey||event.metaKey)return;
        if(event.key==='PageUp'||event.key==='PageDown'){event.preventDefault();this.record(event.key==='PageUp'?-1:1);}
      });
      document.addEventListener('pointerdown',()=>this.cancelNavigation(),true);document.addEventListener('keydown',()=>this.cancelNavigation(),true);
      window.addEventListener('resize',()=>this.controls());this.controls();
    }
    create(){
      const d=make('dialog','float-dialog record-card-dialog');d.id='recordCardDialog';d.setAttribute('aria-labelledby','recordCardTitle');
      // All source strings use textContent; this template is static.
      d.innerHTML=`<div class="dialog-head"><div id="recordCardTitle" class="dialog-title">记录卡片</div><button id="recordCardClose" class="dialog-close" type="button" aria-label="关闭记录卡片">×</button></div>
        <p id="recordCardSource" class="record-card-source"></p><div class="record-card-tools"><form id="recordCardForm"><label>表头行<input id="recordHeaderRow" class="field-control" type="number" min="0" value="1" title="0 表示使用列字母"></label><label>起始列<input id="recordStartColumn" class="field-control" value="A" autocomplete="off" aria-label="起始列字母或列号"></label><label>每页字段<select id="recordPageSize" class="field-control"></select></label><button id="recordCardApply" class="mini-button" type="submit">读取字段</button></form><label>内容<select id="recordCardMode" class="field-control"><option value="display">显示值</option><option value="raw">原值</option></select></label></div>
        <div class="record-card-position"><strong id="recordCardPosition"></strong><span id="recordCardHeader"></span></div><div id="recordCardFields" class="record-card-fields" tabindex="0" aria-label="当前记录字段"></div><p id="recordCardNotices" class="record-card-notices" hidden></p>
        <div class="record-card-paging"><div><button id="recordCardPrevious" class="mini-button" type="button">上一条</button><button id="recordCardNext" class="mini-button" type="button">下一条</button></div><div><button id="recordFieldPrevious" class="mini-button" type="button">上一页字段</button><span id="recordFieldCount"></span><button id="recordFieldNext" class="mini-button" type="button">下一页字段</button></div></div><div class="record-card-footer"><p id="recordCardStatus" role="status" aria-live="polite"></p><button id="recordCardCancel" class="mini-button" type="button" hidden>取消读取</button><span>PageUp / PageDown</span></div>`;
      document.body.append(d);
      const sizes=[...new Set([this.limits.columns,Math.max(1,Math.floor(this.limits.columns/2)),Math.max(1,Math.floor(this.limits.columns/4)),1])];
      for(const size of sizes)$('recordPageSize').append(new Option(String(size),String(size)));$('recordPageSize').value=String(this.limits.columns);return d;
    }
    createDetail(){
      const d=make('dialog','float-dialog record-text-dialog');d.id='recordTextDialog';d.setAttribute('aria-labelledby','recordTextTitle');
      d.innerHTML='<div class="dialog-head"><div id="recordTextTitle" class="dialog-title">完整内容</div><button id="recordTextClose" class="dialog-close" type="button" aria-label="关闭完整内容">×</button></div><p id="recordTextSource" class="record-card-source"></p><pre id="recordTextValue" class="record-text-value" tabindex="0"></pre><div class="record-card-paging"><button id="recordTextPrevious" class="mini-button" type="button">上一段</button><span id="recordTextPosition"></span><button id="recordTextNext" class="mini-button" type="button">下一段</button><button id="recordTextCopy" class="mini-button" type="button">复制完整内容</button></div><p id="recordTextStatus" role="status" aria-live="polite"></p>';
      document.body.append(d);return d;
    }
    binding(){
      const req={workbookKey:this.s.workbookKey,book:this.s.book,sheet:this.s.sheet,viewId:this.s.rowView?.id||'',filterId:this.s.filtered?this.s.filterId||'':'',pinnedThroughRow:this.pinnedThroughRow()};
      return {request:req,index:this.s.index,key:JSON.stringify([req,this.s.sheetEpoch,this.s.opened,this.s.indexReady]),totalColumns:this.s.index?.maxCol};
    }
    sameSource(bound){const now=this.binding();return !!bound&&now.key===bound.key&&now.index===bound.index;}
    enabled(){return !!this.s.opened&&!!this.s.indexReady&&!this.s.opening&&!this.s.sheetLoadingEpoch&&!this.s.rowViewFailure&&!!this.s.rowView?.id&&!!this.s.book&&!!this.s.sheet&&!!this.s.selected&&this.s.selected.row>=1&&this.s.selected.row<=this.s.index?.maxRow&&this.s.index?.maxCol>0&&this.s.index?.totalRows>0&&this.canRead();}
    status(text,error=false){$('recordCardStatus').textContent=text||'';$('recordCardStatus').classList.toggle('is-error',error);$('recordCardStatus').setAttribute('role',error?'alert':'status');}
    controls(){
      if(this.bound&&!this.sameSource(this.bound)){this.resetBook();return;}
      const enabled=this.enabled(),ready=enabled&&!!this.page&&!this.busy;
      $('recordCardOpenBtn').disabled=!enabled;if($('menuRecordCard'))$('menuRecordCard').disabled=!enabled;
      $('recordCardApply').disabled=!enabled||this.busy;$('recordCardCancel').hidden=!this.busy;$('recordCardFields').setAttribute('aria-busy',String(this.busy));
      for(const id of ['recordHeaderRow','recordStartColumn','recordPageSize'])$(id).disabled=this.busy;
      $('recordCardPrevious').disabled=!ready||!this.page.previousRow;$('recordCardNext').disabled=!ready||!this.page.nextRow;
      $('recordFieldPrevious').disabled=!ready||this.page.firstColumn<=1;$('recordFieldNext').disabled=!ready||this.page.firstColumn+this.page.columns>this.page.totalColumns;
      for(const node of $('recordCardFields').querySelectorAll('button'))node.disabled=!ready||node.dataset.unavailable==='true';
    }
    async open(){
      this.controls();if(!this.enabled()){this.toast('请先在已完成加载的工作表中选择一条可见记录','warning');return;}
      if(this.dialog.open)return;
      this.cancelNavigation();this.clear();this.bound=this.binding();this.mode='display';$('recordCardMode').value='display';
      const header=this.s.autoFilter?.row||1,columns=this.limits.columns,column=Math.max(1,Math.min(this.bound.totalColumns,this.s.selected.col||1));
      $('recordHeaderRow').value=String(header);$('recordHeaderRow').max=String(this.source().maxRows);$('recordPageSize').value=String(columns);
      $('recordStartColumn').value=this.colName(Math.floor((column-1)/columns)*columns+1);$('recordCardSource').textContent=`${this.s.fileName||'当前工作簿'} / ${this.s.sheet}`;
      $('recordCardFields').textContent='正在读取记录…';this.dialog.showModal();await this.apply(this.s.selected.row);
      if(this.dialog.open)$('recordCardFields').focus({preventScroll:true});
    }
    stop(){++this.serial;this.abort?.abort();this.abort=null;this.busy=false;}
    clear(){this.stop();this.closeDetail();this.bound=this.page=this.accepted=null;$('recordCardFields').replaceChildren();for(const id of ['recordCardPosition','recordCardHeader','recordFieldCount','recordCardNotices'])$(id).textContent='';$('recordCardNotices').hidden=true;this.status('');}
    close(preserveNavigation=false){if(!preserveNavigation)this.cancelNavigation();this.clear();if(this.dialog.open)this.dialog.close();this.controls();}
    resetBook(){this.close();}
    cancel(){this.stop();if(!this.page)$('recordCardFields').textContent='读取已取消，可重新读取字段。';this.status(this.page?'已取消读取，保留上一次完整记录':'已取消读取');this.controls();}
    async apply(row=this.page?.row||this.s.selected?.row){
      if(!this.dialog.open||this.busy)return;
      const headerText=$('recordHeaderRow').value.trim(),headerRow=Number(headerText),columnText=$('recordStartColumn').value.trim(),firstColumn=this.parseColumn(columnText),columns=Number($('recordPageSize').value);
      if(!/^\d+$/.test(headerText)||!Number.isSafeInteger(headerRow)||headerRow<0||headerRow>this.source().maxRows||!columnText||!Number.isSafeInteger(firstColumn)||firstColumn<1||firstColumn>this.s.index?.maxCol||!Number.isSafeInteger(columns)||columns<1||columns>this.limits.columns){this.status('请输入有效字段行和已用范围内的起始列；字段行 0 表示列字母',true);return;}
      await this.load({row,headerRow,firstColumn,columns});
    }
    async load(spec){
      this.controls();if(!this.enabled()||!this.dialog.open||!this.bound||!this.sameSource(this.bound))return;
      this.stop();const serial=this.serial,bound=this.bound,request={...bound.request,...spec},controller=new AbortController();this.abort=controller;this.busy=true;this.closeDetail();this.controls();this.status('正在读取记录…');
      const current=()=>serial===this.serial&&!controller.signal.aborted&&this.dialog.open&&this.sameSource(bound);
      try{
        const body=JSON.stringify(request);if(new TextEncoder().encode(body).length>this.limits.requestBytes)throw Error('记录卡请求超过容量');
        const page=await this.api('/api/record',{method:'POST',body,signal:controller.signal});if(!current())return;
        this.model.validate(page,request,this.limits,this.source(),this.colName,bound.totalColumns);
        const fields=this.build(page);if(!current())return;
        this.page=page;this.accepted=request;this.adopt(fields);this.status('');
      }catch(error){if(current()&&error.name!=='AbortError'){if(!this.page)$('recordCardFields').textContent='未能读取记录，请调整设置后重试。';this.status((this.page?'保留上一次完整记录。':'')+error.message,true);}}
      finally{if(serial===this.serial){this.abort=null;this.busy=false;this.controls();}}
    }
    record(direction){if(!this.page||this.busy||!this.enabled())return;const row=direction<0?this.page.previousRow:this.page.nextRow;if(row)return this.load({...this.accepted,row});}
    fields(direction){
      if(!this.page||this.busy||!this.enabled())return;
      const firstColumn=direction<0?Math.max(1,this.page.firstColumn-this.accepted.columns):this.page.firstColumn+this.page.columns;
      if(firstColumn>=1&&firstColumn<=this.page.totalColumns)return this.load({...this.accepted,firstColumn});
    }
    build(page){
      const fragment=document.createDocumentFragment();
      for(const field of page.fields){
        const card=make('article','record-field');card.dataset.column=String(field.column);card.dataset.ref=field.cell.ref;
        const name=make('div','record-field-name'),column=this.colName(field.column),header=page.headerRow?this.model.value(field.header,'display'):null;
        const label=header===null?(page.headerRow?'列 '+column+'（字段名无保存结果）':'列 '+column):header===''?'列 '+column+'（空白字段名）':this.model.preview(header,this.limits.textUnits);
        name.append(make('span','record-field-label',label),make('small','record-field-column','源列 '+column));
        if(header!==null&&header.length>this.limits.textUnits)name.append(this.button('查看完整字段名',()=>this.full(field,'header'), 'record-header-full'));
        const content=make('div','record-field-content'),text=this.model.value(field.cell,this.mode),value=make('pre','record-field-value',text===null?'公式未保存计算结果':text===''?'（空白）':this.model.preview(text,this.limits.textUnits));
        content.append(value);if(text!==null&&text.length>this.limits.textUnits)content.append(make('span','record-field-preview-note','当前显示首段；查看全文可逐段核对。'));
        const meta=make('div','record-field-meta');meta.append(make('span','record-field-type',this.model.type(field.cell)+(field.cell.hasFormula?' · 公式':'')));
        if(field.cell.hasFormula&&!this.model.formula(field.cell))meta.append(make('span','record-field-notice','公式无法还原'));
        if(field.cell.conditional?.notice)meta.append(make('span','record-field-notice',this.model.preview(field.cell.conditional.notice,this.limits.textUnits)));
        if(field.cell.richText?.notice)meta.append(make('span','record-field-notice',this.model.preview(field.cell.richText.notice,this.limits.textUnits)));
        content.append(meta);const actions=make('div','record-field-actions');
        actions.append(this.button('定位 '+field.cell.ref,()=>this.locate(field.column),'record-field-locate'));
        const copy=this.button('复制'+(this.mode==='raw'?'原值':'显示值'),()=>this.copyField(field),'record-field-copy');copy.disabled=text===null;copy.dataset.unavailable=text===null?'true':'';actions.append(copy);
        const full=this.button('查看全文',()=>this.full(field,'value'),'record-field-full');full.disabled=text===null;full.dataset.unavailable=text===null?'true':'';actions.append(full);
        if(this.model.formula(field.cell))actions.append(this.button('公式文字',()=>this.full(field,'formula'),'record-field-formula'));
        content.append(actions);card.append(name,content);fragment.append(card);
      }
      return fragment;
    }
    button(text,run,cls=''){const b=make('button','mini-button '+cls,text);b.type='button';b.addEventListener('click',run);return b;}
    adopt(fields){
      const page=this.page;$('recordCardFields').replaceChildren(fields);$('recordCardFields').scrollTop=0;
      $('recordCardPosition').textContent=`第 ${page.position.toLocaleString()} / ${page.totalRecords.toLocaleString()} 条 · 源行 ${page.row}`;
      $('recordCardHeader').textContent=page.headerRow?'字段行 '+page.headerRow:'字段名使用列字母';
      $('recordFieldCount').textContent=`${this.colName(page.firstColumn)}–${this.colName(page.firstColumn+page.columns-1)} / ${page.totalColumns.toLocaleString()} 列`;
      $('recordHeaderRow').value=String(page.headerRow);$('recordStartColumn').value=this.colName(page.firstColumn);$('recordPageSize').value=String(this.accepted.columns);
      $('recordCardNotices').textContent=page.notices.map(text=>this.model.preview(text,this.limits.textUnits)).join('；');$('recordCardNotices').hidden=!page.notices.length;
    }
    render(){if(!this.page||!this.sameSource(this.bound))return;this.adopt(this.build(this.page));this.controls();}
    usable(){this.controls();return !!this.page&&this.sameSource(this.bound)&&this.enabled()&&!this.busy;}
    async copyField(field){
      if(!this.usable()||!this.page.fields.includes(field))return;const text=this.model.value(field.cell,this.mode);if(text===null)return;
      const page=this.page,bound=this.bound,mode=this.mode;try{const ok=await this.copy(text);if(this.page===page&&this.sameSource(bound)&&this.mode===mode)this.status(ok===true?'已复制完整'+(mode==='raw'?'原值':'显示值'):'复制失败，请重试',ok!==true);}catch(error){if(this.page===page&&this.sameSource(bound)&&this.mode===mode)this.status('复制失败：'+error.message,true);}
    }
    full(field,kind){
      if(!this.usable()||!this.page.fields.includes(field))return;
      const text=kind==='header'?this.model.value(field.header,'display'):kind==='formula'?this.model.formula(field.cell):this.model.value(field.cell,this.mode);if(text==null)return;
      this.closeDetail();this.detail={text,offsets:this.model.offsets(text,this.limits.textUnits),index:0,page:this.page,bound:this.bound};
      $('recordTextTitle').textContent=kind==='header'?'完整字段名':kind==='formula'?'公式文字':'完整'+(this.mode==='raw'?'原值':'显示值');
      $('recordTextSource').textContent=`${this.s.sheet}!${kind==='header'?field.header.ref:field.cell.ref}`;this.renderText();this.detailDialog.showModal();$('recordTextValue').focus({preventScroll:true});
    }
    renderText(){const d=this.detail;if(!d)return;$('recordTextValue').textContent=d.text.slice(d.offsets[d.index],d.offsets[d.index+1]);$('recordTextPosition').textContent=`第 ${d.index+1} / ${d.offsets.length-1} 段`;$('recordTextPrevious').disabled=d.index===0;$('recordTextNext').disabled=d.index+2===d.offsets.length;$('recordTextStatus').textContent='';}
    textPart(direction){if(!this.detail||!this.usable())return;const d=this.detail,index=d.index+direction;if(index>=0&&index<d.offsets.length-1){d.index=index;this.renderText();}}
    closeDetail(){this.detail=null;if(this.detailDialog?.open)this.detailDialog.close();if($('recordTextValue'))$('recordTextValue').textContent='';}
    async copyDetail(){
      const d=this.detail;if(!d||!this.usable()||this.page!==d.page||!this.sameSource(d.bound))return;
      try{const ok=await this.copy(d.text);if(this.detail===d){$('recordTextStatus').textContent=ok===true?'已复制完整内容':'复制失败，请重试';$('recordTextStatus').setAttribute('role',ok===true?'status':'alert');}}catch(error){if(this.detail===d){$('recordTextStatus').textContent='复制失败：'+error.message;$('recordTextStatus').setAttribute('role','alert');}}
    }
    cancelNavigation(){if(this.navigating){++this.nav;this.navigating=false;}}
    async locate(column){
      if(!this.usable())return;const field=this.page.fields.find(f=>f.column===column);if(!field)return;
      const bound=this.bound,ref=field.cell.ref,nav=++this.nav;this.navigating=true;this.close(true);
      const current=()=>nav===this.nav&&this.sameSource(bound)&&this.canRead();
      try{if(current())await this.navigate(bound.request.sheet,ref,current);}catch(error){if(current())this.toast(error.message,'error');}finally{if(nav===this.nav)this.navigating=false;}
    }
  }
  window.ExcelLensRecordCard=RecordCard;
})();
