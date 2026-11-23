// Compare the actual pixel rows/columns of adjacent strokes in the production renderer.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {createHash} = require('node:crypto');
const {launch} = require('./browser_harness.cjs');
const {zip} = require('./generated_workbook.cjs');
const {validationVersion} = require('./project_config.cjs');

function fixture(varied = false) {
  const ns = 'http://schemas.openxmlformats.org/spreadsheetml/2006/main';
  const rel = 'http://schemas.openxmlformats.org/officeDocument/2006/relationships';
  const pkg = 'http://schemas.openxmlformats.org/package/2006/relationships';
  const types=varied?['thin','thick','double','mediumDashed','dotted',null]:['thin'];
  const borders=types.map(type=>'<border>'+(type?['left','right','top','bottom'].map(side=>
    '<'+side+' style="'+type+'"><color rgb="FF626F7A"/></'+side+'>').join(''):'')+'</border>').join('');
  const formats=types.map((_,i)=>'<xf fillId="2" borderId="'+(i+1)+'" applyFill="1" applyBorder="1"/>').join('');
  const overrides={B3:2,D3:4,E3:4,F6:3,G7:5,H10:6};
  const rows = Array.from({length: 11}, (_, r) => {
    const cells = Array.from('ABCDEFGHI', (col, c) => {
      const styled = r >= 2 && r <= 7 && c >= 1 && c <= 6;
      const style=(varied&&overrides[col+(r+1)])||Number(styled);
      if(col==='F'&&r===5)return '<c r="F6" s="'+style+'"><v>5.54</v></c>';
      return '<c r="' + col + (r + 1) + '" s="' + style + '" t="inlineStr"><is><t>' + (styled ? '' : col + (r+1)) + '</t></is></c>';
    });
    return '<row r="' + (r + 1) + '" ht="25.7" customHeight="1">' + cells.join('') + '</row>';
  });
  return zip({
    '[Content_Types].xml': '<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/><Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/></Types>',
    '_rels/.rels': '<Relationships xmlns="' + pkg + '"><Relationship Id="book" Type="' + rel + '/officeDocument" Target="xl/workbook.xml"/></Relationships>',
    'xl/workbook.xml': '<workbook xmlns="' + ns + '" xmlns:r="' + rel + '"><sheets><sheet name="明细" sheetId="1" r:id="s1"/></sheets></workbook>',
    'xl/_rels/workbook.xml.rels': '<Relationships xmlns="' + pkg + '"><Relationship Id="s1" Type="' + rel + '/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="styles" Type="' + rel + '/styles" Target="styles.xml"/></Relationships>',
    'xl/styles.xml': '<styleSheet xmlns="' + ns + '"><fonts count="1"><font><sz val="11"/><name val="Calibri"/></font></fonts><fills count="3"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill><fill><patternFill patternType="solid"><fgColor rgb="FFDBDBDB"/><bgColor indexed="64"/></patternFill></fill></fills><borders count="' + (types.length+1) + '"><border/>' + borders + '</borders><cellStyleXfs count="1"><xf/></cellStyleXfs><cellXfs count="' + (types.length+1) + '"><xf/>' + formats + '</cellXfs></styleSheet>',
    'xl/worksheets/sheet1.xml': '<worksheet xmlns="' + ns + '"><dimension ref="A1:I11"/><cols><col min="1" max="9" width="10.7" customWidth="1"/></cols><sheetData>' + rows.join('') + '</sheetData><mergeCells count="3"><mergeCell ref="B3:B4"/><mergeCell ref="D3:E3"/><mergeCell ref="B7:C7"/></mergeCells></worksheet>'
  });
}

async function readStrokes(encoded, pane, real) {
  const img = new Image();
  img.src = 'data:image/png;base64,' + encoded;
  await img.decode();
  const canvas = new OffscreenCanvas(img.width, img.height);
  const ctx = canvas.getContext('2d');
  ctx.drawImage(img, 0, 0);
  const data = ctx.getImageData(0, 0, img.width, img.height).data;
  const ratio = img.width / innerWidth;
  const scan = (ref, horizontal) => {
    const el = document.querySelector(pane + ' .grid-cell[data-ref="' + ref + '"]');
    const box = el.getBoundingClientRect();
    // Stay away from perpendicular gridlines halfway across two-cell merges.
    const x = Math.round((box.x + (horizontal ? box.width * .29 : 0)) * ratio);
    const y = Math.round((box.y + (horizontal ? 0 : box.height * .29)) * ratio);
    let peak = Infinity;
    const ink = [];
    const coverage = [];
    for (let step = -4; step <= 4; step++) {
      const px = x + (horizontal ? 0 : step), py = y + (horizontal ? step : 0);
      const index = (py * img.width + px) * 4;
      // Compare the stroke's center, including ties for a multi-pixel line.
      // Measuring the faint antialiasing fringe also measures the adjacent fill.
      if (real ? Math.max(data[index],data[index+1],data[index+2]) >= 180 :
          data[index] >= 205 || data[index+2]-data[index] < 7) continue;
      coverage.push(horizontal ? py : px);
      const darkness = data[index] + data[index+1] + data[index+2];
      if (darkness < peak) { peak = darkness; ink.length = 0; }
      if (darkness === peak) ink.push(horizontal ? py : px);
    }
    return real && horizontal ? coverage : ink;
  };
  const row = real ? ['B2','C2','D2','G2'] : ['B3','C3','D3','F3'];
  const col = real ? ['B2','B5','B6','B7'] : ['B3','B5','B6','B7','B8'];
  return {horizontal: row.map(ref => scan(ref,true)), vertical: col.map(ref => scan(ref,false))};
}

