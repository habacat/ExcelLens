(() => {
  'use strict';
  const empty=()=>({text:'',note:'',committed:'',result:null,savedResult:null,error:'',revision:0,source:null,restored:false,committedRestored:false});
  const blank=Object.freeze(empty());
  class FormulaTable {
    constructor(){this.rows=new Map();this.used=new Set();this.lastUsed=-1;this.rowCount=5;this.active=0;this.onChange=null;}
    get(index){return this.rows.get(index)||blank;}
    ensure(index){
      if(!Number.isSafeInteger(index)||index<0)throw Error('行号无效');
      if(!this.rows.has(index))this.rows.set(index,empty());this.rowCount=Math.max(this.rowCount,index+1);return this.rows.get(index);
    }
    measure(index){
      const row=this.get(index);
      if(row.text!==''||row.note!==''){this.used.add(index);this.lastUsed=Math.max(this.lastUsed,index);}
      else{this.used.delete(index);if(index===this.lastUsed){this.lastUsed=-1;for(const i of this.used)this.lastUsed=Math.max(this.lastUsed,i);}}
      this.fit();
    }
    fit(){this.rowCount=Math.max(5,this.lastUsed+2,this.active+1);}
    changed(index){const row=this.get(index);this.onChange?.(String(index),row.text||row.note?{text:row.text,note:row.note,source:row.source?{...row.source}:null}:null);}
    setNote(index,text){if(text.length>4096)throw Error('备注过长');const row=this.ensure(index);if(row.note===text)return;row.note=text;this.measure(index);this.changed(index);}
    edit(index,text,source){
      if(text.length>4096)throw Error('公式过长');
      const row=this.ensure(index);if(row.text===text)return row;
      if(!row.text||!row.source?.workbookKey)row.source={...source};
      row.text=text;row.result=null;row.error='';row.restored=false;row.revision++;if(!text)row.source=null;this.measure(index,text);this.changed(index);return row;
    }
    cancel(index){const row=this.ensure(index);row.text=row.committed;row.result=row.savedResult;row.restored=row.committedRestored;row.error='';row.revision++;this.measure(index,row.text);this.changed(index);}
    restore(records){
      const items=Object.entries(records);
      for(const [key,data] of items)if(!/^(0|[1-9]\d*)$/.test(key)||!Number.isSafeInteger(Number(key))||Number(key)>Number.MAX_SAFE_INTEGER-2||!data||typeof data.text!=='string'||typeof data.note!=='string'||data.text.length>4096||data.note.length>4096||data.source!=null&&(!data.source||typeof data.source.workbookKey!=='string'||typeof data.source.sheet!=='string'))throw Error('计算表恢复内容无效');
      for(const [key,data] of items){const i=Number(key),row=this.ensure(i),formula=data.text.trim().startsWith('=');Object.assign(row,empty(),data,{source:data.source?{...data.source}:null,committed:data.text,restored:formula,committedRestored:formula});this.measure(i);}
    }
  }
  window.ExcelLensCalculatorModel={FormulaTable};
})();
