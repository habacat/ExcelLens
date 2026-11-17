// formulacatalog writes the same function catalog used by the calculation API
// and input completion. Regenerate with: go run ./tools/formulacatalog
package main

import (
	"fmt"
	"os"
	"strings"

	"excellens/internal/calculator"
)

func main() {
	var out strings.Builder
	functions := calculator.Functions()
	fmt.Fprintln(&out, "# 计算函数清单")
	fmt.Fprintf(&out, "\n当前共 %d 个函数名，包含兼容别名；`AVG` 是额外提供的便利别名。\n", len(functions))
	fmt.Fprintln(&out, "\n本清单由 `go run ./tools/project generate` 从计算引擎共享目录生成，界面补全和 HTTP 接口使用同一目录。登记的函数不代表已实现 Excel 的全部参数形式；范围、精度和已知边界见 [计算说明](calculator.md)。")
	fmt.Fprintln(&out, "\n| 函数 | 参数 |\n| --- | --- |")
	for _, fn := range functions {
		fmt.Fprintf(&out, "| `%s` | `%s` |\n", fn.Name, fn.Signature)
	}
	if len(os.Args) > 1 {
		if err := os.WriteFile(os.Args[1], []byte(out.String()), 0644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("Generated %s: %d function names\n", os.Args[1], len(functions))
		return
	}
	fmt.Print(out.String())
}
