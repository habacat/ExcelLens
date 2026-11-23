// Draw only the visible perimeter. A range spanning a frozen boundary is
// projected into each pane without adding a border at the cut between them.
(() => {
  'use strict';
  function axisPieces(start, end, header, frozen, scroll, limit) {
    const pieces = [];
    const split = header + frozen;
    function project(lo, hi, shift) {
      const first = header + start - shift, last = header + end - shift;
      const from = Math.max(lo, first), to = Math.min(hi, last);
      if (from < to) pieces.push({from,to,origin:header-shift,leading:first>=lo,trailing:last<=hi});
    }
    if (frozen > 0) project(header, Math.min(split, limit), 0);
    project(split, limit, scroll);
    return pieces;
  }

  // Trace the centre of the worksheet rules. The SVG pen is independent of
  // their widths. Fractional coverage keeps a 3px pen centred on an even-
  // pixel rule as well as on an odd-pixel one.
  const selectionWidth=3;
  function paintSeams(frame,edges,col,row) {
    const half=selectionWidth/2,ns='http://www.w3.org/2000/svg';
    const svg=frame.firstElementChild||frame.appendChild(document.createElementNS(ns,'svg'));
    let count=0;
    function rect(x,y,w,h){
      if(w<=0||h<=0)return;
      const node=svg.children[count++]||svg.appendChild(document.createElementNS(ns,'rect'));
      const key=[x,y,w,h].join(',');
      if(node._position===key)return;
      node.setAttribute('x',x);node.setAttribute('y',y);
      node.setAttribute('width',w);node.setAttribute('height',h);node._position=key;
    }
    function distance(axis,position){
      if(!edges.raster)return position-axis.from;
      const {step,origin}=edges.raster,base=axis===col?origin.left:origin.top;
      return (Math.round((base+position)/step)-Math.round((base+axis.from)/step))*step;
    }
    function runs(segments,axis){
      const parts=[];
      let cursor=0;const length=distance(axis,axis.to);
      for(const s of segments){
        const start=Math.max(0,distance(axis,s.from+axis.origin)),end=Math.min(length,distance(axis,s.to+axis.origin));
        if(end<=start)continue;
        if(start>cursor)parts.push({start:cursor,end:start,offset:0});
        parts.push({start,end,offset:s.offset});cursor=end;
      }
      if(cursor<length)parts.push({start:cursor,end:length,offset:0});
      if(!parts.length)parts.push({start:0,end:length,offset:0});
      return parts;
    }
    const top=row.leading&&runs(edges.top,col),bottom=row.trailing&&runs(edges.bottom,col);
    const left=col.leading&&runs(edges.left,row),right=col.trailing&&runs(edges.right,row);
    const w=distance(col,col.to),h=distance(row,row.to);
    // Horizontal strips own the corner. Vertical strips meet their inner
    // edge, so even translucent edge pixels are painted only once.
    function side(parts,horizontal,cross,start,end){
      if(!parts)return;
      for(let i=0;i<parts.length;i++){
        const s=parts[i],a=i===0?start:s.start,b=i===parts.length-1?end:s.end;
        const offset=cross+s.offset-half;
        if(horizontal)rect(a,offset,b-a,selectionWidth);
        else rect(offset,a,selectionWidth,b-a);
        const next=parts[i+1];
        if(next&&Math.abs(next.offset-s.offset)>selectionWidth){
          const from=cross+Math.min(s.offset,next.offset),length=Math.abs(next.offset-s.offset);
          if(horizontal)rect(b-half,from,selectionWidth,length);
          else rect(from,b-half,length,selectionWidth);
        }
      }
    }
    side(top,true,0,left?left[0].offset-half:0,w+(right?right[0].offset+half:0));
    side(bottom,true,h,left?left.at(-1).offset-half:0,w+(right?right.at(-1).offset+half:0));
    side(left,false,0,top?top[0].offset+half:0,h+(bottom?bottom[0].offset-half:0));
    side(right,false,w,top?top.at(-1).offset+half:0,h+(bottom?bottom.at(-1).offset-half:0));
    while(svg.children.length>count)svg.lastElementChild.remove();
  }

  function paint(layer, areas, view, stroke = 3) {
    let count = 0;
    const thickness = piece => Math.min(stroke, (piece.to-piece.from)/(piece.leading && piece.trailing ? 2 : 1))+'px';
    function box(className, x, y, geometry = '') {
      const node = layer.children[count++] || layer.appendChild(document.createElement('i'));
      if (node.className !== className) node.className = className;
      const style = `left:${x}px;top:${y}px;${geometry}`;
      if (node._position !== style) { node.style.cssText = style; node._position = style; }
      return node;
    }
    for (const area of areas) {
      const x1=view.colLeft(Math.min(area.col1,area.col2)),x2=view.colLeft(Math.max(area.col1,area.col2)+1);
      const x = axisPieces(x1,x2,view.rowHead,view.frozenWidth,view.left,view.width);
      const a = view.rowBounds(area.row1,area.visual1), b = view.rowBounds(area.row2,area.visual2);
      const y1=Math.min(a[0],b[0]),y2=Math.max(a[1],b[1]);
      const y = axisPieces(y1,y2,view.header,view.frozenHeight,view.top,view.height);
      const seams=x.length&&y.length&&!area.color&&view.seams?.(x1,x2,y1,y2);
      for (const col of x) for (const row of y) {
        if (!(row.leading || col.trailing || row.trailing || col.leading)) continue;
        const xStroke=thickness(col), yStroke=thickness(row);
        const edges = [row.leading?yStroke:0,col.trailing?xStroke:0,row.trailing?yStroke:0,col.leading?xStroke:0].join(' ');
        const color = area.color ? ';--reference-color:'+area.color : '';
        const frame=box('range-frame',col.from,row.from,`width:${col.to-col.from}px;height:${row.to-row.from}px;border-width:${seams?'0':edges}${color}`);
        if(seams)paintSeams(frame,seams,col,row);
        else if(frame.childElementCount)frame.replaceChildren();
        if (!area.color && areas.length === 1 && col.trailing && row.trailing) {
          const w=Math.min(7,col.to-col.from), h=Math.min(7,row.to-row.from);
          const corner=box('range-corner',col.to-w,row.to-h,`width:${w}px;height:${h}px`);
          if(corner.childElementCount)corner.replaceChildren();
        }
      }
    }
    while (layer.children.length > count) layer.lastElementChild.remove();
  }
  // The grid cells own the tint inside the worksheet. Only paint the exposed
  // scrollport beyond the last row/column, using the same pane projection.
  function paintCrosshair(layer, cell, visual, view, dataWidth, dataHeight) {
    const cutX = Math.max(view.rowHead + view.frozenWidth, view.rowHead + dataWidth - view.left);
    const cutY = Math.max(view.header + view.frozenHeight, view.header + dataHeight - view.top);
    function strip(node,x,y,w,h) {
      node.hidden = !(w>0 && h>0);
      if (node.hidden) return;
      const style = 'left:'+x+'px;top:'+y+'px;width:'+w+'px;height:'+h+'px';
      if (node._position !== style) { node.style.cssText=style; node._position=style; }
    }
    const [a,b] = view.rowBounds(cell.row,visual);
    const row = cutX < view.width && visual>0 && axisPieces(a,b,view.header,view.frozenHeight,view.top,view.height)[0];
    const col = cutY < view.height && axisPieces(view.colLeft(cell.col),view.colLeft(cell.col+1),view.rowHead,view.frozenWidth,view.left,view.width)[0];
    strip(layer.firstElementChild,cutX,row?.from,view.width-cutX,row ? row.to-row.from : 0);
    strip(layer.lastElementChild,col?.from,cutY,col ? col.to-col.from : 0,view.height-cutY);
  }
  window.ExcelLensRangeFrame = {paint,paintCrosshair};
})();
