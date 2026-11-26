package server

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"excellens/internal/composition"
)

func TestCompositionActiveScanKeepsItsOwnReferenceWhenOwnerCloses(t *testing.T) {
	a, _ := recordFixture(t, 12, "")
	snapshot := compositionCapture(t, a, compositionDefinition(t, a))
	started, proceed := make(chan struct{}), make(chan struct{})
	finished := make(chan error, 1)
	var once sync.Once
	release := func() { once.Do(func() { close(proceed) }) }
	t.Cleanup(release)
	go func() {
		a.cacheMu.RLock()
		defer a.cacheMu.RUnlock()
		count := 0
		finished <- snapshot.scan(context.Background(), []int{1}, func(composition.Record) error {
			count++
			if count == 1 {
				close(started)
				<-proceed
			}
			return nil
		})
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("scan did not start")
	}
	snapshot.close()
	if snapshot.refs() != 1 {
		t.Fatal("active scan did not retain an independent reference")
	}
	release()
	select {
	case err := <-finished:
		if err != nil || snapshot.refs() != 0 {
			t.Fatal("owner close interrupted a retained scan or leaked its reference", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("retained scan did not finish")
	}
}

func TestCompositionConcurrentRetainCloseNeverResurrectsReleasedSources(t *testing.T) {
	var releases atomic.Int64
	owner := ownCompositionSnapshot(&compositionSnapshotData{release: []func(){func() { releases.Add(1) }}})
	start := make(chan struct{})
	var workers sync.WaitGroup
	for range 32 {
		workers.Go(func() {
			<-start
			for range 1000 {
				copy, err := owner.retain()
				if err == nil {
					copy.close()
					copy.close()
				}
			}
		})
	}
	close(start)
	owner.close()
	workers.Wait()
	if releases.Load() != 1 || owner.refs() != 0 {
		t.Fatal("source ownership was resurrected or released more than once", releases.Load(), owner.refs())
	}
}
