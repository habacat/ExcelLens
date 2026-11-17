(() => {
  'use strict';
  const integer=(n,min=0,max=Number.MAX_SAFE_INTEGER)=>Number.isSafeInteger(n)&&n>=min&&n<=max;
  const exact=s=>typeof s==='string'&&s.length<=8192&&/^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?$|^[+-]?\d+\/[1-9]\d*$/.test(s);
  function validate(page,request,limits,rows,first,last,maxRow){
    const fail=()=>{throw Error('快速分析结果与当前选区不符，请重新运行');};
    if(!page||new TextEncoder().encode(JSON.stringify(page)).length>limits.bytes)fail();
    for(const key of ['workbookKey','book','sheet','viewId','start','end','skipFirstRow','minuend','subtrahend'])if(page[key]!==request[key])fail();
    for(const key of ['filterId','visualStart','visualEnd','excludeThroughRow'])if((page[key]||'')!==(request[key]||''))fail();
    if(page.selectedRows!==rows||page.rows!==rows-(request.skipFirstRow?1:0)||page.firstColumn!==first||page.lastColumn!==last||!Array.isArray(page.columns)||page.columns.length!==last-first+1)fail();
    const summary=(s,total)=>{
      if(!s||!integer(s.count,0,total)||!exact(s.sum))fail();
      if(s.count===0){if(s.sum!=='0'||s.average!==''||s.minimum!==''||s.maximum!==''||s.minimumRow!==0||s.maximumRow!==0)fail();}
      else if(!exact(s.average)||!exact(s.minimum)||!exact(s.maximum)||!integer(s.minimumRow,1,maxRow)||!integer(s.maximumRow,1,maxRow))fail();
    };
    const series=(s,count)=>{
      summary(s.summary,page.rows);if(s.summary.count!==count||!Array.isArray(s.segments)||s.segments.length!==Math.min(page.rows,limits.segments))fail();
      let next=1,total=0;
      for(const p of s.segments){
        if(p.first!==next||!integer(p.last,p.first,page.rows)||!integer(p.firstRow,1,maxRow)||!integer(p.lastRow,1,maxRow))fail();
        summary(p.summary,p.last-p.first+1);total+=p.summary.count;next=p.last+1;
      }
      if(next!==page.rows+1||total!==count)fail();
    };
    page.columns.forEach((col,i)=>{
      if(col.column!==first+i||!col.counts)fail();const c=col.counts;
      for(const key of ['number','negative','zero','positive','blank','other','errors','missing','invalid'])if(!integer(c[key],0,page.rows))fail();
      if(c.number!==c.negative+c.zero+c.positive||c.number+c.blank+c.other+c.errors+c.missing+c.invalid!==page.rows)fail();series(col,c.number);
    });
    if(request.minuend){const d=page.difference;if(!d||!integer(d.paired,0,page.rows)||!integer(d.unpaired,0,page.rows)||d.paired+d.unpaired!==page.rows)fail();series(d,d.paired);}
    else if(page.difference!=null)fail();
    return page;
  }
  function chart(series){
    return {columnKeys:['分段平均'],rows:series.segments.map((p,i)=>({keys:[String(i+1)],values:[{exact:p.summary.average,display:p.summary.average,count:p.summary.count}]}))};
  }
  window.ExcelLensSelectionQuickModel={validate,chart};
})();
