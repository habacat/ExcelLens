/* Print geometry is exact integer nanopts. Only final, bounded CSS pixels
 * become Numbers; differences are taken before any lossy conversion. */
(() => {
  'use strict';
  const max64=9223372036854775807n,integer=n=>Number.isSafeInteger(n),object=v=>v&&typeof v==='object'&&!Array.isArray(v);
  const fail=()=>{throw Error('预览返回的页面数据无效，请重新准备');};
  function units(value,negative=false){
    if(typeof value!=='string'||! /^(?:0|-?[1-9]\d*)$/.test(value)||value.length>20)fail();
    const n=BigInt(value);if(n>max64||n< -max64||!negative&&n<0n)fail();return n;
  }
  const {columnName, columnNumber} = window.ExcelLensAddress;
  function ref(text,source){
    const match=/^\$?([A-Za-z]+)\$?([1-9]\d*)$/.exec(text.trim());
    if(!match)throw Error('区域地址无效：'+text);
    const col=columnNumber(match[1]),row=Number(match[2]);
    if(!integer(row)||!integer(col)||row>source.maxRows||col>source.maxColumns)throw Error('区域地址超出此来源范围：'+text);
    return {row,col};
  }
  function list(text,maximum){const parts=text.trim().split(/[,，;；]/).map(s=>s.trim());if(parts.some(s=>!s)||parts.length>maximum)throw Error('区域数量超限或含空地址');return parts;}
  function areas(text,source,limits){
    return list(text,limits.areas).map(part=>{const pair=part.split(':');if(pair.length>2)throw Error('区域地址无效：'+part);const a=ref(pair[0],source),b=ref(pair[1]||pair[0],source);if(b.row<a.row||b.col<a.col)throw Error('区域终点不能在起点之前');return {row:a.row,col:a.col,endRow:b.row,endCol:b.col};});
  }
  function titles(text,axis,source,limits){
    const maximum=axis==='row'?source.maxRows:source.maxColumns;
    return list(text,limits.areas).map(part=>{const pair=part.replaceAll('$','').split(':').map(s=>s.trim());if(pair.length>2)throw Error('重复标题范围无效');const number=value=>axis==='row'&&/^[1-9]\d*$/.test(value)?Number(value):axis==='col'&&/^[a-z]+$/i.test(value)?columnNumber(value):NaN;const first=number(pair[0]),last=number(pair[1]||pair[0]);if(!integer(first)||!integer(last)||first<1||last<first||last>maximum)throw Error('重复标题范围超出此来源或顺序无效');return {first,last};});
  }
  const papers={a4:{width:'210mm',height:'297mm'},a3:{width:'297mm',height:'420mm'},letter:{width:'8.5in',height:'11in'},legal:{width:'8.5in',height:'14in'}};
  function decimal(text,label,positive=true){const value=String(text).trim();if(value.length>64||! /^(?:\d+(?:\.\d*)?|\.\d+)$/.test(value)||!Number.isFinite(Number(value))||Number(value)<0||positive&&Number(value)===0)throw Error(label+'请输入'+(positive?'正数':'不小于零的数'));return value;}
  function count(text,label,max){const value=String(text).trim();if(!/^\d+$/.test(value)||!integer(Number(value))||Number(value)>max)throw Error(label+'超出允许范围');return Number(value);}
  function options(input,source,limits){
    if(!integer(source?.maxRows)||!integer(source?.maxColumns))throw Error('来源容量不可用，请重新打开工作表');
    const out={usePreviewDefaults:!!input.defaults};
    if(input.paper==='custom')out.paper={width:decimal(input.width,'纸张宽度')+'mm',height:decimal(input.height,'纸张高度')+'mm'};
    else if(Object.hasOwn(papers,input.paper))out.paper={...papers[input.paper]};
    else if(input.paper!=='source')throw Error('请选择有效纸张');
    if(input.orientation){if(!['portrait','landscape'].includes(input.orientation))throw Error('请选择有效纸张方向');out.orientation=input.orientation;}
    if(input.scaleMode==='percent'){out.scalePercent=decimal(input.percent,'打印缩放');if(Number(out.scalePercent)<1||Number(out.scalePercent)>400)throw Error('打印缩放须在1%到400%之间');}
    else if(input.scaleMode==='fit'){out.fit={width:count(input.fitWidth,'横向页数',limits.pages),height:count(input.fitHeight,'纵向页数',limits.pages)};if(!out.fit.width&&!out.fit.height)throw Error('适配页数至少指定一个方向，0表示该方向不限');}
    else if(input.scaleMode!=='source')throw Error('请选择有效打印缩放方式');
    if(input.area.trim())out.areas=areas(input.area,source,limits);
    for(const [key,axis] of [['repeatRows','row'],['repeatColumns','col']]){if(input['clear'+key[0].toUpperCase()+key.slice(1)])out[key]=[];else if(input[key].trim())out[key]=titles(input[key],axis,source,limits);}
    if(input.marginMode==='custom'){out.margins={};for(const key of ['left','right','top','bottom','header','footer'])out.margins[key]=decimal(input[key],'页边距',false)+'mm';}
    else if(input.marginMode!=='source')throw Error('请选择有效页边距设置');
    if(input.pageOrder){if(!['downThenOver','overThenDown'].includes(input.pageOrder))throw Error('请选择有效页序');out.pageOrder=input.pageOrder;}
    if(input.firstPage.trim()){if(!/^-?\d+$/.test(input.firstPage.trim()))throw Error('起始页码请输入整数');out.firstPageNumber=Number(input.firstPage);if(!integer(out.firstPageNumber)||out.firstPageNumber<limits.firstPageMin||out.firstPageNumber>limits.firstPageMax)throw Error('起始页码超出源格式范围');}
    if(input.rowHeight.trim())out.missingRowHeight=decimal(input.rowHeight,'缺省行高');
    if(input.columnWidth.trim())out.missingColumnWidth=decimal(input.columnWidth,'缺省列宽');
    if(input.ignoreBreaks)out.ignoreManualBreaks=true;
    if(JSON.stringify(out).length>limits.requestBytes)throw Error('页面设置超出本次容量');return out;
  }
  function envelope(value,binding,id){if(value?.id!==id||value.workbookKey!==binding.workbookKey||value.book!==binding.book||value.sheet!==binding.sheet)throw Error('预览来源或任务已变化，请重新准备');}
  function notices(value,limits){if(value==null)return [];if(!Array.isArray(value)||value.length>limits.notices||value.some(x=>typeof x!=='string'))fail();return value;}
  function rectangle(value,negative=false){if(!object(value))fail();return {x:units(value.x,negative),y:units(value.y,negative),width:units(value.width),height:units(value.height)};}
  function dimensions(value){if(!object(value))fail();return {width:units(value.width),height:units(value.height)};}
  function range(value,source){if(!object(value)||![value.row,value.col,value.endRow,value.endCol].every(integer)||value.row<1||value.col<1||value.endRow<value.row||value.endCol<value.col||value.endRow>source.maxRows||value.endCol>source.maxColumns)fail();}
  function summary(value,binding,id,limits,source){
    envelope(value,binding,id);const s=value.summary;if(!object(s)||!integer(s.pages)||s.pages<0||s.pages>limits.pages||s.sheet!==binding.sheet||typeof s.sourceId!=='string'||!/^[a-f0-9]{64}$/.test(s.sourceId)||typeof value.sourceComplete!=='boolean'||!object(value.sourceProperties))fail();
    notices(s.notices,limits);notices(value.sourceNotices,limits);dimensions(s.paper);dimensions(s.headings);
    if(!object(s.origins)||Object.values(s.origins).some(x=>typeof x!=='string'))fail();
    if((s.areas||[]).length>limits.areas||!Array.isArray(s.areas)&&s.areas!=null)fail();for(const a of s.areas||[])range(a,source);
    if(s.pages){if(!integer(s.firstPageNumber)||s.firstPageNumber<limits.firstPageMin||s.firstPageNumber>limits.firstPageMax||!object(s.scale)||! /^[1-9]\d{0,127}$/.test(s.scale.numerator)||! /^[1-9]\d{0,127}$/.test(s.scale.denominator))fail();const n=BigInt(s.scale.numerator),d=BigInt(s.scale.denominator);if(n>4n*d||!units(s.paper.width)||!units(s.paper.height)||!(s.areas?.length))fail();}
    if(!object(s.margins))fail();for(const k of ['left','right','top','bottom','header','footer'])units(s.margins[k]);
    return value;
  }
  function axis(items,parts,kind,source,limits){
    const maximum=kind==='row'?source.maxRows:source.maxColumns,cap=kind==='row'?limits.rows:limits.columns;
    if(!Array.isArray(items)||!items.length||items.length>cap||!Array.isArray(parts)||!parts.length||parts.length>limits.parts)fail();
    let total=0n;const offsets=[];
    for(const p of parts){if(!integer(p.first)||!integer(p.last)||p.first<1||p.last<p.first||p.last>maximum||typeof p.repeated!=='boolean')fail();offsets.push(total);units(p.offset);const length=units(p.length);if(!length)fail();total+=length;if(total>max64)fail();}
    let end=0n,lastPart=-1,lastNumber=0;
    for(const i of items){if(!integer(i.number)||i.number<1||i.number>maximum||!integer(i.part)||i.part<0||i.part>=parts.length||typeof i.repeated!=='boolean'||i.repeated!==parts[i.part].repeated||i.number<parts[i.part].first||i.number>parts[i.part].last||i.part<lastPart||i.part===lastPart&&i.number<=lastNumber)fail();
      const start=units(i.start,true),size=units(i.size),clip=units(i.clipStart),length=units(i.clipSize),p=parts[i.part];
      if(!size||!length||clip!==end||clip<start||clip+length>start+size||clip<offsets[i.part]||clip+length>offsets[i.part]+units(p.length))fail();
      end=clip+length;lastPart=i.part;lastNumber=i.number;
    }
    if(end!==total)fail();return total;
  }
  function page(value,binding,id,index,summaryValue,limits,source){
    envelope(value,binding,id);if(value.page!==index||!integer(index)||index<0||index>=summaryValue.summary.pages)fail();
    const content=value.content,p=content?.page,s=summaryValue.summary;
    if(!object(content)||!object(p)||p.number!==s.firstPageNumber+index||!integer(p.area)||p.area<1||p.area>s.areas.length||!Array.isArray(content.cells)||content.cells.length>limits.cells||!object(content.styles))fail();
    notices(content.notices,limits);dimensions(p.content);dimensions(p.body);units(p.offsetX);units(p.offsetY);
    const height=axis(content.rows,p.rows,'row',source,limits),width=axis(content.columns,p.columns,'col',source,limits);
    for(const i of content.cells){
      if(!integer(i.row)||!integer(i.col)||i.row<1||i.col<1||i.row>source.maxRows||i.col>source.maxColumns||i.ref!==columnName(i.col)+i.row||!integer(i.style)||i.style<0)fail();
      const b=rectangle(i.box,true),c=rectangle(i.clip);
      if(!b.width||!b.height||!c.width||!c.height||c.x<b.x||c.y<b.y||c.x+c.width>b.x+b.width||c.y+c.height>b.y+b.height||c.x+c.width>width||c.y+c.height>height)fail();
      if(i.merge){range(i.merge,source);if(i.merge.row!==i.row||i.merge.col!==i.col)fail();}
      if(i.hiddenAnchor!=null&&(typeof i.hiddenAnchor!=='boolean'||i.hiddenAnchor&&!i.merge))fail();
      if(i.cell!=null&&(!object(i.cell)||i.cell.ref!==i.ref||i.cell.col!==i.col||typeof i.cell.raw!=='string'||typeof i.cell.display!=='string'||typeof i.cell.hasFormula!=='boolean'||!integer(i.cell.style)))fail();
    }
    return value;
  }
  function pixels(value,limits,n=1n,d=1n){
    const amount=typeof value==='bigint'?value:units(value,true),q=amount*n*4n*1000000n/(d*3n*BigInt(limits.unitsPerPoint));
    if(q>BigInt(limits.domPixels)*1000000n||q< -BigInt(limits.domPixels)*1000000n)throw Error('本页物理尺寸超出浏览器预览容量，请调整纸张、缩放或范围');
    return Number(q)/1000000;
  }
  function project(summaryValue,pageValue,limits){
    const s=summaryValue.summary,c=pageValue.content,n=BigInt(s.scale.numerator),d=BigInt(s.scale.denominator),px=v=>pixels(v,limits,n,d),plain=v=>pixels(v,limits);
    return {width:plain(s.paper.width),height:plain(s.paper.height),x:plain(c.page.offsetX),y:plain(c.page.offsetY),contentWidth:plain(c.page.content.width),contentHeight:plain(c.page.content.height),scale:Number(n)/Number(d),
      cells:c.cells.map(i=>{const b=rectangle(i.box,true),clip=rectangle(i.clip);return {item:i,x:px(clip.x),y:px(clip.y),width:px(clip.width),height:px(clip.height),innerX:px(b.x-clip.x),innerY:px(b.y-clip.y),innerWidth:px(b.width),innerHeight:px(b.height)};}),
      rows:c.rows.map(i=>({item:i,start:px(i.clipStart),size:px(i.clipSize)})),columns:c.columns.map(i=>({item:i,start:px(i.clipStart),size:px(i.clipSize)}))};
  }
  const flag=(properties,group,name)=>properties?.[group]?.[name]?.status==='stored'&&['true','1'].includes(properties[group][name].value);
  window.ExcelLensPrintingModel={units,columnName,columnNumber,areas,titles,options,envelope,summary,page,pixels,project,flag};
})();
