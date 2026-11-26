/* Derived presentation never replaces the source value, type or style ID. */
(() => {
  'use strict';
  const value=(cell,formulas=false,styles=true)=>cell?(formulas&&cell.hasFormula&&cell.formula?cell.formula:String(styles&&cell.conditional?.display!=null?cell.conditional.display:cell.display??cell.raw??'')):'';
  const visual=(cell,palette,rowStyle=0,enabled=true)=>enabled?(cell?.conditional?.visual??palette?.[Number(cell?.style??rowStyle)||0]??{}):null;
  const color=(cell,style)=>cell?.conditional?.valueColor!=null?(cell.conditional.valueColor||style?.color||''):(cell?.valueColor||style?.color||'');
  const font=cell=>cell?.conditional?.style?.font||{};
  const needsInline=(cell,base={})=>['superscript','subscript'].includes(font(cell).vertical||base?.fontVertical);
  function notice(node,cell,row,enabled,base={}){
    const notices=[];
    const pending=Boolean(enabled&&cell?.conditional?.pending);
    if(enabled){if(cell?.conditional?.notice)notices.push(cell.conditional.notice);if(row?.presentationNotice)notices.push(row.presentationNotice);
      if(cell?.conditional?.style?.rotation)notices.push('条件格式文字旋转尚未显示');
      if(/Accounting$/.test(font(cell).underline||base?.underlineStyle||''))notices.push('会计下划线按文字下划线显示');
    }
    const text=[...new Set(notices)].join('；');node._xlPresentationNotice=text;
    if(pending)node.classList.add('cell-conditional-pending');else node.classList.remove('cell-conditional-pending');
    if(text){node.classList.add('cell-presentation-notice');node.title=[node._xlRichNotice,text].filter(Boolean).join('；');}
    else node.classList.remove('cell-presentation-notice');
  }
  const same=(a,b)=>a===b||!!a&&!!b&&JSON.stringify(a)===JSON.stringify(b);
  // Include both parsed objects and transient/cached serialized comparisons.
  const estimate=p=>p?512+4*JSON.stringify(p).length:0;
  // A cancel can arrive before the start response supplies the job ID. Keep the
  // following retry behind that cancellation, even when the server reuses jobs.
  function createStatsRequests(post) {
    let tail = Promise.resolve();
    let started;
    function enqueue(action) {
      const result = tail.then(action);
      tail = result.catch(() => {});
      return result;
    }
    return {
      start(sheet) {
        started = enqueue(() => post('start', { sheet }));
        return started;
      },
      cancel(id) {
        const prior = started;
        started = null;
        return enqueue(async () => {
          const target = id || (await prior?.catch(() => null))?.jobId;
          if (target) await post('cancel', { id: target });
        });
      },
    };
  }
  window.ExcelLensConditional={value,visual,color,font,needsInline,notice,same,estimate,createStatsRequests};
})();
