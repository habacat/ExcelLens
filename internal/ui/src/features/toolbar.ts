/**
 * Adaptive ribbon & classic toolbar layout controller.
 * Both layouts share the same live DOM controls, event handlers, and state.
 */
import { byId } from '../core/dom.ts';
import { getOverlayMenus, type OverlayMenus } from '../core/overlays.ts';

const TAB_KEY = 'ExcelLens.ribbonTab';
const COLLAPSE_KEY = 'ExcelLens.ribbonCollapsed';

const CLASSIC_TOP: readonly (readonly [string, number])[] = [
  ['archiveBtn', 2],
  ['openBtn', 0],
  ['historyBackBtn', 4],
  ['historyForwardBtn', 4],
  ['searchOpenBtn', 0],
  ['filterOpenBtn', 0],
  ['textHeaderBtn', 1],
  ['multiSortBtn', 0],
  ['freezeBtn', 2],
  ['exportOpenBtn', 0],
  ['editCopyOpen', 1],
  ['copyMenuBtn', 2],
  ['analysisOpenBtn', 0],
  ['profileOpenBtn', 3],
  ['calculatorOpenBtn', 3],
  ['clearFilterBtn', 1],
];

const CLASSIC_BEFORE_IDS: readonly string[] = [
  'catalogueOpenBtn',
  'dataRulesOpenBtn',
  'recordCardOpenBtn',
];

const CLASSIC_AFTER_IDS: readonly string[] = [
  'copyFormulaBtn',
  'copyValueBtn',
  'tracePrecedentsBtn',
  'traceDependentsBtn',
];

export interface ToolbarMeasureItem {
  readonly node: HTMLElement;
  readonly width: number;
  readonly priority: number;
}

interface ControlPosition {
  readonly node: HTMLElement;
  readonly marker: Comment;
  readonly group: Element;
  readonly menuItem: boolean;
}

