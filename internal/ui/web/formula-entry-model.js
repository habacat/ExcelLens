(() => {
  'use strict';

  // Only separators outside literals, sheet names, table selectors and array
  // constants advance a function argument. Caret positions are DOM UTF-16 offsets.
  function context(text, caret) {
    if (!text.trimStart().startsWith('=')) return null;
    const stack = [];
    let quote = '', word = '', brackets = 0, braces = 0;
    const before = text.slice(0, caret);
    for (let i = 0; i < before.length; i++) {
      const c = before[i];
      if (quote) {
        if (c === quote) {
          if (before[i + 1] === quote) i++;
          else quote = '';
        }
        continue;
      }
      if (brackets) {
        if (c === '[') brackets++;
        else if (c === ']') brackets--;
        continue;
      }
      if (c === '"' || c === "'") quote = c;
      else if (c === '[') brackets++;
      else if (c === '{') braces++;
      else if (c === '}') braces--;
      else if (/[A-Za-z_]/.test(c) || word && /[\d.]/.test(c)) { word += c; continue; }
      else if (c === '(') stack.push({fn:word.toUpperCase(), arg:0, braces});
      else if (c === ')') stack.pop();
      else if (c === ',' && stack.length && braces === stack.at(-1).braces) stack.at(-1).arg++;
      word = '';
    }
    if (quote || brackets) return null;
    const token = /[\p{L}_\\][\p{L}\p{N}_.]*$/u.exec(before);
    const prefix = token && !/[\p{L}\p{N}_.$!:]$/u.test(before.slice(0, token.index).trimEnd()) ? token[0] : '';
    const enclosing = stack.slice().reverse().find(frame => frame.fn);
    return {prefix, start:caret - prefix.length, end:caret, fn:enclosing?.fn || '', arg:enclosing?.arg || 0,
      operand:!braces && /[=+\-*/^&(<>,:]\s*$/.test(before)};
  }

  function close(text) {
    let depth = 0, quote = '', brackets = 0;
    for (let i = 0; i < text.length; i++) {
      const c = text[i];
      if (quote) {
        if (c === quote) {
          if (text[i+1] === quote) i++;
          else quote = '';
        }
      } else if (brackets) {
        if (c === '[') brackets++;
        else if (c === ']') brackets--;
      } else if (c === '"' || c === "'") quote = c;
      else if (c === '[') brackets++;
      else if (c === '(') depth++;
      else if (c === ')' && --depth < 0) return text;
    }
    return !quote && !brackets && depth > 0 && depth <= 40 ? text + ')'.repeat(depth) : text;
  }

  function sheetPrefix(sheet, origin) {
    if (sheet === origin) return '';
    if (/^[\p{L}_][\p{L}\p{N}_.]*$/u.test(sheet) && !/^(?:[a-z]{1,3}\d+|r\d*c\d*|[rc])$/i.test(sheet)) return sheet + '!';
    return "'" + sheet.replaceAll("'", "''") + "'!";
  }

  function reference(a, b, sheet, origin, previous = '') {
    const name = window.ExcelLensAddress.columnName;
    const prefix = sheetPrefix(sheet, origin);
    const locks = previous.slice(previous.lastIndexOf('!')+1).split(':');
    if (a.kind === 'column' || a.kind === 'row') {
      const column = a.kind === 'column';
      const low = Math.min(column ? a.col : a.row, column ? b.col : b.row);
      const high = Math.max(column ? a.col : a.row, column ? b.col : b.row);
      const axis = (n, lock) => (lock.startsWith('$') ? '$' : '') + (column ? name(n) : n);
      return prefix + axis(low,locks[0]) + ':' + axis(high,locks[1] || locks[0]);
    }
    const address = (col,row,pattern) => {
      const axes = /^(\$?)[A-Z]+(\$?)\d+$/i.exec(pattern);
      return (axes?.[1] || '') + name(col) + (axes?.[2] || '') + row;
    };
    const first = address(Math.min(a.col,b.col), Math.min(a.row,b.row), locks[0]);
    const last = address(Math.max(a.col,b.col), Math.max(a.row,b.row), locks[1] || locks[0]);
    return prefix + first + (first === last ? '' : ':' + last);
  }

  function absolute(text, start, end, origin) {
    const ref = window.ExcelLensFormulaInspector.parseFormula(text, origin).find(r =>
      !r.external && r.startIndex <= start && end <= r.endIndex);
    if (!ref) return null;
    const qualifier = ref.label.lastIndexOf('!') + 1;
    const body = ref.label.slice(qualifier);
    const atoms = body.split(':');
    const offset = ref.startIndex + qualifier;
    if (start < offset && start !== ref.startIndex) return null;
    // A selected/pointed rectangle cycles as a unit. A typed range under the
    // caret cycles just its active endpoint, as in the native formula editor.
    let from = offset, until = ref.endIndex, value = body;
    if (atoms.length === 2 && start === end) {
      const split = offset + atoms[0].length;
      if (start <= split) { until = split; value = atoms[0]; }
      else { from = split + 1; value = atoms[1]; }
    }
    if (ref.kind === 'row' || ref.kind === 'column') {
      const axis = value.split(':').map(v => v.startsWith('$') ? v.slice(1) : '$' + v);
      return {from,until,value:axis.join(':')};
    }
    const cycle = (_, colLock, col, rowLock, row) => {
      if (!colLock && !rowLock) return '$' + col + '$' + row;
      if (colLock && rowLock) return col + '$' + row;
      if (rowLock) return '$' + col + row;
      return col + row;
    };
    return {from, until, value:value.replace(/(\$?)([A-Za-z]+)(\$?)(\d+)/g, cycle)};
  }


  // Leave quoted text, sheet names and structured field captions intact.
  function display(text) {
    if(!text || !/_(?:xlfn|xlpm)\./i.test(text))return text;
    const hasLet=/_xlfn\.(?:LET|LAMBDA)\s*\(/i.test(text);
    let out='',quote='',brackets=0;
    const token=/_(xlfn\.(?:_xlws\.)?|xlpm\.)([\p{L}_][\p{L}\p{N}_.]*)/iuy;
    for(let i=0;i<text.length;){
      const c=text[i];
      if(quote){out+=c;i++;if(c===quote){if(text[i]===quote){out+=text[i++];}else quote='';}continue;}
      if(brackets){out+=c;i++;if(c==="'"&&i<text.length)out+=text[i++];else if(c==='[')brackets++;else if(c===']')brackets--;continue;}
      if(c==='"'||c==="'")quote=c;
      else if(c==='[')brackets++;
      else if(c==='_' && (i===0||!/[\p{L}\p{N}_.]/u.test(text[i-1]))){
        token.lastIndex=i;const m=token.exec(text);
        if(m){
          const local=m[1].toLowerCase()==='xlpm.';
          // SINGLE and ANCHORARRAY encode operators. Leave these intact
          // until a full reference expression can be rendered losslessly.
          if(local?hasLet:(/^\s*\(/.test(text.slice(token.lastIndex)) && !/^(SINGLE|ANCHORARRAY)$/i.test(m[2]))){
            out+=m[2];i=token.lastIndex;continue;
          }
        }
      }
      out+=c;i++;
    }
    return out;
  }

  // Track nested structured selectors independently of quoted sheet names.
  // A closing bracket can itself be part of an escaped column caption.
  function columnContext(text,caret){
    if(!text.trimStart().startsWith('='))return null;
    const stack=[];let quote='',table='';
    for(let i=0;i<caret;i++){
      const c=text[i];
      if(quote){if(c===quote){if(text[i+1]===quote)i++;else quote='';}continue;}
      if(stack.length && c==="'"){i++;continue;}
      if(!stack.length&&(c==='"'||c==="'")){quote=c;continue;}
      if(c==='['){
        if(!stack.length)table=/[\p{L}_][\p{L}\p{N}_.]*$/u.exec(text.slice(0,i))?.[0]||'';
        stack.push(i+1);
      }else if(c===']'&&stack.length)stack.pop();
    }
    if(!stack.length)return null;
    let start=stack.at(-1);if(text[start]==='@')start++;
    const raw=text.slice(start,caret);
    if(raw.startsWith('#') || raw.includes('],'))return null;
    let end=caret;
    while(end<text.length){
      const c=text[end];
      if(c==="'" && /['#@\[\]]/.test(text[end+1]||' ')){end+=2;continue;}
      if(c===']'){end++;break;}
      if(c==='['||c===')')break;
      end++;
    }
    const tail=text.slice(end);
    const alreadyClosed=/^\]*/.exec(tail)[0].length;
    const closures=/^[,:]/.test(tail)?1:Math.max(1,stack.length-alreadyClosed);
    return {table,start,end,closures,prefix:raw.replace(/'(['#@\[\]])/g,'$1')};
  }
  window.ExcelLensFormulaInput = {context,close,reference,absolute,display,columnContext};
})();
