package tcprelay

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"net"
	"time"

	"github.com/samosvalishe/free-turn-proxy/internal/logx"
	"github.com/samosvalishe/free-turn-proxy/internal/proxy/bond"
	"github.com/samosvalishe/free-turn-proxy/internal/stats"
)

func proxyBond(ctx context.Context, log logx.Logger, conn net.Conn, pool *sessionPool) {
	defer func() { _ = conn.Close() }()
	lanes, traffic := openBondLanes(pool)
	defer func() {
		for _, lane := range lanes {
			_ = lane.Close()
		}
	}()
	if len(lanes) == 0 {
		return
	}
	stop := context.AfterFunc(ctx, func() {
		for _, lane := range lanes {
			_ = lane.Close()
		}
	})
	defer stop()
	var id [8]byte
	_, _ = rand.Read(id[:])
	deadline := time.Now().Add(bond.SetupTimeout)
	for i, lane := range lanes {
		h := bond.Hello{ID: binary.BigEndian.Uint64(id[:]), Index: uint16(i), Count: uint16(len(lanes))} //nolint:gosec // len(lanes) <= MaxLanes
		if err := helloBondLane(lane, h, deadline); err != nil {
			log.Debugf("TCP bond setup: %v", err)
			return
		}
	}
	log.Debugf("TCP bond connected: lanes=%d", len(lanes))
	if err := bond.Copy(ctx, conn, lanes, traffic); err != nil && ctx.Err() == nil {
		log.Debugf("TCP bond closed: %v", err)
	}
}

func helloBondLane(lane net.Conn, h bond.Hello, deadline time.Time) error {
	if err := lane.SetWriteDeadline(deadline); err != nil {
		return err
	}
	if err := bond.WriteHello(lane, h); err != nil {
		return err
	}
	return lane.SetWriteDeadline(time.Time{})
}

func openBondLanes(pool *sessionPool) ([]net.Conn, *stats.Stats) {
	pool.mu.RLock()
	candidates := append([]*pooledSession(nil), pool.sessions...)
	pool.mu.RUnlock()
	lanes := make([]net.Conn, 0, len(candidates))
	var traffic *stats.Stats
	for _, ps := range candidates {
		if len(lanes) == bond.MaxLanes {
			break
		}
		if ps.sess.IsClosed() {
			continue
		}
		stream, err := ps.sess.OpenStream()
		if err != nil {
			continue
		}
		lanes = append(lanes, stream)
		traffic = ps.traffic
	}
	return lanes, traffic
}
