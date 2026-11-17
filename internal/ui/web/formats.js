(() => {
  "use strict";
  // Configuration comes from ui.Index and internal/formats, never a second
  // extension list. Keep this adapter independent of DOM and workbook state.
  function create(config) {
    const extensions = new Set(config.extensions);
    const sources = config.sources;
    return {
      maxRows: Math.max(...Object.values(sources).map(source => source.maxRows)),
      maxColumns: Math.max(...Object.values(sources).map(source => source.maxColumns)),
      largeDataRows: config.largeDataRows,
      queryTempBytes: config.queryTempBytes,
      richText: config.richText,
      clipboard: config.clipboard,
	  printing: config.printing,
	  record: config.record,
	  cellExplanation: config.cellExplanation,
	  profile: config.profile,
	  selectionQuick: config.selectionQuick,
	  chartExport: config.chartExport,
	  transpose: config.transpose,
	  relationExports: config.relationExports,
	  catalogue: config.catalogue,
	  dataRules: config.dataRules,
      supports(name) {
        const dot = String(name).lastIndexOf(".");
        return dot >= 0 && extensions.has(String(name).slice(dot).toLowerCase());
      },
      source(id) { return sources[id] || sources.xlsx; }
    };
  }
  window.ExcelLensFileFormats = {create};
})();
