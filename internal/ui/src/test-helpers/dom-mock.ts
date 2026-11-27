/**
 * Lightweight, strictly-typed DOM & Browser global test harness for Node's built-in
 * `node:test` runner. Supports `instanceof` checks across all HTML element subclasses,
 * CSS selector queries, event dispatch/bubbling, `<dialog>` lifecycle, 2D canvas mock,
 * `localStorage`/`sessionStorage`, and automatic parsing of `internal/ui/web/index.html`.
 */
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const INDEX_HTML_PATH = path.resolve(HERE, '../../web/index.html');

interface IndexOptionSpec {
  readonly value: string;
  readonly text: string;
  readonly selected: boolean;
  readonly disabled: boolean;
}

interface IndexElementSpec {
  readonly id: string;
  readonly tag: string;
  readonly typeAttr: string;
  readonly attrs: Readonly<Record<string, string>>;
  readonly options?: readonly IndexOptionSpec[];
}

let cachedIndexSpecs: readonly IndexElementSpec[] | null = null;

function parseIndexHtmlSpecs(): readonly IndexElementSpec[] {
  if (cachedIndexSpecs) return cachedIndexSpecs;
  const html = fs.readFileSync(INDEX_HTML_PATH, 'utf8');
  const specs: IndexElementSpec[] = [];
  const seen = new Set<string>();
  const tagRegex = /<([a-zA-Z][a-zA-Z0-9-]*)\b([^>]*)>/g;
  let match: RegExpExecArray | null = tagRegex.exec(html);
  while (match !== null) {
    const tag = (match[1] ?? '').toLowerCase();
    const rawAttrs = match[2] ?? '';
    const idMatch = /\bid="([^"]+)"/.exec(rawAttrs);
    if (idMatch?.[1] && !seen.has(idMatch[1])) {
      seen.add(idMatch[1]);
      const attrs: Record<string, string> = {};
      const attrRegex = /([a-zA-Z0-9:-]+)(?:="([^"]*)")?/g;
      let am: RegExpExecArray | null = attrRegex.exec(rawAttrs);
      while (am !== null) {
        const name = am[1];
        if (name) attrs[name] = am[2] ?? '';
        am = attrRegex.exec(rawAttrs);
      }
      let options: IndexOptionSpec[] | undefined;
      if (tag === 'select') {
        const afterStart = match.index + match[0].length;
        const closeIdx = html.indexOf('</select>', afterStart);
        if (closeIdx > afterStart) {
          const inner = html.slice(afterStart, closeIdx);
          const optRegex = /<option\b([^>]*)>([\s\S]*?)<\/option>/gi;
          let om: RegExpExecArray | null = optRegex.exec(inner);
          const parsedOpts: IndexOptionSpec[] = [];
          while (om !== null) {
            const optAttrs = om[1] ?? '';
            const optText = (om[2] ?? '').trim();
            const valMatch = /\bvalue="([^"]*)"/.exec(optAttrs);
            parsedOpts.push({
              value: valMatch ? (valMatch[1] ?? '') : optText,
              text: optText,
              selected: /\bselected\b/i.test(optAttrs),
              disabled: /\bdisabled\b/i.test(optAttrs),
            });
            om = optRegex.exec(inner);
          }
          if (parsedOpts.length > 0) options = parsedOpts;
        }
      }
      specs.push({
        id: idMatch[1],
        tag,
        typeAttr: attrs.type ?? '',
        attrs,
        options,
      });
    }
    match = tagRegex.exec(html);
  }
  cachedIndexSpecs = specs;
  return specs;
}

export class MockClassList {
  private readonly tokens = new Set<string>();
  private readonly onChange: () => void;

  constructor(onChange: () => void) {
    this.onChange = onChange;
  }

  syncFromString(value: string): void {
    this.tokens.clear();
    for (const part of value.trim().split(/\s+/)) {
      if (part) this.tokens.add(part);
    }
  }

  add(...names: readonly string[]): void {
    for (const name of names) {
      if (name) this.tokens.add(name);
    }
    this.onChange();
  }

  remove(...names: readonly string[]): void {
    for (const name of names) this.tokens.delete(name);
    this.onChange();
  }

  toggle(name: string, force?: boolean): boolean {
    if (force === true) {
      this.tokens.add(name);
      this.onChange();
      return true;
    }
    if (force === false) {
      this.tokens.delete(name);
      this.onChange();
      return false;
    }
    if (this.tokens.has(name)) {
      this.tokens.delete(name);
      this.onChange();
      return false;
    }
    this.tokens.add(name);
    this.onChange();
    return true;
  }

  contains(name: string): boolean {
    return this.tokens.has(name);
  }

  get length(): number {
    return this.tokens.size;
  }

  [Symbol.iterator](): IterableIterator<string> {
    return this.tokens.values();
  }

  toString(): string {
    return [...this.tokens].join(' ');
  }
}

function camelToKebab(prop: string): string {
  if (prop.startsWith('--')) return prop;
  return prop.replace(/[A-Z]/g, (m) => `-${m.toLowerCase()}`);
}

function kebabToCamel(prop: string): string {
  return prop.replace(/-([a-z])/g, (_, ch: string) => ch.toUpperCase());
}

export class MockStyleDeclaration {
  private readonly props = new Map<string, string>();
  public readonly proxy: Record<string, unknown>;

  constructor() {
    this.proxy = new Proxy<Record<string, unknown>>(
      {},
      {
        get: (_target, prop) => {
          if (typeof prop !== 'string') return undefined;
          if (prop === 'setProperty') {
            return (name: string, value: string): void => {
              if (value === '') this.props.delete(name);
              else this.props.set(name, value);
            };
          }
          if (prop === 'getPropertyValue') {
            return (name: string): string => this.props.get(name) ?? '';
          }
          if (prop === 'removeProperty') {
            return (name: string): string => {
              const prev = this.props.get(name) ?? '';
              this.props.delete(name);
              return prev;
            };
          }
          if (prop === 'cssText') {
            return [...this.props.entries()].map(([k, v]) => `${k}: ${v};`).join(' ');
          }
          return this.props.get(camelToKebab(prop)) ?? '';
        },
        set: (_target, prop, value: unknown) => {
          if (typeof prop !== 'string') return true;
          if (prop === 'cssText') {
            this.props.clear();
            const text = typeof value === 'string' ? value : '';
            for (const part of text.split(';')) {
              const idx = part.indexOf(':');
              if (idx < 0) continue;
              const k = part.slice(0, idx).trim();
              const v = part.slice(idx + 1).trim();
              if (k && v) this.props.set(k, v);
            }
            return true;
          }
          const key = camelToKebab(prop);
          const str =
            typeof value === 'string'
              ? value
              : typeof value === 'number' || typeof value === 'boolean'
                ? String(value)
                : '';
          if (str === '') this.props.delete(key);
          else this.props.set(key, str);
          return true;
        },
      },
    );
  }
}

