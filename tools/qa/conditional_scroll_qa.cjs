// Run with the synthetic ledger from TestConditionalStatisticsAcrossViewports.
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const { launch, pause } = require('./browser_harness.cjs');

async function waitForStyledGrid(page) {
  const deadline = Date.now() + 240000;
  while (Date.now() < deadline) {
    const ready = await page.run(() => {
      const perf = window.ExcelLensPerformance?.();
      const sample = document.querySelector('#rowsLayer [data-ref="F2"]');
      return perf?.hasVisibleCoverage && !perf.sheetLoading && sample?._xlModel?.conditional?.matched === 1;
    });
    if (ready) return;
    await pause(100);
  }
  throw Error('Timed out waiting for verified conditional styles');
}

async function trace(page, styled) {
  const originalFormat = await page.evaluate('document.querySelector("#formatBtn").getAttribute("aria-pressed") === "true"');
  if (originalFormat !== styled) await page.click('#formatBtn');

  await page.run(() => {
    document.querySelector('#grid').scrollTop = 0;
    delete window.conditionalScrollQA;
  });
  await page.wait(`document.querySelector('#grid').scrollTop === 0 && window.ExcelLensPerformance().hasVisibleCoverage && !!document.querySelector('#rowsLayer [data-ref="A2"]')`);
  if (styled) await waitForStyledGrid(page);

  await page.run(() => {
    const grid = document.querySelector('#grid');
    const intervals = [];
    let emptyFrames = 0;
    let incompleteCoverageFrames = 0;
    let begin;
    let last;
    function frame(time) {
      if (begin === undefined) begin = time;
      else intervals.push(time - last);
      last = time;
      if (!document.querySelector('#rowsLayer .grid-row .grid-cell')) emptyFrames++;
      if (!window.ExcelLensPerformance().hasVisibleCoverage) incompleteCoverageFrames++;
      grid.scrollTop += 24;
      const durationMs = time - begin;
      if (durationMs < 10000) {
        requestAnimationFrame(frame);
        return;
      }
      intervals.sort((a, b) => a - b);
      window.conditionalScrollQA = {
        durationMs,
        frames: intervals.length,
        fps: intervals.length * 1000 / durationMs,
        p95FrameMs: intervals[Math.ceil(intervals.length * .95) - 1],
        emptyFrames,
        incompleteCoverageFrames,
      };
    }
    requestAnimationFrame(frame);
  });
  await page.wait('!!window.conditionalScrollQA');
  await page.wait('window.ExcelLensPerformance().hasVisibleCoverage');
  if (styled) await page.wait(`[...document.querySelectorAll('#rowsLayer [data-col="6"]')].some(c => c._xlModel?.conditional?.matched === 1)`);
  const sample = await page.evaluate('window.conditionalScrollQA');
  assert.equal(sample.emptyFrames, 0);
  return { originalFormat: styled, ...sample };
}

async function main() {
  const [file, receipt] = process.argv.slice(2);
  assert.ok(file && receipt, 'pass the generated ledger and an output JSON path');
  const errors = [];
  const harness = await launch({ errors, duration: '12m' });
  const report = { source: 'synthetic ledger', scroll: [], desktopManualAcceptance: false };
  try {
    const start = Date.now();
    await harness.page.file('#fileInput', path.resolve(file));
    await waitForStyledGrid(harness.page);
    report.openToStyledGridMs = Date.now() - start;
    for (const styled of [true, false, false, true]) {
      report.scroll.push(await trace(harness.page, styled));
    }
    assert.deepEqual(errors, []);
  } finally {
    try {
      await harness.close();
      report.profileCleanup = 'complete';
    } catch (error) {
      // Chrome on Windows can briefly retain a file lock after the service and browser exit.
      const profile = path.resolve(harness.storage, 'chrome') + path.sep;
      if (!error.path?.startsWith(profile) || !['EBUSY', 'EPERM', 'ENOTEMPTY'].includes(error.code)) throw error;
      report.profileCleanup = error.code;
    }
  }
  report.result = 'PASS';
  fs.writeFileSync(receipt, JSON.stringify(report, null, 2) + '\n');
  console.log(JSON.stringify(report));
}

main().catch(error => {
  console.error(error);
  process.exitCode = 1;
});
