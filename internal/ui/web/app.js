(() => {
  "use strict";

  const $ = (id) => document.getElementById(id);
  const icon = name => window.ExcelLensDisplayFormat?.icon ? window.ExcelLensDisplayFormat.icon(name) : `<svg class="icon" aria-hidden="true" focusable="false"><use href="#i-${name}"/></svg>`;
  const fileFormats = window.ExcelLensFileFormats.current?.() || window.ExcelLensFileFormats.create(JSON.parse($("fileFormats").textContent));
  window.ExcelLensClipboard?.configure(fileFormats.clipboard);
  let ROW_HEAD = 26;
  const BASE_ROW = 25;
  const MAX_BROWSER_SCROLL_PIXELS = 15000000;
  const sourceRowLimit = () => fileFormats.source(S.source?.format).maxRows;
  const colName = window.ExcelLensAddress.columnName;
  const parseColumn = text => window.ExcelLensAddress.parseColumn(text, fileFormats.maxColumns);
  const parseRef = text => window.ExcelLensAddress.parseRef(text, sourceRowLimit(), fileFormats.maxColumns);
  // The DOM cannot represent the full external row capacity in CSS. Scale only the
  // scrollbar distance; virtualized row geometry stays true to cell heights.
  const virtualScroll = new window.ExcelLensVirtualScroll(MAX_BROWSER_SCROLL_PIXELS);
  const getVirtualScrollTop = () => virtualScroll.logical($('grid').scrollTop);
  const scrollTopToPhysical = (logical) => virtualScroll.physical(logical);
  const setVirtualScrollTop = (logical) => { $('grid').scrollTop = scrollTopToPhysical(logical); };
  const BASE_COL = 132;
  const HEADER_BASE = 27;
  const motion = new window.ExcelLensVirtualScroll.Motion();
  const DOM_MARGIN_BUDGET = 2600;
  const PREFETCH_CELL_BUDGET = 12000;
  // Include a small band of cells on either side of the visible area so a
  // long string can originate off-screen and still spill over blank cells.
  // The bounded request remains independent of the full worksheet width.
  const TEXT_SPILL_LOOKAHEAD = 12;
  const MAX_ROWS_PER_REQUEST = 150;
  const MAX_COLS_PER_REQUEST = 80;
  const VIEWPORT_CACHE_BUDGET = 24 * 1024 * 1024;
  const viewport = window.ExcelLensViewport;
  const tiles = new viewport.TileCache(VIEWPORT_CACHE_BUDGET,32);
  const scheduler = new window.ExcelLensViewportScheduler(readViewportTile);
  let bytesPerCell=240, columnVisibility=null, columnVisibilityKey='';
  let foregroundLookups=0, foregroundHits=0;

  const geometry = new window.ExcelLensGeometry(fileFormats.maxRows,fileFormats.maxColumns);
  const mergedCells = new window.ExcelLensMergedCells(fileFormats.maxRows,fileFormats.maxColumns);

  const S = {
    opened: false,
    opening: false,
    sheets: [],
    styles: [],
    showStyles: true,
    showFormulas: false,
    fontOverride: "",
    activeTab: 0,
    sheet: null,
    fileName: "",
    workbookKey: "",
	book: "", rowView:null, rowVisibility:null, rowViewFailure:false,
    fileSize: 0,
    sheetEpoch: 0,
    viewportRequest: 0,
    navigationTicket:0,
    navigationAbort:null,
    viewportFrame: 0,
    fallbackFrame: 0,
    viewportActivity: null,
    viewportForce: false,
    viewportRetryTimer: 0,
    viewportRetryAttempt: 0,
    viewportData: null,
    renderedRows: new Map(),
    renderedScrollTop: 0,
    renderedHeadersKey: "",
    decorVersion: 0,
    renderMetrics: {commits:0, reusedRows:0, createdRows:0, createdCells:0, lastMs:0},
    lastHighlight: {col:0,ref:"",hadRange:false},
    index: null,
    indexJob: null,
    indexReady: false,
	previewOnly: false,
	previewAbort: null,
	previewData: null,
	previewFrame: 0,
    maxRow: 1048576,
    virtualScrollScale: 1,
    maxCol: 16384,
    usedMaxRow: 1,
    usedMaxCol: 1,
    largeData: false,
    dimension: "A1",
    filtered: false,
    filterId: "",
    filterTotal: 0,
    filtersBySheet: new Map(),
    positionsBySheet: new Map(),
    sheetViews: new Map(),
    sheetViewsBytes: 0,
    sheetLoadAbort: null,
    sheetLoadingEpoch: 0,
    sheetLayout: null,
    reusableSheetRows: null,
    reusableSheetData: null,
    // Full retained results, including frozen source rows. filterTotal is the
    // scrollable body's count after a viewport response excludes pinned rows.
    filterTotalsBySheet: new Map(),
    autoFiltersBySheet: new Map(),
    sortKeysBySheet:new Map(),
    sortHeaderBySheet:new Map(),
    lastRulesBySheet:new Map(),
    autoFilter: null,
    textHeaderRevision: 0,
    wholeRowSelected: false,
    filterJob: null,
    filterRequest: 0,
    filterCommitAbort: null,
    searchJob: null,
    selected: null,
    selectedVisualIndex: 1,
    cellMap: new Map(),
    rowNumberByVisual: new Map(),
    window: null,
    traceRefs: [],
    zoom: 100,
    searchMatches: [],
    searchMode: "value",
    freeze: {rows:0,cols:0},
    frozenCellMap: new Map(),
    searchCache: new Map(),
    searchSession: null,
    searchSelectedIndex: -1,
    searchRequestID: 0,
    visibleSearchIndexes: [],
    serverSearchHistory: [],
    history: [],
    historyIndex: -1,
    historyNavigating: false,
    range: null,
    multiRanges: [],
    rangeAnchor: null,
    columnWidths: {}, rowSizes: {}, sourceRowSizes: [], sourceColumnWidths: {}, sourceDefaultRow: 0, sourceDefaultCol: 0, sourceGridLines: true, columnSourceRanges: [], columnMode: 'original',
    columnOverrides: {}, hiddenColumns: new Set(),
    columnSelection: null, columnAnchor: 0
  };

  const cellPresenter = new window.ExcelLensCellView.Presenter({state:S,applyStyle:applyCellStyle,colName,scale:()=>scale(),fontStack,merge:activeMerge,richLimits:fileFormats.richText,numericPadding:node=>node.classList.contains('auto-filter-header')?32:S.showStyles&&fileFormats.source(S.source?.format).styled?5*scale():14});
  const frozenPresenter = new window.ExcelLensCellView.Presenter(cellPresenter.o);
  let frozenPanes, pictures;
  let autoFilterUI;
  let editCopy;
  let exportCenter, profileCenter, bookmarkCenter, formulaInspector, recentManager, multiSort, selectionStats, selectionInspector, archiveCenter, calculator, readingMode, highlightMode, traceUI, reconcileCenter, relationsCenter, namedViews, rowViews, copySpecial, catalogueCenter, dataRulesCenter, printingCenter, recordCardCenter, selectionQuick, cellExplanation, taskRecovery, transposeCenter;

  const OPERATORS = [
    ["contains", "包含"], ["notContains", "不包含"], ["equals", "等于"],
    ["notEquals", "不等于"], ["startsWith", "开头是"], ["endsWith", "结尾是"],
    ["gt", "大于"], ["gte", "大于等于"], ["lt", "小于"], ["lte", "小于等于"],
    ["blank", "为空"], ["notBlank", "非空"]
  ];

  const api=window.ExcelLensTransport.api;

  const delay = (ms) => new Promise(resolve => setTimeout(resolve, ms));
  const scale = () => S.zoom / 100;
  const snapPixel = value => {
    const ratio = window.devicePixelRatio || 1;
    return Math.round(value * ratio) / ratio;
  };
  const headerHeight = () => snapPixel(HEADER_BASE * scale());
  function updateGeometryBase(){
    // Never shrink every row merely to fit the CSS maximum scroll height.
    // The scrollbar is virtualized independently in updateCanvasSize().
    geometry.rows.setBase(S.sourceDefaultRow||BASE_ROW);
  }
  function physicalCanvasHeight(fullHeight,grid) {
    const physical=virtualScroll.configure(fullHeight,grid.clientHeight);
    S.virtualScrollScale = virtualScroll.scale;
    return physical;
  }
  // A shared rule is only continuous if both cells end and begin at the same
  // device pixel. Derive each height from the two cumulative, snapped edges.
  const sourceRowTop = row => geometry.rows.paintOffset(row,scale(),window.devicePixelRatio||1);
  const sourceRowHeight = row => sourceRowTop(row+1)-sourceRowTop(row);
  const rowTop = row => S.filtered ? snapPixel((row-1)*geometry.rows.base*scale()) : sourceRowTop(row);
  const rowHeight = (row=0) => row ? rowTop(row+1)-rowTop(row) : geometry.rows.base*scale();
  const colWidth = (col=0) => col?geometry.cols.paintSize(col,scale(),window.devicePixelRatio||1):geometry.cols.base*scale();
  const colLeft = (col) => geometry.cols.paintOffset(col,scale(),window.devicePixelRatio||1);
  const rowAtOffset = (y) => S.filtered?Math.max(1,Math.floor(y/rowHeight())+1):geometry.rows.at(Math.max(0,y)/scale());
  const colAtOffset = (x) => geometry.cols.at(Math.max(0,x)/scale());
  const rowTotal = count => rowTop(count+1);
  const rowVisible = row => !S.rowVisibility||S.rowVisibility.visible(row);
  const nextVisibleRow = row => S.rowVisibility?S.rowVisibility.next(row):Math.max(1,row);
  const previousVisibleRow = row => S.rowVisibility?S.rowVisibility.previous(row):Math.max(0,row);
  const advanceVisibleRow = (row,delta) => S.rowVisibility?S.rowVisibility.advance(row,delta):Math.max(1,Math.min(sourceRowLimit(),row+delta));
  const visibleSourceCount = (first,last) => S.rowVisibility?S.rowVisibility.count(first,last):Math.max(0,last-first+1);
  const filteredBodyCount = () => S.rowView&&S.rowView.filterId===S.filterId?Math.max(0,S.rowView.totalRows-(S.rowView.matchedPinnedRows||[]).filter(row=>row<=frozenPanes.effective().rows).length):S.filterTotal;
  const sheetHeight = count => rowTotal(count)+(S.filtered?sourceRowTop((frozenPanes?.effective().rows||0)+1):0);
  const colTotal = (count) => colLeft(count+1);
  const fmtBytes = value => window.ExcelLensDisplayFormat.bytes(value);

  // The Name Box is a range selector, not merely a single-cell jump.
  // Whole-row/column shortcuts are bounded to the used area in this read-only
  // viewer; Excel's one-million-row selection is not instantiated in the DOM.
  function parseNameAddress(value) {
    let text=String(value||'').trim(), requestedSheet='';
    const qualified=text.match(/^(?:'((?:[^']|'')+)'|([^'!]+))!(.+)$/);
    if(qualified){requestedSheet=(qualified[1]||qualified[2]).replaceAll("''","'");text=qualified[3].trim();}
    text=text.replaceAll('$','').toUpperCase();
    let first,last;
    const parts=text.split(':');
    if(parts.length===1){first=parseRef(parts[0]);last=first;}
    else if(parts.length===2){
      first=parseRef(parts[0]);last=parseRef(parts[1]);
      if(!first||!last){
        if(parts.every(p=>/^[A-Z]{1,3}$/.test(p))){
          const c1=parseColumn(parts[0]),c2=parseColumn(parts[1]);
          if(c1&&c2){first=parseRef(`${colName(c1)}1`);last=parseRef(`${colName(c2)}${S.usedMaxRow}`);}
        }else if(parts.every(p=>/^[1-9][0-9]*$/.test(p))){
          const r1=Number(parts[0]),r2=Number(parts[1]);
          if(r1<=sourceRowLimit()&&r2<=sourceRowLimit()){first=parseRef(`A${r1}`);last=parseRef(`${colName(S.usedMaxCol)}${r2}`);}
        }
      }
    }
    return first&&last?{sheet:requestedSheet,start:first,end:last}:null;
  }

  async function navigateNameBox(value) {
    const parsed=parseNameAddress(value);
    if(!parsed){
	  if(catalogueCenter&&fileFormats.source(S.source?.format).catalogueMetadata){await catalogueCenter.open(value);return;}
	  toast('地址无效：支持 A1、A1:D50、B:B、3:5、Sheet1!A1:B9','warning');return;
	}
    let sheet=S.sheet;
    if(parsed.sheet){
      const target=S.sheets.find(item=>item.name.toLocaleLowerCase()===parsed.sheet.toLocaleLowerCase());
      if(!target){toast(`找不到工作表：${parsed.sheet}`,'error');return;}
      sheet=target.name;
    }
    const multiple=parsed.start.ref!==parsed.end.ref;
    if(S.filtered&&sheet===S.sheet&&multiple){toast('筛选视图请按可见行拖选；名称框矩形范围使用源行号','warning');return;}
    if(!await navigateToCell(sheet,parsed.start.ref))return;
    const first=activeMerge(parsed.start.row,parsed.start.col);
    if(sheet!==S.sheet || S.selected?.ref!==(first ? colName(first.col)+first.row : parsed.start.ref))return;
    if(multiple){
      if(S.filtered){toast('筛选视图不支持直接按源行号选中矩形；请清除筛选后重试','warning');return;}
      extendRange({row:parsed.start.row,col:parsed.start.col,visual:parsed.start.row},parsed.end.row,parsed.end.col);
      const points=window.ExcelLensMultiSelection.points(S.range,colName);
      $('nameBox').value=`${points.start}:${points.end}`;
    }
  }

  function setStatus(text) {
    const el=$("globalStatus");
    el.textContent=text;
    // The bottom-left sheet status already identifies the open worksheet.
    // Keep the top bar available only for transient opening/error states.
    el.hidden=/^已打开\s*·/.test(text) || (S.indexReady && /^等待选择文件$/.test(text));
  }

  function toast(message, tone='info') {
    const node = $("toast");
    node.dataset.tone = ['info','success','warning','error'].includes(tone) ? tone : 'info';
    node.setAttribute('role',node.dataset.tone==='error'?'alert':'status');
    node.textContent = String(message ?? '');
    node.hidden = false;
    window.ExcelLensMotion?.enter(node,{duration:140});
    clearTimeout(toast.timer);
    toast.timer = setTimeout(() => { node.hidden = true; }, 5200);
  }

  // Commands that only need an open workbook.
  const workbookCommands = ['copyMenuBtn','selectionQuickBtn','transposeOpenBtn'];

  function renderSourceIntegrity() {
    const notice = $("sourceIntegrityNotice");
    const integrity = S.source.archiveNotice || (S.source.incomplete ? S.source.notice || "当前数据为不完整片段，不能代表全量账目。" : "");
    const message = [integrity, S.source.presentationNotice].filter(Boolean).join('；');
    $("sourceIntegrityMessage").textContent = message;
    $("retainDatabaseSnapshot").hidden = !S.source.databaseSnapshot?.legacy || S.source.databaseSnapshot.retained;
    notice.hidden = !message;
  }

  function showWorkspace() {
    renderSourceIntegrity();
    document.body.classList.remove("home-view");
    $("welcome").hidden = true;
    $("workspace").hidden = false;
    $("searchOpenBtn").disabled = false;
    $("filterOpenBtn").disabled = false;
    $("multiSortBtn").disabled = false;
    $("inspectOpenBtn").disabled = false;
    $("duplicateOpenBtn").disabled = false;
    $("formulaViewBtn").disabled = false;
    $("advancedFilterBtn").disabled = false;
    $("freezeBtn").disabled = false;
    $("formatBtn").disabled = false;
    $("exportOpenBtn").disabled = false;
    $("profileOpenBtn").disabled = false;
    $("bookmarkOpenBtn").disabled = false;
    $("columnsBtn").disabled = false;
    $('rowsBtn').disabled=false;
    for (const id of workbookCommands) $(id).disabled = false;
    editCopy?.availability(true);
  }

  function showWelcome() {
    if (editCopy?.grid.active) editCopy.grid.leave();
    editCopy?.availability(false);
    $("sourceIntegrityNotice").hidden = true;
    pictures?.close();
    document.body.classList.add("home-view");
    $("workspace").hidden = true;
    $("welcome").hidden = false;
    $('taskRecoveryHome').hidden = true;
    taskRecovery?.refreshHome();
    $("searchOpenBtn").disabled = true;
    $("filterOpenBtn").disabled = true;
    $("textHeaderBtn").disabled = true;
    $("multiSortBtn").disabled = true;
    $("inspectOpenBtn").disabled = true;
    $("duplicateOpenBtn").disabled = true;
    $("formulaViewBtn").disabled = true;
    selectionInspector?.close();
    $("advancedFilterBtn").disabled = true;
    $("freezeBtn").disabled = true;
    $("formatBtn").disabled = true;
    $("exportOpenBtn").disabled = true;
    $("profileOpenBtn").disabled = true;
    $("bookmarkOpenBtn").disabled = true;
    $("clearFilterBtn").disabled = true;
    $("columnsBtn").disabled = true;
    $('rowsBtn').disabled=true;
    for (const id of workbookCommands) $(id).disabled = true;
    hideColumnsMenu();
  }

  // Each visual style is resolved once by Go and delivered in a compact palette.
  // Applying styles to only the ~visible cells preserves virtual scroll speed.
  async function loadStylePalette() {
    try {
      const response = await api("/api/styles");
      S.styles = Array.isArray(response.styles) ? response.styles : [];
      if(response.richTextNotice)toast(response.richTextNotice,'warning');
    } catch (error) {
      S.styles = [];
      toast(`原始格式暂不可用：${error.message}`,'error');
    }
  }

  // Windows ships these font families by different Chinese and Latin names.
  // Never bundle proprietary font binaries; select installed local faces and
  // use a CJK-capable fallback when the original font is unavailable.
  const FONT_STACKS = Object.freeze({
    "宋体": '"SimSun", "宋体", "Songti SC", "Noto Serif CJK SC", serif',
    "新宋体": '"NSimSun", "新宋体", "SimSun", "宋体", serif',
    "楷体": '"KaiTi", "楷体", "Kaiti SC", "STKaiti", serif',
    "仿宋": '"FangSong", "仿宋", "STFangsong", serif',
    "微软雅黑": '"Microsoft YaHei", "微软雅黑", "Microsoft YaHei UI", sans-serif',
    "等线": '"DengXian", "等线", "Microsoft YaHei", sans-serif',
    "黑体": '"SimHei", "黑体", "Microsoft YaHei", sans-serif',
    "华文宋体": '"STSong", "华文宋体", "SimSun", serif',
    "华文楷体": '"STKaiti", "华文楷体", "KaiTi", serif',
    "华文仿宋": '"STFangsong", "华文仿宋", "FangSong", serif',
    "隶书": '"LiSu", "隶书", "SimSun", serif',
    "Arial": 'Arial, "Microsoft YaHei", sans-serif',
    "Microsoft Sans Serif": '"Microsoft Sans Serif", "SimSun", "宋体", sans-serif',
    "Carlito": 'Carlito, Calibri, "Microsoft YaHei", sans-serif',
    "Calibri": 'Calibri, Carlito, "Microsoft YaHei", sans-serif',
    "Consolas": 'Consolas, "Microsoft YaHei", monospace'
  });
  const FONT_ALIASES = Object.freeze({SimSun:"宋体", NSimSun:"新宋体", KaiTi:"楷体", FangSong:"仿宋", DengXian:"等线", SimHei:"黑体", "Microsoft YaHei":"微软雅黑", "Microsoft YaHei UI":"微软雅黑", STKaiti:"华文楷体", STSong:"华文宋体", STFangsong:"华文仿宋", LiSu:"隶书"});
  function fontStack(value) {
    const name = String(value || "").trim().slice(0, 96);
    if (!name) return "";
    const normalized = FONT_ALIASES[name] || name;
    if (FONT_STACKS[normalized]) return FONT_STACKS[normalized];
    return `"${name.replace(/["\\\n\r]/g, "")}", "Microsoft YaHei", "Noto Sans CJK SC", sans-serif`;
  }

  function applyDisplayFont(node, sourceName) {
    const chosen = S.fontOverride || (S.showStyles ? sourceName : "");
    if (chosen) node.style.fontFamily = fontStack(chosen);
    else node.style.removeProperty("font-family");
  }

  function repaintDisplayFonts() {
    // Paint keys include S.fontOverride; renderGridFromCurrentWindow() and
    // frozenPanes.schedule(true) repaint every visible cell and text spill once.
    renderGridFromCurrentWindow();
    frozenPanes?.schedule(true);
  }

  function setDisplayFontSelection(font) {
    S.fontOverride = font;
    const picker = $("fontSelect");
    picker.value = font;
    picker.title = `显示字体（${picker.selectedOptions[0]?.text || font}）`;
  }

  async function changeDisplayFont(font) {
    if (!S.indexReady || !S.sheet || !(font in FONT_STACKS) && font !== "") return;
    setDisplayFontSelection(font);
    repaintDisplayFonts();
    const sheet = S.sheet;
    try {
      await api("/api/layout", { method:"POST",headers:{"Content-Type":"application/json"},
        body:JSON.stringify({sheet,kind:"font",font}) });
    } catch (err) { toast(`字体设置保存失败：${err.message}`,'error'); }
  }

  const STYLE_ALIGN = {left:"flex-start", center:"center", right:"flex-end", justify:"space-between", distributed:"space-around", centerContinuous:"center"};
  const STYLE_VERTICAL = {top:"flex-start", center:"center", bottom:"flex-end"};

  // Cache the style declarations of fresh cells. Existing nodes can also be repainted
  // by edit/print callers; preserve their geometry and other inline declarations.
  const cellStyleCache=new Map();
  function applyCellStyle(node,cell){
    if(!cell||node.style.cssText||cell.conditional){writeCellStyle(node,cell);return;}
    const numeric=cell.type==='n'||(!cell.type&&cell.raw&&Number.isFinite(Number(cell.raw)));
    const key=[cell.style||0,S.showStyles,S.zoom,S.fontOverride,cell.valueColor||'',Boolean(numeric)].join('|');
    let css=cellStyleCache.get(key);
    if(css===undefined){
      writeCellStyle(node,cell);css=node.style.cssText;
      if(cellStyleCache.size>=512)cellStyleCache.clear();
      cellStyleCache.set(key,css);
    }else if(css)node.style.cssText=css;
  }
  function writeCellStyle(node, cell) {
    const p = node.style;
    // Recycled nodes can move from a conditional/explicit property to its
    // absence or false. Reset presentation only; keep geometry and seams.
    for(const property of ['--xl-fill','--xl-color'])p.removeProperty(property);
    for(const property of ['fontSize','fontWeight','fontStyle','textDecoration','textDecorationStyle','textUnderlinePosition','justifyContent','alignItems','textAlign','whiteSpace','wordBreak'])p[property]='';
    if (!cell) { applyDisplayFont(node, ""); return; }
    const visual = cellPresenter.style(cell);
    if (!S.showStyles || !visual) { applyDisplayFont(node, ""); return; }
    if (visual.background) p.setProperty("--xl-fill", visual.background);
    const ink=window.ExcelLensConditional?.color(cell,visual)??(cell.valueColor||visual.color);
    if (ink) p.setProperty("--xl-color", ink);
    applyDisplayFont(node, visual.fontName);
    if (visual.fontSize) p.fontSize = `${visual.fontSize * (4 / 3) * S.zoom / 100}px`;
    if (visual.bold) p.fontWeight = "700";
    if (visual.italic) p.fontStyle = "italic";
    if (visual.underline || visual.strike) p.textDecoration = [visual.underline && "underline", visual.strike && "line-through"].filter(Boolean).join(" ");
    const underline=cell.conditional?.style?.font?.underline||visual.underlineStyle;
    if(underline==='double'||underline==='doubleAccounting')p.textDecorationStyle='double';
    if(/Accounting$/.test(underline||''))p.textUnderlinePosition='under';
    if (STYLE_ALIGN[visual.horizontal]) {
      p.justifyContent = STYLE_ALIGN[visual.horizontal];
      if (visual.wrap) p.textAlign = visual.horizontal === 'centerContinuous' ? 'center' : visual.horizontal;
    }
    else if ((!visual.horizontal || visual.horizontal === "general") && (cell.type === "n" || (!cell.type && cell.raw && Number.isFinite(Number(cell.raw))))) p.justifyContent = "flex-end";
    if (STYLE_VERTICAL[visual.vertical]) p.alignItems = STYLE_VERTICAL[visual.vertical];
    if (visual.wrap) {p.whiteSpace = "normal"; p.wordBreak = "break-word";}
  }

  // Each seam belongs to one DOM cell (the left/top owner). The neighboring
  // Excel style may specify its *left* or *top* border: that color still wins
  // over the faint default line, without drawing a second stroke.
  const visualForBorder=(cell,row)=>S.showStyles?(window.ExcelLensConditional?.visual(cell,S.styles,row?.style)??S.styles[Number(cell?.style??row?.style)||0]??{}):{};
  function mergedVisualBorder(own,range,models=S.cellMap){
    const at=(r,c)=>visualForBorder(models.get(colName(c)+r)||S.frozenCellMap.get(colName(c)+r));
    const tr=at(range.row,range.endCol),bl=at(range.endRow,range.col),br=at(range.endRow,range.endCol);
    // XLS/XLSX may put the perimeter on styled covered cells rather than the
    // anchor. Read the outer corners; never turn interior edges into seams.
    return {...own,borderTop:own.borderTop||tr.borderTop,borderLeft:own.borderLeft||bl.borderLeft,borderRight:tr.borderRight||br.borderRight||own.borderRight,borderBottom:bl.borderBottom||br.borderBottom||own.borderBottom};
  }
  function updateSharedBorders(node,cell,row,rightCell,rightRow,belowCell,belowRow,edge={}){
    const r=Number(node.dataset.row),c=Number(node.dataset.col),merge=edge.mergeFree?null:activeMerge(r,c),anchor=merge&&merge.row===r&&merge.col===c;
    if (merge && !anchor) return;
    const borders=window.ExcelLensGridBorders;
    if (!S.showStyles) {
      borders.paint(node,'right','',rowTop);
      borders.paint(node,'bottom','',colLeft);
      if (node._xlOuterLeft) { node.style.removeProperty('border-left'); node._xlOuterLeft=''; }
      if (node._xlOuterTop) { node.style.removeProperty('border-top'); node._xlOuterTop=''; }
      return;
    }
    if(edge.mergeFree&&edge.nextCol&&belowRow&&!edge.outerTop&&!edge.outerLeft){
      const own=visualForBorder(cell,row),right=visualForBorder(rightCell,rightRow),below=visualForBorder(belowCell,belowRow);
      const x=borders.choose(own.borderRight,right.borderLeft)||(own.background||right.background?'0px':'');
      const y=borders.choose(own.borderBottom,below.borderTop)||(own.background||below.background?'0px':'');
      borders.paint(node,'right',x,rowTop,y);borders.paint(node,'bottom',y,colLeft,x);
      if(node._xlOuterLeft){node.style.removeProperty('border-left');node._xlOuterLeft='';}
      if(node._xlOuterTop){node.style.removeProperty('border-top');node._xlOuterTop='';}
      return;
    }
    const models=edge.cellMap||S.cellMap;
    const own=anchor?mergedVisualBorder(visualForBorder(cell,row),merge,models):visualForBorder(cell,row);
    const endRow=merge?.endRow??r,endCol=merge?.endCol??c;
    const rightCol=anchor ? (edge.columns?.find(col=>col>endCol) ?? endCol+1) : (edge.nextCol||c+1);
    const under=!anchor&&belowRow ? belowRow.number : (nextVisibleRow(endRow+1)||endRow+1);
    const opposite=(nr,nc,data,direct)=>{
      const range=edge.mergeFree?null:activeMerge(nr,nc);
      if (!range && direct!==undefined) return visualForBorder(direct,data);
      const row=range?.row??nr,col=range?.col??nc,ref=colName(col)+row;
      const record=data?.number===row ? data : edge.rows?.get(row);
      const model=(row===nr&&col===nc?direct:null)||models.get(ref)||S.frozenCellMap.get(ref)||record?.cells?.find(cell=>cell.col===col);
      const visual=visualForBorder(model,record);
      return range?mergedVisualBorder(visual,range,models):visual;
    };
    // Always paint an interior seam on the trailing edge of its predecessor.
    // Painting inside the next merge's leading edge shifts it a device pixel.
    const directRight=endCol===c&&edge.nextCol ? (rightCell??null) : undefined;
    const directBelow=endRow===r&&belowRow ? (belowCell??null) : undefined;
    // A source fill hides the default gridline on either side of the seam.
    // Explicit borders still win, including ones contributed by a merged neighbour.
    const seam=(primary,neighbor,side)=>borders.choose(primary,neighbor[side]) || (own.background || neighbor.background ? '0px' : '');
    const rightAt=rr=>seam(own.borderRight,opposite(rr,rightCol,
      rr===r?rightRow:null,rr===r?directRight:undefined),'borderLeft');
    const bottomAt=cc=>seam(own.borderBottom,opposite(under,cc,
      belowRow,cc===c?directBelow:undefined),'borderTop');
    const fallback=own.background?'0px':'';
    const right=endRow===r?rightAt(r):borders.runs(r,endRow,edge.lastRow??endRow,nextVisibleRow,rightAt,own.borderRight||fallback,edge.firstRow);
    const bottom=endCol===c?bottomAt(c):borders.runs(c,endCol,edge.columns?.at(-1)??endCol,i=>i,bottomAt,own.borderBottom||fallback,edge.columns?.[0]);
    const left=edge.outerLeft&&colLeft(c)===0?own.borderLeft||'':'';
    const top=edge.outerTop?own.borderTop||'':'';
    borders.paint(node,'right',right,rowTop,top||bottom);
    borders.paint(node,'bottom',bottom,colLeft,left||right);
    if(node._xlOuterLeft!==left){node._xlOuterLeft=left;
      if(left)node.style.borderLeft=left;else node.style.removeProperty('border-left');}
    if(node._xlOuterTop!==top){node._xlOuterTop=top;
      if(top)node.style.borderTop=top;else node.style.removeProperty('border-top');}
  }

  function toggleOriginalFormat() {
    S.showStyles = !S.showStyles;
    updateSourceAppearance();
    $("formatBtn").classList.toggle("format-active", S.showStyles);
    $("formatBtn").setAttribute("aria-pressed", String(S.showStyles));
    // Repaint the existing data; changing visual formatting must never cause
    // another file read or flash a blank viewport.
    resetGridDOM();
    renderGridFromCurrentWindow();
    frozenPanes.schedule(true);
  }

  function updateSourceAppearance(){
    $('workspace').classList.toggle('source-sheet-view',S.showStyles&&fileFormats.source(S.source?.format).styled);
    $('workspace').classList.toggle('source-no-gridlines',S.showStyles&&!S.sourceGridLines);
    $('workspace').classList.toggle('source-html-view',S.showStyles&&S.source?.format==='xls-html');
  }

  function renderSheetTabs() {
    const tabs = $("sheetTabs");
    const fragment = document.createDocumentFragment();
    S.sheets.forEach((sheet, index) => {
      const button = document.createElement("button");
      button.type = "button";
      button.className = "sheet-tab" + (S.sheet === sheet.name ? " active" : "") + (sheet.state && sheet.state !== "visible" ? " hidden-sheet" : "");
      if (/^#[0-9A-Fa-f]{6}$/.test(sheet.tabColor || "")) {
        button.classList.add("has-tab-color");
        button.style.setProperty("--sheet-tab-color", sheet.tabColor);
      }
      button.textContent = sheet.name;
      button.title = (sheet.sourceName || sheet.name) + (sheet.state && sheet.state !== "visible" ? "（隐藏工作表）" : "");
      button.setAttribute("role", "tab");
      button.setAttribute("aria-selected", S.sheet === sheet.name ? "true" : "false");
      button.addEventListener("click", () => selectSheetTab(sheet.name));
      fragment.appendChild(button);
      if (index === S.activeTab) button.dataset.activeIndex = String(index);
    });
    tabs.replaceChildren(fragment);
    const active = tabs.querySelector(".sheet-tab.active");
    if (active) active.scrollIntoView({ block: "nearest", inline: "nearest" });
    requestAnimationFrame(updateSheetPager);
  }

  function updateSheetPager() {
    const tabs=$("sheetTabs");
    const hasOverflow=tabs.scrollWidth>tabs.clientWidth+2;
    $("sheetPrevBtn").hidden=!hasOverflow;
    $("sheetNextBtn").hidden=!hasOverflow;
    $("sheetPrevBtn").disabled=tabs.scrollLeft<=1;
    $("sheetNextBtn").disabled=tabs.scrollLeft>=tabs.scrollWidth-tabs.clientWidth-2;
  }
  function pageSheetTabs(direction) {
    const tabs=$("sheetTabs");
    const reduced=window.matchMedia?.('(prefers-reduced-motion: reduce)').matches;
    tabs.scrollBy({left:direction*Math.max(110,Math.round(tabs.clientWidth*.72)),behavior:reduced?'auto':'smooth'});
  }

  function renderSheetPicker(query = "") {
    const list = $("sheetPickerList");
    const q = query.trim().toLocaleLowerCase();
    const fragment = document.createDocumentFragment();
    let shown = 0;
    S.sheets.forEach((sheet, index) => {
      if (q && !sheet.name.toLocaleLowerCase().includes(q)) return;
      const button = document.createElement("button");
      button.type = "button";
      button.className = "sheet-picker-item" + (S.sheet === sheet.name ? " active" : "");
      const number = document.createElement("span");
      number.className = "sheet-number";
      number.textContent = String(index + 1).padStart(2, "0");
      const name = document.createElement("span");
      name.textContent = sheet.name;
      if (/^#[0-9A-Fa-f]{6}$/.test(sheet.tabColor || "")) {
        const swatch=document.createElement('span');swatch.className='sheet-picker-color';
        swatch.style.backgroundColor=sheet.tabColor;
        button.append(number,swatch,name);
      } else button.append(number,name);
      button.addEventListener("click", async () => {
        $("sheetDialog").close();
      await selectSheetTab(sheet.name);
      });
      fragment.appendChild(button);
      shown++;
    });
    if (!shown) {
      const empty = document.createElement("div");
      empty.className = "dialog-summary";
      empty.textContent = "没有匹配的工作表";
      fragment.appendChild(empty);
    }
    list.replaceChildren(fragment);
  }

  function updateFileTitle() {
    $('fileBadge').textContent=S.fileName;
    $('fileBadge').title=S.fileName;
    document.title=S.fileName||'ExcelLens';
  }
  async function loadStatus() {
    const launch=window.ExcelLensSession?.nativeLaunch || window.ExcelLensSession?.restoring;
    const activity=launch ? globalThis.ExcelLensActivity?.begin('正在打开文件',{priority:2}) : null;
    if(launch){
      S.opening=true;
      $('openBtn').disabled=$('chooseFileBtn').disabled=true;
      activity?.stage('正在打开文件',0,60);
    }
    try {
      const data = await api("/api/status");
      archiveCenter?.use(data.archive);
      if (!data.opened) {
        if(data.archive)archiveCenter.present(data.archive);
        S.opened = false;
	  catalogueCenter?.resetBook();
	  dataRulesCenter?.resetBook();printingCenter?.resetBook();recordCardCenter?.resetBook();selectionQuick?.resetBook();transposeCenter?.resetBook();cellExplanation?.resetBook();
        showWelcome();
        recentManager?.load();
        if (data.error) {
          $("welcomeError").textContent = data.error;
          $("welcomeError").hidden = false;
          setStatus("打开失败");
        } else setStatus("等待选择文件");
        return;
      }
      S.opened = true;
      activity?.stage('正在准备工作表',60,65);
      S.sheets = data.sheets || [];
      S.activeTab = Number.isInteger(data.activeTab) ? data.activeTab : 0;
      S.fileName = data.fileName || "";
      S.book=data.book||'';
      S.fileSize = data.fileSize || 0;

      S.source = data.source || {format:"xlsx"};
      if(S.workbookKey!==(data.workbookKey||''))bookmarkCenter?.resetBook();
      S.workbookKey=data.workbookKey||'';calculator?.bookChanged();window.ExcelLensAnalytics?.bookChanged(S.workbookKey,S.book);
	  catalogueCenter?.controls();
	  dataRulesCenter?.controls();printingCenter?.controls();recordCardCenter?.controls();selectionQuick?.changed();transposeCenter?.changed();cellExplanation?.changed();profileCenter?.changed();
      updateFileTitle();
      showWorkspace();
      await loadStylePalette();
      const launchParams=new URLSearchParams(window.location?.search||'');
      const requested=launchParams.get('sourceSheet');
      const active = S.sheets.find(sheet=>sheet.name===requested)||S.sheets[S.activeTab]||S.sheets[0];
      if (active) await selectSheet(active.name,requested===active.name?(launchParams.get('sourceRef')||''):'', false, null, activity);
      else { renderSheetTabs(); setStatus("工作簿中没有工作表"); }
      bookmarkCenter.load(true).catch(() => {});
      refreshSearchHistory().catch(() => {});
    } catch (error) {
      showWelcome();
      recentManager?.load();
      setStatus("本机服务连接失败");
      toast(error.message,'error');
    } finally {
      activity?.end();
      if(launch){finishOpening();$('openBtn').disabled=$('chooseFileBtn').disabled=false;}
    }
  }

  // Panels that stay unavailable while a file opens are refreshed when it ends;
  // otherwise their buttons remain disabled until the next sheet switch.
  function finishOpening(){
    S.opening=false;
    dataRulesCenter?.controls();printingCenter?.controls();recordCardCenter?.controls();
    selectionQuick?.changed();cellExplanation?.changed();profileCenter?.changed();
  }

  function chooseFile() {
    if (!S.opening&&canOpenWorkbook()) $("fileInput").click();
  }

  function canOpenWorkbook(action='切换文件'){
    if (S.opening) return false;
    if (editCopy?.grid.active && editCopy.grid.blocked) {
      toast('请先确认当前单元格的输入'); return false;
    }
    if(window.ExcelLensAnalytics?.hasUnsaved()){
      toast(`分析表仍有未保存内容，请先在分析中重试保存，再${action}`,'warning');return false;
    }
    return true;
  }

  async function openWorkbook(load, name = '', size = 0, current = () => true) {
    if (S.opening || !canOpenWorkbook()) return false;
    S.opening = true;
    $("openBtn").disabled = $("chooseFileBtn").disabled = $("homeBtn").disabled = true;
    $("welcomeError").hidden = true;
    setStatus("正在打开文件");
    const activity = window.ExcelLensActivity?.begin('正在打开文件', {priority:2});
    activity?.stage('正在打开文件', 0, 60);
    try {
      await window.ExcelLensSession?.ready;
      const data = await load(activity);
      if (!current()) return false;
      activity?.stage('正在准备工作表', 60, 65);
      await acceptOpenedWorkbook(data, {fallbackName:name, fallbackSize:size, activity});
      await activity?.paintIfDue?.();
      return true;
    } catch (error) {
      if (!current()) return false;
      $("welcomeError").textContent = error.message;
      $("welcomeError").hidden = false;
      setStatus("打开失败");
      throw error;
    } finally {
      activity?.end();
      finishOpening();
      $("openBtn").disabled = $("chooseFileBtn").disabled = $("homeBtn").disabled = false;
    }
  }

  async function uploadFile(file) {
    if (!file) return;
    await window.ExcelLensWorkbookReady;
    if (S.opening) return;
    if (!fileFormats.supports(file.name)) { toast("请选择受支持的数据文件或压缩包",'warning'); return; }
    return openWorkbook(activity => window.ExcelLensTransport.upload(file,activity,fmtBytes), file.name, file.size).catch(error => toast(error.message,'error'));
  }

  async function acceptOpenedWorkbook(data, {fallbackName = "", fallbackSize = 0, activity, revisionRef = ""} = {}) {
      if (editCopy?.grid.active) editCopy.grid.leave();
      archiveCenter?.use(data.archive);
      if(data.archiveOnly){archiveCenter.present(data.archive);setStatus("请选择压缩包内的文件");return;}
      S.opened = true;
      S.sheets = data.sheets || [];
      S.activeTab = Number.isInteger(data.activeTab) ? data.activeTab : 0;
      S.fileName = data.fileName || fallbackName;
      S.fileSize = data.fileSize || fallbackSize;
      ++S.sheetEpoch;
      S.sheet = null;
      S.indexReady = false;
      S.index = null;
      // Only a confirmed workbook handoff discards the old cells. In
      // particular, don't lay out the previous grid again on leaving Home.
      invalidateViewport();
      frozenPanes?.reset();
      searchPanel.resetWorkbook();
      traceUI?.reset();
      formulaInspector?.resetWorkbook();
      selectionInspector?.resetWorkbook();
      profileCenter?.resetBook();
      exportCenter?.resetBook();
      reconcileCenter?.resetBook();
      relationsCenter?.resetBook();
      namedViews?.resetBook();
      catalogueCenter?.resetBook();
      dataRulesCenter?.resetBook();printingCenter?.resetBook();recordCardCenter?.resetBook();selectionQuick?.resetBook();transposeCenter?.resetBook();cellExplanation?.resetBook();profileCenter?.resetBook();
      rowViews?.resetBook();
      if(S.filterJob)cancelFilter(S.filterJob);
      S.history = [];
      S.historyIndex = -1;
      S.filtersBySheet.clear();
      S.positionsBySheet.clear();
      S.sheetViews.clear();S.sheetViewsBytes=0;
      S.filterTotalsBySheet.clear();
      S.autoFiltersBySheet.clear();
      bookmarkCenter.resetBook();
      S.autoFilter = null;
      S.sortKeysBySheet.clear();
      S.sortHeaderBySheet.clear();
      S.lastRulesBySheet.clear();
      selectionStats?.resetBook();
      S.filterId = "";
      S.filtered = false;

      S.source = data.source || {format:"xlsx"};
      S.workbookKey=data.workbookKey||'';calculator?.bookChanged();
      S.book=data.book||'';
	  catalogueCenter?.controls();
	  dataRulesCenter?.controls();printingCenter?.controls();recordCardCenter?.controls();selectionQuick?.changed();transposeCenter?.changed();cellExplanation?.changed();profileCenter?.changed();
      window.ExcelLensAnalytics?.bookChanged(S.workbookKey,S.book);
      updateFileTitle();
      showWorkspace();
      await loadStylePalette();
      const active = S.sheets[S.activeTab] || S.sheets[0];
      if (active) await selectSheet(active.name, revisionRef, false, null, activity);
      else { renderSheetTabs(); setStatus("工作簿中没有工作表"); }
      bookmarkCenter.load(true).catch(() => {});
      refreshSearchHistory().catch(() => {});
  }

  async function openTaskSource(payload,current){
    return openWorkbook(() => api('/api/tasks/open',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(payload)}), '', 0, current);
  }

  async function cancelIndex(jobID) {
    if (!jobID) return;
    try { await api("/api/index/cancel", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ id: jobID }) }); }
    catch { /* job may have finished */ }
  }

  const sheetPresentationKey=()=>[S.zoom,S.showStyles,S.showFormulas,window.devicePixelRatio||1].join('|');
  function rememberSheetView(){
    if (editCopy?.grid.active) return;
    if(!S.sheet||!S.indexReady||!S.sheetLayout||!S.viewportData||S.filterJob)return;
    const layout={...S.sheetLayout,rows:S.rowSizes,cols:S.columnWidths,font:S.fontOverride,
      columnMode:S.columnMode,columnVisibility:S.columnOverrides};
    const frozen=new Map([...(frozenPanes?.cache||[])].slice(-3));
    let nodes=S.renderedRows.size,retainedText=0;
    const textBytes=cell=>2*((cell?.raw?.length||0)+(cell?.display?.length||0)+(cell?.formula?.length||0));
    for(const row of S.renderedRows.values()){
      nodes+=row._xlCells?.size||0;
      for(const cell of row._xlCells?.values()||[]) {
        nodes+=(cell.childElementCount||0)+(cell._xlRichNode?.childNodes.length||0)*2;
        nodes+=(cell._xlborderRightParts?.childElementCount||0)+(cell._xlborderBottomParts?.childElementCount||0);
        retainedText+=textBytes(cell._xlModel)+window.ExcelLensRichText.estimate(cell._xlModel?.richText)+(window.ExcelLensConditional?.estimate(cell._xlModel?.conditional)||0);
      }
    }
    const bytes=estimateViewportBytes(S.viewportData.data)+[...frozen.values()].reduce((n,data)=>n+estimateViewportBytes(data),0)+
      512+nodes*1024+retainedText+textBytes(S.selected)+window.ExcelLensRichText.estimate(S.selected?.richText)+(window.ExcelLensConditional?.estimate(S.selected?.conditional)||0)+(layout.sourceMerges?.length||0)*224+(layout.sourceRows?.length||0)*128+
      (Object.keys(layout.rows||{}).length+Object.keys(layout.cols||{}).length+Object.keys(layout.sourceCols||{}).length)*96;
    const old=S.sheetViews.get(S.sheet);if(old)S.sheetViewsBytes-=old.bytes;
    S.sheetViews.delete(S.sheet);
    if(bytes>16*1024*1024)return;
    S.sheetViews.set(S.sheet,{bytes,index:S.index,layout,freeze:{...S.freeze},entry:S.viewportData,
      frozen,selected:S.selected,filterId:S.filterId,maxRow:S.maxRow,maxCol:S.maxCol,
      rows:S.renderedRows,presentation:sheetPresentationKey()});
    S.sheetViewsBytes+=bytes;
    while(S.sheetViews.size>6||S.sheetViewsBytes>16*1024*1024){
      const key=S.sheetViews.keys().next().value;S.sheetViewsBytes-=S.sheetViews.get(key).bytes;S.sheetViews.delete(key);
    }
  }

  function finishSheetSwitch(epoch,ready=true){
    if(epoch!==S.sheetEpoch)return;
    S.sheetLoadingEpoch=0;
    $('grid').inert=!ready;
    $('grid').parentElement.classList.remove('sheet-switching');
    editCopy?.grid.controls();
    if (ready && editCopy?.grid.active) { editCopy.grid.selection(); editCopy.grid.entry.focusAfterSwitch(); }
	if(typeof catalogueCenter!=='undefined')catalogueCenter?.controls();
	if(typeof dataRulesCenter!=='undefined')dataRulesCenter?.controls();if(typeof printingCenter!=='undefined')printingCenter?.controls();recordCardCenter?.controls();selectionQuick?.changed();transposeCenter?.changed();cellExplanation?.changed();profileCenter?.changed();
  }

  function selectSheetTab(name) {
    const editor=editCopy?.grid, draft=editor?.active ? editor.entry.switchDraft(name) : null;
    return selectSheet(name,'',false,null,null,draft);
  }
  async function selectSheet(name, targetRef = "", preserveTrace = false, navigationGuard = null, openingActivity = null, retainedDraft = null) {
    if (editCopy?.grid.active && editCopy.grid.draft && editCopy.grid.draft !== retainedDraft && !await editCopy.grid.act()) return false;
    if(navigationGuard&&!navigationGuard())return;
    if (!name || !S.opened) return;
    if (S.sheet === name && S.indexReady) {
      return targetRef ? navigateToCell(name, targetRef, preserveTrace, navigationGuard) : true;
    }
	if(typeof dataRulesCenter!=='undefined')dataRulesCenter?.resetBook();if(typeof printingCenter!=='undefined')printingCenter?.resetBook();recordCardCenter?.resetBook();selectionQuick?.resetBook();transposeCenter?.resetBook();cellExplanation?.resetBook();profileCenter?.resetBook();
    if (S.sheet && S.indexReady) {
      const grid=$('grid');
      S.positionsBySheet.set(S.sheet,{top:getVirtualScrollTop(),left:grid.scrollLeft,ref:S.selected?.ref||'A1',visual:S.selectedVisualIndex,frozenThrough:S.selectedFrozenThrough});
      rememberSheetView();
    }
    const previousView=S.positionsBySheet.get(name);
    if(preserveTrace)traceUI?.clearPulse();else traceUI?.selectionChanged();
    let warm=S.sheetViews.get(name);
    if(warm&&warm.filterId!==(S.filtersBySheet.get(name)||''))warm=null;
    S.sheetLoadAbort?.abort();
    const opening=new AbortController();S.sheetLoadAbort=opening;
    if (S.indexJob) cancelIndex(S.indexJob);
    if (S.filterJob) cancelFilter(S.filterJob);
    invalidateViewport({preserveDOM:true});
    S.reusableSheetRows=warm?.presentation===sheetPresentationKey()?warm.rows:null;
    S.reusableSheetData=S.reusableSheetRows?warm.entry.data:null;
    S.navigationAbort?.abort();
    ++S.navigationTicket;
    const epoch = ++S.sheetEpoch;
    rowViews?.cancel();S.rowView=null;S.rowVisibility=null;S.rowViewFailure=false;geometry.rows.setHidden?.([]);
    S.sheetLoadingEpoch=epoch;
    editCopy?.grid.controls();
    $('grid').inert=true;$('grid').parentElement.classList.add('sheet-switching');
    pictures?.reset();
    S.sheet = name;
    S.sourceRowSizes=[];S.sourceColumnWidths={};S.sourceDefaultRow=0;S.sourceDefaultCol=0;S.sourceGridLines=S.source?.format!=='xls-html';updateSourceAppearance();
    S.index = null;
    S.indexReady = false;
    S.sheetLayout=null;
    mergedCells.load([]);
    S.window = null;
    frozenPanes?.reset();
    S.freeze = {rows:0,cols:0};
    setDisplayFontSelection("");
    S.cellMap.clear();
    S.rowNumberByVisual.clear();
    if (!preserveTrace) S.traceRefs = [];
    S.filterId = S.filtersBySheet.get(name) || "";
    S.filtered = Boolean(S.filterId);
    S.filterTotal = S.filterTotalsBySheet.get(name) || 0;
    if(S.source?.format==='txt'&&!S.autoFiltersBySheet.has(name)) {
      S.autoFiltersBySheet.set(name,window.ExcelLensTableHeaders.fromHeader(S.source.tableHeader));
    }
    S.autoFilter = S.autoFiltersBySheet.get(name) || null;
    ++S.textHeaderRevision;
    S.selected = null;
    S.wholeRowSelected = false;
    S.range = null; S.multiRanges = []; S.rangeAnchor = null;
    paintRangeFrames([]);
    S.columnSelection = null; S.columnAnchor = 0;
    selectionStats?.clear();
    S.selectedVisualIndex = 1;
    $("nameBox").value = "";
    formulaInspector?.clear();
    $("formulaValue").textContent = "-";
    $("tracePrecedentsBtn").disabled = true;
    $("traceDependentsBtn").disabled = true;
    $("cellDetailBtn").disabled = true;
    $("filterStatus").hidden = !S.filtered;
    $("clearFilterBtn").disabled = !S.filtered;
    updateAutoFilterButton();
    $("gridEmpty").hidden = true;
    $("indexOverlay").hidden = true;
	S.previewOnly=false;
    $("retryIndexBtn").hidden = true;
    $('showRowsRecoveryBtn').hidden=true;
    renderSheetTabs();
    setStatus(`正在打开 · ${name}`);
    const activity = openingActivity || window.ExcelLensActivity?.begin('正在准备工作表', {priority:2, signal:opening.signal});
    if (openingActivity) opening.signal.addEventListener('abort', activity.end, {once:true});
    activity?.stage('正在读取工作表', openingActivity ? 65 : 0, 85);
    try {
      const start = warm ? {...warm.index,ready:true} : await api("/api/index/start", {
        method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ sheet: name }),signal:opening.signal
      });
      if (epoch !== S.sheetEpoch) return;
      if (start.ready) S.index = start;
      else {
        S.indexJob = start.jobId;
        activity?.onCancel(() => cancelIndex(start.jobId));
		paintInitialPreview(name,epoch);
        S.index = await pollIndex(start.jobId, name, epoch, activity);
      }
      if (epoch !== S.sheetEpoch) return;
      S.indexJob = null;
      if(S.previewOnly)for(const row of S.renderedRows.values())row._xlSheetEpoch=-1;
	  S.previewAbort?.abort();S.previewAbort=null;S.previewOnly=false;S.previewData=null;cancelAnimationFrame(S.previewFrame);S.previewFrame=0;
      activity?.stage('正在读取工作表布局', 85, 95);
      S.usedMaxRow = Math.max(1, Number(S.index.maxRow || 1));
      S.usedMaxCol = Math.max(1, Number(S.index.maxCol || 1));
      S.dimension = S.index.usedDimension || `A1:${colName(S.usedMaxCol)}${S.usedMaxRow}`;
      try {
        const sizes=warm?.layout || await api(`/api/layout?sheet=${encodeURIComponent(name)}`,{signal:opening.signal});
        if (epoch !== S.sheetEpoch) return;
        S.sheetLayout=sizes;
        mergedCells.load(sizes.sourceMerges);
        S.rowSizes=sizes.rows||{};
        S.columnWidths=sizes.cols||{};
        S.sourceRowSizes=sizes.sourceRows||[];
        S.sourceColumnWidths=sizes.sourceCols||{};
        S.sourceGridLines=sizes.sourceGridLines??(S.source?.format!=='xls-html');
        S.sourceDefaultRow=sizes.sourceDefaultRow||0;
        S.sourceDefaultCol=sizes.sourceDefaultCol||0;
        updateSourceAppearance();
        S.columnSourceRanges=sizes.sourceHidden||[];
        S.columnMode=sizes.columnMode||'original';
        S.columnOverrides=sizes.columnVisibility||{};
        applyColumnVisibility();
        setDisplayFontSelection(FONT_STACKS[sizes.font] ? sizes.font : "");
      } catch(error) {
        if(epoch!==S.sheetEpoch||opening.signal.aborted)return;
        mergedCells.load([]);
        S.rowSizes={};S.columnWidths={};S.columnSourceRanges=[];S.columnMode='original';S.columnOverrides={};
        applyColumnVisibility(); setDisplayFontSelection("");
        toast('读取列布局失败：'+error.message,'error');
      }
      try {
        const view = warm?.freeze || await api(`/api/view?sheet=${encodeURIComponent(name)}`,{signal:opening.signal});
        if (epoch !== S.sheetEpoch) return;
        S.freeze = {rows: Number(view.rows || 0), cols: Number(view.cols || 0)};
      } catch (error) { if(epoch!==S.sheetEpoch||opening.signal.aborted)return;S.freeze = {rows:0,cols:0}; }
      if(epoch!==S.sheetEpoch)return;
      S.indexReady = true;
      updateAutoFilterButton();
      updateFreezeUI();
      // Keep roughly one small overscroll cushion, not the old 2,000 extra
      // rows / 16,384 columns. Explicit Go To still extends the viewport.
      S.maxRow = Math.min(sourceRowLimit(), Math.max(S.usedMaxRow + 18,Math.ceil($('grid').clientHeight/25)+4));
      S.maxCol = Math.min(16384, Math.max(S.usedMaxCol + 3,Math.ceil($('grid').clientWidth/132)+2));
      if(warm){S.maxRow=Math.max(S.maxRow,warm.maxRow);S.maxCol=Math.max(S.maxCol,warm.maxCol);}
      if(rowViews){
        const previousRows=rowViews.cache.get(name);
        try{await rowViews.prepare({signal:opening.signal});}
        catch(error){
          if(epoch!==S.sheetEpoch||opening.signal.aborted)return;
          if(!S.filtered||!/筛选结果已失效/.test(error.message))throw error;
          S.filtersBySheet.delete(name);S.filterTotalsBySheet.delete(name);S.filterId='';S.filtered=false;S.filterTotal=0;
          if(S.autoFilter){S.autoFilter.rules.clear();S.autoFilter.sortColumn=0;S.autoFilter.descending=false;}
          $('filterStatus').hidden=true;$('clearFilterBtn').disabled=true;
          await rowViews.prepare({signal:opening.signal,settings:previousRows?.settings});
          if(epoch!==S.sheetEpoch)return;
          toast('此前的筛选结果已失效，已保留行显隐并恢复源行顺序','warning');
        }
        if(epoch!==S.sheetEpoch)return;geometry.rows.setHidden(S.rowVisibility.hidden);if(S.filtered)S.filterTotal=filteredBodyCount();
      }
      updateGeometryBase();
      updateCanvasSize();
      pictures?.load(name,epoch).then(()=>pictures?.schedule()).catch(error=>{
        if(epoch===S.sheetEpoch)toast("图片读取失败："+error.message,'error');
      });
      if(previousView && !targetRef) {
        setVirtualScrollTop(previousView.top);
        $('grid').scrollLeft=previousView.left;
      }else{
        setVirtualScrollTop(0);$('grid').scrollLeft=0;
      }
      if(warm&&(!S.rowView||warm.entry.request.viewId===S.rowView.id)){
        for(const [key,data]of warm.frozen)frozenPanes.cachePart(key,data);
        tiles.store(viewportKey(warm.entry.request),warm.entry.data,warm.entry.request);
      }
      try {
        activity?.stage('正在显示单元格', 95, 99);
        await loadViewport(true, epoch, true);
      } catch (error) {
        if (S.filtered && /筛选结果已失效/.test(error.message)) {
          S.filtersBySheet.delete(S.sheet);
          S.filterTotalsBySheet.delete(S.sheet);
          S.filterId = "";
          S.filtered = false;
          S.filterTotal = 0;
          $("filterStatus").hidden = true;
          $("clearFilterBtn").disabled = true;
          updateCanvasSize();
          await loadViewport(true, epoch, true);
        } else throw error;
      }
      if (epoch !== S.sheetEpoch) return;
      $("indexOverlay").hidden = true;
      setStatus(`已打开 · ${name}`);
      let arrived=true;
      if (targetRef) {
        try {
          arrived=await navigateToCell(name, targetRef, preserveTrace, navigationGuard);
        } catch(error) {
          if(epoch!==S.sheetEpoch)return false;
          toast(error.message,'error');
          arrived=false;
        }
        if(epoch!==S.sheetEpoch)return false;
        if(!arrived&&!S.selected)selectInitialCell();
      } else if(previousView && previousView.ref){
        const ref=parseRef(previousView.ref);
        if(ref&&rowVisible(ref.row)&&S.filtered&&(!previousView.visual||previousView.frozenThrough!==frozenPanes.effective().rows)){
          if(!await navigateToCell(name,ref.ref)&&epoch===S.sheetEpoch&&!S.selected)selectInitialCell();
        }else if(ref&&rowVisible(ref.row)){
          const cell=S.cellMap.get(ref.ref)||S.frozenCellMap.get(ref.ref)||(warm?.selected?.ref===ref.ref?warm.selected:null)||await getCell(name,ref.ref).catch(()=>cellAt(ref.row,ref.col));
          if(epoch!==S.sheetEpoch)return;
          selectCell(cell,ref.row,ref.col,ref.ref,previousView.visual||ref.row);
          $('grid').focus({preventScroll:true});
        }else selectInitialCell();
      } else selectInitialCell();
      if(warm)await frozenPanes.refresh(true);else frozenPanes.schedule(true);
      if(epoch!==S.sheetEpoch)return;
      finishSheetSwitch(epoch);
      return arrived;
    } catch (error) {
      if (epoch !== S.sheetEpoch) return;
	  S.previewAbort?.abort();S.previewOnly=false;
      $("indexOverlay").hidden = false;
      S.indexReady=false;finishSheetSwitch(epoch,false);
      $("indexTitle").textContent = `${name} · 读取失败`;
      $("indexDetail").textContent = error.message;
      $("retryIndexBtn").hidden = false;
      $('showRowsRecoveryBtn').hidden=!S.rowViewFailure;
      setStatus("读取失败");
      toast(error.message,'error');
    } finally {
      if (openingActivity) opening.signal.removeEventListener('abort', activity.end);
      else { await activity?.paintIfDue?.(); activity?.end(); }
    }
  }

  async function paintInitialPreview(name,epoch) {
	if(S.filtered)return;
	const controller=new AbortController();S.previewAbort=controller;
	try {
	  const data=await api('/api/preview?compact=1&sheet='+encodeURIComponent(name),{signal:controller.signal});
	  if(controller.signal.aborted||epoch!==S.sheetEpoch||S.indexReady||!data.available)return;
	  S.previewOnly=true;
	  S.previewData=data;
	  S.rowSizes={};S.columnWidths={};S.hiddenColumns=new Set();geometry.rows.load({});geometry.cols.load({});
	  S.maxRow=Math.max(1,Math.min(120,data.maxRow||80));S.maxCol=Math.max(12,Math.min(40,(data.maxCol||1)+3));
	  S.virtualScrollScale=1;updateCanvasSize();
	  renderInitialPreview();
	  finishSheetSwitch(epoch);
	  $('gridStatus').textContent=`${name} · 首屏预览（完整索引建立中）`;
	} catch(error) { /* Full indexing remains the authoritative validation path. */ }
  }

  function renderInitialPreview() {
    if(!S.previewOnly||!S.previewData||S.indexReady)return;
    const request=getViewportRequest();
    const first=Math.max(1,request.visibleColStart-TEXT_SPILL_LOOKAHEAD);
    const last=Math.min(S.maxCol,request.visibleColEnd+TEXT_SPILL_LOOKAHEAD);
    const columns=Array.from({length:Math.max(0,last-first+1)},(_,i)=>first+i);
    const data={...S.previewData,startCol:first,colCount:columns.length,columns};
    renderGrid(data,{...request,filtered:false,displayStart:Math.max(1,request.visibleStart-8),displayEnd:Math.min(S.maxRow,request.visibleEnd+8)});
  }

  function scheduleInitialPreview() {
    if(S.previewFrame)return;
    S.previewFrame=requestAnimationFrame(()=>{S.previewFrame=0;renderInitialPreview();});
  }

  async function pollIndex(jobID, sheetName, epoch, activity) {
    while (epoch === S.sheetEpoch) {
      await delay(320);
      const progress = await api(`/api/index/status?id=${encodeURIComponent(jobID)}`);
      if (epoch !== S.sheetEpoch) return null;
      activity?.report(progress.doneBytes, progress.totalBytes, progress.stage || '正在建立索引',
        progress.totalBytes > 0 ? fmtBytes(progress.doneBytes) + ' / ' + fmtBytes(progress.totalBytes) : '');
      if (progress.status === "error") throw new Error(progress.error || "索引建立失败");
      if (progress.status === "cancelled") throw new Error("索引任务已取消");
      if (progress.status === "done") return progress;
    }
    return null;
  }

  // Frozen panes are absolutely positioned above #grid. Native scrollbar
  // gutters belong to #grid, so the panes MUST stop before those gutters.
  // Measure rather than assuming 13px: Windows, Firefox and DPI differ.
  function syncFreezeScrollGutters() {
    const grid = $("grid");
    const vertical = Math.max(0, grid.offsetWidth - grid.clientWidth - grid.clientLeft);
    const horizontal = Math.max(0, grid.offsetHeight - grid.clientHeight - grid.clientTop);
    const region = document.querySelector(".grid-region");
    region.style.setProperty("--grid-v-scrollbar", `${vertical}px`);
    region.style.setProperty("--grid-h-scrollbar", `${horizontal}px`);
    syncTableColumnHeaders();
  }

  function updateCanvasSize() {
    if(S.filtered)S.filterTotal=filteredBodyCount();
    const rows = S.filtered ? Math.max(0, S.filterTotal) : S.maxRow;
    updateGeometryBase();
    ROW_HEAD=window.ExcelLensGeometry.rowHeaderWidth(S.maxRow,window.devicePixelRatio||1);
    const width = ROW_HEAD + colTotal(S.maxCol);
    const height = headerHeight() + sheetHeight(rows);
    const canvas = $("gridCanvas");
    const grid = $("grid");
    const oldLogicalScroll = getVirtualScrollTop();
    canvas.style.width = `${Math.max(width, grid.clientWidth)}px`;
    canvas.style.height = `${physicalCanvasHeight(height,grid)}px`;
    setVirtualScrollTop(oldLogicalScroll);
    canvas.style.setProperty("--canvas-width", `${Math.max(width, $("grid").clientWidth)}px`);
    canvas.style.setProperty("--row-height", `${rowHeight()}px`);
    canvas.style.setProperty("--col-width", `${colWidth()}px`);
    canvas.style.setProperty("--header-height", `${headerHeight()}px`);
    canvas.style.setProperty("--row-header-width", `${ROW_HEAD}px`);
    $("columnHeaders").style.height = `${headerHeight()}px`;
    $("rowsLayer").style.top = `${headerHeight()}px`;
    $("rowsLayer").style.width = `${width}px`;
    $("gridStatus").textContent = `${S.sheet || ""} · 已用区域 ${S.dimension}`;
    syncFreezeScrollGutters();
    pictures?.schedule();
  }

  // A size or visibility change moves cells without a scroll or selection
  // event, so the selection frame and crosshair are put back here.
  // Keep mounted DOM rows/panes visible until the refreshed viewport commits
  // to avoid blank white flashes during resize and column visibility changes.
  function relayoutGrid(){
    invalidateViewport({preserveDOM:true});updateCanvasSize();
    frozenPanes.reset({preserveDOM:true});frozenPanes.schedule(true);
    scheduleViewport(true);
    paintRangeFrames();
  }

  function ensureExtent(row,col){
    if(!S.indexReady)return;
    const neededRows=Math.min(sourceRowLimit(),Math.max(S.maxRow,row+18));
    const neededCols=Math.min(16384,Math.max(S.maxCol,col+3));
    if(neededRows!==S.maxRow||neededCols!==S.maxCol){
      S.maxRow=neededRows;S.maxCol=neededCols;updateCanvasSize();
    }
  }
  async function commitResize(kind,index,size){
    if(!S.indexReady)return;
    if(kind==='row'&&S.filtered){toast('筛选视图暂不调整单行高度，请清除筛选后操作','warning');return;}
    const sheet=S.sheet, epoch=S.sheetEpoch;
    const axis=kind==='row'?geometry.rows:geometry.cols;
    const original=axis.values.get(index)||0;
    const previousLayoutSize=kind==='row'?S.rowSizes[String(index)]:S.columnWidths[String(index)];
    if(kind==='col' && S.hiddenColumns.has(index)) {toast('请先显示该列，再调整列宽','warning');return;}
    if(!geometry.set(kind,index,size||(kind==='col'?Number(S.sourceColumnWidths?.[index])||0:0)))return;
    const sizeStore=kind==='col'?S.columnWidths:S.rowSizes;
    if(size) sizeStore[String(index)]=size;
    else delete sizeStore[String(index)];
    try{
      await api('/api/layout',{method:'POST',headers:{'Content-Type':'application/json'},
        body:JSON.stringify({sheet,kind,index,size})});
    }catch(error){
      // A delayed response from the previous worksheet must not overwrite
      // the new worksheet's geometry when switching tabs during a resize.
      if(S.sheet===sheet&&S.sheetEpoch===epoch) {
        geometry.set(kind,index,original);
        if(previousLayoutSize) sizeStore[String(index)]=previousLayoutSize;else delete sizeStore[String(index)];
      }
      toast('保存尺寸失败：'+error.message,'error');
    }
    if(S.sheet!==sheet||S.sheetEpoch!==epoch)return;
    relayoutGrid();
  }
  async function promptResize(kind,explicitIndex){
    if(kind==='row'&&S.filtered){toast('请先清除筛选，再调整单行高度','warning');return;}
    const index=explicitIndex||((kind==='row')?(S.selected?.row||1):(S.selected?.col||1));
    const oldValue=kind==='row'?rowHeight(index):colWidth(index);
    const [low,high]=kind==='row'?[14,500]:[32,800],sheet=S.sheet,epoch=S.sheetEpoch;
    // 0 still means the default size, as it did in the earlier prompt.
    const valid=text=>/^\d+$/.test(text)&&(Number(text)===0||(Number(text)>=low&&Number(text)<=high));
    const answer=await window.ExcelLensOverlays.ask({
      title:kind==='row'?`第 ${index} 行的行高`:`${colName(index)} 列的列宽`,
      label:`像素，${low}–${high}`,value:String(Math.round(oldValue/scale())),inputMode:'numeric',alternative:'恢复默认',
      validate:text=>valid(text)?'':`请输入 ${low} 到 ${high} 之间的整数`});
    // The question stays open while other work finishes; act only on the same sheet.
    if(!answer||S.sheet!==sheet||S.sheetEpoch!==epoch)return;
    commitResize(kind,index,answer.alternative?0:Number(answer.value));
  }
  function bindResizeGrip(grip,kind,index){
    grip.title=kind==='row'?`拖动调整第 ${index} 行高度；双击按内容自动适应`:
      `拖动调整 ${colName(index)} 列宽度；双击抽样全部数据自动适应`;
    grip.addEventListener('pointerdown',event=>{
      if(event.button!==0)return;
      if(kind==='row'&&S.filtered){event.preventDefault();event.stopPropagation();toast('请先清除筛选，再调整单行高度','warning');return;}
      event.preventDefault();event.stopPropagation();
      const start=kind==='row'?event.clientY:event.clientX;
      const previous=kind==='row'?rowHeight(index):colWidth(index);
      const guide=$('resizeGuide');
      let next=previous;
      const host=$('grid').getBoundingClientRect();
      const update=delta=>{
        next=Math.max(kind==='row'?14:32,Math.min(kind==='row'?500:800,(previous+delta)/scale()));
        const css=next*scale();
        guide.hidden=false;
        guide.className='resize-guide '+(kind==='row'?'horizontal':'vertical');
        if(kind==='row'){guide.style.top=`${Math.min(host.height-1,Math.max(0,event.clientY-host.top+delta))}px`;
          guide.style.left='0';guide.style.width='100%';}
        else{guide.style.left=`${Math.min(host.width-1,Math.max(0,event.clientX-host.left+delta))}px`;
          guide.style.top='0';guide.style.height='100%';}
      };
      const move=e=>{e.preventDefault();update((kind==='row'?e.clientY:e.clientX)-start);};
      const end=e=>{
        document.removeEventListener('pointermove',move,true);
        document.removeEventListener('pointerup',end,true);
        document.removeEventListener('pointercancel',cancel,true);
        window.removeEventListener('blur',cancel);
        guide.hidden=true;
        if(e.type==='pointercancel')return;
        const px=kind==='row'?Math.max(14,Math.min(500,next)):
          Math.max(32,Math.min(800,next));
        commitResize(kind,index,Math.round(px));
      };
      const cancel=e=>end(e);
      // Listen at document level as well: a resize handle is only 9px wide,
      // so the pointer normally leaves it during the drag. Never lose pointerup.
      document.addEventListener('pointermove',move,true);
      document.addEventListener('pointerup',end,true);
      document.addEventListener('pointercancel',cancel,true);
      window.addEventListener('blur',cancel);
      update(0);
    });
    grip.addEventListener('click',event=>{event.preventDefault();event.stopPropagation();});
    grip.addEventListener('dblclick',event=>{
      event.preventDefault();event.stopPropagation();
      autoFitSize(kind,index);
    });
  }
  const columnController = new window.ExcelLensColumnController({
    state: S, api, toast, geometry, baseRow: BASE_ROW, baseCol: BASE_COL, colName, parseColumn,
    relayoutGrid, selectCellByAddress: (ref, visual) => selectCellByAddress(ref, visual),
    hideCellMenu: () => hideCellMenu(), selectionStats: () => selectionStats, calculator: () => calculator
  });
  const selectedColumnSpan = () => columnController.selectedSpan();
  const hasColumnOfType = (span, hidden) => columnController.hasOfType(span, hidden);
  const paintColumnSelection = () => columnController.paintSelection();
  const selectColumnSpan = (anchor, end) => columnController.selectSpan(anchor, end);
  const captureColumnSelection = () => columnController.captureSelection();
  const applyColumnVisibility = () => columnController.applyVisibility();
  const nextVisibleColumn = from => columnController.nextVisible(from);
  const previousVisibleColumn = from => columnController.previousVisible(from);
  const hideColumnsMenu = () => columnController.hideMenu();
  const setColumnVisibility = (mode, index, hidden, end) => columnController.setVisibility(mode, index, hidden, end);

  // Data fetching and DOM painting deliberately use separate generations. A
  // request for a scrolled-away tile must never replace the tile under the
  // user's CURRENT viewport (including after scrolling back while it is pending).
  function columnOrder(){
    if(!S.hiddenColumns.size)return null;
    const key=geometry.cols.version+'|'+S.maxCol;
    if(columnVisibilityKey!==key){
      const ranges=[];
      for(const col of [...S.hiddenColumns].filter(c=>c<=S.maxCol).sort((a,b)=>a-b)){
        const last=ranges.at(-1);
        if(last?.max===col-1)last.max=col;else ranges.push({min:col,max:col});
      }
      columnVisibility=new window.ExcelLensRowVisibility(S.maxCol,ranges);columnVisibilityKey=key;
    }
    return columnVisibility;
  }
  function viewportContext(){
    return JSON.stringify([S.book,S.workbookKey,S.sheet,editCopy?.grid.active?editCopy.data?.document?.revision??0:null]);
  }
  // Ranks count visible rows/columns; HTTP receives source coordinates or sorted offsets.
  function viewportBand(base,r1,r2,c1,c2){
    const {rowOrder,colOrder,rowCount,colCount}=base;
    r1=Math.max(1,Math.min(rowCount||1,r1));r2=Math.max(r1,Math.min(rowCount||1,r2));
    c1=Math.max(1,Math.min(colCount||1,c1));c2=Math.max(c1,Math.min(colCount||1,c2));
    const displayStart=rowOrder?.at(r1-1)||r1,displayEnd=rowCount?(rowOrder?.at(r2-1)||r2):displayStart-1;
    const selectedCols=colOrder?Array.from({length:c2-c1+1},(_,i)=>colOrder.at(c1+i-1)||1):null;
    return {...base,r1,r2,c1,c2,displayStart,displayEnd,row:displayStart,offset:r1-1,limit:r2-r1+1,
      startCol:selectedCols?.[0]||c1,cols:c2-c1+1,selectedCols};
  }
  function getViewportRequest(){
    const grid=$('grid'),pinned=frozenPanes?.effective()||{rows:0,cols:0};
    const w=Math.max(1,grid.clientWidth-ROW_HEAD),h=Math.max(1,grid.clientHeight-headerHeight());
    const top=getVirtualScrollTop(),left=grid.scrollLeft,ph=sourceRowTop(pinned.rows+1),pw=colLeft(pinned.cols+1);
    if(S.filtered)S.filterTotal=filteredBodyCount();
    const rowOrder=S.filtered?null:S.rowVisibility,colOrder=columnOrder();
    const rowCount=S.filtered?S.filterTotal:visibleSourceCount(1,S.maxRow),colCount=colOrder?colOrder.length:S.maxCol;
    const rowRank=px=>{const row=rowAtOffset(px);return Math.min(rowCount||1,rowOrder?rowOrder.before(row)+1:row);};
    const colRank=px=>{const col=colAtOffset(px);return Math.min(colCount||1,colOrder?colOrder.before(col)+1:col);};
    const y0=top+(S.filtered?0:ph),y1=top+h-(S.filtered?ph:0),x0=left+pw,x1=left+w;
    const v1=rowRank(y0),v2=Math.max(v1,rowRank(y1-0.01)),u1=colRank(x0),u2=Math.max(u1,colRank(x1-0.01));
    const visibleStart=rowOrder?.at(v1-1)||v1,visibleEnd=rowCount?(rowOrder?.at(v2-1)||v2):0;
    const visibleColStart=colOrder?.at(u1-1)||u1,visibleColEnd=colCount?(colOrder?.at(u2-1)||u2):0;
    const position={x:left,y:top,physical:grid.scrollTop,w:w+ROW_HEAD,h:h+headerHeight(),pinned,ph,pw};
    const base={position,book:S.book,workbookKey:S.workbookKey,sheet:S.sheet,context:viewportContext(),filtered:S.filtered,filterId:S.filtered?S.filterId:'',viewId:S.rowView?.id||'',
      excludeThroughRow:S.filtered?pinned.rows:undefined,rowOrder,colOrder,rowCount,colCount,visibleStart,visibleEnd,visibleColStart,visibleColEnd};
    // Mount a small aligned band. Include the off-screen text origin on the left.
    // Spend optional margins only after the real viewport and its spill dependencies fit.
    const rowStep=Math.max(1,Math.min(4,Math.floor((DOM_MARGIN_BUDGET/(u2-u1+TEXT_SPILL_LOOKAHEAD+6)-(v2-v1+1))/3)));
    const r1=Math.floor((v1-1)/rowStep)*rowStep+1-rowStep,r2=Math.ceil(v2/rowStep)*rowStep+rowStep;
    const now=performance.now(),lead=motion.predict(w,h,now);
    // Start staging while the leading edge still has real, interactive DOM.
    // A thumb jump gives its destination priority over the transient velocity.
    const moving=motion.time!==null&&now-motion.time<280&&Math.abs(motion.dx)<w*1.5&&Math.abs(motion.dy)<h*1.5;
    const vx=moving?motion.vx:0,vy=moving?motion.vy:0,renderHorizon=Math.max(48,Math.min(220,motion.renderMs+36));
    const colLead=Math.min(w*.4,Math.abs(vx)*Math.min(72,renderHorizon));
    const mountRank=Math.max(1,Math.min(Math.floor((u1-1)/4)*4-1,colRank(x0-(vx<0?colLead:0))));
    const mountEnd=Math.max(Math.ceil(u2/4)*4+3,colRank(x1+(vx>0?colLead:0)));
    const cellRoom=Math.max(0,Math.floor(DOM_MARGIN_BUDGET/(mountEnd-mountRank+1))-(r2-r1+1));
    const rowLead=Math.min(h*.55,Math.abs(vy)*Math.min(72,renderHorizon));
    const mountR1=Math.min(r1,Math.max(r1-cellRoom,rowRank(y0-(vy<0?rowLead:0))));
    const mountR2=Math.max(r2,Math.min(r2+cellRoom,rowRank(y1+(vy>0?rowLead:0))));
    const req=viewportBand(base,mountR1,mountR2,mountRank-TEXT_SPILL_LOOKAHEAD,mountEnd);
    req.mountColStart=colOrder?.at(mountRank-1)||mountRank;
    req.visibleRanks=[v1,v2,u1,u2];
    const aheadR=vy>0?v2+.6*(req.r2-v2):v1-.6*(v1-req.r1);
    const aheadC=vx>0?u2+.6*(req.c2-u2):u1-.6*(u1-mountRank);
    const headRow=Math.max(1,Math.round(aheadR)),headCol=Math.max(1,Math.round(aheadC));
    req.leadRow=rowOrder?.at(headRow-1)||headRow;req.leadCol=colOrder?.at(headCol-1)||headCol;
    req.prediction=[rowRank(y0-lead.y[0]),rowRank(y1+lead.y[1]),colRank(x0-lead.x[0]),colRank(x1+lead.x[1])];
    req.tileRows=Math.min(96,Math.max(32,Math.ceil(h/Math.max(10,rowHeight())/16)*32));
    req.tileCols=Math.min(48,Math.max(16,Math.ceil(w/Math.max(16,colWidth())/12)*16));
    while(req.tileRows*req.tileCols*bytesPerCell>VIEWPORT_CACHE_BUDGET/8&&req.tileRows>4)req.tileRows=Math.ceil(req.tileRows/2);
    return req;
  }
  const {viewportCoversVisible,sameViewportRows}=viewport;
  const estimateViewportBytes=viewport.estimateViewportBytes;
  const viewportKey=request=>viewport.viewportKey(S.sheetEpoch,request);
  const windowContains=request=>viewportCoversVisible(S.window,request);
  // Keep a long merge's off-screen anchor and perimeter as sparse points. Never
  // enlarge the mounted rectangle to encompass the entire merged range.
  function mergeParts(req){
    if(req.mergeParts)return req.mergeParts;
    const groups=new Map(),anchors=new Map(),right=req.selectedCols?.at(-1)??req.startCol+req.cols-1;
    if(!req.filtered&&mergedCells.root){
      for(const m of mergedCells.intersect(req.displayStart,req.displayEnd,req.mountColStart,right)){
        if(!activeMerge(m.row,m.col)||S.hiddenColumns.has(m.col))continue;
        if(m.row<req.displayStart||m.col<req.startCol){
          if(!anchors.has(m.row))anchors.set(m.row,[]);
          anchors.get(m.row).push(m.col);
        }
        for(const r of [m.row,m.endRow])for(const c of [m.col,m.endCol]){
          if(!rowVisible(r)||S.hiddenColumns.has(c)||r>=req.displayStart&&r<=req.displayEnd&&c>=req.startCol&&c<=right)continue;
          if(!groups.has(r))groups.set(r,new Set());
          groups.get(r).add(c);
        }
      }
    }
    const parts=[];
    for(const [row,set] of groups){
      const columns=[...set].sort((a,b)=>a-b);
      for(let i=0;i<columns.length;i+=MAX_COLS_PER_REQUEST){
        const selectedCols=columns.slice(i,i+MAX_COLS_PER_REQUEST);
        parts.push({...req,displayStart:row,displayEnd:row,row,limit:1,startCol:selectedCols[0],cols:selectedCols.length,selectedCols});
      }
    }
    req.mergeParts={anchors,parts};return req.mergeParts;
  }
  function projectViewport(request,received){
    const visuals=Array.from({length:request.rowCount?request.limit:0},(_,i)=>request.rowOrder?.at(request.r1+i-1)||request.r1+i);
    const entry=tiles.project(request,visuals,received);
    if(!entry)return null;
    entry.bounds.startCol=Math.max(entry.bounds.startCol,request.mountColStart);
    const {parts,anchors}=mergeParts(request);
    if(parts.length){
      const lookup=tiles.reader(request,received),rows=new Map(entry.data.rows.map(r=>[r.number,r]));
      for(const part of parts)for(const col of part.selectedCols){
        const sampled=lookup(part.row,col);
        if(!sampled)return null;
        let row=rows.get(part.row);
        if(!row){row={...sampled.row,number:part.row,cells:[]};rows.set(part.row,row);}
        if(sampled.cell)row.cells.push(sampled.cell);
      }
      entry.data.rows=[...rows.values()];entry.data.anchors=anchors;
    }
    return entry;
  }

  function planViewport(request,needCurrent){
    const {tileRows,tileCols,prediction:[pr1,pr2,pc1,pc2]}=request;
    const current=[],predicted=[],resident=[];
    const first=(rank,step)=>Math.floor((Math.max(1,rank)-1)/step)*step+1;
    const currentMinR=first(request.r1,tileRows),currentMinC=first(request.c1,tileCols);
    const available=(r,c)=>r>=currentMinR&&r<=request.r2&&c>=currentMinC&&c<=request.c2;
    const add=(list,r,c)=>{
      const band=viewportBand(request,r,r+tileRows-1,c,c+tileCols-1),key=viewportKey(band);
      const hit=tiles.find(key,band);
      if(hit){if(list===current)resident.push(hit);}else list.push({key,request:band,sheet:S.sheet,epoch:S.sheetEpoch});
    };
    if(needCurrent){
      for(const band of mergeParts(request).parts){
        const key=viewportKey(band),hit=tiles.find(key,band);
        if(hit)resident.push(hit);else current.push({key,request:band,sheet:S.sheet,epoch:S.sheetEpoch});
      }
      for(let r=currentMinR;r<=request.r2;r+=tileRows)for(let c=currentMinC;c<=request.c2;c+=tileCols)add(current,r,c);
    }
    if(!needCurrent&&motion.time!==null){
      for(let r=currentMinR;r<=request.r2;r+=tileRows)for(let c=currentMinC;c<=request.c2;c+=tileCols)add(predicted,r,c);
    }
    const cap=Math.min(PREFETCH_CELL_BUDGET,Math.floor(VIEWPORT_CACHE_BUDGET/4/bytesPerCell));
    const candidates=[];
    for(let r=first(pr1,tileRows);r<=pr2;r+=tileRows)for(let c=first(pc1,tileCols);c<=pc2;c+=tileCols){
      if(!available(r,c))candidates.push({r,c,d:Math.abs(r-(motion.vy<0?request.r1:request.r2))/tileRows+Math.abs(c-(motion.vx<0?request.c1:request.c2))/tileCols});
    }
    candidates.sort((a,b)=>a.d-b.d);
    const allowance=Math.max(0,Math.floor(cap/(tileRows*tileCols))-predicted.length);
    for(const {r,c} of candidates.slice(0,allowance))add(predicted,r,c);
    return scheduler.plan(current,predicted.slice(0,Math.floor(cap/(tileRows*tileCols)))).then(results=>resident.concat(results.filter(Boolean)));
  }

  let fallbackPalette=null;
  function canvasTheme(){
    const root=document.documentElement,key=root.getAttribute('data-reading-preset')||'';
    if(fallbackPalette?.key===key)return fallbackPalette;
    const css=getComputedStyle(root),color=name=>css.getPropertyValue(name).trim();
    const paper=color('--grid-paper'),surface=color('--grid-head');
    return fallbackPalette={key,paper,surface,neutral:'color-mix(in srgb, '+paper+' 50%, '+surface+')',
      line:color('--grid-line-color'),muted:color('--grid-head-ink'),tableHead:color('--grid-accent-soft'),band:color('--grid-band'),dark:root.getAttribute('data-reading-tone')==='dark'};
  }
  // Each bitmap belongs to a fixed rectangle of logical worksheet pixels.
  // Reversing can reuse both its native-pixel image and the interactive DOM.
  const styleTiles=new Map();
  let styleIdentity='';
  function paintViewportFallback(req){
    const host=$('viewportFallback');
    if(!req){
      cancelAnimationFrame(S.fallbackFrame);S.fallbackFrame=0;styleTiles.clear();styleIdentity='';
      S.viewportActivity?.end();S.viewportActivity=null;host.replaceChildren();host.hidden=true;return;
    }
    if(viewportCoversVisible(S.window,req)){S.viewportActivity?.end();S.viewportActivity=null;}
    else S.viewportActivity||=window.ExcelLensActivity?.begin('正在读取可见区域',{slot:'viewportActivitySlot'});
    const {x,y,w:vw,h:vh,physical,pinned,ph,pw}=req.position;
    if(!vw||!vh)return;
    const theme=canvasTheme(),hh=headerHeight();
    const cap=4*1024*1024,ratio=Math.min(2,window.devicePixelRatio||1,Math.sqrt(cap*.48/(vw*vh)));
    const pixelW=Math.max(1,Math.ceil(vw*ratio/3)),pixelH=Math.max(1,Math.ceil(vh*ratio/3));
    const tw=pixelW/ratio,th=pixelH/ratio,slots=Math.max(1,Math.min(48,Math.floor(cap/(pixelW*pixelH))));
    const identity=[req.context,req.filterId,req.viewId,req.excludeThroughRow,S.zoom,geometry.version,S.showStyles,S.sourceGridLines,
      S.textHeaderRevision,theme.key,ROW_HEAD,pw,ph,ratio,pixelW,pixelH].join('|');
    if(styleIdentity!==identity){styleTiles.clear();host.replaceChildren();styleIdentity=identity;}
    const lead=motion.predict(vw,vh,performance.now());
    const firstX=Math.floor(x/tw),firstY=Math.floor(y/th),endX=Math.floor((x+vw-.01)/tw),endY=Math.floor((y+vh-.01)/th);
    const fromX=Math.max(0,Math.floor((x-Math.min(vw*1.5,lead.x[0]))/tw)),toX=Math.floor((x+vw+Math.min(vw*1.5,lead.x[1]))/tw);
    const fromY=Math.max(0,Math.floor((y-Math.min(vh*2,lead.y[0]))/th)),toY=Math.floor((y+vh+Math.min(vh*2,lead.y[1]))/th);
    const desired=[];
    for(let ty=fromY;ty<=toY;ty++)for(let tx=fromX;tx<=toX;tx++){
      const dx=Math.max(firstX-tx,0,tx-endX),dy=Math.max(firstY-ty,0,ty-endY);
      const behindX=motion.vx>0?tx<firstX:motion.vx<0&&tx>endX,behindY=motion.vy>0?ty<firstY:motion.vy<0&&ty>endY;
      desired.push({tx,ty,key:tx+','+ty,visible:!dx&&!dy,d:dx*dx+dy*dy+(behindX?dx:0)+(behindY?dy:0)});
    }
    desired.sort((a,b)=>a.d-b.d);desired.length=Math.min(slots,desired.length);
    const wanted=new Set(desired.map(t=>t.key)),spares=[];
    for(const [key,tile] of styleTiles){
      if(!wanted.has(key)){styleTiles.delete(key);tile.canvas.remove();spares.push(tile.canvas);}
      else if(virtualScroll.scale!==1)tile.canvas.style.top=virtualScroll.rowPosition(tile.y,physical)+'px';
    }
    const started=performance.now(),lookup=tiles.reader(req),offset=S.filtered?ph:0;
    const styleFor=({cell,row},c,r)=>{
      const data=editCopy?.grid.active&&row?editCopy.grid.project(S.sheet,colName(c)+row.number,cell):cell;
      if(!theme.dark&&row&&window.ExcelLensTableHeaders.isTable(S.autoFilter)){
        if(row.number===S.autoFilter.row)return {background:theme.tableHead};
        if(row.number>S.autoFilter.row&&(r-S.autoFilter.row)%2===0)return {background:theme.band};
      }
      return visualForBorder(data,row);
    };
    let deferred=false;
    for(const {key,tx,ty,visible} of desired){
      const old=styleTiles.get(key);
      if(old&&!old.dirty)continue;
      if(!visible&&performance.now()-started>=6){deferred=true;break;}
      const left=tx*tw,top=ty*th,canvas=old?.canvas||spares.pop()||document.createElement('canvas');
      if(canvas.width!==pixelW)canvas.width=pixelW;
      if(canvas.height!==pixelH)canvas.height=pixelH;
      const ctx=canvas.getContext('2d',{alpha:true});
      if(!ctx)continue;
      ctx.setTransform(ratio,0,0,ratio,0,0);ctx.clearRect(0,0,tw,th);
      if(!old){
        canvas.className='viewport-fallback';canvas.style.width=tw+'px';canvas.style.height=th+'px';
        canvas.style.left=left+'px';canvas.style.top=virtualScroll.rowPosition(top,physical)+'px';
      }
      const minVisual=rowAtOffset(Math.max(0,top-hh-offset)),maxVisual=Math.min(S.filtered?S.filterTotal:S.maxRow,rowAtOffset(top+th-hh-offset));
      const firstCol=Math.max(1,colAtOffset(Math.max(0,left-ROW_HEAD))),lastCol=Math.min(S.maxCol,colAtOffset(left+tw-ROW_HEAD));
      const colsToPaint=[];
      for(let c=req.colOrder?.next(firstCol)??firstCol;c&&c<=lastCol;c=req.colOrder?req.colOrder.next(c+1):c+1)colsToPaint.push(c);
      window.ExcelLensGridBackdrop.paintMissing({ctx,pinned,top:Math.max(0,hh+ph-top),left:Math.max(0,ROW_HEAD+pw-left),
        minVisual,maxVisual,colsToPaint,scrollLeft:left,w:tw,h:th,hh,rowHead:ROW_HEAD,state:S,scrollTop:top,
        sourceRowTop,colWidth,colLeft,rowHeight,rowTop,nextVisibleRow,rowVisible,lookup,mergeAt:activeMerge,theme,styleFor});
      if(!old)host.append(canvas);
      styleTiles.set(key,{canvas,y:top,minVisual,maxVisual,firstCol,lastCol,dirty:false});
    }
    host.hidden=false;
    if(deferred)scheduleStylePaint();
  }
  function updateViewportLoading(){
    paintViewportFallback(S.indexReady&&S.sheet?(lastViewportDemand||getViewportRequest()):null);
  }
  function scheduleStylePaint(){
    if(!S.fallbackFrame)S.fallbackFrame=requestAnimationFrame(()=>{S.fallbackFrame=0;updateViewportLoading();});
  }

  function invalidateViewport({preserveDOM=false}={}) {
	S.previewAbort?.abort();S.previewAbort=null;S.previewOnly=false;
	S.previewData=null;cancelAnimationFrame(S.previewFrame);S.previewFrame=0;
    cancelAnimationFrame(S.viewportFrame);
    S.viewportFrame = 0;
    paintViewportFallback(null);
    S.viewportForce = false;
    cancelGridWork();rowPool.clear();poolCells=0;cellStyleCache.clear();finishedCells.clear();
    scheduler.reset();motion.reset();columnVisibilityKey='';lastViewportDemand=null;
    clearTimeout(S.viewportRetryTimer);
    S.viewportRetryTimer = 0;
    S.viewportRetryAttempt = 0;
    ++S.viewportRequest;
    S.viewportData = null;
    tiles.clear();
    S.window = null;
    if(!preserveDOM)resetGridDOM();
  }

  function publishViewport(entry){
    S.window=entry.bounds;S.viewportData=entry;
    clearTimeout(S.viewportRetryTimer);S.viewportRetryTimer=0;S.viewportRetryAttempt=0;
    if(entry.request.filtered){S.filterTotal=entry.data.totalRows;syncFilteredSelection(entry.request.excludeThroughRow);}
    if(S.freeze.rows||S.freeze.cols||frozenPanes.base)frozenPanes.schedule();
    scheduleStylePaint();
  }
  let gridWork=null,lastViewportDemand=null;
  function cancelGridWork(){
    if(!gridWork)return;
    cancelAnimationFrame(gridWork.frame);gridWork.resolve();gridWork=null;
  }
  function applyViewport(entry,sliced,urgent=false){
    cancelGridWork();
    if(!sliced||editCopy?.grid.active){
      renderGrid(entry.data,entry.request);publishViewport(entry);return;
    }
    // Keep the committed coordinate maps and DOM live while staging new cells.
    // A new sheet, revision, explicit navigation or disjoint jump supersedes this job.
    if(urgent){
      // Drop only coordinates outside both the current view and its spill/merge
      // dependencies before publishing strips. A superseded job cannot accumulate
      // offscreen nodes or hold the old coverage window beyond the retained rows.
      const demand=entry.request;
      const keep=viewportBand(demand,demand.r1-6,demand.r2+6,demand.c1-4,demand.c2+4);
      const {displayStart,displayEnd,startCol}=keep,endCol=keep.selectedCols?.at(-1)??startCol+keep.cols-1;
      for(const [v,row] of S.renderedRows){
        const anchors=entry.data.anchors?.get(v);
        if((v<displayStart||v>displayEnd)&&!anchors){
          retireRow(row);S.renderedRows.delete(v);S.rowNumberByVisual.delete(v);
          for(const node of row._xlCells.values())S.cellMap.delete(node.dataset.ref);
          continue;
        }
        for(const [col,node] of row._xlCells){
          if(col>=startCol&&col<=endCol||anchors?.includes(col))continue;
          node.remove();row._xlCells.delete(col);S.cellMap.delete(node.dataset.ref);
        }
      }
      if(S.window)S.window={...S.window,displayStart:Math.max(displayStart,S.window.displayStart),
        displayEnd:Math.min(displayEnd,S.window.displayEnd),startCol:Math.max(S.window.startCol,startCol),endCol:Math.min(S.window.endCol,endCol)};
    }
    const iterator=gridSteps(entry.data,entry.request,true,urgent?24:6,urgent);
    let resolve,reject;
    const promise=new Promise((ok,fail)=>{resolve=ok;reject=fail;});
    const job={entry,resolve,promise,frame:0,queued:false,step};gridWork=job;
    function step(){
      if(gridWork!==job)return;
      try{
        const latest=lastViewportDemand||entry.request,b=entry.bounds;
        if(!sameViewportRows(b,latest)||b.displayEnd<latest.visibleStart||b.displayStart>latest.visibleEnd||
          b.endCol<latest.visibleColStart||b.startCol>latest.visibleColEnd){
          cancelGridWork();scheduleViewport();return;
        }
        if(!iterator.next().done){
          // Yield input and paint between short batches. Chromium can use spare time
          // before the next vsync; a slow worksheet need not wait a full frame per row.
          if(!job.queued){
            job.queued=true;
            const resume=()=>{job.queued=false;step();};
            if(window.scheduler?.postTask)window.scheduler.postTask(resume,{priority:'user-visible'}).catch(reject);
            else job.frame=requestAnimationFrame(resume);
          }
          return;
        }
        gridWork=null;publishViewport(entry);resolve();
        if(!viewportCoversVisible(b,latest))scheduleViewport();
      }catch(error){gridWork=null;reject(error);}
    }
    step();return promise;
  }

  // One animation-frame coalescing, NOT a trailing-edge debounce: continuous
  // high-frequency wheel/touchpad input must never postpone the network call.
  // A cached leading strip can be mounted during the native scroll event. Read
  // its real source cells before scheduling distant work. Limit synchronous work
  // to adjacent windows; a jump or a slow, complex row uses the sliced path.
  function mountCachedLeadingEdge(request){
    if(!S.window||!sameViewportRows(S.window,request)||editCopy?.grid.active)return false;
    if(gridWork&&viewportCoversVisible(gridWork.entry.bounds,request)){
      if(!viewportCoversVisible(S.window,request)){gridWork.step();}
      return true;
    }
    const [v1,v2,u1,u2]=request.visibleRanks;
    const prior=S.viewportData?.request;
    if(!prior||Math.abs(motion.dy)>request.position.h*1.5||Math.abs(motion.dx)>request.position.w*1.5)return false;
    const from=Math.max(1,u1-(motion.vx<0?2:1));
    const core=viewportBand(request,v1-(motion.vy<0?4:1),v2+(motion.vy>0?4:1),from-TEXT_SPILL_LOOKAHEAD,u2+(motion.vx>0?2:1));
    core.mountColStart=request.colOrder?.at(from-1)||from;
    foregroundLookups++;
    const entry=projectViewport(core);
    if(!entry)return false;
    foregroundHits++;
    applyViewport(entry,true,true)?.catch(error=>toast(error.message,'error'));
    return true;
  }
  function scheduleViewport(force = false) {
	if(S.previewOnly){scheduleInitialPreview();return;}
    if (!S.indexReady || !S.sheet) return;
    S.viewportForce ||= force;
    const target=lastViewportDemand=getViewportRequest();
    if(!S.viewportForce){
      const covered=viewportCoversVisible(S.window,target);
      if(covered ? gridWork||viewport.viewportHasHeadroom(S.window,target) : mountCachedLeadingEdge(target)){
        planViewport(target,false);paintViewportFallback(target);return;
      }
      // Start the next strip while the leading cells are still visible. Work on
      // an already covered viewport uses the short, interruptible render slices.
    }
    paintViewportFallback(target);
    if (S.viewportFrame) return;
    S.viewportFrame = requestAnimationFrame(() => {
      S.viewportFrame = 0;
      const shouldForce = S.viewportForce;
      S.viewportForce = false;
      loadViewport(shouldForce).catch(error => toast(error.message,'error'));
    });
  }

  async function readViewportTile(job,signal){
    const request=job.request, t=performance.now();
    const params=new URLSearchParams({sheet:job.sheet,limit:request.limit,col:request.startCol,cols:request.cols,compact:'1',rowWindow:'1'});
    if(request.selectedCols)params.set('columns',request.selectedCols.join(','));
    if(request.viewId)params.set('viewId',request.viewId);
    if(request.filtered){
      params.set('offset',request.offset);params.set('filterId',request.filterId);params.set('excludeThroughRow',request.excludeThroughRow);
    }else params.set('row',request.row);
    const data=await api('/api/sheet?'+params,{signal,activity:false});
    if(signal.aborted||job.epoch!==S.sheetEpoch||request.context!==viewportContext())return;
    if(data.sheet!==job.sheet||(data.viewId||'')!==request.viewId||(data.filterId||'')!==request.filterId)throw Error('工作表视口已变化，请重新读取');
    if(!job.foreground && estimateViewportBytes(data)>VIEWPORT_CACHE_BUDGET/4)return;
    const entry=tiles.store(job.key,data,request);
    bytesPerCell=bytesPerCell*.75+Math.max(64,entry.bytes/(request.cols*request.limit))*.25;
    motion.observe('read',performance.now()-t);
    const b=entry.bounds;
    for(const tile of styleTiles.values())if(b.displayEnd>=tile.minVisual&&b.displayStart<=tile.maxVisual&&b.endCol>=tile.firstCol&&b.startCol<=tile.lastCol)tile.dirty=true;
    scheduleStylePaint();
    return entry;
  }

  async function loadViewport(force=false,expectedEpoch=S.sheetEpoch,strict=false){
    if(!S.indexReady||!S.sheet||expectedEpoch!==S.sheetEpoch)return;
    const request=lastViewportDemand=getViewportRequest(),ticket=S.viewportRequest;
    if(!force&&viewport.viewportHasHeadroom(S.window,request)){planViewport(request,false);updateViewportLoading();return;}
    if(!force&&gridWork&&viewportCoversVisible(gridWork.entry.bounds,request))return gridWork.promise;
    let entry=projectViewport(request);
    foregroundLookups++;
    if(entry){foregroundHits++;planViewport(request,false);}
    else{
      let received;
      try{received=await planViewport(request,true);}
      catch(error){
        if(ticket!==S.viewportRequest||expectedEpoch!==S.sheetEpoch||error.name==='AbortError')return;
        if(strict)throw error;
        if(!S.viewportRetryTimer && ++S.viewportRetryAttempt<=2){
          S.viewportRetryTimer=setTimeout(()=>{S.viewportRetryTimer=0;scheduleViewport(true);},S.viewportRetryAttempt*160);
        }else if(!S.viewportRetryTimer)throw error;
        return;
      }
      if(ticket!==S.viewportRequest||expectedEpoch!==S.sheetEpoch||!S.indexReady)return;
      const latest=getViewportRequest();
      if(!force&&viewport.viewportHasHeadroom(S.window,latest))return;
      entry=projectViewport(latest,received);
      if(!entry){scheduleViewport();return;}
    }
    if(!force&&gridWork){
      const b=gridWork.entry.bounds;
      if(sameViewportRows(b,request)&&b.displayEnd>=request.visibleStart&&b.displayStart<=request.visibleEnd&&
        b.endCol>=request.visibleColStart&&b.startCol<=request.visibleColEnd)return gridWork.promise;
    }
    return applyViewport(entry,!force&&!strict);
  }

  // Row/column nodes are recycled across overlapping tiles. Under continuous
  // scrolling, only newly exposed rows/columns are created: the ~80% overlap
  // stays mounted and retains its original Excel colors, font and formula text.
  function resetGridDOM() {
    S.renderedRows = new Map();
    S.renderedScrollTop=$('grid').scrollTop;
    S.renderedHeadersKey = "";
    $("rowsLayer").replaceChildren();
    $("columnHeaders").replaceChildren();
  }

  function followGridScroll() {
    if(virtualScroll.scale===1||!S.renderedRows.size)return;
    const physical=$('grid').scrollTop;
    // Update the bounded viewport's row positions even on a cached tile hit.
    // Browser scroll alone only moves physical pixels, while the row geometry
    // must move by logical pixels. Each row stays near the visible scrollport.
    if(physical===S.renderedScrollTop)return;
    // CSSOM serializes large top values to about six significant digits:
    // neighboring rows near 15 million pixels can read back identically.
    // Always derive positions from numeric geometry, as renderGrid does;
    // feeding serialized CSS back into the next scroll collapses those rows.
    const frozenHeight=S.filtered?sourceRowTop(frozenPanes.effective().rows+1):0;
    for(const [visual,row] of S.renderedRows)
      row.style.top=`${virtualScroll.rowPosition(rowTop(visual)+frozenHeight,physical)}px`;
    S.renderedScrollTop=physical;
  }

  function displayCellValue(cell) { return cellPresenter.value(cell); }
  function extendTextIntoBlanks(node,data,rowValues,layouts,position) {
    cellPresenter.overflow(node,data,rowValues,layouts,position);
  }
  function sameRenderedCell(a,b) {
    if(a===b)return true;
    if(!a || !b)return !a&&!b;
    return a.display===b.display && a.raw===b.raw && a.type===b.type &&
      a.style===b.style && a.formula===b.formula &&
      a.hasFormula===b.hasFormula && a.valueColor===b.valueColor &&
      window.ExcelLensRichText.same(a.richText,b.richText) &&
      (window.ExcelLensConditional?.same(a.conditional,b.conditional)??a.conditional===b.conditional) && a.presentationOnly===b.presentationOnly &&
      a.cacheMissing===b.cacheMissing &&
      a.hyperlink?.kind===b.hyperlink?.kind && a.hyperlink?.sheet===b.hyperlink?.sheet &&
      a.hyperlink?.ref===b.hyperlink?.ref && a.hyperlink?.url===b.hyperlink?.url;
  }

  function activeMerge(row,col) {
    if(!S.indexReady||S.filtered)return null;
    const range=mergedCells.at(row,col);
    if(!range)return null;
    if(S.rowVisibility&&!S.rowVisibility.visible(range.row))return null;
    const {rows=0,cols=0}=S.freeze;
    if((range.row<=rows&&range.endRow>rows)||(range.col<=cols&&range.endCol>cols))return null;
    return range;
  }
  function crosshairCell() {
    const cell=S.selected,point=cell?.crosshair;
    if(!point||!rowVisible(point.row)||S.hiddenColumns.has(point.col))return cell;
    const merge=activeMerge(point.row,point.col);
    return merge?.row===cell.row && merge.col===cell.col ? point : cell;
  }
  function decorateMergedCell(node,row,col,focus) {
    mergedCells.decorate(node,activeMerge(row,col),row,col,colLeft,rowTop,colWidth);
    if(node._xlMerge)mergedCells.paintCrosshair(node,focus??crosshairCell(),colLeft,rowTop);
  }

  // Updating the text node only preserves AutoFilter buttons and existing
  // DOM nodes. A formula-mode switch never issues a new worksheet request.
  function replaceCellText(node, value) {
    if(node.firstElementChild?.classList.contains('cell-overflow-content'))node.firstElementChild.textContent=value;
    else if (node.firstChild?.nodeType === Node.TEXT_NODE) node.firstChild.nodeValue = value;
    else node.insertBefore(document.createTextNode(value),node.firstChild);
  }
  function toggleFormulaView() {
    S.showFormulas = !S.showFormulas;
    $('formulaViewBtn').setAttribute('aria-pressed',String(S.showFormulas));
    // Paint keys include S.showFormulas; re-rendering from the current tile
    // updates cell values, classes and text-spill in place without blanking rows.
    if(S.viewportData) {renderGrid(S.viewportData.data,S.viewportData.request);frozenPanes.schedule(true);}
    else {
      for (const el of document.querySelectorAll('.grid-region .grid-cell')) {
        const data = S.cellMap.get(el.dataset.ref) || S.frozenCellMap.get(el.dataset.ref);
        replaceCellText(el,displayCellValue(data));
        el.classList.toggle('formula-view-cell',Boolean(S.showFormulas && data?.hasFormula));
      }
    }
    toast(S.showFormulas?'显示公式':'显示数值');
  }

  function makeGridCell(cellData, rowData, rowNumber, visual, layout, byColumn, columnLayouts, position) {
    const {col, letter, left, width} = layout;
    const ref = `${letter}${rowNumber}`;
    const cell=cellPresenter.create(cellData,rowData,rowNumber,col);
    cell.dataset.ref = ref;
    cell.dataset.row = String(rowNumber);
    cell.dataset.col = String(col);
    cell.dataset.visualIndex = String(visual);
    updateGridCellGeometry(cell,layout);
    // The custom hover inspector reads S.cellMap; native titles duplicate
    // enormous formulas in DOM attributes and can cause slow hover reflow.
    return cell;
  }

  function updateGridCellGeometry(cell,layout){
    if(cell._xlColumnLeft!==layout.left){cell.style.left=layout.left+'px';cell._xlColumnLeft=layout.left;}
    if(cell._xlColumnWidth!==layout.width){cell.style.width=layout.width+'px';cell._xlColumnWidth=layout.width;}
  }

  // Only resolved, validated link targets reach this function. Never run a
  // HYPERLINK formula or accept file:/javascript: from workbook content.
  async function followCellHyperlink(link) {
    if (!link) return;
    if (link.kind === 'internal') {
      const target = S.sheets.find(item => item.name === link.sheet);
      if (!target || !parseRef(link.ref)) { toast('\u8df3\u8f6c\u76ee\u6807\u5df2\u5931\u6548','error'); return; }
      await navigateToCell(target.name, link.ref);
    } else if (link.kind === 'external') {
      try {
        const target = new URL(link.url);
        if (!['https:', 'http:', 'mailto:'].includes(target.protocol) ||
            (target.protocol !== 'mailto:' && !target.hostname)) throw new Error('unsupported');
        const opened = window.open(target.href, '_blank', 'noopener,noreferrer');
        if (!opened) { /* Browsers may deliberately return null with noopener. */ }
      } catch { toast('\u4e0d\u652f\u6301\u8be5\u8d85\u94fe\u63a5\u534f\u8bae','warning'); }
    }
  }

  function linkDescription(link) {
    if (!link) return '';
    return link.kind === 'internal' ? `${link.sheet}!${link.ref}` : link.url;
  }

  function paintRangeCell(node,row,col,visual,list=window.ExcelLensMultiSelection.areas(S)){
    if (editCopy?.grid.active) editCopy.grid.ranges.paintCut(node,S.sheet,row,col);
    const through=S.filtered?S.selectedFrozenThrough||0:0;
    const inside=list.some(area=>window.ExcelLensMultiSelection.contains(area,row,col,visual,S.filtered,through));
    if(inside){
      const style=cellPresenter.style(node._xlModel,node._xlRowStyle)||{},foreground=S.showFormulas&&node._xlModel?.hasFormula?'#315d8b':S.showStyles?(window.ExcelLensConditional?.color(node._xlModel,style)??(node._xlModel?.valueColor||style.color)):undefined;
      const colorKey=[style.background||'',foreground||''].join('|');
      if(node._xlRangeColor!==colorKey){node._xlRangeColor=colorKey;node.style.setProperty('--range-fill',window.ExcelLensCellView.selectionFill(style.background,foreground));}
    }
    if(node._xlInRange!==inside) {node._xlInRange=inside;node.classList.toggle('range-cell',inside);}
  }

  // Only the visible row/column bands along the perimeter need a pen lookup.
  // Keep the owning cell, including a merged predecessor, as the authority.
  let selectionRasterRatio=0,selectionRasterStep=1;
  function selectionSeams(view,pinned) {
    const rowBands=new Map(), columns=new Set(), styles=new WeakMap();
    const ratio=window.devicePixelRatio||1,layer=$('rangeFrames');
    if(ratio!==selectionRasterRatio){
      selectionRasterStep=parseFloat(getComputedStyle(layer,'::before').borderTopWidth)||1/ratio;
      selectionRasterRatio=ratio;
    }
    const raster=Math.abs(selectionRasterStep*ratio-1)>.001
      ? {step:selectionRasterStep,origin:layer.getBoundingClientRect()} : null;
    function addRow(row,visual){
      const [from,to]=view.rowBounds(row,visual);
      if(to>from)rowBands.set(from,{row,visual,from,to});
    }
    for(const [visual,node] of S.renderedRows)addRow(Number(node.dataset.row),visual);
    for(let row=1;row<=pinned.rows;row++)if(rowVisible(row))addRow(row,row);
    const first=S.renderedRows.values().next().value;
    for(const col of first?._xlCells?.keys()||[])columns.add(col);
    for(let col=1;col<=pinned.cols;col++)columns.add(col);
    const rows=[...rowBands.values()].sort((a,b)=>a.from-b.from);
    const cols=[...columns].sort((a,b)=>a-b).map(col=>({col,from:colLeft(col),to:colLeft(col+1)})).filter(c=>c.to>c.from);
    const read=(node,side,index)=>window.ExcelLensGridBorders.usedWidth(node,side,index,styles);
    function owner(band,col){
      const merge=activeMerge(band.row,col),r=merge?.row??band.row,c=merge?.col??col;
      const frozen=r<=pinned.rows||c<=pinned.cols;
      return (frozen&&frozenPanes.cells.get(colName(c)+r)) || S.renderedRows.get(merge?r:band.visual)?._xlCells?.get(c);
    }
    function edge(horizontal,position,start,end){
      const across=horizontal?rows:cols,along=horizontal?cols:rows;
      const leading=position===0, band=across.find(b=>leading?b.from===0:b.to===position);
      const segments=[];
      for(const b of along){
        if(b.to<=start||b.from>=end)continue;
        let width=0,inside=false;
        if(band){
          const row=horizontal?band:b,col=horizontal?b.col:band.col;
          const cell=owner(row,col);
          const side=horizontal?(leading?'Top':'Bottom'):(leading?'Left':'Right');
          width=read(cell,side,horizontal?col:row.row);
          inside=leading&&width>0;
          if(position>0&&position===(horizontal?view.frozenHeight:view.frozenWidth)){
            const pane=horizontal?(col<=pinned.cols?'freezeCorner':'freezeTop'):(row.row<=pinned.rows?'freezeCorner':'freezeLeft');
            width=read($(pane),horizontal?'Bottom':'Right',0)||width;
          }
          if(leading&&!width){
            const header=horizontal?$('columnHeaders'):S.renderedRows.get(row.visual)?.firstElementChild;
            width=read(header,horizontal?'Bottom':'Right',0);
          }
        }
        const offset=(inside?width:-width)/2;
        const from=Math.max(start,b.from),to=Math.min(end,b.to),last=segments.at(-1);
        if(last&&last.to===from&&last.offset===offset)last.to=to;
        else segments.push({from,to,offset});
      }
      return segments;
    }
    return (x1,x2,y1,y2)=>({raster,top:edge(true,y1,x1,x2),bottom:edge(true,y2,x1,x2),
      left:edge(false,x1,y1,y2),right:edge(false,x2,y1,y2)});
  }

  function scheduleRangeFrames() {
    if (S.rangeFrameRaf) return;
    S.rangeFrameRaf = requestAnimationFrame(() => {
      S.rangeFrameRaf = 0;
      paintRangeFrames();
    });
  }

  function paintRangeFrames(areas = window.ExcelLensMultiSelection.areas(S)) {
    if (S.rangeFrameRaf) { cancelAnimationFrame(S.rangeFrameRaf); S.rangeFrameRaf = 0; }
    const layer=$('rangeFrames');
    const visible=S.indexReady || S.previewOnly;
    layer.parentElement.classList.toggle('has-range',Boolean(visible && areas.length));
    const single=visible && !areas.length && S.selected;
    const tails=$('crosshairTails');
    tails.hidden=!single;
    layer.parentElement.classList.toggle('has-cell-selection',Boolean(single));
    if (single) {
      const merge=activeMerge(single.row,single.col);
      areas=[{row1:merge?.row??single.row,col1:merge?.col??single.col,row2:merge?.endRow??single.row,col2:merge?.endCol??single.col,
        visual1:S.selectedVisualIndex,visual2:S.selectedVisualIndex}];
    }
    const references=$('formulaEntryFrames');
    const refs=visible && editCopy?.grid.active ? editCopy.grid.entry.areas(S.sheet) : [];
    if (visible && editCopy?.grid.active && editCopy.grid.fillHandle?.area) refs.push(editCopy.grid.fillHandle.area);
    if (!layer.childElementCount && !areas.length && !references?.childElementCount && !refs.length) return;
    const pinned=S.indexReady ? frozenPanes.effective() : {rows:0,cols:0}, grid=$('grid');
    const frozenHeight=sourceRowTop(pinned.rows+1);
    const geometry={
      width:grid.clientWidth,height:grid.clientHeight,left:grid.scrollLeft,top:getVirtualScrollTop(),
      header:headerHeight(),rowHead:ROW_HEAD,frozenWidth:colLeft(pinned.cols+1),frozenHeight,colLeft,
      rowBounds:(row,visual)=>S.filtered && row>pinned.rows
        ? [frozenHeight+rowTop(visual),frozenHeight+rowTop(visual+1)] : [sourceRowTop(row),sourceRowTop(row+1)]
    };
    let seams;
    geometry.seams=(...bounds)=>(seams ||= selectionSeams(geometry,pinned))(...bounds);
    window.ExcelLensRangeFrame.paint(layer,visible ? areas : [],geometry);
    if (single) {
      const focus=crosshairCell();
      window.ExcelLensRangeFrame.paintCrosshair(tails,focus,S.filtered ? S.selectedVisualIndex : focus.row,geometry,colTotal(S.maxCol),sheetHeight(S.filtered?S.filterTotal:S.maxRow));
    }
    if (references && (refs.length || references.childElementCount)) {
      const shown=window.ExcelLensEditReferenceView.visibleAreas(refs,S.filtered,pinned.rows,S.rowNumberByVisual,activeMerge);
      window.ExcelLensRangeFrame.paint(references,shown,geometry,2);
    }
  }

  function decorateGridCell(cell, row, col, ref, focus) {
    cell.classList.toggle("selected",S.selected?.ref === ref);
    cell.classList.toggle("active-column",focus?.col === col);
    cell.classList.toggle("precedent",S.traceRefs.length > 0 && isPrecedent(row,col));
    cell.classList.toggle("search-match",isSearchMatch(S.sheet,ref));
    paintRangeCell(cell,row,col,Number(cell.dataset.visualIndex));
    const columnSpan=selectedColumnSpan();
    cell.classList.toggle('column-range-cell',Boolean(columnSpan&&col>=columnSpan.start&&col<=columnSpan.end));
    cell.classList.toggle("audit-marked",Boolean(bookmarkCenter?.has(S.sheet,ref)));
    formulaInspector?.decorateCell(cell,row,col,S.sheet);
    selectionInspector?.decorateCell(cell,ref,S.sheet);
  }

  const finishedCells=new Map();
  let finishedCellContext='';
  const rowPool=new Map();
  let poolCells=0;
  function retireRow(row){
    row.remove();
    const cells=row._xlCells.size;
    if(!cells||row._xlSheetEpoch!==S.sheetEpoch||cells>384||row._xlHeavy)return;
    const visual=Number(row.dataset.visualIndex),old=rowPool.get(visual);
    if(old)poolCells-=old._xlCells.size;
    rowPool.delete(visual);rowPool.set(visual,row);poolCells+=cells;
    while(poolCells>384){const [k,r]=rowPool.entries().next().value;rowPool.delete(k);poolCells-=r._xlCells.size;}
  }
  function renderGrid(data,request){
    const interrupted=Boolean(gridWork);
    cancelGridWork();const steps=gridSteps(data,request,false);steps.next();
    if(interrupted)scheduleViewport();
  }
  function* gridSteps(data, request, sliced, sliceBudget=sliced?6:0,retain=false) {
    const started=performance.now();let sliceStart=started,cpuMs=0;
    const initialDecor=S.decorVersion;
    cellPresenter.prepare(data.rows);
    if (data.hyperlinkError && S.linkErrorSheet !== S.sheet) {
      S.linkErrorSheet = S.sheet;
      toast('\u5de5\u4f5c\u8868\u8d85\u94fe\u63a5\u5143\u6570\u636e\u8bfb\u53d6\u5931\u8d25','error');
    }
    const hh = headerHeight();
    const canvas = $("gridCanvas");
    const rowExtent = S.filtered ? data.totalRows : S.maxRow;
    const width = ROW_HEAD + colTotal(S.maxCol);
    const height = hh + sheetHeight(rowExtent);
    const grid=$('grid');
    // Measure scrollport BEFORE modifying canvas dimensions. Writing style
    // and immediately reading clientWidth forced a synchronous layout on
    // every tile commit, even when the entire workbook size was unchanged.
    const pinnedHeight=S.filtered?sourceRowTop(frozenPanes.effective().rows+1):0;
    const measured=sliced?request.position:null;
    const canvasWidth=`${Math.max(width,measured?.w??grid.clientWidth)}px`;
    const canvasHeight=`${measured?virtualScroll.configure(height,measured.h):physicalCanvasHeight(height,grid)}px`;
    S.virtualScrollScale=virtualScroll.scale;
    if(canvas.hasAttribute('data-viewport')!==S.indexReady)canvas.toggleAttribute('data-viewport',S.indexReady);
    if(canvas.style.width!==canvasWidth)canvas.style.width=canvasWidth;
    if(canvas.style.height!==canvasHeight)canvas.style.height=canvasHeight;
    if(canvas.style.getPropertyValue('--canvas-width')!==canvasWidth)
      canvas.style.setProperty('--canvas-width',canvasWidth);

    const columns=Array.isArray(data.columns)&&data.columns.length?data.columns:
      Array.from({length:data.colCount},(_,i)=>data.startCol+i);
    const visibleColumns=columns.filter(col=>!S.hiddenColumns.has(col)),focus=crosshairCell();
    const headerKey=`${visibleColumns.join(',')}|${S.zoom}|${geometry.version}|${window.devicePixelRatio}|${S.indexReady}|${S.textHeaderRevision}|${ROW_HEAD}|${S.sheetEpoch}`;
    const commitHeaders=()=>{if (S.renderedHeadersKey !== headerKey) {
      renderColumnHeaders(visibleColumns,width,focus);
      S.renderedHeadersKey = headerKey;
    }};
    const rowDataByNumber = new Map();
    const visualRows = new Map();
    if (S.filtered) data.rows.forEach((row,i) => visualRows.set(request.displayStart+i,row));
    else for (const row of data.rows) rowDataByNumber.set(row.number,row);

    const models=new Map(),rowNumbers=new Map();
    for(const row of data.rows)for(const cell of row.cells||[])models.set(`${colName(cell.col)}${row.number}`,cell);
    const previous = S.renderedRows;
    const next=retain?new Map(previous):new Map();
    const newNodes = document.createDocumentFragment();
    const firstVisual = request.displayStart;
    const lastVisual = S.filtered ? Math.min(data.totalRows,request.displayStart+data.rows.length-1) : Math.min(S.maxRow,request.displayEnd);
    let reusedRows=0, createdRows=0, createdCells=0;
    let liveRoom=sliced&&sameViewportRows(S.window,request)?DOM_MARGIN_BUDGET:0;
    if(liveRoom)for(const row of previous.values())liveRoom-=row._xlCells.size;
    const lastCol=(columns.at(-1)||data.startCol)+1;
    // The canvas owns the full horizontal scroll size, not each physical row.
    // A 16,384-column workbook can have a >2-million-pixel canvas; giving
    // every row that width causes enormous off-screen paint/hit-test regions.
    // Limit each row to the currently buffered band while preserving absolute
    // cell coordinates and sticky row headers.
    const rowPaintWidth = `${ROW_HEAD + colLeft(lastCol)}px`;
    // Geometry is invariant across all rows in this viewport. Previously each
    // cell recalculated sparse column offsets (binary search) and labels.
    const layoutFor=col=>({col,letter:colName(col),left:ROW_HEAD+colLeft(col),width:colWidth(col)});
    const bodyLayouts=visibleColumns.map(layoutFor);
    const layoutVersion=geometry.version+'|'+S.zoom+'|'+(window.devicePixelRatio||1);
    const paintKey=cellPresenter.paintKey(),cellContext=[S.sheetEpoch,layoutVersion,paintKey,ROW_HEAD,S.source?.format].join('|');
    if(cellContext!==finishedCellContext){finishedCells.clear();finishedCellContext=cellContext;}
    const numericPadding=S.showStyles&&fileFormats.source(S.source?.format).styled?5*scale():14;
    const extraMarks=!!(S.range||S.multiRanges?.length||selectedColumnSpan()||S.traceRefs.length||
      S.searchHitMap?.get(S.sheet)?.size||bookmarkCenter?.keys?.size||formulaInspector?.active||
      selectionInspector?.hits?.size||editCopy?.grid.active);
    const mountStart=request.mountColStart??visibleColumns[0],mountLeft=ROW_HEAD+colLeft(mountStart||1);
    const replacedRows=[],updates=[];
    const reusable=S.reusableSheetRows,restoring=Boolean(reusable&&S.reusableSheetData===data);
    S.reusableSheetRows=null;S.reusableSheetData=null;
    const rowsLayer=$('rowsLayer');
    const physicalScroll=measured?.physical??grid.scrollTop;
    const visuals=[...(data.anchors?.keys()||[])].filter(r=>r<firstVisual);
    for(let r=firstVisual;r<=lastVisual;r=S.filtered?r+1:(nextVisibleRow(r+1)||Infinity))visuals.push(r);
    if(sliced){
      const priority=v=>{
        if(v<firstVisual)return -3;
        if(v>=request.visibleStart&&v<=request.visibleEnd){
          const row=previous.get(v);
          return !row||!row._xlCells.has(request.visibleColStart)||!row._xlCells.has(request.visibleColEnd)?0:2;
        }
        return (motion.vy<0?v<request.visibleStart:v>request.visibleEnd)?1:3;
      };
      const order=new Map(visuals.map(v=>[v,priority(v)]));
      visuals.sort((a,b)=>order.get(a)-order.get(b)||(motion.vy>0?b-a:a-b));
    }
    for(const visual of visuals){
      const extra=data.anchors?.get(visual);
      const columnLayouts=extra?[...extra.filter(c=>visual<firstVisual||c<data.startCol).sort((a,b)=>a-b).map(layoutFor),...(visual<firstVisual?[]:bodyLayouts)]:bodyLayouts;
      if(!S.filtered&&!rowVisible(visual))continue;
      const rowData = S.filtered ? visualRows.get(visual) : rowDataByNumber.get(visual);
      if (S.filtered && !rowData) continue;
      const rowNumber = rowData?.number || visual;
      rowNumbers.set(visual,rowNumber);
      const oldRow=previous.get(visual),savedRow=reusable?.get(visual);
      let row=savedRow||oldRow;
      const pooled=!row&&rowPool.get(visual);
      if(pooled){
        rowPool.delete(visual);poolCells-=pooled._xlCells.size;
        if(pooled.dataset.row===String(rowNumber)){row=pooled;row._xlDecorVersion=-1;newNodes.appendChild(row);}
      }
      if(savedRow&&savedRow.dataset.row===String(rowNumber)){
        savedRow._xlSheetEpoch=S.sheetEpoch;savedRow._xlDecorVersion=-1;
        if(savedRow.parentElement!==rowsLayer)newNodes.appendChild(savedRow);
      }
      if(oldRow&&oldRow!==row)replacedRows.push(oldRow);
      if (row && (row.dataset.row !== String(rowNumber)||row._xlSheetEpoch!==S.sheetEpoch)) {
        replacedRows.push(row);row=null;
      }
      if (!row) {
        createdRows++;
        row = document.createElement("div");
        row.className="grid-row";
        row.dataset.visualIndex=String(visual);
        row.dataset.row=String(rowNumber);
        row._xlSheetEpoch=S.sheetEpoch;
        row.style.width = rowPaintWidth;
        row._xlCells=new Map();
        const rowHead = document.createElement("div");
        rowHead.className="row-head";
        rowHead.textContent=String(rowNumber);
        rowHead.title=`${rowNumber} · 拖动下边缘调整行高`;
        const grip=document.createElement('span');grip.className='row-resize-grip';
        bindResizeGrip(grip,'row',rowNumber);
        rowHead.append(grip);
        row.appendChild(rowHead);
        newNodes.appendChild(row);
      } else {
        reusedRows++;
        if (row.style.width !== rowPaintWidth) row.style.width = rowPaintWidth;
      }
      // Repeated assignments to unchanged geometry can invalidate CSS layout
      // even when every recycled row already occupies the correct pixel band.
      const logicalTop = rowTop(visual)+pinnedHeight;
      const topPx = `${virtualScroll.rowPosition(logicalTop,physicalScroll)}px`;
      const heightPx = `${rowHeight(visual)}px`;
      if(row.style.top!==topPx)row.style.top=topPx;
      if(row.style.height!==heightPx){row.style.height=heightPx;row.style.setProperty('--row-height',heightPx);}
      if(row.firstElementChild.style.height!==heightPx)row.firstElementChild.style.height=heightPx;
      row.classList.toggle("active-row",focus?.row === rowNumber);
      row.classList.toggle("whole-row-selected",S.wholeRowSelected && S.selected?.row === rowNumber);
      row.classList.toggle('table-header-row',window.ExcelLensTableHeaders.isTable(S.autoFilter)&&rowNumber===S.autoFilter.row);
      row.classList.toggle('table-band',window.ExcelLensTableHeaders.isTable(S.autoFilter)&&rowNumber>S.autoFilter.row&&(visual-S.autoFilter.row)%2===0);
      // Sparse payloads use a compact plain object: fewer temporary Map and
      // arrow-function allocations when a tile contains thousands of cells.
      let byColumn=viewport.rowColumns(rowData)||{};
      if(editCopy?.grid.active)byColumn={...byColumn};
      if (editCopy?.grid.active) for (const {col,letter} of columnLayouts) byColumn[col]=editCopy.grid.project(S.sheet,letter+rowNumber,byColumn[col]);
      const belowRow=S.filtered?visualRows.get(visual+1):rowDataByNumber.get(nextVisibleRow(rowNumber+1));
      const belowColumns=viewport.rowColumns(belowRow)||{};
      const redecorate = row._xlDecorVersion !== S.decorVersion;
      const newCells=document.createDocumentFragment();
      const cells=retain?new Map(row._xlCells):new Map(),remove=[];
      const stableRows=row._xlBelow===belowRow?.number&&row._xlLayoutVersion===layoutVersion&&!editCopy?.grid.active;
      const stableColumns=row._xlGridKey===headerKey;
      const mergeFree=S.filtered||!mergedCells.root||!mergedCells.intersect(rowNumber,belowRow?.number||rowNumber+1,bodyLayouts[0]?.col||1,lastCol).length;
      row._xlHeavy=Boolean(rowData?.cells?.some(c=>c.richText||c.conditional||(c.raw?.length||0)+(c.display?.length||0)+(c.formula?.length||0)>512));
      for(let layoutIndex=0;layoutIndex<columnLayouts.length;layoutIndex++) {
        if(sliced&&columnLayouts.length>48&&(layoutIndex&7)===0&&performance.now()-sliceStart>=sliceBudget){
          cpuMs+=performance.now()-sliceStart;yield;sliceStart=performance.now();
        }
        const layout=columnLayouts[layoutIndex];
        const col=layout.col;
        const cellData=byColumn[col];
        const ref=`${layout.letter}${rowNumber}`;
        if(col<mountStart){
          // Keep the source model for selection and neighbouring seams. Only attach
          // the off-screen origin if its actual merge or measured text reaches the band.
          const merge=activeMerge(rowNumber,col);
          if(!(merge?.row===rowNumber&&merge.col===col&&merge.endCol>=mountStart)&&
            !(cellData&&layout.left+cellPresenter.spillWidth(cellData,byColumn,columnLayouts,layoutIndex,rowData?.style,rowNumber)>mountLeft))continue;
        }
        let cell=row._xlCells.get(col);
        if(cell)cells.set(col,cell);
        const nextCol=columnLayouts[layoutIndex+1]?.col;
        if(sliced&&cell&&stableRows&&!redecorate&&!cellData?.richText&&!cellData?.conditional&&
          cell._xlRowPresentationNotice===(rowData?.presentationNotice||'')&&cell._xlRowStyle===(rowData?.style||0)&&
          cell._xlModel===(cellData??null)&&(mergeFree||!activeMerge(rowNumber,col)&&!activeMerge(rowNumber,nextCol||col+1)&&!activeMerge(belowRow?.number||rowNumber+1,col))){
          const interior=layoutIndex>0&&cell._xlNextCol===nextCol&&cell._xlColumnLeft===layout.left&&cell._xlColumnWidth===layout.width;
          if(stableColumns||interior&&!window.ExcelLensCellView.isText(cellData))continue;
        }
        const replaced=Boolean(cell && (!sameRenderedCell(cell._xlModel,cellData)||cell._xlRowStyle!==(rowData?.style||0)||cellPresenter.needsPaint(cell,cellData,rowData)));
        if(replaced){
          // A tile can be re-fetched after a filter/sheet/row-content change.
          // Reused coordinates must not retain old text, style or spill state.
          const fresh=makeGridCell(cellData,rowData,rowNumber,visual,layout,byColumn,columnLayouts,layoutIndex);
          remove.push(cell);newCells.appendChild(fresh);cells.set(col,fresh);cell=fresh;createdCells++;
        }
        const fresh=!cell||replaced,needsDecoration=fresh||redecorate;
        let templateKey=null,cloned=false;
        if(!cell&&mergeFree&&belowRow&&nextCol&&cellData&&(!cellData.type||cellData.type==='n')&&
          !cellData.conditional&&!cellData.richText&&!cellData.hyperlink&&!cellData.cacheMissing&&
          !byColumn[nextCol]?.conditional&&!belowColumns[col]?.conditional&&!rowData?.presentationNotice&&
          !S.showFormulas&&!editCopy?.grid.active&&rowNumber!==S.autoFilter?.row){
          const metric=cellPresenter.numberMetrics(cellData,rowData?.style);
          if(metric&&metric.width<=layout.width-numericPadding+.25){
            const outerLeft=layoutIndex===0&&col>1&&layout.left===ROW_HEAD;
            templateKey=[cellData.style??rowData?.style??0,cellData.valueColor||'',!!cellData.hasFormula,
              byColumn[nextCol]?.style??rowData?.style??0,belowColumns[col]?.style??belowRow.style??0,rowTop(visual)===0,outerLeft].join('|');
            const saved=finishedCells.get(templateKey);
            if(saved){
              cell=saved.node.cloneNode(false);cell.textContent=metric.text;cell.dataset.ref=ref;
              cell.dataset.row=String(rowNumber);cell.dataset.col=String(col);cell.dataset.visualIndex=String(visual);
              Object.assign(cell,saved.metadata,{_xlModel:cellData,_xlRowStyle:rowData?.style||0});
              cells.set(col,cell);newCells.appendChild(cell);createdCells++;cloned=true;
            }
          }
        }
        if (!cell) {
          cell=makeGridCell(cellData,rowData,rowNumber,visual,layout,byColumn,columnLayouts,layoutIndex);
          cells.set(col,cell);
          newCells.appendChild(cell);
          createdCells++;
        }
        // Resolve borders using the source styles of BOTH adjacent cells.
        updateGridCellGeometry(cell,layout);
        cell._xlNextCol=nextCol;
        if(!cloned)updateSharedBorders(cell,cellData,rowData,nextCol?byColumn[nextCol]:null,rowData,
          belowColumns[col],belowRow,{nextCol,mergeFree,columns:visibleColumns,firstRow:firstVisual,lastRow:lastVisual,rows:rowDataByNumber,cellMap:models,outerLeft:layoutIndex===0&&col>1,outerTop:rowTop(visual)===0});
        // For column A, the row header is the single owner of A's left edge.
        if(layoutIndex===0&&col===1){
          const border=visualForBorder(cellData,rowData).borderLeft||'';
          if(row._xlHeaderSeam!==border){row._xlHeaderSeam=border;
            if(border)row.firstElementChild.style.borderRight=border;
            else row.firstElementChild.style.removeProperty('border-right');}
        }
        // Resolve the full merged width before fitting a source number. Text
        // spilling also depends on the current row's neighboring cells.
        if(!mergeFree||cell._xlMerge||cell._xlCovered)decorateMergedCell(cell,rowNumber,col,focus);
        // A new numeric cell admitted to the template cache already passed the
        // exact font/width check and cannot spill text into its neighbour.
        if(!templateKey&&(!restoring||!savedRow||replaced)&&(cellData||cell._xlOverflowSpan||cell._xlNumericSpan))
          extendTextIntoBlanks(cell,cellData,byColumn,columnLayouts,layoutIndex);
        if(templateKey&&!cloned&&!cell.childElementCount&&!cell.title){
          const metadata={};
          for(const key of ['_xlColumnLeft','_xlColumnWidth','_xlPaintKey','_xlRichAllowed','_xlRowPresentationNotice',
            '_xlborderRight','_xlborderBottom','_xlOuterLeft','_xlOuterTop','_xlNextCol'])metadata[key]=cell[key];
          if(finishedCells.size>=1024)finishedCells.clear();
          finishedCells.set(templateKey,{node:cell.cloneNode(false),metadata});
        }
        if(fresh&&!extraMarks){
          if(S.selected?.ref===ref)cell.classList.add('selected');
          if(focus?.col===col)cell.classList.add('active-column');
        }else if(needsDecoration)decorateGridCell(cell,rowNumber,col,ref,focus);
      }
      if(!oldRow&&newCells.childNodes.length)row.appendChild(newCells);
      for(const [col,cell] of row._xlCells)if(!cells.has(col))remove.push(cell);
      // Expose completed additions with their original model now. Existing rows
      // and columns remain interactive until the final rectangle is committed.
      // A superseding scroll reuses these exact nodes. No partial removal can
      // invalidate the last published coverage rectangle.
      if(sliced&&liveRoom>0&&(!oldRow||row===oldRow)){
        const additions=[];
        for(const [col,node] of cells)if(!oldRow||!oldRow._xlCells.has(col))additions.push([col,node]);
        if(additions.length&&additions.length<=liveRoom){
          liveRoom-=additions.length;
          if(!oldRow){if(virtualScroll.scale!==1)row.style.top=virtualScroll.rowPosition(logicalTop,S.renderedScrollTop)+'px';rowsLayer.append(row);previous.set(visual,row);}
          const ready=document.createDocumentFragment();
          for(const [col,node] of additions){
            if(node.parentNode!==row)ready.appendChild(node);
            row._xlCells.set(col,node);
            if(node._xlModel)S.cellMap.set(node.dataset.ref,node._xlModel);
          }
          if(ready.childNodes.length)row.appendChild(ready);
          S.rowNumberByVisual.set(visual,rowNumber);
        }
      }
      updates.push({row,cells,newCells,remove,below:belowRow?.number});
      next.set(visual,row);
      const spent=sliced?performance.now()-sliceStart:0;
      if(sliced&&(spent>=sliceBudget||spent>=3&&navigator.scheduling?.isInputPending?.({includeContinuous:true}))){
        cpuMs+=performance.now()-sliceStart;yield; sliceStart=performance.now();
      }
    }
    commitHeaders();
    for(const {row,cells,newCells,remove,below} of updates){
      if(newCells.childNodes.length)row.appendChild(newCells);
      for(const cell of remove)if(cell.parentNode===row)cell.remove();
      row._xlCells=cells;row._xlGridKey=headerKey;row._xlBelow=below;row._xlLayoutVersion=layoutVersion;row._xlDecorVersion=initialDecor;
    }
    if(retain){
      for(const [visual,row] of next){
        if(!rowNumbers.has(visual))rowNumbers.set(visual,Number(row.dataset.row));
        for(const node of row._xlCells.values())if(node._xlModel&&!models.has(node.dataset.ref))models.set(node.dataset.ref,node._xlModel);
      }
    }
    S.cellMap=models;S.rowNumberByVisual=rowNumbers;
    // Append first, remove stale nodes second: no frame ever contains a cleared
    // grid while the next buffered viewport is being reconciled.
    rowsLayer.appendChild(newNodes);
    for(const row of replacedRows)row.remove();
    for(const [visual,row] of previous) if(!next.has(visual))retireRow(row);
    S.renderedRows=next;
    S.renderedScrollTop=physicalScroll;
    if(sliced)followGridScroll();
    if(!sliced)syncTableColumnHeaders();
    if(sliced&&S.decorVersion!==initialDecor){
      const current=crosshairCell();
      for(const row of next.values()){
        const number=Number(row.dataset.row);
        row.classList.toggle('active-row',number===current?.row);
        for(const [col,node] of row._xlCells){
          decorateMergedCell(node,number,col,current);decorateGridCell(node,number,col,node.dataset.ref,current);
        }
        row._xlDecorVersion=S.decorVersion;
      }
    }
    if(S.selected||S.range||S.multiRanges?.length){if(sliced)scheduleRangeFrames();else paintRangeFrames();}
    const hasVisibleRows=S.filtered ? data.rows.length>0 : visibleSourceCount(1,S.maxRow)>0;
    $("gridEmpty").hidden=hasVisibleRows;
    if(!hasVisibleRows) $("gridEmpty").textContent=S.filtered?"没有可见的筛选记录；可在「行与大纲」中显示全部":"没有可见行；可在「行与大纲」中显示全部";
    updateGridStatus(data);
    editCopy?.grid.schedulePosition();
    traceUI?.refreshPulse();
    updateSearchNavButtons();
    S.renderMetrics={commits:S.renderMetrics.commits+1,reusedRows,createdRows,createdCells,
      lastMs:Math.round((cpuMs+performance.now()-sliceStart)*100)/100};
    motion.observe('render',performance.now()-started);
  }

  function renderColumnHeaders(columns, width, focus) {
    const fragment = document.createDocumentFragment();
    const corner = document.createElement("div");
    corner.className = "column-head corner";
    corner.textContent = "";
    corner.style.width = `${ROW_HEAD}px`;
    corner.style.height = `${headerHeight()}px`;
    fragment.appendChild(corner);
    for (const col of columns) {
      const head = document.createElement("div");
      const span=selectedColumnSpan();
      head.className = "column-head" + (focus?.col === col ? " active" : "") + (span&&col>=span.start&&col<=span.end?" column-selected":"");
      head.style.left = `${ROW_HEAD + colLeft(col)}px`;
      head.style.width = `${colWidth(col)}px`;
      head.style.height = `${headerHeight()}px`;
      head.dataset.col = String(col);
      if(!decorateTableColumnHeader(head,col)) {
        const label = document.createElement("span");
        label.textContent = colName(col);
        head.appendChild(label);
      }
      const grip=document.createElement('span');grip.className='column-resize-grip';
      bindResizeGrip(grip,'col',col);
      head.append(grip);
      // Pointer drag/Shift selection is owned by beginColumnDrag. Keyboard-
      // initiated clicks (detail=0) also select the entire column.
      head.addEventListener("click", event => {
        if(event.target.closest?.('.column-resize-grip,.column-filter,.auto-filter-button'))return;
        if(event.detail===0){selectColumnSpan(event.shiftKey?(S.columnAnchor||col):col,col);captureColumnSelection();}
      });
      fragment.appendChild(head);
    }
    const headers = $("columnHeaders");
    headers.style.width = `${width}px`;
    headers.style.height = `${headerHeight()}px`;
    headers.replaceChildren(fragment);
    paintColumnSelection();
  }

  function updateGridStatus(data) {
	if(S.previewOnly){$('gridStatus').textContent=`${S.sheet} · 首屏预览（完整索引建立中）`;return;}
    const total = S.filtered ? data.totalRows : S.maxRow;
    const filterText = S.filtered ? ` · 筛选后 ${Number(total).toLocaleString()} 行` : "";
    const tableText=window.ExcelLensTableHeaders.isTable(S.autoFilter)?` · 表头 第 ${S.autoFilter.row} 行${S.autoFilter.detected?'（自动）':''}`:'';
    const label=`${S.sheet} · 已用区域 ${S.dimension}${filterText}${tableText}`;
    if($("gridStatus").textContent!==label)$("gridStatus").textContent=label;
    $("filterStatus").hidden = !S.filtered;
    $("clearFilterBtn").disabled = !S.filtered;
  }

  function isPrecedent(row, col) {
    const bounds=activeMerge(row,col)||{row,col,endRow:row,endCol:col};
    for (const ref of S.traceRefs) {
      if (String(ref.sheet).toLocaleLowerCase() !== String(S.sheet).toLocaleLowerCase() || !ref.start) continue;
      if (bounds.row <= ref.end.row && bounds.endRow >= ref.start.row && bounds.col <= ref.end.col && bounds.endCol >= ref.start.col) return true;
    }
    return false;
  }

  // Cell selection is extremely frequent. Clearing every visible DOM cell on
  // each click or Shift+Arrow previously visited up to 12,000 nodes on the
  // hot input path. Touch only cells that were actually decorated and the
  // columns/rows involved in this change. Range selection is bounded by the
  // selected rectangle and the existing virtual viewport.
  function applySelectionClasses() {
    ++S.decorVersion;
    selectionQuick?.changed();transposeCenter?.changed();cellExplanation?.changed();profileCenter?.changed();
    const old=S.lastHighlight;
    const focus=crosshairCell(),selectedRow=focus?.row||0,selectedCol=focus?.col||0;
    const anchorCol=S.selected?.col||0;
    const selectedRef=S.selected?.ref||"";
    // Selection on every Arrow/Tab key previously ran several document-wide
    // selector queries across thousands of cells. Only the old/new selected
    // columns and the actual visible rows can change. All other classes stay.
    for(const row of S.renderedRows.values()){
      const rowNumber=Number(row.dataset.row);
      const active=rowNumber===selectedRow;
      row.classList.toggle('active-row',active);
      row.classList.toggle('whole-row-selected',active&&Boolean(S.wholeRowSelected));
      const cells=row._xlCells;
      if(!cells)continue;
      if(old.col && old.col!==selectedCol)cells.get(old.col)?.classList.remove('active-column');
      if(old.ref && old.ref!==selectedRef){
        const previous=cells.get(old.anchorCol);
        if(previous?.dataset.ref===old.ref)previous.classList.remove('selected');
      }
      cells.get(selectedCol)?.classList.add('active-column');
      if(rowNumber===S.selected?.row)cells.get(anchorCol)?.classList.add('selected');
    }
    // Range highlight has a bounded visible intersection; only scan its DOM
    // when entering/leaving/changing a range, never on normal arrow presses.
    const areas=window.ExcelLensMultiSelection.areas(S);
    if((typeof mergedCells==='undefined'||mergedCells.root)&&(old.row!==selectedRow||old.col!==selectedCol)){
      for(const node of document.getElementsByClassName('merged-cell'))mergedCells.paintCrosshair(node,focus,colLeft,rowTop);
    }
    paintRangeFrames(areas);
    const hasFreeze=!S.freeze||S.freeze.rows>0||S.freeze.cols>0||(typeof frozenPanes!=='undefined'&&frozenPanes?.cells?.size>0);
    const pinnedCells=hasFreeze?document.querySelectorAll('.grid-region .pinned-cell'):[];
    for(const node of pinnedCells){
      node.classList.toggle('selected',node.dataset.ref===selectedRef);
      node.classList.toggle('crosshair-row',Number(node.dataset.row)===selectedRow);
      node.classList.toggle('active-column',Number(node.dataset.col)===selectedCol);
    }
    if(old.hadRange || areas.length){
      for(const [visual,row] of S.renderedRows)
        for(const [col,cell] of row._xlCells||[])paintRangeCell(cell,Number(row.dataset.row),col,visual,areas);
      for(const node of pinnedCells)
        paintRangeCell(node,Number(node.dataset.row),Number(node.dataset.col),Number(node.dataset.visualIndex),areas);
    }
    if(old.col && old.col!==selectedCol){
      for(const node of document.querySelectorAll(`.grid-region .column-head[data-col="${old.col}"]`))node.classList.remove('active');
    }
    if(selectedCol){
      for(const node of document.querySelectorAll(`.grid-region .column-head[data-col="${selectedCol}"]`))node.classList.add('active');
    }
    if(hasFreeze){
      for(const node of document.querySelectorAll('.grid-region .pinned-row-head'))node.classList.toggle('active',Number(node.dataset.row)===selectedRow);
    }
    S.lastHighlight={row:selectedRow,col:selectedCol,anchorCol,ref:selectedRef,hadRange:Boolean(areas.length)};
  }

  function selectCell(cell, rowNumber, colNumber, ref, visualIndex, preserveTrace = false, keepSelection = false, crosshair = null) {
    const normalizedRef = ref || cell?.ref || `${colName(colNumber)}${rowNumber}`;
    const col = colNumber || cell?.col || parseRef(normalizedRef)?.col || 1;
    const row = rowNumber || parseRef(normalizedRef)?.row || 1;
    const source = cell || { ref: normalizedRef, col, raw: "", display: "", hasFormula: false };
    const normalized = editCopy?.grid.active ? editCopy.grid.project(S.sheet,normalizedRef,source) : source;
    if(!preserveTrace&&(!S.selected||S.selected.ref!==normalizedRef||S.selected.sheet!==S.sheet))traceUI?.selectionChanged();
    if (!preserveTrace && S.traceRefs.length && (!S.selected || S.selected.ref !== normalizedRef || S.selected.sheet !== S.sheet)) {
      S.traceRefs = [];
      document.querySelectorAll(".grid-cell.precedent").forEach(node => node.classList.remove("precedent"));
    }
    S.selected = { ...normalized, ref: normalizedRef, row, col, sheet: S.sheet, crosshair };
    if (!keepSelection) {
      if(S.columnSelection){S.columnSelection=null;S.columnAnchor=0;paintColumnSelection();}
      S.wholeRowSelected = false;
      S.range = null; S.multiRanges = [];
      selectionStats?.clear();
      S.rangeAnchor = {row,col,visual:visualIndex||row};
      $("selectionStatus").textContent = "";
    }
    S.selectedVisualIndex = visualIndex || row;
    S.selectedFrozenThrough=S.filtered?frozenPanes.effective().rows:0;
    if (!keepSelection || !S.range) $("nameBox").value = normalizedRef;
    formulaInspector?.show(normalized,S.sheet);
    $("formulaValue").textContent = normalized.cacheMissing ? "无缓存" : (normalized.display ?? "");
    $("formulaValue").title = `保存结果：${normalized.cacheMissing ? "无缓存" : (normalized.display ?? "")}\n原始值：${normalized.raw ?? ""}`;
    $("tracePrecedentsBtn").disabled = !normalized.hasFormula || !normalized.formula;
    $("traceDependentsBtn").disabled = false;
    $("cellDetailBtn").disabled = false;
    $("copyFormulaBtn").disabled = !normalized.hasFormula || !normalized.formula;
    $("copyValueBtn").disabled = false;
    applySelectionClasses();
    selectionStats?.schedule();
    recordCardCenter?.controls();
    editCopy?.grid.selection();
  }

  function selectCellByAddress(ref, visualIndex, keepSelection = false) {
    const parsed = parseRef(ref);
    if (!parsed) return;
    const cell = S.cellMap.get(parsed.ref) || S.frozenCellMap.get(parsed.ref) || { ref: parsed.ref, col: parsed.col, raw: "", display: "", hasFormula: false };
    selectCell(cell, parsed.row, parsed.col, parsed.ref, visualIndex || parsed.row, keepSelection, keepSelection);
  }

  function selectInitialCell() {
    const firstSource=nextVisibleRow(1);
    const row = firstSource&&firstSource<=frozenPanes.effective().rows?firstSource:(S.rowNumberByVisual.values().next().value||(S.filtered?0:firstSource));
    if (!row) {
      S.selected=null;S.range=null;S.multiRanges=[];S.rangeAnchor=null;S.wholeRowSelected=false;
      $('nameBox').value='';$('selectionStatus').textContent='';formulaInspector?.clear();$('formulaValue').textContent='-';$('formulaValue').title='';
      for(const id of ['tracePrecedentsBtn','traceDependentsBtn','cellDetailBtn','copyFormulaBtn','copyValueBtn'])$(id).disabled=true;
      selectionStats?.clear();applySelectionClasses();return;
    }
    const column=nextVisibleColumn(1);
    const ref = `${colName(column)}${row}`;
    const visual=S.filtered?[...S.rowNumberByVisual].find(([,number])=>number===row)?.[0]||row:row;
    selectCell(cellAt(row, column), row, column, ref, visual);
    recordVisit(S.sheet,ref);
    $("grid").focus({ preventScroll: true });
  }

  function cellAt(row, col) {
    const ref = `${colName(col)}${row}`;
    return S.cellMap.get(ref) || S.frozenCellMap.get(ref) || { ref, col, raw: "", display: "", hasFormula: false };
  }

  function syncFilteredSelection(through) {
    if(!S.selected)return;
    const changed=S.selectedFrozenThrough!==through;
    if(!changed&&S.selectedVisualIndex)return;
    const {row,col}=S.selected;
    let visual=row<=through?row:0;
    if(!visual){
      for(const [position,source] of S.rowNumberByVisual){
        if(source===row){visual=position;break;}
      }
    }
    S.selectedVisualIndex=visual;
    S.selectedFrozenThrough=through;
    S.rangeAnchor={row,col,visual};
    if(changed&&(S.range||S.multiRanges.length||S.wholeRowSelected)){
      S.range=null;S.multiRanges=[];S.wholeRowSelected=false;
      $('selectionStatus').textContent='';
      selectionStats?.clear();
      applySelectionClasses();
    }
  }

  function beginGridNavigation(pinnedRows) {
    const intent={}, origin=S.selected, sheet=S.sheet, epoch=S.sheetEpoch;
    const book=S.book, key=S.workbookKey, filterId=S.filterId, viewId=S.rowView?.id;
    S.gridNavigation=intent;
    return ()=>S.gridNavigation===intent&&S.selected===origin&&sheet===S.sheet&&epoch===S.sheetEpoch&&
      book===S.book&&key===S.workbookKey&&filterId===S.filterId&&viewId===S.rowView?.id&&
      (!S.filtered||pinnedRows===frozenPanes.effective().rows);
  }

  function revealCellOffset(offset, extent, inset, start, size, leadingMargin) {
    const available = extent - inset;
    if (available <= 0) return offset;
    if (size >= available) return Math.max(0, start - inset);
    const slack = available - size;
    if (start < offset + inset) return Math.max(0, start - inset - Math.min(slack, leadingMargin));
    if (start + size > offset + extent) return start + size - extent + Math.min(slack, size);
    return offset;
  }

  // A row of zero is an unresolved position in the filtered body. Use its
  // visual index for scrolling, then resolve the source row from the tile.
  function scrollCellIntoView(row,col,visual,pinned) {
    ensureExtent(row||1,col);
    const grid=$('grid'),viewTop=getVirtualScrollTop(),viewLeft=grid.scrollLeft;
    let top=viewTop,left=viewLeft;
    if(!row||row>pinned.rows){
      const header=headerHeight(),frozenHeight=sourceRowTop(pinned.rows+1);
      const cellTop=header+rowTop(visual)+(S.filtered?frozenHeight:0),height=rowHeight(visual);
      top=revealCellOffset(viewTop,grid.clientHeight,header+frozenHeight,cellTop,height,height*2);
    }
    if(col>pinned.cols){
      const cellLeft=ROW_HEAD+colLeft(col),width=colWidth(col);
      left=revealCellOffset(viewLeft,grid.clientWidth,ROW_HEAD+colLeft(pinned.cols+1),cellLeft,width,width);
    }
    if(top!==viewTop||left!==viewLeft)grid.scrollTo({top:scrollTopToPhysical(top),left,behavior:'auto'});
  }

  async function locateFilteredVisual(row,through) {
    if(row<=through)return row;
    const params=new URLSearchParams({sheet:S.sheet,filterId:S.filterId,row:String(row),excludeThroughRow:String(through)});
    if(S.rowView)params.set('viewId',S.rowView.id);
    const match=await api('/api/filter/locate?'+params);
    return match.found&&match.row===row?match.visualIndex:0;
  }

  async function navigateToCell(sheet, ref, preserveTrace = false, guard = null) {
    const book=S.book,key=S.workbookKey,current=()=>book===S.book&&key===S.workbookKey&&(!guard||guard());
    if(!current())return false;
    let parsed = parseRef(ref);
    if (!parsed) { toast(`无法定位单元格：${ref}`,'error'); return false; }
    if (S.selected && !S.historyNavigating) recordVisit(S.selected.sheet, S.selected.ref);
    if (sheet !== S.sheet) return selectSheet(sheet, parsed.ref, preserveTrace, current);
    const epoch=S.sheetEpoch;
    if(S.rowVisibility&&!rowVisible(parsed.row)){
      if(!rowViews||!await rowViews.reveal(parsed.row,()=>current()&&epoch===S.sheetEpoch))return false;
      if(!current()||epoch!==S.sheetEpoch)return false;
      toast(`已临时显示第 ${parsed.row} 行`,'info');
    }
    const merged=activeMerge(parsed.row,parsed.col);
    if(merged)parsed=parseRef(`${colName(merged.col)}${merged.row}`);
    const pinned=frozenPanes.effective(),readCurrent=beginGridNavigation(pinned.rows);
    const viewCurrent=()=>current()&&readCurrent();
    const filteredBody=S.filtered&&parsed.row>pinned.rows;
    let visual=parsed.row;
    try{
      if(filteredBody){
        visual=await locateFilteredVisual(parsed.row,pinned.rows);
        if(!viewCurrent())return false;
        if(!visual){toast('该单元格所在行被筛选条件隐藏；可清除筛选后定位','warning');return false;}
      }
      scrollCellIntoView(parsed.row,parsed.col,visual,pinned);
      await loadViewport(true);
      if(!viewCurrent())return false;
      if(filteredBody&&S.rowNumberByVisual.get(visual)!==parsed.row){toast('尚未读取目标行，请重试定位','warning');return false;}
      const cell=S.cellMap.get(parsed.ref)||S.frozenCellMap.get(parsed.ref)||await getCell(sheet,parsed.ref);
      if(!viewCurrent())return false;
      selectCell(cell,parsed.row,parsed.col,parsed.ref,visual,preserveTrace);
      recordVisit(sheet,parsed.ref);
      $('grid').focus({preventScroll:true});
      return true;
    }catch(error){
      if(viewCurrent())throw error;
      return false;
    }
  }

  async function getCell(sheet, ref) {
    const params = new URLSearchParams({ sheet, ref });
    const data = await api(`/api/cell?${params.toString()}`);
    return data.cell || { ref, raw: "", display: "", hasFormula: false };
  }

  async function navigateCatalogueArea(area,guard) {
	if(!guard()||!S.opened)return false;
	if(S.opening||S.sheetLoadingEpoch||S.filterJob||rowViews?.busy)throw Error('请先完成当前工作表加载、筛选或行显隐更新');
	const target=S.sheets.find(sheet=>sheet.name===area?.sheet),start=parseRef(area?.start),end=parseRef(area?.end);
	if(!target||!start||!end||end.row<start.row||end.col<start.col)throw Error('目录定位范围无效，请重新读取目录');
	const multiple=start.ref!==end.ref;
	if(multiple&&(target.name===S.sheet?S.filtered:S.filtersBySheet.get(target.name)))throw Error('该表处于筛选视图，请先清除筛选，再按源坐标选择整个区域');
	showWorkspace();await navigateToCell(target.name,start.ref,false,guard);
	if(!guard()||S.sheet!==target.name||!S.selected||!S.indexReady)return false;
	const merge=activeMerge(start.row,start.col),anchor=merge?`${colName(merge.col)}${merge.row}`:start.ref;
	if(S.selected.ref!==anchor)return false;
	if(multiple){
	  if(S.filtered)throw Error('该表处于筛选视图，请先清除筛选后重试');
	  ensureExtent(end.row,end.col);S.multiRanges=[];extendRange({row:start.row,col:start.col,visual:start.row},end.row,end.col);
	  $('nameBox').value=`${start.ref}:${end.ref}`;
	}
	return true;
  }

  function adjacentGridPoint(targetRow,targetVisual,originCol,rowDelta,colDelta,pinned) {
    const originVisual=targetVisual;
    let targetCol = Math.max(1, Math.min(16384, originCol + colDelta));
    const fromMerge=activeMerge(targetRow,originCol);
    if(fromMerge&&colDelta===1)targetCol=Math.min(16384,fromMerge.endCol+1);
    if(colDelta && S.hiddenColumns.has(targetCol)) targetCol=(colDelta>0 ? nextVisibleColumn(targetCol) : previousVisibleColumn(targetCol)) || originCol;
    if (S.filtered) {
      if(rowDelta){
        if(targetRow<=pinned.rows){
          const header=advanceVisibleRow(targetRow,rowDelta);
          if(header&&header<=pinned.rows){targetRow=header;targetVisual=header;}
          else if(rowDelta>0&&S.filterTotal){targetRow=0;targetVisual=1;}
        }else{
          const header=previousVisibleRow(pinned.rows);
          if(targetVisual+rowDelta<1&&header){targetRow=header;targetVisual=header;}
          else{
            targetVisual=Math.max(1,Math.min(S.filterTotal,targetVisual+rowDelta));
            if(targetVisual!==originVisual)targetRow=S.window?.excludeThroughRow===pinned.rows?S.rowNumberByVisual.get(targetVisual)||0:0;
          }
        }
      }
    } else {
      targetRow = advanceVisibleRow(targetRow,rowDelta)||targetRow;
      if(fromMerge&&rowDelta===1)targetRow=nextVisibleRow(fromMerge.endRow+1)||targetRow;
      const destination=activeMerge(targetRow,targetCol);
      if(destination){targetRow=destination.row;targetCol=destination.col;}
      targetVisual=targetRow;
    }
    return {kind:'cell',row:targetRow,col:targetCol,visual:targetVisual};
  }

  async function moveSelection(rowDelta, colDelta, extend = false, keepAnchor = false) {
    if (!S.selected) {
      selectInitialCell();
      return;
    }
    const editingRange = extend && keepAnchor && S.range;
    let targetRow = editingRange ? S.range.row2 : S.selected.row;
    let targetVisual = editingRange ? S.range.visual2 || targetRow : S.selectedVisualIndex;
    const pinned = frozenPanes.effective();
    const current=beginGridNavigation(pinned.rows);
    let preceding=extend?S.multiRanges.slice():[];
    let anchor=extend?(S.rangeAnchor||{row:targetRow,col:S.selected.col,visual:targetVisual}):null;
    if(S.filtered&&(S.selectedFrozenThrough!==pinned.rows||!targetVisual)){
      targetVisual=await locateFilteredVisual(targetRow,pinned.rows);
      if(!current()||!targetVisual)return;
      preceding=[];
      if(extend)anchor={row:targetRow,col:S.selected.col,visual:targetVisual};
    }
    const point=adjacentGridPoint(targetRow,targetVisual,editingRange ? S.range.col2 : S.selected.col,rowDelta,colDelta,pinned);
    targetRow=point.row; targetVisual=point.visual;
    const targetCol=point.col;
    scrollCellIntoView(targetRow,targetCol,targetVisual,pinned);
    if (!targetRow || !S.window || !windowContains(getViewportRequest())) await loadViewport(true);
    if(!current())return;
    if(!targetRow&&S.window?.excludeThroughRow===pinned.rows)targetRow=S.rowNumberByVisual.get(targetVisual);
    if(!targetRow)return;
    const targetRef = `${colName(targetCol)}${targetRow}`;
    if (!extend || !keepAnchor) selectCell(cellAt(targetRow,targetCol),targetRow,targetCol,targetRef,targetVisual);
    if (extend){S.multiRanges=preceding;extendRange(anchor,targetRow,targetCol,targetVisual);}
  }

  // Ctrl+Arrow skips empty stretches like Excel, but confines the movement
  // to the real content extent. Go lazily caches each column's occupied rows;
  // no million-entry index is copied into the browser.
  async function jumpDataEdge(direction,extend=false) {
    if(!S.indexReady||!S.selected)return;
    if(S.largeData&&(direction==='up'||direction==='down')){toast('超大数据模式关闭纵向 Ctrl+方向键扫描；请使用地址框或 Ctrl+Home / Ctrl+End 定位','warning');return;}
    if(S.filtered){toast('数据边界快捷导航暂不适用于排序/筛选视图；请先清除筛选','warning');return;}
    const sheet=S.sheet,epoch=S.sheetEpoch;
    const anchor=extend?(S.rangeAnchor||{row:S.selected.row,col:S.selected.col}):null;
    const preceding=extend?S.multiRanges.slice():[];
    S.navigationAbort?.abort();
    const abort=new AbortController();S.navigationAbort=abort;
    const ticket=++S.navigationTicket;
    const origin=S.selected.ref;
    setStatus(`正在查找 ${direction==='up'||direction==='down'?'列':'行'}数据边界…`);
    try{
      const params=new URLSearchParams({sheet,ref:origin,direction});
      const result=await api(`/api/navigate?${params}`,{signal:abort.signal});
      if(abort.signal.aborted||ticket!==S.navigationTicket||S.sheetEpoch!==epoch||S.sheet!==sheet)return;
      await navigateToCell(sheet,result.ref);
      if(anchor && S.sheetEpoch===epoch){S.multiRanges=preceding;extendRange(anchor,result.row,result.col);}
    }catch(error){if(!abort.signal.aborted)toast(error.message,'error')}
    finally{if(ticket===S.navigationTicket){S.navigationAbort=null;setStatus(`已打开 · ${sheet}`)}}
  }

  // Excel-like frozen panes, persisted per sheet in the workbook fingerprint cache.
  function updateFreezeUI() {
    recordCardCenter?.controls();selectionQuick?.changed();transposeCenter?.changed();cellExplanation?.changed();profileCenter?.changed();
    const rows = S.freeze.rows, cols = S.freeze.cols;
    const label = !rows && !cols ? "冻结" : `冻结 ${rows ? rows + "行" : ""}${cols ? " " + cols + "列" : ""}`;
    $("freezeBtnLabel").textContent = label;
    $("freezeStatus").hidden = !rows && !cols;
    $("freezeStatus").textContent = label;
  }

  function openFreezeDialog() {
    if (!S.indexReady) return;
    $("freezeRowsInput").value = S.freeze.rows;
    $("freezeColsInput").value = S.freeze.cols;
    openDialog("freezeDialog");
  }

  async function applyFreeze(rows, cols) {
    rows = Number(rows); cols = Number(cols);
    if (!Number.isSafeInteger(rows) || !Number.isSafeInteger(cols) || rows < 0 || cols < 0 || rows > 30 || cols > 12) {
      toast("冻结行数应为0–30，列数应为0–12",'warning'); return;
    }
    if (!S.indexReady) return;
    const sheet = S.sheet, epoch = S.sheetEpoch;
    try {
      await api("/api/view", {method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({sheet,rows,cols})});
      if (epoch !== S.sheetEpoch) return;
      S.freeze = {rows,cols};
      frozenPanes.reset();
      updateFreezeUI();
      invalidateViewport();
      updateCanvasSize();
      await loadViewport(true);
      frozenPanes.schedule(true);
      pictures?.schedule();
      closeDialog("freezeDialog");
    } catch(error) {toast(`保存冻结窗格失败：${error.message}`,'error');}
  }

  // Browsing history contains explicit jumps, not thousands of arrow-key moves.
  function recordVisit(sheet, ref) {
    if (!S.opened || !sheet || !ref || S.historyNavigating) return;
    const previous = S.history[S.historyIndex];
    if (previous?.sheet === sheet && previous?.ref === ref) return;
    S.history = S.history.slice(0,S.historyIndex + 1);
    S.history.push({sheet,ref});
    if (S.history.length > 90) S.history.shift();
    S.historyIndex = S.history.length - 1;
    updateHistoryButtons();
  }

  function updateHistoryButtons() {
    $("historyBackBtn").disabled = S.historyIndex <= 0;
    $("historyForwardBtn").disabled = S.historyIndex >= S.history.length - 1;
  }

  async function goHistory(delta) {
    const next = S.historyIndex + delta;
    if (next < 0 || next >= S.history.length) return;
    const item = S.history[next];
    S.historyNavigating = true;
    try {
      if(await navigateToCell(item.sheet,item.ref))S.historyIndex=next;
    } catch(error) {toast(error.message,'error');}
    finally {S.historyNavigating = false; updateHistoryButtons();}
  }

  // Multiple cells can be highlighted and copied without touching workbook values.
  function inSelectedRange(row,col,visualIndex=0){
    const selection=window.ExcelLensMultiSelection;
    const through=S.filtered?frozenPanes.effective().rows:0;
    return selection.areas(S).some(area=>selection.contains(area,row,col,visualIndex,S.filtered,through));
  }

  function extendRange(anchor,row,col,visualIndex=0) {
    if(!anchor)return;
    let v1=0,v2=0;
    if(S.filtered){
      v1=Number(anchor.visual)||Number(S.rangeAnchor?.visual)||0;
      v2=Number(visualIndex)||Number(document.querySelector(`.grid-row[data-row="${row}"]`)?.dataset.visualIndex)||0;
      if(!v2&&row===S.selected?.row)v2=S.selectedVisualIndex;
      if(!v1||!v2){toast('当前筛选行位置尚未加载，请先滚动到目标行','warning');return;}
    }
    S.rangeAnchor={...anchor,visual:v1||anchor.visual||anchor.row};
    const area={row1:anchor.row,col1:anchor.col,row2:row,col2:col,visual1:v1,visual2:v2};
    S.range=S.filtered ? area : mergedCells.expand(area,S.rowVisibility);
    const rows=S.rowVisibility?S.rowVisibility.selectionCount(S.range,S.filtered,frozenPanes.effective().rows):window.ExcelLensMultiSelection.rowCount(S.range,S.filtered,frozenPanes.effective().rows);
    const cols=Math.abs(S.range.col1-S.range.col2)+1;
    $('selectionStatus').textContent=S.multiRanges.length?`${S.multiRanges.length+1} 处选区`:`${rows}行 × ${cols}列 · ${(rows*cols).toLocaleString()}格`;
    applySelectionClasses();
    selectionStats?.schedule();
  }

  async function writeClipboardPayload(content,cut=false) {
    if(navigator.clipboard?.write && typeof ClipboardItem!=='undefined'){
      try {
        await navigator.clipboard.write([new ClipboardItem({
          'text/plain':new Blob([content.text],{type:'text/plain'}),
          'text/html':new Blob([content.html],{type:'text/html'})
        })]);
        if (!cut) toast('已复制','success');
        return true;
      } catch { /* Some browsers only allow HTML during a native copy event. */ }
    }
    let rich=false;
    const onCopy=event=>{
      if (!event.clipboardData) return;
      event.preventDefault();
      event.stopImmediatePropagation();
      event.clipboardData.setData('text/plain',content.text);
      event.clipboardData.setData('text/html',content.html);
      rich=true;
    };
    document.addEventListener('copy',onCopy,true);
    try {
      if (document.execCommand('copy') && rich) { if (!cut) toast('已复制','success'); return true; }
    } catch { /* A text-only clipboard can still accept the TSV. */ }
    finally { document.removeEventListener('copy',onCopy,true); }
    if (cut) throw Error('浏览器未写入剪切区域，请重新剪切');
    const copied=await copyText(content.text);
    if(copied)toast('已复制为纯文本，当前浏览器未写入表格格式','warning');
    return copied;
  }

  function clipboardIdentity(){return JSON.stringify([S.book,S.workbookKey,S.sheet,S.sheetEpoch,S.indexReady,S.filtered,S.filterId,S.rowView?.id,S.selected?.ref,S.selected?.row,S.selected?.col,S.selectedVisualIndex,S.range,S.multiRanges,S.showStyles,S.fontOverride,S.freeze,editCopy?.grid.active && editCopy.data.document.revision]);}
  function selectionSourceIdentity(){return JSON.stringify([S.book,S.workbookKey,S.sheet,S.sheetEpoch,S.indexReady,S.filtered,S.filterId,S.rowView?.id,S.freeze]);}
  function captureClipboardSelection({purpose='复制',limits=fileFormats.clipboard,withMerges=false}={}){
    if(!S.indexReady||!S.selected||S.sheetLoadingEpoch)throw Error('请先选择一个已加载的单元格或区域');
    if(editCopy?.grid.active && (editCopy.grid.stale || editCopy.pending || editCopy.abort))throw Error('请先确认当前编辑修订');
    if(S.multiRanges.length)throw Error(`请选择一个连续区域进行${purpose}`);
    const model=window.ExcelLensMultiSelection,visibility=S.rowVisibility;
    let area=model.areas(S,true).at(-1);
    if (withMerges && !S.filtered) area=mergedCells.expand(area,visibility);
    if (withMerges && S.filtered && !S.range) {
      const m=mergedCells.at(S.selected.row,S.selected.col);
      if (m && m.row===S.selected.row) area={...area,col1:m.col,col2:m.endCol};
    }
    const points=model.points(area,colName), col1=Math.min(area.col1,area.col2), col2=Math.max(area.col1,area.col2);
    const rowCount=S.rowVisibility?S.rowVisibility.selectionCount(area,S.filtered,frozenPanes.effective().rows):model.rowCount(area,S.filtered,frozenPanes.effective().rows);
    const colCount=col2-col1+1;
    if(rowCount<1)throw Error('选区没有可见行');
    if(limits&&(rowCount>limits.rows||colCount>limits.columns||rowCount*colCount>limits.cells))throw Error(`${purpose}最多${limits.rows}行、${limits.columns}列、${limits.cells}格，请缩小选区`);
    const sheet=S.sheet,identity=clipboardIdentity(),selected={...S.selected},viewId=S.rowView?.id||'',filterId=S.filtered?S.filterId:'',excluded=frozenPanes.effective().rows,book=S.book,workbookKey=S.workbookKey,styled=S.showStyles,styles=S.styles,fontOverride=S.fontOverride;
    const current=()=>identity===clipboardIdentity();
    const sourceIdentity=selectionSourceIdentity(),sourceCurrent=()=>sourceIdentity===selectionSourceIdentity();
    const mergeAt=withMerges && mergedCells.root ? (row,col)=>mergedCells.copyAnchor(row,col,visibility) : undefined;
    const rangeParams=()=>{
      const params=new URLSearchParams({sheet,start:points.start,end:points.end,book,workbookKey});
      if(viewId)params.set('viewId',viewId);
      if(filterId){
        params.set('filterId',filterId);
        params.set('visualStart',String(points.visualStart));
        params.set('visualEnd',String(points.visualEnd));
      }
      if(viewId||filterId)params.set('excludeThroughRow',String(excluded));
      return params;
    };
    return {book,workbookKey,sheet,styled,styles,fontOverride,current,sourceCurrent,mergeAt,rows:rowCount,firstColumn:col1,lastColumn:col2,...points,maxRow:sourceRowLimit(),headerRow:S.autoFilter?.row||S.sortHeaderBySheet.get(sheet)||S.source?.tableHeader?.row||1,
      label:`${sheet} · ${points.start}:${points.end} · ${rowCount}行 × ${colCount}列`,
      readTSV:()=>{
        const params=rangeParams();
        params.set('format','tsv');
        return api(`/api/range?${params}`,{},'text');
      },
      read:async({headerRow=0,richText=true,conditional=styled,force=false,portable=false}={},signal)=>{
        if(!current())throw Error('来源或选区已变化，请重新选择');
        // A lone cell already holds the current edit projection.
        if(rowCount===1&&colCount===1&&!headerRow&&!force&&!portable){
          const cell={...selected};if(!richText)delete cell.richText;if(!conditional)delete cell.conditional;
          return {book,workbookKey,sheet,startCol:col1,endCol:col2,rows:[{row:selected.row,cells:[cell]}]};
        }
        const params=rangeParams();
        params.set('format','json');params.set('richText',richText?'1':'0');params.set('conditional',conditional?'1':'0');
        if(portable)params.set('portable','1');
        if(headerRow)params.set('headerRow',String(headerRow));
        const payload=await api(`/api/range?${params}`,{signal});
        if(!current())throw Object.assign(Error('复制已取消，来源或选区已变化'),{name:'AbortError'});
        if (editCopy?.grid.active) {
          for (const row of payload.rows) row.cells=editCopy.grid.projectRow(sheet,row,col1,col2);
          if (payload.header) payload.header.cells=editCopy.grid.projectRow(sheet,payload.header,col1,col2);
        }
        return payload;
      }};
  }
  async function copySelection(plain=false,cut=false) {
    const activity=globalThis.ExcelLensActivity?.begin(cut?'正在剪切选区':'正在复制选区');
    const ranges=editCopy?.grid.active ? editCopy.grid.ranges : null;
    const epoch=ranges ? (ranges.copyEpoch=(ranges.copyEpoch||0)+1) : 0;
    try{
      const progressive=!ranges&&!cut;
      const snapshot=captureClipboardSelection({withMerges:!plain,limits:progressive?null:fileFormats.clipboard});
      if(!progressive)return await copyGridPayload(snapshot,plain,cut,ranges,epoch);
      return await window.ExcelLensClipboard.copyByBudget(snapshot,fileFormats.clipboard,plain,
        ()=>copyGridPayload(snapshot,false,false,null,0),
        async()=>{
          const text=await snapshot.readTSV();
          return snapshot.current()&&copyText(text,'已复制为纯文本');
        },
        ()=>{
          if(exportCenter.running||exportCenter.busy){toast('导出中心正在处理任务，请在完成后导出选区','warning');return;}
          exportCenter.open('selection',snapshot);
          toast('选区超过复制容量，可导出为文件','warning');
        });
    }catch(err){if(err.name!=='AbortError')toast(err.message,'error');}finally{activity?.end();}
  }

  async function copyGridPayload(snapshot,plain,cut,ranges,epoch) {
    const [payload,initial]=await Promise.all([
      snapshot.read({richText:!plain&&snapshot.styled,conditional:!plain&&snapshot.styled,portable:!plain&&!ranges}),
      !plain && editCopy?.grid.active && !ranges.view.mappedRows() ? editCopy.grid.ranges.copySnapshot(snapshot,cut) : null
    ]);
    const typed=!plain && ranges && ranges.view.mappedRows() && snapshot.current() ? await ranges.copySnapshot(snapshot,cut,payload.rows.map(r=>r.row)) : initial;
    if(!snapshot.current() || ranges && ranges.copyEpoch!==epoch)return;
    if(cut && !typed)throw Error('剪切需要一个连续、完整可见且未超出编辑容量的工作表区域');
    const content=window.ExcelLensClipboard.build(payload,snapshot.styles,snapshot.styled&&!plain,snapshot.fontOverride,fileFormats.richText,fileFormats.clipboard,snapshot.mergeAt);
    if (typed) {
      typed.text=content.text.replaceAll('\r\n','\n');
      content.html=content.html.replace('<table ','<table id="'+typed.marker+'"'+window.ExcelLensGridPaste.markup(typed)+' ');
    }
    if(!typed && payload.portable) content.html=content.html.replace('<table ','<table'+window.ExcelLensGridPaste.markup({clip:{portable:payload.portable}})+' ');
    if((typed||payload.portable)&&window.ExcelLensClipboard.utf8Length(content.html)>fileFormats.clipboard.htmlBytes){
      throw Object.assign(Error('复制内容超过本次容量'),{status:413});
    }
    if(payload.portableNotice)content.notices.push(payload.portableNotice);
    const copied=plain ? await copyText(content.text) : await writeClipboardPayload(content,cut);
    if (cut && !snapshot.current()) return false;
    if (copied && snapshot.current() && editCopy?.grid.active && ranges) {
      if (ranges.copyEpoch!==epoch) {
        if (cut && typed && !ranges.copied) ranges.copied={...typed,inactive:true};
        return false;
      }
      const repaintCut=typed?.cut || ranges.copied?.cut && !ranges.copied.inactive;
      ranges.copied=typed;
      if (repaintCut) editCopy.grid.view.redraw();
    }
    if(!plain&&content.notices.length)toast(content.notices.join('；'),'warning');
    return copied;
  }

  function openDialog(id) {
    const dialog = $(id);
    if (typeof dialog.showModal === "function") dialog.showModal();
    else dialog.setAttribute("open", "");
  }

  function closeDialog(id) {
    const dialog = $(id);
    if (dialog.open) dialog.close();
  }

  const searchPanel = new window.ExcelLensSearchPanel({
    state: S, api, toast, delay, copyText: (value, msg) => copyText(value, msg),
    navigateToCell: (sheet, ref) => navigateToCell(sheet, ref),
    runSearch: sheet => runSearch(sheet), cancelSearch: (id, updateUI) => cancelSearch(id, updateUI)
  });

  function searchKey(mode, scope, query, sheet) {
    const text=query.trim();
    return [mode, scope, mode==='advanced'?text:text.toLowerCase(), scope === "all" ? "*" : sheet].join("\u0000");
  }

  function buildSearchHits(matches) { searchPanel.buildHits(matches); }
  const isSearchMatch = (sheet, ref) => searchPanel.isMatch(sheet, ref);
  const updateSearchNavButtons = () => searchPanel.updateNavButtons();
  const refreshSearchHistory = () => searchPanel.refreshHistory();
  const displaySearchSummary = message => searchPanel.displaySummary(message);
  const syncSearchMode = () => searchPanel.syncMode();
  const openSearch = (mode = null, query = null) => searchPanel.open(mode, query);
  const applySearchHighlightClasses = () => searchPanel.applyHighlightClasses();
  const renderSearchResults = data => searchPanel.renderResults(data);
  const goSearchResult = index => searchPanel.goResult(index);
  const stepSearch = delta => searchPanel.step(delta);

  function copyCellEvidence() {
    const c=S.selected;
    if(!c){toast('请先选中一个单元格','warning');return;}
    const text=[
      `工作簿：${S.fileName}`,
      `工作表：${S.sheet}`,
      `单元格：${c.ref}`,
      `显示值：${c.display??''}`,
      `缓存原始值：${c.raw??''}`,
      `公式：${c.formula??'（非公式单元格）'}`,
      `标识：${c.cacheMissing?'公式缺少缓存值':'只读读取原文件保存的值（未重算）'}`
    ].join('\n');
    return copyText(text);
  }
  function copyQualifiedAddress() {
    if(!S.selected)return toast('请先选择单元格','warning');
    const sheet="'"+String(S.sheet).replaceAll("'","''")+"'";
    return copyText(`${sheet}!${S.selected.ref}`);
  }

  async function runSearch(preferredSheet = null) {
    const input = ($('searchMode').value==='advanced'?$('searchAdvancedInput'):$('searchInput')).value.trim();
    const currentSheet = typeof preferredSheet === "string" && preferredSheet ? preferredSheet : S.sheet;
    if (!input || !currentSheet) { ($('searchMode').value==='advanced'?$('searchAdvancedInput'):$('searchInput')).focus(); return; }
    const mode = $("searchMode").value;
    const scope = $("searchScope").value;
    let query = input;
    if (mode === "reference" && scope === "all" && !query.includes("!")) {
      query = `'${currentSheet.replaceAll("'", "''")}'!${query}`;
    }
    const full = $('searchFull').checked;
    const key = (full?'full:':'')+searchKey(mode,scope,query,currentSheet);
    const recoverable=$('searchRecoverable').checked,book=S.book,workbookKey=S.workbookKey;
    const requestID = ++S.searchRequestID;
    S.searchActivity?.end();
    const current=()=>requestID===S.searchRequestID&&book===S.book&&workbookKey===S.workbookKey;
    if (S.searchJob) await cancelSearch(S.searchJob, false);
    if(!current())return;
    const oldSession = S.searchSession;
    if (!full&&!recoverable&&oldSession?.key === key && oldSession.status === "done") {
      displaySearchSummary(`${oldSession.matches.length} 条匹配 · 已保留`);
      renderSearchResults({matches:oldSession.matches,status:"done",mode});
      return;
    }
    beginSearchSession({key,sheet:currentSheet,input,query,mode,scope,full});
    const cached = !full&&!recoverable&&S.searchCache.get(key);
    if (cached) {
      S.searchSession.matches = cached.matches || [];
      buildSearchHits(S.searchSession.matches);
      renderSearchResults({ ...cached, mode });
      applySearchHighlightClasses();
      S.searchSession.status = "done";
      S.searchSession.truncated = cached.truncated || false;
      displaySearchSummary(`${cached.matches.length} 条匹配 · 本机缓存${cached.truncated ? "（已截断）" : ""}`);
      $("searchBtn").disabled = false;
      $("cancelSearchBtn").hidden = true;
      return;
    }
    const activity=S.searchActivity=globalThis.ExcelLensActivity?.begin('正在搜索',{slot:'searchActivity'});
    try {
      const start = await api("/api/search/start", {
        method:"POST", headers:{"Content-Type":"application/json"},
        body:JSON.stringify({sheet:currentSheet,query,mode,scope,recoverable,full,book,workbookKey})
      });
      if (!current()){cancelSearch(start.jobId,false);return;}
      S.searchJob = start.jobId;
      await pollSearch(start.jobId, requestID, Boolean(start.cached), activity);
    } catch (error) {
      if (!current()) return;
      S.searchSession.status = "error";
      displaySearchSummary(error.message);
      $("searchBtn").disabled = false;
      $("cancelSearchBtn").hidden = true;
    } finally { activity?.end(); }
  }

  function beginSearchSession(spec){
    searchPanel.leavePages();
    S.searchSession={...spec,matches:[],status:'running',truncated:false,summary:'正在搜索…',selected:-1};
    $('searchResultFilter').value='';$('searchResultSheet').value='';S.visibleSearchIndexes=[];S.searchSelectedIndex=-1;
    buildSearchHits([]);applySearchHighlightClasses();displaySearchSummary('正在搜索…');$('searchResults').replaceChildren();$('searchResultsPane').hidden=true;
    $('searchBtn').disabled=true;$('cancelSearchBtn').hidden=false;
  }

  function currentQueryRules(){
    // Advanced predicates can contain several conditions for one column. The
    // confirmed array is authoritative; a header's one-rule map is only UI.
    return S.filtered&&S.lastRulesBySheet.has(S.sheet)?S.lastRulesBySheet.get(S.sheet):(S.autoFilter?[...S.autoFilter.rules.values()]:[]);
  }

  async function adoptRecoveredTask(start,item){
    if(item.book!==S.book||item.workbookKey!==S.workbookKey)return false;
    showWorkspace();
    if(start.operation==='export')return exportCenter.adoptRecovered(start,item);
    if(start.operation==='search'){
      if(S.searchJob)return false;
      const {query,mode,scope,sheet}=item,key=searchKey(mode,scope,query,sheet),request=++S.searchRequestID;
      $('searchMode').value=mode;$('searchScope').value=scope;
      (mode==='advanced'?$('searchAdvancedInput'):$('searchInput')).value=query;syncSearchMode();
      $('searchFull').checked=!!item.full;beginSearchSession({key:(item.full?'full:':'')+key,sheet,input:query,query,mode,scope,full:!!item.full});S.searchJob=start.jobId;
      openDialog('searchDialog');pollSearch(start.jobId,request,false).catch(error=>toast(error.message,'error'));return true;
    }
    if(start.operation==='filter'){
      const spec=start.filter;
      if(S.filterJob||!spec||!Array.isArray(spec.rules)||!Array.isArray(spec.sortKeys)||!Number.isSafeInteger(spec.headerRow))return false;
      const book=S.book,key=S.workbookKey,request=S.filterRequest,search=S.searchRequestID;
      const opening=S.sheet!==item.sheet?selectSheet(item.sheet):null,expectedEpoch=S.sheetEpoch;
      if(opening)await opening;
      if(book!==S.book||key!==S.workbookKey||expectedEpoch!==S.sheetEpoch||request!==S.filterRequest||search!==S.searchRequestID||S.sheet!==item.sheet||!S.indexReady||S.filterJob||S.searchJob)return false;
      const sheet=S.sheet,epoch=S.sheetEpoch;
      ++S.filterRequest;S.filterJob=start.jobId;S.pendingFilterSpec={sheet,rules:spec.rules,options:spec,restore:spec};
      filterRecoveryMode(true);
      $('filterProgress').hidden=false;$('applyFilterBtn').disabled=true;$('cancelFilterJobBtn').hidden=false;
      openDialog('filterDialog');
      pollFilter(start.jobId,sheet,epoch).catch(error=>{if(S.filterJob===start.jobId){S.filterJob=null;S.pendingFilterSpec=null;$('applyFilterBtn').disabled=false;$('cancelFilterJobBtn').hidden=true;$('filterProgressText').textContent=error.message;toast(error.message,'error');}});
      return true;
    }
    return false;
  }

  function installRestoredFilter(sheet,spec){
    const keys=spec.sortKeys.length?spec.sortKeys:spec.sortColumn?[{column:spec.sortColumn,descending:!!spec.descending,mode:''}]:[];
    if(keys.length){S.sortKeysBySheet.set(sheet,keys);S.sortHeaderBySheet.set(sheet,spec.headerRow);}else{S.sortKeysBySheet.delete(sheet);S.sortHeaderBySheet.delete(sheet);}
    S.lastRulesBySheet.set(sheet,spec.rules);
    if(spec.headerRow){
      S.autoFilter={...(S.autoFilter||{}),row:spec.headerRow,startCol:1,endCol:S.usedMaxCol,rules:new Map(spec.rules.map(rule=>[rule.column,rule])),sortColumn:spec.sortColumn||0,descending:!!spec.descending};
      S.autoFiltersBySheet.set(sheet,S.autoFilter);
    }else if(S.autoFilter){S.autoFilter.rules.clear();S.autoFilter.sortColumn=0;S.autoFilter.descending=false;}
    refreshAutoFilterHeaders();updateAutoFilterButton();
  }

  async function pollSearch(jobID, requestID, alreadyCached, activity) {
    return searchPanel.poll(jobID, requestID, alreadyCached, activity);
  }

  async function cancelSearch(jobID, updateUI = true) {
    if (!jobID) return;
    const owned=S.searchJob===jobID;
    if(owned){S.searchJob=null;S.searchActivity?.end();}
    if(owned&&updateUI){
      ++S.searchRequestID;
      if(S.searchSession)S.searchSession.status='cancelled';
      $('searchBtn').disabled=false;$('cancelSearchBtn').hidden=true;
      displaySearchSummary('已取消搜索 · 已找到的结果仍可访问');
    }
    try { await api("/api/search/cancel", {
      method:"POST", headers:{"Content-Type":"application/json"}, body:JSON.stringify({id:jobID})
    }); } catch { /* Already completed. */ }
  }

  function addFilterRule(column = S.selected?.col || 1, operator = "contains", value = "") {
    const row = document.createElement("div");
    row.className = "filter-rule";
    const colInput = document.createElement("input");
    colInput.className = "field-control filter-column";
    colInput.value = colName(column);
    colInput.placeholder = "列 A";
    colInput.title = "列标识，例如 A 或 27";
    const op = document.createElement("select");
    op.className = "field-control filter-operator";
    for (const [v, label] of OPERATORS) {
      const option = document.createElement("option");
      option.value = v; option.textContent = label; op.appendChild(option);
    }
    op.value = operator;
    const val = document.createElement("input");
    val.className = "field-control filter-value";
    val.value = value;
    val.placeholder = "筛选值";
    const remove = document.createElement("button");
    remove.type = "button";
    remove.className = "remove-rule";
    remove.textContent = "×";
    remove.title = "删除条件";
    remove.setAttribute("aria-label", "删除条件");
    remove.addEventListener("click", () => row.remove());
    op.addEventListener("change", () => { val.disabled = ["blank", "notBlank"].includes(op.value); val.placeholder = val.disabled ? "无需填写" : "筛选值"; });
    for (const [caption, control] of [["列", colInput], ["条件", op], ["筛选值", val]]) {
      const label = document.createElement("label");
      const text = document.createElement("span");
      text.textContent = caption;
      label.append(text, control);
      row.appendChild(label);
    }
    row.appendChild(remove);
    $("filterRules").appendChild(row);
    op.dispatchEvent(new Event("change"));
  }

  // AutoFilter is an Excel-style header-row control. Its checkbox predicates
  // share the existing read-only Go filtering engine with advanced rules.
  function updateAutoFilterButton() {
    S.largeData=S.indexReady&&Number(S.index?.totalRows||0)>fileFormats.largeDataRows;
    $('multiSortBtn').disabled=!S.indexReady;
    for(const id of ['autoFilterSortAsc','autoFilterSortDesc'])$(id).disabled=!S.indexReady;
    $('largeDataStatus').hidden=!S.largeData;
    $('largeDataStatus').title=`关闭纵向 Ctrl+方向键扫描；查重区域最多${Number(fileFormats.largeDataRows).toLocaleString('zh-CN')}行。筛选与排序共用${Number(fileFormats.queryTempBytes/1024**3).toLocaleString('zh-CN')} GiB临时空间，超出时不发布新结果。`;
    const enabled=Boolean(S.autoFilter);
    const button=$("filterOpenBtn");
    button.classList.toggle("format-active",enabled);
    button.setAttribute("aria-pressed",String(enabled));
    button.title=enabled?`关闭筛选（表头在第 ${S.autoFilter.row} 行，Ctrl+Shift+L）`:
      "以当前行为表头开启筛选（Ctrl+Shift+L）";
    button.innerHTML=icon('filter')+'筛选';
    const headerButton=$('textHeaderBtn');
    headerButton.hidden=S.source?.format!=='txt';
    headerButton.disabled=!S.indexReady;
    headerButton.classList.toggle('format-active',window.ExcelLensTableHeaders.isTable(S.autoFilter));
    headerButton.title=window.ExcelLensTableHeaders.isTable(S.autoFilter)?`表头第 ${S.autoFilter.row} 行${S.autoFilter.detected?'（自动识别）':''}，点击更改或切换普通文本视图`:'自动识别或指定 TXT 表头行';
    document.querySelector('.grid-region').classList.toggle('text-table-view',window.ExcelLensTableHeaders.isTable(S.autoFilter));
    syncTableColumnHeaders();
  }

  function syncTableColumnHeaders() {
    const config=S.autoFilter;
    const host=$('grid').parentElement;
    if(config){
      const pinned=frozenPanes?.effective().rows||0;
      host.classList.toggle('table-header-scrolled',window.ExcelLensTableHeaders.useStickyLabels(config,getVirtualScrollTop(),sourceRowTop(config.row+1)-sourceRowTop(pinned+1),pinned));
    }else if(host.classList.contains('table-header-scrolled'))host.classList.remove('table-header-scrolled');
    syncGridBackdrop();
  }

  function syncGridBackdrop() {
    const backdrop=$('gridBackdrop');
    backdrop.hidden=!readingMode?.textureEnabled||!S.indexReady;
    if(backdrop.hidden)return;
    const grid=$('grid'),hh=headerHeight();
    const pinned=frozenPanes?.effective()||{rows:0,cols:0};
    const rows=S.filtered?S.filterTotal+pinned.rows:S.usedMaxRow;
    const fixedRight = ROW_HEAD + colTotal(S.usedMaxCol);
    window.ExcelLensGridBackdrop.paint(backdrop, {
      width:grid.clientWidth, height:grid.clientHeight, rowHead:ROW_HEAD, header:hh,
      pinWidth:colTotal(pinned.cols), pinHeight:sourceRowTop(pinned.rows+1),
      right:fixedRight-grid.scrollLeft, bottom:hh+rowTotal(rows)-getVirtualScrollTop(),
      fixedRight, fixedBottom:hh+rowTotal(S.usedMaxRow)
    });
  }

  function decorateTableColumnHeader(head,col) {
    return window.ExcelLensTableHeaders.decorate(head,col,S.autoFilter,colName,S.indexReady);
  }

  function refreshAutoFilterHeaders(){
    // Changing the header row only changes visible DOM: never re-read the file.
    resetGridDOM();
    renderGridFromCurrentWindow();
    frozenPanes.schedule(true);
  }

  async function applyTextTableHeader(number,automatic=false) {
    if(!S.indexReady||S.source?.format!=='txt')return;
    const sheet=S.sheet,epoch=S.sheetEpoch,revision=++S.textHeaderRevision;
    autoFilterUI?.hide();
    if(S.filterJob)await cancelFilter(S.filterJob);
    if(epoch!==S.sheetEpoch||sheet!==S.sheet||revision!==S.textHeaderRevision)return;
    let header;
    if(automatic) {
      header=S.source.tableHeader;
      if(!header){toast('未识别到可靠表头，请填写表头所在行，或保持普通文本视图','warning');return;}
    } else {
      if(!Number.isInteger(number)||number<1||number>S.usedMaxRow){toast('表头行须在已用数据范围内','warning');return;}
      try{header=await api('/api/table/header?'+new URLSearchParams({sheet,row:String(number)}));}
      catch(error){if(epoch===S.sheetEpoch)toast('读取表头失败：'+error.message,'error');return;}
    }
    if(epoch!==S.sheetEpoch||sheet!==S.sheet||revision!==S.textHeaderRevision)return;
    if(S.filterId)await clearFilter();
    if(epoch!==S.sheetEpoch||sheet!==S.sheet||revision!==S.textHeaderRevision)return;
    S.sortKeysBySheet.delete(sheet);S.sortHeaderBySheet.delete(sheet);S.lastRulesBySheet.delete(sheet);
    S.autoFilter=window.ExcelLensTableHeaders.fromHeader(header);
    S.autoFiltersBySheet.set(sheet,S.autoFilter);
    updateAutoFilterButton();refreshAutoFilterHeaders();
    closeDialog('textHeaderDialog');
    toast(`第 ${header.row} 行已设为表头；顶部字段栏可直接筛选和排序`,'success');
  }

  async function disableTextTableHeader() {
    if(!S.indexReady)return;
    const sheet=S.sheet,epoch=S.sheetEpoch,revision=++S.textHeaderRevision;
    autoFilterUI?.hide();
    if(S.filterJob)await cancelFilter(S.filterJob);
    if(epoch!==S.sheetEpoch||sheet!==S.sheet||revision!==S.textHeaderRevision)return;
    if(S.filterId)await clearFilter();
    if(epoch!==S.sheetEpoch||sheet!==S.sheet||revision!==S.textHeaderRevision)return;
    S.autoFilter=null;S.autoFiltersBySheet.set(S.sheet,null);
    S.sortKeysBySheet.delete(S.sheet);S.sortHeaderBySheet.delete(S.sheet);S.lastRulesBySheet.delete(S.sheet);
    updateAutoFilterButton();refreshAutoFilterHeaders();closeDialog('textHeaderDialog');
  }

  async function toggleAutoFilter(){
    if(!S.indexReady)return;
    autoFilterUI?.hide();
    if(S.autoFilter&&S.source?.format==='txt') {
      await disableTextTableHeader();toast('已切换为普通文本视图');return;
    }
    if(S.autoFilter){
      ++S.textHeaderRevision;
      S.autoFilter=null;
      S.autoFiltersBySheet.set(S.sheet,null);
      S.sortKeysBySheet.delete(S.sheet);S.sortHeaderBySheet.delete(S.sheet);
      if(S.filterId)await clearFilter();
      refreshAutoFilterHeaders();
      toast("已关闭自动筛选");
    }else{
      const row=Math.max(1,Math.min(S.usedMaxRow,S.selected?.row||1));
      if(S.source?.format==='txt'){await applyTextTableHeader(row);return;}
      if(S.filterId)await clearFilter();
      S.autoFilter={row,startCol:1,endCol:Math.max(1,S.usedMaxCol),rules:new Map(),sortColumn:0,descending:false};
      S.autoFiltersBySheet.set(S.sheet,S.autoFilter);
      refreshAutoFilterHeaders();
      toast(`第 ${row} 行已设为表头；点击表头单元格右侧的 ▾ 筛选`,'success');
    }
    updateAutoFilterButton();
  }

  async function applyAutoFilter(options={}){
    if(!S.autoFilter)return;
    const c=S.autoFilter,sheet=S.sheet,epoch=S.sheetEpoch;
    // Keep dropdown arrows even when no predicate is active.
    const multi=S.sortKeysBySheet.get(S.sheet)||[];
    const rules=options.preserveRules?currentQueryRules():[...c.rules.values()];
    if(rules.length===0 && !c.sortColumn && !multi.length){
      if(S.filterId)await clearFilter();
      if(S.sheet===sheet&&S.sheetEpoch===epoch&&S.autoFilter===c)refreshAutoFilterHeaders();
      return;
    }
    try{
      await submitFilterRules(rules,{headerRow:c.row,sortColumn:c.sortColumn,descending:c.descending,sortKeys:multi,recoverable:!!options.recoverable});
      if(S.sheet===sheet&&S.sheetEpoch===epoch&&S.autoFilter===c)refreshAutoFilterHeaders();
    }catch(err){toast(`筛选失败：${err.message}`,'error');}
  }

  async function filterSelectedValue(){
    if(!S.indexReady||!S.selected)return;
    const selected={...S.selected};
    const value=String(selected.display??selected.raw??'');
    if(value.length>1024){toast('当前单元格超过1024字符，请使用「条件」设置筛选','warning');return;}
    if(!S.autoFilter){
      S.autoFilter={row:1,startCol:1,endCol:Math.max(1,S.usedMaxCol),rules:new Map(),sortColumn:0,descending:false};
      S.autoFiltersBySheet.set(S.sheet,S.autoFilter);
    }
    const config=S.autoFilter;
    if(selected.row<=config.row){toast(`请选择第 ${config.row} 行表头下面的数据单元格`,'warning');return;}
    // Match the displayed value exactly as the native filter checkbox does:
    // 'equals' normalizes numeric strings and trims user input, which can
    // unexpectedly include/exclude formatted IDs or whitespace-sensitive text.
    config.rules.set(selected.col,value===''?
      {column:selected.col,operator:'blank',value:''}:
      {column:selected.col,operator:'in',values:[value]});
    updateAutoFilterButton();
    refreshAutoFilterHeaders();
    await applyAutoFilter();
  }

  const autoFitCanvas=document.createElement('canvas');
  const autoFitCtx=autoFitCanvas.getContext('2d');
  function measureAutoFit(text,visual){
    const size=S.showStyles&&visual?.fontSize>0?Math.min(80,visual.fontSize*4/3):11;
    if(autoFitCtx){
      const name=S.fontOverride||(S.showStyles?visual?.fontName:'')||'Segoe UI';
      autoFitCtx.font=`${visual?.italic?'italic ':''}${visual?.bold?'bold ':''}${size}px ${fontStack(name)}`;
    }
    const lines=String(text??'').slice(0,2048).split(/\r?\n/);
    return {size,width:Math.max(0,...lines.map(line=>autoFitCtx?autoFitCtx.measureText(line).width:line.length*size*.8)),lines};
  }
  async function autoFitSize(kind,index){
    if(!S.indexReady)return;
    if(kind==='row'&&S.filtered){toast('筛选后请先清除筛选，再自动调整行高','warning');return;}
    const sheet=S.sheet,epoch=S.sheetEpoch;
    const params=new URLSearchParams({sheet,kind,index:String(index)});
    if(kind==='row'){
      const first=S.window?.startCol||1;
      params.set('firstCol',String(first));
      params.set('lastCol',String(Math.min(16384,first+79)));
    }
    try{
      const data=await api(`/api/autofit?${params}`);
      if(epoch!==S.sheetEpoch||sheet!==S.sheet)return;
      let result=kind==='col'?65:BASE_ROW;
      if(kind==='col'){
        for(const row of data.rows||[]){for(const cell of row.cells||[]){
          const text=cell.display??cell.raw??'';
          if(!text)continue;
          const width=measureAutoFit(text,S.styles[Number(cell.style)||0]).width;
          result=Math.max(result,Math.min(800,Math.ceil(width+22)));
        }}
        result=Math.max(32,Math.min(800,result));
      }else{
        for(const row of data.rows||[]){for(const cell of row.cells||[]){
          const visual=S.showStyles?S.styles[Number(cell.style)||0]:null;
          const text=cell.display??cell.raw??'';
          if(!text)continue;
          const measured=measureAutoFit(text,visual);
          const canWrap=Boolean(visual?.wrap);
          let lines=measured.lines.length;
          if(canWrap){
            const width=Math.max(24,geometry.cols.measure(cell.col)-16);
            lines=measured.lines.reduce((n,line)=>n+Math.max(1,Math.ceil((autoFitCtx?autoFitCtx.measureText(line).width:line.length*measured.size*.8)/width)),0);
          }
          result=Math.max(result,Math.ceil(lines*measured.size*1.35+8));
        }}
        result=Math.max(14,Math.min(500,result));
      }
      await commitResize(kind,index,result);
      if(epoch===S.sheetEpoch)toast(`${kind==='col'?colName(index)+' 列':'第 '+index+' 行'}已根据${kind==='col'?'抽样内容':'当前查看列'}自动调整`,'success');
    }catch(err){if(epoch===S.sheetEpoch)toast('自动调整失败：'+err.message,'error');}
  }

  function selectWholeRow(row,visualIndex){
    if(!Number.isSafeInteger(row)||row<1||!S.indexReady)return;
    const ref=`A${row}`;
    selectCell(cellAt(row,1),row,1,ref,visualIndex||row);
    S.wholeRowSelected=true;
    $("selectionStatus").textContent=`第 ${row} 行（整行）`;
    S.range={row1:row,row2:row,col1:1,col2:S.usedMaxCol};
    applySelectionClasses();
    selectionStats?.schedule();
    if(!calculator?.captureRange(`${row}:${row}`))$("grid").focus({preventScroll:true});
  }

  function openFilter(column = S.selected?.col || 1) {
    if (!S.sheet) return;
    filterRecoveryMode(false);
    $("filterRules").replaceChildren();
    addFilterRule(column);
    $("filterProgress").hidden = true;
    $("applyFilterBtn").disabled = false;
    $("cancelFilterJobBtn").hidden = true;
    openDialog("filterDialog");
  }

  function filterRecoveryMode(recovered){
    // Recovered whitelist/multi-sort definitions are not editable as the
    // advanced dialog's simpler single-value rules. Show their progress only.
    for(const id of ['filterRules','addFilterRuleBtn','applyFilterBtn','filterHelp','filterProgressChoice'])$(id).hidden=recovered;
  }

  function collectFilterRules() {
    const rows = [...$("filterRules").querySelectorAll(".filter-rule")];
    if (!rows.length) throw new Error("请至少添加一个筛选条件");
    if (rows.length > 32) throw new Error("最多支持 32 个筛选条件");
    return rows.map((row, index) => {
      const column = parseColumn(row.querySelector(".filter-column").value);
      const operator = row.querySelector(".filter-operator").value;
      const value = row.querySelector(".filter-value").value;
      if (!column) throw new Error(`第 ${index + 1} 个条件的列标识无效`);
      if (!["blank", "notBlank"].includes(operator) && !value.trim()) throw new Error(`第 ${index + 1} 个条件需要填写筛选值`);
      return { column, operator, value };
    });
  }

  async function applyFilter() {
    let rules;
    try { rules = collectFilterRules(); }
    catch (error) { toast(error.message,'error'); return; }
    await submitFilterRules(rules,{headerRow:S.autoFilter?.row||0,recoverable:$('filterRecoverable').checked});
  }

  async function submitFilterRules(rules,options={}) {
    const requestedSheet=S.sheet,requestedEpoch=S.sheetEpoch,requestedBook=S.workbookKey;
    if (S.filterJob) await cancelFilter(S.filterJob);
    if(S.sheet!==requestedSheet||S.sheetEpoch!==requestedEpoch||S.workbookKey!==requestedBook)return false;
    const ticket=S.filterRequest=(S.filterRequest||0)+1,sheet=S.sheet,epoch=S.sheetEpoch,book=S.workbookKey;
    const current=()=>ticket===S.filterRequest&&sheet===S.sheet&&epoch===S.sheetEpoch&&book===S.workbookKey;
    S.filterJob='pending:'+ticket;
    $("applyFilterBtn").disabled = true;
    $("filterProgress").hidden = false;
    $("filterBar").style.width = "0%";
    $("filterProgressText").textContent = "正在扫描当前工作表…";
    setStatus(`正在筛选 · ${S.sheet}`);
    S.filterActivity?.end();
    const activity=S.filterActivity=globalThis.ExcelLensActivity?.begin('正在筛选数据',{slot:'filterActivity'});
    try {
      const start = await api("/api/filter/start", {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ sheet, rules,...options,workbookKey:book,book:S.book })
      });
      if(!current()){if(S.filterJob==='pending:'+ticket)S.filterJob=null;api('/api/filter/cancel',{method:'POST',body:JSON.stringify({id:start.jobId})}).catch(()=>{});return false;}
      S.filterJob = start.jobId;
      S.pendingFilterSpec={sheet,rules,options};
      $("cancelFilterJobBtn").hidden = false;
      return await pollFilter(start.jobId,sheet,epoch,activity);
    } catch (error) {
      if(!current())return false;
      S.filterJob=null;
      $("filterProgressText").textContent = error.message;
      $("applyFilterBtn").disabled = false;
      $("cancelFilterJobBtn").hidden = true;
      setStatus(`已打开 · ${sheet}`);
      toast(error.message,'error');
      return false;
    } finally { activity?.end(); }
  }

  async function commitRowView(data,guard=()=>true){
    if(!guard())return false;
    recordCardCenter?.resetBook();selectionQuick?.resetBook();transposeCenter?.resetBook();cellExplanation?.resetBook();profileCenter?.resetBook();
    const sheet=S.sheet,epoch=S.sheetEpoch,anchor=S.selected?.ref;
    const current=()=>guard()&&sheet===S.sheet&&epoch===S.sheetEpoch&&S.rowView?.id===data.id;
    const topSource=S.filtered?0:rowAtOffset(getVirtualScrollTop()+sourceRowTop((frozenPanes?.effective().rows||0)+1));
    rowViews.install(data);geometry.rows.setHidden(S.rowVisibility.hidden);
    if(S.filtered)S.filterTotal=filteredBodyCount();
    S.sheetViews.clear();S.sheetViewsBytes=0;invalidateViewport();frozenPanes.reset();selectionStats?.clear();
    S.selected=null;S.range=null;S.multiRanges=[];S.rangeAnchor=null;S.wholeRowSelected=false;
    updateCanvasSize();
    if(topSource){const next=nextVisibleRow(topSource)||previousVisibleRow(topSource);setVirtualScrollTop(Math.max(0,sourceRowTop(next||1)-sourceRowTop(frozenPanes.effective().rows+1)));}
    await loadViewport(true);if(!current())return false;
    if(anchor){const parsed=parseRef(anchor);if(parsed&&rowVisible(parsed.row)){await navigateToCell(sheet,anchor,false,current);return current();}}
    selectInitialCell();frozenPanes.schedule(true);return true;
  }

  async function adoptFilterResult(data,sheet=S.sheet,preparedRows=null,{guard=()=>true,signal,onCommit}={}) {
    const epoch=S.sheetEpoch,book=S.workbookKey,request=S.filterRequest;
    let rowTicket;
    const current=()=>sheet===S.sheet&&epoch===S.sheetEpoch&&book===S.workbookKey&&request===S.filterRequest&&(rowTicket===undefined||rowTicket===rowViews?.serial)&&!signal?.aborted&&guard();
    try{
    if(!current())return false;
    if(rowViews&&!preparedRows){const pending=rowViews.prepareFilter(data.filterId,{guard:current,signal});rowTicket=rowViews.serial;preparedRows=await pending;}
    if(!current())return false;
    if(preparedRows)rowViews.validate(preparedRows,sheet,data.filterId);
    onCommit?.();
    recordCardCenter?.resetBook();selectionQuick?.resetBook();transposeCenter?.resetBook();cellExplanation?.resetBook();profileCenter?.resetBook();
    if(S.filterId&&S.filterId!==data.filterId){
      api('/api/filter/clear',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({id:S.filterId})}).catch(()=>{});
    }
    S.filterId=data.filterId;S.filtersBySheet.set(sheet,S.filterId);S.filtered=true;
    S.filterTotal=Number(data.matchedRows||0);S.filterTotalsBySheet.set(sheet,S.filterTotal);
    if(preparedRows){rowViews.install(preparedRows);geometry.rows.setHidden(S.rowVisibility.hidden);S.filterTotal=filteredBodyCount();}
    invalidateViewport();S.selected=null;S.traceRefs=[];$('grid').scrollTop=0;$('grid').scrollLeft=0;
    updateCanvasSize();await loadViewport(true);
    if(!current()||S.filterId!==data.filterId||preparedRows&&S.rowView?.id!==preparedRows.id)return false;
    selectInitialCell();frozenPanes.schedule(true);
    $('filterStatus').hidden=false;$('clearFilterBtn').disabled=false;
    return true;
    }finally{if(preparedRows&&S.rowView?.id!==preparedRows.id)rowViews.cleanup(preparedRows.id);}
  }

  function captureNamedView() {
    if(!S.opened||!S.indexReady||S.opening||S.sheetLoadingEpoch||S.filterJob)throw Error('请先完成工作表加载或筛选，再保存视图');
    const table=S.autoFilter;
    return {sheet:S.sheet,filterId:S.filtered?S.filterId:'',...(S.rowView?{viewId:S.rowView.id}:{}),headerRow:S.sortHeaderBySheet.get(S.sheet)??table?.row??0,
      config:{freeze:{...S.freeze},layout:{rows:{...S.rowSizes},cols:{...S.columnWidths},font:S.fontOverride,columnMode:S.columnMode,columnVisibility:{...S.columnOverrides}},
        table:table?{row:table.row,startCol:table.startCol,endCol:table.endCol,sticky:!!table.table}:null,zoom:S.zoom,showStyles:S.showStyles,showFormulas:S.showFormulas}};
  }

  async function applyNamedView(prepared,data,current) {
    if(!current())return false;
    if(S.filterJob)throw Error('当前有筛选任务，未替换视图');
    const view=prepared.view,sheet=view.source.sheet,config=view.config,query=view.query;
    if(data&&(!data.filterId||data.sheet!==sheet))throw Error('视图查询来源不一致');
    let preparedRows=null;
    if(rowViews){preparedRows=await rowViews.build({sheet,filterId:data?.filterId||'',settings:config.rowVisibility||{mode:'original'},guard:current});if(!current()){rowViews.cleanup(preparedRows.id);return false;}}
    try{
    let table=null;
    if(config.table){table=config.table.sticky?window.ExcelLensTableHeaders.fromHeader(prepared.tableHeader):{...config.table,rules:new Map()};if(!table)throw Error('视图表头已失效');table.rules=new Map(query.rules.map(rule=>[rule.column,{...rule}]));table.sortColumn=query.sortColumn||0;table.descending=!!query.descending;}
    const opening=selectSheet(sheet),epoch=S.sheetEpoch;await opening;
    if(!current()||S.sheet!==sheet||S.sheetEpoch!==epoch||!S.indexReady)return false;
    S.filterRequest=(S.filterRequest||0)+1;
    S.sheetViews.clear();S.sheetViewsBytes=0;
    S.rowSizes={...config.layout.rows};S.columnWidths={...config.layout.cols};S.columnMode=config.layout.columnMode||'original';S.columnOverrides={...config.layout.columnVisibility};
    setDisplayFontSelection(config.layout.font);S.showStyles=config.showStyles;S.showFormulas=config.showFormulas;S.zoom=config.zoom;S.freeze={...config.freeze};
    S.sheetLayout={...S.sheetLayout,rows:S.rowSizes,cols:S.columnWidths,font:S.fontOverride,columnMode:S.columnMode,columnVisibility:S.columnOverrides};
    S.autoFilter=table;S.autoFiltersBySheet.set(sheet,table);
    const keys=query.sortKeys?.length?query.sortKeys:query.sortColumn?[{column:query.sortColumn,descending:!!query.descending,mode:''}]:[];
    if(keys.length){S.sortKeysBySheet.set(sheet,keys);S.sortHeaderBySheet.set(sheet,query.headerRow);}else{S.sortKeysBySheet.delete(sheet);S.sortHeaderBySheet.delete(sheet);}
    S.lastRulesBySheet.set(sheet,query.rules||[]);S.pendingFilterSpec=null;
    $('zoomValue').textContent=S.zoom+'%';$('formatBtn').classList.toggle('format-active',S.showStyles);$('formatBtn').setAttribute('aria-pressed',String(S.showStyles));$('formulaViewBtn').setAttribute('aria-pressed',String(S.showFormulas));
    applyColumnVisibility();updateSourceAppearance();updateFreezeUI();updateAutoFilterButton();frozenPanes.reset();
    document.documentElement.style.setProperty('--row-height',`${rowHeight()}px`);document.documentElement.style.setProperty('--col-width',`${colWidth()}px`);document.documentElement.style.setProperty('--header-height',`${headerHeight()}px`);
    S.range=null;S.multiRanges=[];S.rangeAnchor=null;S.traceRefs=[];
    if(data){if(!await adoptFilterResult(data,sheet,preparedRows,{guard:current}))return false;}
    else{
      const old=S.filterId;if(old)api('/api/filter/clear',{method:'POST',body:JSON.stringify({id:old})}).catch(()=>{});
      S.filtersBySheet.delete(sheet);S.filterTotalsBySheet.delete(sheet);S.filterId='';S.filtered=false;S.filterTotal=0;S.selected=null;
      if(preparedRows){rowViews.install(preparedRows);geometry.rows.setHidden(S.rowVisibility.hidden);}
      invalidateViewport();$('grid').scrollTop=0;$('grid').scrollLeft=0;updateCanvasSize();await loadViewport(true);
      if(!current()||S.sheet!==sheet||S.sheetEpoch!==epoch||preparedRows&&S.rowView?.id!==preparedRows.id)return false;
      selectInitialCell();frozenPanes.schedule(true);$('filterStatus').hidden=true;$('clearFilterBtn').disabled=true;
    }
    pictures?.schedule();return true;
    }finally{if(preparedRows&&S.rowView?.id!==preparedRows.id)rowViews.cleanup(preparedRows.id);}
  }

  async function pollFilter(jobID,sheet=S.sheet,epoch=S.sheetEpoch,activity=globalThis.ExcelLensActivity?.begin('正在筛选数据',{slot:'filterActivity'})) {
    S.filterActivity=activity;
    try {
      while (S.filterJob === jobID) {
        await delay(350);
        const data = await api(`/api/filter/status?id=${encodeURIComponent(jobID)}`);
        if(S.filterJob!==jobID)return;
        if(S.sheet!==sheet||S.sheetEpoch!==epoch){S.filterJob=null;return;}
        const percent = Number(data.progress || 0);
        activity?.job(data);
        $("filterBar").style.width = `${percent}%`;
        // A finished scan is not a finished filter: the rows are not on screen
        // yet, and saving a view or starting another query is still refused.
        $("filterProgressText").textContent = data.status === "done" ? "正在显示筛选结果…" :
          `${data.stage || "正在筛选"} · ${data.scanned || 0}/${data.total || 0} 行`;
        if (data.status === "done") {
          activity?.stage('正在显示筛选结果',95,99);
          const abort=new AbortController();S.filterCommitAbort=abort;
          let accepted=false;
          try{accepted=await adoptFilterResult(data,sheet,null,{guard:()=>S.filterJob===jobID,signal:abort.signal,onCommit:()=>{if(S.pendingFilterSpec?.restore)installRestoredFilter(sheet,S.pendingFilterSpec.restore);}});}
          finally{if(S.filterCommitAbort===abort)S.filterCommitAbort=null;}
          if(S.filterJob!==jobID||S.sheet!==sheet||S.sheetEpoch!==epoch)return false;
          if(!accepted){S.filterJob=null;S.pendingFilterSpec=null;$('applyFilterBtn').disabled=false;$('cancelFilterJobBtn').hidden=true;$('filterProgressText').textContent='视图已变化；筛选任务已结束';setStatus(`已打开 · ${sheet}`);return false;}
          if(S.pendingFilterSpec?.sheet===sheet){
            S.lastRulesBySheet.set(sheet,S.pendingFilterSpec.rules);
            S.pendingFilterSpec=null;
          }
          $("filterProgressText").textContent = `筛选完成 · ${S.filterTotal.toLocaleString()} 行符合条件`;
          $("filterStatus").hidden = false;
          $("clearFilterBtn").disabled = false;
          $("applyFilterBtn").disabled = false;
          $("cancelFilterJobBtn").hidden = true;
          S.filterJob = null;
          setStatus(`已打开 · ${sheet}`);
          toast(`筛选完成 · ${Math.max(0,S.filterTotal-(S.autoFilter?.row||0)).toLocaleString()} 行`,'success');
          return true;
        }
        if (data.status === "error" || data.status === "cancelled") {
          $("filterProgressText").textContent = data.error || "筛选已取消";
          $("applyFilterBtn").disabled = false;
          $("cancelFilterJobBtn").hidden = true;
          S.filterJob = null;
          setStatus(`已打开 · ${sheet}`);
          if(data.status==='error')toast(data.error||'筛选失败','error');
          return false;
        }
      }
    } finally { activity?.end(); }
  }

  async function cancelFilter(jobID) {
    if (!jobID) return;
    if(S.filterJob===jobID)S.filterActivity?.end();
    if (S.filterJob === jobID) {
      S.filterRequest=(S.filterRequest||0)+1;
      S.filterCommitAbort?.abort();S.filterCommitAbort=null;
      S.filterJob = null;
      $('applyFilterBtn').disabled=false;
      $('cancelFilterJobBtn').hidden=true;
      $('filterProgressText').textContent='筛选已取消';
      setStatus(`已打开 · ${S.sheet}`);
    }
    try { if(!jobID.startsWith('pending:'))await api("/api/filter/cancel", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ id: jobID }) }); }
    catch { /* ignore */ }
  }

  async function clearFilter() {
    if (!S.filterId) return;
    const oldID = S.filterId,sheet=S.sheet,epoch=S.sheetEpoch,book=S.workbookKey;
    if(S.filterJob)await cancelFilter(S.filterJob);
    if(sheet!==S.sheet||epoch!==S.sheetEpoch||book!==S.workbookKey||S.filterId!==oldID)return;
    const ticket=S.filterRequest=(S.filterRequest||0)+1;
    let rowTicket;
    const current=()=>sheet===S.sheet&&epoch===S.sheetEpoch&&book===S.workbookKey&&ticket===S.filterRequest&&(rowTicket===undefined||rowTicket===rowViews?.serial);
    let preparedRows=null;
    try{
    if(rowViews){try{const pending=rowViews.prepareFilter('',{guard:current});rowTicket=rowViews.serial;preparedRows=await pending;}catch(error){if(current()&&error.name!=='AbortError')toast('清除筛选失败：'+error.message,'error');return;}}
    if(!current())return;
    try { await api("/api/filter/clear", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ id: oldID }) }); }
    catch { /* expired filter results are also cleared locally */ }
    if(!current())return;
    S.filtersBySheet.delete(S.sheet);
    S.filterTotalsBySheet.delete(S.sheet);
    S.filterId = "";
    S.filtered = false;
    S.filterTotal = 0;
    if(preparedRows){rowViews.install(preparedRows);geometry.rows.setHidden(S.rowVisibility.hidden);}
    invalidateViewport();
    S.selected = null;
    S.traceRefs = [];
    $("grid").scrollTop = 0;
    $("grid").scrollLeft = 0;
    updateCanvasSize();
    await loadViewport(true);
    if(!current()||S.filterId||preparedRows&&S.rowView?.id!==preparedRows.id)return;
    selectInitialCell();
    frozenPanes.schedule(true);
    $("filterStatus").hidden = true;
    $("clearFilterBtn").disabled = true;
    if(S.autoFilter){
      S.autoFilter.rules.clear();
      S.autoFilter.sortColumn=0;
      S.autoFilter.descending=false;
      refreshAutoFilterHeaders();
    }
    }finally{if(preparedRows&&S.rowView?.id!==preparedRows.id)rowViews.cleanup(preparedRows.id);}
  }

  function parseFormulaReferences(formula,currentSheet) {
    const refs=window.ExcelLensFormulaInspector.parseFormula(formula,currentSheet,256);
    const unique=new Set();
    return refs.filter(r=>{
      if(r.external||unique.has(r.key))return false;
      unique.add(r.key);return true;
    }).slice(0,100).map(ref=>({
      ...ref,start:{...ref.start,ref:`${colName(ref.start.col)}${ref.start.row}`},
      end:{...ref.end,ref:`${colName(ref.end.col)}${ref.end.row}`}
    }));
  }

  async function tracePrecedents() { await traceUI.open('precedents'); }

  function traceVisibleRect(node, forEditor=false){
    const grid=$('grid');if(!S.indexReady||grid.inert)return null;
    const row=node.closest('.grid-row');if(row&&row._xlSheetEpoch!==S.sheetEpoch)return null;
    const rect=node.getBoundingClientRect();if(!rect.width||!rect.height)return null;
    const g=grid.getBoundingClientRect(),p=frozenPanes.effective(),fw=colLeft(p.cols+1),fh=sourceRowTop(p.rows+1),hh=headerHeight();
    let left=g.left+ROW_HEAD+fw,top=g.top+hh+fh,right=g.left+grid.clientWidth,bottom=g.top+grid.clientHeight,pane='body';
    if(node.closest('#freezeCorner')){pane='corner';left=g.left+ROW_HEAD;top=g.top+hh;right=left+fw;bottom=top+fh;}
    else if(node.closest('#freezeTop')){pane='top';top=g.top+hh;bottom=top+fh;}
    else if(node.closest('#freezeLeft')){pane='left';left=g.left+ROW_HEAD;right=left+fw;}
    const visible={left:Math.max(left,rect.left),top:Math.max(top,rect.top),right:Math.min(right,rect.right),bottom:Math.min(bottom,rect.bottom),pane};
    if (visible.right<=visible.left || visible.bottom<=visible.top) return null;
    if (forEditor===true) visible.viewport={left,top,right:g.left+grid.clientWidth,bottom:g.top+grid.clientHeight};
    return visible;
  }

  function renderGridFromCurrentWindow() {
    // Always re-render from the complete cached payload. Reconstructing a
    // viewport from the DOM loses off-screen values and can cause blank bands.
    const entry=S.viewportData;if(!entry)return;
    const current=getViewportRequest();
    if(!sameViewportRows(entry.bounds,current)){
      const prior=entry.request;
      if(prior.book!==S.book||prior.workbookKey!==S.workbookKey||prior.sheet!==S.sheet||
        !sameViewportRows({...entry.bounds,context:current.context},current))return;
      // Reproject the immutable source cells immediately on an edit revision change.
      scheduler.reset();++S.viewportRequest;tiles.clear();
      const request={...prior,context:current.context},bounds={...entry.bounds,context:current.context};
      S.viewportData={...entry,request,bounds};S.window=bounds;
      tiles.store(viewportKey(request),entry.data,request);
    }
    renderGrid(S.viewportData.data,S.viewportData.request);
  }

  async function traceDependents() {
    if (!S.selected) return;
    await traceUI.open('dependents');
  }

  function showCellDetail() {
    return cellExplanation?.open();
  }

  async function copyText(value, successMessage='已复制') {
    const text = String(value ?? "");
    try {
      if (navigator.clipboard?.writeText) await navigator.clipboard.writeText(text);
      else {
        const helper = document.createElement("textarea");
        helper.value = text;
        helper.style.position = "fixed";
        helper.style.opacity = "0";
        document.body.appendChild(helper);
        helper.select();
        try {
          if(!document.execCommand("copy"))throw new Error('浏览器未能写入剪贴板');
        } finally { helper.remove(); }
      }
      toast(successMessage,'success');
      return true;
    } catch (error) { toast(`复制失败：${error.message}`,'error'); return false; }
  }

  let hoverRef = "";
  function showTooltip(event) {
    if (editCopy?.grid.active) return;
    const cellEl=event.target.closest?.(".grid-cell");
    const tooltip=$("cellTooltip");
    if (dragAnchor || (S.range || S.multiRanges.length) && cellEl?.classList.contains('range-cell')) {
      tooltip.hidden=true;hoverRef="";return;
    }
    if(!cellEl) {tooltip.hidden=true;hoverRef="";return;}
    const ref=cellEl.dataset.ref;
    if (ref!==hoverRef || tooltip.hidden) {
      hoverRef=ref;
      const cell=S.cellMap.get(ref)||S.frozenCellMap.get(ref)||{ref,display:"",raw:""};
      const value=cell.display??"";
      const head=document.createElement("b");head.textContent=ref+"  ";
      const text=document.createElement("span");text.textContent=value===""?"（空白）":value;
      const content=[head,text];
      if (cell.hyperlink) {
        const hint=document.createElement('span');hint.className='tooltip-formula tooltip-hyperlink';
        hint.textContent='\u8df3\u8f6c\u81f3\uff1a'+linkDescription(cell.hyperlink);
        content.push(hint);
      }
      if(cell.raw!==undefined&&cell.raw!==""&&String(cell.raw)!==String(value)) {
        const raw=document.createElement("span");raw.className="tooltip-formula";raw.textContent="原始值："+cell.raw;
        content.push(raw);
      }
      if(cell.hasFormula&&cell.formula) {
        const formula=document.createElement("span");formula.className="tooltip-formula";formula.textContent=cell.formula;
        content.push(formula);
      }
      tooltip.replaceChildren(...content);
      tooltip.hidden=false;
    }
    // Position without forced layout; browser clamps the fixed-width tooltip.
    tooltip.style.left=Math.max(8,Math.min(window.innerWidth-440,event.clientX+14))+"px";
    tooltip.style.top=Math.max(8,Math.min(window.innerHeight-160,event.clientY+16))+"px";
  }

  function setZoom(next) {
    const value = Math.max(70, Math.min(125, Math.round(next / 5) * 5));
    if (value === S.zoom) return;
    const grid = $("grid");
    const row = S.selected?.row || 1;
    const col = S.selected?.col || 1;
    S.zoom = value;
    pictures?.schedule();
    $("zoomValue").textContent = `${value}%`;
    document.documentElement.style.setProperty("--row-height", `${rowHeight()}px`);
    document.documentElement.style.setProperty("--col-width", `${colWidth()}px`);
    document.documentElement.style.setProperty("--header-height", `${headerHeight()}px`);
	if(S.previewOnly&&S.previewData){resetGridDOM();updateCanvasSize();renderInitialPreview();paintRangeFrames();return;}
    invalidateViewport({preserveDOM:true});
    updateCanvasSize();
    frozenPanes.reset({preserveDOM:true});
    frozenPanes.schedule(true);
    if (S.selected) {
      setVirtualScrollTop(headerHeight()+rowTop(row)-rowHeight(row)*2);
      grid.scrollLeft = ROW_HEAD+colLeft(col)-colWidth(col)*2;
    }
    scheduleViewport(true);
    // A sheet that fits the window raises no scroll event when it is zoomed.
    paintRangeFrames();
  }

  async function toggleFullscreen() {
    try {
      if (!document.fullscreenElement) await document.documentElement.requestFullscreen();
      else await document.exitFullscreen();
    } catch (error) { toast(`无法切换全屏：${error.message}`,'error'); }
  }

  function updateFullscreenButton() {
    const full = Boolean(document.fullscreenElement);
    $("fullscreenBtn").innerHTML = icon('fullscreen') + (full ? '退出全屏' : '全屏');
    setTimeout(() => scheduleViewport(true), 80);
  }

  let sizeContextTarget=null;
  function hideCellMenu() { $("cellMenu").hidden = true; $("sizeContextMenu").hidden = true; $("editGridMenu").hidden = true; }

  function positionCellMenu(menu,x,y) {
    hideCellMenu();
    menu.hidden=false;
    menu.style.left=Math.max(5,Math.min(x,window.innerWidth-menu.offsetWidth-8))+'px';
    menu.style.top=Math.max(5,Math.min(y,window.innerHeight-menu.offsetHeight-8))+'px';
  }

  function openCellMenu(event) {
    if(!S.indexReady){hideCellMenu();return;}
    const head=event.target.closest?.('.row-head,.pinned-row-head,.column-head[data-col]');
    if(head){
      const kind=head.dataset.col?'col':'row';
      const index=Number(head.dataset.col||head.dataset.row||head.closest('.grid-row')?.dataset.row);
      if(!Number.isInteger(index)||index<1){hideCellMenu();return;}
      event.preventDefault();
      hideCellMenu();
      if(kind==='col'){
        const previous=selectedColumnSpan();
        if(!previous||index<previous.start||index>previous.end)selectColumnSpan(index,index);
      }
      const span=kind==='col'?selectedColumnSpan():null;
      sizeContextTarget={kind,index:span?.start||index,end:span?.end||index};
      const menu=$('sizeContextMenu');
      const multi=span&&span.start!==span.end;
      $('sizeContextAdjust').textContent=kind==='row'?'设置第 '+index+' 行高度…':'设置 '+colName(index)+' 列宽度…';
      for(const id of ['sizeContextAdjust','sizeContextFit','sizeContextReset'])$(id).hidden=Boolean(multi);
      $('sizeContextAdjust').disabled=kind==='row'&&S.filtered;
      $('sizeContextReset').disabled=kind==='row'&&S.filtered;
      $('sizeContextHideCol').textContent=multi?'隐藏选中列':'隐藏此列';
      $('sizeContextShowCol').textContent='显示隐藏列';
      $('sizeContextHideCol').hidden=kind!=='col'||!hasColumnOfType(span,false);
      $('sizeContextShowCol').hidden=kind!=='col'||!hasColumnOfType(span,true);
      positionCellMenu(menu,event.clientX,event.clientY);
      return;
    }
    const cell = event.target.closest?.(".grid-cell,.pinned-cell");
    if (!cell) {hideCellMenu();return;}
    event.preventDefault();
    const target = parseRef(cell.dataset.ref);
    if (!target) return;
    // Excel preserves an existing range when right-clicking inside it.
    if (!S.range || !inSelectedRange(target.row,target.col,Number(cell.dataset.visualIndex))) {
      selectPointedCell(cellAt(target.row,target.col),{...target,visual:Number(cell.dataset.visualIndex)},event);
    }
    if (editCopy?.grid.active) {
      editCopy.grid.ranges.showMenu(event.clientX,event.clientY); return;
    }
    $("menuCopyFormula").disabled = !S.selected?.hasFormula;
    $('menuFilterValue').disabled=!S.selected||S.selected.row<=(S.autoFilter?.row||1);
    $("menuTrace").disabled = !S.selected?.hasFormula;
    $('menuOpenHyperlink').hidden = !S.selected?.hyperlink;
    if (S.selected?.hyperlink) {
      $('menuOpenHyperlink').textContent = S.selected.hyperlink.kind === 'internal' ?
        '\u8df3\u8f6c\u5230\u94fe\u63a5\u4f4d\u7f6e' : '\u6253\u5f00\u8d85\u94fe\u63a5';
    }
    positionCellMenu($("cellMenu"),event.clientX,event.clientY);
  }

  function selectPointedCell(cell,point,event){
    selectCell(cell,point.row,point.col,point.ref,point.visual,false,false,mergedCells.pointerCell(event,colLeft,rowTop));
  }

  // Ctrl-click adds an independently selected cell; Ctrl-drag adds a rectangle.
  // Shift extends the active rectangle. Source cells remain immutable.
  function selectWithModifiers(dataCell,point,event){
    const selection=window.ExcelLensMultiSelection;
    const additive=Boolean(event.ctrlKey||event.metaKey),extend=Boolean(event.shiftKey);
    const oldAnchor=extend?(S.rangeAnchor||{
      row:S.selected?.row||point.row,col:S.selected?.col||point.col,
      visual:S.selectedVisualIndex||point.visual}):null;
    const previous=additive?(extend?S.multiRanges.slice():selection.areas(S,true)):
      (extend?S.multiRanges.slice():[]);
    if(additive&&!extend){
      const removed=selection.removeSingleton(previous,point.row,point.col,point.visual,S.filtered);
      if(removed&&removed.length===0){
        // Keep one active cell: a completely empty selection is not useful.
        selectionStats?.schedule();return;
      }
      if(removed&&removed.length){
        const last=removed[removed.length-1];
        const row=last.row2,col=last.col2;
        const visual=last.visual2||row;
        selectCell(cellAt(row,col),row,col,`${colName(col)}${row}`,visual);
        S.multiRanges=removed.slice(0,-1);
        S.range=last;
        $('selectionStatus').textContent=removed.length>1?`${removed.length} 处选区`:'1 处选区';
        applySelectionClasses();selectionStats?.schedule();return;
      }
    }
    if(previous.length>=selection.LIMIT){toast('最多同时选择256处区域','warning');return;}
    if(extend && oldAnchor && editCopy?.grid.active && !additive) {
      extendRange(oldAnchor,point.row,point.col,point.visual); return;
    }
    selectPointedCell(dataCell,point,event);
    S.multiRanges=previous;
    if(extend){extendRange(oldAnchor,point.row,point.col,point.visual)}
    else if(additive){
      S.range=selection.rectangle(point,point,S.filtered);
      $('selectionStatus').textContent=`${previous.length+1} 处选区`;
      applySelectionClasses();selectionStats?.schedule();
    }
  }

  // Drag-to-select is deliberately visual only. Selection never writes to Excel.
  let dragAnchor=null,dragMoved=false,dragBase=[],suppressGridClick=false;
  let dragFrame=0,dragPending=null,scrollPress=null;
  function flushRangeDrag(){
    dragFrame=0;
    const pending=dragPending;dragPending=null;
    if(!pending||!dragAnchor)return;
    if(!dragMoved){
      dragMoved=true;
      selectCell(dragAnchor.cell||cellAt(dragAnchor.row,dragAnchor.col),dragAnchor.row,dragAnchor.col,dragAnchor.ref,dragAnchor.visual,false,false,dragAnchor.crosshair);
    }
    S.multiRanges=dragBase;
    extendRange(dragAnchor,pending.row,pending.col,pending.visual);
  }
  // A styled tile may be on screen one frame before its interactive DOM.
  // Resolve a body press against the same snapped edges and source data as
  // the renderer. A missing tile has no known cell to select.
  function cachedBodyPress(event){
    if(!['rowsLayer','gridCanvas','viewportFallback','grid'].includes(event.target.id))return null;
    const grid=$('grid'),rect=grid.getBoundingClientRect(),request=getViewportRequest();
    const {pw,ph}=request.position;
    const localX=event.clientX-rect.left-grid.clientLeft-ROW_HEAD;
    const localY=event.clientY-rect.top-grid.clientTop-headerHeight();
    if(localX<pw||localY<ph||localX+ROW_HEAD>=grid.clientWidth||localY+headerHeight()>=grid.clientHeight)return null;
    const hit=tiles.sourceAtPoint(request,{x:request.position.x+localX,y:request.position.y+localY-(S.filtered?ph:0),
      lastRow:S.filtered?S.filterTotal:S.maxRow,lastCol:S.maxCol,rowTop,colLeft,mergeAt:activeMerge});
    if(hit)hit.ref=colName(hit.col)+hit.row;
    return hit;
  }
  function beginRangeDrag(event){
    scrollPress=null;
    if(event.button!==0||!S.indexReady||event.target.closest('.column-filter,.auto-filter-button,.column-resize-grip'))return;
    const cell=event.target.closest?.('.grid-cell');
    const readingPress=event.pointerType==='mouse'&&!event.shiftKey&&!event.ctrlKey&&!event.metaKey&&!event.altKey&&
      !editCopy?.grid.active&&!calculator?.picking&&!S.sheetLoadingEpoch&&event.target.closest('#grid');
    const cached=!cell&&readingPress?cachedBodyPress(event):null;
    const parsed=cached||parseRef(cell?.dataset.ref);
    if(!parsed)return;
    hideCellMenu();
    $('cellTooltip').hidden=true;
    dragAnchor={row:parsed.row,col:parsed.col,ref:parsed.ref,visual:cached?.visual||Number(cell?.dataset.visualIndex)||parsed.row,
      cell:cached?.cell||cell?._xlModel,crosshair:cached?.crosshair,
      clientX:event.clientX,clientY:event.clientY};
    dragBase=(event.ctrlKey||event.metaKey)?window.ExcelLensMultiSelection.areas(S,true):
      (event.shiftKey?S.multiRanges.slice():[]);
    if(dragBase.length>=window.ExcelLensMultiSelection.LIMIT){dragAnchor=null;toast('最多同时选择256处区域','warning');return;}
    dragPending=null;
    if(dragFrame){cancelAnimationFrame(dragFrame);dragFrame=0}
    dragMoved=false;
    // During native scrolling the release can land on a different row or the
    // common rows layer. Bind a plain mouse selection to the source actually
    // pressed. Activating a link still requires a click on the original cell.
    if(readingPress){
      if(cached)selectCell(cached.cell,cached.row,cached.col,cached.ref,cached.visual,false,false,cached.crosshair);
      else selectWithModifiers(cell._xlModel||cellAt(parsed.row,parsed.col),dragAnchor,event);
      scrollPress={ref:parsed.ref,epoch:S.sheetEpoch,context:viewportContext(),view:S.rowView?.id,filter:S.filterId};
      $('grid').focus({preventScroll:true});
    }
  }
  function updateRangeDrag(event){
    if(!dragAnchor||!(event.buttons&1))return;
    if(!dragMoved&&event.clientX===dragAnchor.clientX&&event.clientY===dragAnchor.clientY)return;
    const cell=event.target.closest?.('.grid-cell');
    const target=parseRef(cell?.dataset.ref);
    if(!target||(target.row===dragAnchor.row&&target.col===dragAnchor.col))return;
    dragPending={row:target.row,col:target.col,visual:Number(cell.dataset.visualIndex)||target.row};
    if(!dragFrame)dragFrame=requestAnimationFrame(flushRangeDrag);
  }
  function endRangeDrag(event){
    if(!dragAnchor)return;
    if(dragFrame){cancelAnimationFrame(dragFrame);dragFrame=0}
    flushRangeDrag();
    if(dragMoved){
      if(event?.type!=='pointercancel'&&calculator?.picking&&S.range){
        const area=S.range;
        calculator.captureRange(`${colName(Math.min(area.col1,area.col2))}${Math.min(area.row1,area.row2)}`,`${colName(Math.max(area.col1,area.col2))}${Math.max(area.row1,area.row2)}`);
      }
      suppressGridClick=true;
      setTimeout(()=>{suppressGridClick=false},0);
    }
    if(dragMoved||event?.type==='pointercancel')scrollPress=null;
    dragAnchor=null;dragBase=[];
  }

  // Every command, including those on other tabs, is reachable by name.
  // Names match the control labels; tags carry the words people search for,
  // including the names these commands had before.
  const {buttonCommand, CommandPalette} = window.ExcelLensCommands;
  const ACTIONS = [
    buttonCommand('打开文件','openLocalMenuBtn','工作簿 选择 文件','Ctrl+O'),
    buttonCommand('连接在线数据库','openDatabaseBtn','数据库 MySQL PostgreSQL Oracle SQL Server ClickHouse'),
    buttonCommand('数据库快照','databaseSnapshotsBtn','固化 保存 核验 数据库'),
    buttonCommand('搜索','searchOpenBtn','查找公式或值 查找 公式 值','Ctrl+F'),
    buttonCommand('筛选','filterOpenBtn','自动筛选 表头 下拉','Ctrl+Shift+L'),
    buttonCommand('清除筛选','clearFilterBtn','清除 筛选 恢复 原序'),
    buttonCommand('排序','multiSortBtn','多级自定义排序 多级 多列 升序 降序 自定义'),
    buttonCommand('冻结窗格','freezeBtn','表头 行 列 固定'),
    buttonCommand('导出','exportOpenBtn','保存 CSV XLSX HTML 数据 导出中心','Ctrl+Shift+E'),
    {name:'导出当前选区',shortcut:'',tags:'区域 导出',run:()=>exportCenter.open('selection')},
    {name:'导出整个工作簿',shortcut:'',tags:'全部 Sheet 工作表',run:()=>exportCenter.open('all')},
    buttonCommand('编辑副本','editCopyOpen','改值 粘贴 另存 撤销 修改'),
    buttonCommand('分析','analysisOpenBtn','数据分析 透视 汇总 图表 分析表'),
    buttonCommand('统计选区','profileOpenBtn','数据洞察 精确统计 汇总 求和 公式 异常 计数','Ctrl+Alt+S'),
    buttonCommand('计算','calculatorOpenBtn','公式计算 加减乘除 金额','Ctrl+Alt+C'),
    buttonCommand('复制选区','copySelectionBtn','Excel 格式 复制 保留颜色 粘贴','Ctrl+C'),
    buttonCommand('复制为纯文本','copyPlainBtn','复制选区为纯文本 TSV 不带格式 粘贴 值'),
    buttonCommand('选择性复制','copySpecialBtn','表头 TSV Markdown JSON 原值 公式 复制','Ctrl+Alt+Shift+C'),
    buttonCommand('复制当前值','copyValueBtn','复制当前单元格值 复制 值 单格'),
    buttonCommand('复制公式','copyFormulaBtn','复制当前公式 复制 公式 原文','Ctrl+Shift+C'),
    buttonCommand('复制地址','copyAddressBtn','复制跨 Sheet 地址 引用 定位 复制'),
    buttonCommand('复制证据摘要','copyEvidenceBtn','复制审计证据摘要 审计 数值 公式 复制 底稿 来源'),
    buttonCommand('压缩包里的其他文件','archiveBtn','浏览当前压缩包 压缩包 文件 成员 浏览'),
    buttonCommand('高级筛选','advancedFilterBtn','高级条件筛选 筛选 条件 AND 多条件'),
    {name:'按当前单元格的值筛选',shortcut:'',tags:'根据当前单元格值筛选 快捷筛选 等于 当前值',run:()=>filterSelectedValue()},
    buttonCommand('TXT 表头','textHeaderBtn','设置文本表头 文本 表头 字段 设置'),
    buttonCommand('查找重复值','duplicateOpenBtn','高亮重复值 查重 高亮 重复单号 收款人 账号 重复数据','Ctrl+Shift+D'),
    buttonCommand('定位条件','inspectOpenBtn','查找 定位特殊单元格 公式 常量 错误 空值','Ctrl+Alt+G'),
    buttonCommand('数据规则','dataRulesOpenBtn','查看数据规则 数据验证 有效性 下拉列表 验证条件'),
    buttonCommand('记录卡片','recordCardOpenBtn','查看记录卡片 宽表 字段 长文本 原值 记录'),
    buttonCommand('快速分析选区','selectionQuickBtn','选区快速分析 分布 差额 趋势 小图 同行 比较','Ctrl+Q'),
    buttonCommand('转置查看选区','transposeOpenBtn','转置阅读当前选区 行列 交换 宽表 原坐标'),
    buttonCommand('显示公式','formulaViewBtn','显示或隐藏全部公式 公式视图 显示 隐藏 计算结果','Ctrl+`'),
    buttonCommand('追踪引用','tracePrecedentsBtn','公式追踪 引用 上游'),
    buttonCommand('追踪从属','traceDependentsBtn','追踪从属单元格 公式 下游 依赖 从属'),
    buttonCommand('名称与表','catalogueOpenBtn','名称与表目录 定义名称 表对象 结构化引用 区域','Ctrl+F3'),
    buttonCommand('单元格信息','cellDetailBtn','原值 源类型 公式 缓存 统计 解释','Ctrl+1'),
    buttonCommand('显示或隐藏列','columnsBtn','列 显隐 隐藏 显示'),
    buttonCommand('行与大纲','rowsBtn','行 隐藏 显示 分组 折叠 展开'),
    buttonCommand('原格式','formatBtn','显示/隐藏原格式 显示 隐藏 背景 颜色 字体 边框'),
    buttonCommand('显示或隐藏图片','imagesBtn','图片 图形 显示 隐藏'),
    buttonCommand('命名视图','namedViewOpenBtn','保存 应用 视图 筛选 排序 布局'),
    buttonCommand('分页预览','printPreviewOpenBtn','打印 纸张 页边距 缩放 重复标题','Ctrl+P'),
    buttonCommand('全屏','fullscreenBtn','窗口','F11'),
    {name:'设置当前行高',shortcut:'Alt+H,O,H',tags:'行高 调整 大小',run:()=>promptResize('row')},
    {name:'设置当前列宽',shortcut:'Alt+H,O,W',tags:'列宽 调整 大小',run:()=>promptResize('col')},
    {name:'当前列宽自动适应',shortcut:'Alt+H,O,I',tags:'列宽 自动 调整',run:()=>autoFitSize('col',S.selected?.col||1)},
    {name:'当前行高自动适应',shortcut:'Alt+H,O,A',tags:'行高 自动 调整',run:()=>autoFitSize('row',S.selected?.row||1)},
    buttonCommand('对账','reconcileOpenBtn','双表对账 主键 差异 未匹配 重复键'),
    buttonCommand('关联','relationsOpenBtn','关联提取 连接 左表 右表 未匹配'),
    buttonCommand('标记与备注','bookmarkOpenBtn','审计标记 备注复核 异常 核实 审核 提醒 标签 待复核','Ctrl+Alt+B'),
    buttonCommand('比较快照','snapshotsOpenBtn','保存 分析 趋势 差额 历史 比较'),
    buttonCommand('本地数据','storageOpenBtn','数据管理 备份 迁入 缓存 清理 存储'),
    buttonCommand('任务恢复','taskRecoveryOpenBtn','继续 查询 筛选 排序 导出 下载 中断'),
    {name:'跳转到单元格',shortcut:'Ctrl+G',tags:'定位 行 列 名称框',run:()=>{$('nameBox').focus();$('nameBox').select();}},
    {name:'跳到数据区域边界',shortcut:'Ctrl+方向键',tags:'跳至数据区域边界 移动 跳过 空值 快速定位',run:()=>jumpDataEdge('down')},
    buttonCommand('返回上一位置','historyBackBtn','历史 返回 位置','Alt+←'),
    buttonCommand('前进到下一位置','historyForwardBtn','历史 前进 位置','Alt+→'),
    {name:'打开所选单元格的超链接',shortcut:'Ctrl+Enter',tags:'打开所选单元格超链接 跳转 工作表 网页',run:()=>followCellHyperlink(S.selected?.hyperlink)},
    {name:'全部工作表',shortcut:'',tags:'工作表列表 Sheet 导航',run:()=>{$('sheetListBtn').click();}},
    buttonCommand('返回首页','homeBtn','返回最近文件 最近打开 历史 工作簿'),
    buttonCommand('快捷键','helpBtn','快捷键与操作帮助 帮助 操作说明'),
    buttonCommand('退出程序','exitBtn','退出 关闭 程序'),
  ];
  const commandPalette = new CommandPalette(ACTIONS, {
    node:$, sheets:() => S.sheets, selectSheet, canOpen:() => S.opened, openDialog, closeDialog, toast,
    reveal:id => window.ExcelLensRibbon?.reveal(id)
  });
  const openCommandPalette = () => commandPalette.open();

  function selectNativeRange(kind){
    if(!S.indexReady||!S.selected)return;
    const row=S.selected.row,col=S.selected.col;
    const visual=S.filtered?S.selectedVisualIndex:row;
    if(kind==='row'){extendRange({row,col:1,visual},row,Math.max(1,S.usedMaxCol),visual);}
    else if(kind==='column'){
      extendRange({row:S.filtered?row:1,col,visual:S.filtered?1:1},S.filtered?row:Math.max(1,S.usedMaxRow),col,S.filtered?S.filterTotal:S.usedMaxRow);
    }else{
      extendRange({row:1,col:1,visual:1},Math.max(1,S.usedMaxRow),Math.max(1,S.usedMaxCol),S.filtered?S.filterTotal:S.usedMaxRow);
    }
    $('grid').focus({preventScroll:true});
  }
  function switchSheet(delta){
    if(!S.sheet||!S.sheets.length)return;
    const index=S.sheets.findIndex(x=>x.name===S.sheet);
    const next=(index+delta+S.sheets.length)%S.sheets.length;
    selectSheetTab(S.sheets[next].name).catch(error=>toast(error.message,'error'));
  }

  // Excel Home ribbon access keys: Alt+H, O, I/A/W/H. Unlike a global
  // keylogger this tiny state machine is active only while the grid is focused
  // and expires, so text fields and browser navigation remain unmodified.
  let ribbonSequence='',ribbonExpires=0;
  function handleRibbonSequence(event,key,gridFocused,take){
    if(!gridFocused||event.ctrlKey||event.metaKey||event.shiftKey)return false;
    const now=Date.now();
    if(now>ribbonExpires)ribbonSequence='';
    if(event.altKey&&key==='h'){
      ribbonSequence='H';ribbonExpires=now+3000;
      take(()=>{});return true;
    }
    if(!ribbonSequence||event.key.length!==1)return false;
    const next=ribbonSequence+key.toUpperCase();
    if(next==='HO'){
      ribbonSequence=next;ribbonExpires=now+3000;
      take(()=>{});return true;
    }
    if(ribbonSequence==='HO'){
      ribbonSequence='';
      const target={I:()=>autoFitSize('col',S.selected?.col||1),
        A:()=>autoFitSize('row',S.selected?.row||1),
        W:()=>promptResize('col',S.selected?.col||1),
        H:()=>promptResize('row',S.selected?.row||1)}[key.toUpperCase()];
      if(target){take(target);return true;}
    }else ribbonSequence='';
    return false;
  }

  // Name the way forward when an edit key is pressed while reading.
  const readOnlyHint = () => $('editCopyOpen').hidden ? '当前是只读浏览，原文件不会被修改' : '当前是只读浏览。要修改内容，请选择“编辑副本”';

  // Excel-style keyboard routing: do not hijack editing, browser selection or
  // modal dialog controls. Only the grid receives caret/navigation keys;
  // read-only application commands also work after toolbar clicks.
  function handleSpreadsheetKeydown(event){
    if (window.ExcelLensDisplayFormat?.isComposing?.(event) || event.isComposing || event.keyCode === 229) return;
    if (editCopy?.grid.active && !document.querySelector('dialog:modal')) return;
    const key=event.key.toLowerCase();
    const ctrl=event.ctrlKey||event.metaKey;
    const alt=event.altKey;
    const shift=event.shiftKey;
    const active=document.activeElement;
    const editing=active?.matches?.('input,textarea,select,[contenteditable="true"],[role="searchbox"]')||active?.isContentEditable;
    const modal=document.querySelector('dialog:modal');
    const insideFormula=active===$('formulaText')||$('formulaText').contains(active);
    const gridFocused=active===$('grid')||insideFormula||active?.closest?.('.grid-region');
    const take=callback=>{event.preventDefault();event.stopPropagation();Promise.resolve().then(callback).catch(err=>toast(err.message,'error'));};
    if(!modal&&ctrl&&alt&&!shift&&key==='c'){take(()=>calculator.toggle());return;}
    if(!modal&&calculator?.handleKey(event))return;
    if(event.key==='Escape'){
      ribbonSequence='';
      hideCellMenu();autoFilterUI?.hide();
      readingMode?.close();
      highlightMode?.close();
      if($('traceDialog').open&&!modal)traceUI?.minimize();
      if($('searchDialog').open && !modal){event.preventDefault();closeDialog('searchDialog');}
      return;
    }
    if(modal||event.defaultPrevented){ribbonSequence='';return;}
    if(ctrl&&!alt&&!shift&&key==='enter'&&gridFocused&&!editing&&S.selected?.hyperlink){
      take(()=>followCellHyperlink(S.selected.hyperlink));return;
    }
    if(ctrl&&!alt&&!shift&&key==='o'&&!editing){take(chooseFile);return;}
    if(!$('welcome').hidden||!S.opened||!S.indexReady)return;
    if(ctrl&&!alt&&!shift&&key==='c'&&window.getSelection()?.toString())return;
    if(insideFormula&&ctrl&&!alt&&!shift&&key==='a'){
      take(()=>{const range=document.createRange();range.selectNodeContents($('formulaText'));
        const selection=window.getSelection();selection?.removeAllRanges();selection?.addRange(range);});
      return;
    }
    if(editing)return;
    if(handleRibbonSequence(event,key,gridFocused,take))return;

    if(ctrl&&!alt&&!shift&&key==='k'){take(openCommandPalette);return;}
    if(ctrl&&!alt&&!shift&&key==='s'){take(()=>exportCenter.open());return;}
    if(ctrl&&!alt&&!shift&&key==='f'){take(()=>openSearch());return;}
    if((ctrl&&!alt&&!shift&&key==='g')||event.key==='F5'){
      take(()=>{$('nameBox').focus();$('nameBox').select();});return;
    }
    if(ctrl&&!alt&&!shift&&key==='c'){take(()=>copySelection());return;}
    if(ctrl&&alt&&shift&&key==='c'){take(()=>copySpecial.open());return;}
    if(ctrl&&!alt&&shift&&key==='c'){
      take(()=>S.selected?.hasFormula?copyText(S.selected.formula||''):toast('当前单元格不是公式'));return;
    }
    if(ctrl&&!alt&&!shift&&key==='a'&&gridFocused){take(()=>selectNativeRange('all'));return;}
    if(ctrl&&!alt&&!shift&&event.code==='Space'&&gridFocused){take(()=>selectNativeRange('column'));return;}
    if(ctrl&&!alt&&shift&&event.code==='Space'&&gridFocused){take(()=>selectNativeRange('row'));return;}
    if(!ctrl&&!alt&&shift&&event.code==='Space'&&gridFocused){take(()=>selectNativeRange('row'));return;}
    if(ctrl&&shift&&!alt&&key==='l'){take(toggleAutoFilter);return;}
    if(ctrl&&!alt&&!shift&&(event.code==='Backquote'||event.key==='`')){take(toggleFormulaView);return;}
    if(ctrl&&alt&&key==='g'){take(()=>selectionInspector.open('formulas'));return;}
    if(ctrl&&shift&&!alt&&key==='d'){take(()=>selectionInspector.open('duplicates'));return;}
    if(ctrl&&shift&&!alt&&key==='e'){take(()=>exportCenter.open());return;}
    if(ctrl&&alt&&key==='s'){take(()=>profileCenter.open());return;}
    if(ctrl&&alt&&key==='b'){take(()=>bookmarkCenter.open());return;}
    if(ctrl&&!shift&&!alt&&key==='q'){take(()=>selectionQuick?.open());return;}
    if(ctrl&&!alt&&!shift&&key==='1'){take(showCellDetail);return;}
    if(event.key==='F2'){take(showCellDetail);return;}
    if(ctrl&&!alt&&!shift&&(event.key==='PageUp'||event.key==='PageDown')){
      take(()=>switchSheet(event.key==='PageUp'?-1:1));return;
    }
    if(!ctrl&&!alt&&event.key==='F3'){take(()=>stepSearch(shift?-1:1));return;}
	if(ctrl&&!alt&&!shift&&event.key==='F3'){take(()=>catalogueCenter.open());return;}
    if(alt&&!ctrl&&(event.key==='ArrowLeft'||event.key==='ArrowRight')){
      take(()=>goHistory(event.key==='ArrowLeft'?-1:1));return;
    }
    if(alt&&!ctrl&&!shift&&event.key==='ArrowDown'&&S.selected){
      take(()=>{
        if(!S.autoFilter){toast('先按 Ctrl+Shift+L 开启自动筛选','warning');return;}
        const c=S.selected.col;
        const bounds=$('grid').getBoundingClientRect();
        const anchor=[...document.querySelectorAll(`.auto-filter-button[data-column="${c}"]`)].find(node=>{
          const rect=node.getBoundingClientRect();
          return rect.width>0&&rect.top>=bounds.top&&rect.bottom<=bounds.bottom&&rect.left>=bounds.left&&rect.right<=bounds.right;
        })||$('filterOpenBtn');
        return autoFilterUI.open(c,anchor);
      });return;
    }
    if(!gridFocused)return;
    if((ctrl&&!alt&&['x','v','z','y'].includes(key))||(!ctrl&&!alt&&(event.key==='Backspace'||event.key==='Delete'))){
      take(()=>toast(readOnlyHint()));return;
    }
    if(ctrl&&!alt&&['ArrowUp','ArrowDown','ArrowLeft','ArrowRight'].includes(event.key)){
      const direction={ArrowUp:'up',ArrowDown:'down',ArrowLeft:'left',ArrowRight:'right'}[event.key];
      take(()=>jumpDataEdge(direction,shift));return;
    }
    if(!ctrl&&!alt&&['ArrowUp','ArrowDown','ArrowLeft','ArrowRight'].includes(event.key)){
      const [row,col]={ArrowUp:[-1,0],ArrowDown:[1,0],ArrowLeft:[0,-1],ArrowRight:[0,1]}[event.key];
      take(()=>moveSelection(row,col,shift));return;
    }
    if(!ctrl&&!alt&&(event.key==='Tab'||event.key==='Enter')){
      const sign=shift?-1:1;
      take(()=>event.key==='Tab'?moveSelection(0,sign):moveSelection(sign,0));return;
    }
    // Selection extension uses the ORIGINAL anchor, even though navigation
    // itself refreshes the active cell and would otherwise discard the range.
    if((event.key==='Home'||event.key==='End')&&!alt){
      if(event.key==='End'&&!ctrl)return;
      const targetRow=event.key==='End'?S.usedMaxRow:(ctrl?1:S.selected?.row||1);
      const targetCol=event.key==='End'?S.usedMaxCol:1;
      take(async()=>{
        const anchor=shift?(S.rangeAnchor||{row:S.selected.row,col:S.selected.col,visual:S.selectedVisualIndex}):null;
        if(anchor&&S.filtered){toast('筛选后请通过可见行拖选扩展区域','warning');return;}
        await navigateToCell(S.sheet,`${colName(targetCol)}${targetRow}`);
        if(anchor)extendRange(anchor,targetRow,targetCol);
      });return;
    }
    if(alt&&!ctrl&&(event.key==='PageUp'||event.key==='PageDown')){
      const width=$('grid').clientWidth-ROW_HEAD;
      const count=Math.max(1,Math.floor(width/Math.max(32,colWidth(S.selected?.col||1)))-1);
      take(()=>moveSelection(0,(event.key==='PageUp'?-1:1)*count,shift));return;
    }
    if(!ctrl&&!alt&&(event.key==='PageUp'||event.key==='PageDown')){
      const step=Math.max(1,Math.floor($('grid').clientHeight/rowHeight())-2);
      take(()=>moveSelection(event.key==='PageDown'?step:-step,0,shift));return;
    }
  }

  function bindEvents() {
    const homeMenu = $('homeDataMenu');
    const menuTrigger = $('homeDataTrigger');
    homeMenu.addEventListener('click', event => {
      if (!event.target.closest('[role="button"]')) return;
      const selection = window.getSelection();
      if (event.detail && !selection.isCollapsed && homeMenu.contains(selection.anchorNode)) {
        event.stopPropagation();
        return;
      }
      homeMenu.open = false;
      menuTrigger.focus();
    }, true);
    homeMenu.addEventListener('keydown', event => {
      if (event.target.matches('[role="button"]') && (event.key === 'Enter' || event.key === ' ')) {
        event.preventDefault();
        event.stopPropagation();
        if (!event.repeat) event.target.click();
        return;
      }
      if (event.key === 'Escape' && homeMenu.open) {
        event.preventDefault();
        event.stopPropagation();
        homeMenu.open = false;
        menuTrigger.focus();
      }
    });
    document.addEventListener('pointerdown', event => {
      if (homeMenu.open && !homeMenu.contains(event.target)) homeMenu.open = false;
    });
    window.ExcelLensStorage({api, openDialog, background: window.ExcelLensBackground({
      api, state:S, openDialog, toast, adoptTask:adoptRecoveredTask,
      analysis:(job,status)=>window.ExcelLensAnalytics.reopenJob(job,status),
      reconcile:(job,status)=>reconcileCenter.reopenJob(job,status)
    })});
    window.addEventListener("excellens:session-expired", event => { setStatus(event.detail); toast(event.detail, "warning"); });
    document.addEventListener('toolbar-setting-error', event => toast(event.detail, 'error'));
    commandPalette.bind();
    $("openLocalMenuBtn").addEventListener("click", chooseFile);
    const databasePanel = new window.ExcelLensDatabasePanel({api, onOpened:async result=>{
      const current = await api('/api/status');
      if (!current.opened || current.workbookKey !== result.workbookKey) throw new Error('工作簿已切换');
      await acceptOpenedWorkbook(current);
    }});
    for (const id of ['openDatabaseBtn','openDatabaseHome']) {
      $(id).addEventListener('click', () => { if (canOpenWorkbook('连接数据库')) void databasePanel.open(); });
    }
    const databaseSnapshots = new window.ExcelLensDatabaseSnapshots({
      api, fmtBytes, openWorkbook,
      onRetained: key => { if (S.workbookKey === key) $('retainDatabaseSnapshot').hidden = true; }
    });
    for (const id of ['databaseSnapshotsBtn', 'databaseSnapshotsHome']) {
      $(id).addEventListener('click', () => databaseSnapshots.show());
    }
    $('retainDatabaseSnapshot').addEventListener('click', () => databaseSnapshots.show(S.workbookKey));
    $("homeBtn").addEventListener("click",()=>{if(canOpenWorkbook('返回首页')){showWelcome();recentManager.load();}});
    $("chooseFileBtn").addEventListener("click", chooseFile);
    $("fileInput").addEventListener("change", event => {
      const file = event.target.files?.[0];
      event.target.value = '';
      uploadFile(file);
    });
    const isComposing = event => window.ExcelLensDisplayFormat?.isComposing?.(event) ?? (event.isComposing || event.keyCode === 229);
    $("nameBox").addEventListener("keydown", event => {
      if (isComposing(event)) return;
      if (event.key === "Enter") {
        event.preventDefault();
        const address = $("nameBox").value;
        const navigate = () => navigateNameBox(address).then(()=>editCopy?.grid.active ? editCopy.grid.focus() : $('grid').focus({preventScroll:true}));
        if (editCopy?.grid.active) editCopy.grid.navigate(navigate);
        else navigate().catch(err=>toast(err.message,'error'));
      } else if (event.key === "Escape") {
        $("nameBox").value = S.selected?.ref || "";
        $("nameBox").blur();
      }
    });
    searchPanel.bind?.();
    $("multiSortBtn").addEventListener("click",()=>multiSort.show());
    $("inspectOpenBtn").addEventListener("click",()=>selectionInspector.open());
    $("duplicateOpenBtn").addEventListener("click",()=>selectionInspector.open("duplicates"));
    $("formulaViewBtn").addEventListener("click",toggleFormulaView);
    $("exportOpenBtn").addEventListener("click", () => exportCenter.open());
    $("profileOpenBtn").addEventListener("click", () => profileCenter.open());
    $("commandOpenBtn").addEventListener("click", () => openCommandPalette());
    $("selectionStatus").addEventListener("click", () => profileCenter.open());
    $("filterOpenBtn").addEventListener("click", toggleAutoFilter);
    $('textHeaderBtn').addEventListener('click',()=>{
      if(!S.indexReady)return;
      $('textHeaderRow').value=String(S.autoFilter?.row||S.selected?.row||1);
      $('textHeaderAuto').disabled=!S.source?.tableHeader;
      openDialog('textHeaderDialog');
    });
    $('textHeaderApply').addEventListener('click',()=>applyTextTableHeader(Number($('textHeaderRow').value)));
    $('textHeaderRow').addEventListener('keydown',event=>{if(!isComposing(event)&&event.key==='Enter'){event.preventDefault();applyTextTableHeader(Number(event.target.value));}});
    $('textHeaderAuto').addEventListener('click',()=>applyTextTableHeader(0,true));
    $('textHeaderPlain').addEventListener('click',disableTextTableHeader);
    $("advancedFilterBtn").addEventListener("click", () => openFilter());
    $("freezeBtn").addEventListener("click", openFreezeDialog);
    $("formatBtn").addEventListener("click", toggleOriginalFormat);
    $("fontSelect").addEventListener("change", event => changeDisplayFont(event.target.value));
    $("freezeFirstRowBtn").addEventListener("click", () => applyFreeze(1,S.freeze.cols));
    $("freezeFirstColBtn").addEventListener("click", () => applyFreeze(S.freeze.rows,1));
    $("freezeAtCellBtn").addEventListener("click", () => applyFreeze(Math.max(0,(S.selected?.row||1)-1),Math.max(0,(S.selected?.col||1)-1)));
    $("freezeResetBtn").addEventListener("click", () => applyFreeze(0,0));
    $("freezeApplyBtn").addEventListener("click", () => applyFreeze($("freezeRowsInput").value,$("freezeColsInput").value));
    $("historyBackBtn").addEventListener("click", () => goHistory(-1));
    $("historyForwardBtn").addEventListener("click", () => goHistory(1));
    $("helpBtn").addEventListener("click", () => openDialog("helpDialog"));
    $("addFilterRuleBtn").addEventListener("click", () => addFilterRule());
    $("applyFilterBtn").addEventListener("click", applyFilter);
    $("cancelFilterJobBtn").addEventListener("click", async () => {
      const jobID = S.filterJob;
      await cancelFilter(jobID);
      $("filterProgressText").textContent = "已请求取消筛选任务";
      $("applyFilterBtn").disabled = false;
      $("cancelFilterJobBtn").hidden = true;
    });
    $("clearFilterBtn").addEventListener("click", () => clearFilter().catch(error => toast(error.message,'error')));
    $("sheetListBtn").addEventListener("click", () => { renderSheetPicker(); openDialog("sheetDialog"); $("sheetPickerSearch").focus(); });
    $("sheetPrevBtn").addEventListener("click",()=>pageSheetTabs(-1));
    $("sheetNextBtn").addEventListener("click",()=>pageSheetTabs(1));
    $("sheetTabs").addEventListener("scroll",updateSheetPager,{passive:true});
    new ResizeObserver(updateSheetPager).observe($("sheetTabs"));
    $("sheetPickerSearch").addEventListener("input", event => renderSheetPicker(event.target.value));
    $("tracePrecedentsBtn").addEventListener("click", tracePrecedents);
    $("traceDependentsBtn").addEventListener("click", traceDependents);
    $("cellDetailBtn").addEventListener("click", showCellDetail);
    $("copyFormulaBtn").addEventListener("click", () => copyText(S.selected?.formula || ""));
    $("copyValueBtn").addEventListener("click", () => copyText(S.selected?.display ?? S.selected?.raw ?? ""));
    $('copySelectionBtn').addEventListener('click', () => copySelection());
    $('copyPlainBtn').addEventListener('click', () => copySelection(true));
    $('copySpecialBtn').addEventListener('click', () => copySpecial.open());
    $('copyAddressBtn').addEventListener('click', () => copyQualifiedAddress());
    $('copyEvidenceBtn').addEventListener('click', () => copyCellEvidence());
    $('selectionQuickBtn').addEventListener('click', () => selectionQuick?.open());
    $('transposeOpenBtn').addEventListener('click', () => transposeCenter?.open());
    $('snapshotsOpenBtn').addEventListener('click', () => window.ExcelLensSnapshots?.open());
    $('taskRecoveryOpenBtn').addEventListener('click', () => taskRecovery.open());
    $("fullscreenBtn").addEventListener("click", toggleFullscreen);
    document.addEventListener("fullscreenchange", updateFullscreenButton);
    $("zoomOutBtn").addEventListener("click", () => setZoom(S.zoom - 5));
    $("zoomInBtn").addEventListener("click", () => setZoom(S.zoom + 5));
    $("retryIndexBtn").addEventListener("click", () => selectSheet(S.sheet));
    for (const id of ["exitBtn", "homeExitBtn"]) $(id).addEventListener("click", async () => {
      if(!canOpenWorkbook('退出程序'))return;
      try { await api("/api/exit", { method: "POST" }); } catch { /* process may close immediately */ }
      setStatus("程序已退出");
    });

    $("grid").addEventListener("wheel", event => {
      if(event.ctrlKey){
        event.preventDefault();
        setZoom(S.zoom + (event.deltaY < 0 ? 5 : -5));
        return;
      }
      if(virtualScroll.scale===1||!event.cancelable)return;
      event.preventDefault();
      const grid=$('grid');
      const next=virtualScroll.wheel(event,grid.scrollTop,rowHeight(),grid.clientHeight-headerHeight());
      grid.scrollLeft+=next.left;
      grid.scrollTop=next.top;
      followGridScroll();
    }, { passive:false });
    $("grid").addEventListener("scroll", () => {
      scheduleRangeFrames();
      if(S.sheetLoadingEpoch)return;
      if(!$("cellMenu").hidden||!$("sizeContextMenu").hidden||!$("editGridMenu").hidden)hideCellMenu();
      followGridScroll();
      syncTableColumnHeaders();
      frozenPanes?.followScroll();
      if(S.freeze.rows||S.freeze.cols)frozenPanes.schedule();
      if(hoverRef||!$("cellTooltip").hidden){$("cellTooltip").hidden = true;hoverRef = "";}
      motion.sample($('grid').scrollLeft,getVirtualScrollTop(),performance.now());
      scheduleViewport(false);
    }, { passive: true });
    $("grid").addEventListener("click", event => {
      if (suppressGridClick) {suppressGridClick=false;return;}
      const rowHead=event.target.closest?.(".row-head");
      if(rowHead){
        if(event.target.closest?.('.row-resize-grip'))return;
        const owner=rowHead.closest(".grid-row");
        selectWholeRow(Number(owner?.dataset.row),Number(owner?.dataset.visualIndex));
        return;
      }
      const cell = event.target.closest?.(".grid-cell");
      if (!cell) return;
      const ref = cell.dataset.ref;
      const parsed = parseRef(ref);
      if (!parsed) return;
      const visual=Number(cell.dataset.visualIndex);
      const dataCell=cellAt(parsed.row,parsed.col),pressed=scrollPress;scrollPress=null;
      if(pressed&&event.detail){
        if(pressed.epoch!==S.sheetEpoch||pressed.context!==viewportContext()||pressed.view!==S.rowView?.id||pressed.filter!==S.filterId||pressed.ref!==ref)return;
      }else selectWithModifiers(dataCell,{...parsed,visual},event);
      const calculating=calculator?.captureCell(parsed.ref,event);
      if (!calculator?.picking && !event.shiftKey && !event.ctrlKey && !event.metaKey && !event.altKey && dataCell.hyperlink) {
        followCellHyperlink(dataCell.hyperlink).catch(error=>toast(error.message,'error'));
      }
      if(!calculating)$("grid").focus({ preventScroll: true });
      $("cellTooltip").hidden = true;
    });
    document.querySelector(".grid-region").addEventListener("click", event => {
      if (suppressGridClick) {suppressGridClick=false;return;}
      if (event.target.closest("#grid")) return;
      const frozenRowHead=event.target.closest(".pinned-row-head");
      if(frozenRowHead){if(!event.target.closest?.('.row-resize-grip'))selectWholeRow(Number(frozenRowHead.dataset.row));return;}
      const column = event.target.closest(".pinned-head");
      if (column) {
        if(event.target.closest?.('.column-resize-grip,.column-filter,.auto-filter-button'))return;
        const col = Number(column.dataset.col), row = S.selected?.row || 1;
        if(event.detail===0||!S.columnSelection){selectColumnSpan(event.shiftKey?(S.columnAnchor||col):col,col);captureColumnSelection();}
        return;
      }
      const cell = event.target.closest(".pinned-cell");
      if (!cell) return;
      const parsed = parseRef(cell.dataset.ref); if (!parsed) return;
      const visual=Number(cell.dataset.visualIndex)||parsed.row;
      const dataCell=cellAt(parsed.row,parsed.col);
      selectWithModifiers(dataCell,{...parsed,visual},event);
      const calculating=calculator?.captureCell(parsed.ref,event);
      if(!calculator?.picking&&!event.shiftKey&&!event.ctrlKey&&!event.metaKey&&!event.altKey&&dataCell.hyperlink){
        followCellHyperlink(dataCell.hyperlink).catch(error=>toast(error.message,'error'));
      }
      if(!calculating)$("grid").focus({preventScroll:true});
    });
    document.querySelector(".grid-region").addEventListener("pointermove", event => {
      if (event.target.closest("#grid")) return;
      showTooltip(event);
    });
    const region = document.querySelector(".grid-region");
    region.addEventListener("contextmenu", openCellMenu);
    region.addEventListener("dblclick", event => {
      if(calculator?.picking){event.preventDefault();return;}
      if (event.target.closest?.(".grid-cell")) showCellDetail();
    });
    columnController.bind?.(region);
    region.addEventListener("pointerdown", beginRangeDrag);
    document.addEventListener("pointermove", updateRangeDrag);
    document.addEventListener("pointerup", endRangeDrag);
    document.addEventListener("pointercancel", endRangeDrag);
    document.addEventListener("click", event => {if (!event.target.closest?.(".cell-context-menu")) hideCellMenu();});
    $('sizeContextAdjust').addEventListener('click',()=>{const target=sizeContextTarget;hideCellMenu();if(target)promptResize(target.kind,target.index);});
    $('sizeContextReset').addEventListener('click',()=>{const target=sizeContextTarget;hideCellMenu();if(target)commitResize(target.kind,target.index,0);});
    $('sizeContextFit').addEventListener('click',()=>{const target=sizeContextTarget;hideCellMenu();if(target)autoFitSize(target.kind,target.index);});
    $('sizeContextHideCol').addEventListener('click',()=>{const target=sizeContextTarget;hideCellMenu();if(target?.kind==='col')setColumnVisibility(target.index===target.end?'column':'range',target.index,true,target.end);});
    $('sizeContextShowCol').addEventListener('click',()=>{const target=sizeContextTarget;hideCellMenu();if(target?.kind==='col')setColumnVisibility(target.index===target.end?'column':'range',target.index,false,target.end);});
    $("menuCopyValue").addEventListener("click", () => {copySelection();hideCellMenu();});
    $('menuCopyPlain').addEventListener('click',()=>{copySelection(true);hideCellMenu();});
    $('menuCopySpecial').addEventListener('click',()=>{hideCellMenu();copySpecial.open();});
    $('menuFilterValue').addEventListener('click',()=>{hideCellMenu();filterSelectedValue();});
    $("menuCopyFormula").addEventListener("click", () => {copyText(S.selected?.formula||"");hideCellMenu();});
    $('menuOpenHyperlink').addEventListener('click',()=>{
      const link=S.selected?.hyperlink;hideCellMenu();
      followCellHyperlink(link).catch(error=>toast(error.message,'error'));
    });
    $("menuCopyAddress").addEventListener("click", () => {hideCellMenu();copyQualifiedAddress();});
    $("menuCopyEvidence").addEventListener("click", () => {hideCellMenu();copyCellEvidence();});
    $("menuSearchValue").addEventListener("click", () => {const q=S.selected?.display ?? "";hideCellMenu();openSearch("value",q);});
    $("menuTrace").addEventListener("click", () => {hideCellMenu();tracePrecedents();});
    $("menuInspect").addEventListener("click", () => {hideCellMenu();selectionInspector.open('formulas');});
    $("menuDuplicates").addEventListener("click", () => {hideCellMenu();selectionInspector.open('duplicates');});
	$('menuDataRules').addEventListener('click',()=>{hideCellMenu();dataRulesCenter.open();});
	$('menuRecordCard').addEventListener('click',()=>{hideCellMenu();recordCardCenter?.open();});
	$('menuPrintPreview').addEventListener('click',()=>{hideCellMenu();printingCenter?.open();});
    $("menuFreezeHere").addEventListener("click", () => {hideCellMenu();applyFreeze(Math.max(0,(S.selected?.row||1)-1),Math.max(0,(S.selected?.col||1)-1));});
    $("menuExportRange").addEventListener("click", () => {hideCellMenu();exportCenter.open("selection");});
    $("menuProfileRange").addEventListener("click", () => {hideCellMenu();profileCenter.open();});
	$('menuSelectionQuick').addEventListener('click',()=>{hideCellMenu();selectionQuick?.open();});
	$('menuTranspose').addEventListener('click',()=>{hideCellMenu();transposeCenter?.open();});
	$('profileQuickBtn').addEventListener('click',()=>{$('profileDialog').close();selectionQuick?.open();});
    $("menuBookmark").addEventListener("click", () => {hideCellMenu();bookmarkCenter.open();});
    $("grid").addEventListener("pointermove", showTooltip);
    $("grid").addEventListener("pointerleave", () => { $("cellTooltip").hidden = true; });


    const dropZone = $("dropZone");
    for (const type of ["dragenter", "dragover"]) dropZone.addEventListener(type, event => {
      event.preventDefault(); event.stopPropagation(); dropZone.classList.add("dragging");
    });
    for (const type of ["dragleave", "dragend"]) dropZone.addEventListener(type, event => {
      event.preventDefault(); event.stopPropagation(); dropZone.classList.remove("dragging");
    });
    dropZone.addEventListener("drop", event => {
      event.preventDefault(); event.stopPropagation(); dropZone.classList.remove("dragging");
      uploadFile(event.dataTransfer.files?.[0]);
    });
    document.addEventListener("dragover", event => event.preventDefault());
    document.addEventListener("drop", event => {
      if (event.target.closest("#dropZone")) return;
      event.preventDefault();
      const file = event.dataTransfer?.files?.[0];
      if (file) uploadFile(file);
    });
    new ResizeObserver(() => {syncFreezeScrollGutters();scheduleRangeFrames();}).observe($("grid"));
    let gridPixelRatio = window.devicePixelRatio || 1, resizeFrame = 0;
    function resizeWorkspace() {
      const ratio = window.devicePixelRatio || 1, changed = ratio !== gridPixelRatio;
      gridPixelRatio = ratio;
      if (S.previewOnly) { updateCanvasSize(); scheduleInitialPreview(); return; }
      if (!S.indexReady) return;
      updateCanvasSize();
      if (changed) {
        renderGridFromCurrentWindow();
        paintRangeFrames();
      } else {
        scheduleRangeFrames();
      }
      // Ordinary window resizes reuse existing buffered tiles whenever they
      // still cover the visible area; only a DPR change forces a full repaint.
      scheduleViewport(changed);
      frozenPanes.schedule(changed);
    }
    function scheduleResizeWorkspace() {
      if (resizeFrame) return;
      resizeFrame = requestAnimationFrame(() => { resizeFrame = 0; resizeWorkspace(); });
    }
    function watchPixelRatio() {
      if ((window.devicePixelRatio || 1) !== gridPixelRatio) resizeWorkspace();
      window.matchMedia?.(`(resolution: ${gridPixelRatio}dppx)`).addEventListener('change', watchPixelRatio, {once:true});
    }
    watchPixelRatio();
    window.addEventListener("resize", scheduleResizeWorkspace);
    // Exactly one keyboard dispatcher. Grid shortcuts bubble here, so a
    // non-modal search panel cannot trigger two actions or open a JS prompt.
    document.addEventListener('keydown',handleSpreadsheetKeydown);


  }

  document.addEventListener('excellens:readingchange',scheduleStylePaint);
  readingMode=new window.ExcelLensReadingMode({button:$('readingModeBtn'),panel:$('readingModePanel'),veil:$('readingVeil'),textureToggle:$('gridTextureToggle'),onTextureChange:syncGridBackdrop});
  highlightMode=new window.ExcelLensHighlightMode({toggle:$('crosshairToggle'),button:$('crosshairColorBtn'),panel:$('crosshairPanel'),onChange:applySelectionClasses});
  recentManager=new window.ExcelLensRecentWorkbooks({api,toast,open:item=>openWorkbook(() =>
    api('/api/recent',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({action:'open',id:item.id})}),item.name,item.size)});
  if (window.ExcelLensEditCopy && fileFormats.editing && $('editCopyDialog')) {
    editCopy = new window.ExcelLensEditCopy({api, openDialog, adopt: acceptOpenedWorkbook, canOpen: canOpenWorkbook,
      context: () => ({book: S.book, fileName: S.fileName, source: S.source, sheet: S.sheet, ref: S.selected?.ref}),
      grid: {
        selected:()=>S.selected, ready:()=>S.indexReady && !S.sheetLoadingEpoch, filtered:()=>S.filtered, mappedRows:()=>S.filtered||!!S.rowVisibility?.hidden.length,
        clipboardIdentity, paintReferences:()=>paintRangeFrames(),
        selectArea:a=>{
          selectCellByAddress(colName(a.col1)+a.row1,a.row1);
          extendRange({row:a.row1,col:a.col1,visual:a.row1},a.row2,a.col2,a.row2);
        },
        captureOrigin:()=>({range:S.range && {...S.range},multi:S.multiRanges.slice(),anchor:S.rangeAnchor && {...S.rangeAnchor},
          filter:S.filterId,view:S.rowView?.id}),
        restoreOrigin:async d=>{
          if (!await selectSheet(d.address.sheet,d.ref,false,null,null,d) || editCopy.grid.draft !== d) return false;
          const origin=d.origin;
          if (origin.filter===S.filterId && origin.view===S.rowView?.id) {
            S.range=origin.range; S.multiRanges=origin.multi; S.rangeAnchor=origin.anchor;
            applySelectionClasses(); selectionStats?.schedule();
          }
          return true;
        },
        dragPoint:(x,y,kind,scroll)=>window.ExcelLensEditReferenceView.dragPoint(x,y,kind,$('grid'),headerHeight(),ROW_HEAD,dy=>{
          const top=getVirtualScrollTop(); setVirtualScrollTop(top+dy); return getVirtualScrollTop()!==top;
        },scroll),
        referenceStep:(p,key)=>{
          const pinned=frozenPanes.effective();
          const delta={ArrowLeft:[0,-1],ArrowRight:[0,1],ArrowUp:[-1,0],ArrowDown:[1,0]}[key];
          const point=adjacentGridPoint(p.row,p.visual || S.selectedVisualIndex || p.row,p.col,...delta,pinned);
          scrollCellIntoView(point.row,point.col,point.visual,pinned);
          if (point.row) return point;
          const epoch=S.sheetEpoch, filter=S.filterId, view=S.rowView?.id;
          return loadViewport(true).then(()=>{
            if (epoch!==S.sheetEpoch || filter!==S.filterId || view!==S.rowView?.id || S.window?.excludeThroughRow!==pinned.rows) return null;
            const row=S.rowNumberByVisual.get(point.visual);
            return row ? {...point,row} : null;
          });
        },
        dragged:()=>suppressGridClick,
        selectPoint:(node,event)=>{
          const p=parseRef(node.dataset.ref);
          if (p) selectWithModifiers(cellAt(p.row,p.col),{...p,visual:Number(node.dataset.visualIndex)||p.row},event);
        },
        range:()=>{
          if (!S.selected || S.multiRanges.length || S.filtered) return '';
          const model=window.ExcelLensMultiSelection, area=mergedCells.expand(model.areas(S,true).at(-1));
          const {start,end}=model.points(area,colName);
          return start===end?start:start+':'+end;
        },
        merged:()=>S.selected&&mergedCells.at(S.selected.row,S.selected.col),
        areas:()=>window.ExcelLensMultiSelection.areas(S,true),
        expand:area=>mergedCells.expand(area), mergeAt:(row,col)=>mergedCells.at(row,col),
        nextRow:nextVisibleRow, previousRow:previousVisibleRow,
        nextCol:nextVisibleColumn, previousCol:previousVisibleColumn,
        moveInside:async(row,col)=>{
          const pinned=frozenPanes.effective(), current=beginGridNavigation(pinned.rows), range=S.range;
          scrollCellIntoView(row,col,row,pinned);
          if (!S.window || !windowContains(getViewportRequest())) await loadViewport(true);
          if (current() && range===S.range) selectCell(cellAt(row,col),row,col,colName(col)+row,row,true,true);
        },
        switchSheet:selectSheetTab,
        pasteRows:async count=>{
          const snapshot=captureClipboardSelection({purpose:'粘贴',limits:{rows:fileFormats.editing.batchCells,columns:fileFormats.editing.batchCells,cells:fileFormats.editing.batchCells}});
          if(snapshot.rows>1){
            const data=await snapshot.read({richText:false,conditional:false});
            if(!snapshot.current())throw Error('选区已变化，请重新粘贴');
            return data.rows.map(r=>r.row);
          }
          const through=frozenPanes.effective().rows,first=S.selected.row,rows=[];
          if(!S.filtered){
            const visibility=S.rowVisibility;
            for(let row=first;row&&rows.length<count;row=visibility.next(row+1))rows.push(row);
            return rows;
          }
          let offset=first<=through?0:(S.selectedVisualIndex||await locateFilteredVisual(first,through))-1;
          if(offset<0)throw Error('当前单元格已离开筛选结果');
          if(first<=through)for(let row=first;row&&row<=through&&rows.length<count;row=nextVisibleRow(row+1))rows.push(row);
          while(rows.length<count){
            if(!snapshot.current())throw Error('选区已变化，请重新粘贴');
            const size=Math.min(MAX_ROWS_PER_REQUEST,count-rows.length);
            const params=new URLSearchParams({sheet:S.sheet,filterId:S.filterId,excludeThroughRow:through,offset,limit:size,col:S.selected.col,cols:1,compact:1,rowWindow:1});
            if(S.rowView)params.set('viewId',S.rowView.id);
            const data=await api('/api/sheet?'+params);
            if(!snapshot.current())throw Error('选区已变化，请重新粘贴');
            rows.push(...data.rows.map(row=>row.number));
            if(data.rows.length<size)break;
            offset+=size;
          }
          return rows;
        },
        visibleSelection:()=>{
          const cells=fileFormats.editing.batchCells;
          return captureClipboardSelection({purpose:'编辑',limits:{rows:cells,columns:cells,cells}});
        },
        move:(row,col,extend)=>moveSelection(row,col,extend,true), select:selectCellByAddress, copy:copySelection,
        positionMenu:positionCellMenu,
        bounds:ref=>{
          for(const node of document.querySelectorAll(`.grid-region .grid-cell[data-ref="${ref}"]`)){
            const rect=traceVisibleRect(node,true); if(rect)return {rect,node};
          }
          return null;
        },
        redraw:()=>{
          ++S.decorVersion; renderGridFromCurrentWindow(); frozenPanes?.schedule(true);
          if(S.selected) selectCellByAddress(S.selected.ref,S.selectedVisualIndex,true);
        }
      },
      limits: fileFormats.editing, colName, fmtBytes});
  }
  // A bounded read-only bridge; the analysis workspace cannot mutate grid
  // state or capture unfinished controls as if they were successful results.
  window.ExcelLensAnalysisSourceContext=(mode)=>{
    if(!S.opened||!S.sheet||!S.indexReady||S.opening||S.sheetLoadingEpoch||S.filterJob||rowViews?.busy||S.rowViewFailure)throw Error('请先完成工作表加载、筛选或行显隐更新');
    if(mode==='filtered'&&(!S.filtered||!S.filterId))throw Error('当前工作表没有已完成的筛选或排序');
    if(mode==='visible'&&!S.rowView?.id)throw Error('当前可见行尚未准备好');
    const data={workbookKey:S.workbookKey,book:S.book,source:'wb:'+S.sheet,filterId:S.filtered?S.filterId:'',viewId:S.rowView?.id||'',excludeThroughRow:frozenPanes.effective().rows};
    if(mode==='selection'){
      const model=window.ExcelLensMultiSelection,areas=model.areas(S,true);
      if(areas.length!==1)throw Error('请在工作簿中保留一个连续选区');
      Object.assign(data,model.points(areas[0],colName));
    }
    return data;
  };
  if(window.ExcelLensRowViews)rowViews=new window.ExcelLensRowViews({state:S,api,toast,maximum:sourceRowLimit,commit:commitRowView,navigate:navigateToCell,reload:()=>selectSheet(S.sheet),openDialog});
  calculator=new window.ExcelLensCalculator({state:S,api,toast,grid:$('grid'),copy:copyText});
  selectionStats=new window.ExcelLensSelectionStats({state:S,api,colName,toast,profile:()=>profileCenter.open(),excludeThroughRow:()=>frozenPanes?.effective().rows||0,
    editBinding:()=>{
      if (!editCopy?.grid.active) return undefined;
      const {id,binding,revision}=editCopy.data.document;
      return {document:{id,binding},revision,book:S.book};
    }
  });
  multiSort=new window.ExcelLensMultiSort({state:S,parseColumn,colName,toast,openDialog,
    submit:async(keys,header)=>{
      const rules=currentQueryRules();
      await submitFilterRules(rules,{headerRow:header,sortKeys:keys,recoverable:$('sortRecoverable').checked});
    },
    clear:async()=>{
      if(S.autoFilter){
        S.autoFilter.sortColumn=0;S.autoFilter.descending=false;
        await applyAutoFilter({preserveRules:true});
      } else if((S.lastRulesBySheet.get(S.sheet)||[]).length){
        await submitFilterRules(S.lastRulesBySheet.get(S.sheet),{headerRow:0});
      }else if(S.filterId)await clearFilter();
    }
  });
  selectionInspector = new window.ExcelLensSelectionInspector({
    state:S,api,colName,parseColumn,toast,navigate:navigateToCell,copy:copyText,rowLimit:sourceRowLimit,largeDataRows:fileFormats.largeDataRows,excludeThroughRow:()=>frozenPanes.effective().rows,
    onChange:()=>{++S.decorVersion;renderGridFromCurrentWindow();
      for(const node of document.querySelectorAll('.grid-region .pinned-cell'))selectionInspector.decorateCell(node,node.dataset.ref,S.sheet);
    }
  });
  frozenPanes = new window.ExcelLensFrozenPanes({
    state:S, grid:$("grid"), get rowHead(){return ROW_HEAD;}, api,
    getRequest:getViewportRequest, rowHeight, colWidth, rowTop,colLeft,headerHeight,colName,bindResizeGrip,
    sourceRowTop,sourceRowHeight,
    getVirtualScrollTop,
    updateSharedBorders,
    isPrecedent, isSearchMatch, applyCellStyle, onRender:()=>{applySelectionClasses();traceUI?.refreshPulse();editCopy?.grid.schedulePosition();},
    decorateFormulaRef:(element,row,col,sheet)=>{formulaInspector?.decorateCell(element,row,col,sheet);selectionInspector?.decorateCell(element,element.dataset.ref,sheet);},
    beforePaint:rows=>frozenPresenter.prepare(rows),
    paintCell:frozenPresenter.paint.bind(frozenPresenter),extendTextIntoBlanks:frozenPresenter.overflow.bind(frozenPresenter),
    projectCell:(ref,cell)=>editCopy?.grid.active ? editCopy.grid.project(S.sheet,ref,cell) : cell,
    cachedPart:(name,params,base)=>{
      if(name!=='left'&&base.filtered)return null;
      const first=Number(params.get('col')),count=Number(params.get('cols'));
      const columns=params.get('columns')?.split(',').map(Number)||Array.from({length:count},(_,i)=>first+i);
      const visuals=name==='left'?Array.from({length:base.rowCount?base.limit:0},(_,i)=>base.rowOrder?.at(base.r1+i-1)||base.r1+i):
        Array.from({length:Number(params.get('limit'))},(_,i)=>i+1).filter(rowVisible);
      const band={...base,displayStart:visuals[0]||1,displayEnd:visuals.at(-1)||0,startCol:first,cols:count,selectedCols:columns};
      return tiles.project(band,visuals)?.data;
    },
    estimateBytes:estimateViewportBytes,cacheBudget:VIEWPORT_CACHE_BUDGET/4,
    decorateMergedCell,
    decorateColumnHeader:decorateTableColumnHeader,
    isBookmarked:(sheet,ref)=>Boolean(bookmarkCenter?.has(sheet,ref)),
    onError:(error)=>toast(`冻结窗格读取失败：${error.message}`,'error')
  });
  if(window.ExcelLensPictures){
    pictures=new window.ExcelLensPictures({state:S,grid:$("grid"),api,toast,
      get rowHead(){return ROW_HEAD;},scale,rowTop,colLeft,headerHeight,
      frozen:()=>frozenPanes?.effective()||{rows:0,cols:0}});
  }
  formulaInspector = new window.ExcelLensFormulaInspector.FormulaInspector({
    element:$("formulaText"), tooltip:$("formulaRefTooltip"), grid:$("grid"), state:S, api,
    getCell, navigate:navigateToCell, notify:toast, copy:copyText,
    onLayoutChange:()=>{if(S.indexReady){scheduleViewport(true);frozenPanes.schedule(true);}},
    sheetExists:sheet=>S.sheets.some(s=>s.name.toLocaleLowerCase()===sheet.toLocaleLowerCase())
  });
  traceUI=new window.ExcelLensTracing({state:S,api,panel:$('traceDialog'),dock:$('traceDock'),layer:$('traceArrival'),grid:$('grid'),
    title:$('traceTitle'),formula:$('traceFormula'),summary:$('traceSummary'),list:$('traceList'),scopeRow:$('traceScopeRow'),scope:$('traceScope'),
    cancel:$('traceCancelBtn'),minimize:$('traceMinimizeBtn'),restore:$('traceRestoreBtn'),origin:$('traceOriginBtn'),end:$('traceEndBtn'),
    parse:parseFormulaReferences,parseRef,getCell,notify:toast,readScope:()=>$('searchScope').value,writeScope:value=>{$('searchScope').value=value;},
    navigate:(sheet,ref,guard)=>navigateToCell(sheet,ref,true,guard),
    atTarget:ref=>{const p=activeMerge(ref.start.row,ref.start.col)||ref.start;return S.indexReady&&S.selected?.sheet===ref.sheet&&S.selected.row===p.row&&S.selected.col===p.col;},
    bounds:(row,col)=>activeMerge(row,col)||{row,col,endRow:row,endCol:col},visibleRect:traceVisibleRect,
    setRefs:refs=>{S.traceRefs=refs;++S.decorVersion;for(const node of document.querySelectorAll('.grid-region .grid-cell[data-row][data-col]'))node.classList.toggle('precedent',isPrecedent(Number(node.dataset.row),Number(node.dataset.col)));}
  });
  archiveCenter = new window.ExcelLensArchives({api,toast,open:acceptOpenedWorkbook,fmtBytes,canOpen:canOpenWorkbook});
  autoFilterUI = new window.ExcelLensAutoFilterDropdown({state:S,api,colName,toast,onApply:applyAutoFilter});
  exportCenter = new window.ExcelLensExportCenter({state:S,api,toast,colName,openDialog,excludeThroughRow:()=>frozenPanes.effective().rows,sourceCapabilities:()=>fileFormats.source(S.source?.format)});
  taskRecovery=new window.ExcelLensTaskRecovery({state:S,api,toast,openDialog,maximum:fileFormats.taskRecovery?.tasks,openSource:openTaskSource,adopt:adoptRecoveredTask,
    context:()=>[S.workbookKey,S.book,S.sheetEpoch,S.searchRequestID,S.filterRequest,exportCenter.serial].join('|'),
    canResume:(item,action)=>{if(action==='open')return !S.opening&&canOpenWorkbook();if(S.opening||S.sheetLoadingEpoch){toast('请先完成文件加载','warning');return false;}if(item.kind==='export'?(exportCenter.running||exportCenter.busy):!!(S.searchJob||S.filterJob)){toast('请先完成或取消当前任务','warning');return false;}return true;}});
  $('taskRecoveryHome').addEventListener('click',()=>taskRecovery.open());
  $('recentRefreshBtn').addEventListener('click',()=>taskRecovery.refreshHome());
  window.addEventListener('focus',()=>taskRecovery.refreshHome());
  copySpecial = new window.ExcelLensCopySpecial({capture:()=>captureClipboardSelection({withMerges:true}),copy:content=>content.html?writeClipboardPayload(content):copyText(content.text),toast,limits:fileFormats.clipboard});
  transposeCenter=new window.ExcelLensTranspose({capture:captureClipboardSelection,copy:copyText,toast,navigate:navigateToCell,colName,fontStack,limits:fileFormats.transpose,richLimits:fileFormats.richText});
  window.ExcelLensExportIssues=(jobId,protectCSV)=>exportCenter.startGenerated('/api/analysis/issues/export',{jobId,protectCSV},'异常核对清单（完整）');
  window.ExcelLensExportClusters=(jobId,protectCSV)=>exportCenter.startGenerated('/api/analysis/clusters/export',{jobId,protectCSV},'疑似重复候选（完整计数、有限位置样例）');
  window.ExcelLensAuditExport=(path,payload,label)=>exportCenter.startGenerated(path,payload,label);
  window.ExcelLensExportReconcile=(jobId,protectCSV,label,options={})=>exportCenter.startGenerated('/api/reconcile/export',{jobId,protectCSV,...options},label);
  if(window.ExcelLensReconcile)reconcileCenter=new window.ExcelLensReconcile({state:S,api,toast,colName,navigate:navigateToCell,canRun:canOpenWorkbook,recordBytes:fileFormats.reconcileRecordBytes});
  if(window.ExcelLensRelations){relationsCenter=new window.ExcelLensRelations({api,toast,formats:fileFormats,fieldLimits:fileFormats.composition,navigate:navigateToCell,canRun:canOpenWorkbook,context:()=>({bookKey:S.workbookKey,book:S.book,opened:S.opened,source:'wb:'+S.sheet}),onExport:(payload,name)=>exportCenter.startGenerated('/api/relation/export',payload,name)});window.ExcelLensRelationsUI=relationsCenter;}
  if(window.ExcelLensNamedViews)namedViews=new window.ExcelLensNamedViews({state:S,api,toast,capture:captureNamedView,apply:applyNamedView,supportsRowVisibility:()=>!!rowViews,canApply:()=>{if(S.filterJob||S.opening||S.sheetLoadingEpoch||rowViews?.busy){toast('请先完成当前加载、筛选或行显隐更新','warning');return false;}return true;}});
	if(window.ExcelLensCatalogue)catalogueCenter=new window.ExcelLensCatalogue({state:S,api,toast,limits:fileFormats.catalogue,supported:()=>!!fileFormats.source(S.source?.format).catalogueMetadata,navigate:navigateCatalogueArea,canNavigate:()=>!!S.opened&&!S.opening&&!S.sheetLoadingEpoch&&!S.filterJob&&!rowViews?.busy});
	if(window.ExcelLensDataRules)dataRulesCenter=new window.ExcelLensDataRules({state:S,api,toast,limits:fileFormats.dataRules,supported:()=>!!fileFormats.source(S.source?.format).dataRuleMetadata,navigate:navigateCatalogueArea,canNavigate:()=>!!S.opened&&!S.opening&&!S.sheetLoadingEpoch&&!S.filterJob&&!rowViews?.busy});
	if(window.ExcelLensPrinting)printingCenter=new window.ExcelLensPrinting({state:S,api,toast,source:()=>fileFormats.source(S.source?.format),limits:fileFormats.printing,richLimits:fileFormats.richText,fontStack});
	if(window.ExcelLensRecordCard)recordCardCenter=new window.ExcelLensRecordCard({state:S,api,toast,copy:copyText,colName,parseColumn,limits:fileFormats.record,source:()=>fileFormats.source(S.source?.format),pinnedThroughRow:()=>frozenPanes.effective().rows,canRead:()=>!S.filterJob&&!rowViews?.busy,navigate:(sheet,ref,guard)=>navigateToCell(sheet,ref,false,guard)});
  profileCenter = new window.ExcelLensProfileCenter({state:S,api,toast,openDialog,navigateToCell,colName,limits:fileFormats.profile,source:()=>fileFormats.source(S.source?.format),canRead:()=>!S.filterJob&&!rowViews?.busy,excludeThroughRow:()=>frozenPanes.effective().rows});
	selectionQuick=new window.ExcelLensSelectionQuick({state:S,api,toast,colName,limits:fileFormats.selectionQuick,pinnedThroughRow:()=>frozenPanes.effective().rows,canRead:()=>!S.filterJob&&!rowViews?.busy,navigate:(sheet,ref,guard)=>navigateToCell(sheet,ref,false,guard)});
	cellExplanation=new window.ExcelLensCellExplanation({state:S,api,toast,colName,parseRef,limits:fileFormats.cellExplanation,source:()=>fileFormats.source(S.source?.format),pinnedThroughRow:()=>frozenPanes.effective().rows,canRead:()=>!S.filterJob&&!rowViews?.busy,notice:cell=>[...document.querySelectorAll('.grid-region .grid-cell.rich-text-notice,.grid-region .grid-cell.cell-presentation-notice')].filter(node=>node.dataset.ref===cell.ref).map(node=>node._xlRichNotice||node._xlPresentationNotice).filter(Boolean).join('；')||window.ExcelLensRichText.inspect(cell,fileFormats.richText)?.notice||''});
  bookmarkCenter = new window.ExcelLensBookmarks({state:S,api,toast,openDialog,navigateToCell,canNotes:()=>fileFormats.source(S.source?.format).noteMetadata,applyNotes:async(book,notes)=>{
    if(!editCopy||editCopy.pending||editCopy.abort||editCopy.grid.blocked)throw Error('请先完成当前编辑操作');
    if(!editCopy.grid.active)await editCopy.show();
    if(!editCopy.grid.active||S.book!==book||editCopy.data.book!==book)throw Error('编辑来源已变化，请重新选择标记');
    const ok=await editCopy.commit('structure',[],{direct:true,structure:{kind:'reviewNotes',sheet:notes[0].sheet,notes}});
    if(!ok)throw Error(document.getElementById('editCopyStatus')?.textContent||'批注写入失败');
    toast('批注已写入当前修订，可撤销或生成副本','success');
  },onChange:()=>{
    ++S.decorVersion; renderGridFromCurrentWindow(); frozenPanes.schedule(true);
  }});
  // Contains counters only, never spreadsheet content. Used by automated QA.
  window.ExcelLensPerformance = () => ({
    ...S.renderMetrics,
    currentRows:S.renderedRows.size,
    visibleNodes:$("rowsLayer").querySelectorAll(".grid-cell").length,
    bufferedTiles:tiles.size,
    bufferedApproxBytes:tiles.bytes,
    cachedSheetViews:S.sheetViews.size,
    cachedSheetBytes:S.sheetViewsBytes,
    sheetLoading:Boolean(S.sheetLoadingEpoch),
    hasPendingRequest:scheduler.active.size>0,
    foregroundHits,foregroundLookups,requests:{...scheduler.stats},
    hasVisibleCoverage:S.indexReady && viewportCoversVisible(S.window,getViewportRequest())
  });
  window.addEventListener('excellens:analysis-navigate', event => {
    const detail=event.detail||{};
    if(typeof detail.sheet==='string'&&typeof detail.ref==='string'){
      navigateToCell(detail.sheet,detail.ref).catch(err=>toast(err.message,'error'));
    }
  });
  bindEvents();
  window.ExcelLensWorkbookReady=loadStatus();
})();
