/* Source HTML ranges are presentation metadata. Values stay at their original
 * anchors; an interval tree keeps lookup bounded by the visible ranges. */
(() => {
  'use strict';
  class MergedCells {
    constructor(maxRows=Number.MAX_SAFE_INTEGER,maxColumns=Number.MAX_SAFE_INTEGER){this.maxRows=maxRows;this.maxColumns=maxColumns;}
    load(ranges) {
      const sorted=(ranges||[]).filter(r=>[r.row,r.col,r.endRow,r.endCol].every(Number.isInteger)&&r.row>0&&r.col>0&&r.endRow>=r.row&&r.endCol>=r.col&&r.endRow<=this.maxRows&&r.endCol<=this.maxColumns)
        .map(r=>({...r})).sort((a,b)=>a.row-b.row||a.col-b.col);
      const build=(lo,hi)=>{
        if(lo>=hi)return null;
        const mid=(lo+hi)>>>1,range=sorted[mid],left=build(lo,mid),right=build(mid+1,hi);
        return {range,left,right,max:Math.max(range.endRow,left?.max||0,right?.max||0)};
      };
      this.root=build(0,sorted.length);
    }
    at(row,col) {
      if(!this.root)return null;
      const find=node=>{
        if(!node||node.max<row)return null;
        const r=node.range;
        const left=find(node.left);if(left)return left;
        if(r.row>row)return null;
        if(row<=r.endRow&&col>=r.col&&col<=r.endCol)return r;
        return find(node.right);
      };
      return find(this.root);
    }
    decorate(node,range,row,col,colLeft,rowTop,colWidth) {
      if(!range&&!node._xlMerge&&!node._xlCovered)return;
      const covered=range&&(range.row!==row||range.col!==col);
      if(!!covered!==!!node._xlCovered){node.style.display=covered?'none':'';node._xlCovered=!!covered;}
      if(range&&!covered){
        const width=`${colLeft(range.endCol+1)-colLeft(col)}px`,height=`${rowTop(range.endRow+1)-rowTop(row)}px`;
        if(node.style.width!==width)node.style.width=width;
        if(node.style.height!==height)node.style.height=height;
        node.style.zIndex='2';node._xlMerge=true;
      }else if(node._xlMerge){
        node.style.width=`${colWidth(col)}px`;node.style.height='';node.style.zIndex='';node._xlMerge=false;
      }
      node.classList.toggle('merged-cell',!!range&&!covered);
    }
  }
  window.ExcelLensMergedCells=MergedCells;
})();