interface ListenerRecord {
  readonly fn: (event: Event) => void;
  readonly capture: boolean;
  readonly once: boolean;
}

export class MockNode {
  public readonly nodeType: number;
  public parentNode: MockNode | null = null;
  public readonly childNodes: MockNode[] = [];
  protected readonly listeners = new Map<string, ListenerRecord[]>();

  constructor(nodeType: number) {
    this.nodeType = nodeType;
  }

  [Symbol.for('nodejs.util.inspect.custom')](): string {
    if (this instanceof MockElement) {
      const id = this.id ? `#${this.id}` : '';
      const cls = this.className ? `.${this.className.split(/\s+/).join('.')}` : '';
      return `<${this.tagName.toLowerCase()}${id}${cls}>`;
    }
    return `[MockNode type=${this.nodeType}]`;
  }

  get parentElement(): MockElement | null {
    return this.parentNode instanceof MockElement ? this.parentNode : null;
  }

  get firstChild(): MockNode | null {
    return this.childNodes[0] ?? null;
  }

  get lastChild(): MockNode | null {
    return this.childNodes.at(-1) ?? null;
  }

  get isConnected(): boolean {
    if (this.nodeType === 9) return true;
    let cur: MockNode | null = this.parentNode;
    while (cur) {
      if (cur.nodeType === 9) return true;
      cur = cur.parentNode;
    }
    return false;
  }

  get textContent(): string {
    return this.childNodes.map((child) => child.textContent).join('');
  }

  set textContent(value: string) {
    for (const child of this.childNodes) child.parentNode = null;
    this.childNodes.length = 0;
    if (value !== '') {
      const text = new MockTextNode(value);
      text.parentNode = this;
      this.childNodes.push(text);
    }
  }

  appendChild<T extends MockNode>(node: T): T {
    if (node instanceof MockDocumentFragment) {
      const moved = [...node.childNodes];
      node.childNodes.length = 0;
      for (const child of moved) {
        child.parentNode = this;
        this.childNodes.push(child);
      }
      return node;
    }
    if (node.parentNode) node.parentNode.removeChild(node);
    node.parentNode = this;
    this.childNodes.push(node);
    return node;
  }

  insertBefore<T extends MockNode>(node: T, ref: MockNode | null): T {
    if (!ref) return this.appendChild(node);
    const index = this.childNodes.indexOf(ref);
    if (index < 0) return this.appendChild(node);
    if (node instanceof MockDocumentFragment) {
      const moved = [...node.childNodes];
      node.childNodes.length = 0;
      for (let i = 0; i < moved.length; i++) {
        const child = moved[i];
        if (!child) continue;
        child.parentNode = this;
        this.childNodes.splice(index + i, 0, child);
      }
      return node;
    }
    if (node.parentNode) node.parentNode.removeChild(node);
    node.parentNode = this;
    this.childNodes.splice(index, 0, node);
    return node;
  }

  removeChild<T extends MockNode>(node: T): T {
    const idx = this.childNodes.indexOf(node);
    if (idx >= 0) {
      this.childNodes.splice(idx, 1);
      node.parentNode = null;
    }
    return node;
  }

  remove(): void {
    this.parentNode?.removeChild(this);
  }

  contains(other: MockNode | null): boolean {
    let cur = other;
    while (cur) {
      if (cur === this) return true;
      cur = cur.parentNode;
    }
    return false;
  }

  append(...nodes: readonly (MockNode | string)[]): void {
    for (const item of nodes) {
      this.appendChild(typeof item === 'string' ? new MockTextNode(item) : item);
    }
  }

  prepend(...nodes: readonly (MockNode | string)[]): void {
    const first = this.childNodes[0] ?? null;
    for (const item of nodes) {
      this.insertBefore(typeof item === 'string' ? new MockTextNode(item) : item, first);
    }
  }

  replaceChildren(...nodes: readonly (MockNode | string)[]): void {
    for (const child of this.childNodes) child.parentNode = null;
    this.childNodes.length = 0;
    this.append(...nodes);
  }

  before(...nodes: readonly (MockNode | string)[]): void {
    if (!this.parentNode) return;
    for (const item of nodes) {
      this.parentNode.insertBefore(typeof item === 'string' ? new MockTextNode(item) : item, this);
    }
  }

  after(...nodes: readonly (MockNode | string)[]): void {
    if (!this.parentNode) return;
    const idx = this.parentNode.childNodes.indexOf(this);
    const next = idx >= 0 ? (this.parentNode.childNodes[idx + 1] ?? null) : null;
    for (const item of nodes) {
      this.parentNode.insertBefore(typeof item === 'string' ? new MockTextNode(item) : item, next);
    }
  }

  replaceWith(...nodes: readonly (MockNode | string)[]): void {
    if (!this.parentNode) return;
    this.before(...nodes);
    this.remove();
  }

  cloneNode(_deep = false): MockNode {
    return new MockNode(this.nodeType);
  }

  addEventListener(
    type: string,
    listener: ((event: Event) => void) | { handleEvent(event: Event): void } | null,
    options?: boolean | AddEventListenerOptions,
  ): void {
    if (!listener) return;
    const fn = typeof listener === 'function' ? listener : (e: Event) => listener.handleEvent(e);
    const capture = typeof options === 'boolean' ? options : Boolean(options?.capture);
    const once = typeof options === 'object' && Boolean(options.once);
    const list = this.listeners.get(type) ?? [];
    list.push({ fn, capture, once });
    this.listeners.set(type, list);
    if (typeof options === 'object' && options.signal) {
      options.signal.addEventListener(
        'abort',
        () => this.removeEventListener(type, listener, options),
        { once: true },
      );
    }
  }

  removeEventListener(
    type: string,
    listener: ((event: Event) => void) | { handleEvent(event: Event): void } | null,
    options?: boolean | EventListenerOptions,
  ): void {
    if (!listener) return;
    const capture = typeof options === 'boolean' ? options : Boolean(options?.capture);
    const list = this.listeners.get(type);
    if (!list) return;
    const next = list.filter((rec) => !(rec.fn === listener && rec.capture === capture));
    this.listeners.set(type, next);
  }

