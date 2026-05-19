package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/cacggghp/vk-turn-proxy/internal/bond"
	bondserver "github.com/cacggghp/vk-turn-proxy/internal/bond/server"
	"github.com/cacggghp/vk-turn-proxy/internal/config"
	"github.com/cacggghp/vk-turn-proxy/internal/stats"
	"github.com/cacggghp/vk-turn-proxy/internal/wrap"
	"github.com/cacggghp/vk-turn-proxy/tcputil"
	"github.com/pion/dtls/v3"
	"github.com/pion/dtls/v3/pkg/crypto/selfsign"
	"github.com/xtaci/smux"
)

var isDebug bool

func debugf(format string, v ...any) {
	if isDebug {
		log.Printf(format, v...)
	}
}

func main() {
	cfg, err := config.ParseServer(os.Args[1:], os.Stderr)
	if err != nil {
		log.Panicf("%v", err)
	}
	isDebug = cfg.Debug

	if cfg.GenWrapKey {
		key, gerr := wrap.GenKeyHex()
		if gerr != nil {
			log.Panicf("gen-wrap-key: %v", gerr)
		}
		fmt.Println(key)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-signalChan
		log.Printf("Terminating...\n")
		cancel()
		<-signalChan
		log.Fatalf("Exit...\n")
	}()

	addr, err := net.ResolveUDPAddr("udp", cfg.Listen)
	if err != nil {
		panic(err)
	}
	wrapKey := cfg.WrapKey
	log.Printf("Starting server listen=%s connect=%s vless=%t vless-bond=%t wrap=%t bond-autodetect=true", cfg.Listen, cfg.Connect, cfg.VLESSMode, cfg.VLESSBond, cfg.WrapMode)
	// Generate a certificate and private key to secure the connection
	certificate, genErr := selfsign.GenerateSelfSigned()
	if genErr != nil {
		panic(genErr)
	}

	//
	// Everything below is the pion-DTLS API! Thanks for using it ❤️.
	//

	dtlsOpts := []dtls.ServerOption{
		dtls.WithCertificates(certificate),
		dtls.WithExtendedMasterSecret(dtls.RequireExtendedMasterSecret),
		dtls.WithCipherSuites(dtls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256),
		dtls.WithConnectionIDGenerator(dtls.RandomCIDGenerator(8)),
	}
	var listener net.Listener
	if cfg.WrapMode {
		log.Printf("WRAP mode enabled: listener only accepts clients with matching -wrap-key")
		wrapListener, werr := wrap.Listen(addr, wrapKey)
		if werr != nil {
			panic(werr)
		}
		listener, err = dtls.NewListenerWithOptions(wrapListener, dtlsOpts...)
	} else {
		listener, err = dtls.ListenWithOptions("udp", addr, dtlsOpts...)
	}
	if err != nil {
		panic(err)
	}
	context.AfterFunc(ctx, func() {
		if err = listener.Close(); err != nil {
			panic(err)
		}
	})

	fmt.Println("Listening")

	wg1 := sync.WaitGroup{}
	for {
		select {
		case <-ctx.Done():
			wg1.Wait()
			return
		default:
		}
		// Wait for a connection.
		conn, err := listener.Accept()
		if err != nil {
			log.Println(err)
			continue
		}
		wg1.Go(func() {
			defer func() {
				if closeErr := conn.Close(); closeErr != nil {
					log.Printf("failed to close incoming connection: %s", closeErr)
				}
			}()
			debugf("Connection from %s\n", conn.RemoteAddr())

			// Perform the handshake with a 30-second timeout
			ctx1, cancel1 := context.WithTimeout(ctx, 30*time.Second)
			defer cancel1()

			dtlsConn, ok := conn.(*dtls.Conn)
			if !ok {
				log.Println("Type error: expected *dtls.Conn")
				return
			}
			debugf("Start handshake")
			if err := dtlsConn.HandshakeContext(ctx1); err != nil {
				log.Printf("Handshake failed: %v", err)
				return
			}
			debugf("Handshake done")

			if cfg.VLESSMode {
				handleVLESSConnection(ctx, dtlsConn, cfg.Connect, cfg.VLESSBond)
			} else {
				handleUDPConnection(ctx, conn, cfg.Connect)
			}

			debugf("Connection closed: %s\n", conn.RemoteAddr())
		})
	}
}

