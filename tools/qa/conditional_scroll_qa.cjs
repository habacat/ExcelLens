// Fixed-velocity scroll, text readiness, conditional readiness, decoupled open
// latency, and composited pixel coverage QA for conditional formatting workbooks.
// Usage:
//   node tools/qa/conditional_scroll_qa.cjs [receipt.json]
//   node tools/qa/conditional_scroll_qa.cjs <external.xlsx> <receipt.json>
'use strict';

const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const crypto = require('node:crypto');
const assert = require('node:assert/strict');
const { launch, pause } = require('./browser_harness.cjs');
const { complexConditionalFixture, baseColors, cfColors, border } = require('./conditional_scroll_fixture.cjs');

const hash = buf => crypto.createHash('sha256').update(buf).digest('hex');
const quantile = (list, p) => list.length ? list.slice().sort((a, b) => a - b)[Math.max(0, Math.ceil(list.length * p) - 1)] : 0;

function parseArgs(argv) {
  const args = argv.slice(2);
  if (args.length >= 2 && /\.xlsx$/i.test(args[0])) {
    return { mode: 'external', file: path.resolve(args[0]), receipt: path.resolve(args[1]) };
  }
  const receipt = path.resolve(args[0] || 'artifacts/qa/conditional-scroll-qa.json');
  const file = path.join(path.dirname(receipt), 'complex-conditional-fixture.xlsx');
  return { mode: 'complex', file, receipt };
}

async function waitForInteractiveGrid(page, timeoutMs = 120000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const ready = await page.run(() => {
      const grid = document.getElementById('grid');
      const perf = window.ExcelLensPerformance?.();
      const first = document.querySelector('#rowsLayer .grid-cell[data-ref="A2"]');
      return Boolean(grid && !grid.inert && perf?.hasVisibleCoverage && !perf.sheetLoading && first && first.textContent !== '');
    });
    if (ready) return;
    await pause(25);
  }
  throw Error('Timed out waiting for interactive grid');
}

async function waitForStyledGrid(page, mode, timeoutMs = 240000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const ready = await page.run(currentMode => {
      const perf = window.ExcelLensPerformance?.();
      if (!perf?.hasVisibleCoverage || perf.sheetLoading || perf.conditionalStatsRunning) return false;
      const pending = document.querySelector('#rowsLayer .cell-conditional-pending');
      if (pending) return false;
      if (currentMode === 'complex') {
        const b2 = document.querySelector('#rowsLayer [data-ref="B2"]');
        const c2 = document.querySelector('#rowsLayer [data-ref="C2"]');
        const d2 = document.querySelector('#rowsLayer [data-ref="D2"]');
        const f2 = document.querySelector('#rowsLayer [data-ref="F2"]');
        return Boolean(
          b2?._xlModel?.conditional?.matched === 1 &&
          c2?._xlModel?.conditional?.matched === 1 &&
          d2?._xlModel?.conditional?.matched === 1 &&
          f2?._xlModel?.conditional?.matched === 1
        );
      }
      const f2 = document.querySelector('#rowsLayer [data-ref="F2"]');
      return f2?._xlModel?.conditional?.matched === 1;
    }, mode);
    if (ready) return;
    await pause(50);
  }
  throw Error('Timed out waiting for verified conditional styles');
}

