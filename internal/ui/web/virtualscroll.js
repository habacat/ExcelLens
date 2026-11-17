/* Logical worksheet pixels and the browser's bounded native scrollbar.
 * Native thumb dragging spans the entire file; wheel input retains row-scale
 * precision. Overscan never participates in the scroll range. */
(() => {
  'use strict';
  class VirtualScroll {
    constructor(maxPixels=15000000) { this.maxPixels=maxPixels;this.scale=1;this.logicalMax=0;this.physicalMax=0; }
    configure(height,viewport) {
      const physical=Math.max(viewport,Math.min(this.maxPixels,height));
      if(this.logicalMax!==Math.max(0,height-viewport)||this.physicalMax!==Math.max(0,physical-viewport))this.wheelAnchor=null;
      this.logicalMax=Math.max(0,height-viewport);
      this.physicalMax=Math.max(0,physical-viewport);
      this.scale=this.physicalMax>0?Math.max(1,this.logicalMax/this.physicalMax):1;
      return physical;
    }
    logical(physical) { return Math.min(this.logicalMax,Math.max(0,physical)*this.scale); }
    physical(logical) { return Math.min(this.physicalMax,Math.max(0,logical)/this.scale); }
    rowPosition(logicalRow,physical) { return logicalRow-this.logical(physical)+physical; }
    wheel(event,physical,rowHeight,pageHeight) {
      const unit=event.deltaMode===1?rowHeight:event.deltaMode===2?Math.max(rowHeight,pageHeight):1;
      const x=(event.deltaX||0)*unit,y=(event.deltaY||0)*unit;
      if(event.shiftKey&&!x) return {top:physical,left:y};
      // Engines quantize scrollTop to fractional CSS pixels. Accumulate the
      // logical target while the native position matches the last wheel
      // assignment, so tiny touchpad deltas are not rounded away on huge files.
      const previous=this.wheelAnchor;
      const base=previous&&Math.abs(physical-previous.physical)<=1/32?previous.logical:this.logical(physical);
      const target=Math.min(this.logicalMax,Math.max(0,base+y));
      // Browser scrollTop can retain subpixel residue after trackpad inertia.
      // A gesture reaching either endpoint always lands exactly there.
      const top=target<=0?0:target>=this.logicalMax?this.physicalMax:this.physical(target);
      this.wheelAnchor={physical:top,logical:target};
      return {top,left:x};
    }
  }
  window.ExcelLensVirtualScroll=VirtualScroll;
})();
