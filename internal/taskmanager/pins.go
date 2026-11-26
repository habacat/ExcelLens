package taskmanager

import "sync"

// PinSet tracks reference-counted active pins on resources (such as open
// workbooks or uploaded temporary files) so background cleanup never deletes
// or closes resources while any task or session holds a pin.
type PinSet[K comparable] struct {
	mu   sync.Mutex
	pins map[K]int
}

// NewPinSet creates an empty reference-counted pin set.
func NewPinSet[K comparable]() *PinSet[K] {
	return &PinSet[K]{pins: make(map[K]int)}
}

// Pin increments the pin count for every non-zero key (or every passed key if
// skipZero is false) and returns an idempotent unpin function.
func (p *PinSet[K]) Pin(keys ...K) func() {
	var zero K
	p.mu.Lock()
	if p.pins == nil {
		p.pins = make(map[K]int)
	}
	for _, k := range keys {
		if k != zero {
			p.pins[k]++
		}
	}
	p.mu.Unlock()
	return sync.OnceFunc(func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		for _, k := range keys {
			if k == zero {
				continue
			}
			p.pins[k]--
			if p.pins[k] <= 0 {
				delete(p.pins, k)
			}
		}
	})
}

// Snapshot returns all currently pinned keys.
func (p *PinSet[K]) Snapshot() []K {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]K, 0, len(p.pins))
	for k := range p.pins {
		out = append(out, k)
	}
	return out
}

// Any reports whether any currently pinned key satisfies pred.
func (p *PinSet[K]) Any(pred func(K) bool) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k := range p.pins {
		if pred(k) {
			return true
		}
	}
	return false
}

// Count returns the active reference count for key.
func (p *PinSet[K]) Count(key K) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pins[key]
}

// Len returns the number of distinct pinned keys.
func (p *PinSet[K]) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.pins)
}

// Total returns the sum of active pin counts across all keys.
func (p *PinSet[K]) Total() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	total := 0
	for _, n := range p.pins {
		total += n
	}
	return total
}
