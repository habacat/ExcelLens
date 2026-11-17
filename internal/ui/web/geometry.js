/* Sparse per-sheet spreadsheet geometry. O(log n) seek by pixel / index.
 * Overrides are in unzoomed CSS pixels, sorted into a tiny prefix-delta index.
 * Large worksheets therefore require no million-entry layout allocation. */
(() => {
  'use strict';
  class Axis {
    constructor(base, max) {this.base=base; this.max=max; this.values=new Map(); this.positions=[]; this.deltas=[]; this.ends=[];this.source=[];this.sourceDeltas=[];this.hidden=[];this.hiddenPixels=[];this.hiddenStarts=[];this.version=0;}
    setHidden(ranges=[]){
      const clean=[];let previous=0;
      for(const r of ranges){if(!Number.isSafeInteger(r.min)||!Number.isSafeInteger(r.max)||r.min<=previous||r.max<r.min||r.max>this.max)throw Error('行显隐区间无效');const last=clean.at(-1);if(last&&last.max+1===r.min)last.max=r.max;else clean.push({min:r.min,max:r.max});previous=r.max;}
      this.hidden=clean;this.reindexHidden();this.version++;
    }
    hiddenAt(index){let lo=0,hi=this.hidden.length;while(lo<hi){const mid=(lo+hi)>>>1;if(this.hidden[mid].max<index)lo=mid+1;else hi=mid;}return lo;}
    visible(index){const r=this.hidden[this.hiddenAt(index)];return !r||r.min>index;}
    reindexHidden(){let start=0,sourceEnd=0;this.hiddenPixels=[];this.hiddenStarts=[];for(const r of this.hidden){start+=this.rawOffset(r.min)-sourceEnd;this.hiddenStarts.push(start);sourceEnd=this.rawOffset(r.max+1);this.hiddenPixels.push(sourceEnd-start);}}
    setBase(base){ if(this.base!==base){this.base=base;this.reindex();} }
    set(index,value){
      if(!Number.isInteger(index)||index<1||index>this.max)return false;
      if(value===0||value===null||value===undefined){this.values.delete(index);}
      else if(!Number.isFinite(value)||value<10||value>1200)return false;
      else this.values.set(index,value);
      this.reindex();return true;
    }
    load(values,ranges){this.source=[];let last=0;for(const r of ranges||[]){
      if(Number.isInteger(r.min)&&Number.isInteger(r.max)&&r.min>last&&r.max>=r.min&&r.max<=this.max&&Number.isFinite(r.size)&&r.size>=10&&r.size<=1200){this.source.push(r);last=r.max;}
    }this.values.clear();for(const [key,v] of Object.entries(values||{})){
      const i=Number(key), n=Number(v);
      if(Number.isInteger(i)&&i>=1&&i<=this.max&&Number.isFinite(n)&&(n===0||(n>=10&&n<=1200)))this.values.set(i,n);
    }this.reindex();}
    sourceMeasure(index){let lo=0,hi=this.source.length;while(lo<hi){const mid=(lo+hi)>>>1;if(this.source[mid].max<index)lo=mid+1;else hi=mid;}const r=this.source[lo];return r&&r.min<=index?r.size:this.base;}
    sourceOffset(index){
      let lo=0,hi=this.source.length;while(lo<hi){const mid=(lo+hi)>>>1;if(this.source[mid].max<index)lo=mid+1;else hi=mid;}
      const r=this.source[lo];return (index-1)*this.base+(lo?this.sourceDeltas[lo-1]:0)+(r&&r.min<index?(index-r.min)*(r.size-this.base):0);
    }
    reindex(){
      let sourceDelta=0;this.sourceDeltas=this.source.map(r=>sourceDelta+=(r.max-r.min+1)*(r.size-this.base));
      this.positions=[...this.values.keys()].sort((a,b)=>a-b);
      this.deltas=[];this.ends=[];let total=0;
      for(const p of this.positions){
        total+=this.values.get(p)-this.sourceMeasure(p);
        this.deltas.push(total);
        // The end of each customized row/column, in source pixels.
        // Ends are strictly increasing because all sizes are positive.
        this.ends.push(this.sourceOffset(p+1)+total);
      }
      this.reindexHidden();this.version++;
    }
    measure(index){return this.visible(index)?this.values.get(index)??this.sourceMeasure(index):0;}
    rawOffset(index){
      const row=Math.max(1,Math.min(this.max+1,Math.floor(index)));
      // Number of customized entries STRICTLY before row.
      let lo=0,hi=this.positions.length;
      while(lo<hi){const mid=(lo+hi)>>1;if(this.positions[mid]<row)lo=mid+1;else hi=mid;}
      return this.sourceOffset(row)+(lo?this.deltas[lo-1]:0);
    }
    offset(index){
      const row=Math.max(1,Math.min(this.max+1,Math.floor(index))),i=this.hiddenAt(row),r=this.hidden[i];
      if(r&&r.min<=row)return this.hiddenStarts[i];
      // Anchor each visible run to its preceding compressed boundary. This
      // keeps every hidden row's start/end identical even at fractional sizes.
      return i?this.hiddenStarts[i-1]+(this.rawOffset(row)-this.rawOffset(this.hidden[i-1].max+1)):this.rawOffset(row);
    }
    total(count){return this.offset(Math.min(this.max+1,count+1));}
    paintOffset(index,factor=1,ratio=1){return Math.round(this.offset(index)*factor*ratio)/ratio;}
    paintSize(index,factor=1,ratio=1){return this.paintOffset(index+1,factor,ratio)-this.paintOffset(index,factor,ratio);}
    at(pixel){
      if(!this.hidden.length)return this.rawAt(pixel);
      const pos=Math.max(0,pixel);if(Number.isNaN(pos))return 1;
      let lo=0,hi=this.hiddenStarts.length;while(lo<hi){const mid=(lo+hi)>>>1;if(this.hiddenStarts[mid]<=pos)lo=mid+1;else hi=mid;}
      let index=this.rawAt(pos+(lo?this.hiddenPixels[lo-1]:0));
      if(!this.visible(index)){const r=this.hidden[this.hiddenAt(index)];index=r.max<this.max?r.max+1:Math.max(1,r.min-1);}
      // Adding removed pixels back can round to the preceding source row.
      // Resolve the boundary with the same compressed arithmetic as painting.
      if(index>1&&this.offset(index)>pos){const previous=index-1,r=this.hidden[this.hiddenAt(previous)];index=r&&r.min<=previous?Math.max(1,r.min-1):previous;}
      else if(index<this.max&&this.offset(index+1)<=pos){const next=index+1,r=this.hidden[this.hiddenAt(next)],candidate=r&&r.min<=next?r.max+1:next;if(candidate<=this.max)index=candidate;}
      return index;
    }
    rawAt(pixel){
      // Invert the piecewise-linear prefix sum in ONE binary search over
      // customized row/column ends. The old algorithm did a binary search
      // over 1M row numbers and another binary search inside every probe.
      const pos=Math.max(0,pixel);
      if(Number.isNaN(pos))return 1;
      // Source HTML row runs stay compressed; seeking never expands millions
      // of repeated heights into a per-row array. Local overrides still win.
      if(this.source.length){let low=1,high=this.max;while(low<high){const mid=Math.floor((low+high)/2);if(this.rawOffset(mid+1)<=pos)low=mid+1;else high=mid;}return low;}
      let lo=0,hi=this.ends.length;
      while(lo<hi){const mid=(lo+hi)>>>1;if(this.ends[mid]<=pos)lo=mid+1;else hi=mid;}
      const delta=lo?this.deltas[lo-1]:0;
      let index=Math.floor((pos-delta)/this.base)+1;
      // When the next customized interval is wider than the base, a
      // naive division can overshoot into the following row: clamp it.
      if(lo<this.positions.length && index>this.positions[lo])index=this.positions[lo];
      // The reference seek uses offset(index) which calculates base * row
      // before adding the prefix delta. Floating-point division can round
      // down one integer at a fractional-pixel boundary: check that boundary
      // using identical arithmetic, without another nested binary search.
      if(lo && index<=this.positions[lo-1])index=this.positions[lo-1]+1;
      index=Math.max(1,Math.min(this.max,index));
      const nextCustom=lo<this.positions.length?this.positions[lo]:this.max+1;
      const start=(index-1)*this.base+delta;
      const end=index*this.base+(index===nextCustom?this.deltas[lo]:delta);
      if(index>1 && start>pos)index--;
      else if(index<this.max && end<=pos)index++;
      return index;
    }
  }
  class Geometry {
    static rowHeaderWidth(maxRow,ratio=1){
      const value=Number(maxRow),digits=String(Number.isFinite(value)?Math.max(1,Math.floor(value)):1).length;
      const dpr=Number.isFinite(ratio)&&ratio>0?ratio:1;
      return Math.ceil(Math.max(26,12+6*digits)*dpr)/dpr;
    }
    constructor(maxRows,maxColumns){
      if(!Number.isSafeInteger(maxRows)||maxRows<1||!Number.isSafeInteger(maxColumns)||maxColumns<1)throw Error('Geometry requires injected source limits');
      this.rows=new Axis(25,maxRows);this.cols=new Axis(132,maxColumns);
    }
    get version(){return `${this.rows.version}:${this.cols.version}`;}
    load(data){this.rows.load(data?.rows);this.cols.load(data?.cols);}
    set(kind,index,size){return (kind==='row'?this.rows:this.cols).set(index,size);}
  }
  window.ExcelLensGeometry=Geometry;
})();
