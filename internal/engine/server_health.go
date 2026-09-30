package engine

import (
	"sync"
	"time"
)

// Server health tracking: never delete high-Files servers on a transient
// handshake timeout — demote + exponential backoff instead (Grok / eMule ops).

type serverHealthState int

const (
	healthUnknown serverHealthState = iota
	healthOK
	healthHandshakeTimeout
	healthTCPFail
)

type serverHealth struct {
	state            serverHealthState
	consecutiveFail  int
	lastSuccess      time.Time
	lastFail         time.Time
	nextRetry        time.Time
	handshakeOKTotal int
}

type serverHealthMap struct {
	mu   sync.Mutex
	by   map[string]*serverHealth
}

func newServerHealthMap() *serverHealthMap {
	return &serverHealthMap{by: make(map[string]*serverHealth)}
}

func (m *serverHealthMap) getLocked(addr string) *serverHealth {
	h, ok := m.by[addr]
	if !ok {
		h = &serverHealth{state: healthUnknown}
		m.by[addr] = h
	}
	return h
}

func (m *serverHealthMap) MarkOK(addr string) {
	if m == nil || addr == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	h := m.getLocked(addr)
	h.state = healthOK
	h.consecutiveFail = 0
	h.lastSuccess = time.Now()
	h.nextRetry = time.Time{}
	h.handshakeOKTotal++
}

func (m *serverHealthMap) MarkHandshakeTimeout(addr string) {
	if m == nil || addr == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	h := m.getLocked(addr)
	h.state = healthHandshakeTimeout
	h.consecutiveFail++
	h.lastFail = time.Now()
	// backoff: min(30m * 2^(fail-1), 6h)
	shift := h.consecutiveFail - 1
	if shift < 0 {
		shift = 0
	}
	if shift > 4 {
		shift = 4 // 30m * 16 = 8h capped below
	}
	backoff := 30 * time.Minute << uint(shift)
	if backoff > 6*time.Hour {
		backoff = 6 * time.Hour
	}
	h.nextRetry = time.Now().Add(backoff)
}

func (m *serverHealthMap) MarkTCPFail(addr string) {
	if m == nil || addr == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	h := m.getLocked(addr)
	h.state = healthTCPFail
	h.consecutiveFail++
	h.lastFail = time.Now()
	backoff := 15 * time.Minute << uint(minInt(h.consecutiveFail-1, 3))
	if backoff > 3*time.Hour {
		backoff = 3 * time.Hour
	}
	h.nextRetry = time.Now().Add(backoff)
}

// AllowConnect reports whether addr may be dialed in the primary wave.
// During backoff the server is demoted (skipped here) but never deleted;
// callers can still force-retry when the pool is empty. Hard quarantine only
// after ≥10 consecutive failures with no success in 24h.
func (m *serverHealthMap) AllowConnect(addr string) bool {
	if m == nil || addr == "" {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.by[addr]
	if !ok {
		return true
	}
	now := time.Now()
	if h.state == healthOK {
		return true
	}
	if !h.nextRetry.IsZero() && now.Before(h.nextRetry) {
		// Soft demote during backoff.
		return false
	}
	if h.consecutiveFail >= 10 {
		if h.lastSuccess.IsZero() || now.Sub(h.lastSuccess) > 24*time.Hour {
			// Still allow once nextRetry has elapsed (periodic resurrection).
			return h.nextRetry.IsZero() || !now.Before(h.nextRetry)
		}
	}
	return true
}

// SortKey returns a lower number for healthier / more reliable servers.
// Used as a tie-breaker after Files ranking.
func (m *serverHealthMap) SortKey(addr string) int {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.by[addr]
	if !ok {
		return 1 // unknown — slightly behind proven OK
	}
	switch h.state {
	case healthOK:
		return 0
	case healthUnknown:
		return 1
	case healthHandshakeTimeout:
		return 3 + h.consecutiveFail
	case healthTCPFail:
		return 5 + h.consecutiveFail
	default:
		return 2
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
