const assert = require('node:assert/strict');

// The generated CSV has unique fixed-width IDs, 997 different amounts and 100
// offices. Derive order and original coordinates directly from the generator.
async function measureRowQuery(page, book, rows, meter, waitFor) {
  const ids = [];
  for (let id = 42; id < rows; id += 100) ids.push(id);
  ids.sort((a, b) => b % 997 - a % 997 || a - b);
  const expected = [0, ...ids]; // The filter retains the source header.
  const sheet = book.sheets[0].name;
  const initial = (await meter.sample('start', true)).current;
  const t = performance.now();
  const { jobId } = await page.api('/api/filter/start', {
    sheet, headerRow: 1, workbookKey: book.workbookKey,
    rules: [{ column: 3, operator: 'equals', value: 'office-42' }],
    sortKeys: [{ column: 2, mode: 'number', descending: true }, { column: 1, mode: 'text', descending: false }],
  });
  let result;
  await waitFor(async () => {
    result = await page.api('/api/filter/status?id=' + jobId);
    if (result.status === 'error' || result.status === 'cancelled') throw Error(JSON.stringify(result));
    return result.status === 'done';
  }, 'filtered multi-key row order');
  const filterAndSortMs = performance.now() - t;
  assert.equal(result.matchedRows, expected.length);
  async function read(offset, limit) {
    const params = new URLSearchParams({ sheet, filterId: result.filterId, offset, limit, col: 1, cols: 3, conditional: 0 });
    const response = await page.api('/api/sheet?' + params);
    assert.equal(response.totalRows, expected.length);
    assert.equal(response.rows.length, Math.min(limit, expected.length - offset));
    for (const [i, row] of response.rows.entries()) {
      const id = expected[offset + i];
      assert.equal(row.number, id + 1);
      const values = row.cells.map(c => c.raw);
      assert.deepEqual(values, id ? [String(id).padStart(16, '0'), (id % 997) + '.25', 'office-42'] : ['id', 'amount', 'office']);
    }
  }
  const samples = [];
  for (let i = 0; i < 16; i++) {
    const begin = performance.now();
    await read((i * 7919) % expected.length, 25);
    samples.push(performance.now() - begin);
  }
  for (let offset = 0; offset < expected.length; offset += 150) await read(offset, 150);
  const met = await meter.sample(), sorted = samples.toSorted((a, b) => a - b);
  return { sourceRows: rows, matchedDataRows: ids.length, filterAndSortMs, warmPageSamplesMs: samples,
    warmPageMedianMs: (sorted[7] + sorted[8]) / 2, warmPageP95Ms: sorted[15],
    peakServiceRSSBytes: met.peak.rssBytes, peakAdditionalDataBytes: Math.max(0, met.peak.dataBytes - initial.dataBytes),
    everyCoordinateAndRawValueVerified: true };
}
module.exports = { measureRowQuery };
