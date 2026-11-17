/* ExcelLens frozen-pane renderer. No frameworks, no workbook mutations.
 * The main grid owns the scrollbars. Four viewport-anchored panes draw only
 * frozen rows/columns; they never copy the entire worksheet into the DOM.
 */
(() => {
  'use strict';
  class FrozenPanes {
    constructor(o) {
      this.o = o;
      this.key = '';
      this.cache = new Map();
      this.cacheSizes = new Map();this.cacheBytes=0;
      this.timer = 0;
      this.abort = null;
      this.pending = null;
      this.serial = 0;
      this.lastSheet = '';
    }
    effective() {
      const {state: s, grid, rowHeight, colWidth, headerHeight, rowHead} = this.o;
      let rows=0,cols=0,rowUsed=0,colUsed=0;
      for(let i=1;i<=s.freeze.rows;i++){
        const size=(this.o.sourceRowHeight||rowHeight)(i);if(rowUsed+size>grid.clientHeight-headerHeight()-105)break;
        rowUsed+=size;rows++;
      }
      for(let i=1;i<=Math.min(s.freeze.cols,80);i++){
        const size=colWidth(i);if(colUsed+size>grid.clientWidth-rowHead-230)break;
        colUsed+=size;cols++;
      }
      return {rows,cols};
    }
    reset() {
      ++this.serial;
      clearTimeout(this.timer);
      this.abort?.abort(); this.abort = null;
      this.pending = null;
      this.key = '';
      this.cache.clear();
      this.cacheSizes.clear();this.cacheBytes=0;
      this.o.state.frozenCellMap.clear();
      for (const id of ['freezeTop','freezeLeft','freezeCorner','freezeHeader','freezeRowHeads']) {
        const el = document.getElementById(id);
        if (el) { el.hidden = true; }
      }
      for (const id of ['freezeTopCells','freezeLeftCells','freezeCorner','freezeHeader','freezeRowHeads']) {
        document.getElementById(id)?.replaceChildren();
      }
      this.base = null;
      document.getElementById('freezeTopCells').style.transform = '';
      document.getElementById('freezeLeftCells').style.transform = '';
    }
    followScroll() {
      if (!this.base) return;
      document.getElementById('freezeTopCells').style.transform = `translateX(${- (this.o.grid.scrollLeft - this.base.left)}px)`;
      document.getElementById('freezeLeftCells').style.transform = `translateY(${- (this.o.getVirtualScrollTop() - this.base.top)}px)`;
    }
    schedule(force = false) {
      if (!this.o.state.indexReady) return;
      // The main viewport controls which data band to fetch. Refresh frozen
      // panes as soon as the main band is committed, not 42 ms after the last
      // scroll event (which may never occur during continuous touchpad input).
      clearTimeout(this.timer);
      this.timer = setTimeout(() => this.refresh(force).catch(err => {
        if (err?.name !== 'AbortError') this.o.onError(err);
      }), 0);
    }
    async fetchPart(name, params, signal) {
      const key = `${name}|${params.toString()}`;
      if (this.cache.has(key)) {const data=this.cache.get(key);this.cache.delete(key);this.cache.set(key,data);return data;}
      const data = await this.o.api(`/api/sheet?${params}`, {signal});
      if(signal.aborted)return data;
      this.cachePart(key,data);
      return data;
    }
    cachePart(key,data){
      if(this.cacheSizes.has(key))this.cacheBytes-=this.cacheSizes.get(key);
      this.cache.delete(key);
      const bytes=this.o.estimateBytes?.(data)||0;
      this.cache.set(key, data);
      this.cacheSizes.set(key,bytes);this.cacheBytes+=bytes;
      while(this.cache.size>1&&(this.cache.size>48||this.cacheBytes>(this.o.cacheBudget??Infinity))){
        const oldest=this.cache.keys().next().value;this.cacheBytes-=this.cacheSizes.get(oldest)||0;
        this.cache.delete(oldest);this.cacheSizes.delete(oldest);
      }
    }
    async refresh(force = false) {
      const {state: s, getRequest, grid, api, rowHeight, colWidth, rowTop, colLeft, headerHeight, rowHead} = this.o;
      const sourceTop=this.o.sourceRowTop||rowTop,sourceHeight=this.o.sourceRowHeight||rowHeight;
      const visible=row=>!s.rowVisibility||s.rowVisibility.visible(row);
      const next=row=>s.rowVisibility?s.rowVisibility.next(row):row;
      if (!s.indexReady || !s.sheet) return;
      const {rows: nr, cols: nc} = this.effective();
      const ids = ['freezeTop','freezeLeft','freezeCorner','freezeHeader','freezeRowHeads'];
      if (!nr && !nc) {
        ++this.serial;
        this.abort?.abort();this.abort=null;this.pending=null;
        this.key = '';
        s.frozenCellMap.clear();
        ids.forEach(id => { const e = document.getElementById(id); if (e) e.hidden = true; });
        for (const id of ['freezeTopCells','freezeLeftCells','freezeCorner','freezeHeader','freezeRowHeads']) document.getElementById(id)?.replaceChildren();
        this.base = null;
        return;
      }
      const req = getRequest();
      const topCol = Math.max(1, req.startCol);
      const viewId=s.rowView?.id||'';
      const partsKey = [s.sheet, s.sheetEpoch, s.filtered, s.filterId, viewId, rowHead, nr, nc, req.displayStart, req.displayEnd, topCol, req.cols, req.selectedCols?.join(',')||'', req.filtered?req.offset:req.row].join('|');
      if (!force && partsKey === this.key) { this.followScroll(); return; }
      if (this.pending?.key === partsKey && !this.pending.abort.signal.aborted) return this.pending.promise;
      const ticket = ++this.serial;
      this.abort?.abort();
      const abort = new AbortController();
      this.abort = abort;
      const build = (init) => new URLSearchParams({sheet:s.sheet,rowWindow:'1',...(viewId?{viewId,filterId:s.filtered?s.filterId:'',pinnedThroughRow:String(nr)}:{}), ...init});
      const jobs = [];
      if (nr && nc) jobs.push(this.fetchPart('corner', build({row:'1',limit:String(nr),col:'1',cols:String(nc)}), abort.signal));
      else jobs.push(Promise.resolve(null));
      if (nr) {
        const params=build({row:'1',limit:String(nr),col:String(topCol),cols:String(req.cols)});
        if(req.selectedCols)params.set('columns',req.selectedCols.join(','));
        jobs.push(this.fetchPart('top',params,abort.signal));
      } else jobs.push(Promise.resolve(null));
      if (nc) {
        const params = build({limit:String(req.limit),col:'1',cols:String(nc)});
        params.delete('pinnedThroughRow');
        if (req.filtered) {
          params.set('offset',String(req.offset));
          params.set('filterId',s.filterId);
          if (nr) params.set('excludeThroughRow',String(nr));
        } else params.set('row',String(req.row));
        jobs.push(this.fetchPart('left', params, abort.signal));
      } else jobs.push(Promise.resolve(null));
      const pending={key:partsKey,abort,promise:null};
      this.pending=pending;
      pending.promise=(async()=>{
      try {
      const [corner, top, left] = await Promise.all(jobs);
      if (ticket !== this.serial || abort.signal.aborted || !s.indexReady || viewId!==(s.rowView?.id||'')) return;
      if(rowHead!==this.o.rowHead){this.schedule(true);return;}
      const now = getRequest();
      // Never let a frozen response for an off-screen position overwrite
      // panes after a rapid scroll or a return to an earlier position.
      if (now.visibleStart < req.displayStart || now.visibleEnd > req.displayEnd ||
          now.visibleColStart < req.visibleColStart || now.visibleColEnd > req.visibleColEnd) return;
      this.key = partsKey;
      this.o.beforePaint?.([...(corner?.rows||[]),...(top?.rows||[]),...(left?.rows||[])]);
      this.base = {left:grid.scrollLeft,top:this.o.getVirtualScrollTop()};
      const rh = rowHeight(), cw = colWidth(), hh = headerHeight();
      const topHeight = sourceTop(nr+1), leftWidth = colLeft(nc+1);
      const freezeTop = document.getElementById('freezeTop');
      const freezeTopBody = document.getElementById('freezeTopBody');
      const freezeLeft = document.getElementById('freezeLeft');
      const freezeCorner = document.getElementById('freezeCorner');
      const freezeHeader = document.getElementById('freezeHeader');
      const freezeRowHeads = document.getElementById('freezeRowHeads');
      s.frozenCellMap.clear();
      // Border resolution may need a later covered cell of a merged range.
      // Make every fetched model available before painting the first anchor.
      for(const part of [corner,top,left])for(const row of part?.rows||[])for(const cell of row.cells||[])s.frozenCellMap.set(`${this.o.colName(cell.col)}${row.number}`,cell);
      // The independent frozen-pane painter follows the same visible-column
      // spill rule as the scrolling grid, including originally hidden columns.
      const topCols=req.selectedCols||Array.from({length:req.cols},(_,i)=>topCol+i);
      const topLayouts=topCols.filter(c=>c>nc&&!s.hiddenColumns?.has(c)).map(c=>({col:c,width:colWidth(c)}));
      const pinnedLayouts=Array.from({length:nc},(_,i)=>i+1).filter(c=>!s.hiddenColumns?.has(c)).map(c=>({col:c,width:colWidth(c)}));
      const topPositions=new Map(topLayouts.map((c,i)=>[c.col,i]));
      const pinnedPositions=new Map(pinnedLayouts.map((c,i)=>[c.col,i]));
      const following=layouts=>new Map(layouts.map((part,i)=>[part.col,layouts[i+1]?.col||0]));
      const topNext=following(topLayouts),pinnedNext=following(pinnedLayouts);
      const rowColumns=new WeakMap();
      const cellByColumn=data=>{
        if(!data)return null;
        let columns=rowColumns.get(data);
        if(!columns){columns=Object.create(null);for(const c of data.cells||[])columns[c.col]=c;rowColumns.set(data,columns);}
        return columns;
      };
      const make = (data, col, row, visual, x, y, type='body', nextData=null, nextCol=0, outerLeft=false) => {
        const el = document.createElement('div');
        const ref = `${this.o.colName(col)}${row}`;
        const cell = cellByColumn(data)?.[col];
        const selectedColumns=s.columnSelection;
        const highlighted=selectedColumns&&col>=Math.min(selectedColumns.start,selectedColumns.end)&&col<=Math.max(selectedColumns.start,selectedColumns.end);
        el.className = 'grid-cell pinned-cell' + (highlighted?' column-range-cell':'');
        el.style.left = `${x}px`; el.style.top = `${y}px`;
        el.style.width = `${colWidth(col)}px`;el.style.height = `${type==='left'?rowHeight(visual):sourceHeight(row)}px`;
        el.dataset.ref = ref; el.dataset.row = String(row);
        el.dataset.col = String(col); el.dataset.visualIndex = String(visual);
        this.o.paintCell(el,cell,data);if(cell)s.frozenCellMap.set(ref,cell);
        const rightCell=cellByColumn(data)?.[nextCol];
        const belowCell=cellByColumn(nextData)?.[col];
        this.o.updateSharedBorders(el,cell,data,rightCell,data,belowCell,nextData,{outerLeft,outerTop:row===1});
        this.o.decorateMergedCell?.(el,row,col);
        if(cell && this.o.extendTextIntoBlanks){
          const pinned=type==='corner'||type==='left';
          const layouts=pinned?pinnedLayouts:topLayouts;
          const position=(pinned?pinnedPositions:topPositions).get(col);
          if(position!==undefined)this.o.extendTextIntoBlanks(el,cell,cellByColumn(data),layouts,position);
        }
        if (this.o.isPrecedent(row,col)) el.classList.add('precedent');
        this.o.decorateFormulaRef?.(el,row,col,s.sheet);
        if (this.o.isSearchMatch(s.sheet,ref)) el.classList.add('search-match');
        if (this.o.isBookmarked?.(s.sheet,ref)) el.classList.add('audit-marked');
        return el;
      };
      const head = document.createDocumentFragment();
      if (nc) for (let col=1; col<=nc; col++) {
        if(s.hiddenColumns?.has(col))continue;
        const el=document.createElement('div');
        const selectedColumns=s.columnSelection;
        const highlighted=selectedColumns&&col>=Math.min(selectedColumns.start,selectedColumns.end)&&col<=Math.max(selectedColumns.start,selectedColumns.end);
        el.className='column-head pinned-head' + (highlighted?' column-selected':'');
        el.dataset.col=String(col);
        el.style.left=`${colLeft(col)}px`;
        el.style.width=`${colWidth(col)}px`;
        const tableHead=this.o.decorateColumnHeader?.(el,col);
        if(!tableHead)el.textContent=this.o.colName(col);
        const resize=document.createElement('span');resize.className='column-resize-grip';
        this.o.bindResizeGrip(resize,'col',col);el.append(resize);
        const filter=document.createElement('button');
        filter.type='button'; filter.className='column-filter';
        filter.textContent='▾';filter.dataset.filterCol=String(col);
        filter.title=`筛选 ${this.o.colName(col)} 列`;
        if(!tableHead)el.append(filter);head.append(el);
      }
      freezeHeader.hidden = !nc;
      freezeHeader.style.left = `${rowHead}px`;
      freezeHeader.style.width = `${leftWidth}px`;
      freezeHeader.style.height = `${hh}px`;
      freezeHeader.replaceChildren(head);

      const topHeaders=document.createDocumentFragment();
      const topCells=document.createDocumentFragment();
      const topMap=new Map((top?.rows||[]).map(r=>[r.number,r]));
      for (let row=1;row<=nr;row++) {
        if(!visible(row))continue;
        const label=document.createElement('div');
        label.className='pinned-row-head';label.textContent=String(row);
        label.dataset.row=String(row); label.style.top=`${sourceTop(row)}px`;
        label.style.height=`${sourceHeight(row)}px`;
        const resize=document.createElement('span');resize.className='row-resize-grip';
        this.o.bindResizeGrip(resize,'row',row);label.append(resize);
        topHeaders.append(label);
        for(const col of topCols) {
          if(col<=nc||s.hiddenColumns?.has(col))continue;
          // Draw the *whole buffered band*, not just the cells on screen at
          // response time. followScroll() then moves them without blank gaps.
          const x=colLeft(col)-leftWidth-grid.scrollLeft;
          topCells.append(make(topMap.get(row),col,row,row,x,sourceTop(row),'top',topMap.get(next(row+1)),topNext.get(col)||0,topLayouts[0]?.col===col&&col>1));
        }
      }
      freezeTop.hidden=freezeRowHeads.hidden=!nr;
      freezeTop.style.top=`${hh}px`;
      freezeTop.style.height=`${topHeight}px`;
      freezeTopBody.style.left=`${rowHead+leftWidth}px`;
      freezeRowHeads.style.width=`${rowHead}px`;
      freezeRowHeads.replaceChildren(topHeaders);
      document.getElementById('freezeTopCells').replaceChildren(topCells);

      const cornerNodes=document.createDocumentFragment();
      const cornerMap=new Map((corner?.rows||[]).map(r=>[r.number,r]));
      for(let r=1;r<=nr;r++)if(visible(r))for(let c=1;c<=nc;c++)if(!s.hiddenColumns?.has(c))cornerNodes.append(make(cornerMap.get(r),c,r,r,colLeft(c),sourceTop(r),'corner',cornerMap.get(next(r+1)),pinnedNext.get(c)||0,pinnedLayouts[0]?.col===c&&c>1));
      freezeCorner.hidden=!(nr && nc);
      freezeCorner.style.top=`${hh}px`;freezeCorner.style.left=`${rowHead}px`;
      freezeCorner.style.width=`${leftWidth}px`;freezeCorner.style.height=`${topHeight}px`;
      freezeCorner.replaceChildren(cornerNodes);

      const leftNodes=document.createDocumentFragment();
      const leftRows=left?.rows||[];
      const firstVisual=req.displayStart;
      const lastVisual=s.filtered?firstVisual+leftRows.length-1:req.displayEnd;
      const leftMap=new Map(leftRows.map((rd,i)=>[s.filtered?firstVisual+i:rd.number,rd]));
      const origin=this.o.getVirtualScrollTop()+(s.filtered?0:topHeight);
      for(let visual=firstVisual;visual<=lastVisual;visual=s.filtered?visual+1:(next(visual+1)||Infinity)){
        const rd=leftMap.get(visual);
        const row=rd?.number||visual;
        if(!s.filtered&&!visible(row))continue;
        const y=rowTop(visual)-origin;
        // Include buffered rows and sparse/empty rows. Only painting rows that
        // were visible when the fetch returned leaves permanent white strips.
        for(let c=1;c<=nc;c++)if(!s.hiddenColumns?.has(c))leftNodes.append(make(rd,c,row,visual,colLeft(c),y,'left',leftMap.get(s.filtered?visual+1:next(visual+1)),pinnedNext.get(c)||0,pinnedLayouts[0]?.col===c&&c>1));
      }
      freezeLeft.hidden=!nc;
      freezeLeft.style.top=`${hh+topHeight}px`;
      freezeLeft.style.left=`${rowHead}px`;
      freezeLeft.style.width=`${leftWidth}px`;
      document.getElementById('freezeLeftCells').replaceChildren(leftNodes);
      this.followScroll();
      this.o.onRender();
      } catch(error) {
        if(abort.signal.aborted||ticket!==this.serial)return;
        abort.abort();
        throw error;
      } finally {
        if(this.pending===pending)this.pending=null;
        if(this.abort===abort)this.abort=null;
      }
      })();
      return pending.promise;
    }
  }
  window.ExcelLensFrozenPanes = FrozenPanes;
})();
