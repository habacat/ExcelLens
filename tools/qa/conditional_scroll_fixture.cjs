// Deterministic complex conditional-formatting workbook covering all cost-based
// evaluation strategies (direct, short viewport cache, sliding window, shared
// aggregate, disk prefix sum, disk group COUNTIF, and disk MATCH) alongside dense
// source fills/borders for composited pixel coverage verification.
'use strict';

const { zip } = require('./generated_workbook.cjs');

const baseColors = ['C5DCEF', 'E7C1AF', 'BADABE', 'F1D49B', 'CBBFE2', '9ECFD1', 'D5CBAA'];
const cfColors = [
  'F4B6C2', // dxf 0: direct cellIs (col A)
  'A8D8B9', // dxf 1: short 3-row SUM($C2:$C4)=6 -> viewport cache (col B)
  'FCE38A', // dxf 2: 24-row SUM($C2:$C25)>=36 -> sliding window (col C)
  'B5D3E7', // dxf 3: fixed large SUM($C$2:$C$N)>100 -> shared aggregate (col D)
  'D4B5E7', // dxf 4: dynamic prefix SUM($C$2:$C2)>20 -> disk prefix (col E)
  'F6C89F', // dxf 5: COUNTIF($A:$A,$A2)>1 -> disk group (col F)
  '9BE3DE', // dxf 6: MATCH($A2,$A:$A,0)<ROW() -> disk match (col G)
];
const border = '48647A';

function column(n) {
  let s = '';
  while (n) {
    n--;
    s = String.fromCharCode(65 + (n % 26)) + s;
    n = Math.floor(n / 26);
  }
  return s;
}

function expectedCellText(r, c) {
  if (r === 1) return `H${c}`;
  if (c === 1) return `K${String((r - 2) % 400).padStart(4, '0')}`;
  if (c === 2) return String(r);
  if (c === 3) return String((r % 3) + 1); // 1, 2, 3 repeating -> every 3-row sum = 6!
  if (c === 4) return String(r * 10);
  if (c === 5) return String(r * 2);
  if (c === 6) return `G${(r - 2) % 400}`;
  if (c === 7) return `M${(r - 2) % 400}`;
  return String(r * 1000 + c);
}

