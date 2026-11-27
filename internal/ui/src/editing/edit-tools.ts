/**
 * Edit copy tool tabs, structural operations (rows/columns/sheets/merges/arrays/tables),
 * and pivot/chart object editing coordinator.
 */
import type { ActivityHandle } from '../contracts/common.ts';
import type {
  EditAddress,
  EditOperation,
  EditReadResponse,
  EditSheetMeta,
  EditStateResponse,
  EditStructure,
  PivotInfo,
  TableRange,
} from '../contracts/edit.ts';
import type { SelectedCell } from '../contracts/workbook.ts';
import { errorMessage } from '../core/display-format.ts';
import { requireButton, requireEl, requireInput, requireSelect } from '../core/dom.ts';
import { parseEditTarget } from './edit-copy-model.ts';
import { PivotLayout } from './edit-pivot.ts';
import { PivotCriteria, type PivotCriteriaChange } from './edit-pivot-criteria.ts';
import { PivotCalculatedItems } from './edit-pivot-items.ts';
import { PivotMeasures } from './edit-pivot-measures.ts';
import { PivotValues } from './edit-pivot-values.ts';
import { EditSheetTabs } from './edit-sheet-tabs.ts';
import { decodePivotInfo } from '../core/http-decoders.generated.ts';

const RANGE_KINDS = new Set([
  'mergeCells',
  'unmergeCells',
  'fixedArray',
  'spillFormula',
  'clearArray',
  'resizeTable',
]);
const ARRAY_FORMULA_KINDS = new Set(['fixedArray', 'spillFormula']);
const PIVOT_RETURN_KINDS = new Set([
  'configurePivot',
  'refreshPivot',
  'pivotValueFilter',
  'pivotCalculatedItem',
  'pivotLabelFilter',
  'pivotDateGroup',
  'pivotDateFilter',
]);

type ToolTabKey = 'Copy' | 'Paste' | 'Fill' | 'Rules' | 'Structure' | 'Objects';

interface ToolTabEntry {
  readonly tab: HTMLButtonElement;
  readonly option: HTMLOptionElement;
  readonly pane: HTMLElement;
}

interface PivotMetaLoad {
  readonly key: string;
  pending: boolean;
  readonly controller: AbortController;
  info?: PivotInfo;
  error?: string;
}

export interface EditToolsOwner {
  readonly data: EditStateResponse | null;
  readonly cell: EditReadResponse | null;
  readonly shape: EditSheetMeta | undefined;
  readonly editable: boolean;
  readonly invalidated: boolean;
  readonly tools: EditTools;
  readonly grid: {
    readonly active: boolean;
    readonly blocked: boolean;
    readonly shape: EditSheetMeta | undefined;
    readonly view: {
      range(): string;
      positionMenu(menu: HTMLElement, x: number, y: number): void;
      switchSheet(sheet: string): Promise<void> | void;
    };
    act(
      next:
        | ((activity?: ActivityHandle | null) => Promise<boolean | undefined> | boolean | undefined)
        | null,
      confirmDraft?: boolean,
      label?: string,
    ): Promise<boolean>;
  };
  context(): { readonly sheet: string | null };
  colName(col: number): string;
  post(command: string, body: unknown, signal?: AbortSignal): Promise<unknown>;
  controls(): void;
  clearPreview(): void;
  close(): void;
  advanced(tool?: string): unknown;
  commit(
    action: 'structure',
    changes: undefined,
    options: { readonly structure: EditStructure; readonly direct?: boolean },
  ): Promise<boolean | undefined>;
  commitStructure(structure: EditStructure): Promise<boolean | undefined>;
}

function el(suffix: string): HTMLElement {
  return requireEl(`editCopy${suffix}`);
}

function inputEl(suffix: string): HTMLInputElement {
  return requireInput(`editCopy${suffix}`);
}

function selectEl(suffix: string): HTMLSelectElement {
  return requireSelect(`editCopy${suffix}`);
}

function buttonEl(suffix: string): HTMLButtonElement {
  return requireButton(`editCopy${suffix}`);
}

function setDisabled(node: HTMLElement, disabled: boolean): void {
  if (
    node instanceof HTMLButtonElement ||
    node instanceof HTMLInputElement ||
    node instanceof HTMLSelectElement ||
    node instanceof HTMLTextAreaElement ||
    node instanceof HTMLFieldSetElement
  ) {
    node.disabled = disabled;
  }
}

