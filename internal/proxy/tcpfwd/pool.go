// Package tcpfwd implements VLESS mode: TCP forwarding over a pool of TURN-tunneled
// smux sessions. Each accepted local TCP connection is opened as a smux stream
// (round-robin across sessions) or, with bond, striped across all live sessions.
//
// SessionPool/PooledSession are exported so the bond client (internal/proxy/bondclient)
// can stripe a single TCP connection across multiple live sessions.
package tcpfwd

import (
	"sync"
	"sync/atomic"

	"github.com/xtaci/smux"
)

// PooledSession is a single TURN+DTLS+KCP+smux session inside the pool, with
// its lifetime counters. Fields are exported so internal/proxy/bondclient can
// account per-lane traffic; mutate the atomics via their atomic methods only.
type PooledSession struct {
	ID          int
	Sess        *smux.Session
	Active      atomic.Int32
	Opened      atomic.Uint64
	Closed      atomic.Uint64
	ToSession   atomic.Uint64
	FromSession atomic.Uint64
}

// SessionPool is a concurrency-safe round-robin pool of live smux sessions.
type SessionPool struct {
	mu          sync.RWMutex
	sessions    []*PooledSession
	counter     atomic.Uint64
	connCounter atomic.Uint64

	readyOnce sync.Once
	ready     chan struct{}
}

// Ready returns a channel closed the first time the pool gains a session.
func (p *SessionPool) Ready() <-chan struct{} {
	p.mu.Lock()
	if p.ready == nil {
		p.ready = make(chan struct{})
	}
	ch := p.ready
	p.mu.Unlock()
	return ch
}

// Add registers a freshly connected session in the pool.
func (p *SessionPool) Add(id int, s *smux.Session) *PooledSession {
	ps := &PooledSession{ID: id, Sess: s}
	p.mu.Lock()
	p.sessions = append(p.sessions, ps)
	if p.ready == nil {
		p.ready = make(chan struct{})
	}
	ready := p.ready
	p.mu.Unlock()
	p.readyOnce.Do(func() { close(ready) })
	return ps
}

// Remove drops ps from the pool. No-op if not present.
func (p *SessionPool) Remove(ps *PooledSession) {
	p.mu.Lock()
	for i, sess := range p.sessions {
		if sess == ps {
			p.sessions = append(p.sessions[:i], p.sessions[i+1:]...)
			break
		}
	}
	p.mu.Unlock()
}

// Pick returns the next session in round-robin order, or nil if pool is empty.
func (p *SessionPool) Pick() *PooledSession {
	p.mu.RLock()
	defer p.mu.RUnlock()
	n := len(p.sessions)
	if n == 0 {
		return nil
	}
	idx := (p.counter.Add(1) - 1) % uint64(n)
	return p.sessions[idx]
}

// NextConnID returns a monotonically increasing connection ID.
func (p *SessionPool) NextConnID() uint64 {
	return p.connCounter.Add(1)
}

// Snapshot returns a copy of all currently-live (non-closed) sessions.
func (p *SessionPool) Snapshot() []*PooledSession {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*PooledSession, 0, len(p.sessions))
	for _, ps := range p.sessions {
		if !ps.Sess.IsClosed() {
			out = append(out, ps)
		}
	}
	return out
}

// Count returns the number of sessions currently in the pool (including any
// that may have just closed; use Snapshot for live-only).
func (p *SessionPool) Count() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.sessions)
}