function installInstruments(mode, rows, keys, colors) {
  const g = document.getElementById('grid');
  const el = ref => document.querySelector('#rowsLayer .grid-cell[data-ref="' + ref + '"]');
  const a1 = el('A1');
  const grid = g.getBoundingClientRect();
  const a = a1.getBoundingClientRect();
  const cProbe = el('E1') || el('D1') || el('B1');
  const rProbe = el('A17') || el('A9') || el('A2');
  const colSpan = Math.max(1, Number(cProbe.dataset.col) - 1);
  const rowSpan = Math.max(1, Number(rProbe.dataset.row) - 1);
  const dx = (cProbe.getBoundingClientRect().left - a.left) / colSpan;
  const dy = (rProbe.getBoundingClientRect().top - a.top) / rowSpan;
  const rowsRect = document.getElementById('rowsLayer').getBoundingClientRect();
  const box = {
    x: Math.ceil(a.left),
    y: Math.ceil(a.top),
    right: Math.min(Math.floor(grid.left + g.clientWidth), Math.floor(rowsRect.right) - 2),
    bottom: Math.floor(grid.top + g.clientHeight),
  };

  let stamp = document.getElementById('cfScrollQAStamp');
  if (!stamp) {
    stamp = document.createElement('canvas');
    stamp.id = 'cfScrollQAStamp';
    stamp.width = stamp.height = 8;
    stamp.style.cssText = 'position:fixed;left:0;top:0;width:8px;height:8px;z-index:2147483647;pointer-events:none';
    document.body.append(stamp);
  }
  const ctx = stamp.getContext('2d');

  const expectedText = (r, c) => {
    if (r === 1) return 'H' + c;
    if (c === 1) return 'K' + String((r - 2) % 400).padStart(4, '0');
    if (c === 2) return String(r);
    if (c === 3) return String((r % 3) + 1);
    if (c === 4) return String(r * 10);
    if (c === 5) return String(r * 2);
    if (c === 6) return 'G' + ((r - 2) % 400);
    if (c === 7) return 'M' + ((r - 2) % 400);
    return String(r * 1000 + c);
  };

  const populatedCols = new Set();
  for (const node of document.querySelectorAll('#rowsLayer .grid-cell[data-row="2"]')) {
    if (node.textContent !== '') populatedCols.add(Number(node.dataset.col));
  }

  const q = { mode, box, dx, dy, stamp, events: [], calls: 0, cancels: 0 };

  if (!window.__cfScrollFetchHooked) {
    window.__cfScrollFetchHooked = true;
    const real = window.fetch;
    window.fetch = function (url, opt) {
      if (new URL(url, location.href).pathname === '/api/sheet') {
        if (window.cfScrollQA) window.cfScrollQA.calls++;
        opt?.signal?.addEventListener('abort', () => {
          if (window.cfScrollQA) window.cfScrollQA.cancels++;
        }, { once: true });
      }
      return real.apply(this, arguments);
    };
  }

  // Determine the expected character and rule from source coordinates and fixture data.
  const expectedMatch = (row, col) => {
    if (row < 2 || row > rows) return null;
    if (mode !== 'complex') {
      if (!keys || col < 6 || col > 11) return null;
      return col !== 11 || row + 2 <= rows;
    }
    if (col < 1 || col > 7) return null;
    switch (col) {
      case 1: return row % 2 === 0;
      case 2: return row + 2 <= rows;
      case 3: {
        let sum = 0;
        for (let i = row; i <= Math.min(rows, row + 23); i++) sum += i % 3 + 1;
        return sum >= 36;
      }
      case 4: return true;
      case 5: return row >= 11;
      case 6: return row - 400 >= 2 || row + 400 <= rows;
      case 7: return row >= 402;
    }
  };
  q.probeFrame = styled => {
    let textComplete = true;
    let sampled = 0;
    let conditionalOk = true;
    let applicable = 0;
    for (const u of [0.05, 0.5, 0.94]) {
      for (const v of [0.06, 0.54, 0.94]) {
        const x = box.x + (box.right - box.x) * u;
        const y = box.y + (box.bottom - box.y) * v;
        const logicalX = x - a.left + g.scrollLeft;
        const logicalY = y - a.top + g.scrollTop;
        const fracX = logicalX % dx, fracY = logicalY % dy;
        if (Math.min(fracX, dx - fracX, fracY, dy - fracY) < 1.5) continue;
        const c = Math.floor(logicalX / dx) + 1;
        const r = Math.floor(logicalY / dy) + 1;
        const wantedMatch = styled ? expectedMatch(r, c) : null;
        if (wantedMatch !== null) applicable++;
        const hit = document.elementFromPoint(x, y)?.closest('.grid-cell');
        if (!hit || Number(hit.dataset.row) !== r || Number(hit.dataset.col) !== c) {
          textComplete = false;
          if (wantedMatch !== null) conditionalOk = false;
          continue;
        }
        sampled++;
        if (mode === 'complex' && r <= rows && c <= 48) {
          if (hit.textContent !== expectedText(r, c)) textComplete = false;
        } else if (keys && r >= 2 && r <= rows && c <= 3) {
          const want = c === 1 ? 'K' + (r - 2) % keys : c === 2 ? String(Math.floor((r - 2) / keys) % 2) : '2';
          if (hit.textContent !== want) textComplete = false;
        } else if (populatedCols.has(c) && r >= 2 && hit.textContent === '') {
          textComplete = false;
        }
        if (wantedMatch !== null) {
          const rule = hit._xlModel?.conditional;
          if (rule?.pending || rule?.notice || Boolean(rule?.matched) !== wantedMatch) conditionalOk = false;
          if (wantedMatch && mode === 'complex' && rule?.visual?.background?.toUpperCase() !== '#' + colors[c - 1]) conditionalOk = false;
        }
      }
    }
    if (!sampled) textComplete = false;
    if (styled && document.querySelector('#rowsLayer .cell-conditional-pending')) {
      conditionalOk = false;
    }
    return { textComplete, conditionalOk, applicable };
  };

  q.mark = phase => {
    const id = q.events.length + 1;
    ctx.fillStyle = 'rgb(' + (id & 255) + ',' + ((id >> 8) & 255) + ',73)';
    ctx.fillRect(0, 0, 8, 8);
    q.events.push({ id, phase, x: g.scrollLeft, y: g.scrollTop });
  };

  window.cfScrollQA = q;
  return { box, dx, dy, maxScrollLeft: Math.max(0, g.scrollWidth - g.clientWidth), maxScrollTop: Math.max(0, g.scrollHeight - g.clientHeight) };
}

