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

### P0 — 三大单体的第一阶段治理进展

| 文件 | 拆分前 | 拆分后 | 状态与实际拆解结果 |
| --- | --- | --- | --- |
| [`internal/ui/web/app.js`](../../internal/ui/web/app.js) | **4,947 行** / 278.3 KB（含 585 行 `bindEvents`） | **3,802 行** / 219.2 KB（净减 **1,145 行**） | **已完成首批 4 个正交控制器抽出**：[`grid-clipboard.js`](../../internal/ui/web/grid-clipboard.js)（317 行，剪贴板/取证/限定地址）、[`grid-context-menu.js`](../../internal/ui/web/grid-context-menu.js)（281 行，弹窗/提示/缩放/全屏/右键菜单）、[`grid-workbook-io.js`](../../internal/ui/web/grid-workbook-io.js)（376 行，工作簿打开/上传/取消与工作表标签选择器）、[`grid-interactions.js`](../../internal/ui/web/grid-interactions.js)（893 行，选区拖拽、键盘导航、`Alt` 功能区键序，并将原 585 行 `bindEvents` 分解为 4 个职责清晰的分组绑定方法）。`app.js` 保留为组合根与虚拟滚动内核（`gridSteps` 及 15 个既有切片测试锚点完好保留，后续按需再拆）。 |
| [`internal/ui/web/index.html`](../../internal/ui/web/index.html) | **1,533 行** / 163.6 KB（3 个数据库弹窗夹在 `<script>` 段中间） | **1,539 行** / 163.9 KB | **已修复 DOM 结构腐化并加测试守门**：将 `#databaseDialog`、`#databaseSnapshotsDialog`、`#dbSnapshotEvidence` 移回顶部 `<dialog>` 区域，按依赖顺序注册新增的 6 个控制器脚本；在 [`internal/ui/assets_test.go`](../../internal/ui/assets_test.go) 新增 `TestExtractedModulesLoadBeforeBootstrapAndDialogsPrecedeScripts` 强制约束所有 `<dialog>` 位于业务脚本之前且加载次序正确。非首屏弹窗按需挂载留作后续演进项。 |
| [`internal/ui/web/analytics.js`](../../internal/ui/web/analytics.js) | **1,249 行** / 110.4 KB（90 B/行） | **837 行** / 81.4 KB（净减 **412 行**） | **已完成核心子域三拆**：抽出 [`analytics-dashboard.js`](../../internal/ui/web/analytics-dashboard.js)（286 行，仪表盘卡片懒加载/拖拽重排与模板匹配引擎）和 [`analytics-sheets.js`](../../internal/ui/web/analytics-sheets.js)（739 行，可编辑工作表网格、串行保存队列、草稿重试、撤销重做栈与分页）；并将 [`analysis-drafts.test.cjs`](../../internal/ui/web/analysis-drafts.test.cjs) 从 4 处源码字符串切片升级为直接模块单测，新增 [`analytics-dashboard.test.cjs`](../../internal/ui/web/analytics-dashboard.test.cjs)。 |

> **测试夹具解耦进展**：已完成 [`analysis-drafts.test.cjs`](../../internal/ui/web/analysis-drafts.test.cjs) 与 [`notifications.test.cjs`](../../internal/ui/web/notifications.test.cjs) 的去切片化升级，新增 [`analytics-dashboard.test.cjs`](../../internal/ui/web/analytics-dashboard.test.cjs) 与 [`grid-controls.test.cjs`](../../internal/ui/web/grid-controls.test.cjs)，扩展 [`calculator.test.cjs`](../../internal/ui/web/calculator.test.cjs) 与 [`internal/server/server_test.go`](../../internal/server/server_test.go) 覆盖新增模块链路。其余 14 个依赖 `app.js`/`analytics.js` 内部辅助函数的存量切片测试全部保持稳定通过，后续再随对应子域迁移。

### P1 — 暂缓/不建议盲拆的前端模块

