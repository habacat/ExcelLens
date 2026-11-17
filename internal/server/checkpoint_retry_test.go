package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"excellens/internal/checkpoint"
)

func TestCheckpointAdmissionRetryNeverRepeatsUncertainOrOtherFailures(t *testing.T) {
	attempts := 0
	value, err := retryCheckpointBusy(context.Background(), func() (int, error) {
		attempts++
		if attempts <= 2 {
			return 0, checkpoint.ErrBusy
		}
		return 7, nil
	})
	if err != nil || value != 7 || attempts != 3 {
		t.Fatal("transient admission conflict failed a task", attempts, value, err)
	}
	for _, failure := range []error{checkpoint.ErrUncertain, errors.Join(checkpoint.ErrUncertain, checkpoint.ErrBusy), checkpoint.ErrConflict, checkpoint.ErrCorrupt, checkpoint.ErrCapacity, errors.New("disk full")} {
		attempts = 0
		_, err := retryCheckpointBusy(context.Background(), func() (int, error) { attempts++; return 0, failure })
		if err != failure || attempts != 1 {
			t.Fatal("non-retryable/uncertain persistence was replayed", attempts, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	_, err = retryCheckpointBusy(ctx, func() (int, error) { return 0, checkpoint.ErrBusy })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("busy admission ignored cancellation", err)
	}
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	attempts = 0
	_, err = retryCheckpointBusy(cancelled, func() (int, error) { attempts++; return 0, nil })
	if err != context.Canceled || attempts != 0 {
		t.Fatal("cancelled admission started an operation", attempts, err)
	}
}

func TestSearchCheckpointConcurrentListingsAndTwoTabsDoNotFailSourceScans(t *testing.T) {
	isolateTestStorage(t)
	file := checkpointCSV(t, 40000)
	a := checkpointOpen(t, file)
	child, err := a.sessionFor(randomID())
	if err != nil {
		t.Fatal(err)
	}
	if err := child.app.OpenPath(file); err != nil {
		t.Fatal(err)
	}
	store, err := openTaskCheckpoints()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	stop, stopped := make(chan struct{}), make(chan int, 1)
	go func() {
		listings := 0
		defer func() { stopped <- listings }()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_, err := store.List()
			if err != nil && !errors.Is(err, checkpoint.ErrBusy) {
				t.Error("metadata listing failed", err)
				return
			}
			listings++
			time.Sleep(time.Millisecond)
		}
	}()
	defer func() {
		close(stop)
		if listings := <-stopped; listings == 0 {
			t.Error("metadata listing did not overlap the searches")
		}
	}()
	first, _ := checkpointStart(t, a, `A:"needle"`, "advanced", "current")
	second, _ := checkpointStart(t, child.app, `A~"needleABC"`, "advanced", "current")
	checkpointWait(t, first)
	checkpointWait(t, second)
	if first.Status != "done" || second.Status != "done" || len(first.Matches) != 3 || len(second.Matches) != 2 {
		t.Fatal("short directory contention failed a durable scan", first.Status, first.Error, second.Status, second.Error)
	}
}
