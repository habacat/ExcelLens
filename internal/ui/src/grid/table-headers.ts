/**
 * TXT/CSV table header presentation model. Source records and row numbers remain
 * intact; the sticky column strip retains field names independently of row tiles.
 */
import { t as localize } from '../core/i18n.ts';
import type { AutoFilterState, TextTableHeader } from '../contracts/workbook.ts';

export function isTableHeader(config: AutoFilterState | null | undefined): config is AutoFilterState {
  return Boolean(config?.table && config.row > 0);
}

export function tableHeaderFromSource(
  header: TextTableHeader | null | undefined,
): AutoFilterState | null {
  if (
    !header ||
    !Number.isInteger(header.row) ||
    header.row < 1 ||
    !Array.isArray(header.columns) ||
    !header.columns.length
  ) {
    return null;
  }
  const names = header.columns.slice(0, 16384).map((value) => String(value));
  return {
    row: header.row,
    startCol: 1,
    endCol: names.length,
    names,
    table: true,
    detected: header.detected,
    rules: new Map(),
    sortColumn: 0,
    descending: false,
  };
}

export function tableHeaderColumnName(
  config: AutoFilterState | null | undefined,
  col: number,
  colName: (col: number) => string,
): string {
  if (!isTableHeader(config) || col < config.startCol || col > config.endCol) return '';
  return config.names?.[col - 1] || localize('ui.tableHeaders.column', { colName: colName(col) });
}

export function useStickyTableLabels(
  config: AutoFilterState | null | undefined,
  scrollTop: number,
  headerBottom: number,
  frozenRows = 0,
): boolean {
  return isTableHeader(config) && config.row > frozenRows && scrollTop >= headerBottom;
}

export function decorateTableHeader(
  head: HTMLElement,
  col: number,
  config: AutoFilterState | null | undefined,
  colName: (col: number) => string,
  ready: boolean,
): boolean {
  const name = tableHeaderColumnName(config, col, colName);
  if (!name || !config) return false;
  head.classList.add('table-column-head');
  head.title = localize('ui.tableHeaders.headerRow', { colName: colName(col), name, row: config.row });
  const letter = document.createElement('span');
  letter.className = 'table-column-letter';
  letter.textContent = colName(col);
  letter.setAttribute('aria-hidden', 'true');
  const label = document.createElement('span');
  label.className = 'table-column-name';
  label.textContent = name;
  const arrow = document.createElement('button');
  arrow.type = 'button';
  arrow.className = `auto-filter-button${config.rules.has(col) ? ' is-filtered' : ''}`;
  arrow.dataset.column = String(col);
  arrow.textContent = '▾';
  arrow.disabled = !ready;
  arrow.setAttribute('aria-label', localize('ui.tableHeaders.filteringSorting', { name }));
  arrow.title = ready ? localize('ui.tableHeaders.filteringSorting', { name }) : localize('ui.tableHeaders.completeIndexCanBeFiltered');
  head.append(letter, label, arrow);
  return true;
}
