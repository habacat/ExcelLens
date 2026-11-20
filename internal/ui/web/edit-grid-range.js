(() => {
  'use strict';
  const model = window.ExcelLensEditModel;
  const bounds = area => ({
    top:Math.min(area.row1, area.row2), bottom:Math.max(area.row1, area.row2),
    left:Math.min(area.col1, area.col2), right:Math.max(area.col1, area.col2)
  });

  class GridRangeEditor {
    constructor(editor) {
      this.grid = editor;
      this.owner = editor.owner;
      this.view = editor.view;
      this.menu = document.getElementById('editGridMenu');
      this.menu.addEventListener('click',event=>{
        const action=event.target.closest('button')?.dataset.editAction;
        if (!action) return;
        this.menu.hidden=true; editor.focus();
        if (action==='cut') editor.copyForCut();
        if (action==='copy') this.view.copy();
        if (action==='resize-table') this.owner.advanced('Table');
        if (action==='paste') editor.act(activity=>this.pasteClipboard(activity),true,'正在粘贴选区');
        if (action==='clear') editor.act(()=>this.clear(),true,'正在清除选区');
        if (action==='down' || action==='right') editor.act(()=>this.fill('fill-'+action),true,'正在填充选区');
      });
      this.menu.addEventListener('keydown',event=>{
        if (event.key==='Escape') { this.menu.hidden=true; editor.focus(); }
        else if (event.key==='ArrowDown' || event.key==='ArrowUp') {
          const buttons=[...this.menu.querySelectorAll('button:enabled:not([hidden])')], i=buttons.indexOf(document.activeElement);
          buttons[(i+buttons.length+(event.key==='ArrowDown' ? 1 : -1))%buttons.length].focus();
        } else return;
        event.preventDefault(); event.stopPropagation();
      });
      this.menu.addEventListener('focusout',event=>{ if (!this.menu.contains(event.relatedTarget)) this.menu.hidden=true; });
    }
    showMenu(x,y) {
      if (this.grid.blocked || this.grid.stale) return;
      this.menu.querySelector('[data-edit-action="cut"]').disabled = !!this.owner.data.encoding || this.view.filtered() || this.view.areas().length!==1;
      for (const action of ['down','right']) this.menu.querySelector('[data-edit-action="'+action+'"]').disabled = this.view.filtered() || this.view.areas().length!==1;
      this.menu.querySelector('[data-edit-action="resize-table"]').hidden=!this.owner.tools.tableAt(this.grid.selected);
      this.view.positionMenu(this.menu,x,y);
      this.menu.querySelector('button').focus({preventScroll:true});
    }
    typedClipboard(raw,html) {
      return window.ExcelLensGridPaste.identify(raw,html,this.copied,this.owner.limits.requestBytes);
    }
    async pasteClipboard(activity) {
      const target=this.pasteTarget(), selection=this.rectangle(true);
      let raw='', html='';
      try {
        if (navigator.clipboard.read) {
          for (const item of await navigator.clipboard.read()) {
            if (!item.types.includes('text/plain')) continue;
            raw=await (await item.getType('text/plain')).text();
            if (item.types.includes('text/html')) html=await (await item.getType('text/html')).text();
            break;
          }
        } else raw=await navigator.clipboard.readText();
      } catch { throw Error('浏览器未读取剪贴板，请按 Ctrl+V 粘贴'); }
      this.current(target);
      const now=this.rectangle(true);
      if (Object.keys(selection).some(edge=>selection[edge]!==now[edge])) throw Error('选区已变化，请重新粘贴');
      const typed=this.typedClipboard(raw,html);
      if (!raw && !typed) throw Error('剪贴板没有可粘贴的单元格或文字');
      return this.paste(raw,typed,activity);
    }
    target(copyOnly=false) {
      if (!this.grid.active || !this.view.ready() || !this.grid.selected || this.grid.stale || this.owner.pending) throw Error('请先核对当前修订');
      if (!copyOnly && this.grid.shape?.readOnly) throw Error(this.grid.shape.readOnly);
      const {id,binding,revision} = this.owner.data.document;
      return {document:{id,binding}, revision, sheet:this.grid.selected.sheet};
    }
    pasteTarget() {
      const target=this.target();
      Object.defineProperty(target,'selection',{value:this.view.clipboardIdentity()});
      return target;
    }
    current(target) {
      if (target.selection && (target.selection !== this.view.clipboardIdentity() || this.grid.queue.some(task => task.navigate))) {
        throw Error('选区或筛选已变化，请重新粘贴');
      }
      const current = this.target();
      if (current.sheet !== target.sheet || current.document.id !== target.document.id ||
          current.document.binding !== target.document.binding || current.revision !== target.revision) throw Error('选区或修订已变化，请重新操作');
    }
    rectangle(allowVisible=false) {
      const ranges = this.view.areas();
      if (ranges.length !== 1) throw Error('请选择一个连续区域');
      if (!allowVisible && this.view.filtered() && (ranges[0].visual1 !== ranges[0].visual2 || ranges[0].col1 !== ranges[0].col2)) {
        throw Error('请清除筛选或排序后按原地址填充或粘贴');
      }
      const rect = bounds(ranges[0]), merge = this.view.mergeAt(rect.top,rect.left);
      if (merge && merge.row === rect.top && merge.col === rect.left && merge.endRow === rect.bottom && merge.endCol === rect.right) {
        return {...rect,bottom:rect.top,right:rect.left};
      }
      return rect;
    }
    checkSize(top, bottom, left, right) {
      if ((bottom-top+1)*(right-left+1) > this.owner.limits.batchCells) throw Error('区域超出一次编辑容量，请缩小范围');
      const shape = this.grid.shape;
      if (top < shape.firstRow || left < shape.firstCol || bottom > shape.rows || right > shape.cols) throw Error('选区超出可编辑区域');
    }
    async cycle(key, backward) {
      const ranges = this.view.areas();
      if (ranges.length !== 1 || this.view.filtered()) return false;
      const {top,bottom,left,right} = bounds(ranges[0]);
      if (top === bottom && left === right) return false;
      const horizontal = key === 'Tab', view = this.view;
      const only = view.mergeAt(top,left);
      if (only && only.endRow >= bottom && only.endCol >= right) return false;
      const advance = (value,lo,hi,next,previous) => {
        const n = backward ? previous(value-1) : next(value+1);
        if (n >= lo && n <= hi && n !== value) return [n,false];
        return [backward ? previous(hi) : next(lo),true];
      };
      let {row,col} = this.grid.selected;
      const start = {row,col};
      do {
        const merge = view.mergeAt(row,col);
        let wrapped;
        if (horizontal) {
          if (merge) col = backward ? merge.col : merge.endCol;
          [col,wrapped] = advance(col,left,right,view.nextCol,view.previousCol);
          if (wrapped) {
            if (merge && merge.col <= left && merge.endCol >= right) row = backward ? merge.row : merge.endRow;
            [row] = advance(row,top,bottom,view.nextRow,view.previousRow);
          }
        } else {
          if (merge) row = backward ? merge.row : merge.endRow;
          [row,wrapped] = advance(row,top,bottom,view.nextRow,view.previousRow);
          if (wrapped) {
            if (merge && merge.row <= top && merge.endRow >= bottom) col = backward ? merge.col : merge.endCol;
            [col] = advance(col,left,right,view.nextCol,view.previousCol);
          }
        }
        if (row < top || row > bottom || col < left || col > right) return true;
        const owner = view.mergeAt(row,col);
        if (!owner || row === owner.row && col === owner.col) break;
        if (backward && (horizontal ? row === owner.row : col === owner.col)) {
          row = owner.row; col = owner.col; break;
        }
      } while (row !== start.row || col !== start.col);
      await view.moveInside(row,col);
      return true;
    }
    async clear() {
      const target = this.target(), sheet = target.sheet, changes = new Map();
      const add = (row,col) => {
        const merge = this.view.mergeAt(row,col);
        if (merge && (merge.row !== row || merge.col !== col)) return;
        changes.set(row+','+col,{sheet,row,col,value:{kind:'blank',raw:''}});
        if (changes.size > this.owner.limits.batchCells) throw Error('选区超出一次编辑容量，请缩小范围');
      };
      const ranges = this.view.areas();
      if (this.view.filtered()) {
        const snapshot = this.view.visibleSelection();
        const data = await snapshot.read({richText:false,conditional:false});
        if (!snapshot.current()) throw Error('选区已变化，请重新操作');
        for (const row of data.rows) {
          for (let col=snapshot.firstColumn; col<=snapshot.lastColumn; col++) add(row.row,col);
        }
      } else {
        for (const range of ranges) {
          const {top,bottom,left,right} = bounds(this.view.expand(range));
          this.checkSize(top,bottom,left,right);
          for (let row=top; row<=bottom; row++) {
            for (let col=left; col<=right; col++) add(row,col);
          }
        }
      }
      this.current(target);
      return this.owner.commit('change', model.bounded([...changes.values()],this.owner.limits), {direct:true});
    }
    async fill(mode, input) {
      const target = this.target();
      if (this.view.filtered() && mode !== 'fill-entry') throw Error('请清除筛选或排序后按原地址填充');
      const {top,bottom,left,right} = this.rectangle();
      this.checkSize(top,bottom,left,right);
      const ref = (r,c) => this.owner.colName(c)+r;
      const args = {...target,mode,ref:ref(top,left),end:ref(bottom,right),direct:true};
      if (input) args.operation = {changes:[input]};
      const preview = await this.owner.post('preview',args);
      this.current(target);
      if (!preview.changes?.length) return true;
      const entryAnchor = input && {sheet:input.sheet,row:input.row,col:input.col};
      return this.owner.commit('change',preview.changes,{proof:preview.proof,direct:true,inferNumberFormat:!!input,entryAnchor});
    }
    hasMerge(top,bottom,left,right) {
      for (let row=top; row<=bottom; row++) {
        for (let col=left; col<=right; col++) if (this.view.mergeAt(row,col)) return true;
      }
      return false;
    }
    cancelCut() {
      this.copyEpoch=(this.copyEpoch||0)+1;
      if (!this.copied?.cut || this.copied.inactive) return;
      this.copied.inactive=true;
      this.view.redraw();
      this.grid.status('');
    }
    paintCut(node,sheet,row,col) {
      const source=this.copied;
      let mask=0;
      if (source?.cut && !source.inactive && source.sheet===sheet && source.revision===this.owner.data?.document.revision &&
          row>=source.first.row && row<=source.last.row && col>=source.first.col && col<=source.last.col) {
        const merge=this.view.mergeAt(row,col);
        mask=(row===source.first.row?1:0)|((merge?.endCol??col)===source.last.col?2:0)|
          ((merge?.endRow??row)===source.last.row?4:0)|(col===source.first.col?8:0);
      }
      if (mask===node._xlCutMask || !mask && !node._xlCutMask) return;
      node._xlCutMask=mask;
      node.classList.toggle('cut-source',!!mask);
      if (mask) node.style.setProperty('--cut-edges',[1,2,4,8].map(bit=>mask&bit?'2px':'0').join(' '));
      else node.style.removeProperty('--cut-edges');
    }
    async copySnapshot(snapshot,cut=false,visibleRows) {
      if (this.owner.data.encoding || cut && this.view.filtered() || snapshot.rows*(snapshot.lastColumn-snapshot.firstColumn+1)>this.owner.limits.batchCells) return null;
      const first = model.target(snapshot.start,this.grid.shape), last = model.target(snapshot.end,this.grid.shape);
      if (!visibleRows && last.row-first.row+1 !== snapshot.rows) return null;
      const target = this.target(!cut), merged=!visibleRows && this.hasMerge(first.row,last.row,first.col,last.col);
      const source={marker:'excellens-copy-'+crypto.randomUUID(),cut,merged,first,last,sheet:target.sheet,
        start:snapshot.start,end:snapshot.end,id:target.document.id,binding:target.document.binding,revision:target.revision};
      try {
        const clip = await this.owner.post('copy',{...target,ref:snapshot.start,end:snapshot.end,mode:'grid',visibleRows});
        return {...source,clip};
      } catch (error) {
        // The normal HTML/TSV copy still works for a large typed payload.
        if (error.status === 413) return merged || cut ? source : null;
        throw error;
      }
    }
    async paste(raw,typed,activity) {
      const target=this.pasteTarget();
      const rect=this.rectangle(true), {top,bottom,left,right}=rect;
      const {limits,data} = this.owner, textFile = !!data.encoding;
      const local=typed && typed.id===target.document.id;
      if (!textFile && (!local || this.view.mappedRows() && !typed?.cut || typed?.clip?.portable?.range.sourceRows)) {
        let portable=typed?.portable || typed?.clip?.portable;
        if(typed?.external)portable=await window.ExcelLensGridPaste.native(this.owner,target,raw);
        this.current(target);
        if(portable)return window.ExcelLensGridPaste.pasteRange(this,target,rect,portable,activity);
      }
      if (local && !textFile) {
        const ref = this.owner.colName(left)+top;
        const end = top===bottom && left===right ? undefined : this.owner.colName(right)+bottom;
        if (typed.inactive) return true;
        if (typed.binding!==target.document.binding) throw Error('副本已切换，请重新复制');
        const height=typed.last.row-typed.first.row+1, width=typed.last.col-typed.first.col+1;
        if (this.view.filtered() && (end || height>1 || typed.cut)) throw Error('请清除筛选或排序后按原地址粘贴');
        const lastRow=typed.cut?top+height-1:Math.max(bottom,top+height-1);
        const lastCol=typed.cut?left+width-1:Math.max(right,left+width-1);
        this.checkSize(top,lastRow,left,lastCol);
        if (typed.cut || typed.merged || typed.structural || this.hasMerge(top,lastRow,left,lastCol)) {
          if ((typed.acceptedRevision??typed.revision)!==target.revision) throw Error('来源修订已变化，请重新复制或剪切');
          const structure={kind:typed.cut?'cutRange':'copyRange',sheet:typed.sheet,range:typed.start+':'+typed.end,
            destinationSheet:target.sheet,destination:end?ref+':'+end:ref,sourceRevision:typed.revision};
          if (target.sheet===typed.sheet && ref===typed.start && (!end || end===typed.end) && target.revision===typed.revision) {
            if (typed.cut) this.cancelCut();
            return true;
          }
          const ok=await this.owner.commit('structure',undefined,{structure,direct:true});
          if (ok) {
            typed.inactive=typed.cut;
            typed.structural=true;
            typed.acceptedRevision=this.owner.data.document.revision;
            this.copied=typed;
            this.view.redraw();
          }
          return ok;
        }
        const plan = await this.owner.post('preview',{...target,mode:'all',ref,end,clipboard:{...typed.clip,portable:undefined},direct:true});
        this.current(target);
        if (!plan.changes?.length) return true;
        return this.owner.commit('change',plan.changes,{proof:plan.proof,direct:true,expandTable:!this.view.filtered()});
      }
      if (this.copied?.cut) this.cancelCut();
      const rows = /[\r\n\t]/.test(raw) ? window.ExcelLensClipboard.parse(raw,{
        maxRows:limits.batchCells,maxCols:this.grid.shape.cols,maxCells:limits.batchCells,
        maxCellLength:limits.cellBytes,maxTextLength:limits.requestBytes
      }) : [[raw]];
      const height = rows.length, width = Math.max(...rows.map(r=>r.length));
      const dest=await window.ExcelLensGridPaste.destination(this,rect,height,width);
      this.current(target);
      const h=dest.rows.length,w=dest.width;
      const addresses = [], values = [];
      for (let i=0; i<h; i++) {
        for (let j=0; j<w; j++) {
          addresses.push({sheet:target.sheet,row:dest.rows[i],col:left+j});
          values.push(rows[i%height][j%width] ?? '');
        }
      }
      const kinds = textFile ? [] : await this.owner.post('input-kinds',{...target,cells:addresses});
      this.current(target);
      function* changes() {
        for (const [i,a] of addresses.entries()) yield {...a,value:model.gridValue(values[i],'auto',kinds[i],textFile,limits)};
      }
      return this.owner.commit('change', model.bounded(changes(),limits), {direct:true,inferNumberFormat:true,expandTable:!this.view.filtered()});
    }
  }
  window.ExcelLensGridRangeEditor = GridRangeEditor;
})();
