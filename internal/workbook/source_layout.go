package workbook

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"excellens/internal/formats"
)

const sourceLayoutCacheVersion = 6

// SourceLayout is the common display contract for OOXML, BIFF and HTML.
// User overrides are stored separately and always take precedence.
type SourceLayout struct {
	digitWidth                 float64
	Merges                     []CellMerge       `json:"merges,omitempty"`
	Rows                       []SourceSizeRange `json:"rows,omitempty"`
	Cols                       map[int]float64   `json:"cols,omitempty"`
	DefaultRow                 float64           `json:"defaultRow,omitempty"`
	DefaultCol                 float64           `json:"defaultCol,omitempty"`
	GridLines                  *bool             `json:"gridLines,omitempty"`
	RowStates                  []SourceAxisRange `json:"rowStates,omitempty"`
	ColStates                  []SourceAxisRange `json:"colStates,omitempty"`
	ZeroHeight                 bool              `json:"zeroHeight,omitempty"`
	DefaultRowZeroSize         bool              `json:"defaultRowZeroSize,omitempty"`
	DefaultColZeroSize         bool              `json:"defaultColZeroSize,omitempty"`
	SummaryBelow               *bool             `json:"summaryBelow,omitempty"`
	SummaryRight               *bool             `json:"summaryRight,omitempty"`
	ConditionalBlocks          []SourceXMLRange  `json:"conditionalBlocks,omitempty"`
	ConditionalNotice          string            `json:"conditionalNotice,omitempty"`
	ConditionalNamespaces      map[string]string `json:"conditionalNamespaces,omitempty"`
	ConditionalMainNamespace   string            `json:"conditionalMainNamespace,omitempty"`
	ConditionalNamespacesReady bool              `json:"conditionalNamespacesReady,omitempty"`
	StateNotice                string            `json:"stateNotice,omitempty"`
	capture                    *sourceMetadataCapture
	catalogue                  *worksheetCatalogueSeed
}

func (w *Workbook) normalDigitWidth() float64 {
	w.sourceDigitOnce.Do(func() {
		w.sourceDigitWidth = 7
		font := w.normalFont
		if font.FontName == "" && len(w.VisualStyles) > 0 {
			font = w.VisualStyles[0]
		}
		w.sourceDigitWidth = measureSourceDigits(font)
	})
	return w.sourceDigitWidth
}

// CellMerge is presentation metadata shared by OOXML, BIFF8 and HTML.
// Source values remain at their original addresses.
type CellMerge struct {
	Row    int `json:"row"`
	Col    int `json:"col"`
	EndRow int `json:"endRow"`
	EndCol int `json:"endCol"`
}

type SourceSizeRange struct {
	Min  int     `json:"min"`
	Max  int     `json:"max"`
	Size float64 `json:"size"`
}

func validSourceSize(size float64) bool { return size >= 10 && size <= 1200 }

func appendSourceRowSize(rows *[]SourceSizeRange, row int, size float64) {
	if row < 1 || row > maxExternalRows || !validSourceSize(size) {
		return
	}
	if len(*rows) > 0 {
		last := &(*rows)[len(*rows)-1]
		if row <= last.Max {
			return
		}
		if last.Max+1 == row && last.Size == size {
			last.Max = row
			return
		}
	}
	if len(*rows) < 100000 {
		*rows = append(*rows, SourceSizeRange{row, row, size})
	}
}

func (s SourceLayout) valid() bool {
	if s.DefaultRowZeroSize && s.DefaultRow != 0 || s.DefaultColZeroSize && s.DefaultCol != 0 {
		return false
	}
	if len(s.Merges) > 100000 || len(s.Rows) > 100000 || len(s.Cols) > 16384 || (s.DefaultRow != 0 && !validSourceSize(s.DefaultRow)) || (s.DefaultCol != 0 && !validSourceSize(s.DefaultCol)) {
		return false
	}
	last := 0
	for _, r := range s.Rows {
		if r.Min <= last || r.Max < r.Min || r.Max > maxExternalRows || !validSourceSize(r.Size) {
			return false
		}
		last = r.Max
	}
	for c, size := range s.Cols {
		if c < 1 || c > 16384 || !validSourceSize(size) {
			return false
		}
	}
	for _, m := range s.Merges {
		if m.Row < 1 || m.Col < 1 || m.EndRow < m.Row || m.EndCol < m.Col || m.EndRow > maxExternalRows || m.EndCol > 16384 {
			return false
		}
	}
	return validSourceAxisRanges(s.RowStates, formats.ExternalRows) && validSourceAxisRanges(s.ColStates, formats.ExcelColumns) && validSourceXMLRanges(s.ConditionalBlocks) && validConditionalNamespaces(s) && (len(s.ConditionalBlocks) == 0 || s.ConditionalNamespacesReady)
}

type sourceLayoutCache struct {
	Version                      int
	Fingerprint, Sheet, Checksum string
	Layout                       SourceLayout
}

type sourceLayoutLoad struct {
	done   chan struct{}
	layout SourceLayout
	err    error
}

