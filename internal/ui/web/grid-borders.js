/* A shared edge is drawn exactly once. Never paint the right edge of A and
 * the left edge of B as two overlapping strokes, or a row border beneath an
 * OOXML cell border. Border properties here are data, not layout dimensions. */
(() => {
  'use strict';
  const ranks={double:6,solid:5,dashed:4,dotted:3,groove:2,ridge:2};
  const score=value=>{
    if(!value)return 0;
    const parts=String(value).split(/\s+/);
    const width=parseFloat(parts[0])||0.5;
    const kind=parts.find(p=>p in ranks)||'solid';
    return width*100+(ranks[kind]||1);
  };
  // Equal-weight borders prefer the current cell's right/bottom edge. This
  // makes the result stable independently of source array iteration order.
  function choose(primary,secondary){
    return score(secondary)>score(primary)?secondary||'':primary||'';
  }
  function resolve(current={},right={},below={}){
    return {right:choose(current.borderRight,right.borderLeft),
      bottom:choose(current.borderBottom,below.borderTop)};
  }
  // A long merge can adjoin short cells with different edge styles. Only
  // inspect the rendered band, and coalesce consecutive identical strokes.
  function runs(first,last,visibleEnd,next,styleAt,fallback) {
    const parts=[];
    function add(start,end,style) {
      if (start>=end) return;
      if (parts.length && parts.at(-1).style===style) parts.at(-1).end=end;
      else parts.push({start,end,style});
    }
    let index=first;
    while (index<=last && index<=visibleEnd) {
      const following=Math.min(last+1,next(index+1)||last+1);
      add(index,following,styleAt(index));
      index=following;
    }
    add(index,last+1,fallback);
    return parts.length===1 ? parts[0].style : parts;
  }
  function paint(node,side,stroke,offset) {
    const mixed=Array.isArray(stroke), property=side==='right'?'borderRight':'borderBottom';
    const value=mixed?'0px':stroke;
    const key='_xl'+property;
    if (node[key]!==value) {
      node[key]=value;
      if (value) node.style[property]=value;
      else node.style.removeProperty('border-'+side);
    }
    const layerKey=key+'Parts';
    let layer=node[layerKey];
    if (!mixed) {
      if (layer) { layer.remove(); node[layerKey]=null; }
      return;
    }
    if (!layer || layer.parentNode!==node) {
      layer=document.createElement('span');
      layer.className='grid-border-segments'; layer.setAttribute('aria-hidden','true');
      node.appendChild(layer); node[layerKey]=layer;
    }
    const vertical=side==='right', origin=offset(stroke[0].start);
    stroke.forEach(({start,end,style},i)=>{
      const line=layer.children[i] || layer.appendChild(document.createElement('i'));
      line.style[side]='0';
      line.style[vertical?'top':'left']=(offset(start)-origin)+'px';
      line.style[vertical?'height':'width']=(offset(end)-offset(start))+'px';
      line.style[property]=style || 'var(--grid-default-seam)';
    });
    while (layer.children.length>stroke.length) layer.lastChild.remove();
  }
  window.ExcelLensGridBorders=Object.freeze({choose,resolve,runs,paint});
})();
