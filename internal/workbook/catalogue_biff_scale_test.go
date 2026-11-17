package workbook

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"excellens/internal/buildinfo"
	"excellens/internal/formats"
)

func TestCatalogueBIFFComplete65536RowSheet(t *testing.T) {
	if os.Getenv("EXCELLENS_BIFF_CATALOGUE_SCALE") != "1" {
		t.Skip("optional generated full BIFF8 worksheet verification, not Office/WPS acceptance")
	}
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "full-biff-sheet.xls")
	bounds := biffCatalogueSheetFixture("原表", 0)
	wbBytes := appendBIFF(nil, 0x0809, []byte{0, 6, 5, 0})
	boundsAt := len(wbBytes) + 4
	wbBytes = appendBIFF(wbBytes, 0x0085, bounds)
	wbBytes = appendBIFF(wbBytes, 0x000A, nil)
	binary.LittleEndian.PutUint32(wbBytes[boundsAt:], uint32(len(wbBytes)))
	wbBytes = appendBIFF(wbBytes, 0x0809, []byte{0, 6, 0x10, 0})
	for row := 0; row < formats.BIFFRows; row++ {
		label := make([]byte, 6)
		binary.LittleEndian.PutUint16(label, uint16(row))
		label = append(label, xlsU16(fmt.Sprintf("%010d", row))...)
		wbBytes = appendBIFF(wbBytes, 0x0204, label)
		number := make([]byte, 14)
		binary.LittleEndian.PutUint16(number, uint16(row))
		binary.LittleEndian.PutUint16(number[2:], 1)
		binary.LittleEndian.PutUint64(number[6:], math.Float64bits(float64(row)+0.5))
		wbBytes = appendBIFF(wbBytes, 0x0203, number)
	}
	feature := biffSmallTableFixture()
	binary.LittleEndian.PutUint16(feature[29:], formats.BIFFRows-1)
	for _, record := range biffFeatureRecords(feature) {
		wbBytes = append(wbBytes, record...)
	}
	wbBytes = appendBIFF(wbBytes, 0x000A, nil)
	writeBIFFCFBFixture(t, file, wbBytes)
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	beforeHash := sha256.Sum256(before)
	wb, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	meta := wb.Sheets[0]
	started := time.Now()
	idx, err := wb.BuildIndex(meta, nil, nil)
	indexMS := time.Since(started).Milliseconds()
	if err != nil || idx.RowCount() != formats.BIFFRows || idx.MaxRow != formats.BIFFRows || idx.MaxCol != 2 {
		t.Fatal("actual BIFF source extent changed", idx, err)
	}
	verified := 0
	positions := func(yield func(int) bool) {
		for row := 0; row < idx.RowCount(); row++ {
			if !yield(row) {
				return
			}
		}
	}
	verify := func(book *Workbook, index *SheetIndex) {
		t.Helper()
		verified = 0
		if err := book.VisitRowsContext(context.Background(), index, positions, 1, 2, func(row RowData) error {
			n := verified
			if row.Number != n+1 || len(row.Cells) != 2 || row.Cells[0].Ref != fmt.Sprintf("A%d", n+1) || row.Cells[0].Type != "inlineStr" || row.Cells[0].Raw != fmt.Sprintf("%010d", n) || row.Cells[1].Ref != fmt.Sprintf("B%d", n+1) || row.Cells[1].Type != "n" || row.Cells[1].Raw != strconv.Itoa(n)+".5" {
				return fmt.Errorf("source row %d changed: %+v", n+1, row)
			}
			verified++
			return nil
		}); err != nil || verified != formats.BIFFRows {
			t.Fatal("full source verification failed", verified, err)
		}
	}
	verify(wb, idx)
	assert := func(value Catalogue) {
		t.Helper()
		item := catalogueFind(t, value, "账表", "")
		if !value.Complete || item.Status != "ready" || !item.Table.Verified || item.Areas[0].End != "B65536" || item.Table.Columns[1].Name != "金额" {
			t.Fatal("full source table not confirmed", value)
		}
	}
	value, err := wb.ConfirmCatalogue(context.Background(), meta, idx, nil)
	if err != nil {
		t.Fatal(err)
	}
	assert(value)
	proof, err := os.Stat(wb.biffCataloguePath(meta))
	if err != nil || proof.Size() > 4096 {
		t.Fatal("proof retained source rows", err)
	}
	if err := os.Remove(wb.biffCataloguePath(meta)); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	loaded := reopened.GetCachedIndex(meta.Path)
	if loaded == nil {
		t.Fatal("full BIFF index did not reopen")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelledAt := int64(0)
	value, err = reopened.ConfirmCatalogue(ctx, meta, loaded, func(done, _ int64, _ string) {
		if done >= 256<<10 {
			cancelledAt = done
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) || cancelledAt == 0 || len(value.Items) != 0 {
		t.Fatal("active binary scan cancellation failed", cancelledAt, value, err)
	}
	if _, err := os.Stat(wb.biffCataloguePath(meta)); !os.IsNotExist(err) {
		t.Fatal("cancelled scan published proof", err)
	}
	started = time.Now()
	value, err = reopened.ConfirmCatalogue(context.Background(), meta, loaded, nil)
	rebuildMS := time.Since(started).Milliseconds()
	if err != nil {
		t.Fatal(err)
	}
	assert(value)
	verify(reopened, loaded)
	after, err := os.ReadFile(file)
	if err != nil || sha256.Sum256(after) != beforeHash {
		t.Fatal("source changed", err)
	}
	report := map[string]any{"version": buildinfo.Version, "result": "PASS", "generatedSourceRows": formats.BIFFRows, "verifiedSourceRowsEachPass": verified, "verificationPasses": 2, "sourceColumns": 2, "sourceBytes": len(before), "proofBytes": proof.Size(), "cancelledAtStreamByte": cancelledAt, "coldIndexMsObserved": indexMS, "legacyRebuildMsObserved": rebuildMS, "sourceSHA256": hex.EncodeToString(beforeHash[:]), "sourceUnchanged": true, "performanceComparison": false, "desktopManualAcceptance": false, "officeWpsAcceptance": false}
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(raw))
	if output := os.Getenv("EXCELLENS_BIFF_CATALOGUE_SCALE_REPORT"); output != "" {
		if err := os.WriteFile(output, append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