function isDisabled(node: HTMLElement): boolean {
  if (
    node instanceof HTMLButtonElement ||
    node instanceof HTMLInputElement ||
    node instanceof HTMLSelectElement ||
    node instanceof HTMLTextAreaElement ||
    node instanceof HTMLFieldSetElement
  ) {
    return node.disabled;
  }
  return false;
}

function isToolTabKey(value: string): value is ToolTabKey {
  return (
    value === 'Copy' ||
    value === 'Paste' ||
    value === 'Fill' ||
    value === 'Rules' ||
    value === 'Structure' ||
    value === 'Objects'
  );
}

type StructurePayload = Omit<EditStructure, 'sheet'>;

export class EditTools {
  private readonly editor: EditToolsOwner;
  public readonly sheetTabs: EditSheetTabs;
  public readonly calculatedItems: PivotCalculatedItems;
  public readonly valueFilters: PivotValues;
  public readonly pivotLayout: PivotLayout;
  public readonly measures: PivotMeasures;
  public readonly criteria: PivotCriteria;
  public selected: ToolTabKey = 'Copy';
  public contextualPivot = false;
  public chooseRangeKind = false;
  private readonly tabs: HTMLButtonElement[];
  private readonly options: Map<ToolTabKey, ToolTabEntry>;
  private pivots: PivotInfo[] = [];
  private charts = false;
  private meta: PivotMetaLoad | null = null;

  constructor(editor: EditToolsOwner) {
    this.editor = editor;
    this.sheetTabs = new EditSheetTabs(editor);
    this.calculatedItems = new PivotCalculatedItems((item) => {
      void this.commit({
        kind: 'pivotCalculatedItem',
        pivot: selectEl('Pivot').value,
        calculatedItem: item,
      });
    });
    this.valueFilters = new PivotValues((filter) => {
      void this.commit({
        kind: 'pivotValueFilter',
        pivot: selectEl('Pivot').value,
        valueFilter: filter,
      });
    });
    this.pivotLayout = new PivotLayout(editor);
    this.measures = new PivotMeasures(editor, this.pivotLayout);
    this.criteria = new PivotCriteria((change: PivotCriteriaChange) => {
      void this.commit({
        ...change,
        pivot: selectEl('Pivot').value,
      });
    });

    const toolSelect = selectEl('ToolSelect');
    this.tabs = [...el('Tools').querySelectorAll<HTMLButtonElement>('[role="tab"]')];
    this.options = new Map<ToolTabKey, ToolTabEntry>();
    for (const tab of this.tabs) {
      const rawKey = tab.id.slice('editCopyTab'.length);
      if (!isToolTabKey(rawKey)) continue;
      const option = new Option(tab.textContent, rawKey);
      toolSelect.add(option);
      tab.addEventListener('click', () => this.select(rawKey));
      tab.addEventListener('keydown', (event: KeyboardEvent) => this.navigate(event, tab));
      const controlsId = tab.getAttribute('aria-controls') ?? '';
      this.options.set(rawKey, { tab, option, pane: requireEl(controlsId) });
    }
    toolSelect.addEventListener('change', () => {
      if (isToolTabKey(toolSelect.value)) this.select(toolSelect.value);
    });
    selectEl('StructureKind').addEventListener('change', () => {
      this.structureFields(editor.cell);
      editor.controls();
    });
    for (const id of [
      'NewName',
      'At',
      'Count',
      'Before',
      'Range',
      'ArrayFormula',
      'ReplaceContents',
    ]) {
      el(id).addEventListener('input', () => {
        if (id === 'Range') inputEl('ReplaceContents').checked = false;
        editor.controls();
      });
    }
    selectEl('TableName').addEventListener('change', () => {
      this.structureFields(editor.cell);
      editor.controls();
    });
    buttonEl('ApplyStructure').addEventListener('click', () => {
      void this.commit(this.structureInput());
    });
    selectEl('Pivot').addEventListener('change', () => {
      this.pivotFields();
      editor.controls();
    });
    selectEl('PivotFilter').addEventListener('change', () => editor.controls());
    inputEl('PivotItem').addEventListener('input', () => editor.controls());
    buttonEl('PivotRefresh').addEventListener('click', () => {
      void this.commit({ kind: 'refreshPivot', pivot: selectEl('Pivot').value });
    });
    buttonEl('PivotApply').addEventListener('click', () => {
      const filterMode = selectEl('PivotFilter').value;
      void this.commit({
        kind: 'configurePivot',
        pivot: selectEl('Pivot').value,
        axes: this.pivotLayout.axes ?? undefined,
        measures: this.measures.items,
        valuesAxis: this.measures.items.length > 1 ? this.measures.axis : undefined,
        filterMode,
        item: filterMode === 'item' ? inputEl('PivotItem').value : '',
      });
    });
    buttonEl('Charts').addEventListener('click', () => {
      void this.commit({ kind: 'refreshCharts' });
    });
  }

