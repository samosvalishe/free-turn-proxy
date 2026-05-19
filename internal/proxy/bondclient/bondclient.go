// Package bondclient implements the client side of the bonded VLESS lane:
// a single accepted TCP connection striped (round-robin) across all currently
// live smux sessions in a tcpfwd.SessionPool. Frame wire-format lives in
// internal/bond; this package wires the local TCP <-> lanes copy loops.
package bondclient

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cacggghp/vk-turn-proxy/internal/logx"
	"github.com/cacggghp/vk-turn-proxy/internal/proxy/tcpfwd"
	"github.com/cacggghp/vk-turn-proxy/internal/stats"
	"github.com/cacggghp/vk-turn-proxy/internal/wire/bondframe"
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

	lanes := make([]*lane, 0, len(candidates))
	laneIDs := make([]string, 0, len(candidates))
	for i, ps := range candidates {
		if ps.Sess.IsClosed() {
			continue
		}
		stream, err := ps.Sess.OpenStream()
		if err != nil {
			log.Printf("[bond %d] session %d open stream error: %s", connID, ps.ID, err)
			continue
		}
		if err := bondframe.WriteHello(stream, connID, uint16(i), uint16(len(candidates))); err != nil {
			log.Printf("[bond %d] session %d hello error: %s", connID, ps.ID, err)
			_ = stream.Close()
			continue
		}
		ps.Opened.Add(1)
		ps.Active.Add(1)
		lanes = append(lanes, &lane{ps: ps, stream: stream})
		laneIDs = append(laneIDs, strconv.Itoa(ps.ID))
	}

	if len(lanes) == 0 {
		log.Printf("[bond %d] no usable lanes, rejecting TCP from %s", connID, tcpConn.RemoteAddr())
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
				log.Printf("[bond %d] write data error: %v", connID, writeErr)
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
					log.Printf("[bond %d] session %d write FIN error: %v", connID, l.ps.ID, writeErr)
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
	pending := make(map[uint64][]byte)
	var expect uint64
	var finSeq *uint64

	for {
		if finSeq != nil && expect == *finSeq {
			bondframe.CloseWrite(tcpConn, h.Deps.log().Debugf)
			h.Deps.log().Debugf("[bond %d] download finished chunks=%d", connID, expect)
			return
		}

		select {
		case <-ctx.Done():
			return
		case f, ok := <-recvCh:
			if !ok {
				return
			}
			switch f.Type {
			case bondframe.FrameData:
				if len(pending) >= 1024 {
					log.Printf("[bond %d] pending map overflow (>1024), closing", connID)
					return
				}
				pending[f.Seq] = f.Data
			case bondframe.FrameFIN:
				v := f.Seq
				if finSeq == nil || v < *finSeq {
					finSeq = &v
				}
			default:
				log.Printf("[bond %d] unknown frame type %d", connID, f.Type)
				return
			}

			for {
				data, ok := pending[expect]
				if !ok {
					break
				}
				delete(pending, expect)
				if len(data) > 0 {
					if _, err := tcpConn.Write(data); err != nil {
						log.Printf("[bond %d] local TCP write error: %v", connID, err)
						return
					}
				}
				expect++
			}
		}
	}
}
