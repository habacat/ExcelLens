package workbook

import (
	"context"
	"errors"

	"excellens/internal/formats"
)

type conditionalCacheEntry struct {
	index *SourceConditionalFormats
	stamp uint64
}
type conditionalLoad struct {
	done  chan struct{}
	index *SourceConditionalFormats
	err   error
}

// SourceConditionalFormats returns an immutable range/rule index. Only the
// presentation caller opts in; ordinary value reads do not call this method.
// The 32MiB memo budget excludes source strings, DXFs and active callers.
func (w *Workbook) SourceConditionalFormats(ctx context.Context, meta SheetMeta, idx *SheetIndex) (*SourceConditionalFormats, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	capability, ok := formats.Lookup(w.Format)
	if !ok || !capability.ConditionalMetadata || idx == nil {
		return &SourceConditionalFormats{root: -1}, nil
	}
	if idx.SheetPath != meta.Path {
		return nil, errors.New("条件格式索引与来源工作表不匹配")
	}
	actual, found := w.FindSheet(meta.Name)
	if !found || actual.Path != meta.Path {
		return nil, errors.New("条件格式来源工作表不存在")
	}
	key := meta.Path
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		w.conditionalMu.Lock()
		if cached, ok := w.conditionalCache[key]; ok {
			w.conditionalClock++
			cached.stamp = w.conditionalClock
			w.conditionalCache[key] = cached
			w.conditionalMu.Unlock()
			return cached.index, nil
		}
		if loading := w.conditionalLoading[key]; loading != nil {
			w.conditionalMu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-loading.done:
			}
			if errors.Is(loading.err, context.Canceled) || errors.Is(loading.err, context.DeadlineExceeded) {
				continue
			}
			return loading.index, loading.err
		}
		loading := &conditionalLoad{done: make(chan struct{})}
		if w.conditionalLoading == nil {
			w.conditionalLoading = make(map[string]*conditionalLoad)
		}
		w.conditionalLoading[key] = loading
		w.conditionalMu.Unlock()
		index, err := w.loadConditionalFormats(ctx, meta, idx)
		if err == nil {
			err = ctx.Err()
		}
		w.conditionalMu.Lock()
		loading.index, loading.err = index, err
		delete(w.conditionalLoading, key)
		if err == nil && index != nil {
			if index.bytes == 0 {
				index.bytes = conditionalIndexBytes(index)
			}
			w.cacheConditionalLocked(key, index)
		}
		close(loading.done)
		w.conditionalMu.Unlock()
		return index, err
	}
}

func (w *Workbook) cacheConditionalLocked(key string, index *SourceConditionalFormats) {
	if index.bytes < 0 || index.bytes > formats.ConditionalCacheBytes {
		return
	}
	if w.conditionalCache == nil {
		w.conditionalCache = make(map[string]conditionalCacheEntry)
	}
	if old, ok := w.conditionalCache[key]; ok {
		w.conditionalBytes -= old.index.bytes
		delete(w.conditionalCache, key)
	}
	for w.conditionalBytes+index.bytes > formats.ConditionalCacheBytes && len(w.conditionalCache) > 0 {
		var oldest string
		var stamp uint64
		for path, entry := range w.conditionalCache {
			if oldest == "" || entry.stamp < stamp {
				oldest, stamp = path, entry.stamp
			}
		}
		w.conditionalBytes -= w.conditionalCache[oldest].index.bytes
		delete(w.conditionalCache, oldest)
	}
	w.conditionalClock++
	w.conditionalCache[key] = conditionalCacheEntry{index: index, stamp: w.conditionalClock}
	w.conditionalBytes += index.bytes
}
