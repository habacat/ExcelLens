/**
 * ExcelLens formula inspector: read-only A1-reference lexer and interactive
 * formula bar preview. Never evaluates formulas or mutates the workbook.
 */
import type { ApiClient, ToastFn } from '../contracts/common.ts';
import type { CellData } from '../contracts/workbook.ts';
import { beginActivity } from '../core/activity.ts';
import { columnName, columnNumber } from '../core/cell-address.ts';
import { errorMessage } from '../core/display-format.ts';
import { byId } from '../core/dom.ts';
import type { WorkbookState } from '../grid/state.ts';
import {
  decodeIndexStartResponse,
  decodeIndexStatusResponse,
} from '../core/http-decoders.generated.ts';

const MAX_ROW = 1_048_576;
const MAX_COL = 16_384;

export const PALETTE: readonly string[] = [
  '#1d70b6',
  '#b45309',
  '#16805e',
  '#8b46b1',
  '#cc4763',
  '#187e95',
  '#9b6b18',
  '#654fb3',
];

export type FormulaRefKind = 'cell' | 'range' | 'column' | 'row';

export interface FormulaRefCoord {
  readonly row: number;
  readonly col: number;
}

export interface FormulaToken {
  readonly kind: FormulaRefKind;
  readonly start: FormulaRefCoord;
  readonly end: FormulaRefCoord;
  sheet: string;
  readonly external: boolean;
  readonly startIndex: number;
  readonly endIndex: number;
  readonly label: string;
  readonly key: string;
  readonly colorIndex: number;
  readonly range: boolean;
  index?: number;
}

export interface FormulaRefMatch {
  readonly token: FormulaToken;
  readonly focus: boolean;
}

export interface FormulaInspectorOptions {
  readonly element: HTMLElement;
  readonly tooltip: HTMLElement;
  readonly grid?: HTMLElement | null;
  readonly state: WorkbookState;
  readonly api: ApiClient;
  readonly getCell: (sheet: string, ref: string) => Promise<CellData>;
  readonly navigate: (
    sheet: string,
    ref: string,
    preserveTrace?: boolean,
  ) => Promise<unknown>;
  readonly notify: ToastFn;
  readonly copy: (value: string) => Promise<boolean> | boolean | undefined;
  readonly onLayoutChange?: () => void;
  readonly sheetExists: (sheet: string) => boolean;
}

type RefAtom =
  | { readonly kind: 'cell'; readonly row: number; readonly col: number; readonly end: number }
  | { readonly kind: 'column'; readonly col: number; readonly end: number }
  | { readonly kind: 'row'; readonly row: number; readonly end: number };

const wordChar = (c: string | undefined): boolean => Boolean(c && /[\p{L}\p{N}_.]/u.test(c));
const isLetter = (c: string | undefined): boolean => Boolean(c && /[A-Za-z]/.test(c));
const isDigit = (c: string | undefined): boolean => Boolean(c && c >= '0' && c <= '9');
const address = (row: number, col: number): string => `${columnName(col)}${row}`;

