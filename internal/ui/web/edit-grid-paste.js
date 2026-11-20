(() => {
  'use strict';
  // A clipboard can outlive a source tab. Embed only the copied rectangle.
  // The backend authenticates its portable style components before importing.
  function markup(typed) {
    const portable=typed.clip?.portable;
    if (!portable) return '';
    const bytes=new TextEncoder().encode(JSON.stringify(portable));
    let binary='';
    for(let i=0;i<bytes.length;i+=8192) binary+=String.fromCharCode(...bytes.subarray(i,i+8192));
    return ' data-excellens-range="'+btoa(binary)+'"';
  }

  function identify(raw,html,copied,limit) {
    if(copied && raw.replaceAll('\r\n','\n')===copied.text && (html.includes('id="'+copied.marker+'"')||html.includes("id='"+copied.marker+"'")))return copied;
    const attr=/\bdata-excellens-range=["']([a-zA-Z0-9+/=]+)["']/.exec(html);
    if(attr){
      if(attr[1].length>2*limit)throw Error('复制区域超过编辑容量');
      const text=new TextDecoder().decode(Uint8Array.from(atob(attr[1]),c=>c.charCodeAt(0)));
      return {portable:JSON.parse(text)};
    }
    return /<(?:table|tr|td)\b/i.test(html) ? {external:true} : null;
  }

  async function native(owner,target,raw) {
    const bytes=new TextEncoder().encode(raw.replaceAll('\r\n','\n').replace(/\n$/,''));
    const hash=new Uint8Array(await crypto.subtle.digest('SHA-256',bytes));
    const proof=Array.from(hash,b=>b.toString(16).padStart(2,'0')).join('');
    return owner.post('native-paste',{...target,proof});
  }

  async function destination(ranges,rect,height,width) {
    const {top,bottom,left,right}=rect;
    const filter=ranges.view.mappedRows();
    const size=filter?ranges.view.visibleSelection().rows:bottom-top+1;
    const selected=size>1||right>left;
    if(selected && (size%height || (right-left+1)%width))throw Error('请选择一个粘贴起点或与复制区域匹配的矩形');
    const h=selected?size:height,w=selected?right-left+1:width;
    if(h*w>ranges.owner.limits.batchCells)throw Error('区域超出一次编辑容量，请缩小范围');
    const rows=filter?await ranges.view.pasteRows(h):Array.from({length:h},(_,i)=>top+i);
    if(rows.length!==h||new Set(rows).size!==h)throw Error('筛选后的可见行不足，无法粘贴整个区域');
    const shape=ranges.grid.shape;
    if(rows.some(r=>!Number.isInteger(r)||r<shape.firstRow||r>shape.rows)||left<shape.firstCol||left+w-1>shape.cols)throw Error('选区超出可编辑区域');
    return {rows,width:w,left};
  }

  async function pasteRange(ranges,target,rect,clip) {
    const selection=clip.range;
    const dest=await destination(ranges,rect,selection.rows,selection.cols);
    ranges.current(target);
    const {rows,left,width}=dest;
    const cell=(r,c)=>ranges.owner.colName(c)+r;
    const range=cell(rows[0],left)+':'+cell(ranges.view.mappedRows()?rows[0]:rows.at(-1),left+width-1);
    const structure={kind:'pasteRange',sheet:target.sheet,range,paste:selection};
    if(ranges.view.mappedRows())structure.targetRows=rows;
    if(!selection.merges?.length){
      const preview=await ranges.owner.post('preview',{...target,mode:'portable',proof:clip.proof,operation:{structure}});
      ranges.current(target);
      if(!preview.structure)return ranges.owner.commit('change',preview.changes,{proof:preview.proof,direct:true,expandTable:!ranges.view.mappedRows()});
    }
    return ranges.owner.commit('structure',undefined,{structure,proof:clip.proof,direct:true});
  }
  window.ExcelLensGridPaste={markup,identify,native,destination,pasteRange};
})();
