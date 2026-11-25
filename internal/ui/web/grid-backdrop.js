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

  const pens=new Map();
  function pen(css){
    if(pens.has(css))return pens.get(css);
    const match=/^([\d.]+)px\s+(solid|double|dashed|dotted)\s+(.+)$/.exec(css||'');
    const result=match&&{width:+match[1],kind:match[2],color:match[3]};
    if(pens.size>=256)pens.clear();
    pens.set(css,result);return result;
  }
  function paintMissing({ctx,bounds,pinned,top,left,minVisual,maxVisual,colsToPaint,bufferedColumns,
    scrollLeft,w,h,hh,rowHead,state:S,scrollTop,sourceRowTop,colWidth,colLeft,rowHeight,rowTop,nextVisibleRow,rowVisible,
    lookup,mergeAt,styleFor,theme}){
    const {paper,neutral,line,surface,muted,dark}=theme;
    const drawGrid=!S.showStyles||S.sourceGridLines,lines=new Map(),labels=[];
    const addLine=(css,x,y,endX,endY,vertical)=>{
      const stroke=pen(css);if(!stroke)return;
      let path=lines.get(stroke);if(!path){path=new Path2D();lines.set(stroke,path);}
      const inset=stroke.width/2;
      if(vertical)x=endX=x-inset;else y=endY=y-inset;
      path.moveTo(x,y);path.lineTo(endX,endY);
    };
    const meta=[];
    for(const c of colsToPaint){
      if(c>S.maxCol||S.hiddenColumns.has(c))continue;
      const x=rowHead+colLeft(c)-scrollLeft,end=x+colWidth(c);
      if(end>left&&x<w)meta.push({c,x,end,mounted:bounds&&c>=bounds.startCol&&c<=bounds.endCol&&(!bufferedColumns||bufferedColumns.has(c))});
    }
    const filteredOffset=S.filtered?sourceRowTop(pinned.rows+1):0;
    const gridLine='1px solid '+line,styles=new Map();
    // Cache neighbours too. A null sample means unknown; a known empty cell can
    // still inherit its source row style. Filtering uses visual ranks for lookup.
    const sample=(r,c)=>{
      const key=r+','+c;
      if(styles.has(key))return styles.get(key);
      const record=lookup(r,c);
      const style=record?styleFor(record,c,r):null;
      styles.set(key,style);return style;
    };
    ctx.save();ctx.beginPath();ctx.rect(left,top,Math.max(0,w-left),Math.max(0,h-top));ctx.clip();
    for(let r=minVisual;r<=maxVisual;r=S.filtered?r+1:(nextVisibleRow(r+1)||Infinity)){
      if(!S.filtered&&!rowVisible(r))continue;
      const y=hh+rowTop(r)+filteredOffset-scrollTop,endY=y+rowHeight(r);
      if(endY<=top||y>=h)continue;
      const rowMounted=bounds&&r>=bounds.displayStart&&r<=bounds.displayEnd;
      let missing=false;
      for(let i=0;i<meta.length;i++){
        const {c,x,end,mounted}=meta[i];
        if(rowMounted&&mounted)continue;
        const merge=mergeAt(r,c),anchorR=merge?.row??r,anchorC=merge?.col??c;
        if(merge&&bounds&&anchorR>=bounds.displayStart&&anchorR<=bounds.displayEnd&&
          anchorC>=bounds.startCol&&anchorC<=bounds.endCol&&(!bufferedColumns||bufferedColumns.has(anchorC)))continue;
        missing=true;
        const own=sample(anchorR,anchorC),fill=own?.background;
        ctx.fillStyle=own?(fill&&dark?'color-mix(in srgb, '+fill+' 14%, '+paper+')':fill||paper):neutral;
        ctx.fillRect(x,y,end-x,endY-y);
        const edge=merge?sample(r,c):own;
        const right=meta[i+1]?.c??(c+1),below=S.filtered?r+1:(nextVisibleRow(r+1)||r+1);
        const seam=(primary,other,side)=>window.ExcelLensGridBorders.choose(primary,other?.[side])||(!fill&&!other?.background&&drawGrid?gridLine:null);
        if(!merge||right>merge.endCol)addLine(seam(edge?.borderRight||own?.borderRight,sample(r,right),'borderLeft'),end,y,end,endY,true);
        if(!merge||below>merge.endRow)addLine(seam(edge?.borderBottom||own?.borderBottom,sample(below,c),'borderTop'),x,endY,end,endY,false);
        if(colLeft(c)===0&&pen(own?.borderLeft))addLine(own.borderLeft,x+pen(own.borderLeft).width,y,x,endY,true);
        if(rowTop(r)===0&&pen(own?.borderTop))addLine(own.borderTop,x,y+pen(own.borderTop).width,end,y,false);
      }
      if(scrollLeft===0&&missing&&!rowMounted&&!S.filtered&&r>pinned.rows)labels.push({r,y,height:endY-y});
    }
    for(const [stroke,path] of lines){
      ctx.strokeStyle=stroke.color;ctx.lineWidth=stroke.width;
      ctx.setLineDash(stroke.kind==='dashed'?[3*stroke.width,2*stroke.width]:stroke.kind==='dotted'?[stroke.width,stroke.width]:[]);
      if(stroke.kind==='double'&&stroke.width>=2){
        // Stroke the two rails separately so the gap keeps the source background.
        ctx.lineWidth=stroke.width/3;
        ctx.save();ctx.translate(-stroke.width/3,-stroke.width/3);ctx.stroke(path);ctx.restore();
        ctx.save();ctx.translate(stroke.width/3,stroke.width/3);ctx.stroke(path);ctx.restore();
      }else ctx.stroke(path);
    }
    ctx.restore();
    ctx.font='10px Consolas, monospace';ctx.textAlign='right';ctx.textBaseline='middle';
    for(const {r,y,height} of labels){
      ctx.fillStyle=surface;ctx.fillRect(0,Math.max(top,y),rowHead,Math.min(h,y+height)-Math.max(top,y));
      ctx.fillStyle=muted;ctx.fillText(String(r),rowHead-8,y+height/2);
    }
  }
  window.ExcelLensGridBackdrop = Object.freeze({paintMissing,panes:gridBackdropPanes, paint});
})();
