(() => {
  'use strict';
  const $=id=>document.getElementById(id),special=window.ExcelLensClipboardSpecial;
  function status(id,text,error=false){const node=$(id);node.textContent=text;node.setAttribute('role',error?'alert':'status');node.classList.toggle('has-error',error);}
  class CopySpecial {
    constructor({capture,copy,toast,limits}){
      Object.assign(this,{capture,copy,toast,limits});this.serial=0;this.dialog=$('copySpecialDialog');
      $('copySpecialClose').onclick=()=>this.dialog.close();$('copySpecialReload').onclick=()=>this.load();
      $('copySpecialForm').onsubmit=event=>{event.preventDefault();this.write();};
      for(const id of ['copySpecialFormat','copySpecialMode'])$(id).onchange=()=>this.render();
      for(const id of ['copySpecialHeader','copySpecialHeaderRow'])$(id).onchange=()=>this.load();
      this.dialog.addEventListener('close',()=>{if(this.dialog.open)return;++this.serial;this.abort?.abort();this.payload=null;this.result=null;this.snapshot=null;});
    }
    options(){return {format:$('copySpecialFormat').value,mode:$('copySpecialMode').value,includeHeader:$('copySpecialHeader').checked,styled:this.snapshot?.styled,sheet:this.snapshot?.sheet,mergeAt:this.snapshot?.mergeAt};}
    open(){
      try{this.snapshot=this.capture();}catch(error){this.toast(error.message,'warning');return;}
      $('copySpecialSource').textContent=this.snapshot.label;$('copySpecialHeaderRow').value=String(this.snapshot.headerRow);$('copySpecialHeaderRow').max=String(this.snapshot.maxRow);
      $('copySpecialLimits').textContent=`最多 ${this.limits.rows} 行、${this.limits.columns} 列、${this.limits.cells.toLocaleString()} 格（含表头）`;
      this.dialog.showModal();this.load();
    }
    async load(){
      if(!this.snapshot)return;
      const serial=++this.serial;this.abort?.abort();const controller=new AbortController();this.abort=controller;
      this.payload=null;this.result=null;$('copySpecialWrite').disabled=true;$('copySpecialPreview').value='';
      const included=$('copySpecialHeader').checked,headerRow=included?Number($('copySpecialHeaderRow').value):0;$('copySpecialHeaderRow').disabled=!included;
      if(included&&(!Number.isSafeInteger(headerRow)||headerRow<1||headerRow>this.snapshot.maxRow)){status('copySpecialStatus','请提供有效的源表头行',true);return;}
      status('copySpecialStatus','正在读取选区…');
      try{
        const payload=await this.snapshot.read({headerRow,richText:false,conditional:this.snapshot.styled},controller.signal);
        if(serial!==this.serial||!this.dialog.open)return;
        if(!this.snapshot.current())throw Error('来源或选区已变化，请关闭后重新选择');
        this.payload=payload;this.render();
      }catch(error){if(serial===this.serial&&(error.name!=='AbortError'||!this.snapshot?.current()))status('copySpecialStatus',error.message,true);}
      finally{if(this.abort===controller)this.abort=null;}
    }
    render(){
      $('copySpecialMode').disabled=$('copySpecialFormat').value==='json';
      if(!this.payload)return;
      this.result=null;$('copySpecialWrite').disabled=true;
      try{
        if(!this.snapshot.current())throw Error('来源或选区已变化，请关闭后重新选择');
        const result=special.selection(this.payload,this.options(),this.limits);this.result=result;
        $('copySpecialPreview').value=result.text.slice(0,10000);
        const notes=[`${result.rows} 行数据 · ${result.columns} 列`,result.text.length>10000?'预览仅显示前10,000字符；复制包含完整内容':'',...result.notices].filter(Boolean);
        status('copySpecialStatus',notes.join('；'));$('copySpecialWrite').disabled=false;
      }catch(error){$('copySpecialPreview').value='';status('copySpecialStatus',error.message,true);}
    }
    async write(){
      if(!this.result||!this.snapshot?.current()){status('copySpecialStatus','来源或选区已变化，请关闭后重新选择',true);return;}
      const serial=this.serial;
      $('copySpecialWrite').disabled=true;
      try{const copied=await this.copy(this.result);if(serial!==this.serial||!this.dialog.open)return;if(copied!==false)this.dialog.close();else status('copySpecialStatus','复制未完成，可重试或手动选择预览文字',true);}
      catch(error){if(serial===this.serial)status('copySpecialStatus',error.message,true);}
      finally{if(serial===this.serial&&this.dialog.open)$('copySpecialWrite').disabled=!this.result||!this.snapshot?.current();}
    }
  }
  class PasteSpecial {
    constructor({capture,commit,toast,refresh}){
      Object.assign(this,{capture,commit,toast,refresh});this.serial=0;this.dialog=$('pasteSpecialDialog');this.busy=false;
      $('pasteSpecialClose').onclick=()=>this.dialog.close();$('pasteSpecialRead').onclick=()=>this.readClipboard();$('pasteSpecialReload').onclick=()=>this.reload();
      $('pasteSpecialForm').onsubmit=event=>{event.preventDefault();this.apply();};
      $('pasteSpecialText').oninput=()=>{this.textIntent=(this.textIntent||0)+1;this.plan=null;$('pasteSpecialApply').disabled=true;clearTimeout(this.timer);this.timer=setTimeout(()=>this.preview(),120);};
      for(const id of ['pasteSpecialFormat','pasteSpecialMode','pasteSpecialHeader','pasteSpecialSkipFirst','pasteSpecialTranspose','pasteSpecialSkipBlank'])$(id).onchange=()=>this.preview();
      this.dialog.addEventListener('cancel',event=>{if(this.busy)event.preventDefault();});
      this.dialog.addEventListener('close',()=>{if(this.dialog.open)return;++this.serial;clearTimeout(this.timer);this.context=null;this.plan=null;});
    }
    open(context){
      if(this.busy)return;
      try{this.context=context||this.capture();}catch(error){this.toast(error.message,'warning');return;}
      ++this.serial;$('pasteSpecialTarget').textContent=this.context.label;this.dialog.showModal();this.preview();$('pasteSpecialText').focus();
    }
    options(){return {format:$('pasteSpecialFormat').value,mode:$('pasteSpecialMode').value,withHeader:$('pasteSpecialHeader').checked,skipFirstRow:$('pasteSpecialFormat').value==='tsv'&&$('pasteSpecialSkipFirst').checked,transpose:$('pasteSpecialTranspose').checked,skipBlank:$('pasteSpecialSkipBlank').checked,startRow:this.context.row,startCol:this.context.col,columns:this.context.columns};}
    preview(){
      if(!this.context||!this.dialog.open||this.busy)return;
      this.plan=null;$('pasteSpecialApply').disabled=true;$('pasteSpecialPreview').replaceChildren();
      const json=$('pasteSpecialFormat').value==='json';$('pasteSpecialMode').disabled=!json;$('pasteSpecialHeader').disabled=!json;$('pasteSpecialSkipFirst').disabled=json;
      try{
        if(!this.context.current())throw Error('目标表、位置或版本已变化，请重新读取目标或关闭后重选');
        const text=$('pasteSpecialText').value;if(!text){status('pasteSpecialStatus','粘贴文字到上方文本框，或点击“读取剪贴板”。');return;}
        const plan=special.pastePlan(text,this.options());this.plan=plan;
        const body=document.createElement('tbody');for(const row of plan.preview){const tr=document.createElement('tr');for(const value of row){const td=document.createElement('td');td.textContent=Array.from(value).slice(0,160).join('')+(Array.from(value).length>160?'…':'');td.title=value;tr.append(td);}body.append(tr);}$('pasteSpecialPreview').append(body);
        status('pasteSpecialStatus',`${plan.rows} 行 × ${plan.columns} 列 · 将写入 ${plan.changes.length.toLocaleString()} 格`+(plan.skipped?` · 跳过 ${plan.skipped} 个空白`:'')+'；预览最多6行、8列');$('pasteSpecialApply').disabled=!plan.changes.length;
      }catch(error){status('pasteSpecialStatus',error.message,true);}
    }
    async readClipboard(){
      const serial=this.serial,intent=this.textIntent=(this.textIntent||0)+1;try{if(!navigator.clipboard?.readText)throw Error('浏览器不支持读取剪贴板，请在文本框中按Ctrl+V');const text=await navigator.clipboard.readText();if(serial!==this.serial||intent!==this.textIntent||!this.dialog.open)return;$('pasteSpecialText').value=text;this.preview();}
      catch(error){if(serial===this.serial&&intent===this.textIntent)status('pasteSpecialStatus','读取失败：'+error.message+'；也可在文本框中按Ctrl+V',true);}
    }
    async reload(){
      if(this.busy||!this.context)return;const serial=this.serial,previous=this.context;
      try{await this.refresh(previous);if(serial!==this.serial||!this.dialog.open)return;this.context=this.capture(previous);$('pasteSpecialTarget').textContent=this.context.label;this.preview();}
      catch(error){if(serial===this.serial)status('pasteSpecialStatus',error.message,true);}
    }
    async apply(){
      if(this.busy)return;this.preview();if(!this.plan?.changes.length||!this.context?.current())return;
      const serial=this.serial,context=this.context,changes=this.plan.changes;this.busy=true;$('pasteSpecialFields').disabled=true;$('pasteSpecialClose').disabled=true;$('pasteSpecialApply').disabled=true;status('pasteSpecialStatus','正在整批保存…');
      try{await this.commit(context,changes);if(serial===this.serial){this.dialog.close();this.toast(`已粘贴 ${changes.length.toLocaleString()} 格，可整批撤销`,'success');}}
      catch(error){if(serial===this.serial)status('pasteSpecialStatus',error.message,true);}
      finally{this.busy=false;$('pasteSpecialFields').disabled=false;$('pasteSpecialClose').disabled=false;if(this.dialog.open)$('pasteSpecialApply').disabled=true;}
    }
  }
  window.ExcelLensCopySpecial=CopySpecial;window.ExcelLensPasteSpecial=PasteSpecial;
})();
