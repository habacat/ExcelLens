package workbook

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSearchSourceStampSurvivesWarmReopenAndRejectsUnownedIndex(t *testing.T) {
	w, idx, _ := searchResumeNative(t, 8)
	stamp, err := w.SearchSourceStamp(context.Background(), w.Sheets[0], idx)
	if err != nil || len(stamp) != 64 {
		t.Fatal(stamp, err)
	}
	reopened, err := OpenWorkbook(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	loaded := reopened.GetCachedIndex(w.Sheets[0].Path)
	got, err := reopened.SearchSourceStamp(context.Background(), reopened.Sheets[0], loaded)
	if err != nil || got != stamp {
		t.Fatal("warm reopen lost the exact reading identity", got, stamp, err)
	}
	if _, err := reopened.SearchSourceStamp(context.Background(), reopened.Sheets[0], idx); err == nil {
		t.Fatal("index borrowed from another Workbook was accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := reopened.SearchSourceStamp(ctx, reopened.Sheets[0], loaded); err != context.Canceled {
		t.Fatal("binding ignored cancellation", err)
	}
}

func TestSearchSourceStampDetectsSameSizeCacheRewriteWithRestoredTime(t *testing.T) {
	w, idx, _ := searchResumeNative(t, 8)
	for _, path := range []string{idx.IndexPath, idx.XMLPath} {
		before, err := w.SearchSourceStamp(context.Background(), w.Sheets[0], idx)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		data[len(data)-1] ^= 1
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
			t.Fatal(err)
		}
		after, err := w.SearchSourceStamp(context.Background(), w.Sheets[0], idx)
		if err == nil && before == after {
			t.Fatal("same-size rewrite retained its saved identity", filepath.Base(path))
		}
	}
}
