/**
 * Exact relation envelopes and bounded source presentation, without IO or DOM.
 */
import { interfaceLocale, t as localize } from '../core/i18n.ts';
import type { CompositionField, CompositionSourceInfo, TableRef } from '../contracts/analysis.ts';
import type {
  ReconcileField,
  ReconcilePair,
  ReconcileRecord,
  ReconcileSourceInfo,
  ReconcileSourceRef,
  ReconcileValue,
  RelationCounts,
  RelationLimits,
  RelationPreview,
  RelationQuery,
  RelationResultResponse,
  RelationRow,
} from '../contracts/features.ts';
import type { FileFormatsRegistry } from '../contracts/formats.ts';
import type { ConditionalCellLike } from '../grid/conditional.ts';

export type RelationOutputModeId =
  | 'left'
  | 'inner'
  | 'left_matched'
  | 'left_unmatched'
  | 'right_unmatched'
  | 'left_invalid'
  | 'right_invalid';

export interface RelationModeInfo {
  readonly id: RelationOutputModeId;
  readonly label: string;
  readonly sides: readonly (0 | 1)[];
}

export interface RelationFieldChoice {
  readonly book: string;
  readonly source: string;
  readonly headerRow: number;
}

export interface RelationIdentity {
  readonly bookKey: string;
  readonly book: string;
}

export interface RelationExpectedEnvelope {
  readonly id: string;
  readonly identity: RelationIdentity;
  readonly mode: RelationOutputModeId;
  readonly operation: 'prepare' | 'preview';
  readonly parentId?: string;
  readonly query: RelationQuery;
  readonly infos: readonly [CompositionSourceInfo, CompositionSourceInfo];
}

export interface RelationColumnSpec {
  readonly side: 0 | 1;
  readonly column: number;
  readonly field: ReconcileField;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function isUnknownArray(value: unknown): value is readonly unknown[] {
  return Array.isArray(value);
}

function integer(value: unknown, min: number, max: number): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= min && value <= max;
}

export function copyValue<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T;
}

const COUNT_KEYS = [
  'leftRecords',
  'rightRecords',
  'leftInvalid',
  'rightInvalid',
  'leftDuplicateGroups',
  'rightDuplicateGroups',
  'manyToManyGroups',
  'matchingGroups',
  'leftMatched',
  'rightMatched',
  'leftOnly',
  'rightOnly',
  'innerRows',
  'leftJoinRows',
] as const;

export const RELATION_MODES: readonly RelationModeInfo[] = Object.freeze([
  { id: 'left', label: localize('ui.relationsModel.leftTableEntirely'), sides: [0, 1] },
  { id: 'inner', label: localize('ui.relationsModel.matchDetails'), sides: [0, 1] },
  { id: 'left_matched', label: localize('ui.relationsModel.leftTableMatched'), sides: [0] },
  { id: 'left_unmatched', label: localize('ui.relationsModel.leftTableNotMatched'), sides: [0] },
  { id: 'right_unmatched', label: localize('ui.relationsModel.rightTableNotMatched'), sides: [1] },
  { id: 'left_invalid', label: localize('ui.relationsModel.leftTableInvalidKey'), sides: [0] },
  { id: 'right_invalid', label: localize('ui.relationsModel.rightTableInvalidKey'), sides: [1] },
]);

export function relationMode(id: string): RelationModeInfo | undefined {
  return RELATION_MODES.find((item) => item.id === id);
}

function fail(): never {
  throw new Error(localize('ui.relationsModel.correlationResponseDoesNotMatch'));
}

export function parseRelationCount(value: unknown): bigint {
  if (typeof value !== 'string' || !/^(0|[1-9]\d{0,18})$/.test(value)) fail();
  return BigInt(value);
}

export function relationTotal(counts: RelationCounts, id: string): string {
  switch (id) {
    case 'left':
      return counts.leftJoinRows;
    case 'inner':
      return counts.innerRows;
    case 'left_matched':
      return counts.leftMatched;
    case 'left_unmatched':
      return counts.leftOnly;
    case 'right_unmatched':
      return counts.rightOnly;
    case 'left_invalid':
      return counts.leftInvalid;
    case 'right_invalid':
      return counts.rightInvalid;
    default:
      fail();
  }
}

export function formatRelationCount(value: string | number | bigint): string {
  return parseRelationCount(String(value)).toLocaleString(interfaceLocale());
}

