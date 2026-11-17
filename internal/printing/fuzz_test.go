package printing

import (
	"context"
	"testing"
)

func FuzzAxisPagination(f *testing.F) {
	f.Add([]byte{0, 30, 2, 0, 100}, uint16(17), uint16(3))
	f.Add([]byte{1, 1, 1}, uint16(1), uint16(0))
	f.Fuzz(func(t *testing.T, data []byte, rawCapacity, rawTitle uint16) {
		if len(data) == 0 || len(data) > 64 {
			return
		}
		capacity := int64(rawCapacity%1024) + 1
		title := int64(rawTitle) % capacity
		g := make(testGeometry, len(data)+1)
		var total int64
		for at, value := range data {
			g[at] = int64(value)
			total += int64(value)
		}
		g[len(data)] = title
		var titles []Part
		if title > 0 {
			titles = []Part{{First: len(data) + 1, Last: len(data) + 1, Length: title, Repeated: true}}
		}
		w := &work{ctx: context.Background()}
		bands, count, err := w.pack(g, "row", []Interval{{1, len(data)}}, titles, title, capacity, nil, 0, true)
		if err != nil {
			if bands != nil || count != 0 {
				t.Fatal("partial pages on failure")
			}
			return
		}
		if count != len(bands) {
			t.Fatal("page count")
		}
		var sum int64
		for _, band := range bands {
			if band.used > capacity || band.used <= 0 {
				t.Fatal("page overflow")
			}
			for _, part := range band.parts {
				if part.Length <= 0 || part.Offset < 0 {
					t.Fatal("invalid physical slice")
				}
				if !part.Repeated {
					sum += part.Length
				}
			}
		}
		if sum != total {
			t.Fatal("source length lost or duplicated", sum, total)
		}
	})
}

func FuzzPaperMeasure(f *testing.F) {
	for _, value := range []string{"25.4mm", "0.7in", "1e-1000000000pt", "900000000000pt", "NaN", "0.0000000001pt"} {
		f.Add(value)
	}
	f.Fuzz(func(t *testing.T, value string) {
		if len(value) > 129 {
			return
		}
		floor, err := measure(value, false)
		if err != nil {
			return
		}
		ceil, err := measure(value, true)
		if err == nil && (floor < 0 || ceil < floor || ceil-floor > 1) {
			t.Fatal("measure rounding bound", value, floor, ceil)
		}
	})
}
