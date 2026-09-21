// Package wire предоставляет интерфейс и фабрики для профилей обфускации трафика.
package wire

import (
	"fmt"
	"net"
	"time"

	dtlsnet "github.com/pion/dtls/v3/pkg/net"

	"github.com/samosvalishe/free-turn-proxy/internal/wire/rtpopus"
	"github.com/samosvalishe/free-turn-proxy/internal/wire/rtpopus2"
	"github.com/samosvalishe/free-turn-proxy/internal/wire/rtpopus3"
	"github.com/samosvalishe/free-turn-proxy/internal/wire/shape"
)

const (
	ProfileNone     = "none"
	ProfileRTPOpus  = "rtpopus"
	ProfileRTPOpus2 = "rtpopus2"
	ProfileRTPOpus3 = "rtpopus3"
)

// Codec определяет методы кодирования и декодирования пакетов wire-профиля.
type Codec interface {
	WrapInPlace(buf []byte, plainLen int) (int, error)
	UnwrapInPlace(wire []byte) ([]byte, error)
	WrapInto(dst, payload []byte) (int, error)
	Unwrap(wire, dst []byte) (int, error)
	HeaderLen() int
	Overhead() int
	MaxWire(payloadLen int) int
}

// NewClientCodec создаёт экземпляр клиентского кодека по имени профиля.
func NewClientCodec(profile string, key []byte) (Codec, error) {
	switch profile {
	case ProfileNone, "":
		return nil, nil
	case ProfileRTPOpus:
		return rtpopus.NewConn(key, false)
	case ProfileRTPOpus2:
		return rtpopus2.NewConn(key, false)
	case ProfileRTPOpus3:
		return rtpopus3.NewConn(key, false)
	default:
		return nil, fmt.Errorf("wire: unknown obf profile %q", profile)
	}
}

// Listen создаёт серверный PacketListener для указанного профиля обфускации.
func Listen(profile string, addr *net.UDPAddr, key []byte, serverTiming ...time.Duration) (dtlsnet.PacketListener, error) {
	var timing time.Duration
	if len(serverTiming) > 0 {
		timing = serverTiming[0]
	}

	newCodec, err := serverCodecFactory(profile, key)
	if err != nil {
		return nil, err
	}
	listener, err := ListenCodec(addr, newCodec)
	if err != nil {
		return nil, err
	}
	return shape.WrapPacketListener(listener, timing), nil
}

func serverCodecFactory(profile string, key []byte) (NewServerCodec, error) {
	switch profile {
	case ProfileRTPOpus:
		st, err := rtpopus.NewState(key)
		if err != nil {
			return nil, err
		}
		return func() (Codec, error) { return rtpopus.NewConnFromState(st, true) }, nil
	case ProfileRTPOpus2:
		st, err := rtpopus2.NewState(key)
		if err != nil {
			return nil, err
		}
		return func() (Codec, error) { return rtpopus2.NewConnFromState(st, true) }, nil
	case ProfileRTPOpus3:
		st, err := rtpopus3.NewState(key)
		if err != nil {
			return nil, err
		}
		return func() (Codec, error) { return rtpopus3.NewConnFromState(st, true) }, nil
	default:
		return nil, fmt.Errorf("wire: profile %q has no server listener", profile)
	}
}
