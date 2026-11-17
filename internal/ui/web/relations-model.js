/* Exact relation envelopes and bounded source presentation, without IO/DOM. */
(() => {
  'use strict';
  const copy=value=>JSON.parse(JSON.stringify(value)),integer=(n,min,max)=>Number.isSafeInteger(n)&&n>=min&&n<=max;
  const countKeys=['leftRecords','rightRecords','leftInvalid','rightInvalid','leftDuplicateGroups','rightDuplicateGroups','manyToManyGroups','matchingGroups','leftMatched','rightMatched','leftOnly','rightOnly','innerRows','leftJoinRows'];
  const modes=Object.freeze([{id:'left',label:'左表全部',sides:[0,1]},{id:'inner',label:'匹配明细',sides:[0,1]},{id:'left_matched',label:'左表已匹配',sides:[0]},{id:'left_unmatched',label:'左表未匹配',sides:[0]},{id:'right_unmatched',label:'右表未匹配',sides:[1]},{id:'left_invalid',label:'左表无效键',sides:[0]},{id:'right_invalid',label:'右表无效键',sides:[1]}]);
  const mode=id=>modes.find(item=>item.id===id),fail=()=>{throw Error('关联响应与当前来源或查询不一致，请重新运行');};
  const count=value=>{if(typeof value!=='string'||!/^(0|[1-9]\d{0,18})$/.test(value))fail();return BigInt(value);};
  const total=(counts,id)=>({left:counts.leftJoinRows,inner:counts.innerRows,left_matched:counts.leftMatched,left_unmatched:counts.leftOnly,right_unmatched:counts.rightOnly,left_invalid:counts.leftInvalid,right_invalid:counts.rightInvalid})[id];
  const formatCount=value=>count(String(value)).toLocaleString('zh-CN');
  const fieldRef=info=>({book:info.ref.book,source:info.ref.source,headerRow:info.ref.headerRow,stamp:info.ref.stamp});
  const queryKey=query=>JSON.stringify([query.keys.map(pair=>[pair.left,pair.right,pair.mode||'',pair.trim??false,pair.ignoreCase??false]),query.leftColumns,query.rightColumns]);
  function validateLimits(value){for(const key of ['keys','columns','previewRows','defaultRows','maximumRows','requestBytes','resultBytes','viewRows','viewColumns','initialFields','snippetUnits'])if(!integer(value?.[key],1,Number.MAX_SAFE_INTEGER))throw Error('关联容量配置无效');if(value.viewRows>value.previewRows||value.initialFields>value.columns||value.defaultRows>value.maximumRows||value.zipRows!==undefined&&!integer(value.zipRows,1,value.maximumRows))throw Error('关联容量配置无效');return copy(value);}
  function validateFields(data,choice,identity,formats){
    const info=data?.info,ref=info?.ref;
    if(data?.workbookKey!==identity.bookKey||data?.book!==identity.book||data?.mode!=='fields'||!info||!ref||ref.book!==choice.book||ref.source!==choice.source||ref.headerRow!==choice.headerRow||ref.scope!=null||typeof ref.fingerprint!=='string'||!ref.fingerprint||typeof ref.stamp!=='string'||!ref.stamp||!['workbook','analysis'].includes(info.kind)||typeof info.name!=='string'||typeof info.sheet!=='string'||!integer(info.rows,0,formats.maxRows)||!Array.isArray(info.fields)||info.fields.length<1||info.fields.length>formats.maxColumns)fail();
    let previous=0;for(const field of info.fields){if(!integer(field.column,previous+1,formats.maxColumns)||typeof field.name!=='string'||field.headerHash!==undefined&&typeof field.headerHash!=='string')fail();for(const key of ['headerLoaded','headerUnique','truncated'])if(field[key]!==undefined&&typeof field[key]!=='boolean')fail();previous=field.column;}
    return copy(info);
  }
  function validateQuery(query,infos,limits){
    if(!query||!Array.isArray(query.keys)||query.keys.length<1||query.keys.length>limits.keys)throw Error('请选择关联键');
    for(let side=0;side<2;side++){
      const columns=query[side?'rightColumns':'leftColumns'];if(!Array.isArray(columns)||columns.length>limits.columns)throw Error('结果字段超出容量');
      const available=new Set(infos[side].fields.map(field=>field.column)),used=new Set(),keys=new Set();
      for(const column of columns){if(!available.has(column)||used.has(column))throw Error('结果字段不存在或重复');used.add(column);}
      for(const pair of query.keys){if(!pair||typeof pair!=='object'||Array.isArray(pair))throw Error('关联键无效或重复');const column=pair[side?'right':'left'];if(!available.has(column)||keys.has(column)||pair.mode!==undefined&&typeof pair.mode!=='string'||!['','text','number'].includes(pair.mode||'')||pair.trim!==undefined&&typeof pair.trim!=='boolean'||pair.ignoreCase!==undefined&&typeof pair.ignoreCase!=='boolean')throw Error('关联键无效或重复');keys.add(column);}
    }
    if(!query.leftColumns.length&&!query.rightColumns.length)throw Error('请选择结果字段');return query;
  }
  function validate(data,expected,limits,formats){
    if(!data||data.id!==expected.id||data.workbookKey!==expected.identity.bookKey||data.book!==expected.identity.book||data.outputMode!==expected.mode||!mode(data.outputMode)||data.mode!==expected.operation||!Array.isArray(data.query?.keys)||!Array.isArray(data.query.leftColumns)||!Array.isArray(data.query.rightColumns))fail();
    const query=data.query,refs=expected.infos.map(fieldRef),sources=[data.left,data.right];
    if(data.parentId!==(expected.operation==='prepare'?data.id:expected.parentId))fail();
    validateQuery(query,expected.infos,limits);
    if(queryKey(query)!==queryKey(expected.query))fail();
    for(let side=0;side<2;side++){
      const info=sources[side],original=expected.infos[side],ref=refs[side];
      if(!info||info.id!==ref.book+'/'+ref.source||info.fingerprint!==original.ref.fingerprint||info.stamp!==ref.stamp||info.headerRow!==ref.headerRow||info.kind!==original.kind||(info.format||'')!==(original.format||'')||info.sheet!==original.sheet||info.name!==original.name||info.total!==original.rows||!integer(info.total,0,formats.maxRows)||!Array.isArray(info.fields))fail();
      const needed=new Set([...query[side?'rightColumns':'leftColumns'],...query.keys.map(key=>key[side?'right':'left'])]),seen=new Set();
      for(const field of info.fields){if(!needed.has(field.column)||seen.has(field.column)||typeof field.name!=='string')fail();seen.add(field.column);}
      if(seen.size!==needed.size)fail();
    }
    const c=data.counts;if(!c)fail();for(const key of countKeys)if(count(c[key])>BigInt(formats.maxRows)**2n)fail();
    if(count(c.leftRecords)!==BigInt(data.left.total)||count(c.rightRecords)!==BigInt(data.right.total)||count(c.leftMatched)+count(c.leftOnly)+count(c.leftInvalid)!==count(c.leftRecords)||count(c.rightMatched)+count(c.rightOnly)+count(c.rightInvalid)!==count(c.rightRecords)||count(c.innerRows)+count(c.leftOnly)+count(c.leftInvalid)!==count(c.leftJoinRows)||count(c.matchingGroups)>count(c.leftMatched)||count(c.matchingGroups)>count(c.rightMatched)||count(c.manyToManyGroups)>count(c.leftDuplicateGroups)||count(c.manyToManyGroups)>count(c.rightDuplicateGroups)||count(c.leftDuplicateGroups)*2n>count(c.leftRecords)||count(c.rightDuplicateGroups)*2n>count(c.rightRecords))fail();
    const preview=data.preview;if(!preview||preview.total!==total(c,data.outputMode)||!Array.isArray(preview.rows)||preview.rows.length>limits.previewRows||BigInt(preview.rows.length)>count(preview.total)||typeof preview.truncated!=='boolean'||preview.truncated!==(BigInt(preview.rows.length)<count(preview.total)))fail();
    const required=mode(data.outputMode).sides,previous=[-1,-1];
    for(let i=0;i<preview.rows.length;i++){
      const row=preview.rows[i];if(!row||row.sequence!==String(i+1)||!['matched','unmatched','invalid_key'].includes(row.status)||row.reason!==undefined&&typeof row.reason!=='string')fail();
      const positions=[-1,-1];
      for(let side=0;side<2;side++){
        const record=row[side?'right':'left'];if(record==null)continue;
        if(!required.includes(side)||!integer(record.position,0,formats.maxRows-1)||!integer(record.number,1,formats.maxRows)||typeof record.values!=='object'||!record.values||Array.isArray(record.values))fail();positions[side]=record.position;
        const allowed=new Set([...query[side?'rightColumns':'leftColumns'],...query.keys.map(key=>key[side?'right':'left'])]);
        for(const [key,value]of Object.entries(record.values)){
          if(!/^[1-9]\d*$/.test(key)||!allowed.has(Number(key))||!value||typeof value.raw!=='string'||typeof value.exists!=='boolean')fail();
          for(const prop of ['display','type','uncertain'])if(value[prop]!==undefined&&typeof value[prop]!=='string')fail();
          for(const prop of ['missingCache','typeMissing','truncated'])if(value[prop]!==undefined&&typeof value[prop]!=='boolean')fail();
        }
      }
      const rightCount=count(row.rightMatches);if(rightCount>count(c.rightRecords)||positions.every(at=>at<0)||row.status==='matched'&&(rightCount===0n||!row.left)||row.status!=='matched'&&(rightCount!==0n||row.left&&row.right)||row.status==='invalid_key'&&!row.reason)fail();
      if(data.outputMode==='left'&&(!row.left||row.status==='matched'&&!row.right)||data.outputMode==='inner'&&(!row.left||!row.right||row.status!=='matched')||data.outputMode==='left_matched'&&(!row.left||row.right||row.status!=='matched')||data.outputMode.endsWith('_unmatched')&&row.status!=='unmatched'||data.outputMode.endsWith('_invalid')&&row.status!=='invalid_key'||required.length===1&&!row[required[0]?'right':'left'])fail();
      if(positions[0]<previous[0]||positions[0]===previous[0]&&positions[1]<=previous[1])fail();previous[0]=positions[0];previous[1]=positions[1];
    }
    if(new TextEncoder().encode(JSON.stringify(data)).length>limits.resultBytes)fail();return copy(data);
  }
  function columns(data){return mode(data.outputMode).sides.flatMap(side=>data.query[side?'rightColumns':'leftColumns'].map(column=>({side,column,field:data[side?'right':'left'].fields.find(field=>field.column===column)})));}
  const cell=value=>value?{raw:value.raw,display:value.display??value.raw,type:value.type||'',cacheMissing:!!value.missingCache}:null;
  window.ExcelLensRelationsModel={copy,modes,mode,count,total,formatCount,fieldRef,validateLimits,validateFields,validateQuery,validate,columns,cell};
})();