| 文件 | 规模 | 字节密度 | 处置策略 |
| --- | --- | --- | --- |
| [`internal/ui/web/style.css`](../../internal/ui/web/style.css) | 812 行 / 61.0 KB | 77 B/行 | **明确暂不拆分**：该单文件被 [`virtualscroll.test.cjs`](../../internal/ui/web/virtualscroll.test.cjs) 及 10+ 历史 `tools/qa/*.cjs` 夹具直接读取内联，盲拆收益低且测试破坏面广，保持现状。 |
| [`internal/ui/web/edit-copy.js`](../../internal/ui/web/edit-copy.js) | 656 行 / 36.7 KB | 57 B/行 | **P1 后续演进**：41 个方法集中在单一类（副本会话管理与单格/批量变更提交），待后续改动编辑副本链路时再分层拆分。 |
| [`internal/ui/web/search-panel.js`](../../internal/ui/web/search-panel.js) | 583 行 / 27.8 KB | 49 B/行 | **P1 后续演进**：高级 DSL 面板构建器与结果分页/导出可在后续搜索面板改版时拆离。 |
| [`internal/ui/web/edit-grid.js`](../../internal/ui/web/edit-grid.js) | 553 行 / 28.1 KB | 52 B/行 | **P1 后续演进**：内联编辑器浮层与修订投影逻辑后续按需拆分。 |
| [`internal/ui/web/dialogs.css`](../../internal/ui/web/dialogs.css) | 401 行 / 35.0 KB | 89 B/行 | **规则收敛**：不再往本文件堆叠新弹窗样式，后续新增弹窗延续 `online-database.css` 的独立文件约定。 |
| [`internal/ui/web/reconcile.js`](../../internal/ui/web/reconcile.js) / [`relations.js`](../../internal/ui/web/relations.js) / [`calculator.js`](../../internal/ui/web/calculator.js) / [`printing.js`](../../internal/ui/web/printing.js) / [`analytics.css`](../../internal/ui/web/analytics.css) | 173–390 行 / 25–35 KB | **92–162 B/行** | **高密度排版警示**：非逻辑过度膨胀，主要问题为单行多语句压缩排版；在后续触及对应功能时逐步恢复正常分行，不做纯格式化大规模无关 Diff。 |

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
| **P1** | [`internal/workbook/format.go`](../../internal/workbook/format.go)（**877 行**） | [`cell_ref.go`](../../internal/workbook/cell_ref.go)（82 行）、[`format.go`](../../internal/workbook/format.go)（479 行）、[`format_date.go`](../../internal/workbook/format_date.go)（300 行） | **已完成**。A1 坐标解析、数字/会计格式、Excel 1900/1904 日期格式化彻底解耦；将 `formatHasToken` 收敛为复用 `firstFormatToken >= 0`，消除 20+ 行重复扫描逻辑。 |
| **P1** | [`internal/calculator/formula.go`](../../internal/calculator/formula.go)（**832 行**） | [`formula_value.go`](../../internal/calculator/formula_value.go)（173 行）、[`formula.go`](../../internal/calculator/formula.go)（456 行）、[`formula_eval.go`](../../internal/calculator/formula_eval.go)（246 行） | **已完成**。值模型、Pratt 语法解析、AST 求值分离；将 `compareFormulaValues` 中每次调用重建 map 的排序改为零分配 `formulaKindRank` switch。 |
| **P1** | [`internal/editfile/xlsx_package.go`](../../internal/editfile/xlsx_package.go)（**800 行**） | [`xlsx_package.go`](../../internal/editfile/xlsx_package.go)（561 行）、[`xlsx_layout.go`](../../internal/editfile/xlsx_layout.go)（250 行） | **已完成**。ZIP/XML 包读取与 `XLSXLayout` 预检/网格布局升级分离。 |
| **P1** | [`internal/editfile/xlsx_write.go`](../../internal/editfile/xlsx_write.go)（**761 行**） | [`xlsx_write.go`](../../internal/editfile/xlsx_write.go)（597 行）、[`xlsx_verify.go`](../../internal/editfile/xlsx_verify.go)（177 行） | **已完成**。写出补丁流水线与生成后回读核验（`VerifyXLSX`）分离。 |
| **P1** | [`internal/editfile/xlsx_sheet.go`](../../internal/editfile/xlsx_sheet.go)（**709 行**） | [`xlsx_sheet.go`](../../internal/editfile/xlsx_sheet.go)（529 行）、[`xlsx_values.go`](../../internal/editfile/xlsx_values.go)（190 行） | **已完成**。工作表 XML 扫描与单元格值/公式合法性校验分离。 |
| **P1** | [`internal/recovery/store.go`](../../internal/recovery/store.go)（**718 行**） | [`codec.go`](../../internal/recovery/codec.go)（139 行）、[`journal.go`](../../internal/recovery/journal.go)（222 行）、[`store.go`](../../internal/recovery/store.go)（397 行） | **已完成**。JSON 编解码/摘要校验、WAL 日志帧读写与尾部修复、内存配额存储三层解耦。 |
| **P1** | [`internal/archives/archive.go`](../../internal/archives/archive.go)（**707 行**） | [`archive.go`](../../internal/archives/archive.go)（348 行）、[`extract.go`](../../internal/archives/extract.go)（382 行） | **已完成**。多压缩格式浏览与内容寻址缓存提取/SQLite WAL 伴随提取分离；提取 `cacheMemberTarget` 与 `reuseCachedMember` 消除缓存命中路径重复校验。 |
| **P1** | [`internal/reconcile/relation.go`](../../internal/reconcile/relation.go)（**690 行**） | [`relation.go`](../../internal/reconcile/relation.go)（332 行）、[`relation_stream.go`](../../internal/reconcile/relation_stream.go)（368 行） | **已完成**。关系声明/校验与外排归并流式输出分离。 |
| **P1** | [`internal/workbook/external_text.go`](../../internal/workbook/external_text.go)（**675 行**） | [`external_text.go`](../../internal/workbook/external_text.go)（301 行）、[`external_text_encoding.go`](../../internal/workbook/external_text_encoding.go)（387 行） | **已完成**。文本工作簿行流与编码嗅探/UTF-16/GB18030 转码/分隔定宽探测分离。 |
| **P2** | [`internal/searchdsl/query.go`](../../internal/searchdsl/query.go)（**661 行**） | [`lex.go`](../../internal/searchdsl/lex.go)（118 行）、[`parse.go`](../../internal/searchdsl/parse.go)（331 行）、[`query.go`](../../internal/searchdsl/query.go)（236 行） | **已完成**。词法分析、语法与 Schema 解析、AST 求值三层清晰拆分；提取 `evalOrderCmp` 消除数字/日期/字符串比较分支重复。 |
| **P2** | [`internal/workbook/conditional_expr_funcs.go`](../../internal/workbook/conditional_expr_funcs.go) / [`conditional_formula.go`](../../internal/workbook/conditional_formula.go)（979 / 963 行） | 保持现状 | **后续按需演进**：已在架构审阅中从 2,105 行单体初步拆开，待后续扩充新函数族时再按文本/日期细分。 |
| **P2** | [`internal/export/xlsx_style_xml.go`](../../internal/export/xlsx_style_xml.go) / [`xlsx_conditional_styles.go`](../../internal/export/xlsx_conditional_styles.go) / [`internal/workbook/styles.go`](../../internal/workbook/styles.go)（674 / 649 / 654 行） | 保持现状 | **后续按需演进**：样式 XML 解析与映射整体内聚，待修改样式引擎时再评估拆分。 |

