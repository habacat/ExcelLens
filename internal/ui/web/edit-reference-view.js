(() => {
  'use strict';
  function visibleAreas(areas, filtered, pinned, rows, mergeAt) {
    const result = [];
    for (let area of areas) {
      if (area.row1 === area.row2 && area.col1 === area.col2) {
        const merge = mergeAt(area.row1,area.col1);
        if (merge) area = {...area,row1:merge.row,row2:merge.endRow,col1:merge.col,col2:merge.endCol};
      }
      if (!filtered) { result.push(area); continue; }
      if (area.row1 <= pinned) result.push({...area,row2:Math.min(pinned,area.row2)});
      let run;
      // Sorting can put distant source rows next to each other. Draw the
      // matching visible runs without inventing source rows from screen offsets.
      for (const [visual,source] of rows) {
        if (source <= pinned || source < area.row1 || source > area.row2) { run = null; continue; }
        if (run && visual === run.visual2 + 1) { run.row2=source; run.visual2=visual; }
        else {
          run = {...area,row1:source,row2:source,visual1:visual,visual2:visual};
          result.push(run);
        }
      }
    }
    return result;
  }

  function dragPoint(x,y,kind,grid,header,rowHead,scrollVertical,scroll = true) {
    const r = grid.getBoundingClientRect();
    const left = r.left + (kind === 'row' ? 0 : rowHead);
    const top = r.top + (kind === 'column' ? 0 : header);
    const right = r.left + grid.clientWidth, bottom = r.top + grid.clientHeight;
    const speed = (v,lo,hi) => v < lo + 14 ? -Math.min(36,Math.ceil((lo+14-v)/2))
      : v > hi - 14 ? Math.min(36,Math.ceil((v-hi+14)/2)) : 0;
    const dx = kind === 'row' ? 0 : speed(x,left,right), dy = kind === 'column' ? 0 : speed(y,top,bottom);
    const oldLeft = grid.scrollLeft;
    if (scroll && dx) grid.scrollLeft += dx;
    const vertical = scroll && dy && scrollVertical(dy);
    return {x:kind==='row' ? r.left+rowHead/2 : Math.max(left+1,Math.min(right-1,x)),
      y:kind==='column' ? r.top+header/2 : Math.max(top+1,Math.min(bottom-1,y)),
      scrolling:vertical || oldLeft !== grid.scrollLeft};
  }

  window.ExcelLensEditReferenceView = {visibleAreas,dragPoint};
})();
