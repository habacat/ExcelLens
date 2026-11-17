(() => {
  'use strict';
  const lower=(values,test)=>{let lo=0,hi=values.length;while(lo<hi){const mid=(lo+hi)>>>1;if(test(values[mid]))hi=mid;else lo=mid+1;}return lo;};
  class RowVisibility {
    constructor(maximum,ranges=[]){
      if(!Number.isSafeInteger(maximum)||maximum<1)throw Error('源行容量无效');
      this.maximum=maximum;this.hidden=[];this.prefix=[];let previous=0,total=0;
      for(const r of ranges){
        if(!Number.isSafeInteger(r.min)||!Number.isSafeInteger(r.max)||r.min<=previous||r.max<r.min||r.max>maximum)throw Error('行显隐区间无效');
        this.hidden.push(Object.freeze({min:r.min,max:r.max}));total+=r.max-r.min+1;this.prefix.push(total);previous=r.max;
      }
      this.length=maximum-total;Object.freeze(this.hidden);Object.freeze(this.prefix);Object.freeze(this);
    }
    before(index){const row=Math.max(1,Math.min(this.maximum+1,index)),i=lower(this.hidden,r=>r.max>=row),r=this.hidden[i];return row-1-(i?this.prefix[i-1]:0)-(r&&r.min<row?row-r.min:0);}
    visible(row){if(!Number.isInteger(row)||row<1||row>this.maximum)return false;const r=this.hidden[lower(this.hidden,r=>r.max>=row)];return !r||r.min>row;}
    at(position){if(!Number.isInteger(position)||position<0||position>=this.length)return 0;let lo=0,hi=this.hidden.length;while(lo<hi){const mid=(lo+hi)>>>1;if(this.hidden[mid].min-1-(mid?this.prefix[mid-1]:0)>position)hi=mid;else lo=mid+1;}return position+1+(lo?this.prefix[lo-1]:0);}
    next(row){const rank=this.before(Math.max(1,row));return this.at(rank);}
    previous(row){const last=Math.min(this.maximum,row);return this.at(this.before(last+1)-1);}
    advance(row,delta){return this.at(this.before(row)+delta-(!this.visible(row)&&delta>0?1:0));}
    count(first,last){return last<first?0:this.before(last+1)-this.before(first);}
    selectionCount(area,filtered,through=0){
      const a=area.row1,b=area.row2;if(!filtered)return this.count(Math.min(a,b),Math.max(a,b));
      if(a<=through&&b<=through)return this.count(Math.min(a,b),Math.max(a,b));
      if(a<=through)return this.count(a,through)+area.visual2;
      if(b<=through)return this.count(b,through)+area.visual1;
      return Math.abs(area.visual1-area.visual2)+1;
    }
  }
  window.ExcelLensRowVisibility=RowVisibility;
})();
