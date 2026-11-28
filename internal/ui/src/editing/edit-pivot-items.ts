/**
 * Pivot table calculated-item editor sub-controller.
 */
import { t as localize } from '../core/i18n.ts';
import type { PivotCalculatedItem, PivotInfo } from '../contracts/edit.ts';
import { requireButton, requireEl, requireInput, requireSelect } from '../core/dom.ts';

export type PivotCalculatedItemCommitFn = (item: PivotCalculatedItem) => void;

export class PivotCalculatedItems {
  private readonly field: HTMLSelectElement;
  private readonly name: HTMLInputElement;
  private readonly formula: HTMLInputElement;
  private readonly list: HTMLElement;
  private readonly save: HTMLButtonElement;
  private readonly remove: HTMLButtonElement;
  private info: PivotInfo | null = null;

  constructor(commit: PivotCalculatedItemCommitFn) {
    this.field = requireSelect('editCopyItemField');
    this.name = requireInput('editCopyItemName');
    this.formula = requireInput('editCopyItemFormula');
    this.list = requireEl('editCopyItemList');
    this.save = requireButton('editCopyItemApply');
    this.remove = requireButton('editCopyItemRemove');
    this.field.addEventListener('change', () => this.selectField());
    this.name.addEventListener('change', () => {
      const item = this.saved();
      if (item) this.formula.value = item.formula;
      this.controls();
    });
    for (const node of [this.name, this.formula]) {
      node.addEventListener('input', () => this.controls());
    }
    this.save.addEventListener('click', () => commit(this.input()));
    this.remove.addEventListener('click', () => commit({ ...this.input(), formula: '' }));
  }

  input(): PivotCalculatedItem {
    return {
      field: this.field.value,
      name: this.name.value.trim(),
      formula: this.formula.value.trim(),
    };
  }

  saved(): PivotCalculatedItem | undefined {
    const trimmed = this.name.value.trim();
    return this.info?.calculatedItems?.find(
      (item) => item.field === this.field.value && item.name === trimmed,
    );
  }

  bind(info: PivotInfo | null): void {
    if (this.info === info) return;
    this.info = info;
    const fields = [...(info?.axes?.rows ?? []), ...(info?.axes?.columns ?? [])];
    const previous = this.field.value;
    this.field.replaceChildren(...fields.map((name) => new Option(name, name)));
    if (fields.includes(previous)) this.field.value = previous;
    requireEl('editCopyItemTools').hidden = !fields.length;
    this.selectField();
  }

  selectField(): void {
    this.name.value = '';
    this.formula.value = '';
    const items = (this.info?.calculatedItems ?? []).filter(
      (item) => item.field === this.field.value,
    );
    this.list.replaceChildren(...items.map((item) => new Option(item.name)));
    this.controls();
  }

  controls(): void {
    const { field, name, formula } = this.input();
    const saved = this.saved();
    this.save.disabled =
      !field || !name || formula.length < 2 || !formula.startsWith('=') || saved?.formula === formula;
    this.save.textContent = saved ? localize('ui.editPivotItems.updateProject') : localize('ui.editPivotItems.addItems');
    this.remove.hidden = !saved;
  }
}
