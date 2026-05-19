// Package bondserver implements the server side of the bonded VLESS lane:
// a single backend TCP connection multiplexed across N smux streams that all
// share a ConnID. Frame wire-format lives in internal/bond; this package wires
// the backend TCP <-> lanes copy loops and the per-ConnID registry.
package bondserver

import (
	"context"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"github.com/cacggghp/vk-turn-proxy/internal/bond"
	"github.com/xtaci/smux"
)

// laneStream is the subset of *smux.Stream that serverLane needs. Defined as
// an interface so unit tests can inject in-memory pipes without spinning up a
// real smux session.
type laneStream interface {
	io.Reader
	io.Writer
	SetDeadline(time.Time) error
}

// Deps groups host-process dependencies needed by the bond server.
type Deps struct {
	Debug  bool
	Debugf func(format string, v ...any)
}

func (d *Deps) debugf(format string, v ...any) {
	if d.Debugf != nil {
		d.Debugf(format, v...)
	}
}

// Registry deduplicates concurrent lanes that share a ConnID into a single
// backend TCP connection.
type Registry struct {
	deps Deps

	mu    sync.Mutex
	conns map[uint64]*serverConn
}

// NewRegistry creates an empty Registry.
func NewRegistry(deps Deps) *Registry {
	return &Registry{deps: deps, conns: make(map[uint64]*serverConn)}
}

// HandleStreamAfterMagic accepts a smux stream whose first 4 magic bytes have
// already been consumed (server-side multiplex pre-peek), reads the Hello,
// attaches as a lane, and blocks until the underlying bond connection is done.
func (r *Registry) HandleStreamAfterMagic(ctx context.Context, stream *smux.Stream, connectAddr string, magic [4]byte) {
	r.handleStream(ctx, stream, connectAddr, func(rd io.Reader) (bond.Hello, error) {
		return bond.ReadHelloAfterMagic(rd, magic)
	})
}

func (r *Registry) handleStream(ctx context.Context, stream *smux.Stream, connectAddr string, readHello func(io.Reader) (bond.Hello, error)) {
	defer func() {
		if err := stream.Close(); err != nil && err != smux.ErrGoAway {
			log.Printf("failed to close bond smux stream: %v", err)
		}
	}()

	hello, err := readHello(stream)
	if err != nil {
		log.Printf("bond hello error: %v", err)
		return
	}

	conn := r.get(ctx, hello.ConnID, connectAddr)
	conn.addLane(&serverLane{
		index:  hello.LaneIndex,
		stream: stream,
	}, hello.LaneCount)

	select {
	case <-ctx.Done():
	case <-conn.done:
	}
}

func (r *Registry) get(ctx context.Context, id uint64, connectAddr string) *serverConn {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c := r.conns[id]; c != nil {
		return c
	}
	connCtx, cancel := context.WithCancel(ctx)
	c := &serverConn{
		deps:        &r.deps,
		id:          id,
		connectAddr: connectAddr,
		ctx:         connCtx,
		cancel:      cancel,
		done:        make(chan struct{}),
		ready:       make(chan struct{}, 1),
		recvCh:      make(chan bond.Frame, 1024),
	}
	r.conns[id] = c
	go func() {
		<-c.done
		r.mu.Lock()
		if r.conns[id] == c {
			delete(r.conns, id)
		}
		r.mu.Unlock()
	}()
	return c
}

type serverLane struct {
	index  uint16
	stream laneStream
	mu     sync.Mutex
}

type serverConn struct {
	deps        *Deps
	id          uint64
	connectAddr string
	ctx         context.Context
	cancel      context.CancelFunc
	done        chan struct{}

	lanesMu sync.RWMutex
	lanes   []*serverLane
	want    uint16
	ready   chan struct{}

	recvCh chan bond.Frame
	once   sync.Once
}

func (c *serverConn) addLane(l *serverLane, laneCount uint16) {
	c.lanesMu.Lock()
	if laneCount > c.want {
		c.want = laneCount
	}
	c.lanes = append(c.lanes, l)
	count := len(c.lanes)
	c.lanesMu.Unlock()
	c.deps.debugf("[bond %d] lane %d attached (lanes=%d)", c.id, l.index, count)
	select {
	case c.ready <- struct{}{}:
	default:
	}

	go c.readLane(l)
	c.once.Do(func() {
		go c.run()
	})
}

func (c *serverConn) snapshotLanes() []*serverLane {
	c.lanesMu.RLock()
	defer c.lanesMu.RUnlock()
	out := make([]*serverLane, len(c.lanes))
	copy(out, c.lanes)
	return out
}

func (c *serverConn) removeLane(l *serverLane) int {
	c.lanesMu.Lock()
	defer c.lanesMu.Unlock()
	for i, lane := range c.lanes {
		if lane == l {
			c.lanes = append(c.lanes[:i], c.lanes[i+1:]...)
			break
		}
	}
	return len(c.lanes)
}

