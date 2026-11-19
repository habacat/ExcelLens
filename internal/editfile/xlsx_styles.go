package editfile

import (
	"context"
	"encoding/xml"
	"fmt"
	"strconv"
)

// A worksheet cell's s attribute indexes the direct xf children of cellXfs.
// All other format collections and their vendor extensions are copied verbatim.
// They must never contribute to this index, even if they contain an xf tag.
func (p *xlsxPackage) readStyles(ctx context.Context, data []byte) error {
	var found bool
	count := 0
	p.unlockedStyles = map[string]bool{}
	protectionSeen := false
	err := walkXML(ctx, data, func(e xmlEvent) error {
		start, ok := e.token.(xml.StartElement)
		if !ok {
			return nil
		}
		switch len(e.stack) {
		case 1:
			if start.Name != (xml.Name{Space: mainNS, Local: "styleSheet"}) {
				return fmt.Errorf("%w：样式表根元素无效", ErrXLSX)
			}
		case 2:
			if start.Name.Space != mainNS {
				// A compatibility choice here could supply another cellXfs.
				return fmt.Errorf("%w：无法确定单元格样式索引（%s）", ErrXLSX, start.Name.Local)
			}
			switch start.Name.Local {
			case "cellXfs":
				if found {
					return fmt.Errorf("%w：单元格样式索引重复", ErrXLSX)
				}
				found = true
				// Excel reads the stored records even when the producer's count
				// is stale. Only real records may satisfy a cell's style index.
			case "extLst":
				p.layout.Structured = true
			}
		case 3:
			switch e.stack[1].Local {
			case "cellXfs":
				if start.Name != (xml.Name{Space: mainNS, Local: "xf"}) {
					return fmt.Errorf("%w：单元格样式索引包含 %s", ErrXLSX, start.Name.Local)
				}
				count++
				protectionSeen = false
			case "dxfs", "tableStyles":
				p.layout.Structured = true
			}
		case 4:
			if e.stack[1] == (xml.Name{Space: mainNS, Local: "cellXfs"}) && e.parent() == "xf" && start.Name == (xml.Name{Space: mainNS, Local: "protection"}) {
				flag := attribute(start, "", "locked")
				if protectionSeen || flag != "" && !ruleBool(flag) {
					return fmt.Errorf("%w：单元格锁定标志无效", ErrXLSX)
				}
				protectionSeen = true
				if flag == "0" || flag == "false" {
					p.unlockedStyles[strconv.Itoa(count-1)] = true
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	p.styles = count
	return nil
}
