/* Recent workbooks live in Go's user-local recent.json; this controller never
 * stores spreadsheet content, never accesses the network outside localhost,
 * and never assumes a missing file is safe to delete automatically. */
(() => {
  'use strict';
  const glyph=name=>`<svg class="icon" aria-hidden="true" focusable="false"><use href="#i-${name}"/></svg>`;
  class RecentWorkbooks {
    constructor({api, open, toast}) {
      this.api=api; this.open=open; this.toast=toast;
      this.items=[]; this.ticket=0; this.busy=false;
      this.list=document.getElementById('recentList');
      this.search=document.getElementById('recentSearch');
      this.search.addEventListener('input',()=>this.render());
      document.getElementById('recentRefreshBtn').addEventListener('click',()=>this.load());
      this.list.addEventListener('click',e=>this.handleClick(e));
      this.list.addEventListener('keydown',e=>{if((e.key==='Enter'||e.key===' ') && e.target.matches('.recent-item')){e.preventDefault();e.target.click();}});
      window.addEventListener('focus',()=>this.refreshTimes());
      this.ageTimer=setInterval(()=>this.refreshTimes(),60000);
    }
    prettyDate(raw,now=Date.now()) {
      const timestamp=Date.parse(raw);
      if(!Number.isFinite(timestamp))return '时间未知';
      const age=Math.max(0,now-timestamp);
      const minute=60000,hour=60*minute,day=24*hour;
      if(age<minute)return '刚刚';
      if(age<hour)return Math.max(1,Math.floor(age/minute))+'分钟前';
      if(age<day)return Math.max(1,Math.floor(age/hour))+'小时前';
      if(age<30*day)return Math.max(1,Math.floor(age/day))+'天前';
      const date=new Date(timestamp),today=new Date(now);
      const months=(today.getFullYear()-date.getFullYear())*12+today.getMonth()-date.getMonth();
      if(months===1)return '上个月';
      if(months>=2&&months<12&&age<365*day)return months+'个月前';
      if(age<60*day)return '上个月';
      if(age<365*day)return Math.max(2,Math.floor(age/(30*day)))+'个月前';
      return '很久以前';
    }
    exactDate(raw){
      const date=new Date(raw);
      return Number.isNaN(+date)?'时间未知':new Intl.DateTimeFormat('zh-CN',{
        year:'numeric',month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit',second:'2-digit',hour12:false
      }).format(date);
    }
    formatSize(bytes){return window.ExcelLensDisplayFormat.bytes(bytes);}
    refreshTimes(){
      const now=Date.now();
      for(const node of this.list.querySelectorAll('.recent-date[data-opened-at]')){
        node.textContent=this.prettyDate(node.dataset.openedAt,now);
      }
    }
    async load() {
      const ticket=++this.ticket;
      try {
        const data=await this.api('/api/recent');
        if(ticket!==this.ticket)return;
        this.items=Array.isArray(data.items)?data.items:[];
        this.render();
      } catch(error) {
        if(ticket!==this.ticket)return;
        this.list.replaceChildren(this.empty(`最近文件读取失败：${error.message}`));
      }
    }
    empty(message) {
      const el=document.createElement('div');el.className='recent-empty';
      const detail=document.createElement('span');detail.textContent=message;
      el.append(detail);return el;
    }
    render() {
      const q=this.search.value.trim().toLocaleLowerCase();
      const matching=this.items.filter(x=>!q || `${x.name} ${x.path}`.toLocaleLowerCase().includes(q));
      const frag=document.createDocumentFragment();
      const latest=this.items.reduce((best,item)=>!best||Date.parse(item.openedAt)>Date.parse(best.openedAt)?item:best,null);
      const now=Date.now();
      for(const item of matching) {
        const btn=document.createElement('div');
        btn.className='recent-item'+(item.available?'':' is-missing');
        btn.dataset.id=item.id;
        const icon=document.createElement('div');icon.className='recent-file-icon';icon.textContent=(item.name.split('.').pop()||'XL').slice(0,2).toUpperCase();
        const content=document.createElement('div');content.className='recent-content';
        const title=document.createElement('div');title.className='recent-title-line';
        const name=document.createElement('div');name.className='recent-name';name.textContent=item.name;name.title=item.path;
        title.append(name);
        if(latest?.id===item.id){const tag=document.createElement('span');tag.className='recent-last-tag';tag.textContent='上次打开';tag.title='最近一次打开的工作簿';title.append(tag);}
        const meta=document.createElement('div');meta.className='recent-meta';
        const one=document.createElement('span');one.textContent=item.available?`${item.sheetCount} 张表`:'文件已移动或删除';
        const two=document.createElement('span');two.textContent=item.archiveName?item.archiveName+" / "+item.name:item.path;two.title=item.archivePath||item.path;
        meta.append(one,two);content.append(title,meta);
        const side=document.createElement('div');side.className='recent-side';
        const date=document.createElement('div');date.className='recent-date';date.dataset.openedAt=item.openedAt;
        date.textContent=this.prettyDate(item.openedAt,now);date.title='打开时间：'+this.exactDate(item.openedAt);
        date.setAttribute('aria-label','打开时间：'+this.exactDate(item.openedAt));
        const size=document.createElement('span');size.className='recent-size';size.textContent=this.formatSize(item.fileSize);
        size.title=size.textContent==='—'?'原文件大小未知':'原文件大小：'+size.textContent;
        const buttons=document.createElement('div');buttons.className='recent-controls';
        const pin=document.createElement('button');pin.type='button';pin.className='recent-action'+(item.pinned?' is-pinned':'');pin.dataset.action='pin';pin.innerHTML=glyph('pin');pin.title=item.pinned?'取消置顶':'置顶';pin.setAttribute('aria-label',pin.title);
        const remove=document.createElement('button');remove.type='button';remove.className='recent-action';remove.dataset.action='remove';remove.innerHTML=glyph('x');remove.title='从列表移除（不删除文件）';remove.setAttribute('aria-label',remove.title);
        buttons.append(pin,remove);side.append(date,size,buttons);btn.append(icon,content,side);
        btn.setAttribute('role','button');btn.tabIndex=0;btn.title=item.available?'点击直接打开':'文件不在原位置，请重新选择文件';
        frag.appendChild(btn);
      }
      if(!matching.length)frag.appendChild(this.empty(q?'没有匹配的文件':'打开过的文件会列在这里'));
      this.list.replaceChildren(frag);
      document.getElementById('recentSummary').textContent=this.items.length?`共 ${this.items.length} 个文件`:'';
    }
    async handleClick(e) {
      const itemNode=e.target.closest?.('.recent-item');
      if(!itemNode||this.busy)return;
      const id=itemNode.dataset.id;
      const entry=this.items.find(x=>x.id===id);
      if(!entry)return;
      const action=e.target.closest('[data-action]')?.dataset.action||'open';
      if(action==='open' && !entry.available){this.toast('文件已移动或删除，可移除该记录后重新选择文件','warning');return;}
      this.busy=true;itemNode.classList.add('recent-working');
      try {
        if(action==='open') {
          await this.open(entry);
        } else {
          await this.api('/api/recent',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({action,id})});
          await this.load();
        }
      }catch(err){this.toast(err.message,'error');await this.load();}
      finally{this.busy=false;itemNode.classList.remove('recent-working');}
    }
  }
  window.ExcelLensRecentWorkbooks=RecentWorkbooks;
})();