function skipString(s: string, start: number): number {
  let i = start + 1;
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

function skipBracket(s: string, start: number): number {
  let depth = 0;
  for (let i = start; i < s.length; i++) {
    if (s[i] === '[') depth++;
    else if (s[i] === ']' && --depth === 0) return i + 1;
  }
  return s.length;
}

interface SheetPrefixResult {
  readonly name: string;
  readonly after: number;
  readonly external: boolean;
}

function sheetPrefix(s: string, i: number): SheetPrefixResult | null {
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
  if (!wordChar(s[i]) || isDigit(s[i])) return null;
  let p = i;
  while (p < s.length && wordChar(s[p])) p++;
  if (s[p] === '!' && p > i) return { name: s.slice(i, p), after: p + 1, external: false };
  return null;
}

function atom(s: string, from: number): RefAtom | null {
  let p = from;
  if (s[p] === '$') p++;
  const start = p;
  while (isLetter(s[p]) && p - start < 4) p++;
  const letters = s.slice(start, p);
  if (letters.length > 0 && letters.length <= 3) {
    const col = columnNumber(letters);
    if (col < 1 || col > MAX_COL) return null;
    if (s[p] === '$') p++;
    const rs = p;
    while (isDigit(s[p]) && p - rs <= 7) p++;
    if (p > rs) {
      const row = Number(s.slice(rs, p));
      return row > 0 && row <= MAX_ROW ? { kind: 'cell', row, col, end: p } : null;
    }
    return { kind: 'column', col, end: p };
  }
  p = from;
  if (s[p] === '$') p++;
  const rs = p;
  while (isDigit(s[p]) && p - rs <= 7) p++;
  if (p === rs) return null;
  const row = Number(s.slice(rs, p));
  return row > 0 && row <= MAX_ROW ? { kind: 'row', row, end: p } : null;
}

function bounds(
  a: RefAtom,
  b: RefAtom,
): { readonly kind: FormulaRefKind; readonly start: FormulaRefCoord; readonly end: FormulaRefCoord } {
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

export function parseFormula(
  formula: string | null | undefined,
  currentSheet = '',
  maxRefs = 256,
): FormulaToken[] {
  const s = formula ?? '';
  const refs: FormulaToken[] = [];
  const colorKeys = new Map<string, number>();
  for (let i = 0; i < s.length && refs.length < maxRefs; ) {
    if (s[i] === '"') {
      i = skipString(s, i);
      continue;
    }
    if (s[i] === '[') {
      i = skipBracket(s, i);
      continue;
    }
    if (i > 0 && (wordChar(s[i - 1]) || s[i - 1] === '!' || s[i - 1] === ']' || s[i - 1] === '$')) {
      i++;
      continue;
    }
    const prefix = sheetPrefix(s, i);
    if (s[i] === "'" && !prefix) {
      // Quoted sheet name that is not followed by a reference.
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
    if (i > 0 && s[i - 1] === ':' && prefix) {
      i = prefix.after;
      continue;
    }
    const sheet = prefix?.name || currentSheet;
    const startAt = prefix ? prefix.after : i;
    const a = atom(s, startAt);
    if (!a) {
      i = prefix ? Math.max(i + 1, prefix.after) : i + 1;
      continue;
    }
    let p = a.end;
    let b: RefAtom = a;
    if (s[p] === ':') {
      let next = p + 1;
      const secondPrefix = sheetPrefix(s, next);
      if (secondPrefix) {
        if (secondPrefix.name.toLocaleLowerCase() !== sheet.toLocaleLowerCase()) {
          i = secondPrefix.after;
          continue;
        }
        next = secondPrefix.after;
      }
      const second = atom(s, next);
      if (second?.kind === a.kind) {
        b = second;
        p = second.end;
      }
    }
    if (
      (a.kind !== 'cell' && b === a) ||
      wordChar(s[p]) ||
      s[p] === '!' ||
      s[p] === '(' ||
      s[p] === '$'
    ) {
      i = prefix ? Math.max(i + 1, prefix.after) : i + 1;
      continue;
    }
    const r = bounds(a, b);
    const external = Boolean(prefix?.external);
    const key = `${sheet.toLocaleLowerCase()}|${r.kind}|${r.start.row},${r.start.col}|${r.end.row},${r.end.col}`;
    let colorIndex = colorKeys.get(key);
    if (colorIndex === undefined) {
      colorIndex = colorKeys.size % PALETTE.length;
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

export class FormulaInspector {
  public expanded = false;
  public active = false;
  public tokens: FormulaToken[] = [];

  private readonly o: FormulaInspectorOptions;
  private readonly el: HTMLElement;
  private readonly pop: HTMLElement;
  private readonly expandButton: HTMLElement | null;
  private readonly toolbar: Element | null;
  private readonly workspace: Element | null;
  private readonly tokensBySheet = new Map<string, FormulaToken[]>();
  private workbook = new AbortController();
  private readonly refCache = new Map<string, Promise<CellData>>();
  private readonly indexTasks = new Map<string, Promise<void>>();
  private readonly readySheets = new Set<string>();

  private head!: HTMLDivElement;
  private content!: HTMLDivElement;
  private actions!: HTMLDivElement;
  private jump!: HTMLButtonElement;
  private copy!: HTMLButtonElement;

  private sheet = '';
  private hoverIndex = -1;
  private pinIndex = -1;
  private generation = 0;
  private previewID = 0;
  private pendingTimer: ReturnType<typeof setTimeout> | undefined;

  constructor(options: FormulaInspectorOptions) {
    this.o = options;
    this.el = options.element;
    this.pop = options.tooltip;
    this.expandButton = byId('formulaExpandBtn');
    this.toolbar = this.el.closest('.formula-toolbar');
    this.workspace = this.el.closest('.workspace');
    this.makeTooltip();
    this.bind();
  }

  resetWorkbook(): void {
    this.workbook.abort();
    this.workbook = new AbortController();
    this.refCache.clear();
    this.indexTasks.clear();
    this.readySheets.clear();
    this.clear();
  }

  clear(): void {
    this.generation++;
    this.previewID++;
    if (this.pendingTimer !== undefined) {
      clearTimeout(this.pendingTimer);
      this.pendingTimer = undefined;
    }
    this.active = false;
    this.tokens = [];
    this.tokensBySheet.clear();
    this.sheet = '';
    this.hoverIndex = -1;
    this.pinIndex = -1;
    this.el.textContent = '';
    this.el.scrollLeft = 0;
    this.el.scrollTop = 0;
    this.el.classList.remove('formula-focused');
    this.pop.hidden = true;
    this.decorateAll();
  }

  show(cell: CellData | null | undefined, sheet: string | null | undefined): void {
    this.generation++;
    this.previewID++;
    if (this.pendingTimer !== undefined) {
      clearTimeout(this.pendingTimer);
      this.pendingTimer = undefined;
    }
    this.active = false;
    this.hoverIndex = -1;
    this.pinIndex = -1;
    this.pop.hidden = true;
    this.sheet = sheet ?? '';
    const text = cell?.hasFormula ? (cell.formula ?? '') : (cell?.raw ?? '');
    this.tokens = cell?.hasFormula ? parseFormula(text, this.sheet) : [];
    // Resolve case-insensitive Excel sheet names back to their canonical
    // spelling before making requests or navigating between sheet tabs.
    for (const token of this.tokens) {
      const actual = this.o.state.sheets.find(
        (sh) => sh.name.toLocaleLowerCase() === token.sheet.toLocaleLowerCase(),
      );
      if (actual) token.sheet = actual.name;
    }
    this.tokensBySheet.clear();
    this.tokens.forEach((token, i) => {
      token.index = i;
      const lower = token.sheet.toLocaleLowerCase();
      let list = this.tokensBySheet.get(lower);
      if (!list) {
        list = [];
        this.tokensBySheet.set(lower, list);
      }
      if (!token.external) list.push(token);
    });
    this.el.title =
      this.tokens.length >= 256 ? '引用较多：目前只交互显示前 256 处引用' : '';
    if (!this.tokens.length) {
      this.el.textContent = text;
      this.el.classList.remove('formula-focused');
      this.decorateAll();
      return;
    }
    const f = document.createDocumentFragment();
    let pos = 0;
    for (let n = 0; n < this.tokens.length; n++) {
      const token = this.tokens[n];
      if (!token) continue;
      if (token.startIndex > pos) {
        f.appendChild(document.createTextNode(text.slice(pos, token.startIndex)));
      }
      const span = document.createElement('span');
      span.className = `formula-token${token.external ? ' formula-token-external' : ''}`;
      span.dataset.token = String(n);
      span.style.setProperty('--formula-ref-color', PALETTE[token.colorIndex] ?? PALETTE[0] ?? '');
      span.textContent = text.slice(token.startIndex, token.endIndex);
      span.tabIndex = 0;
      span.setAttribute('role', 'button');
      span.setAttribute('aria-label', `${token.label}：悬停查看引用值，回车高亮`);
      f.appendChild(span);
      pos = token.endIndex;
    }
    if (pos < text.length) f.appendChild(document.createTextNode(text.slice(pos)));
    this.el.replaceChildren(f);
    this.el.scrollLeft = 0;
    this.el.scrollTop = 0;
    this.el.classList.remove('formula-focused');
    this.decorateAll();
  }

  setExpanded(expanded: boolean): void {
    this.expanded = expanded;
    this.toolbar?.classList.toggle('formula-expanded', this.expanded);
    this.workspace?.classList.toggle('formula-expanded', this.expanded);
    if (this.expandButton) {
      const label = this.expanded ? '收起公式栏' : '展开公式栏';
      this.expandButton.setAttribute('aria-expanded', String(this.expanded));
      this.expandButton.setAttribute('aria-label', label);
      this.expandButton.title = this.expanded
        ? '收起公式栏（单行显示）'
        : '展开公式栏（长公式换行显示）';
    }
    this.el.scrollTop = 0;
    this.el.scrollLeft = 0;
    // The spreadsheet viewport becomes taller/shorter when the formula
    // editor expands, so refresh only the currently visible virtual tiles.
    requestAnimationFrame(() => this.o.onLayoutChange?.());
  }

  private bind(): void {
    this.expandButton?.addEventListener('click', () => this.setExpanded(!this.expanded));
    // A normal mouse wheel can be used with Shift to reveal text beyond the
    // end of the one-line bar. No visible or overlapping scrollbar is needed.
    this.el.addEventListener(
      'wheel',
      (event: WheelEvent) => {
        if (this.expanded || !event.shiftKey || this.el.scrollWidth <= this.el.clientWidth) return;
        if (event.deltaY) {
          event.preventDefault();
          this.el.scrollLeft += event.deltaY;
        }
      },
      { passive: false },
    );
    this.el.addEventListener('click', (event: MouseEvent) => {
      if (!this.tokens.length) return;
      this.activate();
      const target = event.target instanceof Element ? event.target : null;
      const node = target?.closest<HTMLElement>('.formula-token');
      if (node) {
        const n = Number(node.dataset.token);
        this.pinIndex = n;
        this.hoverIndex = n;
        this.preview(n, node, true);
      } else {
        this.pinIndex = -1;
        this.hoverIndex = -1;
        this.pop.hidden = true;
        this.decorateAll();
      }
    });
    this.el.addEventListener('dblclick', (event: MouseEvent) => {
      const target = event.target instanceof Element ? event.target : null;
      const node = target?.closest<HTMLElement>('.formula-token');
      if (!node) return;
      const token = this.tokenAt(Number(node.dataset.token));
      if (!token || token.external || !this.o.sheetExists(token.sheet)) return;
      event.preventDefault();
      this.pop.hidden = true;
      this.o
        .navigate(token.sheet, address(token.start.row, token.start.col), false)
        .catch((err: unknown) => this.o.notify(errorMessage(err), 'error'));
    });
    this.el.addEventListener('pointermove', (event: PointerEvent) => {
      if (!this.active || !this.tokens.length) return;
      const target = event.target instanceof Element ? event.target : null;
      const node = target?.closest<HTMLElement>('.formula-token');
      const n = node ? Number(node.dataset.token) : -1;
      if (n === this.hoverIndex) return;
      this.hoverIndex = n;
      if (n >= 0 && node) {
        this.preview(n, node, false);
      } else if (this.pinIndex < 0) {
        this.previewID++;
        if (this.pendingTimer !== undefined) {
          clearTimeout(this.pendingTimer);
          this.pendingTimer = undefined;
        }
        this.pop.hidden = true;
      }
      this.decorateAll();
    });
    this.el.addEventListener('pointerleave', () => {
      this.hoverIndex = -1;
      if (this.pinIndex < 0) {
        this.previewID++;
        if (this.pendingTimer !== undefined) {
          clearTimeout(this.pendingTimer);
          this.pendingTimer = undefined;
        }
        this.pop.hidden = true;
      }
      this.decorateAll();
    });
    this.el.addEventListener('keydown', (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        event.stopPropagation();
        this.pinIndex = -1;
        this.hoverIndex = -1;
        this.pop.hidden = true;
        this.active = false;
        this.el.classList.remove('formula-focused');
        this.decorateAll();
        this.o.grid?.focus({ preventScroll: true });
        return;
      }
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'c') {
        event.stopPropagation();
        const selected = window.getSelection()?.toString();
        if (!selected) {
          event.preventDefault();
          void this.o.copy(this.el.textContent);
        }
        return;
      }
      const target = event.target instanceof Element ? event.target : null;
      const node = target?.closest<HTMLElement>('.formula-token');
      if (node && (event.key === 'Enter' || event.key === ' ')) {
        event.preventDefault();
        event.stopPropagation();
        this.activate();
        const n = Number(node.dataset.token);
        this.pinIndex = n;
        this.hoverIndex = n;
        this.preview(n, node, true);
        this.decorateAll();
      } else if (event.key.startsWith('Arrow')) {
        event.stopPropagation();
      }
    });
    this.pop.addEventListener('pointerenter', () => {
      if (this.pinIndex >= 0 && this.pendingTimer !== undefined) {
        clearTimeout(this.pendingTimer);
        this.pendingTimer = undefined;
      }
    });
    document.addEventListener('pointerdown', (event: PointerEvent) => {
      const target = event.target;
      if (
        this.pinIndex < 0 ||
        (target instanceof Node && (this.el.contains(target) || this.pop.contains(target)))
      ) {
        return;
      }
      this.pinIndex = -1;
      this.hoverIndex = -1;
      this.pop.hidden = true;
      this.decorateAll();
    });
    document.addEventListener('keydown', (event: KeyboardEvent) => {
      const target = event.target;
      if (
        event.key !== 'Escape' ||
        !this.active ||
        (target instanceof Element && target.closest('dialog')) ||
        (target instanceof Node && this.el.contains(target))
      ) {
        return;
      }
      this.pinIndex = -1;
      this.hoverIndex = -1;
      this.pop.hidden = true;
      this.active = false;
      this.el.classList.remove('formula-focused');
      this.decorateAll();
    });
  }

  private makeTooltip(): void {
    const pop = this.pop;
    pop.replaceChildren();
    this.head = document.createElement('div');
    this.head.className = 'formula-preview-heading';
    this.content = document.createElement('div');
    this.content.className = 'formula-preview-content';
    this.actions = document.createElement('div');
    this.actions.className = 'formula-preview-actions';
    this.jump = document.createElement('button');
    this.jump.textContent = '定位引用';
    this.jump.type = 'button';
    this.copy = document.createElement('button');
    this.copy.textContent = '复制地址';
    this.copy.type = 'button';
    this.actions.append(this.jump, this.copy);
    pop.append(this.head, this.content, this.actions);

    this.jump.addEventListener('click', () => {
      const t = this.tokenAt(this.pinIndex >= 0 ? this.pinIndex : this.hoverIndex);
      if (!t || t.external) return;
      this.pop.hidden = true;
      this.o
        .navigate(t.sheet, address(t.start.row, t.start.col), false)
        .catch((err: unknown) => this.o.notify(errorMessage(err), 'error'));
    });
    this.copy.addEventListener('click', () => {
      const t = this.tokenAt(this.pinIndex >= 0 ? this.pinIndex : this.hoverIndex);
      if (t) void this.o.copy(t.label);
    });
  }

  activate(): void {
    if (!this.active) {
      this.active = true;
      this.el.classList.add('formula-focused');
      this.decorateAll();
    }
  }

  tokenAt(n: number): FormulaToken | null {
    return this.tokens[n] ?? null;
  }

  currentTarget(): FormulaToken | null {
    return this.tokenAt(this.hoverIndex >= 0 ? this.hoverIndex : this.pinIndex);
  }

  matchAt(row: number, col: number, sheet: string): FormulaRefMatch | null {
    if (!this.active) return null;
    const relevant = this.tokensBySheet.get(sheet.toLocaleLowerCase()) ?? [];
    let candidate: FormulaRefMatch | null = null;
    for (const t of relevant) {
      if (row < t.start.row || row > t.end.row || col < t.start.col || col > t.end.col) continue;
      if (this.hoverIndex === t.index || this.pinIndex === t.index) {
        return { token: t, focus: true };
      }
      candidate ??= { token: t, focus: false };
    }
    return candidate;
  }

  decorateCell(el: HTMLElement, row: number, col: number, sheet: string): void {
    const match = this.matchAt(row, col, sheet);
    if (!match) {
      if (el.classList.contains('formula-referenced')) {
        el.classList.remove('formula-referenced', 'formula-ref-focus');
        el.style.removeProperty('--formula-ref-color');
      }
      return;
    }
    el.classList.add('formula-referenced');
    el.classList.toggle('formula-ref-focus', match.focus);
    const color = PALETTE[match.token.colorIndex] ?? PALETTE[0] ?? '';
    if (el.style.getPropertyValue('--formula-ref-color') !== color) {
      el.style.setProperty('--formula-ref-color', color);
    }
  }

  decorateAll(): void {
    this.o.state.decorVersion++;
    const currentSheet = this.o.state.sheet ?? '';
    for (const node of document.querySelectorAll<HTMLElement>(
      '.grid-region .grid-cell[data-row][data-col]',
    )) {
      this.decorateCell(node, Number(node.dataset.row), Number(node.dataset.col), currentSheet);
    }
    for (const tokenNode of this.el.querySelectorAll<HTMLElement>('.formula-token')) {
      const n = Number(tokenNode.dataset.token);
      tokenNode.classList.toggle('is-hovered', n === this.hoverIndex || n === this.pinIndex);
    }
  }

  private place(node: HTMLElement): void {
    const rect = node.getBoundingClientRect();
    const width = Math.min(390, window.innerWidth - 16);
    const x = Math.max(8, Math.min(window.innerWidth - width - 8, rect.left));
    const below = rect.bottom + 9;
    const height = 188;
    const y = below + height < window.innerHeight ? below : Math.max(8, rect.top - height - 8);
    this.pop.style.left = `${x}px`;
    this.pop.style.top = `${y}px`;
  }

  preview(n: number, node: HTMLElement, pinned: boolean): void {
    const t = this.tokenAt(n);
    if (!t) return;
    const request = ++this.previewID;
    if (this.pendingTimer !== undefined) {
      clearTimeout(this.pendingTimer);
      this.pendingTimer = undefined;
    }
    const exists = this.o.sheetExists(t.sheet);
    this.head.textContent = t.label;
    this.content.textContent = t.external
      ? '外部工作簿引用：不读取外部文件'
      : exists
        ? '读取引用单元格…'
        : '工作簿中不存在对应工作表';
    this.actions.hidden = !pinned || t.external || !exists;
    this.pop.classList.toggle('is-pinned', pinned);
    this.pop.hidden = false;
    this.place(node);
    if (t.external || !exists) return;
    const generation = this.generation;
    const update = (message: string): void => {
      if (this.generation === generation && this.previewID === request) {
        this.content.textContent = message;
      }
    };
    this.pendingTimer = setTimeout(() => {
      void (async () => {
        try {
          const start = t.start;
          const size = (t.end.row - start.row + 1) * (t.end.col - start.col + 1);
          const prefix =
            t.kind === 'cell'
              ? ''
              : `${t.kind === 'row' ? '整行' : t.kind === 'column' ? '整列' : '区域'}引用 · ${t.end.row - start.row + 1} 行 × ${t.end.col - start.col + 1} 列\n`;
          const first = await this.readCell(t.sheet, address(start.row, start.col), update);
          if (this.generation !== generation || this.previewID !== request) return;
          let msg = prefix + this.describe(first, address(start.row, start.col));
          update(msg);
          if (t.kind === 'range' && size > 1 && size <= 10000) {
            const endAddress = address(t.end.row, t.end.col);
            const last = await this.readCell(t.sheet, endAddress, update);
            if (this.generation !== generation || this.previewID !== request) return;
            msg += `\n终点 ${this.describe(last, endAddress)}`;
            update(msg);
          } else if (t.kind !== 'cell') {
            msg += '\n仅预览起点，不计算区域结果';
            update(msg);
          }
        } catch (err: unknown) {
          update(`读取失败：${errorMessage(err)}`);
        }
      })();
    }, pinned ? 0 : 140);
  }

  describe(cell: CellData | null | undefined, ref: string): string {
    const value = cell?.cacheMissing ? '无缓存（未重算）' : (cell?.display ?? '');
    const raw = cell?.raw ?? '';
    return (
      `${ref} · 保存值：${value === '' ? '（空白）' : value}` +
      (raw !== '' && raw !== value ? `\n原始值：${raw}` : '') +
      (cell?.hasFormula ? `\n公式：${cell.formula || '（未解析到公式文本）'}` : '')
    );
  }

  async ensureIndex(sheet: string, progress: (message: string) => void): Promise<void> {
    if (sheet === this.o.state.sheet && this.o.state.indexReady) return;
    if (this.readySheets.has(sheet)) return;
    const existing = this.indexTasks.get(sheet);
    if (existing) return existing;
    const { signal } = this.workbook;
    const activity = beginActivity('正在读取引用工作表');
    const task = (async (): Promise<void> => {
      const started = await this.o.api('/api/index/start', {
        signal,
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ sheet }),
      }, decodeIndexStartResponse);
      signal.throwIfAborted();
      if (started.ready) {
        this.readySheets.add(sheet);
        return;
      }
      for (;;) {
        await new Promise<void>((r) => setTimeout(r, 270));
        signal.throwIfAborted();
        const result = await this.o.api(
          `/api/index/status?id=${encodeURIComponent(started.jobId)}`,
          { signal },
          decodeIndexStatusResponse,
        );
        signal.throwIfAborted();
        activity?.job({
          doneBytes: result.doneBytes,
          totalBytes: result.totalBytes,
          progress: result.progress,
          stage: result.stage,
        });
        progress(`首次读取「${sheet}」：建立索引 ${Math.round(result.progress ?? 0)}%`);
        if (result.status === 'done') {
          this.readySheets.add(sheet);
          return;
        }
        if (result.status === 'error' || result.status === 'cancelled') {
          throw new Error(result.error || '建立索引失败');
        }
      }
    })();
    this.indexTasks.set(sheet, task);
    try {
      await task;
    } finally {
      activity?.end();
      if (this.indexTasks.get(sheet) === task) this.indexTasks.delete(sheet);
    }
  }

  async readCell(
    sheet: string,
    ref: string,
    progress: (message: string) => void,
  ): Promise<CellData> {
    const key = `${sheet}!${ref}`;
    const cached = this.refCache.get(key);
    if (cached) return cached;
    const inViewport =
      sheet === this.o.state.sheet
        ? (this.o.state.cellMap.get(ref) ?? this.o.state.frozenCellMap.get(ref))
        : undefined;
    if (inViewport) {
      this.refCache.set(key, Promise.resolve(inViewport));
      return inViewport;
    }
    const { signal } = this.workbook;
    const task = (async (): Promise<CellData> => {
      await this.ensureIndex(sheet, progress);
      signal.throwIfAborted();
      const cell = await this.o.getCell(sheet, ref);
      signal.throwIfAborted();
      return cell;
    })();
    this.refCache.set(key, task);
    if (this.refCache.size > 512) {
      const oldest = this.refCache.keys().next();
      if (!oldest.done) this.refCache.delete(oldest.value);
    }
    try {
      return await task;
    } catch (err: unknown) {
      if (this.refCache.get(key) === task) this.refCache.delete(key);
      throw err;
    }
  }
}
