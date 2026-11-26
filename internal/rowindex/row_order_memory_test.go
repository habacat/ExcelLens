package rowindex

import (
	"context"
	"testing"
)

func TestRowOrderRetainedHeapSeparatesResidentArraysMappedPagesAndIndependentLeases(t *testing.T) {
	ctx, dir := context.Background(), t.TempDir()
	resident, err := BuildRowOrder(ctx, dir, 2000, func(ctx context.Context, add func(int) error) error {
		for i := 999; i >= 0; i-- {
			if err := add(i * 2); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resident.Close()
	residentBytes, err := resident.RetainedHeapBytes()
	if err != nil || residentBytes < 12*1000 {
		t.Fatal("resident order/inverse arrays were omitted", residentBytes, err)
	}
	mapped, err := BuildRowOrder(ctx, dir, 540_000, func(ctx context.Context, add func(int) error) error {
		for i := 0; i < 270_000; i++ {
			if err := add(i * 2); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer mapped.Close()
	bytes, err := mapped.RetainedHeapBytes()
	if err != nil || bytes <= 0 || bytes >= residentBytes || !QueryFilesInUse(dir) {
		t.Fatal("mapped storage was conflated with retained Go heap or lost disk reservation", bytes, err)
	}
	borrowed, err := mapped.Retain()
	if err != nil {
		t.Fatal(err)
	}
	defer borrowed.Close()
	if err := mapped.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := mapped.RetainedHeapBytes(); err == nil {
		t.Fatal("closed owner still inspected live order memory")
	}
	borrowedBytes, err := borrowed.RetainedHeapBytes()
	if err != nil || borrowedBytes != bytes || !QueryFilesInUse(dir) {
		t.Fatal("memory estimate borrowed the wrong lease", borrowedBytes, err)
	}
	if err := borrowed.Close(); err != nil || QueryFilesInUse(dir) {
		t.Fatal("last mapped lease failed to return disk reservation", err)
	}
}
