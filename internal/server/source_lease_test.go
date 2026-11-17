package server

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestSourceLeaseConcurrentRetainCloseNeverResurrectsOrReleasesBorrowedResources(t *testing.T) {
	var released atomic.Int64
	owner := newSourceLease("immutable source", func() { released.Add(1) })
	borrowed, err := owner.retain()
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var workers sync.WaitGroup
	for range 32 {
		workers.Go(func() {
			<-start
			for range 1000 {
				next, err := owner.retain()
				if err == nil {
					if next.data.value != "immutable source" {
						t.Error("source payload changed across independent leases")
					}
					next.close()
					next.close()
				}
			}
		})
	}
	close(start)
	owner.close()
	workers.Wait()
	if released.Load() != 0 || borrowed.data.refs.Load() != 1 {
		t.Fatal("owner close reclaimed a live borrow", released.Load(), borrowed.data.refs.Load())
	}
	if copy, err := owner.retain(); err == nil || copy != nil {
		t.Fatal("closed lease admitted a new owner")
	}
	borrowed.close()
	borrowed.close()
	if released.Load() != 1 || borrowed.data.refs.Load() != 0 {
		t.Fatal("final release was duplicated or leaked", released.Load(), borrowed.data.refs.Load())
	}
	if borrowed.data.value != "" || owner.data.release != nil {
		t.Fatal("a retained closed handle kept its released source payload/callback alive")
	}
}
