(() => {
  'use strict';
  const el = name => document.getElementById('editCopy' + name);
  const modes = [
    ['topCount', '前 N 项'], ['bottomCount', '后 N 项'],
    ['topPercent', '累计占比最高'], ['bottomPercent', '累计占比最低'],
    ['topSum', '累计值最高'], ['bottomSum', '累计值最低'],
    ['valueGreaterThan', '大于'], ['valueGreaterThanOrEqual', '大于或等于'],
    ['valueLessThan', '小于'], ['valueLessThanOrEqual', '小于或等于'],
    ['valueEqual', '等于'], ['valueNotEqual', '不等于'],
    ['valueBetween', '介于'], ['valueNotBetween', '不介于'], ['clear', '清除筛选']
  ];
  class PivotValues {
    constructor(apply) {
      this.field = el('ValueField'); this.kind = el('ValueKind');
      this.start = el('ValueStart'); this.end = el('ValueEnd'); this.apply = el('ValueApply');
      this.kind.replaceChildren(...modes.map(([value, label]) => new Option(label, value)));
      this.field.addEventListener('change', () => this.select());
      this.kind.addEventListener('change', () => this.controls());
      for (const input of [this.start, this.end]) input.addEventListener('input', () => this.controls());
      this.apply.addEventListener('click', () => {
        const filter = this.read();
        if (filter) apply(filter);
      });
    }
    bind(info) {
      if (this.info === info) return;
      this.info = info;
      const fields = [...(info?.axes?.rows || []), ...(info?.axes?.columns || [])];
      const previous = this.field.value;
      this.field.replaceChildren(...fields.map(value => new Option(value, value)));
      if (fields.includes(previous)) this.field.value = previous;
      el('PivotValues').hidden = !fields.length;
      this.select();
    }
    select() {
      const filter = this.info?.valueFilters?.find(item => item.field === this.field.value);
      this.kind.value = filter?.kind || 'topCount';
      this.start.value = filter?.value ?? 10;
      this.end.value = filter?.second ?? '';
      this.controls();
    }
    read() {
      const field = this.field.value, kind = this.kind.value;
      if (!field || !kind) return null;
      if (kind === 'clear') return this.info?.valueFilters?.some(f => f.field === field) ? {field, kind} : null;
      const value = this.start.valueAsNumber;
      if (!Number.isFinite(value)) return null;
      if (kind.endsWith('Count') && (!Number.isInteger(value) || value < 1)) return null;
      if (kind.endsWith('Percent') && (value <= 0 || value > 100)) return null;
      if (kind.endsWith('Sum') && value <= 0) return null;
      const filter = {field, kind, value};
      if (kind.endsWith('Between')) {
        filter.second = this.end.valueAsNumber;
        if (!Number.isFinite(filter.second) || filter.second < value) return null;
      }
      return filter;
    }
    controls() {
      const kind = this.kind.value;
      this.start.hidden = kind === 'clear';
      this.end.hidden = !kind.endsWith('Between');
      el('PivotThresholdLabel').textContent = kind.endsWith('Count') ? '数量' : kind.endsWith('Percent') ? '占比（%）' : kind.endsWith('Between') ? '下限' : '数值';
      this.start.step = kind.endsWith('Count') ? '1' : 'any';
      this.apply.disabled = !this.read();
    }
  }
  window.ExcelLensPivotValues = PivotValues;
})();