async function driveFixedVelocity(phases, styled) {
  const q = window.cfScrollQA;
  const g = document.getElementById('grid');
  const raf = () => new Promise(requestAnimationFrame);
  const results = [];
  q.events = [];

  for (const [phase, duration, deltaX, deltaY] of phases) {
    let time = await raf();
    const start = time;
    let last = time;
    const intervals = [];
    let frames = 0;
    let emptyFrames = 0;
    let incompleteCoverageFrames = 0;
    let textReadyFrames = 0;
    let conditionalReadyFrames = 0;
    let conditionalCheckedFrames = 0;
    let maxNodes = 0;
    let maxCacheBytes = 0;
    let lastInput = performance.now();

    const perfStart = window.ExcelLensPerformance();
    const lookupsStart = perfStart.foregroundLookups || 0;
    const hitsStart = perfStart.foregroundHits || 0;
    const callsStart = q.calls;
    const cancelsStart = q.cancels;
    const fromX = g.scrollLeft;
    const fromY = g.scrollTop;

    do {
      time = await raf();
      intervals.push(time - last);
      last = time;
      const perf = window.ExcelLensPerformance();
      if (!document.querySelector('#rowsLayer .grid-row .grid-cell')) emptyFrames++;
      if (!perf.hasVisibleCoverage) incompleteCoverageFrames++;
      if (frames > 0) {
        const probe = q.probeFrame(styled);
        if (probe.textComplete) textReadyFrames++;
        if (probe.applicable) {
          conditionalCheckedFrames++;
          if (probe.conditionalOk) conditionalReadyFrames++;
        }
      }
      const t = Math.min(1, (time - start) / duration);
      g.scrollTo(
        Math.max(0, fromX + Math.round(deltaX * t)),
        Math.max(0, fromY + Math.round(deltaY * t))
      );
      lastInput = performance.now();
      q.mark(phase);
      frames++;
      maxNodes = Math.max(maxNodes, perf.visibleNodes || 0);
      maxCacheBytes = Math.max(maxCacheBytes, perf.bufferedApproxBytes || 0);
    } while (time - start < duration);

    const deadline = lastInput + 15000;
    while (true) {
      const perf = window.ExcelLensPerformance();
      const probe = q.probeFrame(styled);
      if (perf.hasVisibleCoverage && !perf.sheetLoading && probe.textComplete && (!styled || probe.conditionalOk)) break;
      if (performance.now() > deadline) {
        const misses = [];
        for (const u of [0.05, 0.5, 0.94]) {
          for (const v of [0.06, 0.54, 0.94]) {
            const x = q.box.x + (q.box.right - q.box.x) * u;
            const y = q.box.y + (q.box.bottom - q.box.y) * v;
            const rawEl = document.elementFromPoint(x, y);
            const cell = rawEl?.closest('.grid-cell');
            misses.push({ u, v, x: Math.round(x), y: Math.round(y), tag: rawEl?.tagName, cls: rawEl?.className, ref: cell?.dataset?.ref, text: cell?.textContent });
          }
        }
        throw Error('Final coordinates/styles did not settle in phase ' + phase + ' ' + JSON.stringify({
          perf,
          probe,
          scroll: [g.scrollLeft, g.scrollTop],
          pending: document.querySelectorAll('#rowsLayer .cell-conditional-pending').length,
          misses,
        }));
      }
      await raf();
    }
    const settleMs = performance.now() - lastInput;
    const perfEnd = window.ExcelLensPerformance();
    const totalMs = intervals.reduce((s, x) => s + x, 0);
    const checked = frames - 1;
    const distancePx = Math.hypot(deltaX, deltaY);

    results.push({
      phase,
      durationMs: totalMs,
      logicalDeltaX: deltaX,
      logicalDeltaY: deltaY,
      logicalVelocityPxPerSec: duration > 1 ? Math.round((distancePx * 1000) / duration) : 0,
      frames,
      checkedFrames: frames - 1,
      fps: totalMs > 0 ? (intervals.length * 1000) / totalMs : 0,
      intervals,
      settleMs,
      emptyFrames,
      incompleteCoverageFrames,
      textReadyFrames,
      textReadyPct: checked ? Number(((textReadyFrames * 100) / checked).toFixed(2)) : null,
      conditionalCheckedFrames,
      conditionalReadyFrames: styled ? conditionalReadyFrames : null,
      conditionalReadyPct: conditionalCheckedFrames ? Number(((conditionalReadyFrames * 100) / conditionalCheckedFrames).toFixed(2)) : null,
      foregroundLookups: (perfEnd.foregroundLookups || 0) - lookupsStart,
      foregroundHits: (perfEnd.foregroundHits || 0) - hitsStart,
      requests: q.calls - callsStart,
      cancels: q.cancels - cancelsStart,
      maxNodes,
      maxCacheBytes,
      end: [g.scrollLeft, g.scrollTop],
    });
    await raf();
    await raf();
  }
  return { results, events: q.events, box: q.box };
}