  dispatchEvent(event: Event): boolean {
    const ancestors: MockNode[] = [];
    let cur: MockNode | null = this.parentNode;
    while (cur) {
      ancestors.push(cur);
      cur = cur.parentNode;
    }
    try {
      Object.defineProperty(event, 'target', { value: this, configurable: true });
    } catch {
      // Native Event target may be non-configurable in some environments.
    }

    const stopState = { stopped: false };
    const origStop = event.stopPropagation.bind(event);
    event.stopPropagation = () => {
      stopState.stopped = true;
      origStop();
    };

    const invoke = (node: MockNode, capturePhase: boolean): void => {
      try {
        Object.defineProperty(event, 'currentTarget', { value: node, configurable: true });
      } catch {
        // Ignore non-configurable currentTarget.
      }
      const list = [...(node.listeners.get(event.type) ?? [])];
      for (const rec of list) {
        if (rec.capture !== capturePhase && node !== this) continue;
        if (rec.once) {
          const curList = node.listeners.get(event.type);
          if (curList) {
            const idx = curList.indexOf(rec);
            if (idx >= 0) curList.splice(idx, 1);
          }
        }
        rec.fn(event);
      }
      if (!capturePhase) {
        const propHandler: unknown = Reflect.get(node, `on${event.type}`);
        if (typeof propHandler === 'function') {
          (propHandler as (e: Event) => void).call(node, event);
        }
      }
    };

    for (let i = ancestors.length - 1; i >= 0; i--) {
      if (stopState.stopped) break;
      const ancestor = ancestors[i];
      if (ancestor) invoke(ancestor, true);
    }
    if (!stopState.stopped) invoke(this, false);
    if (event.bubbles) {
      for (const ancestor of ancestors) {
        if (stopState.stopped) break;
        invoke(ancestor, false);
      }
    }
    return !event.defaultPrevented;
  }
}

export class MockTextNode extends MockNode {
  public nodeValue: string;

  constructor(text: string) {
    super(3);
    this.nodeValue = text;
  }

  override get textContent(): string {
    return this.nodeValue;
  }

  override set textContent(value: string) {
    this.nodeValue = value;
  }

  override cloneNode(): MockTextNode {
    return new MockTextNode(this.nodeValue);
  }
}

export class MockCommentNode extends MockNode {
  public nodeValue: string;

  constructor(text: string) {
    super(8);
    this.nodeValue = text;
  }

  override get textContent(): string {
    return this.nodeValue;
  }

  override cloneNode(): MockCommentNode {
    return new MockCommentNode(this.nodeValue);
  }
}

export class MockDocumentFragment extends MockNode {
  constructor() {
    super(11);
  }

  get children(): MockElement[] {
    return this.childNodes.filter((n): n is MockElement => n instanceof MockElement);
  }

  get childElementCount(): number {
    return this.children.length;
  }
}

