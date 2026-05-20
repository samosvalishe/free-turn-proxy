// Package bondclient implements the client side of the bonded VLESS lane:
// a single accepted TCP connection striped (round-robin) across all currently
// live smux sessions in a tcpfwd.SessionPool. Frame wire-format lives in
// internal/wire/bondframe; this package wires the local TCP <-> lanes copy loops.
package bondclient

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/samosvalishe/btp/internal/logx"
	"github.com/samosvalishe/btp/internal/proxy/tcpfwd"
	"github.com/samosvalishe/btp/internal/stats"
	"github.com/samosvalishe/btp/internal/wire/bondframe"
	"github.com/xtaci/smux"
)

// Deps groups host-process dependencies needed by the bond client.
type Deps struct {
	Log logx.Logger
}

func (d *Deps) log() logx.Logger {
	if d.Log == nil {
		return logx.Nop()
	}
	return d.Log
}

// Handler binds Deps and exposes Handle, matching the tcpfwd.BondHandler signature.
type Handler struct {
	Deps Deps
}

// lane is one striped smux stream within a bonded TCP connection.
type lane struct {
	ps     *tcpfwd.PooledSession
	stream *smux.Stream
	mu     sync.Mutex
	dead   atomic.Bool
}

// Handle stripes the local TCP connection across all live candidate sessions.
// Signature matches tcpfwd.BondHandler.
func (h *Handler) Handle(ctx context.Context, tcpConn net.Conn, connID uint64, candidates []*tcpfwd.PooledSession) {
	defer func() { _ = tcpConn.Close() }()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Phase 1: open streams on usable sessions. Phase 2: send Hello with the
	// actual lane count so bondserver.waitForInitialLanes doesn't wait on lanes
	// that were never opened.
	type pending struct {
		ps     *tcpfwd.PooledSession
		stream *smux.Stream
	}
	opened := make([]pending, 0, len(candidates))
	for _, ps := range candidates {
		if ps.Sess.IsClosed() {
			continue
		}
		stream, err := ps.Sess.OpenStream()
		if err != nil {
			h.Deps.log().Errorf("[bond %d] session %d open stream error: %s", connID, ps.ID, err)
			continue
		}
		opened = append(opened, pending{ps: ps, stream: stream})
	}

	lanes := make([]*lane, 0, len(opened))
	laneIDs := make([]string, 0, len(opened))
	laneCount := uint16(len(opened))
	for i, p := range opened {
		if err := bondframe.WriteHello(p.stream, connID, uint16(i), laneCount); err != nil {
			h.Deps.log().Errorf("[bond %d] session %d hello error: %s", connID, p.ps.ID, err)
			_ = p.stream.Close()
			continue
		}
		p.ps.Opened.Add(1)
		p.ps.Active.Add(1)
		lanes = append(lanes, &lane{ps: p.ps, stream: p.stream})
		laneIDs = append(laneIDs, strconv.Itoa(p.ps.ID))
	}

	if len(lanes) == 0 {
		h.Deps.log().Errorf("[bond %d] no usable lanes, rejecting TCP from %s", connID, tcpConn.RemoteAddr())
		return
	}
	context.AfterFunc(ctx, func() {
		now := time.Now()
		if err := tcpConn.SetDeadline(now); err != nil {
			h.Deps.log().Debugf("[bond %d] local TCP deadline error: %v", connID, err)
		}
		for _, l := range lanes {
			if err := l.stream.SetDeadline(now); err != nil {
				h.Deps.log().Debugf("[bond %d] session %d stream deadline error: %v", connID, l.ps.ID, err)
			}
		}
	})

	h.Deps.log().Debugf("[bond %d] TCP accept from=%s lanes=%d [%s]", connID, tcpConn.RemoteAddr(), len(lanes), strings.Join(laneIDs, ","))
	defer func() {
		for _, l := range lanes {
			_ = l.stream.Close()
			active := l.ps.Active.Add(-1)
			closed := l.ps.Closed.Add(1)
			h.Deps.log().Debugf("[bond %d] lane session %d close active=%d closed=%d totals: to-session=%s from-session=%s",
				connID, l.ps.ID, active, closed,
				stats.FormatByteCount(l.ps.ToSession.Load()), stats.FormatByteCount(l.ps.FromSession.Load()))
		}
	}()

	recvCh := make(chan bondframe.Frame, 1024)
	var readWG sync.WaitGroup
	for _, l := range lanes {
		readWG.Go(func() {
			for {
				f, err := bondframe.ReadFrame(l.stream)
				if err != nil {
					l.dead.Store(true)
					select {
					case <-ctx.Done():
					default:
						if err != io.EOF {
							h.Deps.log().Debugf("[bond %d] session %d read frame error: %v", connID, l.ps.ID, err)
						}
					}
					return
				}
				if f.Type == bondframe.FrameData {
					l.ps.FromSession.Add(uint64(len(f.Data)))
				}
				select {
				case recvCh <- f:
				case <-ctx.Done():
					return
				}
			}
		})
	}
	go func() {
		readWG.Wait()
		close(recvCh)
	}()

	var wg sync.WaitGroup
	wg.Go(func() {
		h.copyTCPToBond(ctx, connID, tcpConn, lanes)
	})
	wg.Go(func() {
		h.copyBondToTCP(ctx, connID, tcpConn, recvCh)
		cancel()
	})
	wg.Wait()
}

