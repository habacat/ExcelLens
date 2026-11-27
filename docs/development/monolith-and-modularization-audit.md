# 前后端超长文件与模块化审阅清单

本清单基于维护树（基线 `7890125` 之上已完成 2026-11-26 架构审阅收口）的静态扫描与代码结构人工复核整理，并同步记录**第一阶段务实模块化重构的实际落盘结果**（原则：按高内聚业务边界拆分真正混杂的巨石文件、顺手治理工程坏味道，不为降行数盲目制造碎片）。

## 盘点口径与判定标准

- **统计范围**：仅包含生产代码（`cmd/`、`internal/` 下的 `.go`、`.js`、`.css`、`.html`），排除 `*_test.go`、`*.test.cjs`、`tools/`、`vendor/`、`artifacts/`、`validation/`、`fortest/`、`release/`。
- **综合判定维度**：不只按行数一刀切，同时综合以下四项指标判断是否存在维护风险：
  1. **代码体量与密度**：前端部分文件存在“单行多语句”的紧凑写法，行数看似可控但字节密度高达 `90–162 B/行`，实际阅读与 Diff 成本远超行数表面值。
  2. **职责混杂度**：单个文件是否同时承担解析、状态机、UI 渲染、持久化、事件绑定等彼此正交的职责。
  3. **巨型单函数**：单函数超过 220 行通常意味着深层嵌套、多分支状态耦合，难以单测和定位回归。
  4. **包/结构体级膨胀与测试耦合**：不仅看单文件，也关注上帝结构体（God Struct）和阻滞重构的测试夹具形态。

---

## 一、前端巨石文件与高密度模块

> **2026-11-27 全面 TypeScript 迁移完成**：全部旧版 `internal/ui/web/*.js`（125 文件）及 `*.test.cjs`（84 文件）已一次性迁移至严格类型检查的 `internal/ui/src/`（139 个生产 TS 模块 + 94 个直接导入 TS 测试文件），并按五层单向 DAG（`contracts -> core -> grid -> {editing, features, analysis} -> app`）彻底拆解 `app.js` 与 `analytics.js` 巨石文件、消除内部 `window.ExcelLens*` 全局命名空间与源码切片测试，经由 `esbuild` 确定性打包为 `internal/ui/web/dist/{app,storage-recovery}.js`。详见 [前端 TypeScript 架构与工程规范](frontend-typescript.md)。

### P0 — 原三大单体的演进与 TypeScript 彻底拆解结果

| 原文件 | 拆分前 | TypeScript 迁移后 | 状态与实际拆解结果 |
| --- | --- | --- | --- |
| `internal/ui/web/app.js` | **4,947 行** / 278.3 KB（含 585 行 `bindEvents`） | [`app/main.ts`](../../internal/ui/src/app/main.ts) + [`grid/state.ts`](../../internal/ui/src/grid/state.ts) + [`grid/viewport-pipeline.ts`](../../internal/ui/src/grid/viewport-pipeline.ts) + [`grid/sheet-controller.ts`](../../internal/ui/src/grid/sheet-controller.ts) + [`grid/navigation.ts`](../../internal/ui/src/grid/navigation.ts) + [`grid/grid-clipboard.ts`](../../internal/ui/src/grid/grid-clipboard.ts) / [`grid-context-menu.ts`](../../internal/ui/src/grid/grid-context-menu.ts) / [`grid-workbook-io.ts`](../../internal/ui/src/grid/grid-workbook-io.ts) / [`grid-interactions.ts`](../../internal/ui/src/grid/grid-interactions.ts) | **已完成全面模块化拆解**：组合根、工作簿状态、视口流水线、工作表/筛选控制器、导航、剪贴板、上下文菜单、工作簿 IO、网格交互均独立为严格类型模块；旧版 15 个源码切片测试全部升级为直接导入 TS 模块的单元测试。 |
| [`internal/ui/web/index.html`](../../internal/ui/web/index.html) | **1,533 行** / 163.6 KB（125 个串行 `<script>` 标签，3 个弹窗夹在脚本段中间） | **1,424 行**（单 `<script src="/assets/app.js">` 入口） | **已完成结构收口与守门**：所有 `<dialog>` 位于业务脚本之前，125 个手排 `<script>` 标签收敛为单一版本化包入口；[`internal/ui/assets_test.go`](../../internal/ui/assets_test.go) 校验单一哈希脚本入口与 `<dialog>` 次序。 |
| `internal/ui/web/analytics.js` | **1,249 行** / 110.4 KB（90 B/行） | [`analysis/workspace.ts`](../../internal/ui/src/analysis/workspace.ts) + [`analysis/pivot-view.ts`](../../internal/ui/src/analysis/pivot-view.ts) + [`analysis/dashboard.ts`](../../internal/ui/src/analysis/dashboard.ts) + [`analysis/sheets.ts`](../../internal/ui/src/analysis/sheets.ts) | **已完成四域拆解**：分析工作区编排、透视与图表/下钻视图、仪表盘与模板引擎、可编辑分析表独立成模块，旧版字符串切片测试全部替换为直接导入 TS 单测（如 [`drafts.test.ts`](../../internal/ui/src/analysis/drafts.test.ts)、[`dashboard.test.ts`](../../internal/ui/src/analysis/dashboard.test.ts)）。 |