---

## 三、巨型单函数热点（≥ 240 行，后续按触达时机治理）

原前端最长单函数 `app.js:bindEvents`（585 行）已在本轮拆分为 [`grid-interactions.js`](../../internal/ui/web/grid-interactions.js) 中的 4 个分组绑定方法。以下热点属于稳定的“大 `switch` 派发”或“单阶段状态机”，暂无缺陷驱动，保留作后续修改触及对应文件时的重构清单：

| 行数 | 位置 | 函数名 | 成因与后续建议 |
| --- | --- | --- | --- |
| **453** | [`internal/calculator/functions_arrays.go`](../../internal/calculator/functions_arrays.go) | `arrayFunction` | 所有数组函数共用巨型 `switch`，后续新增函数时改为按函数名独立注册。 |
| **370** | [`internal/calculator/functions_lookup.go`](../../internal/calculator/functions_lookup.go) | `lookupFunction` | 同上（`VLOOKUP`/`HLOOKUP`/`INDEX`/`MATCH`/`XLOOKUP` 共享大 `switch`）。 |
| **355** | [`internal/workbook/xls_html_document.go`](../../internal/workbook/xls_html_document.go) | `readStyledHTMLTables` | HTML 表格词法状态、合并占位与行发出内联在单循环中。 |
| **354** | [`internal/editfile/xlsx_sheet.go`](../../internal/editfile/xlsx_sheet.go) | `scanSheet` | XML 令牌循环内同时处理行列、样式继承、共享/数组公式和范围追踪。 |
| **325** | [`internal/printing/settings.go`](../../internal/printing/settings.go) | `resolve` | 打印区域、分页符、缩放与纸张默认值的多层覆盖规则。 |
| **324** | [`internal/ui/web/app.js`](../../internal/ui/web/app.js) | `gridSteps` | 虚拟网格核心协程，与 15 个切片单测强相关，需先迁移虚拟滚动测试再拆。 |
| **322** | [`internal/workbook/scanner.go`](../../internal/workbook/scanner.go) | `scanWorksheetWithSharedHint` | 字节流扫描工作表行偏移、维度、合并与共享公式提示。 |
| **307** | [`internal/calculator/functions.go`](../../internal/calculator/functions.go) | `scalarFunction` | 标量函数大 `switch` 派发（同类还有 `functions_dates.go:dateFunction`、`functions_math.go:mathFunction`、`functions_text.go:textFunction`）。 |
| **294** | [`internal/editfile/structure_xml.go`](../../internal/editfile/structure_xml.go) | `editPart` | 行列增删改对公式/合并/表格/验证/筛选等 XML 区域的统一重写循环。 |
| **291** | [`internal/workbook/conditional_expr_funcs.go`](../../internal/workbook/conditional_expr_funcs.go) | `evalTextFunc` | 17 个条件格式文本函数集中在单个 `switch`。 |
| **256** | [`internal/workbook/print_settings_xml.go`](../../internal/workbook/print_settings_xml.go) | `scanPrintSourceXML` | 打印元数据 XML 扫描器（同类还有 `data_rules_xml.go:scanDataRuleXML`）。 |
| **255** | [`internal/server/range.go`](../../internal/server/range.go) | `handleRange` | 范围查询多路径分派与响应编码。 |
| **249** | [`internal/workbook/search_fast.go`](../../internal/workbook/search_fast.go) | `searchSimpleResumable` | 断点恢复搜索主循环。 |
| **245** | [`internal/server/sheet_http.go`](../../internal/server/sheet_http.go) | `handleSheet` | 视口查询、行视图与条件样式线协议打包。 |
| **242** | [`internal/analysis/engine.go`](../../internal/analysis/engine.go) | `runLevel` | 透视单层分组、指标聚合与 Top-N 截断。 |

