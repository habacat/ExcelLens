/* Bounded presentation of source strings; no numeric or type conversion. */
(() => {
  'use strict';
  const integer=(value,first,last)=>Number.isSafeInteger(value)&&value>=first&&value<=last;
  const fail=()=>{throw Error('记录卡响应与当前来源或字段页不一致，请重新读取');};
  function validate(page,request,limits,source,colName,totalColumns){
    if(!page||!integer(limits?.columns,1,source.maxColumns)||!integer(limits?.bytes,1,Number.MAX_SAFE_INTEGER))fail();
    for(const key of ['workbookKey','book','sheet','viewId','row','headerRow','firstColumn','pinnedThroughRow'])if(page[key]!==request[key])fail();
    if((page.filterId||'')!==(request.filterId||''))fail();
    if(!integer(page.totalColumns,1,source.maxColumns)||page.totalColumns!==totalColumns||!integer(request.columns,1,limits.columns)||!integer(page.firstColumn,1,totalColumns))fail();
    const count=Math.min(request.columns,totalColumns-page.firstColumn+1);
    if(page.columns!==count||!Array.isArray(page.fields)||page.fields.length!==count||!integer(page.row,1,source.maxRows)||!integer(page.headerRow,0,source.maxRows))fail();
    if(!integer(page.totalRecords,1,source.maxRows)||!integer(page.position,1,page.totalRecords)||!integer(page.previousRow,0,source.maxRows)||!integer(page.nextRow,0,source.maxRows))fail();
    if((page.position===1)!==(page.previousRow===0)||(page.position===page.totalRecords)!==(page.nextRow===0)||page.previousRow===page.row||page.nextRow===page.row||page.previousRow&&page.previousRow===page.nextRow)fail();
    if(!integer(page.rowStyle,0,Number.MAX_SAFE_INTEGER)||!integer(page.headerStyle,0,Number.MAX_SAFE_INTEGER)||typeof page.rowIndexed!=='boolean'||!Array.isArray(page.notices)||page.notices.some(text=>typeof text!=='string'))fail();
    const cell=(value,row,column)=>{
      if(!value||value.ref!==colName(column)+row||value.col!==column||typeof value.raw!=='string'||typeof value.display!=='string'||typeof value.hasFormula!=='boolean'||!integer(value.style,0,Number.MAX_SAFE_INTEGER))fail();
      for(const key of ['formula','type','valueColor'])if(value[key]!==undefined&&typeof value[key]!=='string')fail();
      for(const key of ['cacheMissing','sharedFormula','presentationOnly'])if(value[key]!==undefined&&typeof value[key]!=='boolean')fail();
      if(value.conditional?.display!=null&&typeof value.conditional.display!=='string')fail();
    };
    for(const [at,field] of page.fields.entries()){
      const column=request.firstColumn+at;if(field?.column!==column)fail();cell(field.cell,page.row,column);
      if(page.headerRow)cell(field.header,page.headerRow,column);else if(field.header!=null)fail();
    }
    if(new TextEncoder().encode(JSON.stringify(page)).length>limits.bytes)throw Error('记录卡响应超过显示容量，请减少每页字段数');
    return page;
  }
  function offsets(text,units){
    if(typeof text!=='string'||!integer(units,2,Number.MAX_SAFE_INTEGER))throw Error('记录卡文本容量无效');
    const result=[0];let at=0;
    while(at<text.length){
      let end=Math.min(text.length,at+units);
      if(end<text.length&&text.charCodeAt(end-1)>=0xd800&&text.charCodeAt(end-1)<=0xdbff&&text.charCodeAt(end)>=0xdc00&&text.charCodeAt(end)<=0xdfff)--end;
      result.push(end);at=end;
    }
    if(result.length===1)result.push(0);return result;
  }
  function preview(text,units){
    if(text.length<=units)return text;
    let end=units;if(text.charCodeAt(end-1)>=0xd800&&text.charCodeAt(end-1)<=0xdbff&&text.charCodeAt(end)>=0xdc00&&text.charCodeAt(end)<=0xdfff)--end;
    return text.slice(0,end);
  }
  const value=(cell,mode)=>cell.cacheMissing?null:mode==='raw'?cell.raw:window.ExcelLensCellView.value(cell,false,true);
  // The shared reader can retain diagnostic text in formula. Match the
  // existing formula-copy contract instead of presenting that text as code.
  const formula=cell=>window.ExcelLensCellView.formula(cell);
  const type=cell=>window.ExcelLensCellView.sourceType(cell);
  window.ExcelLensRecordCardModel={validate,offsets,preview,value,formula,type};
})();
