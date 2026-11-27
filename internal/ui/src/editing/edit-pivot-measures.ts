/**
 * Pivot table data measures editor: field/aggregation rows, reordering,
 * custom captions, and multi-measure values-axis placement.
 */
import type { PivotInfo } from '../contracts/edit.ts';
import { requireEl, requireSelect } from '../core/dom.ts';
import type { PivotLayout } from './edit-pivot.ts';

export interface MutablePivotMeasure {
  field: string;
  aggregate: string;
  caption?: string;
  sourceIndex?: number;
}

export interface MutablePivotValuesAxis {
  axis: 'rows' | 'columns';
  position: number;
}

export interface PivotMeasuresOwner {
  controls(): void;
}

const AGGREGATE_METHODS: Readonly<Record<string, string>> = {
  sum: '求和',
  count: '计数',
  average: '平均值',
  min: '最小值',
  max: '最大值',
  product: '乘积',
  countNums: '数值计数',
  stdDev: '样本标准差',
  stdDevp: '总体标准差',
  var: '样本方差',
  varp: '总体方差',
};

export class PivotMeasures {
  private readonly editor: PivotMeasuresOwner;
  private readonly layout: PivotLayout;
  private info: PivotInfo | null = null;
  public items: MutablePivotMeasure[] = [];
  public axis: MutablePivotValuesAxis = { axis: 'columns', position: 0 };
  private positionBinding = '';

  constructor(editor: PivotMeasuresOwner, layout: PivotLayout) {
    this.editor = editor;
    this.layout = layout;
    const addMeasure = requireSelect('editCopyPivotAddMeasure');
    addMeasure.onchange = (): void => {
      if (addMeasure.value) {
        this.items.push({ field: addMeasure.value, aggregate: 'sum', sourceIndex: -1 });
        this.draw();
        this.editor.controls();
      }
    };
    const valuesAxis = requireSelect('editCopyPivotValuesAxis');
    valuesAxis.onchange = (): void => {
      const nextAxis: 'rows' | 'columns' = valuesAxis.value === 'rows' ? 'rows' : 'columns';
      const len = this.layout.axes?.[nextAxis].length ?? 0;
      this.axis = { axis: nextAxis, position: len };
      this.positions();
    };
    const valuesPos = requireSelect('editCopyPivotValuesPosition');
    valuesPos.onchange = (): void => {
      this.axis.position = Number(valuesPos.value);
    };
  }

  bind(info: PivotInfo | null): void {
    this.info = info;
    if (info?.measures) {
      this.items = info.measures.map((m) => ({ ...m }));
    } else if (info?.data) {
      this.items = [{ field: info.data, aggregate: info.aggregate ?? 'sum' }];
    } else {
      this.items = [];
    }
    if (info?.valuesAxis) {
      this.axis = {
        axis: info.valuesAxis.axis === 'rows' ? 'rows' : 'columns',
        position: info.valuesAxis.position,
      };
    } else {
      this.axis = { axis: 'columns', position: this.layout.axes?.columns.length ?? 0 };
    }
    this.draw();
  }

  positions(): void {
    requireEl('editCopyPivotValuesPositionFields').hidden = this.items.length < 2;
    if (this.items.length < 2) return;
    const fields = this.layout.axes?.[this.axis.axis] ?? [];
    this.axis.position = Math.min(fields.length, this.axis.position);
    const binding = JSON.stringify([this.axis, fields]);
    if (this.positionBinding === binding) return;
    this.positionBinding = binding;
    requireSelect('editCopyPivotValuesAxis').value = this.axis.axis;
    const options = fields.map((name, i) => new Option(`${name}之前`, String(i)));
    options.push(new Option(fields.length ? '最后' : '首位', String(fields.length)));
    const posSelect = requireSelect('editCopyPivotValuesPosition');
    posSelect.replaceChildren(...options);
    posSelect.value = String(this.axis.position);
  }

  draw(): void {
    const root = requireEl('editCopyPivotMeasures');
    root.replaceChildren();
    const availableFields = this.info?.fields ?? [];
    for (const [i, item] of this.items.entries()) {
      const row = document.createElement('div');
      row.className = 'edit-pivot-measure';
      const field = document.createElement('select');
      const method = document.createElement('select');
      field.setAttribute('aria-label', `值字段 ${i + 1}`);
      method.setAttribute('aria-label', `汇总方式 ${i + 1}`);
      if (i === 0) {
        field.id = 'editCopyPivotData';
        method.id = 'editCopyPivotAggregate';
      }
      for (const f of availableFields) field.add(new Option(f, f));
      field.value = item.field;
      for (const [v, s] of Object.entries(AGGREGATE_METHODS)) method.add(new Option(s, v));
      method.value = item.aggregate;
      const customize = document.createElement('details');
      customize.className = 'edit-measure-caption';
      const summary = document.createElement('summary');
      summary.textContent = item.caption || '自定义标题';
      const input = document.createElement('input');
      input.maxLength = 255;
      input.placeholder = '自动标题';
      input.value = item.caption || '';
      input.setAttribute('aria-label', `值字段标题 ${i + 1}`);
      input.oninput = (): void => {
        item.caption = input.value || undefined;
        summary.textContent = item.caption || '自定义标题';
      };
      const change = (): void => {
        if (item.field !== field.value) item.sourceIndex = -1;
        item.field = field.value;
        item.aggregate = method.value;
        delete item.caption;
        input.value = '';
        summary.textContent = '自定义标题';
        this.editor.controls();
      };
      field.onchange = change;
      method.onchange = change;
      const actions = document.createElement('div');
      actions.className = 'edit-pivot-order';
      const move = (delta: number): void => {
        const j = i + delta;
        const a = this.items[i];
        const b = this.items[j];
        if (a && b) {
          this.items[i] = b;
          this.items[j] = a;
        }
        this.draw();
        this.editor.controls();
      };
      const up = this.layout.action(`上移值字段 ${item.field}`, 'm6 14 6-6 6 6', () => move(-1));
      up.disabled = i === 0;
      const down = this.layout.action(`下移值字段 ${item.field}`, 'm6 10 6 6 6-6', () => move(1));
      down.disabled = i === this.items.length - 1;
      const remove = this.layout.action(
        `移除值字段 ${item.field}`,
        'm7 7 10 10 M17 7 7 17',
        () => {
          this.items.splice(i, 1);
          this.draw();
          this.editor.controls();
        },
      );
      remove.disabled = this.items.length === 1;
      customize.append(summary, input);
      actions.append(up, down, remove);
      row.append(field, method, actions, customize);
      root.append(row);
    }
    requireSelect('editCopyPivotAddMeasure').replaceChildren(
      new Option('添加值字段…', ''),
      ...availableFields.map((f) => new Option(f, f)),
    );
    this.positions();
  }
}
