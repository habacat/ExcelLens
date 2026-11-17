(() => {
  'use strict';
  const labels=[];
  const own=(value,key)=>Object.prototype.hasOwnProperty.call(value,key);
  function label(col){
    if(labels[col])return labels[col];
    let n=col,text='';while(n>0){n--;text=String.fromCharCode(65+n%26)+text;n=Math.floor(n/26);}
    return labels[col]=text;
  }
  function unpack(rows){
    return rows.map(row=>{
      const suffix=String(row.n),out={number:row.n,cells:row.c.map(value=>{
        const cell={col:value.c,ref:own(value,'r')?value.r:label(value.c)+suffix,raw:value.v,display:own(value,'d')?value.d:value.v,hasFormula:Boolean(value.k&1)};
        if(value.t)cell.type=value.t;if(value.s||value.k&8)cell.style=value.s||0;if(value.f)cell.formula=value.f;
        if(value.k&2)cell.cacheMissing=true;if(value.k&4)cell.sharedFormula=true;
        if(value.o)cell.valueColor=value.o;if(value.h)cell.hyperlink=value.h;
        if(value.x)cell.richText=value.x;
        if(value.q)cell.conditional=value.q;if(value.k&16)cell.presentationOnly=true;
        return cell;
      })};
      if(row.s)out.style=row.s;if(row.p)out.presentationNotice=row.p;return out;
    });
  }
  window.ExcelLensWire={unpack};
})();
