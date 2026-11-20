/* A tiny, deterministic multi-area selection model. No per-cell bitmap or
 * million-element array is constructed, even for a full-column selection. */
(() => {
  'use strict';
  const LIMIT=256;
  const cell=(row,col,visual=row)=>({row,col,visual});
  function rectangle(a,b,filtered=false){
    if(!a||!b)return null;
    return {row1:a.row,row2:b.row,col1:a.col,col2:b.col,
      visual1:filtered?a.visual||a.row:0,visual2:filtered?b.visual||b.row:0};
  }
  const position=(row,visual,through)=>row<=through?row:through+visual;
  function contains(area,row,col,visual,filtered=false,through=0){
    if(!area)return false;
    if(col<Math.min(area.col1,area.col2)||col>Math.max(area.col1,area.col2))return false;
    if(!filtered)return row>=Math.min(area.row1,area.row2)&&row<=Math.max(area.row1,area.row2);
    const a=position(area.row1,area.visual1,through),b=position(area.row2,area.visual2,through);
    const index=position(row,visual,through);
    return visual>0&&index>=Math.min(a,b)&&index<=Math.max(a,b);
  }
  function areas(state,includeSingle=false){
    const result=[...(state.multiRanges||[])];
    if(state.range)result.push(state.range);
    else if(includeSingle&&state.selected)
      result.push(rectangle(cell(state.selected.row,state.selected.col,state.selectedVisualIndex),
        cell(state.selected.row,state.selected.col,state.selectedVisualIndex),state.filtered));
    return result;
  }
  function points(area, colName){
    // In a sorted view each visual index belongs to its original endpoint.
    // Sorting physical rows here would detach those pairs for upward drags
    // between a data row and a pinned header.
    const visual=!!area.visual1||!!area.visual2;
    const r1=visual?area.row1:Math.min(area.row1,area.row2),r2=visual?area.row2:Math.max(area.row1,area.row2);
    const c1=Math.min(area.col1,area.col2),c2=Math.max(area.col1,area.col2);
    return {start:`${colName(c1)}${r1}`,end:`${colName(c2)}${r2}`,
      visualStart:area.visual1||undefined,visualEnd:area.visual2||undefined};
  }
  function rowCount(area,filtered=false,through=0){
    if(!filtered)return Math.abs(area.row2-area.row1)+1;
    const pinned1=area.row1<=through,pinned2=area.row2<=through;
    if(pinned1&&pinned2)return Math.abs(area.row2-area.row1)+1;
    if(pinned1)return through-area.row1+1+area.visual2;
    if(pinned2)return through-area.row2+1+area.visual1;
    return Math.abs(area.visual2-area.visual1)+1;
  }
  // Toggling is deliberately restricted to a singleton selection. This avoids
  // breaking an existing rectangle into thousands of disjoint areas.
  function removeSingleton(areas,row,col,visual,filtered=false){
    const found=areas.findIndex(a=>a.row1===a.row2&&a.col1===a.col2&&contains(a,row,col,visual,filtered));
    if(found===-1)return null;
    return areas.filter((_,i)=>i!==found);
  }
  window.ExcelLensMultiSelection={LIMIT,cell,rectangle,contains,areas,points,rowCount,removeSingleton};
})();
