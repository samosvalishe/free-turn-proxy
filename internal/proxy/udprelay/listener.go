package udprelay

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
)

const inboundQueueCap = 2000

// Packet is a pooled UDP datagram carried from the listener to the per-stream
// DTLS worker. N is the populated prefix of Data.
type Packet struct {
	Data []byte
	N    int
}

// packetPool reuses Packet buffers across the inbound hot path. Buffer size
// matches the 2048-byte default the listener loop expects.
var packetPool = sync.Pool{
	New: func() any { return &Packet{Data: make([]byte, 2048)} },
}

// runListener reads packets from listenConn, refreshes the active-peer cache,
// and posts each packet to inboundChan. Packets are dropped when the channel
// is full to keep the read loop wait-free.
func runListener(ctx context.Context, listenConn net.PacketConn, activeLocalPeer *atomic.Value, inboundChan chan<- *Packet) {
	// Pointer-cache for the last seen local peer addr. Avoids the
	// per-packet addr.String() allocation pair on the hot WG ingest path:
	// most packets come from the same UDPAddr instance, so a pointer
	// equality check covers the fast path. The slow path (new instance
	// from ReadFrom for the same ip:port) does one String compare and
	// then refreshes the cache.
	var lastAddr net.Addr
	var lastAddrStr string
	for {
		if ctx.Err() != nil {
			return
		}
		pktIface := packetPool.Get()
		pkt := pktIface.(*Packet) //nolint:errcheck // pool New always returns *Packet
		nRead, addr, err := listenConn.ReadFrom(pkt.Data)
		if err != nil {
			return
		}

		if addr != lastAddr {
			s := addr.String()
			if s != lastAddrStr {
				activeLocalPeer.Store(addr)
				lastAddrStr = s
			}
			lastAddr = addr
		}

		pkt.N = nRead

		select {
		case inboundChan <- pkt:
		default:
			packetPool.Put(pkt)
		}
	}
}