func (h *Handler) copyTCPToBond(ctx context.Context, connID uint64, tcpConn net.Conn, lanes []*lane) {
	buf := make([]byte, bondframe.MaxChunk)
	var seq uint64
	var laneIdx uint64
	for {
		n, err := tcpConn.Read(buf)
		if n > 0 {
			l, writeErr := writeBondFrameToNextLane(ctx, lanes, bondframe.FrameData, seq, buf[:n], &laneIdx)
			if writeErr != nil {
				h.Deps.log().Errorf("[bond %d] write data error: %v", connID, writeErr)
				return
			}
			l.ps.ToSession.Add(uint64(n))
			seq++
		}
		if err != nil {
			if err != io.EOF {
				h.Deps.log().Debugf("[bond %d] local TCP read finished with error: %v", connID, err)
			}
			for _, l := range lanes {
				if l.dead.Load() {
					continue
				}
				l.mu.Lock()
				writeErr := bondframe.WriteFrame(l.stream, bondframe.FrameFIN, seq, nil)
				l.mu.Unlock()
				if writeErr != nil && ctx.Err() == nil {
					h.Deps.log().Errorf("[bond %d] session %d write FIN error: %v", connID, l.ps.ID, writeErr)
				}
			}
			h.Deps.log().Debugf("[bond %d] upload finished chunks=%d", connID, seq)
			return
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
	}
}

// writeBondFrameToNextLane writes to the next live lane in round-robin order.
// Unlike bondserver.writeToNextLane (which loops waiting for new lanes to
// attach), the client's lane set is fixed for the lifetime of Handle, so once
// all lanes are dead there is nothing to wait for — fail fast.
func writeBondFrameToNextLane(ctx context.Context, lanes []*lane, typ byte, seq uint64, data []byte, laneIdx *uint64) (*lane, error) {
	for range lanes {
		idx := *laneIdx % uint64(len(lanes))
		*laneIdx++
		l := lanes[idx]
		if l.dead.Load() {
			continue
		}
		l.mu.Lock()
		err := bondframe.WriteFrame(l.stream, typ, seq, data)
		l.mu.Unlock()
		if err == nil {
			return l, nil
		}
		l.dead.Store(true)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, fmt.Errorf("no live bond lanes")
}

func (h *Handler) copyBondToTCP(ctx context.Context, connID uint64, tcpConn net.Conn, recvCh <-chan bondframe.Frame) {
	chunks := bondframe.Reorder(ctx, tcpConn, recvCh, bondframe.ReorderHooks{
		OnOverflow:    func(have int) { h.Deps.log().Errorf("[bond %d] pending map overflow (>%d), closing", connID, bondframe.PendingCap) },
		OnUnknownType: func(typ byte) { h.Deps.log().Errorf("[bond %d] unknown frame type %d", connID, typ) },
		OnWriteError:  func(err error) { h.Deps.log().Errorf("[bond %d] local TCP write error: %v", connID, err) },
		OnCloseWrite:  h.Deps.log().Debugf,
	})
	h.Deps.log().Debugf("[bond %d] download finished chunks=%d", connID, chunks)
}
