# 文档索引

本仓库仅包含当前版源码和必要文档；旧版发行材料、原始开发日志及私有资料未公开。开发说明中的场景与边界保留，未公开记录不作为当前公开快照已重新验证的证明。

| 目录 / 文件 | 用途 |
| --- | --- |
| [user/editing.md](user/editing.md) | 网格编辑、公式、保护表及透视布局 |
| [development/xlsx-grid-editing.md](development/xlsx-grid-editing.md) | 工作表写出、计算与结构编辑设计 |
| [releases/1.11.0.md](releases/1.11.0.md) | 填充柄、跨簿粘贴、公式补全、工作表管理与历史压缩 |
| [releases/1.10.1.md](releases/1.10.1.md) | 公式点选、拖选与跨表引用、函数提示和行列高亮延伸，本地试用 |
| [releases/1.10.0.md](releases/1.10.0.md) | 指定和相对日期透视筛选、区域移动、表尾扩展与公式录入 |
| [releases/1.9.1.md](releases/1.9.1.md) | 区域编辑、透视对象和标签页进度 |
| [releases/1.9.0.md](releases/1.9.0.md) | 最新公开版本的升级及验收范围 |
| [user/formats.md](user/formats.md) | 当前格式、精度、缓存、压缩包及导出边界 |
| [user/interface.md](user/interface.md) | 经典工具栏、可选选项卡、命令搜索与托盘 |
| [user/calculator.md](user/calculator.md) | 计算器语义、用法及限制 |
| [user/conversion.md](user/conversion.md) | 手动格式转换、日期顺序、失败处理和计算建议 |
| [user/recovery-and-issues.md](user/recovery-and-issues.md) | 查询/导出任务恢复、计算表及草稿恢复、异常核对清单 |
| [user/reconciliation.md](user/reconciliation.md) | 双表对账、精确方式、来源和完整CSV |
| [user/named-views.md](user/named-views.md) | 保存和应用命名视图、来源校验与配置边界 |
| [user/name-catalogue.md](user/name-catalogue.md) | 名称与表对象目录、作用域、声明确认及源区域定位 |
| [user/data-rules.md](user/data-rules.md) | 当前格/全表数据规则、条件和文字列表、未知覆盖及只读定位 |
| [user/printing.md](user/printing.md) | 只读分页预览、源坐标区域、页面覆盖、屏幕缩放和还原边界 |
| [user/record-card.md](user/record-card.md) | 宽表记录卡片、字段/记录分页、完整值复制和源格定位 |
| [user/cell-information.md](user/cell-information.md) | 单格源类型、数值统计资格、公式结果及当前筛选 |
| [user/data-profile.md](user/data-profile.md) | 全量精确分位、数值区间、常见值和分析范围 |
| [user/value-clustering.md](user/value-clustering.md) | 疑似重复原文核对、显式文本映射、来源范围和候选CSV |
| [user/selection-quick-analysis.md](user/selection-quick-analysis.md) | 选区逐列分布、同行精确差额、分段趋势和源位置 |
| [user/chart-image-export.md](user/chart-image-export.md) | PNG/矢量SVG、精确数据附件、图表范围及下载包 |
| [user/row-visibility.md](user/row-visibility.md) | 行显隐、大纲、可见行导出及临时恢复 |
| [user/analysis-plans.md](user/analysis-plans.md) | 保存分析方案、字段匹配预览及跨来源重放 |
| [user/table-composition.md](user/table-composition.md) | 多表追加、显式字段对齐、有限预览、定义复用和原格追溯 |
| [user/table-relations.md](user/table-relations.md) | 两表关联、重复键展开、已匹配/未匹配提取、精确数量和完整CSV |
| [user/clipboard.md](user/clipboard.md) | 显示值/原值/公式文字、表头、Markdown/JSON复制和文字粘贴预览 |
| [user/transpose.md](user/transpose.md) | 连续选区的转置阅读、原值/公式状态和原格定位 |
| [user/comparison-snapshots.md](user/comparison-snapshots.md) | 完成分析的精确快照、小趋势和分类比较 |
| [user/functions.md](user/functions.md) | 从引擎生成的函数清单 |
| [development/architecture.md](development/architecture.md) | 包职责、依赖规则和格式接入方式 |
| [development/xlsb-compatibility.md](development/xlsb-compatibility.md) | BIFF12 解析、网格呈现边界及原生同源样本复现 |
| [development/reading-benchmarks.md](development/reading-benchmarks.md) | 当前版阅读用时、测试环境及复现方法 |
| [development/ui-stability.md](development/ui-stability.md) | 长表滚动行坐标、右键菜单层级、冻结请求归属与小屏工具栏 |
| [development/ui-design-system.md](development/ui-design-system.md) | 界面令牌、组件、功能区、文案规则和界面检查脚本 |
| [development/windows-race.md](development/windows-race.md) | Windows竞态错误87的复现、原因和检查配置 |
| [development/improvement-progress.md](development/improvement-progress.md) | 调研改进的实际完成状态与后续阶段 |
| [development/row-order-and-sort.md](development/row-order-and-sort.md) | 第二阶段行序、外部排序和实际规模验证 |
| [development/reconciliation.md](development/reconciliation.md) | 双来源快照、流式对账和实际验证 |
| [development/named-views.md](development/named-views.md) | 命名视图、确认规则、持久化及验证 |
| [development/recipes.md](development/recipes.md) | F03分析方案、类型守卫、任务和验证边界 |
| [development/analysis-sources.md](development/analysis-sources.md) | 工作区来源范围、共享读取、快照引用及持久化兼容 |
| [development/analysis-tree.md](development/analysis-tree.md) | F08当前层精确聚合、路径展开、任务和验证边界 |
| [development/value-clustering.md](development/value-clustering.md) | F11有界候选分组、类型守卫、确认映射及实际验证 |
| [development/selection-quick-analysis.md](development/selection-quick-analysis.md) | F13来源绑定选区、精确分段累加、配对差额及容量验证 |
| [development/chart-image-export.md](development/chart-image-export.md) | F14共用绘图、SVG记录器、有界精确附件/ZIP及生命周期 |
| [development/cell-explanation.md](development/cell-explanation.md) | F20来源绑定单格解释、真实状态、预算及当前验证 |
| [development/value-distribution.md](development/value-distribution.md) | F19全量分布、精确分位、来源绑定及当前验证 |
| [development/table-composition.md](development/table-composition.md) | F17只读联合来源、显式字段映射、流式追加、工作区及逐格追溯 |
| [development/table-relations.md](development/table-relations.md) | F18两侧关联键、精确膨胀预检、有界提取、共享字段控件及界面验证 |
| [development/task-recovery.md](development/task-recovery.md) | F21任务检查点、来源/规则绑定、确认边界与当前实施状态 |
| [development/transpose.md](development/transpose.md) | F23局部转置、原坐标/类型、公共读取与分页呈现 |
| [development/comparison-snapshots.md](development/comparison-snapshots.md) | F24来源绑定捕获、持久目录和精确比较 |
| [development/relation-export-optimization.md](development/relation-export-optimization.md) | 关联CSV编码复用、流式ZIP与实际规模测量 |
| [development/task-lifecycle-optimization.md](development/task-lifecycle-optimization.md) | 状态锁外发送、导出完成边界与失败对账来源回收 |
| [development/stability-baseline.md](development/stability-baseline.md) | 查询历史容量、恢复准入及双标签页长时HTTP资源基线 |
| [development/search-intents.md](development/search-intents.md) | 搜索取消后重试、显式来源守卫与候选版验证 |
| [development/catalogue.md](development/catalogue.md) | F12按需名称/表对象元数据、记录身份及底层验证边界 |
| [development/data-rules.md](development/data-rules.md) | F15按需源规则、压缩范围、来源绑定任务、只读界面及验证边界 |
| [development/printing.md](development/printing.md) | F22源打印设置、物理分页、来源绑定任务/按页读取及预览边界 |
| [development/record-card.md](development/record-card.md) | F06来源绑定记录读取、宽表字段页、可见行序及容量边界 |
| [development/analysis-comparisons.md](development/analysis-comparisons.md) | F09精确占比、双向累计、非加性聚合与版本/输出方案 |
| [development/source-fidelity.md](development/source-fidelity.md) | 第三阶段富文本、复制及快照、规模证据和后续缺口 |
| [development/visual-validation.md](development/visual-validation.md) | 第三阶段字体/DPI/应用缩放独立组合、真实字形字体及证据边界 |
| [development/conditional-formats.md](development/conditional-formats.md) | 条件格式/DXF、精确读取、网格/复制/HTML验证及XLSX后续设计 |
| [development/upstream-research.md](development/upstream-research.md) | 上游算法、功能和写入方案调研 |
| [releases/1.18.0.md](releases/1.18.0.md) | 条件格式、选区高亮、模块拆分与审计修复 |
| [releases/1.17.2.md](releases/1.17.2.md) | 快照容量与删除、采集证据导出、暂存恢复和首页复制 |
| [releases/1.17.1.md](releases/1.17.1.md) | 数据库快照留存与核验、XLSB 同源样本和兼容性矩阵 |
| [releases/1.17.0.md](releases/1.17.0.md) | XLSB、RAR、在线数据库、依赖升级和验收范围 |
| [releases/1.16.1.md](releases/1.16.1.md) | 滚动中的真实文字、样式和点击定位，以及冷跳转和三轮性能对照 |
| [releases/1.16.0.md](releases/1.16.0.md) | 全量搜索、传输历史修复、启动恢复进度与发行性能基准 |
| [releases/1.15.0.md](releases/1.15.0.md) | 受限恢复、后台任务管理、磁盘空间核验及前端优化 |
| [releases/1.14.0.md](releases/1.14.0.md) | 跨电脑来源重定位、加密备份、事务迁入与后台任务 |
| [releases/1.13.4.md](releases/1.13.4.md) | 浏览器会话回收、独立计算原生对照及本地数据管理 |
| [releases/1.13.3.md](releases/1.13.3.md) | 选框与表格线、公式栏、工作表标签及选区统计 |
| [releases/1.13.2.md](releases/1.13.2.md) | 双布局、托盘及 Windows 长短路径缓存清理 |
| [releases/1.13.1.md](releases/1.13.1.md) | 已撤回的双布局源码候选与路径问题 |
| [releases/1.13.0.md](releases/1.13.0.md) | 选项卡功能区、窄窗操作、护眼预设与选区显示 |
| [releases/1.12.2.md](releases/1.12.2.md) | 正式双包与多工具路径兼容 |
| [releases/1.12.1.md](releases/1.12.1.md) | 1.12 核查工作流和资源一致性正式包 |
| [releases/1.12.0.md](releases/1.12.0.md) | 主键版本比较、金额容差、ODS、分页与批注 |
| [releases/1.8.19.md](releases/1.8.19.md) | 1.8.19的功能汇总、验证及构建结果 |
| [releases/1.8.20.md](releases/1.8.20.md) | 长表滚动根因修复及验证结果 |
| releases/ | 各发布版本的说明 |
| [CHANGELOG.md](CHANGELOG.md) | 当前版本变更 |
| validation/（源码仓库） | 当前自动检查的必要摘要，不含原始日志或私有资料 |

当前用户说明放 `user/`，维护规则放 `development/`。历史材料用于追溯，不作为当前版本已经通过验收的证据。版本更新不向根 README 追加旧版整段说明。
