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
  window.ExcelLensGridBorders=Object.freeze({choose,resolve});
})();
