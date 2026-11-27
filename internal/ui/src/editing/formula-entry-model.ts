/**
 * Formula entry lexical analysis, A1 reference formatting/cycling,
 * structured table-column completion context, and `_xlfn.`/`_xlpm.` display cleanup.
 */
import { columnName, columnNumber } from '../core/cell-address.ts';

const MAX_ROW = 1_048_576;
const MAX_COL = 16_384;

export const FORMULA_REF_PALETTE = [
  '#1d70b6',
  '#b45309',
  '#16805e',
  '#8b46b1',
  '#cc4763',
  '#187e95',
  '#9b6b18',
  '#654fb3',
] as const;

export type FormulaRefKind = 'cell' | 'range' | 'column' | 'row';

export interface FormulaRefPoint {
  readonly row: number;
  readonly col: number;
}

export interface ParsedFormulaRef {
  readonly kind: FormulaRefKind;
  readonly start: FormulaRefPoint;
  readonly end: FormulaRefPoint;
  readonly sheet: string;
  readonly external: boolean;
  readonly startIndex: number;
  readonly endIndex: number;
  readonly label: string;
  readonly key: string;
  readonly colorIndex: number;
  readonly range: boolean;
}

export type FormulaPoint =
  | {
      readonly kind: 'cell';
      readonly row: number;
      readonly col: number;
      readonly ref?: string;
      readonly visual?: number;
    }
  | { readonly kind: 'column'; readonly col: number }
  | { readonly kind: 'row'; readonly row: number };

export interface FormulaContext {
  readonly prefix: string;
  readonly start: number;
  readonly end: number;
  readonly fn: string;
  readonly arg: number;
  readonly operand: boolean;
}

export interface FormulaColumnContext {
  readonly table: string;
  readonly start: number;
  readonly end: number;
  readonly closures: number;
  readonly prefix: string;
}

export interface AbsoluteReplacement {
  readonly from: number;
  readonly until: number;
  readonly value: string;
}

function isWordChar(c: string | undefined): boolean {
  return Boolean(c) && /[\p{L}\p{N}_.]/u.test(c ?? '');
}

function isAsciiLetter(c: string | undefined): boolean {
  return Boolean(c) && /[A-Za-z]/.test(c ?? '');
}

function isAsciiDigit(c: string | undefined): boolean {
  return c !== undefined && c >= '0' && c <= '9';
}

function skipStringLiteral(s: string, index: number): number {
  let i = index + 1;
  while (i < s.length) {
    if (s[i] === '"') {
      if (s[i + 1] === '"') {
        i += 2;
        continue;
      }
      return i + 1;
    }
    i++;
  }
  return i;
}

function skipBracketGroup(s: string, index: number): number {
  let depth = 0;
  for (let i = index; i < s.length; i++) {
    if (s[i] === '[') depth++;
    else if (s[i] === ']' && --depth === 0) return i + 1;
  }
  return s.length;
}

interface SheetPrefixMatch {
  readonly name: string;
  readonly after: number;
  readonly external: boolean;
}

function parseSheetPrefix(s: string, i: number): SheetPrefixMatch | null {
  if (s[i] === "'") {
    let p = i + 1;
    let name = '';
    while (p < s.length) {
      if (s[p] === "'") {
        if (s[p + 1] === "'") {
          name += "'";
          p += 2;
          continue;
        }
        if (s[p + 1] === '!') {
          return { name, after: p + 2, external: name.includes('[') || name.includes(']') };
        }
        return null;
      }
      name += s[p] ?? '';
      p++;
    }
    return null;
  }
  if (!isWordChar(s[i]) || isAsciiDigit(s[i])) return null;
  let p = i;
  while (p < s.length && isWordChar(s[p])) p++;
  if (s[p] === '!' && p > i) return { name: s.slice(i, p), after: p + 1, external: false };
  return null;
}

type RefAtom =
  | { readonly kind: 'cell'; readonly row: number; readonly col: number; readonly end: number }
  | { readonly kind: 'column'; readonly col: number; readonly end: number }
  | { readonly kind: 'row'; readonly row: number; readonly end: number };

