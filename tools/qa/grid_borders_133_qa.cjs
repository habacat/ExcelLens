/* Browser-independent seam regression: adjacent cells share one painted edge,
 * including a colored left/top OOXML edge next to an unstyled neighbor. */
const fs=require('node:fs');
const vm=require('node:vm');
const assert=require('node:assert/strict');
const path=require('node:path');
const web=path.resolve(__dirname,'../../internal/ui/web');
const context={window:{}};
vm.runInNewContext(fs.readFileSync(path.join(web,'grid-borders.js'),'utf8'),context);
const seam=context.window.ExcelLensGridBorders;
assert.equal(seam.choose('','1px solid #3388cc'),'1px solid #3388cc');
assert.equal(seam.choose('.45px solid #e8ecef','1.15px solid #ef452a'),'1.15px solid #ef452a');
assert.equal(seam.choose('1.15px solid #ef452a','.55px solid #bbbbbb'),'1.15px solid #ef452a');
assert.equal(seam.choose('.55px dashed #2277dd','.55px solid #2277dd'),'.55px solid #2277dd');
const fromLeft=seam.resolve({}, {borderLeft:'1.15px solid #ef452a'},{});
assert.equal(fromLeft.right,'1.15px solid #ef452a');
const fromBelow=seam.resolve({}, {}, {borderTop:'1px dashed #26aa66'});
assert.equal(fromBelow.bottom,'1px dashed #26aa66');
const styles=fs.readFileSync(path.join(web,'style.css'),'utf8');
// The seam declarations live in the main grid rules; match them wherever they sit in the block.
assert.match(styles,/\.grid-row\s*\{[^}]*border-bottom:\s*0\s*!important/,'row stroke must not overlap the cell border');
assert.match(styles,/\.grid-cell\s*\{[^}]*border-left:\s*0;\s*border-top:\s*0;\s*border-right:\s*\.4px solid var\(--grid-line-color\);\s*border-bottom:\s*\.4px solid var\(--grid-line-color\)/,'only right and bottom default seams are painted');
assert.equal((styles.match(/(?:^|\n)\.grid-(?:row|cell)\s*\{[^}]*border-(?:right|bottom):/g)||[]).length,2,'one rule each owns the row and cell seams');
console.log('PASS: border single-owner rules (thin/custom left-top/dashed/medium)');
