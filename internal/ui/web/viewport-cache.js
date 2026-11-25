(() => {
  'use strict';

  function viewportBounds(data,request){
    const columns=Array.isArray(data.columns)&&data.columns.length?data.columns:null;
    return {context:request.context,filtered:request.filtered,filterId:request.filterId,viewId:request.viewId||'',
      excludeThroughRow:request.excludeThroughRow,
      displayStart:request.displayStart,
      displayEnd:request.filtered?request.displayStart+data.rows.length-1:request.displayEnd,
      startCol:data.startCol,
      endCol:columns?columns[columns.length-1]:data.startCol+data.colCount-1,
      columns};
  }
  function coversColumnRange(bounds, first, last) {
    if (bounds.startCol > first || bounds.endCol < last) return false;
    if (!bounds.columns) return true;
    // A previous hidden-column request may straddle a column which is now
    // visible. Bounding coordinates alone cannot establish coverage.
    const available = bounds.columnSet ||= new Set(bounds.columns);
    if (last - first + 1 > available.size) return false;
    for (let col = first; col <= last; col++) {
      if (!available.has(col)) return false;
    }
    return true;
  }
  function hasRequestedColumns(bounds,request,visibleOnly){
    if(request.visibleColEnd<request.visibleColStart)return true;
    if(!request.selectedCols) {
      const first = visibleOnly ? request.visibleColStart : request.startCol;
      const last = visibleOnly ? request.visibleColEnd : request.startCol + request.cols - 1;
      return coversColumnRange(bounds,first,last);
    }
    // Sparse, hidden-column windows are checked repeatedly on every scroll
    // frame. Reuse a membership set instead of rebuilding it for each probe.
    const available = bounds.columns ? (bounds.columnSet ||= new Set(bounds.columns)) : null;
    for(const col of request.selectedCols){
      if(visibleOnly && (col<request.visibleColStart || col>request.visibleColEnd))continue;
      if(col<bounds.startCol||col>bounds.endCol||(available&&!available.has(col)))return false;
    }
    return true;
  }
  function sameViewportRows(bounds,request){
    return bounds&&bounds.filtered===request.filtered&&bounds.filterId===request.filterId&&
      (bounds.viewId||'')===(request.viewId||'')&&bounds.excludeThroughRow===request.excludeThroughRow &&
      (bounds.context||'')===(request.context||'');
  }
  function viewportCoversVisible(bounds,request){
    if(!sameViewportRows(bounds,request))return false;
    if(request.visibleEnd<request.visibleStart)return true;
    return bounds.displayStart<=request.visibleStart&&bounds.displayEnd>=request.visibleEnd&&
      hasRequestedColumns(bounds,request,true);
  }
  function viewportHasHeadroom(bounds,request){
    if(!viewportCoversVisible(bounds,request))return false;
    const row=request.leadRow,col=request.leadCol;
    return row>=bounds.displayStart&&row<=bounds.displayEnd&&containsCol(bounds,col);
  }
  function viewportCoversRequest(bounds,request){
    if(!sameViewportRows(bounds,request))return false;
    return bounds.displayStart<=request.displayStart&&bounds.displayEnd>=request.displayEnd&&
      hasRequestedColumns(bounds,request,false);
  }
  function viewportKey(epoch,request){
    return [epoch,request.context||'',request.filtered,request.filterId,request.viewId||'',request.displayStart,request.limit,
      request.startCol,request.cols,request.selectedCols?.join(',')||'',
      request.excludeThroughRow].join('|');
  }

  function estimateViewportBytes(data) {
    // Cost is linear in the ACTUAL received tile, never the million-row
    // worksheet extent. Include duplicated raw/display/formula strings and
    // per-cell JS object overhead; this is a conservative memory estimate.
    let size=256;
    for(const row of data.rows||[]){
      size+=64+2*(row.presentationNotice?.length||0);
      for(const cell of row.cells||[]){
        size+=176+2*((cell.raw?.length||0)+(cell.display?.length||0)+
          (cell.formula?.length||0)+(cell.ref?.length||0)+(cell.hyperlink?.url?.length||0)+(cell.hyperlink?.sheet?.length||0)+(cell.hyperlink?.ref?.length||0))+window.ExcelLensRichText.estimate(cell.richText)+(window.ExcelLensConditional?.estimate(cell.conditional)||0);
      }
    }
    return size;
  }
  const columnIndex=new WeakMap();
  function rowColumns(row){
    if(!row)return null;
    let columns=columnIndex.get(row);
    if(!columns){
      columns=Object.create(null);
      for(const cell of row.cells||[])columns[cell.col]=cell;
      columnIndex.set(row,columns);
    }
    return columns;
  }
  function tileRow(entry,visual){
    if(entry.bounds.filtered)return entry.data.rows[visual-entry.bounds.displayStart];
    if(!entry.rowMap)entry.rowMap=new Map(entry.data.rows.map(row=>[row.number,row]));
    return entry.rowMap.get(visual);
  }
  function containsCol(bounds,col){
    return col>=bounds.startCol&&col<=bounds.endCol&&(!bounds.columns||(bounds.columnSet||=new Set(bounds.columns)).has(col));
  }
  function indexAtPixel(pixel,last,edge){
    if(last<1||pixel<0||pixel>=edge(last+1))return 0;
    let low=1,high=last;
    while(low<high){
      const mid=Math.floor((low+high)/2);
      if(edge(mid+1)<=pixel)low=mid+1;else high=mid;
    }
    return low;
  }
  class TileCache {
    constructor(budget, maxTiles = 8) {
      this.budget = budget;
      this.maxTiles = maxTiles;
      this.entries = new Map();
      this.bytes = 0;
      this.revision=0;
    }
    get size() { return this.entries.size; }
    clear() {
      this.revision++;
      this.entries.clear();
      this.bytes = 0;
    }
    store(key, data, request) {
      this.revision++;
      const old = this.entries.get(key);
      if (old) {
        this.entries.delete(key);
        this.bytes -= old.bytes;
      }
      const entry = {data, request, bounds: viewportBounds(data,request), bytes: estimateViewportBytes(data)};
      // The requesting foreground operation can project an oversized result once.
      // It never displaces reusable tiles or stays resident in the prefetch cache.
      if(entry.bytes>this.budget)return entry;
      this.entries.set(key, entry);
      this.bytes += entry.bytes;
      while (this.size > 1 && (this.size > this.maxTiles || this.bytes > this.budget)) {
        const oldest = this.entries.keys().next().value;
        this.bytes -= this.entries.get(oldest).bytes;
        this.entries.delete(oldest);
      }
      return entry;
    }
    reader(request,received=[]){
      const entries=[...received,...[...this.entries.values()].reverse()].filter(entry=>sameViewportRows(entry.bounds,request));
      return (visual,col)=>{
        for(const entry of entries){
          const b=entry.bounds;
          if(visual<b.displayStart||visual>b.displayEnd||!containsCol(b,col))continue;
          const row=tileRow(entry,visual);
          return {row,cell:rowColumns(row)?.[col]};
        }
        return null;
      };
    }
    sourceAtPoint(request,{x,y,lastRow,lastCol,rowTop,colLeft,mergeAt}){
      const col=indexAtPixel(x,lastCol,colLeft),visual=indexAtPixel(y,lastRow,rowTop);
      if(!col||!visual)return null;
      const merge=request.filtered?null:mergeAt(visual,col);
      const anchor=merge?.row??visual,sourceCol=merge?.col??col;
      const record=this.reader(request)(anchor,sourceCol);
      if(!record||request.filtered&&!record.row)return null;
      return {cell:record.cell,row:record.row?.number??anchor,col:sourceCol,visual:anchor,
        crosshair:merge?{row:visual,col}:null};
    }
    // Project only the mounted band. Column ownership is constant between tile row
    // boundaries. A missing source cell is a known blank only inside an actual tile.
    project(request,visuals,received=[]){
      const columns=request.selectedCols||Array.from({length:request.cols},(_,i)=>request.startCol+i);
      const entries=[...new Set([...received,...[...this.entries.values()].reverse()])].filter(({bounds:b})=>sameViewportRows(b,request)&&
        b.displayStart<=Math.max(request.displayStart,request.displayEnd)&&b.displayEnd>=request.displayStart-1&&
        b.startCol<=columns.at(-1)&&b.endCol>=columns[0]);
      if(!entries.length)return null;
      const meta=entries[0].data,rows=[];
      let owners=[],until=-1,linkError=false;
      for(const visual of visuals){
        if(request.filtered&&visual>meta.totalRows)break;
        if(visual>until){
          const available=entries.filter(({bounds:b})=>b.displayStart<=visual&&b.displayEnd>=visual);
          owners=columns.map(col=>available.find(e=>containsCol(e.bounds,col)));
          if(owners.some(owner=>!owner))return null;
          until=Math.min(...owners.map(e=>e.bounds.displayEnd),...entries.filter(e=>e.bounds.displayStart>visual).map(e=>e.bounds.displayStart-1));
        }
        let model,owner,byCol,notice='';const cells=[];
        for(let i=0;i<columns.length;i++){
          if(owners[i]!==owner){
            owner=owners[i];const source=tileRow(owner,visual);
            if(request.filtered&&(!source||(model&&model.number!==source.number)))return null;
            model||=source;byCol=rowColumns(source);
            if(source?.presentationNotice&&!notice.includes(source.presentationNotice))notice+=(notice?'；':'')+source.presentationNotice;
            linkError||=Boolean(owner.data.hyperlinkError);
          }
          const cell=byCol?.[columns[i]];
          if(cell)cells.push(cell);
        }
        if(model)rows.push(notice?{...model,cells,presentationNotice:notice}:{...model,cells});
      }
      const data={...meta,hyperlinkError:linkError,rows,startCol:request.startCol,colCount:columns.length,columns:request.selectedCols};
      return {request,data,bounds:viewportBounds(data,request)};
    }
    find(key, request) {
      let entry = this.entries.get(key);
      if (entry&&!viewportCoversRequest(entry.bounds,request))entry=null;
      if (!entry) {
        // Iterate in insertion order to find the newest covering tile.
        for (const [candidateKey, candidate] of this.entries) {
          if (viewportCoversRequest(candidate.bounds, request)) {
            key = candidateKey;
            entry = candidate;
          }
        }
      }
      if (!entry) return null;
      this.entries.delete(key);
      this.entries.set(key, entry);
      return entry;
    }
  }
  window.ExcelLensViewport = {TileCache, viewportBounds, sameViewportRows, viewportCoversVisible, viewportCoversRequest,
    viewportHasHeadroom, viewportKey, estimateViewportBytes, rowColumns};
})();
