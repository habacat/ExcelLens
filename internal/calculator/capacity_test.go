package calculator

import (
	"context"
	"testing"
)

func TestAddressSupportsBillionRowsWithoutExpandingNativeFiles(t *testing.T) {
	got, err := Formula(context.Background(), "=ADDRESS(1000000000,16384)", nil)
	if err != nil || got.Display != "$XFD$1000000000" {
		t.Fatal(got, err)
	}
	if _, err := Formula(context.Background(), "=ADDRESS(1000000001,1)", nil); err == nil {
		t.Fatal("address ceiling ignored")
	}
}
