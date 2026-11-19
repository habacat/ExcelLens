# XLSX 原坐标编辑的工程设计与实施路径

目标是在普通工作表网格中编辑有明确定义的 Excel 单元格。原件提供不可变的坐标、公式、数据类型及校验证据，修订保存为独立 XLSX。可写性由包内关系、工作表和目标坐标共同决定。真实报表只作为验证输入。

| 阶段 | 用户可执行的操作 | 工程路径 | 验收条件 |
| --- | --- | --- | --- |
| 1. 坐标和稀疏存储 | 在空位、缺失的行、原已用区外或空工作表输入并撤销 | 区分 Excel 地址上限、源已用范围、稀疏修订。按行列排序插入，处理自闭合节点，继承行列样式，扩展已有 dimension，处理可选行跨度 | 表首、表中、表尾、新行、空表、边界地址往返；相邻格、隐藏行列和底稿摘要正确 |
| 2. 普通及共享公式 | 在公式栏和格内修改、覆盖或新增公式，复制和平移相对引用 | 解析整条公式并记录引用位置，保留工作表限定、名称、绝对轴和字符串。修改共享组成员时，将该组的实际成员还原成各自的公式，独立替换目标 | 修改锚点和从格均保留其他成员的实际公式；测试跨表、整行整列、绝对引用和条件汇总 |
| 3. 数组和区域所有权 | 修改数组锚点，在透视表、筛选及合并区周围编辑 | 根据数组 ref、透视输出位置、透视源字段和筛选表头识别结构拥有的坐标。单格动态数组保留元数据，固定数组由锚点控制整组；覆盖格返回具体的结构原因 | 原生 Excel 重算、锚点元数据与相邻结果对照；批次中含受限位置时整批拒绝 |
| 4. 重算与互操作 | 保存、重开、到 Excel 重算或刷新 | 从受影响工作表追踪公式与透视依赖。清理失效的结果、可选计算链及关系，保留计算模式和迭代参数。受影响的透视缓存标记待刷新 | 包关系完整、未改部件原压缩字节一致；独立读取和 Excel 对同样修改的计算对照通过 |
| 5. 交互与历史修订 | 点击或键入、连续移格、公式栏、撤销重做、恢复旧底稿 | 网格与高级操作通过同一源读取和批次校验。恢复原修订时沿用底稿身份和历史事务。稀疏区域不分配整张工作表 | 浏览器从正常入口编辑、恢复、下载、重开，校验旧文档、运行标准工程检查 |

### 实现如何连起来

`formats` 提供格式能力和地址上限。`editfile` 在显式创建副本时沿 OPC 关系读取部件，记录已用区、受其他对象控制的区域和表间依赖。`calculator` 从公式语法树识别本簿引用、名称作用域和可平移的位置。双击、公式栏、粘贴和填充把目标的原始行列交给服务端，由同一个批次校验器判定可写性。`editdoc` 只记录已确认的稀疏变更、前值和修订。撤销或重启后，网格用当前修订重新覆盖公共读取器的结果。

保存时重新核对底稿，根据事务计算需要修改的工作表和失效的结果。按地址写出 XML 并复制其他 ZIP 部件；生成件重读后核对部件、坐标、类型与公式，才发布本修订的下载。筛选后的第几条记录、合并项显示的位置或冻结窗格里的第二份视图都不能代替源坐标。版本、范围和历史事务使用同一文档身份。

### 新结构如何接入

1. 用最小工作簿复现，记录首次失败的包关系、工作表元素或地址，再用第二种生成器建立相同结构。测试文件名不参与判定。
2. 沿工作簿关系确定谁拥有数据。自定义属性等独立部件可以原样复制；数组和透视输出需记录覆盖范围；公式、名称和透视源需要记录依赖。如果整体依赖无法界定，在预检处给出具体原因。
3. 把坐标、依赖和写出规则接入公共事务。对同表无关格、对象锚点、覆盖格、混合批次分别断言修改结果或失败后的原修订。保存路径按同一规则重新验证。
4. 用 Excel 在临时底稿上执行相同操作，把生成件重算、刷新、另存后的类型、公式结果及对象与之逐项对照。读取、允许编辑对象旁的格、改变对象本身是三项独立能力。

### 接近 Excel 原生编辑的后续交付顺序

