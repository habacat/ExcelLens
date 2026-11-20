(() => {
  'use strict';
  class FillHandle {
    constructor(grid) {
      this.grid=grid;
      const region=grid.region;
      region.addEventListener('pointerdown',e=>this.start(e),true);
      region.addEventListener('pointermove',e=>{
        if(this.drag?.id!==e.pointerId)return;
        this.drag.x=e.clientX;this.drag.y=e.clientY;
        if(!this.frame)this.frame=requestAnimationFrame(()=>this.track(true));
      });
      region.addEventListener('pointerup',e=>this.end(e),true);
      for(const type of ['lostpointercapture','pointercancel'])region.addEventListener(type,e=>{if(this.drag?.id===e.pointerId)this.reset();});
      document.addEventListener('keydown',e=>{if(this.drag&&e.key==='Escape'){e.preventDefault();e.stopImmediatePropagation();this.reset();grid.focus();}},true);
    }
    start(e) {
      const g=this.grid;
      if(!g.active||e.button!==0||!e.target.closest('#rangeFrames .range-corner'))return;
      e.preventDefault();e.stopImmediatePropagation();
      if(g.blocked||g.stale||g.owner.data.encoding||g.view.filtered())return;
      const selection=g.view.areas();if(selection.length!==1)return;
      const s=selection[0];
      this.drag={id:e.pointerId,target:g.ranges.target(),top:Math.min(s.row1,s.row2),bottom:Math.max(s.row1,s.row2),
        left:Math.min(s.col1,s.col2),right:Math.max(s.col1,s.col2),x:e.clientX,y:e.clientY};
      g.region.setPointerCapture(e.pointerId);
    }
    track(scroll) {
      this.frame=0;
      const d=this.drag,g=this.grid;if(!d)return;
      const pos=g.view.dragPoint(d.x,d.y,'cell',scroll);
      const p=g.entry.hit(pos.x,pos.y);
      if(p?.kind==='cell'){
        const dr=p.row<d.top?p.row-d.top:Math.max(0,p.row-d.bottom);
        const dc=p.col<d.left?p.col-d.left:Math.max(0,p.col-d.right);
        if(!dr&&!dc){d.destination=null;this.area=null;}
        else{
          const vertical=!!dr&&(!dc||Math.abs(dr)>=Math.abs(dc));
          const row=vertical?p.row:d.bottom,col=vertical?d.right:p.col;
          d.destination={row,col};
          this.area={row1:Math.min(d.top,row),row2:Math.max(d.bottom,row),col1:Math.min(d.left,col),col2:Math.max(d.right,col),color:'var(--selection-color)'};
        }
        g.view.paintReferences();
      }
      if(pos.scrolling && scroll)this.frame=requestAnimationFrame(()=>this.track(true));
    }
    reset() {
      const id=this.drag?.id;
      this.drag=null;this.area=null;
      cancelAnimationFrame(this.frame);this.frame=0;
      if(id!=null&&this.grid.region.hasPointerCapture(id))this.grid.region.releasePointerCapture(id);
      this.grid.view.paintReferences();
    }
    end(e) {
      const d=this.drag;if(!d||d.id!==e.pointerId)return;
      e.preventDefault();e.stopImmediatePropagation();
      cancelAnimationFrame(this.frame);d.x=e.clientX;d.y=e.clientY;this.track(false);
      const area=this.area,g=this.grid,alternate=e.ctrlKey||e.metaKey;
      this.reset();
      if(!area)return;
      g.act(async()=>{
        const ref=(row,col)=>g.owner.colName(col)+row;
        g.ranges.current(d.target);
        g.ranges.checkSize(area.row1,area.row2,area.col1,area.col2);
        const plan=await g.owner.post('preview',{...d.target,mode:'auto-fill',ref:ref(d.top,d.left),end:ref(d.bottom,d.right),
          destination:ref(d.destination.row,d.destination.col),alternate,direct:true});
        g.ranges.current(d.target);
        if(plan.changes.length&&!await g.owner.commit('change',plan.changes,{direct:true,proof:plan.proof}))return false;
        g.view.selectArea(area);g.focus();
      },false,'正在填充选区');
    }
  }
  window.ExcelLensFillHandle=FillHandle;
})();