export function relationFieldRef(info: CompositionSourceInfo): ReconcileSourceRef {
  return {
    book: info.ref.book,
    source: info.ref.source,
    headerRow: info.ref.headerRow,
    stamp: info.ref.stamp,
  };
}

function queryKey(query: RelationQuery): string {
  return JSON.stringify([
    query.keys.map((pair) => [
      pair.left,
      pair.right,
      pair.mode || '',
      pair.trim ?? false,
      pair.ignoreCase ?? false,
    ]),
    query.leftColumns,
    query.rightColumns,
  ]);
}

export function validateRelationLimits(value: unknown): RelationLimits {
  if (!isRecord(value)) throw new Error(localize('ui.common.associatedCapacityConfigurationInvalid'));
  const keys = [
    'keys',
    'columns',
    'previewRows',
    'defaultRows',
    'maximumRows',
    'requestBytes',
    'resultBytes',
    'viewRows',
    'viewColumns',
    'initialFields',
    'snippetUnits',
  ] as const;
  for (const key of keys) {
    if (!integer(value[key], 1, Number.MAX_SAFE_INTEGER)) {
      throw new Error(localize('ui.common.associatedCapacityConfigurationInvalid'));
    }
  }
  const keysLimit = value.keys as number;
  const columns = value.columns as number;
  const previewRows = value.previewRows as number;
  const defaultRows = value.defaultRows as number;
  const maximumRows = value.maximumRows as number;
  const requestBytes = value.requestBytes as number;
  const resultBytes = value.resultBytes as number;
  const attachments = integer(value.attachments, 0, Number.MAX_SAFE_INTEGER) ? value.attachments : 0;
  const viewRows = value.viewRows as number;
  const viewColumns = value.viewColumns as number;
  const initialFields = value.initialFields as number;
  const snippetUnits = value.snippetUnits as number;
  const zipRows = value.zipRows;

  if (
    viewRows > previewRows ||
    initialFields > columns ||
    defaultRows > maximumRows ||
    (zipRows !== undefined && !integer(zipRows, 1, maximumRows))
  ) {
    throw new Error(localize('ui.common.associatedCapacityConfigurationInvalid'));
  }
  return {
    keys: keysLimit,
    columns,
    previewRows,
    defaultRows,
    maximumRows,
    requestBytes,
    resultBytes,
    attachments,
    viewRows,
    viewColumns,
    initialFields,
    snippetUnits,
    zipRows: typeof zipRows === 'number' ? zipRows : 0,
  };
}

export function validateRelationFields(
  data: unknown,
  choice: RelationFieldChoice,
  identity: RelationIdentity,
  formats: Pick<FileFormatsRegistry, 'maxRows' | 'maxColumns'>,
): CompositionSourceInfo {
  if (!isRecord(data)) fail();
  const info = data.info;
  if (!isRecord(info)) fail();
  const ref = info.ref;
  if (!isRecord(ref)) fail();
  if (
    data.workbookKey !== identity.bookKey ||
    data.book !== identity.book ||
    data.mode !== 'fields' ||
    ref.book !== choice.book ||
    ref.source !== choice.source ||
    ref.headerRow !== choice.headerRow ||
    ref.scope !== undefined && ref.scope !== null ||
    typeof ref.fingerprint !== 'string' ||
    !ref.fingerprint ||
    typeof ref.stamp !== 'string' ||
    !ref.stamp ||
    (info.kind !== 'workbook' && info.kind !== 'analysis') ||
    typeof info.name !== 'string' ||
    typeof info.sheet !== 'string' ||
    !integer(info.rows, 0, formats.maxRows) ||
    !isUnknownArray(info.fields) ||
    info.fields.length < 1 ||
    info.fields.length > formats.maxColumns
  ) {
    fail();
  }
  let previous = 0;
  const fields: CompositionField[] = [];
  for (const field of info.fields) {
    if (
      !isRecord(field) ||
      !integer(field.column, previous + 1, formats.maxColumns) ||
      typeof field.name !== 'string' ||
      (field.headerHash !== undefined && typeof field.headerHash !== 'string')
    ) {
      fail();
    }
    for (const key of ['headerLoaded', 'headerUnique', 'truncated'] as const) {
      if (field[key] !== undefined && typeof field[key] !== 'boolean') fail();
    }
    previous = field.column;
    fields.push({
      column: field.column,
      name: field.name,
      headerHash: field.headerHash,
      headerLoaded: field.headerLoaded === true,
      headerUnique: field.headerUnique === true,
      truncated: field.truncated === true ? true : field.truncated === false ? false : undefined,
    });
  }
  const tableRef: TableRef = {
    book: ref.book,
    fingerprint: ref.fingerprint,
    source: ref.source,
    stamp: ref.stamp,
    headerRow: ref.headerRow,
  };
  return {
    ref: tableRef,
    name: info.name,
    sheet: info.sheet,
    kind: info.kind,
    format: typeof info.format === 'string' ? info.format : undefined,
    rows: info.rows,
    fields,
  };
}

