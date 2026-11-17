package calculator

import (
	"context"
	"testing"
)

func TestTimePartsNeverRoundAnEarlierInstantIntoTomorrow(t *testing.T) {
	for _, date1904 := range []bool{false, true} {
		ctx := WithDateSystem(context.Background(), date1904)
		for _, tc := range [][2]string{
			{"=HOUR(0.999999999999)", "23"},
			{"=MINUTE(0.999999999999)", "59"},
			{"=SECOND(0.999999999999)", "59"},
			{"=SECOND(0.000011574073)", "0"},
			{"=DAY(DATE(2026,12,31)+0.999999999999)", "31"},
			{"=YEAR(DATE(2026,12,31)+0.999999999999)", "2026"},
		} {
			got, err := Formula(ctx, tc[0], nil)
			if err != nil || got.Display != tc[1] {
				t.Fatalf("1904=%v %s: %+v %v, want %s", date1904, tc[0], got, err, tc[1])
			}
		}
	}
}
