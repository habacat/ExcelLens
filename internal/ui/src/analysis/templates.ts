/**
 * Metadata-driven analysis templates: every template is executable through
 * the same verified pivot engine. Hints map to actual field names at runtime.
 */
import type { AnalysisAggregateKind } from '../contracts/analysis.ts';
import { t, t as localize } from '../core/i18n.ts';

const categories: Readonly<Record<string, string>> = {
  general: t('ui.templates.general'),
  audit: t('ui.templates.audit'),
  investment: t('ui.templates.investment'),
  finance: t('ui.templates.finance'),
  quality: t('ui.templates.quality'),
};

export function templateCategoryLabel(category: string): string {
  return categories[category] ?? category;
}

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
  readonly dateGrouping?: 'year' | 'yearmonth';
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
  dateGrouping?: AnalysisTemplate['dateGrouping'],
): AnalysisTemplate {
  return { category, title, icon, description, chart, aggregate, row, value, col, top, dateGrouping };
}

export const ANALYSIS_TEMPLATES: readonly AnalysisTemplate[] = [
  makeTemplate('general', localize('ui.templates.classificationScaleRanking'), '▥', localize('ui.templates.calculateTotalAmountsCategorySort'), 'horizontal', 'sum', ['类别', '类型', '分类'], ['金额', '余额', '规模', '价值']),
  makeTemplate('general', localize('ui.templates.numberCategoryRecords'), '☷', localize('ui.templates.summarizeNumberRecordsCategory'), 'bar', 'count', ['类别', '类型', '分类'], []),
  makeTemplate('general', localize('ui.templates.monthlyTrendChanges'), '⌁', localize('ui.templates.summarizeDataChangesMonth'), 'line', 'sum', ['月份', '月', '日期', '时间'], ['金额', '余额', '规模'], [], 30, 'yearmonth'),
  makeTemplate('general', localize('ui.templates.structuralProportion'), '◔', localize('ui.templates.observeProportionDifferentCategories'), 'donut', 'sum', ['类别', '类型', '渠道'], ['金额', '余额', '规模'], [], 16),
  makeTemplate('general', localize('ui.templates.regionalCrossAnalysis'), '⊞', localize('ui.templates.crossSummaryRegionBusinessType'), 'stacked', 'sum', ['省', '地区', '城市', '机构'], ['金额', '规模'], ['类别', '渠道', '类型']),
  makeTemplate('general', localize('ui.templates.averageComparison'), '⊟', localize('ui.templates.comparingAverageLevelsEachGroup'), 'horizontal', 'average', ['类型', '机构', '部门'], ['金额', '价格', '费率']),
  makeTemplate('general', localize('ui.templates.maximumValueComparison'), '↥', localize('ui.templates.identifyMaximumValueEachGroup'), 'bar', 'max', ['类型', '分类', '机构'], ['金额', '余额']),
  makeTemplate('general', localize('ui.templates.minimumValueComparison'), '↧', localize('ui.templates.identifyMinimumValueEachGroup'), 'bar', 'min', ['类型', '分类', '机构'], ['金额', '余额']),
  makeTemplate('audit', localize('ui.templates.institutionAmountConcentration'), '◎', localize('ui.templates.identifyDegreeConcentrationAmountsBetween'), 'horizontal', 'sum', ['机构', '分公司', '中支'], ['余额', '金额', '规模'], [], 30),
  makeTemplate('audit', localize('ui.templates.concentrationCounterparties'), '◇', localize('ui.templates.calculateCounterpartyExposure'), 'horizontal', 'sum', ['交易对手', '对手', '发行人', '融资人', '主体'], ['余额', '金额'], [], 25),
  makeTemplate('audit', localize('ui.templates.handlerSBusinessVolume'), '▧', localize('ui.templates.countRecordsHandler'), 'bar', 'count', ['经办人', '操作人', '制单人', '申请人'], []),
  makeTemplate('audit', localize('ui.templates.businessTypeCrossing'), '▤', localize('ui.templates.multiDimensionalCrossStatisticalBusiness'), 'stacked', 'count', ['机构', '分公司'], [], ['业务类型', '类别', '类型']),
  makeTemplate('audit', localize('ui.templates.concentrationPayees'), '◉', localize('ui.templates.identifyingPayeeCentralizedTransactions'), 'horizontal', 'sum', ['收款人', '收款户名', '对方户名'], ['金额', '支付金额', '报销金额']),
  makeTemplate('audit', localize('ui.templates.accountRecordDistribution'), '▦', localize('ui.templates.countBusinessVolumeAccountDimension'), 'bar', 'count', ['账户', '账号', '账户名称'], [], [], 40),
  makeTemplate('audit', localize('ui.templates.locateAbnormalValuesAmounts'), '◫', localize('ui.templates.displayGroupedMaximumTransactionAmount'), 'horizontal', 'max', ['机构', '收款人', '主体'], ['金额']),
  makeTemplate('audit', localize('ui.templates.problemClassificationStatistics'), '▣', localize('ui.templates.countQuantitiesIssueType'), 'pie', 'count', ['问题类型', '风险类型', '问题分类'], [], [], 15),
  makeTemplate('investment', localize('ui.templates.assetCategoryConfiguration'), '◈', localize('ui.templates.assetCategoryBookValueStructure'), 'donut', 'sum', ['资产类型', '投资品种', '资产类别'], ['账面价值', '账面余额', '余额']),
  makeTemplate('investment', localize('ui.templates.portfolioDistribution'), '▥', localize('ui.templates.calculateAmountsInvestmentPortfolio'), 'horizontal', 'sum', ['投资组合', '账户', '组合名称'], ['账面价值', '余额', '成本']),
  makeTemplate('investment', localize('ui.templates.issuerExposure'), '◎', localize('ui.templates.rankingIssuingEntityAmounts'), 'horizontal', 'sum', ['发行人', '原始权益人', '融资人'], ['余额', '账面价值', '金额'], [], 25),
  makeTemplate('investment', localize('ui.templates.investmentManagerPositions'), '⊟', localize('ui.templates.calculateAssetScaleManager'), 'bar', 'sum', ['投资经理', '管理人', '受托人'], ['账面价值', '余额']),
  makeTemplate('investment', localize('ui.templates.productQuantityDistribution'), '☷', localize('ui.templates.countNumberInvestmentProducts'), 'bar', 'count', ['产品类型', '投资品种', '资产类别'], []),
  makeTemplate('investment', localize('ui.templates.dueYearStructure'), '▯', localize('ui.templates.summarizeValuesDueYear'), 'line', 'sum', ['到期年度', '到期年', '到期日'], ['余额', '账面价值'], [], 40, 'year'),
  makeTemplate('investment', localize('ui.templates.yieldComparison'), '⌁', localize('ui.templates.comparingYieldsDifferentAssetCategories'), 'bar', 'average', ['资产类型', '品种'], ['收益率', '票面利率', 'IRR']),
  makeTemplate('investment', localize('ui.templates.accountingCategoryConfiguration'), '◔', localize('ui.templates.displayAssetStructureAccountingCategory'), 'donut', 'sum', ['会计分类', '会计科目', '计量分类'], ['账面价值', '余额']),
  makeTemplate('finance', localize('ui.templates.rankingExpenseItems'), '▥', localize('ui.templates.aggregateAmountsCostItem'), 'horizontal', 'sum', ['费用项目', '费用类型', '科目'], ['报销金额', '费用金额', '金额']),
  makeTemplate('finance', localize('ui.templates.departmentCostStructure'), '▤', localize('ui.templates.crossAnalysisDepartmentsCostCategories'), 'stacked', 'sum', ['部门', '机构'], ['金额', '费用'], ['费用类型', '科目']),
  makeTemplate('finance', localize('ui.templates.monthlyExpenditureTrends'), '⌁', localize('ui.templates.trackExpenditureFluctuationsMonthly'), 'line', 'sum', ['月份', '支付日期', '记账日期'], ['支付金额', '金额'], [], 30, 'yearmonth'),
  makeTemplate('finance', localize('ui.templates.customerContributionRanking'), '↗', localize('ui.templates.summarizeAccordingCustomerRevenue'), 'horizontal', 'sum', ['客户', '客户名称', '投保人'], ['收入', '保费', '金额']),
  makeTemplate('finance', localize('ui.templates.revenueChannelStructure'), '◔', localize('ui.templates.distributionIncomeFromDifferentChannels'), 'pie', 'sum', ['渠道', '来源'], ['收入', '保费'], [], 18),
  makeTemplate('finance', localize('ui.templates.distributionPaymentAccounts'), '▦', localize('ui.templates.calculateAmountsPaymentAccount'), 'bar', 'sum', ['账户', '收款人', '客户'], ['回款金额', '金额']),
  makeTemplate('quality', localize('ui.templates.classificationIntegrityCheck'), '☷', localize('ui.templates.identifyCategoriesBlankCollectionsField'), 'bar', 'count', ['类别', '类型', '机构'], ['编号', '名称']),
  makeTemplate('quality', localize('ui.templates.duplicateIdentificationObservation'), '≋', localize('ui.templates.countDifferentIdentifiersEachGroup'), 'horizontal', 'distinct', ['机构', '类别'], ['客户号', '编号', '流水号']),
  makeTemplate('quality', localize('ui.templates.statusDistribution'), '▤', localize('ui.templates.comparingNumberRecordsEachBusiness'), 'donut', 'count', ['状态', '处理状态', '审批状态'], []),
  makeTemplate('quality', localize('ui.templates.personnelBusinessDistribution'), '◫', localize('ui.templates.processVolumePersonnelStatus'), 'heatmap', 'count', ['经办人', '审核人', '负责人'], [], ['状态', '结果']),
  makeTemplate('quality', localize('ui.templates.errorTypeDistribution'), '▣', localize('ui.templates.countOccurrencesErrorCategories'), 'bar', 'count', ['错误类型', '问题类型', '异常原因'], []),
  makeTemplate('quality', localize('ui.templates.crossDepartmentalDifferences'), '⧉', localize('ui.templates.crossComparingDifferentDepartmentBusiness'), 'heatmap', 'count', ['部门', '机构'], [], ['类别', '类型']),
];