function parseAtom(s: string, from: number): RefAtom | null {
  let p = from;
  if (s[p] === '$') p++;
  const start = p;
  while (isAsciiLetter(s[p]) && p - start < 4) p++;
  const letters = s.slice(start, p);
  if (letters.length > 0 && letters.length <= 3) {
    const col = columnNumber(letters);
    if (col < 1 || col > MAX_COL) return null;
    if (s[p] === '$') p++;
    const rs = p;
    while (isAsciiDigit(s[p]) && p - rs <= 7) p++;
    if (p > rs) {
      const row = Number(s.slice(rs, p));
      return row > 0 && row <= MAX_ROW ? { kind: 'cell', row, col, end: p } : null;
    }
    return { kind: 'column', col, end: p };
  }
  p = from;
  if (s[p] === '$') p++;
  const rs = p;
  while (isAsciiDigit(s[p]) && p - rs <= 7) p++;
  if (p === rs) return null;
  const row = Number(s.slice(rs, p));
  return row > 0 && row <= MAX_ROW ? { kind: 'row', row, end: p } : null;
}

function atomBounds(
  a: RefAtom,
  b: RefAtom,
): { readonly kind: FormulaRefKind; readonly start: FormulaRefPoint; readonly end: FormulaRefPoint } {
  if (a.kind === 'cell' && b.kind === 'cell') {
    return {
      kind: a.row === b.row && a.col === b.col ? 'cell' : 'range',
      start: { row: Math.min(a.row, b.row), col: Math.min(a.col, b.col) },
      end: { row: Math.max(a.row, b.row), col: Math.max(a.col, b.col) },
    };
  }
  if (a.kind === 'column' && b.kind === 'column') {
    return {
      kind: 'column',
      start: { row: 1, col: Math.min(a.col, b.col) },
      end: { row: MAX_ROW, col: Math.max(a.col, b.col) },
    };
  }
  const rowA = a.kind === 'row' ? a.row : 1;
  const rowB = b.kind === 'row' ? b.row : rowA;
  return {
    kind: 'row',
    start: { row: Math.min(rowA, rowB), col: 1 },
    end: { row: Math.max(rowA, rowB), col: MAX_COL },
  };
}

/**
 * Pure read-only A1-reference lexer matching the formula inspector semantics.
 */
export function parseFormula(
  formula: string | null | undefined,
  currentSheet = '',
  maxRefs = 256,
): ParsedFormulaRef[] {
  const s = formula ?? '';
  const refs: ParsedFormulaRef[] = [];
  const colorKeys = new Map<string, number>();
  for (let i = 0; i < s.length && refs.length < maxRefs; ) {
    if (s[i] === '"') {
      i = skipStringLiteral(s, i);
      continue;
    }
    if (s[i] === '[') {
      i = skipBracketGroup(s, i);
      continue;
    }
    const prev = i > 0 ? s[i - 1] : undefined;
    if (i > 0 && (isWordChar(prev) || prev === '!' || prev === ']' || prev === '$')) {
      i++;
      continue;
    }
    const prefix = parseSheetPrefix(s, i);
    if (s[i] === "'" && !prefix) {
      let q = i + 1;
      while (q < s.length) {
        if (s[q] === "'") {
          if (s[q + 1] === "'") {
            q += 2;
            continue;
          }
          q++;
          break;
        }
        q++;
      }
      i = q;
      continue;
    }
    // A 3D reference such as Sheet1:Sheet3!A1 is not one local cell.
    if (i > 0 && prev === ':' && prefix) {
      i = prefix.after;
      continue;
    }
    const sheet = prefix?.name || currentSheet;
    const startAt = prefix ? prefix.after : i;
    const a = parseAtom(s, startAt);
    if (!a) {
      i = prefix ? Math.max(i + 1, prefix.after) : i + 1;
      continue;
    }
    let p = a.end;
    let b: RefAtom = a;
    if (s[p] === ':') {
      let next = p + 1;
      const secondPrefix = parseSheetPrefix(s, next);
      if (secondPrefix) {
        if (secondPrefix.name.toLocaleLowerCase() !== sheet.toLocaleLowerCase()) {
          i = secondPrefix.after;
          continue;
        }
        next = secondPrefix.after;
      }
      const second = parseAtom(s, next);
      if (second?.kind === a.kind) {
        b = second;
        p = second.end;
      }
    }
    if (
      (a.kind !== 'cell' && b === a) ||
      isWordChar(s[p]) ||
      s[p] === '!' ||
      s[p] === '(' ||
      s[p] === '$'
    ) {
      i = prefix ? Math.max(i + 1, prefix.after) : i + 1;
      continue;
    }
    const r = atomBounds(a, b);
    const external = Boolean(prefix?.external);
    const key = `${sheet.toLocaleLowerCase()}|${r.kind}|${r.start.row},${r.start.col}|${r.end.row},${r.end.col}`;
    let colorIndex = colorKeys.get(key);
    if (colorIndex === undefined) {
      colorIndex = colorKeys.size % FORMULA_REF_PALETTE.length;
      colorKeys.set(key, colorIndex);
    }
    refs.push({
      ...r,
      sheet,
      external,
      startIndex: i,
      endIndex: p,
      label: s.slice(i, p),
      key,
      colorIndex,
      range: r.kind !== 'cell',
    });
    i = p;
  }
  return refs;
}

