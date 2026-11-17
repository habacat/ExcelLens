/* A detached, bounded paper painter. Source CellData and style IDs remain
 * immutable; the shared presenter owns values, rich text and conditional ink. */
(() => {
  'use strict';
  const make=(tag,cls='',text='')=>{const n=document.createElement(tag);n.className=cls;n.textContent=text;return n;};
  const put=(node,values)=>{for(const [key,value] of Object.entries(values))node.style[key]=value+'px';};
  const compare=(a,b)=>a<b?-1:a>b?1:0;
  class PrintingRenderer {
    constructor({limits,richLimits,fontStack}){Object.assign(this,{limits,richLimits,fontStack});}
    build(summaryValue,pageValue){
      const m=window.ExcelLensPrintingModel,s=summaryValue.summary,c=pageValue.content,v=m.project(summaryValue,pageValue,this.limits),warnings=new Set(c.notices||[]);
      const paper=make('div','print-paper'),body=make('div','print-body'),data=make('div','print-data'),edges=make('div','print-edges');
      paper.setAttribute('aria-label','源表第'+(pageValue.page+1)+'页');put(paper,{width:v.width,height:v.height});put(body,{left:v.x,top:v.y,width:v.contentWidth,height:v.contentHeight});put(data,{width:v.contentWidth,height:v.contentHeight});body.append(data,edges);paper.append(body);
      const state={showStyles:true,showFormulas:false,fontOverride:'',styles:c.styles},fontStack=this.fontStack,richLimits=this.richLimits,scale=v.scale;
      class PrintPresenter extends window.ExcelLensCellView.Presenter {
        style(cell,rowStyle=0){const style=super.style(cell,rowStyle)||{};return {...style,fontSize:style.fontSize||11,fontName:style.fontName||'Arial'};}
      }
      let presenter;
      presenter=new PrintPresenter({state,scale:()=>scale,fontStack,richLimits,colName:m.columnName,numericPadding:()=>6*scale,applyStyle:(node,cell)=>{
        const style=presenter.style(cell),font={name:style.fontName,size:style.fontSize,bold:!!style.bold,italic:!!style.italic,strike:!!style.strike,underline:style.underlineStyle||(style.underline?'single':'none'),vertical:style.fontVertical||'baseline',color:window.ExcelLensConditional?.color(cell,style)||'#000000'};
        for(const [property,value] of Object.entries(window.ExcelLensRichText.properties(font,style,{dom:true,scale,fontStack,fontOverlay:cell?.conditional?.style?.font})))node.style.setProperty(property,value);
        node.style.backgroundColor=style.background||'transparent';node.style.padding=`${scale}px ${3*scale}px`;
        node.style.justifyContent=({left:'flex-start',right:'flex-end',center:'center',centerContinuous:'center',justify:'space-between',distributed:'space-around'})[style.horizontal]||((cell?.type==='n'||!cell?.type&&!window.ExcelLensCellView.isText(cell))?'flex-end':'flex-start');
        node.style.alignItems=({top:'flex-start',center:'center',bottom:'flex-end'})[style.vertical]||'flex-end';
        node.style.whiteSpace=style.wrap?'pre-wrap':'pre';node.style.wordBreak=style.wrap?'break-word':'normal';
        if(style.rotation)warnings.add('旋转文字按普通文字预览，原始设置保留');
      }});
      const models=v.cells.map(({item})=>item.cell?{...item.cell,style:item.style}:null);
      presenter.prepare([{cells:models.filter(Boolean)}]);
      const edgeGroups=new Map(),n=BigInt(s.scale.numerator),d=BigInt(s.scale.denominator),px=x=>m.pixels(x,this.limits,n,d);
      const grid=m.flag(summaryValue.sourceProperties,'printOptions','gridLines'),gridBorder=grid?'0.5px solid #b8b8b8':'';
      const add=(vertical,position,first,last,side,css)=>{
        if(!css||last<=first)return;const key=(vertical?'v':'h')+position;
        if(!edgeGroups.has(key))edgeGroups.set(key,{vertical,position,events:[]});const segment={side,css};
        edgeGroups.get(key).events.push({at:first,segment,start:true},{at:last,segment,start:false});
      };
      const rowValues=new Map(),layouts=new Map(),columnAt=new Map(),columnPositions=new Map(),mergedRows=new Map(),uniqueMerges=new Map();
      for(const column of v.columns){const group=layouts.get(column.item.part)||[];columnAt.set(column.item.number+':'+column.start,column);columnPositions.set(column.item.part+':'+column.item.number,group.length);group.push({col:column.item.number,width:column.size});layouts.set(column.item.part,group);}
      for(let at=0;at<v.cells.length;at++){
        const item=v.cells[at].item,cell=models[at];if(!rowValues.has(item.row))rowValues.set(item.row,{});if(cell)rowValues.get(item.row)[item.col]=cell;
        if(item.merge)uniqueMerges.set(JSON.stringify(item.merge),item.merge);
      }
      const sourceRows=[...new Set(c.rows.map(row=>row.number))].sort((a,b)=>a-b);
      const lower=(values,value,key)=>{let low=0,high=values.length;while(low<high){const middle=(low+high)>>>1;if((key?values[middle][key]:values[middle])<value)low=middle+1;else high=middle;}return low;};
      for(const merge of uniqueMerges.values())for(let at=lower(sourceRows,merge.row);at<sourceRows.length&&sourceRows[at]<=merge.endRow;at++){const row=sourceRows[at],ranges=mergedRows.get(row)||[];ranges.push({first:merge.col,last:merge.endCol});mergedRows.set(row,ranges);}
      for(const ranges of mergedRows.values())ranges.sort((a,b)=>a.first-b.first);
      presenter.o.merge=(row,col)=>{const ranges=mergedRows.get(row)||[],at=lower(ranges,col+1,'first')-1;return at>=0&&ranges[at].last>=col;};
      for(let at=0;at<v.cells.length;at++){
        const box=v.cells[at],item=box.item,cell=models[at],clip=make('div','print-cell-clip'),node=make('div','print-cell');
        clip.dataset.ref=item.ref;node.dataset.row=String(item.row);node.dataset.col=String(item.col);node.dataset.ref=item.ref;node._xlSourceCell=item.cell||null;
        put(clip,{left:box.x,top:box.y,width:box.width,height:box.height});put(node,{left:box.innerX,top:box.innerY,width:box.innerWidth,height:box.innerHeight});
        presenter.paint(node,cell,{style:item.style});if(!cell)presenter.o.applyStyle(node,{style:item.style});
        if(item.hiddenAnchor){node.replaceChildren();warnings.add('隐藏的合并起点保留原坐标，未把其值移到可见覆盖格');}
        const messages=[node._xlRichNotice,node._xlPresentationNotice].filter(Boolean);for(const message of messages)warnings.add(message);
        if(item.cell?.cacheMissing)warnings.add('本页有公式缺少保存结果，预览保留空结果并可悬停核对');
        node.title=item.ref+(item.hiddenAnchor?'（源合并起点隐藏）':'')+(item.cell?'\n'+String(item.cell.raw).slice(0,2048):'（空白源坐标）')+(item.cell?.cacheMissing?'\n源公式未保存计算结果':'')+(messages.length?'\n'+messages.join('；'):'');
        const numericOverflow=!item.hiddenAnchor&&presenter.fitNumber(node,cell,Math.min(box.width,box.innerWidth),6*scale);
        if(numericOverflow)warnings.add('数值宽度不足时显示#提示，悬停可核对完整显示值，源值不变');
        if(!numericOverflow&&!item.merge&&!item.hiddenAnchor&&box.innerX===0&&box.innerY===0&&box.width===box.innerWidth&&box.height===box.innerHeight){
          const column=columnAt.get(item.col+':'+box.x),group=layouts.get(column?.item.part)||[],position=columnPositions.get(column?.item.part+':'+item.col);
          if(position!=null)presenter.overflow(node,cell,rowValues.get(item.row),group,position);
          clip.classList.toggle('print-can-spill',!!node._xlOverflowSpan);
        }
        clip.append(node);data.append(clip);
        const style={...presenter.style(cell,item.style),...(item.borders||{})},b=item.box,k=item.clip,x=m.units(b.x,true),y=m.units(b.y,true),w=m.units(b.width),h=m.units(b.height),cx=m.units(k.x),cy=m.units(k.y),cw=m.units(k.width),ch=m.units(k.height);
        if(x>=cx&&x<=cx+cw)add(true,x,cy,cy+ch,'left',style.borderLeft||gridBorder);
        if(x+w>=cx&&x+w<=cx+cw)add(true,x+w,cy,cy+ch,'right',style.borderRight||gridBorder);
        if(y>=cy&&y<=cy+ch)add(false,y,cx,cx+cw,'top',style.borderTop||gridBorder);
        if(y+h>=cy&&y+h<=cy+ch)add(false,y+h,cx,cx+cw,'bottom',style.borderBottom||gridBorder);
      }
      // Collapse physical seam segments, including a large merged edge beside
      // several small cells, using the same source-border precedence as grid.
      for(const group of edgeGroups.values()){
        group.events.sort((a,b)=>compare(a.at,b.at));let last=group.events[0]?.at;const active=new Set();
        for(let at=0;at<group.events.length;){
          const position=group.events[at].at;
          if(position>last&&active.size){
            let own='',other='';for(const segment of active){if(segment.side===(group.vertical?'right':'bottom'))own=segment.css;else other=segment.css;}
            const css=group.vertical?window.ExcelLensGridBorders.resolve({borderRight:own},{borderLeft:other},{}).right:window.ExcelLensGridBorders.resolve({borderBottom:own},{},{borderTop:other}).bottom;
            const parsed=/^([\d.]+)px\s+(solid|double|dashed|dotted)\s+(#[\da-f]{6})$/i.exec(css||'');
            if(parsed){const stroke=Number(parsed[1])*scale,line=make('div','print-edge');if(group.vertical){put(line,{left:px(group.position)-stroke/2,top:px(last),height:px(position-last)});line.style.borderLeft=`${stroke}px ${parsed[2]} ${parsed[3]}`;}else{put(line,{left:px(last),top:px(group.position)-stroke/2,width:px(position-last)});line.style.borderTop=`${stroke}px ${parsed[2]} ${parsed[3]}`;}edges.append(line);}
          }
          while(at<group.events.length&&group.events[at].at===position){const event=group.events[at++];event.start?active.add(event.segment):active.delete(event.segment);}last=position;
        }
      }
      const headingWidth=m.pixels(s.headings.width,this.limits),headingHeight=m.pixels(s.headings.height,this.limits);
      if(headingWidth)for(const row of v.rows){const label=make('div','print-source-heading',String(row.item.number));put(label,{left:v.x-headingWidth,top:v.y+row.start,width:headingWidth,height:row.size});label.style.fontSize=Math.min(11,Math.max(.1,row.size*.75))+'px';paper.append(label);}
      if(headingHeight)for(const column of v.columns){const label=make('div','print-source-heading',m.columnName(column.item.number));put(label,{left:v.x+column.start,top:v.y-headingHeight,width:column.size,height:headingHeight});paper.append(label);}
      return {node:paper,width:v.width,height:v.height,warnings:[...warnings]};
    }
  }
  window.ExcelLensPrintingRenderer=PrintingRenderer;
})();