function matchesSimpleSelector(el: MockElement, raw: string): boolean {
  let sel = raw.trim();
  if (!sel) return false;
  if (sel === '*') return true;

  // Handle :not(...)
  const notRegex = /:not\(([^)]+)\)/g;
  let notMatch: RegExpExecArray | null = notRegex.exec(sel);
  while (notMatch !== null) {
    const inner = notMatch[1] ?? '';
    if (matchesSimpleSelector(el, inner)) return false;
    notMatch = notRegex.exec(sel);
  }
  sel = sel.replace(/:not\([^)]+\)/g, '');

  // Pseudo-classes
  if (sel.includes(':checked')) {
    if (!(el instanceof MockHTMLInputElement) || !el.checked) return false;
    sel = sel.replaceAll(':checked', '');
  }
  if (sel.includes(':modal')) {
    if (!(el instanceof MockHTMLDialogElement) || !el.open) return false;
    sel = sel.replaceAll(':modal', '');
  }
  if (sel.includes(':disabled')) {
    if (!Reflect.get(el, 'disabled')) return false;
    sel = sel.replaceAll(':disabled', '');
  }

  // Attribute selectors
  const attrRegex = /\[([^\]=~^$*]+)(?:([~^$*]?=)"([^"]*)")?\]/g;
  let attrMatch: RegExpExecArray | null = attrRegex.exec(sel);
  while (attrMatch !== null) {
    const attrName = (attrMatch[1] ?? '').trim();
    const op = attrMatch[2] ?? '';
    const expected = attrMatch[3] ?? '';
    const actual = el.getAttribute(attrName);
    if (!op) {
      if (actual === null) return false;
    } else if (op === '=') {
      if (actual !== expected) return false;
    } else if (op === '^=') {
      if (!actual?.startsWith(expected)) return false;
    } else if (op === '$=') {
      if (!actual?.endsWith(expected)) return false;
    } else if (op === '*=') {
      if (!actual?.includes(expected)) return false;
    }
    attrMatch = attrRegex.exec(sel);
  }
  sel = sel.replace(/\[[^\]]+\]/g, '');

  // Tag, #id, .class parts
  const parts = sel.match(/^[a-zA-Z0-9-]+|[#.][a-zA-Z0-9_-]+/g);
  if (!parts) return sel === '';
  for (const part of parts) {
    if (part.startsWith('#')) {
      if (el.id !== part.slice(1)) return false;
    } else if (part.startsWith('.')) {
      if (!el.classList.contains(part.slice(1))) return false;
    } else if (el.tagName !== part.toUpperCase()) {
      return false;
    }
  }
  return true;
}

function matchesSelectorChain(el: MockElement, selector: string): boolean {
  for (const branch of selector.split(',')) {
    const trimmed = branch.trim();
    if (!trimmed) continue;
    const tokens = trimmed.replace(/\s*>\s*/g, ' > ').split(/\s+/);
    let current: MockElement | null = el;
    let idx = tokens.length - 1;
    let matched = true;
    while (idx >= 0) {
      const part = tokens[idx];
      if (!part) {
        idx--;
        continue;
      }
      if (part === '>') {
        idx--;
        const parentSel = tokens[idx] ?? '';
        current = current.parentElement;
        if (!current || !matchesSimpleSelector(current, parentSel)) {
          matched = false;
          break;
        }
        idx--;
        continue;
      }
      if (idx === tokens.length - 1) {
        if (!matchesSimpleSelector(current, part)) {
          matched = false;
          break;
        }
        idx--;
      } else {
        let anc: MockElement | null = current.parentElement;
        while (anc && !matchesSimpleSelector(anc, part)) {
          anc = anc.parentElement;
        }
        if (!anc) {
          matched = false;
          break;
        }
        current = anc;
        idx--;
      }
    }
    if (matched && idx < 0) return true;
  }
  return false;
}

export class MockElement extends MockNode {
  public readonly tagName: string;
  public readonly ownerDoc: MockDocument | null;
  private readonly attrs = new Map<string, string>();
  public readonly classList: MockClassList;
  public readonly styleDecl = new MockStyleDeclaration();
  public readonly style: Record<string, unknown>;
  public readonly dataset: Record<string, string | undefined>;
  public hidden = false;
  public inert = false;
  public tabIndex = -1;
  public title = '';
  public scrollTop = 0;
  public scrollLeft = 0;
  public scrollWidth = 0;
  public scrollHeight = 0;
  public clientWidth = 800;
  public clientHeight = 600;
  public clientLeft = 0;
  public clientTop = 0;
  public offsetWidth = 800;
  public offsetHeight = 600;
  public onclick: ((event: MouseEvent) => void) | null = null;
  public oninput: ((event: Event) => void) | null = null;
  public onchange: ((event: Event) => void) | null = null;
  public onkeydown: ((event: KeyboardEvent) => void) | null = null;
  public onsubmit: ((event: Event) => void) | null = null;
  public rect: DOMRect = {
    x: 0,
    y: 0,
    left: 0,
    top: 0,
    right: 100,
    bottom: 25,
    width: 100,
    height: 25,
    toJSON() {
      return this;
    },
  };
  private rawInnerHtml = '';

  constructor(tagName: string, ownerDoc: MockDocument | null = null) {
    super(1);
    this.tagName = tagName.toUpperCase();
    this.ownerDoc = ownerDoc;
    this.style = this.styleDecl.proxy;
    this.classList = new MockClassList(() => {
      const value = this.classList.toString();
      if (value) this.attrs.set('class', value);
      else this.attrs.delete('class');
    });
    this.dataset = new Proxy<Record<string, string | undefined>>(
      {},
      {
        get: (_target, prop) => {
          if (typeof prop !== 'string') return undefined;
          const val = this.attrs.get(`data-${camelToKebab(prop)}`);
          return val ?? undefined;
        },
        set: (_target, prop, value: unknown) => {
          if (typeof prop !== 'string') return true;
          if (value === undefined) this.attrs.delete(`data-${camelToKebab(prop)}`);
          else if (typeof value === 'string' || typeof value === 'number' || typeof value === 'boolean') {
            this.attrs.set(`data-${camelToKebab(prop)}`, String(value));
          }
          return true;
        },
        deleteProperty: (_target, prop) => {
          if (typeof prop === 'string') this.attrs.delete(`data-${camelToKebab(prop)}`);
          return true;
        },
        has: (_target, prop) =>
          typeof prop === 'string' && this.attrs.has(`data-${camelToKebab(prop)}`),
        ownKeys: () =>
          [...this.attrs.keys()]
            .filter((k) => k.startsWith('data-'))
            .map((k) => kebabToCamel(k.slice(5))),
        getOwnPropertyDescriptor: (_target, prop) => {
          if (typeof prop !== 'string') return undefined;
          const val = this.attrs.get(`data-${camelToKebab(prop)}`);
          return val === undefined
            ? undefined
            : { value: val, enumerable: true, configurable: true, writable: true };
        },
      },
    );
  }

  get ownerDocument(): MockDocument | null {
    return this.ownerDoc;
  }

  override get isConnected(): boolean {
    if (this.ownerDoc?.restored) return false;
    return super.isConnected;
  }

  get id(): string {
    return this.attrs.get('id') ?? '';
  }

  set id(value: string) {
    const prev = this.attrs.get('id');
    if (prev && this.ownerDoc) this.ownerDoc.unregisterId(prev, this);
    if (value) {
      this.attrs.set('id', value);
      this.ownerDoc?.registerId(value, this);
    } else {
      this.attrs.delete('id');
    }
  }

  get className(): string {
    return this.classList.toString();
  }

  set className(value: string) {
    this.classList.syncFromString(value);
    const next = this.classList.toString();
    if (next) this.attrs.set('class', next);
    else this.attrs.delete('class');
  }

  get children(): MockElement[] {
    return this.childNodes.filter((n): n is MockElement => n instanceof MockElement);
  }

  get childElementCount(): number {
    return this.children.length;
  }

  get firstElementChild(): MockElement | null {
    return this.children[0] ?? null;
  }

  get lastElementChild(): MockElement | null {
    return this.children.at(-1) ?? null;
  }

  get nextElementSibling(): MockElement | null {
    const siblings = this.parentElement?.children ?? [];
    const idx = siblings.indexOf(this);
    return idx >= 0 ? (siblings[idx + 1] ?? null) : null;
  }

  get previousElementSibling(): MockElement | null {
    const siblings = this.parentElement?.children ?? [];
    const idx = siblings.indexOf(this);
    return idx > 0 ? (siblings[idx - 1] ?? null) : null;
  }

  get innerHTML(): string {
    return this.rawInnerHtml || this.textContent;
  }

  set innerHTML(value: string) {
    this.rawInnerHtml = value;
    for (const child of this.childNodes) child.parentNode = null;
    this.childNodes.length = 0;
    if (!value) return;
    const voidTags = new Set([
      'area',
      'base',
      'br',
      'col',
      'embed',
      'hr',
      'img',
      'input',
      'link',
      'meta',
      'param',
      'source',
      'track',
      'wbr',
    ]);
    const stack: MockElement[] = [this];
    const tokenRegex = /<\/([a-zA-Z][a-zA-Z0-9-]*)\s*>|<([a-zA-Z][a-zA-Z0-9-]*)\b([^>]*?)(\/?)>|([^<]+)/g;
    let match: RegExpExecArray | null = tokenRegex.exec(value);
    while (match !== null) {
      const parent = stack.at(-1) ?? this;
      if (match[1]) {
        const closeTag = match[1].toUpperCase();
        for (let i = stack.length - 1; i > 0; i--) {
          if (stack[i]?.tagName === closeTag) {
            stack.length = i;
            break;
          }
        }
      } else if (match[2]) {
        const tag = match[2].toLowerCase();
        const rawAttrs = match[3] ?? '';
        const selfClosing = match[4] === '/' || voidTags.has(tag);
        const child = this.ownerDoc
          ? this.ownerDoc.createMockElement(tag)
          : new MockHTMLElement(tag, null);
        const attrRegex = /([a-zA-Z0-9:-]+)(?:="([^"]*)"|='([^']*)')?/g;
        let am: RegExpExecArray | null = attrRegex.exec(rawAttrs);
        while (am !== null) {
          const name = am[1];
          const val = am[2] ?? am[3] ?? '';
          if (name) {
            if (name === 'hidden') child.hidden = true;
            else if (name === 'disabled' && 'disabled' in child) Reflect.set(child, 'disabled', true);
            else if (name === 'type' && 'type' in child) Reflect.set(child, 'type', val);
            else if (name === 'value' && 'value' in child) Reflect.set(child, 'value', val);
            else if (name === 'name' && 'name' in child) Reflect.set(child, 'name', val);
            else if (name === 'content' && 'content' in child) Reflect.set(child, 'content', val);
            child.setAttribute(name, val);
          }
          am = attrRegex.exec(rawAttrs);
        }
        parent.appendChild(child);
        if (!selfClosing) stack.push(child);
      } else if (match[5]) {
        parent.appendChild(new MockTextNode(match[5]));
      }
      match = tokenRegex.exec(value);
    }
  }

  getAttribute(name: string): string | null {
    if (name === 'hidden') return this.hidden ? '' : null;
    if (name === 'title') return this.title || this.attrs.get('title') || null;
    return this.attrs.get(name) ?? null;
  }

  setAttribute(name: string, value: string): void {
    if (name === 'id') {
      this.id = value;
      return;
    }
    if (name === 'class') {
      this.className = value;
      return;
    }
    if (name === 'hidden') {
      this.hidden = true;
    }
    if (name === 'title') {
      this.title = value;
    }
    this.attrs.set(name, value);
  }

  removeAttribute(name: string): void {
    if (name === 'hidden') this.hidden = false;
    if (name === 'class') this.className = '';
    this.attrs.delete(name);
  }

  hasAttribute(name: string): boolean {
    if (name === 'hidden') return this.hidden;
    return this.attrs.has(name);
  }

  toggleAttribute(name: string, force?: boolean): boolean {
    const has = this.hasAttribute(name);
    const next = force ?? !has;
    if (next) this.setAttribute(name, '');
    else this.removeAttribute(name);
    return next;
  }

  matches(selector: string): boolean {
    return matchesSelectorChain(this, selector);
  }

  closest(selector: string): MockElement | null {
    if (this.matches(selector)) return this;
    let cur: MockElement | null = this.parentElement;
    while (cur) {
      if (cur.matches(selector)) return cur;
      cur = cur.parentElement;
    }
    return null;
  }

  querySelectorAll(selector: string): MockElement[] {
    const results: MockElement[] = [];
    const walk = (node: MockNode): void => {
      for (const child of node.childNodes) {
        if (child instanceof MockElement) {
          if (child.matches(selector)) results.push(child);
          walk(child);
        }
      }
    };
    walk(this);
    return results;
  }

  querySelector(selector: string): MockElement | null {
    return this.querySelectorAll(selector)[0] ?? null;
  }

  getElementsByClassName(className: string): MockElement[] {
    return this.querySelectorAll(`.${className.trim().split(/\s+/).join('.')}`);
  }

  getElementsByTagName(tagName: string): MockElement[] {
    return this.querySelectorAll(tagName);
  }

  getBoundingClientRect(): DOMRect {
    const r = this.rect;
    return {
      x: r.x,
      y: r.y,
      left: r.left,
      top: r.top,
      right: r.right,
      bottom: r.bottom,
      width: r.width,
      height: r.height,
      toJSON() {
        return this;
      },
    };
  }

  click(): void {
    if (Reflect.get(this, 'disabled')) return;
    this.dispatchEvent(new Event('click', { bubbles: true, cancelable: true }));
  }

  focus(): void {
    if (this.ownerDoc) this.ownerDoc.activeElement = this;
  }

  blur(): void {
    if (this.ownerDoc?.activeElement === this) this.ownerDoc.activeElement = this.ownerDoc.body;
  }

  select(): void {
    // No-op in mock DOM.
  }

  scrollTo(options?: ScrollToOptions | number, y?: number): void {
    if (typeof options === 'number') {
      this.scrollLeft = options;
      if (typeof y === 'number') this.scrollTop = y;
    } else if (options) {
      if (typeof options.left === 'number') this.scrollLeft = options.left;
      if (typeof options.top === 'number') this.scrollTop = options.top;
    }
  }

  scrollBy(options?: ScrollToOptions | number, y?: number): void {
    if (typeof options === 'number') {
      this.scrollLeft += options;
      if (typeof y === 'number') this.scrollTop += y;
    } else if (options) {
      if (typeof options.left === 'number') this.scrollLeft += options.left;
      if (typeof options.top === 'number') this.scrollTop += options.top;
    }
  }

  scrollIntoView(): void {
    // No-op in mock DOM.
  }

  setPointerCapture(): void {
    // No-op in mock DOM.
  }

  releasePointerCapture(): void {
    // No-op in mock DOM.
  }

  override cloneNode(deep = false): MockElement {
    const clone = this.ownerDoc
      ? this.ownerDoc.createMockElement(this.tagName.toLowerCase())
      : new MockHTMLElement(this.tagName.toLowerCase(), null);
    for (const [k, v] of this.attrs) {
      if (k !== 'id') clone.setAttribute(k, v);
    }
    clone.style.cssText = this.style.cssText;
    clone.hidden = this.hidden;
    clone.title = this.title;
    if (deep) {
      for (const child of this.childNodes) {
        clone.appendChild(child.cloneNode(true));
      }
    }
    return clone;
  }
}

