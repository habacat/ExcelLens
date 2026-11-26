package rowindex

import (
	"context"
	"encoding/binary"
	"slices"
	"testing"

	"excellens/internal/formats"
)

func FuzzRowProjectionMatchesLiteralPermutation(f *testing.F) {
	f.Add([]byte{127, 1, 105, 4, 7, 3, 9, 5, 8, 4, 2, 2, 3, 10, 1, 0})
	f.Add([]byte{63, 2, 61, 64, 65, 127, 128, 12, 13, 67, 3, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < 3 || len(data) > 512 {
			return
		}
		get := func(i int) byte { return data[i%len(data)] }
		n := int(get(0)%128) + 1
		offsets := make([]RowOffset, 0, n)
		var states []SourceAxisRange
		original := make([]bool, n)
		positions := make([]int, n)
		for i := 0; i < n; i++ {
			offsets = append(offsets, RowOffset{Number: i*2 + 1})
			positions[i] = i
			original[i] = get(i+3)&1 != 0
			if original[i] {
				states = append(states, SourceAxisRange{Min: i*2 + 1, Max: i*2 + 1, Hidden: true, HiddenSet: true})
			}
		}
		idx := NewRowIndex(offsets)
		for i := n - 1; i > 0; i-- {
			j := int(get(i+5)) % (i + 1)
			positions[i], positions[j] = positions[j], positions[i]
		}
		positions = positions[:int(get(2))%(n+1)]
		mode := []string{"original", "show-all", "hide-all"}[get(1)%3]
		visibility, err := BuildAxisVisibility(context.Background(), AxisLayoutInput{RowStates: states}, "row", n*2, AxisViewSettings{Mode: mode})
		if err != nil {
			t.Fatal(err)
		}
		base, err := NewRowOrder(context.Background(), "", n, positions)
		if err != nil {
			t.Fatal(err)
		}
		defer base.Close()
		through := int(get(3)) % (n*2 + 1)
		p, err := newRowProjection(context.Background(), "", &idx, n*2, base, visibility, through, nil, formats.RowProjectionMemoryBytes, &QueryQuota{max: 1})
		if err != nil {
			t.Fatal(err)
		}
		defer p.Close()
		visible := func(source int) bool {
			return source*2+1 > through && (mode == "show-all" || mode == "original" && !original[source])
		}
		var want []int
		for _, source := range positions {
			if visible(source) {
				want = append(want, source)
			}
		}
		if p.Len() != len(want) || p.SourceRows() != n || !slices.Equal(slices.Collect(p.Iter()), want) {
			t.Fatal("projected permutation changed", positions, want, p.Len())
		}
		start := int(get(8)) % (len(want) + 1)
		count := int(get(9)) % (len(want) - start + 1)
		if got := slices.Collect(p.IterRange(start, count)); !slices.Equal(got, want[start:start+count]) {
			t.Fatal("projected range iteration differs", start, count, got, want)
		}
		start = int(get(10)) % (len(positions) + 1)
		count = int(get(11)) % (len(positions) - start + 1)
		if got := slices.Collect(base.IterRange(start, count)); !slices.Equal(got, positions[start:start+count]) {
			t.Fatal("parent range iteration differs", start, count, got, positions)
		}
		for i, source := range want {
			if p.At(i) != source {
				t.Fatal(i, source, p.At(i))
			}
			if at, found := p.Position(source); !found || at != i {
				t.Fatal("inverse differs", source, at, found)
			}
		}
		for source := 0; source < n; source++ {
			if _, found := p.Position(source); found != slices.Contains(want, source) {
				t.Fatal("membership differs", source, found, want)
			}
		}
	})
}

func FuzzRowProjectionStorageValidationAndRankSelect(f *testing.F) {
	b := newRowProjectionBuilder(context.Background(), "", 128, 128, formats.RowProjectionMemoryBytes, &QueryQuota{max: 1})
	words := make([]uint64, rowProjectionBlockRows/64)
	words[0], words[1] = 0x5555555555555555, 0xAAAAAAAAAAAAAAAA
	if err := b.addBlock(128, words); err != nil {
		f.Fatal(err)
	}
	seed, _, _, err := b.finish()
	if err != nil {
		f.Fatal(err)
	}
	f.Add(seed)
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4096 {
			return
		}
		if err := validateRowProjectionStorage(context.Background(), data, 128, 128, 64); err != nil {
			return
		}
		base := ownRowOrder(&rowOrderData{count: 128, sourceRows: 128, start: 0, increasing: true})
		p := ownRowProjection(&rowProjectionData{base: base, sourceRows: 128, parentRows: 128, count: 64, storage: data, payloadBytes: int(binary.LittleEndian.Uint64(data[24:])), blocks: int(binary.LittleEndian.Uint32(data[20:]))})
		defer p.Close()
		rows := slices.Collect(p.Iter())
		if len(rows) != 64 || !slices.IsSorted(rows) {
			t.Fatal("validated mask did not produce its declared unique order", rows)
		}
		for i, row := range rows {
			if p.At(i) != row {
				t.Fatal(i, row)
			}
			if at, found := p.Position(row); !found || at != i {
				t.Fatal(row, at, found)
			}
		}
	})
}
