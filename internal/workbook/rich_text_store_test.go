package workbook

import (
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

func closeRichStore(s *richTextStore) {
	if s != nil {
		runtime.SetFinalizer(s, nil)
		s.release()
	}
}
func richStoreFixture(t *testing.T) (string, string, *RichText) {
	t.Helper()
	text, value, err := styledText(t, &Workbook{}, `<si><r><rPr><b val="0"/><color rgb="FF1255AA"/></rPr><t>中😀</t></r><r><t>文</t></r></si>`)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	builder := richTextStoreBuilder{dir: dir}
	defer builder.close()
	if err := builder.append(2, text, value); err != nil {
		t.Fatal(err)
	}
	if err := builder.append(9, text, value); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rich.bin")
	store, err := builder.finish(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	closeRichStore(store)
	return path, text, value
}

func TestRichTextStoreSparseLookupKeepsSourceIdentityAndIndependentFontObjects(t *testing.T) {
	path, text, _ := richStoreFixture(t)
	store, err := openRichTextStore(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer closeRichStore(store)
	if store.count != 2 {
		t.Fatal(store.count)
	}
	for _, id := range []int{0, 1, 3, 8, 10, 99999} {
		if value, err := store.lookup(id, text); err != nil || value != nil {
			t.Fatal(id, value, err)
		}
	}
	for _, id := range []int{2, 9} {
		value, err := store.lookup(id, text)
		if err != nil || value.Runs[0].End != 3 || value.Runs[0].Font.Bold == nil || *value.Runs[0].Font.Bold || value.Runs[0].Font.Color != "#1255AA" {
			t.Fatal(id, value, err)
		}
	}
	first, _ := store.lookup(2, text)
	*first.Runs[0].Font.Bold = true
	second, _ := store.lookup(2, text)
	if *second.Runs[0].Font.Bold {
		t.Fatal("decoded cell mutated the shared immutable cache")
	}
	if _, err := store.lookup(2, "不同原文"); err == nil {
		t.Fatal("invalid spans rebound to different source text")
	}
}

func TestRichTextStoreRejectsCorruptHeaderIndexAndRecordWithoutLargeAllocations(t *testing.T) {
	path, text, _ := richStoreFixture(t)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func([]byte){
		func(data []byte) { data[0] = 'X' },
		func(data []byte) {
			binary.LittleEndian.PutUint64(data[8:16], 1<<50)
			binary.LittleEndian.PutUint32(data[28:32], crc32.ChecksumIEEE(data[:28]))
		},
		func(data []byte) {
			at := int(binary.LittleEndian.Uint64(data[16:24]))
			data[at] = 99
			binary.LittleEndian.PutUint32(data[24:28], crc32.ChecksumIEEE(data[at:]))
			binary.LittleEndian.PutUint32(data[28:32], crc32.ChecksumIEEE(data[:28]))
		},
		func(data []byte) {
			at := int(binary.LittleEndian.Uint64(data[16:24]))
			binary.LittleEndian.PutUint64(data[at+4:at+12], 1<<50)
			binary.LittleEndian.PutUint32(data[24:28], crc32.ChecksumIEEE(data[at:]))
			binary.LittleEndian.PutUint32(data[28:32], crc32.ChecksumIEEE(data[:28]))
		},
	} {
		data := append([]byte{}, original...)
		mutate(data)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		store, err := openRichTextStore(path, 10)
		if err == nil {
			closeRichStore(store)
			t.Fatal("corrupt mapped metadata accepted")
		}
	}
	if err := os.WriteFile(path, original[:len(original)-1], 0600); err != nil {
		t.Fatal(err)
	}
	if store, err := openRichTextStore(path, 10); err == nil {
		closeRichStore(store)
		t.Fatal("truncated style cache accepted")
	}
	data := append([]byte{}, original...)
	data[richStoreHeader+richStoreRecordHeader+10] ^= 1
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := openRichTextStore(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer closeRichStore(store)
	if _, err := store.lookup(2, text); err == nil {
		t.Fatal("unchecked metadata record reached a cell")
	}
}

func TestRichTextStoreBuilderRejectsBadKeysAndLeavesVerifiedMappedCacheUsable(t *testing.T) {
	path, text, value := richStoreFixture(t)
	existing, err := openRichTextStore(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer closeRichStore(existing)
	before, _ := os.ReadFile(path)
	builder := richTextStoreBuilder{dir: filepath.Dir(path)}
	defer builder.close()
	if err := builder.append(2, text, value); err != nil {
		t.Fatal(err)
	}
	if err := builder.append(2, text, value); err == nil {
		t.Fatal("duplicate key accepted")
	}
	if err := builder.append(1, text, value); err == nil {
		t.Fatal("decreasing key accepted")
	}
	if err := builder.append(-1, text, value); err == nil {
		t.Fatal("negative key accepted")
	}
	if _, err := builder.finish(path, 2); err == nil {
		t.Fatal("out-of-range key published")
	}
	// Publishing a second valid file for the same immutable source reuses the
	// existing verified file, including when its Windows mapping is active.
	fresh, err := builder.finish(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	closeRichStore(fresh)
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("builder replaced a verified live style cache")
	}
	if _, err := existing.lookup(9, text); err != nil {
		t.Fatal("existing reader invalidated", err)
	}
	plain := richTextStoreBuilder{dir: t.TempDir()}
	defer plain.close()
	if err := plain.append(1, "ordinary", nil); err != nil {
		t.Fatal(err)
	}
	if plain.file != nil {
		t.Fatal("ordinary strings created a presentation cache")
	}
}

func TestRichTextStoreStreamingIndexAndConcurrentLookupsSurviveGC(t *testing.T) {
	dir := t.TempDir()
	builder := richTextStoreBuilder{dir: dir}
	defer builder.close()
	text, value, err := styledText(t, &Workbook{}, `<si><r><rPr><b/></rPr><t>中😀</t></r></si>`)
	if err != nil {
		t.Fatal(err)
	}
	const count = 10001
	for i := 0; i < count; i++ {
		if err := builder.append(i*2, text, value); err != nil {
			t.Fatal(err)
		}
	}
	store, err := builder.finish(filepath.Join(dir, "rich.bin"), count*2)
	if err != nil {
		t.Fatal(err)
	}
	defer closeRichStore(store)
	var wait sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			for i := worker; i < count; i += 4 {
				got, err := store.lookup(i*2, text)
				if err != nil || got == nil || len(got.Runs) != 1 || got.Runs[0].Font.Bold == nil || !*got.Runs[0].Font.Bold {
					t.Errorf("mapped lookup %d failed: %v", i, err)
					return
				}
				if i%997 == 0 {
					runtime.GC()
				}
			}
		}(worker)
	}
	wait.Wait()
}
