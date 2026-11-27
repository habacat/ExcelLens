/**
 * Display formatting, HTML escaping, SVG icon sprite helper, and IME composition guard.
 */

const normal = new Intl.NumberFormat('en-US', { maximumFractionDigits: 1 });
const small = new Intl.NumberFormat('en-US', { maximumFractionDigits: 3 });

const htmlEntities: Readonly<Record<string, string>> = {
  '&': '&amp;',
  '<': '&lt;',
  '>': '&gt;',
  '"': '&quot;',
  "'": '&#39;',
};

export function formatBytes(value: unknown): string {
  if (typeof value !== 'number' || !Number.isFinite(value) || value < 0) return '—';
  let size = value / 1024;
  const units = ['KB', 'MB', 'GB', 'TB'] as const;
  let unit = 0;
  while (size >= 1024 && unit < units.length - 1) {
    size /= 1024;
    unit++;
  }
  const suffix = units[unit] ?? 'TB';
  return `${(size > 0 && size < 0.1 ? small : normal).format(size)} ${suffix}`;
}

function toDisplayText(value: unknown): string {
  if (value === null || value === undefined) return '';
  if (typeof value === 'string') return value;
  if (typeof value === 'number' || typeof value === 'boolean' || typeof value === 'bigint') {
    return String(value);
  }
  return '';
}

export function escapeHtml(value: unknown): string {
  return toDisplayText(value).replace(/[&<>"']/g, (c) => htmlEntities[c] ?? c);
}

export function icon(name: string, className = 'icon'): string {
  return `<svg class="${className}" aria-hidden="true" focusable="false"><use href="#i-${name}"/></svg>`;
}

export interface ComposingEventLike {
  readonly isComposing?: boolean;
  readonly keyCode?: number;
}

export function isComposing(event: ComposingEventLike | null | undefined): boolean {
  return Boolean(event && (event.isComposing || event.keyCode === 229));
}

export function errorMessage(error: unknown): string {
  if (error instanceof Error) return error.message;
  if (typeof error === 'string') return error;
  if (typeof error === 'number' || typeof error === 'boolean') return String(error);
  return '';
}
