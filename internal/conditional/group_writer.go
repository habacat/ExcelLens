package conditional

import (
	"math"
	"math/big"
	"sort"
	"strconv"

	bolt "go.etcd.io/bbolt"
)

// Only group sums are binary64; sliding-prefix records retain their exact
// rational representation. Groups are updated in physical source order.
type groupedValue struct {
	count, first int
	sum          float64
	fault        string
	unknown      bool
}

func (g *groupedValue) add(row, weight int, v Value) {
	if g.first == 0 {
		g.first = row
	}
	g.count += weight
	if v.Fault != "" && (g.fault == "" || v.Unknown && !g.unknown) {
		g.fault, g.unknown = v.Fault, v.Unknown
	}
	if v.Number != nil && g.fault == "" {
		term, _ := v.Number.Float64()
		g.sum += term
		if math.IsInf(g.sum, 0) || math.IsNaN(g.sum) {
			g.fault = "#NUM!"
		}
	}
}

func (g *groupedValue) bytes() []byte {
	// Preserve the existing v1 disk encoding and old-reader compatibility.
	buf := recordHeader(g.count, g.first, g.fault, g.unknown)
	if g.fault != "" || g.sum == 0 {
		return buf
	}
	if math.Abs(g.sum) <= 1<<53 && math.Trunc(g.sum) == g.sum {
		return strconv.AppendInt(buf, int64(g.sum), 10)
	}
	return append(buf, new(big.Rat).SetFloat64(g.sum).RatString()...)
}

// Small key sets are aggregated once and emitted in key order. After the memory
// allowance fills, the same writer switches to the normal bounded disk path.
func (w *Writer) addGroup(id int, key []byte, row, weight int, v Value) error {
	if len(key) > bolt.MaxKeySize {
		return w.recordIssue(id, row, Value{Unknown: true, Fault: "条件统计组合键超过容量"})
	}
	if w.groups != nil {
		g := w.groups[id][string(key)]
		if g == nil && w.groupBytes+len(key)+192 <= w.groupLimit {
			if w.groups[id] == nil {
				w.groups[id] = make(map[string]*groupedValue)
			}
			g = new(groupedValue)
			w.groups[id][string(key)] = g
			w.groupBytes += len(key) + 192 // Includes entry, key, map slots and sorted flush key.
		}
		if g != nil {
			prev := len(g.fault)
			g.add(row, weight, v)
			w.groupBytes += len(g.fault) - prev
			if w.groupBytes <= w.groupLimit {
				return nil
			}
			return w.flushGroups()
		}
		if err := w.flushGroups(); err != nil {
			return err
		}
	}
	b := w.values[id]
	old, err := decode(b.Get(key))
	if err != nil {
		return err
	}
	g := groupedValue{count: old.Count, first: old.First, fault: old.Fault, unknown: old.Unknown}
	if old.Number != nil {
		g.sum, _ = old.Number.Float64()
	}
	g.add(row, weight, v)
	w.dirty = true
	return b.Put(key, g.bytes())
}

func (w *Writer) flushGroups() error {
	var keys []string
	written := 0
	for id, group := range w.groups {
		keys = keys[:0]
		for key := range group {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if written%2048 == 0 {
				if err := w.ctx.Err(); err != nil {
					return err
				}
				if written != 0 {
					if err := w.rotate(); err != nil {
						return err
					}
				}
			}
			if err := w.values[id].Put([]byte(key), group[key].bytes()); err != nil {
				return err
			}
			w.dirty = true
			written++
		}
		w.groups[id] = nil
	}
	w.groups = nil
	w.groupBytes = 0
	return nil
}

// MATCH needs the earliest source position only. Both read paths interpret the
// existing v1 group record's First field identically.
func (w *Writer) putFirstMatch(id int, key []byte, row int) error {
	if len(key) > bolt.MaxKeySize {
		return w.recordIssue(id, row, Value{Unknown: true, Fault: "条件统计组合键超过容量"})
	}
	if w.groups != nil {
		if w.groups[id][string(key)] != nil {
			return nil
		}
		return w.addGroup(id, key, row, 1, Value{})
	}
	b := w.values[id]
	if b.Get(key) != nil {
		return nil
	}
	w.dirty = true
	g := groupedValue{count: 1, first: row}
	return b.Put(key, g.bytes())
}
