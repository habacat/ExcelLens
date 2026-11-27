/**
 * Pure value validation, coordinate parsing, CR/backslash escaping,
 * request-budget batching, TSV paste expansion, and exact decimal fill series.
 */
import type {
  EditAddress,
  EditChange,
  EditSheetMeta,
  EditValue,
  EditValueKind,
} from '../contracts/edit.ts';
import type { PasteParseOptions } from '../grid/clipboard.ts';

export interface EditModelLimits {
  readonly cellBytes: number;
  readonly formulaBytes?: number;
  readonly batchCells: number;
  readonly requestBytes: number;
}

export type EditGridInputKind = 'auto' | EditValueKind;

export type EditTsvParser = (rawText: string, options?: PasteParseOptions) => string[][];

export interface EditPastePlan {
  readonly changes: EditChange[];
  readonly last: EditAddress;
}

export interface EditFillPlan {
  readonly changes: EditChange[];
  readonly last: EditAddress;
}

const utf8 = new TextEncoder();
const bytes = (value: string): number => utf8.encode(value).length;

export function parseEditTarget(
  ref: string,
  sheet: EditSheetMeta | null | undefined,
): EditAddress {
  const hit = /^([A-Z]{1,3})([1-9][0-9]{0,8})$/.exec(ref.trim().toUpperCase());
  const letters = hit?.[1];
  const digits = hit?.[2];
  if (!sheet || !letters || !digits) throw new Error('输入有效的源单元格地址');
  let col = 0;
  for (const char of letters) col = col * 26 + char.charCodeAt(0) - 64;
  const row = Number(digits);
  if (row < sheet.firstRow || row > sheet.rows || col < sheet.firstCol || col > sheet.cols) {
    throw new Error('地址超出可编辑区域');
  }
  return { sheet: sheet.name, row, col };
}

export function encodeEditText(text: string, escaped: boolean): string {
  return escaped ? text.replaceAll('\\', '\\\\').replaceAll('\r', '\\r') : text;
}

export function decodeEditText(text: string, escaped: boolean): string {
  if (!escaped) return text;
  return text.replace(/\\(?:[\\r]|[\s\S]|$)/g, (part) => {
    if (part === '\\r') return '\r';
    if (part === '\\\\') return '\\';
    throw new Error('回车输入使用 \\r，反斜线输入使用 \\\\');
  });
}

export function buildEditValue(
  kind: EditValueKind,
  raw: string,
  limits: Pick<EditModelLimits, 'cellBytes' | 'formulaBytes'>,
): EditValue {
  if (kind === 'blank' || (kind === 'text' && raw === '')) return { kind: 'blank', raw: '' };
  let nextRaw = raw;
  if (bytes(nextRaw) > limits.cellBytes) throw new Error('单元格原文超出编辑上限');
  if (kind === 'boolean') {
    nextRaw = nextRaw.toUpperCase();
    if (nextRaw !== 'TRUE' && nextRaw !== 'FALSE') throw new Error('布尔值请输入 TRUE 或 FALSE');
  }
  if (
    kind === 'number' &&
    !/^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[Ee][+-]?[0-9]+)?$/.test(nextRaw)
  ) {
    throw new Error('输入有效的十进制数值');
  }
  if (kind === 'formula') {
    nextRaw = nextRaw.trim();
    if (!nextRaw.startsWith('=') || !nextRaw.slice(1).trim()) throw new Error('公式请以 = 开头');
    nextRaw = `=${nextRaw.slice(1).trim()}`;
    if (limits.formulaBytes !== undefined && bytes(nextRaw) > limits.formulaBytes) {
      throw new Error('公式原文超出编辑上限');
    }
  }
  return { kind, raw: nextRaw };
}

export function boundedChanges(
  changes: Iterable<EditChange>,
  limits: Pick<EditModelLimits, 'batchCells' | 'requestBytes'>,
): EditChange[] {
  let size = 514;
  const result: EditChange[] = [];
  for (const change of changes) {
    size += bytes(JSON.stringify(change)) + (result.length ? 1 : 0);
    if (result.length === limits.batchCells || size > limits.requestBytes) {
      throw new Error('区域超出一次编辑容量，请缩小范围');
    }
    result.push(change);
  }
  if (!result.length) throw new Error('没有可提交的单元格');
  return result;
}

export function buildEditPaste(
  text: string,
  first: EditAddress,
  sheet: EditSheetMeta,
  skipBlank: boolean,
  limits: EditModelLimits,
  parse: EditTsvParser,
): EditPastePlan {
  if (text === '') throw new Error('请先粘贴表格文本');
  const rows = parse(text, {
    maxRows: limits.batchCells,
    maxCols: sheet.cols - first.col + 1,
    maxCells: limits.batchCells,
    maxCellLength: limits.cellBytes,
    maxTextLength: limits.requestBytes,
  });
  if (first.row + rows.length - 1 > sheet.rows) throw new Error('粘贴范围超出可编辑区域');
  const changes: EditChange[] = [];
  rows.forEach((row, i) => {
    row.forEach((cellText, j) => {
      if (cellText !== '' || !skipBlank) {
        changes.push({
          ...first,
          row: first.row + i,
          col: first.col + j,
          value: buildEditValue('text', cellText, limits),
        });
      }
    });
  });
  return {
    changes: boundedChanges(changes, limits),
    last: {
      ...first,
      row: first.row + rows.length - 1,
      col: first.col + Math.max(...rows.map((row) => row.length)) - 1,
    },
  };
}