interface CallFrame {
  readonly fn: string;
  arg: number;
  readonly braces: number;
}

/**
 * Only separators outside literals, sheet names, table selectors and array
 * constants advance a function argument. Caret positions are DOM UTF-16 offsets.
 */
export function formulaContext(text: string, caret: number): FormulaContext | null {
  if (!text.trimStart().startsWith('=')) return null;
  const stack: CallFrame[] = [];
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
    if (c === '"' || c === "'") {
      quote = c;
    } else if (c === '[') {
      brackets++;
    } else if (c === '{') {
      braces++;
    } else if (c === '}') {
      braces--;
    } else if (/[A-Za-z_]/.test(c) || (word && /[\d.]/.test(c))) {
      word += c;
      continue;
    } else if (c === '(') {
      stack.push({ fn: word.toUpperCase(), arg: 0, braces });
    } else if (c === ')') {
      stack.pop();
    } else if (c === ',') {
      const top = stack.at(-1);
      if (top?.braces === braces) top.arg++;
    }
    word = '';
  }
  if (quote || brackets) return null;
  const token = /[\p{L}_\\][\p{L}\p{N}_.]*$/u.exec(before);
  const prefix =
    token && !/[\p{L}\p{N}_.$!:]$/u.test(before.slice(0, token.index).trimEnd()) ? token[0] : '';
  const enclosing = stack
    .slice()
    .reverse()
    .find((frame) => Boolean(frame.fn));
  return {
    prefix,
    start: caret - prefix.length,
    end: caret,
    fn: enclosing?.fn ?? '',
    arg: enclosing?.arg ?? 0,
    operand: !braces && /[=+\-*/^&(<>,:]\s*$/.test(before),
  };
}

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
    } else if (c === '"' || c === "'") {
      quote = c;
    } else if (c === '[') {
      brackets++;
    } else if (c === '(') {
      depth++;
    } else if (c === ')' && --depth < 0) {
      return text;
    }
  }
  return !quote && !brackets && depth > 0 && depth <= 40 ? text + ')'.repeat(depth) : text;
}

function sheetPrefix(sheet: string, origin: string): string {
  if (sheet === origin) return '';
  if (
    /^[\p{L}_][\p{L}\p{N}_.]*$/u.test(sheet) &&
    !/^(?:[a-z]{1,3}\d+|r\d*c\d*|[rc])$/i.test(sheet)
  ) {
    return `${sheet}!`;
  }
  return `'${sheet.replaceAll("'", "''")}'!`;
}

