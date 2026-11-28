/**
 * Shared dropdown menu positioning, dialog light-dismiss, and in-app ask/confirm
 * modal dialogs (replacing native browser prompt/confirm).
 */
import { t as localize } from './i18n.ts';
import type { ToastTone } from '../contracts/common.ts';
import { isComposing } from './display-format.ts';
import { requireEl } from './dom.ts';
import { enterMotion } from './motion.ts';

const VALID_TOAST_TONES: ReadonlySet<string> = new Set(['info', 'success', 'warning', 'error']);

export interface ToastTimers {
  setTimeout(fn: () => void, ms: number): ReturnType<typeof setTimeout> | number;
  clearTimeout(id: ReturnType<typeof setTimeout> | number): void;
}

export interface ToastController {
  show(message: string, tone?: string): void;
  dispose(): void;
}

export function createToastController(
  elementId: string,
  options: {
    readonly durationMs?: number;
    readonly animate?: boolean;
    readonly timers?: ToastTimers;
  } = {},
): ToastController {
  const durationMs = options.durationMs ?? 5200;
  const animate = options.animate ?? false;
  const timers: ToastTimers = options.timers ?? {
    setTimeout: (fn, ms) => setTimeout(fn, ms),
    clearTimeout: (id) => clearTimeout(id),
  };
  let timer: ReturnType<typeof setTimeout> | number | undefined;
  return {
    show(message: string, tone = 'info'): void {
      const node = requireEl(elementId);
      const resolvedTone: ToastTone = VALID_TOAST_TONES.has(tone) ? (tone as ToastTone) : 'info';
      node.dataset.tone = resolvedTone;
      node.setAttribute('role', resolvedTone === 'error' ? 'alert' : 'status');
      node.textContent = message;
      node.hidden = false;
      if (animate) enterMotion(node, { duration: 140 });
      if (timer !== undefined) timers.clearTimeout(timer);
      timer = timers.setTimeout(() => {
        timer = undefined;
        node.hidden = true;
      }, durationMs);
    },
    dispose(): void {
      if (timer !== undefined) {
        timers.clearTimeout(timer);
        timer = undefined;
      }
    },
  };
}

export interface AnchorBox {
  readonly left: number;
  readonly right: number;
  readonly top: number;
  readonly bottom: number;
  readonly width: number;
  readonly height: number;
}

interface ActiveMenu {
  readonly trigger: HTMLElement;
  readonly menu: HTMLElement;
  readonly ownsMenu: boolean;
}

export interface AskOverlayOptions {
  readonly title: string;
  readonly label?: string;
  readonly value?: string;
  readonly placeholder?: string;
  readonly maxLength?: number;
  readonly inputMode?: string;
  readonly confirmLabel?: string;
  readonly alternative?: string;
  readonly validate?: (value: string) => string | null | undefined;
}

export type AskOverlayResult =
  | { readonly value: string; readonly alternative?: false }
  | { readonly alternative: true; readonly value?: undefined }
  | null;

export interface ConfirmOverlayOptions {
  readonly title: string;
  readonly message?: string;
  readonly confirmLabel?: string;
  readonly danger?: boolean;
}

function isVisible(node: Element | null | undefined): node is HTMLElement {
  return node instanceof HTMLElement && node.getClientRects().length > 0;
}

/**
 * Resolve the screen anchor for a menu even when its triggering control is
 * hidden or folded into the ribbon overflow button.
 */
export function anchorRect(node: Element | null | undefined): AnchorBox {
  const candidates: readonly (Element | null | undefined)[] = [
    node,
    document.getElementById('ribbonMoreBtn'),
    document.getElementById('commandOpenBtn'),
  ];
  for (const candidate of candidates) {
    if (isVisible(candidate)) return candidate.getBoundingClientRect();
  }
  const region = document.querySelector('.grid-region')?.getBoundingClientRect();
  const left = (region?.left ?? 0) + 12;
  const top = (region?.top ?? 0) + 8;
  return { left, right: left, top, bottom: top, width: 0, height: 0 };
}

export function placeOverlay(menu: HTMLElement, anchor: Element | AnchorBox, gap = 4): void {
  const rect = anchor instanceof Element ? anchorRect(anchor) : anchor;
  const width = menu.offsetWidth;
  const height = menu.offsetHeight;
  const edge = 8;
  const below = rect.bottom + gap;
  const above = rect.top - gap - height;
  const fitsBelow = below + height <= window.innerHeight - edge;
  menu.style.left = `${Math.max(edge, Math.min(rect.left, window.innerWidth - width - edge))}px`;
  menu.style.top = `${Math.max(
    edge,
    fitsBelow || above < edge ? Math.min(below, window.innerHeight - height - edge) : above,
  )}px`;
}

export class OverlayMenus {
  public current: ActiveMenu | null = null;

