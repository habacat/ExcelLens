(() => {
  'use strict';
  function gridBackdropPanes({width,height,rowHead,header,pinWidth,pinHeight,right,bottom,fixedRight,fixedBottom}) {
    const xs=[[rowHead,pinWidth,true],[rowHead+pinWidth,Math.max(0,width-rowHead-pinWidth),false]];
    const ys=[[header,pinHeight,true],[header+pinHeight,Math.max(0,height-header-pinHeight),false]];
    const panes=[];
    for(const [x,w,fixedX]of xs)for(const [y,h,fixedY]of ys) {
      if(w<=0||h<=0)continue;
      panes.push({x,y,width:w,height:h,right:Math.max(-96,Math.min(w+96,(fixedX?fixedRight:right)-x)),bottom:Math.max(-80,Math.min(h+80,(fixedY?fixedBottom:bottom)-y))});
    }
    return panes;
  }

  function paint(backdrop, bounds) {
    const right = Math.max(-96, Math.min(bounds.width, bounds.right));
    const bottom = Math.max(-80, Math.min(bounds.height, bounds.bottom));
    for (const [name, value] of [['header', bounds.header], ['row-head', bounds.rowHead], ['data-right', right], ['data-bottom', bottom]]) {
      const property = '--backdrop-' + name, px = value + 'px';
      if (backdrop.style.getPropertyValue(property) !== px) backdrop.style.setProperty(property, px);
    }
    const panes = gridBackdropPanes(bounds);
    const masks=[],sizes=[],positions=[];
    for(const pane of panes)for(const [direction,start,fade]of [['right',pane.right,96],['bottom',pane.bottom,80]]) {
      if(start>=(direction==='right'?pane.width:pane.height))continue;
      masks.push(`linear-gradient(to ${direction},transparent ${start}px,#000 ${start+fade}px)`);
      sizes.push(`${pane.width}px ${pane.height}px`);positions.push(`${pane.x}px ${pane.y}px`);
    }
    for(const [key,value]of [['mask-image',masks.join(',')||'linear-gradient(transparent,transparent)'],['mask-size',sizes.join(',')||'100% 100%'],['mask-position',positions.join(',')||'0px 0px']]) {
      if(backdrop.style.getPropertyValue(key)!==value)backdrop.style.setProperty(key,value);
    }
  }

  window.ExcelLensGridBackdrop = Object.freeze({panes:gridBackdropPanes, paint});
})();