export function formatReference(
  a: FormulaPoint,
  b: FormulaPoint,
  sheet: string,
  origin: string,
  previous = '',
): string {
  const prefix = sheetPrefix(sheet, origin);
  const locks = previous.slice(previous.lastIndexOf('!') + 1).split(':');
  const lock0 = locks[0] ?? '';
  const lock1 = locks[1] || lock0;
  if (a.kind === 'column') {
    const bCol = b.kind === 'column' || b.kind === 'cell' ? b.col : a.col;
    const low = Math.min(a.col, bCol);
    const high = Math.max(a.col, bCol);
    const axis = (n: number, lock: string): string =>
      `${lock.startsWith('$') ? '$' : ''}${columnName(n)}`;
    return `${prefix}${axis(low, lock0)}:${axis(high, lock1)}`;
  }
  if (a.kind === 'row') {
    const bRow = b.kind === 'row' || b.kind === 'cell' ? b.row : a.row;
    const low = Math.min(a.row, bRow);
    const high = Math.max(a.row, bRow);
    const axis = (n: number, lock: string): string => `${lock.startsWith('$') ? '$' : ''}${n}`;
    return `${prefix}${axis(low, lock0)}:${axis(high, lock1)}`;
  }
  const bCol = b.kind === 'row' ? a.col : b.col;
  const bRow = b.kind === 'column' ? a.row : b.row;
  const address = (col: number, row: number, pattern: string): string => {
    const axes = /^(\$?)[A-Z]+(\$?)\d+$/i.exec(pattern);
    return `${axes?.[1] ?? ''}${columnName(col)}${axes?.[2] ?? ''}${row}`;
  };
  const first = address(Math.min(a.col, bCol), Math.min(a.row, bRow), lock0);
  const last = address(Math.max(a.col, bCol), Math.max(a.row, bRow), lock1);
  return prefix + first + (first === last ? '' : `:${last}`);
}

export function cycleAbsolute(
  text: string,
  start: number,
  end: number,
  origin: string,
): AbsoluteReplacement | null {
  const ref = parseFormula(text, origin).find(
    (r) => !r.external && r.startIndex <= start && end <= r.endIndex,
  );
  if (!ref) return null;
  const qualifier = ref.label.lastIndexOf('!') + 1;
  const body = ref.label.slice(qualifier);
  const atoms = body.split(':');
  const offset = ref.startIndex + qualifier;
  if (start < offset && start !== ref.startIndex) return null;
  // A selected/pointed rectangle cycles as a unit. A typed range under the
  // caret cycles just its active endpoint, as in the native formula editor.
  let from = offset;
  let until = ref.endIndex;
  let value = body;
  if (atoms.length === 2 && start === end) {
    const firstAtom = atoms[0] ?? '';
    const secondAtom = atoms[1] ?? '';
    const split = offset + firstAtom.length;
    if (start <= split) {
      until = split;
      value = firstAtom;
    } else {
      from = split + 1;
      value = secondAtom;
    }
  }
  if (ref.kind === 'row' || ref.kind === 'column') {
    const axis = value.split(':').map((v) => (v.startsWith('$') ? v.slice(1) : `$${v}`));
    return { from, until, value: axis.join(':') };
  }
  const cycle = (
    _: string,
    colLock: string,
    col: string,
    rowLock: string,
    row: string,
  ): string => {
    if (!colLock && !rowLock) return `$${col}$${row}`;
    if (colLock && rowLock) return `${col}$${row}`;
    if (rowLock) return `$${col}${row}`;
    return `${col}${row}`;
  };
  return { from, until, value: value.replace(/(\$?)([A-Za-z]+)(\$?)(\d+)/g, cycle) };
}

/**
 * Strip `_xlfn.` / `_xlpm.` internal formula prefixes for display while leaving
 * quoted text, sheet names, and structured field captions intact.
 */
