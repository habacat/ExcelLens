/**
 * Page-wide reading presets & optional grid texture overlay. CSS repaints
 * existing nodes; source values, styles, coordinates, and exports stay untouched.
 */

import { t as localize } from '../core/i18n.ts';
export type ReadingPresetId =
  | 'off'
  | 'warm'
  | 'green'
  | 'mist'
  | 'blush'
  | 'ice'
  | 'lavender'
  | 'graphite'
  | 'navy'
  | 'forest'
  | 'warmdark'
  | 'plum'
  | 'brown';

interface ReadingPreset {
  readonly name: string;
  readonly color: string;
  readonly dark?: boolean;
}

const PRESETS: Readonly<Record<ReadingPresetId, ReadingPreset>> = Object.freeze({
  off: { name: localize('ui.readingMode.primaryColors'), color: '' },
  warm: { name: localize('ui.readingMode.warmPaper'), color: '#f4eedf' },
  green: { name: localize('ui.readingMode.lightGreen'), color: '#e5f0e4' },
  mist: { name: localize('ui.readingMode.softGray'), color: '#e9edf2' },
  blush: { name: localize('ui.readingMode.apricotPowder'), color: '#f4e6e3' },
  ice: { name: localize('ui.readingMode.iceBlue'), color: '#e3eef7' },
  lavender: { name: localize('ui.readingMode.lightPurple'), color: '#eee8f4' },
  graphite: { name: localize('ui.readingMode.graphite'), dark: true, color: '' },
  navy: { name: localize('ui.readingMode.deepBlue'), dark: true, color: '' },
  forest: { name: localize('ui.readingMode.darkGreen'), dark: true, color: '' },
  warmdark: { name: localize('ui.readingMode.warmGray'), dark: true, color: '' },
  plum: { name: localize('ui.readingMode.purpleNight'), dark: true, color: '' },
  brown: { name: localize('ui.readingMode.deepBrown'), dark: true, color: '' },
});

const KEY = 'ExcelLens.readingPreset';
const TEXTURE_KEY = 'ExcelLens.gridTexture';

function isPresetId(value: string): value is ReadingPresetId {
  return Object.hasOwn(PRESETS, value);
}

export interface ReadingModeOptions {
  readonly button: HTMLButtonElement;
  readonly panel: HTMLElement;
  readonly veil: HTMLElement;
  readonly textureToggle?: HTMLInputElement | null;
  readonly onTextureChange?: (enabled: boolean) => void;
}

export class ReadingMode {
  private readonly button: HTMLButtonElement;
  private readonly panel: HTMLElement;
  private readonly veil: HTMLElement;
  private readonly root: HTMLElement;
  private readonly textureToggle?: HTMLInputElement | null;
  private readonly onTextureChange?: (enabled: boolean) => void;
  public value: ReadingPresetId = 'off';
  public textureEnabled: boolean | undefined;

  constructor(options: ReadingModeOptions) {
    this.button = options.button;
    this.panel = options.panel;
    this.veil = options.veil;
    this.root = document.documentElement;
    this.textureToggle = options.textureToggle;
    this.onTextureChange = options.onTextureChange;

    let saved = 'off';
    let texture = false;
    try {
      saved = localStorage.getItem(KEY) ?? 'off';
    } catch {
      // Ignore restricted storage.
    }
    try {
      texture = localStorage.getItem(TEXTURE_KEY) === 'on';
    } catch {
      // Ignore restricted storage.
    }
    this.apply(saved, false);
    this.applyTexture(texture, false);

    this.button.addEventListener('click', () => {
      if (this.panel.hidden) this.open();
      else this.close();
    });
    this.panel.addEventListener('change', (event: Event) => {
      const target = event.target;
      if (!(target instanceof HTMLInputElement)) return;
      if (target.name === 'readingPreset') this.apply(target.value);
      else if (target === this.textureToggle) this.applyTexture(target.checked);
    });
    this.panel.querySelector('[data-reading-close]')?.addEventListener('click', () => this.close());
    this.panel.addEventListener('keydown', (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        event.stopPropagation();
        this.close();
      }
    });
    document.addEventListener('pointerdown', (event: PointerEvent) => {
      const target = event.target;
      if (
        !this.panel.hidden &&
        target instanceof Node &&
        !this.panel.contains(target) &&
        !this.button.contains(target)
      ) {
        this.close(false);
      }
    });
  }

  apply(rawValue: string, persist = true): void {
    const previous = this.value;
    this.value = isPresetId(rawValue) ? rawValue : 'off';
    const preset = PRESETS[this.value];
    if (this.value === 'off') {
      this.root.removeAttribute('data-reading-preset');
      this.root.removeAttribute('data-reading-tone');
    } else {
      this.root.setAttribute('data-reading-preset', this.value);
      this.root.setAttribute('data-reading-tone', preset.dark ? 'dark' : 'light');
    }
    this.veil.hidden = this.value === 'off' || Boolean(preset.dark);
    this.veil.style.backgroundColor = preset.color;
    this.button.classList.toggle('is-active', this.value !== 'off');
    this.button.title = localize('ui.readingMode.eyeProtectionMode', { name: preset.name });
    this.button.setAttribute('aria-label', this.button.title);
    for (const input of this.panel.querySelectorAll<HTMLInputElement>(
      'input[name="readingPreset"]',
    )) {
      input.checked = input.value === this.value;
    }
    if (persist) {
      try {
        localStorage.setItem(KEY, this.value);
      } catch {
        // Ignore restricted storage.
      }
    }
    if (previous !== this.value) {
      document.dispatchEvent(new Event('excellens:readingchange'));
    }
  }

  applyTexture(enabled: boolean, persist = true): void {
    const previous = this.textureEnabled;
    this.textureEnabled = enabled;
    if (this.textureToggle) this.textureToggle.checked = this.textureEnabled;
    if (persist) {
      try {
        localStorage.setItem(TEXTURE_KEY, this.textureEnabled ? 'on' : 'off');
      } catch {
        // Ignore restricted storage.
      }
    }
    if (previous !== undefined && previous !== this.textureEnabled) {
      this.onTextureChange?.(this.textureEnabled);
    }
  }

  open(): void {
    this.panel.hidden = false;
    this.button.setAttribute('aria-expanded', 'true');
    this.panel.querySelector<HTMLInputElement>('input[name="readingPreset"]:checked')?.focus();
  }

  close(focus = true): void {
    if (this.panel.hidden) return;
    this.panel.hidden = true;
    this.button.setAttribute('aria-expanded', 'false');
    if (focus) this.button.focus({ preventScroll: true });
  }
}
