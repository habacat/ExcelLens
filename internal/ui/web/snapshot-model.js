(() => {
  'use strict';
  const integer=(n,min,max)=>Number.isSafeInteger(n)&&n>=min&&n<=max,enc=new TextEncoder();
  const bad=()=>{throw Error('快照数据无效，请刷新后重试');};
  function bounded(value,limit){if(enc.encode(window.ExcelLensChartExportModel.stringify(value,limit)).length>limit)bad();}
  function cell(c){
    if(!c||typeof c.exact!=='string'||typeof c.display!=='string'||!integer(c.count,0,Number.MAX_SAFE_INTEGER))bad();
    if(c.comparison&&(!['','empty','empty_denominator','zero_denominator'].includes(c.comparison.status||'')||typeof c.comparison.exact!=='string'||typeof c.comparison.display!=='string'))bad();return c;
  }
  function catalog(data,limits){
    bounded(data,limits.bytes);
    if(data?.version!==1||!integer(data.revision,0,Number.MAX_SAFE_INTEGER-1)||!Array.isArray(data.items)||data.items.length>limits.entries)bad();const ids=new Set();
    for(const item of data.items){if(typeof item.id!=='string'||!item.id||ids.has(item.id)||typeof item.name!=='string'||typeof item.file!=='string'||!Number.isFinite(Date.parse(item.created)))bad();ids.add(item.id);}return data;
  }
  function snapshot(data,id,limits){
    bounded(data,limits.bytes);const r=data?.result;
    if(data?.version!==1||data.id!==id||typeof data.name!=='string'||typeof data.file!=='string'||!data.workbookKey||!data.book||!data.query||!Array.isArray(data.fields)||!Number.isFinite(Date.parse(data.created))||!r||!Array.isArray(r.columnKeys)||r.columnKeys.length>limits.series||!Array.isArray(r.rows)||r.rows.length>limits.rows||!Array.isArray(r.totals)||r.totals.length!==r.columnKeys.length)bad();
    const keys=new Set();for(const row of r.rows){if(!Array.isArray(row.keys)||!row.keys.every(s=>typeof s==='string')||!Array.isArray(row.values)||row.values.length!==r.columnKeys.length)bad();const k=JSON.stringify(row.keys);if(keys.has(k))bad();keys.add(k);row.values.forEach(cell);cell(row.total);}r.totals.forEach(cell);cell(r.grand);return data;
  }
  function comparison(data,left,right,limits){
    bounded(data,8*limits.bytes);
    if(data?.leftId!==left.id||data.rightId!==right.id||!Array.isArray(data.rows)||data.rows.length>limits.rows*2||!Array.isArray(data.columnKeys)||data.columnKeys.length>limits.series*2||!Array.isArray(data.totals)||data.totals.length!==data.columnKeys.length)bad();
    const delta=d=>{if(!d||!['','only_left','only_right','empty','unavailable','missing'].includes(d.status||''))bad();if(d.left)cell(d.left);if(d.right)cell(d.right);cell(d.difference);if(!d.change||typeof d.change.exact!=='string'||typeof d.change.display!=='string')bad();};
    for(const row of data.rows){if(!Array.isArray(row.keys)||!row.keys.every(s=>typeof s==='string')||!Array.isArray(row.values)||row.values.length!==data.columnKeys.length)bad();row.values.forEach(delta);delta(row.total);}data.totals.forEach(delta);delta(data.grand);return data;
  }
  function metric(s){const m=s.metric,q=s.query,field=id=>q.transforms?.find(f=>f.id===id)?.name||s.fields.find(f=>f.id===id)?.name||id||'记录数';return (m.name||field(m.field)+' '+({sum:'求和',average:'平均',count:'计数',distinct:'去重计数',min:'最小',max:'最大'}[m.aggregate]||m.aggregate))+(m.comparison?' '+window.ExcelLensAnalysisComparisons.label(m):'');}
  function seriesCells(s,key){const j=s.result.columnKeys.indexOf(key);return s.result.rows.map(row=>key===null?row.total:j<0?null:row.values[j]);}
  function description(s){
    const q=s.query,field=id=>q.transforms?.find(f=>f.id===id)?.name||s.fields.find(f=>f.id===id)?.name||'字段',source=q.composition?.name||(q.source.startsWith('wb:')?q.source.slice(3):'分析工作表');
    const lines=[s.file+' / '+source,'分析时间：'+new Date(s.analysisAt||s.created).toLocaleString(),'参与记录：'+s.result.included.toLocaleString()];
    if(q.sourceScope)lines.push('范围：'+(window.ExcelLensAnalysisSourceModel?.caption(q.sourceScope)||'所选范围'));
    if(q.headerRow)lines.push('表头：第 '+q.headerRow+' 行');
    if(q.rows?.length)lines.push('分类：'+q.rows.map(field).join(' / '));if(q.columns?.length)lines.push('系列：'+q.columns.map(field).join(' / '));
    const ops={eq:'等于',neq:'不等于',contains:'包含',notcontains:'不包含',startswith:'开头是',endswith:'结尾是',blank:'为空',notblank:'不为空',gt:'大于',gte:'不小于',lt:'小于',lte:'不大于',between:'介于',in:'属于'};
    for(const f of q.filters||[])lines.push('筛选：'+field(f.field)+' '+(ops[f.op]||f.op)+(f.value?' '+f.value:''));
    for(const t of q.transforms||[])lines.push('计算列：'+t.name);
    return lines.join('\n');
  }
  window.ExcelLensSnapshotModel={catalog,snapshot,comparison,metric,seriesCells,description};
})();
