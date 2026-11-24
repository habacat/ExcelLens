(() => {
  'use strict';
  function buttonCommand(name, button, tags = '', shortcut = '') {
    return {name, button, tags, shortcut};
  }
  function windowStart(total, index, limit) {
    return Math.max(0, Math.min(index - Math.floor(limit / 2), total - limit));
  }
  class CommandPalette {
    constructor(actions, {node, sheets, selectSheet, canOpen, openDialog, closeDialog, toast, reveal}) {
      Object.assign(this, {actions, node, sheets, selectSheet, canOpen, openDialog, closeDialog, toast, reveal});
      this.visible = [];
      this.index = 0;
    }
    bind() {
      const search = this.node('commandSearch');
      search.addEventListener('input', () => { this.index = 0; this.render(); });
      search.addEventListener('keydown', event => {
        if (event.isComposing || event.keyCode === 229) return;
        if (event.key === 'Enter') {
          event.preventDefault();
          this.run(this.visible[this.index]);
        } else if (event.key === 'ArrowUp' || event.key === 'ArrowDown') {
          event.preventDefault();
          const next = this.index + (event.key === 'ArrowDown' ? 1 : -1);
          this.index = Math.max(0, Math.min(this.visible.length - 1, next));
          this.render();
        }
      });
    }
    unavailable(action) {
      if (!action?.button) return false;
      const target = this.node(action.button);
      return !target || target.disabled || target.hidden;
    }
    async run(action) {
      if (!action || this.unavailable(action)) return;
      this.closeDialog('commandDialog');
      try {
        await Promise.resolve().then(() => {
          if (this.unavailable(action)) return;
          if (!action.button) return action.run();
          // Bring the control's tab forward first, so its menu has an anchor.
          this.reveal?.(action.button);
          return this.node(action.button).click();
        });
      } catch (error) { this.toast(error.message, 'error'); }
    }
    open() {
      if (!this.canOpen()) return;
      this.index = 0;
      const input = this.node('commandSearch');
      input.value = '';
      this.render();
      this.openDialog('commandDialog');
      input.focus({preventScroll:true});
    }
    // Every word must appear in the name, search words or shortcut. Commands
    // whose own name matches come first: exact, then prefix, then anywhere.
    matches(query) {
      const terms = query.split(/\s+/).filter(Boolean);
      if (!terms.length) return [...this.actions];
      const ranked = [];
      for (const [order, action] of this.actions.entries()) {
        const name = action.name.toLocaleLowerCase();
        const all = [action.name, action.tags, action.shortcut].join(' ').toLocaleLowerCase();
        if (!terms.every(term => all.includes(term))) continue;
        const rank = name === query ? 0 : name.startsWith(terms[0]) ? 1 : terms.every(term => name.includes(term)) ? 2 : 3;
        ranked.push({action, rank, order});
      }
      return ranked.sort((a, b) => a.rank - b.rank || a.order - b.order).map(item => item.action);
    }
    render() {
      const query = this.node('commandSearch').value.toLocaleLowerCase().trim();
      this.visible = this.matches(query);
      if (query) {
        for (const sheet of this.sheets()) {
          if (sheet.name.toLocaleLowerCase().includes(query)) {
            this.visible.push({name:'打开 Sheet：'+sheet.name, run:() => this.selectSheet(sheet.name)});
          }
        }
      }
      this.index = Math.min(this.index, Math.max(0,this.visible.length-1));
      const limit = Math.max(60,this.actions.length), start = windowStart(this.visible.length,this.index,limit);
      const list = this.node('commandList'), doc = list.ownerDocument;
      const fragment = doc.createDocumentFragment();
      for (const [offset, action] of this.visible.slice(start,start+limit).entries()) {
        const selected = start + offset === this.index;
        const item = doc.createElement('button');
        item.type = 'button';
        item.className = 'command-entry' + (selected ? ' active' : '');
        item.setAttribute('role','option');
        item.setAttribute('aria-selected', String(selected));
        item.disabled = this.unavailable(action);
        if (action.button) item.dataset.commandButton = action.button;
        const label = doc.createElement('span'), shortcut = doc.createElement('span');
        label.textContent = action.name;
        shortcut.className = 'command-shortcut';
        shortcut.textContent = action.shortcut || '';
        item.append(label,shortcut);
        item.addEventListener('click', () => this.run(action));
        fragment.append(item);
      }
      if (!this.visible.length) {
        const empty = doc.createElement('div');
        empty.className = 'command-empty';
        empty.textContent = '没有匹配的功能或工作表';
        fragment.append(empty);
      }
      list.replaceChildren(fragment);
      list.querySelector('.active')?.scrollIntoView({block:'nearest'});
    }
  }
  window.ExcelLensCommands = {CommandPalette, buttonCommand, windowStart};
})();
