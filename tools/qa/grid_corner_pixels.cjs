// Regression for the pixel where independently owned right/bottom strokes meet.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { createHash } = require('node:crypto');
const { launch } = require('./browser_harness.cjs');
const { zip } = require('./generated_workbook.cjs');

function workbook() {
  const ns = 'http://schemas.openxmlformats.org/spreadsheetml/2006/main';
  const rel = 'http://schemas.openxmlformats.org/officeDocument/2006/relationships';
  const pkg = 'http://schemas.openxmlformats.org/package/2006/relationships';
  const styles = { B2: 1, A3: 2, B3: 3, A4: 6, E5: 1, D6: 2, E6: 3, A7: 1, A8: 4,
    C10: 1, D10: 1, B11: 2, B12: 2, C11: 3, F13: 5, G12: 1, G13: 3 };
  const rows = [];
  for (let row = 1; row <= 16; row++) {
    const cells = [];
    for (const col of 'ABCDEFGHI') {
      const ref = col + row;
      const style = styles[ref];
      if (style) cells.push('<c r="' + ref + '" s="' + style + '"><v>27</v></c>');
      else if (col === 'I') cells.push('<c r="' + ref + '"><v>' + row + '</v></c>');
    }
    rows.push('<row r="' + row + '" ht="24" customHeight="1">' + cells.join('') + '</row>');
  }
  const edge = (side, type = 'thick') => '<' + side + ' style="' + type + '"><color rgb="FF000000"/></' + side + '>';
  const borders = ['', edge('bottom'), edge('right'), edge('left'), edge('right', 'thin'), edge('left') + edge('top')].map(b => '<border>' + b + '</border>').join('');
  const formats = [[0,0],[1,0],[2,0],[0,2],[3,2],[4,0],[5,2]].map(([border,fill]) =>
    '<xf borderId="' + border + '" fillId="' + fill + '" applyBorder="1" applyFill="1"/>').join('');
  return zip({
    '[Content_Types].xml': '<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/><Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/></Types>',
    '_rels/.rels': '<Relationships xmlns="' + pkg + '"><Relationship Id="book" Type="' + rel + '/officeDocument" Target="xl/workbook.xml"/></Relationships>',
    'xl/workbook.xml': '<workbook xmlns="' + ns + '" xmlns:r="' + rel + '"><sheets><sheet name="Corners" sheetId="1" r:id="s1"/></sheets></workbook>',
    'xl/_rels/workbook.xml.rels': '<Relationships xmlns="' + pkg + '"><Relationship Id="s1" Type="' + rel + '/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="styles" Type="' + rel + '/styles" Target="styles.xml"/></Relationships>',
    'xl/styles.xml': '<styleSheet xmlns="' + ns + '"><fonts count="1"><font><sz val="11"/><name val="Calibri"/></font></fonts><fills count="3"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill><fill><patternFill patternType="solid"><fgColor rgb="FFFFC000"/><bgColor indexed="64"/></patternFill></fill></fills><borders count="6">' + borders + '</borders><cellStyleXfs count="1"><xf/></cellStyleXfs><cellXfs count="7">' + formats + '</cellXfs></styleSheet>',
    'xl/worksheets/sheet1.xml': '<worksheet xmlns="' + ns + '"><dimension ref="A1:I16"/><cols><col min="1" max="9" width="12" customWidth="1"/></cols><sheetData>' + rows.join('') + '</sheetData><mergeCells count="1"><mergeCell ref="C11:D12"/></mergeCells></worksheet>',
  });
}

