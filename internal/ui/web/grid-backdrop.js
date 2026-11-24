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

  function paintMissing({ctx,bounds,pinned,top,left,minVisual,maxVisual,colsToPaint,bufferedColumns,
    grid,w,h,hh,rowHead,state:S,scrollTop,sourceRowTop,colWidth,colLeft,rowHeight,rowTop,nextVisibleRow,rowVisible}) {
    ctx.font="10px Consolas, monospace";
    const reading=document.documentElement?.getAttribute('data-reading-tone')==='dark'?getComputedStyle(document.documentElement):null;
    const fallbackColor=(name,original)=>reading?.getPropertyValue(name).trim()||original;
    const paperColor=fallbackColor('--paper',"#f8fafc");
    const lineColor=fallbackColor('--grid-line-color',"#e6e6e6");
    const surfaceColor=fallbackColor('--surface',"#f3f6f9");
    const mutedColor=fallbackColor('--muted',"#8493a4");
    const drawGridLines=!S.showStyles||S.sourceGridLines;
    const scrollLeft=grid.scrollLeft;
    const filteredFrozenTop=S.filtered?sourceRowTop(pinned.rows+1):0;
    const colMeta=[];
    for(const c of colsToPaint){
      if(c>S.maxCol||S.hiddenColumns.has(c))continue;
      const cellW=colWidth(c),x=rowHead+colLeft(c)-scrollLeft;
      if(x+cellW<=left||x>=w)continue;
      colMeta.push({x,cellW,x0:Math.max(left,x),x1:Math.min(w,x+cellW),
        inBounds:Boolean(bounds&&c>=bounds.startCol&&c<=bounds.endCol&&(!bufferedColumns||bufferedColumns.has(c)))});
    }
    const linePath=drawGridLines?new Path2D():null;
    const rowLabels=[];
    ctx.fillStyle=paperColor;
    for(let r=minVisual;r<=maxVisual;r=S.filtered?r+1:(nextVisibleRow(r+1)||Infinity)) {
      if(!S.filtered&&!rowVisible(r))continue;
      const cellH=rowHeight(r);
      const y=hh+rowTop(r)+filteredFrozenTop-scrollTop;
      if(y+cellH<=top||y>=h)continue;
      const rowInBounds=Boolean(bounds&&r>=bounds.displayStart&&r<=bounds.displayEnd);
      const y0=Math.max(top,y),y1=Math.min(h,y+cellH),bh=y1-y0;
      let rowMissing=false;
      for(let i=0;i<colMeta.length;i++) {
        const m=colMeta[i];
        if(rowInBounds&&m.inBounds)continue;
        rowMissing=true;
        ctx.fillRect(m.x0,y0,m.x1-m.x0,bh);
        if(linePath){
          const vx=m.x+m.cellW-0.5,hy=y+cellH-0.5;
          linePath.moveTo(vx,y0);linePath.lineTo(vx,y1);
          linePath.moveTo(m.x0,hy);linePath.lineTo(m.x1,hy);
        }
      }
      if(rowMissing&&!S.filtered&&r>pinned.rows)rowLabels.push({r,y,cellH});
    }
    if(linePath){ctx.strokeStyle=lineColor;ctx.stroke(linePath);}
    if(rowLabels.length){
      ctx.fillStyle=surfaceColor;
      for(const item of rowLabels)ctx.fillRect(0,item.y,rowHead,Math.min(item.cellH,h-item.y));
      ctx.fillStyle=mutedColor;ctx.textAlign="right";ctx.textBaseline="middle";
      for(const item of rowLabels)ctx.fillText(String(item.r),rowHead-8,item.y+item.cellH/2);
    }
  }
  window.ExcelLensGridBackdrop = Object.freeze({paintMissing,panes:gridBackdropPanes, paint});
})();