interface ExactDecimal {
  readonly number: bigint;
  readonly scale: number;
}

function parseExactDecimal(text: string): ExactDecimal {
  const hit = /^([+-]?)([0-9]*)(?:\.([0-9]*))?(?:[Ee]([+-]?[0-9]+))?$/.exec(text);
  const sign = hit?.[1] ?? '';
  const whole = hit?.[2] ?? '';
  const frac = hit?.[3] ?? '';
  const exp = hit?.[4] ?? '';
  if (!hit || (!whole && !frac) || text.length > 80) {
    throw new Error('起始值和步长必须是十进制数');
  }
  const scale = frac.length - Number(exp || 0);
  if (!Number.isSafeInteger(scale) || Math.abs(scale) > 40) {
    throw new Error('序列小数位数或指数超出范围');
  }
  return { number: BigInt(`${sign || '+'}${whole}${frac}`), scale };
}

function formatDecimalText(n: bigint, scale: number): string {
  const digits = (n < 0n ? -n : n).toString().padStart(scale + 1, '0');
  return (n < 0n ? '-' : '') + (scale ? `${digits.slice(0, -scale)}.${digits.slice(-scale)}` : digits);
}

export function buildEditFill(
  first: EditAddress,
  last: EditAddress,
  start: EditValue,
  step: string,
  numeric: boolean,
  asText: boolean,
  limits: EditModelLimits,
): EditFillPlan {
  const deltaRow = Math.sign(last.row - first.row);
  const deltaCol = Math.sign(last.col - first.col);
  const count = Math.abs(last.row - first.row) + Math.abs(last.col - first.col);
  if ((deltaRow && deltaCol) || !count || count > limits.batchCells) {
    throw new Error('请选择同列或同行的填充终点');
  }
  const changes: EditChange[] = [];
  if (numeric) {
    const a = parseExactDecimal(start.raw);
    const b = parseExactDecimal(step);
    const scale = Math.max(0, a.scale, b.scale);
    const base = a.number * 10n ** BigInt(scale - a.scale);
    const advance = b.number * 10n ** BigInt(scale - b.scale);
    for (let i = 1; i <= count; i++) {
      changes.push({
        ...first,
        row: first.row + deltaRow * i,
        col: first.col + deltaCol * i,
        value: buildEditValue(
          asText ? 'text' : 'number',
          formatDecimalText(base + advance * BigInt(i), scale),
          limits,
        ),
      });
    }
  } else {
    if (!['blank', 'text', 'number', 'boolean'].includes(start.kind)) {
      throw new Error('当前值不可用于填充');
    }
    for (let i = 1; i <= count; i++) {
      changes.push({
        ...first,
        row: first.row + deltaRow * i,
        col: first.col + deltaCol * i,
        value: start,
      });
    }
  }
  return { changes: boundedChanges(changes, limits), last };
}

export function buildGridValue(
  raw: string,
  kind: EditGridInputKind,
  original: string | undefined,
  textFile: boolean,
  limits: Pick<EditModelLimits, 'cellBytes' | 'formulaBytes'>,
): EditValue {
  if (textFile) return buildEditValue('text', raw, limits);
  if (kind !== 'auto') return buildEditValue(kind, raw, limits);
  if (raw.startsWith("'")) return buildEditValue('text', raw.slice(1), limits);
  if (raw.startsWith('=')) return buildEditValue('formula', raw, limits);
  if (
    original === 'date' &&
    /^\d{4}-\d{2}-\d{2}(?:T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})?)?$/.test(raw)
  ) {
    return buildEditValue('date', raw, limits);
  }
  if (original === 'text') return buildEditValue('text', raw, limits);
  if (/^(?:true|false)$/i.test(raw)) return buildEditValue('boolean', raw, limits);
  const numberMatch = /^[+-]?((?:\d+(?:\.\d*)?|\.\d+))(?:e[+-]?\d+)?$/i.exec(raw);
  const mantissa = numberMatch?.[1];
  if (mantissa && !/^0\d/.test(mantissa) && mantissa.replace('.', '').replace(/^0+/, '').length <= 15) {
    return buildEditValue('number', raw, limits);
  }
  return buildEditValue('text', raw, limits);
}

export const EditModel = Object.freeze({
  target: parseEditTarget,
  encode: encodeEditText,
  decode: decodeEditText,
  value: buildEditValue,
  gridValue: buildGridValue,
  bounded: boundedChanges,
  paste: buildEditPaste,
  fill: buildEditFill,
});
