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
  function mergeSpans(spans){
    if(spans.length<=1)return spans;
    spans.sort((a,b)=>a[0]-b[0]||a[1]-b[1]);
    const out=[[spans[0][0],spans[0][1]]];
    for(let i=1;i<spans.length;i++){
      const [a,b]=spans[i],prev=out[out.length-1];
      if(a<=prev[1]+1)prev[1]=Math.max(prev[1],b);
      else out.push([a,b]);
    }
    return out;
  }
  function inSpans(spans,value){
    if(spans.length===1)return value>=spans[0][0]&&value<=spans[0][1];
    for(let i=0;i<spans.length;i++){if(value>=spans[i][0]&&value<=spans[i][1])return true;}
    return false;
  }
  function containsCol(area,col){
    return Boolean(area)&&col>=Math.min(area.col1,area.col2)&&col<=Math.max(area.col1,area.col2);
  }
  function containsRow(area,row,visual,filtered=false,through=0){
    if(!area)return false;
    if(!filtered)return row>=Math.min(area.row1,area.row2)&&row<=Math.max(area.row1,area.row2);
    if(row>through&&!(visual>0))return false;
    const a=position(area.row1,area.visual1,through),b=position(area.row2,area.visual2,through);
    const index=position(row,visual,through);
    return index>=Math.min(a,b)&&index<=Math.max(a,b);
  }
  function contains(area,row,col,visual,filtered=false,through=0){
    return containsCol(area,col)&&containsRow(area,row,visual,filtered,through);
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
  function highlight(state,focus=state?.selected,list=areas(state||{})){
    const filtered=Boolean(state?.filtered),through=filtered?state.selectedFrozenThrough||0:0;
    let rowSpans=[],colSpans=[],hasRow=()=>false,hasCol=()=>false;
    if(state?.wholeRowSelected&&state.selected){
      const r=state.selected.row,v=state.selectedVisualIndex||0;
      if(r&&(!filtered||r<=through||v>0)){
        const p=filtered?position(r,v,through):r;
        rowSpans=[[p,p]];
        hasRow=(row,visual)=>filtered?(row<=through||visual>0)&&position(row,visual,through)===p:row===r;
      }
    }else if(list.length){
      colSpans=mergeSpans(list.map(a=>[Math.min(a.col1,a.col2),Math.max(a.col1,a.col2)]));
      rowSpans=mergeSpans(list.map(a=>{
        const r1=filtered?position(a.row1,a.visual1,through):a.row1;
        const r2=filtered?position(a.row2,a.visual2,through):a.row2;
        return [Math.min(r1,r2),Math.max(r1,r2)];
      }).filter(([a,b])=>Number.isFinite(a)&&Number.isFinite(b)));
      hasCol=col=>inSpans(colSpans,col);
      hasRow=(row,visual)=>{
        if(!filtered)return inSpans(rowSpans,row);
        return (row<=through||visual>0)&&inSpans(rowSpans,position(row,visual,through));
      };
    }else if(focus){
      if(focus.row){rowSpans=[[focus.row,focus.row]];hasRow=row=>row===focus.row;}
      if(focus.col){colSpans=[[focus.col,focus.col]];hasCol=col=>col===focus.col;}
    }
    const singleCol=colSpans.length===1&&colSpans[0][0]===colSpans[0][1]?colSpans[0][0]:0;
    const hasMultiCol=colSpans.length>1||(colSpans.length===1&&colSpans[0][0]!==colSpans[0][1]);
    const fmt=spans=>spans.map(([a,b])=>a===b?String(a):`${a}-${b}`).join(',');
    const key=`${filtered?'f'+through:'s'}:${fmt(rowSpans)}|${fmt(colSpans)}`;
    const mergeTarget=(rowSpans.length||colSpans.length)?{rowSpans,colSpans}:null;
    return {rowSpans,colSpans,singleCol,hasMultiCol,key,mergeTarget,hasRow,hasCol};
  }
  window.ExcelLensMultiSelection={LIMIT,cell,rectangle,contains,containsRow,containsCol,areas,points,rowCount,removeSingleton,highlight};
})();
