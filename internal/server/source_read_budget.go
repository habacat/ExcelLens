package server

import (
	"encoding/json"

	"unicode/utf8"

	"excellens/internal/i18n"
	"excellens/internal/workbook"
)

var errSourceReadCapacity = i18n.Error("service.sourceReadBudget.sourceRowExceedsSingleRead")

// Match this build's replacement-rune encoding without rewriting source text.
var jsonInvalidUTF8Bytes = func() int {
	encoded, _ := json.Marshal(string([]byte{0xff}))
	return len(encoded) - 2
}()

// Check the existing descriptors before the common reader allocates row buffers.
// This does not read source data or expand absent rows into an index.
func sourceRowsWithinBudget(idx *workbook.SheetIndex, numbers []int, limit int64) error {
	var used int64
	for _, number := range numbers {
		at := idx.FindRowOffset(number)
		if at >= idx.RowCount() || idx.RowAt(at).Number != number {
			continue
		}
		length, err := sourceRowByteLength(idx, at)
		if err != nil {
			return err
		}
		if length > limit-used {
			return errSourceReadCapacity
		}
		used += length
	}
	return nil
}

func sourceRowByteLength(idx *workbook.SheetIndex, at int) (int64, error) {
	if at < 0 || at >= idx.RowCount() {
		return 0, i18n.Error("service.sourceReadBudget.invalidSourceRowIndexPosition")
	}
	offset := idx.RowAt(at)
	if offset.Start < 0 || offset.End < offset.Start || offset.End > idx.XMLSize {
		return 0, i18n.Error("service.sourceReadBudget.sourceRowCacheRangeInvalid")
	}
	return offset.End - offset.Start, nil
}

// Count JSON string contents before allocating escaped copies. Unicode is not
// charged six times simply because it is non-ASCII. The final encoder checks
// the full envelope, punctuation and non-string metadata as well.
func jsonTextFits(text string, remaining *int) bool {
	if len(text) > *remaining {
		return false
	}
	for len(text) > 0 {
		r, size := utf8.DecodeRuneInString(text)
		cost := size
		switch {
		case r == utf8.RuneError && size == 1:
			cost = jsonInvalidUTF8Bytes
		case r == '\u2028', r == '\u2029', r == '<', r == '>', r == '&':
			cost = 6
		case r == '"', r == '\\', r == '\b', r == '\f', r == '\n', r == '\r', r == '\t':
			cost = 2
		case r < 0x20:
			cost = 6
		}
		if cost > *remaining {
			return false
		}
		*remaining -= cost
		text = text[size:]
	}
	return true
}
