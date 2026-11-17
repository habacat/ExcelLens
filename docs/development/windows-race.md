# Windows竞态检测：错误87与编译器探测

2026-11-14，本机Windows 10 x64、Go 1.27.3、MinGW GCC 14.2.0复现并修复。此说明仅针对开发用`-race`检查，生产构建继续使用纯Go、禁用CGO。

## 已确认的原因

1. 1.8.6～1.8.9的竞态收据均通过，使用的也是Go 1.27.3。当前故障可由空测试复现，尚未执行ExcelLens代码就退出，不能归因于本轮业务内存使用。
2. 本机MinGW默认子工具存在DLL加载问题；默认`cc1.exe`返回`0xc0000135`，配套工具目录中的副本可运行。临时办法只在`CC`中加`-B工具目录`。
3. Go的`linkerFlagSupported`用编译器可执行文件探测选项，没有继承额外`CC`参数。本机不带工具目录的简单C链接失败，带上则成功。失败被当成旧编译器不支持关闭ASLR，遗漏`--disable-dynamicbase`与`--disable-high-entropy-va`。
4. Go的Windows TSan不支持这种地址随机化布局。失败测试EXE的PE标志为`0x8160`，DYNAMIC_BASE与HIGH_ENTROPY_VA均开启；TSan请求`0x100ee0bf20000`附近的地址，超出本机用户地址上限`0x7ffffffeffff`，Windows返回参数无效（87）。小额地址预留也复现同一错误，不能据此建议增加物理内存或分页文件。
5. 相同`-B`参数同时传入`CGO_LDFLAGS`后，探测能找到子工具，Go按原有竞态策略关闭这两个测试EXE标志，PE变为`0x8100`。未关闭TSan，未修改系统ASLR策略或生产EXE。

尚无证据确定本机MinGW默认DLL搜索环境何时变化，不将Windows更新、重启或Go版本变化写成确定原因。能够确认的是当前故障链条及修复对照。

## 验证与复用

空测试修复前初始化退出66，修复后通过。故意制造数据竞争的控制测试仍报告`WARNING: DATA RACE`并失败，证明检测器有效。完整项目随后通过`go run ./tools/project check -race`，失败与修复后收据分别保留。诊断摘要为`validation/1.8.15/tsan-diagnosis.json`；最小探针和对应LLVM源文件在忽略的private目录。

完整MinGW安装可直接运行`go run ./tools/project check -race`。需要指定编译器或配套工具目录时使用：

```powershell
.\tools\check-race.ps1 -Compiler 'C:\path\to\gcc.exe' -CompilerToolsDir 'C:\path\to\matching-compiler-tools'
```

脚本仅对当前检查进程设置编译器、链接器及DLL搜索路径，并在`finally`中恢复环境；不下载、安装或改写系统文件。不需要配套目录时省略`-CompilerToolsDir`。子工具及DLL须与所选GCC版本、架构匹配。

来源：[Go链接器实现](https://github.com/golang/go/blob/go1.27.3/src/cmd/link/internal/ld/lib.go)、[Windows TSan已知限制](https://github.com/golang/go/issues/53539)、[对应LLVM地址映射](https://github.com/llvm/llvm-project/blob/51bfeff0e4b0757ff773da6882f4d538996c9b04/compiler-rt/lib/tsan/rtl/tsan_platform.h)。
