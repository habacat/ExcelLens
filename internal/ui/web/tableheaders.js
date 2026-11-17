/* TXT table presentation. Source records and row numbers remain intact.
 * The sticky column strip retains field names independently of row tiles. */
(() => {
  'use strict';
  const isTable=config=>Boolean(config?.table&&config.row>0);
  function fromHeader(header) {
    if(!header||!Number.isInteger(header.row)||header.row<1||!Array.isArray(header.columns)||!header.columns.length)return null;
    const names=header.columns.slice(0,16384).map(value=>String(value??''));
    return {row:header.row,startCol:1,endCol:names.length,names,table:true,detected:Boolean(header.detected),rules:new Map(),sortColumn:0,descending:false};
  }
  function columnName(config,col,colName) {
    return isTable(config)&&col>=config.startCol&&col<=config.endCol ? config.names?.[col-1]||`${colName(col)} 列` : '';
  }
  function useStickyLabels(config,scrollTop,headerBottom,frozenRows=0) {
    // Keep the source header in place, with its original row number. Only
    // repeat its labels after it has scrolled entirely behind the column strip.
    return isTable(config)&&config.row>frozenRows&&scrollTop>=headerBottom;
  }
  function decorate(head,col,config,colName,ready) {
    const name=columnName(config,col,colName);
    if(!name)return false;
    head.classList.add('table-column-head');
    head.title=`${colName(col)} · ${name}（表头第 ${config.row} 行）`;
    const letter=document.createElement('span');letter.className='table-column-letter';letter.textContent=colName(col);letter.setAttribute('aria-hidden','true');
    const label=document.createElement('span');label.className='table-column-name';label.textContent=name;
    const arrow=document.createElement('button');arrow.type='button';arrow.className='auto-filter-button'+(config.rules.has(col)?' is-filtered':'');
    arrow.dataset.column=String(col);arrow.textContent='▾';arrow.disabled=!ready;
    arrow.setAttribute('aria-label',`${name}：筛选和排序`);arrow.title=ready?`${name}：筛选和排序`:'完整索引建立后可筛选和排序';
    head.append(letter,label,arrow);
    return true;
  }
  window.ExcelLensTableHeaders={fromHeader,isTable,columnName,useStickyLabels,decorate};
})();