  constructor() {
    document.addEventListener('click', (event: MouseEvent) => {
      const target = event.target;
      const trigger = target instanceof Element ? target.closest('[data-menu]') : null;
      if (trigger instanceof HTMLElement) {
        event.preventDefault();
        if (!(trigger instanceof HTMLButtonElement && trigger.disabled)) {
          this.toggle(trigger);
        }
        return;
      }
      const item =
        this.current &&
        target instanceof Node &&
        this.current.menu.contains(target) &&
        target instanceof Element
          ? target.closest('button')
          : null;
      if (item && !item.hasAttribute('data-menu-keep')) {
        this.close(this.current?.menu.contains(document.activeElement) ?? false);
      }
    });
    document.addEventListener(
      'pointerdown',
      (event: PointerEvent) => {
        const open = this.current;
        const target = event.target;
        if (
          open &&
          target instanceof Node &&
          !open.menu.contains(target) &&
          !open.trigger.contains(target)
        ) {
          this.close(false);
        }
      },
      true,
    );
    document.addEventListener('keydown', (event: KeyboardEvent) => this.key(event), true);
    document.addEventListener('focusin', (event: FocusEvent) => {
      const open = this.current;
      const target = event.target;
      if (
        open &&
        target instanceof Node &&
        !open.menu.contains(target) &&
        !open.trigger.contains(target)
      ) {
        this.close(false);
      }
    });
    window.addEventListener('resize', () => this.close(false));
    window.addEventListener('blur', () => this.close(false));
  }

  toggle(trigger: HTMLElement): void {
    if (this.current?.trigger === trigger) {
      this.close();
      return;
    }
    const id = trigger.dataset.menu;
    const menu = id ? document.getElementById(id) : null;
    if (menu) this.show(trigger, menu);
  }

  show(trigger: HTMLElement, menu: HTMLElement, anchor: Element | AnchorBox = trigger): void {
    this.close(false);
    if (!menu.dispatchEvent(new CustomEvent('menuopen', { cancelable: true }))) return;
    menu.hidden = false;
    placeOverlay(menu, anchor);
    const ownsMenu = trigger.dataset.menu === menu.id;
    if (ownsMenu) trigger.setAttribute('aria-expanded', 'true');
    this.current = { trigger, menu, ownsMenu };
    const focusable = menu.querySelector<HTMLElement>(
      'button:not(:disabled),input:not(:disabled),select:not(:disabled)',
    );
    focusable?.focus({ preventScroll: true });
  }

  close(focus = true): void {
    const open = this.current;
    if (!open) return;
    this.current = null;
    open.menu.hidden = true;
    if (open.ownsMenu) open.trigger.setAttribute('aria-expanded', 'false');
    if (focus) {
      const candidates: readonly (Element | null)[] = [
        open.trigger,
        document.getElementById('ribbonMoreBtn'),
        document.getElementById('commandOpenBtn'),
      ];
      const trigger = candidates.find(isVisible);
      if (trigger) trigger.focus({ preventScroll: true });
    }
  }

  private key(event: KeyboardEvent): void {
    const open = this.current;
    if (!open) return;
    if (event.key === 'Escape') {
      event.preventDefault();
      event.stopPropagation();
      this.close();
      return;
    }
    if (event.target instanceof Element && event.target.closest('input,select')) return;
    const stepMap: Readonly<Record<string, number>> = {
      ArrowDown: 1,
      ArrowUp: -1,
      Home: 0,
      End: 0,
    };
    const step = stepMap[event.key];
    if (step === undefined) return;
    const items = [
      ...open.menu.querySelectorAll<HTMLElement>('button:not(:disabled),select:not(:disabled)'),
    ].filter(isVisible);
    if (!items.length) return;
    event.preventDefault();
    const active = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const at = active ? items.indexOf(active) : -1;
    const next =
      event.key === 'Home'
        ? 0
        : event.key === 'End'
          ? items.length - 1
          : (at + step + items.length) % items.length;
    const chosen = items[at < 0 && step < 0 ? items.length - 1 : next];
    chosen?.focus();
  }
}

let lightDismissBound = false;
function bindLightDismiss(): void {
  if (lightDismissBound) return;
  lightDismissBound = true;
  let pressed: EventTarget | null = null;
  document.addEventListener(
    'pointerdown',
    (event: PointerEvent) => {
      pressed = event.target;
    },
    true,
  );
  document.addEventListener('click', (event: MouseEvent) => {
    const dialog = event.target;
    if (
      pressed !== dialog ||
      !(dialog instanceof HTMLDialogElement) ||
      !dialog.matches('dialog[data-light-dismiss]:modal')
    ) {
      return;
    }
    const box = dialog.getBoundingClientRect();
    const inside =
      event.clientX >= box.left &&
      event.clientX <= box.right &&
      event.clientY >= box.top &&
      event.clientY <= box.bottom;
    if (!inside && dialog.dispatchEvent(new Event('cancel', { cancelable: true }))) {
      dialog.close();
    }
  });
}

function makeEl<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  className: string,
  text = '',
): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag);
  node.className = className;
  node.textContent = text;
  if (node instanceof HTMLButtonElement) node.type = 'button';
  return node;
}