---

## 四、明确不建议盲目拆分的例外项

以下文件虽然行数在 550–680 行区间，但**边界清晰、职责单一**，本轮刻意保持原样，避免过度拆分造成碎片化：

- [`internal/workbook/xlsb_function_table.go`](../../internal/workbook/xlsb_function_table.go)（679 行 / 9.4 KB / 0 函数）：纯 MS-XLSB 规范函数 ID 到名称的静态查表数据。
- [`internal/workbook/axis_visibility.go`](../../internal/workbook/axis_visibility.go)（616 行）、[`hyperlinks.go`](../../internal/workbook/hyperlinks.go)（610 行）、[`catalogue.go`](../../internal/workbook/catalogue.go)（602 行）、[`internal/checkpoint/store.go`](../../internal/checkpoint/store.go)（603 行）、[`internal/editdoc/document.go`](../../internal/editdoc/document.go)（630 行）：单一领域模型与算法实现，内聚度高。
- [`internal/ui/web/style.css`](../../internal/ui/web/style.css)（812 行）：被多个单测与 QA 夹具直接作为单文件内联读取。

---

## 五、后续可选演进方向

第一阶段的高收益前后端拆分已全部落盘并通过全仓验证。后续仅在业务触及对应模块或启动专项重构时再按需推进：

1. **前端持续去切片化**：继续将 `app.js` 其余存量切片测试逐步迁向独立纯模块，再考虑分离虚拟网格渲染流水线（`gridSteps`）与非首屏 `<dialog>` 模板。
2. **后端会话模型与跨包复用**：将 `server.App` 在类型层拆分为 `RootServer` 与 `TabSession`；在 [`tools/project/architecture.go`](../../tools/project/architecture.go) 评估新增零依赖叶包统一 `calculator`/`workbook`/`editfile` 三处的 Excel 1900/1904 日期序列换算；以及按需将 `calculator/functions_*.go` 巨型 `switch` 改为直绑注册表。