export class MockHTMLElement extends MockElement {}

export class MockHTMLButtonElement extends MockHTMLElement {
  public disabled = false;
  public value = '';

  get type(): string {
    return this.getAttribute('type') ?? 'submit';
  }

  set type(v: string) {
    this.setAttribute('type', v);
  }

  get name(): string {
    return this.getAttribute('name') ?? '';
  }

  set name(v: string) {
    this.setAttribute('name', v);
  }
}

export class MockHTMLInputElement extends MockHTMLElement {
  public value = '';
  public defaultValue = '';
  public checked = false;
  public defaultChecked = false;
  public indeterminate = false;
  public disabled = false;
  public readOnly = false;
  public placeholder = '';
  public min = '';
  public max = '';
  public step = '';
  public files: FileList | null = null;
  public selectionStart: number | null = 0;
  public selectionEnd: number | null = 0;

  get type(): string {
    return this.getAttribute('type') ?? 'text';
  }

  set type(v: string) {
    this.setAttribute('type', v);
  }

  get name(): string {
    return this.getAttribute('name') ?? '';
  }

  set name(v: string) {
    this.setAttribute('name', v);
  }

  setSelectionRange(start: number, end: number): void {
    this.selectionStart = start;
    this.selectionEnd = end;
  }

  setRangeText(
    replacement: string,
    start?: number,
    end?: number,
    selectionMode: SelectionMode = 'preserve',
  ): void {
    const s = start ?? this.selectionStart ?? 0;
    const e = end ?? this.selectionEnd ?? s;
    this.value = this.value.slice(0, s) + replacement + this.value.slice(e);
    if (selectionMode === 'end') {
      this.selectionStart = s + replacement.length;
      this.selectionEnd = s + replacement.length;
    } else if (selectionMode === 'select') {
      this.selectionStart = s;
      this.selectionEnd = s + replacement.length;
    } else if (selectionMode === 'start') {
      this.selectionStart = s;
      this.selectionEnd = s;
    }
  }

  checkValidity(): boolean {
    return true;
  }

  reportValidity(): boolean {
    return true;
  }
}

export class MockHTMLTextAreaElement extends MockHTMLElement {
  public value = '';
  public disabled = false;
  public readOnly = false;
  public placeholder = '';
  public selectionStart = 0;
  public selectionEnd = 0;
  public rows = 2;
  public cols = 20;

  get name(): string {
    return this.getAttribute('name') ?? '';
  }

  set name(v: string) {
    this.setAttribute('name', v);
  }

  setSelectionRange(start: number, end: number): void {
    this.selectionStart = start;
    this.selectionEnd = end;
  }

  setRangeText(
    replacement: string,
    start?: number,
    end?: number,
    selectionMode: SelectionMode = 'preserve',
  ): void {
    const s = start ?? this.selectionStart;
    const e = end ?? this.selectionEnd;
    this.value = this.value.slice(0, s) + replacement + this.value.slice(e);
    if (selectionMode === 'end') {
      this.selectionStart = s + replacement.length;
      this.selectionEnd = s + replacement.length;
    } else if (selectionMode === 'select') {
      this.selectionStart = s;
      this.selectionEnd = s + replacement.length;
    } else if (selectionMode === 'start') {
      this.selectionStart = s;
      this.selectionEnd = s;
    }
  }
}

