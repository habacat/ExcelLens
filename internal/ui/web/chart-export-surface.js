// Canvas subset used by the shared chart core. SVG contains vectors/text only.
(() => {
  'use strict';
  const encoder=new TextEncoder(),capacity=()=>window.ExcelLensChartExportModel.capacity();
	// toBlob itself cannot be aborted. One owner retains its canvas until the
	// real callback; one cancellable waiter permits an immediate UI retry
	// without allowing cancelled encoders to accumulate large bitmaps.
	let rasterOwner=null,rasterWaiter=null;
	function rasterLease(signal){
	  const aborted=()=>Object.assign(Error('图片生成已取消'),{name:'AbortError'});
	  if(signal?.aborted)return Promise.reject(aborted());
	  return new Promise((resolve,reject)=>{
	    const ticket={resolve,reject};
	    ticket.cleanup=()=>signal?.removeEventListener('abort',ticket.abort);
	    ticket.abort=()=>{if(rasterWaiter===ticket){rasterWaiter=null;ticket.cleanup();reject(aborted());}};
	    ticket.release=()=>{
	      if(rasterOwner!==ticket)return;rasterOwner=null;ticket.cleanup();
	      if(rasterWaiter){const next=rasterWaiter;rasterWaiter=null;rasterOwner=next;next.resolve(next.release);}
	    };
	    if(!rasterOwner){rasterOwner=ticket;resolve(ticket.release);}
	    else if(!rasterWaiter){rasterWaiter=ticket;signal?.addEventListener('abort',ticket.abort,{once:true});}
	    else reject(Error('已有图片正在等待编码，请取消后重试'));
	  });
	}
  const finite=n=>{if(!Number.isFinite(n)||Math.abs(n)>1e7)throw Error('图表绘制坐标无效');return n;};
  const number=n=>{finite(n);return String(Number(n.toFixed(6)));};
  function xml(value){
    return String(value).replace(/[\u0000-\u0008\u000B\u000C\u000E-\u001F\uFFFE\uFFFF]|[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/g,'\uFFFD').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&apos;'}[c]));
  }
  function color(value){
    if(typeof value!=='string'||!(/^(?:#(?:[\da-f]{3}|[\da-f]{4}|[\da-f]{6}|[\da-f]{8})|rgba?\([\d.,\s%]+\))$/i.test(value)))throw Error('图表颜色无法导出');
    if(value.startsWith('rgb')){
      const parts=value.slice(value.indexOf('(')+1,-1).split(',').map(s=>s.trim());
      if(parts.length!==(value.startsWith('rgba')?4:3)||parts.some((s,i)=>!/^\d*\.?\d+%?$/.test(s)||Number.parseFloat(s)<0||Number.parseFloat(s)>(s.endsWith('%')?100:i===3?1:255)))throw Error('图表颜色无效');
    }
    return value;
  }
  class SVGContext {
    constructor(measure,limits){this.measure=measure;this.limits=limits;this.nodes=[];this.bytes=0;this.stack=[];this.tx=this.ty=0;this.font='11px "Segoe UI"';this.textAlign='start';this.fillStyle='#000';this.strokeStyle='#000';this.lineWidth=1;this.beginPath();}
    emit(node){if(this.nodes.length>=this.limits.svgNodes)throw capacity();this.bytes+=encoder.encode(node).length;if(this.bytes>this.limits.svgBytes)throw capacity();this.nodes.push(node);}
    point(x,y){return [finite(x+this.tx),finite(y+this.ty)];}
    command(text){if(this.path.length+text.length>this.limits.svgBytes)throw capacity();this.path+=text;}
    beginPath(){this.path='';this.current=null;this.start=null;}
    moveTo(x,y){const p=this.point(x,y);this.command('M'+p.map(number).join(' '));this.current=p;this.start=p;}
    lineTo(x,y){if(!this.current){this.moveTo(x,y);return;}const p=this.point(x,y);this.command('L'+p.map(number).join(' '));this.current=p;}
    closePath(){if(this.current){this.command('Z');this.current=this.start;}}
    arc(x,y,r,start,end,ccw=false){
      [x,y,r,start,end].forEach(finite);if(r<0)throw Error('图表圆弧半径无效');
      const tau=Math.PI*2,raw=ccw?start-end:end-start,span=raw>=tau?tau:((raw%tau)+tau)%tau;
      const first=[x+Math.cos(start)*r,y+Math.sin(start)*r],position=this.point(...first);
      if(!this.current)this.moveTo(...first);else if(Math.hypot(this.current[0]-position[0],this.current[1]-position[1])>1e-8)this.lineTo(...first);
      if(!r||!span)return;
      const count=Math.ceil(span/Math.PI),step=span/count*(ccw?-1:1);
      for(let i=1;i<=count;i++){
        const angle=start+step*i,p=this.point(x+Math.cos(angle)*r,y+Math.sin(angle)*r);
        this.command('A'+number(r)+' '+number(r)+' 0 0 '+(ccw?'0':'1')+' '+p.map(number).join(' '));this.current=p;
      }
    }
    fill(){if(this.path)this.emit('<path d="'+this.path+'" fill="'+color(this.fillStyle)+'"/>');}
    stroke(){if(this.path){if(!Number.isFinite(this.lineWidth)||this.lineWidth<=0)throw Error('图表线宽无效');this.emit('<path d="'+this.path+'" fill="none" stroke="'+color(this.strokeStyle)+'" stroke-width="'+number(this.lineWidth)+'"/>');}}
    fillRect(x,y,width,height){const p=this.point(x,y);finite(width);finite(height);const left=width<0?p[0]+width:p[0],top=height<0?p[1]+height:p[1];this.emit('<rect x="'+number(left)+'" y="'+number(top)+'" width="'+number(Math.abs(width))+'" height="'+number(Math.abs(height))+'" fill="'+color(this.fillStyle)+'"/>');}
    measureText(text){this.measure.font=this.font;return this.measure.measureText(text);}
    fillText(value,x,y,maxWidth){
      const p=this.point(x,y),font=/^(?:(bold|normal)\s+)?(\d+(?:\.\d+)?)px\s+"?([\w\s]+)"?$/.exec(this.font);
      if(!font||!['Segoe UI','Microsoft YaHei'].includes(font[3].trim()))throw Error('图表字体无法导出');
      const measured=this.measureText(String(value)).width;
      const squeeze=maxWidth>0&&measured>maxWidth?' textLength="'+number(maxWidth)+'" lengthAdjust="spacingAndGlyphs"':'';
      const anchor=this.textAlign==='center'?'middle':['right','end'].includes(this.textAlign)?'end':'start';
      this.emit('<text x="'+number(p[0])+'" y="'+number(p[1])+'" fill="'+color(this.fillStyle)+'" font-family="'+xml(font[3].trim())+'" font-size="'+font[2]+'" font-weight="'+(font[1]==='bold'?'700':'400')+'" text-anchor="'+anchor+'" xml:space="preserve"'+squeeze+'>'+xml(value)+'</text>');
    }
    save(){if(this.stack.length>=32)throw capacity();this.stack.push({tx:this.tx,ty:this.ty,font:this.font,textAlign:this.textAlign,fillStyle:this.fillStyle,strokeStyle:this.strokeStyle,lineWidth:this.lineWidth});}
    restore(){const state=this.stack.pop();if(!state)throw Error('SVG绘图状态无效');Object.assign(this,state);}
    translate(x,y){this.tx=finite(this.tx+x);this.ty=finite(this.ty+y);}
    serialize(width,height,title){
      if(this.stack.length)throw Error('SVG绘图状态未结束');
      const text='<?xml version="1.0" encoding="UTF-8"?><svg xmlns="http://www.w3.org/2000/svg" width="'+number(width)+'" height="'+number(height)+'" viewBox="0 0 '+number(width)+' '+number(height)+'" role="img" aria-labelledby="chart-title"><title id="chart-title">'+xml(title)+'</title><desc>已接受结果快照；精确值与完整说明见同包 exact-data.json 及 README.txt。</desc><defs><clipPath id="chart-bounds"><rect width="'+number(width)+'" height="'+number(height)+'"/></clipPath></defs><g clip-path="url(#chart-bounds)">'+this.nodes.join('')+'</g></svg>';
      if(encoder.encode(text).length>this.limits.svgBytes)throw capacity();return text;
    }
  }
  function fitLine(ctx,value,width){
    const chars=[];let shortened=false;
    for(const c of String(value??'')){if(chars.length===512){shortened=true;break;}chars.push(c);}
    let text=chars.join('');if(ctx.measureText(text).width<=width&&!shortened)return text;
    let lo=0,hi=chars.length;
    while(lo<hi){const mid=Math.ceil((lo+hi)/2);if(ctx.measureText(chars.slice(0,mid).join('')+'…').width<=width)lo=mid;else hi=mid-1;}
    return chars.slice(0,lo).join('')+'…';
  }
  function descriptions(payload){
    const c=payload.chart,d=c.drawing,flags=[];
    if(d.resultTruncated)flags.push('结果已按Top-N截取，未显示分类不在附件行中');
    if(d.firstTreeLevel)flags.push('层级透视首层，未导出展开后代');
    if(d.positiveOnly)flags.push('仅从前'+d.consideredRows+'分类绘制前'+d.rowIndices.length+'个正值及第1系列，负数/零不画');
    if(d.missingCells)flags.push(d.missingCells+'个缺值不补0');
    const display=c.numberSettings;
    return [
      '绘制 '+d.rowIndices.length+'/'+d.totalRows+' 个分类 · '+d.seriesIndices.length+'/'+d.totalSeries+' 个系列；范围：'+c.scope,
      flags.join('；')||'本图保留所绘分类顺序；附件保留全部已接受结果。',
      '绘图坐标使用近似值；原始/派生精确值、分数及状态以 exact-data.json 为准。',
      '快照 '+c.capturedAt+' · 千分位'+(display.groupThousands?'开启':'关闭')+' · 科学计数法'+(display.scientific?'10^'+display.scientificExponent+'起':'关闭'),
      '长标签/说明在图片中可能截短，完整原文见附件。',
      '数据附件为已接受结果快照，不代表未截取的整个源表；源文件未修改。'
    ];
  }
  function paintDocument(ctx,payload,spec){
    const c=payload.chart,theme=spec.background==='white'?{themed:false,paper:'#fff',ink:'#2c4852',muted:'#768a97',grid:'#e8eef1',error:'#b16f72'}:c.theme;
    ctx.fillStyle=color(theme.paper);ctx.fillRect(0,0,spec.width,spec.height);ctx.textAlign='left';
    const text=(value,y,size=12,bold=false)=>{ctx.font=(bold?'bold ':'')+size+'px "Segoe UI"';ctx.fillStyle=color(bold?theme.ink:theme.muted);ctx.fillText(fitLine(ctx,value,spec.width-48),24,y);};
    text(c.title||'ExcelLens 图表',32,18,true);text('来源：'+c.source,56);text('指标：'+c.metric+' · 单位：'+c.unit,78);
    ctx.save();ctx.translate(0,100);
    const drawn=window.ExcelLensAnalyticsCharts.paint(ctx,spec.width,spec.height-206,window.ExcelLensChartExportModel.plot(payload),c.mode,theme,c.numberSettings);
    ctx.restore();if(drawn.status!=='ok')throw Error('数值超出可绘制范围，未生成图片');
    const description=descriptions(payload),d=c.drawing,notes=[description[0]];
    if(d.resultTruncated||d.firstTreeLevel||d.positiveOnly||d.missingCells)notes.push(description[1]);
    notes.push('精确数据见 exact-data.json');
    notes.forEach((line,i)=>text(line,spec.height-74+i*18,11));
  }
  async function image(payload,spec,limits,checkpoint,signal){
    const dimensions=window.ExcelLensChartExportModel.dimensions(spec,limits);await checkpoint();
	const release=spec.type==='png'?await rasterLease(signal):()=>{};
	let canvas;
    try{
	  await checkpoint();canvas=document.createElement('canvas');
      if(spec.type==='svg'){
        const measure=canvas.getContext('2d');if(!measure)throw Error('当前环境无法测量图表字体');
        const ctx=new SVGContext(measure,limits);paintDocument(ctx,payload,spec);
        return new Blob([ctx.serialize(spec.width,spec.height,fitLine(measure,payload.chart.title,spec.width))],{type:'image/svg+xml;charset=utf-8'});
      }
      canvas.width=dimensions.width;canvas.height=dimensions.height;const ctx=canvas.getContext('2d',{alpha:false});
      if(!ctx)throw Error('当前环境无法创建图表图片');ctx.setTransform(spec.scale,0,0,spec.scale,0,0);paintDocument(ctx,payload,spec);
      const blob=await new Promise((resolve,reject)=>{try{canvas.toBlob(value=>value?resolve(value):reject(Error('PNG编码失败，请重试')),'image/png');}catch(error){reject(error);}});
      await checkpoint();if(blob.type!=='image/png'||blob.size>limits.imageBytes)throw capacity();return blob;
    }finally{if(canvas)canvas.width=canvas.height=0;release();}
  }
  window.ExcelLensChartExportSurface={SVGContext,xml,fitLine,descriptions,paintDocument,image};
})();
