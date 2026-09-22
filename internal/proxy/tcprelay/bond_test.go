package tcprelay

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/samosvalishe/free-turn-proxy/internal/logx"
	"github.com/samosvalishe/free-turn-proxy/internal/netconn"
	"github.com/samosvalishe/free-turn-proxy/internal/proxy/bond"
	"github.com/samosvalishe/free-turn-proxy/internal/proxy/tcpserver"
	"github.com/samosvalishe/free-turn-proxy/internal/transport/kcpmux"
	"github.com/xtaci/smux"
)

func pairedBondSession(t *testing.T, ctx context.Context, server *bond.Server, backend string) *smux.Session {
	t.Helper()
	a, b := netconn.DatagramPipe(2048, 1024)
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	done := make(chan struct{})
	go func() {
		defer close(done)
		tcpserver.HandleBond(ctx, logx.Nop(), b, backend, kcpmux.DefaultProfile(), server, "test-client")
	}()
	kcp, err := kcpmux.Dial(a, kcpmux.DefaultProfile())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := smux.Client(kcp, kcpmux.SmuxConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = sess.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("bond server leaked")
		}
	})
	return sess
}

func TestBondAcrossSessionsAndAfterSessionFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	pool := newSessionPool(nil)
	pool.bond = true
	server := &bond.Server{}
	backend := echoBackend(t)
	for i := range 3 {
		pool.Add(i+1, pairedBondSession(t, ctx, server, backend), nil)
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = ln.Close() })
	defer stop()
	loopDone := make(chan struct{})
	go func() { defer close(loopDone); acceptLoop(ctx, &Deps{Log: logx.Nop()}, ln, pool) }()
	conn := dialBondTest(t, ctx, ln.Addr().String())
	payload := bytes.Repeat([]byte("bond payload"), 128*1024)
	checkBondEcho(t, conn, payload)
	for _, ps := range pool.sessions {
		if ps.sess.NumStreams() != 1 {
			t.Fatalf("session %d: expected one bond lane", ps.id)
		}
	}
	_ = pool.sessions[1].sess.Close()
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("broken bond still open")
	}
	_ = conn.Close()
	// Следующее соединение использует оставшиеся сессии без перезапуска listener.
	conn = dialBondTest(t, ctx, ln.Addr().String())
	checkBondEcho(t, conn, payload)
	_ = conn.Close()
	cancel()
	select {
	case <-loopDone:
	case <-time.After(5 * time.Second):
		t.Fatal("accept loop leaked")
	}
}

func dialBondTest(t *testing.T, ctx context.Context, addr string) net.Conn {
	t.Helper()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	return conn
}

func checkBondEcho(t *testing.T, conn net.Conn, payload []byte) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		n, err := conn.Write(payload)
		if err == nil && n != len(payload) {
			err = fmt.Errorf("short write %d", n)
		}
		done <- err
	}()
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("corrupted echo")
	}
}
