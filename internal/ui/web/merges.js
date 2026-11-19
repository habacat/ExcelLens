/* Source merge ranges are presentation metadata. Values stay at their original
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
        return {range,left,right,firstRow:sorted[lo].row,max:Math.max(range.endRow,left?.max||0,right?.max||0),
          firstCol:Math.min(range.col,left?.firstCol??Infinity,right?.firstCol??Infinity),
          lastCol:Math.max(range.endCol,left?.lastCol||0,right?.lastCol||0)};
      };
      this.root=build(0,sorted.length);
    }
    at(row,col) {
      if(!this.root)return null;
      const find=node=>{
        if(!node||node.max<row||node.firstRow>row||col<node.firstCol||col>node.lastCol)return null;
        const r=node.range;
        const left=find(node.left);if(left)return left;
        if(r.row>row)return null;
        if(row<=r.endRow&&col>=r.col&&col<=r.endCol)return r;
        return find(node.right);
      };
      return find(this.root);
    }
    copyAnchor(row,col,visibility) {
      const m=this.at(row,col);
      if (!m || m.row!==row || m.col!==col) return null;
      // An explicitly added header may itself be a hidden source row.
      const visibleRows=visibility ? 1+visibility.count(row+1,m.endRow) : m.endRow-row+1;
      return {endRow:m.endRow,endCol:m.endCol,visibleRows};
    }
    // Excel extends a selection until every intersecting merge fits. Enclosing
    // one merge may bring another into the rectangle. Skip entire subtrees
    // that are already inside it, including on whole-row/whole-sheet selects.
    expand(area, visibility) {
      let top=Math.min(area.row1,area.row2), bottom=Math.max(area.row1,area.row2);
      let left=Math.min(area.col1,area.col2), right=Math.max(area.col1,area.col2);
      let changed;
      function visit(node) {
        if (!node || node.max<top || node.firstRow>bottom || node.lastCol<left || node.firstCol>right) return;
        if (node.firstRow>=top && node.max<=bottom && node.firstCol>=left && node.lastCol<=right) return;
        const m=node.range;
        if (m.row<=bottom && m.endRow>=top && m.col<=right && m.endCol>=left && (!visibility || visibility.visible(m.row))) {
          const endRow=visibility ? visibility.previous(m.endRow) : m.endRow;
          if (endRow>=top && (m.row<top || endRow>bottom || m.col<left || m.endCol>right)) {
            top=Math.min(top,m.row); bottom=Math.max(bottom,endRow);
            left=Math.min(left,m.col); right=Math.max(right,m.endCol);
            changed=true;
          }
        }
        visit(node.left);
        visit(node.right);
      }
      do { changed=false; visit(this.root); } while (changed);
      return {...area,row1:area.row1<=area.row2?top:bottom,row2:area.row1<=area.row2?bottom:top,
        col1:area.col1<=area.col2?left:right,col2:area.col1<=area.col2?right:left};
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