async function measure(png, refs) {
  const img = new Image();
  img.src = 'data:image/png;base64,' + png;
  await img.decode();
  const canvas = new OffscreenCanvas(img.width, img.height);
  const context = canvas.getContext('2d');
  context.drawImage(img, 0, 0);
  const { data } = context.getImageData(0, 0, img.width, img.height);
  const scale = img.width / innerWidth;
  function hasInk(x, y) {
    const i = 4 * (y * img.width + x);
    // The fractional-DPI compositor can antialias the thinnest source stroke.
    // The threshold still excludes the neutral gridline and the yellow fill.
    const high = Math.max(data[i], data[i+1], data[i+2]);
    const low = Math.min(data[i], data[i+1], data[i+2]);
    return high < 180 && high - low <= 12;
  }
  return refs.map(([ref,pane]) => {
    const cell = document.querySelector(pane + ' [data-ref="' + ref + '"]');
    if (!cell) throw Error('Missing ' + ref + ' in ' + pane);
    const box = cell.getBoundingClientRect();
    const x = Math.round(box.left * scale), y = Math.round(box.top * scale);
    const east = x + Math.round(box.width * scale / 2);
    const south = y + Math.round(box.height * scale * .76);
    const columns = [], rows = [];
    const search = Math.ceil(6 * scale);
    for (let i = -search; i <= 1; i++) {
      if (hasInk(x+i, south)) columns.push(x+i);
      if (hasInk(east, y+i)) rows.push(y+i);
    }
    const missing = columns.flatMap(cx => rows.filter(ry => !hasInk(cx, ry)).map(ry => [cx-x,ry-y]));
    return { ref, pane, columns: columns.length, rows: rows.length, missing };
  });
}

async function main() {
  const before = process.argv.includes('--before');
  const receipt = process.env.QA_CORNER_RECEIPT;
  const errors = [];
  const h = await launch({ errors });
  try {
    const source = path.join(h.storage, 'corners.xlsx');
    fs.writeFileSync(source, workbook());
    const sha = () => createHash('sha256').update(fs.readFileSync(source)).digest('hex');
    const digest = sha();
    const p = h.page;
    await p.file('#fileInput', source);
    await p.wait('window.ExcelLensPerformance().hasVisibleCoverage && !window.ExcelLensPerformance().sheetLoading');
    await p.run(() => {
      if (crosshairToggle.checked) crosshairToggle.click();
      grid.scrollTop = 0;
      grid.scrollLeft = 0;
    });
    const checks = [];
    for (const frozen of [false, true]) {
      if (frozen) {
        await p.click('#freezeBtn');
        await p.run(() => { freezeRowsInput.value = '5'; freezeColsInput.value = '4'; });
        await p.click('#freezeApplyBtn');
        await p.wait('!!document.querySelector("#freezeCorner [data-ref=B3]")');
      }
      const refs = frozen
        ? [['B3','#freezeCorner'],['A4','#freezeCorner'],['A8','#freezeLeft'],['E6','#rowsLayer'],['G13','#rowsLayer']]
        : ['B3','A4','A8','E6','C11','G13'].map(ref => [ref,'#rowsLayer']);
      for (const ratio of before ? [1] : [1,1.25,1.5,2]) {
        await p.call('Emulation.setDeviceMetricsOverride',{ width:1360,height:900,deviceScaleFactor:ratio,mobile:false });
        await p.evaluate('new Promise(resolve => setTimeout(() => requestAnimationFrame(() => requestAnimationFrame(resolve)), 180))');
        const { data } = await p.call('Page.captureScreenshot',{ format:'png', captureBeyondViewport:false });
        const corners = await p.run(measure, data, refs);
        checks.push({ frozen, ratio, corners });
        if (receipt && (before || corners.some(c => c.missing.length || !c.columns || !c.rows))) {
          const figure = receipt.replace(/\.json$/,'') + '-' + Number(frozen) + '-' + ratio + '.png';
          fs.writeFileSync(figure,Buffer.from(data,'base64'));
        }
      }
    }
    const all = checks.flatMap(c => c.corners);
    const broken = all.filter(c => !c.columns || !c.rows || c.missing.length);
    assert.equal(sha(), digest);
    if (receipt) fs.writeFileSync(receipt, JSON.stringify({digest,checks, errors},null,2)+'\n');
    console.log(JSON.stringify({samples:all.length, broken, errors:errors.length}));
    assert.deepEqual(errors, []);
    if (before) assert.ok(broken.some(c => c.ref === 'B3' && c.columns && c.rows && c.missing.length));
    else assert.equal(broken.length,0);
  } finally { await h.close(); }
}

main().catch(error => { console.error(error); process.exitCode = 1; });
