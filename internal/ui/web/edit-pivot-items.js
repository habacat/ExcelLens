(() => {
  'use strict';
  const el = key => document.getElementById('editCopyItem' + key);
  class PivotCalculatedItems {
    constructor(commit) {
      this.field = el('Field'); this.name = el('Name'); this.formula = el('Formula');
      this.list = el('List'); this.save = el('Apply'); this.remove = el('Remove');
      this.field.addEventListener('change', () => this.selectField());
      this.name.addEventListener('change', () => {
        const item = this.saved();
        if (item) this.formula.value = item.formula;
        this.controls();
      });
      for (const node of [this.name, this.formula]) node.addEventListener('input', () => this.controls());
      this.save.addEventListener('click', () => commit(this.input()));
      this.remove.addEventListener('click', () => commit({...this.input(), formula:''}));
    }
    input() {
      return {field:this.field.value, name:this.name.value.trim(), formula:this.formula.value.trim()};
    }
    saved() {
      return this.info?.calculatedItems?.find(item => item.field === this.field.value && item.name === this.name.value.trim());
    }
    bind(info) {
      if (this.info === info) return;
      this.info = info;
      const fields = [...(info?.axes?.rows || []), ...(info?.axes?.columns || [])];
      const previous = this.field.value;
      this.field.replaceChildren(...fields.map(name => new Option(name, name)));
      if (fields.includes(previous)) this.field.value = previous;
      el('Tools').hidden = !fields.length;
      this.selectField();
    }
    selectField() {
      this.name.value = ''; this.formula.value = '';
      const items = (this.info?.calculatedItems || []).filter(item => item.field === this.field.value);
      this.list.replaceChildren(...items.map(item => new Option(item.name)));
      this.controls();
    }
    controls() {
      const {field, name, formula} = this.input(), saved = this.saved();
      this.save.disabled = !field || !name || formula.length < 2 || !formula.startsWith('=') || saved?.formula === formula;
      this.save.textContent = saved ? '更新项目' : '添加项目';
      this.remove.hidden = !saved;
    }
  }
  window.ExcelLensPivotCalculatedItems = PivotCalculatedItems;
})();