export class MockHTMLOptionElement extends MockHTMLElement {
  private explicitValue: string | null = null;
  public selected = false;
  public disabled = false;

  get value(): string {
    return this.explicitValue ?? this.textContent;
  }

  set value(v: string) {
    this.explicitValue = v;
  }

  get text(): string {
    return this.textContent;
  }

  set text(v: string) {
    this.textContent = v;
  }
}

export class MockHTMLSelectElement extends MockHTMLElement {
  public disabled = false;
  public multiple = false;
  private explicitValue: string | null = null;

  get name(): string {
    return this.getAttribute('name') ?? '';
  }

  set name(v: string) {
    this.setAttribute('name', v);
  }

  get options(): MockHTMLOptionElement[] {
    return this.querySelectorAll('option').filter(
      (n): n is MockHTMLOptionElement => n instanceof MockHTMLOptionElement,
    );
  }

  get selectedOptions(): MockHTMLOptionElement[] {
    const opts = this.options;
    const explicit = opts.filter((o) => o.selected);
    if (explicit.length > 0) return explicit;
    if (this.explicitValue !== null) {
      const matched = opts.find((o) => o.value === this.explicitValue);
      if (matched) return [matched];
    }
    const first = opts[0];
    return first ? [first] : [];
  }

  get value(): string {
    if (this.explicitValue !== null) return this.explicitValue;
    const sel = this.selectedOptions[0];
    return sel ? sel.value : '';
  }

  set value(next: string) {
    this.explicitValue = next;
    for (const opt of this.options) {
      opt.selected = opt.value === next;
    }
  }

  get selectedIndex(): number {
    const opts = this.options;
    const cur = this.value;
    return opts.findIndex((o) => o.value === cur);
  }
}

export class MockHTMLDialogElement extends MockHTMLElement {
  public open = false;
  public returnValue = '';

  show(): void {
    this.open = true;
  }

  showModal(): void {
    this.open = true;
  }

  close(returnValue?: string): void {
    if (!this.open) return;
    if (returnValue !== undefined) this.returnValue = returnValue;
    this.open = false;
    queueMicrotask(() => {
      if (this.ownerDoc?.restored) return;
      this.dispatchEvent(new Event('close'));
    });
  }
}

export class MockHTMLAnchorElement extends MockHTMLElement {
  public href = '';
  public download = '';
  public target = '';
  public rel = '';
}

export interface MockCanvas2DContext {
  fillStyle: string;
  strokeStyle: string;
  font: string;
  lineWidth: number;
  textAlign: string;
  textBaseline: string;
  globalAlpha: number;
  readonly ops: string[];
  save(): void;
  restore(): void;
  scale(x: number, y: number): void;
  translate(x: number, y: number): void;
  rotate(angle: number): void;
  setTransform(...args: readonly number[]): void;
  clearRect(x: number, y: number, w: number, h: number): void;
  fillRect(x: number, y: number, w: number, h: number): void;
  strokeRect(x: number, y: number, w: number, h: number): void;
  beginPath(): void;
  closePath(): void;
  moveTo(x: number, y: number): void;
  lineTo(x: number, y: number): void;
  arc(x: number, y: number, r: number, start: number, end: number): void;
  rect(x: number, y: number, w: number, h: number): void;
  clip(): void;
  stroke(): void;
  fill(): void;
  setLineDash(segments: readonly number[]): void;
  fillText(text: string, x: number, y: number, maxWidth?: number): void;
  strokeText(text: string, x: number, y: number, maxWidth?: number): void;
  drawImage(...args: readonly unknown[]): void;
  measureText(text: string): { readonly width: number };
}

function createMock2DContext(): MockCanvas2DContext {
  const ops: string[] = [];
  return {
    fillStyle: '',
    strokeStyle: '',
    font: '13px sans-serif',
    lineWidth: 1,
    textAlign: 'left',
    textBaseline: 'alphabetic',
    globalAlpha: 1,
    ops,
    save: () => {
      ops.push('save');
    },
    restore: () => {
      ops.push('restore');
    },
    scale: () => {
      ops.push('scale');
    },
    translate: () => {
      ops.push('translate');
    },
    rotate: () => {
      ops.push('rotate');
    },
    setTransform: () => {
      ops.push('setTransform');
    },
    clearRect: () => {
      ops.push('clearRect');
    },
    fillRect: () => {
      ops.push('fillRect');
    },
    strokeRect: () => {
      ops.push('strokeRect');
    },
    beginPath: () => {
      ops.push('beginPath');
    },
    closePath: () => {
      ops.push('closePath');
    },
    moveTo: () => {
      ops.push('moveTo');
    },
    lineTo: () => {
      ops.push('lineTo');
    },
    arc: () => {
      ops.push('arc');
    },
    rect: () => {
      ops.push('rect');
    },
    clip: () => {
      ops.push('clip');
    },
    stroke: () => {
      ops.push('stroke');
    },
    fill: () => {
      ops.push('fill');
    },
    setLineDash: () => {
      ops.push('setLineDash');
    },
    fillText: (text: string) => {
      ops.push(`fillText:${text}`);
    },
    strokeText: (text: string) => {
      ops.push(`strokeText:${text}`);
    },
    drawImage: () => {
      ops.push('drawImage');
    },
    measureText: (text: string) => ({ width: text.length * 7.5 }),
  };
}

export class MockHTMLCanvasElement extends MockHTMLElement {
  public width = 300;
  public height = 150;
  private readonly ctx2d = createMock2DContext();

  getContext(kind: string): MockCanvas2DContext | null {
    return kind === '2d' ? this.ctx2d : null;
  }

  toBlob(callback: (blob: Blob | null) => void, type = 'image/png'): void {
    callback(new Blob([new Uint8Array([137, 80, 78, 71])], { type }));
  }

  toDataURL(type = 'image/png'): string {
    return `data:${type};base64,iVBORw0KGgo=`;
  }
}

export class MockHTMLFormElement extends MockHTMLElement {
  readonly elements = {
    namedItem: (name: string): MockElement | null =>
      this.querySelector(`[name="${name}"]`),
  };

  reset(): void {
    for (const input of this.querySelectorAll('input')) {
      if (input instanceof MockHTMLInputElement) {
        input.value = input.defaultValue;
        input.checked = input.defaultChecked;
      }
    }
    for (const select of this.querySelectorAll('select')) {
      if (select instanceof MockHTMLSelectElement) {
        select.value = select.options[0]?.value ?? '';
      }
    }
  }

  submit(): void {
    this.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
  }

  requestSubmit(): void {
    this.submit();
  }
}

export class MockHTMLDetailsElement extends MockHTMLElement {
  public open = false;
}

export class MockHTMLFieldSetElement extends MockHTMLElement {
  public disabled = false;
  public name = '';
}

export class MockHTMLImageElement extends MockHTMLElement {
  public src = '';
  public alt = '';
  public naturalWidth = 100;
  public naturalHeight = 100;
  public complete = true;

  decode(): Promise<void> {
    return Promise.resolve();
  }
}

