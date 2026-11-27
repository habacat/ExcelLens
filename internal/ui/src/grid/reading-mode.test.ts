import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from '../test-helpers/dom-mock.ts';
import { ReadingMode, type ReadingPresetId } from './reading-mode.ts';

function setup(saved = 'off', denied = false, texture: string | null = null) {
  const dom = installDom();
  const storage = new Map<string, string | null>([
    ['ExcelLens.readingPreset', saved],
    ['ExcelLens.gridTexture', texture],
  ]);
  const changes: { readonly type: string; readonly preset: string }[] = [];
  const textures: boolean[] = [];

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

  document.addEventListener('excellens:readingchange', (event) => {
    changes.push({
      type: event.type,
      preset: document.documentElement.getAttribute('data-reading-preset') ?? 'off',
    });
  });

  const button = dom.register('readingModeBtn', 'button');
  const panel = dom.register('readingModePanel', 'div');
  panel.hidden = true;
  const veil = dom.register('readingVeil', 'div');
  veil.hidden = true;
  const close = document.createElement('button');
  close.setAttribute('data-reading-close', '');
  const textureToggle = document.createElement('input');
  textureToggle.type = 'checkbox';
  panel.append(close, textureToggle);

  const presets: readonly ReadingPresetId[] = [
    'off',
    'warm',
    'green',
    'mist',
    'blush',
    'ice',
    'lavender',
    'graphite',
    'navy',
    'forest',
    'warmdark',
    'plum',
    'brown',
  ];
  const inputs: HTMLInputElement[] = presets.map((value) => {
    const input = document.createElement('input');
    input.type = 'radio';
    input.name = 'readingPreset';
    input.value = value;
    panel.append(input);
    return input;
  });

  const mode = new ReadingMode({
    button,
    panel,
    veil,
    textureToggle,
    onTextureChange: (enabled) => textures.push(enabled),
  });

  return {
    mode,
    root: document.documentElement,
    button,
    panel,
    veil,
    inputs,
    changes,
    textures,
    textureToggle,
    get stored(): string | null {
      return storage.get('ExcelLens.readingPreset') ?? null;
    },
    get textureStored(): string | null {
      return storage.get('ExcelLens.gridTexture') ?? null;
    },
    restore: () => dom.restore(),
  };
}

test('light presets theme the whole document, retain the source-color film, and restore original colors', () => {
  const h = setup();
  try {
    assert.equal(h.veil.hidden, true);
    for (const [value, color] of [
      ['warm', '#f4eedf'],
      ['green', '#e5f0e4'],
      ['mist', '#e9edf2'],
      ['blush', '#f4e6e3'],
      ['ice', '#e3eef7'],
      ['lavender', '#eee8f4'],
    ] as const) {
      h.mode.apply(value);
      assert.equal(h.root.getAttribute('data-reading-preset'), value);
      assert.equal(h.root.getAttribute('data-reading-tone'), 'light');
      assert.equal(h.veil.hidden, false);
      assert.equal(h.veil.style.backgroundColor, color);
      assert.equal(h.stored, value);
      assert.equal(h.inputs.filter((i) => i.checked).length, 1);
    }
    h.mode.apply('off');
    assert.equal(h.veil.hidden, true);
    assert.equal(h.veil.style.backgroundColor, '');
    assert.equal(h.root.getAttribute('data-reading-preset'), null);
    assert.equal(h.root.getAttribute('data-reading-tone'), null);
  } finally {
    h.restore();
  }
});

test('dark presets restore from storage, theme cached nodes through the root, and never darken text with a film', () => {
  const h = setup('navy');
  try {
    assert.equal(h.mode.value, 'navy');
    assert.equal(h.root.getAttribute('data-reading-tone'), 'dark');
    for (const value of ['graphite', 'forest', 'navy', 'warmdark', 'plum', 'brown'] as const) {
      h.mode.apply(value);
      assert.equal(h.root.getAttribute('data-reading-preset'), value);
      assert.equal(h.veil.hidden, true);
      assert.equal(h.veil.style.backgroundColor, '');
      assert.equal(h.inputs.filter((i) => i.checked).length, 1);
      assert.equal(h.stored, value);
    }
    h.mode.apply('warm');
    assert.equal(h.root.getAttribute('data-reading-tone'), 'light');
    assert.equal(h.veil.hidden, false);
    h.mode.apply('off');
    assert.equal(h.root.getAttribute('data-reading-preset'), null);
    assert.equal(h.root.getAttribute('data-reading-tone'), null);
  } finally {
    h.restore();
  }
});