### P1 — 暂缓/按需演进的前端模块

| 文件 | 规模 / 位置 | 处置策略 |
| --- | --- | --- |
| [`internal/ui/web/style.css`](../../internal/ui/web/style.css) | 812 行 / 61.0 KB | **明确暂不拆分**：被多个单测与 QA 夹具直接引用，盲拆收益低，保持现状。 |
| [`internal/ui/src/editing/edit-copy.ts`](../../internal/ui/src/editing/edit-copy.ts) | 编辑副本控制器 | **P1 后续演进**：已迁至严格 TypeScript，待后续改动编辑副本链路时再按会话/提交分层拆分。 |
| [`internal/ui/src/features/search-panel.ts`](../../internal/ui/src/features/search-panel.ts) | 高级搜索面板 | **P1 后续演进**：已迁至严格 TypeScript，DSL 面板构建器与结果分页可在后续改版时拆离。 |
| [`internal/ui/src/editing/edit-grid.ts`](../../internal/ui/src/editing/edit-grid.ts) | 编辑网格内联浮层 | **P1 后续演进**：已迁至严格 TypeScript，内联编辑器浮层与修订投影逻辑后续按需拆分。 |
| [`internal/ui/web/dialogs.css`](../../internal/ui/web/dialogs.css) | 401 行 / 35.0 KB | **规则收敛**：不再往本文件堆叠新弹窗样式，后续新增弹窗延续 `online-database.css` 的独立文件约定。 |
| [`reconcile.ts`](../../internal/ui/src/features/reconcile.ts) / [`relations.ts`](../../internal/ui/src/features/relations.ts) / [`calculator.ts`](../../internal/ui/src/features/calculator.ts) / [`printing.ts`](../../internal/ui/src/features/printing.ts) | 已恢复标准多行 TypeScript 排版 | **高密度排版已治理**：迁移 TypeScript 时已全部恢复清晰的多行结构与严格类型签名。 |

---

## 二、后端聚合体与超长 Go 文件

### 1. 结构体级与包级聚合

- **`server.App` 上帝结构体初步收敛（已完成首轮收口）**
  - 新增 [`internal/server/controllers.go`](../../internal/server/controllers.go)（127 行）及专项单测 [`internal/server/controllers_test.go`](../../internal/server/controllers_test.go)，将 [`internal/server/app.go`](../../internal/server/app.go)（现 **305 行**）中原先平铺的 6 组子域状态与锁收口为内嵌子控制器：`archiveController`、`sourcePinController`、`reconcileController`、`recipeController`、`analysisController`（封装 `cancelAll()`）和 `metadataJobs`（封装 `cancelAllLocked()`）。
  - 同步消除了 `App.Shutdown()` 中 26 行重复的任务 map 取消循环。
  - **后续演进**：在类型层面进一步区分进程级 `RootServer` 与单标签 `TabSession` 留待独立会话架构迭代，不在本轮做高风险大改。
- **大包平铺（`internal/workbook`、`internal/editfile`、`internal/server`）**
  - 本轮遵守 [`tools/project/architecture.go`](../../tools/project/architecture.go) 既有依赖图，采用**包内按高内聚文件拆解**，零额外跨包循环或导出面膨胀；后续若需跨包拆分，须先在 `architecture.go` 登记依赖规则。

