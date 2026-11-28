/**
 * Sparse formula table model and formula-entry context helpers for the
 * floating read-only spreadsheet calculator.
 */

import { t as localize } from '../core/i18n.ts';
export interface CalculatorSourceRef {
  readonly workbookKey: string;
  readonly sheet: string;
}

export interface CalculatorRowRecord {
  readonly text: string;
  readonly note: string;
  readonly source: CalculatorSourceRef | null;
}

export interface CalculatorSuggestion {
  readonly kind: string;
  readonly label: string;
  readonly preview: string;
}

/** @httpResponse CalculatorCellResult */
export interface CalculatorCellResult {
  readonly kind?: string;
  readonly display: string;
  readonly copy: string;
  readonly exact: string;
  readonly suggestions?: readonly CalculatorSuggestion[];
}

export interface CalculatorRowState {
  text: string;
  note: string;
  committed: string;
  result: CalculatorCellResult | null;
  savedResult: CalculatorCellResult | null;
  error: string;
  revision: number;
  source: CalculatorSourceRef | null;
  restored: boolean;
  committedRestored: boolean;
}

export interface FormulaInputContext {
  readonly prefix: string;
  readonly start: number;
  readonly end: number;
  readonly fn: string;
  readonly arg: number;
  readonly operand: boolean;
}

function createEmptyRow(): CalculatorRowState {
  return {
    text: '',
    note: '',
    committed: '',
    result: null,
    savedResult: null,
    error: '',
    revision: 0,
    source: null,
    restored: false,
    committedRestored: false,
  };
}

const BLANK_ROW: Readonly<CalculatorRowState> = Object.freeze(createEmptyRow());

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

/**
 * Compute caret formula-completion context outside quoted literals, sheet names,
 * structured selectors, and array constants.
 */