test('preset popup supports focus, Escape and outside clicks without stealing a grid click', () => {
  const h = setup('green');
  try {
    h.mode.open();
    assert.equal(document.activeElement, h.inputs[2]);
    assert.equal(h.button.getAttribute('aria-expanded'), 'true');
    const outside = document.createElement('div');
    document.body.append(outside);
    outside.dispatchEvent(new Event('pointerdown', { bubbles: true }));
    assert.equal(h.panel.hidden, true);
    assert.equal(document.activeElement, h.inputs[2]);

    h.mode.open();
    const esc = new Event('keydown', { cancelable: true, bubbles: true });
    Object.defineProperty(esc, 'key', { value: 'Escape' });
    h.panel.dispatchEvent(esc);
    assert.equal(esc.defaultPrevented, true);
    assert.equal(document.activeElement, h.button);
  } finally {
    h.restore();
  }
});

test('invalid or unavailable browser storage falls back safely and does not prevent choosing a preset', () => {
  const invalid = setup('__proto__');
  try {
    assert.equal(invalid.mode.value, 'off');
    assert.equal(invalid.veil.hidden, true);
  } finally {
    invalid.restore();
  }
  const denied = setup('mist', true);
  try {
    assert.equal(denied.mode.value, 'off');
    denied.mode.apply('warm');
    assert.equal(denied.veil.hidden, false);
    assert.equal(denied.mode.value, 'warm');
  } finally {
    denied.restore();
  }
});

test('canvas repaint notification follows the applied palette and fires only when the preset changes', () => {
  const h = setup('green');
  try {
    h.changes.length = 0;
    h.mode.apply('graphite');
    h.mode.apply('graphite');
    h.mode.apply('off');
    assert.deepEqual(h.changes, [
      { type: 'excellens:readingchange', preset: 'graphite' },
      { type: 'excellens:readingchange', preset: 'off' },
    ]);
  } finally {
    h.restore();
  }
});

test('worksheet texture defaults off and changes independently of reading colors and chart notifications', () => {
  const h = setup('warm');
  try {
    assert.equal(h.mode.textureEnabled, false);
    assert.equal(h.textureToggle.checked, false);
    assert.deepEqual(h.textures, []);
    h.changes.length = 0;
    h.textureToggle.checked = true;
    h.textureToggle.dispatchEvent(new Event('change', { bubbles: true }));
    assert.equal(h.mode.textureEnabled, true);
    assert.equal(h.textureStored, 'on');
    assert.equal(h.stored, 'warm');
    assert.deepEqual(h.textures, [true]);
    assert.deepEqual(h.changes, []);
    h.mode.apply('graphite');
    assert.equal(h.textureToggle.checked, true);
    assert.equal(h.textureStored, 'on');
    h.changes.length = 0;
    h.mode.applyTexture(false);
    h.mode.applyTexture(false);
    assert.equal(h.textureStored, 'off');
    assert.equal(h.stored, 'graphite');
    assert.equal(h.textureToggle.checked, false);
    assert.deepEqual(h.textures, [true, false]);
    assert.deepEqual(h.changes, []);
  } finally {
    h.restore();
  }
});

test('texture preference restores independently, keeps radio focus, and tolerates invalid or blocked storage', () => {
  const h = setup('navy', false, 'on');
  try {
    assert.equal(h.mode.textureEnabled, true);
    assert.equal(h.mode.value, 'navy');
    h.mode.open();
    assert.equal(
      document.activeElement,
      h.inputs.find((i) => i.value === 'navy'),
    );
    h.textureToggle.dispatchEvent(new Event('pointerdown', { bubbles: true }));
    assert.equal(h.panel.hidden, false);
    h.mode.applyTexture(false);
    const savedPreset = h.stored ?? 'off';
    const savedTex = h.textureStored;
    h.restore();

    const restored = setup(savedPreset, false, savedTex);
    try {
      assert.equal(restored.mode.textureEnabled, false);
      assert.equal(restored.mode.value, 'navy');
    } finally {
      restored.restore();
    }
    const invalid = setup('warm', false, 'invalid');
    try {
      assert.equal(invalid.mode.textureEnabled, false);
    } finally {
      invalid.restore();
    }
    const denied = setup('navy', true, 'on');
    try {
      assert.equal(denied.mode.textureEnabled, false);
      denied.mode.applyTexture(true);
      assert.equal(denied.textureToggle.checked, true);
      assert.deepEqual(denied.textures, [true]);
    } finally {
      denied.restore();
    }
  } finally {
    h.restore();
  }
});
