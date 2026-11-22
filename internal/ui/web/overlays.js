// Shared behavior for dropdown menus and dialog dismissal. Controls keep
// their own handlers; this module only opens, places and closes surfaces.
(() => {
  'use strict';
  const visible = node => !!node && node.getClientRects().length > 0;

  // A command can run while its control is folded away or sits on another
  // tab. Fall back to the overflow button, then to the top of the sheet, so a
  // menu never lands in the window corner.
  function anchorRect(node) {
    for (const candidate of [node, document.getElementById('ribbonMoreBtn')]) {
      if (visible(candidate)) return candidate.getBoundingClientRect();
    }
    const region = document.querySelector('.grid-region')?.getBoundingClientRect();
    const left = (region?.left || 0) + 12, top = (region?.top || 0) + 8;
    return {left, right: left, top, bottom: top, width: 0, height: 0};
  }

  function place(menu, anchor, gap = 4) {
    const rect = anchor instanceof Element ? anchorRect(anchor) : anchor;
    const width = menu.offsetWidth, height = menu.offsetHeight, edge = 8;
    const below = rect.bottom + gap, above = rect.top - gap - height;
    const fitsBelow = below + height <= window.innerHeight - edge;
    menu.style.left = Math.max(edge, Math.min(rect.left, window.innerWidth - width - edge)) + 'px';
    menu.style.top = Math.max(edge, fitsBelow || above < edge ? Math.min(below, window.innerHeight - height - edge) : above) + 'px';
  }

  class Menus {
    constructor() {
      this.current = null;
      document.addEventListener('click', event => {
        const trigger = event.target.closest?.('[data-menu]');
        if (trigger) {
          event.preventDefault();
          if (!trigger.disabled) this.toggle(trigger);
          return;
        }
        const item = this.current && this.current.menu.contains(event.target) && event.target.closest('button');
        if (item && !item.hasAttribute('data-menu-keep')) this.close(this.current.menu.contains(document.activeElement));
      });
      document.addEventListener('pointerdown', event => {
        const open = this.current;
        if (open && !open.menu.contains(event.target) && !open.trigger.contains(event.target)) this.close(false);
      }, true);
      document.addEventListener('keydown', event => this.key(event), true);
      document.addEventListener('focusin', event => {
        const open = this.current;
        if (open && !open.menu.contains(event.target) && !open.trigger.contains(event.target)) this.close(false);
      });
      window.addEventListener('resize', () => this.close(false));
      window.addEventListener('blur', () => this.close(false));
    }
    toggle(trigger) {
      if (this.current?.trigger === trigger) { this.close(); return; }
      const menu = document.getElementById(trigger.dataset.menu);
      if (menu) this.show(trigger, menu);
    }
    show(trigger, menu) {
      this.close(false);
      // Owners may fill or veto a menu immediately before it opens.
      if (!menu.dispatchEvent(new CustomEvent('menuopen', {cancelable: true}))) return;
      menu.hidden = false;
      place(menu, trigger);
      trigger.setAttribute('aria-expanded', 'true');
      this.current = {trigger, menu};
      menu.querySelector('button:not(:disabled),input:not(:disabled),select:not(:disabled)')?.focus({preventScroll:true});
    }
    close(focus = true) {
      const open = this.current;
      if (!open) return;
      this.current = null;
      open.menu.hidden = true;
      open.trigger.setAttribute('aria-expanded', 'false');
      if (focus) {
        const trigger = visible(open.trigger) ? open.trigger : document.getElementById('ribbonMoreBtn');
        if (visible(trigger)) trigger.focus({preventScroll: true});
      }
    }
    key(event) {
      const open = this.current;
      if (!open) return;
      if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); this.close(); return; }
      if (event.target.closest?.('input,select')) return;
      const step = {ArrowDown: 1, ArrowUp: -1, Home: 0, End: 0}[event.key];
      if (step === undefined) return;
      const items = [...open.menu.querySelectorAll('button:not(:disabled),select:not(:disabled)')].filter(visible);
      if (!items.length) return;
      event.preventDefault();
      const at = items.indexOf(document.activeElement);
      const next = event.key === 'Home' ? 0 : event.key === 'End' ? items.length - 1 : (at + step + items.length) % items.length;
      items[at < 0 && step < 0 ? items.length - 1 : next].focus();
    }
  }

  // A click on the backdrop equals Escape, so a dialog keeps its own cancel
  // rules. Only dialogs without typed input opt in.
  function bindLightDismiss() {
    let pressed = null;
    document.addEventListener('pointerdown', event => { pressed = event.target; }, true);
    document.addEventListener('click', event => {
      const dialog = event.target;
      if (pressed !== dialog || !dialog.matches?.('dialog[data-light-dismiss]:modal')) return;
      const box = dialog.getBoundingClientRect();
      const inside = event.clientX >= box.left && event.clientX <= box.right && event.clientY >= box.top && event.clientY <= box.bottom;
      if (!inside && dialog.dispatchEvent(new Event('cancel', {cancelable: true}))) dialog.close();
    });
  }

  // In-app replacements for the browser's prompt and confirm. They use the
  // shared dialog parts, do not block the page and cannot be switched off by
  // the browser. Every label is inserted as text. Both resolve on close.
  function make(tag, className, text = '') {
    const node = document.createElement(tag);
    node.className = className;
    node.textContent = text;
    if (tag === 'button') node.type = 'button';
    return node;
  }
  function frame(title, className) {
    const dialog = make('dialog', 'float-dialog ask-dialog ' + className);
    const head = make('div', 'dialog-head'), body = make('div', 'dialog-body'), actions = make('div', 'dialog-actions');
    const close = make('button', 'dialog-close', '×');
    close.setAttribute('aria-label', '关闭');
    head.append(make('div', 'dialog-title', title), close);
    body.append(actions);
    dialog.append(head, body);
    dialog.setAttribute('aria-label', title);
    return {dialog, body, actions, close};
  }
  function present(dialog, resolve, result) {
    dialog.addEventListener('close', () => { dialog.remove(); resolve(result()); }, {once: true});
    document.body.append(dialog);
    dialog.showModal();
  }

  // Resolves with {value}, with {alternative: true} for the optional second
  // action, or with null when cancelled. `validate` returns a message to keep
  // the dialog open, or nothing to accept the text.
  function ask({title, label = '', value = '', inputMode = 'text', confirmLabel = '确定', alternative = '', validate}) {
    return new Promise(resolve => {
      const {dialog, body, actions, close} = frame(title, 'ask-input');
      const field = make('label', 'ask-field'), input = make('input', 'field-control'), problem = make('p', 'ask-problem');
      input.value = value; input.inputMode = inputMode; input.autocomplete = 'off'; input.spellcheck = false;
      problem.setAttribute('role', 'alert'); problem.hidden = true;
      field.append(make('span', '', label), input);
      body.prepend(field, problem);
      let result = null;
      const finish = next => { result = next; dialog.close(); };
      const accept = () => {
        const message = validate?.(input.value.trim());
        if (message) { problem.textContent = message; problem.hidden = false; input.focus(); input.select(); return; }
        finish({value: input.value.trim()});
      };
      const other = alternative ? make('button', 'mini-button ask-alternative', alternative) : null;
      const cancel = make('button', 'mini-button', '取消'), ok = make('button', 'primary-button', confirmLabel);
      other?.addEventListener('click', () => finish({alternative: true}));
      cancel.addEventListener('click', () => finish(null));
      close.addEventListener('click', () => finish(null));
      ok.addEventListener('click', accept);
      input.addEventListener('input', () => { problem.hidden = true; });
      input.addEventListener('keydown', event => { if (event.key === 'Enter' && !event.isComposing) { event.preventDefault(); accept(); } });
      actions.append(...(other ? [other] : []), cancel, ok);
      present(dialog, resolve, () => result);
      input.focus(); input.select();
    });
  }

  // Resolves with true only when the named action is chosen.
  function confirm({title, message = '', confirmLabel = '确定', danger = false}) {
    return new Promise(resolve => {
      const {dialog, body, actions, close} = frame(title, 'ask-confirm');
      dialog.dataset.lightDismiss = '';
      let result = false;
      const finish = next => { result = next; dialog.close(); };
      const cancel = make('button', 'mini-button', '取消'), ok = make('button', 'primary-button' + (danger ? ' danger' : ''), confirmLabel);
      cancel.addEventListener('click', () => finish(false));
      close.addEventListener('click', () => finish(false));
      ok.addEventListener('click', () => finish(true));
      if (message) body.prepend(make('p', 'ask-message', message));
      actions.append(cancel, ok);
      present(dialog, resolve, () => result);
      // A destructive action is never the default key target.
      (danger ? cancel : ok).focus();
    });
  }

  const menus = typeof document === 'undefined' ? null : new Menus();
  if (menus) bindLightDismiss();
  window.ExcelLensOverlays = {place, anchorRect, menus, ask, confirm};
})();
