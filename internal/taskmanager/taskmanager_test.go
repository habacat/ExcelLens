package taskmanager

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLeaseConcurrentRetainCloseNeverResurrectsOrReleasesBorrowedResources(t *testing.T) {
	var released atomic.Int64
	owner := NewLease("immutable source", func() { released.Add(1) })
	borrowed, err := owner.Retain(nil)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var workers sync.WaitGroup
	for range 32 {
		workers.Go(func() {
			<-start
			for range 1000 {
				next, err := owner.Retain(nil)
				if err == nil {
					if next.Value() != "immutable source" {
						t.Error("source payload changed across independent leases")
					}
					next.Close()
					next.Close()
				}
			}
		})
	}
	close(start)
	owner.Close()
	workers.Wait()
	if released.Load() != 0 || borrowed.Refs() != 1 {
		t.Fatal("owner close reclaimed a live borrow", released.Load(), borrowed.Refs())
	}
	if copyLease, err := owner.Retain(nil); err == nil || copyLease != nil {
		t.Fatal("closed lease admitted a new owner")
	}
	borrowed.Close()
	borrowed.Close()
	if released.Load() != 1 || borrowed.Refs() != 0 {
		t.Fatal("final release was duplicated or leaked", released.Load(), borrowed.Refs())
	}
	if borrowed.Value() != "" || owner.data.release != nil {
		t.Fatal("a retained closed handle kept its released source payload/callback alive")
	}
}

func TestAdmissionBackgroundRespectsBudgetAndCancellation(t *testing.T) {
	adm := NewAdmission()
	rel1, err := adm.BackgroundQuery(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer rel1()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := adm.BackgroundQuery(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second background query should block on scan budget, got %v", err)
	}

	// Interactive query still gets the second slot in Query pool.
	relInteractive, err := AcquireSlot(context.Background(), adm.Query)
	if err != nil {
		t.Fatalf("interactive query should still be admitted: %v", err)
	}
	relInteractive()
	relInteractive() // idempotent
}

func TestPruneFinishedNeverEvictsExitingTask(t *testing.T) {
	type job struct {
		started time.Time
		done    chan struct{}
		closed  bool
	}
	t0 := time.Now()
	live := &job{started: t0.Add(-2 * time.Minute), done: make(chan struct{})}
	done1 := &job{started: t0.Add(-time.Minute), done: make(chan struct{})}
	close(done1.done)
	done2 := &job{started: t0, done: make(chan struct{})}
	close(done2.done)
	jobs := map[string]*job{"live": live, "done1": done1, "done2": done2}

	ok := PruneFinished(jobs, 2, func(j *job) Lifetime {
		return Lifetime{Started: j.started, Done: j.done, Cancel: func() { j.closed = true }}
	})
	if !ok || len(jobs) != 1 || jobs["live"] == nil || !done1.closed || !done2.closed {
		t.Fatalf("unexpected prune result: ok=%v len=%d jobs=%v", ok, len(jobs), jobs)
	}
	if PruneFinished(jobs, 1, func(j *job) Lifetime { return Lifetime{Started: j.started, Done: j.done} }) {
		t.Fatal("pruned a live task whose done channel is still open")
	}
}

func TestPinSetAndByteBudgetAreIdempotentAndConcurrentSafe(t *testing.T) {
	pins := NewPinSet[string]()
	u1 := pins.Pin("a", "b", "")
	u2 := pins.Pin("a")
	if pins.Count("a") != 2 || pins.Count("b") != 1 || pins.Len() != 2 || pins.Total() != 3 {
		t.Fatalf("pin counts mismatch: a=%d b=%d len=%d total=%d", pins.Count("a"), pins.Count("b"), pins.Len(), pins.Total())
	}
	u1()
	u1() // idempotent
	if pins.Count("a") != 1 || pins.Count("b") != 0 || pins.Len() != 1 {
		t.Fatalf("after u1: a=%d b=%d len=%d", pins.Count("a"), pins.Count("b"), pins.Len())
	}
	u2()
	if pins.Len() != 0 || pins.Total() != 0 {
		t.Fatalf("after u2: len=%d total=%d", pins.Len(), pins.Total())
	}

	budget := NewByteBudget(100)
	full := errors.New("full")
	r1, err := budget.Reserve(60, full)
	if err != nil || budget.Used() != 60 {
		t.Fatalf("reserve 60: err=%v used=%d", err, budget.Used())
	}
	if _, err := budget.Reserve(50, full); !errors.Is(err, full) {
		t.Fatalf("expected full error, got %v", err)
	}
	r1()
	r1() // idempotent
	if budget.Used() != 0 {
		t.Fatalf("budget leaked: %d", budget.Used())
	}
}

func TestLeaseValueDuringFinalClose(t *testing.T) {
	for range 100 {
		lease := NewLease("payload", nil)
		begin := make(chan struct{})
		done := make(chan struct{})
		go func() { <-begin; _ = lease.Value(); close(done) }()
		close(begin)
		lease.Close()
		<-done
		if lease.Value() != "" {
			t.Fatal("closed handle retained the payload")
		}
	}
}
