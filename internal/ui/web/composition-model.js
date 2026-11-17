(() => {
  'use strict';
  const copy=value=>JSON.parse(JSON.stringify(value));
  const label=column=>{let out='';while(column>0){column--;out=String.fromCharCode(65+column%26)+out;column=Math.floor(column/26);}return out;};
  function suggest(expected,actual){
    const matches=new Map();for(const field of actual){const hash=field.headerHash;if(!hash)continue;matches.set(hash,matches.has(hash)?null:field);}
    return expected.map(field=>{
      const hash=field.headerHash;if(!hash||!field.headerLoaded||!field.headerUnique||field.truncated)return null;
      const match=matches.get(hash);if(!match?.headerLoaded||!match.headerUnique||match.truncated)return null;
      return match?.column??null;
    });
  }
  function initialRows(fields,columns){
    const used=new Set();return fields.slice(0,columns).map(field=>{
      let name=String(field.name||label(field.column)+' 列').trim(),suffix=1,base=name;
      while(used.has(name))name=base+' '+(++suffix);used.add(name);
      return {name,columns:[field.column],expected:field};
    });
  }
  function validate(name,rows,inputs,limits){
    if(!name.trim()||Array.from(name.trim()).length>80)return '请输入联合来源名称（最多80字）';
    if(inputs.length<2||inputs.length>limits.sources||inputs.some(input=>!input.info))return '请至少选择两份来源并读取字段';
    if(!rows.length||rows.length>limits.columns)return '请选择结果字段';
    const names=new Set(),sources=new Set();
    for(const input of inputs){const ref=copy(input.info.ref);delete ref.stamp;const key=JSON.stringify(ref);if(sources.has(key))return '同一来源和范围不能重复追加';sources.add(key);}
    for(const row of rows){const name=row.name.trim();if(!name||Array.from(name).length>128||names.has(name))return '结果字段名称不能为空或重复';names.add(name);if(!row.columns.some(column=>column>0))return '每个结果字段至少选择一份来源';}
    for(let i=0;i<inputs.length;i++){
      const used=new Set(),fields=new Set(inputs[i].info.fields.map(field=>field.column));
      for(const row of rows){const column=row.columns[i];if(column===null||column===undefined)return '请确认未匹配字段，可选择空列';if(!Number.isInteger(column)||column<0||column>0&&(!fields.has(column)||used.has(column)))return '来源字段无效或重复使用';if(column>0)used.add(column);}
      if(!used.size)return '每份来源至少选择一个字段';
    }
    return '';
  }
  function definition(name,rows,inputs){return {version:1,name:name.trim(),fields:rows.map(row=>row.name.trim()),inputs:inputs.map((input,index)=>({ref:copy(input.info.ref),columns:rows.map(row=>row.columns[index])}))};}
  function search(fields,text,page,size){const q=String(text||'').trim().toLocaleLowerCase(),matches=fields.filter(field=>!q||(label(field.column)+' '+field.column+' '+field.name).toLocaleLowerCase().includes(q));return {count:matches.length,items:matches.slice(page*size,(page+1)*size)};}
  const sampleTypes=(row,inputs)=>inputs.map((input,i)=>input.info?.fields.find(field=>field.column===row.columns[i])?.sampleType);
  function typeConflict(row,inputs){const types=sampleTypes(row,inputs).filter(type=>type&&!['empty','unknown','cache'].includes(type));return types.includes('mixed')||new Set(types).size>1;}
  function typeWarning(row,inputs){const types=sampleTypes(row,inputs);return typeConflict(row,inputs)?'示例类型不同':types.includes('unknown')?'类型未确认':types.includes('cache')?'示例缺缓存':'';}
  window.ExcelLensCompositionModel={copy,label,suggest,initialRows,validate,definition,search,typeConflict,typeWarning};
})();
