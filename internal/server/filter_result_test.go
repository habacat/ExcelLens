package server

import (
	"context"
	"testing"

	"excellens/internal/workbook"
)

func rowOrderFixture(t testing.TB, sourceRows int, positions ...int) *workbook.RowOrder {
	t.Helper()
	rows, err := workbook.NewRowOrder(context.Background(), t.TempDir(), sourceRows, positions)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := rows.Close(); err != nil {
			t.Error(err)
		}
	})
	return rows
}