function readPref(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

function writePref(key: string, value: string): void {
  try {
    localStorage.setItem(key, value);
  } catch {
    // Ignore storage failures in restricted contexts.
  }
}

function isDisabledControl(node: Element): boolean {
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

export class AdaptiveToolbar {
  public readonly root: HTMLElement;
  public readonly actions: HTMLElement;
  public readonly classic: HTMLElement;
  public readonly tabs: HTMLElement[];
  public readonly panels: HTMLElement[];
  public readonly more: HTMLElement | null;
  public readonly moreMenu: HTMLElement | null;
  public readonly collapse: HTMLElement | null;
  public current = '';

  private readonly positions: ControlPosition[] = [];
  private readonly resize: ResizeObserver;
  private readonly changes: MutationObserver;
  private readonly visibility: MutationObserver;
  private readonly menusOverride: OverlayMenus | null;
  private readonly onWindowResize: () => void;
  private readonly onDocumentPointerDown: (event: PointerEvent) => void;
  private readonly onDocumentKeyDown: (event: KeyboardEvent) => void;
  private frame = 0;
  private height = 0;
  private layoutMode: boolean | undefined = undefined;

  static choose(
    items: readonly ToolbarMeasureItem[],
    width: number,
    gap: number,
  ): Set<HTMLElement> {
    const shown = new Set<HTMLElement>();
    const ordered = items
      .map((item, index) => ({ ...item, index }))
      .sort((a, b) => a.priority - b.priority || a.index - b.index);
    let used = 0;
    for (const item of ordered) {
      const next = used + (shown.size ? gap : 0) + item.width;
      if (item.priority === 0 || next <= width) {
        shown.add(item.node);
        used = next;
      }
    }
    return shown;
  }

  constructor(root: HTMLElement, menus: OverlayMenus | null = null) {
    this.root = root;
    this.menusOverride = menus;
    const actions = root.querySelector<HTMLElement>('.app-actions');
    const classic = root.querySelector<HTMLElement>('.classic-actions');
    if (!actions || !classic) {
      throw new Error('Missing toolbar action containers (.app-actions / .classic-actions)');
    }
    this.actions = actions;
    this.classic = classic;

    const remember = (node: HTMLElement | null, group: Element | null): void => {
      if (!node || !group) return;
      const marker = document.createComment(node.id || 'font');
      node.before(marker);
      this.positions.push({
        node,
        marker,
        group,
        menuItem: node.getAttribute('role') === 'menuitem',
      });
    };

    for (const [id, priority] of CLASSIC_TOP) {
      const node = byId(id);
      if (!node) continue;
      node.dataset.classicPriority = String(priority);
      remember(node, this.classic);
    }
    const before = document.querySelector('.classic-before');
    const after = document.querySelector('.classic-after');
    for (const id of CLASSIC_BEFORE_IDS) {
      remember(byId(id), before);
    }
    const fontLabel = byId('fontSelect')?.closest('label') ?? null;
    remember(fontLabel, after);
    for (const id of CLASSIC_AFTER_IDS) {
      remember(byId(id), after);
    }

    this.tabs = [...root.querySelectorAll<HTMLElement>('.ribbon-tabs [data-ribbon-tab]')];
    this.panels = [...root.querySelectorAll<HTMLElement>('[data-ribbon-panel]')];
    this.more = root.querySelector<HTMLElement>('#ribbonMoreBtn');
    this.moreMenu = byId('ribbonMoreMenu');
    this.collapse = root.querySelector<HTMLElement>('#ribbonCollapseBtn');

    this.onWindowResize = (): void => {
      this.schedule();
    };

    this.resize = new ResizeObserver(() => {
      const height = root.getBoundingClientRect().height;
      if (height !== this.height) {
        this.height = height;
        document.documentElement.style.setProperty('--appbar-height', `${height}px`);
      }
      this.schedule();
    });
    this.resize.observe(root);
    this.resize.observe(this.classic);

    // The overflow button and separators are layout outputs. Observing their
    // own visibility changes queues another layout on every animation frame.
    this.changes = new MutationObserver((records) => {
      if (
        records.some(
          (record) =>
            record.attributeName !== 'hidden' ||
            (record.target !== this.more &&
              (!(record.target instanceof Element) ||
                !record.target.classList.contains('ribbon-sep'))),
        )
      ) {
        this.schedule();
      }
    });
    this.changes.observe(this.actions, {
      subtree: true,
      childList: true,
      characterData: true,
      attributes: true,
      attributeFilter: ['hidden', 'disabled'],
    });
    this.changes.observe(this.classic, {
      subtree: true,
      childList: true,
      characterData: true,
      attributes: true,
      attributeFilter: ['hidden', 'disabled'],
    });
    this.visibility = new MutationObserver(() => this.schedule());
    this.visibility.observe(document.body, {
      attributes: true,
      attributeFilter: ['class'],
    });
    window.addEventListener('resize', this.onWindowResize);
    void document.fonts.ready.then(() => this.schedule());

    for (const tab of this.tabs) {
      tab.addEventListener('click', () => this.activate(tab.dataset.ribbonTab ?? ''));
      tab.addEventListener('dblclick', () => this.setCollapsed(!this.collapsed));
      tab.addEventListener('keydown', (event: KeyboardEvent) => this.tabKey(event, tab));
    }
    this.collapse?.addEventListener('click', () => this.setCollapsed(!this.collapsed));
    this.more?.setAttribute('data-menu', 'ribbonMoreMenu');
    this.moreMenu?.addEventListener('menuopen', () => this.fillMore());

    // A folded bar opens over the sheet for one command, then steps aside.
    this.actions.addEventListener('click', (event: MouseEvent) => {
      const target = event.target instanceof Element ? event.target : null;
      if (
        this.peeking &&
        target?.closest('button,a') &&
        !target.closest('[data-menu],#ribbonCollapseBtn,[aria-haspopup="menu"]')
      ) {
        setTimeout(() => this.peek(false), 0);
      }
    });

    this.onDocumentPointerDown = (event: PointerEvent): void => {
      const target = event.target;
      if (
        this.peeking &&
        target instanceof Node &&
        !root.contains(target) &&
        !(target instanceof Element && target.closest('.menu,.columns-menu'))
      ) {
        this.peek(false);
      }
    };
    document.addEventListener('pointerdown', this.onDocumentPointerDown, true);

    this.onDocumentKeyDown = (event: KeyboardEvent): void => {
      const firstTab = this.tabs[0];
      if (
        event.key === 'F1' &&
        event.ctrlKey &&
        !event.altKey &&
        !event.shiftKey &&
        firstTab &&
        firstTab.getClientRects().length > 0 &&
        !document.querySelector('dialog:modal')
      ) {
        event.preventDefault();
        this.setCollapsed(!this.collapsed);
      } else if (event.key === 'Escape' && this.peeking) {
        this.peek(false);
      }
    };
    document.addEventListener('keydown', this.onDocumentKeyDown);

    this.setCollapsed(readPref(COLLAPSE_KEY) === '1', false);
    const savedTab = readPref(TAB_KEY);
    const initialTab =
      savedTab && this.tabs.some((tab) => tab.dataset.ribbonTab === savedTab)
        ? savedTab
        : (this.tabs[0]?.dataset.ribbonTab ?? '');
    this.select(initialTab, false);
    this.setLayout(document.documentElement.dataset.ribbonLayout === 'true');
  }

  get ribbon(): boolean {
    return Boolean(this.layoutMode);
  }

  get collapsed(): boolean {
    return this.root.hasAttribute('data-ribbon-collapsed');
  }

  get peeking(): boolean {
    return this.root.hasAttribute('data-ribbon-peek');
  }

  private schedule(): void {
    if (!this.frame) {
      this.frame = requestAnimationFrame(() => {
        this.frame = 0;
        this.fit();
      });
    }
  }

  private menus(): OverlayMenus | null {
    return this.menusOverride ?? getOverlayMenus();
  }

  setLayout(ribbon: boolean): void {
    if (this.layoutMode === ribbon) return;
    this.menus()?.close(false);
    this.layoutMode = ribbon;
    document.documentElement.dataset.ribbonLayout = String(ribbon);
    const focused = document.activeElement;
    for (const { node, marker, group, menuItem } of this.positions) {
      node.removeAttribute('data-toolbar-overflow');
      if (menuItem) {
        node.classList.toggle('icon-button', !ribbon);
        if (ribbon) node.setAttribute('role', 'menuitem');
        else node.removeAttribute('role');
      }
      if (ribbon) marker.after(node);
      else group.append(node);
    }
    this.peek(false);
    this.fit();
    if (focused instanceof HTMLElement && focused !== document.body && focused.getClientRects().length) {
      focused.focus({ preventScroll: true });
    }
  }

  activate(name: string): void {
    if (this.collapsed) {
      const same = this.current === name && this.peeking;
      this.select(name);
      this.peek(!same);
    } else {
      this.select(name);
    }
  }

  select(name: string, persist = true): void {
    if (!name) return;
    this.current = name;
    for (const tab of this.tabs) {
      const on = tab.dataset.ribbonTab === name;
      tab.setAttribute('aria-selected', String(on));
      tab.tabIndex = on ? 0 : -1;
    }
    for (const panel of this.panels) {
      panel.hidden = panel.dataset.ribbonPanel !== name;
    }
    if (persist) writePref(TAB_KEY, name);
    this.schedule();
  }

  peek(value: boolean): void {
    this.root.toggleAttribute('data-ribbon-peek', value && this.collapsed);
    this.schedule();
  }

  setCollapsed(value: boolean, persist = true): void {
    this.root.toggleAttribute('data-ribbon-collapsed', value);
    this.root.removeAttribute('data-ribbon-peek');
    if (this.collapse) {
      const label = value ? '展开功能区' : '折叠功能区';
      this.collapse.setAttribute('aria-pressed', String(value));
      this.collapse.setAttribute('aria-label', label);
      this.collapse.title = `${label}（Ctrl+F1）`;
    }
    if (persist) writePref(COLLAPSE_KEY, value ? '1' : '0');
    this.schedule();
  }

  private tabKey(event: KeyboardEvent, tab: HTMLElement): void {
    const at = this.tabs.indexOf(tab);
    const nextMap: Readonly<Record<string, number>> = {
      ArrowRight: at + 1,
      ArrowLeft: at - 1,
      Home: 0,
      End: this.tabs.length - 1,
    };
    const next = nextMap[event.key];
    if (next === undefined || !this.tabs.length) return;
    event.preventDefault();
    const target = this.tabs[(next + this.tabs.length) % this.tabs.length];
    if (!target) return;
    this.activate(target.dataset.ribbonTab ?? '');
    target.focus();
  }

  // Show the tab that owns a control before a command acts on it, so menus
  // anchor to a visible button and the user learns where the command lives.
  reveal(id: string): HTMLElement | null {
    const node = byId(id);
    const panel = node?.closest<HTMLElement>('[data-ribbon-panel]') ?? null;
    if (!this.ribbon) return node;
    const panelName = panel?.dataset.ribbonPanel ?? '';
    if (panel && panelName && panelName !== this.current) {
      this.select(panelName);
    }
    if (panel && node) {
      if (this.collapsed && node.matches('[aria-haspopup="menu"]')) this.peek(true);
      this.fit();
    }
    return node;
  }

  fit(): void {
    if (!this.ribbon) {
      if (!this.classic.clientWidth) return;
      const children = [...this.classic.children].filter(
        (child): child is HTMLElement => child instanceof HTMLElement,
      );
      // Clear overflow attributes in one write pass before measuring widths so
      // the browser performs a single layout instead of one reflow per button.
      for (const node of children) node.removeAttribute('data-toolbar-overflow');
      const containerWidth = this.classic.getBoundingClientRect().width;
      const gap = parseFloat(getComputedStyle(this.classic).columnGap) || 0;
      const items: ToolbarMeasureItem[] = [];
      for (const node of children) {
        const width = node.getBoundingClientRect().width;
        if (width) {
          items.push({ node, width, priority: Number(node.dataset.classicPriority) });
        }
      }
      const shown = AdaptiveToolbar.choose(items, containerWidth, gap);
      const focused = document.activeElement;
      for (const { node } of items) {
        if (shown.has(node)) continue;
        node.setAttribute('data-toolbar-overflow', '');
        if (node === focused) {
          byId('commandOpenBtn')?.focus({ preventScroll: true });
        }
      }
      return;
    }

    const actions = this.actions;
    const panel = this.panels.find((node) => !node.hidden);
    if (!panel || !actions.clientWidth) return;
    const setHidden = (node: HTMLElement | null, value: boolean): void => {
      if (node && node.hidden !== value) node.hidden = value;
    };
    const nodes = [...panel.querySelectorAll<HTMLElement>(':scope > [data-toolbar-priority]')];
    const separators = [...panel.querySelectorAll<HTMLElement>(':scope > .ribbon-sep')];
    for (const node of nodes) node.removeAttribute('data-toolbar-overflow');
    for (const node of separators) setHidden(node, false);

    const items: ToolbarMeasureItem[] = nodes
      .filter(
        (node) =>
          !node.hidden &&
          (!node.hasAttribute('data-toolbar-when-enabled') || !isDisabledControl(node)),
      )
      .map((node) => ({
        node,
        width: node.getBoundingClientRect().width,
        priority: Number(node.dataset.toolbarPriority) || 0,
      }))
      .filter((item) => item.width > 0);

    const style = getComputedStyle(actions);
    const gap = parseFloat(getComputedStyle(panel).columnGap) || 0;
    const space =
      actions.clientWidth -
      (parseFloat(style.paddingLeft) || 0) -
      (parseFloat(style.paddingRight) || 0) -
      (this.collapse?.getBoundingClientRect().width ?? 0) -
      gap;

    if (panel.scrollWidth > space + 0.5 && this.more) {
      setHidden(this.more, false);
      const fixed = separators.reduce((sum, node) => {
        const box = getComputedStyle(node);
        return (
          sum +
          node.getBoundingClientRect().width +
          (parseFloat(box.marginLeft) || 0) +
          (parseFloat(box.marginRight) || 0) +
          gap
        );
      }, 0);
      const shown = AdaptiveToolbar.choose(
        items,
        Math.max(0, space - this.more.getBoundingClientRect().width - gap - fixed) - 1,
        gap,
      );
      const focused = document.activeElement;
      for (const item of items) {
        if (!shown.has(item.node)) item.node.setAttribute('data-toolbar-overflow', '');
      }
      if (items.some((item) => item.node.contains(focused) && !shown.has(item.node))) {
        this.more.focus({ preventScroll: true });
      }
      if (!items.some((item) => !shown.has(item.node))) setHidden(this.more, true);
    } else {
      setHidden(this.more, true);
    }

    // A separator needs a visible command on both sides.
    let pending: HTMLElement | null = null;
    let seen = false;
    for (const child of panel.children) {
      if (!(child instanceof HTMLElement)) continue;
      if (child.classList.contains('ribbon-sep')) {
        if (!seen || pending) setHidden(child, true);
        else pending = child;
      } else if (child.getClientRects().length) {
        seen = true;
        pending = null;
      }
    }
    if (pending) setHidden(pending, true);
  }

  fillMore(): void {
    if (!this.moreMenu) return;
    const panel = this.panels.find((node) => !node.hidden);
    const fragment = document.createDocumentFragment();
    const overflowNodes = panel
      ? panel.querySelectorAll<HTMLElement>(':scope > [data-toolbar-overflow]')
      : [];
    for (const node of overflowNodes) {
      const select = node.querySelector('select');
      if (select) {
        // Keep one source of options and use its normal change handler.
        const label = document.createElement('label');
        label.className = 'menu-field';
        const cloned = select.cloneNode(true);
        if (cloned instanceof HTMLSelectElement) {
          cloned.removeAttribute('id');
          cloned.value = select.value;
          cloned.disabled = select.disabled;
          cloned.addEventListener('change', () => {
            select.value = cloned.value;
            select.dispatchEvent(new Event('change', { bubbles: true }));
            this.menus()?.close();
            if (this.peeking) this.peek(false);
          });
          label.append(select.getAttribute('aria-label') ?? '', cloned);
          fragment.append(label);
        }
        continue;
      }
      const item = document.createElement('button');
      const iconEl = node.querySelector('.icon:not(.caret)');
      item.type = 'button';
      item.setAttribute('role', 'menuitem');
      item.disabled = isDisabledControl(node);
      if (iconEl) item.append(iconEl.cloneNode(true));
      item.append(node.textContent.trim());
      item.addEventListener('click', () => node.click());
      fragment.append(item);
    }
    this.moreMenu.replaceChildren(fragment);
  }

  dispose(): void {
    cancelAnimationFrame(this.frame);
    this.resize.disconnect();
    this.changes.disconnect();
    this.visibility.disconnect();
    window.removeEventListener('resize', this.onWindowResize);
    document.removeEventListener('pointerdown', this.onDocumentPointerDown, true);
    document.removeEventListener('keydown', this.onDocumentKeyDown);
  }
}

export type ToolbarRibbon = AdaptiveToolbar;

export function initToolbarRibbon(
  root: HTMLElement | null = document.querySelector<HTMLElement>('.appbar'),
  menus: OverlayMenus | null = null,
): AdaptiveToolbar | null {
  return root ? new AdaptiveToolbar(root, menus) : null;
}
