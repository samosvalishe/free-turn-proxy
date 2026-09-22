package udprelay

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
)

const inboundQueueCap = 2000

var ErrLocalRead = errors.New("udprelay: local read failed") //nolint:gochecknoglobals // sentinel для errors.Is

// Packet представляет буферизованную датаграмму для передачи воркерам.
type Packet struct {
	Data []byte
	N    int
}

// packetPool переиспользует буферы датаграмм.
var packetPool = sync.Pool{
	New: func() any { return &Packet{Data: make([]byte, maxDatagramLen)} },
}

func runListener(ctx context.Context, listenConn net.PacketConn, activeLocalPeer *atomic.Value, inboundChan chan<- *Packet) error {
	var lastPort netip.AddrPort
	var lastAddrStr string
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		pktIface := packetPool.Get()
		pkt := pktIface.(*Packet) //nolint:errcheck // pool New always returns *Packet
		nRead, addr, err := listenConn.ReadFrom(pkt.Data)
		if err != nil {
			packetPool.Put(pkt)
			return fmt.Errorf("%w: %w", ErrLocalRead, err)
		}

		if ua, ok := addr.(*net.UDPAddr); ok {
			if ap := ua.AddrPort(); ap != lastPort {
				activeLocalPeer.Store(addr)
				lastPort = ap
			}
		} else if s := addr.String(); s != lastAddrStr {
			activeLocalPeer.Store(addr)
			lastAddrStr = s
		}

		pkt.N = nRead

		select {
		case inboundChan <- pkt:
		default:
			packetPool.Put(pkt)
		}
	}
}