var globalBondRegistry = bondserver.NewRegistry(bondserver.Deps{Debug: isDebug, Debugf: debugf})

type prefixedConn struct {
	net.Conn
	prefix []byte
}

func (c *prefixedConn) Read(p []byte) (int, error) {
	if len(c.prefix) > 0 {
		n := copy(p, c.prefix)
		c.prefix = c.prefix[n:]
		return n, nil
	}
	return c.Conn.Read(p)
}

// handleUDPConnection forwards DTLS packets to a UDP backend (WireGuard).
func handleUDPConnection(ctx context.Context, conn net.Conn, connectAddr string) {
	serverConn, err := net.Dial("udp", connectAddr)
	if err != nil {
		log.Println(err)
		return
	}
	defer func() {
		if err = serverConn.Close(); err != nil {
			log.Printf("failed to close outgoing connection: %s", err)
		}
	}()

	var wg sync.WaitGroup
	ctx2, cancel2 := context.WithCancel(ctx)
	st := stats.New(isDebug)
	go st.LogEvery(
		ctx2,
		debugf,
		fmt.Sprintf("[DTLS %s]", conn.RemoteAddr()),
		"dtls-to-backend",
		"backend-to-dtls",
	)

	context.AfterFunc(ctx2, func() {
		if err := conn.SetDeadline(time.Now()); err != nil {
			log.Printf("failed to set incoming deadline: %s", err)
		}
		if err := serverConn.SetDeadline(time.Now()); err != nil {
			log.Printf("failed to set outgoing deadline: %s", err)
		}
	})
	wg.Go(func() {
		defer cancel2()
		buf := make([]byte, 1600)
		for {
			select {
			case <-ctx2.Done():
				return
			default:
			}
			if err1 := conn.SetReadDeadline(time.Now().Add(time.Minute * 30)); err1 != nil {
				log.Printf("Failed: %s", err1)
				return
			}
			n, err1 := conn.Read(buf)
			if err1 != nil {
				log.Printf("Failed: %s", err1)
				return
			}

			if err1 = serverConn.SetWriteDeadline(time.Now().Add(time.Minute * 30)); err1 != nil {
				log.Printf("Failed: %s", err1)
				return
			}
			written, err1 := serverConn.Write(buf[:n])
			st.AddTx(written)
			if err1 != nil {
				log.Printf("Failed: %s", err1)
				return
			}
		}
	})
	wg.Go(func() {
		defer cancel2()
		buf := make([]byte, 1600)
		for {
			select {
			case <-ctx2.Done():
				return
			default:
			}
			if err1 := serverConn.SetReadDeadline(time.Now().Add(time.Minute * 30)); err1 != nil {
				log.Printf("Failed: %s", err1)
				return
			}
			n, err1 := serverConn.Read(buf)
			if err1 != nil {
				log.Printf("Failed: %s", err1)
				return
			}

			if err1 = conn.SetWriteDeadline(time.Now().Add(time.Minute * 30)); err1 != nil {
				log.Printf("Failed: %s", err1)
				return
			}
			written, err1 := conn.Write(buf[:n])
			st.AddRx(written)
			if err1 != nil {
				log.Printf("Failed: %s", err1)
				return
			}
		}
	})
	wg.Wait()
}