### 2. 职责混杂的后端超大文件拆解进展

本轮已完成全部 **10 个 P1 后端超长文件** 及 **1 个高收益 P2 文件（`searchdsl/query.go`）** 的模块化拆分，并同步消除了多处重复逻辑与无效分配：

| 原优先级 | 原文件（拆分前） | 拆分后文件与规模 | 状态与顺带工程优化 |
| --- | --- | --- | --- |
| **P1** | [`internal/workbook/xls_biff.go`](../../internal/workbook/xls_biff.go)（**1,101 行**） | [`xls_biff.go`](../../internal/workbook/xls_biff.go)（275 行）、[`xls_biff_styles.go`](../../internal/workbook/xls_biff_styles.go)（328 行）、[`xls_biff_rows.go`](../../internal/workbook/xls_biff_rows.go)（276 行）、[`xls_biff_formula.go`](../../internal/workbook/xls_biff_formula.go)（255 行） | **已完成**。按记录流/SST、全局表与样式、行流解码、公式反编译四层分离；顺手清理 `xlsSegments.nextBytes` 长期未使用的 `continueFlag` 参数。 |
| **P1** | [`internal/workbook/format.go`](../../internal/styles/format.go)（原 **877 行**） | [`cell_ref.go`](../../internal/workbook/cell_ref.go)（82 行）、[`internal/styles/format.go`](../../internal/styles/format.go)、[`internal/styles/format_date.go`](../../internal/styles/format_date.go) | **已完成并升格入 `internal/styles`**。A1 坐标解析留驻 `workbook`，数字/会计/定点十进制/Excel 日期格式化与样式表统一收口至 `internal/styles`；将 `formatHasToken` 收敛为复用 `firstFormatToken >= 0`，消除 20+ 行重复扫描逻辑。 |
| **P1** | [`internal/calculator/formula.go`](../../internal/calculator/formula.go)（**832 行**） | [`formula_value.go`](../../internal/calculator/formula_value.go)（173 行）、[`formula.go`](../../internal/calculator/formula.go)（456 行）、[`formula_eval.go`](../../internal/calculator/formula_eval.go)（246 行） | **已完成**。值模型、Pratt 语法解析、AST 求值分离；将 `compareFormulaValues` 中每次调用重建 map 的排序改为零分配 `formulaKindRank` switch。 |
| **P1** | [`internal/editfile/xlsx_package.go`](../../internal/editfile/xlsx_package.go)（**800 行**） | [`xlsx_package.go`](../../internal/editfile/xlsx_package.go)（561 行）、[`xlsx_layout.go`](../../internal/editfile/xlsx_layout.go)（250 行） | **已完成**。ZIP/XML 包读取与 `XLSXLayout` 预检/网格布局升级分离。 |
| **P1** | [`internal/editfile/xlsx_write.go`](../../internal/editfile/xlsx_write.go)（**761 行**） | [`xlsx_write.go`](../../internal/editfile/xlsx_write.go)（597 行）、[`xlsx_verify.go`](../../internal/editfile/xlsx_verify.go)（177 行） | **已完成**。写出补丁流水线与生成后回读核验（`VerifyXLSX`）分离。 |
| **P1** | [`internal/editfile/xlsx_sheet.go`](../../internal/editfile/xlsx_sheet.go)（**709 行**） | [`xlsx_sheet.go`](../../internal/editfile/xlsx_sheet.go)（529 行）、[`xlsx_values.go`](../../internal/editfile/xlsx_values.go)（190 行） | **已完成**。工作表 XML 扫描与单元格值/公式合法性校验分离。 |
| **P1** | [`internal/recovery/store.go`](../../internal/recovery/store.go)（**718 行**） | [`codec.go`](../../internal/recovery/codec.go)（139 行）、[`journal.go`](../../internal/recovery/journal.go)（222 行）、[`store.go`](../../internal/recovery/store.go)（397 行） | **已完成**。JSON 编解码/摘要校验、WAL 日志帧读写与尾部修复、内存配额存储三层解耦。 |
| **P1** | [`internal/archives/archive.go`](../../internal/archives/archive.go)（**707 行**） | [`archive.go`](../../internal/archives/archive.go)（348 行）、[`extract.go`](../../internal/archives/extract.go)（382 行） | **已完成**。多压缩格式浏览与内容寻址缓存提取/SQLite WAL 伴随提取分离；提取 `cacheMemberTarget` 与 `reuseCachedMember` 消除缓存命中路径重复校验。 |
| **P1** | [`internal/reconcile/relation.go`](../../internal/reconcile/relation.go)（**690 行**） | [`relation.go`](../../internal/reconcile/relation.go)（332 行）、[`relation_stream.go`](../../internal/reconcile/relation_stream.go)（368 行） | **已完成**。关系声明/校验与外排归并流式输出分离。 |
| **P1** | [`internal/workbook/external_text.go`](../../internal/workbook/external_text.go)（**675 行**） | [`external_text.go`](../../internal/workbook/external_text.go)（301 行）、[`external_text_encoding.go`](../../internal/workbook/external_text_encoding.go)（387 行） | **已完成**。文本工作簿行流与编码嗅探/UTF-16/GB18030 转码/分隔定宽探测分离。 |
| **P2** | [`internal/searchdsl/query.go`](../../internal/searchdsl/query.go)（**661 行**） | [`lex.go`](../../internal/searchdsl/lex.go)（118 行）、[`parse.go`](../../internal/searchdsl/parse.go)（331 行）、[`query.go`](../../internal/searchdsl/query.go)（236 行） | **已完成**。词法分析、语法与 Schema 解析、AST 求值三层清晰拆分；提取 `evalOrderCmp` 消除数字/日期/字符串比较分支重复。 |
| **P2** | `internal/workbook/conditional_expr_funcs.go` / [`conditional_formula.go`](../../internal/workbook/conditional_formula.go)（原 979 / 963 行） | 迁入独立包 [`internal/conditional`](../../internal/conditional/funcs.go)（4,829 行，含求值、成本规划、滑动窗口、统计索引与容量管理） | **已完成包级抽离**，共享十进制类型与解析下沉至 [`internal/excelformula/decimal.go`](../../internal/excelformula/decimal.go)。 |
| **P2** | [`internal/export/xlsx_style_xml.go`](../../internal/export/xlsx_style_xml.go) / [`xlsx_conditional_styles.go`](../../internal/export/xlsx_conditional_styles.go) / 原 `workbook/styles.go`（674 / 649 / 654 行） | `workbook` 样式与数字/日期格式整体抽离至 [`internal/styles`](../../internal/styles/styles.go)（3,105 行）；`export` 写出侧保持内聚 | **已完成 `styles` 包级收口**，未将颜色/字体/边框/数字格式拆碎为过多微包。 |

