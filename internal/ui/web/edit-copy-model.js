(() => {
  'use strict';
  const utf8 = new TextEncoder();
  const bytes = value => utf8.encode(value).length;

  function target(ref, sheet) {
    const hit = /^([A-Z]{1,3})([1-9][0-9]{0,8})$/.exec(ref.trim().toUpperCase());
    if (!sheet || !hit) throw Error('输入有效的源单元格地址');
    let col = 0;
    for (const char of hit[1]) col = col * 26 + char.charCodeAt(0) - 64;
    const row = Number(hit[2]);
    if (row < sheet.firstRow || row > sheet.rows || col < sheet.firstCol || col > sheet.cols) throw Error('地址超出可编辑区域');
    return {sheet: sheet.name, row, col};
  }

  function encode(text, escaped) {
    return escaped ? text.replaceAll('\\', '\\\\').replaceAll('\r', '\\r') : text;
  }

  function decode(text, escaped) {
    if (!escaped) return text;
    return text.replace(/\\(?:[\\r]|[\s\S]|$)/g, part => {
      if (part === '\\r') return '\r';
      if (part === '\\\\') return '\\';
      throw Error('回车输入使用 \\r，反斜线输入使用 \\\\');
    });
  }

  function value(kind, raw, limits) {
    if (kind === 'blank' || kind === 'text' && raw === '') return {kind: 'blank', raw: ''};
    if (bytes(raw) > limits.cellBytes) throw Error('单元格原文超出编辑上限');
    if (kind === 'boolean') {
      raw = raw.toUpperCase();
      if (raw !== 'TRUE' && raw !== 'FALSE') throw Error('布尔值请输入 TRUE 或 FALSE');
    }
    if (kind === 'number' && !/^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[Ee][+-]?[0-9]+)?$/.test(raw)) throw Error('输入有效的十进制数值');
    if (kind === 'formula') {
      raw = raw.trim();
      if (!raw.startsWith('=') || !raw.slice(1).trim()) throw Error('公式请以 = 开头');
      raw = '=' + raw.slice(1).trim();
      if (bytes(raw) > limits.formulaBytes) throw Error('公式原文超出编辑上限');
    }
    return {kind, raw};
  }

  function bounded(changes, limits) {
    let size = 514;
    const result = [];
    for (const change of changes) {
      size += bytes(JSON.stringify(change)) + (result.length ? 1 : 0);
      if (result.length === limits.batchCells || size > limits.requestBytes) throw Error('区域超出一次编辑容量，请缩小范围');
      result.push(change);
    }
    if (!result.length) throw Error('没有可提交的单元格');
    return result;
  }

  function paste(text, first, sheet, skipBlank, limits, parse) {
    if (text === '') throw Error('请先粘贴表格文本');
    const rows = parse(text, {maxRows: limits.batchCells, maxCols: sheet.cols - first.col + 1,
      maxCells: limits.batchCells, maxCellLength: limits.cellBytes, maxTextLength: limits.requestBytes});
    if (first.row + rows.length - 1 > sheet.rows) throw Error('粘贴范围超出可编辑区域');
    const changes = [];
    rows.forEach((row, i) => row.forEach((text, j) => {
      if (text !== '' || !skipBlank) changes.push({...first, row: first.row + i, col: first.col + j, value: value('text', text, limits)});
    }));
    return {changes: bounded(changes, limits), last: {...first, row: first.row + rows.length - 1, col: first.col + Math.max(...rows.map(row => row.length)) - 1}};
  }

  function decimal(text) {
    const hit = /^([+-]?)([0-9]*)(?:\.([0-9]*))?(?:[Ee]([+-]?[0-9]+))?$/.exec(text);
    if (!hit || !(hit[2] || hit[3]) || text.length > 80) throw Error('起始值和步长必须是十进制数');
    const scale = (hit[3] || '').length - Number(hit[4] || 0);
    if (!Number.isSafeInteger(scale) || Math.abs(scale) > 40) throw Error('序列小数位数或指数超出范围');
    return {number: BigInt((hit[1] || '+') + hit[2] + (hit[3] || '')), scale};
  }

  function numberText(n, scale) {
    const digits = (n < 0n ? -n : n).toString().padStart(scale + 1, '0');
    return (n < 0n ? '-' : '') + (scale ? `${digits.slice(0, -scale)}.${digits.slice(-scale)}` : digits);
  }

  function fill(first, last, start, step, numeric, asText, limits) {
    const deltaRow = Math.sign(last.row - first.row), deltaCol = Math.sign(last.col - first.col);
    const count = Math.abs(last.row - first.row) + Math.abs(last.col - first.col);
    if (deltaRow && deltaCol || !count || count > limits.batchCells) throw Error('请选择同列或同行的填充终点');
    const changes = [];
    if (numeric) {
      const a = decimal(start.raw), b = decimal(step), scale = Math.max(0, a.scale, b.scale);
      const base = a.number * 10n ** BigInt(scale - a.scale), advance = b.number * 10n ** BigInt(scale - b.scale);
      for (let i = 1; i <= count; i++) changes.push({...first, row: first.row + deltaRow * i, col: first.col + deltaCol * i,
        value: value(asText ? 'text' : 'number', numberText(base + advance * BigInt(i), scale), limits)});
    } else {
      if (!['blank', 'text', 'number', 'boolean'].includes(start.kind)) throw Error('当前值不可用于填充');
      for (let i = 1; i <= count; i++) changes.push({...first, row: first.row + deltaRow * i, col: first.col + deltaCol * i, value: start});
    }
    return {changes: bounded(changes, limits), last};
  }

  function gridValue(raw, kind, original, textFile, limits) {
    if (textFile) return value('text', raw, limits);
    if (kind !== 'auto') return value(kind, raw, limits);
    if (raw.startsWith("'")) return value('text', raw.slice(1), limits);
    if (raw.startsWith('=')) return value('formula', raw, limits);
    if (original === 'date' && /^\d{4}-\d{2}-\d{2}(?:T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})?)?$/.test(raw)) return value('date', raw, limits);
    if (original === 'text') return value('text', raw, limits);
    if (/^(?:true|false)$/i.test(raw)) return value('boolean', raw, limits);
    const number = /^[+-]?((?:\d+(?:\.\d*)?|\.\d+))(?:e[+-]?\d+)?$/i.exec(raw);
    if (number && !/^0\d/.test(number[1]) && number[1].replace('.', '').replace(/^0+/, '').length <= 15) return value('number', raw, limits);
    return value('text', raw, limits);
  }

  window.ExcelLensEditModel = {target, encode, decode, value, gridValue, bounded, paste, fill};
})();
