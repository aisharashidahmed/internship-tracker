package auth

import (
	"sync"
	"time"
)

// Limiter counts failed logins per key (an email, or an IP address) and
// says when a key has failed too often in a time window. It lives in
// memory: a restart resets it, which is fine for one server.
type Limiter struct {
	max    int
	window time.Duration
	now    func() time.Time // tests replace this

	mu       sync.Mutex
	failures map[string][]time.Time
}

// NewLimiter allows max failures per key within window.
func NewLimiter(max int, window time.Duration) *Limiter {
	return &Limiter{max: max, window: window, now: time.Now, failures: map[string][]time.Time{}}
}

// Blocked reports whether key has used up its failures for now.
func (l *Limiter) Blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(key)) >= l.max
}

// Fail records one failed attempt for key.
func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failures[key] = append(l.recent(key), l.now())
}

// Reset forgets key's failures, e.g. after a successful login.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, key)
}

// recent drops failures older than the window. Caller holds l.mu.
func (l *Limiter) recent(key string) []time.Time {
	cutoff := l.now().Add(-l.window)
	kept := l.failures[key][:0]
	for _, t := range l.failures[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(l.failures, key)
		return nil
	}
	l.failures[key] = kept
	return kept
}
