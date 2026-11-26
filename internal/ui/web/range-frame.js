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
  // their widths. Fractional coverage keeps the pen centred on odd- and
  // even-pixel source rules.
  const selectionWidth=2;
  const handleSize=7;
  function paintSeams(frame,edges,col,row,showHandle) {
    const half=selectionWidth/2,ns='http://www.w3.org/2000/svg';
    const svg=frame.firstElementChild||frame.appendChild(document.createElementNS(ns,'svg'));
    let count=0;
    function rect(x,y,w,h,kind=''){
      if(w<=0||h<=0)return;
      const node=svg.children[count++]||svg.appendChild(document.createElementNS(ns,'rect'));
      if((node._kind||'')!==kind){
        if(kind)node.setAttribute('class',kind);else node.removeAttribute('class');
        node._kind=kind;
      }
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
    if(showHandle){
      // Use the same terminal segments and raster coordinates as the two
      // strokes. The outer square is also the pointer target in an edit copy.
      const x=w+right.at(-1).offset-handleSize/2,y=h+bottom.at(-1).offset-handleSize/2;
      rect(x,y,handleSize,handleSize,'range-corner');
      rect(x+1,y+1,handleSize-2,handleSize-2,'range-corner-face');
    }
    while(svg.children.length>count)svg.lastElementChild.remove();
  }

  function paint(layer, areas, view, stroke = selectionWidth) {
    let count = 0;
    const thickness = piece => Math.min(stroke,(piece.to-piece.from)/(piece.leading && piece.trailing ? 2 : 1));
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
        const edges = [row.leading?yStroke:0,col.trailing?xStroke:0,row.trailing?yStroke:0,col.leading?xStroke:0].map(n=>n?n+'px':'0').join(' ');
        const color = area.color ? ';--reference-color:'+area.color : '';
        const frame=box('range-frame',col.from,row.from,`width:${col.to-col.from}px;height:${row.to-row.from}px;border-width:${seams?'0':edges}${color}`);
        const showHandle=!area.color&&areas.length===1&&col.trailing&&row.trailing;
        if(seams)paintSeams(frame,seams,col,row,showHandle);
        else if(frame.childElementCount)frame.replaceChildren();
        if(showHandle&&!seams){
          const cx=col.to-xStroke/2,cy=row.to-yStroke/2;
          const corner=box('range-corner',cx-handleSize/2,cy-handleSize/2,`width:${handleSize}px;height:${handleSize}px`);
          if(corner.childElementCount)corner.replaceChildren();
        }
      }
    }
    while (layer.children.length > count) layer.lastElementChild.remove();
  }
  function mergePieces(pieces) {
    if (pieces.length <= 1) return pieces;
    pieces.sort((a, b) => a.from - b.from || a.to - b.to);
    const out = [{from: pieces[0].from, to: pieces[0].to}];
    for (let i = 1; i < pieces.length; i++) {
      const p = pieces[i], prev = out[out.length - 1];
      if (p.from <= prev.to) prev.to = Math.max(prev.to, p.to);
      else out.push({from: p.from, to: p.to});
    }
    return out;
  }
  // The grid cells own the tint inside the worksheet. Only paint the exposed
  // scrollport beyond the last row/column, using the same pane projection.
  function paintCrosshair(layer, areas, view, dataWidth, dataHeight) {
    const list = Array.isArray(areas) ? areas : [];
    const cutX = Math.max(view.rowHead + view.frozenWidth, view.rowHead + dataWidth - view.left);
    const cutY = Math.max(view.header + view.frozenHeight, view.header + dataHeight - view.top);
    const rowPieces = [], colPieces = [];
    if (cutX < view.width) {
      for (const area of list) {
        if (!area.row1 && !area.row2) continue;
        const a = view.rowBounds(area.row1, area.visual1), b = view.rowBounds(area.row2, area.visual2);
        const y1 = Math.min(a[0], b[0]), y2 = Math.max(a[1], b[1]);
        if (y2 > y1) rowPieces.push(...axisPieces(y1, y2, view.header, view.frozenHeight, view.top, view.height));
      }
    }
    if (cutY < view.height) {
      for (const area of list) {
        if (!area.col1 && !area.col2) continue;
        const x1 = view.colLeft(Math.min(area.col1, area.col2)), x2 = view.colLeft(Math.max(area.col1, area.col2) + 1);
        if (x2 > x1) colPieces.push(...axisPieces(x1, x2, view.rowHead, view.frozenWidth, view.left, view.width));
      }
    }
    const rows = mergePieces(rowPieces), cols = mergePieces(colPieces);
    function strip(node, className, x, y, w, h) {
      if (node.className !== className) node.className = className;
      node.hidden = !(w > 0 && h > 0);
      if (node.hidden) return;
      const style = 'left:'+x+'px;top:'+y+'px;width:'+w+'px;height:'+h+'px';
      if (node._position !== style) { node.style.cssText=style; node._position=style; }
    }
    const ensure = i => layer.children[i] || layer.appendChild(document.createElement('i'));
    if (rows.length <= 1 && cols.length <= 1) {
      const row = rows[0], col = cols[0];
      strip(ensure(0), 'row-tail', cutX, row?.from, view.width - cutX, row ? row.to - row.from : 0);
      strip(ensure(1), 'column-tail', col?.from, cutY, col ? col.to - col.from : 0, view.height - cutY);
      while (layer.children.length > 2) layer.lastElementChild.remove();
      return;
    }
    let count = 0;
    for (const row of rows) strip(ensure(count++), 'row-tail', cutX, row.from, view.width - cutX, row.to - row.from);
    for (const col of cols) strip(ensure(count++), 'column-tail', col.from, cutY, col.to - col.from, view.height - cutY);
    while (layer.children.length > count) layer.lastElementChild.remove();
  }
  window.ExcelLensRangeFrame = {paint,paintCrosshair};
})();
