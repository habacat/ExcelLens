// Package browserlease tracks the document using a tab and its reconnect window.
// The session registry serializes calls and owns the associated resources.
package browserlease

import "time"

const (
	ReconnectGrace = 45 * time.Second
	SilenceLimit   = 10 * time.Minute
)

type Lease struct {
	page       string
	generation uint64
	deadline   time.Time
	requests   int
	detached   bool
}

func (l *Lease) Expired(now time.Time) bool {
	return l.requests == 0 && !now.Before(l.deadline)
}

// Reloads keep the same session but acquire a new document generation. A
// delayed attach from a previous document cannot replace its successor.
func (l *Lease) Attach(page string, generation uint64, now time.Time) bool {
	if generation < l.generation || generation == l.generation && (page != l.page || l.detached) {
		return false
	}
	l.page, l.generation, l.detached = page, generation, false
	l.deadline = now.Add(SilenceLimit)
	return true
}

func (l *Lease) Owns(page string) bool { return page == l.page && !l.detached }

func (l *Lease) Detach(page string, now time.Time) {
	if l.Owns(page) && !l.Expired(now) {
		l.detached = true
		l.deadline = now.Add(ReconnectGrace)
	}
}

func (l *Lease) Begin(now time.Time) {
	l.requests++
	if !l.detached {
		l.deadline = now.Add(SilenceLimit)
	}
}

func (l *Lease) End(now time.Time) {
	l.requests--
	if !l.detached {
		l.deadline = now.Add(SilenceLimit)
	}
}
