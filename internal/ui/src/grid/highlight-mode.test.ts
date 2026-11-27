import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from '../test-helpers/dom-mock.ts';
import { HighlightMode, type HighlightColorId } from './highlight-mode.ts';

function setup(saved: Record<string, string> = {}, denied = false) {
  const dom = installDom();
  const storage = new Map<string, string>(Object.entries(saved));

  Object.defineProperty(globalThis, 'localStorage', {
    value: {
      getItem(key: string): string | null {
        if (denied) throw new Error('blocked');
        return storage.get(key) ?? null;
      },
      setItem(key: string, value: string): void {
        if (denied) throw new Error('blocked');
        storage.set(key, value);
      },
      removeItem(key: string): void {
        storage.delete(key);
      },
      clear(): void {
        storage.clear();
      },
    },
    configurable: true,
    writable: true,
  });

  const toggle = dom.register('crosshairToggle', 'input');
  toggle.type = 'checkbox';
  const button = dom.register('crosshairColorBtn', 'button');
  const panel = dom.register('crosshairPanel', 'div');
  panel.hidden = true;
  const close = document.createElement('button');
  close.setAttribute('data-highlight-close', '');
  panel.append(close);

  const colors: readonly HighlightColorId[] = ['blue', 'green', 'amber', 'purple', 'rose'];
  const inputs: HTMLInputElement[] = colors.map((value) => {
    const input = document.createElement('input');
    input.type = 'radio';
    input.name = 'crosshairColor';
    input.value = value;
    panel.append(input);
    return input;
  });

  let changed = 0;
  const mode = new HighlightMode({
    toggle,
    button,
    panel,
    onChange: () => {
      changed++;
    },
  });

  return {
    mode,
    root: document.documentElement,
    toggle,
    button,
    panel,
    inputs,
    storage,
    get changed(): number {
      return changed;
    },
    restore: () => dom.restore(),
  };
}

test('crosshair is enabled by default and toggling it preserves the chosen color', () => {
  const h = setup();
  try {
    assert.equal(h.toggle.checked, true);
    assert.equal(h.mode.color, 'green');
    for (const [key, color] of [
      ['green', '#109968'],
      ['amber', '#b77a19'],
      ['purple', '#8654b6'],
      ['rose', '#b95179'],
    ] as const) {
      h.mode.apply(true, key);
      assert.equal(h.root.style.getPropertyValue('--crosshair-color'), color);
      assert.equal(h.root.style.getPropertyValue('--selection-color'), color);
      assert.equal(h.inputs.filter((i) => i.checked).length, 1);
    }
    assert.equal(h.changed, 4);
    h.toggle.checked = false;
    h.toggle.dispatchEvent(new Event('change'));
    assert.equal(h.root.getAttribute('data-crosshair-enabled'), 'false');
    assert.equal(h.mode.color, 'rose');
    assert.equal(h.storage.get('ExcelLens.crosshairEnabled'), 'false');
    h.toggle.checked = true;
    h.toggle.dispatchEvent(new Event('change'));
    assert.equal(h.root.getAttribute('data-crosshair-enabled'), 'true');
    assert.equal(h.mode.color, 'rose');
  } finally {
    h.restore();
  }
});

test('saved crosshair settings restore without altering the page theme and tolerate storage denial', () => {
  const h = setup({
    'ExcelLens.crosshairEnabled': 'false',
    'ExcelLens.crosshairColor': 'purple',
  });
  try {
    assert.equal(h.toggle.checked, false);
    assert.equal(h.mode.color, 'purple');
    assert.equal(h.root.getAttribute('data-reading-preset'), null);
  } finally {
    h.restore();
  }
  const invalid = setup({ 'ExcelLens.crosshairColor': '__proto__' });
  try {
    assert.equal(invalid.mode.color, 'green');
  } finally {
    invalid.restore();
  }
  const denied = setup({}, true);
  try {
    denied.mode.apply(false, 'amber');
    assert.equal(denied.mode.color, 'amber');
    assert.equal(denied.toggle.checked, false);
  } finally {
    denied.restore();
  }
});

test('color popup supports keyboard and outside closing while retaining grid click focus', () => {
  const h = setup();
  try {
    h.button.click();
    assert.equal(h.panel.hidden, false);
    assert.equal(document.activeElement, h.inputs[1]);
    const outside = document.createElement('div');
    document.body.append(outside);
    outside.dispatchEvent(new Event('pointerdown', { bubbles: true }));
    assert.equal(h.panel.hidden, true);
    assert.equal(document.activeElement, h.inputs[1]);

    h.button.click();
    const esc = new Event('keydown', { cancelable: true, bubbles: true });
    Object.defineProperty(esc, 'key', { value: 'Escape' });
    h.panel.dispatchEvent(esc);
    assert.equal(h.panel.hidden, true);
    assert.equal(document.activeElement, h.button);
  } finally {
    h.restore();
  }
});
