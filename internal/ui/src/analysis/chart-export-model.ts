/**
 * Pure bounded encoders for chart image + exact-data ZIP export.
 * All exact source/aggregate text remains a string.
 */
import { t as localize } from '../core/i18n.ts';
import type {
  AnalysisDisplayOptions,
  AnalysisField,
  AnalysisQuery,
  AnalysisResult,
  QuickSegment,
} from '../contracts/analysis.ts';
import type { ChartExportLimits } from '../contracts/formats.ts';
import {
  describeChart,
  type ChartDescription,
  type ChartMode,
  type ChartResultLike,
  type ChartTheme,
} from './charts.ts';
import { projectMetric } from './results.ts';

export type ChartExportType = 'png' | 'svg';
export type ChartExportBackground = 'captured' | 'white';

export interface ChartExportSpec {
  readonly type: ChartExportType;
  readonly width: number;
  readonly height: number;
  readonly scale: number;
  readonly background: ChartExportBackground;
}

export interface ChartExportDimensions {
  readonly width: number;
  readonly height: number;
  readonly pixels: number;
}

export interface SelectionQuickChartSeries {
  readonly column?: number;
  readonly segments: readonly QuickSegment[];
}

export interface SelectionQuickChartResult {
  readonly columns: readonly SelectionQuickChartSeries[];
  readonly difference?: SelectionQuickChartSeries | null;
}

export interface ChartExportBaseInput {
  readonly origin: unknown;
  readonly query?: unknown;
  readonly fields?: unknown;
  readonly mode: ChartMode;
  readonly metricIndex?: number;
  readonly seriesKey?: string;
  readonly title: string;
  readonly source: string;
  readonly scope: string;
  readonly metric: string;
  readonly unit: string;
  readonly theme: ChartTheme;
  readonly numberSettings: AnalysisDisplayOptions;
  readonly applicationVersion: string;
}

export interface ChartExportAnalysisInput extends ChartExportBaseInput {
  readonly kind: 'analysis';
  readonly result: AnalysisResult;
  readonly query: AnalysisQuery;
  readonly fields: readonly AnalysisField[];
}

export interface ChartExportSelectionQuickInput extends ChartExportBaseInput {
  readonly kind: 'selection-quick';
  readonly result: SelectionQuickChartResult;
}

export type ChartExportInput = ChartExportAnalysisInput | ChartExportSelectionQuickInput;

export interface ChartExportChartMeta {
  readonly mode: ChartMode;
  readonly metricIndex: number;
  readonly seriesKey: string;
  readonly title: string;
  readonly source: string;
  readonly scope: string;
  readonly metric: string;
  readonly unit: string;
  readonly theme: ChartTheme;
  readonly numberSettings: AnalysisDisplayOptions;
  readonly capturedAt: string;
  readonly applicationVersion: string;
  drawing?: ChartDescription;
}

export interface ChartExportAnalysisPayload {
  readonly schema: 1;
  readonly kind: 'analysis';
  readonly origin: unknown;
  readonly query: AnalysisQuery;
  readonly fields: readonly AnalysisField[];
  readonly result: AnalysisResult;
  readonly chart: ChartExportChartMeta & { readonly drawing: ChartDescription };
}

export interface ChartExportSelectionQuickPayload {
  readonly schema: 1;
  readonly kind: 'selection-quick';
  readonly origin: unknown;
  readonly query: unknown;
  readonly fields: unknown;
  readonly result: SelectionQuickChartResult;
  readonly chart: ChartExportChartMeta & { readonly drawing: ChartDescription };
}

export type ChartExportPayload = ChartExportAnalysisPayload | ChartExportSelectionQuickPayload;

export interface ChartExportFile {
  readonly name: string;
  readonly bytes: Uint8Array<ArrayBuffer>;
}

const encoder = new TextEncoder();

function inIntegerRange(n: number, lo: number, hi: number): boolean {
  return Number.isSafeInteger(n) && n >= lo && n <= hi;
}

function isUnknownArray(value: unknown): value is readonly unknown[] {
  return Array.isArray(value);
}

export function chartExportCapacityError(): Error {
  return new Error(localize('ui.chartExportModel.chartExportExceedsCapacityReduce'));
}