export class MockHTMLProgressElement extends MockHTMLElement {
  public value = 0;
  public max = 1;
}

export class MockHTMLMetaElement extends MockHTMLElement {
  public name = '';
  public content = '';
}

export class MockStorage {
  private readonly map = new Map<string, string>();

  get length(): number {
    return this.map.size;
  }

  clear(): void {
    this.map.clear();
  }

  getItem(key: string): string | null {
    return this.map.get(key) ?? null;
  }

  key(index: number): string | null {
    return [...this.map.keys()][index] ?? null;
  }

  removeItem(key: string): void {
    this.map.delete(key);
  }

  setItem(key: string, value: string): void {
    this.map.set(key, value);
  }
}

export class MockSVGElement extends MockElement {}

export class MockSVGSVGElement extends MockSVGElement {}

export class MockSVGRectElement extends MockSVGElement {}

export class MockDocument extends MockNode {
  public restored = false;
  public readonly documentElement: MockHTMLElement;
  public readonly head: MockHTMLElement;
  public readonly body: MockHTMLElement;
  public activeElement: MockElement | null;
  public hidden = false;
  public fullscreenElement: MockElement | null = null;
  private readonly byIdMap = new Map<string, MockElement>();

  constructor() {
    super(9);
    this.documentElement = new MockHTMLElement('html', this);
    this.head = new MockHTMLElement('head', this);
    this.body = new MockHTMLElement('body', this);
    this.appendChild(this.documentElement);
    this.documentElement.append(this.head, this.body);
    this.activeElement = this.body;
  }

  registerId(id: string, node: MockElement): void {
    this.byIdMap.set(id, node);
  }

  unregisterId(id: string, node: MockElement): void {
    if (this.byIdMap.get(id) === node) this.byIdMap.delete(id);
  }

  createMockElement(tagName: string): MockElement {
    const lower = tagName.toLowerCase();
    switch (lower) {
      case 'button':
        return new MockHTMLButtonElement(lower, this);
      case 'input':
        return new MockHTMLInputElement(lower, this);
      case 'textarea':
        return new MockHTMLTextAreaElement(lower, this);
      case 'select':
        return new MockHTMLSelectElement(lower, this);
      case 'option':
        return new MockHTMLOptionElement(lower, this);
      case 'dialog':
        return new MockHTMLDialogElement(lower, this);
      case 'a':
        return new MockHTMLAnchorElement(lower, this);
      case 'canvas':
        return new MockHTMLCanvasElement(lower, this);
      case 'form':
        return new MockHTMLFormElement(lower, this);
      case 'details':
        return new MockHTMLDetailsElement(lower, this);
      case 'fieldset':
        return new MockHTMLFieldSetElement(lower, this);
      case 'img':
        return new MockHTMLImageElement(lower, this);
      case 'progress':
        return new MockHTMLProgressElement(lower, this);
      case 'meta':
        return new MockHTMLMetaElement(lower, this);
      default:
        return new MockHTMLElement(lower, this);
    }
  }

  createElement(tagName: string): MockElement {
    return this.createMockElement(tagName);
  }

  createElementNS(ns: string, tagName: string): MockElement {
    const lower = tagName.toLowerCase();
    if (ns === 'http://www.w3.org/2000/svg') {
      if (lower === 'svg') return new MockSVGSVGElement(lower, this);
      if (lower === 'rect') return new MockSVGRectElement(lower, this);
      return new MockSVGElement(lower, this);
    }
    return this.createMockElement(tagName);
  }

  createTextNode(text: string): MockTextNode {
    return new MockTextNode(text);
  }

  createComment(text: string): MockCommentNode {
    return new MockCommentNode(text);
  }

  createDocumentFragment(): MockDocumentFragment {
    return new MockDocumentFragment();
  }

  getElementById(id: string): MockElement | null {
    return this.byIdMap.get(id) ?? null;
  }

  querySelector(selector: string): MockElement | null {
    return this.documentElement.querySelector(selector);
  }

  querySelectorAll(selector: string): MockElement[] {
    return this.documentElement.querySelectorAll(selector);
  }

  getElementsByClassName(className: string): MockElement[] {
    return this.documentElement.getElementsByClassName(className);
  }

  elementFromPoint(_x: number, _y: number): MockElement | null {
    return null;
  }

  exitFullscreen(): Promise<void> {
    this.fullscreenElement = null;
    return Promise.resolve();
  }
}

export interface InstallDomOptions {
  readonly fromIndexHtml?: boolean;
  readonly url?: string;
  readonly token?: string;
  readonly version?: string;
  readonly cacheEpoch?: string;
}

export interface TestDomHandle {
  readonly document: MockDocument;
  readonly localStorage: MockStorage;
  readonly sessionStorage: MockStorage;
  register<K extends keyof HTMLElementTagNameMap>(
    id: string,
    tag: K,
    parent?: MockNode,
  ): HTMLElementTagNameMap[K];
  register(id: string, tag?: string, parent?: MockNode): HTMLElement;
  restore(): void;
}

