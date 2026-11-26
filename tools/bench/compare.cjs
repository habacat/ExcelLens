const fs = require('node:fs');
const path = require('node:path');

const hostFields = ['platform', 'os', 'cpu', 'logicalCPUs', 'ramBytes', 'browser', 'go', 'node'];

function fixtureIdentity(report) {
  return JSON.stringify({
    files: report.samples.map(sample => [sample.name, sample.rows, sample.sha256]),
    statistics: report.conditional?.statistics.map(s => [s.rows, s.distinctKeys, s.repetitions, s.sourceSHA256]),
    views: report.conditional?.browser.map(v => [v.mode, v.viewport, v.sourceSHA256]),
    filteredRows: report.rowQuery?.sourceRows,
  });
}

function compareBaseline(current, root, output) {
  const dir = path.join(root, 'validation');
  const signature = fixtureIdentity(current);
  let previous, filename;
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const file = path.join(dir, entry.name, 'performance-baseline.json');
    if (!entry.isDirectory() || file === output || !fs.existsSync(file)) continue;
    const candidate = JSON.parse(fs.readFileSync(file, 'utf8'));
    if (candidate.profile !== 'release' || candidate.result !== 'PASS' || candidate.schema !== current.schema ||
        candidate.suiteSHA256 !== current.suiteSHA256 || hostFields.some(k => candidate.host[k] !== current.host[k]) ||
        fixtureIdentity(candidate) !== signature) continue;
    if (!previous || candidate.finished > previous.finished) {
      previous = candidate;
      filename = file;
    }
  }
  if (!previous) return { comparable: false };

  const deltas = {};
  function fields(prefix, before, after, names) {
    for (const name of names) {
      const a = before[name], b = after[name];
      // Phases without eligible probes deliberately carry null percentages.
      if (!Number.isFinite(a) || !Number.isFinite(b)) continue;
      deltas[prefix + '.' + name] = { previous: a, current: b, change: b - a,
        changePercent: a > 0 ? Number((100 * (b / a - 1)).toFixed(2)) : null };
    }
  }

  for (const item of current.readings) {
    const base = previous.readings.find(r => r.format === item.format && r.rows === item.rows);
    fields(item.format + '.' + item.rows, base, item, ['firstVisibleMs', 'visibleCoverageMs', 'indexRequestToReadyMs', 'peakServiceRSSBytes']);
  }
  const metrics = { scroll: ['fps', 'p95FrameMs', 'emptyFrames', 'noCoverageFrames'],
    reconcile: ['elapsedMs', 'peakAdditionalDataBytes'], fullSearch: ['searchMs', 'csvStreamAndVerifyMs'],
    edit: ['saveAndVerifyMs'], lifecycle: ['deltaRSSBytes', 'deltaHandles'],
    rowQuery: ['filterAndSortMs', 'warmPageMedianMs', 'warmPageP95Ms', 'peakServiceRSSBytes', 'peakAdditionalDataBytes'] };
  for (const [name, keys] of Object.entries(metrics)) {
    if (current[name] && previous[name]) fields(name, previous[name], current[name], keys);
  }
  for (const item of current.tabs) {
    const base = previous.tabs.find(t => t.pages === item.pages);
    fields('tabs.' + item.pages, base, item, ['p95StatusMs', 'serviceRSSBytes', 'serviceHandles']);
    fields('tabs.' + item.pages + '.browser', base.browser, item.browser, ['sumWorkingSetBytes', 'sumHandles']);
  }

  if (current.conditional) {
    const cf = current.conditional, old = previous.conditional;
    for (const [i, item] of cf.statistics.entries()) {
      fields('conditional.' + item.distinctKeys, old.statistics[i], item, ['sourceIndexMedianMs', 'preparationMedianMs', 'firstViewportMedianMs',
        'warmViewportMedianMs', 'warmViewportP95Ms', 'reopenAndIndexMedianMs', 'reopenedViewportMedianMs', 'sharedIndexBytes']);
    }
    fields('conditional.cell', old.perCell, cf.perCell, ['medianNs', 'medianBytes', 'medianAllocs']);
    const medians = runs => Object.fromEntries(Object.keys(runs[0]).map(key => {
      const values = runs.map(r => r[key]).sort((a, b) => a - b);
      return [key, values[Math.floor(values.length / 2)]];
    }));
    for (const [section, prefix] of [['inlinePresentation', 'inline'], ['conditionalViewports', 'conditional.viewport'], ['sourceNumberReads', 'source.read']]) {
      for (const [name, series] of Object.entries(cf[section] ?? {})) {
        if (old[section]?.[name]) fields(prefix + '.' + name, medians(old[section][name]), medians(series), Object.keys(series[0]));
      }
    }
    for (const [i, view] of cf.browser.entries()) {
      const base = old.browser[i], prefix = 'conditional.' + view.mode;
      fields(prefix, base, view, ['openToInteractiveGridMs', 'openToStyledGridMs']);
      fields(prefix, base.resources, view.resources, ['peakServiceRSSBytes', 'peakDataBytes', 'peakHandles']);
      for (const [j, pass] of view.passes.entries()) {
        const prior = base.passes[j];
        if (pass.visual && prior.visual) fields(prefix + '.' + j + '.pixels', prior.visual, pass.visual, ['correctStylePct', 'whitePct']);
        for (const phase of pass.phases) {
          const prev = prior.phases.find(x => x.phase === phase.phase);
          if (prev) fields(prefix + '.' + j + '.' + phase.phase, prev, phase, ['fps', 'p95FrameMs', 'p99FrameMs', 'textReadyPct', 'conditionalReadyPct', 'settleMs']);
        }
      }
    }
  }
  return { comparable: true, baseline: path.relative(root, filename).split(path.sep).join('/'), deltas };
}
module.exports = { compareBaseline };