function complexConditionalFixture(rowCount = 2400, colCount = 48) {
  const ns = 'http://schemas.openxmlformats.org/spreadsheetml/2006/main';
  const rel = 'http://schemas.openxmlformats.org/officeDocument/2006/relationships';
  const rows = [];
  const headerCells = [];
  for (let c = 1; c <= colCount; c++) {
    headerCells.push(`<c r="${column(c)}1" s="1" t="inlineStr"><is><t>H${c}</t></is></c>`);
  }
  rows.push(`<row r="1">${headerCells.join('')}</row>`);

  for (let r = 2; r <= rowCount; r++) {
    const cells = [];
    const keyIdx = (r - 2) % 400;
    const key = `K${String(keyIdx).padStart(4, '0')}`;
    const cVal = (r % 3) + 1;
    const sBase = 1 + ((r * 3 + 5) % baseColors.length);
    cells.push(`<c r="A${r}" s="${sBase}" t="inlineStr"><is><t>${key}</t></is></c>`);
    cells.push(`<c r="B${r}" s="${1 + ((r * 3 + 10) % baseColors.length)}"><v>${r}</v></c>`);
    cells.push(`<c r="C${r}" s="${1 + ((r * 3 + 15) % baseColors.length)}"><v>${cVal}</v></c>`);
    cells.push(`<c r="D${r}" s="${1 + ((r * 3 + 20) % baseColors.length)}"><v>${r * 10}</v></c>`);
    cells.push(`<c r="E${r}" s="${1 + ((r * 3 + 25) % baseColors.length)}"><v>${r * 2}</v></c>`);
    cells.push(`<c r="F${r}" s="${1 + ((r * 3 + 30) % baseColors.length)}" t="inlineStr"><is><t>G${keyIdx}</t></is></c>`);
    cells.push(`<c r="G${r}" s="${1 + ((r * 3 + 35) % baseColors.length)}" t="inlineStr"><is><t>M${keyIdx}</t></is></c>`);
    for (let c = 8; c <= colCount; c++) {
      const s = 1 + ((r * 3 + c * 5 + Math.floor(r / 11)) % baseColors.length);
      const val = r * 1000 + c;
      cells.push(`<c r="${column(c)}${r}" s="${s}"><v>${val}</v></c>`);
    }
    rows.push(`<row r="${r}">${cells.join('')}</row>`);
  }

  const fills = baseColors
    .map(color => `<fill><patternFill patternType="solid"><fgColor rgb="FF${color}"/></patternFill></fill>`)
    .join('');
  const borders = ['thin', 'medium', 'dashed', 'double']
    .map(type => '<border>' + ['left', 'right', 'top', 'bottom'].map(side => `<${side} style="${type}"><color rgb="FF${border}"/></${side}>`).join('') + '</border>')
    .join('');
  const formats = baseColors
    .map((_, i) => `<xf numFmtId="0" fontId="0" fillId="${i + 2}" borderId="${(i % 4) + 1}" applyFill="1" applyBorder="1"/>`)
    .join('');
  const dxfs = cfColors
    .map(color => `<dxf><fill><patternFill patternType="solid"><fgColor rgb="FF${color}"/></patternFill></fill></dxf>`)
    .join('');

  const cfXml = [
    `<conditionalFormatting sqref="A2:A${rowCount}"><cfRule type="expression" priority="1" dxfId="0"><formula>MOD(ROW(),2)=0</formula></cfRule></conditionalFormatting>`,
    `<conditionalFormatting sqref="B2:B${rowCount}"><cfRule type="expression" priority="2" dxfId="1"><formula>SUM($C2:$C4)=6</formula></cfRule></conditionalFormatting>`,
    `<conditionalFormatting sqref="C2:C${rowCount}"><cfRule type="expression" priority="3" dxfId="2"><formula>SUM($C2:$C25)&gt;=36</formula></cfRule></conditionalFormatting>`,
    `<conditionalFormatting sqref="D2:D${rowCount}"><cfRule type="expression" priority="4" dxfId="3"><formula>SUM($C$2:$C$${rowCount})&gt;100</formula></cfRule></conditionalFormatting>`,
    `<conditionalFormatting sqref="E2:E${rowCount}"><cfRule type="expression" priority="5" dxfId="4"><formula>SUM($C$2:$C2)&gt;20</formula></cfRule></conditionalFormatting>`,
    `<conditionalFormatting sqref="F2:F${rowCount}"><cfRule type="expression" priority="6" dxfId="5"><formula>COUNTIF($A:$A,$A2)&gt;1</formula></cfRule></conditionalFormatting>`,
    `<conditionalFormatting sqref="G2:G${rowCount}"><cfRule type="expression" priority="7" dxfId="6"><formula>MATCH($A2,$A:$A,0)&lt;ROW()</formula></cfRule></conditionalFormatting>`,
  ].join('');

  const lastCol = column(colCount);
  return zip({
    '[Content_Types].xml': '<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/><Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/></Types>',
    '_rels/.rels': `<Relationships xmlns="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><Relationship Id="book" Type="${rel}/officeDocument" Target="xl/workbook.xml"/></Relationships>`,
    'xl/workbook.xml': `<workbook xmlns="${ns}" xmlns:r="${rel}"><sheets><sheet name="ComplexCF" sheetId="1" r:id="s1"/></sheets></workbook>`,
    'xl/_rels/workbook.xml.rels': `<Relationships xmlns="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><Relationship Id="s1" Type="${rel}/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="styles" Type="${rel}/styles" Target="styles.xml"/></Relationships>`,
    'xl/styles.xml': `<styleSheet xmlns="${ns}"><fonts count="1"><font><sz val="11"/><name val="Calibri"/></font></fonts><fills count="${baseColors.length + 2}"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill>${fills}</fills><borders count="5"><border/>${borders}</borders><cellStyleXfs count="1"><xf/></cellStyleXfs><cellXfs count="${baseColors.length + 1}"><xf/>${formats}</cellXfs><dxfs count="${cfColors.length}">${dxfs}</dxfs></styleSheet>`,
    'xl/worksheets/sheet1.xml': `<worksheet xmlns="${ns}"><dimension ref="A1:${lastCol}${rowCount}"/><sheetFormatPr defaultRowHeight="18"/><cols><col min="1" max="${colCount}" width="12" customWidth="1"/></cols><sheetData>${rows.join('')}</sheetData>${cfXml}</worksheet>`,
  });
}

module.exports = {
  complexConditionalFixture,
  baseColors,
  cfColors,
  border,
  expectedCellText,
};
