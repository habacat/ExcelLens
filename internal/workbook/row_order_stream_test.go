package workbook

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestStreamingRowOrderRejectsUpstreamAndIgnoredAdmissionErrors(t *testing.T) {
	sentinel := errors.New("upstream failed")
	for _, kind := range []string{"upstream", "duplicate", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			order, err := BuildRowOrder(ctx, dir, 1_000_000, func(ctx context.Context, add func(int) error) error {
				for i := 0; i < 270_000; i++ {
					if err := add(i * 2); err != nil {
						return err
					}
				}
				switch kind {
				case "upstream":
					return sentinel
				case "duplicate":
					_ = add(538_000)
					_ = add(538_000) // an uncooperative scanner ignores the error
				case "cancelled":
					cancel()
					_ = add(540_000)
				}
				return nil
			})
			if order != nil || err == nil || kind == "upstream" && !errors.Is(err, sentinel) || kind == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatal(order, err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 || QueryFilesInUse(dir) {
				t.Fatal("failed streamed order leaked a partial run", entries, err)
			}
		})
	}
}

func TestStreamingRowOrderUsesExistingMappedLeaseAndInverse(t *testing.T) {
	dir := t.TempDir()
	order, err := BuildRowOrder(context.Background(), dir, 300_000, func(ctx context.Context, add func(int) error) error {
		for i := 299_999; i >= 0; i-- {
			if err := add(i); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	borrowed, err := order.Retain()
	if err != nil {
		t.Fatal(err)
	}
	if err := order.Close(); err != nil {
		t.Fatal(err)
	}
	if borrowed.Len() != 300_000 || borrowed.At(299_999) != 0 {
		t.Fatal("streaming order lost mapped values")
	}
	if position, found := borrowed.Position(123); !found || position != 299_876 {
		t.Fatal(position, found)
	}
	if !QueryFilesInUse(dir) {
		t.Fatal("borrowed mapping lost its shared disk reservation")
	}
	if err := borrowed.Close(); err != nil {
		t.Fatal(err)
	}
	if QueryFilesInUse(dir) {
		t.Fatal("last borrower leaked the temporary order")
	}
}
