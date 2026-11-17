(() => {
  'use strict';
  const $=id=>document.getElementById(id),make=(tag,cls='',text='')=>{const node=document.createElement(tag);node.className=cls;node.textContent=text;return node;};
  const preview=text=>{let end=Math.min(text.length,240);if(end<text.length&&/[\uD800-\uDBFF]/.test(text[end-1]))--end;return text.slice(0,end)+(end<text.length?'…':'');};
  class Transpose {
    constructor({capture,toast,copy,navigate,colName,fontStack,limits,richLimits}){
      Object.assign(this,{capture,toast,copy,navigate,colName,fontStack,limits,richLimits});
      if(!limits?.cells||!limits.bytes||!limits.pageColumns)throw Error('转置容量配置不可用');
      this.model=window.ExcelLensTransposeModel;this.serial=0;this.navigation=0;this.busy=false;this.mode='display';this.start=0;this.columns=1;this.dialog=this.create();
      $('transposeClose').onclick=()=>this.close();$('transposeReload').onclick=()=>this.load();$('transposeCancel').onclick=()=>this.cancel();
      $('transposePrevious').onclick=()=>this.page(-1);$('transposeNext').onclick=()=>this.page(1);
      $('transposeMode').onchange=()=>{this.mode=$('transposeMode').value;this.render();};
      $('transposeCopy').onclick=()=>this.copySelected();$('transposeLocate').onclick=()=>this.locate();
      this.dialog.addEventListener('cancel',event=>{event.preventDefault();this.close();});
      this.dialog.addEventListener('close',()=>{if(!this.dialog.open&&this.snapshot)this.clear();});
      $('transposeTable').addEventListener('keydown',event=>this.key(event));
      this.resize=new ResizeObserver(()=>{if(!this.dialog.open||!this.data)return;const size=this.model.pageSize($('transposeBody').clientWidth,this.limits.pageColumns);if(size!==this.columns){this.columns=size;this.start=Math.floor(this.selected.index/size)*size;this.render();}});this.resize.observe($('transposeBody'));
      for(const name of ['pointerdown','keydown'])document.addEventListener(name,()=>{++this.navigation;},true);
    }
    create(){
      const d=make('dialog','float-dialog transpose-dialog');d.id='transposeDialog';d.setAttribute('aria-labelledby','transposeTitle');
      d.innerHTML='<div class="dialog-head"><div id="transposeTitle" class="dialog-title">转置阅读</div><button id="transposeClose" class="dialog-close" type="button" aria-label="关闭转置阅读">×</button></div><div class="transpose-tools"><span id="transposeSource"></span><select id="transposeMode" class="field-control" aria-label="显示内容"><option value="display">显示值</option><option value="raw">原值</option><option value="formula">公式</option></select><button id="transposeReload" type="button" class="mini-button">重新读取</button><button id="transposeCancel" type="button" class="mini-button" hidden>取消</button></div><div id="transposeBody" class="transpose-body"><table id="transposeTable" role="grid" aria-label="转置选区"></table></div><div class="transpose-pager"><button id="transposePrevious" class="mini-button" type="button" disabled>上一页</button><span id="transposePosition"></span><button id="transposeNext" class="mini-button" type="button" disabled>下一页</button></div><section id="transposeDetail" class="transpose-detail" hidden><div class="transpose-detail-head"><strong id="transposeRef"></strong><span id="transposeType"></span><span class="status-spacer"></span><button id="transposeCopy" class="mini-button" type="button">复制</button><button id="transposeLocate" class="mini-button" type="button">定位原格</button></div><pre id="transposeValue" tabindex="0"></pre><p id="transposeNotice" hidden></p></section><p id="transposeStatus" class="transpose-status" role="status" aria-live="polite" hidden></p>';
      document.body.append(d);return d;
    }
    status(text,error=false){$('transposeStatus').textContent=text;$('transposeStatus').hidden=!text;$('transposeStatus').classList.toggle('is-error',error);}
    controls(){
      $('transposeReload').disabled=this.busy;$('transposeCancel').hidden=!this.busy;$('transposeMode').disabled=this.busy||!this.data;
      $('transposePrevious').disabled=this.busy||!this.data||this.start===0;$('transposeNext').disabled=this.busy||!this.data||this.start+this.columns>=this.data.rows.length;
      $('transposeCopy').disabled=this.busy||!this.selectedItem||this.model.value(this.selectedItem,this.mode,this.snapshot?.styled)===null;$('transposeLocate').disabled=this.busy||!this.selectedItem;
    }
    open(){
      if(this.dialog.open)return;
      try{this.snapshot=this.capture({purpose:'转置',limits:this.limits});}catch(error){this.toast(error.message,'warning');return;}
      this.start=0;this.selected=null;this.data=null;this.mode='display';$('transposeMode').value=this.mode;$('transposeSource').textContent=this.snapshot.sheet+'!'+this.snapshot.start+':'+this.snapshot.end;
      this.dialog.showModal();this.load();
    }
    stop(){++this.serial;this.abort?.abort();this.abort=null;this.busy=false;}
    clear(){this.stop();++this.navigation;this.snapshot=this.data=this.selected=this.selectedItem=null;$('transposeTable').replaceChildren();$('transposeValue').textContent='';$('transposeNotice').textContent='';$('transposeDetail').hidden=true;$('transposePosition').textContent='';this.status('');}
    close(){this.clear();if(this.dialog.open)this.dialog.close();}
    resetBook(){this.close();}
    changed(){if(this.snapshot&&!this.snapshot.current())this.close();}
    cancel(){this.stop();this.status('已取消');this.controls();}
    async load(){
      if(!this.snapshot||!this.dialog.open||this.busy)return;
      this.changed();if(!this.snapshot)return;
      const snapshot=this.snapshot;this.stop();const serial=this.serial,controller=new AbortController();this.abort=controller;this.busy=true;this.controls();this.status('正在读取…');
      const current=()=>serial===this.serial&&!controller.signal.aborted&&this.dialog.open&&snapshot===this.snapshot&&snapshot.current();
      try{
        const payload=await snapshot.read({force:true,richText:snapshot.styled,conditional:snapshot.styled},controller.signal);if(!current())return;
        const data=this.model.validate(payload,snapshot,this.limits,this.colName);if(!current())return;
        this.data=data;this.columns=this.model.pageSize($('transposeBody').clientWidth,this.limits.pageColumns);this.start=0;this.selected={column:data.firstColumn,index:0};this.render();this.status('');
      }catch(error){if(current()&&error.name!=='AbortError')this.status(error.message+(this.data?'（保留上次内容）':''),true);}
      finally{if(serial===this.serial){this.abort=null;this.busy=false;this.controls();}}
    }
    page(direction){if(!this.data||this.busy)return;this.start=Math.max(0,Math.min(Math.floor((this.data.rows.length-1)/this.columns)*this.columns,this.start+direction*this.columns));this.selected.index=this.start;this.render();}
    render(focus=false){
      this.changed();if(!this.data||!this.dialog.open)return;
      const state={showStyles:this.snapshot.styled,showFormulas:false,fontOverride:this.snapshot.fontOverride,styles:this.snapshot.styles},fragment=document.createDocumentFragment(),head=make('thead'),line=make('tr'),width=Math.min(this.columns,this.data.rows.length-this.start),warnings=new Set();
      line.append(make('th','','源列 / 源行'));
      for(let index=this.start;index<this.start+width;index++){const th=make('th','',String(this.data.rows[index].number));th.scope='col';line.append(th);}head.append(line);fragment.append(head);const body=make('tbody');
      let presenter;presenter=new window.ExcelLensCellView.Presenter({state,richLimits:this.richLimits,fontStack:this.fontStack,scale:()=>1,colName:this.colName,applyStyle:(node,cell)=>{
        node.style.cssText=window.ExcelLensClipboard.styleOf(cell,state.styles,state.showStyles,state.fontOverride);
        // Explicit source ink/fill belongs to the workbook's white paper, not
        // the surrounding dark UI. Preserve specified colors and supply only
        // their missing default counterpart, like the detached paper painter.
        if(state.showStyles){const ink=!!node.style.color,fill=!!node.style.backgroundColor;if(fill&&!ink)node.style.color='#000000';if(ink&&!fill)node.style.backgroundColor='#ffffff';}
        if(!node.style.textAlign)node.style.textAlign=cell?.type==='n'||!cell?.type&&!window.ExcelLensCellView.isText(cell)?'right':'left';
      }});
      const items=[];for(let column=this.data.firstColumn;column<=this.data.lastColumn;column++)for(let index=this.start;index<this.start+width;index++)items.push(this.model.at(this.data,column,index,this.colName));
      presenter.prepare(items.map(item=>({style:item.style,cells:item.cell?[item.cell]:[]})));
      let position=0;
      for(let column=this.data.firstColumn;column<=this.data.lastColumn;column++){
        const tr=make('tr'),th=make('th','',this.colName(column));th.scope='row';tr.append(th);
        for(let index=this.start;index<this.start+width;index++){
          const item=items[position++],cell=item.cell,value=this.model.value(item,this.mode,state.showStyles),text=value===null?(this.mode==='formula'?'公式不可还原':'无保存结果'):value;
          const td=make('td'),button=make('button','transpose-cell'),node=make('span','transpose-cell-value');button.type='button';button.dataset.column=String(column);button.dataset.index=String(index);button.dataset.ref=item.ref;
          node.dataset.row=String(item.row);node.dataset.col=String(column);node.dataset.ref=item.ref;
          if(cell){
            // The proxy exists only for bounded DOM text. The original cell is
            // retained unchanged in the model and in the full-content panel.
            const clipped=preview(text),rich=this.mode==='display'&&value!==null&&clipped===text;
            const painted=rich?cell:{...cell,display:clipped,richText:undefined,conditional:cell.conditional?{...cell.conditional,display:clipped}:undefined};
            presenter.paint(node,painted,{style:item.style});
            if(value===null||this.mode!=='display'||clipped!==text)node.textContent=clipped;
          }else{presenter.paint(node,null,{style:item.style});presenter.o.applyStyle(node,{style:item.style});}
          for(const notice of [item.notice,node._xlRichNotice,node._xlPresentationNotice])if(notice)warnings.add(notice);
          button.classList.toggle('is-selected',this.selected.column===column&&this.selected.index===index);button.setAttribute('aria-label',item.ref+' '+(cell?window.ExcelLensCellView.sourceType(cell):'空白'));button.setAttribute('aria-selected',String(this.selected.column===column&&this.selected.index===index));
          button.onclick=()=>{this.selected={column,index};this.detail();for(const other of $('transposeTable').querySelectorAll('.transpose-cell')){const active=other===button;other.classList.toggle('is-selected',active);other.setAttribute('aria-selected',String(active));}};
          button.append(node);td.append(button);tr.append(td);
        }
        body.append(tr);
      }
      fragment.append(body);$('transposeTable').replaceChildren(fragment);$('transposePosition').textContent=`${this.start+1}–${this.start+width} / ${this.data.rows.length}`;this.detail();this.controls();
      if(warnings.size){$('transposeNotice').textContent=[...warnings].join('；');$('transposeNotice').hidden=false;}
      if(focus)$('transposeTable').querySelector(`[data-column="${this.selected.column}"][data-index="${this.selected.index}"]`)?.focus({preventScroll:true});
    }
    detail(){
      if(!this.data||!this.selected)return;const item=this.model.at(this.data,this.selected.column,this.selected.index,this.colName),value=this.model.value(item,this.mode,this.snapshot.styled);this.selectedItem=item;
      $('transposeDetail').hidden=false;$('transposeRef').textContent=item.ref;$('transposeType').textContent=item.cell?window.ExcelLensCellView.sourceType(item.cell):'空白';$('transposeValue').textContent=value===null?(this.mode==='formula'?'公式不可还原':'公式无保存结果'):value;
      const notes=[item.notice,item.cell?.conditional?.notice].filter(Boolean);$('transposeNotice').textContent=notes.join('；');$('transposeNotice').hidden=!notes.length;this.controls();
    }
    key(event){
      if(!event.target.closest?.('.transpose-cell')||!this.data||this.busy)return;
      if((event.ctrlKey||event.metaKey)&&event.key.toLowerCase()==='c'){event.preventDefault();event.stopPropagation();this.copySelected();return;}
      const directions={ArrowLeft:[0,-1],ArrowRight:[0,1],ArrowUp:[-1,0],ArrowDown:[1,0]},step=directions[event.key];if(!step)return;event.preventDefault();event.stopPropagation();
      this.selected={column:Math.max(this.data.firstColumn,Math.min(this.data.lastColumn,this.selected.column+step[0])),index:Math.max(0,Math.min(this.data.rows.length-1,this.selected.index+step[1]))};this.start=Math.floor(this.selected.index/this.columns)*this.columns;this.render(true);
    }
    async copySelected(){
      if(!this.selectedItem||this.busy||!this.snapshot?.current())return;const value=this.model.value(this.selectedItem,this.mode,this.snapshot.styled);if(value===null)return;
      try{await this.copy(value);}catch(error){this.toast(error.message,'error');}
    }
    async locate(){
      if(!this.selectedItem||this.busy||!this.snapshot?.current())return;const snapshot=this.snapshot,ref=this.selectedItem.ref;this.close();const ticket=++this.navigation;
      const guard=()=>!this.dialog.open&&ticket===this.navigation&&snapshot.sourceCurrent();
      try{await this.navigate(snapshot.sheet,ref,guard);}catch(error){if(guard())this.toast(error.message,'error');}
    }
  }
  window.ExcelLensTranspose=Transpose;
})();