export function validateRelationQuery(
  query: unknown,
  infos: readonly [CompositionSourceInfo, CompositionSourceInfo],
  limits: Pick<RelationLimits, 'keys' | 'columns'>,
): RelationQuery {
  if (!isRecord(query) || !isUnknownArray(query.keys) || query.keys.length < 1 || query.keys.length > limits.keys) {
    throw new Error(localize('ui.relationsModel.selectAssociatedKey'));
  }
  const leftColsRaw = query.leftColumns;
  const rightColsRaw = query.rightColumns;
  const leftColumns: number[] = [];
  const rightColumns: number[] = [];
  const validatedKeys: ReconcilePair[] = [];

  for (let side = 0; side < 2; side++) {
    const columnsRaw = side === 1 ? rightColsRaw : leftColsRaw;
    if (!isUnknownArray(columnsRaw) || columnsRaw.length > limits.columns) {
      throw new Error(localize('ui.relationsModel.resultFieldsExceedCapacity'));
    }
    const sideInfo = infos[side];
    if (!sideInfo) throw new Error(localize('ui.relationsModel.resultFieldsDoNotExist'));
    const available = new Set((sideInfo.fields ?? []).map((field) => field.column));
    const used = new Set<number>();
    const keys = new Set<number>();
    for (const column of columnsRaw) {
      if (typeof column !== 'number' || !available.has(column) || used.has(column)) {
        throw new Error(localize('ui.relationsModel.resultFieldsDoNotExist'));
      }
      used.add(column);
      if (side === 0) leftColumns.push(column);
      else rightColumns.push(column);
    }
    for (const pair of query.keys) {
      if (!isRecord(pair)) throw new Error(localize('ui.common.associatedKeysInvalidOrDuplicate'));
      const column = side === 1 ? pair.right : pair.left;
      const modeVal = pair.mode;
      if (
        typeof column !== 'number' ||
        !available.has(column) ||
        keys.has(column) ||
        (modeVal !== undefined && typeof modeVal !== 'string') ||
        !['', 'text', 'number'].includes(modeVal || '') ||
        (pair.trim !== undefined && typeof pair.trim !== 'boolean') ||
        (pair.ignoreCase !== undefined && typeof pair.ignoreCase !== 'boolean')
      ) {
        throw new Error(localize('ui.common.associatedKeysInvalidOrDuplicate'));
      }
      keys.add(column);
    }
  }
  if (!leftColumns.length && !rightColumns.length) throw new Error(localize('ui.relationsModel.selectResultField'));
  for (const pair of query.keys) {
    if (!isRecord(pair) || typeof pair.left !== 'number' || typeof pair.right !== 'number') {
      throw new Error(localize('ui.common.associatedKeysInvalidOrDuplicate'));
    }
    validatedKeys.push({
      left: pair.left,
      right: pair.right,
      mode: typeof pair.mode === 'string' ? pair.mode : '',
      trim: pair.trim === true,
      ignoreCase: pair.ignoreCase === true,
    });
  }
  return {
    keys: validatedKeys,
    leftColumns,
    rightColumns,
  };
}

function validateReconcileValue(raw: unknown): ReconcileValue {
  if (!isRecord(raw) || typeof raw.raw !== 'string' || typeof raw.exists !== 'boolean') fail();
  for (const prop of ['display', 'type', 'uncertain'] as const) {
    if (raw[prop] !== undefined && typeof raw[prop] !== 'string') fail();
  }
  for (const prop of ['missingCache', 'typeMissing', 'truncated'] as const) {
    if (raw[prop] !== undefined && typeof raw[prop] !== 'boolean') fail();
  }
  return {
    raw: raw.raw,
    display: typeof raw.display === 'string' ? raw.display : undefined,
    type: typeof raw.type === 'string' ? raw.type : undefined,
    exists: raw.exists,
    missingCache: typeof raw.missingCache === 'boolean' ? raw.missingCache : undefined,
    uncertain: typeof raw.uncertain === 'string' ? raw.uncertain : undefined,
    typeMissing: typeof raw.typeMissing === 'boolean' ? raw.typeMissing : undefined,
    truncated: typeof raw.truncated === 'boolean' ? raw.truncated : undefined,
  };
}