export function boundedStringify(value: unknown, limit: number): string {
  if (!inIntegerRange(limit, 1, 1 << 30)) throw chartExportCapacityError();
  const chunks: string[] = [];
  const active = new Set<object>();
  let remaining = limit;

  const emit = (text: string): void => {
    if (text.length > remaining) throw chartExportCapacityError();
    const size = encoder.encode(text).length;
    if (size > remaining) throw chartExportCapacityError();
    remaining -= size;
    chunks.push(text);
  };

  function visit(item: unknown, depth = 0): void {
    if (depth > 64) throw new Error(localize('ui.chartExportModel.invalidChartSnapshotLevel'));
    if (item === null) {
      emit('null');
      return;
    }
    switch (typeof item) {
      case 'string':
        if (item.length > remaining) throw chartExportCapacityError();
        emit(JSON.stringify(item));
        return;
      case 'boolean':
        emit(String(item));
        return;
      case 'number':
        if (!Number.isFinite(item)) throw new Error(localize('ui.chartExportModel.snapshotContainsInvalidValues'));
        emit(String(item));
        return;
      case 'object':
        break;
      default:
        throw new Error(localize('ui.chartExportModel.snapshotContainsDataTypesThat'));
    }
    if (active.has(item)) throw new Error(localize('ui.chartExportModel.chartSnapshotCircularReferences'));
    active.add(item);
    if (isUnknownArray(item)) {
      emit('[');
      for (let i = 0; i < item.length; i++) {
        if (i) emit(',');
        const elem = item[i];
        visit(elem === undefined ? null : elem, depth + 1);
      }
      emit(']');
    } else {
      const record = item as Record<string, unknown>;
      emit('{');
      let first = true;
      for (const key of Object.keys(record)) {
        const prop = record[key];
        if (prop === undefined) continue;
        if (!first) emit(',');
        first = false;
        emit(JSON.stringify(key));
        emit(':');
        visit(prop, depth + 1);
      }
      emit('}');
    }
    active.delete(item);
  }

  visit(value);
  return chunks.join('');
}

export function validateChartExportDimensions(
  spec: {
    readonly type: string;
    readonly width: number;
    readonly height: number;
    readonly scale: number;
    readonly background: string;
  },
  limits: ChartExportLimits,
): ChartExportDimensions {
  if (
    !limits.types.includes(spec.type) ||
    !inIntegerRange(spec.width, limits.minWidth, limits.maxWidth) ||
    !inIntegerRange(spec.height, limits.minHeight, limits.maxHeight) ||
    !limits.scales.includes(spec.scale) ||
    (spec.type === 'svg' && spec.scale !== 1) ||
    (spec.background !== 'captured' && spec.background !== 'white')
  ) {
    throw new Error(localize('ui.chartExportModel.chartTypeSizeOrMagnification'));
  }
  const pixels = spec.width * spec.height * spec.scale * spec.scale;
  if (!Number.isSafeInteger(pixels) || pixels > limits.pixels) throw chartExportCapacityError();
  return { width: spec.width * spec.scale, height: spec.height * spec.scale, pixels };
}

function selectionQuickSeriesChart(series: SelectionQuickChartSeries): ChartResultLike {
  return {
    columnKeys: [localize('ui.chartExportModel.segmentAverage')],
    rows: series.segments.map((p, i) => ({
      keys: [String(i + 1)],
      values: [{ exact: p.summary.average, display: p.summary.average, count: p.summary.count }],
    })),
  };
}

export function resolveChartExportPlot(
  payload:
    | Pick<ChartExportAnalysisPayload, 'kind' | 'result' | 'chart'>
    | Pick<ChartExportSelectionQuickPayload, 'kind' | 'result' | 'chart'>,
): ChartResultLike {
  if (payload.kind === 'analysis') {
    return projectMetric(payload.result, payload.chart.metricIndex);
  }
  const key = payload.chart.seriesKey;
  const series =
    key === 'difference'
      ? payload.result.difference
      : payload.result.columns.find((c) => String(c.column) === key);
  if (!series) throw new Error(localize('ui.chartExportModel.chartSeriesBecomeInvalid'));
  return selectionQuickSeriesChart(series);
}

export function captureChartExportPayload(
  input: ChartExportInput,
  limits: ChartExportLimits,
): ChartExportPayload {
  const payload = {
    schema: 1 as const,
    kind: input.kind,
    origin: input.origin,
    query: input.query,
    fields: input.fields,
    result: input.result,
    chart: {
      mode: input.mode,
      metricIndex: input.metricIndex ?? 0,
      seriesKey: input.seriesKey || '',
      title: input.title,
      source: input.source,
      scope: input.scope,
      metric: input.metric,
      unit: input.unit,
      theme: input.theme,
      numberSettings: input.numberSettings,
      capturedAt: new Date().toISOString(),
      applicationVersion: input.applicationVersion,
    },
  };
  // Bound before building the detached snapshot; a large current result must
  // never be silently reduced to the chart's few visible categories.
  const detached = JSON.parse(boundedStringify(payload, limits.dataBytes)) as ChartExportPayload;
  const result = resolveChartExportPlot(detached);
  const scope = describeChart(result, detached.chart.mode);
  if (scope.unrepresentable || !scope.drawableCells) {
    throw new Error(localize('ui.chartExportModel.currentChartDoesNotHave'));
  }
  return {
    ...detached,
    chart: { ...detached.chart, drawing: scope },
  };
}

