package dnsdial

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func dnsAnswer(t *testing.T, query []byte) []byte {
	t.Helper()
	req := new(dns.Msg)
	if err := req.Unpack(query); err != nil {
		t.Fatalf("unpack query: %v", err)
	}
	if len(req.Question) != 1 {
		t.Fatalf("expected 1 question, got %d", len(req.Question))
	}
	reply := new(dns.Msg)
	reply.SetReply(req)
	if q := req.Question[0]; q.Qtype == dns.TypeA {
		reply.Answer = append(reply.Answer, &dns.A{
			Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300},
			A:   net.IPv4(9, 9, 9, 9),
		})
	}
	out, err := reply.Pack()
	if err != nil {
		t.Fatalf("pack reply: %v", err)
	}
	return out
}

func mockDoH(t *testing.T) *DohResolver {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(dnsAnswer(t, body))
	}))
	t.Cleanup(srv.Close)
	return newDohResolverWithClient(
		[]DohEndpoint{{URL: srv.URL, Hostname: "mock", BootstrapIPs: []string{"127.0.0.1"}}},
		srv.Client(),
	)
}

// udpDNSServer поднимает UDP/53-стенд; reply == nil - сервер молчит.
func udpDNSServer(t *testing.T, reply func(query []byte) []byte) string {
	t.Helper()
	pc, err := (&net.ListenConfig{}).ListenPacket(t.Context(), "udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 512)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if reply != nil {
				_, _ = pc.WriteTo(reply(buf[:n]), addr)
			}
		}
	}()
	return pc.LocalAddr().String()
}

func useUDPServers(t *testing.T, servers []string) {
	t.Helper()
	old := udpDNSServersPtr.Load()
	udpDNSServersPtr.Store(&servers)
	t.Cleanup(func() { udpDNSServersPtr.Store(old) })
}

func TestAutoDial_DoHStickyUntilServersChange(t *testing.T) {
	dial := autoDial(mockDoH(t))
	// Невалидный адрес валит UDP-пробу сразу.
	servers := []string{"not-a-valid-host-port"}
	useUDPServers(t, servers)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	dialTo := func() string {
		t.Helper()
		conn, err := dial(ctx, "udp", "unused")
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer func() { _ = conn.Close() }()
		return conn.RemoteAddr().String()
	}
	dialTo()

	good := udpDNSServer(t, func(q []byte) []byte { return dnsAnswer(t, q) })
	servers[0] = good
	if dialTo() == good {
		t.Fatal("re-probed UDP without servers change")
	}

	SetUDPDNSServers([]string{good})
	if got := dialTo(); got != good {
		t.Fatalf("after servers change dial went to %s, want UDP %s", got, good)
	}
}

// Молчащий первый сервер не должен съедать бюджет второго.
func TestUDPProbe_SilentFirstServer(t *testing.T) {
	useUDPServers(t, []string{udpDNSServer(t, nil), udpDNSServer(t, func(q []byte) []byte { return dnsAnswer(t, q) })})
	if !udpProbe(300 * time.Millisecond) {
		t.Fatal("udpProbe = false, want true via second server")
	}
}

// Эхо запроса - не DNS-ответ, UDP/53 такой сервер не обслуживает.
func TestUDPProbe_RejectsNonResponse(t *testing.T) {
	useUDPServers(t, []string{udpDNSServer(t, func(q []byte) []byte { return append([]byte(nil), q...) })})
	if udpProbe(300 * time.Millisecond) {
		t.Fatal("udpProbe = true on echoed query, want false")
	}
}