function validateRecordSide(
  raw: unknown,
  allowed: ReadonlySet<number>,
  maxRows: number,
): ReconcileRecord {
  if (
    !isRecord(raw) ||
    !integer(raw.position, 0, maxRows - 1) ||
    !integer(raw.number, 1, maxRows) ||
    !isRecord(raw.values)
  ) {
    fail();
  }
  const values: Record<string, ReconcileValue> = {};
  for (const [key, value] of Object.entries(raw.values)) {
    if (!/^[1-9]\d*$/.test(key) || !allowed.has(Number(key))) fail();
    values[key] = validateReconcileValue(value);
  }
  return {
    position: raw.position,
    number: raw.number,
    values,
  };
}

function validateSourceInfo(
  raw: unknown,
  original: CompositionSourceInfo,
  ref: ReconcileSourceRef,
  needed: ReadonlySet<number>,
  maxRows: number,
): ReconcileSourceInfo {
  if (!isRecord(raw)) fail();
  if (
    raw.id !== `${ref.book}/${ref.source}` ||
    raw.fingerprint !== original.ref.fingerprint ||
    typeof raw.stamp !== 'string' ||
    raw.stamp !== ref.stamp ||
    raw.headerRow !== ref.headerRow ||
    raw.kind !== original.kind ||
    (typeof raw.format === 'string' ? raw.format : '') !== (original.format || '') ||
    raw.sheet !== original.sheet ||
    raw.name !== original.name ||
    raw.total !== original.rows ||
    !integer(raw.total, 0, maxRows) ||
    !isUnknownArray(raw.fields)
  ) {
    fail();
  }
  const seen = new Set<number>();
  const fields: ReconcileField[] = [];
  for (const field of raw.fields) {
    if (
      !isRecord(field) ||
      typeof field.column !== 'number' ||
      !needed.has(field.column) ||
      seen.has(field.column) ||
      typeof field.name !== 'string'
    ) {
      fail();
    }
    seen.add(field.column);
    fields.push({
      column: field.column,
      name: field.name,
      rawName: typeof field.rawName === 'string' ? field.rawName : undefined,
      headerHash: typeof field.headerHash === 'string' ? field.headerHash : undefined,
      headerLoaded: field.headerLoaded === true,
      truncated: typeof field.truncated === 'boolean' ? field.truncated : undefined,
      uniqueHeader: typeof field.uniqueHeader === 'boolean' ? field.uniqueHeader : undefined,
    });
  }
  if (seen.size !== needed.size) fail();
  return {
    id: raw.id,
    name: raw.name,
    sheet: raw.sheet,
    kind: raw.kind,
    format: typeof raw.format === 'string' ? raw.format : undefined,
    notice: typeof raw.notice === 'string' ? raw.notice : undefined,
    fingerprint: raw.fingerprint,
    stamp: raw.stamp,
    headerRow: raw.headerRow,
    fields,
    total: raw.total,
  };
}

