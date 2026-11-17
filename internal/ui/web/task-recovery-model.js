/* Persistent task metadata only. No source reads or automatic job starts. */
(() => {
  'use strict';
  const operation=item=>item.driver==='source-search'?'search':item.driver==='source-filter'?'filter':item.driver==='source-export'?'export':'';
  function items(data,maximum){
    if(!Array.isArray(data?.items)||data.items.length>maximum)throw Error('任务列表无效，请刷新重试');
    const seen=new Set();
    return data.items.map(item=>{
      if(!item||!/^([a-f0-9]{32})$/.test(item.id)||seen.has(item.id)||typeof item.revision!=='string'||!/^\d+$/.test(item.revision)||BigInt(item.revision)>18446744073709551615n)throw Error('任务列表无效，请刷新重试');
      seen.add(item.id);
      for(const key of ['driver','file','path','book','workbookKey','sheet','query','mode','scope','error'])if(typeof item[key]!=='string')throw Error('任务信息不完整，请刷新重试');
      for(const key of ['busy','complete','backup'])if(typeof item[key]!=='boolean')throw Error('任务状态无效，请刷新重试');
      if(item.revision==='0'&&!item.error)throw Error('任务版本无效，请刷新重试');
      return {...item};
    });
  }
  const matches=(item,state)=>!!state.opened&&item.book===state.book&&item.workbookKey===state.workbookKey;
  const request=(item,state)=>({id:item.id,revision:item.revision,book:state.book||'',workbookKey:state.workbookKey||''});
  const label=item=>operation(item)==='search'?'搜索':operation(item)==='filter'?'筛选 / 排序':operation(item)==='export'?'导出 '+item.mode.toUpperCase():'任务';
  function action(item,state){
    if(item.busy||item.error||!operation(item)||item.revision==='0')return '';
    if(!matches(item,state))return 'open';
    return item.complete&&operation(item)==='export'?'download':'resume';
  }
  window.ExcelLensTaskRecoveryModel={items,operation,matches,request,label,action};
})();