  async commit(change: StructurePayload | null): Promise<void> {
    if (!change) return;
    const returnToGrid =
      change.kind.endsWith('Sheet') ||
      Boolean(change.range) ||
      (this.contextualPivot && PIVOT_RETURN_KINDS.has(change.kind));
    const saved = await this.editor.commitStructure({
      ...change,
      sheet: selectEl('Sheet').value,
    });
    if (saved && returnToGrid) this.editor.close();
  }

  tableAt(cell: EditReadResponse | SelectedCell | EditAddress | null | undefined): TableRange | null {
    const address = cell && 'address' in cell ? cell.address : cell;
    const shape = this.editor.grid.shape;
    if (!address || !shape) return null;
    return (
      this.editor.data?.tables?.find((table) => {
        if (table.sheet !== shape.name) return false;
        const parts = table.ref.split(':').map((ref) => parseEditTarget(ref, shape));
        const start = parts[0];
        const end = parts[1] ?? start;
        if (!start || !end) return false;
        return (
          address.row >= start.row &&
          address.row <= end.row &&
          address.col >= start.col &&
          address.col <= end.col
        );
      }) ?? null
    );
  }

  entryRefAfterResize(
    sheet: string | null,
    ref: string | undefined,
    operation: EditOperation,
    next: EditStateResponse,
  ): string | undefined {
    if (!operation.expandTable || !operation.changes?.length || !ref) return ref;
    const first = operation.changes[0];
    const shape = this.editor.grid.shape;
    if (!first || !shape || sheet !== first.sheet) return ref;
    const point = parseEditTarget(ref, shape);
    const old = this.editor.data?.tables?.find((table) => {
      if (table.sheet !== sheet || !table.totals) return false;
      const parts = table.ref.split(':').map((value) => parseEditTarget(value, shape));
      const lo = parts[0];
      const hi = parts[1] ?? lo;
      if (!lo || !hi) return false;
      return (
        first.row === hi.row + 1 &&
        first.col >= lo.col &&
        first.col <= hi.col &&
        (operation.changes?.every((c) => c.row >= first.row) ?? false)
      );
    });
    const current = old ? next.tables?.find((t) => t.name === old.name) : undefined;
    return current && old && current.ref !== old.ref && point.row === first.row
      ? `${this.editor.colName(point.col)}${point.row - 1}`
      : ref;
  }

  pivotAt(cell: SelectedCell | EditAddress | null | undefined): PivotInfo | null {
    if (!cell) return null;
    const sheet = this.editor.context().sheet;
    return (
      this.editor.data?.pivots?.find(
        (pivot) =>
          pivot.sheet === sheet &&
          (pivot.areas?.some(
            (area) =>
              cell.row >= area.start.row &&
              cell.row <= area.endRow &&
              cell.col >= area.start.col &&
              cell.col <= area.endCol,
          ) ??
            false),
      ) ?? null
    );
  }

  openPivot(pivot: PivotInfo): void {
    this.selected = 'Objects';
    selectEl('Pivot').value = pivot.name;
    this.pivotFields();
  }

  select(key: ToolTabKey): void {
    this.selected = key;
    this.editor.clearPreview();
    this.editor.controls();
  }

  navigate(event: KeyboardEvent, tab: HTMLButtonElement): void {
    const visible = this.tabs.filter((item) => !item.hidden);
    const index = visible.indexOf(tab);
    const count = visible.length;
    if (!count) return;
    let next: number | undefined;
    if (event.key === 'ArrowRight') next = (index + 1) % count;
    else if (event.key === 'ArrowLeft') next = (index + count - 1) % count;
    else if (event.key === 'Home') next = 0;
    else if (event.key === 'End') next = count - 1;
    if (next === undefined) return;
    const target = visible[next];
    if (!target) return;
    event.preventDefault();
    target.click();
    target.focus();
  }

