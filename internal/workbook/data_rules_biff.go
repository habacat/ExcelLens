package workbook

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"

	"excellens/internal/formats"
)

const biffDVal = 0x01B2
const biffDv = 0x01BE

// Only scan the selected worksheet's own record boundaries. Embedded chart
// rules are not worksheet criteria, and their EOF cannot terminate this read.
func (w *Workbook) readBIFFDataRules(ctx context.Context, meta SheetMeta, b *dataRuleBuilder, progress func(int64, int64, string)) error {
	r, err := w.openBIFFWorksheet(meta)
	if err != nil {
		return err
	}
	defer r.cf.Close()
	r.ctx = ctx
	expected, count := -1, 0
	var parts [][]byte
	partBytes := 0
	appendPart := func(data []byte) error {
		if len(parts) >= formats.DataRuleBIFFSegments || len(data) > formats.DataRuleBIFFBytes-partBytes {
			return errors.New("BIFF8数据规则续记录超过单项读取容量")
		}
		parts = append(parts, bytes.Clone(data))
		partBytes += len(data)
		return nil
	}
	flush := func() error {
		if len(parts) == 0 {
			return nil
		}
		rule, err := readBIFFDataRuleParts(ctx, parts)
		if err != nil {
			return err
		}
		if err := b.add(rule); err != nil {
			return err
		}
		count++
		parts = nil
		partBytes = 0
		return nil
	}
	last := int64(0)
	total := r.reader.end - r.start
	if progress != nil {
		progress(0, total, "正在读取源数据规则")
	}
	for {
		id, data, err := r.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if id == 0x003C && len(parts) > 0 {
			if err := appendPart(data); err != nil {
				return err
			}
			continue
		}
		if err := flush(); err != nil {
			return err
		}
		if expected >= 0 && count < expected && id != biffDv {
			return errors.New("BIFF8数据规则集合未按声明完整结束，未发布部分规则")
		}
		switch id {
		case biffDVal:
			if expected >= 0 || len(data) != 18 {
				return errors.New("BIFF8数据规则集合声明重复或长度无效")
			}
			if xlsUint32(data, 2) > 65535 || xlsUint32(data, 6) > 65535 {
				return errors.New("BIFF8数据规则提示位置无效")
			}
			expected = int(xlsUint32(data, 14))
			if expected > formats.DataRuleCount {
				return errors.New("BIFF8数据规则数量超过读取容量，未发布部分规则")
			}
			b.index.summary.InputWindowClosed = xlsUint16(data, 0)&1 != 0
		case biffDv:
			if expected < 0 || count >= expected {
				return errors.New("BIFF8数据规则缺少集合声明或超出声明数量")
			}
			if err := appendPart(data); err != nil {
				return err
			}
		}
		current := r.reader.pos - r.start
		if progress != nil && current-last >= 8<<20 {
			progress(current, total, "正在读取源数据规则")
			last = current
		}
	}
	if err := flush(); err != nil {
		return err
	}
	if expected >= 0 && count != expected {
		return errors.New("BIFF8数据规则数量与源声明不一致")
	}
	return ctx.Err()
}

func dataRuleBIFFConstant(tokens []byte) (string, string, bool) {
	if len(tokens) == 0 {
		return "", "", true
	}
	switch tokens[0] {
	case 0x1E:
		if len(tokens) == 3 {
			return fmt.Sprintf("=%d", xlsUint16(tokens, 1)), "", true
		}
	case 0x1F:
		if len(tokens) == 9 {
			number := xlsFloat64(tokens, 1)
			if !math.IsNaN(number) && !math.IsInf(number, 0) {
				return "=" + xlsNumber(number), "", true
			}
		}
	case 0x17:
		if len(tokens) < 3 {
			break
		}
		count, width := int(tokens[1]), 1
		if tokens[2]&1 != 0 {
			width = 2
		}
		if len(tokens) != 3+count*width {
			break
		}
		if count == 0 {
			return `=""`, "", true
		}
		text, consumed, err := biffCatalogueString(tokens[2:], count)
		if err == nil && consumed == len(tokens)-2 {
			return `="` + strings.ReplaceAll(text, `"`, `""`) + `"`, text, true
		}
	}
	return "", "", false
}

func readBIFFDataRule(data []byte) (SourceDataRule, error) {
	return readBIFFDataRuleParts(context.Background(), [][]byte{data})
}

