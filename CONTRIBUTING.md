# 维护与验证

开发环境使用 `go.mod` 指定的 Go 工具链、Node.js 22+ 和 Git。CI 使用 Node.js 26 LTS 验证前端。Windows 发布另需 UPX。

## 持续改进接续

[实施待办](docs/development/improvement-progress.md#接续入口)是当前工作包和下一动作的唯一接续入口；实施记录只保留各自的实现、证据及边界，不另存易过期的下一工作包。

每完成一项实现、取得验证结果或准备停止、交接、压缩上下文时，先更新待办，再推进后续工作或整理摘要。写清完成项、剩余项、草稿路径、通过/失败/未执行的检查、活动进程及唯一下一动作，并同步阶段表和功能表。恢复后先读待办，核对工作树和收据；已完成项只在新变更、失败或验证缺口时补必要回归。

## 日常命令

在仓库根目录执行：

```powershell
go run ./tools/project fmt
go run ./tools/project check
go run ./tools/project check -report validation/1.8.3/check.json
```

`check` 依次校验工具链与版本、Go 格式、包依赖边界、文档链接、发布清单、离线 vendor 依赖、完整 Go 测试、`go vet`、JavaScript 语法与自动回归、生成的函数目录。失败返回非零退出码，发布构建复用同一入口。它不会访问网络、更新依赖或改写代码。

`go.mod` 的 `ignore` 指令将根目录的 `artifacts/`、`fortest/`、`release/`、`validation/` 排除在 Go 包发现范围之外。这些目录可以在测试和构建期间存放工作簿、浏览器配置和发行件。真实样本测试通过显式文件路径读取输入。

`fmt` 只格式化 `cmd/`、`internal/`、`tools/` 的 Go 文件。修改计算函数目录或依赖后运行：

```powershell
go run ./tools/project generate
```

这会生成函数清单和第三方许可；检查最终 diff。并发实现变更需要 `go run ./tools/project check -race`；该选项只在检查时启用 CGO，生产构建继续禁用 CGO。Windows 若没有 C 编译器，应如实记录，或使用 Linux CI 的竞态检查。

`tools/qa/` 另有专项与历史模拟浏览器脚本，不属于每次默认检查的桌面验收。当前原生样本脚本使用 `project_config.cjs` 读取生产首页和版本，真实样本与截图只存放在忽略的目录；运行前按脚本所需环境和样本准备，不将历史脚本的存在当作当前验证通过。

`validation/`中的原始收据和日志按字节保留，Git不进行换行规范化；不要为清除尾随空格提示改写历史失败日志。生产代码和文档仍遵循仓库的LF约定。

Windows竞态检查出现初始化分配失败、错误87，或需要指定MinGW子工具目录时，参见[诊断与检查配置](docs/development/windows-race.md)。不要仅凭TSan报错认定物理内存不足，或把初始化失败记录为竞态通过。

## 格式变更的完成条件

1. 使用真实适配器验证原值、类型、坐标、空白及公式缓存，不能只检验文件扩展名。
2. 沿用公共读取和显示接口，确认搜索、筛选、缓存重开；影响导出时导出后重开核对内容和类型。
3. 日期、编号、金额等边界必须有具体断言。无法解析的内容明确拒绝或标识，不静默补值、截断或重算源公式。
4. 读取前后核对源内容。私有样本只通过环境变量接入，缺少样本时报告跳过，不能写成通过。
5. 更新 [格式边界](docs/user/formats.md) 和当前验证说明，分别写清已验证与未覆盖部分。

## 发布

1. 修改 `internal/buildinfo/version.go` 后执行 `go run ./tools/icon`，一并提交重建的 Windows 资源。同步 README 当前标题，在 `docs/releases/<版本>.md` 记录变更及验证；历史报告保留原版本。
2. 执行 `build.ps1` 或 `upgrade-build.ps1`。构建脚本从唯一版本源、`go.mod` 和发布清单读取配置；先通过全部检查，再构建、检查 PE、UPX 压缩与校验、逐文件核对 ZIP。
正式构建会通过隔离服务与无头 Chromium 运行合成基准。开发机需安装 Python 3、Node.js 22+ 及 Chrome，测试工具可通过 `QA_PYTHON`、`QA_CHROME` 指定。用户安装的程序继续独立运行。完整测量含短时滚动和 100 轮回收，规则及复现方法见[发行基准](docs/development/reading-benchmarks.md)。

3. 候选核验使用 `build.ps1 -StageOnly`。完整检查结果写入 `validation/<版本>/private/candidate-check.json`，同版本正式发行的检查收据继续对应正式源码。PE、UPX 和 ZIP 核验通过后，完整候选包位于 `artifacts/<版本>-verified-*`。普通构建全部成功后才原子替换 `release/`。正在运行旧版时会保留已验证包，用户退出托盘后可继续发布。
4. 不自动提交、推送或公开发布。源码交付需与指定 Git 提交一致；工作树未提交时不能宣称源码包等于 HEAD。

本地交付源码时，先为发行提交建标签，再从标签生成归档。以下命令从唯一版本源取当前版本，独立核对两个包。源码包内的每个文件都要匹配发行标签的 Git 对象，之后的收据提交可以继续追加到分支。

```powershell
$releaseVersion = [regex]::Match((Get-Content -Raw internal/buildinfo/version.go), 'const Version = "([0-9.]+)"').Groups[1].Value
$releaseTag = "v$releaseVersion"
git archive --format=zip -9 -o "release/ExcelLens_${releaseVersion}_Source_Full.zip" $releaseTag
python tools/qa/verify_release.py $releaseVersion
python tools/qa/verify_release.py $releaseVersion --source --ref $releaseTag
```

本地与 GitHub 交付相同双包时，先在公开源码提交建立对应标签，再从该工作树运行 `build.ps1 -TaggedRelease -DeliveryRoot <本地同版本项目目录>`。脚本校验标签对应的构建输入，在同一暂存目录核验 Windows 包、标签源码包及该标签实测的 Performance.json，三项摘要收录于 SHA-256 清单，再整体切换 `release/`。可加 `-StageOnly` 预备双包。仅需独立复验现有附件时使用 `tools/qa/verify_release.py`。

用户指南、当前变更及验证分开放置；完整目录说明见 [文档索引](docs/README.md)。
