package workbook

import "context"

// Each code has one display value. Missing and present empty cells both use
// code zero and agree for these text predicates. Numeric comparisons also
// depend on the source type and continue to use the ordinary cell evaluator.
type dictionaryFilter struct {
	codes   []uint32
	allowed []byte
}

func (s *filterScanner) dictionaryFilters(ctx context.Context, remaining int) ([]dictionaryFilter, error) {
	if !s.cached || len(s.sortSlots) != 0 || remaining < 64 {
		return nil, nil
	}
	for _, rule := range s.prepared {
		view := s.views[rule.slot]
		if view.codes == nil || len(view.dictionary) > remaining/8 {
			return nil, nil
		}
		switch rule.Operator {
		case "equals", "notEquals":
			if rule.hasNumeric {
				return nil, nil
			}
		case "in", "blank", "notBlank", "contains", "notContains", "startsWith", "endsWith":
		default:
			return nil, nil
		}
	}
	filters := make([]dictionaryFilter, 0, len(s.prepared))
	for _, rule := range s.prepared {
		view := s.views[rule.slot]
		f := dictionaryFilter{codes: view.codes, allowed: make([]byte, len(view.dictionary))}
		for code, value := range view.dictionary {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if matchCompiledFilter(CellData{Display: value, Type: "s"}, true, rule) {
				f.allowed[code] = 1
			}
		}
		filters = append(filters, f)
	}
	return filters, nil
}

// Evaluate up to 64 source positions per contiguous column pass. The tail
// is bounded by end. Checkpoints and the output order still advance by row.
func dictionaryBlockMask(filters []dictionaryFilter, start, end int) uint64 {
	mask := ^uint64(0)
	for _, f := range filters {
		var word uint64
		for i, code := range f.codes[start:end] {
			word |= uint64(f.allowed[code]) << uint(i)
		}
		mask &= word
		if mask == 0 {
			break
		}
	}
	return mask
}
