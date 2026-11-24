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
	mu      sync.Mutex
	running int
	until   time.Time
}

func (w *Work) Begin() func() {
	w.mu.Lock()
	w.running++
	w.mu.Unlock()
	return func() { w.mu.Lock(); w.running--; w.until = time.Now().Add(ResultWindow); w.mu.Unlock() }
}
func (w *Work) Retained(now time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.running > 0 || now.Before(w.until)
}
