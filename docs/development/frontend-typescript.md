# 前端 TypeScript 架构与工程规范

ExcelLens 前端全部生产源码与单元测试位于 `internal/ui/src/`，采用现代严格 TypeScript（`strict`、`noUncheckedIndexedAccess`、`verbatimModuleSyntax`、`erasableSyntaxOnly`）编写，经由确定性 `esbuild` 打包为两个静态 IIFE 产物（`internal/ui/web/dist/app.js` 与 `internal/ui/web/dist/storage-recovery.js`），再通过 Go `//go:embed` 嵌入单一原生可执行文件。**发布后的单 EXE 运行时不依赖 Node.js、npm、Excel、WPS 或任何外部框架。**

---

## 一、五层单向依赖架构（严格 DAG）

生产代码（139 个模块）按职责划分为 5 个层级与 7 个注册目录，由 [`tools/frontend/check-arch.mjs`](../../tools/frontend/check-arch.mjs) 通过 TypeScript Compiler API 静态扫描全部 `import`/`export` 声明，强制检查零环依赖、零跨层逆向导入、零禁止的全局命名空间访问以及零静默类型压制。

| 层级 | 目录 | 模块职责 | 允许导入的层 |
| --- | --- | --- | --- |
| Layer 0 | `internal/ui/src/contracts/` | 前后端 HTTP JSON 数据契约与通用领域接口（纯 `type-only` 声明，零运行时代码） | `contracts`（仅 `import type`） |
| Layer 1 | `internal/ui/src/core/` | 传输层、会话租约、活动进度、恢复日志、DOM/格式化/A1 地址解析、公共异步任务机、来源任务机、字段选择器与通用浮层 | `contracts`, `core` |
| Layer 2 | `internal/ui/src/grid/` | 工作簿状态容器、虚拟滚动与几何引擎、合并/冻结/多选/选区外框/条件格式/富文本/剪贴板/单元格呈现、视口缓存与调度器、行显隐、表头状态、工作表切换与视口渲染流水线、交互/菜单/IO 控制器 | `contracts`, `core`, `grid` |
| Layer 3a | `internal/ui/src/editing/` | 可编辑副本会话、单元格与区域粘贴/填充、公式录入/补全/引用高亮、编辑工作表标签与数据透视表布局/筛选编辑器 | `contracts`, `core`, `grid`, `editing` |
| Layer 3b | `internal/ui/src/features/` | 独立工具模块：计算器、公式追踪、搜索面板、多级排序、数据洞察/定位检查、选区统计与快速分析、导出中心、转置与选择性剪贴板、名称目录、数据规则、打印分页预览、记录卡、命名视图、行视图、双表对账/关联与提取、任务恢复、压缩包、在线数据库与快照、阅读/高亮模式、工具栏与命令面板 | `contracts`, `core`, `grid`, `features` |
| Layer 3c | `internal/ui/src/analysis/` | 独立分析工作区：工作区装配器、透视视图与下钻、仪表盘与模板引擎、可编辑分析工作表、图表渲染与 PNG/SVG/ZIP 导出、对比占比、数值格式设置、异常核对、疑似重复聚类、多表追加、分析方案与比较快照 | `contracts`, `core`, `grid`, `analysis` |
| Layer 4 | `internal/ui/src/app/` | 组合根与入口：`main.ts`（主界面装配）、`storage-recovery.ts`（独立本地数据恢复页）、`runtime-bridge.ts`（受控外部 QA 诊断钩子） | 全部层级 |

### 核心拆解与状态边界

- 原巨石文件 `app.js` 拆解为 `grid/state.ts`（状态结构）、`grid/viewport-pipeline.ts`（视口调度与渲染提交）、`grid/sheet-controller.ts`（工作表切换/筛选/排序/行视图提交）、`grid/navigation.ts`（焦点/滚屏/公式追踪导航）以及 `app/main.ts`（组合根），并保留既有的 `grid/grid-{clipboard,context-menu,workbook-io,interactions}.ts` 控制器。
- 原巨石文件 `analytics.js` 拆解为 `analysis/pivot-view.ts`（透视表、层级树、图表、下钻与结果导出视图）和 `analysis/workspace.ts`（工作区编排、字段与来源作用域、规则/转换编辑器、报表保存与工作区刷新），复用既有的 `analysis/dashboard.ts` 与 `analysis/sheets.ts`。
- 全部模块内部通过显式 ES Module 导入和依赖注入协作，消除旧版 `window.ExcelLens*` 内部全局命名空间；仅 [`internal/ui/src/app/runtime-bridge.ts`](../../internal/ui/src/app/runtime-bridge.ts) 保留供隔离浏览器基准与 QA 脚手架使用的四个只读/会话钩子（`ExcelLensSession`、`ExcelLensPerformance`、`ExcelLensWorkbookReady`、`ExcelLensAnalytics`）。

