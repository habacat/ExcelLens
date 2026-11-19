(() => {
  'use strict';

  function viewportBounds(data,request){
    const columns=Array.isArray(data.columns)&&data.columns.length?data.columns:null;
    return {filtered:request.filtered,filterId:request.filterId,viewId:request.viewId||'',
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
      if(available?!available.has(col):(col<bounds.startCol||col>bounds.endCol))return false;
    }
    return true;
  }
  function sameViewportRows(bounds,request){
    return bounds&&bounds.filtered===request.filtered&&bounds.filterId===request.filterId&&
      (bounds.viewId||'')===(request.viewId||'')&&bounds.excludeThroughRow===request.excludeThroughRow;
  }
  function viewportCoversVisible(bounds,request){
    if(!sameViewportRows(bounds,request))return false;
    if(request.visibleEnd<request.visibleStart)return true;
    return bounds.displayStart<=request.visibleStart&&bounds.displayEnd>=request.visibleEnd&&
      hasRequestedColumns(bounds,request,true);
  }
  function viewportHasHeadroom(bounds,request,maxRow,maxCol){
    if(!viewportCoversVisible(bounds,request))return false;
    const rowMin=Math.max(1,request.visibleStart-3),rowMax=Math.min(
      maxRow,request.visibleEnd+3);
    if(request.selectedCols)return bounds.displayStart<=rowMin&&bounds.displayEnd>=rowMax&&
      hasRequestedColumns(bounds,request,false);
    const colMin=Math.max(1,request.visibleColStart-1),colMax=Math.min(maxCol,request.visibleColEnd+1);
    return bounds.displayStart<=rowMin && bounds.displayEnd>=rowMax &&
      coversColumnRange(bounds,colMin,colMax);
  }
  function viewportCoversRequest(bounds,request){
    if(!sameViewportRows(bounds,request))return false;
    return bounds.displayStart<=request.displayStart&&bounds.displayEnd>=request.displayEnd&&
      hasRequestedColumns(bounds,request,false);
  }
  function viewportKey(epoch,request){
    return [epoch,request.filtered,request.filterId,request.viewId||'',request.displayStart,request.limit,
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
          (cell.formula?.length||0)+(cell.ref?.length||0))+window.ExcelLensRichText.estimate(cell.richText)+(window.ExcelLensConditional?.estimate(cell.conditional)||0);
      }
    }
    return size;
  }
  class TileCache {
    constructor(budget, maxTiles = 8) {
      this.budget = budget;
      this.maxTiles = maxTiles;
      this.entries = new Map();
      this.bytes = 0;
    }
    get size() { return this.entries.size; }
    clear() {
      this.entries.clear();
      this.bytes = 0;
    }
    store(key, data, request) {
      const old = this.entries.get(key);
      if (old) {
        this.entries.delete(key);
        this.bytes -= old.bytes;
      }
      const entry = {data, request, bounds: viewportBounds(data,request), bytes: estimateViewportBytes(data)};
      this.entries.set(key, entry);
      this.bytes += entry.bytes;
      // A single oversized visible tile is usable until the user moves on.
      while (this.size > 1 && (this.size > this.maxTiles || this.bytes > this.budget)) {
        const oldest = this.entries.keys().next().value;
        this.bytes -= this.entries.get(oldest).bytes;
        this.entries.delete(oldest);
      }
      return entry;
    }
    find(key, request) {
      let entry = this.entries.get(key);
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
    viewportHasHeadroom, viewportKey, estimateViewportBytes};
})();
