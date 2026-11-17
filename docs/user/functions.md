# 计算函数清单

当前共 243 个函数名，包含兼容别名；`AVG` 是额外提供的便利别名。

本清单由 `go run ./tools/project generate` 从计算引擎共享目录生成，界面补全和 HTTP 接口使用同一目录。登记的函数不代表已实现 Excel 的全部参数形式；范围、精度和已知边界见 [计算说明](calculator.md)。

| 函数 | 参数 |
| --- | --- |
| `ABS` | `ABS(数值)` |
| `ACOS` | `ACOS(数值)` |
| `ACOSH` | `ACOSH(数值)` |
| `ADDRESS` | `ADDRESS(行号, 列号, [绝对类型], [A1样式], [工作表])` |
| `AND` | `AND(条件1, …)` |
| `ASIN` | `ASIN(数值)` |
| `ASINH` | `ASINH(数值)` |
| `ATAN` | `ATAN(数值)` |
| `ATAN2` | `ATAN2(数值1, 数值2)` |
| `ATANH` | `ATANH(数值)` |
| `AVEDEV` | `AVEDEV(数值1, …)` |
| `AVERAGE` | `AVERAGE(数值1, …)` |
| `AVERAGEIF` | `AVERAGEIF(条件区域, 条件, [数值区域])` |
| `AVERAGEIFS` | `AVERAGEIFS(数值区域, 条件区域1, 条件1, …)` |
| `AVG` | `AVG(数值1, …)` |
| `CEILING` | `CEILING(数值, 基数)` |
| `CEILING.MATH` | `CEILING.MATH(数值, [基数], [模式])` |
| `CEILING.PRECISE` | `CEILING.PRECISE(数值, [基数])` |
| `CHAR` | `CHAR(值)` |
| `CHOOSE` | `CHOOSE(序号, 值1, …)` |
| `CHOOSECOLS` | `CHOOSECOLS(数组, 序号1, …)` |
| `CHOOSEROWS` | `CHOOSEROWS(数组, 序号1, …)` |
| `CLEAN` | `CLEAN(值)` |
| `CODE` | `CODE(值)` |
| `COLUMN` | `COLUMN(区域)` |
| `COLUMNS` | `COLUMNS(区域)` |
| `COMBIN` | `COMBIN(数值1, 数值2)` |
| `COMBINA` | `COMBINA(数值1, 数值2)` |
| `CONCAT` | `CONCAT(文本1, …)` |
| `CONCATENATE` | `CONCATENATE(文本1, …)` |
| `CORREL` | `CORREL(数组1, 数组2)` |
| `COS` | `COS(数值)` |
| `COSH` | `COSH(数值)` |
| `COUNT` | `COUNT(数值1, …)` |
| `COUNTA` | `COUNTA(数值1, …)` |
| `COUNTBLANK` | `COUNTBLANK(区域)` |
| `COUNTIF` | `COUNTIF(条件区域, 条件)` |
| `COUNTIFS` | `COUNTIFS(条件区域1, 条件1, …)` |
| `COVAR` | `COVAR(数组1, 数组2)` |
| `COVARIANCE.P` | `COVARIANCE.P(数组1, 数组2)` |
| `COVARIANCE.S` | `COVARIANCE.S(数组1, 数组2)` |
| `DATE` | `DATE(年, 月, 日)` |
| `DATEDIF` | `DATEDIF(起始日期, 结束日期, 单位)` |
| `DATEVALUE` | `DATEVALUE(日期或时间)` |
| `DAY` | `DAY(日期或时间)` |
| `DAYS` | `DAYS(日期, 参数)` |
| `DAYS360` | `DAYS360(起始日期, 结束日期, [方法])` |
| `DDB` | `DDB(原值, 残值, 使用年限, 期间, [倍率])` |
| `DEGREES` | `DEGREES(数值)` |
| `DEVSQ` | `DEVSQ(数值1, …)` |
| `DOLLARDE` | `DOLLARDE(金额, 分母)` |
| `DOLLARFR` | `DOLLARFR(金额, 分母)` |
| `DROP` | `DROP(数组, 行数, [列数])` |
| `EDATE` | `EDATE(日期, 参数)` |
| `EFFECT` | `EFFECT(利率, 每年复利次数)` |
| `EOMONTH` | `EOMONTH(日期, 参数)` |
| `ERROR.TYPE` | `ERROR.TYPE(值)` |
| `EVEN` | `EVEN(数值)` |
| `EXACT` | `EXACT(文本1, 文本2)` |
| `EXP` | `EXP(数值)` |
| `FACT` | `FACT(数值)` |
| `FACTDOUBLE` | `FACTDOUBLE(数值)` |
| `FALSE` | `FALSE()` |
| `FILTER` | `FILTER(数组, 条件数组, [无结果时])` |
| `FIND` | `FIND(查找文本, 文本, [起点])` |
| `FIXED` | `FIXED(数值, [小数位数], [不分组])` |
| `FLOOR` | `FLOOR(数值, 基数)` |
| `FLOOR.MATH` | `FLOOR.MATH(数值, [基数], [模式])` |
| `FLOOR.PRECISE` | `FLOOR.PRECISE(数值, [基数])` |
| `FORECAST` | `FORECAST(预测点, 已知Y, 已知X)` |
| `FORECAST.LINEAR` | `FORECAST.LINEAR(预测点, 已知Y, 已知X)` |
| `FV` | `FV(利率, 期数, 金额, [末值], [期初期末])` |
| `GCD` | `GCD(数值1, …)` |
| `GEOMEAN` | `GEOMEAN(数值1, …)` |
| `HARMEAN` | `HARMEAN(数值1, …)` |
| `HLOOKUP` | `HLOOKUP(查找值, 数据区域, 返回序号, [近似匹配])` |
| `HOUR` | `HOUR(日期或时间)` |
| `HSTACK` | `HSTACK(数组1, …)` |
| `IF` | `IF(条件, 成立时, 不成立时)` |
| `IFERROR` | `IFERROR(公式, 出错时)` |
| `IFNA` | `IFNA(表达式, 未找到时)` |
| `IFS` | `IFS(条件1, 值1, …)` |
| `INDEX` | `INDEX(区域, 行号, [列号])` |
| `INDIRECT` | `INDIRECT(地址文本, [A1样式])` |
| `INT` | `INT(数值)` |
| `INTERCEPT` | `INTERCEPT(数组1, 数组2)` |
| `IPMT` | `IPMT(利率, 期间, 总期数, 现值, [末值], [期初期末])` |
| `IRR` | `IRR(现金流区域, [猜测值])` |
| `ISBLANK` | `ISBLANK(值)` |
| `ISERR` | `ISERR(值)` |
| `ISERROR` | `ISERROR(值)` |
| `ISEVEN` | `ISEVEN(值)` |
| `ISLOGICAL` | `ISLOGICAL(值)` |
| `ISNA` | `ISNA(值)` |
| `ISNONTEXT` | `ISNONTEXT(值)` |
| `ISNUMBER` | `ISNUMBER(值)` |
| `ISODD` | `ISODD(值)` |
| `ISOWEEKNUM` | `ISOWEEKNUM(日期或时间)` |
| `ISREF` | `ISREF(值)` |
| `ISTEXT` | `ISTEXT(值)` |
| `LARGE` | `LARGE(区域, 参数)` |
| `LCM` | `LCM(数值1, …)` |
| `LEFT` | `LEFT(文本, 字符数)` |
| `LEN` | `LEN(值)` |
| `LET` | `LET(名称1, 值1, …, 计算表达式)` |
| `LN` | `LN(数值)` |
| `LOG` | `LOG(数值, [参数])` |
| `LOG10` | `LOG10(数值)` |
| `LOOKUP` | `LOOKUP(查找值, 查找区域, [返回区域])` |
| `LOWER` | `LOWER(值)` |
| `MATCH` | `MATCH(查找值, 区域, [匹配方式])` |
| `MAX` | `MAX(数值1, …)` |
| `MAXIFS` | `MAXIFS(数值区域, 条件区域1, 条件1, …)` |
| `MEDIAN` | `MEDIAN(数值1, …)` |
| `MID` | `MID(文本, 起点, 字符数)` |
| `MIN` | `MIN(数值1, …)` |
| `MINIFS` | `MINIFS(数值区域, 条件区域1, 条件1, …)` |
| `MINUTE` | `MINUTE(日期或时间)` |
| `MIRR` | `MIRR(现金流区域, 融资利率, 再投资利率)` |
| `MOD` | `MOD(数值1, 数值2)` |
| `MODE` | `MODE(数值1, …)` |
| `MODE.SNGL` | `MODE.SNGL(数值1, …)` |
| `MONTH` | `MONTH(日期或时间)` |
| `MROUND` | `MROUND(数值1, 数值2)` |
| `N` | `N(值)` |
| `NA` | `NA()` |
| `NETWORKDAYS` | `NETWORKDAYS(起始日期, 结束日期或天数, [节假日])` |
| `NETWORKDAYS.INTL` | `NETWORKDAYS.INTL(起始日期, 结束日期或天数, [周末], [节假日])` |
| `NOMINAL` | `NOMINAL(利率, 每年复利次数)` |
| `NORM.DIST` | `NORM.DIST(数值, 均值, 标准差, 累计)` |
| `NORM.INV` | `NORM.INV(概率, 均值, 标准差)` |
| `NORM.S.DIST` | `NORM.S.DIST(数值, 累计)` |
| `NORM.S.INV` | `NORM.S.INV(概率)` |
| `NOT` | `NOT(条件)` |
| `NOW` | `NOW()` |
| `NPER` | `NPER(利率, 每期支付, 现值, [末值], [期初期末])` |
| `NPV` | `NPV(贴现率, 现金流1, …)` |
| `NUMBERVALUE` | `NUMBERVALUE(文本, [小数分隔符], [分组分隔符])` |
| `ODD` | `ODD(数值)` |
| `OFFSET` | `OFFSET(区域, 偏移行, 偏移列, [高度], [宽度])` |
| `OR` | `OR(条件1, …)` |
| `PDURATION` | `PDURATION(期数或利率, 现值, 终值)` |
| `PEARSON` | `PEARSON(数组1, 数组2)` |
| `PERCENTILE` | `PERCENTILE(区域, 参数)` |
| `PERCENTILE.EXC` | `PERCENTILE.EXC(区域, 参数)` |
| `PERCENTILE.INC` | `PERCENTILE.INC(区域, 参数)` |
| `PERMUT` | `PERMUT(数值1, 数值2)` |
| `PERMUTATIONA` | `PERMUTATIONA(数值1, 数值2)` |
| `PI` | `PI()` |
| `PMT` | `PMT(利率, 期数, 金额, [末值], [期初期末])` |
| `POWER` | `POWER(数值1, 数值2)` |
| `PPMT` | `PPMT(利率, 期间, 总期数, 现值, [末值], [期初期末])` |
| `PRODUCT` | `PRODUCT(数值1, …)` |
| `PROPER` | `PROPER(值)` |
| `PV` | `PV(利率, 期数, 金额, [末值], [期初期末])` |
| `QUARTILE` | `QUARTILE(区域, 参数)` |
| `QUARTILE.EXC` | `QUARTILE.EXC(区域, 参数)` |
| `QUARTILE.INC` | `QUARTILE.INC(区域, 参数)` |
| `QUOTIENT` | `QUOTIENT(数值1, 数值2)` |
| `RADIANS` | `RADIANS(数值)` |
| `RANK` | `RANK(数值, 区域, [顺序])` |
| `RANK.AVG` | `RANK.AVG(数值, 区域, [顺序])` |
| `RANK.EQ` | `RANK.EQ(数值, 区域, [顺序])` |
| `RATE` | `RATE(期数, 每期支付, 现值, [末值], [期初期末], [猜测值])` |
| `REPLACE` | `REPLACE(文本, 起点, 字符数, 新文本)` |
| `REPT` | `REPT(文本, 次数)` |
| `RIGHT` | `RIGHT(文本, 字符数)` |
| `ROUND` | `ROUND(数值, 位数)` |
| `ROUNDDOWN` | `ROUNDDOWN(数值, 位数)` |
| `ROUNDUP` | `ROUNDUP(数值, 位数)` |
| `ROW` | `ROW(区域)` |
| `ROWS` | `ROWS(区域)` |
| `RRI` | `RRI(期数或利率, 现值, 终值)` |
| `RSQ` | `RSQ(数组1, 数组2)` |
| `SEARCH` | `SEARCH(查找文本, 文本, [起点])` |
| `SECOND` | `SECOND(日期或时间)` |
| `SEQUENCE` | `SEQUENCE(行数, [列数], [起点], [步长])` |
| `SIGN` | `SIGN(数值)` |
| `SIN` | `SIN(数值)` |
| `SINH` | `SINH(数值)` |
| `SLN` | `SLN(原值, 残值, 使用年限)` |
| `SLOPE` | `SLOPE(数组1, 数组2)` |
| `SMALL` | `SMALL(区域, 参数)` |
| `SORT` | `SORT(数组, [排序序号], [顺序], [按列排序])` |
| `SQRT` | `SQRT(数值)` |
| `SQRTPI` | `SQRTPI(数值)` |
| `STDEV` | `STDEV(数值1, …)` |
| `STDEV.P` | `STDEV.P(数值1, …)` |
| `STDEV.S` | `STDEV.S(数值1, …)` |
| `STDEVP` | `STDEVP(数值1, …)` |
| `STEYX` | `STEYX(数组1, 数组2)` |
| `SUBSTITUTE` | `SUBSTITUTE(文本, 原文本, 新文本, [第几次])` |
| `SUM` | `SUM(数值1, …)` |
| `SUMIF` | `SUMIF(条件区域, 条件, [数值区域])` |
| `SUMIFS` | `SUMIFS(数值区域, 条件区域1, 条件1, …)` |
| `SUMPRODUCT` | `SUMPRODUCT(区域1, [区域2], …)` |
| `SUMSQ` | `SUMSQ(数值1, …)` |
| `SUMX2MY2` | `SUMX2MY2(数组1, 数组2)` |
| `SUMX2PY2` | `SUMX2PY2(数组1, 数组2)` |
| `SUMXMY2` | `SUMXMY2(数组1, 数组2)` |
| `SWITCH` | `SWITCH(表达式, 匹配值1, 返回值1, …)` |
| `SYD` | `SYD(原值, 残值, 使用年限, 期间)` |
| `T` | `T(值)` |
| `TAKE` | `TAKE(数组, 行数, [列数])` |
| `TAN` | `TAN(数值)` |
| `TANH` | `TANH(数值)` |
| `TEXT` | `TEXT(值, 格式)` |
| `TEXTAFTER` | `TEXTAFTER(文本, 分隔符, [第几次], [忽略大小写], [匹配末尾], [未找到时])` |
| `TEXTBEFORE` | `TEXTBEFORE(文本, 分隔符, [第几次], [忽略大小写], [匹配末尾], [未找到时])` |
| `TEXTJOIN` | `TEXTJOIN(分隔符, 忽略空值, 文本1, …)` |
| `TEXTSPLIT` | `TEXTSPLIT(文本, 列分隔符, [行分隔符], [忽略空项], [忽略大小写], [填充值])` |
| `TIME` | `TIME(时, 分, 秒)` |
| `TIMEVALUE` | `TIMEVALUE(日期或时间)` |
| `TOCOL` | `TOCOL(数组, [忽略项], [按列扫描])` |
| `TODAY` | `TODAY()` |
| `TOROW` | `TOROW(数组, [忽略项], [按列扫描])` |
| `TRANSPOSE` | `TRANSPOSE(数组)` |
| `TRIM` | `TRIM(值)` |
| `TRUE` | `TRUE()` |
| `TRUNC` | `TRUNC(数值, [参数])` |
| `TYPE` | `TYPE(值)` |
| `UNICHAR` | `UNICHAR(值)` |
| `UNICODE` | `UNICODE(值)` |
| `UNIQUE` | `UNIQUE(数组, [按列], [仅出现一次])` |
| `UPPER` | `UPPER(值)` |
| `VALUE` | `VALUE(值)` |
| `VAR` | `VAR(数值1, …)` |
| `VAR.P` | `VAR.P(数值1, …)` |
| `VAR.S` | `VAR.S(数值1, …)` |
| `VARP` | `VARP(数值1, …)` |
| `VLOOKUP` | `VLOOKUP(查找值, 数据区域, 返回序号, [近似匹配])` |
| `VSTACK` | `VSTACK(数组1, …)` |
| `WEEKDAY` | `WEEKDAY(日期, [类型])` |
| `WEEKNUM` | `WEEKNUM(日期, [类型])` |
| `WORKDAY` | `WORKDAY(起始日期, 结束日期或天数, [节假日])` |
| `WORKDAY.INTL` | `WORKDAY.INTL(起始日期, 结束日期或天数, [周末], [节假日])` |
| `XIRR` | `XIRR(现金流区域, 日期区域, [猜测值])` |
| `XLOOKUP` | `XLOOKUP(查找值, 查找区域, 返回区域, [未找到], [匹配方式], [查找方向])` |
| `XMATCH` | `XMATCH(查找值, 区域, [匹配方式], [查找方向])` |
| `XNPV` | `XNPV(贴现率, 现金流区域, 日期区域)` |
| `XOR` | `XOR(条件1, …)` |
| `YEAR` | `YEAR(日期或时间)` |
| `YEARFRAC` | `YEARFRAC(起始日期, 结束日期, [方法])` |