---

## 二、类型安全与静态检查规范

配置见 [`tsconfig.json`](../../tsconfig.json)、[`tsconfig.app.json`](../../tsconfig.app.json) 与 [`eslint.config.mjs`](../../eslint.config.mjs)：

1. **严格编译配置**：开启 `strict`、`noUncheckedIndexedAccess`、`noImplicitOverride`、`noImplicitReturns`、`noFallthroughCasesInSwitch`、`noUnusedLocals`、`noUnusedParameters`、`useUnknownInCatchVariables`、`verbatimModuleSyntax`、`erasableSyntaxOnly`。应用编译（`tsconfig.app.json`）明确设 `"types": []`，隔离 Node 类型避免误入浏览器代码。
2. **零不安全逃生舱**：ESLint 启用 `typescript-eslint` 的 `strictTypeChecked` 与 `stylisticTypeChecked`，强制禁止 `any`、`no-unsafe-*`、非空断言 `!`、`@ts-ignore`/`@ts-expect-error`/`@ts-nocheck`、`eslint-disable` 以及双重类型断言 `as unknown as T`（`TSAsExpression > TSAsExpression`）。
3. **契约优先**：前后端交换的 JSON 载荷均在 `internal/ui/src/contracts/` 定义与 Go HTTP 结构体对齐的只读类型；对于受容量与安全约束的关键载荷（打印、洞察、规则、关联、恢复、聚类、图表导出等），同时保留运行时显式校验器，绝不信任未校验外部数据。

---

## 三、构建产物、嵌入方式与开发命令

所有前端脚本统一维护在 [`package.json`](../../package.json) 与 `tools/frontend/`：

| 命令 | 作用 |
| --- | --- |
| `npm run typecheck` | 同时对测试代码（`tsconfig.json`）与纯浏览器生产代码（`tsconfig.app.json`）执行零错误类型检查 |
| `npm run lint` | 对 `internal/ui/src/**/*.ts` 执行严格类型感知 ESLint 检查 |
| `npm run arch:check` | 校验分层 DAG、零环依赖、零内部全局变量、零静默压制注释，以及 `internal/ui/web/` 无遗留 `.js`/`.cjs` 脚本 |
| `npm run build:ui` | 通过 [`tools/frontend/build.mjs`](../../tools/frontend/build.mjs) 使用 `esbuild` 生成确定性（UTF-8、LF、固定头注释）浏览器产物 `internal/ui/web/dist/app.js` 与 `storage-recovery.js` |
| `npm run build:ui:check` | 在内存中重编译并按字节比对 `internal/ui/web/dist/*.js`，确保提交产物与 TypeScript 源码完全同步 |
| `npm run test:ui` | 通过 Node 22.18+（22.x）或 24+ 原生 TypeScript 类型剥离运行 [`tools/frontend/run-tests.mjs`](../../tools/frontend/run-tests.mjs)，直接导入执行全部 94 个 `.test.ts` 模块 |
| `npm run check:ui` | 串联执行上述全部五项前端质量门禁 |

### Go 静态嵌入与缓存版本化

[`internal/ui/assets.go`](../../internal/ui/assets.go) 嵌入 `web/*.html`、`web/dist/*.js`、`web/*.css` 与 `web/*.svg`：

- `index.html` 仅引用 `/assets/app.js`（由服务端自动改写为带 SHA-256 哈希查询串的 `/assets/app.js?v=<sha256>`）；
- `storage-recovery.html` 仅引用 `/assets/storage-recovery.js`；
- `ReadAssetWithETag` 将 `.js` 请求映射到 `web/dist/<name>`，`.css`/`.svg` 保持在 `web/<name>`，同时拒绝任意路径穿越、目录访问及非白名单扩展名。

### 统一质量门禁集成

`go run ./tools/project check` 已将 TypeScript 类型检查、ESLint、前端分层架构校验、确定性构建校验、全部前端 TypeScript 单元测试以及 `tools/` 工具脚本语法检查统一纳入同一离线质量门禁，并与 `build.ps1` 及 GitHub Actions CI 共用同一入口。
