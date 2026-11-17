(() => {
  'use strict';
  const $=id=>document.getElementById(id);
  const TOKEN=document.querySelector('meta[name="app-token"]')?.content||'';
  const root=$('analysisRoot');if(!root)return;
  const templates=window.ExcelLensAnalysisTemplates||[];
  const charts=window.ExcelLensAnalyticsCharts;
  const numbers=window.ExcelLensAnalysisNumbers;
  const esc=s=>String(s??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
  const sleep=ms=>new Promise(r=>setTimeout(r,ms));
  const post=(path,data)=>api(path,{method:'POST',body:JSON.stringify(data)});
  const state={fingerprint:'',opened:false,tab:'pivot',workspace:null,source:'',headerRow:1,fields:[],preview:[],query:null,jobID:'',jobSeq:0,activeResult:null,activeResultQuery:null,loadedSheet:null,sheetPage:0,reportCache:new window.ExcelLensAnalysisResults(),reportOpenSerial:0,templateCategory:'全部',templateText:'',drillQuery:null,drillKeys:[],drillColumn:'',drillOffset:0,drillAbort:null,abortIndex:0,activeReportID:'',sheetRevisions:new Map(),undoHistory:new Map(),gridData:null,profileController:null,sheetLoadSeq:0,sheetAbortController:null};
  const drafts=new window.ExcelLensAnalysisDrafts();
  const compositionTasks=new window.ExcelLensCompositionTasks({api,context:()=>({bookKey:state.fingerprint,book:state.book,opened:state.opened})});
  const sourceRows=new window.ExcelLensAnalysisSources({api,tasks:compositionTasks,context:()=>({bookKey:state.fingerprint,book:state.book,opened:state.opened}),grid:mode=>window.ExcelLensAnalysisSourceContext(mode)});
  const compositionPanel=new window.ExcelLensComposition({api,toast,onUse:adoptComposition,limits:JSON.parse($('fileFormats').textContent).composition,context:()=>({bookKey:state.fingerprint,book:state.book,opened:state.opened,source:state.source,sourceScope:state.query?.sourceScope,composition:state.query?.composition,keepId:sourceRows.binding?.composition?sourceRows.binding.id:''})});
  window.ExcelLensCompositionUI=compositionPanel;
  const sourceModel=window.ExcelLensAnalysisSourceModel;
  const displaySettings=new window.ExcelLensAnalysisSettings({api,toast});
  const snapshots=window.ExcelLensSnapshotsController?new window.ExcelLensSnapshotsController({api,toast,limits:JSON.parse($('fileFormats').textContent).snapshots}):null;
  window.ExcelLensSnapshots=snapshots;
  const pasteSpecial=new window.ExcelLensPasteSpecial({capture:capturePasteTarget,toast,refresh:refreshPasteTarget,
    commit:(context,changes)=>commitBatch(context.id,changes,{refresh:true,guard:context.current})});
  const analysisJobs=new window.ExcelLensAnalysisJobs({api,identity:()=>JSON.stringify([state.fingerprint,state.book,state.opened]),requestIdentity:()=>state.fingerprint,sleep,
    onBusy:setAnalysisBusy,onProgress:showAnalysisProgress});
  const treeRenderer=new window.ExcelLensAnalysisTreeRenderer();
  const tree=new window.ExcelLensAnalysisTree({api,context:()=>({bookKey:state.fingerprint,book:state.book,opened:state.opened}),limits:JSON.parse($('fileFormats').textContent).analysisTree,
    onMessage:message=>toast(message,'warning'),onChange:event=>{if(state.opened&&state.activeResult?.tree&&tree.root?.job===state.jobID){if(event.type==='progress')treeRenderer.progress(event.node);else renderPivot();}}});
  const plans=window.ExcelLensRecipes?new window.ExcelLensRecipes({api,toast,adopt:adoptAnalysisPlan,
    context:()=>({bookKey:state.fingerprint,opened:state.opened,intent:state.reportOpenSerial,source:state.source,headerRow:state.headerRow,sourceId:sourceRows.binding?.id||'',sourceScope:state.query?.sourceScope,resultJob:analysisJobs.main||state.activeResultQuery?.composition?'':state.jobID,
      sources:[...$('analysisSourceSelect').options].filter(option=>!option.value.startsWith('append:')).map(option=>({value:option.value,label:option.textContent}))}),
    capture:()=>{if(!state.activeResult||!state.jobID||analysisJobs.main)throw Error('请先完成一次分析，再保存方案');return {jobId:state.jobID,format:$('analysisExportFormat').value};}}):null;
  let pendingWrites=0;
  let toastTimer=0,chartResize=null;
  const issuePanel=window.ExcelLensAnalysisIssues?new window.ExcelLensAnalysisIssues({api,identity:()=>state.fingerprint,toast,askName:askText,
    startExport:(...args)=>window.ExcelLensExportIssues(...args),
    onMaterialized:async data=>{await refreshWorkspace();state.loadedSheet=data.id;state.sheetPage=0;setTab('sheets');},
    navigate:async(q,issue,ref)=>{
      if(q.composition){await navigateComposition(issue,issue.sourceColumn,issuePanel.snapshot?.job);}
      else if(q.source.startsWith('wb:')){close();window.dispatchEvent(new CustomEvent('excellens:analysis-navigate',{detail:{sheet:q.source.slice(3),ref}}));}
      else{const id=q.source.slice(8);state.loadedSheet=id;state.sheetPage=Math.floor((issue.row-1)/sheetPageSize(id));setTab('sheets');await loadSheet(id,state.sheetPage);$('analysisEditableGrid').querySelector(`textarea[data-row="${issue.row-1}"][data-col="${Math.max(0,issue.sourceColumn-1)}"]`)?.focus();}
    }}):null;
  const clusters=window.ExcelLensAnalysisClusters?new window.ExcelLensAnalysisClusters({api,toast,scopeKey:sourceModel.key,limits:JSON.parse($('fileFormats').textContent).clustering,
    context:()=>({bookKey:state.fingerprint,book:state.book,opened:state.opened,ready:state.fieldsReady&&!state.fieldsLoading,source:state.source,headerRow:state.headerRow,sourceScope:state.query?.sourceScope,sourceId:sourceRows.binding?.id||''}),
    onTransform:(step,guard)=>{if(!guard())throw Error('分析来源已变化，请重新核对');if(state.query.transforms.length>=8)throw Error('最多8个计算列，请先移除部分计算列');state.query.transforms.push(JSON.parse(JSON.stringify(step)));renderFields();renderTransforms();},
    startExport:(jobId,protectCSV)=>window.ExcelLensExportClusters(jobId,protectCSV),
    navigate:async(data,guard)=>{if(!guard())return false;if(data.source.startsWith('wb:')){close();window.dispatchEvent(new CustomEvent('excellens:analysis-navigate',{detail:{sheet:data.source.slice(3),ref:data.ref}}));return true;}
      const id=data.source.slice(8);state.loadedSheet=id;state.sheetPage=Math.floor((data.row-1)/sheetPageSize(id));setTab('sheets');await loadSheet(id,state.sheetPage);if(!guard())return false;$('analysisEditableGrid').querySelector('textarea[data-row="'+(data.row-1)+'"][data-col="'+(data.column-1)+'"]')?.focus();return true;}}):null;
  if(window.ExcelLensRecovery){
    const recovery=new window.ExcelLensRecovery.Journal({kind:'analysis',api,
      onRestore:records=>{drafts.restore(records);updateSaveStatus();if(state.opened&&state.loadedSheet)loadSheet(state.loadedSheet,state.sheetPage).catch(error=>toast(error.message,'error'));},
      onStatus:()=>{const button=$('analysisDraftRecovery');button.title=recovery.error||'找回未确认的分析输入；核对后点击重试保存';button.classList.toggle('danger',!!recovery.error);}});
    window.ExcelLensAnalysisRecovery=recovery;drafts.onChange=(key,data)=>recovery.set(key,data);
    $('analysisDraftRecovery').addEventListener('click',()=>window.ExcelLensRecovery.show(recovery,{title:'恢复分析草稿',explanation:'草稿按原文件和编辑表匹配。载入后核对当前已保存值，再点击“重试保存”；本页同格草稿优先保留。'}));
  }
  async function api(path,opts={}){
    await window.ExcelLensSession?.ready;
    const headers=new Headers(opts.headers||{});headers.set('X-ExcelLens-Token',TOKEN);window.ExcelLensSession?.apply(headers);
    if(opts.body&&!(opts.body instanceof FormData))headers.set('Content-Type','application/json');
    const response=await fetch(path,{...opts,headers,cache:'no-store'});
    const data=await response.json().catch(()=>({}));
    if(!response.ok){const error=new Error(data.error||`请求失败 HTTP ${response.status}`);error.status=response.status;throw error}
    return data;
  }
  function toast(message,tone='info'){
    const el=$('analysisToast');
    el.dataset.tone=['info','success','warning','error'].includes(tone)?tone:'info';
    el.setAttribute('role',el.dataset.tone==='error'?'alert':'status');
    el.textContent=String(message??'');el.hidden=false;clearTimeout(toastTimer);toastTimer=setTimeout(()=>el.hidden=true,4800);
  }
  function fmtNum(n){if(!Number.isFinite(+n))return String(n??'—');return numbers.format({exact:String(n),display:String(n)})}
  function fieldName(id){if(!id)return '记录数';const f=state.query?.transforms?.find(x=>x.id===id)||state.fields.find(x=>x.id===id);return f?.name||id}
  function queryBase(source){return {source,headerRow:state.headerRow,rows:[],columns:[],value:'',aggregate:'count',metrics:[],filters:[],transforms:[],sort:'value_desc',top:30,precision:2,chart:'bar',chartMetric:0,layout:''}}
  function appendCompositionSources(select){
    for(const group of select.querySelectorAll?.('optgroup[data-composition]')||[])group.remove();
    const choices=new Map();for(const report of state.workspace?.reports||[])if(report.query.composition)choices.set(report.query.source,report.query.composition);
    if(state.compositionDraft)choices.set(state.compositionDraft.source,state.compositionDraft.definition);
    if(state.query?.composition)choices.set(state.query.source,state.query.composition);
    if(!choices.size)return;const group=document.createElement('optgroup');group.label='联合来源';group.setAttribute?.('data-composition','');for(const [source,definition] of choices)group.append(new Option(definition.name,source));select.append(group);
  }
  function queryScope(q){return q.composition?q.composition.inputs.length+' 个来源':sourceModel.caption(q.sourceScope);}
  async function adoptComposition(data,guard){
    if(!guard())return false;const book=state.fingerprint,pathBook=state.book,previousInfo=$('analysisSourceInfo').textContent,old={source:state.source,headerRow:state.headerRow,query:state.query,fields:state.fields,preview:state.preview,loadedFieldsKey:state.loadedFieldsKey,activeReportID:state.activeReportID};
    ++state.reportOpenSerial;state.source=data.source;state.headerRow=0;state.query={...queryBase(data.source),composition:JSON.parse(JSON.stringify(data.definition)),headerRow:0};
    const loaded=await loadFields({prepared:data,guard});
    if(book!==state.fingerprint||pathBook!==state.book||!state.opened)return false;
    if(!loaded){const reason=$('analysisSourceInfo').textContent;Object.assign(state,old);state.fieldsReady=!!old.loadedFieldsKey&&old.loadedFieldsKey===fieldsKey();renderSourceScope();renderFields();renderBuilder();$('analysisSourceSelect').value=state.source;$('analysisHeaderRow').value=String(state.headerRow);setText('analysisSourceInfo',previousInfo);setAnalysisBusy(!!analysisJobs.main);if(guard())throw Error(reason||'读取字段失败，请重试');return false;}
    analysisJobs.cancelMain();state.activeReportID='';state.compositionDraft={source:state.source,definition:JSON.parse(JSON.stringify(state.query.composition))};appendCompositionSources($('analysisSourceSelect'));$('analysisSourceSelect').value=state.source;$('analysisHeaderRow').value='0';setTab('pivot');setAnalysisBusy(false);return true;
  }
  function currentQuery(){const q=JSON.parse(JSON.stringify(state.query));q.source=state.source;q.headerRow=state.headerRow;q.chart=$('analysisChartSelect').value;return q}
  function setText(id,text){const el=$(id);if(el)el.textContent=text}
  const aggregations=[['sum','求和'],['count','计数'],['average','平均'],['min','最小'],['max','最大'],['distinct','去重计数']];
  const effectiveMetrics=q=>(q?.metrics?.length?q.metrics:[{field:q?.value||'',aggregate:q?.aggregate||'count',name:''}]);
  const metricLabel=(m,lookup)=>(m.name||((m.field?lookup(m.field):'记录数')+' · '+(aggregations.find(a=>a[0]===m.aggregate)?.[1]||m.aggregate)))+(m.comparison?' · '+window.ExcelLensAnalysisComparisons.label(m):'');
  const metricCaption=m=>metricLabel(m,fieldName);
  const resultMetricCaption=m=>metricLabel(m,resultFieldName);
  function syncFirstMetric(){
    if(state.query.metrics?.length){state.query.value=state.query.metrics[0].field||'';state.query.aggregate=state.query.metrics[0].aggregate||'count'}
  }
  function addMetric(field){
    const q=state.query;if(!q.metrics)q.metrics=[];
    if(!q.metrics.length&&q.value)q.metrics.push({field:q.value,aggregate:q.aggregate,name:''});
    if(q.metrics.length>=6){toast('一次分析最多6个指标','warning');return}
    const choice=field||allFields().find(f=>f.numeric)?.id||'';
    q.metrics.push({field:choice,aggregate:allFields().find(f=>f.id===choice)?.numeric?'sum':'count',name:''});
    syncFirstMetric();renderBuilder();
  }
  function renderMetricEditor(){
    const list=$('analysisMetricEditor');list.replaceChildren();
    const metrics=state.query.metrics?.length?state.query.metrics:[{field:state.query.value||'',aggregate:state.query.aggregate||'count',name:''}];
    const adopt=()=>{if(!state.query.metrics?.length)state.query.metrics=metrics;};
    metrics.forEach((metric,i)=>{
      const row=document.createElement('div');row.className='analysis-metric-row';
      const field=fieldSelect(metric.field,'记录数');field.title='参与统计的字段';
      field.onchange=()=>{adopt();metric.field=field.value;if(i===0)syncFirstMetric();renderBuilder()};
      const agg=document.createElement('select');agg.className='analysis-control';
      for(const [value,label] of aggregations)agg.append(new Option(label,value));
      agg.value=metric.aggregate;
      agg.onchange=()=>{adopt();metric.aggregate=agg.value;if(i===0)syncFirstMetric();renderBuilder()};
      const remove=document.createElement('button');remove.textContent='×';remove.title='移除指标';remove.onclick=()=>{
        adopt();state.query.metrics.splice(i,1);
        if(!state.query.metrics.length){state.query.value='';state.query.aggregate='count'}else syncFirstMetric();renderBuilder()
      };
      const presentation=document.createElement('div');presentation.className='analysis-metric-comparison';
      const compareLabel=document.createElement('label');compareLabel.textContent='结果显示';
      const compare=document.createElement('select');compare.className='analysis-control analysis-comparison-mode';compare.setAttribute('aria-label','指标 '+(i+1)+' 结果显示');
      for(const [value,label] of window.ExcelLensAnalysisComparisons.modes)compare.append(new Option(label,value));compare.value=metric.comparison||'';
      compare.onchange=()=>{adopt();metric.comparison=compare.value;if(!window.ExcelLensAnalysisComparisons.running(metric))delete metric.comparisonAxis;syncFirstMetric();renderBuilder();};compareLabel.append(compare);presentation.append(compareLabel);
      if(window.ExcelLensAnalysisComparisons.running(metric)){
        const axisLabel=document.createElement('label');axisLabel.textContent='累计方向';const axis=document.createElement('select');axis.className='analysis-control analysis-comparison-axis';axis.setAttribute('aria-label','指标 '+(i+1)+' 累计方向');axis.append(new Option('沿行（按分类排序）','rows'),new Option('沿列（按列标签排序）','columns'));axis.value=metric.comparisonAxis||'rows';axis.onchange=()=>{adopt();metric.comparisonAxis=axis.value;};axisLabel.append(axis);presentation.append(axisLabel);
      }
      row.append(field,agg,remove,presentation);list.append(row)
    });
  }
  function setTab(tab){
    if(tab!=='pivot')++state.reportOpenSerial;
    state.tab=tab;window.ExcelLensChartExport?.revalidate();
    tree.setBlocked(tab!=='pivot'||!!analysisJobs.main||state.reportPending===state.reportOpenSerial);
    root.querySelectorAll('[data-analysis-tab]').forEach(btn=>btn.classList.toggle('is-active',btn.dataset.analysisTab===tab));
    root.querySelectorAll('[data-analysis-panel]').forEach(el=>el.hidden=el.dataset.analysisPanel!==tab);
    $('analysisBuilder').hidden=tab!=='pivot';
    if(tab==='pivot')setTimeout(renderChart,40);
    if(tab==='templates')renderTemplates();
    if(tab==='sheets')return renderSheets();
    else {state.sheetLoadSeq++;state.sheetAbortController?.abort();state.sheetAbortController=null;}
    if(tab==='dashboard')renderDashboard();
  }
  async function open(){
    const ticket=state.openSerial=(state.openSerial||0)+1;
    state.fieldsReady=false;setAnalysisBusy(!!analysisJobs.main);
    try{
      const status=await api('/api/status');if(!status.opened){toast('请先打开原始 Excel 工作簿','warning');return}
      if(ticket!==state.openSerial)return;
      state.opened=true;root.hidden=false;document.body.classList.add('analysis-mode');
      displaySettings.load();
      const workspace=await api('/api/analysis/workspace');
      if(ticket!==state.openSerial||!state.opened)return;
      if(workspace.fingerprint!==status.workbookKey)throw Error('工作簿已切换，请重新打开分析');
      state.workspace=workspace;
      if(state.fingerprint&&state.workspace.fingerprint!==state.fingerprint){
        state.query=null;state.source='';state.compositionDraft=null;state.loadedSheet=null;state.activeResult=null;state.activeResultQuery=null;state.activeReportID='' ;
        state.undoHistory.clear();state.sheetRevisions.clear();state.gridData=null;
        analysisJobs.reset();state.reportCache.clear();state.jobID='';state.jobSeq++;
        clearResultDisplay();
      }
      state.fingerprint=state.workspace.fingerprint||'';state.book=status.book||'';state.sourceFormat=status.source?.format||'';
      const original=state.workspace.sourceSheets||[];
      const select=$('analysisSourceSelect');select.replaceChildren();
      const originalGroup=document.createElement('optgroup');originalGroup.label='原始 Excel · 只读';
      original.forEach(s=>{const o=new Option(s.name,'wb:'+s.name);originalGroup.append(o)});select.append(originalGroup);
      if(state.workspace.sheets.length){const editable=document.createElement('optgroup');editable.label='分析工作表 · 可编辑';state.workspace.sheets.forEach(s=>editable.append(new Option(s.name,'scratch:'+s.id)));select.append(editable)}
      appendCompositionSources(select);const oldSource=state.source;
      state.source=oldSource&&Array.from(select.options).some(x=>x.value===oldSource)?oldSource:select.options[Math.min(status.activeTab||0,select.options.length-1)]?.value||'';
      select.value=state.source;
      if(!state.query||state.query.source!==state.source)state.query=queryBase(state.source);
      state.headerRow=state.query.composition?0:state.query.headerRow||1;$('analysisHeaderRow').value=String(state.headerRow);
      setText('analysisSourceCaption',state.workspace.workbook||status.fileName||'');
      setTab(state.tab);await loadFields();if(state.activeResult&&state.opened){bindTreeResult();renderPivot();}
      if(state.tab==='sheets')renderSheets();
    }catch(err){if(ticket===state.openSerial)toast(err.message,'error')}
  }
  function invalidateSourceRequests(){++state.abortIndex;state.fieldsController?.abort();state.profileController?.abort();state.drillAbort?.abort();clusters?.reset();sourceRows.cancel();state.fieldsLoading=false;}
  function close(){state.openSerial=(state.openSerial||0)+1;++state.reportOpenSerial;invalidateSourceRequests();window.ExcelLensCompositionUI?.reset();sourceRows.reset();tree.reset();plans?.close();issuePanel?.close();if(pasteSpecial.dialog.open)pasteSpecial.dialog.close();analysisJobs.reset();state.sheetLoadSeq++;state.sheetAbortController?.abort();state.sheetAbortController=null;root.hidden=true;document.body.classList.remove('analysis-mode');$('analysisDrill').hidden=true;state.opened=false;window.ExcelLensChartExport?.revalidate();}
  function clearResultDisplay(){
    snapshots?.changed();if($('analysisSnapshotSave'))$('analysisSnapshotSave').disabled=true;
    state.chartCapture=null;window.ExcelLensChartExport?.revalidate();$('analysisChartImage').disabled=true;
    tree.reset();$('analysisTreeTools').hidden=true;
    $('analysisMetrics').hidden=true;$('analysisPivotTable').replaceChildren();charts?.draw($('analysisChart'),null);$('analysisChartHint').hidden=false;
    setText('analysisReportTitle','透视分析');setText('analysisReportSubtitle','从左侧拖入字段，或从模板库快速开始。');setText('analysisChartSummary','');setText('analysisTableSummary','尚未运行分析');
    for(const id of ['analysisSaveReport','analysisMaterialize','analysisMaterializeDetails','analysisExport'])$(id).disabled=true;
  }
  function bookChanged(key,book){
    state.currentBook=key||'';
    if(key===state.fingerprint&&(!book||book===state.book))return;
    state.compositionDraft=null;
    close();issuePanel?.reset();++state.abortIndex;state.profileController?.abort();
    snapshots?.changed();
    state.activeResult=null;state.activeResultQuery=null;state.jobID='';state.reportCache.clear();
  }
  async function refreshWorkspace(){
    if(!state.opened)return;
    const serial=state.workspaceRefreshSerial=(state.workspaceRefreshSerial||0)+1,opened=state.openSerial;
    const book=state.fingerprint,pathBook=state.book,workspace=await api('/api/analysis/workspace');
    if(!state.opened||serial!==state.workspaceRefreshSerial||opened!==state.openSerial||book!==state.fingerprint||pathBook!==state.book)return false;
    if(workspace.fingerprint!==book)throw Error('工作簿已切换，未载入其他来源的分析工作区');
    state.workspace=workspace;
    state.fingerprint=state.workspace.fingerprint||'';
    const old=state.source;const select=$('analysisSourceSelect');
    const original=state.workspace.sourceSheets||[];select.replaceChildren();const originalGroup=document.createElement('optgroup');originalGroup.label='原始 Excel · 只读';
    original.forEach(s=>originalGroup.append(new Option(s.name,'wb:'+s.name)));select.append(originalGroup);
    if(state.workspace.sheets.length){const grp=document.createElement('optgroup');grp.label='分析工作表 · 可编辑';state.workspace.sheets.forEach(s=>grp.append(new Option(s.name,'scratch:'+s.id)));select.append(grp)}
    appendCompositionSources(select);if(Array.from(select.options).some(x=>x.value===old)){select.value=old}else{state.source=select.options[0]?.value||'';state.query=queryBase(state.source);select.value=state.source}
    if(state.tab==='dashboard')renderDashboard();
    return true;
  }
  async function ensureIndex(sheet,current=()=>state.opened){
    const data=await post('/api/index/start',{sheet});if(data.ready)return;
    let deadline=Date.now()+120000;
    while(Date.now()<deadline&&current()){
      await sleep(350);
      const s=await api('/api/index/status?id='+encodeURIComponent(data.jobId));
      if(!current())throw Object.assign(Error('读取字段已取消'),{name:'AbortError'});
      setText('analysisSourceInfo',s.stage||'正在建立行索引…');
      if(s.status==='done')return;
      if(s.status==='error'||s.status==='cancelled')throw new Error(s.error||'建立索引未完成');
    }
    throw new Error('建立索引超时或已取消');
  }
  function renderSourceScope(){
    const combined=!!state.query?.composition;$('analysisSourceScope').disabled=combined;$('analysisHeaderLabel').hidden=combined;$('analysisPlansOpen').disabled=combined;
    $('analysisScopeLabel').hidden=combined;$('analysisScopeControls').hidden=combined;
    const mode=state.query?.sourceScope?.mode||'full';$('analysisSourceScope').value=mode;
    setText('analysisScopeInfo',combined?state.query.composition.inputs.length+' 个来源':sourceModel.caption(state.query?.sourceScope));
    $('analysisSourceRefresh').disabled=state.fieldsLoading||mode==='full';$('analysisSourceCancel').hidden=!state.fieldsLoading;
    for(const option of $('analysisSourceScope').options)if(option.value!=='full')option.disabled=!state.source.startsWith('wb:');
  }
  function fieldsKey(){return JSON.stringify([state.fingerprint,state.book,state.source,state.headerRow,state.query?.sourceScope||null]);}
  async function loadFields(options={}){
    if(!state.source)return false;
    invalidateSourceRequests();const stamp=state.abortIndex,book=state.fingerprint,pathBook=state.book,source=state.source,header=state.headerRow,controller=new AbortController();state.fieldsController=controller;
    const current=()=>stamp===state.abortIndex&&book===state.fingerprint&&pathBook===state.book&&source===state.source&&header===state.headerRow&&state.opened&&!controller.signal.aborted&&(!options.guard||options.guard());
    state.fieldsReady=false;state.fieldsLoading=true;setAnalysisBusy(!!analysisJobs.main);renderSourceScope();
    $('analysisFieldProfile').hidden=true;setText('analysisSourceInfo','正在读取表头…');
    let candidate;
    try{
      if(!options.mode&&state.query?.sourceScope&&source.startsWith('wb:')&&!sourceRows.matches(source,state.query.sourceScope))await ensureIndex(source.slice(3),current);
      candidate=await sourceRows.prepare({source,sourceScope:state.query?.sourceScope,composition:state.query?.composition,prepared:options.prepared,mode:options.mode},current);
      if(!current())return false;
      const url='/api/analysis/fields?'+new URLSearchParams({source,headerRow:String(header),...(candidate.id?{sourceId:candidate.id}:{})});
      let data;
      try{data=await api(url,{signal:controller.signal})}catch(e){if(e.status===409&&source.startsWith('wb:')&&!candidate.id&&current()){await ensureIndex(source.slice(3),current);data=await api(url,{signal:controller.signal})}else throw e}
      if(!current())return false;
      sourceRows.commit(candidate);
      if(candidate.sourceScope)state.query.sourceScope=JSON.parse(JSON.stringify(candidate.sourceScope));else delete state.query.sourceScope;
      if(candidate.composition)state.query.composition=JSON.parse(JSON.stringify(candidate.composition));else delete state.query.composition;
      state.fields=data.fields||[];state.preview=data.preview||[];
      // The backend owns numeric typing; visible strings cannot override a
      // native Excel text column or turn leading-zero identifiers into amounts.
      setText('analysisFieldCount',String(state.fields.length));setText('analysisSourceInfo',fmtNum(data.totalRows)+' 条源记录'+(data.truncatedFields?' · 仅显示前256列':''));
      state.query.source=state.source;state.query.headerRow=state.headerRow;
      state.loadedFieldsKey=fieldsKey();state.fieldsReady=true;renderFields();renderBuilder();return true;
    }catch(err){if(current()&&err.name!=='AbortError'){state.fieldsReady=state.loadedFieldsKey===fieldsKey();setText('analysisSourceInfo',err.message);toast(err.message,'error')}return false;}
    finally{sourceRows.release(candidate);if(current()){state.fieldsLoading=false;renderSourceScope();setAnalysisBusy(!!analysisJobs.main);}}
  }
  function allFields(){const additional=(state.query?.transforms||[]).map(t=>({id:t.id,name:t.name,numeric:['number','abs','multiply','divide','length'].includes(t.op)}));return [...state.fields,...additional]}
  function fieldSelect(value='',emptyLabel='选择字段',choices=allFields()){
    const el=document.createElement('select');el.className='analysis-control';el.append(new Option(emptyLabel,''));
    choices.forEach(f=>el.append(new Option(f.name,f.id)));el.value=value;return el;
  }
  function renderFields(){
    const list=$('analysisFields');list.replaceChildren();const q=$('analysisFieldSearch').value.trim().toLocaleLowerCase();
    const fragment=document.createDocumentFragment();
    for(const field of allFields()){
      if(q&&!String(field.name+' '+field.id).toLocaleLowerCase().includes(q))continue;
      const wrapper=document.createElement('div');wrapper.className='analysis-field-wrapper';
      const chip=document.createElement('button');chip.type='button';chip.className='analysis-field-chip';chip.draggable=true;
      chip.title='拖到行、列、汇总区域；单击按类型分配';chip.dataset.field=field.id;
      const type=document.createElement('span');type.className='analysis-type';type.textContent=field.numeric?'#':'Aa';
      const name=document.createElement('span');name.textContent=field.name;chip.append(type,name);
      chip.addEventListener('dragstart',e=>{e.dataTransfer.setData('text/plain',field.id);e.dataTransfer.effectAllowed='copyMove'});
      chip.addEventListener('click',()=>addField(field.id,field.numeric?'values':'rows'));
      wrapper.append(chip);
      if(!field.id.startsWith('D_')){
        const info=document.createElement('button');info.type='button';info.className='analysis-field-info-btn';info.title='查看字段画像与常见值';info.textContent='ⓘ';
        info.onclick=()=>loadFieldProfile(field);wrapper.append(info);
      }
      fragment.append(wrapper);
    }
    list.append(fragment);
  }
  async function loadFieldProfile(field){
    if(!state.fieldsReady||state.fieldsLoading)return;
    const host=$('analysisFieldProfile');host.hidden=false;host.textContent='正在分析 '+field.name+' …';
    state.profileController?.abort();const controller=new AbortController();state.profileController=controller;
    const source=state.source,header=state.headerRow,book=state.fingerprint,pathBook=state.book,scope=sourceModel.key(state.query?.sourceScope);
    const current=()=>state.opened&&!controller.signal.aborted&&state.profileController===controller&&book===state.fingerprint&&pathBook===state.book&&source===state.source&&header===state.headerRow&&scope===sourceModel.key(state.query?.sourceScope);
    try{
      const info=await api('/api/analysis/profile?'+new URLSearchParams({source,headerRow:String(header),field:field.id,...(sourceRows.binding?.id?{sourceId:sourceRows.binding.id}:{})}),{signal:controller.signal});
      if(!current())return;
      host.replaceChildren();
      const head=document.createElement('div');head.className='analysis-profile-head';const title=document.createElement('strong');title.textContent=field.name;
      const close=document.createElement('button');close.textContent='×';close.title='收起字段画像';close.onclick=()=>{controller.abort();host.hidden=true;};head.append(title,close);host.append(head);
      if(clusters&&!state.query?.composition&&(state.source.startsWith('scratch:')||JSON.parse($('fileFormats').textContent).sources[state.sourceFormat]?.textClustering)){
        const candidates=document.createElement('button');candidates.type='button';candidates.className='analysis-quiet-button';candidates.textContent='疑似重复';candidates.dataset.clusterField=field.id;candidates.addEventListener('click',()=>{if(current())clusters.open(field);});host.append(candidates);
      }
      const stats=document.createElement('div');stats.className='analysis-profile-stat';
      [['总行数',info.rows],['空值',info.empty],['唯一值'+(info.uniqueTruncated?'（至少）':''),info.unique],['数值',info.numeric]].forEach(([label,value])=>{
        const box=document.createElement('div'),top=document.createElement('strong'),bot=document.createElement('div');
        top.textContent=fmtNum(value);bot.textContent=label;box.append(top,bot);stats.append(box)
      });host.append(stats);
      if(info.minNumber!==''&&info.maxNumber!==''){
        const range=document.createElement('p');range.className='analysis-profile-range';range.textContent='数值范围：'+info.minNumber+' ～ '+info.maxNumber;host.append(range)
      }
      const caption=document.createElement('p');caption.className='analysis-profile-range';caption.textContent='常见值 · 点击即可作为筛选条件';host.append(caption);
      for(const item of info.frequent||[]){
        const row=document.createElement('div');row.className='analysis-profile-item';const select=document.createElement('button');select.type='button';select.textContent=item.value||'（空白）';select.title=item.value;
        select.onclick=()=>{if(!current())return;if(state.query.filters.length>=8){toast('最多8个筛选条件','warning');return}state.query.filters.push({field:field.id,op:item.value===''?'blank':'eq',value:item.value});renderRules();toast('已添加筛选：'+field.name,'success')};
        const count=document.createElement('span');count.textContent=fmtNum(item.count);row.append(select,count);host.append(row)
      }
      if(info.missingFormulaCaches){const warning=document.createElement('p');warning.textContent=fmtNum(info.missingFormulaCaches)+' 条公式缺少缓存值';host.append(warning)}
    }catch(err){if(current()&&err.name!=='AbortError'){host.textContent=err.message;toast(err.message,'error')}}
  }
  async function backupExport(){
    await saveChain.catch(()=>{});
    if(hasUnsaved()){toast('仍有未保存的分析内容，请先重试保存','warning');return;}
    try{
      await window.ExcelLensSession?.ready;
      const res=await fetch('/api/analysis/backup',{headers:window.ExcelLensSession?.apply(new Headers({'X-ExcelLens-Token':TOKEN}))||{'X-ExcelLens-Token':TOKEN}});
      if(!res.ok)throw new Error('获取备份失败');
      downloadBlob(await res.blob(),'ExcelLens-Analysis-Backup-'+new Date().toISOString().slice(0,10)+'.json');toast('已导出分析工作区备份','success');
    }catch(err){toast(err.message,'error')}
  }
  async function backupImport(file){
    if(!file)return;
    if(hasUnsaved()){toast('仍有未保存的分析内容，请先重试保存','warning');return;}
    if(file.size>41*1024*1024){toast('备份文件超过大小限制','warning');return}
    const answer=await askText('恢复分析工作区','','请输入“恢复”确认覆盖当前编辑表和已保存报表');
    if(answer!=='恢复')return;
    try{
      const raw=await file.text();const document=JSON.parse(raw);
      await post('/api/analysis/backup',document);
      state.reportCache.clear();state.sheetRevisions.clear();state.undoHistory.clear();state.gridData=null;state.activeResult=null;state.activeResultQuery=null;state.activeReportID='';state.loadedSheet=null;
      await refreshWorkspace();await loadFields();setTab('dashboard');toast('分析工作区已从备份恢复','success');
    }catch(err){toast('恢复失败：'+err.message,'error')}
  }
  function addField(id,zone){
    if(!allFields().some(f=>f.id===id))return;
    // Rows and Columns are mutually exclusive; Values may reuse a dimension.
    if(zone==='rows'||zone==='columns'){state.query.rows=state.query.rows.filter(x=>x!==id);state.query.columns=state.query.columns.filter(x=>x!==id)}
    if(zone==='rows'){if(state.query.rows.length>=3){toast('行维度最多支持3个','warning');return}state.query.rows.push(id)}
    if(zone==='columns'){state.query.columns=[id]}
    if(zone==='values'){
      if(state.query.metrics?.length){addMetric(id);return}
      state.query.value=id;if(state.query.aggregate==='count'&&allFields().find(f=>f.id===id)?.numeric)state.query.aggregate='sum';
      state.query.metrics=[{field:id,aggregate:state.query.aggregate||'count',name:''}];syncFirstMetric();
    }
    renderBuilder();
  }
  function renderBuilder(){
    if(!state.query)return;
    $('analysisLayout').value=state.query.layout||'';
    for(const zone of ['rows','columns','values']){
      const el=$('analysis'+zone[0].toUpperCase()+zone.slice(1)+'Zone');el.replaceChildren();
      const metrics=state.query.metrics||[];
      const ids=zone==='values'?(metrics.length?metrics.map(m=>m.field):(state.query.value?[state.query.value]:[])):state.query[zone];
      ids.forEach((id,index)=>{
        const chip=document.createElement('div');chip.className='analysis-selected-chip';chip.draggable=zone!=='values';
        const title=document.createElement('span');title.textContent=zone==='values'&&metrics.length?metricCaption(metrics[index]):fieldName(id);
        const remove=document.createElement('button');remove.type='button';remove.textContent='×';remove.title='移除字段';
        remove.addEventListener('click',()=>{
          if(zone==='values'){
            if(metrics.length){state.query.metrics.splice(index,1);syncFirstMetric()}else state.query.value='';
            if(!state.query.metrics?.length){state.query.value='';state.query.aggregate='count'}
          }else state.query[zone]=state.query[zone].filter(v=>v!==id);
          renderBuilder()
        });
        if(zone!=='values')chip.addEventListener('dragstart',e=>{e.dataTransfer.setData('text/plain',id);e.dataTransfer.effectAllowed='move'});
        chip.append(title,remove);el.append(chip);
      });
    }
    $('analysisAggregate').value=state.query.aggregate||'count';
    $('analysisAggregate').disabled=Boolean(state.query.metrics?.length>1);
    $('analysisSort').value=state.query.sort||'value_desc';$('analysisTop').value=String(state.query.top??30);$('analysisPrecision').value=String(state.query.precision??2);
    $('analysisChartSelect').value=state.query.chart||'bar';
    renderMetricEditor();renderRules();renderTransforms();
  }
  function renderRules(){
    const list=$('analysisFilterList');list.replaceChildren();
    state.query.filters.forEach((f,i)=>{
      const box=document.createElement('div');box.className='analysis-rule';const h=document.createElement('div');h.className='analysis-rule-header';
      const select=fieldSelect(f.field,'字段');select.onchange=()=>{f.field=select.value};h.append(select);
      const remove=document.createElement('button');remove.type='button';remove.className='analysis-rule-remove';remove.textContent='×';remove.onclick=()=>{state.query.filters.splice(i,1);renderRules()};h.append(remove);
      const controls=document.createElement('div');controls.className='analysis-rule-controls';const op=document.createElement('select');op.className='analysis-control';
      [['eq','等于'],['neq','不等于'],['contains','包含'],['notcontains','不包含'],['startswith','开头是'],['endswith','结尾是'],['gt','大于'],['gte','不小于'],['lt','小于'],['lte','不大于'],['between','在区间内'],['in','多值匹配'],['blank','为空'],['notblank','非空']].forEach(x=>op.append(new Option(x[1],x[0])));op.value=f.op;op.onchange=()=>{f.op=op.value};controls.append(op);
      const input=document.createElement('input');input.className='analysis-control';input.value=f.value||'';input.placeholder=f.op==='between'?'100..200 / 2026-01-01..2026-12-31':f.op==='in'?'债券|基金|股票':'匹配值';
      input.onchange=()=>f.value=input.value;input.oninput=()=>f.value=input.value;
      op.onchange=()=>{f.op=op.value;input.disabled=['blank','notblank'].includes(f.op);input.placeholder=f.op==='between'?'100..200 或 日期..日期':f.op==='in'?'多项用 | 分隔':'匹配值'};
      op.dispatchEvent(new Event('change'));controls.append(input);
      box.append(h,controls);list.append(box);
    });
  }
  function removeTransform(id){
    const removed=new Set([id]),q=state.query;
    for(const step of q.transforms)if(removed.has(step.source))removed.add(step.id);
    q.transforms=q.transforms.filter(t=>!removed.has(t.id));q.filters=q.filters.filter(f=>!removed.has(f.field));q.rows=q.rows.filter(id=>!removed.has(id));q.columns=q.columns.filter(id=>!removed.has(id));
    q.metrics=(q.metrics||[]).filter(m=>!removed.has(m.field));if(removed.has(q.value)){q.value='';q.aggregate='count';}syncFirstMetric();renderFields();renderBuilder();
  }
  function renderTransforms(){
    const list=$('analysisTransformList');list.replaceChildren();
    state.query.transforms.forEach((t,i)=>{
      const box=document.createElement('div');box.className='analysis-rule';
      const row=document.createElement('div');row.className='analysis-rule-header';const name=document.createElement('input');name.className='analysis-control';name.placeholder='新列名';name.value=t.name;name.oninput=()=>{t.name=name.value||'新列';renderFields()};row.append(name);
      const remove=document.createElement('button');remove.className='analysis-rule-remove';remove.textContent='×';remove.onclick=()=>removeTransform(t.id);row.append(remove);
      const temporal=['date','time','datetime'].includes(t.op);
      const operations=[['date','转日期'],['time','转时间'],['datetime','转日期时间'],['text','转文本'],['number','转数值'],['trim','去空格'],['upper','大写'],['lower','小写'],['year','提取年份'],['month','提取月份'],['yearmonth','提取年月'],['abs','绝对值'],['multiply','乘常数'],['divide','除常数'],['prefix','添加前缀'],['suffix','添加后缀'],['replace','替换文本'],['length','文本长度']];
      if(t.op==='map_text')operations.push(['map_text','文本映射']);
      const ops=document.createElement('select');ops.className='analysis-control';operations.forEach(x=>ops.append(new Option(x[1],x[0])));ops.value=t.op;ops.onchange=()=>{t.op=ops.value;delete t.mapping;t.arg=['date','time','datetime'].includes(t.op)?'auto':'';renderFields();renderTransforms()};
      const source=fieldSelect(t.source,'来源列',state.fields.concat(state.query.transforms.slice(0,i).map(t=>({id:t.id,name:t.name}))));source.onchange=()=>{t.source=source.value};
      const input=document.createElement(temporal?'select':'input');input.className='analysis-control';input.setAttribute('aria-label',temporal?'日期或时间的输入格式':'转换参数');
      if(temporal){[['auto','年在前 / 时间（自动识别）'],['compact','年月日紧排：20261111'],['dmy','日/月/年：11/12/2026'],['mdy','月/日/年：11/12/2026']].forEach(x=>input.append(new Option(x[1],x[0])));input.value=t.arg||'auto';}
      else{input.placeholder='常数 / 查找=>替换';input.value=t.arg||'';input.hidden=['text','number','trim','upper','lower','year','month','yearmonth','abs','length','map_text'].includes(t.op);}
      input.onchange=()=>t.arg=input.value;input.oninput=()=>t.arg=input.value;
      const controls=document.createElement('div');controls.className='analysis-rule-controls';controls.append(source,ops);box.append(row,controls,input);input.style.marginTop='7px';list.append(box);
      if(temporal){const note=document.createElement('small');note.className='analysis-conversion-note';note.textContent='运行分析时转换；无效或含糊的值保留原文并计数。';box.append(note);}
      if(t.op==='map_text'){const detail=document.createElement('details');detail.className='analysis-mapping-details';const summary=document.createElement('summary');summary.textContent=(t.mapping||[]).length+' 条精确文本映射 · 查看原文';const content=document.createElement('pre');detail.append(summary,content);detail.addEventListener('toggle',()=>{if(detail.open)content.textContent=(t.mapping||[]).map(entry=>JSON.stringify(entry.from)+' → '+JSON.stringify(entry.to)).join('\n');});box.append(detail);}
    });
  }
  function setAnalysisBusy(busy){
    snapshots?.changed();if($('analysisSnapshotSave'))$('analysisSnapshotSave').disabled=busy||!state.chartCapture?.current();
    state.chartBusy=busy;$('analysisChartImage').disabled=busy||!state.chartCapture?.current();window.ExcelLensChartExport?.revalidate();
    tree.setBlocked(busy||state.tab!=='pivot'||state.reportPending===state.reportOpenSerial);
    if(issuePanel)$('analysisIssuesOpen').disabled=busy;
    $('analysisProgress').hidden=!busy;$('analysisCancel').hidden=!busy;$('analysisRun').disabled=busy||state.fieldsLoading||!state.fieldsReady;
    if(busy){setText('analysisProgressText','正在准备分析…');setText('analysisProgressPercent','');$('analysisProgressBar').style.width='0%';}
    const ready=!busy&&state.reportPending!==state.reportOpenSerial&&!!state.activeResult;
    for(const id of ['analysisSaveReport','analysisMaterialize','analysisMaterializeDetails','analysisExport'])$(id).disabled=!ready;
  }
  function showAnalysisProgress(status){
    const percent=status.total?Math.min(99,Math.round(100*status.progress/status.total)):0;
    setText('analysisProgressText',status.stage||'正在分析…');
    setText('analysisProgressPercent',status.total?percent+'%':fmtNum(status.progress)+' 行');
    $('analysisProgressBar').style.width=percent+'%';
  }
  async function executeQuery(q,options={}){
    const request=options.main?sourceRows.query(q):q;
    return analysisJobs.execute(request,options);
  }
  async function run(){
    ++state.reportOpenSerial;
    if(!state.fieldsReady||state.fieldsLoading||!state.fields.length){toast('请先成功读取所选范围的字段','warning');return}
    const q=currentQuery();
    if(q.layout==='tree'&&!q.rows.length){toast('层级展开需要至少一个行维度','warning');return;}
    if(effectiveMetrics(q).some(m=>m.aggregate!=='count'&&!m.field)){toast('数值指标必须选择字段','warning');return}
    if(!q.rows.length&&!q.columns.length&&effectiveMetrics(q).every(m=>m.aggregate==='count'&&!m.field)){toast('请选择至少一个分类字段','warning');return}
    try{
      setTab('pivot');const data=await executeQuery(q,{main:true});
      if(!data.current())return;
      activateResult(state.reportCache.set(state.fingerprint,data.query||q,data));
      renderResult(data.result,state.activeResultQuery);
    }catch(err){if(err.name!=='AbortError')toast(err.message,'error');}
  }
  function activateResult(snapshot){
    if(!snapshot||snapshot.book!==state.fingerprint)return false;
    state.drillAbort?.abort();$('analysisDrill').hidden=true;
    state.activeResult=snapshot.result;state.activeResultQuery=restoreReportQuery(snapshot.query);state.jobID=snapshot.job;
    state.activeResultFields=(snapshot.sourceFields||state.fields||[]).map(field=>({...field}));
    window.ExcelLensChartExport?.revalidate();bindTreeResult();
    return true;
  }
  function bindTreeResult(){tree.setRoot({bookKey:state.fingerprint,book:state.book,job:state.jobID,query:state.activeResultQuery,result:state.activeResult});}
  async function adoptAnalysisPlan(completed,preview,current){
    if(!current()||!state.opened)return false;
    const book=state.fingerprint,serial=state.reportOpenSerial,status=completed.result,q=restoreReportQuery(status.query);
    if(!status.result||!q.source||q.source!==preview.ref.source)throw Error('方案结果与目标来源不一致');
    // Prepare the complete builder schema before replacing the active result.
    // Errors, cancellation and edits after completion leave the old UI intact.
    const guard=()=>current()&&completed.current()&&book===state.fingerprint&&serial===state.reportOpenSerial&&state.opened;
    const candidate=await sourceRows.prepare({source:q.source,sourceScope:q.sourceScope},guard);
    let schema;
    try{
      schema=await api('/api/analysis/fields?'+new URLSearchParams({source:q.source,headerRow:String(q.headerRow),stamp:'1',...(candidate.id?{sourceId:candidate.id}:{})}));
      if(!guard())return false;
      if(schema.sourceStamp!==preview.stamp)throw Error('目标表在分析后发生变化，请重新核对字段；当前结果保留');
      sourceRows.commit(candidate);
    }finally{sourceRows.release(candidate);}
    ++state.reportOpenSerial;++state.abortIndex;analysisJobs.cancelMain();state.fieldsController?.abort();state.profileController?.abort();state.fieldsLoading=false;state.fieldsReady=true;
    state.source=q.source;state.headerRow=q.headerRow;state.query=q;state.fields=schema.fields||[];state.preview=schema.preview||[];state.activeReportID='';
    $('analysisSourceSelect').value=state.source;$('analysisHeaderRow').value=String(state.headerRow);
    setText('analysisFieldCount',String(state.fields.length));setText('analysisSourceInfo',fmtNum(schema.totalRows)+' 条源记录'+(schema.truncatedFields?' · 仅显示前256列':''));$('analysisFieldProfile').hidden=true;
    activateResult(state.reportCache.set(book,q,{job:completed.job,result:status.result,sourceFields:status.sourceFields}));
    state.loadedFieldsKey=fieldsKey();$('analysisExportFormat').value=status.exportFormat||'xlsx';setTab('pivot');renderSourceScope();renderFields();renderBuilder();renderResult(status.result,state.activeResultQuery);setAnalysisBusy(false);
    return true;
  }
  function resultFieldName(id){
    if(!id)return '记录数';
    return state.activeResultQuery?.transforms?.find(field=>field.id===id)?.name||state.activeResultFields?.find(field=>field.id===id)?.name||id;
  }
  function metricCell(result,row,columnIndex,metricIndex,total=false){return window.ExcelLensAnalysisResults.metricCell(result,row,columnIndex,metricIndex,total);}
  function projectMetric(result,index){return window.ExcelLensAnalysisResults.projectMetric(result,index);}
  function openResultImage(snapshot,canvas,expectedResult,mode,index,title,current){
    const q=snapshot.query,fields=snapshot.sourceFields||[],lookup=id=>q.transforms?.find(f=>f.id===id)?.name||fields.find(f=>f.id===id)?.name||id||'记录数';
    const metric=effectiveMetrics(q)[index],percent=expectedResult.rows?.some(row=>row.values?.some(cell=>cell?.comparison?.percent));
    const sourceName=q.composition?.name||(q.source.startsWith('wb:')?q.source.slice(3):state.workspace?.sheets?.find(sheet=>'scratch:'+sheet.id===q.source)?.name||'分析工作表');
    window.ExcelLensChartExport?.open({kind:'analysis',canvas,expectedResult,mode,metricIndex:index,result:snapshot.result,query:q,fields,
      origin:{workbookKey:state.fingerprint,book:state.book,job:snapshot.job,source:q.source},title,
      source:String(state.workspace?.workbook||$('analysisSourceCaption').textContent||'当前工作簿')+' / '+sourceName,
      scope:queryScope(q)+(q.filters?.length?' · 已筛选':''),
      metric:metricLabel(metric,lookup),unit:percent?'百分比':metric.aggregate==='count'||metric.aggregate==='distinct'?'个':'源指标原单位（未换算）',current,notify:toast});
  }
  function renderMetrics(result,q){
    const stats=effectiveMetrics(q).slice(0,6).map((m,i)=>{
      const cell=metricCell(result,null,0,i,true);return [resultMetricCaption(m),numbers.format(cell),(result.tree?'本层范围汇总':'全量汇总')+' · '+(i+1),window.ExcelLensAnalysisComparisons.title(cell)];
    });
    stats.push(['参与记录',fmtNum(result.included),'实际纳入统计',String(result.included)],[result.tree?'首层分类':'分类数',fmtNum(result.rows?.length??0),'透视结果行',String(result.rows?.length??0)]);
    const el=$('analysisMetrics');el.replaceChildren();
    stats.forEach(([name,value,note,exact])=>{
      const box=document.createElement('div');box.className='analysis-metric';
      box.innerHTML=`<div class="analysis-metric-label">${esc(name)}</div><div class="analysis-metric-value" title="${esc(exact===undefined?value:exact)}">${esc(value)}</div><div class="analysis-metric-note">${esc(note)}</div>`;
      el.append(box);
    });el.hidden=false;
  }
  function renderResult(result,q){
    if(!result)return;
    issuePanel?.setSnapshot({book:state.fingerprint,job:state.jobID,query:q,result});
    const measures=effectiveMetrics(q);
    setText('analysisReportTitle',q.title||'透视分析');
    setText('analysisReportSubtitle',queryScope(q)+' · '+(result.tree?'层级展开 · ':'')+(q.rows.map(resultFieldName).join(' / ')||'全部记录')+' · '+measures.map(resultMetricCaption).join(' / '));
    renderMetrics(result,q);
    const alerts=[];if(result.invalidNumbers)alerts.push(`${fmtNum(result.invalidNumbers)} 个非数值参与字段未计入数值聚合`);
    if(result.invalidConversions)alerts.push(`扫描中有 ${fmtNum(result.invalidConversions)} 个值转换失败，转换列保留原文；请检查输入格式`);
    if(result.missingFormulaCaches)alerts.push(`${fmtNum(result.missingFormulaCaches)} 个公式缺少缓存值，已排除`);
    if(result.truncated)alerts.push('仅显示排名前 N 项，合计仍为全量结果');
    const comparisonScope=window.ExcelLensAnalysisComparisons.scope(q);if(comparisonScope)alerts.push(comparisonScope);
    if(result.rows?.some(row=>row.values?.some(c=>/^[+-]?\d{16,}(?:\.|$)/.test(String(c?.exact??'')))))alerts.push('图表按屏幕像素近似绘制；透视表及导出保留精确数值');
    setText('analysisChartSummary',alerts.join('；')||'点击图表或透视数值查看来源记录');
    setText('analysisTableSummary',fmtNum(result.rows?.length||0)+' 个分类 · '+fmtNum(result.included)+' 条记录');
    $('analysisSaveReport').disabled=false;$('analysisMaterialize').disabled=false;$('analysisMaterializeDetails').disabled=false;$('analysisExport').disabled=false;
    $('analysisSaveReport').textContent=state.activeReportID?'更新报表':'保存报表';
    $('analysisMaterialize').textContent=result.tree?'首层编辑表':'生成编辑表';$('analysisExport').textContent=result.tree?'导出首层':'导出结果';
    setText('analysisTableHint',result.tree?'箭头展开分组，数值查看来源明细':'点击数值查看对应明细 ↗');
    const chartMetric=$('analysisChartMetric'),chartLabel=$('analysisChartMetricLabel');chartMetric.replaceChildren();
    measures.forEach((m,i)=>chartMetric.add(new Option(resultMetricCaption(m),String(i))));
    chartMetric.value=String(Math.min(measures.length-1,Math.max(0,Number(q.chartMetric)||0)));
    chartMetric.hidden=chartLabel.hidden=measures.length<=1;
    $('analysisChartHint').hidden=Boolean(result.rows?.length);
    renderPivot();renderChart();
    if(result.invalidNumbers||result.missingFormulaCaches||result.invalidConversions)toast('本次分析存在需核对项目，请查看图表上方提示','warning');
  }
  function renderPivot(rowLimit=150){
    const result=state.activeResult,q=state.activeResultQuery,table=$('analysisPivotTable');if(!result||!q){table.replaceChildren();return;}
    state.pivotLimit=rowLimit;
    $('analysisTreeTools').hidden=!result.tree;
    if(result.tree){
      bindTreeResult();const page=treeRenderer.render(table,tree,{fieldName:resultFieldName,metricName:resultMetricCaption,metricCell,drill,formatNumber:numbers.format});
      $('analysisTreePrevious').disabled=tree.page===0;$('analysisTreeNext').disabled=!page.more;
      setText('analysisTreePage',page.last?`${fmtNum(page.first)}–${fmtNum(page.last)} 行`:'暂无分组');
      setText('analysisTableSummary',`${fmtNum(result.rows?.length)} 个首层分组 · ${fmtNum(result.included)} 条记录`);return;
    }
    table.replaceChildren();
    table.removeAttribute('role');table.removeAttribute('aria-label');
    const measures=effectiveMetrics(q),withColumns=Boolean(q.columns?.length),dimCount=Math.max(1,q.rows?.length||0);
    const headers=[...(q.rows?.length?q.rows.map(resultFieldName):['分类'])];
    if(withColumns){
      (result.columnKeys||[]).forEach(k=>measures.forEach(m=>headers.push((k||'（空白）')+' · '+resultMetricCaption(m))));
      measures.forEach(m=>headers.push('合计 · '+resultMetricCaption(m)));
    }else measures.forEach(m=>headers.push(resultMetricCaption(m)));
    const head=document.createElement('thead'),tr=document.createElement('tr');
    headers.forEach(name=>{const th=document.createElement('th');th.textContent=name;tr.append(th)});head.append(tr);table.append(head);
    const body=document.createElement('tbody');const visible=(result.rows||[]).slice(0,rowLimit);
    const addValue=(tr,cell,keys,colKey,clickable)=>{
      const td=document.createElement('td');td.className='analysis-number';td.title=window.ExcelLensAnalysisComparisons.title(cell);
      if(clickable&&cell?.exact!==undefined&&cell.exact!==''){
        const btn=document.createElement('button');btn.type='button';btn.className='analysis-drill-link';btn.textContent=numbers.format(cell);
        btn.title=td.title+' · 查看当前分组来源明细';btn.onclick=()=>drill(keys,colKey,0);td.append(btn)
      }else td.textContent=numbers.format(cell);tr.append(td)
    };
    visible.forEach(row=>{
      const tr=document.createElement('tr');
      const keys=q.rows?.length?row.keys:['全部'];
      keys.forEach(k=>{const td=document.createElement('td');td.textContent=k||'（空白）';tr.append(td)});
      if(withColumns)(result.columnKeys||[]).forEach((colKey,j)=>measures.forEach((_,k)=>addValue(tr,metricCell(result,row,j,k),row.keys,colKey,true)));
      measures.forEach((_,k)=>addValue(tr,metricCell(result,row,0,k,true),row.keys,'',!withColumns));
      body.append(tr)
    });table.append(body);
    const foot=document.createElement('tfoot'),footer=document.createElement('tr');
    for(let i=0;i<dimCount;i++){const td=document.createElement('td');td.textContent=i===0?'全量总计':'';footer.append(td)}
    if(withColumns)(result.columnKeys||[]).forEach((_,j)=>measures.forEach((_,k)=>addValue(footer,metricCell(result,null,j,k),[],'',false)));
    measures.forEach((_,k)=>addValue(footer,metricCell(result,null,0,k,true),[],'',false));
    foot.append(footer);table.append(foot);
    if(result.rows.length>rowLimit){const extra=document.createElement('tr'),cell=document.createElement('td');cell.colSpan=headers.length;
      const button=document.createElement('button');button.type='button';button.className='analysis-quiet-button';button.textContent=`继续显示（还有 ${fmtNum(result.rows.length-rowLimit)} 行）`;
      button.onclick=()=>renderPivot(Math.min(result.rows.length,rowLimit+150));cell.append(button);extra.append(cell);body.append(extra)
    }
  }
  function renderChart(){
    if(!state.opened||state.tab!=='pivot'||!state.activeResult)return;
    const result=state.activeResult,q=state.activeResultQuery,book=state.fingerprint,path=state.book,opened=state.openSerial,job=state.jobID;
    const metric=Math.max(0,Math.min(effectiveMetrics(q).length-1,Number($('analysisChartMetric').value)||0)),mode=$('analysisChartSelect').value;
    const current=()=>state.opened&&state.tab==='pivot'&&state.activeResult===result&&state.activeResultQuery===q&&state.fingerprint===book&&state.book===path&&state.openSerial===opened&&state.jobID===job&&!state.chartBusy&&state.reportPending!==state.reportOpenSerial&&$('analysisChartSelect').value===mode&&(Number($('analysisChartMetric').value)||0)===metric;
    state.chartCapture=null;$('analysisChartImage').disabled=true;if($('analysisSnapshotSave'))$('analysisSnapshotSave').disabled=true;window.ExcelLensChartExport?.revalidate();
    requestAnimationFrame(()=>{
      if(!current())return;const projected=projectMetric(result,metric),canvas=$('analysisChart');
      charts?.draw(canvas,projected,mode,(keys,colKey)=>drill(keys,colKey,0,Boolean(q.columns?.length)));
      state.chartCapture={current,snapshot:{book,query:q,result,job,sourceFields:state.activeResultFields},canvas,projected,mode,metric};$('analysisChartImage').disabled=false;
      if($('analysisSnapshotSave'))$('analysisSnapshotSave').disabled=false;snapshots?.changed();
    });
  }
  function exportChartImage(){const c=state.chartCapture;if(c?.current())openResultImage(c.snapshot,c.canvas,c.projected,c.mode,c.metric,c.snapshot.query.title||'透视分析',c.current);}
  function setChartMode(){const mode=$('analysisChartSelect').value;if(state.query)state.query.chart=mode;if(state.activeResultQuery)state.activeResultQuery.chart=mode;renderChart();}
  async function drill(keys,colKey,offset=0,columnSelected){
    const q=state.activeResultQuery;if(!q)return;
    state.drillAbort?.abort();const controller=new AbortController();state.drillAbort=controller;
    const jobId=state.jobID,book=state.fingerprint,pathBook=state.book;
    const current=()=>state.opened&&!controller.signal.aborted&&state.drillAbort===controller&&state.activeResultQuery===q&&state.jobID===jobId&&book===state.fingerprint&&pathBook===state.book;
    const drawer=$('analysisDrill');drawer.hidden=false;
    state.drillQuery=q;state.drillJob=jobId;state.drillKeys=[...keys];state.drillColumn=colKey;state.drillColumnSelected=columnSelected;state.drillOffset=offset;
    setText('analysisDrillTitle',(keys.join(' / ')||'全部记录')+(colKey?' · '+colKey:''));setText('analysisDrillInfo','正在读取源数据…');
    setText('analysisDrillTable','');setText('analysisDrillPaging','');
    try{
      const data=await api('/api/analysis/drill',{method:'POST',body:JSON.stringify({query:q,jobId,keys,columnKey:colKey,...(columnSelected!==undefined?{columnSelected}:{}),offset,limit:100}),signal:controller.signal});
      if(!current()||drawer.hidden)return;
      setText('analysisDrillInfo',`已扫描 ${fmtNum(data.scanned)} 行 · `+((data.rows?.length||0)?`显示第 ${offset+1}–${offset+data.rows.length} 条`:'没有匹配明细'));
      const wrap=$('analysisDrillTable');const table=document.createElement('table');const head=document.createElement('thead');const tr=document.createElement('tr');
      for(const title of [...(q.composition?['来源文件','来源表']:[]),'原行号',...(data.fields||[]).map(x=>x.name)]){const th=document.createElement('th');th.textContent=title;tr.append(th)}head.append(tr);table.append(head);
      const body=document.createElement('tbody');(data.rows||[]).forEach(row=>{
        const tr=document.createElement('tr');const pos=document.createElement('td'),position=document.createElement('button');position.type='button';position.className='analysis-source-row analysis-drill-link';position.textContent=String(row.row);position.title='转到来源行';pos.append(position);
        if(q.composition)for(const text of [row.origin?.file,row.origin?.sheet]){const td=document.createElement('td');td.className='composition-origin';td.textContent=text||'';tr.append(td);}
        position.onclick=()=>{if(q.composition)navigateComposition(row,row.origin?.columns.find(column=>column>0),jobId);else if(q.source.startsWith('wb:')){close();window.dispatchEvent(new CustomEvent('excellens:analysis-navigate',{detail:{sheet:q.source.slice(3),ref:'A'+row.row}}))}else navigateSheet(q.source.slice(8),row.row,1).catch(error=>toast(error.message,'error'));};tr.append(pos);
        row.cells.forEach((v,i)=>{const td=document.createElement('td');td.title=v;if(q.composition&&row.origin?.columns[data.fields[i].column-1]>0){const jump=document.createElement('button');jump.type='button';jump.className='analysis-source-row analysis-drill-link';jump.textContent=v;jump.onclick=()=>navigateComposition(row,row.origin.columns[data.fields[i].column-1],jobId);td.append(jump);}else td.textContent=v;tr.append(td);});body.append(tr)
      });table.append(body);wrap.replaceChildren(table);
      const paging=$('analysisDrillPaging');const prev=document.createElement('button');prev.textContent='上一页';prev.disabled=offset===0;prev.onclick=()=>drill(keys,colKey,Math.max(0,offset-100),columnSelected);paging.append(prev);
      const next=document.createElement('button');next.textContent='下一页';next.disabled=!data.hasMore;next.onclick=()=>drill(keys,colKey,offset+100,columnSelected);paging.append(next);
    }catch(err){if(current()&&!drawer.hidden&&err.name!=='AbortError'){setText('analysisDrillInfo',err.message);toast('下钻失败：'+err.message,'error')}}
  }
  async function navigateComposition(record,column,job=state.jobID){
    const origin=record.origin;if(!origin||!column)return;const identity=JSON.stringify([state.fingerprint,state.book,state.opened]),foreign=origin.book!==state.book;
    const popup=foreign?window.open('about:blank','_blank'):null;if(popup)popup.opener=null;let handled=false;
    try{
      const target=await post('/api/analysis/composition/navigate',{jobId:job,input:origin.input,book:origin.book,row:record.row,col:column});
      if(identity!==JSON.stringify([state.fingerprint,state.book,state.opened]))return;
      if(target.sameBook){popup?.close();handled=true;if(target.kind==='analysis')await navigateSheet(target.analysisSheet,target.row,target.col);else{close();window.dispatchEvent(new CustomEvent('excellens:analysis-navigate',{detail:{sheet:target.sheet,ref:window.ExcelLensCompositionModel.label(target.col)+target.row}}));}}
      else if(popup){popup.location.assign(target.url);handled=true;}else toast('请允许打开来源窗口后重试','warning');
    }catch(error){if(identity===JSON.stringify([state.fingerprint,state.book,state.opened]))toast(error.message,'error');}
    finally{if(popup&&!handled)popup.close();}
  }
  function askText(title,initial='',hint='请输入名称'){
    return new Promise(resolve=>{
      const shade=document.createElement('div');shade.className='analysis-modal-shade';shade.innerHTML=`<div class="analysis-modal" role="dialog" aria-modal="true"><h3>${esc(title)}</h3><input class="analysis-control" maxlength="80" placeholder="${esc(hint)}"><div><button data-no type="button" class="analysis-outline-button">取消</button><button data-yes type="button" class="analysis-primary-button">确定</button></div></div>`;
      shade.style.cssText='position:absolute;inset:0;z-index:12;background:#1c2c3950;display:grid;place-items:center';
      const box=shade.firstChild;box.style.cssText='border:1px solid var(--reading-line,#e2e9ed);box-shadow:0 20px 50px #1b3f4750;border-radius:12px;padding:22px;width:min(360px,86vw)';
      const h=box.querySelector('h3');h.style.margin='0 0 16px';box.querySelector('div').style.cssText='display:flex;justify-content:flex-end;gap:9px;margin-top:17px';
      const input=shade.querySelector('input');input.value=initial;
      const done=v=>{shade.remove();resolve(v)};shade.querySelector('[data-no]').onclick=()=>done(null);shade.querySelector('[data-yes]').onclick=()=>done(input.value.trim());
      shade.addEventListener('keydown',e=>{if(e.key==='Escape')done(null);if(e.key==='Enter')done(input.value.trim())});root.append(shade);input.focus();input.select();
    });
  }
  async function saveReport(){
    if(!state.activeResultQuery||!state.activeResult)return;
    const snapshot={book:state.fingerprint,query:JSON.parse(JSON.stringify(state.activeResultQuery)),job:state.jobID,result:state.activeResult,sourceFields:state.activeResultFields};
    const pathBook=state.book,opened=state.openSerial,reportID=state.activeReportID||'';
    const current=()=>state.opened&&snapshot.book===state.fingerprint&&pathBook===state.book&&opened===state.openSerial;
    const old=state.workspace?.reports?.find(x=>x.id===state.activeReportID);
    const name=await askText(old?'更新分析报表':'保存分析报表',old?.name||state.activeResultQuery.title||'我的数据分析','例如：机构资产规模分布');if(!name)return;
    if(!current())return;
    try{
      const data=await post('/api/analysis/report',{id:reportID,name,query:snapshot.query,workbookKey:snapshot.book,book:pathBook});
      if(!current())return;
      if(state.activeResult===snapshot.result&&state.jobID===snapshot.job&&state.activeReportID===reportID){state.activeReportID=data.id;$('analysisSaveReport').textContent='更新报表';}
      // The dashboard can become visible while its catalogue refresh is in
      // flight. Cache the completed result before that refresh adopts/renders.
      state.reportCache.set(snapshot.book,snapshot.query,snapshot);
      try{await refreshWorkspace();}catch(e){if(current())toast('报表已保存，列表刷新失败：'+e.message,'warning');return;}
      if(current())toast(old?'已更新已保存的报表':'已保存到仪表盘 · '+name,'success');
    }catch(e){if(current())toast(e.message,'error')}
  }
  async function materialize(){
    if(!state.jobID||!state.activeResult)return;
    const job=state.jobID,book=state.fingerprint;
    const name=await askText(state.activeResult.tree?'创建首层结果表':'创建可编辑结果表',(state.activeResult.tree?'首层结果 ':'透视结果 ')+new Date().toLocaleDateString('zh-CN'));if(!name)return;
    if(book!==state.fingerprint)return;
    try{const data=await post('/api/analysis/materialize',{name,jobId:job});await refreshWorkspace();state.loadedSheet=data.id;state.sheetPage=0;setTab('sheets');toast('新工作表已保存，可编辑且不会修改源 Excel','success')}catch(e){toast(e.status===404?'结果任务已过期，请重新运行分析后保存':e.message,'error')}
  }
  async function materializeDetails(groupOnly=false){
    const q=groupOnly?state.drillQuery:(state.activeResultQuery||currentQuery());
    if(!q){toast('请先配置分析条件','warning');return}
    const keys=groupOnly?[...state.drillKeys]:null,jobId=groupOnly?state.drillJob:state.jobID,columnKey=groupOnly?state.drillColumn:'',columnSelected=groupOnly?state.drillColumnSelected:undefined,book=state.fingerprint,pathBook=state.book;
    const initial=groupOnly?'下钻明细 '+new Date().toLocaleDateString('zh-CN'):'清洗明细 '+new Date().toLocaleDateString('zh-CN');
    const name=await askText('提取可编辑明细',initial,'最多10,000行，保留源行号');if(!name)return;
    if(!state.opened||book!==state.fingerprint||pathBook!==state.book)return;
    const btn=$(groupOnly?'analysisDrillSave':'analysisMaterializeDetails');btn.disabled=true;
    setText('analysisAutoSave','正在提取明细…');
    try{
      const response=await post('/api/analysis/detail/materialize',{query:q,jobId,keys,columnKey,...(columnSelected!==undefined?{columnSelected}:{}),name,limit:10000});
      if(!state.opened||book!==state.fingerprint||pathBook!==state.book)return;
      await refreshWorkspace();state.loadedSheet=response.id;state.sheetPage=0;
      $('analysisDrill').hidden=true;setTab('sheets');setText('analysisAutoSave','已保存');
      toast(`已提取 ${fmtNum(response.rows)} 行`+(response.truncated?'；已达到1万行上限，请增加筛选条件':'')+'，原工作簿未修改',response.truncated?'warning':'success');
    }catch(e){setText('analysisAutoSave','提取失败');toast(e.message,'error')}
    finally{btn.disabled=false}
  }
  function downloadBlob(blob,name){
    const url=URL.createObjectURL(blob),a=document.createElement('a');a.href=url;a.download=name;
    document.body.append(a);a.click();a.remove();setTimeout(()=>URL.revokeObjectURL(url),5000);
  }
  async function exportCSV(){
    if(!state.jobID)return;
    const job=state.jobID,book=state.fingerprint;
    const format=$('analysisExportFormat').value==='csv'?'csv':'xlsx';
    try{
      await window.ExcelLensSession?.ready;
      if(book!==state.fingerprint)return;
      const res=await fetch('/api/analysis/export?id='+encodeURIComponent(job)+'&format='+format,{headers:window.ExcelLensSession?.apply(new Headers({'X-ExcelLens-Token':TOKEN}))||{'X-ExcelLens-Token':TOKEN}});
      if(res.status===404)throw new Error('结果任务已过期，请重新运行分析后导出');
      if(!res.ok){const data=await res.json().catch(()=>({}));throw new Error(data.error||'导出失败')}
      const blob=await res.blob();if(book!==state.fingerprint)return;
      downloadBlob(blob,'ExcelLens-Analysis.'+format);toast('分析结果已导出','success');
    }catch(e){toast(e.message,'error')}
  }
  function restoreReportQuery(query){
    const q=JSON.parse(JSON.stringify(query));
    for(const key of ['rows','columns','metrics','filters','transforms'])if(!Array.isArray(q[key]))q[key]=[];
    // Go omits zero-valued scalar settings. A saved precision/top of zero
    // must survive restoration instead of adopting new-query defaults.
    q.top??=0;q.precision??=0;q.headerRow=q.composition?0:q.headerRow||1;q.chart||='bar';q.sort||='value_desc';
    return q;
  }
  async function openReport(report){
    const serial=++state.reportOpenSerial,book=state.fingerprint;
    analysisJobs.cancelMain();state.reportPending=serial;setAnalysisBusy(false);
    state.source=report.query.source;state.query=restoreReportQuery(report.query);state.headerRow=state.query.headerRow;state.activeReportID='';
    $('analysisSourceSelect').value=state.source;$('analysisHeaderRow').value=state.headerRow;
    setTab('pivot');const loaded=await loadFields();
    if(serial!==state.reportOpenSerial||book!==state.fingerprint||!state.opened)return;
    state.reportPending=0;if(loaded===false){setAnalysisBusy(false);return;}
    state.activeReportID=report.id;
    const cached=state.reportCache.get(book,report.query);
    if(cached&&activateResult(cached)){renderResult(cached.result,state.activeResultQuery);setAnalysisBusy(false);}else await run();
  }
  async function renderDashboard(){
    if(!state.opened)return;
    const book=state.fingerprint,pathBook=state.book,opened=state.openSerial;
    const host=$('analysisDashboardCards');host.replaceChildren();window.ExcelLensChartExport?.revalidate();
    const reports=(state.workspace?.reports||[]).slice().reverse();
    const savedOrder=state.workspace?.dashboardOrder||[];
    if(savedOrder.length){
      reports.sort((a,b)=>{const ai=savedOrder.indexOf(a.id),bi=savedOrder.indexOf(b.id);return ai<0&&bi<0?0:ai<0?1:bi<0?-1:ai-bi});
    }
    // Render all report shells, but only query charts as they approach the
    // viewport. A large dashboard must not sequentially rescan 80 workbooks.
    if(!reports.length){const el=document.createElement('div');el.className='analysis-chart-card';el.style.cssText='padding:45px;color:#80909a;grid-column:1/-1;text-align:center';el.textContent='这里还没有报表。先完成一次透视分析，点击「保存报表」后即可在仪表盘中查看。';host.append(el);return}
    const contentScroll=root.querySelector('.analysis-main');
    const lazyObserver=('IntersectionObserver' in window)?new IntersectionObserver(entries=>{
      for(const entry of entries){if(entry.isIntersecting){lazyObserver.unobserve(entry.target);entry.target.__loadAnalysisCard?.()}}
    },{root:contentScroll,rootMargin:'200px'}):null;
    for(const report of reports){
      if(!state.opened||state.tab!=='dashboard')return;
      const card=document.createElement('div');card.className='analysis-dashboard-card';
      card.dataset.reportId=report.id;card.draggable=true;card.title='拖动调整仪表盘顺序';
      card.innerHTML=`<div class="analysis-dashboard-card-head"><h3><span class="analysis-dashboard-grip" aria-hidden="true">⠿</span>${esc(report.name)}</h3><div class="analysis-dashboard-actions"><button type="button" data-report-image disabled title="导出已加载图表及精确附件">图片</button><button type="button" data-report-open>打开 ↗</button><button type="button" class="analysis-dashboard-remove" data-report-remove title="删除该报表" aria-label="删除报表">×</button></div></div><div class="analysis-dashboard-plot"><canvas></canvas><div class="analysis-chart-tooltip" hidden></div></div>`;
      host.append(card);
      card.addEventListener('dragstart',event=>{
        if(event.target.closest('button')){event.preventDefault();return}
        event.dataTransfer.setData('text/plain',report.id);event.dataTransfer.effectAllowed='move';card.classList.add('is-dragging')
      });
      card.addEventListener('dragend',()=>{host.querySelectorAll('.analysis-dashboard-card').forEach(c=>c.classList.remove('is-dragging','is-drop-target'))});
      card.addEventListener('dragover',event=>{event.preventDefault();card.classList.add('is-drop-target')});
      card.addEventListener('dragleave',()=>card.classList.remove('is-drop-target'));
      card.addEventListener('drop',async event=>{
        event.preventDefault();card.classList.remove('is-drop-target');const draggedID=event.dataTransfer.getData('text/plain');
        const selected=host.querySelector(`.analysis-dashboard-card[data-report-id="${CSS.escape(draggedID)}"]`);
        if(!selected||selected===card)return;
        const bounds=card.getBoundingClientRect();host.insertBefore(selected,event.clientX<bounds.left+bounds.width/2?card:card.nextSibling);
        const ids=[...host.querySelectorAll('.analysis-dashboard-card')].map(el=>el.dataset.reportId);
        try{await post('/api/analysis/dashboard/order',{ids});state.workspace.dashboardOrder=ids;toast('仪表盘顺序已保存','success')}
        catch(e){toast('排序保存失败：'+e.message,'error');renderDashboard()}
      });
      card.querySelector('[data-report-remove]').onclick=async()=>{
        const typed=await askText('删除分析报表','',`输入“${report.name}”以确认`);
        if(typed!==report.name)return;
        try{await post('/api/analysis/delete',{kind:'report',id:report.id});await refreshWorkspace();toast('报表已删除','success')}
        catch(e){toast(e.message,'error')}
      };
      const openButton=card.querySelector('[data-report-open]');openButton.onclick=()=>openReport(report);
      const info=document.createElement('span');info.className='analysis-card-loading';info.textContent='接近视口时加载';card.querySelector('.analysis-dashboard-plot').append(info);
      card.__loadAnalysisCard=async()=>{
        if(!card.isConnected||state.tab!=='dashboard')return;
		const load=card.__chartLoad=(card.__chartLoad||0)+1,viewQuery=JSON.parse(JSON.stringify(report.query));
		card.__chartImageToken=null;card.querySelector('[data-report-image]').disabled=true;window.ExcelLensChartExport?.revalidate();
        info.textContent='正在计算…';
        try{
          let snapshot=state.reportCache.get(book,viewQuery);
          if(!snapshot){const fetched=await executeQuery(viewQuery);if(!fetched.current()||!card.isConnected)return;snapshot=state.reportCache.set(book,fetched.query||viewQuery,fetched)}
          if(card.isConnected&&state.tab==='dashboard'&&book===state.fingerprint&&pathBook===state.book&&opened===state.openSerial&&load===card.__chartLoad){
            const mode=viewQuery.chart||'bar',metric=Math.min(effectiveMetrics(snapshot.query).length-1,Math.max(0,Number(viewQuery.chartMetric)||0)),canvas=card.querySelector('canvas'),projected=projectMetric(snapshot.result,metric),token={snapshot,mode,metric};
            info.remove();charts?.draw(canvas,projected,mode,(keys,col)=>{
              if(activateResult(snapshot)){renderResult(snapshot.result,state.activeResultQuery);drill(keys,col,0,Boolean(state.activeResultQuery.columns?.length));}
            });
            card.__chartImageToken=token;window.ExcelLensChartExport?.revalidate();
            const current=()=>state.opened&&state.tab==='dashboard'&&card.isConnected&&book===state.fingerprint&&pathBook===state.book&&opened===state.openSerial&&card.__chartImageToken===token;
            const image=card.querySelector('[data-report-image]');image.disabled=false;image.onclick=()=>{if(current())openResultImage(snapshot,canvas,projected,mode,metric,report.name,current);};
          }
        }catch(e){if(card.isConnected){info.textContent='无法更新：'+e.message;info.classList.add('analysis-card-error')}}
      };
      if(lazyObserver)lazyObserver.observe(card);else card.__loadAnalysisCard();
    }
  }
  function findMatch(hints,exclude=[]){
    const all=allFields().filter(f=>!exclude.includes(f.id));
    for(const hint of hints||[]){const m=all.find(f=>f.name.toLocaleLowerCase().includes(hint.toLocaleLowerCase()));if(m)return m}
    return null;
  }
  function renderTemplates(){
    const categories=['全部',...new Set(templates.map(t=>t.category))];const cats=$('analysisTemplateCategories');cats.replaceChildren();
    categories.forEach(name=>{const btn=document.createElement('button');btn.textContent=name;btn.className=name===state.templateCategory?'is-active':'';btn.onclick=()=>{state.templateCategory=name;renderTemplates()};cats.append(btn)});
    const container=$('analysisTemplateGrid');container.replaceChildren();
    const search=state.templateText.toLocaleLowerCase();
    templates.filter(t=>(state.templateCategory==='全部'||t.category===state.templateCategory)&&(!search||(t.title+' '+t.description).toLocaleLowerCase().includes(search))).forEach(t=>{
      const card=document.createElement('button');card.type='button';card.className='analysis-template-card';card.innerHTML=`<span class="analysis-template-ico">${esc(t.icon)}</span><strong>${esc(t.title)}</strong><small>${esc(t.description)}</small>`;
      card.onclick=()=>applyTemplate(t);container.append(card);
    });
  }
  function applyTemplate(template){
    if(!state.fields.length){toast('请先选择一张包含数据的工作表','warning');setTab('pivot');return}
    state.activeReportID='';const q=queryBase(state.source);q.layout=state.query?.layout||'';if(state.query?.sourceScope)q.sourceScope=JSON.parse(JSON.stringify(state.query.sourceScope));const main=findMatch(template.row);
    // No silent substitution of an unrelated numerical column. Audit and
    // financial templates must never produce a plausible but wrong report.
    const metric=findMatch(template.value||[],main?[main.id]:[])||null;
    if(main)q.rows=[main.id];
    const column=findMatch(template.col||[],q.rows);if(column)q.columns=[column.id];
    q.value=template.aggregate==='count'?'':metric?.id||'';
    q.aggregate=template.aggregate;
    const ready=Boolean(main)&&(q.aggregate==='count'||Boolean(metric));
    q.chart=template.chart;q.top=template.top||30;q.title=template.title;
    if(main&&(template.title.includes('月度')||template.title.includes('到期年份'))&&/日期|时间|日|月|年/.test(main.name)){
      const fieldID='D_template_date';const op=template.title.includes('到期年份')?'year':'yearmonth';
      q.transforms=[{id:fieldID,name:op==='year'?'年份':'年月',source:main.id,op,arg:''}];q.rows=[fieldID]
    }
    state.query=q;setTab('pivot');renderFields();renderBuilder();
    if(ready)run();
    else toast('模板已载入；没有匹配到'+(!main?'分类字段':(!metric?'数值指标':'必需字段'))+'，请在左侧确认字段后运行','warning');
  }
  let saveChain=Promise.resolve();
  function hasUnsaved(){return pendingWrites>0||drafts.entries(state.currentBook??state.fingerprint).length>0;}
  function updateSaveStatus(){
    const count=drafts.entries(state.fingerprint).length;
    setText('analysisAutoSave',pendingWrites?'保存中…':count?`${count}格未保存`:'已保存');
    $('analysisRetrySave').hidden=!count||pendingWrites>0;
    $('analysisRetrySave').disabled=pendingWrites>0;
  }
  function paintDraft(input,row,col,saved){
    const draft=drafts.get(state.fingerprint,state.loadedSheet,row,col);
    input.classList.toggle('is-unsaved',!!draft);
    input.setAttribute('aria-invalid',draft?'true':'false');
    input.title=draft?'未保存草稿；当前已保存值：'+String(saved??''):(input.dataset.savedTitle||'');
    return draft;
  }
  function queueWrite(run){
    ++pendingWrites;updateSaveStatus();
    saveChain=saveChain.catch(()=>{}).then(async()=>{
      try{return await run()}catch(err){toast('保存失败：'+err.message+(drafts.size?'；草稿已保留':''),'error');throw err}
      finally{--pendingWrites;updateSaveStatus();}
    });return saveChain;
  }
  async function createSheet(){
    const name=await askText('新建可编辑工作表','数据分析表 '+((state.workspace?.sheets?.length||0)+1));if(!name)return;
    try{const data=await post('/api/analysis/sheet/create',{name});state.loadedSheet=data.id;state.sheetPage=0;await refreshWorkspace();setTab('sheets');toast('已新建工作表，所有改动独立保存','success')}catch(err){toast(err.message,'error')}
  }
  async function renderSheets(){
    if(!state.opened||state.tab!=='sheets')return;
    const host=$('analysisSheetList');host.replaceChildren();const sheets=state.workspace?.sheets||[];
    for(const sheet of sheets){const btn=document.createElement('button');btn.type='button';btn.textContent=sheet.name;btn.title=sheet.name;btn.className=sheet.id===state.loadedSheet?'is-active':'';btn.onclick=()=>{state.loadedSheet=sheet.id;state.sheetPage=0;renderSheets()};host.append(btn)}
    if(!sheets.length){
      state.sheetLoadSeq++;state.sheetAbortController?.abort();state.sheetAbortController=null;
      state.loadedSheet=null;state.gridData=null;
      setText('analysisSheetTitle','编辑工作表');
      for(const id of ['analysisPasteSpecial','analysisUndo','analysisRedo','analysisAddColumn','analysisAddRows','analysisDeleteSheet'])$(id).disabled=true;
      $('analysisSheetPagination').replaceChildren();
      const empty=document.createElement('div');empty.className='analysis-sheet-empty';
      const glyph=document.createElement('div');glyph.className='analysis-sheet-empty-icon';glyph.textContent='▦';glyph.setAttribute('aria-hidden','true');
      const title=document.createElement('h3');title.textContent='暂无工作表';
      const actions=document.createElement('div');actions.className='analysis-sheet-empty-actions';
      const create=document.createElement('button');create.type='button';create.className='analysis-primary-button';create.textContent='＋ 新建工作表';create.addEventListener('click',createSheet);
      const pivot=document.createElement('button');pivot.type='button';pivot.className='analysis-outline-button';pivot.textContent='转到透视分析';pivot.addEventListener('click',()=>setTab('pivot'));
      actions.append(create,pivot);empty.append(glyph,title,actions);
      $('analysisEditableGrid').replaceChildren(empty);
      const hint=document.createElement('div');hint.className='analysis-sheet-list-hint';hint.textContent='尚无编辑表';host.append(hint);
      return;
    }
    if(!sheets.some(s=>s.id===state.loadedSheet))state.loadedSheet=sheets[0].id;
    host.querySelectorAll('button').forEach((btn,i)=>btn.classList.toggle('is-active',sheets[i].id===state.loadedSheet));
    await loadSheet(state.loadedSheet,state.sheetPage);
  }
  function getSheetHistory(id){
    if(!state.undoHistory.has(id))state.undoHistory.set(id,{undo:[],redo:[]});
    return state.undoHistory.get(id);
  }
  function updateSheetHistoryButtons(){
    const hist=state.loadedSheet?getSheetHistory(state.loadedSheet):{undo:[],redo:[]};
    $('analysisUndo').disabled=!hist.undo.length;
    $('analysisRedo').disabled=!hist.redo.length;
  }
  async function commitBatch(id,changes,{track=true,refresh=false,guard}={}){
    if(!id||!changes?.length)return;
    if(guard&&!guard())throw Object.assign(Error('粘贴目标或版本已变化，请重新读取目标'),{status:409});
    const book=state.fingerprint,staged=drafts.stage(book,id,changes);
    const result=await queueWrite(async()=>{
      if(book!==state.fingerprint)throw Error('工作簿已切换');
      if(guard&&!guard())throw Object.assign(Error('粘贴目标或版本已变化，整批未写入；输入已保留'),{status:409});
      window.ExcelLensClipboard.validateChanges(changes);
      // Resolve expected version INSIDE the serialized write queue.
      const expectedRevision=state.sheetRevisions.get(id)??0;
      const saved=await post('/api/analysis/sheet/batch',{id,workbookKey:book,expectedRevision,changes});
      if(book===state.fingerprint){state.sheetRevisions.set(id,saved.revision);state.reportCache.clear();}
      return saved;
    });
    drafts.acknowledge(staged);updateSaveStatus();
    if(book!==state.fingerprint)return result;
    if(track){
      const hist=getSheetHistory(id);
      hist.undo.push({before:result.previous,after:changes.map(c=>({...c}))});
      if(hist.undo.length>100)hist.undo.shift();hist.redo=[];
    }
    updateSheetHistoryButtons();
    if(state.loadedSheet===id)for(const input of $('analysisEditableGrid').querySelectorAll('textarea[data-row][data-col]')){
      const row=Number(input.dataset.row),col=Number(input.dataset.col);paintDraft(input,row,col,editGridCellValue(row,col));
    }
    if(refresh&&state.loadedSheet===id)await loadSheet(id,state.sheetPage);
    return result;
  }
  async function retryDrafts(){
    if(pendingWrites)return;
    const book=state.fingerprint,groups=new Map();
    for(const entry of drafts.entries(book)){
      if(!groups.has(entry.id))groups.set(entry.id,[]);groups.get(entry.id).push(entry);
    }
    for(const [id,entries] of groups){
      try{
        for(const batch of window.ExcelLensClipboard.batches(entries)){
          if(book!==state.fingerprint)return;
          const active=batch.filter(entry=>drafts.get(book,id,entry.row,entry.col)===entry);
          if(!active.length)continue;
          try{await commitBatch(id,active.map(({row,col,value})=>({row,col,value})),{refresh:state.loadedSheet===id});}
          catch(err){
            if(err.status===409){
              state.undoHistory.delete(id);state.loadedSheet=id;
              state.sheetPage=Math.floor(active[0].row/sheetPageSize(id));setTab('sheets');await loadSheet(id,state.sheetPage);
              toast('版本冲突，草稿已保留；悬停可核对已保存值，再点击重试保存','warning');
            }
            return;
          }
        }
      }catch(err){toast(err.message,'warning');return;}
    }
    if(book===state.fingerprint&&!drafts.entries(book).length)toast('草稿已保存','success');
  }
  async function changeSheetHistory(direction){
    const id=state.loadedSheet;if(!id)return;
    const hist=getSheetHistory(id),from=direction==='undo'?hist.undo:hist.redo;
    if(!from.length)return;
    const item=from.pop();updateSheetHistoryButtons();
    try{
      await commitBatch(id,direction==='undo'?item.before:item.after,{track:false,refresh:false});
      (direction==='undo'?hist.redo:hist.undo).push(item);
      updateSheetHistoryButtons();await loadSheet(id,state.sheetPage);
    }catch(err){from.push(item);updateSheetHistoryButtons();
      if(err.status===409){hist.undo=[];hist.redo=[];await loadSheet(id,state.sheetPage);toast('编辑版本已变化，操作未执行，已重新载入','warning')}else toast(err.message,'error')
    }
  }
  async function loadSheet(id,page){
    if(!id)return;
    $('analysisPasteSpecial').disabled=true;
    const seq=++state.sheetLoadSeq;
    state.sheetAbortController?.abort();
    const controller=new AbortController();state.sheetAbortController=controller;
    try{
      // Reuse the fetch page size across loads; cap DOM nodes for wide grids.
      const pageSize=sheetPageSize(id);
      const data=await api('/api/analysis/sheet?'+new URLSearchParams({id,offset:String(page*pageSize),limit:String(pageSize)}),{signal:controller.signal});
      if(seq!==state.sheetLoadSeq||state.loadedSheet!==id||state.sheetPage!==page)return;
      state.sheetRevisions.set(id,data.revision??0);
      state.gridData=data;
      $('analysisPasteSpecial').disabled=false;
      setText('analysisSheetTitle',data.name);$('analysisAddColumn').disabled=false;$('analysisAddRows').disabled=false;$('analysisDeleteSheet').disabled=false;
      updateSheetHistoryButtons();
      const area=$('analysisEditableGrid');area.replaceChildren();const table=document.createElement('table');
      const head=document.createElement('thead'),tr=document.createElement('tr');
      const zero=document.createElement('th');zero.textContent='';tr.append(zero);
      data.columns.forEach((name,j)=>{
        const th=document.createElement('th'),input=document.createElement('input');input.value=name;input.title='编辑列名，失焦后保存';
        input.addEventListener('change',()=>{
          const cols=[...data.columns];cols[j]=input.value.trim();
          if(!cols[j]){input.value=name;return}
          queueWrite(async()=>{
            const rev=state.sheetRevisions.get(id)??0;
            const result=await post('/api/analysis/sheet/columns',{id,columns:cols,expectedRevision:rev});
            state.sheetRevisions.set(id,rev+1);state.reportCache.clear();return result;
          }).then(()=>{data.columns=cols;state.undoHistory.delete(id);updateSheetHistoryButtons();refreshWorkspace()}).catch(async()=>{input.value=name;await loadSheet(id,page)});
        });th.append(input);tr.append(th);
      });head.append(tr);table.append(head);
      const body=document.createElement('tbody');
      const rows=Math.min(Math.max(data.rows?.length||0,Math.min(25,pageSize)),Math.min(pageSize,10000-data.offset));
      const fragment=document.createDocumentFragment();
      for(let i=0;i<rows;i++){
        const row=document.createElement('tr'),n=document.createElement('td');n.textContent=String(data.offset+i+1);row.append(n);
        for(let j=0;j<data.columns.length;j++){
          const td=document.createElement('td'),input=document.createElement('textarea');input.rows=1;
          const saved=data.rows?.[i]?.[j]??'';
          input.value=saved;input.dataset.row=String(data.offset+i);input.dataset.col=String(j);
          input.title=`${data.columns[j]} · 第 ${data.offset+i+1} 行`;
          input.dataset.savedTitle=input.title;
          const draft=paintDraft(input,data.offset+i,j,saved);if(draft)input.value=draft.value;
          td.append(input);row.append(td)
        }fragment.append(row)
      }
      body.append(fragment);table.append(body);area.append(table);
      const pagination=$('analysisSheetPagination');pagination.replaceChildren();
      const prev=document.createElement('button');prev.textContent='‹ 上一页';prev.disabled=page===0;prev.onclick=async()=>{await saveChain.catch(()=>{});state.sheetPage=page-1;loadSheet(id,state.sheetPage)};pagination.append(prev);
      const span=document.createElement('span');span.textContent=`第 ${page+1} 页 · 已保存 ${fmtNum(data.total)} 行 · 每页 ${pageSize} 行`;pagination.append(span);
      const next=document.createElement('button');next.textContent='下一页 ›';
      const lastAllowed=Math.min(9999,Math.max(data.total,1)+pageSize-1);
      next.disabled=(page+1)*pageSize>lastAllowed;
      next.onclick=async()=>{await saveChain.catch(()=>{});state.sheetPage=page+1;loadSheet(id,state.sheetPage)};pagination.append(next);
    }catch(e){if(e.name!=='AbortError'&&seq===state.sheetLoadSeq)toast(e.message,'error')}
    finally{if(state.sheetAbortController===controller)state.sheetAbortController=null}
  }
  function editGridCellValue(row,col){
    const data=state.gridData;if(!data)return '';
    const index=row-data.offset;
    return data.rows?.[index]?.[col]??'';
  }
  function sheetPageSize(id){
    const info=state.workspace?.sheets?.find(x=>x.id===id);
    return Math.max(30,Math.min(80,Math.floor(2600/Math.max(1,info?.columns?.length||10))));
  }
  function editGridWriteLocal(row,col,value){
    const data=state.gridData;if(!data||row<data.offset)return;
    const index=row-data.offset;
    if(index>=300)return;
    if(!data.rows)data.rows=[];
    while(data.rows.length<=index)data.rows.push([]);
    while(data.rows[index].length<=col)data.rows[index].push('');
    data.rows[index][col]=value;
  }
  function bindSheetGrid(){
    const area=$('analysisEditableGrid');
    area.addEventListener('focusin',event=>{const input=event.target.closest('textarea[data-row][data-col]');if(input)state.sheetCell={id:state.loadedSheet,page:state.sheetPage,row:Number(input.dataset.row),col:Number(input.dataset.col)};});
    area.addEventListener('input',event=>{
      const input=event.target.closest('textarea[data-row][data-col]');if(!input||!state.loadedSheet)return;
      const row=Number(input.dataset.row),col=Number(input.dataset.col),saved=editGridCellValue(row,col);
      const staged=drafts.stage(state.fingerprint,state.loadedSheet,[{row,col,value:input.value}]);
      if(input.value===saved&&!pendingWrites)drafts.acknowledge(staged);
      paintDraft(input,row,col,saved);updateSaveStatus();
    });
    area.addEventListener('change',event=>{
      const input=event.target.closest('textarea[data-row][data-col]');if(!input||!state.loadedSheet)return;
      const row=Number(input.dataset.row),col=Number(input.dataset.col);
      const previous=editGridCellValue(row,col);
      if(previous===input.value&&!pendingWrites){const draft=drafts.get(state.fingerprint,state.loadedSheet,row,col);if(draft)drafts.acknowledge([draft]);updateSaveStatus();return;}
      const id=state.loadedSheet,next=input.value;
      commitBatch(id,[{row,col,value:next}]).then(()=>{
        if(state.loadedSheet===id)editGridWriteLocal(row,col,next)
      }).catch(async err=>{
        if(err.status===409&&state.loadedSheet===id){state.undoHistory.delete(id);await loadSheet(id,state.sheetPage);toast('版本冲突，草稿已保留；请核对已保存值后重试','warning')}
      });
    });
    area.addEventListener('paste',event=>{
      const input=event.target.closest('textarea[data-row][data-col]');if(!input||!state.loadedSheet)return;
      const text=event.clipboardData?.getData('text/plain')||'';
      if(!text.includes('\t')&&!/[\r\n]/.test(text)){
        const value=input.value.slice(0,input.selectionStart)+text+input.value.slice(input.selectionEnd);
        try{window.ExcelLensClipboard.validateChanges([{row:Number(input.dataset.row),col:Number(input.dataset.col),value}]);}
        catch(err){event.preventDefault();toast(err.message,'warning');}
        return;
      }
      event.preventDefault();
      const startRow=Number(input.dataset.row),startCol=Number(input.dataset.col);
      const cols=state.gridData?.columns?.length||0;
      let changes;
      try{changes=window.ExcelLensClipboard.pasteChanges(text,{startRow,startCol,rowLimit:10000,columns:cols});}
      catch(err){toast(err.message,'warning');return;}
      const id=state.loadedSheet;commitBatch(id,changes,{refresh:true}).catch(async err=>{
        if(err.status===409&&state.loadedSheet===id){state.undoHistory.delete(id);await loadSheet(id,state.sheetPage);toast('版本冲突，粘贴草稿已保留；请核对后重试保存','warning')}
        else toast('粘贴未保存：'+err.message,'error');
      });
    });
    area.addEventListener('keydown',event=>{
      const input=event.target.closest('textarea[data-row][data-col]');if(!input||event.isComposing)return;
      const mod=event.ctrlKey||event.metaKey;
      if(mod&&event.shiftKey&&!event.altKey&&event.key.toLowerCase()==='v'){event.preventDefault();event.stopPropagation();openPasteSpecial();return;}
      if(mod&&!event.altKey&&['z','y'].includes(event.key.toLowerCase())){
        const row=Number(input.dataset.row),col=Number(input.dataset.col);
        if(input.value!==editGridCellValue(row,col))return; // native undo while typing
        event.preventDefault();changeSheetHistory(event.key.toLowerCase()==='z'&&!event.shiftKey?'undo':'redo');return;
      }
      if(mod||event.altKey)return;
      let nr=Number(input.dataset.row),nc=Number(input.dataset.col);
      if(event.key==='Enter'&&!event.shiftKey)nr++;else if(event.key==='Tab')nc+=event.shiftKey?-1:1;else return;
      const data=state.gridData;
      if(!data||nr<data.offset||nr>=data.offset+300||nc<0||nc>=data.columns.length)return;
      event.preventDefault();input.blur();
      const next=area.querySelector(`textarea[data-row="${nr}"][data-col="${nc}"]`);next?.focus();next?.select();
    });
  }
  async function addColumn(){
    if(!state.loadedSheet)return;
    const id=state.loadedSheet,book=state.fingerprint;
    const data=await api('/api/analysis/sheet?'+new URLSearchParams({id,offset:'0',limit:'1'}));
    if(data.columns.length>=80){toast('编辑工作表最多80列','warning');return}
    const name=await askText('添加新列','字段'+(data.columns.length+1));if(!name)return;
    try{await queueWrite(async()=>{
      if(book!==state.fingerprint)throw Error('工作簿已切换');
      const result=await post('/api/analysis/sheet/columns',{id,workbookKey:book,expectedRevision:data.revision,columns:[...data.columns,name]});
      state.sheetRevisions.set(id,result.revision);
    });if(book===state.fingerprint){await refreshWorkspace();renderSheets();}}catch{}
  }
  async function addRows(){
    if(!state.loadedSheet)return;
    const id=state.loadedSheet,book=state.fingerprint;
    try{await queueWrite(async()=>{
      if(book!==state.fingerprint)throw Error('工作簿已切换');
      const result=await post('/api/analysis/sheet/rows',{id,workbookKey:book,expectedRevision:state.sheetRevisions.get(id)??0,count:10});
      state.sheetRevisions.set(id,result.revision);
    });if(book===state.fingerprint){await refreshWorkspace();renderSheets();}}catch{}
  }
  async function deleteSheet(){
    if(hasUnsaved()){toast('仍有未保存的分析内容，请先重试保存','warning');return;}
    const sheet=state.workspace.sheets.find(s=>s.id===state.loadedSheet);if(!sheet)return;
    const confirmation=await askText('确认删除编辑工作表', '',`输入“${sheet.name}”以确认`);
    if(confirmation!==sheet.name){if(confirmation!==null)toast('名称不匹配，已取消删除','warning');return}
    try{await post('/api/analysis/delete',{kind:'sheet',id:sheet.id});state.loadedSheet=null;await refreshWorkspace();setTab('sheets');toast('已删除所选工作表','success')}catch(e){toast(e.message,'error')}
  }
  function bind(){
    $('analysisCompositionOpen').addEventListener('click',()=>compositionPanel.open());
    $('analysisOpenBtn')?.addEventListener('click',open);$('analysisClose').addEventListener('click',close);
    root.querySelectorAll('[data-analysis-tab]').forEach(el=>el.addEventListener('click',()=>setTab(el.dataset.analysisTab)));
    $('analysisBackupExport').addEventListener('click',backupExport);
    $('analysisBackupImport').addEventListener('click',()=>$('analysisBackupFile').click());
    $('analysisBackupFile').addEventListener('change',e=>{backupImport(e.target.files?.[0]).finally(()=>e.target.value='')});
    $('analysisSourceSelect').addEventListener('change',e=>{++state.reportOpenSerial;analysisJobs.cancelMain();sourceRows.reset();state.activeReportID='';state.source=e.target.value;const definition=state.workspace?.reports?.find(report=>report.query.source===state.source)?.query.composition||(state.compositionDraft?.source===state.source?state.compositionDraft.definition:null);state.headerRow=state.source.startsWith('append:')?0:1;$('analysisHeaderRow').value=String(state.headerRow);state.query=queryBase(state.source);if(state.source.startsWith('append:')&&definition)state.query.composition=JSON.parse(JSON.stringify(definition));loadFields()});
    const selectScope=mode=>{++state.reportOpenSerial;analysisJobs.cancelMain();state.activeReportID='';loadFields({mode});};
    $('analysisSourceScope').addEventListener('change',e=>selectScope(e.target.value));
    $('analysisSourceRefresh').addEventListener('click',()=>selectScope(state.query?.sourceScope?.mode||'full'));
    $('analysisSourceCancel').addEventListener('click',()=>{invalidateSourceRequests();state.fieldsReady=state.loadedFieldsKey===fieldsKey();renderSourceScope();setAnalysisBusy(!!analysisJobs.main);setText('analysisSourceInfo','已取消选择，保留原范围');});
    $('analysisHeaderRow').addEventListener('change',()=>{++state.reportOpenSerial;analysisJobs.cancelMain();state.activeReportID='';state.headerRow=Math.max(1,Math.min(Number($('analysisHeaderRow').max),Number($('analysisHeaderRow').value)||1));state.query.headerRow=state.headerRow;loadFields()});
    $('analysisLoadFields').addEventListener('click',()=>loadFields());$('analysisFieldSearch').addEventListener('input',renderFields);
    root.querySelectorAll('[data-analysis-zone]').forEach(zone=>{
      zone.addEventListener('dragover',e=>{e.preventDefault();zone.classList.add('drag-over')});zone.addEventListener('dragleave',()=>zone.classList.remove('drag-over'));
      zone.addEventListener('drop',e=>{e.preventDefault();zone.classList.remove('drag-over');const id=e.dataTransfer.getData('text/plain');addField(id,zone.dataset.analysisZone)});
    });
    for(const [id,key] of [['analysisLayout','layout'],['analysisAggregate','aggregate'],['analysisSort','sort'],['analysisTop','top'],['analysisPrecision','precision']]){
      $(id).addEventListener('change',()=>{
        state.query[key]=['top','precision'].includes(key)?Number($(id).value):$(id).value;
        if(key==='aggregate'&&state.query.metrics?.length){state.query.metrics[0].aggregate=state.query.aggregate}
      })
    }
    $('analysisChartImage').addEventListener('click',exportChartImage);
    $('analysisSnapshotsOpen')?.addEventListener('click',()=>snapshots?.open());
    $('analysisSnapshotSave')?.addEventListener('click',()=>{const c=state.chartCapture;if(c?.current())snapshots?.open({job:c.snapshot.job,metric:c.metric,workbookKey:state.fingerprint,book:state.book,title:$('analysisReportTitle').textContent||'分析快照',current:c.current});});
    $('analysisChartSelect').addEventListener('change',setChartMode);
    $('analysisChartMetric').addEventListener('change',()=>{state.query.chartMetric=Number($('analysisChartMetric').value)||0;state.activeResultQuery&&(state.activeResultQuery.chartMetric=state.query.chartMetric);renderChart()});
    $('analysisAddMetric').addEventListener('click',()=>addMetric(''));
    $('analysisAddFilter').addEventListener('click',()=>{if(!state.fields.length)return;if(state.query.filters.length>=8){toast('最多8条筛选条件','warning');return}state.query.filters.push({field:state.fields[0].id,op:'eq',value:''});renderRules()});
    $('analysisAddConversion').addEventListener('click',()=>{
      if(!state.fields.length)return;if(state.query.transforms.length>=8){toast('最多8个计算列','warning');return}
      state.query.transforms.push({id:'D_'+Date.now().toString(36)+'_'+state.query.transforms.length,name:'转换日期',source:state.fields[0].id,op:'date',arg:'auto'});renderFields();renderBuilder();
    });
    $('analysisAddTransform').addEventListener('click',()=>{
      if(!state.fields.length)return;if(state.query.transforms.length>=8){toast('最多8个计算列','warning');return}
      state.query.transforms.push({id:'D_'+Date.now().toString(36)+'_'+state.query.transforms.length,name:'计算列'+(state.query.transforms.length+1),source:state.fields[0].id,op:'trim',arg:''});renderFields();renderBuilder();
    });
    $('analysisRun').addEventListener('click',run);
    $('analysisTreeCollapse').addEventListener('click',()=>tree.collapseAll());
    $('analysisTreePrevious').addEventListener('click',()=>{tree.page=Math.max(0,tree.page-1);renderPivot();});
    $('analysisTreeNext').addEventListener('click',()=>{tree.page++;renderPivot();});
    $('analysisCancel').addEventListener('click',()=>{if(analysisJobs.cancelMain())toast('已取消分析任务');});
    $('analysisSaveReport').addEventListener('click',saveReport);$('analysisMaterialize').addEventListener('click',materialize);$('analysisMaterializeDetails').addEventListener('click',()=>materializeDetails(false));$('analysisDrillSave').addEventListener('click',()=>materializeDetails(true));$('analysisExport').addEventListener('click',exportCSV);
    $('analysisDrillClose').addEventListener('click',()=>{state.drillAbort?.abort();$('analysisDrill').hidden=true});
    $('analysisDashboardRefresh').addEventListener('click',()=>{state.reportCache.clear();renderDashboard()});
    $('analysisTemplateSearch').addEventListener('input',e=>{state.templateText=e.target.value;renderTemplates()});
    bindSheetGrid();$('analysisUndo').addEventListener('click',()=>changeSheetHistory('undo'));$('analysisRedo').addEventListener('click',()=>changeSheetHistory('redo'));
    $('analysisRetrySave').addEventListener('click',retryDrafts);
    $('analysisCreateSheet').addEventListener('click',createSheet);$('analysisAddColumn').addEventListener('click',addColumn);$('analysisAddRows').addEventListener('click',addRows);$('analysisDeleteSheet').addEventListener('click',deleteSheet);
    root.addEventListener('keydown',e=>{if(e.key==='Escape'){$('analysisDrill').hidden=true;const shade=root.querySelector('.analysis-modal-shade');if(!shade)close();e.stopPropagation()}});
    window.addEventListener('resize',()=>{if(state.opened)charts?.resize();});
    window.addEventListener('beforeunload',event=>{if(hasUnsaved()){event.preventDefault();event.returnValue='';}});
  }
  bind();
  async function navigateSheet(id,row,column){
    if(hasUnsaved())throw Error('分析表仍有未保存内容，请先重试保存后再定位');
    if(!Number.isInteger(row)||!Number.isInteger(column)||row<1||column<1)throw Error('编辑表定位坐标无效');
    await open();
    const sheet=state.workspace?.sheets?.find(sheet=>sheet.id===id);
    if(!state.opened||!sheet||row>sheet.rows||column>sheet.columns.length)throw Error('编辑表或来源位置已变化');
    state.loadedSheet=id;state.sheetPage=Math.floor((row-1)/sheetPageSize(id));state.gridData=null;
    await setTab('sheets');
    if(state.gridData?.id!==id)throw Error('编辑表读取失败，请重试定位');
    const target=$('analysisEditableGrid').querySelector(`textarea[data-row="${row-1}"][data-col="${column-1}"]`);
    if(!target)throw Error('未能读取所选来源位置');
    target.focus();target.scrollIntoView({block:'center',inline:'nearest'});
  }
  window.ExcelLensAnalytics={open,close,bookChanged,hasUnsaved,navigateSheet,refresh:refreshWorkspace,diagnostics:()=>({opened:state.opened,tab:state.tab,source:state.source,sourceMode:state.query?.sourceScope?.mode||'full',layout:state.query?.layout||'',tree:tree.diagnostics(),fields:state.fields.length,fieldsReady:!!state.fieldsReady,fieldsLoading:!!state.fieldsLoading,resultJob:state.jobID,hasResult:!!state.activeResult,pendingWrites,drafts:drafts.size,activeJobs:analysisJobs.tasks.size})};
  function capturePasteTarget(previous){
    const data=state.gridData;
    if(!state.opened||state.tab!=='sheets'||!data||data.id!==state.loadedSheet)throw Error('请先打开独立编辑表');
    const id=state.loadedSheet,book=state.fingerprint,pathBook=state.book,page=state.sheetPage,revision=state.sheetRevisions.get(id);
    if(previous&&(previous.id!==id||previous.book!==book||previous.pathBook!==pathBook||previous.page!==page))throw Error('目标表或页面已变化，请关闭后重新选择');
    if(!previous&&drafts.entries(book).some(entry=>entry.id===id))throw Error('此表还有未保存草稿，请先重试保存后再选择性粘贴');
    const selected=previous||(state.sheetCell?.id===id&&state.sheetCell.page===page?state.sheetCell:{row:data.offset,col:0});
    const row=selected.row,col=selected.col;
    const letter=n=>{let text='';for(let value=n+1;value>0;value=Math.floor((value-1)/26))text=String.fromCharCode(65+(value-1)%26)+text;return text;};
    return {id,book,pathBook,page,revision,row,col,columns:data.columns.length,label:`${data.name} · ${letter(col)}${row+1}`,
      current:()=>state.opened&&state.tab==='sheets'&&state.fingerprint===book&&state.book===pathBook&&state.loadedSheet===id&&state.sheetPage===page&&state.gridData?.id===id&&state.sheetRevisions.get(id)===revision};
  }
  async function refreshPasteTarget(previous){
    if(state.fingerprint!==previous.book||state.book!==previous.pathBook||state.loadedSheet!==previous.id||state.sheetPage!==previous.page)throw Error('目标已变化，请关闭后重新选择');
    await saveChain.catch(()=>{});await loadSheet(previous.id,previous.page);
  }
  async function openPasteSpecial(){
    const book=state.fingerprint,pathBook=state.book,id=state.loadedSheet,page=state.sheetPage;
    await saveChain.catch(()=>{});
    if(book!==state.fingerprint||pathBook!==state.book||id!==state.loadedSheet||page!==state.sheetPage)return;
    pasteSpecial.open();
  }
  $('analysisPasteSpecial').addEventListener('click',openPasteSpecial);
  document.addEventListener('excellens:analysisnumberchange',()=>{
    if(state.opened&&state.activeResult){renderMetrics(state.activeResult,state.activeResultQuery);renderPivot(state.pivotLimit||150);}
  });
  const launchParams=typeof URLSearchParams==='function'?new URLSearchParams(window.location?.search||''):null;
  if(launchParams?.has('analysisSheet'))Promise.resolve(window.ExcelLensWorkbookReady).then(()=>navigateSheet(launchParams.get('analysisSheet'),Number(launchParams.get('sourceRow')),Number(launchParams.get('sourceCol')))).catch(error=>toast(error.message,'error'));
})();