  controls(blocked: boolean): void {
    const editor = this.editor;
    const excel = Boolean(editor.data && !editor.data.encoding);
    const structure = excel && editor.grid.active && Boolean(editor.shape);
    const objects = structure && (this.pivots.length > 0 || this.charts);
    const available: Readonly<Record<ToolTabKey, boolean>> = {
      Copy: editor.editable,
      Paste: editor.editable,
      Fill: editor.editable,
      Rules: editor.editable && excel && !editor.data?.structured,
      Structure: structure,
      Objects: objects,
    };
    const keys: readonly ToolTabKey[] = ['Copy', 'Paste', 'Fill', 'Rules', 'Structure', 'Objects'];
    const active: ToolTabKey | undefined = available[this.selected]
      ? this.selected
      : keys.find((key) => available[key]);
    el('Batch').hidden = !active;
    for (const [key, { tab, option, pane }] of this.options) {
      const hidden = !available[key];
      tab.hidden = hidden;
      option.hidden = hidden;
      option.disabled = hidden;
      tab.disabled = blocked;
      const isSelected = key === active;
      tab.setAttribute('aria-selected', String(isSelected));
      tab.tabIndex = isSelected ? 0 : -1;
      pane.hidden = !isSelected;
    }
    const toolSelect = selectEl('ToolSelect');
    toolSelect.value = active ?? '';
    toolSelect.disabled = blocked;
    if (active === 'Objects' && !blocked) void this.resolvePivotLayout();
    for (const id of [
      'StructureKind',
      'TableName',
      'NewName',
      'At',
      'Count',
      'Before',
      'Range',
      'ArrayFormula',
      'ReplaceContents',
      'Pivot',
      'PivotFields',
      'PivotFilter',
      'PivotItem',
      'PivotRefresh',
      'PivotApply',
      'Charts',
    ]) {
      setDisabled(el(id), blocked || editor.invalidated);
    }
    const pivotFilter = selectEl('PivotFilter');
    const pivotFilterParent = pivotFilter.parentElement;
    if (pivotFilterParent) pivotFilterParent.hidden = !this.pivotLayout.axes?.rows.length;
    const pivotItem = inputEl('PivotItem');
    pivotItem.hidden = Boolean(pivotFilterParent?.hidden) || pivotFilter.value !== 'item';
    if (!el('PivotReason').hidden) buttonEl('PivotRefresh').disabled = true;
    const pivotFields = el('PivotFields');
    if (
      active === 'Objects' &&
      this.meta?.key === this.pivotKey() &&
      (this.meta.pending || this.meta.error)
    ) {
      setDisabled(pivotFields, true);
      buttonEl('PivotRefresh').disabled = true;
    }
    const applyingLayout = active === 'Objects' && !el('Work').hidden && !pivotFields.hidden;
    const fieldsDisabled = isDisabled(pivotFields);
    this.criteria.dateFilter.footer(applyingLayout, fieldsDisabled);
    buttonEl('PivotApply').disabled = fieldsDisabled || (!pivotItem.hidden && !pivotItem.value);
    const saveBtn = buttonEl('Save');
    saveBtn.classList.toggle('primary-button', !applyingLayout);
    saveBtn.classList.toggle('mini-button', applyingLayout);
    buttonEl('ApplyStructure').disabled = blocked || editor.invalidated || !this.structureInput();
  }