---

## 三、巨型单函数热点（≥ 240 行，后续按触达时机治理）

原前端最长单函数 `app.js:bindEvents`（585 行）已拆分为 [`grid-interactions.ts`](../../internal/ui/src/grid/grid-interactions.ts) 中的 4 个分组绑定方法，原 `app.js:gridSteps`（324 行）已随 TypeScript 迁移拆入 [`viewport-pipeline.ts`](../../internal/ui/src/grid/viewport-pipeline.ts) 与 [`sheet-controller.ts`](../../internal/ui/src/grid/sheet-controller.ts)。以下热点属于稳定的“大 `switch` 派发”或“单阶段状态机”，暂无缺陷驱动，保留作后续修改触及对应文件时的重构清单：

| 行数 | 位置 | 函数名 | 成因与后续建议 |
| --- | --- | --- | --- |
| **453** | [`internal/calculator/functions_arrays.go`](../../internal/calculator/functions_arrays.go) | `arrayFunction` | 所有数组函数共用巨型 `switch`，后续新增函数时改为按函数名独立注册。 |
| **370** | [`internal/calculator/functions_lookup.go`](../../internal/calculator/functions_lookup.go) | `lookupFunction` | 同上（`VLOOKUP`/`HLOOKUP`/`INDEX`/`MATCH`/`XLOOKUP` 共享大 `switch`）。 |
| **355** | [`internal/workbook/xls_html_document.go`](../../internal/workbook/xls_html_document.go) | `readStyledHTMLTables` | HTML 表格词法状态、合并占位与行发出内联在单循环中。 |
| **354** | [`internal/editfile/xlsx_sheet.go`](../../internal/editfile/xlsx_sheet.go) | `scanSheet` | XML 令牌循环内同时处理行列、样式继承、共享/数组公式和范围追踪。 |
| **325** | [`internal/printing/settings.go`](../../internal/printing/settings.go) | `resolve` | 打印区域、分页符、缩放与纸张默认值的多层覆盖规则。 |
| **322** | [`internal/workbook/scanner.go`](../../internal/workbook/scanner.go) | `scanWorksheetWithSharedHint` | 字节流扫描工作表行偏移、维度、合并与共享公式提示。 |
| **307** | [`internal/calculator/functions.go`](../../internal/calculator/functions.go) | `scalarFunction` | 标量函数大 `switch` 派发（同类还有 `functions_dates.go:dateFunction`、`functions_math.go:mathFunction`、`functions_text.go:textFunction`）。 |
| **294** | [`internal/editfile/structure_xml.go`](../../internal/editfile/structure_xml.go) | `editPart` | 行列增删改对公式/合并/表格/验证/筛选等 XML 区域的统一重写循环。 |
| **291** | [`internal/conditional/funcs.go`](../../internal/conditional/funcs.go) | `evalTextFunc` | 17 个条件格式文本函数集中在单个 `switch`（已随引擎迁入 `internal/conditional`）。 |
| **256** | [`internal/workbook/print_settings_xml.go`](../../internal/workbook/print_settings_xml.go) | `scanPrintSourceXML` | 打印元数据 XML 扫描器（同类还有 `data_rules_xml.go:scanDataRuleXML`）。 |
| **255** | [`internal/server/range.go`](../../internal/server/range.go) | `handleRange` | 范围查询多路径分派与响应编码。 |
| **249** | [`internal/workbook/search_fast.go`](../../internal/workbook/search_fast.go) | `searchSimpleResumable` | 断点恢复搜索主循环。 |
| **245** | [`internal/server/sheet_http.go`](../../internal/server/sheet_http.go) | `handleSheet` | 视口查询、行视图与条件样式线协议打包。 |
| **242** | [`internal/analysis/engine.go`](../../internal/analysis/engine.go) | `runLevel` | 透视单层分组、指标聚合与 Top-N 截断。 |

