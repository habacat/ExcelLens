# 界面语言与消息目录

六种语言的 UTF-8 消息目录位于 `internal/i18n/catalogs/`。Go 将目录和页面模板嵌入可执行文件。每个页面只附带当前语言所需的脚本消息，静态标签及辅助属性由 Go 的 HTML 模板渲染。浏览器按语言偏好顺序匹配，自动模式下托盘读取 Windows 显示语言。固定选择保存在本地设置，可随本地数据备份迁移。

## 修改文案

- 选用能说明用途的稳定消息 ID。静态界面使用 HTML 模板的 `.T`，TypeScript 使用 `t` 或插入 HTML 的 `th`，Go 使用 `Locale.Text`。共享消息适用于语义相同的控件。
- 将整句及具名参数放进目录，例如 `{count}` 和 `{fileName}`。各语言可以按语法调整顺序。字节、日期和应用统计的呈现使用选定语言。工作簿中的值、公式、表名、源坐标、可移植工作产物和原始诊断按来源保留。
- 在操作和任务状态中传递稳定字段。HTTP 业务错误通过 `writeMessage` 或 `writeFailure` 输出 `error` 以及可用的 `code`。异步阶段在响应时渲染，历史记录中的原诊断保留原文。新文本及模板参数优先通过现有组件填入。
- 执行 `npm run generate:messages` 和 `npm run build:ui`。统一检查验证目录覆盖、具名参数集合、TypeScript 使用、HTML 模板和生成物。Windows 可用 `tools/qa/language_preferences.cjs` 复测实际服务与无头浏览器。

## 语言切换

托盘、服务及页面共用语言设置。页面从响应中获取语言并随请求携带，任务结果可供不同语言的标签页查看。空闲首页监听界面设置变更，各标签页按自己的浏览器偏好协商。工作簿标签页保留加载时的语言，在返回首页时切换。浏览器的语言自动匹配使用 BCP 47 语言标签，托盘读取 Windows 用户显示语言列表。 [HTTP 语言协商](https://www.rfc-editor.org/rfc/rfc9110.html#name-accept-language) · [Go 语言匹配器](https://pkg.go.dev/golang.org/x/text/language) · [Windows 显示语言](https://learn.microsoft.com/en-us/windows/win32/api/winnls/nf-winnls-getuserpreferreduilanguages)
