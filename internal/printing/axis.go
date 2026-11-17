package printing

import (
	"errors"
	"sort"

	"excellens/internal/formats"
)

func normalize(intervals []Interval, maximum int) ([]Interval, error) {
	if len(intervals) > formats.PrintPaginationAreas {
		return nil, errors.New("重复标题范围数量超过容量")
	}
	result := append([]Interval(nil), intervals...)
	sort.Slice(result, func(i, j int) bool { return result[i].First < result[j].First })
	n := 0
	for _, item := range result {
		if item.First < 1 || item.Last < item.First || item.Last > maximum {
			return nil, errors.New("重复标题源范围越界")
		}
		if n > 0 && result[n-1].Last+1 >= item.First {
			result[n-1].Last = max(result[n-1].Last, item.Last)
		} else {
			result[n] = item
			n++
		}
	}
	return result[:n], nil
}

func subtract(area Interval, titles []Interval) []Interval {
	result := []Interval{}
	first := area.First
	for _, title := range titles {
		if title.Last < first {
			continue
		}
		if title.First > area.Last {
			break
		}
		if title.First > first {
			result = append(result, Interval{first, title.First - 1})
		}
		first = max(first, title.Last+1)
	}
	if first <= area.Last {
		result = append(result, Interval{first, area.Last})
	}
	return result
}

func (w *work) extent(g geometry, axis string, first, last int) (int64, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	w.calls++
	if w.calls > formats.PrintPaginationWork {
		return 0, errors.New("打印分页工作量超过容量")
	}
	value, err := g.Extent(w.ctx, axis, first, last)
	if err != nil {
		return 0, err
	}
	w.approximate = w.approximate || value.Approximate
	w.defaulted = w.defaulted || value.Defaulted
	return value.Units, nil
}

func (w *work) parts(g geometry, axis string, intervals []Interval, repeated bool) ([]Part, int64, error) {
	result := []Part{}
	var total int64
	for _, item := range intervals {
		units, err := w.extent(g, axis, item.First, item.Last)
		if err != nil {
			return nil, 0, err
		}
		total, err = add(total, units)
		if err != nil {
			return nil, 0, err
		}
		if units > 0 {
			result = append(result, Part{First: item.First, Last: item.Last, Length: units, Repeated: repeated})
		}
	}
	return result, total, nil
}

// Axis packing streams coordinates, never cell IDs. A large cell is split into
// physical slices so no part disappears; hidden gaps cost one prefix query.
func (w *work) pack(g geometry, axis string, body []Interval, titles []Part, titleUnits, capacity int64, breaks []int, stopAfter int, collect bool) ([]axisBand, int, error) {
	if capacity > 0 && titleUnits == capacity {
		for _, interval := range body {
			value, err := w.extent(g, axis, interval.First, interval.Last)
			if err != nil {
				return nil, 0, err
			}
			if value != 0 {
				return nil, 0, errors.New("重复标题占满纸张，正文无法分页")
			}
		}
		if collect {
			return []axisBand{{parts: append([]Part(nil), titles...), used: titleUnits}}, 1, nil
		}
		return nil, 1, nil
	}
	if capacity <= 0 || titleUnits > capacity {
		return nil, 0, errors.New("重复标题或边距占满纸张，请调整纸张或缩放")
	}
	bodyCapacity := capacity - titleUnits
	var bands []axisBand
	count, parts := 0, 0
	current := axisBand{parts: []Part{}, used: titleUnits}
	remaining := bodyCapacity
	flush := func() error {
		if current.used == titleUnits && titleUnits == 0 {
			return nil
		}
		count++
		if count > formats.PrintPaginationPages && stopAfter == 0 {
			return errors.New("打印预览页数超过容量，未截断分页结果")
		}
		if collect {
			current.parts = append(append([]Part(nil), titles...), current.parts...)
			bands = append(bands, current)
		}
		current = axisBand{parts: []Part{}, used: titleUnits}
		remaining = bodyCapacity
		return nil
	}
	for _, interval := range body {
		position := interval.First
		var offset int64
		for position <= interval.Last {
			if err := w.ctx.Err(); err != nil {
				return nil, 0, err
			}
			limit := interval.Last
			manual := false
			if at := sort.SearchInts(breaks, position+1); at < len(breaks) && breaks[at] <= limit {
				limit = breaks[at] - 1
				manual = true
			}
			length, err := w.extent(g, axis, position, limit)
			if err != nil {
				return nil, 0, err
			}
			if length == 0 {
				position = limit + 1
				if manual && current.used > titleUnits {
					if err := flush(); err != nil {
						return nil, 0, err
					}
				}
				continue
			}
			if remaining == 0 {
				if err := flush(); err != nil {
					return nil, 0, err
				}
				if stopAfter > 0 && count > stopAfter {
					return nil, count, nil
				}
				continue
			}
			last := position - 1
			if offset == 0 {
				low, high := position, limit
				for low <= high {
					middle := low + (high-low)/2
					value, err := w.extent(g, axis, position, middle)
					if err != nil {
						return nil, 0, err
					}
					if value <= remaining {
						last = middle
						low = middle + 1
					} else {
						high = middle - 1
					}
				}
			}
			part := Part{First: position}
			if last >= position {
				part.Last = last
				part.Length, err = w.extent(g, axis, position, last)
				position = last + 1
			} else {
				if current.used > titleUnits && offset == 0 {
					if err := flush(); err != nil {
						return nil, 0, err
					}
					if stopAfter > 0 && count > stopAfter {
						return nil, count, nil
					}
					continue
				}
				cell, readErr := w.extent(g, axis, position, position)
				if readErr != nil {
					return nil, 0, readErr
				}
				part.Last, part.Offset, part.Length = position, offset, min(remaining, cell-offset)
				current.oversize = true
				offset += part.Length
				if offset == cell {
					position++
					offset = 0
				}
			}
			if err != nil {
				return nil, 0, err
			}
			if part.Length == 0 && last >= part.First {
				continue
			}
			if part.Length <= 0 {
				return nil, 0, errors.New("打印分页未取得可推进的物理长度")
			}
			current.used += part.Length
			remaining -= part.Length
			parts++
			if parts > formats.PrintPaginationParts {
				return nil, 0, errors.New("打印分页片段超过容量")
			}
			if collect {
				current.parts = append(current.parts, part)
			}
			if manual && position > limit && offset == 0 {
				if err := flush(); err != nil {
					return nil, 0, err
				}
				if stopAfter > 0 && count > stopAfter {
					return nil, count, nil
				}
			}
		}
	}
	if current.used > titleUnits || count == 0 && titleUnits > 0 {
		if err := flush(); err != nil {
			return nil, 0, err
		}
	}
	return bands, count, nil
}

func (w *work) fitCapacity(g geometry, axis string, body []Interval, titles []Part, titleUnits, total int64, pages int) (int64, error) {
	if total == 0 {
		return titleUnits, nil
	}
	if pages == 1 {
		return add(titleUnits, total)
	}
	low, high := total/int64(pages), total
	if low == 0 {
		low = 1
	}
	for low < high {
		middle := low + (high-low)/2
		capacity, err := add(titleUnits, middle)
		if err != nil {
			return 0, err
		}
		_, count, err := w.pack(g, axis, body, titles, titleUnits, capacity, nil, pages, false)
		if err != nil {
			return 0, err
		}
		if count <= pages {
			high = middle
		} else {
			low = middle + 1
		}
	}
	return add(titleUnits, low)
}
