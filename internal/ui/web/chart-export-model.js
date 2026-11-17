// Pure bounded encoders. All exact source/aggregate text remains a string.
(() => {
  'use strict';
  const encoder=new TextEncoder(),integer=(n,lo,hi)=>Number.isSafeInteger(n)&&n>=lo&&n<=hi;
  const capacity=()=>Error('图表导出超过容量，请缩小尺寸或使用结果数据导出');
  function stringify(value,limit){
    if(!integer(limit,1,1<<30))throw capacity();
    const chunks=[],active=new Set();let remaining=limit;
    const emit=text=>{if(text.length>remaining)throw capacity();const size=encoder.encode(text).length;if(size>remaining)throw capacity();remaining-=size;chunks.push(text);};
    function visit(item,depth=0){
      if(depth>64)throw Error('图表快照层级无效');
      if(item===null){emit('null');return;}
      switch(typeof item){
        case 'string':if(item.length>remaining)throw capacity();emit(JSON.stringify(item));return;
        case 'boolean':emit(String(item));return;
        case 'number':if(!Number.isFinite(item))throw Error('快照包含无效数值');emit(String(item));return;
        case 'object':break;
        default:throw Error('快照包含不支持的数据类型');
      }
      if(active.has(item))throw Error('图表快照存在循环引用');active.add(item);
      if(Array.isArray(item)){
        emit('[');for(let i=0;i<item.length;i++){if(i)emit(',');visit(item[i]===undefined?null:item[i],depth+1);}emit(']');
      }else{
        emit('{');let first=true;for(const key of Object.keys(item)){
          if(item[key]===undefined)continue;if(!first)emit(',');first=false;emit(JSON.stringify(key));emit(':');visit(item[key],depth+1);
        }emit('}');
      }
      active.delete(item);
    }
    visit(value);return chunks.join('');
  }
  function dimensions(spec,limits){
    if(!limits.types.includes(spec.type)||!integer(spec.width,limits.minWidth,limits.maxWidth)||!integer(spec.height,limits.minHeight,limits.maxHeight)||!limits.scales.includes(spec.scale)||spec.type==='svg'&&spec.scale!==1||!['captured','white'].includes(spec.background))throw Error('图表类型、尺寸或倍率无效');
    const pixels=spec.width*spec.height*spec.scale*spec.scale;if(!Number.isSafeInteger(pixels)||pixels>limits.pixels)throw capacity();
    return {width:spec.width*spec.scale,height:spec.height*spec.scale,pixels};
  }
  function plot(payload){
    if(payload.kind==='analysis')return window.ExcelLensAnalysisResults.projectMetric(payload.result,payload.chart.metricIndex);
    if(payload.kind==='selection-quick'){
      const key=payload.chart.seriesKey,series=key==='difference'?payload.result.difference:payload.result.columns.find(c=>String(c.column)===key);
      if(!series)throw Error('图表系列已失效');return window.ExcelLensSelectionQuickModel.chart(series);
    }
    throw Error('图表来源类型无效');
  }
  function capture(input,limits){
    const payload={schema:1,kind:input.kind,origin:input.origin,query:input.query,fields:input.fields,result:input.result,
      chart:{mode:input.mode,metricIndex:input.metricIndex??0,seriesKey:input.seriesKey||'',title:input.title,source:input.source,scope:input.scope,metric:input.metric,unit:input.unit,
        theme:input.theme,numberSettings:input.numberSettings,capturedAt:new Date().toISOString(),applicationVersion:input.applicationVersion}};
    // Bound before building the detached snapshot; a large current result must
    // never be silently reduced to the chart's few visible categories.
    const detached=JSON.parse(stringify(payload,limits.dataBytes)),result=plot(detached);
    const scope=window.ExcelLensAnalyticsCharts.describe(result,detached.chart.mode);
    if(scope.unrepresentable||!scope.drawableCells)throw Error('当前图表没有可导出的有效绘图数值，请核对指标或精确结果');
    detached.chart.drawing=scope;return detached;
  }
  const table=Uint32Array.from({length:256},(_,n)=>{for(let i=0;i<8;i++)n=n&1?0xedb88320^(n>>>1):n>>>1;return n>>>0;});
  async function crc32(bytes,checkpoint){
    let crc=0xffffffff;
    for(let start=0;start<bytes.length;start+=1<<20){
      await checkpoint();const end=Math.min(bytes.length,start+(1<<20));for(let i=start;i<end;i++)crc=table[(crc^bytes[i])&255]^(crc>>>8);
    }
    return (crc^0xffffffff)>>>0;
  }
  async function zip(files,limit,checkpoint=async()=>{}){
    if(!Array.isArray(files)||!files.length||files.length>16)throw Error('下载包文件数量无效');
    let total=22;const names=new Set(),items=[];
    for(const file of files){
      if(!/^[A-Za-z0-9][A-Za-z0-9._-]{0,79}$/.test(file.name)||names.has(file.name)||!(file.bytes instanceof Uint8Array))throw Error('下载包文件无效');
      names.add(file.name);const name=encoder.encode(file.name);total+=76+name.length*2+file.bytes.length;
      if(!Number.isSafeInteger(total)||total>limit||file.bytes.length>0xffffffff)throw capacity();items.push({...file,nameBytes:name});
    }
    const chunks=[],directory=[];let offset=0;
    for(const item of items){
      const data=item.bytes,name=item.nameBytes,crc=await crc32(data,checkpoint),head=new Uint8Array(30+name.length),view=new DataView(head.buffer);
      view.setUint32(0,0x04034b50,true);view.setUint16(4,20,true);view.setUint16(6,0x800,true);view.setUint16(12,0x21,true);
      view.setUint32(14,crc,true);view.setUint32(18,data.length,true);view.setUint32(22,data.length,true);view.setUint16(26,name.length,true);head.set(name,30);
      const central=new Uint8Array(46+name.length),c=new DataView(central.buffer);
      c.setUint32(0,0x02014b50,true);c.setUint16(4,20,true);c.setUint16(6,20,true);c.setUint16(8,0x800,true);c.setUint16(14,0x21,true);
      c.setUint32(16,crc,true);c.setUint32(20,data.length,true);c.setUint32(24,data.length,true);c.setUint16(28,name.length,true);c.setUint32(42,offset,true);central.set(name,46);
      chunks.push(head,data);directory.push(central);offset+=head.length+data.length;
    }
    await checkpoint();const directoryBytes=directory.reduce((n,x)=>n+x.length,0),end=new Uint8Array(22),e=new DataView(end.buffer);
    e.setUint32(0,0x06054b50,true);e.setUint16(8,items.length,true);e.setUint16(10,items.length,true);e.setUint32(12,directoryBytes,true);e.setUint32(16,offset,true);
    const blob=new Blob([...chunks,...directory,end],{type:'application/zip'});if(blob.size!==total)throw Error('下载包长度校验失败');return blob;
  }
  window.ExcelLensChartExportModel={stringify,dimensions,plot,capture,zip,capacity};
})();