  bindCell(cell: EditReadResponse | null): void {
    if (this.meta?.error) this.meta = null;
    const sheet = selectEl('Sheet').value;
    const tables = (this.editor.data?.tables ?? []).filter((table) => table.sheet === sheet);
    const table = this.tableAt(cell);
    const tableName = selectEl('TableName');
    const previousTable = tableName.value;
    tableName.replaceChildren(...tables.map((t) => new Option(t.name, t.name)));
    if (table) tableName.value = table.name;
    else if (tables.some((t) => t.name === previousTable)) tableName.value = previousTable;
    const kindSelect = selectEl('StructureKind');
    const resize = kindSelect.querySelector<HTMLOptionElement>('[value="resizeTable"]');
    if (resize) {
      resize.hidden = !tables.length;
      resize.disabled = !tables.length;
    }
    if (!tables.length && kindSelect.value === 'resizeTable') kindSelect.value = 'mergeCells';
    if (cell && this.chooseRangeKind) {
      this.chooseRangeKind = false;
      if (cell.array) kindSelect.value = cell.array.dynamic ? 'spillFormula' : 'fixedArray';
    }
    this.structureFields(cell);
    this.pivots = (this.editor.data?.pivots ?? []).filter((pivot) => pivot.sheet === sheet);
    this.charts = Boolean(this.editor.data?.chartSheets?.includes(sheet));
    buttonEl('Charts').hidden = !this.charts;
    el('PivotTools').hidden = !this.pivots.length;
    const pivotSelect = selectEl('Pivot');
    const previous = pivotSelect.value;
    pivotSelect.replaceChildren(...this.pivots.map((p) => new Option(p.name, p.name)));
    if (this.pivots.some((p) => p.name === previous)) pivotSelect.value = previous;
    this.pivotFields();
  }

  pivotFields(): void {
    const saved = this.pivots.find((p) => p.name === selectEl('Pivot').value);
    const meta = this.meta?.key === this.pivotKey() ? this.meta : null;
    const info = meta?.info ?? saved ?? null;
    const reason = el('PivotReason');
    reason.textContent = meta?.error ?? info?.refreshReason ?? '';
    reason.hidden = !reason.textContent;
    const fields = el('PivotFields');
    fields.hidden = !info?.data || Boolean(info.refreshReason);
    const bound = fields.hidden ? null : info;
    this.pivotLayout.bind(bound);
    this.measures.bind(bound);
    this.criteria.bind(bound);
    this.valueFilters.bind(bound);
    this.calculatedItems.bind(bound);
    if (fields.hidden) return;
    selectEl('PivotFilter').value = 'keep';
    const pivotItem = inputEl('PivotItem');
    pivotItem.value = '';
    pivotItem.hidden = true;
  }

  pivotKey(): string {
    const doc = this.editor.data?.document;
    return JSON.stringify([doc?.id, doc?.revision, selectEl('Sheet').value, selectEl('Pivot').value]);
  }

  async resolvePivotLayout(): Promise<void> {
    const saved = this.pivots.find((p) => p.name === selectEl('Pivot').value);
    const key = this.pivotKey();
    const doc = this.editor.data?.document;
    if (!doc || !saved?.axes || saved.refreshReason || this.meta?.key === key) return;
    this.meta?.controller.abort();
    const meta: PivotMetaLoad = { key, pending: true, controller: new AbortController() };
    this.meta = meta;
    try {
      meta.info = await this.editor.post(
        'pivot-layout',
        {
          document: { id: doc.id, binding: doc.binding },
          revision: doc.revision,
          sheet: saved.sheet,
          pivot: saved.name,
        },
        meta.controller.signal,
      ).then(decodePivotInfo);
    } catch (error: unknown) {
      meta.error = errorMessage(error);
    }
    if (this.meta !== meta || this.pivotKey() !== key) return;
    meta.pending = false;
    this.pivotFields();
    this.editor.controls();
  }

