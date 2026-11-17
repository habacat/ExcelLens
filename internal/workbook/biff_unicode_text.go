package workbook

import (
	"context"
	"errors"
	"unicode/utf16"
)

func (c *xlsSegments) remaining() int {
	count := 0
	for at := c.s; at < len(c.seg); at++ {
		count += len(c.seg[at])
		if at == c.s {
			count -= c.i
		}
	}
	return count
}

// Structural bytes cross records verbatim. A continuation within characters
// starts with a fresh compression flag; UTF-16 units must remain intact.
func readBIFFUnicodeText(ctx context.Context, c *xlsSegments, maxUnits, maxBytes int) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	header, err := c.nextBytes(3)
	if err != nil {
		return "", err
	}
	count := int(xlsUint16(header, 0))
	if count > maxUnits || count > c.remaining() {
		return "", errors.New("BIFF8文字长度超过读取容量或剩余源字节")
	}
	wide := header[2]&1 != 0
	units := make([]uint16, 0, count)
	for len(units) < count {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if c.s >= len(c.seg) {
			return "", errors.New("BIFF8文字截断")
		}
		if c.i == len(c.seg[c.s]) {
			c.s++
			c.i = 0
			if c.s >= len(c.seg) || len(c.seg[c.s]) == 0 {
				return "", errors.New("BIFF8续记录缺少字符宽度标志")
			}
			wide = c.seg[c.s][0]&1 != 0
			c.i = 1
			if c.i == len(c.seg[c.s]) {
				continue
			}
		}
		width := 1
		if wide {
			width = 2
		}
		available := len(c.seg[c.s]) - c.i
		if available < width {
			return "", errors.New("BIFF8 UTF-16字符在字节中间截断")
		}
		n := min(count-len(units), available/width)
		for range n {
			value := uint16(c.seg[c.s][c.i])
			if wide {
				value = xlsUint16(c.seg[c.s], c.i)
			}
			units = append(units, value)
			c.i += width
		}
	}
	for at := 0; at < len(units); at++ {
		value := units[at]
		if value >= 0xD800 && value <= 0xDBFF {
			if at+1 >= len(units) || units[at+1] < 0xDC00 || units[at+1] > 0xDFFF {
				return "", errors.New("BIFF8 UTF-16代理项无效")
			}
			at++
		} else if value >= 0xDC00 && value <= 0xDFFF {
			return "", errors.New("BIFF8 UTF-16代理项无效")
		}
	}
	text := string(utf16.Decode(units))
	if len(text) > maxBytes {
		return "", errors.New("BIFF8文字超过读取容量")
	}
	return text, ctx.Err()
}
