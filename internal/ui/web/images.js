/* ExcelLens picture layer: lazy viewport images; source coordinates remain
 * independent from virtualized cells. Original workbooks are never mutated.
 */
(() => {
  'use strict';
  const $=id=>document.getElementById(id);
  const clamp=(x,min,max)=>Math.max(min,Math.min(max,x));
  const fmt=x=>`${Math.round(x*100)/100}px`;
  const PANE_NAMES=['normal','top','left','corner'];
  const validRect=r=>r&&[r.x,r.y,r.w,r.h].every(Number.isFinite)&&r.w>0&&r.h>0;

  class Pictures {
    constructor(o){
      this.o=o;this.items=[];this.nodes=new Map();this.enabled=true;this.frame=0;this.serial=0;
      this.active=-1;this.rotation=0;this.zoom=1;this.panX=0;this.panY=0;this.natural={w:1,h:1};
      this.initialRects=new Map();this.focusBeforeViewer=null;this.flipH=false;this.flipV=false;this.fitToWindow=true;
      this.viewer=$('pictureViewer');this.stage=$('pictureStage');this.preview=$('picturePreview');
      this.root=$('pictureOverlay');
      this.panes=Object.fromEntries(PANE_NAMES.map(name=>[name,$(`picturePane-${name}`)]));
      this.bind();
      o.grid.addEventListener('scroll',()=>this.schedule(),{passive:true});
      new ResizeObserver(()=>this.schedule()).observe(o.grid);
    }
    reset(){
      ++this.serial;cancelAnimationFrame(this.frame);this.frame=0;this.items=[];
      for(const node of this.nodes.values())node.remove();this.nodes.clear();
      this.root.hidden=true;this.close();this.active=-1;this.initialRects.clear();
      $('imagesBtn').hidden=true;
    }
    async load(sheet,epoch){
      this.reset();const serial=this.serial;
      const data=await this.o.api(`/api/images?sheet=${encodeURIComponent(sheet)}`);
      if(serial!==this.serial||this.o.state.sheetEpoch!==epoch||this.o.state.sheet!==sheet)return;
      this.items=Array.isArray(data.images)?data.images:[];
      this.root.hidden=this.items.length===0;
      const btn=$('imagesBtn');btn.hidden=this.items.length===0;
      btn.innerHTML=`<svg class="icon" aria-hidden="true" focusable="false"><use href="#i-image"/></svg>图片<span class="image-count"></span>`;
      btn.querySelector('.image-count').textContent=String(this.items.length);
      btn.setAttribute('aria-pressed',String(this.enabled));
      btn.title=`工作表共有 ${this.items.length} 张图片；单击此按钮显示或隐藏图片`;
      this.schedule();
    }
    schedule(){if(this.frame)return;this.frame=requestAnimationFrame(()=>{this.frame=0;this.render();});}
    // XLSX anchors are zero-based. Sheet geometry APIs are one-based; offsets
    // remain in the unzoomed 96dpi coordinate system until this final step.
    anchorPoint(m){
      const s=this.o.scale();
      return {x:this.o.colLeft(m.col+1)+(Number(m.colOff)||0)*s,
              y:this.o.rowTop(m.row+1)+(Number(m.rowOff)||0)*s};
    }
    rect(im){
      const s=this.o.scale();let x,y,w,h;
      if(im.kind==='absolute'){
        x=(Number(im.x)||0)*s;y=(Number(im.y)||0)*s;
        w=(Number(im.width)||0)*s;h=(Number(im.height)||0)*s;
      }else{
        const start=this.anchorPoint(im.from);x=start.x;y=start.y;
        if(im.kind==='twoCell'&&im.to){
          const end=this.anchorPoint(im.to);
          const frozen=this.initialRects.get(im.id);
          if(im.editAs==='absolute'){
            // Excel's "don't move or size" anchor stores original xfrm.
            // Some producers omit xfrm; freeze the first computed geometry.
            const baseline=frozen||{x:(im.hasTransform?Number(im.x)||0:x/s),y:(im.hasTransform?Number(im.y)||0:y/s),
                                   w:(Number(im.width)||(end.x-x)/s),h:(Number(im.height)||(end.y-y)/s)};
            if(!frozen)this.initialRects.set(im.id,baseline);
            x=baseline.x*s;y=baseline.y*s;w=baseline.w*s;h=baseline.h*s;
          }else if(im.editAs==='oneCell'){
            // Follow the cell, but preserve the picture's physical dimensions.
            const baseline=frozen||{w:(Number(im.width)||(end.x-x)/s),
                                    h:(Number(im.height)||(end.y-y)/s)};
            if(!frozen)this.initialRects.set(im.id,baseline);
            w=baseline.w*s;h=baseline.h*s;
          }else{w=end.x-x;h=end.y-y;}
        }else{w=(Number(im.width)||0)*s;h=(Number(im.height)||0)*s;}
      }
      return {x:x+this.o.rowHead,y:y+this.o.headerHeight(),w,h};
    }
    paneGeometry(){
      const grid=this.o.grid,pin=this.o.frozen();
      const hh=this.o.headerHeight(),lh=this.o.rowHead;
      const left=lh+this.o.colLeft(pin.cols+1),top=hh+this.o.rowTop(pin.rows+1);
      // Keep pictures strictly inside the visible grid; do not cover native
      // scrollbar gutters, row/column headers, or the formula toolbar.
      const width=grid.clientWidth,height=grid.clientHeight;
      return {
        normal:{x:pin.cols?left:lh,y:pin.rows?top:hh,w:width-(pin.cols?left:lh),h:height-(pin.rows?top:hh),dx:-grid.scrollLeft,dy:-grid.scrollTop},
        top:{x:pin.cols?left:lh,y:hh,w:width-(pin.cols?left:lh),h:top-hh,dx:-grid.scrollLeft,dy:0},
        left:{x:lh,y:pin.rows?top:hh,w:left-lh,h:height-(pin.rows?top:hh),dx:0,dy:-grid.scrollTop},
        corner:{x:lh,y:hh,w:left-lh,h:top-hh,dx:0,dy:0}
      };
    }
    render(){
      if(this.root.hidden||!this.enabled||!this.o.state.indexReady)return;
      // Filters reorder source rows. Showing the source-positioned picture on a
      // different filtered row would be factually incorrect; the viewer button
      // remains available even when inline placement is temporarily hidden.
      if(this.o.state.filtered){for(const n of this.nodes.values())n.remove();this.nodes.clear();return;}
      const grid=this.o.grid, panes=this.paneGeometry(),s=this.o.state;
      const pin=this.o.frozen(),required=new Set();
      for(const name of PANE_NAMES){
        const el=this.panes[name],p=panes[name];
        el.style.left=fmt(p.x);el.style.top=fmt(p.y);
        el.style.width=fmt(Math.max(0,p.w));el.style.height=fmt(Math.max(0,p.h));
      }
      const usePane=im=>{
        const frozenRow=pin.rows>0&&im.kind!=='absolute'&&im.from.row<pin.rows;
        const frozenCol=pin.cols>0&&im.kind!=='absolute'&&im.from.col<pin.cols;
        if(frozenRow&&frozenCol)return 'corner';
        if(frozenRow)return 'top';
        if(frozenCol)return 'left';
        return 'normal';
      };
      for(const im of this.items){
        const rect=this.rect(im);if(!validRect(rect))continue;
        const name=usePane(im),p=panes[name];
        if(p.w<=0||p.h<=0)continue;
        const x=rect.x+p.dx,y=rect.y+p.dy;
        // 60 CSS px prerender overscan. Never instantiate every picture in a
        // workbook on each scroll; the viewport owns the image DOM budget.
        if(x+rect.w<p.x-60||x>p.x+p.w+60||y+rect.h<p.y-60||y>p.y+p.h+60)continue;
        const key=String(im.id);required.add(key);
        let node=this.nodes.get(key);
        if(!node){
          node=document.createElement('button');node.type='button';node.className='worksheet-picture';
          node.setAttribute('aria-label',`查看图片：${im.description||im.name||'图片 '+(im.id+1)}`);
          const img=document.createElement('img');img.src=im.src;img.alt=im.description||im.name||'';
          img.loading='lazy';img.decoding='async';img.draggable=false;
          // Crop/rotation belong to immutable OOXML metadata. They do not
          // change as the user scrolls or zooms the sheet: configure once
          // instead of rewriting four style properties on every scroll.
          const crop=im.crop||{};
          const l=clamp(Number(crop.left)||0,0,.95),r=clamp(Number(crop.right)||0,0,.95);
          const t=clamp(Number(crop.top)||0,0,.95),b=clamp(Number(crop.bottom)||0,0,.95);
          const cw=Math.max(.02,1-l-r),ch=Math.max(.02,1-t-b);
          img.style.left=`${-l/cw*100}%`;img.style.top=`${-t/ch*100}%`;
          img.style.width=`${100/cw}%`;img.style.height=`${100/ch}%`;
          node.style.transform=`rotate(${Number(im.rotation)||0}deg) scale(${im.flipH?-1:1},${im.flipV?-1:1})`;
          img.addEventListener('error',()=>node.classList.add('picture-load-failed'));
          node.appendChild(img);
          node.dataset.id=key;
          node.addEventListener('click',e=>{e.stopPropagation();this.open(im.id);});
          this.nodes.set(key,node);
        }
        if(node.parentElement!==this.panes[name])this.panes[name].appendChild(node);
        const style=node.style;
        const px=x-p.x,py=y-p.y;
        // Set only changed properties: pointer/scroll hot paths must not force
        // a full sheet repaint or remount <img> elements.
        for(const [prop,value] of [['left',fmt(px)],['top',fmt(py)],['width',fmt(rect.w)],['height',fmt(rect.h)]]){
          if(style[prop]!==value)style[prop]=value;
        }
      }
      for(const [key,node] of this.nodes){if(!required.has(key)){node.remove();this.nodes.delete(key);}}
    }
    toggle(){
      // In a filtered/reordered view, source coordinates cannot be mapped to
      // visible row positions. Offer the gallery instead of an inert toggle.
      if(this.o.state.filtered&&this.items.length){this.open(this.items[0].id);return;}
      this.enabled=!this.enabled;
      $('imagesBtn').setAttribute('aria-pressed',String(this.enabled));
      if(!this.enabled){for(const node of this.nodes.values())node.remove();this.nodes.clear();}
      else this.schedule();
      $('imagesBtn').title=this.enabled?'隐藏工作表图片':'显示工作表图片';
    }
    open(id){
      const index=this.items.findIndex(im=>im.id===id);if(index<0)return;
      this.focusBeforeViewer=this.viewer.hidden?document.activeElement:this.focusBeforeViewer;
      this.active=index;this.panX=0;this.panY=0;
      const im=this.items[index];
      this.rotation=Number(im.rotation)||0;
      this.flipH=Boolean(im.flipH);this.flipV=Boolean(im.flipV);
      this.preview.onload=()=>{
        if(!this.viewer.hidden&&this.items[this.active]===im){
          this.natural={w:this.preview.naturalWidth||1,h:this.preview.naturalHeight||1};this.fit();
        }
      };
      this.preview.onerror=()=>{$('pictureCaption').textContent='该图片格式无法预览';};
      this.preview.alt=im.description||im.name||`图片 ${index+1}`;
      this.preview.src=im.src;
      this.viewer.hidden=false;
      this.viewer.focus({preventScroll:true});
      this.setCaption();
      if(this.preview.complete&&this.preview.naturalWidth){this.natural={w:this.preview.naturalWidth,h:this.preview.naturalHeight};this.fit();}
    }
    close(){
      const wasOpen=!this.viewer.hidden;
      this.viewer.hidden=true;this.preview.removeAttribute('src');
      if(wasOpen&&this.focusBeforeViewer?.isConnected&&this.focusBeforeViewer.focus){
        this.focusBeforeViewer.focus({preventScroll:true});
      }
      this.focusBeforeViewer=null;
    }
    setCaption(){
      const im=this.items[this.active];if(!im)return;
      $('pictureCaption').textContent=im.description||im.name||`图片 ${this.active+1}`;
      $('pictureIndex').textContent=`${this.active+1} / ${this.items.length}`;
      $('picturePrevious').disabled=this.items.length<2;
      $('pictureNext').disabled=this.items.length<2;
      $('pictureDownload').href=im.src+'&download=1';
      $('pictureDownload').download='';
    }
    select(delta){if(!this.items.length)return;this.open(this.items[(this.active+delta+this.items.length)%this.items.length].id);}
    fit(){
      if(this.viewer.hidden)return;
      const r=this.stage.getBoundingClientRect();
      // Account for arbitrary OOXML rotations, not only quarter turns.
      const angle=this.rotation*Math.PI/180,cos=Math.abs(Math.cos(angle)),sin=Math.abs(Math.sin(angle));
      const width=this.natural.w*cos+this.natural.h*sin;
      const height=this.natural.w*sin+this.natural.h*cos;
      this.zoom=clamp(Math.min(1,(r.width-56)/width,(r.height-56)/height),.01,8);
      this.fitToWindow=true;this.panX=0;this.panY=0;this.paintViewer();
    }
    actual(){this.fitToWindow=false;this.zoom=1;this.panX=0;this.panY=0;this.paintViewer();}
    changeZoom(value,point){
      const next=clamp(value,.05,8),prev=this.zoom;
      if(next===prev)return;
      const bounds=this.stage.getBoundingClientRect();
      const x=(point?.x??bounds.left+bounds.width/2)-bounds.left-bounds.width/2;
      const y=(point?.y??bounds.top+bounds.height/2)-bounds.top-bounds.height/2;
      this.panX=x-(x-this.panX)*(next/prev);
      this.panY=y-(y-this.panY)*(next/prev);
      this.fitToWindow=false;this.zoom=next;this.paintViewer();
    }
    paintViewer(){
      this.preview.style.width=fmt(this.natural.w);
      this.preview.style.height=fmt(this.natural.h);
      this.preview.style.transform=`translate(calc(-50% + ${this.panX}px),calc(-50% + ${this.panY}px)) rotate(${this.rotation}deg) scale(${this.flipH?-this.zoom:this.zoom},${this.flipV?-this.zoom:this.zoom})`;
      $('pictureZoomValue').textContent=`${Math.round(this.zoom*100)}%`;
    }
    async copy(){
      const im=this.items[this.active];if(!im)return;
      try{
        const blob=await (await fetch(im.src)).blob();
        if(!navigator.clipboard?.write||!window.ClipboardItem)throw Error('当前环境不支持复制图片');
        // Chromium clipboard supports PNG. Convert other formats via canvas.
        let png=blob;
        if(blob.type!=='image/png'){
          const bitmap=await createImageBitmap(blob);
          const canvas=document.createElement('canvas');canvas.width=bitmap.width;canvas.height=bitmap.height;
          canvas.getContext('2d').drawImage(bitmap,0,0);bitmap.close?.();
          png=await new Promise((resolve,reject)=>canvas.toBlob(b=>b?resolve(b):reject(Error('图片转换失败')),'image/png'));
        }
        await navigator.clipboard.write([new ClipboardItem({'image/png':png})]);
        this.o.toast('图片已复制','success');
      }catch(err){this.o.toast('复制图片失败：'+err.message,'error');}
    }
    bind(){
      $('imagesBtn').addEventListener('click',()=>this.toggle());
      $('pictureClose').addEventListener('click',()=>this.close());
      this.viewer.addEventListener('pointerdown',e=>{if(e.target===this.viewer)this.close();});
      $('pictureFit').addEventListener('click',()=>this.fit());
      $('pictureActual').addEventListener('click',()=>this.actual());
      $('pictureZoomIn').addEventListener('click',()=>this.changeZoom(this.zoom*1.25));
      $('pictureZoomOut').addEventListener('click',()=>this.changeZoom(this.zoom/1.25));
      $('pictureRotate').addEventListener('click',()=>{this.rotation=(this.rotation+90)%360;this.fit();});
      $('picturePrevious').addEventListener('click',()=>this.select(-1));
      $('pictureNext').addEventListener('click',()=>this.select(1));
      $('pictureCopy').addEventListener('click',()=>this.copy());
      this.stage.addEventListener('wheel',e=>{
        e.preventDefault();this.changeZoom(this.zoom*Math.exp(-e.deltaY*.0015),{x:e.clientX,y:e.clientY});
      },{passive:false});
      let drag=null;
      this.stage.addEventListener('pointerdown',e=>{
        if(e.button!==0||this.viewer.hidden)return;
        drag={id:e.pointerId,x:e.clientX,y:e.clientY,panX:this.panX,panY:this.panY};
        this.stage.setPointerCapture(e.pointerId);this.stage.classList.add('panning');e.preventDefault();
      });
      this.stage.addEventListener('pointermove',e=>{
        if(!drag||e.pointerId!==drag.id)return;
        this.panX=drag.panX+e.clientX-drag.x;this.panY=drag.panY+e.clientY-drag.y;this.paintViewer();
      });
      const stop=e=>{if(drag&&e.pointerId===drag.id){drag=null;this.stage.classList.remove('panning');}};
      this.stage.addEventListener('pointerup',stop);this.stage.addEventListener('pointercancel',stop);
      this.stage.addEventListener('dblclick',()=>{if(Math.abs(this.zoom-1)<.01)this.fit();else this.actual();});
      document.addEventListener('keydown',e=>{
        if(this.viewer.hidden)return;
        if(e.key==='Tab'){
          // Keep keyboard focus in the modal; restore the previous focus on close.
          const controls=[...this.viewer.querySelectorAll('button:not(:disabled),a[href]')];
          if(controls.length){
            const first=controls[0],last=controls[controls.length-1];
            if(e.shiftKey&&(document.activeElement===first||document.activeElement===this.viewer)){
              e.preventDefault();last.focus();
            }else if(!e.shiftKey&&(document.activeElement===last||document.activeElement===this.viewer)){
              e.preventDefault();first.focus();
            }
          }
          return;
        }
        if(e.key==='Escape'){e.preventDefault();e.stopImmediatePropagation();this.close();return;}
        const input=['INPUT','TEXTAREA','SELECT'].includes(document.activeElement?.tagName);
        if(input)return;
        let done=true;
        switch(e.key){
          case '+':case '=':this.changeZoom(this.zoom*1.25);break;
          case '-':this.changeZoom(this.zoom/1.25);break;
          case '0':this.fit();break;
          case '1':this.actual();break;
          case 'ArrowLeft':this.select(-1);break;
          case 'ArrowRight':this.select(1);break;
          case 'r':case 'R':this.rotation=(this.rotation+90)%360;this.fit();break;
          default:done=false;
        }
        if(done){e.preventDefault();e.stopImmediatePropagation();}
      },true);
      new ResizeObserver(()=>{if(!this.viewer.hidden){if(this.fitToWindow)this.fit();else this.paintViewer();}}).observe(this.stage);
    }
  }
  window.ExcelLensPictures=Pictures;
})();
