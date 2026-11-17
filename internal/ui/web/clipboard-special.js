// Bounded clipboard representations and text-only paste plans. These functions
// never write to a workbook, execute formulas or infer a source numeric type.
(() => {
  'use strict';
  const clip=window.ExcelLensClipboard;
  const colName=column=>{let result='';for(let n=column;n>0;n=Math.floor((n-1)/26))result=String.fromCharCode(65+(n-1)%26)+result;return result;};
  function cellsOf(row,start,end){
    const cells=new Map();
    for(const cell of row.cells||[]){if(!Number.isSafeInteger(cell.col)||cell.col<start||cell.col>end||cells.has(cell.col))throw Error('复制单元格坐标无效或重复');cells.set(cell.col,cell);}
    return Array.from({length:end-start+1},(_,i)=>cells.get(start+i)||{col:start+i,ref:colName(start+i)+row.row,raw:'',display:'',hasFormula:false,present:false});
  }
  function valueOf(cell,mode='raw',styled=false){
    if(mode==='formula'&&cell.hasFormula){
      if(typeof cell.formula!=='string'||!cell.formula.startsWith('='))throw Error((cell.ref||'此单元格')+' 的公式无法还原，请使用保留状态的JSON');
      return String(cell.formula);
    }
    if(cell.cacheMissing)throw Error((cell.ref||'此单元格')+' 的公式缺少缓存值，请选择公式文字或JSON');
    return mode==='display'?String(styled&&cell.conditional?.display!=null?cell.conditional.display:cell.display??cell.raw??''):String(cell.raw??cell.display??'');
  }
  const markdown=value=>String(value).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/\\/g,'\\\\').replace(/\|/g,'\\|').replace(/\r\n?|\n/g,'<br>').replace(/\t/g,'&#9;').replace(/^ +| +$/g,s=>'&#32;'.repeat(s.length));
  function selection(payload,{format='tsv',mode='display',includeHeader=false,styled=false,sheet=''}={},limits){
    if(!['tsv','markdown','json'].includes(format)||!['display','raw','formula'].includes(mode))throw Error('复制方式无效');
    const rows=includeHeader?payload.rows.filter(row=>row.row!==payload.header?.row):payload.rows;
    if(includeHeader&&!payload.header)throw Error('请先读取所选表头行');
    const all=includeHeader?[payload.header,...rows]:rows,{start,end,limits:budget}=clip.checkCopy({...payload,rows:all},limits),notices=new Set();
    const inspect=row=>{if(row.presentationNotice)notices.add(row.presentationNotice);return cellsOf(row,start,end);};
    let text,html;
    if(format==='json'){
      const encode=row=>({row:row.row,cells:inspect(row).map(cell=>({col:cell.col,ref:cell.ref||colName(cell.col)+row.row,raw:String(cell.raw??''),display:String(cell.display??''),type:cell.type||'',hasFormula:!!cell.hasFormula,
        ...(cell.hasFormula?{formula:cell.formula||''}:{}),...(cell.cacheMissing?{cacheMissing:true}:{}),...(cell.sharedFormula?{sharedFormula:true}:{}),...(cell.presentationOnly?{presentationOnly:true}:{}),...(cell.present===false?{present:false}:{}),
        ...(cell.conditional?.display!=null?{presentationDisplay:cell.conditional.display}:{})}))});
      const header=includeHeader?encode(payload.header):null;
      const document={format:'ExcelLens.Selection',version:1,sheet:String(sheet),columns:Array.from({length:end-start+1},(_,i)=>({col:start+i,...(header?{header:header.cells[i].display}:{} )})),...(header?{header}:{}),rows:rows.map(encode)};
      text=JSON.stringify(document,null,2);
    }else{
      const values=all.map(row=>inspect(row).map(cell=>{if(styled&&cell.conditional?.notice)notices.add(cell.conditional.notice);return valueOf(cell,mode,styled);}));
      if(format==='tsv'){
        const literal={startCol:start,endCol:end,rows:values.map((row,i)=>({row:i+1,cells:row.map((value,j)=>({col:start+j,raw:value,display:value,type:'inlineStr',hasFormula:false}))}))};
        const result=clip.build(literal,[],false,'',null,budget);text=result.text;html=result.html;
      }
      else{
        const header=includeHeader?values.shift():Array.from({length:end-start+1},(_,i)=>colName(start+i));
        text=[header.map(markdown),header.map(()=>'---'),...values.map(row=>row.map(markdown))].map(row=>'| '+row.join(' | ')+' |').join('\n');
      }
    }
    if(clip.utf8Length(text)>budget.textBytes)throw Error('复制结果超过本次文字容量，请缩小选区或使用导出');
    return {text,...(html?{html}:{}),notices:[...notices],rows:rows.length,columns:end-start+1};
  }
  function parseJSON(text,{mode='raw',withHeader=false}={}){
    let document;try{document=JSON.parse(text);}catch{throw Error('JSON格式无效，未粘贴');}
    const string=value=>{if(value===null)return '';if(typeof value!=='string')throw Error('JSON单元格必须为字符串，数字请加引号以保留精度');return value;};
    if(Array.isArray(document)){
      if(document.length>2000||!document.every(row=>Array.isArray(row)&&row.length<=80)||document.reduce((sum,row)=>sum+row.length,0)>20000)throw Error('JSON应为容量以内的二维文字数组');
      return document.map(row=>row.map(string));
    }
    if(document?.format!=='ExcelLens.Selection'||document.version!==1||!Array.isArray(document.columns)||!document.columns.length||document.columns.length>80||!Array.isArray(document.rows)||document.rows.length>2000)throw Error('不支持此JSON结构、版本或容量');
    const columns=document.columns.map(column=>column.col);
    if(columns.some((col,i)=>!Number.isSafeInteger(col)||col<1||i>0&&col!==columns[i-1]+1))throw Error('JSON列坐标无效');
    const rows=withHeader?[document.header,...document.rows]:document.rows;
    if(withHeader&&!document.header)throw Error('JSON中没有附带表头');
    return rows.map(row=>{
      if(!row||!Number.isSafeInteger(row.row)||row.row<1||!Array.isArray(row.cells)||row.cells.length!==columns.length)throw Error('JSON行内容无效');
      const cells=new Map();
      for(const cell of row.cells){if(!cell||!columns.includes(cell.col)||cells.has(cell.col)||typeof cell.raw!=='string'||typeof cell.display!=='string'||typeof cell.type!=='string'||typeof cell.hasFormula!=='boolean'||(cell.hasFormula&&typeof cell.formula!=='string'))throw Error('JSON单元格值、类型或坐标无效');cells.set(cell.col,cell);}
      if(cells.size!==columns.length)throw Error('JSON行缺少单元格，未粘贴');
      return columns.map(col=>valueOf(cells.get(col),mode));
    });
  }
  function pastePlan(text,{format='tsv',mode='raw',withHeader=false,skipFirstRow=false,transpose=false,skipBlank=false,startRow,startCol,rowLimit=10000,columns}={}){
    text=String(text??'');if(text.length>2*1024*1024||clip.utf8Length(text)>2*1024*1024)throw Error('剪贴板内容超过2 MiB，请分批粘贴');
    let rows=format==='tsv'?clip.parse(text):format==='json'?parseJSON(text,{mode,withHeader}):null;
    if(!rows||!rows.length||rows.some(row=>!Array.isArray(row)))throw Error('没有可粘贴的数据');
    let width=Math.max(0,...rows.map(row=>row.length));
    if(!width||rows.length>2000||width>80||rows.reduce((sum,row)=>sum+row.length,0)>20000)throw Error('粘贴数据超过本次行列或单元格容量');
    for(const row of rows)for(const value of row)if(clip.characterCount(value)>4000)throw Error('单元格内容超过4000字符，未粘贴');
    if(skipFirstRow)rows=rows.slice(1);
    if(!rows.length)throw Error('跳过表头后没有可粘贴的数据');
    width=Math.max(0,...rows.map(row=>row.length));
    if(!width)throw Error('没有可粘贴的数据');
    if(transpose)rows=Array.from({length:width},(_,col)=>rows.map(row=>row[col]??''));
    const resultWidth=transpose?rows[0].length:width;
    if(!Number.isSafeInteger(startRow)||!Number.isSafeInteger(startCol)||startRow<0||startCol<0||!Number.isSafeInteger(columns)||columns<1||startRow+rows.length>rowLimit||startCol+resultWidth>columns)throw Error('粘贴范围超出编辑表边界，未粘贴；请增加行列或缩小范围');
    const changes=[];let skipped=0;
    rows.forEach((row,i)=>row.forEach((value,j)=>{if(skipBlank&&value==='')skipped++;else changes.push({row:startRow+i,col:startCol+j,value});}));
    clip.validateChanges(changes);
    return {changes,rows:rows.length,columns:resultWidth,skipped,preview:rows.slice(0,6).map(row=>row.slice(0,8))};
  }
  window.ExcelLensClipboardSpecial={selection,pastePlan};
})();