func (c sourceLayoutCache) checksum() string {
	c.Checksum = ""
	raw, _ := json.Marshal(c)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (w *Workbook) sourceLayoutPath(meta SheetMeta) string {
	key := sha256.Sum256([]byte(fmt.Sprintf("%s|%.2f", meta.Path, w.normalDigitWidth())))
	return filepath.Join(w.CacheDir, fmt.Sprintf("source-layout-v%d-%x.json", sourceLayoutCacheVersion, key[:12]))
}

func (w *Workbook) rememberSourceLayout(meta SheetMeta, layout SourceLayout) {
	w.sourceLayoutMu.Lock()
	if w.sourceLayouts == nil {
		w.sourceLayouts = make(map[string]SourceLayout)
	}
	w.sourceLayouts[meta.Path] = layout
	w.sourceLayoutMu.Unlock()
	w.saveSourceLayout(meta, layout)
}

func (w *Workbook) saveSourceLayout(meta SheetMeta, layout SourceLayout) {
	if !layout.valid() {
		return
	}
	cache := sourceLayoutCache{Version: sourceLayoutCacheVersion, Fingerprint: w.Fingerprint, Sheet: meta.Path, Layout: layout}
	cache.Checksum = cache.checksum()
	raw, err := json.Marshal(cache)
	if err != nil || len(raw) > 16<<20 {
		return
	}
	f, err := os.CreateTemp(w.CacheDir, ".source-layout-*")
	if err != nil {
		return
	}
	name := f.Name()
	defer os.Remove(name)
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil && closeErr == nil {
		_ = os.Rename(name, w.sourceLayoutPath(meta))
	}
}

func (w *Workbook) SourceLayout(meta SheetMeta, idx *SheetIndex) (SourceLayout, error) {
	return w.SourceLayoutContext(context.Background(), meta, idx)
}

func (w *Workbook) SourceLayoutContext(ctx context.Context, meta SheetMeta, idx *SheetIndex) (SourceLayout, error) {
	if err := ctx.Err(); err != nil {
		return SourceLayout{}, err
	}
	if w.Path == "" && idx == nil {
		return SourceLayout{}, nil
	}
	if w.Format == "xls" && w.source == nil {
		return SourceLayout{}, nil
	}
	if rows, cols, lines := w.HTMLLayout(meta); lines != nil {
		return SourceLayout{Rows: rows, Cols: cols, GridLines: lines, Merges: w.HTMLMerges(meta)}, nil
	}
	if w.source != nil && w.Format != "xls" {
		return SourceLayout{}, nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return SourceLayout{}, err
		}
		w.sourceLayoutMu.Lock()
		if layout, ok := w.sourceLayouts[meta.Path]; ok {
			w.sourceLayoutMu.Unlock()
			return layout, nil
		}
		if idx == nil && w.Format != "xls" {
			w.sourceLayoutMu.Unlock()
			// No source index yet is a temporary state, not an empty layout.
			// A disk sidecar may still be usable; a miss must never be memoized.
			layout, _ := w.cachedSourceLayout(meta)
			return layout, ctx.Err()
		}
		if loading := w.sourceLayoutLoading[meta.Path]; loading != nil {
			w.sourceLayoutMu.Unlock()
			select {
			case <-ctx.Done():
				return SourceLayout{}, ctx.Err()
			case <-loading.done:
			}
			if errors.Is(loading.err, context.Canceled) || errors.Is(loading.err, context.DeadlineExceeded) {
				continue
			}
			return loading.layout, loading.err
		}
		loading := &sourceLayoutLoad{done: make(chan struct{})}
		if w.sourceLayoutLoading == nil {
			w.sourceLayoutLoading = make(map[string]*sourceLayoutLoad)
		}
		w.sourceLayoutLoading[meta.Path] = loading
		w.sourceLayoutMu.Unlock()
		layout, err := w.loadSourceLayout(ctx, meta, idx)
		if err == nil {
			err = ctx.Err()
		}
		w.sourceLayoutMu.Lock()
		if err == nil {
			if current, ok := w.sourceLayouts[meta.Path]; ok {
				layout = current
			} else {
				if w.sourceLayouts == nil {
					w.sourceLayouts = make(map[string]SourceLayout)
				}
				w.sourceLayouts[meta.Path] = layout
			}
		}
		loading.layout, loading.err = layout, err
		delete(w.sourceLayoutLoading, meta.Path)
		close(loading.done)
		w.sourceLayoutMu.Unlock()
		return layout, err
	}
}

// IO and old-layout migration run outside the map lock. Other sheets and
// cancelled waiters do not wait for this sheet's XML scan or disk sync.
func (w *Workbook) loadSourceLayout(ctx context.Context, meta SheetMeta, idx *SheetIndex) (SourceLayout, error) {
	if layout, ok := w.cachedSourceLayout(meta); ok {
		return layout, nil
	}
	var layout SourceLayout
	var err error
	if w.Format == "xls" {
		layout, err = w.readBIFFSourceLayout(ctx, meta)
	} else if idx != nil {
		layout, err = readOOXMLSourceLayoutContext(ctx, idx, w.normalDigitWidth())
	} else {
		return layout, nil
	}
	if err != nil {
		return SourceLayout{}, err
	}
	if err := ctx.Err(); err != nil {
		return SourceLayout{}, err
	}
	w.saveSourceLayout(meta, layout)
	return layout, nil
}

func (w *Workbook) cachedSourceLayout(meta SheetMeta) (SourceLayout, bool) {
	if st, err := os.Stat(w.sourceLayoutPath(meta)); err == nil && st.Size() <= 16<<20 {
		raw, err := os.ReadFile(w.sourceLayoutPath(meta))
		var cached sourceLayoutCache
		if err == nil && json.Unmarshal(raw, &cached) == nil && cached.Version == sourceLayoutCacheVersion && cached.Fingerprint == w.Fingerprint && cached.Sheet == meta.Path && cached.Layout.valid() && cached.Checksum == cached.checksum() {
			cached.Layout.digitWidth = w.normalDigitWidth()
			return cached.Layout, true
		}
	}
	return SourceLayout{}, false
}
