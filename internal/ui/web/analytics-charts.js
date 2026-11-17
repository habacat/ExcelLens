(() => {
  'use strict';
  const palette=['#287b78','#709cbe','#d19a59','#8b82b7','#66a58e','#c67e7f','#d2b66e','#777fa5','#7bb0bd','#ac9c87'];
  const rendered=new WeakMap();
  // Axes use drawing approximations; data labels and tooltips use exact cells.
  // Approximate ONLY for drawing. Fractions must not fall back to a rounded
  // display string, and missing/overflowed values must never become zero.
  function chartNumber(cell={}){
    cell=cell.comparison??cell;
    if(cell.status)return null;
    const raw=String(cell.exact??cell.display??'').trim();if(!raw)return null;
    if(raw.includes('/')){
      const parts=raw.split('/');if(parts.length!==2||!parts.every(x=>/^[+-]?\d+$/.test(x)))return NaN;
      const component=s=>{const sign=s[0]==='-'?-1:1,digits=s.replace(/^[+-]/,'').replace(/^0+/,'');return {mantissa:sign*Number(digits.slice(0,16)||'0')/10**(Math.min(digits.length,16)-1),exponent:Math.max(0,digits.length-1)};};
      const n=component(parts[0]),d=component(parts[1]);if(!d.mantissa)return NaN;if(!n.mantissa)return 0;
      const value=Number(String(n.mantissa/d.mantissa)+'e'+(n.exponent-d.exponent));return Number.isFinite(value)&&value!==0?value:NaN;
    }
    if(!/^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?$/.test(raw))return NaN;
    const value=Number(raw),nonzero=/[1-9]/.test(raw.split(/[eE]/)[0]);
    return Number.isFinite(value)&&(value!==0||!nonzero)?value:NaN;
  }
  const modes=Object.freeze(['bar','horizontal','line','area','donut','pie','scatter','stacked','heatmap']);
  function prepare(result,mode='bar'){
    if(!modes.includes(mode))throw Error('图表类型无效');
    const allRows=result?.rows||[],allSeries=result?.columnKeys||[];
    const single=['horizontal','pie','donut'].includes(mode),limit=mode==='horizontal'?24:mode==='heatmap'?28:60;
    const points=allRows.slice(0,limit),series=allSeries.slice(0,single?1:12);
    const percent=points.some(row=>row.values?.some(cell=>cell?.comparison?.percent)),compared=points.some(row=>row.values?.some(cell=>cell?.comparison));
    const values=points.map(row=>series.map((_,j)=>{const cell=row.values?.[j],value=chartNumber(cell);return value===null?null:cell?.comparison?.percent?value*100:value;}));
    const positive=points.map((_,i)=>({v:values[i]?.[0],i})).filter(x=>x.v>0),pie=positive.slice(0,16);
    return {allRows,allSeries,points,series,values,percent,compared,positive,pie,mode};
  }
  function describe(result,mode='bar'){
    const p=prepare(result,mode),pie=mode==='pie'||mode==='donut';
    const rowIndices=pie?p.pie.map(x=>x.i):p.points.map((_,i)=>i),seriesIndices=p.series.map((_,i)=>i);
    const drawable=rowIndices.reduce((n,i)=>n+p.values[i].filter(Number.isFinite).length,0);
    return {mode,totalRows:p.allRows.length,totalSeries:p.allSeries.length,consideredRows:p.points.length,rowIndices,seriesIndices,drawableCells:drawable,
      missingCells:rowIndices.reduce((n,i)=>n+p.values[i].filter(v=>v===null).length,0),unrepresentable:p.values.some(row=>row.some(Number.isNaN)),
      positiveOnly:pie,negativeExcluded:pie&&p.values.some(row=>row[0]<0),resultTruncated:!!result?.truncated,firstTreeLevel:!!result?.tree,percent:p.percent};
  }
  function themeFor(canvas){
    const style=getComputedStyle(canvas),get=(key,fallback)=>style.getPropertyValue(key).trim()||fallback;
    return {themed:document.documentElement.hasAttribute('data-reading-preset'),paper:get('--reading-surface','#fff'),ink:get('--reading-ink','#2c4852'),
      muted:get('--reading-muted','#768a97'),grid:get('--reading-grid-line','#e8eef1'),error:get('--reading-error','#b16f72')};
  }
  function paint(ctx,w,h,result,mode='bar',theme={},numberSettings=window.ExcelLensAnalysisNumbers.get()){
    const {themed=false,paper='#fff',ink='#2c4852',muted='#768a97',grid='#e8eef1'}=theme;
    const plan=prepare(result,mode);
    ctx.fillStyle=paper;ctx.fillRect(0,0,w,h);
    const fmt = v => {if(!Number.isFinite(v))return '—';const n=Math.abs(v);if(n!==0&&n<.01)return v.toExponential(2);return window.ExcelLensAnalysisNumbers.format({exact:String(v),display:v.toLocaleString('en-US',{useGrouping:false,maximumFractionDigits:2})},numberSettings);};
    const {allRows,allSeries,points,series,values,percent,compared}=plan;
    if(!points.length)return {status:'empty',hit:[],points,series};
    const unitFmt=v=>fmt(v)+(percent?'%':'');
    const plot={left:62,right:w-21,top:22,bottom:h-49},pw=Math.max(10,w-84),ph=Math.max(10,h-71);
    const hit=[];
    const text=(x,y,str,color='#768a97',align='center',font='11px "Segoe UI"',maxWidth)=>{ctx.font=font;ctx.textAlign=align;ctx.fillStyle=themed?(color==='#2c4852'?ink:color==='#b16f72'?(theme.error||color):muted):color;if(maxWidth>0)ctx.fillText(String(str),x,y,maxWidth);else ctx.fillText(String(str),x,y)};
    const line=(a,b,c,d,color='#e8eef1')=>{ctx.beginPath();ctx.moveTo(a,b);ctx.lineTo(c,d);ctx.strokeStyle=themed?grid:color;ctx.lineWidth=1;ctx.stroke()};
    const vRange=()=>{
      let low=0,high=0;
      for(const row of values){
        if(mode==='stacked'&&series.length>1){
          let positive=0,negative=0;
          for(const v of row){if(Number.isFinite(v)){if(v>=0)positive+=v;else negative+=v}}
          low=Math.min(low,negative);high=Math.max(high,positive)
        }else for(const v of row){if(Number.isFinite(v)){low=Math.min(low,v);high=Math.max(high,v)}}
      }
      if(high===low)high=low+1;
      const gap=(high-low)*.08;
      // Preserve a true zero baseline whenever all values share a sign.
      // Otherwise positive-only charts appear to cross a negative y-axis.
      return {low:low===0?0:low-gap,high:high===0?0:high+gap};
    };
    const label=row=>row.keys?.length?row.keys.map(key=>key===''?'空白值':key).join(' / '):'总计';
    const seriesLabel=key=>key===''?(result.columnField||allSeries.length>1?'空白列':'汇总'):key;
    const interactive=(x,y,rwidth,rheight,rowIndex,colIndex)=>hit.push({x,y,w:rwidth,h:rheight,row:rowIndex,col:colIndex});
    const color=j=>palette[j%palette.length];
    const range=vRange();
    if(values.some(row=>row.some(Number.isNaN))||!Number.isFinite(range.high-range.low)||range.high===range.low){
      text(w/2,h/2,'数值超出图表范围，请查看透视表','#b16f72');return {status:'range',hit:[],points,series};
    }
    let scope=points.length<allRows.length||series.length<allSeries.length?`显示 ${points.length}/${allRows.length} 个分类 · ${series.length}/${allSeries.length} 个系列`:'';
    if(result?.truncated)scope+=(scope?'；':'')+'分类已截取，完整合计见透视表';
    if(mode==='donut'||mode==='pie'){
      const positive=plan.positive,data=plan.pie;
      const total=data.reduce((s,x)=>s+x.v,0),cx=w*.42,cy=h*.49,r=Math.min(w*.26,h*.38),inner=mode==='donut'?r*.59:0;
      if(!Number.isFinite(total)){text(w/2,h/2,'数值超出图表范围，请查看透视表','#b16f72');return {status:'range',hit:[],points,series};}
      let angle=-Math.PI/2;
      for(let j=0;j<data.length;j++){
        const obj=data[j],end=angle+obj.v/total*2*Math.PI;
        ctx.beginPath();ctx.moveTo(cx+Math.cos(angle)*inner,cy+Math.sin(angle)*inner);ctx.arc(cx,cy,r,angle,end);
        if(inner>0)ctx.arc(cx,cy,inner,end,angle,true);else ctx.lineTo(cx,cy);
        ctx.closePath();ctx.fillStyle=color(j);ctx.fill();hit.push({pie:true,cx,cy,r,inner,from:angle,to:end,row:obj.i,col:0});angle=end;
      }
      if(inner>0){text(cx,cy-3,unitFmt(total),'#2c4852','center','bold 20px "Segoe UI"',inner*1.7);text(cx,cy+14,compared?'已绘制数值之和':'已绘制合计','#a1adb6')}
      data.slice(0,9).forEach((obj,j)=>{ctx.fillStyle=color(j);ctx.fillRect(w*.76,35+j*28,9,9);text(w*.76+15,43+j*28,label(points[obj.i]).slice(0,17),'#516675','left')});
      if(!data.length)text(w/2,h/2,'当前分类没有可绘制的正数','#a9b5be');
      if(positive.length>data.length||allRows.length>points.length)scope=`绘制 ${data.length} 个正值分类（共 ${allRows.length} 个分类）`+(allSeries.length>1?' · 第1系列':'')+(result?.truncated?'；分类已截取':'');
      if(points.some((_,i)=>values[i]?.[0]<0))text(w*.49,h-27,'含负数：扇形仅表示正值','#b16f72');
    }else if(mode==='heatmap' && series.length>1){
      const rows=Math.min(points.length,28),cols=series.length,cellW=pw/cols,cellH=ph/rows;
      const max=Math.max(0,...values.flat().filter(Number.isFinite).map(Math.abs))||1;
      points.slice(0,rows).forEach((row,i)=>{
        text(plot.left-9,plot.top+(i+.65)*cellH,label(row).slice(0,9),'#758591','right','10px "Segoe UI"');
        series.forEach((_,j)=>{const v=values[i][j];if(v===null)return;const alpha=Math.max(.07,Math.min(.94,Math.abs(v)/max));ctx.fillStyle=v<0?`rgba(194,95,94,${alpha})`:`rgba(35,128,124,${alpha})`;ctx.fillRect(plot.left+j*cellW+1,plot.top+i*cellH+1,Math.max(1,cellW-2),Math.max(1,cellH-2));interactive(plot.left+j*cellW,plot.top+i*cellH,cellW,cellH,i,j)})
      });series.forEach((s,j)=>text(plot.left+(j+.5)*cellW,h-20,seriesLabel(s).slice(0,9)));
    }else if(mode==='horizontal'){
      const count=points.length,rowH=ph/Math.max(1,count);
      points.slice(0,count).forEach((row,i)=>{
        const left=plot.left+45,usable=Math.max(35,plot.right-left-25);
        text(left-9,plot.top+(i+.65)*rowH,label(row).slice(0,13),'#758591','right','10px "Segoe UI"');
        const y=plot.top+(i+.24)*rowH;
        const val=values[i][0];if(val===null||val===undefined)return;
        const project=v=>left+(v-range.low)/(range.high-range.low)*usable;
        const x0=project(0),x1=project(val),start=Math.min(x0,x1),bar=Math.abs(x1-x0);
        ctx.fillStyle=val<0?'#c67e7f':color(i%4);
        ctx.fillRect(start,y,bar,Math.max(2,rowH*.55));interactive(start,y,Math.max(5,bar),Math.max(4,rowH*.55),i,0);
        if(w>500)text(x1+(val<0?-5:5),y+rowH*.44,window.ExcelLensAnalysisNumbers.format(row.values?.[0],numberSettings),'#667985',val<0?'right':'left','10px "Segoe UI"',Math.max(25,val<0?x1-10:w-x1-10));
      });
    }else{
      const project=v=>plot.bottom-(v-range.low)/(range.high-range.low)*ph;
      const zero=project(0);
      for(let i=0;i<=4;i++){const vv=range.low+(range.high-range.low)*i/4,y=project(vv);line(plot.left,y,plot.right,y);text(plot.left-8,y+3,unitFmt(vv),'#92a0ac','right','11px "Segoe UI"',plot.left-12)}
      const count=points.length,slot=pw/count,step=Math.max(1,Math.ceil(count/11));
      points.forEach((row,i)=>{if(i%step===0)text(plot.left+(i+.5)*slot,h-21,label(row).slice(0,11),'#82939f')});
      if(mode==='line'||mode==='area'||mode==='scatter'){
        series.forEach((_,j)=>{
          const segments=[];let segment=[];
          points.forEach((_,i)=>{if(values[i][j]===null){if(segment.length)segments.push(segment);segment=[];}else segment.push(i);});if(segment.length)segments.push(segment);
          for(const indices of segments){
            if(mode==='area'){ctx.beginPath();ctx.moveTo(plot.left+(indices[0]+.5)*slot,zero);indices.forEach(i=>ctx.lineTo(plot.left+(i+.5)*slot,project(values[i][j])));ctx.lineTo(plot.left+(indices.at(-1)+.5)*slot,zero);ctx.closePath();ctx.fillStyle='rgba(47,133,128,.16)';ctx.fill();}
            if(mode!=='scatter'){ctx.beginPath();indices.forEach((i,k)=>{const x=plot.left+(i+.5)*slot,y=project(values[i][j]);if(k===0)ctx.moveTo(x,y);else ctx.lineTo(x,y);});ctx.strokeStyle=color(j);ctx.lineWidth=2;ctx.stroke();}
            indices.forEach(i=>{const x=plot.left+(i+.5)*slot,y=project(values[i][j]);ctx.beginPath();ctx.arc(x,y,Math.max(2.5,Math.min(4,slot*.15)),0,Math.PI*2);ctx.fillStyle=color(j);ctx.fill();interactive(x-8,y-8,16,16,i,j);});
          }
        });
      }else{
        points.forEach((row,i)=>{
          if(mode==='stacked'&&series.length>1){let positive=0,negative=0;series.forEach((_,j)=>{const val=values[i][j];if(val===null)return;const baseValue=val>=0?positive:negative;const y=project(baseValue+val),base=project(baseValue);const x=plot.left+(i+.17)*slot;ctx.fillStyle=color(j);ctx.fillRect(x,Math.min(y,base),slot*.67,Math.max(1,Math.abs(base-y)));interactive(x,Math.min(y,base),slot*.67,Math.max(3,Math.abs(base-y)),i,j);if(val>=0)positive+=val;else negative+=val})}
          else{series.forEach((_,j)=>{const val=values[i][j];if(val===null)return;const x=plot.left+(i+.1+j*.78/series.length)*slot;const bw=slot*.78/series.length,y=project(val);ctx.fillStyle=color(j);ctx.fillRect(x,Math.min(y,zero),Math.max(2,bw),Math.max(1,Math.abs(zero-y)));interactive(x,Math.min(y,zero),Math.max(2,bw),Math.max(4,Math.abs(zero-y)),i,j)})}
        });
      }
      if(series.length>1)series.slice(0,7).forEach((s,j)=>{const start=w-145-(j%2)*120;ctx.fillStyle=color(j);ctx.fillRect(Math.max(10,start),8+Math.floor(j/2)*15,7,7);text(Math.max(20,start+12),15+Math.floor(j/2)*15,seriesLabel(s).slice(0,13),'#758997','left','10px "Segoe UI"')});
    }
    if(scope)text(w/2,h-5,scope,'#768a97','center','10px "Segoe UI"');
    return {status:'ok',hit,points,series,label,seriesLabel,plan};
  }
  function draw(canvas,result,mode='bar',onPick){
    if(!canvas)return()=>null;
    const theme=themeFor(canvas),numberSettings=window.ExcelLensAnalysisNumbers.get();
    rendered.set(canvas,{result,mode,onPick,theme,numberSettings});
    const dpr=Math.min(window.devicePixelRatio||1,2),bounds=canvas.getBoundingClientRect();
    const w=Math.max(140,Math.floor(bounds.width)),h=Math.max(140,Math.floor(bounds.height));
    canvas.width=w*dpr;canvas.height=h*dpr;
    const ctx=canvas.getContext('2d',{alpha:false});ctx.setTransform(dpr,0,0,dpr,0,0);
    canvas.onclick=null;canvas.onmousemove=null;canvas.onmouseleave=null;canvas.style.cursor='crosshair';
    const tip=canvas.parentElement.querySelector('.analysis-chart-tooltip');if(tip)tip.hidden=true;
    const drawing=paint(ctx,w,h,result,mode,theme,numberSettings);
    if(drawing.status!=='ok')return()=>null;
    const {hit,points,series,label,seriesLabel}=drawing;
    function pick(e){
      const rect=canvas.getBoundingClientRect(),x=(e.clientX-rect.left)*w/rect.width,y=(e.clientY-rect.top)*h/rect.height;
      for(let i=hit.length-1;i>=0;i--){const box=hit[i];if(box.pie){const dx=x-box.cx,dy=y-box.cy,rad=Math.hypot(dx,dy),angle=Math.atan2(dy,dx);const aa=angle<box.from?angle+Math.PI*2:angle;if(rad>=box.inner&&rad<=box.r&&aa>=box.from&&aa<=box.to)return box}else if(x>=box.x&&x<=box.x+box.w&&y>=box.y&&y<=box.y+box.h)return box}
      return null;
    }
    canvas.onclick=e=>{const p=pick(e);if(p&&onPick)onPick(points[p.row].keys,series[p.col])};
    canvas.onmousemove=e=>{
      const p=pick(e);canvas.style.cursor=p?'pointer':'crosshair';const tip=canvas.parentElement.querySelector('.analysis-chart-tooltip');if(!tip)return;
      if(!p){tip.hidden=true;return}
      const cell=points[p.row].values[p.col]||{};
      const display=window.ExcelLensAnalysisNumbers.format(cell);
      tip.hidden=false;tip.textContent=label(points[p.row])+' · '+seriesLabel(series[p.col])+'：'+display+(cell.comparison&&window.ExcelLensAnalysisComparisons?'（'+window.ExcelLensAnalysisComparisons.title(cell)+'）':cell.exact&&cell.exact!==display?'（精确值 '+cell.exact+'）':'');
      const rect=canvas.parentElement.getBoundingClientRect();tip.style.left=Math.max(6,Math.min(rect.width-tip.getBoundingClientRect().width-6,e.clientX-rect.left+12))+'px';tip.style.top=Math.max(6,e.clientY-rect.top-28)+'px';
    };
    canvas.onmouseleave=()=>{const tip=canvas.parentElement.querySelector('.analysis-chart-tooltip');if(tip)tip.hidden=true};
    return pick;
  }
  // Bounded snapshot trends use the same exact-to-plot conversion. Missing
  // values break the path; unrepresentable exact values never become zero.
  function sparkline(canvas,cells,domain=cells){
    const number=c=>c?chartNumber(c):null,values=cells.map(number),range=domain.map(number),theme=themeFor(canvas);
    const box=canvas.getBoundingClientRect(),w=Math.max(120,Math.floor(box.width)),h=Math.max(32,Math.floor(box.height)),dpr=Math.min(window.devicePixelRatio||1,2);
    canvas.width=w*dpr;canvas.height=h*dpr;const ctx=canvas.getContext('2d');ctx.setTransform(dpr,0,0,dpr,0,0);ctx.clearRect(0,0,w,h);
    const finite=range.filter(Number.isFinite);
    const invalid=values.some(Number.isNaN)||range.some(Number.isNaN);
    if(invalid||!finite.length){ctx.fillStyle=theme.muted;ctx.font='12px "Segoe UI"';ctx.textAlign='center';ctx.fillText(invalid?'数值超出绘图范围':'无可绘制数值',w/2,h/2+4);return;}
    const unit=Math.max(...finite.map(Math.abs))||1,low=Math.min(...finite.map(v=>v/unit)),high=Math.max(...finite.map(v=>v/unit));
    ctx.strokeStyle=palette[0];ctx.fillStyle=palette[0];ctx.lineWidth=1.8;ctx.beginPath();let connected=false;
    values.forEach((v,i)=>{if(v===null){connected=false;return;}const x=5+i*(w-10)/Math.max(1,values.length-1),y=high===low?h/2:h-5-(v/unit-low)/(high-low)*(h-10);if(connected)ctx.lineTo(x,y);else ctx.moveTo(x,y);connected=true;});ctx.stroke();
    // A run consisting of one point has no stroked length. Mark each valid
    // point so alternating missing series remain visible without joining gaps.
    values.forEach((v,i)=>{if(v===null)return;const x=5+i*(w-10)/Math.max(1,values.length-1),y=high===low?h/2:h-5-(v/unit-low)/(high-low)*(h-10);ctx.beginPath();ctx.arc(x,y,1.6,0,Math.PI*2);ctx.fill();});
  }
  window.ExcelLensAnalyticsCharts={draw,paint,prepare,describe,themeFor,modes,sparkline,snapshot:canvas=>rendered.get(canvas)||null};
  // Repaint existing charts from their displayed results; never re-run analysis
  // or keep removed dashboard canvases (and their results) alive.
  const repaint=()=>{
    for(const canvas of document.querySelectorAll('#analysisRoot canvas')){
      const saved=rendered.get(canvas);
      if(saved&&canvas.getClientRects().length)draw(canvas,saved.result,saved.mode,saved.onPick);
    }
  };
  let resizeFrame=0;
  const resize=()=>{if(resizeFrame)return;resizeFrame=requestAnimationFrame(()=>{resizeFrame=0;repaint();});};
  window.ExcelLensAnalyticsCharts.resize=resize;
  // Panels are permanent; observing them does not retain removed cards/results.
  // Changes in a containing panel also resize charts without a window event.
  if(window.ResizeObserver){const observer=new window.ResizeObserver(resize);for(const panel of document.querySelectorAll('#analysisRoot .analysis-panel'))observer.observe(panel);}
  document.addEventListener('excellens:readingchange',repaint);
  document.addEventListener('excellens:analysisnumberchange',repaint);
})();