---

## 四、明确不建议盲目拆分的例外项

以下文件虽然行数在 550–680 行区间，但**边界清晰、职责单一**，本轮刻意保持原样，避免过度拆分造成碎片化：

- [`internal/workbook/xlsb_function_table.go`](../../internal/workbook/xlsb_function_table.go)（679 行 / 9.4 KB / 0 函数）：纯 MS-XLSB 规范函数 ID 到名称的静态查表数据。
- [`internal/rowindex/axis_visibility.go`](../../internal/rowindex/axis_visibility.go)（616 行，已随 `rowindex` 成包）、[`hyperlinks.go`](../../internal/workbook/hyperlinks.go)（610 行）、[`catalogue.go`](../../internal/workbook/catalogue.go)（602 行）、[`internal/checkpoint/store.go`](../../internal/checkpoint/store.go)（603 行）、[`internal/editdoc/document.go`](../../internal/editdoc/document.go)（630 行）：单一领域模型与算法实现，内聚度高。
- [`internal/ui/web/style.css`](../../internal/ui/web/style.css)（812 行）：被多个单测与 QA 夹具直接作为单文件内联读取。

---

## 五、第二阶段包级职责治理（2026-11-26）

围绕 `internal/workbook`、`internal/server`、`internal/editfile` 三个大型核心包，先按 Go AST 扫描实测内部符号依赖，再遵循“高内聚、低耦合、单向依赖、最小公开接口、不增加热路径转换/动态接口/额外锁”的原则完成以下包级抽取与审慎边界决策：

