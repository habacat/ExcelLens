package printing

import (
	"context"
	"errors"
	"math/rand"
	"sort"
	"testing"

	"excellens/internal/workbook"
)

type testGeometry []int64

func (g testGeometry) Extent(ctx context.Context, _ string, first, last int) (workbook.PrintGeometryExtent, error) {
	if err := ctx.Err(); err != nil {
		return workbook.PrintGeometryExtent{}, err
	}
	if first < 1 || last < first || last > len(g) {
		return workbook.PrintGeometryExtent{}, errors.New("test coordinate outside source")
	}
	var sum int64
	for _, value := range g[first-1 : last] {
		sum += value
	}
	return workbook.PrintGeometryExtent{Units: sum}, nil
}

func TestAxisRandomPhysicalCoverageManualBoundariesAndFitMinimum(t *testing.T) {
	random := rand.New(rand.NewSource(2026111603))
	for iteration := 0; iteration < 1200; iteration++ {
		n := 1 + random.Intn(40)
		g := make(testGeometry, n+1)
		var total int64
		for at := 0; at < n; at++ {
			g[at] = int64(random.Intn(30))
			total += g[at]
		}
		capacity := int64(1 + random.Intn(60))
		title := int64(random.Intn(int(capacity)))
		g[n] = title
		var titleParts []Part
		if title > 0 {
			titleParts = []Part{{First: n + 1, Last: n + 1, Length: title, Repeated: true}}
		}
		var breaks []int
		for at := 2; at <= n; at++ {
			if random.Intn(7) == 0 {
				breaks = append(breaks, at)
			}
		}
		w := &work{ctx: context.Background()}
		bands, count, err := w.pack(g, "row", []Interval{{1, n}}, titleParts, title, capacity, breaks, 0, true)
		if err != nil || count != len(bands) {
			t.Fatal(iteration, err, count, len(bands))
		}
		coverage := make([][]Interval, n)
		for _, band := range bands {
			if band.used > capacity || band.used <= 0 {
				t.Fatal("page physical boundary", iteration, band, capacity)
			}
			var used int64
			first, last := 0, 0
			for _, part := range band.parts {
				used += part.Length
				if part.Repeated {
					if part.First != n+1 || part.Length != title {
						t.Fatal(part)
					}
					continue
				}
				if first == 0 {
					first = part.First
				}
				last = part.Last
				if part.First == part.Last {
					if part.Offset < 0 || part.Offset+part.Length > g[part.First-1] {
						t.Fatal("bad source slice", part)
					}
					coverage[part.First-1] = append(coverage[part.First-1], Interval{int(part.Offset), int(part.Offset + part.Length)})
				} else {
					if part.Offset != 0 {
						t.Fatal(part)
					}
					var length int64
					for row := part.First; row <= part.Last; row++ {
						length += g[row-1]
						if g[row-1] > 0 {
							coverage[row-1] = append(coverage[row-1], Interval{0, int(g[row-1])})
						}
					}
					if length != part.Length {
						t.Fatal("part length disagrees with source", part, length)
					}
				}
			}
			if used != band.used {
				t.Fatal("page sum", used, band.used)
			}
			for _, boundary := range breaks {
				if first < boundary && last >= boundary {
					t.Fatal("manual boundary crossed", iteration, first, last, boundary)
				}
			}
		}
		for row, parts := range coverage {
			sort.Slice(parts, func(i, j int) bool { return parts[i].First < parts[j].First })
			cursor := 0
			for _, part := range parts {
				if part.First != cursor {
					t.Fatal("lost or duplicated source physical content", iteration, row, parts)
				}
				cursor = part.Last
			}
			if int64(cursor) != g[row] {
				t.Fatal("incomplete physical source coverage", iteration, row, cursor, g[row])
			}
		}
		if total > 0 {
			pages := 1 + random.Intn(8)
			w = &work{ctx: context.Background()}
			minimum, err := w.fitCapacity(g, "row", []Interval{{1, n}}, titleParts, title, total, pages)
			if err != nil {
				t.Fatal(iteration, err)
			}
			_, count, err = w.pack(g, "row", []Interval{{1, n}}, titleParts, title, minimum, nil, pages, false)
			if err != nil || count > pages {
				t.Fatal("fit target missed", iteration, minimum, count, pages, err)
			}
			if minimum > title+1 {
				_, count, err = w.pack(g, "row", []Interval{{1, n}}, titleParts, title, minimum-1, nil, pages, false)
				if err == nil && count <= pages {
					t.Fatal("fit wasted usable page scale", iteration, minimum, count, pages)
				}
			}
		}
	}
}

func TestAxisOnlyTitlesMayFillPageButBodyCannotBeDropped(t *testing.T) {
	w := &work{ctx: context.Background()}
	g := testGeometry{20, 0}
	bands, count, err := w.pack(g, "row", []Interval{{2, 2}}, []Part{{First: 1, Last: 1, Length: 20, Repeated: true}}, 20, 20, nil, 0, true)
	if err != nil || count != 1 || len(bands[0].parts) != 1 {
		t.Fatal(bands, count, err)
	}
	g[1] = 1
	if _, _, err = w.pack(g, "row", []Interval{{2, 2}}, []Part{{First: 1, Last: 1, Length: 20, Repeated: true}}, 20, 20, nil, 0, true); err == nil {
		t.Fatal("body lost when titles filled paper")
	}
}