func (c *serverConn) waitForInitialLanes() {
	timer := time.NewTimer(bond.LaneAttachTimeout)
	defer timer.Stop()
	for {
		c.lanesMu.RLock()
		count := len(c.lanes)
		want := int(c.want)
		c.lanesMu.RUnlock()
		if want <= 0 || count >= want {
			return
		}
		select {
		case <-c.ctx.Done():
			return
		case <-c.ready:
		case <-timer.C:
			c.deps.debugf("[bond %d] starting with %d/%d lanes after attach timeout", c.id, count, want)
			return
		}
	}
}

func (c *serverConn) readLane(l *serverLane) {
	for {
		f, err := bond.ReadFrame(l.stream)
		if err != nil {
			left := c.removeLane(l)
			select {
			case <-c.ctx.Done():
			default:
				if err != io.EOF {
					c.deps.debugf("[bond %d] lane %d read error: %v (lanes=%d)", c.id, l.index, err, left)
				}
				if left == 0 {
					c.cancel()
				}
			}
			return
		}
		select {
		case c.recvCh <- f:
		case <-c.ctx.Done():
			return
		}
	}
}

func (c *serverConn) run() {
	defer close(c.done)
	defer c.cancel()

	c.waitForInitialLanes()

	backendConn, err := net.DialTimeout("tcp", c.connectAddr, 10*time.Second)
	if err != nil {
		log.Printf("[bond %d] backend dial error: %s", c.id, err)
		return
	}
	defer func() {
		if err := backendConn.Close(); err != nil {
			log.Printf("[bond %d] failed to close backend connection: %v", c.id, err)
		}
	}()
	context.AfterFunc(c.ctx, func() {
		now := time.Now()
		if err := backendConn.SetDeadline(now); err != nil {
			log.Printf("[bond %d] backend deadline error: %v", c.id, err)
		}
		for _, lane := range c.snapshotLanes() {
			if err := lane.stream.SetDeadline(now); err != nil {
				log.Printf("[bond %d] lane %d deadline error: %v", c.id, lane.index, err)
			}
		}
	})
	c.deps.debugf("[bond %d] backend connected", c.id)

	var wg sync.WaitGroup
	wg.Go(func() {
		c.copyBondToBackend(backendConn)
	})
	wg.Go(func() {
		defer c.cancel()
		c.copyBackendToBond(backendConn)
	})
	wg.Wait()
}

func (c *serverConn) copyBondToBackend(backendConn net.Conn) {
	pending := make(map[uint64][]byte)
	var expect uint64
	var finSeq *uint64

	for {
		if finSeq != nil && expect == *finSeq {
			bond.CloseWrite(backendConn, c.deps.debugf)
			c.deps.debugf("[bond %d] upload to backend finished chunks=%d", c.id, expect)
			return
		}

		select {
		case <-c.ctx.Done():
			return
		case f := <-c.recvCh:
			switch f.Type {
			case bond.FrameData:
				pending[f.Seq] = f.Data
			case bond.FrameFIN:
				v := f.Seq
				if finSeq == nil || v < *finSeq {
					finSeq = &v
				}
			default:
				log.Printf("[bond %d] unknown frame type %d", c.id, f.Type)
				return
			}

			for {
				data, ok := pending[expect]
				if !ok {
					break
				}
				delete(pending, expect)
				if len(data) > 0 {
					if _, err := backendConn.Write(data); err != nil {
						log.Printf("[bond %d] backend write error: %v", c.id, err)
						return
					}
				}
				expect++
			}
		}
	}
}

func (c *serverConn) copyBackendToBond(backendConn net.Conn) {
	buf := make([]byte, bond.MaxChunk)
	var seq uint64
	var laneIdx uint64
	for {
		n, err := backendConn.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])
			if writeErr := c.writeToNextLane(bond.FrameData, seq, data, &laneIdx); writeErr != nil {
				log.Printf("[bond %d] lane write data error: %v", c.id, writeErr)
				return
			}
			seq++
		}
		if err != nil {
			lanes := c.snapshotLanes()
			for _, lane := range lanes {
				lane.mu.Lock()
				writeErr := bond.WriteFrame(lane.stream, bond.FrameFIN, seq, nil)
				lane.mu.Unlock()
				if writeErr != nil && c.ctx.Err() == nil {
					log.Printf("[bond %d] lane %d write FIN error: %v", c.id, lane.index, writeErr)
				}
			}
			c.deps.debugf("[bond %d] download from backend finished chunks=%d", c.id, seq)
			return
		}
		select {
		case <-c.ctx.Done():
			return
		default:
		}
	}
}

func (c *serverConn) writeToNextLane(typ byte, seq uint64, data []byte, laneIdx *uint64) error {
	for {
		lanes := c.snapshotLanes()
		for range lanes {
			lane := lanes[*laneIdx%uint64(len(lanes))]
			*laneIdx++
			lane.mu.Lock()
			err := bond.WriteFrame(lane.stream, typ, seq, data)
			lane.mu.Unlock()
			if err == nil {
				return nil
			}
			left := c.removeLane(lane)
			log.Printf("[bond %d] lane %d write error: %v (lanes=%d)", c.id, lane.index, err, left)
			if left == 0 {
				return err
			}
		}
		select {
		case <-c.ctx.Done():
			return c.ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}
