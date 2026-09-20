package main

import (
	"net"
	"sync"
	"time"
)

type authLimiter struct {
	mu             sync.Mutex
	maxKeys, limit int
	window         time.Duration
	hits           map[string][]time.Time
}

func newAuthLimiter(maxKeys, limit int, window time.Duration) *authLimiter {
	return &authLimiter{maxKeys: maxKeys, limit: limit, window: window, hits: make(map[string][]time.Time)}
}
func limiterKey(remote string) string {
	host, _, err := net.SplitHostPort(remote)
	if err == nil {
		return host
	}
	return remote
}
func (l *authLimiter) allow(remote string, now time.Time) bool {
	key := limiterKey(remote)
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, hits := range l.hits {
		if len(hits) == 0 || now.Sub(hits[len(hits)-1]) >= l.window {
			delete(l.hits, k)
		}
	}
	hits := l.hits[key]
	keep := hits[:0]
	for _, at := range hits {
		if now.Sub(at) < l.window {
			keep = append(keep, at)
		}
	}
	if len(keep) >= l.limit {
		l.hits[key] = keep
		return false
	}
	if _, ok := l.hits[key]; !ok && len(l.hits) >= l.maxKeys {
		var oldest string
		var oldestAt time.Time
		for k, values := range l.hits {
			if len(values) > 0 && (oldestAt.IsZero() || values[len(values)-1].Before(oldestAt)) {
				oldest, oldestAt = k, values[len(values)-1]
			}
		}
		if oldest != "" {
			delete(l.hits, oldest)
		}
	}
	l.hits[key] = append(keep, now)
	return true
}
func (l *authLimiter) size() int { l.mu.Lock(); defer l.mu.Unlock(); return len(l.hits) }
