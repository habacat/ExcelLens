(() => {
  'use strict';
  // Labels are shared by rendering, editing, printing and analysis. Source
  // limits are supplied by the caller; the cache only limits retained memory.
  const labels = new Map();
  function columnName(column) {
    if (!Number.isSafeInteger(column) || column < 1) return '';
    if (labels.has(column)) return labels.get(column);
    let text = '';
    for (let n = column; n > 0; n = Math.floor((n - 1) / 26)) {
      text = String.fromCharCode(65 + (n - 1) % 26) + text;
    }
    if (labels.size < 16384) labels.set(column, text);
    return text;
  }
  function columnNumber(letters) {
    if (!/^[A-Z]+$/i.test(letters)) return 0;
    let value = 0;
    for (const ch of letters.toUpperCase()) value = value * 26 + ch.charCodeAt(0) - 64;
    return Number.isSafeInteger(value) ? value : 0;
  }
  function parseColumn(value, maxColumns) {
    const text = String(value || '').trim().replaceAll('$', '');
    const col = /^\d+$/.test(text) ? Number(text) : columnNumber(text);
    return Number.isSafeInteger(col) && col > 0 && col <= maxColumns ? col : 0;
  }
  function parseRef(value, maxRows, maxColumns) {
    const text = String(value || '').trim().replaceAll('$', '');
    const match = /^([A-Za-z]+)([1-9]\d*)$/.exec(text);
    if (!match) return null;
    const col = parseColumn(match[1], maxColumns), row = Number(match[2]);
    if (!col || !Number.isSafeInteger(row) || row > maxRows) return null;
    return {col, row, ref: columnName(col) + row};
  }
  window.ExcelLensAddress = {columnName, columnNumber, parseColumn, parseRef};
})();
