package netconn

import (
	"errors"
	"net"
	"os"
	"sync"
	"time"
)

// ErrPacketTooLarge возвращается, если размер датаграммы превышает MTU пайпа.
var ErrPacketTooLarge = errors.New("netconn: packet exceeds pipe MTU")

const (
	defaultPipeMTU   = 2048
	defaultPipeQueue = 256
)

type pipeAddr string

func (pipeAddr) Network() string  { return "pipe" }
func (a pipeAddr) String() string { return string(a) }

// PacketPipe возвращает пару связанных net.PacketConn в памяти.
func PacketPipe(mtu, queue int) (net.PacketConn, net.PacketConn) {
	if mtu <= 0 {
		mtu = defaultPipeMTU
	}
	if queue <= 0 {
		queue = defaultPipeQueue
	}

	// sync.Pool хранит *[]byte для избежания аллокаций при передаче в канал.
	pool := &sync.Pool{New: func() any { b := make([]byte, mtu); return &b }}
	toA := make(chan *[]byte, queue)
	toB := make(chan *[]byte, queue)

	a := &packetPipe{local: pipeAddr("pipe:a"), remote: pipeAddr("pipe:b"), rx: toA, tx: toB, mtu: mtu, pool: pool, done: make(chan struct{})}
	b := &packetPipe{local: pipeAddr("pipe:b"), remote: pipeAddr("pipe:a"), rx: toB, tx: toA, mtu: mtu, pool: pool, done: make(chan struct{})}
	a.peer, b.peer = b, a
	return a, b
}

type packetPipe struct {
	local  net.Addr
	remote net.Addr
	rx     <-chan *[]byte
	tx     chan<- *[]byte
	peer   *packetPipe
	mtu    int
	pool   *sync.Pool

	// deadline хранит дедлайн и канал оповещения для немедленного пробуждения читателя.
	deadline struct {
		mu      sync.Mutex
		at      time.Time
		changed chan struct{}
	}

	closeOnce sync.Once
	done      chan struct{}
}

func (p *packetPipe) ReadFrom(b []byte) (int, net.Addr, error) {
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	for {
		at, changed := p.readDeadline()

		if timer != nil {
			timer.Stop()
			timer = nil
		}
		var timeout <-chan time.Time
		if !at.IsZero() {
			timer = time.NewTimer(time.Until(at))
			timeout = timer.C
		}

		select {
		case <-p.done:
			return 0, nil, net.ErrClosed
		case <-p.peer.done:
			if n, ok := p.drain(b); ok {
				return n, p.remote, nil
			}
			return 0, nil, net.ErrClosed
		case <-changed:
			continue
		case <-timeout:
			return 0, nil, os.ErrDeadlineExceeded
		case buf, ok := <-p.rx:
			if !ok {
				return 0, nil, net.ErrClosed
			}
			n := copy(b, *buf)
			p.pool.Put(buf)
			return n, p.remote, nil
		}
	}
}

func (p *packetPipe) drain(b []byte) (int, bool) {
	select {
	case buf, ok := <-p.rx:
		if !ok {
			return 0, false
		}
		n := copy(b, *buf)
		p.pool.Put(buf)
		return n, true
	default:
		return 0, false
	}
}

func (p *packetPipe) readDeadline() (time.Time, <-chan struct{}) {
	p.deadline.mu.Lock()
	defer p.deadline.mu.Unlock()
	if p.deadline.changed == nil {
		p.deadline.changed = make(chan struct{})
	}
	return p.deadline.at, p.deadline.changed
}

func (p *packetPipe) WriteTo(b []byte, _ net.Addr) (int, error) {
	if len(b) > p.mtu {
		return 0, ErrPacketTooLarge
	}

	select {
	case <-p.done:
		return 0, net.ErrClosed
	case <-p.peer.done:
		return 0, net.ErrClosed
	default:
	}

	buf, _ := p.pool.Get().(*[]byte)
	*buf = (*buf)[:len(b)]
	copy(*buf, b)

	select {
	case p.tx <- buf:
	default:
		p.pool.Put(buf)
	}
	return len(b), nil
}

func (p *packetPipe) Close() error {
	p.closeOnce.Do(func() { close(p.done) })
	return nil
}

func (p *packetPipe) LocalAddr() net.Addr { return p.local }

func (p *packetPipe) SetDeadline(t time.Time) error {
	return p.SetReadDeadline(t)
}

func (p *packetPipe) SetReadDeadline(t time.Time) error {
	p.deadline.mu.Lock()
	defer p.deadline.mu.Unlock()
	p.deadline.at = t
	if p.deadline.changed != nil {
		close(p.deadline.changed)
	}
	p.deadline.changed = make(chan struct{})
	return nil
}

// SetWriteDeadline - no-op: неблокирующая запись дропает пакет при переполнении очереди.
func (*packetPipe) SetWriteDeadline(time.Time) error { return nil }

// datagramConn подаёт packetPipe как net.Conn: границы датаграмм сохраняются, поэтому
// поверх такой пары можно поднимать KCP без реального сокета.
type datagramConn struct {
	net.PacketConn
	remote net.Addr
}

func (c *datagramConn) Read(b []byte) (int, error) {
	n, _, err := c.ReadFrom(b)
	return n, err
}

func (c *datagramConn) Write(b []byte) (int, error) { return c.WriteTo(b, c.remote) }
func (c *datagramConn) RemoteAddr() net.Addr        { return c.remote }

// DatagramPipe - пара связанных net.Conn в памяти с датаграммной семантикой.
func DatagramPipe(mtu, queue int) (net.Conn, net.Conn) {
	a, b := PacketPipe(mtu, queue)
	return &datagramConn{PacketConn: a, remote: b.LocalAddr()}, &datagramConn{PacketConn: b, remote: a.LocalAddr()}
}