export function validateRelationResult(
  data: unknown,
  expected: RelationExpectedEnvelope,
  limits: RelationLimits,
  formats: Pick<FileFormatsRegistry, 'maxRows'>,
): RelationResultResponse {
  if (!isRecord(data)) fail();
  const outputModeInfo = typeof data.outputMode === 'string' ? relationMode(data.outputMode) : undefined;
  if (
    data.id !== expected.id ||
    data.workbookKey !== expected.identity.bookKey ||
    data.book !== expected.identity.book ||
    data.outputMode !== expected.mode ||
    !outputModeInfo ||
    data.mode !== expected.operation ||
    !isRecord(data.query) ||
    !isUnknownArray(data.query.keys) ||
    !isUnknownArray(data.query.leftColumns) ||
    !isUnknownArray(data.query.rightColumns)
  ) {
    fail();
  }
  const expectedParentId = expected.operation === 'prepare' ? data.id : expected.parentId;
  if (typeof data.parentId !== 'string' || data.parentId !== expectedParentId) fail();

  const query = validateRelationQuery(data.query, expected.infos, limits);
  if (queryKey(query) !== queryKey(expected.query)) fail();

  const leftRef = relationFieldRef(expected.infos[0]);
  const rightRef = relationFieldRef(expected.infos[1]);
  const leftNeeded = new Set([...query.leftColumns, ...query.keys.map((key) => key.left)]);
  const rightNeeded = new Set([...query.rightColumns, ...query.keys.map((key) => key.right)]);
  const leftSource = validateSourceInfo(
    data.left,
    expected.infos[0],
    leftRef,
    leftNeeded,
    formats.maxRows,
  );
  const rightSource = validateSourceInfo(
    data.right,
    expected.infos[1],
    rightRef,
    rightNeeded,
    formats.maxRows,
  );

  const rawCounts = data.counts;
  if (!isRecord(rawCounts)) fail();
  const maxSquared = BigInt(formats.maxRows) ** 2n;
  for (const key of COUNT_KEYS) {
    if (parseRelationCount(rawCounts[key]) > maxSquared) fail();
  }
  const counts: RelationCounts = {
    leftRecords: rawCounts.leftRecords as string,
    rightRecords: rawCounts.rightRecords as string,
    leftInvalid: rawCounts.leftInvalid as string,
    rightInvalid: rawCounts.rightInvalid as string,
    leftDuplicateGroups: rawCounts.leftDuplicateGroups as string,
    rightDuplicateGroups: rawCounts.rightDuplicateGroups as string,
    manyToManyGroups: rawCounts.manyToManyGroups as string,
    matchingGroups: rawCounts.matchingGroups as string,
    leftMatched: rawCounts.leftMatched as string,
    rightMatched: rawCounts.rightMatched as string,
    leftOnly: rawCounts.leftOnly as string,
    rightOnly: rawCounts.rightOnly as string,
    innerRows: rawCounts.innerRows as string,
    leftJoinRows: rawCounts.leftJoinRows as string,
  };
  if (
    parseRelationCount(counts.leftRecords) !== BigInt(leftSource.total) ||
    parseRelationCount(counts.rightRecords) !== BigInt(rightSource.total) ||
    parseRelationCount(counts.leftMatched) +
      parseRelationCount(counts.leftOnly) +
      parseRelationCount(counts.leftInvalid) !==
      parseRelationCount(counts.leftRecords) ||
    parseRelationCount(counts.rightMatched) +
      parseRelationCount(counts.rightOnly) +
      parseRelationCount(counts.rightInvalid) !==
      parseRelationCount(counts.rightRecords) ||
    parseRelationCount(counts.innerRows) +
      parseRelationCount(counts.leftOnly) +
      parseRelationCount(counts.leftInvalid) !==
      parseRelationCount(counts.leftJoinRows) ||
    parseRelationCount(counts.matchingGroups) > parseRelationCount(counts.leftMatched) ||
    parseRelationCount(counts.matchingGroups) > parseRelationCount(counts.rightMatched) ||
    parseRelationCount(counts.manyToManyGroups) > parseRelationCount(counts.leftDuplicateGroups) ||
    parseRelationCount(counts.manyToManyGroups) > parseRelationCount(counts.rightDuplicateGroups) ||
    parseRelationCount(counts.leftDuplicateGroups) * 2n > parseRelationCount(counts.leftRecords) ||
    parseRelationCount(counts.rightDuplicateGroups) * 2n > parseRelationCount(counts.rightRecords)
  ) {
    fail();
  }

  const rawPreview = data.preview;
  if (
    !isRecord(rawPreview) ||
    rawPreview.total !== relationTotal(counts, outputModeInfo.id) ||
    !isUnknownArray(rawPreview.rows) ||
    rawPreview.rows.length > limits.previewRows ||
    BigInt(rawPreview.rows.length) > parseRelationCount(rawPreview.total) ||
    typeof rawPreview.truncated !== 'boolean' ||
    rawPreview.truncated !== (BigInt(rawPreview.rows.length) < parseRelationCount(rawPreview.total))
  ) {
    fail();
  }

  const required = outputModeInfo.sides;
  const previous: [number, number] = [-1, -1];
  const validatedRows: RelationRow[] = [];

  for (let i = 0; i < rawPreview.rows.length; i++) {
    const row = rawPreview.rows[i];
    if (
      !isRecord(row) ||
      row.sequence !== String(i + 1) ||
      (row.status !== 'matched' && row.status !== 'unmatched' && row.status !== 'invalid_key') ||
      (row.reason !== undefined && typeof row.reason !== 'string')
    ) {
      fail();
    }
    const positions: [number, number] = [-1, -1];
    let leftRecord: ReconcileRecord | undefined;
    let rightRecord: ReconcileRecord | undefined;

    if (row.left !== undefined && row.left !== null) {
      if (!required.includes(0)) fail();
      leftRecord = validateRecordSide(row.left, leftNeeded, formats.maxRows);
      positions[0] = leftRecord.position;
    }
    if (row.right !== undefined && row.right !== null) {
      if (!required.includes(1)) fail();
      rightRecord = validateRecordSide(row.right, rightNeeded, formats.maxRows);
      positions[1] = rightRecord.position;
    }

    const rightCount = parseRelationCount(row.rightMatches);
    if (
      rightCount > parseRelationCount(counts.rightRecords) ||
      (positions[0] < 0 && positions[1] < 0) ||
      (row.status === 'matched' && (rightCount === 0n || !leftRecord)) ||
      (row.status !== 'matched' && (rightCount !== 0n || (Boolean(leftRecord) && Boolean(rightRecord)))) ||
      (row.status === 'invalid_key' && !row.reason)
    ) {
      fail();
    }
    const onlySide = required.length === 1 ? required[0] : undefined;
    if (
      (outputModeInfo.id === 'left' && (!leftRecord || (row.status === 'matched' && !rightRecord))) ||
      (outputModeInfo.id === 'inner' && (!leftRecord || !rightRecord || row.status !== 'matched')) ||
      (outputModeInfo.id === 'left_matched' && (!leftRecord || Boolean(rightRecord) || row.status !== 'matched')) ||
      (outputModeInfo.id.endsWith('_unmatched') && row.status !== 'unmatched') ||
      (outputModeInfo.id.endsWith('_invalid') && row.status !== 'invalid_key') ||
      (onlySide === 0 && !leftRecord) ||
      (onlySide === 1 && !rightRecord)
    ) {
      fail();
    }
    if (
      positions[0] < previous[0] ||
      (positions[0] === previous[0] && positions[1] <= previous[1])
    ) {
      fail();
    }
    previous[0] = positions[0];
    previous[1] = positions[1];
    validatedRows.push({
      sequence: row.sequence,
      left: leftRecord,
      right: rightRecord,
      status: row.status,
      reason: row.reason,
      rightMatches: row.rightMatches as string,
    });
  }

  if (new TextEncoder().encode(JSON.stringify(data)).length > limits.resultBytes) fail();

  const preview: RelationPreview = {
    rows: validatedRows,
    total: rawPreview.total,
    truncated: rawPreview.truncated,
  };
  return {
    id: data.id,
    mode: expected.operation,
    outputMode: outputModeInfo.id,
    parentId: data.parentId,
    workbookKey: data.workbookKey,
    book: data.book,
    left: leftSource,
    right: rightSource,
    query,
    counts,
    preview,
  };
}

