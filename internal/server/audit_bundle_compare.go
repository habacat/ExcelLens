package server

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"excellens/internal/formats"
	"excellens/internal/reconcile"
	"excellens/internal/workbook"
)

type batchBundle struct {
	record reconcileRecord
	data   *os.File
}

func validDigest(value string, n int) bool {
	if len(value) != n*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func readBatchBundle(ctx context.Context, dir, side string) (batchBundle, error) {
	var bundle batchBundle
	archive, err := zip.OpenReader(filepath.Join(dir, side+".zip"))
	if err != nil {
		return bundle, err
	}
	defer archive.Close()
	var manifest, detail *zip.File
	for _, part := range archive.File {
		switch part.Name {
		case "核查记录.json":
			if manifest != nil {
				return bundle, errors.New("核查包记录重复")
			}
			manifest = part
		case "核对明细.csv":
			if detail != nil {
				return bundle, errors.New("核查包明细重复")
			}
			detail = part
		}
	}
	if manifest == nil || detail == nil || manifest.UncompressedSize64 > formats.ReconcileRecordBytes || detail.UncompressedSize64 > formats.BatchDetailBytes {
		return bundle, errors.New("包内缺少核查记录、明细或超出读取容量")
	}
	r, err := manifest.Open()
	if err != nil {
		return bundle, err
	}
	data, err := io.ReadAll(io.LimitReader(r, formats.ReconcileRecordBytes+1))
	r.Close()
	if err != nil || len(data) > formats.ReconcileRecordBytes || json.Unmarshal(data, &bundle.record) != nil {
		return bundle, errors.New("核查记录不完整")
	}
	m := bundle.record
	if m.Kind != "ExcelLens.Reconcile" || (m.RuleVersion != 1 && m.RuleVersion != reconcile.CurrentRuleVersion) || m.Version != 1 || m.Query.RuleVersion != 0 && m.Query.RuleVersion != m.RuleVersion || m.Scope != "all-data-rows" || m.Detail.Status != "" || m.Detail.File != detail.Name || !validDigest(m.Detail.SHA256, 32) || m.Detail.Rows < 0 || m.Detail.Rows > formats.ExternalRows {
		return bundle, errors.New("请使用同版本规则、全部类型的完整核查包")
	}
	if len(m.Query.Keys) == 0 || len(m.Query.Keys) > reconcile.MaxKeys || len(m.Query.Fields) > reconcile.MaxFields {
		return bundle, errors.New("核查字段数量无效")
	}
	for section, pairs := range [][]reconcile.Pair{m.Query.Keys, m.Query.Fields} {
		for _, pair := range pairs {
			if pair.Tolerance == "" {
				continue
			}
			if section == 0 || m.RuleVersion == 1 {
				return bundle, errors.New("核查包中主键或旧规则包含无效容差")
			}
			if _, err := pair.AmountTolerance(); err != nil {
				return bundle, err
			}
		}
	}
	for _, input := range m.Inputs {
		if !validDigest(input.Stamp, 32) || (input.Kind != "analysis" && (input.Kind != "workbook" || !validDigest(input.FileSHA256, 32))) {
			return bundle, errors.New("核查输入摘要无效")
		}
	}
	reader, err := detail.Open()
	if err != nil {
		return bundle, err
	}
	defer reader.Close()
	f, err := os.CreateTemp(dir, side+"-*.csv")
	if err != nil {
		return bundle, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(&batchContextReader{ctx, reader}, formats.BatchDetailBytes+1))
	if err != nil || uint64(n) != detail.UncompressedSize64 || hex.EncodeToString(h.Sum(nil)) != m.Detail.SHA256 {
		f.Close()
		return bundle, errors.New("核查明细长度或 SHA-256 与记录不符")
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return bundle, err
	}
	bundle.data = f
	return bundle, nil
}

type batchContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *batchContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func batchField(m reconcileRecord, side, col int) (reconcile.Field, bool) {
	for _, f := range m.Inputs[side].Fields {
		if f.Column == col {
			return f, true
		}
	}
	return reconcile.Field{}, false
}
func sameBatchRules(a, b reconcileRecord) bool {
	if a.RuleVersion != b.RuleVersion || a.Detail.ProtectCSV != b.Detail.ProtectCSV {
		return false
	}
	for i, pa := range [][]reconcile.Pair{a.Query.Keys, a.Query.Fields} {
		pb := [][]reconcile.Pair{b.Query.Keys, b.Query.Fields}[i]
		if len(pa) != len(pb) {
			return false
		}
		for n, x := range pa {
			y := pb[n]
			if x.Mode != y.Mode || x.Trim != y.Trim || x.IgnoreCase != y.IgnoreCase {
				return false
			}
			if x.Tolerance != y.Tolerance {
				xt, xe := x.AmountTolerance()
				yt, ye := y.AmountTolerance()
				if xe != nil || ye != nil || xt == nil || yt == nil || xt.Cmp(yt) != 0 {
					return false
				}
			}
			for side, col := range [2]int{x.Left, x.Right} {
				other := [2]int{y.Left, y.Right}[side]
				f, ok := batchField(a, side, col)
				g, found := batchField(b, side, other)
				if !ok || !found {
					return false
				}
				if a.Inputs[side].Stamp == b.Inputs[side].Stamp && col == other {
					continue
				}
				if !f.UniqueHeader || !g.UniqueHeader || !validDigest(f.HeaderHash, 32) || f.HeaderHash != g.HeaderHash {
					return false
				}
			}
		}
	}
	return true
}

// The CSV from each period is checked before sorting. Only keys and source
// offsets enter the shared spill sorter. Duplicate groups need no in-memory
// member list, including when one key spans the entire file.
type batchItem struct {
	group, order string
	fields       []string
}

func batchKey(row []string, m reconcileRecord, period string) (string, error) {
	if !validDigest(row[3], 16) {
		return "", errors.New("明细主键组标识无效")
	}
	group := row[3]
	kind := row[0]
	if row[1] == "无效主键" {
		group = period + group
	}
	if kind == "主键汇总" {
		return group + "/S", nil
	}
	if kind != "重复键记录" && kind != "核对记录" {
		return "", errors.New("核查明细记录类别无效")
	}
	pairs := m.Query.Fields
	if len(pairs) == 0 {
		pairs = m.Query.Keys[:1]
	}
	side, ref := 0, row[10]
	if ref == "" {
		side, ref = 1, row[19]
	}
	col, _, err := workbook.ParseCellRef(ref)
	if err != nil {
		return "", errors.New("核查明细的源坐标无效")
	}
	pair := slices.IndexFunc(pairs, func(p reconcile.Pair) bool { return side == 0 && p.Left == col || side == 1 && p.Right == col })
	if pair < 0 {
		return "", errors.New("明细源格与记录的字段映射不符")
	}
	key := fmt.Sprintf("%s/N%02d", group, pair)
	if kind == "重复键记录" {
		data, _ := json.Marshal([]string{row[11], row[12], row[13], row[20], row[21], row[22]})
		key = fmt.Sprintf("%s/D%02d%d%x", group, pair, side, sha256.Sum256(data))
	}
	return key, nil
}

func (b batchBundle) items(ctx context.Context, dir, period string) iter.Seq2[batchItem, error] {
	return func(yield func(batchItem, error) bool) {
		index, err := os.CreateTemp(dir, "positions-")
		if err != nil {
			yield(batchItem{}, err)
			return
		}
		defer index.Close()
		var block [16]byte
		var expected bytes.Buffer
		reference, err := reconcile.NewCSVWriter(&expected, b.record.Query, b.record.Inputs[0].SourceInfo, b.record.Inputs[1].SourceInfo, nil)
		if err != nil {
			yield(batchItem{}, err)
			return
		}
		if err = reference.Flush(); err != nil {
			yield(batchItem{}, err)
			return
		}
		header, err := csv.NewReader(&expected).Read()
		if err != nil {
			yield(batchItem{}, err)
			return
		}
		if b.record.Detail.ProtectCSV {
			header[13] += "（文本保护）"
			header[22] += "（文本保护）"
		}
		// Bound a single logical record even if a supplied CSV never closes a quote.
		limit := &io.LimitedReader{R: b.data, N: formats.BatchCSVRowBytes}
		reader := csv.NewReader(limit)
		reader.FieldsPerRecord = len(header)
		actual, err := reader.Read()
		if err == nil {
			actual[0] = strings.TrimPrefix(actual[0], "\ufeff")
		}
		if err != nil || !slices.Equal(actual, header) {
			yield(batchItem{}, errors.New("核查明细表头无效"))
			return
		}
		rows := int(b.record.Detail.Rows)
		err = workbook.SortKeyStream(ctx, dir, rows, formats.QuerySortMemoryBytes/2, func(ctx context.Context, add func(workbook.KeyRecord) error) error {
			offsets := bufio.NewWriter(index)
			for n := 0; ; n++ {
				if err := ctx.Err(); err != nil {
					return err
				}
				limit.N = formats.BatchCSVRowBytes + 1
				start := reader.InputOffset()
				row, err := reader.Read()
				if err == io.EOF {
					if n != rows || limit.N == 0 {
						return errors.New("核查明细行数与记录不符")
					}
					return offsets.Flush()
				}
				size := reader.InputOffset() - start
				if err != nil || size > formats.BatchCSVRowBytes || n >= rows {
					return errors.New("核查明细行无效或超出容量")
				}
				binary.LittleEndian.PutUint64(block[:8], uint64(start))
				binary.LittleEndian.PutUint64(block[8:], uint64(size))
				if _, err = offsets.Write(block[:]); err != nil {
					return err
				}
				if row[1] == "一致" || row[1] == "容差内" {
					continue
				}
				key, err := batchKey(row, b.record, period)
				if err != nil {
					return err
				}
				if err = add(workbook.KeyRecord{Position: n, Key: key}); err != nil {
					return err
				}
			}
		}, func(sorted workbook.KeyRecord) error {
			if _, err := index.ReadAt(block[:], int64(sorted.Position)*16); err != nil {
				return err
			}
			pos, n := int64(binary.LittleEndian.Uint64(block[:8])), int64(binary.LittleEndian.Uint64(block[8:]))
			fields, err := csv.NewReader(io.NewSectionReader(b.data, pos, n)).Read()
			if err != nil {
				return err
			}
			group, order, _ := strings.Cut(sorted.Key, "/")
			if !yield(batchItem{group, order, fields}, nil) {
				return errBatchClosed
			}
			return nil
		}, nil)
		if err != nil && !errors.Is(err, errBatchClosed) {
			yield(batchItem{}, err)
		}
	}
}

var errBatchClosed = errors.New("batch comparison reader closed")

func batchEvidence(row []string) []string {
	if row == nil {
		return make([]string, 11)
	}
	return []string{row[1], row[4], row[25], row[5] + " / " + row[8], row[10], row[13], row[14] + " / " + row[17], row[19], row[22], row[26], row[27]}
}

func compareBatchDetails(ctx context.Context, dir string, before, after batchBundle, out *auditCSV) error {
	if !sameBatchRules(before.record, after.record) {
		return errors.New("两批规则版本、主键、字段、金额容差、匹配方式或 CSV 文字保护设置不一致")
	}
	header := []string{"跟踪结果", "主键组ID"}
	for _, period := range []string{"基线", "当期"} {
		for _, name := range []string{"分类", "主键原文", "比较字段", "左来源", "左源格", "左值", "右来源", "右源格", "右值", "差额", "说明"} {
			header = append(header, period+name)
		}
	}
	header = append(header, "基线组左记录数", "基线组右记录数", "当期组左记录数", "当期组右记录数")
	if err := out.write(header...); err != nil {
		return err
	}
	nl, sl := iter.Pull2(before.items(ctx, dir, "old-"))
	defer sl()
	nr, sr := iter.Pull2(after.items(ctx, dir, "new-"))
	defer sr()
	l, le, hl := nl()
	r, re, hr := nr()
	for hl || hr {
		if le != nil {
			return le
		}
		if re != nil {
			return re
		}
		id := r.group
		if hl && (!hr || l.group < id) {
			id = l.group
		}
		leftGroup, rightGroup := hl && l.group == id, hr && r.group == id
		for (hl && l.group == id) || (hr && r.group == id) {
			if err := ctx.Err(); err != nil {
				return err
			}
			useL := hl && l.group == id && (!hr || r.group != id || l.order <= r.order)
			useR := hr && r.group == id && (!hl || l.group != id || r.order <= l.order)
			var x, y []string
			if useL {
				x = l.fields
			}
			if useR {
				y = r.fields
			}
			label := "持续·明细变化"
			switch {
			case strings.HasPrefix(id, "old-"):
				label = "基线无效主键"
			case strings.HasPrefix(id, "new-"):
				label = "当期无效主键"
			case !leftGroup:
				label = "新增异常"
			case !rightGroup:
				label = "已消失异常"
			case useL && useR:
				label = "持续·内容相同"
				for _, i := range []int{0, 1, 2, 11, 12, 13, 20, 21, 22, 23, 24, 26, 27} {
					if x[i] != y[i] {
						label = "持续·值或状态变化"
						break
					}
				}
			}
			fields := append([]string{label, strings.TrimPrefix(strings.TrimPrefix(id, "old-"), "new-")}, batchEvidence(x)...)
			fields = append(fields, batchEvidence(y)...)
			counts := func(row []string) []string {
				if row == nil {
					return []string{"", ""}
				}
				return row[23:25]
			}
			fields = append(fields, counts(x)...)
			fields = append(fields, counts(y)...)
			if err := out.write(fields...); err != nil {
				return err
			}
			if useL {
				l, le, hl = nl()
				if le != nil {
					return le
				}
			}
			if useR {
				r, re, hr = nr()
				if re != nil {
					return re
				}
			}
		}
	}
	if out.rows == 1 {
		row := make([]string, len(header))
		row[0] = "两批均无异常"
		return out.write(row...)
	}
	return nil
}

func (a *App) handleBatchComparison(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	book := a.currentWorkbook()
	if book == nil || sourceBookID(book) != r.URL.Query().Get("book") {
		writeError(w, 409, "当前工作簿已切换")
		return
	}
	job, err := a.newExportJob(book, "csv")
	if err != nil {
		a.writeExportStartError(w, err)
		return
	}
	dir, err := os.MkdirTemp("", "excellens-batch-")
	if err == nil {
		r.Body = http.MaxBytesReader(w, r.Body, 2*formats.BatchArchiveBytes+(1<<20))
		body := r.Body
		stop := context.AfterFunc(job.ctx, func() { _ = body.Close() })
		err = receiveBatchFiles(r, dir)
		stop()
	}
	if err != nil {
		job.fail(err)
		close(job.done)
		if dir != "" {
			_ = os.RemoveAll(dir)
		}
		writeError(w, 400, err.Error())
		return
	}
	go a.runAuditReport(job, "异常逐项跟踪", func() { _ = os.RemoveAll(dir) }, func(ctx context.Context, out *auditCSV) error {
		left, err := readBatchBundle(ctx, dir, "baseline")
		if err != nil {
			return err
		}
		defer left.data.Close()
		right, err := readBatchBundle(ctx, dir, "current")
		if err != nil {
			return err
		}
		defer right.data.Close()
		return compareBatchDetails(ctx, dir, left, right, out)
	})
	writeJSON(w, map[string]string{"jobId": job.ID})
}

func receiveBatchFiles(r *http.Request, dir string) error {
	reader, err := r.MultipartReader()
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := part.FormName()
		if (name != "baseline" && name != "current") || seen[name] {
			return errors.New("请选择基线和当期各一个核查包")
		}
		f, err := os.Create(filepath.Join(dir, name+".zip"))
		if err != nil {
			return err
		}
		n, copyErr := io.Copy(f, io.LimitReader(part, formats.BatchArchiveBytes+1))
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if n > formats.BatchArchiveBytes {
			return errors.New("核查包超过 256 MB")
		}
		seen[name] = true
	}
	if len(seen) != 2 {
		return errors.New("缺少基线或当期核查包")
	}
	return nil
}
