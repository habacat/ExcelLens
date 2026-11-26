// Full-release conditional statistics and display workloads share the ordinary
// production probe, resource monitor and generated sources with the other scenarios.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { execFileSync } = require('node:child_process');
const { measureConditionalScroll } = require('../qa/conditional_scroll_qa.cjs');

function percentile(samples, p) {
  assert.ok(samples.length && samples.every(Number.isFinite), 'missing measurement samples');
  const sorted = samples.toSorted((a, b) => a - b);
  if (p === .5 && sorted.length % 2 === 0) return (sorted[sorted.length / 2 - 1] + sorted[sorted.length / 2]) / 2;
  return sorted[Math.ceil(sorted.length * p) - 1];
}

async function measureConditional({ root, temp, go, exe, meter, smoke }) {
  const cases = smoke ? [100, 5000] : [10000, 500000];
  const rows = smoke ? 10000 : 1000000;
  const repetitions = smoke ? 1 : 3;
  const cwd = path.join(root, 'internal/workbook');
  const test = path.join(temp, process.platform === 'win32' ? 'conditional.test.exe' : 'conditional.test');
  execFileSync(go, ['test', '-mod=vendor', '-c', '-o', test, './internal/workbook'], { cwd: root, windowsHide: true });
  const fixture = path.join(temp, 'conditional-ledger.xlsx');
  const byKeys = new Map(cases.map(keys => [keys, []]));
  for (let round = 0; round < repetitions; round++) {
    for (const keys of round % 2 ? cases.toReversed() : cases) {
      const receipt = path.join(temp, 'stats-' + round + '-' + keys + '.json');
      const env = { ...process.env, EXCELLENS_CF_SCALE_ROWS: String(rows), EXCELLENS_CF_SCALE_KEYS: String(keys),
        EXCELLENS_CF_SCALE_REPORT: receipt, EXCELLENS_CF_SCALE_FIXTURE: round === 0 && keys === cases[0] ? fixture : '' };
      execFileSync(test, ['-test.run=^TestConditionalStatisticsAcrossViewports$', '-test.count=1', '-test.timeout=30m'], { cwd, env, windowsHide: true });
      const sample = JSON.parse(fs.readFileSync(receipt, 'utf8'));
      assert.equal(sample.result, 'PASS');
      assert.equal(sample.rows, rows);
      assert.equal(sample.distinctKeys, keys);
      assert.equal(sample.rules.length, 6);
      assert.equal(sample.sharedIndexUnchangedOnWarmAndReopen, true);
      assert.equal(sample.warmViewportSamplesMs.length, 16);
      byKeys.get(keys).push(sample);
    }
  }
  const statistics = [...byKeys].map(([distinctKeys, runs]) => {
    const warm = runs.flatMap(r => r.warmViewportSamplesMs);
    assert.equal(new Set(runs.map(r => r.sourceSHA256)).size, 1);
    return { rows, distinctKeys, repetitions, sourceSHA256: runs[0].sourceSHA256,
      sourceIndexMedianMs: percentile(runs.map(r => r.sourceIndexMs), .5),
      preparationMedianMs: percentile(runs.map(r => r.statisticsPreparationMs), .5),
      firstViewportMedianMs: percentile(runs.map(r => r.firstViewportMs), .5),
      warmViewportMedianMs: percentile(warm, .5), warmViewportP95Ms: percentile(warm, .95),
      reopenAndIndexMedianMs: percentile(runs.map(r => r.reopenAndIndexMs), .5),
      reopenedViewportMedianMs: percentile(runs.map(r => r.reopenedViewportMs), .5),
      sharedIndexBytes: Math.max(...runs.map(r => r.sharedIndexBytes)), runs };
  });
  const micro = execFileSync(test, ['-test.run=^$', '-test.bench=^BenchmarkConditionalCellEvaluation$', '-test.benchmem',
    '-test.benchtime=300ms', '-test.count=' + repetitions], { cwd, windowsHide: true, encoding: 'utf8' });
  const measurements = [...micro.matchAll(/BenchmarkConditionalCellEvaluation-\d+\s+\d+\s+([\d.]+) ns\/op\s+(\d+) B\/op\s+(\d+) allocs\/op/g)]
    .map(([, ns, bytes, allocs]) => ({ nsPerCell: Number(ns), bytesPerCell: Number(bytes), allocationsPerCell: Number(allocs) }));
  assert.equal(measurements.length, repetitions);
  const browser = [];
  for (const profile of [
    { mode: 'complex', rows: 2400, file: path.join(temp, 'complex-conditional-fixture.xlsx') },
    { mode: 'external', rows: rows + 1, keys: cases[0], file: fixture },
  ]) {
    const run = await measureConditionalScroll({ ...profile, receipt: path.join(temp, profile.mode + '-scroll.json') }, { exe, meter });
    assert.equal(run.sourceUnchanged, true);
    assert.ok(run.resources.peakServiceRSSBytes > 0 && run.resources.peakDataBytes > 0);
    if (profile.mode === 'external') assert.equal(run.sourceSHA256, statistics[0].sourceSHA256);
    browser.push(run);
  }
  return { statistics, perCell: { runs: measurements, medianNs: percentile(measurements.map(m => m.nsPerCell), .5),
    medianBytes: percentile(measurements.map(m => m.bytesPerCell), .5), medianAllocs: percentile(measurements.map(m => m.allocationsPerCell), .5) }, browser };
}

module.exports = { measureConditional };
