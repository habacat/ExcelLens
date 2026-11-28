/**
 * Display formatting, HTML escaping, SVG icon sprite helper, and IME composition guard.
 */

import { interfaceLocale } from './i18n.ts';
export { escapeHtml } from './html.ts';

let normal: Intl.NumberFormat | undefined;
let small: Intl.NumberFormat | undefined;

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
  const formatter = size > 0 && size < 0.1
    ? (small ??= new Intl.NumberFormat(interfaceLocale(), { maximumFractionDigits: 3 }))
    : (normal ??= new Intl.NumberFormat(interfaceLocale(), { maximumFractionDigits: 1 }));
  return `${formatter.format(size)} ${suffix}`;
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