export function displayFormula(text: string): string {
  if (!text || !/_(?:xlfn|xlpm)\./i.test(text)) return text;
  const hasLet = /_xlfn\.(?:LET|LAMBDA)\s*\(/i.test(text);
  let out = '';
  let quote = '';
  let brackets = 0;
  const token = /_(xlfn\.(?:_xlws\.)?|xlpm\.)([\p{L}_][\p{L}\p{N}_.]*)/iuy;
  for (let i = 0; i < text.length; ) {
    const c = text[i] ?? '';
    if (quote) {
      out += c;
      i++;
      if (c === quote) {
        if (text[i] === quote) {
          out += text[i] ?? '';
          i++;
        } else {
          quote = '';
        }
      }
      continue;
    }
    if (brackets) {
      out += c;
      i++;
      if (c === "'" && i < text.length) {
        out += text[i] ?? '';
        i++;
      } else if (c === '[') {
        brackets++;
      } else if (c === ']') {
        brackets--;
      }
      continue;
    }
    if (c === '"' || c === "'") {
      quote = c;
    } else if (c === '[') {
      brackets++;
    } else if (c === '_' && (i === 0 || !/[\p{L}\p{N}_.]/u.test(text[i - 1] ?? ''))) {
      token.lastIndex = i;
      const m = token.exec(text);
      const prefixGroup = m?.[1];
      const nameGroup = m?.[2];
      if (m && prefixGroup && nameGroup) {
        const local = prefixGroup.toLowerCase() === 'xlpm.';
        // SINGLE and ANCHORARRAY encode operators. Leave these intact
        // until a full reference expression can be rendered losslessly.
        if (
          local
            ? hasLet
            : /^\s*\(/.test(text.slice(token.lastIndex)) &&
              !/^(SINGLE|ANCHORARRAY)$/i.test(nameGroup)
        ) {
          out += nameGroup;
          i = token.lastIndex;
          continue;
        }
      }
    }
    out += c;
    i++;
  }
  return out;
}

/**
 * Track nested structured selectors independently of quoted sheet names.
 * A closing bracket can itself be part of an escaped column caption.
 */
export function columnContext(text: string, caret: number): FormulaColumnContext | null {
  if (!text.trimStart().startsWith('=')) return null;
  const stack: number[] = [];
  let quote = '';
  let table = '';
  for (let i = 0; i < caret; i++) {
    const c = text[i] ?? '';
    if (quote) {
      if (c === quote) {
        if (text[i + 1] === quote) i++;
        else quote = '';
      }
      continue;
    }
    if (stack.length && c === "'") {
      i++;
      continue;
    }
    if (!stack.length && (c === '"' || c === "'")) {
      quote = c;
      continue;
    }
    if (c === '[') {
      if (!stack.length) {
        table = /[\p{L}_][\p{L}\p{N}_.]*$/u.exec(text.slice(0, i))?.[0] ?? '';
      }
      stack.push(i + 1);
    } else if (c === ']' && stack.length) {
      stack.pop();
    }
  }
  const top = stack.at(-1);
  if (top === undefined) return null;
  let start = top;
  if (text[start] === '@') start++;
  const raw = text.slice(start, caret);
  if (raw.startsWith('#') || raw.includes('],')) return null;
  let end = caret;
  while (end < text.length) {
    const c = text[end] ?? '';
    if (c === "'" && /['#@[\]]/.test(text[end + 1] ?? ' ')) {
      end += 2;
      continue;
    }
    if (c === ']') {
      end++;
      break;
    }
    if (c === '[' || c === ')') break;
    end++;
  }
  const tail = text.slice(end);
  const alreadyClosed = /^\]*/.exec(tail)?.[0].length ?? 0;
  const closures = /^[,:]/.test(tail) ? 1 : Math.max(1, stack.length - alreadyClosed);
  return { table, start, end, closures, prefix: raw.replace(/'(['#@[\]])/g, '$1') };
}

export const FormulaInput = Object.freeze({
  context: formulaContext,
  close: closeFormula,
  reference: formatReference,
  absolute: cycleAbsolute,
  display: displayFormula,
  columnContext,
});
