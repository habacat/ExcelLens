(() => {
  'use strict';
  const integer=(n,min=0,max=Number.MAX_SAFE_INTEGER)=>Number.isSafeInteger(n)&&n>=min&&n<=max;
  // Validate bounded numeric representations without interpreting source text
  // or converting financial values to binary floating point.
  const exact=s=>typeof s==='string'&&s.length<=8192&&/^(?:[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?|[+-]?\d+\/[1-9]\d*)$/.test(s);
  function validate(page,request,limits,source){
    const fail=()=>{throw Error('数据洞察结果与当前来源或范围不符，请重试');};
    if(!page||!limits?.bytes||new TextEncoder().encode(JSON.stringify(page)).length>limits.bytes)fail();
    for(const key of ['workbookKey','book','sheet','start','end','bindingViewId'])if(page[key]!==request[key])fail();
    for(const key of ['filterId','bindingFilterId','viewId','visualStart','visualEnd','excludeThroughRow'])if((page[key]||'')!==(request[key]||''))fail();
    if(!!page.includeDistribution!==!!request.includeDistribution||page.range!==request.start+':'+request.end||page.filtered!==!!request.filterId)fail();
    if(!integer(page.rows,0,source.maxRows)||!integer(page.firstColumn,1,source.maxColumns)||!integer(page.lastColumn,page.firstColumn,source.maxColumns)||!integer(page.total,0,limits.cells)||page.total!==page.rows*(page.lastColumn-page.firstColumn+1))fail();
    for(const key of ['nonEmpty','blank','number','text','formulas','cacheMissing','errors'])if(!integer(page[key],0,page.total))fail();
    if(page.nonEmpty+page.blank!==page.total||page.number+page.text!==page.nonEmpty||page.formulas>page.nonEmpty||page.cacheMissing>page.formulas||page.errors>page.text||!exact(page.sumExact))fail();
    for(const key of ['sum','average','minimum','maximum'])if(typeof page[key]!=='string'||page[key].length>8192)fail();
    if(page.number===0){if(page.sumExact!=='0'||page.averageExact!==''||page.minimumExact!==''||page.maximumExact!=='')fail();}
    else if(!exact(page.averageExact)||!exact(page.minimumExact)||!exact(page.maximumExact))fail();
    if(!integer(page.unique,0,page.nonEmpty)||typeof page.uniqueTruncated!=='boolean'||!Array.isArray(page.topValues))fail();
    for(const value of page.topValues)if(typeof value?.value!=='string'||!integer(value.count,1,page.nonEmpty))fail();
    for(const key of ['errorRefs','missingRefs'])if(!Array.isArray(page[key])||page[key].some(ref=>typeof ref!=='string'||!/^[A-Z]+[1-9]\d*$/.test(ref)))fail();
    if(page.warning!==undefined&&typeof page.warning!=='string')fail();
    if(request.includeDistribution){
      const d=page.distribution;
      if(!d||d.method!=='exact'||d.definition!=='inclusive-linear'||d.count!==page.number||!Array.isArray(d.quantiles)||!Array.isArray(d.bins))fail();
      const percents=page.number?[5,25,50,75,95]:[];
      if(d.quantiles.length!==percents.length||d.quantiles.some((q,i)=>q?.percent!==percents[i]||!exact(q.value)))fail();
      if(page.number===0&&d.bins.length||page.number>0&&(d.bins.length!==limits.bins&&d.bins.length!==1))fail();
      if(d.bins.length&&(d.bins[0].lower!==page.minimumExact||d.bins.at(-1).upper!==page.maximumExact))fail();
      let count=0;
      for(const [at,bin]of d.bins.entries()){
        if(!exact(bin?.lower)||!exact(bin.upper)||!integer(bin.count,0,page.number)||bin.upperInclusive!==(at===d.bins.length-1)||at&&d.bins[at-1].upper!==bin.lower)fail();count+=bin.count;
      }
      if(count!==page.number||d.bins.length===1&&(d.bins[0].lower!==d.bins[0].upper||d.bins[0].count!==page.number))fail();
    }else if(page.distribution!=null)fail();
    return page;
  }
  window.ExcelLensProfileModel={validate};
})();
