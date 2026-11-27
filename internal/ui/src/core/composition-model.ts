/**
 * Pure field alignment, search, validation, and type-warning model shared by
 * Multi-Table Composition and FieldPicker (used by both Composition and Relations).
 */
import type {
  CompositionDefinition,
  CompositionField,
  CompositionSampleType,
  CompositionSourceInfo,
  TableRef,
} from '../contracts/analysis.ts';
import type { CompositionLimits } from '../contracts/formats.ts';
import { columnName } from './cell-address.ts';

export interface CompositionMappingRow {
  name: string;
  columns: (number | null)[];
  expected?: CompositionField;
}

export interface CompositionInputSlot {
  info?: CompositionSourceInfo | null;
}

export interface SearchableField {
  readonly column: number;
  readonly name: string;
}

export interface FieldSearchPage<T extends SearchableField> {
  readonly count: number;
  readonly items: readonly T[];
}

export function cloneValue<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T;
}

export const labelColumn = columnName;

export function suggestFieldMatches(
  expected: readonly CompositionField[],
  actual: readonly CompositionField[],
): (number | null)[] {
  const matches = new Map<string, CompositionField | null>();
  for (const field of actual) {
    const hash = field.headerHash;
    if (!hash) continue;
    matches.set(hash, matches.has(hash) ? null : field);
  }
  return expected.map((field) => {
    const hash = field.headerHash;
    if (!hash || !field.headerLoaded || !field.headerUnique || field.truncated) return null;
    const match = matches.get(hash);
    if (!match?.headerLoaded || !match.headerUnique || match.truncated) return null;
    return match.column;
  });
}

export function initialCompositionRows(
  fields: readonly CompositionField[],
  columns: number,
): CompositionMappingRow[] {
  const used = new Set<string>();
  return fields.slice(0, columns).map((field) => {
    const base = (field.name || `${columnName(field.column)} 列`).trim();
    let name = base;
    let suffix = 1;
    while (used.has(name)) {
      name = `${base} ${++suffix}`;
    }
    used.add(name);
    return { name, columns: [field.column], expected: field };
  });
}

export function validateComposition(
  name: string,
  rows: readonly CompositionMappingRow[],
  inputs: readonly CompositionInputSlot[],
  limits: Pick<CompositionLimits, 'sources' | 'columns'>,
): string {
  const trimmedName = name.trim();
  if (!trimmedName || Array.from(trimmedName).length > 80) {
    return '请输入联合来源名称（最多80字）';
  }
  if (
    inputs.length < 2 ||
    inputs.length > limits.sources ||
    inputs.some((input) => !input.info)
  ) {
    return '请至少选择两份来源并读取字段';
  }
  if (!rows.length || rows.length > limits.columns) {
    return '请选择结果字段';
  }
  const names = new Set<string>();
  const sources = new Set<string>();
  for (const input of inputs) {
    const info = input.info;
    if (!info) return '请至少选择两份来源并读取字段';
    const ref: TableRef = cloneValue(info.ref);
    delete ref.stamp;
    const key = JSON.stringify(ref);
    if (sources.has(key)) return '同一来源和范围不能重复追加';
    sources.add(key);
  }
  for (const row of rows) {
    const fieldName = row.name.trim();
    if (!fieldName || Array.from(fieldName).length > 128 || names.has(fieldName)) {
      return '结果字段名称不能为空或重复';
    }
    names.add(fieldName);
    if (!row.columns.some((column) => column !== null && column > 0)) {
      return '每个结果字段至少选择一份来源';
    }
  }
  for (let i = 0; i < inputs.length; i++) {
    const info = inputs[i]?.info;
    if (!info) return '请至少选择两份来源并读取字段';
    const used = new Set<number>();
    const fieldSet = new Set((info.fields ?? []).map((field) => field.column));
    for (const row of rows) {
      const column = row.columns[i];
      if (column === null || column === undefined) {
        return '请确认未匹配字段，可选择空列';
      }
      if (
        !Number.isInteger(column) ||
        column < 0 ||
        (column > 0 && (!fieldSet.has(column) || used.has(column)))
      ) {
        return '来源字段无效或重复使用';
      }
      if (column > 0) used.add(column);
    }
    if (!used.size) return '每份来源至少选择一个字段';
  }
  return '';
}

export function buildCompositionDefinition(
  name: string,
  rows: readonly CompositionMappingRow[],
  inputs: readonly CompositionInputSlot[],
): CompositionDefinition {
  return {
    version: 1,
    name: name.trim(),
    fields: rows.map((row) => row.name.trim()),
    inputs: inputs.map((input, index) => {
      const info = input.info;
      if (!info) throw new Error('Missing composition source info');
      return {
        ref: cloneValue(info.ref),
        columns: rows.map((row) => row.columns[index] ?? 0),
      };
    }),
  };
}

export function searchFields<T extends SearchableField>(
  fields: readonly T[],
  text: string,
  page: number,
  size: number,
): FieldSearchPage<T> {
  const q = text.trim().toLocaleLowerCase();
  const matches = fields.filter(
    (field) =>
      !q || `${columnName(field.column)} ${field.column} ${field.name}`.toLocaleLowerCase().includes(q),
  );
  return {
    count: matches.length,
    items: matches.slice(page * size, (page + 1) * size),
  };
}

function sampleTypes(
  row: CompositionMappingRow,
  inputs: readonly CompositionInputSlot[],
): (CompositionSampleType | undefined)[] {
  return inputs.map((input, i) => {
    const col = row.columns[i];
    return input.info?.fields?.find((field) => field.column === col)?.sampleType;
  });
}

export function hasTypeConflict(
  row: CompositionMappingRow,
  inputs: readonly CompositionInputSlot[],
): boolean {
  const types = sampleTypes(row, inputs).filter(
    (type): type is CompositionSampleType =>
      type !== undefined && !['empty', 'unknown', 'cache'].includes(type),
  );
  return types.includes('mixed') || new Set(types).size > 1;
}

export function compositionTypeWarning(
  row: CompositionMappingRow,
  inputs: readonly CompositionInputSlot[],
): string {
  const types = sampleTypes(row, inputs);
  if (hasTypeConflict(row, inputs)) return '示例类型不同';
  if (types.includes('unknown')) return '类型未确认';
  if (types.includes('cache')) return '示例缺缓存';
  return '';
}
