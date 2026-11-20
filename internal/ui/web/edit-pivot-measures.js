(() => {
  'use strict';
  const el = id => document.getElementById('editCopyPivot'+id);
  const methods = {sum:'求和',count:'计数',average:'平均值',min:'最小值',max:'最大值',product:'乘积',countNums:'数值计数',stdDev:'样本标准差',stdDevp:'总体标准差',var:'样本方差',varp:'总体方差'};
  class PivotMeasures {
    constructor(editor, layout) {
      this.editor=editor;this.layout=layout;
      el('AddMeasure').onchange=()=>{if(el('AddMeasure').value){this.items.push({field:el('AddMeasure').value,aggregate:'sum',sourceIndex:-1});this.draw();this.editor.controls();}};
      el('ValuesAxis').onchange=()=>{this.axis={axis:el('ValuesAxis').value,position:this.layout.axes[el('ValuesAxis').value].length};this.positions();};
      el('ValuesPosition').onchange=()=>{this.axis.position=Number(el('ValuesPosition').value);};
    }
    bind(info) {
      this.info=info;
      this.items=structuredClone(info?.measures || (info?.data?[{field:info.data,aggregate:info.aggregate}]:[]));
      this.axis=structuredClone(info?.valuesAxis || {axis:'columns',position:this.layout.axes?.columns?.length||0});
      this.draw();
    }
    positions() {
      el('ValuesPositionFields').hidden=this.items.length<2;
      if(this.items.length<2)return;
      const fields=this.layout.axes[this.axis.axis] || [];
      this.axis.position=Math.min(fields.length,this.axis.position);
      const binding=JSON.stringify([this.axis,fields]);
      if(this.positionBinding===binding)return;
      this.positionBinding=binding;
      el('ValuesAxis').value=this.axis.axis;
      const options=fields.map((name,i)=>new Option(name+'之前',String(i)));
      options.push(new Option(fields.length?'最后':'首位',String(fields.length)));
      el('ValuesPosition').replaceChildren(...options);el('ValuesPosition').value=this.axis.position;
    }
    draw() {
      const root=el('Measures');root.replaceChildren();
      for(const [i,item] of this.items.entries()){
        const row=document.createElement('div');row.className='edit-pivot-measure';
        const field=document.createElement('select'),method=document.createElement('select');
        field.setAttribute('aria-label','值字段 '+(i+1));method.setAttribute('aria-label','汇总方式 '+(i+1));
        if(i===0){field.id='editCopyPivotData';method.id='editCopyPivotAggregate';}
        for(const f of this.info.fields)field.add(new Option(f,f));field.value=item.field;
        for(const [v,s] of Object.entries(methods))method.add(new Option(s,v));method.value=item.aggregate;
        const change=()=>{
          if(item.field!==field.value)item.sourceIndex=-1;
          item.field=field.value;item.aggregate=method.value;
          delete item.caption;input.value='';summary.textContent='自定义标题';
          this.editor.controls();
        };
        field.onchange=method.onchange=change;
        const actions=document.createElement('div');actions.className='edit-pivot-order';
        const move=delta=>{const j=i+delta;[this.items[i],this.items[j]]=[this.items[j],this.items[i]];this.draw();this.editor.controls();};
        const up=this.layout.action('上移值字段 '+item.field,'m6 14 6-6 6 6',()=>move(-1));up.disabled=i===0;
        const down=this.layout.action('下移值字段 '+item.field,'m6 10 6 6 6-6',()=>move(1));down.disabled=i===this.items.length-1;
        const remove=this.layout.action('移除值字段 '+item.field,'m7 7 10 10 M17 7 7 17',()=>{this.items.splice(i,1);this.draw();this.editor.controls();});remove.disabled=this.items.length===1;
        const customize=document.createElement('details');customize.className='edit-measure-caption';
        const summary=document.createElement('summary');summary.textContent=item.caption||'自定义标题';
        const input=document.createElement('input');input.maxLength=255;input.placeholder='自动标题';input.value=item.caption||'';
        input.setAttribute('aria-label','值字段标题 '+(i+1));
        input.oninput=()=>{item.caption=input.value||undefined;summary.textContent=item.caption||'自定义标题';};
        customize.append(summary,input);actions.append(up,down,remove);row.append(field,method,actions,customize);root.append(row);
      }
      el('AddMeasure').replaceChildren(new Option('添加值字段…',''),...(this.info?.fields||[]).map(f=>new Option(f,f)));
      this.positions();
    }
  }
  window.ExcelLensPivotMeasures=PivotMeasures;
})();
