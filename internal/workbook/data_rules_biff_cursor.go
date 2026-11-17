package workbook

import (
	"context"
	"errors"

	"excellens/internal/formats"
)

// Ordinary structure/formula bytes can cross a CONTINUE boundary verbatim.
// Only a boundary inside Unicode characters carries a new compression flag.
type dataRuleBIFFCursor struct {
	ctx context.Context
	xlsSegments
}

func (c *dataRuleBIFFCursor) bytes(n int) ([]byte, error) {
	if err := c.ctx.Err(); err != nil {
		return nil, err
	}
	if n < 0 || n > formats.DataRuleBIFFBytes {
		return nil, errors.New("BIFF8数据规则字段超过读取容量")
	}
	return c.nextBytes(n)
}

func (c *dataRuleBIFFCursor) text() (string, error) {
	return readBIFFUnicodeText(c.ctx, &c.xlsSegments, 65535, formats.DataRuleTextBytes)
}