async function main() {
  const baseline = process.argv.includes('--before');
  const releaseDir = path.resolve('validation', validationVersion());
  const dir = path.join(releaseDir, 'private', 'grid-seams');
  fs.mkdirSync(dir, {recursive: true});
  const errors = [], checks = [];
  const h = await launch({errors});
  try {
    const p = h.page, real = process.env.QA_GRID_SOURCE;
    const f = real ? path.resolve(real) : path.join(h.storage, '共享边框.xlsx');
    if (!real) fs.writeFileSync(f, fixture());
    const hash = () => createHash('sha256').update(fs.readFileSync(f)).digest('hex');
    const sourceHash = hash();
    await p.file('#fileInput', f);
    await p.wait('window.ExcelLensPerformance().hasVisibleCoverage && !window.ExcelLensPerformance().sheetLoading');
    if (real) {
      await p.run(()=>Array.from(document.querySelectorAll('.sheet-tab')).find(tab=>tab.textContent==='销售费用').click());
      await p.wait('document.querySelector(".sheet-tab.active")?.textContent === "销售费用" && window.ExcelLensPerformance().hasVisibleCoverage && !window.ExcelLensPerformance().sheetLoading');
      await p.click('#freezeBtn');
      await p.run(()=>{ freezeRowsInput.value='0'; freezeColsInput.value='0'; });
      await p.click('#freezeApplyBtn');
    }
    await p.click('#crosshairToggle');
    await p.run(()=>{grid.scrollLeft=0;grid.scrollTop=0;});
    await p.wait('window.ExcelLensPerformance().hasVisibleCoverage && document.querySelector("#rowsLayer .grid-cell[data-ref=A1]")');
    await p.click('.grid-cell[data-ref="A1"]');
    for (const mode of (real ? ['body'] : ['body', 'frozen'])) {
      if (mode === 'frozen') {
        await p.click('#freezeBtn');
        await p.run(() => { freezeRowsInput.value = '8'; freezeColsInput.value = '7'; });
        await p.click('#freezeApplyBtn');
        await p.wait('!!document.querySelector("#freezeCorner .grid-cell[data-ref=B8]")');
      }
      const pane = mode === 'frozen' ? '#freezeCorner' : '#rowsLayer';
      for (const dpr of [1, 1.25, 1.5, 2]) {
        await p.call('Emulation.setDeviceMetricsOverride',{width: 1360, height: 760, deviceScaleFactor: dpr, mobile: false});
        for (const zoom of [85, 100, 115]) {
          await p.run(z => {
            const from = parseInt(zoomValue.textContent);
            for (let i = 0; i < Math.abs(z-from)/5; i++) (z > from ? zoomInBtn : zoomOutBtn).click();
          }, zoom);
          await p.wait('parseInt(zoomValue.textContent) === ' + zoom + ' && window.ExcelLensPerformance().hasVisibleCoverage');
          await p.evaluate('new Promise(resolve => setTimeout(() => requestAnimationFrame(() => requestAnimationFrame(resolve)), 220))');
          const {data} = await p.call('Page.captureScreenshot',{format:'png',captureBeyondViewport:false});
          const strokes = await p.run(readStrokes,data,pane,Boolean(real));
          // The real report uses different stroke weights. Their trailing
          // edge must coincide; the uniform fixture must match across its full peak.
          const aligned = lines => lines[0].length > 0 && lines.every(line =>
            real ? line.at(-1) === lines[0].at(-1) : String(line) === String(lines[0]));
          const check={mode, dpr, zoom, horizontal: aligned(strokes.horizontal), vertical: aligned(strokes.vertical), strokes};
          if (!baseline && (!check.horizontal || !check.vertical)) {
            check.boxes=await p.run(pane=>Object.fromEntries(['A3','A4','A5','B3','B5','D2','D3','E2'].map(ref=>{
              const el=document.querySelector(pane+' .grid-cell[data-ref="'+ref+'"]'),s=getComputedStyle(el);
              return [ref,[el.getBoundingClientRect().toJSON(),s.borderLeft,s.borderRight,s.borderBottom,s.borderTop,s.zIndex]];
            })),pane);
            fs.writeFileSync(path.join(dir,(real?'real-':'')+'check-'+mode+'-'+dpr+'-'+zoom+'.png'),Buffer.from(data,'base64'));
          }
          checks.push(check);
          if (dpr === 1 && zoom === 100) fs.writeFileSync(path.join(dir,(real?'real-':'')+(baseline?'before':'after')+'-'+mode+'.png'),Buffer.from(data,'base64'));
        }
      }
    }
    const failed = checks.filter(c => !c.horizontal || !c.vertical);
    assert.equal(hash(), sourceHash);
    fs.writeFileSync(path.join(dir,(real?'real-':'')+(baseline?'before.json':'after.json')), JSON.stringify({sourceHash,checks, errors},null,2));
    console.log(JSON.stringify({samples:checks.length, misaligned:failed.length, first:failed[0], errors:errors.length}));
    assert.deepEqual(errors, []);
    if (baseline) assert.ok(failed.some(c => c.dpr===1 && c.zoom===100 && !c.horizontal && !c.vertical &&
      [...c.strokes.horizontal,...c.strokes.vertical].every(line=>line.length)));
    else {
      assert.equal(failed.length, 0);
      if (!real) fs.writeFileSync(path.join(releaseDir, 'grid-seam-browser.json'), JSON.stringify({
        result:'PASS',scenarios:checks.length, panes:['body','frozen'], deviceRatios:[1,1.25,1.5,2],
        sheetZoom:[85,100,115],horizontalSamples:4,verticalSamples:5,misaligned:0,errors:errors.length
      },null,2)+'\n');
    }
  } finally { await h.close(); }
}
module.exports = {fixture};
if (require.main === module) main().catch(error => { console.error(error); process.exitCode = 1; });