export function relationColumns(data: RelationResultResponse): RelationColumnSpec[] {
  const modeInfo = relationMode(data.outputMode);
  if (!modeInfo) return [];
  const result: RelationColumnSpec[] = [];
  for (const side of modeInfo.sides) {
    const cols = side === 1 ? data.query.rightColumns : data.query.leftColumns;
    const fields = side === 1 ? data.right.fields : data.left.fields;
    for (const column of cols) {
      const field = fields.find((f) => f.column === column);
      if (field) result.push({ side, column, field });
    }
  }
  return result;
}

export function relationCell(
  value: ReconcileValue | null | undefined,
): (ConditionalCellLike & { readonly type: string; readonly cacheMissing: boolean }) | null {
  return value
    ? {
        raw: value.raw,
        display: value.display ?? value.raw,
        type: value.type || '',
        cacheMissing: Boolean(value.missingCache),
      }
    : null;
}

export const RelationsModel = Object.freeze({
  copy: copyValue,
  modes: RELATION_MODES,
  mode: relationMode,
  count: parseRelationCount,
  total: relationTotal,
  formatCount: formatRelationCount,
  fieldRef: relationFieldRef,
  validateLimits: validateRelationLimits,
  validateFields: validateRelationFields,
  validateQuery: validateRelationQuery,
  validate: validateRelationResult,
  columns: relationColumns,
  cell: relationCell,
});
