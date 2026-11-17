package workbook

import (
	"context"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"sync"
)

type sheetMetadataEntry[T any] struct {
	index *T
	stamp uint64
}

type sheetMetadataLoad[T any] struct {
	done  chan struct{}
	index *T
	err   error
}

// One state belongs to one metadata kind on one Workbook. Callers retain their
// own immutable result after eviction. No IO or user callback holds the mutex.
type sheetMetadataState[T any] struct {
	mu      *sync.Mutex
	cache   *map[string]sheetMetadataEntry[T]
	loading *map[string]*sheetMetadataLoad[T]
	clock   *uint64
	bytes   *int64
}

type sheetMetadataLimits struct {
	bytes  int64
	sheets int
}

func readSheetMetadata[T any](ctx context.Context, key string, state sheetMetadataState[T], limits sheetMetadataLimits, size func(*T) int64, validate func() error, load func() (*T, error)) (*T, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validate(); err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		state.mu.Lock()
		if entry, ok := (*state.cache)[key]; ok {
			*state.clock++
			entry.stamp = *state.clock
			(*state.cache)[key] = entry
			state.mu.Unlock()
			return entry.index, nil
		}
		if pending := (*state.loading)[key]; pending != nil {
			state.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-pending.done:
			}
			if errors.Is(pending.err, context.Canceled) || errors.Is(pending.err, context.DeadlineExceeded) {
				continue
			}
			if pending.err == nil {
				if err := validate(); err != nil {
					return nil, err
				}
			}
			return pending.index, pending.err
		}
		pending := &sheetMetadataLoad[T]{done: make(chan struct{})}
		if *state.loading == nil {
			*state.loading = make(map[string]*sheetMetadataLoad[T])
		}
		(*state.loading)[key] = pending
		state.mu.Unlock()
		value, err := load()
		if err == nil {
			err = ctx.Err()
		}
		if err == nil {
			err = validate()
		}
		if err == nil && value == nil {
			err = errors.New("未取得完整源工作表元数据")
		}
		if err != nil {
			value = nil
		}
		state.mu.Lock()
		if err == nil {
			cacheSheetMetadataLocked(key, value, state, limits, size)
		}
		pending.index, pending.err = value, err
		delete(*state.loading, key)
		close(pending.done)
		state.mu.Unlock()
		return value, err
	}
}

func cacheSheetMetadataLocked[T any](key string, value *T, state sheetMetadataState[T], limits sheetMetadataLimits, size func(*T) int64) {
	if value == nil || size(value) < 0 || size(value) > limits.bytes || limits.sheets < 1 {
		return
	}
	if *state.cache == nil {
		*state.cache = make(map[string]sheetMetadataEntry[T])
	}
	cache := *state.cache
	if old, found := cache[key]; found {
		*state.bytes -= size(old.index)
		delete(cache, key)
	}
	for len(cache) > 0 && (len(cache) >= limits.sheets || size(value) > limits.bytes-*state.bytes) {
		var oldest string
		var stamp uint64
		for path, entry := range cache {
			if oldest == "" || entry.stamp < stamp {
				oldest, stamp = path, entry.stamp
			}
		}
		*state.bytes -= size(cache[oldest].index)
		delete(cache, oldest)
	}
	*state.clock++
	cache[key] = sheetMetadataEntry[T]{value, *state.clock}
	*state.bytes += size(value)
}

// Explicit metadata scans consume the already prepared worksheet XML. Verify
// actual size and the unchanged source ZIP member's CRC before publishing it.
func (w *Workbook) readVerifiedSheetXML(ctx context.Context, meta SheetMeta, idx *SheetIndex, scan func(io.Reader, int64) error) error {
	reader, err := w.openCatalogueReader(ctx)
	if err != nil {
		return err
	}
	defer reader.zip.Close()
	return reader.readVerifiedSheetXML(meta, idx, scan)
}

func (reader *catalogueReader) readVerifiedSheetXML(meta SheetMeta, idx *SheetIndex, scan func(io.Reader, int64) error) error {
	part := reader.entries[meta.Path]
	if part == nil || idx.XMLSize < 0 || part.UncompressedSize64 != uint64(idx.XMLSize) {
		return errors.New("源工作表元数据部件与当前索引不匹配")
	}
	f, err := os.Open(idx.XMLPath)
	if err != nil {
		return err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || stat.Size() != idx.XMLSize || idx.XMLSize > maxSheetXMLSize {
		return errors.New("源工作表元数据XML缓存大小无效")
	}
	checksum := crc32.NewIEEE()
	counted := &countingReader{r: io.TeeReader(f, checksum)}
	if err := scan(counted, idx.XMLSize); err != nil {
		return err
	}
	if counted.n != idx.XMLSize || checksum.Sum32() != part.CRC32 {
		return errors.New("源工作表元数据XML缓存校验失败，请重新建立工作表索引")
	}
	return reader.ctx.Err()
}