async function imageData(encoded) {
  const img = new Image();
  img.src = 'data:image/png;base64,' + encoded;
  await img.decode();
  const ctx = new OffscreenCanvas(img.width, img.height).getContext('2d', { willReadFrequently: true });
  ctx.drawImage(img, 0, 0);
  return ctx;
}

async function comparePixels(before, after, rect, palette) {
  const a = (await window.cfScrollImage(before)).getImageData(rect.x, rect.y, rect.right - rect.x, rect.bottom - rect.y).data;
  const b = (await window.cfScrollImage(after)).getImageData(rect.x, rect.y, rect.right - rect.x, rect.bottom - rect.y).data;
  const styles = new Set(palette.map(s => parseInt(s, 16)));
  const paper = 0xffffff;
  let eligible = 0;
  let correct = 0;
  let white = 0;
  for (let i = 0; i < b.length; i += 4) {
    const reference = b[i] * 65536 + b[i + 1] * 256 + b[i + 2];
    if (!styles.has(reference)) continue;
    eligible++;
    const observed = a[i] * 65536 + a[i + 1] * 256 + a[i + 2];
    if (observed === reference) correct++;
    if (observed === paper) white++;
  }
  return {
    eligible,
    correct,
    white,
    correctStylePct: eligible > 0 ? Number(((correct * 100) / eligible).toFixed(2)) : null,
    whitePct: eligible > 0 ? Number(((white * 100) / eligible).toFixed(2)) : 0,
  };
}

