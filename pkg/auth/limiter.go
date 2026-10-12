package auth

import (
	"sync"
	"time"
)

// Limits of the sign-in.
const (
	MaxFailures  = 5
	LockDuration = 5 * time.Minute
	// maxTracked bounds the failures kept in memory, an attacker cannot fill it with addresses.
	maxTracked = 10000
)

type failure struct {
	count int
	until time.Time
}

// Limiter locks an address for LockDuration after MaxFailures wrong passwords in a row.
type Limiter struct {
	now func() time.Time

	mu       sync.Mutex
	failures map[string]*failure
}

// NewLimiter creates a limiter.
func NewLimiter() *Limiter { return &Limiter{now: time.Now, failures: map[string]*failure{}} }

// Locked reports whether the address is locked.
func (l *Limiter) Locked(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	f := l.failures[ip]
	return f != nil && f.until.After(l.now())
}

// Fail counts a wrong password.
func (l *Limiter) Fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.failures) >= maxTracked {
		for k, f := range l.failures {
			if !f.until.After(now) {
				delete(l.failures, k)
			}
		}
		if len(l.failures) >= maxTracked {
			return // full of live locks, one more address is not worth the memory
		}
	}
	f := l.failures[ip]
	if f == nil || (!f.until.IsZero() && !f.until.After(now)) {
		f = &failure{}
		l.failures[ip] = f
	}
	f.count++
	if f.count >= MaxFailures {
		f.count, f.until = 0, now.Add(LockDuration)
	}
}

// Clear forgets the failures of an address after a right password.
func (l *Limiter) Clear(ip string) {
	l.mu.Lock()
	delete(l.failures, ip)
	l.mu.Unlock()
}