func readBIFFDataRuleParts(ctx context.Context, parts [][]byte) (SourceDataRule, error) {
	if len(parts) == 0 || len(parts) > formats.DataRuleBIFFSegments {
		return SourceDataRule{}, errors.New("BIFF8数据规则续记录数量无效")
	}
	countBytes := 0
	for _, part := range parts {
		if len(part) > formats.DataRuleBIFFBytes-countBytes {
			return SourceDataRule{}, errors.New("BIFF8数据规则超过单项读取容量")
		}
		countBytes += len(part)
	}
	cursor := dataRuleBIFFCursor{ctx: ctx, xlsSegments: xlsSegments{seg: parts}}
	head, err := cursor.bytes(4)
	if err != nil {
		return SourceDataRule{}, err
	}
	flags := xlsUint32(head, 0)
	rule := SourceDataRule{Status: "described", RecordHex: hex.EncodeToString(parts[0]), Attributes: map[string]string{"flags": fmt.Sprintf("0x%08X", flags)}}
	for _, part := range parts[1:] {
		rule.ContinuationHex = append(rule.ContinuationHex, hex.EncodeToString(part))
	}
	value := func(options []string, code uint32) string {
		if int(code) < len(options) {
			return options[code]
		}
		return fmt.Sprintf("unknown:%d", code)
	}
	rule.Type = value(dataRuleTypes, flags&15)
	rule.ErrorStyle = value(dataRuleErrorStyles, (flags>>4)&7)
	rule.IMEMode = value(dataRuleIMEModes, (flags>>10)&255)
	if (flags>>10)&255 == 3 {
		rule.IMEMode = "unknown:3"
	}
	rule.Operator = value(dataRuleOperators, (flags>>20)&15)
	rule.AllowBlank, rule.SuppressDropDown = flags&0x100 != 0, flags&0x200 != 0
	rule.ShowInputMessage, rule.ShowErrorMessage = flags&0x40000 != 0, flags&0x80000 != 0
	for _, field := range []*string{&rule.PromptTitle, &rule.ErrorTitle, &rule.Prompt, &rule.Error} {
		text, err := cursor.text()
		if err != nil {
			return SourceDataRule{}, err
		}
		*field = text
	}
	var tokens [2][]byte
	for pos := range tokens {
		header, err := cursor.bytes(4)
		if err != nil {
			return SourceDataRule{}, err
		}
		value, err := cursor.bytes(int(xlsUint16(header, 0)))
		if err != nil {
			return SourceDataRule{}, err
		}
		tokens[pos] = value
	}
	header, err := cursor.bytes(2)
	if err != nil {
		return SourceDataRule{}, err
	}
	count := int(xlsUint16(header, 0))
	if count < 1 || count > formats.DataRuleBIFFRanges || count*8 != cursor.remaining() {
		return SourceDataRule{}, errors.New("BIFF8数据规则适用区域数量或长度无效")
	}
	for pos := 0; pos < count; pos++ {
		data, err := cursor.bytes(8)
		if err != nil {
			return SourceDataRule{}, err
		}
		area := CellMerge{Row: int(xlsUint16(data, 0)) + 1, EndRow: int(xlsUint16(data, 2)) + 1, Col: int(xlsUint16(data, 4)) + 1, EndCol: int(xlsUint16(data, 6)) + 1}
		if area.Row > area.EndRow || area.Col > area.EndCol || area.EndRow > formats.BIFFRows || area.EndCol > formats.BIFFColumns {
			return SourceDataRule{}, errors.New("BIFF8数据规则适用源区域越界或倒置")
		}
		rule.Ranges = append(rule.Ranges, area)
	}
	rule.SqRef = dataRuleScopeText(rule.Ranges)
	rule.Formula1Tokens, rule.Formula2Tokens = hex.EncodeToString(tokens[0]), hex.EncodeToString(tokens[1])
	if rule.Type == "none" && len(tokens[0]) != 0 {
		dataRuleIssue(&rule, "invalid", "BIFF8任意值规则含不应存在的第一个条件，原令牌保留")
	}
	if (rule.Type == "none" || rule.Type == "list" || rule.Type == "custom" || (flags>>20)&15 >= 2) && len(tokens[1]) != 0 {
		dataRuleIssue(&rule, "invalid", "BIFF8规则含不应存在的第二个条件，原令牌保留")
	}
	for pos, field := range []*string{&rule.Formula1, &rule.Formula2} {
		formula, text, ok := dataRuleBIFFConstant(tokens[pos])
		if !ok {
			// PtgRef/Area contain source coordinates. The N variants require
			// an evaluation origin, which a sheet-wide rule does not assume.
			formula = decodeBIFFFormula(tokens[pos], -1, -1)
			if !strings.HasPrefix(formula, "=") {
				dataRuleIssue(&rule, "unresolved", "BIFF8条件表达式或相对基准尚未可靠还原，保留原始令牌，不执行公式")
				continue
			}
		}
		*field = formula
		if pos == 0 && rule.Type == "list" && flags&0x80 != 0 {
			if len(tokens[0]) == 0 || tokens[0][0] != 0x17 {
				dataRuleIssue(&rule, "invalid", "BIFF8显式列表未使用可确认的文字定义")
				continue
			}
			rule.Formula1 = "" // An embedded NUL list is not an invented Excel expression.
			rule.InlineList, rule.ListSource = strings.Split(text, "\x00"), "inline"
		}
	}
	if rule.Type == "list" && rule.ListSource == "" {
		rule.ListSource = "expression"
		dataRuleIssue(&rule, "unresolved", "列表来自尚未读取的引用或表达式，原始令牌保留")
	}
	validateDataRule(&rule)
	return rule, nil
}
