package bond

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"
)

type connectionKey struct {
	client string
	id     uint64
}

type group struct {
	lanes    []net.Conn
	attached int
	ready    chan struct{}
	done     chan struct{}
	cancel   context.CancelFunc
	err      error
}

// Server общий для всех DTLS-сессий одного listener; Client ID изолирует группы клиентов.
type Server struct {
	mu     sync.Mutex
	groups map[connectionKey]*group
}

func (s *Server) Handle(ctx context.Context, lane net.Conn, client, backend string) error {
	defer func() { _ = lane.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = lane.Close() })
	defer stop()
	if err := lane.SetReadDeadline(time.Now().Add(SetupTimeout)); err != nil {
		return fmt.Errorf("bond hello deadline: %w", err)
	}
	h, err := ReadHello(lane)
	if err != nil {
		return err
	}
	if err = lane.SetReadDeadline(time.Time{}); err != nil {
		return fmt.Errorf("bond hello deadline: %w", err)
	}
	g, err := s.attach(ctx, connectionKey{client: client, id: h.ID}, h, lane, backend)
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		g.cancel()
		<-g.done
	case <-g.done:
	}
	return g.err
}

func (s *Server) attach(ctx context.Context, key connectionKey, h Hello, lane net.Conn, backend string) (*group, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.groups == nil {
		s.groups = make(map[connectionKey]*group)
	}
	g := s.groups[key]
	if g == nil {
		groupCtx, cancel := context.WithCancel(ctx)
		g = &group{lanes: make([]net.Conn, h.Count), ready: make(chan struct{}), done: make(chan struct{}), cancel: cancel}
		s.groups[key] = g
		go s.run(groupCtx, key, g, backend)
	}
	if len(g.lanes) != int(h.Count) || g.lanes[h.Index] != nil {
		return nil, fmt.Errorf("%w: duplicate lane or count mismatch", ErrProtocol)
	}
	g.lanes[h.Index] = lane
	g.attached++
	if g.attached == len(g.lanes) {
		close(g.ready)
	}
	return g, nil
}

func (s *Server) run(ctx context.Context, key connectionKey, g *group, backend string) {
	g.err = serveGroup(ctx, g, backend)
	g.cancel()
	s.mu.Lock()
	delete(s.groups, key)
	lanes := append([]net.Conn(nil), g.lanes...)
	s.mu.Unlock()
	for _, lane := range lanes {
		if lane != nil {
			_ = lane.Close()
		}
	}
	close(g.done)
}

func serveGroup(ctx context.Context, g *group, backend string) error {
	timer := time.NewTimer(SetupTimeout)
	defer timer.Stop()
	select {
	case <-g.ready:
	case <-ctx.Done():
		return fmt.Errorf("bond attach: %w", ctx.Err())
	case <-timer.C:
		return fmt.Errorf("bond attach: %w", context.DeadlineExceeded)
	}
	conn, err := (&net.Dialer{Timeout: SetupTimeout}).DialContext(ctx, "tcp", backend)
	if err != nil {
		return fmt.Errorf("bond backend: %w", err)
	}
	return Copy(ctx, conn, g.lanes, nil)
}
