package workbook

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Oracle's journal spool uses literal @ separators, not CSV quoting. A quote
// in an account description must never absorb subsequent physical records.
type oracleTextLayout struct {
	columns      int
	headerTail   string
	joinedHeader bool
}

func detectOracleTextLayout(sample string, delimiter rune) *oracleTextLayout {
	if delimiter != '@' {
		return nil
	}
	lines := strings.Split(sample, "\n")
	first := strings.Split(strings.TrimSuffix(lines[0], "\r"), "@")
	head := []string{"凭证来源", "批名", "凭证编号", "凭证名", "日期", "凭证类别", "过帐人", "创建人", "审批人", "原币", "原币借", "原币贷", "本位币", "本位借", "本位贷"}
	if len(first) < len(head)+2 {
		return nil
	}
	for i, name := range head {
		if first[i] != name {
			return nil
		}
	}
	// Use only complete following physical lines to establish the schema.
	counts := make(map[int]int)
	for i := 1; i < len(lines)-1 && i <= 24; i++ {
		if strings.TrimSpace(lines[i]) != "" {
			counts[strings.Count(lines[i], "@")+1]++
		}
	}
	columns, frequency := len(first), 0
	for n, count := range counts {
		if count > frequency || count == frequency && n > columns {
			columns, frequency = n, count
		}
	}
	if columns < len(head)+2 || columns > 16384 || (columns-len(head))%2 != 0 || len(first) < columns {
		return nil
	}
	joined := len(first) == 2*columns-1
	if len(first) != columns && !joined {
		return nil
	}
	tail := ""
	for segment, col := 1, len(head); col < columns; segment, col = segment+1, col+2 {
		if first[col] != "segment"+strconv.Itoa(segment) {
			return nil
		}
		tail = "seg" + strconv.Itoa(segment) + "_desc"
		if col+1 == columns-1 && joined {
			if !strings.HasPrefix(first[col+1], tail) || first[col+1] == tail {
				return nil
			}
		} else if first[col+1] != tail {
			return nil
		}
	}
	if joined && frequency < 2 {
		return nil // A single irregular record is not enough evidence to repair.
	}
	return &oracleTextLayout{columns: columns, headerTail: tail, joinedHeader: joined}
}

func streamOracleDelimited(src io.Reader, layout *oracleTextLayout, emit func([]string) error) error {
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 64<<10), maxTextLine)
	line := 0
	for scanner.Scan() {
		line++
		fields := strings.Split(scanner.Text(), "@")
		if line == 1 && layout.joinedHeader {
			if len(fields) != 2*layout.columns-1 || !strings.HasPrefix(fields[layout.columns-1], layout.headerTail) {
				return fmt.Errorf("Oracle 表头与首条数据的边界已改变，请重新打开文件")
			}
			header := append([]string(nil), fields[:layout.columns]...)
			header[layout.columns-1] = layout.headerTail
			if err := emit(header); err != nil {
				return err
			}
			fields = fields[layout.columns-1:]
			fields[0] = strings.TrimPrefix(fields[0], layout.headerTail)
		}
		if len(fields) != layout.columns {
			return fmt.Errorf("Oracle TXT 第 %d 个物理行有 %d 列，表头为 %d 列；请检查字段中的未转义 @ 或换行", line, len(fields), layout.columns)
		}
		if err := emit(fields); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("Oracle TXT 第 %d 个物理行读取失败：%w", line+1, err)
	}
	return nil
}
