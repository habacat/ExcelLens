# 计算

点击「计算」或按 Ctrl+Alt+C。输入 `=` 后输入数字、文本或点击源单元格，Enter 计算并进入下一行，Esc 取消本次编辑。输入函数名时用方向键选择补全，Tab/Enter 插入函数。

示例：`=1=1` → TRUE，`=IF(A1=B1,"一致","差异")`，`=LEFT("000123",3)` → 000，`="凭证"&"001"`，`=SUM(A1:A10)`。支持跨工作表引用；未选择的 IF 分支不读取源数据。

选择公式参数时可点击列标或行号：B 列生成 `B:B`，第2行生成 `2:2`；拖动 A 到 C 列标、或 Shift 扩选生成 `A:C`。主表及冻结区域都保留公式输入焦点，Enter 计算。连续重新选择会替换刚插入的引用，添加逗号或运算符后可选择下一个参数；切换 Sheet 时自动加上表名。调整行高/列宽或点击筛选按钮不会插入引用。整行/整列计算范围及限制见下文。

默认五行，视口随内容增长至十二行，更多行通过滚动查看。支持多行公式粘贴、结果格 Ctrl+C 复制；「备注」在结果右侧显示备注列，再次点击隐藏，内容保留。收起后点击底部状态栏按钮恢复。

公式或备注每次可粘贴一列、最多2000行，支持Excel/WPS的引号与单元格内部换行；多余列、未闭合引号或超长内容整次拒绝。独立分析编辑表支持多列粘贴，最多20,000格；超过现有列宽或总行数上限时整次拒绝，不裁掉边缘内容。

常用函数覆盖以下类别，完整名称及参数见 [函数清单](functions.md)。

| 类别 | 常用函数与示例 |
| --- | --- |
| 条件统计 | SUMIF/SUMIFS、COUNTIF/COUNTIFS、AVERAGEIF/AVERAGEIFS、MINIFS/MAXIFS、COUNTBLANK。`=SUMIFS(C2:C100,A2:A100,"北京",B2:B100,">="&DATE(2024,1,1))` |
| 查找与引用 | XLOOKUP、VLOOKUP/HLOOKUP、MATCH/XMATCH、INDEX、LOOKUP、OFFSET、INDIRECT、ADDRESS、ROW/COLUMN。XLOOKUP 支持按匹配行返回多列，或按匹配列返回多行 |
| 数学与统计 | ROUND、MOD、POWER、SQRT、SUMPRODUCT、MEDIAN、STDEV.S、VAR.P、RANK、PERCENTILE、CORREL、COVARIANCE.S、SLOPE、FORECAST.LINEAR 等 |
| 逻辑与文本 | IF、IFS、IFERROR、IFNA、SWITCH、LET、TEXTJOIN、TEXTBEFORE/TEXTAFTER、TEXTSPLIT、NUMBERVALUE 等 |
| 日期与工作日 | DATE、TIME、YEAR、DATEDIF、EDATE、EOMONTH、DAYS360、YEARFRAC、NETWORKDAYS/WORKDAY 及其 INTL 形式 |
| 财务 | PV/FV/PMT、RATE、NPER、IPMT/PPMT、NPV/XNPV、IRR/XIRR/MIRR、SLN/SYD/DDB、EFFECT/NOMINAL、RRI/PDURATION |
| 数组 | FILTER、SORT、UNIQUE、SEQUENCE、TRANSPOSE、TAKE/DROP、CHOOSECOLS/CHOOSEROWS、HSTACK/VSTACK、TOCOL/TOROW |

条件支持 `*`、`?` 及 `~` 转义，文本匹配不区分大小写；多条件同时成立。支持 `{1,2;3,4}` 数组常量，数组运算、广播以及 `=SUMPRODUCT((A2:A100="北京")*C2:C100)`、`=SUM(IFERROR(C2:C100,0))`。常见数学、文本、日期、类型判断函数可对数组逐元素计算。数组结果显示“行数 × 列数”，结果格 Ctrl+C 复制完整 TSV；结果停留在计算面板，不向源表溢出写入。

金额使用精确有理数，循环小数以分数保留底层结果并标记近似显示；三角、对数、小数幂、正态分布和大多数财务函数使用浮点计算，结果标记 `≈`。采用 Excel 的连续幂从左到右及负号先于幂规则；`=2^3^2` → 64，`=-2^2` → 4。支持源文件1900/1904日期系统，包含序号60的假闰日。Enter 显式重算；仅切换焦点或备注不重复计算。

源文本和长编号保持原值；CSV/TXT/HTML 等导入金额继续沿用原有会计数字解析，既保留文本身份，也可参与金额计算。这一点与 Excel 对纯文本金额的默认处理可能不同。范围公式按源坐标计算，包含筛选后隐藏行。

公式和备注增量保存为本机恢复副本，不写入源文件；恢复后的公式等待主动计算，保存状态及容量见[输入恢复说明](recovery-and-issues.md)。计算表行数没有固定上限，受浏览器内存约束；最多四条公式请求并行。单条公式最多4096字节、128项引用、40层嵌套及累计400万格源范围；数组与需物化的统计数据最多20万格，单次数组文本最多32MiB，文本及数组复制结果最多64KiB。条件统计按源单元格流式读取，无需把整个文本列装入旧的8192格文本缓冲。旧版历史文件保留，不在当前面板展示。

整列（如 `A:A`）和整行（如 `1:1`）仅覆盖源已用区域，不扩展到 Excel 工作表的全部空白格；COUNTBLANK 等空白统计需要完整范围时请写明确地址。查找支持正向与反向线性搜索，不支持 XLOOKUP/XMATCH 的二分搜索模式；近似查找请使用按 Excel 要求排序的数据。INDIRECT 支持 A1 地址，ROW/COLUMN 需要明确引用，OFFSET 需要直接源引用。

这是可继续扩展的 Excel 常用函数子集。尚未提供 LAMBDA/MAP/REDUCE、自定义函数、结构化表引用、命名区域、三维引用、外部工作簿、SUBTOTAL/AGGREGATE 的隐藏行语义及源公式递归重算。不是所有函数都支持数组参数，条件统计目前使用标量条件。TEXT 支持常见数值与日期时间格式，不包含完整 Excel 格式语言；DATEVALUE/TIMEVALUE 支持常用中文或年-月-日格式与24小时制时间，不实现系统区域设置的全部解析。CODE/CHAR 仅实现单字节码值，中文请用 UNICODE/UNICHAR。特殊金融日期惯例、浮点迭代及复杂区域化行为可能与 Excel 存在差异，关键结果应核对。
