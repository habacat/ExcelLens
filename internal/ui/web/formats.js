(() => {
  "use strict";
  // Configuration comes from ui.Index and internal/formats, never a second
  // extension list. Keep this adapter independent of DOM and workbook state.
  let cachedRaw = null, cachedInstance = null;
  function create(config) {
    const extensions = new Set(config.extensions);
    const sources = config.sources;
    return {
      raw: config,
      sources,
      maxRows: Math.max(...Object.values(sources).map(source => source.maxRows)),
      maxColumns: Math.max(...Object.values(sources).map(source => source.maxColumns)),
      largeDataRows: config.largeDataRows,
      queryTempBytes: config.queryTempBytes,
      richText: config.richText,
      clipboard: config.clipboard,
      reconcileRecordBytes: config.reconcileRecordBytes,
      printing: config.printing,
      record: config.record,
      cellExplanation: config.cellExplanation,
      profile: config.profile,
      clustering: config.clustering,
      composition: config.composition,
      selectionQuick: config.selectionQuick,
      chartExport: config.chartExport,
      transpose: config.transpose,
      snapshots: config.snapshots,
      editing: config.editing,
      relationExports: config.relationExports,
      taskRecovery: config.taskRecovery,
      catalogue: config.catalogue,
      dataRules: config.dataRules,
      analysisTree: config.analysisTree,
      supports(name) {
        const dot = String(name).lastIndexOf(".");
        return dot >= 0 && extensions.has(String(name).slice(dot).toLowerCase());
      },
      source(id) { return sources[id] || sources.xlsx; }
    };
  }
  function current() {
    const text = typeof document !== "undefined" ? document.getElementById("fileFormats")?.textContent || "" : "";
    if (cachedInstance && text === cachedRaw) return cachedInstance;
    if (!text) return null;
    cachedRaw = text;
    cachedInstance = create(JSON.parse(text));
    return cachedInstance;
  }
  window.ExcelLensFileFormats = Object.freeze({create, current});
})();
