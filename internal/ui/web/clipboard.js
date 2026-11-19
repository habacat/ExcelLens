/* Excel-compatible clipboard payload. Pure string construction: does not touch
 * the live virtual grid, evaluate formulas, or execute workbook HTML. */
(() => {
  'use strict';
  let configured=null;
  function configure(value){
    for(const key of ['rows','columns','cells','textBytes','htmlBytes'])if(!Number.isSafeInteger(value?.[key])||value[key]<1)throw Error('复制容量配置无效');
    configured=Object.freeze({...value});
  }
  function copyLimits(override){
    const limits={...configured,...override};
    for(const key of ['rows','columns','cells','textBytes','htmlBytes'])if(!Number.isSafeInteger(limits[key])||limits[key]<1)throw Error('复制容量配置尚未载入');
    return limits;
  }
  function checkCopy(payload,override){
    const limits=copyLimits(override),start=payload?.startCol,end=payload?.endCol;
    if(!Number.isSafeInteger(start)||!Number.isSafeInteger(end)||start<1||end<start||end-start+1>limits.columns||!Array.isArray(payload.rows)||payload.rows.length>limits.rows||payload.rows.length*(end-start+1)>limits.cells)throw Error('复制区域超出安全上限');
    return {start,end,limits};
  }
  const escape = value => String(value ?? '').replace(/&/g,'&amp;').replace(/</g,'&lt;')
    .replace(/>/g,'&gt;').replace(/"/g,'&quot;').replace(/'/g,'&#39;');
  // Excel interprets a plain HTML break as the start of another worksheet row.
  const cellHTML = value => escape(value).replace(/\r\n?|\n/g,'<br style="mso-data-placement:same-cell">');
  const tsvCell = value => {
    const text=String(value ?? '');
    return /[\t\r\n"]/.test(text)?'"'+text.replace(/"/g,'""')+'"':text;
  };
  const color = value => /^(#[0-9a-f]{3}(?:[0-9a-f]{3})?|rgba?\([\d\s,.%]+\))$/i.test(String(value||'')) ? value : '';
  const border = value => /^(?:\d+(?:\.\d+)?px\s+)?(?:solid|dashed|dotted|double)\s+(?:#[\da-f]{3}(?:[\da-f]{3})?|rgba?\([\d\s,.%]+\))$/i.test(String(value||''))?value:'';
  function styleOf(cell, palette, enabled, fontOverride,rich=false) {
    const source=window.ExcelLensConditional?.visual(cell,palette,0,enabled)??(enabled?palette?.[Number(cell?.style)||0]:null);
    const parts=['padding:2px 7px','border:1px solid #e3e7eb','vertical-align:middle'];
    if(source){
      const fg=color(window.ExcelLensConditional?.color(cell,source)??(cell?.valueColor||source.color)), bg=color(source.background);
      if(fg)parts.push('color:'+fg);
      if(bg)parts.push('background-color:'+bg);
      if(source.bold)parts.push('font-weight:bold');
      if(source.italic)parts.push('font-style:italic');
      if(!rich&&(source.underline||source.strike))parts.push('text-decoration:'+[
        source.underline&&'underline',source.strike&&'line-through'].filter(Boolean).join(' '));
      const underline=cell?.conditional?.style?.font?.underline||source.underlineStyle;
      if(!rich&&(underline==='double'||underline==='doubleAccounting'))parts.push('text-decoration-style:double');
      if(Number(source.fontSize)>0&&Number(source.fontSize)<=409)parts.push('font-size:'+Number(source.fontSize)+'pt');
      if(['left','center','right','justify'].includes(source.horizontal))parts.push('text-align:'+source.horizontal);
      if(['top','center','bottom'].includes(source.vertical))parts.push('vertical-align:'+(source.vertical==='center'?'middle':source.vertical));
      if(source.wrap)parts.push('white-space:pre-wrap');
      for(const [side,key] of [['top','borderTop'],['bottom','borderBottom'],['left','borderLeft'],['right','borderRight']]){
        const safe=border(source[key]);if(safe)parts.push('border-'+side+':'+safe);
      }
    }
    const font=String(fontOverride||source?.fontName||'').replace(/["'<>;\\\r\n]/g,'').slice(0,80);
    if(font)parts.push('font-family:"'+font+'"');
    // Do not convert apparent numbers stored as text into formulas or numbers.
    if(cell?.type==='s'||cell?.type==='inlineStr'||cell?.type==='str'||cell?.type==='e' ||
       (!cell?.type && /^(=|\+|\-|@)/.test(String(cell?.display||'')) && !/^-[\d,.]+$/.test(String(cell?.display||'')))) {
      parts.push('mso-number-format:"\\@"');
    }
    return parts.join(';');
  }
  // Rows in a filtered view, or a separately requested header, can be
  // reordered. Only merge down when the visible source members are together
  // in their original order. Horizontal spans are independent of row order.
  function mergeHeight(rows, index, merge) {
    const height=merge.visibleRows;
    if (index+height>rows.length) return 1;
    for (let offset=1;offset<height;offset++) {
      const row=rows[index+offset].row;
      if (row<=rows[index+offset-1].row || row>merge.endRow) return 1;
    }
    return height;
  }
  function build(payload, styles=[], showStyles=true, fontOverride='',richLimits,copyLimits,mergeAt) {
    const {start,end,limits}=checkCopy(payload,copyLimits);copyLimits=limits;
    const plain=[],notices=new Set(),html=[];let sourceBytes=0,htmlBytes=0;
    const append=(...parts)=>{for(const part of parts){htmlBytes+=utf8Length(part);if(copyLimits&&htmlBytes>copyLimits.htmlBytes)throw Error('复制HTML超过本次容量，请缩小选区或使用导出');html.push(part);}};
    append('<html><body><!--StartFragment--><table border="0" cellspacing="0" cellpadding="0" style="border-collapse:collapse">');
    const coveredUntil=new Uint32Array(end-start+1);
    for(const [index,row] of payload.rows.entries()){
      if(showStyles&&row.presentationNotice)notices.add(row.presentationNotice);
      const cells=new Map((row.cells||[]).map(x=>[x.col,x]));
      const values=[];
      append('<tr>');
      for(let col=start;col<=end;col++){
        if (coveredUntil[col-start]>index) {
          values.push('');
          continue;
        }
        let span='';
        const merge=mergeAt?.(row.row,col);
        if (merge) {
          const width=Math.min(end,merge.endCol)-col+1, height=mergeHeight(payload.rows,index,merge);
          if (width>1) span+=' colspan="'+width+'"';
          if (height>1) span+=' rowspan="'+height+'"';
          coveredUntil.fill(index+height,col-start,col-start+width);
        }
        const cell=cells.get(col)||{display:'',raw:'',style:0};
        sourceBytes+=utf8Length(cell.raw||'')+utf8Length(cell.display||'')+utf8Length(cell.formula||'');
        if(showStyles&&cell.conditional?.display!=null)sourceBytes+=utf8Length(cell.conditional.display);
        if(copyLimits&&sourceBytes>copyLimits.textBytes)throw Error('复制文字超过本次容量，请缩小选区或使用导出');
        const value=window.ExcelLensConditional?.value(cell,false,showStyles)??String(cell.display??cell.raw??'');
        values.push(tsvCell(value));
        const base=window.ExcelLensConditional?.visual(cell,styles)||styles[Number(cell.style)||0]||{};
        const rich=showStyles?window.ExcelLensRichText?.html(cell,base,{limits:richLimits,fontOverride},cellHTML):null;
        if(rich?.notice)notices.add(rich.notice);
        const notice=[rich?.notice,showStyles&&cell.conditional?.notice].filter(Boolean).join('；');if(notice)notices.add(notice);
        append('<td',span,' style="',escape(styleOf(cell,styles,showStyles,fontOverride,rich?.rich)),'"',notice?' title="'+escape(notice)+'"':'','>',rich?.html??cellHTML(value),'</td>');
      }
      plain.push(values.join('\t'));
      append('</tr>');
    }
    append('</table><!--EndFragment--></body></html>');
    const text=plain.join('\n');if(copyLimits&&utf8Length(text)>copyLimits.textBytes)throw Error('复制文字超过本次容量，请缩小选区或使用导出');
    return {text,html:html.join(''),notices:[...notices]};
  }
  // Excel/WPS quote cells containing tabs, line breaks or double quotes.
  // Bound parsing before constructing edits; never partially paste a payload.
  const characterCount=value=>{let count=0;for(const _ of String(value??''))count++;return count;};
  const utf8Length=value=>{let bytes=0;for(const ch of String(value??'')){const n=ch.codePointAt(0);bytes+=n<=0x7f?1:n<=0x7ff?2:n<=0xffff?3:4;}return bytes;};
  const requestLimit=2*1024*1024,requestReserve=1024,maxBatchCells=20000;
  const changeBytes=change=>{
    if(characterCount(change.value)>4000)throw Error('单元格内容超过4000字符，请缩短后重试');
    return utf8Length(JSON.stringify({row:change.row,col:change.col,value:change.value}))+1;
  };
  function validateChanges(changes){
    if(changes.length>maxBatchCells)throw Error('一次最多粘贴20,000个单元格');
    let bytes=requestReserve;for(const change of changes){bytes+=changeBytes(change);if(bytes>requestLimit)throw Error('编辑请求超过2 MiB，请分批粘贴');}
    return changes;
  }
  // Only explicit retry may split pre-existing independent drafts. Initial
  // pastes are validated as one atomic transaction by pasteChanges.
  function* batches(changes){
    let batch=[],bytes=requestReserve;
    for(const change of changes){
      const size=changeBytes(change);
      if(batch.length&&(batch.length===maxBatchCells||bytes+size>requestLimit)){yield batch;batch=[];bytes=requestReserve;}
      batch.push(change);bytes+=size;
    }
    if(batch.length)yield batch;
  }
  function parse(text,{maxRows=2000,maxCols=80,maxCells=20000,maxCellLength=4000,maxTextLength=2*1024*1024}={}){
    text=String(text??'');
    if(text.length>maxTextLength||utf8Length(text)>maxTextLength)throw Error('剪贴板内容超过2 MiB，请分批粘贴');
    const rows=[];let row=[],cell='',quoted=false,closed=false,endedRow=false,cells=0;
    const pushCell=()=>{
      if(characterCount(cell)>maxCellLength)throw Error(`单元格内容超过${maxCellLength}字符，未粘贴`);
      if(row.length>=maxCols)throw Error(`一次最多粘贴${maxCols}列，未粘贴`);
      if(++cells>maxCells)throw Error(`一次最多粘贴${maxCells.toLocaleString()}个单元格`);
      row.push(cell);cell='';closed=false;
    };
    const pushRow=()=>{pushCell();if(rows.length>=maxRows)throw Error(`一次最多粘贴${maxRows}行`);rows.push(row);row=[];};
    for(let i=0;i<text.length;i++){
      const ch=text[i];endedRow=false;
      if(quoted){
        if(ch==='"'){
          if(text[i+1]==='"'){cell+='"';i++;}else{quoted=false;closed=true;}
        }else cell+=ch;
      }else if(ch==='\t'){pushCell();}
      else if(ch==='\r'||ch==='\n'){
        if(ch==='\r'&&text[i+1]==='\n')i++;
        pushRow();endedRow=true;
      }else if(closed)throw Error('剪贴板引号格式无效，未粘贴');
      else if(ch==='"'&&cell==='')quoted=true;
      else cell+=ch;
      if(cell.length>maxCellLength*2)throw Error(`单元格内容超过${maxCellLength}字符，未粘贴`);
    }
    if(quoted)throw Error('剪贴板引号未闭合，未粘贴');
    if(!endedRow)pushRow();
    return rows;
  }
  function pasteChanges(text,{startRow,startCol,rowLimit=10000,columns}){
    const rows=parse(text);
    if(!Number.isSafeInteger(startRow)||!Number.isSafeInteger(startCol)||startRow<0||startCol<0||
       startRow+rows.length>rowLimit||rows.some(row=>startCol+row.length>columns))throw Error('粘贴范围超出编辑表边界，未粘贴；请增加行列或缩小范围');
    const changes=[];
    rows.forEach((row,i)=>row.forEach((value,j)=>changes.push({row:startRow+i,col:startCol+j,value})));
    return validateChanges(changes);
  }
  window.ExcelLensClipboard={configure,copyLimits,checkCopy,build,styleOf,tsvCell,parse,pasteChanges,characterCount,utf8Length,validateChanges,batches};
})();
