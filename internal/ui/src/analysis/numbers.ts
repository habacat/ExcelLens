/**
 * Presentation of aggregate cells only. Never infer numeric source types or
 * convert exact amounts to binary floating point, including for rounding.
 */
import type {
  AnalysisCell,
  AnalysisDisplayOptions,
  ComparedValue,
} from '../contracts/analysis.ts';

export interface FormattableAnalysisCell {
  readonly display?: string;
  readonly exact?: string;
  readonly status?: string;
  readonly percent?: boolean;
  readonly comparison?: ComparedValue;
}

export const DEFAULT_ANALYSIS_NUMBER_OPTIONS: Readonly<AnalysisDisplayOptions> = Object.freeze({
  groupThousands: true,
  scientific: true,
  scientificExponent: 12,
});

let currentOptions: AnalysisDisplayOptions = { ...DEFAULT_ANALYSIS_NUMBER_OPTIONS };

// Bound work on unexpected input. Out-of-budget representations retain the
// server display and exact tooltip instead of approximating or inventing zero.
const MAX_TEXT_LENGTH = 8192;
const MAX_EXPONENT = 4096;

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

export function isValidAnalysisNumberOptions(value: unknown): value is AnalysisDisplayOptions {
  if (!isRecord(value)) return false;
  return (
    typeof value.groupThousands === 'boolean' &&
    typeof value.scientific === 'boolean' &&
    (value.scientificExponent === 9 ||
      value.scientificExponent === 12 ||
      value.scientificExponent === 15)
  );
}

interface ExactRational {
  readonly n: bigint;
  readonly d: bigint;
  readonly negative: boolean;
}

function parseRational(raw: string): ExactRational | null {
  if (!raw || raw.length > MAX_TEXT_LENGTH) return null;
  let n: bigint;
  let d: bigint;
  if (raw.includes('/')) {
    const parts = raw.split('/');
    const numStr = parts[0];
    const denStr = parts[1];
    if (
      parts.length !== 2 ||
      numStr === undefined ||
      denStr === undefined ||
      !/^[+-]?\d+$/.test(numStr) ||
      !/^[+-]?\d+$/.test(denStr)
    ) {
      return null;
    }
    n = BigInt(numStr);
    d = BigInt(denStr);
    if (d === 0n) return null;
    if (d < 0n) {
      n = -n;
      d = -d;
    }
  } else {
    const match = /^([+-]?)(?:(\d+)(?:\.(\d*))?|\.(\d+))(?:[eE]([+-]?\d{1,5}))?$/.exec(raw);
    if (!match) return null;
    const fraction = match[3] ?? match[4] ?? '';
    const exponent = Number(match[5] || 0) - fraction.length;
    if (Math.abs(exponent) > MAX_EXPONENT) return null;
    n = BigInt((match[1] ?? '') + (match[2] ?? '0') + fraction);
    d = 1n;
    if (exponent >= 0) {
      n *= 10n ** BigInt(exponent);
    } else {
      d = 10n ** BigInt(-exponent);
    }
  }
  const negative = n < 0n;
  if (negative) n = -n;
  return { n, d, negative };
}

function formatScientific(raw: string, threshold: number, multiplier = 1n): string | null {
  const ratio = parseRational(raw);
  if (!ratio || ratio.n === 0n) return null;
  const { d, negative } = ratio;
  const n = ratio.n * multiplier;
  let exponent = n.toString().length - d.toString().length;
  if (exponent < threshold) return null;
  // Digit lengths give an upper bound of at most one power of ten.
  if (n < d * 10n ** BigInt(exponent)) exponent--;
  if (exponent < threshold) return null;
  // Three significant digits, rounded once from the exact rational value.
  const denominator = d * 10n ** BigInt(exponent - 2);
  let rounded = n / denominator;
  if ((n % denominator) * 2n >= denominator) rounded++;
  if (rounded >= 1000n) {
    rounded /= 10n;
    exponent++;
  }
  const digits = rounded.toString().padStart(3, '0');
  const firstDigit = digits[0] ?? '0';
  return `${negative ? '-' : ''}${firstDigit}.${digits.slice(1)}e+${exponent}`;
}

function groupDigits(display: string): string {
  const match = /^([+-]?)(\d+)(\.\d+)?$/.exec(display);
  const sign = match?.[1];
  const digits = match?.[2];
  if (sign === undefined || !digits || digits.length < 4) return display;
  const first = digits.length % 3 || 3;
  const rest = digits.slice(first).match(/\d{3}/g) ?? [];
  return `${sign}${[digits.slice(0, first), ...rest].join(',')}${match[3] ?? ''}`;
}

export function formatAnalysisNumber(
  cell: AnalysisCell | ComparedValue | FormattableAnalysisCell | null | undefined,
  settings: AnalysisDisplayOptions = currentOptions,
): string {
  const target: FormattableAnalysisCell | ComparedValue | null | undefined =
    cell && 'comparison' in cell && cell.comparison ? cell.comparison : cell;
  const display = target?.display ?? '';
  if (!display) return '—';
  const status = target && 'status' in target ? target.status : undefined;
  if (status) return display;
  const percent = target && 'percent' in target && target.percent === true;
  const suffix = percent ? '%' : '';
  const plain = percent && display.endsWith('%') ? display.slice(0, -1) : display;
  const activeSettings = isValidAnalysisNumberOptions(settings)
    ? settings
    : DEFAULT_ANALYSIS_NUMBER_OPTIONS;
  if (activeSettings.scientific) {
    const exactSource = target?.exact ?? plain;
    const value = formatScientific(
      exactSource,
      activeSettings.scientificExponent,
      percent ? 100n : 1n,
    );
    if (value !== null) return value + suffix;
  }
  return (activeSettings.groupThousands && plain.length <= MAX_TEXT_LENGTH ? groupDigits(plain) : plain) + suffix;
}

export function getAnalysisNumberOptions(): AnalysisDisplayOptions {
  return { ...currentOptions };
}

export function setAnalysisNumberOptions(value: unknown): void {
  if (!isValidAnalysisNumberOptions(value)) {
    throw new Error('分析显示设置无效');
  }
  currentOptions = { ...value };
}

export const AnalysisNumbers = Object.freeze({
  defaults: DEFAULT_ANALYSIS_NUMBER_OPTIONS,
  valid: isValidAnalysisNumberOptions,
  format: formatAnalysisNumber,
  get: getAnalysisNumberOptions,
  set: setAnalysisNumberOptions,
});
