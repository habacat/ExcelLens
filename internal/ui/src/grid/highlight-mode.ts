/**
 * Crosshair highlight color & visibility toggle. Display-only preference.
 */

export type HighlightColorId = 'blue' | 'green' | 'amber' | 'purple' | 'rose';

interface HighlightColorSpec {
  readonly name: string;
  readonly color: string;
}

const COLORS: Readonly<Record<HighlightColorId, HighlightColorSpec>> = Object.freeze({
  blue: { name: '蓝色', color: '#3c7dc3' },
  green: { name: '绿色', color: '#109968' },
  amber: { name: '琥珀', color: '#b77a19' },
  purple: { name: '紫色', color: '#8654b6' },
  rose: { name: '玫红', color: '#b95179' },
});

const ENABLED_KEY = 'ExcelLens.crosshairEnabled';
const COLOR_KEY = 'ExcelLens.crosshairColor';

function isColorId(value: string): value is HighlightColorId {
  return Object.hasOwn(COLORS, value);
}

export interface HighlightModeOptions {
  readonly toggle: HTMLInputElement;
  readonly button: HTMLButtonElement;
  readonly panel: HTMLElement;
  readonly onChange?: () => void;
}

export class HighlightMode {
  private readonly toggle: HTMLInputElement;
  private readonly button: HTMLButtonElement;
  private readonly panel: HTMLElement;
  private readonly root: HTMLElement;
  private readonly onChange?: () => void;
  public enabled = true;
  public color: HighlightColorId = 'green';

  constructor(options: HighlightModeOptions) {
    this.toggle = options.toggle;
    this.button = options.button;
    this.panel = options.panel;
    this.root = document.documentElement;
    this.onChange = options.onChange;

    let enabled = true;
    let color = 'green';
    try {
      enabled = localStorage.getItem(ENABLED_KEY) !== 'false';
      color = localStorage.getItem(COLOR_KEY) ?? 'green';
    } catch {
      // Ignore restricted storage.
    }
    this.apply(enabled, color, false);

    this.toggle.addEventListener('change', () => this.apply(this.toggle.checked, this.color));
    this.button.addEventListener('click', () => {
      if (!this.panel.hidden) {
        this.close();
      } else {
        this.panel.hidden = false;
        this.button.setAttribute('aria-expanded', 'true');
        this.panel.querySelector<HTMLInputElement>('input:checked')?.focus();
      }
    });
    this.panel.addEventListener('change', (event: Event) => {
      const target = event.target;
      if (target instanceof HTMLInputElement && target.name === 'crosshairColor') {
        this.apply(this.enabled, target.value);
      }
    });
    this.panel.querySelector('[data-highlight-close]')?.addEventListener('click', () => this.close());
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

  apply(enabled: boolean, rawColor: string, persist = true): void {
    this.enabled = enabled;
    this.color = isColorId(rawColor) ? rawColor : 'green';
    const palette = COLORS[this.color];
    this.toggle.checked = this.enabled;
    this.root.setAttribute('data-crosshair-enabled', String(this.enabled));
    this.root.style.setProperty('--crosshair-color', palette.color);
    this.root.style.setProperty('--selection-color', palette.color);
    this.root.style.setProperty('--selection-active-fill', `${palette.color}1f`);
    this.button.title = `行列与选区颜色：${palette.name}`;
    this.button.setAttribute('aria-label', this.button.title);
    for (const input of this.panel.querySelectorAll<HTMLInputElement>(
      'input[name="crosshairColor"]',
    )) {
      input.checked = input.value === this.color;
    }
    if (persist) {
      try {
        localStorage.setItem(ENABLED_KEY, String(this.enabled));
        localStorage.setItem(COLOR_KEY, this.color);
      } catch {
        // Ignore restricted storage.
      }
      this.onChange?.();
    }
  }

  close(focus = true): void {
    if (this.panel.hidden) return;
    this.panel.hidden = true;
    this.button.setAttribute('aria-expanded', 'false');
    if (focus) this.button.focus({ preventScroll: true });
  }
}
