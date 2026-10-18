package auth

import (
	"crypto/sha256"
	"strings"
	"sync"
	"time"
)

type loginKey struct{ ip, account [32]byte }
type loginAttempt struct {
	failures int
	until    time.Time
	touched  time.Time
}

// LoginLimiter bounds memory and expires idle entries after an hour.
type LoginLimiter struct {
	mu      sync.Mutex
	entries map[loginKey]loginAttempt
	cleaned time.Time
}

func NewLoginLimiter() *LoginLimiter {
	return &LoginLimiter{entries: make(map[loginKey]loginAttempt)}
}

func accountKey(ip, account string) loginKey {
	return loginKey{sha256.Sum256([]byte(ip)), sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(account))))}
}

func (l *LoginLimiter) prune(now time.Time) {
	if now.Sub(l.cleaned) < time.Minute {
		return
	}
	for key, entry := range l.entries {
		if now.Sub(entry.touched) >= time.Hour {
			delete(l.entries, key)
		}
	}
	l.cleaned = now
}

func (l *LoginLimiter) Allow(ip, account string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(now)
	entry, exists := l.entries[accountKey(ip, account)]
	return (!exists && len(l.entries) < 10000) || (exists && !now.Before(entry.until))
}

func (l *LoginLimiter) Failure(ip, account string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(now)
	key := accountKey(ip, account)
	entry, exists := l.entries[key]
	if !exists && len(l.entries) >= 10000 {
		return
	}
	if entry.failures < 11 {
		entry.failures++
	}
	delay := time.Second << (entry.failures - 1)
	if delay > 15*time.Minute {
		delay = 15 * time.Minute
	}
	entry.until, entry.touched = now.Add(delay), now
	l.entries[key] = entry
}

func (l *LoginLimiter) Success(ip, account string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, accountKey(ip, account))
}