  structureFields(cell: EditReadResponse | null): void {
    const kindSelect = selectEl('StructureKind');
    const kind = kindSelect.value;
    const sheetAction = kind.endsWith('Sheet');
    const nameRequired = sheetAction && kind !== 'deleteSheet';
    const rows = kind.endsWith('Rows');
    const range = RANGE_KINDS.has(kind);
    el('RangeField').hidden = !range;
    el('TableField').hidden = kind !== 'resizeTable';
    el('ArrayFormulaField').hidden = !ARRAY_FORMULA_KINDS.has(kind);
    inputEl('ArrayFormula').value =
      cell?.array?.formula ?? (cell?.value.kind === 'formula' ? cell.value.raw : '');
    el('MergeConsent').hidden = kind !== 'mergeCells' && kind !== 'fixedArray';
    el('ConsentLabel').textContent =
      kind === 'fixedArray' ? '替换选区内已有内容' : '若有多个值，仅保留首个有内容的格';
    inputEl('ReplaceContents').checked = false;
    const sheetValue = selectEl('Sheet').value;
    const selected =
      this.editor.grid.active && this.editor.context().sheet === sheetValue
        ? this.editor.grid.view.range()
        : (cell?.ref ?? '');
    const fullArray = cell?.array?.range;
    const rangeInput = inputEl('Range');
    rangeInput.value =
      fullArray &&
      (!selected.includes(':') || selected === fullArray) &&
      (ARRAY_FORMULA_KINDS.has(kind) || kind === 'clearArray')
        ? kind === 'spillFormula'
          ? (fullArray.split(':')[0] ?? '')
          : fullArray
        : selected;
    if (kind === 'resizeTable') {
      const tableName = selectEl('TableName').value;
      rangeInput.value =
        this.editor.data?.tables?.find((t) => t.name === tableName)?.ref ?? '';
    }
    el('NewNameField').hidden = !nameRequired;
    el('AtField').hidden = sheetAction || range;
    el('CountField').hidden = sheetAction || range;
    el('BeforeField').hidden = !kind.startsWith('move');
    el('AtLabel').textContent = rows ? '起始行号' : '起始列号';
    inputEl('At').value = cell ? String(rows ? cell.address.row : cell.address.col) : '';
    const newName = inputEl('NewName');
    newName.value = sheetValue;
    if (kind === 'newSheet' || kind === 'copySheet') {
      newName.value = this.sheetTabs.nextName(kind, sheetValue);
    }
    buttonEl('ApplyStructure').textContent = kindSelect.selectedOptions[0]?.textContent ?? '';
  }

  structureInput(): StructurePayload | null {
    const sheet = selectEl('Sheet').value;
    const kind = selectEl('StructureKind').value;
    if (!sheet) return null;
    if (RANGE_KINDS.has(kind)) {
      const raw = inputEl('Range').value.trim().toUpperCase();
      const range =
        kind === 'resizeTable'
          ? raw
              .split(':')
              .map((ref) => ref.trim().replace(/^\$?([A-Z]{1,3})\$?([1-9]\d*)$/, '$1$2'))
              .join(':')
          : raw;
      const parts = range.split(':');
      if (parts.length > 2) return null;
      try {
        const targets = parts.map((ref) => parseEditTarget(ref, this.editor.shape));
        const a = targets[0];
        const b = targets[1] ?? a;
        if (
          !a ||
          !b ||
          a.row > b.row ||
          a.col > b.col ||
          (kind === 'mergeCells' && a.row === b.row && a.col === b.col)
        ) {
          return null;
        }
      } catch {
        return null;
      }
      let name: string | undefined;
      if (kind === 'resizeTable') {
        const selectedTable = selectEl('TableName').value;
        const table = this.editor.data?.tables?.find(
          (t) => t.sheet === sheet && t.name === selectedTable,
        );
        if (!table || table.ref === range) return null;
        name = table.name;
      }
      let formula: string | undefined;
      if (ARRAY_FORMULA_KINDS.has(kind)) {
        formula = inputEl('ArrayFormula').value.trim();
        if (!formula.startsWith('=') || formula.length < 2) return null;
      }
      const replaceContents = !el('MergeConsent').hidden && inputEl('ReplaceContents').checked;
      return {
        kind,
        range,
        ...(name !== undefined ? { name } : {}),
        ...(formula !== undefined ? { formula } : {}),
        ...(replaceContents ? { replaceContents: true } : {}),
      };
    }
    if (kind === 'deleteSheet') {
      return (this.editor.data?.sheets.length ?? 0) > 1 ? { kind } : null;
    }
    if (kind.endsWith('Sheet')) {
      const name = inputEl('NewName').value;
      return name.trim() && name !== sheet ? { kind, name } : null;
    }
    const at = inputEl('At').valueAsNumber;
    const count = inputEl('Count').valueAsNumber;
    const limit = kind.endsWith('Rows') ? this.editor.shape?.rows : this.editor.shape?.cols;
    if (
      !limit ||
      !Number.isInteger(at) ||
      !Number.isInteger(count) ||
      at < 1 ||
      count < 1 ||
      at + count - 1 > limit
    ) {
      return null;
    }
    if (kind.startsWith('move')) {
      const before = inputEl('Before').valueAsNumber;
      if (
        !Number.isInteger(before) ||
        before < 1 ||
        before > limit + 1 ||
        (before >= at && before <= at + count)
      ) {
        return null;
      }
      return { kind, at, count, before };
    }
    return { kind, at, count };
  }
}