| 目标包 | 抽取/治理前 | 抽取/治理后 | 落盘结构与工程收益 |
| --- | --- | --- | --- |
| [`internal/workbook`](../../internal/workbook/workbook.go) | 39,397 行（1.18.1 起点） | **30,820 行**（净减 **8,577 行 / -21.8%**） | 抽离三个单向依赖、零拷贝内聚包（`workbook` 侧通过 Go 类型别名零开销桥接）：<br>1. [`internal/conditional`](../../internal/conditional/eval.go)（4,829 行，依赖 `formats`、`excelformula`）：条件表达式求值、成本规划、滑动窗口、统计磁盘索引与容量预算；共享十进制类型/解析下沉至 [`internal/excelformula/decimal.go`](../../internal/excelformula/decimal.go)。<br>2. [`internal/rowindex`](../../internal/rowindex/row_index.go)（3,611 行，依赖 `formats`、`sourceview`、`excelformula`）：`RowOffset`/`RowIndex`、OS `mmap`、进程临时查询文件配额、`RowOrder`/`RowSet`、`AxisVisibility`、`RowProjection`、`RowSorter` 外部归并排序与 `SortKeyStream`/`SortRationalKeyStream`。<br>3. [`internal/styles`](../../internal/styles/styles.go)（3,105 行，依赖 `formats`、`excelformula`）：将 `VisualStyle`、OOXML `styles.xml`/`theme1.xml` 解析、主题色与着色、`RichFont`/`DifferentialStyle` 合并与应用、数字/会计/定点十进制/Excel 日期格式化、区域短日期与系统字体度量统一收口于单一高内聚样式包，避免拆碎为过多微包。 |
| [`internal/server`](../../internal/server/app.go) | 31,972 行 | **32,276 行**（含新增后台条件统计能力）+ 新增 [`internal/taskmanager`](../../internal/taskmanager/lease.go)（455 行，纯 Go 标准库零内部依赖） | 抽离进程内并发与任务生命周期基元：`Admission` 并发槽池、`Lease[T]` 无锁原子引用计数快照租约（统一替换原分散在 `source_lease.go`、`composition_source.go`、`analysis_source_view.go`、`row_view.go` 四处的重复 CAS 租约实现）、`PinSet[K]` 资源固定集、`ByteBudget` 字节预算（消除 `composition_budget.go` 与 `relation_snapshot.go` 重复计费并移除共享 `sourceMu` 假共享）以及 `PruneFinished`/`PruneBudgeted` 确定性任务历史淘汰。 |
| [`internal/editfile`](../../internal/editfile/text.go) | 26,147 行（114 文件） | **26,147 行（审慎保持内聚，不强拆）** | Go AST 符号级耦合矩阵显示：`xlsx`（6,692 行）、`pivot`（6,804 行）、`table`（1,932 行）、`structure`（1,299 行）、`recalculation`（1,289 行）、`sheet`（1,142 行）等核心子域围绕同一 OOXML 修订事务存在双向依赖（如 `pivot` 出边指向 `xlsx(31)/recalculation(8)/entry_format(6)/array(4)/chart(3)`，同时被 `xlsx/chart/array/table/range/transfer` 反向引用）。现阶段强拆会迫使引入大量动态接口、数据拷贝或 `editcommon` 杂烩包，且让单次编辑改动波及 4–5 个包；因此明确保留整体内聚，待未来沉淀稳定的不可变 OOXML 部件游标与修订叠加层后再评估外迁。 |

## 六、后续可选演进方向

前两阶段的高收益前后端拆分与包级治理已全部落盘。后续仅在业务触及对应模块或启动专项重构时再按需推进：

1. **前端持续去切片化**：继续将 `app.js` 其余存量切片测试逐步迁向独立纯模块，再考虑分离虚拟网格渲染流水线（`gridSteps`）与非首屏 `<dialog>` 模板。
2. **`workbook` 剩余格式适配器边界**：当前 `xlsb`（4,047 行）、`xls`（2,849 行）、`ods`（799 行）、`external`（1,669 行）直接操作 `Workbook`/`SheetIndex`/`CellData`/`ELNROW02`；后续若要拆出独立 `formats/xlsx|xlsb|xls` 解析包，应先设计零拷贝 `cellmodel` 与 `ELNROW02` 编解码叶包，避免在数据读取热路径引入结构体复制或虚函数分发。
3. **后端会话模型与公式注册表**：将 `server.App` 在类型层进一步区分 `RootServer` 与 `TabSession`；按需将 `calculator/functions_*.go` 巨型 `switch` 改为直绑注册表。