export function installDom(options: InstallDomOptions = {}): TestDomHandle {
  const doc = new MockDocument();
  const local = new MockStorage();
  const session = new MockStorage();
  const url = options.url ?? 'http://127.0.0.1/';

  doc.documentElement.dataset.cacheEpoch = options.cacheEpoch ?? '0';
  const tokenMeta = doc.createMockElement('meta');
  tokenMeta.setAttribute('name', 'app-token');
  tokenMeta.setAttribute('content', options.token ?? 'test-token');
  if (tokenMeta instanceof MockHTMLMetaElement) {
    tokenMeta.name = 'app-token';
    tokenMeta.content = options.token ?? 'test-token';
  }
  const versionMeta = doc.createMockElement('meta');
  versionMeta.setAttribute('name', 'app-version');
  versionMeta.setAttribute('content', options.version ?? '1.4.4');
  if (versionMeta instanceof MockHTMLMetaElement) {
    versionMeta.name = 'app-version';
    versionMeta.content = options.version ?? '1.4.4';
  }
  doc.head.append(tokenMeta, versionMeta);

  if (options.fromIndexHtml) {
    const gridRegion = doc.createMockElement('main');
    gridRegion.className = 'grid-region';
    doc.body.appendChild(gridRegion);
    for (const spec of parseIndexHtmlSpecs()) {
      const node = doc.createMockElement(spec.tag);
      node.id = spec.id;
      if (node instanceof MockHTMLInputElement) {
        if (spec.typeAttr) node.type = spec.typeAttr;
        if ('checked' in spec.attrs) {
          node.checked = true;
          node.defaultChecked = true;
        }
        if (spec.attrs.value !== undefined) {
          node.value = spec.attrs.value;
          node.defaultValue = spec.attrs.value;
        }
        if (spec.attrs.min !== undefined) node.min = spec.attrs.min;
        if (spec.attrs.max !== undefined) node.max = spec.attrs.max;
        if (spec.attrs.step !== undefined) node.step = spec.attrs.step;
      }
      if (node instanceof MockHTMLButtonElement && spec.typeAttr) {
        node.type = spec.typeAttr;
      }
      if (node instanceof MockHTMLTextAreaElement && spec.attrs.value !== undefined) {
        node.value = spec.attrs.value;
      }
      if (node instanceof MockHTMLSelectElement && spec.options) {
        for (const optSpec of spec.options) {
          const opt = new MockHTMLOptionElement('option', doc);
          opt.value = optSpec.value;
          opt.textContent = optSpec.text;
          opt.selected = optSpec.selected;
          opt.disabled = optSpec.disabled;
          node.appendChild(opt);
        }
      }
      if ('hidden' in spec.attrs) node.hidden = true;
      if ('disabled' in spec.attrs && 'disabled' in node) {
        Reflect.set(node, 'disabled', true);
      }
      for (const [k, v] of Object.entries(spec.attrs)) {
        if (k !== 'id' && k !== 'hidden' && k !== 'disabled') {
          node.setAttribute(k, v);
        }
      }
      if (spec.id === 'grid' || spec.id === 'traceArrival') {
        gridRegion.appendChild(node);
      } else {
        doc.body.appendChild(node);
      }
    }
  }

  const windowListeners = new Map<string, ((event: Event) => void)[]>();
  const rafTimers = new Set<ReturnType<typeof setTimeout>>();
  const mockWindow: Record<string, unknown> = {
    document: doc,
    localStorage: local,
    sessionStorage: session,
    devicePixelRatio: 1,
    innerWidth: 1280,
    innerHeight: 800,
    location: {
      href: url,
      pathname: new URL(url).pathname,
      search: new URL(url).search,
      replace: () => undefined,
      assign: () => undefined,
      reload: () => undefined,
    },
    history: {
      state: null,
      replaceState: () => undefined,
      pushState: () => undefined,
    },
    matchMedia: () => ({
      matches: false,
      addEventListener: () => undefined,
      removeEventListener: () => undefined,
    }),
    getComputedStyle: () => ({
      transform: 'none',
      getPropertyValue: () => '',
    }),
    requestAnimationFrame: (cb: FrameRequestCallback): number => {
      const handle = setTimeout(() => {
        rafTimers.delete(handle);
        if (!doc.restored) cb(performance.now());
      }, 0);
      rafTimers.add(handle);
      return Number(handle);
    },
    cancelAnimationFrame: (id: number): void => {
      clearTimeout(id);
    },
    setTimeout,
    clearTimeout,
    setInterval,
    clearInterval,
    open: () => null,
    addEventListener: (type: string, fn: (event: Event) => void) => {
      const list = windowListeners.get(type) ?? [];
      list.push(fn);
      windowListeners.set(type, list);
    },
    removeEventListener: (type: string, fn: (event: Event) => void) => {
      const list = windowListeners.get(type);
      if (list) {
        const idx = list.indexOf(fn);
        if (idx >= 0) list.splice(idx, 1);
      }
    },
    dispatchEvent: (event: Event): boolean => {
      for (const fn of [...(windowListeners.get(event.type) ?? [])]) {
        fn(event);
      }
      return !event.defaultPrevented;
    },
  };
  mockWindow.window = mockWindow;

  class MockOptionConstructor extends MockHTMLOptionElement {
    constructor(text = '', value?: string, defaultSelected = false, selected = false) {
      super('option', doc);
      this.textContent = text;
      this.value = value ?? text;
      this.selected = selected || defaultSelected;
    }
  }

  const globalsToInstall: readonly (readonly [string, unknown])[] = [
    ['window', mockWindow],
    ['document', doc],
    ['location', mockWindow.location],
    ['history', mockWindow.history],
    ['localStorage', local],
    ['sessionStorage', session],
    ['Node', MockNode],
    ['Element', MockElement],
    ['HTMLElement', MockHTMLElement],
    ['HTMLButtonElement', MockHTMLButtonElement],
    ['HTMLInputElement', MockHTMLInputElement],
    ['HTMLTextAreaElement', MockHTMLTextAreaElement],
    ['HTMLSelectElement', MockHTMLSelectElement],
    ['HTMLOptionElement', MockHTMLOptionElement],
    ['Option', MockOptionConstructor],
    ['HTMLDialogElement', MockHTMLDialogElement],
    ['HTMLAnchorElement', MockHTMLAnchorElement],
    ['HTMLCanvasElement', MockHTMLCanvasElement],
    ['HTMLFormElement', MockHTMLFormElement],
    ['HTMLDetailsElement', MockHTMLDetailsElement],
    ['HTMLFieldSetElement', MockHTMLFieldSetElement],
    ['HTMLImageElement', MockHTMLImageElement],
    ['HTMLProgressElement', MockHTMLProgressElement],
    ['HTMLMetaElement', MockHTMLMetaElement],
    ['SVGElement', MockSVGElement],
    ['SVGSVGElement', MockSVGSVGElement],
    ['SVGRectElement', MockSVGRectElement],
    ['DocumentFragment', MockDocumentFragment],
    ['getComputedStyle', mockWindow.getComputedStyle],
    ['requestAnimationFrame', mockWindow.requestAnimationFrame],
    ['cancelAnimationFrame', mockWindow.cancelAnimationFrame],
    ['ResizeObserver', class { observe(): void {} unobserve(): void {} disconnect(): void {} }],
    ['MutationObserver', class { observe(): void {} disconnect(): void {} }],
    ['IntersectionObserver', class { observe(): void {} unobserve(): void {} disconnect(): void {} }],
  ];

  const saved = new Map<string, PropertyDescriptor | undefined>();
  for (const [key, value] of globalsToInstall) {
    saved.set(key, Object.getOwnPropertyDescriptor(globalThis, key));
    Object.defineProperty(globalThis, key, {
      value,
      configurable: true,
      writable: true,
    });
  }

  function register<K extends keyof HTMLElementTagNameMap>(
    id: string,
    tag: K,
    parent?: MockNode,
  ): HTMLElementTagNameMap[K];
  function register(id: string, tag?: string, parent?: MockNode): HTMLElement;
  function register(id: string, tag = 'div', parent: MockNode = doc.body): HTMLElement {
    const existing = doc.getElementById(id);
    if (existing?.tagName === tag.toUpperCase()) {
      const found = document.getElementById(id);
      if (found) return found;
    }
    const created = document.createElement(tag);
    created.id = id;
    const raw = doc.getElementById(id);
    if (raw) parent.appendChild(raw);
    return created;
  }

  return {
    document: doc,
    localStorage: local,
    sessionStorage: session,
    register,
    restore(): void {
      doc.restored = true;
      for (const h of rafTimers) clearTimeout(h);
      rafTimers.clear();
      for (const [key, desc] of saved) {
        if (desc === undefined) {
          Reflect.deleteProperty(globalThis, key);
        } else {
          Object.defineProperty(globalThis, key, desc);
        }
      }
    },
  };
}