function buildPhases(maxScrollLeft, maxScrollTop) {
  const vNormal = Math.min(760, Math.floor(maxScrollTop * 0.25));
  const vFast = Math.min(5400, Math.floor(maxScrollTop * 0.45));
  const hFast = Math.min(3600, Math.floor(maxScrollLeft * 0.8));
  const phases = [
    ['normal', 800, 0, vNormal],
    ['fastVertical', 900, 0, vFast],
  ];
  if (hFast >= 400) {
    phases.push(['fastHorizontal', 800, hFast, 0]);
    phases.push(['reverse', 850, -Math.floor(hFast * 0.85), -Math.floor(vFast * 0.8)]);
  } else {
    phases.push(['reverse', 850, 0, -Math.floor(vFast * 0.8)]);
  }
  const jumpY = Math.min(24000, Math.max(1200, Math.floor(maxScrollTop * 0.65)));
  const jumpX = hFast >= 400 ? Math.floor(hFast * 0.35) : 0;
  phases.push(['jump', 1, jumpX, jumpY]);
  return phases;
}

async function runScrollPass(page, mode, styled, captureVisual, palette, rows, keys) {
  const currentStyled = await page.evaluate('document.querySelector("#formatBtn").getAttribute("aria-pressed") === "true"');
  if (currentStyled !== styled) await page.click('#formatBtn');

  await page.run(() => {
    const g = document.getElementById('grid');
    g.scrollTo(0, 0);
  });
  await page.wait('document.getElementById("grid").scrollTop === 0 && document.getElementById("grid").scrollLeft === 0 && window.ExcelLensPerformance().hasVisibleCoverage');
  if (styled) await waitForStyledGrid(page, mode);

  const dims = await page.run(installInstruments, mode, rows, keys || 0, cfColors);
  const phases = buildPhases(dims.maxScrollLeft, dims.maxScrollTop);

  const frames = [];
  const onEvent = ({ data }) => {
    const m = JSON.parse(data);
    if (m.method === 'Page.screencastFrame') {
      frames.push(m.params.data);
      page.call('Page.screencastFrameAck', { sessionId: m.params.sessionId }).catch(() => {});
    }
  };
  if (captureVisual) {
    page.socket.addEventListener('message', onEvent);
    await page.call('Page.startScreencast', { format: 'png', everyNthFrame: 1 });
  }
  const trace = await page.run(driveFixedVelocity, phases, styled);
  if (captureVisual) {
    await page.call('Page.stopScreencast');
    page.socket.removeEventListener('message', onEvent);
  }

  const summarizedPhases = trace.results.map(({ intervals, ...rest }) => ({
    ...rest,
    fps: Number(rest.fps.toFixed(2)),
    p95FrameMs: Number(quantile(intervals, 0.95).toFixed(2)),
    p99FrameMs: Number(quantile(intervals, 0.99).toFixed(2)),
    settleMs: Number(rest.settleMs.toFixed(2)),
  }));
  for (const ph of summarizedPhases) {
    assert.equal(ph.emptyFrames, 0, `emptyFrames > 0 in ${ph.phase}`);
  }

  let visual = null;
  if (captureVisual) {
    await page.evaluate('window.cfScrollImage=' + imageData.toString() + ';true');
    const byPhase = new Map();
    for (const encoded of frames) {
      const id = await page.run(async data => {
        const px = (await window.cfScrollImage(data)).getImageData(3, 3, 1, 1).data;
        return px[2] === 73 ? px[0] + 256 * px[1] : 0;
      }, encoded);
      if (!id) continue;
      const point = trace.events[id - 1];
      if (!point) continue;
      if (!byPhase.has(point.phase)) byPhase.set(point.phase, new Map());
      const group = byPhase.get(point.phase);
      if (!group.has(id)) group.set(id, { point, encoded });
    }
    const samples = [];
    for (const { phase } of summarizedPhases) {
      const group = [...(byPhase.get(phase)?.values() || [])];
      if (!group.length) continue;
      const picks = new Set([0, Math.floor((group.length - 1) / 2), group.length - 1]);
      for (const idx of picks) {
        const { point, encoded } = group[idx];
        await page.run(({ x, y }) => document.getElementById('grid').scrollTo(x, y), point);
        await page.wait('window.ExcelLensPerformance().hasVisibleCoverage && !window.ExcelLensPerformance().sheetLoading');
        await page.evaluate('new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)))');
        const ref = await page.call('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false });
        const stats = await page.run(comparePixels, encoded, ref.data, trace.box, palette);
        samples.push({ ...point, ...stats });
      }
    }
    const eligibleTotal = samples.reduce((s, x) => s + x.eligible, 0);
    assert.ok(eligibleTotal > 0, 'pixel pass captured no verifiable style pixels');
    const correctTotal = samples.reduce((s, x) => s + x.correct, 0);
    const whiteTotal = samples.reduce((s, x) => s + x.white, 0);
    visual = {
      sampledFrames: samples.length,
      eligiblePixels: eligibleTotal,
      correctStylePct: eligibleTotal > 0 ? Number(((correctTotal * 100) / eligibleTotal).toFixed(2)) : null,
      whitePct: eligibleTotal > 0 ? Number(((whiteTotal * 100) / eligibleTotal).toFixed(2)) : 0,
      samples,
    };
  }

  return {
    originalFormat: styled,
    phases: summarizedPhases,
    visual,
  };
}

async function measureConditionalScroll(cfg, { exe, meter } = {}) {
  const rows = cfg.rows || (cfg.mode === 'complex' ? Math.max(500, Number(process.env.CF_SCROLL_ROWS || 2400)) : 0);
  fs.mkdirSync(path.dirname(cfg.receipt), { recursive: true });
  if (cfg.mode === 'complex') {
    const buf = complexConditionalFixture(rows, 48);
    fs.writeFileSync(cfg.file, buf);
  }
  const sourceBefore = hash(fs.readFileSync(cfg.file));
  const errors = [];
  const harness = await launch({ errors, duration: '12m', exe });
  const palette = [...baseColors, ...cfColors, border];
  const report = {
    mode: cfg.mode,
    sourceFile: path.basename(cfg.file),
    sourceSHA256: sourceBefore,
    cpu: os.cpus()[0].model,
    platform: process.platform,
    node: process.version,
    viewport: [1120, 780],
    desktopManualAcceptance: false,
    passes: [],
  };

  let monitor;
  try {
    if (meter) {
      monitor = await meter(harness.servicePID, path.join(harness.storage, 'ExcelLens'));
      await monitor.sample('start', true);
    }
    const page = harness.page;
    await page.call('Emulation.setDeviceMetricsOverride', { width: 1120, height: 780, deviceScaleFactor: 1, mobile: false });
    const start = Date.now();
    await page.file('#fileInput', cfg.file);
    await waitForInteractiveGrid(page);
    report.openToInteractiveGridMs = Date.now() - start;
    report.interactiveSnapshot = await page.run(() => ({
      gridInert: Boolean(document.getElementById('grid')?.inert),
      pendingCells: document.querySelectorAll('#rowsLayer .cell-conditional-pending').length,
      conditionalStatsVisible: !document.getElementById('conditionalStatsStatus')?.hidden,
      conditionalStatsText: document.getElementById('conditionalStatsText')?.textContent || '',
      k2MatchedImmediately: document.querySelector('#rowsLayer [data-ref="K2"]')?._xlModel?.conditional?.matched || 0,
    }));
    await waitForStyledGrid(page, cfg.mode);
    report.openToStyledGridMs = Date.now() - start;

    if (cfg.mode === 'complex') {
      // Verify every cost-based strategy rendered real conditional fills on viewport cells.
      report.strategyVerification = await page.run(() => {
        const read = ref => {
          const n = document.querySelector('#rowsLayer [data-ref="' + ref + '"]');
          return {
            ref,
            text: n?.textContent || '',
            matched: n?._xlModel?.conditional?.matched || 0,
            fill: n?.style.getPropertyValue('--xl-fill') || '',
            pending: Boolean(n?._xlModel?.conditional?.pending),
          };
        };
        return {
          directExpr: read('A2'),
          viewportCacheShortSum: read('B2'),
          slidingWindow24Sum: read('C2'),
          sharedAggregateFixedSum: read('D2'),
          diskPrefixDynamicSum: read('E12'),
          diskGroupCountIf: read('F2'),
        };
      });
      assert.equal(report.strategyVerification.directExpr.matched, 1);
      assert.equal(report.strategyVerification.viewportCacheShortSum.matched, 1);
      assert.equal(report.strategyVerification.slidingWindow24Sum.matched, 1);
      assert.equal(report.strategyVerification.sharedAggregateFixedSum.matched, 1);
      assert.equal(report.strategyVerification.diskPrefixDynamicSum.matched, 1);
      assert.equal(report.strategyVerification.diskGroupCountIf.matched, 1);
      for (const [ref, count] of [['G2', 0], ['G402', 1]]) {
        const { cell } = await page.api('/api/cell?sheet=ComplexCF&ref=' + ref);
        assert.equal(cell.ref, ref);
        assert.equal(cell.conditional?.matched || 0, count);
        assert.ok(!cell.conditional?.notice && !cell.conditional?.pending);
        if (count) assert.equal(cell.conditional.visual.background.toUpperCase(), '#' + cfColors[6]);
      }
      report.strategyVerification.diskMatchPositions = ['G2', 'G402'];
    }

    // Run timing + visual pixel coverage passes across original format ON / OFF.
    report.passes.push(await runScrollPass(page, cfg.mode, true, cfg.mode === 'complex', palette, rows, cfg.keys));
    report.passes.push(await runScrollPass(page, cfg.mode, false, false, palette, rows, cfg.keys));
    report.passes.push(await runScrollPass(page, cfg.mode, true, false, palette, rows, cfg.keys));

    // Verify storage dialog shows dedicated conditional stats cache & budget controls.
    await page.click('#storageOpenBtn');
    await page.wait('document.getElementById("storageDialog")?.open && !document.querySelector(\'#storageDialog [data-storage="set-conditional-stats-budget"]\')?.disabled && document.getElementById("storageConditionalSummary")?.textContent.includes("上限")');
    report.storageConditionalSummary = await page.evaluate('document.getElementById("storageConditionalSummary").textContent');
    await page.run(() => {
      document.getElementById('storageConditionalBudget').value = '512';
      document.querySelector('#storageDialog [data-storage="set-conditional-stats-budget"]').click();
    });
    await page.wait('document.getElementById("storageStatus")?.textContent.includes("512.0 MB")');
    report.storageBudgetUpdateStatus = await page.evaluate('document.getElementById("storageStatus").textContent');
    await page.run(() => document.getElementById('storageDialog')?.close());

    if (monitor) {
      const met = await monitor.sample();
      report.resources = { peakServiceRSSBytes: met.peak.rssBytes, peakDataBytes: met.peak.dataBytes, peakHandles: met.peak.handles };
    }
    assert.deepEqual(errors, []);
  } finally {
    if (monitor) await monitor.close();
    try {
      await harness.close();
      report.profileCleanup = 'complete';
    } catch (error) {
      const profile = path.resolve(harness.storage, 'chrome') + path.sep;
      if (!error.path?.startsWith(profile) || !['EBUSY', 'EPERM', 'ENOTEMPTY'].includes(error.code)) throw error;
      report.profileCleanup = error.code;
    }
  }

  assert.equal(hash(fs.readFileSync(cfg.file)), sourceBefore);
  report.sourceUnchanged = true;
  report.result = 'PASS';
  fs.writeFileSync(cfg.receipt, JSON.stringify(report, null, 2) + '\n');
  return report;
}

if (require.main === module) {
  measureConditionalScroll(parseArgs(process.argv)).then(report => console.log(JSON.stringify(report, null, 2))).catch(error => {
    console.error(error);
    process.exitCode = 1;
  });
}
module.exports = { measureConditionalScroll };
