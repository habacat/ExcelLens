# XLSB 兼容性矩阵

ExcelLens 按 MS-XLSB 保留单元格来源、公式与保存结果。下表分别记录二进制声明、实际交给工作簿模型的数据和当前网格能力。自动核对以两套 Excel 同源样本及专项异常用例为准。桌面人工逐像素验收仍未执行。[微软 MS-XLSB](https://learn.microsoft.com/en-us/openspecs/office_file_formats/ms-xlsb/acc8aa92-1f02-4167-99f5-84f9f676b95a)

| 场景 | BIFF12 记录或结构 | 解析和界面结果 | 当前证据 |
| --- | --- | --- | --- |
| 工作表、共享文本、数字和日期 | `BrtBundleSh`、`BrtWbProp`、`BrtSSTItem`、`BrtCellIsst`、`BrtCellRk`、`BrtCellReal` | 保留源坐标、数据类型、原值、显隐状态和 1900／1904 日期口径。网格按样式显示，统计用原值。 | 两组 Excel 同源、虚拟网格下游回归 |
| 单元格、跨表、共享、数组和结构化公式 | `ParsedFormula`、`BrtShrFmla`、`BrtArrFmla`、`PtgRef3d`、`PtgList` | 公式栏显示可还原表达式，缓存保持原类型。数组导出时保留锚点范围及成员缓存。 | 同源抽查，HTTP 搜索和 XLSX 回读 |
| 图案和渐变填充 | `BrtFill` | 无填充与纯色可呈现，图案和渐变的多层信息会触发工作簿呈现提示。 | 纯色样本通过，多层效果依据解析实现核对，待专门实表覆盖 |
| 对角线边框 | `BrtBorder` | 可显示四周边框。检测到对角线时显示呈现范围提示。 | 四周样式同源；对角线待专门实表覆盖 |
| 特殊对齐与文字方向 | `BrtXF` | 常用水平、垂直对齐与换行接入网格。缩进、部分方向及缩小字体以提示标明。 | 常规对齐同源，特殊组合待图样核对 |
| 条件格式数据条 | `BrtBeginDatabar`、`BrtBeginDatabar14`、`BrtCFVO14`、`BrtCFRuleExt` | 保留规则、阈值、颜色和可核实的扩展参数。规则面板显示待求值状态。 | Excel 同源扩展参数与颜色断言 |
| 条件格式色阶 | `BrtBeginColorScale`、`BrtCFVO`、`BrtColor` | 规则和参数可查，网格对色阶显示待求值状态。 | 同源规则数量及待求值状态断言 |
| 条件格式图标集 | `BrtBeginIconSet`、`BrtBeginIconSet14`、`BrtCFIcon` | 读取可识别的规则与图标参数。网格显示待求值状态。 | 代码与记录结构核对，待含自定义图标的 Excel 同源样本 |
| 外部工作簿名称引用 | `BrtSupBookSrc`、`BrtExternSheet`、`PtgNameX` | 公式栏标明外部名称无法还原，使用源公式缓存。 | 明确的外部名称分支及未知令牌缓存回归，待外部名称专门实表 |
| 注音文本 | `RichStr` 注音标志 | 基础文本可读，单元格信息呈现注音限制。 | 富文本同源，注音层待专门实表 |
| 图表页与宏表 | `BrtBundleSh` 的 OPC 关系 | 网格呈现普通工作表，其他页类型触发工作簿通知。 | 关系分派与提示代码核对，待混合页专门实表 |
| 批注、链接、图像、名称、验证和打印 | `BrtComment*`、`BrtHLink`、`BrtDrawing`、`BrtName`、`BrtDVal`、`BrtPageSetup` | 接入相应查看入口。测试夹具保留打印区域、重复标题与分页，剥离本机打印设备配置。 | 同源元数据断言和共有的只读下游回归 |

## 复现

完整源码自带 `internal/workbook/testdata/xlsb-excel/` 中的两个 Excel 生成的配对样本，共四个小文件。数据、注释、图片和地址均为人工构造的合成内容。清理作者、批注者、设备配置与绝对路径后固定 SHA-256。原生二进制工作表、公式及样式由 Excel 生成。

```powershell
go test ./internal/workbook ./internal/server -run '^TestXLSBExcel' -count=1
```

默认读取仓库内的样本并核对固定摘要。项目门禁和 CI 都会运行这些用例。设置 `EXCELLENS_XLSB_EXCEL_PAIRS` 可使用同样结构的本机试样补充验证。

在装有 Excel 和 pywin32 的 Windows 开发机上，可运行以下命令重新制作合成对照样本。新生成的文件用于语义复核，Excel 版本和自动生成标识可能改变归档摘要。程序运行和常规 CI 只读取已保存的样本。

```powershell
python tools/qa/generate_xlsb_pairs.py validation/1.17.0/private/xlsb-pairs
```
