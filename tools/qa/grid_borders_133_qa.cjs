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
assert.match(styles,/\.grid-row\s*\{border-bottom:0!important/,'row stroke must not overlap the cell border');
assert.match(styles,/\.grid-cell\s*\{border-left:0;border-top:0;border-right:\.45px solid/,'only right and bottom default seams are painted');
console.log('PASS: border single-owner rules (thin/custom left-top/dashed/medium)');
