(() => {
  'use strict';
  const $=id=>document.getElementById(id),make=(tag,cls='',text='')=>{const node=document.createElement(tag);node.className=cls;node.textContent=text;return node;};
  const reasons=new Map([
    ['number','可用'],['imported-amount','可用（导入金额）'],['source-text','不参与（源为文本）'],
    ['text-or-identifier','不参与（文本或编号）'],['blank','不参与（空白）'],['missing-result','不参与（公式无保存结果）'],
    ['invalid-number','不参与（无效数值）'],['error','不参与（错误值）'],['boolean','不参与（布尔值）'],
    ['date','不参与（日期文本）'],['unsupported-type','不参与（不支持的源类型）']
  ]);
  const rowStates=new Map([['visible','当前可见'],['outside','已用范围外'],['hidden','当前已隐藏'],['header','表头，不参与筛选'],['pinned','冻结保留行'],['matched','在筛选结果中'],['filtered-out','不在筛选结果中']]);
  const operators=new Map([['contains','包含'],['notContains','不包含'],['equals','等于'],['notEquals','不等于'],['startsWith','开头是'],['endsWith','结尾是'],['gt','大于'],['gte','大于等于'],['lt','小于'],['lte','小于等于'],['blank','为空'],['notBlank','非空'],['in','属于']]);
  const integer=(n,min,max)=>Number.isSafeInteger(n)&&n>=min&&n<=max;
  function validate(page,request,limits,source,parseRef){
    const fail=()=>{throw Error('单元格信息与当前来源不一致，请重试');};
    if(!page||!limits?.bytes||!source)fail();
    for(const key of ['workbookKey','book','sheet','viewId','ref','pinnedThroughRow'])if(page[key]!==request[key])fail();
    if((page.filterId||'')!==(request.filterId||''))fail();
    const cell=page.cell,ref=parseRef(request.ref);
    if(!ref||!integer(ref.row,1,source.maxRows)||!integer(ref.col,1,source.maxColumns)||!cell||cell.ref!==request.ref||cell.col!==ref.col||typeof cell.raw!=='string'||typeof cell.display!=='string'||typeof cell.hasFormula!=='boolean'||!integer(cell.style,0,Number.MAX_SAFE_INTEGER))fail();
    for(const key of ['formula','type'])if(cell[key]!==undefined&&typeof cell[key]!=='string')fail();
    for(const key of ['cacheMissing','sharedFormula','presentationOnly'])if(cell[key]!==undefined&&typeof cell[key]!=='boolean')fail();
    if(cell.conditional?.display!=null&&typeof cell.conditional.display!=='string')fail();
    const numeric=page.numeric;
    if(!numeric||typeof numeric.eligible!=='boolean'||!reasons.has(numeric.reason)||numeric.eligible!==['number','imported-amount'].includes(numeric.reason))fail();
    if(numeric.eligible&&(cell.cacheMissing||cell.raw===''||typeof numeric.exact!=='string'||!numeric.exact||numeric.reason==='number'&&!['n','',undefined].includes(cell.type)||numeric.reason==='imported-amount'&&(!source.textAmounts||!['s','str','inlineStr'].includes(cell.type))))fail();
    if(!numeric.eligible&&numeric.exact!==undefined)fail();
    if(page.formulaState!==(cell.hasFormula?(cell.cacheMissing?'missing':'cached'):'none')||page.formulaAvailable!==!!window.ExcelLensCellView.formula(cell)||!rowStates.has(page.rowState))fail();
    if(page.presentationNotice!==undefined&&typeof page.presentationNotice!=='string')fail();
    if(page.filter){
      const filter=page.filter;
      if(!request.filterId||!integer(filter.headerRow,0,source.maxRows)||!Array.isArray(filter.rules)||!Array.isArray(filter.sortKeys))fail();
      for(const rule of filter.rules){
        if(!integer(rule?.column,1,source.maxColumns)||!operators.has(rule.operator)||typeof rule.value!=='string'||rule.values!==undefined&&(!Array.isArray(rule.values)||rule.values.some(value=>typeof value!=='string')))fail();
      }
      if(filter.sortColumn!==undefined&&!integer(filter.sortColumn,0,source.maxColumns))fail();
      if(filter.descending!==undefined&&typeof filter.descending!=='boolean')fail();
      for(const sort of filter.sortKeys)if(!integer(sort?.column,1,source.maxColumns)||typeof sort.descending!=='boolean'||sort.mode!==undefined&&!['','source','number','text','natural'].includes(sort.mode))fail();
    }
    if(new TextEncoder().encode(JSON.stringify(page)).length>limits.bytes)throw Error('单元格信息超过显示容量，请使用导出');
    return page;
  }
  class CellExplanation {
    constructor({state,api,toast,limits,source,parseRef,colName,pinnedThroughRow,canRead=()=>true,notice=()=>''}){
      Object.assign(this,{s:state,api,toast,limits,source,parseRef,colName,pinnedThroughRow,canRead,notice});
      this.serial=0;this.dialog=$('detailDialog');
      this.dialog.addEventListener('cancel',event=>{event.preventDefault();this.resetBook();});
      this.dialog.addEventListener('close',()=>{if(!this.dialog.open)this.clear();});
      $('detailRetry').addEventListener('click',()=>this.load());
    }
    binding(){
      const req={workbookKey:this.s.workbookKey,book:this.s.book,sheet:this.s.sheet,viewId:this.s.rowView?.id||'',filterId:this.s.filtered?this.s.filterId||'':'',ref:this.s.selected?.ref||'',pinnedThroughRow:this.pinnedThroughRow()};
      return {request:req,index:this.s.index,key:JSON.stringify([req,this.s.sheetEpoch,this.s.opened,this.s.indexReady])};
    }
    same(bound){const now=this.binding();return !!bound&&bound.key===now.key&&bound.index===now.index;}
    enabled(){return this.s.opened&&this.s.indexReady&&this.s.selected&&this.s.book&&this.s.rowView?.id&&!this.s.opening&&!this.s.sheetLoadingEpoch&&!this.s.rowViewFailure&&this.canRead();}
    stop(){++this.serial;this.abort?.abort();this.abort=null;}
    clear(){this.stop();this.bound=null;this.page=null;$('detailBody').replaceChildren();$('detailStatus').textContent='';$('detailRetry').hidden=true;$('detailBody').setAttribute('aria-busy','false');}
    resetBook(){this.clear();if(this.dialog.open)this.dialog.close();}
    changed(){if(this.bound&&!this.same(this.bound))this.resetBook();}
    async open(){
      if(!this.enabled()){this.toast('请先完成工作表加载并选择单元格','warning');return;}
      this.clear();this.bound=this.binding();if(!this.dialog.open)this.dialog.showModal();await this.load();
    }
    async load(){
      if(!this.dialog.open||!this.bound||!this.same(this.bound)||!this.enabled())return;
      this.stop();const ticket=this.serial,bound=this.bound,controller=new AbortController();this.abort=controller;
      const current=()=>ticket===this.serial&&!controller.signal.aborted&&this.dialog.open&&this.same(bound);
      $('detailStatus').textContent='正在读取…';$('detailStatus').setAttribute('role','status');$('detailRetry').hidden=true;$('detailBody').setAttribute('aria-busy','true');
      try{
        const body=JSON.stringify(bound.request);if(new TextEncoder().encode(body).length>this.limits.requestBytes)throw Error('单元格信息请求超过容量');
        const response=await this.api('/api/cell/explain',{method:'POST',body,signal:controller.signal});if(!current())return;
        const page=validate(response,bound.request,this.limits,this.source(),this.parseRef),fragment=this.build(page);if(!current())return;
        this.page=page;$('detailBody').replaceChildren(fragment);$('detailStatus').textContent='';
      }catch(error){if(current()&&error.name!=='AbortError'){$('detailStatus').textContent=error.message;$('detailStatus').setAttribute('role','alert');$('detailRetry').hidden=false;}}
      finally{if(ticket===this.serial){this.abort=null;$('detailBody').setAttribute('aria-busy','false');}}
    }
    build(page){
      const fragment=document.createDocumentFragment(),cell=page.cell,view=window.ExcelLensCellView;
      const add=(label,text)=>{const value=make('div','detail-value');if(typeof text==='string')value.textContent=text;else value.append(text);fragment.append(make('div','detail-label',label),value);};
      add('位置',page.sheet+'!'+page.ref);
      add('显示值',cell.cacheMissing?'未保存计算结果':view.value(cell,false,this.s.showStyles)!==''?view.value(cell,false,this.s.showStyles):cell.hasFormula?'（空字符串）':'（空白）');
      add('原始值',cell.raw===''?'（空白）':cell.raw);
      add('源类型',view.sourceType(cell));
      add('数值统计',reasons.get(page.numeric.reason));
      if(page.numeric.eligible&&page.numeric.exact!==cell.raw)add('统计数值',page.numeric.exact);
      if(page.formulaState!=='none'){
        add('公式',page.formulaAvailable?view.formula(cell):'原文无法还原');
        add('计算结果',page.formulaState==='missing'?'未保存':'已保存');
      }
      if(page.filter){
        if(page.filter.rules.length){
          const rules=make('div','cell-explanation-rules');
          for(const rule of page.filter.rules){
            const column=this.colName(rule.column)+'列 ',op=operators.get(rule.operator);
            if(rule.operator==='in'){
              const values=rule.values||[],details=make('details'),summary=make('summary','',column+op+' '+values.length.toLocaleString()+' 项');details.append(summary);
              details.addEventListener('toggle',()=>{if(details.open&&details.children.length===1)details.append(make('pre','',values.map(value=>JSON.stringify(value)).join('\n')));});rules.append(details);
            }else rules.append(make('div','',column+op+(['blank','notBlank'].includes(rule.operator)?'':' '+JSON.stringify(rule.value))));
          }
          add('已应用筛选',rules);add('当前行',rowStates.get(page.rowState));
        }
        const sorts=page.filter.sortKeys.length?page.filter.sortKeys:page.filter.sortColumn?[{column:page.filter.sortColumn,descending:!!page.filter.descending}]:[];
        if(sorts.length)add('已应用排序',sorts.map(sort=>this.colName(sort.column)+'列 '+(sort.descending?'降序':'升序')+({number:'（数值）',text:'（文本）',natural:'（自然）'}[sort.mode]||'')).join('\n'));
      }
      if(!page.filter?.rules.length&&(page.rowState==='hidden'||page.rowState==='outside'))add('当前行',rowStates.get(page.rowState));
      if(cell.presentationOnly)add('源单元格','源为空白，仅显示条件格式');
      if(cell.conditional?.display!=null&&cell.conditional.display!==cell.display)add('源显示值',cell.display);
      const notice=this.notice(cell)||page.presentationNotice||cell.conditional?.notice||cell.richText?.notice;
      if(notice)add('呈现状态',notice);
      return fragment;
    }
  }
  window.ExcelLensCellExplanation=CellExplanation;
  window.ExcelLensCellExplanationModel={validate};
})();