function createDialogFrame(title: string, className: string) {
  const dialog = makeEl('dialog', `float-dialog ask-dialog ${className}`);
  const head = makeEl('div', 'dialog-head');
  const body = makeEl('div', 'dialog-body');
  const actions = makeEl('div', 'dialog-actions');
  const close = makeEl('button', 'dialog-close', '×');
  close.setAttribute('aria-label', localize('ui.common.close'));
  head.append(makeEl('div', 'dialog-title', title), close);
  body.append(actions);
  dialog.append(head, body);
  dialog.setAttribute('aria-label', title);
  return { dialog, body, actions, close };
}

function presentDialog<T>(
  dialog: HTMLDialogElement,
  resolve: (value: T) => void,
  readResult: () => T,
): void {
  const opener = document.activeElement;
  dialog.addEventListener(
    'close',
    () => {
      dialog.remove();
      if (
        opener instanceof HTMLElement &&
        opener.isConnected &&
        (!document.activeElement || document.activeElement === document.body)
      ) {
        opener.focus({ preventScroll: true });
      }
      resolve(readResult());
    },
    { once: true },
  );
  document.body.append(dialog);
  dialog.showModal();
}

export function askOverlay(options: AskOverlayOptions): Promise<AskOverlayResult> {
  const {
    title,
    label = '',
    value = '',
    placeholder = '',
    maxLength = 0,
    inputMode = 'text',
    confirmLabel = localize('ui.overlays.ok'),
    alternative = '',
    validate,
  } = options;
  return new Promise<AskOverlayResult>((resolve) => {
    const { dialog, body, actions, close } = createDialogFrame(title, 'ask-input');
    const field = makeEl('label', 'ask-field');
    const input = makeEl('input', 'field-control');
    const problem = makeEl('p', 'ask-problem');
    input.value = value;
    input.inputMode = inputMode;
    input.autocomplete = 'off';
    input.spellcheck = false;
    if (placeholder) input.placeholder = placeholder;
    if (maxLength > 0) input.maxLength = maxLength;
    problem.setAttribute('role', 'alert');
    problem.hidden = true;
    if (label) field.append(makeEl('span', '', label));
    else input.setAttribute('aria-label', title);
    field.append(input);
    body.prepend(field, problem);

    let result: AskOverlayResult = null;
    const finish = (next: AskOverlayResult): void => {
      result = next;
      dialog.close();
    };
    const accept = (): void => {
      const trimmed = input.value.trim();
      const message = validate?.(trimmed);
      if (message) {
        problem.textContent = message;
        problem.hidden = false;
        input.focus();
        input.select();
        return;
      }
      finish({ value: trimmed });
    };
    const other = alternative ? makeEl('button', 'mini-button ask-alternative', alternative) : null;
    const cancel = makeEl('button', 'mini-button', localize('ui.common.cancel'));
    const ok = makeEl('button', 'primary-button', confirmLabel);
    cancel.dataset.no = '';
    ok.dataset.yes = '';
    other?.addEventListener('click', () => finish({ alternative: true }));
    cancel.addEventListener('click', () => finish(null));
    close.addEventListener('click', () => finish(null));
    ok.addEventListener('click', accept);
    input.addEventListener('input', () => {
      problem.hidden = true;
    });
    input.addEventListener('keydown', (event: KeyboardEvent) => {
      if (event.key === 'Enter' && !isComposing(event)) {
        event.preventDefault();
        accept();
      }
    });
    actions.append(...(other ? [other] : []), cancel, ok);
    presentDialog(dialog, resolve, () => result);
    input.focus();
    input.select();
  });
}

export function confirmOverlay(options: ConfirmOverlayOptions): Promise<boolean> {
  const { title, message = '', confirmLabel = localize('ui.overlays.ok'), danger = false } = options;
  return new Promise<boolean>((resolve) => {
    const { dialog, body, actions, close } = createDialogFrame(title, 'ask-confirm');
    dialog.dataset.lightDismiss = '';
    let result = false;
    const finish = (next: boolean): void => {
      result = next;
      dialog.close();
    };
    const cancel = makeEl('button', 'mini-button', localize('ui.common.cancel'));
    const ok = makeEl('button', `primary-button${danger ? ' danger' : ''}`, confirmLabel);
    cancel.dataset.no = '';
    ok.dataset.yes = '';
    cancel.addEventListener('click', () => finish(false));
    close.addEventListener('click', () => finish(false));
    ok.addEventListener('click', () => finish(true));
    if (message) body.prepend(makeEl('p', 'ask-message', message));
    actions.append(cancel, ok);
    presentDialog(dialog, resolve, () => result);
    (danger ? cancel : ok).focus();
  });
}

let sharedMenus: OverlayMenus | null = null;

export function initOverlays(): OverlayMenus | null {
  if (typeof document === 'undefined') return null;
  if (!sharedMenus) {
    sharedMenus = new OverlayMenus();
    bindLightDismiss();
  }
  return sharedMenus;
}

export function getOverlayMenus(): OverlayMenus | null {
  return sharedMenus ?? initOverlays();
}

export const overlays = {
  place: placeOverlay,
  anchorRect,
  ask: askOverlay,
  confirm: confirmOverlay,
  get menus(): OverlayMenus | null {
    return getOverlayMenus();
  },
};
