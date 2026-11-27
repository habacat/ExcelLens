/**
 * Metadata-driven analysis templates: every template is executable through
 * the same verified pivot engine. Hints map to actual field names at runtime.
 */
import type { AnalysisAggregateKind } from '../contracts/analysis.ts';

export interface AnalysisTemplate {
  readonly category: string;
  readonly title: string;
  readonly icon: string;
  readonly description: string;
  readonly chart: string;
  readonly aggregate: AnalysisAggregateKind;
  readonly row: readonly string[];
  readonly value: readonly string[];
  readonly col: readonly string[];
  readonly top: number;
}

function makeTemplate(
  category: string,
  title: string,
  icon: string,
  description: string,
  chart: string,
  aggregate: AnalysisAggregateKind,
  row: readonly string[],
  value: readonly string[],
  col: readonly string[] = [],
  top = 30,
): AnalysisTemplate {
  return { category, title, icon, description, chart, aggregate, row, value, col, top };
}

export const ANALYSIS_TEMPLATES: readonly AnalysisTemplate[] = [
  makeTemplate('通用', '分类规模排行', '▥', '按类别计算总额并排序', 'horizontal', 'sum', ['类别', '类型', '分类'], ['金额', '余额', '规模', '价值']),
  makeTemplate('通用', '分类记录数', '☷', '按类别汇总记录数量', 'bar', 'count', ['类别', '类型', '分类'], []),
  makeTemplate('通用', '月度变化趋势', '⌁', '按月份汇总数据变化', 'line', 'sum', ['月份', '月', '日期', '时间'], ['金额', '余额', '规模']),
  makeTemplate('通用', '结构占比', '◔', '观察不同分类的占比', 'donut', 'sum', ['类别', '类型', '渠道'], ['金额', '余额', '规模'], [], 16),
  makeTemplate('通用', '地区交叉分析', '⊞', '按地区与业务类型交叉汇总', 'stacked', 'sum', ['省', '地区', '城市', '机构'], ['金额', '规模'], ['类别', '渠道', '类型']),
  makeTemplate('通用', '平均水平比较', '⊟', '比较各组平均水平', 'horizontal', 'average', ['类型', '机构', '部门'], ['金额', '价格', '费率']),
  makeTemplate('通用', '最大值对比', '↥', '识别各组最大值', 'bar', 'max', ['类型', '分类', '机构'], ['金额', '余额']),
  makeTemplate('通用', '最小值对比', '↧', '识别各组最小值', 'bar', 'min', ['类型', '分类', '机构'], ['金额', '余额']),
  makeTemplate('审计', '机构金额集中度', '◎', '识别机构间的金额集中程度', 'horizontal', 'sum', ['机构', '分公司', '中支'], ['余额', '金额', '规模'], [], 30),
  makeTemplate('审计', '交易对手集中度', '◇', '统计交易对手敞口', 'horizontal', 'sum', ['交易对手', '对手', '发行人', '融资人', '主体'], ['余额', '金额'], [], 25),
  makeTemplate('审计', '经办人业务量', '▧', '按经办人统计记录数量', 'bar', 'count', ['经办人', '操作人', '制单人', '申请人'], []),
  makeTemplate('审计', '业务类型交叉', '▤', '多维度交叉统计业务分布', 'stacked', 'count', ['机构', '分公司'], [], ['业务类型', '类别', '类型']),
  makeTemplate('审计', '收款人集中程度', '◉', '识别收款人集中交易', 'horizontal', 'sum', ['收款人', '收款户名', '对方户名'], ['金额', '支付金额', '报销金额']),
  makeTemplate('审计', '账户记录分布', '▦', '按账户维度统计业务数量', 'bar', 'count', ['账户', '账号', '账户名称'], [], [], 40),
  makeTemplate('审计', '金额异常值定位', '◫', '展示分组最大交易金额', 'horizontal', 'max', ['机构', '收款人', '主体'], ['金额']),
  makeTemplate('审计', '问题分类统计', '▣', '按问题类型统计数量', 'pie', 'count', ['问题类型', '风险类型', '问题分类'], [], [], 15),
  makeTemplate('投资', '资产类别配置', '◈', '资产类别与账面价值结构', 'donut', 'sum', ['资产类型', '投资品种', '资产类别'], ['账面价值', '账面余额', '余额']),
  makeTemplate('投资', '投资组合分布', '▥', '按投资组合统计金额', 'horizontal', 'sum', ['投资组合', '账户', '组合名称'], ['账面价值', '余额', '成本']),
  makeTemplate('投资', '发行人敞口', '◎', '发行主体金额排行', 'horizontal', 'sum', ['发行人', '原始权益人', '融资人'], ['余额', '账面价值', '金额'], [], 25),
  makeTemplate('投资', '投资经理持仓', '⊟', '按管理人统计资产规模', 'bar', 'sum', ['投资经理', '管理人', '受托人'], ['账面价值', '余额']),
  makeTemplate('投资', '产品数量分布', '☷', '统计投资产品数量', 'bar', 'count', ['产品类型', '投资品种', '资产类别'], []),
  makeTemplate('投资', '到期年份结构', '▯', '按到期年份汇总价值', 'line', 'sum', ['到期年度', '到期年', '到期日'], ['余额', '账面价值'], [], 40),
  makeTemplate('投资', '收益率比较', '⌁', '比较不同资产类别收益率', 'bar', 'average', ['资产类型', '品种'], ['收益率', '票面利率', 'IRR']),
  makeTemplate('投资', '会计分类配置', '◔', '按会计分类展示资产结构', 'donut', 'sum', ['会计分类', '会计科目', '计量分类'], ['账面价值', '余额']),
  makeTemplate('财务', '费用项目排行', '▥', '按费用项目归集金额', 'horizontal', 'sum', ['费用项目', '费用类型', '科目'], ['报销金额', '费用金额', '金额']),
  makeTemplate('财务', '部门费用结构', '▤', '部门与费用科目交叉分析', 'stacked', 'sum', ['部门', '机构'], ['金额', '费用'], ['费用类型', '科目']),
  makeTemplate('财务', '月度支出趋势', '⌁', '按月跟踪支出波动', 'line', 'sum', ['月份', '支付日期', '记账日期'], ['支付金额', '金额']),
  makeTemplate('财务', '客户贡献排行', '↗', '按客户收入汇总', 'horizontal', 'sum', ['客户', '客户名称', '投保人'], ['收入', '保费', '金额']),
  makeTemplate('财务', '收入渠道结构', '◔', '不同渠道收入分布', 'pie', 'sum', ['渠道', '来源'], ['收入', '保费'], [], 18),
  makeTemplate('财务', '回款账户分布', '▦', '按收款账户统计金额', 'bar', 'sum', ['账户', '收款人', '客户'], ['回款金额', '金额']),
  makeTemplate('质量', '分类完整性检查', '☷', '按照字段计数识别空白集中类别', 'bar', 'count', ['类别', '类型', '机构'], ['编号', '名称']),
  makeTemplate('质量', '重复标识观察', '≋', '统计各组不同标识数量', 'horizontal', 'distinct', ['机构', '类别'], ['客户号', '编号', '流水号']),
  makeTemplate('质量', '状态分布', '▤', '比较各业务状态记录数量', 'donut', 'count', ['状态', '处理状态', '审批状态'], []),
  makeTemplate('质量', '人员业务分布', '◫', '按人员与状态统计处理量', 'heatmap', 'count', ['经办人', '审核人', '负责人'], [], ['状态', '结果']),
  makeTemplate('质量', '错误类型分布', '▣', '统计错误类别出现次数', 'bar', 'count', ['错误类型', '问题类型', '异常原因'], []),
  makeTemplate('质量', '跨部门差异', '⧉', '交叉比较不同部门业务类别', 'heatmap', 'count', ['部门', '机构'], [], ['类别', '类型']),
];
