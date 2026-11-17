/* Exchange only the reading axes. Every stored CellData keeps its source ref,
 * type, raw value and formula state; sparse blanks are not manufactured cells. */
(() => {
  'use strict';
  const integer=(n,min,max)=>Number.isSafeInteger(n)&&n>=min&&n<=max;
  function validate(payload,snapshot,limits,colName){
    const fail=()=>{throw Error('转置内容与原选区不一致，请重新读取');};
    for(const key of ['book','workbookKey','sheet'])if(payload?.[key]!==snapshot[key])fail();
    if(payload.startCol!==snapshot.firstColumn||payload.endCol!==snapshot.lastColumn||!Array.isArray(payload.rows)||payload.rows.length!==snapshot.rows||!integer(payload.rows.length,1,limits.rows))fail();
    const columns=payload.endCol-payload.startCol+1;
    if(!integer(columns,1,limits.columns)||columns*payload.rows.length>limits.cells||new TextEncoder().encode(JSON.stringify(payload)).length>limits.bytes)fail();
    const seen=new Set(),rows=[];
    for(const row of payload.rows){
      if(!integer(row?.row,1,snapshot.maxRow)||seen.has(row.row)||row.cells!==null&&!Array.isArray(row.cells)||(row.cells||[]).length>columns||row.style!==undefined&&!integer(row.style,0,Number.MAX_SAFE_INTEGER)||row.presentationNotice!==undefined&&typeof row.presentationNotice!=='string')fail();
      seen.add(row.row);const cells=new Map();
      for(const cell of row.cells||[]){
        if(!integer(cell?.col,payload.startCol,payload.endCol)||cells.has(cell.col)||cell.ref!==colName(cell.col)+row.row||typeof cell.raw!=='string'||typeof cell.display!=='string'||typeof cell.hasFormula!=='boolean'||!integer(cell.style,0,Number.MAX_SAFE_INTEGER))fail();
        for(const key of ['formula','type'])if(cell[key]!==undefined&&typeof cell[key]!=='string')fail();
        for(const key of ['cacheMissing','presentationOnly','sharedFormula'])if(cell[key]!==undefined&&typeof cell[key]!=='boolean')fail();
        if(cell.conditional?.display!==undefined&&typeof cell.conditional.display!=='string')fail();
        cells.set(cell.col,cell);
      }
      rows.push({number:row.row,style:row.style||0,notice:row.presentationNotice||'',cells});
    }
    return {payload,rows,firstColumn:payload.startCol,lastColumn:payload.endCol};
  }
  function at(model,sourceColumn,index,colName){
    const row=model.rows[index];if(!row||!integer(sourceColumn,model.firstColumn,model.lastColumn))return null;
    return {row:row.number,column:sourceColumn,ref:colName(sourceColumn)+row.number,cell:row.cells.get(sourceColumn)||null,style:row.style,notice:row.notice};
  }
  function value(item,mode,styled){
    const cell=item?.cell;if(!cell)return '';
    if(mode==='formula'&&cell.hasFormula)return window.ExcelLensCellView.formula(cell);
    if(cell.cacheMissing)return null;
    return mode==='display'?window.ExcelLensCellView.value(cell,false,styled):cell.raw;
  }
  const pageSize=(width,maximum)=>Math.max(1,Math.min(maximum,Math.floor((Math.max(0,width)-58)/140)));
  window.ExcelLensTransposeModel={validate,at,value,pageSize};
})();