前五段交付的是单元格内容编辑。下面每个工作包列出可以独立验证的用户操作、所需改动和通过条件。实际断点和已交付版本统一记在[接续入口](improvement-progress.md#接续入口)。

| 顺序 | 交付操作 | 实施路径及依赖 | 进入发行的条件 |
| --- | --- | --- | --- |
| 6. 修订内重算 | 改输入后直接看到有依赖的公式结果 | 在现有公式树上提取单格、范围和名称依赖，构建反向索引。先支持算术、SUM、IF，再接入条件聚合与查找。由源值和修订覆盖共同提供操作数，按修订丢弃迟到输出。区分已算值、Excel 错误、循环和暂待宿主重算的结果 | 两个上游工作表、跨表连锁、相同输入的撤销与重做都与 Excel 原生结果同值同类型；对稀疏整列记录访问量，对大批修改记录重算耗时 |
| 7. 表格、规则与格式 | 在有条件格式的工作表上写值，按既有样式输入或粘贴，编辑 Excel 表格的数据行 | 先识别被动的样式、富文本和条件格式部件及局部依赖，再解析 table、列标识、计算列和汇总行。复用现有工作表样式及规则模型。保护表按工作表保护设置和有效样式逐格判断，保留原保护信息 | 对含多个表对象和多个独立区域的样本测试。仅值、仅格式及只改邻格分别核对原字节、呈现结果、保护状态、批次拒绝和撤销 |
| 8. 多格结构操作 | 合并和取消合并、对整个固定数组换公式或调整范围、输入真正的动态溢出公式 | 在单格事务之上增加矩形前像、锚点和范围所有权变更。检查与透视、表格、合并、非空格的交叠。先交付固定范围，再处理由重算决定的动态范围和溢出错误 | 覆盖扩张、缩小、阻塞、释放旧结果，验证中断恢复和多用户修订冲突下的原子性。生成件与原生 Excel 的锚点、结果范围及相邻原值逐格比对 |
| 9. 整行整列和工作表操作 | 插入、删除、移动行列，重命名工作表 | 先为单表建坐标变换，随后接入跨表公式、名称、合并、规则、打印和对象锚点。每类引用实现其移动、复制和删除语义，接入撤销日志后才开放相应命令 | 混合绝对引用、引用删除、整列、跨表及同名局部名称均与 Excel 对照。混合操作能撤销到原坐标，保存再打开保持同样的引用 |
| 10. 透视表和图表对象 | 在副本里刷新透视、调整字段布局并更新关联图表 | 先做本簿固定源的数据刷新及缓存重建，再加入字段、筛选、计算项、串联透视和图表系列缓存。根据依赖顺序安排计算和刷新；已有图片和绘图锚点继续随包保留 | 用相同输入、字段排列和筛选比较结果区域、项目顺序及总计。保存后 Excel 正常打开、刷新、另存，未修改对象逐项核对 |

每包至少使用一份无业务数据的可提交回归和独立生成器样本，按所改功能追加真实报表。针对单个坏坐标的拒绝需同时验证同表其他可编辑地址；批次包含坏坐标时检查整批前值和修订。性能分别测量首次进入、单格确认、连续输入、批量粘贴和生成，记录样本、版本和基线。浏览器自动化、Excel 原生对照与 WPS 实际互操作分别出具结果。

### 第 6 包的计算路径

1. 编辑入口核对底稿摘要。首次需要求值时扫描工作表 XML、平移共享公式、解析名称并绑定公式的行列坐标。单格依赖精确索引，矩形按列和行段登记，整列引用保留上下界。 [Excel 的依赖与重算](https://learn.microsoft.com/en-us/office/client-developer/excel/excel-recalculation)
2. 将确认修订的稀疏变更叠加在底稿上，登记新增或替换的公式。从改动地址沿反向引用寻找下游。整列引用用作单值条件时，按公式所在行取值。多次编辑时，仅复用输入未受本次修改影响的结果。撤销或恢复删除补丁时，原地址也触发失效。长链分段求值，按 IF 实际执行的分支读取操作数。 [Excel 的隐式交叉](https://support.microsoft.com/en-us/office/implicit-intersection-operator-ce3be07b-0101-4450-a24e-c1c999be2b34)
3. 网格接收直接编辑和当前修订的公式结果。切换修订时整组替换计算结果，单格读取和仅值复制使用同一快照。源格缓存、计算结果、循环、待重算和待刷新各有对应状态。文件写出由 XLSX 部件及依赖校验单独负责。 [SpreadsheetML 的公式和保存值](https://learn.microsoft.com/en-us/office/open-xml/spreadsheet/working-with-formulas)
4. 从编辑器的生产接口读取结果，与 Excel 全量重算后的临时副本逐格匹配。保存源文件中的值与 Excel 再算的末位可能有差别，收据分别记录原始状态、改值、撤销和重做，以及非精确相等的数量。 [Excel 数值精度](https://learn.microsoft.com/en-us/troubleshoot/microsoft-365-apps/excel/floating-point-arithmetic-inaccurate-result)

### 第 7 包的坐标、公式和保护

1. 从工作表的 `tablePart` 关系读取表格，按实际字段顺序将矩形拆为标题、数据与汇总。保存列 ID 与标题，解析计算列模板和汇总函数。源定义中的字段 ID 可以不连续。 [SpreadsheetML 表格列](https://learn.microsoft.com/en-us/dotnet/api/documentformat.openxml.spreadsheet.tablecolumn)
2. 在网格和粘贴预览使用同一套列规则。输入可按行平移的统一公式时检查选区外的成员。自动填充、整列模板和例外格组成一次事务，规则或保护校验失败时修订保持原值。整列混合粘贴和逐格输入在 Excel 中产生不同的列定义。 [Excel 计算列自动填充](https://learn.microsoft.com/en-us/office/vba/api/excel.autocorrect.autofillformulasinlists)
3. 将结构化引用绑定到工作表、数据区域和当前行，再交给第 6 包的计算器。分类汇总按函数编号、隐藏行和嵌套汇总取值。存在筛选且无法分辨手工隐藏与筛除时，依赖此区别的汇总显示待重算。 [结构化引用](https://support.microsoft.com/en-us/office/using-structured-references-with-excel-tables-f5ed2452-2337-4f71-bed3-c8ae6d2b276e) · [SUBTOTAL](https://support.microsoft.com/en-us/office/subtotal-function-7b027003-f060-4ade-9040-e478765b9939)
4. 工作表启用内容保护时，已分配的格按其实际样式判断锁定，未分配格依次查行、列和默认样式。值编辑和公式展开都复查受影响的坐标。保护记录、密码校验信息和原样式按源部件保留。 [Excel 的 Locked 属性](https://learn.microsoft.com/en-us/office/vba/api/excel.range.locked)
5. 保存时在工作表和表格部件写出同一修订。重读并核对列模板、汇总函数、标签和单元格内容，再开放下载。原生验证比较工作表矩形的实际值、类型和公式，也比对生成件与 Excel 对照中的每套表格定义。

### 第 8 包的区域、数组与修订

1. 合并前按源坐标读取完整矩形，检查现有数组、合并及对象归属。首个内容需要移至左上角时重绑直接引用与名称，保留被移单元格的样式及公式。结构快照保存清除前的整块内容。
2. 固定数组按输出矩形广播单行或单列，超过可用结果的坐标填 `#N/A`。动态数组由公式结果决定尺寸。每轮在同一份范围归属上计算，形状变化后重绑稀疏范围、跨表引用和 `A1#`，并清除新范围外的旧结果。越界、合并区以及已占用格的原生溢出错误分别记录。
3. 普通输入、显式区域命令和生成文件共用计算与写出代码。生成件使用数组锚点的 `f@ref` 和 XLDAPR 元数据。溢出及其传播错误使用值元数据绑定到富值记录。超出计算容量或无法确定整组结果的操作在提交前返回原因。 [动态数组与溢出行为](https://support.microsoft.com/en-us/office/dynamic-array-formulas-and-spilled-array-behavior-205c6b06-03ba-4151-89a1-87a7eb36e531) · [单元格元数据与数组公式](https://learn.microsoft.com/en-us/dotnet/api/documentformat.openxml.spreadsheet.cellformula)
4. 普通输入引起数组结果变化时，输入和输出作为同一笔修订持久化。重复请求按原始操作摘要返回收据，撤销和重做切换到对应的已核验工作簿快照。

### 第 9 包的结构事务

1. 按当前修订的一基坐标描述剪切块及插入点，插入沿用前一行或列的格式。坐标变换区分物理位置、连续区域和 Excel 错误引用。遇到 `Data!#REF!` 保留限定表名和错误类型。 [Excel 插入位置与格式来源](https://learn.microsoft.com/en-us/office/vba/api/excel.range.insert) · [Excel 剪切](https://learn.microsoft.com/en-us/office/vba/api/excel.range.cut)
2. 在旧坐标还原受影响的共享公式，然后变换同表、跨表、定义名称、打印区域、规则和可移动锚点。公式字符串保持原文，表名中的单引号按 Excel 语法转义。部分移动合并区、表格或透视输出时，在事务确认前返回具体原因。
3. 以文档租约串行确认结构底稿。将格修改与规则先写入临时副本，核对 ZIP 部件后再写日志。混合历史使用原修订的底稿、稀疏值及规则，支持进程重启、请求重试和退回原始坐标。

### 第 10 包的透视与图表

1. 从工作表、透视表和缓存的 OPC 关系绑定源矩形、行字段和输出。按当前修订的公式结果读取源记录，保留旧共享项目编号并登记新增项目。数据和值字段的汇总、总计、项目顺序和手动筛选共同决定输出矩形。 [透视缓存定义](https://learn.microsoft.com/en-us/dotnet/api/documentformat.openxml.spreadsheet.pivotcachedefinition) · [RowItems](https://learn.microsoft.com/en-us/dotnet/api/documentformat.openxml.spreadsheet.rowitems)
2. 同一缓存服务的受支持布局在一个事务里生成。扩张前检查相邻格和其他对象的所有权，缩小时清理释放的格。透视结果产生后的计算和图表缓存都读取同一修订。本簿普通图表的固定序列保留点号、空位和已有点格式，未涉及的系列保留原 XML。 [Excel 刷新透视表](https://learn.microsoft.com/en-us/office/vba/api/excel.pivottable.refreshtable) · [DrawingML 数值缓存](https://learn.microsoft.com/en-us/dotnet/api/documentformat.openxml.drawing.charts.numberingcache)
3. 1.8.29 第一组操作覆盖单行字段、单值字段、五种普通汇总和行项目筛选。1.8.30 扩展到多层行列和分页字段。1.9.1 继续交付值筛选、计算项目、跨缓存级联与专用透视图表，区域编辑见上文第 8 包。

#### 多层布局与分页的计算和写出

1. 用户打开对象工具时按当前修订读取源标题。按名称重绑仍存在的缓存字段，更新缓存字段、透视字段和轴序号。分页候选只在用户展开筛选或查找项目时读取，分页使用带类型的项目身份。已有底稿仍能按原身份继续修订。 [分页字段及项目序号](https://learn.microsoft.com/en-us/dotnet/api/documentformat.openxml.spreadsheet.pagefield)
2. 先应用各分页和手动项目筛选，再由同一批源记录建立两棵有序的层级树。逐条记录累计它所属的行组、列组及祖先交叉点。空组合、零、分类小计和总计各自保留原生含义。重复前缀按字段项目位置编码，恢复时能区分组首、明细、组尾、空行和总计。 [行项目](https://learn.microsoft.com/en-us/dotnet/api/documentformat.openxml.spreadsheet.rowitem)
3. 主结果和分页分别计算占用坐标，检查新增区域与已有内容和对象的交叠。缩小布局时清除旧坐标。按 OOXML 定义顺序写出行列字段、项目和分页，保留表样式、扩展与缓存关系。结果、源记录、依赖公式及普通图表共同进入既有结构事务。 [结果及分页的位置](https://learn.microsoft.com/en-us/dotnet/api/documentformat.openxml.spreadsheet.location)
4. 工作表、缓存和分页筛选分别保留类型。布尔值和错误值在缓存中使用 `b` 和 `e`，Excel 对这两类项目沿用文本组的共享项标志，行列及分页标签显示为文本。日期从已有的数值样式和工作簿日期系统取值，缓存按秒分组，回写标签使用日期序列和同簿样式。重新统计共享项目和内联记录的类型及数值边界。已有日期格式的文本编号保持文本。 [共享项及字段标志](https://learn.microsoft.com/en-us/dotnet/api/documentformat.openxml.spreadsheet.shareditems) · [Excel 日期系统](https://support.microsoft.com/en-us/office/date-systems-in-excel-e7fe7167-48a9-4b96-bb53-5612a800b487)
5. 行列同时启用自定义小计时，同一统计方法计算两组的交集，不同方法的交叉格留空。受筛选的组没有非空记录时保留空值。平均值从交集内的原始记录计算，生成件保留二进制浮点数回读所需的有效位。核对时对 Excel 工作表公式逐字比较，对数值单元格同时记录类型和 `Value2`。Excel 的 `Range.Formula` 也会为数字常数返回经舍入的显示文字，核对器遇到这一情形会检查两端的 `HasFormula`。 [Excel Range.Formula](https://learn.microsoft.com/en-us/office/vba/api/excel.range.formula) · [Range.Value2](https://learn.microsoft.com/en-us/office/vba/api/excel.range.value2)

1.9.1 在同一结构事务中接入下列操作。

| 用户操作 | 实施路径 | 原生核对 |
| --- | --- | --- |
| 添加值筛选和计算项目 | 解析筛选的字段序号、汇总字段及执行顺序，将项目公式绑定到所属字段与缓存项目。将数据源变动、汇总、筛选和总计作为同一修订提交。 | 比对并列前五项、空项、项目公式与手动筛选组合后的输出和字段定义。 [透视筛选](https://learn.microsoft.com/en-us/dotnet/api/documentformat.openxml.spreadsheet.pivotfilter) · [计算项目](https://learn.microsoft.com/en-us/dotnet/api/documentformat.openxml.spreadsheet.calculateditem) |
| 刷新由上游透视供数的缓存及专用透视图表 | 由输出区域与下游源区域建立缓存间依赖。按依赖次序刷新，检测循环，解析图表的透视来源名称和格式标识，再重建类别层级及系列缓存。 | 在同一文件中核对两级缓存、图表类别与值，检查失败回退、撤销、重做、Excel 刷新另存和重开。 [图表的透视来源](https://learn.microsoft.com/en-us/dotnet/api/documentformat.openxml.drawing.charts.pivotsource) |

值筛选使用当前值字段的汇总结果，在同轴父项内评估。值筛选保留另一轴的项目，即使该交叉格在筛选后为空。计数排名包含并列，累计百分比及累计值按项目顺序达到阈值后截止。 [Excel 值筛选类型](https://learn.microsoft.com/en-us/office/vba/api/excel.xlpivotfiltertype)

计算项目先绑定到缓存字段和共享项目编号，再按项目引用排序计算。乘积等非线性表达式在其余可见轴的每个组合上执行，然后参加小计和总计。源缓存记录保持业务行数。刷新时使用本簿公式的反向引用和输入矩形建立缓存依赖，写出前检测循环。专用图表通过透视表名称及格式标识关联，只将可见数据项写入系列缓存，工作表引用仍覆盖原生 Excel 的总计坐标。

## 依据

- SpreadsheetML 的单元格按行、列地址存储。dimension 描述已用区域，可以省略；行 spans 是可选的读优化。 [Microsoft 单元格插入](https://learn.microsoft.com/en-us/office/open-xml/spreadsheet/how-to-insert-text-into-a-cell-in-a-spreadsheet) · [SheetDimension](https://learn.microsoft.com/en-us/dotnet/api/documentformat.openxml.spreadsheet.sheetdimension) · [Row](https://learn.microsoft.com/en-us/dotnet/api/documentformat.openxml.spreadsheet.row)
- 共享公式通过 si、锚点原文及相对位移表达。数组公式的 ref 由锚点拥有，传统数组的结果范围可以包含没有 f 节点的格。 [单元格公式](https://learn.microsoft.com/en-us/dotnet/api/documentformat.openxml.spreadsheet.cellformula) · [数组公式实现说明](https://openpyxl.readthedocs.io/en/stable/simple_formulae.html)
- 公式返回几个值取决于输入区域及选择参数。INDEX 的零或省略下标可能选出整行、整列；MATCH 返回从 1 开始的位置。ROW 和 COLUMN 省略参数时使用公式自己的坐标。编辑器依此判断公式能否写入单格动态数组锚点。 [INDEX](https://support.microsoft.com/en-us/office/index-function-a5dcf0dd-996d-40a4-a822-b56b061328bd) · [MATCH](https://learn.microsoft.com/en-us/office/vba/api/excel.worksheetfunction.match) · [ROW](https://support.microsoft.com/en-us/office/row-function-3a63b74a-c4d0-4093-b49a-e76eb49a6d8d)
- 计算链记录上次计算的顺序。透视缓存的 invalid 和 refreshOnLoad 分别表达缓存失效和打开时刷新；透视字段标题、布局范围及既有快照有独立语义。 [计算链](https://learn.microsoft.com/en-us/office/open-xml/spreadsheet/working-with-the-calculation-chain) · [透视缓存](https://learn.microsoft.com/en-us/dotnet/api/documentformat.openxml.spreadsheet.pivotcachedefinition) · [ECMA-376](https://ecma-international.org/publications-and-standards/standards/ecma-376/)
