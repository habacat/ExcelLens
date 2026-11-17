package server

import (
	"context"
	"reflect"
	"testing"

	"excellens/internal/printing"
)

func FuzzPrintPagePhysicalSlices(f *testing.F) {
	f.Add([]byte{0, 0, 7, 1, 0}, byte(0), byte(0), byte(0))
	f.Add([]byte{255}, byte(1), byte(254), byte(1))
	f.Fuzz(func(t *testing.T, data []byte, mode, offset, span byte) {
		if len(data) == 0 || len(data) > 128 {
			return
		}
		axis := &printAxisOracle{values: map[int]int64{}}
		var units int64
		for at, value := range data {
			axis.values[at+1] = int64(value)
			units += int64(value)
		}
		part := printing.Part{First: 1, Last: len(data), Length: units, Repeated: mode&2 != 0}
		if mode&1 != 0 {
			size := int64(data[0]) + 1
			axis.values[1] = size
			part.Last, part.Offset = 1, int64(offset)%size
			part.Length = 1 + int64(span)%(size-part.Offset)
		}
		want := []printPageAxis{}
		position := -part.Offset
		for at := part.First; at <= part.Last; at++ {
			size := axis.values[at]
			left, right := max(position, 0), min(position+size, part.Length)
			if right > left {
				want = append(want, printPageAxis{Number: at, Start: position, Size: size, ClipStart: left, ClipSize: right - left, Repeated: part.Repeated})
			}
			position += size
		}
		work := &printPageWork{ctx: context.Background()}
		got, err := work.axis(axis, "row", []printing.Part{part}, 128)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatal("source coordinates/physical slices differ from independent reference", err, got, want)
		}
	})
}