const CRC_TABLE = Uint32Array.from({ length: 256 }, (_, n) => {
  let c = n;
  for (let i = 0; i < 8; i++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
  return c >>> 0;
});

async function crc32(bytes: Uint8Array, checkpoint: () => Promise<void>): Promise<number> {
  let crc = 0xffffffff;
  for (let start = 0; start < bytes.length; start += 1 << 20) {
    await checkpoint();
    const end = Math.min(bytes.length, start + (1 << 20));
    for (let i = start; i < end; i++) {
      const byte = bytes[i] ?? 0;
      crc = (CRC_TABLE[(crc ^ byte) & 255] ?? 0) ^ (crc >>> 8);
    }
  }
  return (crc ^ 0xffffffff) >>> 0;
}

export async function buildZipBlob(
  files: readonly ChartExportFile[],
  limit: number,
  checkpoint: () => Promise<void> = () => Promise.resolve(),
): Promise<Blob> {
  if (!files.length || files.length > 16) throw new Error(localize('ui.chartExportModel.numberDownloadPackageFilesInvalid'));
  let total = 22;
  const names = new Set<string>();
  const items: {
    readonly name: string;
    readonly bytes: Uint8Array<ArrayBuffer>;
    readonly nameBytes: Uint8Array<ArrayBuffer>;
  }[] = [];

  for (const file of files) {
    if (
      !/^[A-Za-z0-9][A-Za-z0-9._-]{0,79}$/.test(file.name) ||
      names.has(file.name) ||
      !(file.bytes instanceof Uint8Array)
    ) {
      throw new Error(localize('ui.chartExportModel.downloadPackageFileInvalid'));
    }
    names.add(file.name);
    const nameBytes = encoder.encode(file.name);
    total += 76 + nameBytes.length * 2 + file.bytes.length;
    if (!Number.isSafeInteger(total) || total > limit || file.bytes.length > 0xffffffff) {
      throw chartExportCapacityError();
    }
    items.push({ name: file.name, bytes: file.bytes, nameBytes });
  }

  const chunks: Uint8Array<ArrayBuffer>[] = [];
  const directory: Uint8Array<ArrayBuffer>[] = [];
  let offset = 0;

  for (const item of items) {
    const data = item.bytes;
    const name = item.nameBytes;
    const crc = await crc32(data, checkpoint);
    const head = new Uint8Array(30 + name.length);
    const view = new DataView(head.buffer);
    view.setUint32(0, 0x04034b50, true);
    view.setUint16(4, 20, true);
    view.setUint16(6, 0x800, true);
    view.setUint16(12, 0x21, true);
    view.setUint32(14, crc, true);
    view.setUint32(18, data.length, true);
    view.setUint32(22, data.length, true);
    view.setUint16(26, name.length, true);
    head.set(name, 30);

    const central = new Uint8Array(46 + name.length);
    const c = new DataView(central.buffer);
    c.setUint32(0, 0x02014b50, true);
    c.setUint16(4, 20, true);
    c.setUint16(6, 20, true);
    c.setUint16(8, 0x800, true);
    c.setUint16(14, 0x21, true);
    c.setUint32(16, crc, true);
    c.setUint32(20, data.length, true);
    c.setUint32(24, data.length, true);
    c.setUint16(28, name.length, true);
    c.setUint32(42, offset, true);
    central.set(name, 46);

    chunks.push(head, data);
    directory.push(central);
    offset += head.length + data.length;
  }

  await checkpoint();
  const directoryBytes = directory.reduce((n, x) => n + x.length, 0);
  const end = new Uint8Array(22);
  const e = new DataView(end.buffer);
  e.setUint32(0, 0x06054b50, true);
  e.setUint16(8, items.length, true);
  e.setUint16(10, items.length, true);
  e.setUint32(12, directoryBytes, true);
  e.setUint32(16, offset, true);

  const blob = new Blob([...chunks, ...directory, end], { type: 'application/zip' });
  if (blob.size !== total) throw new Error(localize('ui.chartExportModel.downloadPackageLengthVerificationFailed'));
  return blob;
}

export const ChartExportModel = Object.freeze({
  stringify: boundedStringify,
  dimensions: validateChartExportDimensions,
  plot: resolveChartExportPlot,
  capture: captureChartExportPayload,
  zip: buildZipBlob,
  capacity: chartExportCapacityError,
});
