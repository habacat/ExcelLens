package workbook

import (
	"bytes"
	"encoding/xml"
	"io"
	"strings"
)

// Namespaces can be declared on any real ancestor. Resolve the four headers
// together, and require the actual worksheet/extLst/ext URI before accepting
// x14 criteria. An identically named tag in cell data is never a rule.
func dataRuleExtensionHeader(root, list, ext, block []byte) (bool, error) {
	d := xml.NewDecoder(io.MultiReader(bytes.NewReader(root), bytes.NewReader(list), bytes.NewReader(ext), bytes.NewReader(block)))
	var main string
	for depth, local := range []string{"worksheet", "extLst", "ext", "dataValidations"} {
		token, err := d.Token()
		if err != nil {
			return false, err
		}
		if err := dataRuleXMLToken(token, depth); err != nil {
			return false, err
		}
		node, ok := token.(xml.StartElement)
		if !ok || node.Name.Local != local {
			return false, nil
		}
		if depth == 0 {
			main = node.Name.Space
			if !catalogueNamespace(main) {
				return false, nil
			}
		}
		if depth < 3 && node.Name.Space != main || depth == 3 && node.Name.Space != x14DataRulesNamespace {
			return false, nil
		}
		if depth == 2 {
			uri := ""
			for _, attr := range node.Attr {
				if attr.Name.Space == "" && attr.Name.Local == "uri" {
					uri = attr.Value
				}
			}
			if !strings.EqualFold(uri, x14DataRulesExtensionURI) {
				return false, nil
			}
		}
	}
	return true, nil
}