export function formulaContext(text: string, caret: number): FormulaInputContext | null {
  if (!text.trimStart().startsWith('=')) return null;
  const stack: { readonly fn: string; arg: number; readonly braces: number }[] = [];
  let quote = '';
  let word = '';
  let brackets = 0;
  let braces = 0;
  const before = text.slice(0, caret);
  for (let i = 0; i < before.length; i++) {
    const c = before[i] ?? '';
    if (quote) {
      if (c === quote) {
        if (before[i + 1] === quote) i++;
        else quote = '';
      }
      continue;
    }
    if (brackets) {
      if (c === '[') brackets++;
      else if (c === ']') brackets--;
      continue;
    }
    if (c === '"' || c === "'") quote = c;
    else if (c === '[') brackets++;
    else if (c === '{') braces++;
    else if (c === '}') braces--;
    else if (/[A-Za-z_]/.test(c) || (word && /[\d.]/.test(c))) {
      word += c;
      continue;
    } else if (c === '(') stack.push({ fn: word.toUpperCase(), arg: 0, braces });
    else if (c === ')') stack.pop();
    else if (c === ',' && stack.length) {
      const top = stack.at(-1);
      if (top?.braces === braces) top.arg++;
    }
    word = '';
  }
  if (quote || brackets) return null;
  const token = /[\p{L}_\\][\p{L}\p{N}_.]*$/u.exec(before);
  const prefix =
    token && !/[\p{L}\p{N}_.$!:]$/u.test(before.slice(0, token.index).trimEnd()) ? token[0] : '';
  const enclosing = [...stack].reverse().find((frame) => frame.fn);
  return {
    prefix,
    start: caret - prefix.length,
    end: caret,
    fn: enclosing?.fn ?? '',
    arg: enclosing?.arg ?? 0,
    operand: !braces && /[=+\-*/^&(<>,:]\s*$/.test(before),
  };
}

/**
 * Append missing closing parentheses when safe (outside quotes/brackets, depth <= 40).
 */
export function closeFormula(text: string): string {
  let depth = 0;
  let quote = '';
  let brackets = 0;
  for (let i = 0; i < text.length; i++) {
    const c = text[i] ?? '';
    if (quote) {
      if (c === quote) {
        if (text[i + 1] === quote) i++;
        else quote = '';
      }
    } else if (brackets) {
      if (c === '[') brackets++;
      else if (c === ']') brackets--;
    } else if (c === '"' || c === "'") quote = c;
    else if (c === '[') brackets++;
    else if (c === '(') depth++;
    else if (c === ')' && --depth < 0) return text;
  }
  return !quote && !brackets && depth > 0 && depth <= 40 ? text + ')'.repeat(depth) : text;
}

export class FormulaTable {
  public readonly rows = new Map<number, CalculatorRowState>();
  public readonly used = new Set<number>();
  public lastUsed = -1;
  public rowCount = 5;
  public active = 0;
  public onChange: ((key: string, data: CalculatorRowRecord | null) => void) | null = null;

  get(index: number): Readonly<CalculatorRowState> {
    return this.rows.get(index) ?? BLANK_ROW;
  }

  ensure(index: number): CalculatorRowState {
    if (!Number.isSafeInteger(index) || index < 0) throw new Error(localize('ui.calculatorModel.invalidRowNumbers'));
    let row = this.rows.get(index);
    if (!row) {
      row = createEmptyRow();
      this.rows.set(index, row);
    }
    this.rowCount = Math.max(this.rowCount, index + 1);
    return row;
  }

  measure(index: number): void {
    const row = this.get(index);
    if (row.text !== '' || row.note !== '') {
      this.used.add(index);
      this.lastUsed = Math.max(this.lastUsed, index);
    } else {
      this.used.delete(index);
      if (index === this.lastUsed) {
        this.lastUsed = -1;
        for (const i of this.used) this.lastUsed = Math.max(this.lastUsed, i);
      }
    }
    this.fit();
  }

  fit(): void {
    this.rowCount = Math.max(5, this.lastUsed + 2, this.active + 1);
  }

  changed(index: number): void {
    const row = this.get(index);
    this.onChange?.(
      String(index),
      row.text || row.note
        ? {
            text: row.text,
            note: row.note,
            source: row.source ? { ...row.source } : null,
          }
        : null,
    );
  }

  setNote(index: number, text: string): void {
    if (text.length > 4096) throw new Error(localize('ui.calculatorModel.notesTooLong'));
    const row = this.ensure(index);
    if (row.note === text) return;
    row.note = text;
    this.measure(index);
    this.changed(index);
  }

  edit(index: number, text: string, source: CalculatorSourceRef): CalculatorRowState {
    if (text.length > 4096) throw new Error(localize('ui.calculatorModel.formulaTooLong'));
    const row = this.ensure(index);
    if (row.text === text) return row;
    if (!row.text || !row.source?.workbookKey) row.source = { ...source };
    row.text = text;
    row.result = null;
    row.error = '';
    row.restored = false;
    row.revision++;
    if (!text) row.source = null;
    this.measure(index);
    this.changed(index);
    return row;
  }

  cancel(index: number): void {
    const row = this.ensure(index);
    row.text = row.committed;
    row.result = row.savedResult;
    row.restored = row.committedRestored;
    row.error = '';
    row.revision++;
    this.measure(index);
    this.changed(index);
  }

  restore(records: Readonly<Record<string, unknown>>): void {
    const items = Object.entries(records);
    const validated: {
      readonly index: number;
      readonly text: string;
      readonly note: string;
      readonly source: CalculatorSourceRef | null;
    }[] = [];

    for (const [key, data] of items) {
      if (
        !/^(0|[1-9]\d*)$/.test(key) ||
        !Number.isSafeInteger(Number(key)) ||
        Number(key) > Number.MAX_SAFE_INTEGER - 2 ||
        !isRecord(data) ||
        typeof data.text !== 'string' ||
        typeof data.note !== 'string' ||
        data.text.length > 4096 ||
        data.note.length > 4096
      ) {
        throw new Error(localize('ui.calculatorModel.calculatedTableRecoveryContentInvalid'));
      }
      let source: CalculatorSourceRef | null = null;
      if (data.source != null) {
        if (
          !isRecord(data.source) ||
          typeof data.source.workbookKey !== 'string' ||
          typeof data.source.sheet !== 'string'
        ) {
          throw new Error(localize('ui.calculatorModel.calculatedTableRecoveryContentInvalid'));
        }
        source = { workbookKey: data.source.workbookKey, sheet: data.source.sheet };
      }
      validated.push({
        index: Number(key),
        text: data.text,
        note: data.note,
        source,
      });
    }

    for (const entry of validated) {
      const row = this.ensure(entry.index);
      const formula = entry.text.trim().startsWith('=');
      Object.assign(row, createEmptyRow(), {
        text: entry.text,
        note: entry.note,
        source: entry.source,
        committed: entry.text,
        restored: formula,
        committedRestored: formula,
      });
      this.measure(entry.index);
    }
  }
}
