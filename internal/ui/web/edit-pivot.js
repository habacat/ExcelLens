(() => {
  'use strict';
  const el = name => document.getElementById('editCopyPivot' + name);
  const labels = {rows:'行字段', columns:'列字段', pages:'分页筛选'};
  const keyOf = item => JSON.stringify([item.type, item.value]);
  function display(item) {
    if (item.type === 'm') return '(空白)';
    if (item.type === 'b') return item.value === '1' ? 'TRUE' : 'FALSE';
    if (item.type === 'd') return item.value.replace(/T00:00:00$/, '').replace('T', ' ');
    return item.value;
  }
  const fieldName = f => typeof f === 'string' ? f : f.field;

  function action(label, path, click) {
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'edit-pivot-action';
    button.title = label;
    button.setAttribute('aria-label', label);
    const icon = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    const stroke = document.createElementNS(icon.namespaceURI, 'path');
    icon.setAttribute('viewBox','0 0 24 24'); icon.setAttribute('aria-hidden','true');
    stroke.setAttribute('d', path); icon.append(stroke); button.append(icon);
    button.addEventListener('click', click);
    return button;
  }

  class PivotLayout {
    constructor(editor) { this.editor = editor; }
    bind(info) {
      this.info = info;
      this.axes = info ? structuredClone(info.axes || {rows:info.row ? [info.row] : []}) : null;
      if (this.axes) for (const key of Object.keys(labels)) this.axes[key] ||= [];
      this.draw();
    }
    draw() {
      this.requests?.abort();
      this.requests = new AbortController();
      const root = el('Axes');
      root.replaceChildren();
      if (!this.axes) return;
      for (const [key,label] of Object.entries(labels)) {
        const area = document.createElement('section');
        area.className = 'edit-pivot-axis'; area.dataset.axis = key;
        const heading = document.createElement('span'); heading.className = 'edit-pivot-heading';
        heading.id = 'editPivotAxis' + key; heading.textContent = label;
        area.setAttribute('aria-labelledby',heading.id);
        const fields = document.createElement('div');fields.className='edit-pivot-fields';
        const group = this.axes[key];
        group.forEach((field,index) => {
          const name = fieldName(field);
          const row = document.createElement('div');row.className='edit-pivot-field';
          const select = this.select(key,index,name);
          if (key === 'rows' && index === 0) select.id = 'editCopyPivotRows';
          const controls=document.createElement('div'); controls.className='edit-pivot-order';
          const up=action('上移'+name,'m6 14 6-6 6 6',()=>this.move(key,index,-1)); up.disabled=index===0;
          const down=action('下移'+name,'m6 10 6 6 6-6',()=>this.move(key,index,1)); down.disabled=index===group.length-1;
          controls.append(up,down,action('移除'+name,'m7 7 10 10 M17 7 7 17',()=>this.change(key,index,'')));
          row.append(select,controls);
          if(key==='pages')row.append(this.pageFilter(field));
          fields.append(row);
        });
        if(group.length<this.info.fields.length)fields.append(this.select(key,group.length,''));
        area.append(heading,fields);root.append(area);
      }
    }
    select(key,index,name) {
      const select=document.createElement('select');
      select.dataset.index=index;
      select.setAttribute('aria-label',name ? labels[key]+' '+(index+1) : '添加'+labels[key]);
      if(!name){select.className='edit-pivot-add';select.add(new Option('添加'+labels[key]+'…',''));}
      for(const f of this.info.fields) {
        if(f!==name&&this.axes[key].some(a=>fieldName(a)===f))continue;
        select.add(new Option(f,f));
      }
      select.value=name;
      select.addEventListener('change',()=>this.change(key,index,select.value));
      return select;
    }
    focus(key,index) {
      const fields=el('Axes').querySelectorAll('[data-axis="'+key+'"] select');
      fields[Math.min(index,fields.length-1)]?.focus();
    }
    change(key,index,name) {
      const group=this.axes[key];
      if(!name)group.splice(index,1);
      else {
        for(const other of Object.keys(labels)){
          if(other!==key)this.axes[other]=this.axes[other].filter(field=>fieldName(field)!==name);
        }
        group[index]=key==='pages'?{field:name,items:[]}:name;
      }
      this.updated(key,index);
    }
    move(key,index,delta) {
      const group=this.axes[key],other=index+delta;
      [group[index],group[other]]=[group[other],group[index]];
      this.updated(key,other);
    }
    updated(key,index) {
      if(!this.axes.rows.length){el('Filter').value='keep';el('Item').value='';}
      this.draw();this.editor.controls();this.focus(key,index);
    }
    pageFilter(page) {
      const box=document.createElement('details');box.className='edit-pivot-filter';
      const summary=document.createElement('summary');
      const selected=new Map((page.items||[]).map(item=>[keyOf(item),item]));
      const caption=()=>{summary.textContent=selected.size>1?'已选 '+selected.size+' 项':selected.size?display(selected.values().next().value):'全部项目';};
      caption();summary.setAttribute('aria-label',page.field+' 筛选项目');box.append(summary);
      const panel=document.createElement('div');panel.className='edit-pivot-options';box.append(panel);
      const search=document.createElement('input');search.type='search';search.placeholder='查找项目';search.setAttribute('aria-label','查找'+page.field+'项目');
      const list=document.createElement('div');list.className='edit-pivot-items';
      const message=document.createElement('output');message.setAttribute('aria-live','polite');
      const more=document.createElement('button');more.type='button';more.className='mini-button';more.textContent='更多项目';more.hidden=true;
      const all=document.createElement('button');all.type='button';all.className='mini-button';all.textContent='显示全部';
      panel.append(search,all,list,message,more);
      let offset=0, ticket=0,loaded=false;
      const signal=this.requests.signal;
      const sync=()=>{page.items=[...selected.values()];caption();};
      all.onclick=()=>{selected.clear();sync();for(const cb of list.querySelectorAll('input'))cb.checked=false;};
      const load=async reset=>{
        const current=++ticket;
        if(reset){offset=0;list.replaceChildren();}
        more.hidden=true;message.textContent='正在读取…';
        const doc=this.editor.data.document;
        try{
          const reply=await this.editor.post('pivot-items',{document:{id:doc.id,binding:doc.binding},revision:doc.revision,sheet:this.info.sheet,pivot:this.info.name,field:page.field,search:search.value,offset},signal);
          if(current!==ticket||signal.aborted||!box.isConnected)return;
          for(const item of reply.items){
            const label=document.createElement('label'),check=document.createElement('input');
            check.type='checkbox';const key=keyOf(item);check.checked=selected.has(key);
            check.onchange=()=>{if(check.checked)selected.set(key,item);else selected.delete(key);sync();};
            label.append(check,document.createTextNode(display(item)));list.append(label);
          }
          offset+=reply.items.length;more.hidden=!reply.more;message.textContent=offset?'':'没有匹配项目';loaded=true;
        }catch(error){if(current===ticket&&!signal.aborted){message.textContent=error.message;loaded=false;}}
      };
      let timer;
      search.oninput=()=>{++ticket;clearTimeout(timer);timer=setTimeout(()=>{if(!signal.aborted)load(true);},180);};
      more.onclick=()=>load(false);
      box.ontoggle=()=>{if(box.open&&!loaded)load(true);};
      return box;
    }
  }
  window.ExcelLensPivotLayout=PivotLayout;
})();
