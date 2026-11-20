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
      if (from < to) pieces.push({from, to, leading:first >= lo, trailing:last <= hi});
    }
    if (frozen > 0) project(header, Math.min(split, limit), 0);
    project(split, limit, scroll);
    return pieces;
  }

  function paint(layer, areas, view) {
    let count = 0;
    function box(className, x, y, geometry = '') {
      const node = layer.children[count++] || layer.appendChild(document.createElement('i'));
      if (node.className !== className) node.className = className;
      const style = `left:${x}px;top:${y}px;${geometry}`;
      if (node._position !== style) { node.style.cssText = style; node._position = style; }
    }
    for (const area of areas) {
      const x = axisPieces(view.colLeft(Math.min(area.col1,area.col2)), view.colLeft(Math.max(area.col1,area.col2)+1),
        view.rowHead, view.frozenWidth, view.left, view.width);
      const a = view.rowBounds(area.row1,area.visual1), b = view.rowBounds(area.row2,area.visual2);
      const y = axisPieces(Math.min(a[0],b[0]), Math.max(a[1],b[1]), view.header, view.frozenHeight, view.top, view.height);
      for (const col of x) for (const row of y) {
        if (!(row.leading || col.trailing || row.trailing || col.leading)) continue;
        const edges = [row.leading,col.trailing,row.trailing,col.leading].map(edge=>edge?'var(--range-stroke)':'0').join(' ');
        const color = area.color ? ';--reference-color:'+area.color : '';
        box('range-frame',col.from,row.from,`width:${col.to-col.from}px;height:${row.to-row.from}px;border-width:${edges}${color}`);
        if (!area.color && areas.length === 1 && col.trailing && row.trailing) {
          box('range-corner',col.to,row.to);
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
