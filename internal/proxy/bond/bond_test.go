package bond

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/samosvalishe/free-turn-proxy/internal/stats"
	"github.com/xtaci/smux"
)

func tcpPair(t *testing.T) (*net.TCPConn, net.Conn) {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	a, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	b, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	if err := a.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		t.Fatal(err)
	}
	tcp, ok := a.(*net.TCPConn)
	if !ok {
		t.Fatal("expected TCP")
	}
	return tcp, b
}

func TestSessionFailureAfterFINStopsCopy(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	a, b := net.Pipe()
	clientSession, err := smux.Client(a, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = clientSession.Close() }()
	serverSession, err := smux.Server(b, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = serverSession.Close() }()
	lane, err := clientSession.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	remote, err := serverSession.AcceptStream()
	if err != nil {
		t.Fatal(err)
	}
	client, local := tcpPair(t)
	done := make(chan error, 1)
	go func() { done <- Copy(ctx, local, []net.Conn{lane}, nil) }()
	if err = writeFrame(remote, frameFIN, 0, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = client.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("half-close: %v", err)
	}
	_ = serverSession.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("session failure after FIN left local read blocked")
	}
}

type delayedRead struct {
	net.Conn
	once sync.Once
}

type blockedWriter struct {
	net.Conn
	writing chan struct{}
}

func (w *blockedWriter) Write(b []byte) (int, error) {
	close(w.writing)
	return w.Conn.Write(b)
}

func TestLaneFailureUnblocksLocalWrite(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	a, b := net.Pipe()
	defer func() { _ = b.Close() }()
	local := &blockedWriter{Conn: a, writing: make(chan struct{})}
	lane, remote := net.Pipe()
	defer func() { _ = remote.Close() }()
	done := make(chan error, 1)
	go func() { done <- Copy(ctx, local, []net.Conn{lane}, nil) }()
	if err := writeFrame(remote, frameData, 0, []byte("blocked")); err != nil {
		t.Fatal(err)
	}
	<-local.writing
	_ = remote.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("lane failure left local write blocked")
	}
}

func (d *delayedRead) Read(p []byte) (int, error) {
	d.once.Do(func() { time.Sleep(30 * time.Millisecond) })
	return d.Conn.Read(p)
}

func TestCopyReordersAndPreservesHalfClose(t *testing.T) {
	for _, count := range []int{1, 3} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			client, clientLocal := tcpPair(t)
			backend, serverLocal := tcpPair(t)
			left, right := make([]net.Conn, count), make([]net.Conn, count)
			for i := range left {
				left[i], right[i] = net.Pipe()
			}
			right[0] = &delayedRead{Conn: right[0]}
			traffic := stats.New(true)
			done := make(chan error, 2)
			go func() { done <- Copy(ctx, clientLocal, left, traffic) }()
			go func() { done <- Copy(ctx, serverLocal, right, nil) }()
			request := bytes.Repeat([]byte("request contents"), 128*1024)
			response := bytes.Repeat([]byte("response after EOF"), 128*1024)
			writer := make(chan error, 1)
			go func() {
				_, err := client.Write(request)
				writer <- errors.Join(err, client.CloseWrite())
			}()
			got, err := io.ReadAll(backend)
			if err != nil || !bytes.Equal(got, request) {
				t.Fatalf("request: len=%d err=%v", len(got), err)
			}
			if err = <-writer; err != nil {
				t.Fatal(err)
			}
			go func() {
				_, werr := backend.Write(response)
				writer <- errors.Join(werr, backend.CloseWrite())
			}()
			got, err = io.ReadAll(client)
			if err != nil || !bytes.Equal(got, response) {
				t.Fatalf("response: len=%d err=%v", len(got), err)
			}
			if err := <-writer; err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			}
			tx, rx := traffic.Counters()
			if tx != uint64(len(request)) || rx != uint64(len(response)) {
				t.Fatalf("traffic=%d/%d", tx, rx)
			}
		})
	}
}

func TestCopyLaneFailureAndCancel(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "lane failure"}[failure], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			local, peer := net.Pipe()
			defer func() { _ = peer.Close() }()
			lanes, remote := make([]net.Conn, 3), make([]net.Conn, 3)
			for i := range lanes {
				lanes[i], remote[i] = net.Pipe()
				defer func() { _ = remote[i].Close() }()
			}
			done := make(chan error, 1)
			go func() { done <- Copy(ctx, local, lanes, nil) }()
			if failure {
				_ = remote[1].Close()
			} else {
				cancel()
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("expected failure")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("copy did not stop")
			}
			if _, err := peer.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
				t.Fatalf("local not closed: %v", err)
			}
		})
	}
}

func TestHelloWireAndValidation(t *testing.T) {
	var b bytes.Buffer
	h := Hello{ID: 0x0102030405060708, Index: 1, Count: 3}
	if err := WriteHello(&b, h); err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(b.Bytes()); got != "564c423101010203040506070800010003" {
		t.Fatal(got)
	}
	got, err := ReadHello(&b)
	if err != nil || got != h {
		t.Fatalf("hello=%+v, %v", got, err)
	}
	for _, invalid := range []Hello{{Count: 0}, {Count: MaxLanes + 1}, {Count: 2, Index: 2}} {
		b.Reset()
		if err := WriteHello(&b, invalid); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadHello(&b); !errors.Is(err, ErrProtocol) {
			t.Fatalf("accepted %+v: %v", invalid, err)
		}
	}
}

func TestReorderRejectsInvalidSequences(t *testing.T) {
	for name, frames := range map[string][]frame{
		"gap":             {{typ: frameFIN, seq: 1}},
		"duplicate":       {{typ: frameData, seq: 1}, {typ: frameData, seq: 1}},
		"overflow":        {{typ: frameData, seq: pendingCap + 1}},
		"conflicting FIN": {{typ: frameData, seq: 1}, {typ: frameFIN, seq: 0}},
	} {
		t.Run(name, func(t *testing.T) {
			ch := make(chan received, len(frames))
			for _, f := range frames {
				ch <- received{frame: f}
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			local, peer := net.Pipe()
			defer func() { _ = local.Close(); _ = peer.Close() }()
			if err := reorder(ctx, local, ch, 1, nil); !errors.Is(err, ErrProtocol) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestServerSeparatesClientsAndRejectsDuplicateLane(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	s := &Server{}
	laneA, remoteA := net.Pipe()
	laneB, remoteB := net.Pipe()
	defer func() { _ = remoteA.Close(); _ = remoteB.Close() }()
	h := Hello{ID: 7, Index: 0, Count: 2}
	key := connectionKey{client: "a", id: h.ID}
	a, err := s.attach(ctx, key, h, laneA, "127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.attach(ctx, connectionKey{client: "b", id: h.ID}, h, laneB, "127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("different clients share group")
	}
	if _, err := s.attach(ctx, key, h, laneA, "127.0.0.1:1"); !errors.Is(err, ErrProtocol) {
		t.Fatal("duplicate accepted")
	}
	cancel()
	for _, g := range []*group{a, b} {
		select {
		case <-g.done:
		case <-time.After(3 * time.Second):
			t.Fatal("group did not stop")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.groups) != 0 {
		t.Fatal("groups leaked")
	}
}