// handleVLESSConnection creates a KCP+smux session over DTLS and forwards
// each smux stream as a TCP connection to the backend (Xray/VLESS).
func handleVLESSConnection(ctx context.Context, dtlsConn net.Conn, connectAddr string, useBond bool) {
	// 1. Create KCP session over DTLS
	statsCtx, statsCancel := context.WithCancel(ctx)
	defer statsCancel()
	st := stats.New(isDebug)
	go st.LogEvery(
		statsCtx,
		debugf,
		fmt.Sprintf("[VLESS %s]", dtlsConn.RemoteAddr()),
		"to-client",
		"from-client",
	)

	kcpSess, err := tcputil.NewKCPOverDTLS(&stats.CountingConn{Conn: dtlsConn, Stats: st}, true)
	if err != nil {
		log.Printf("KCP session error: %s", err)
		return
	}
	defer func() {
		if closeErr := kcpSess.Close(); closeErr != nil {
			log.Printf("failed to close KCP session: %v", closeErr)
		}
	}()
	debugf("KCP session established (server)")

	// 2. Create smux server session over KCP
	smuxSess, err := smux.Server(kcpSess, tcputil.DefaultSmuxConfig())
	if err != nil {
		log.Printf("smux server error: %s", err)
		return
	}
	defer func() {
		if err := smuxSess.Close(); err != nil {
			log.Printf("failed to close smux session: %v", err)
		}
	}()
	debugf("smux session established (server)")

	// 3. Accept smux streams and forward to backend via TCP
	var wg sync.WaitGroup
	for {
		stream, err := smuxSess.AcceptStream()
		if err != nil {
			select {
			case <-ctx.Done():
			default:
				log.Printf("smux accept error: %s", err)
			}
			break
		}

		s := stream
		wg.Go(func() {
			var prefix [4]byte
			if _, err := io.ReadFull(s, prefix[:]); err != nil {
				if err != io.EOF && err != io.ErrUnexpectedEOF {
					log.Printf("smux stream prefix read error: %v", err)
				}
				_ = s.Close()
				return
			}
			if string(prefix[:]) == bond.Magic {
				debugf("auto-detected bond smux stream")
				globalBondRegistry.HandleStreamAfterMagic(ctx, s, connectAddr, prefix)
				return
			}
			if useBond {
				log.Printf("non-bond smux stream accepted while -vless-bond is enabled")
			}

			defer func() {
				if err := s.Close(); err != nil && err != smux.ErrGoAway {
					log.Printf("failed to close smux stream: %v", err)
				}
			}()

			// Connect to backend (Xray/VLESS)
			backendConn, err := net.DialTimeout("tcp", connectAddr, 10*time.Second)
			if err != nil {
				log.Printf("backend dial error: %s", err)
				return
			}
			defer func() {
				if err := backendConn.Close(); err != nil {
					log.Printf("failed to close backend connection: %v", err)
				}
			}()

			// Bidirectional copy
			pipeConn(ctx, &prefixedConn{Conn: s, prefix: prefix[:]}, backendConn)
		})
	}
	wg.Wait()
}

// pipeConn copies data bidirectionally between two connections.
func pipeConn(ctx context.Context, c1, c2 net.Conn) {
	ctx2, cancel := context.WithCancel(ctx)
	defer cancel()

	context.AfterFunc(ctx2, func() {
		if err := c1.SetDeadline(time.Now()); err != nil {
			debugf("pipeConn: failed to set deadline c1: %v", err)
		}
		if err := c2.SetDeadline(time.Now()); err != nil {
			debugf("pipeConn: failed to set deadline c2: %v", err)
		}
	})

	var wg sync.WaitGroup

	wg.Go(func() {
		if _, err := io.Copy(c1, c2); err != nil {
			debugf("pipeConn: c1<-c2 copy error: %v", err)
		}
	})

	wg.Go(func() {
		if _, err := io.Copy(c2, c1); err != nil {
			debugf("pipeConn: c2<-c1 copy error: %v", err)
		}
	})

	wg.Wait()

	// Reset deadlines (best-effort; connection may already be closed)
	if err := c1.SetDeadline(time.Time{}); err != nil {
		debugf("pipeConn: failed to reset deadline c1: %v", err)
	}
	if err := c2.SetDeadline(time.Time{}); err != nil {
		debugf("pipeConn: failed to reset deadline c2: %v", err)
	}
}
