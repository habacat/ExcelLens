package browserlease

import (
	"sync"
	"time"
)

// Work keeps explicitly submitted jobs and their completed in-memory results
// available independently of the browser document. Page requests and heartbeat
// traffic do not extend this delivery window.
const ResultWindow = 15 * time.Minute

type Work struct {
	mu   sync.Mutex
	jobs map[string]*workItem
}
type workItem struct {
	done  <-chan struct{}
	until time.Time
}

func (w *Work) Track(id string, done <-chan struct{}) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.jobs == nil {
		w.jobs = make(map[string]*workItem)
	}
	w.stateLocked(time.Now())
	item := &workItem{done: done}
	w.jobs[id] = item
	go func() {
		<-done
		w.mu.Lock()
		if item.until.IsZero() {
			item.until = time.Now().Add(ResultWindow)
		}
		w.mu.Unlock()
	}()
}

// The closed channel, not the scheduling of the completion observer, decides
// whether a worker still owns resources at the instant of a restore.
func (w *Work) stateLocked(now time.Time) (running, retained bool) {
	for id, item := range w.jobs {
		select {
		case <-item.done:
			if item.until.IsZero() {
				item.until = now.Add(ResultWindow)
			}
			if !now.Before(item.until) {
				delete(w.jobs, id)
				continue
			}
		default:
			running = true
		}
		retained = true
	}
	return
}
func (w *Work) State(now time.Time) (running, retained bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stateLocked(now)
}
func (w *Work) Forget(id string) { w.mu.Lock(); delete(w.jobs, id); w.mu.Unlock() }
