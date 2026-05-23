// Package common содержит хелперы, общие для udprelay и tcpfwd
// (TURN-dial + создание obf-кодека). Два режима прокси по-разному компонуют DTLS
// и rtpopus, поэтому полная абстракция Engine/Handler намеренно не вводится —
// пакет собирает только действительно идентичный код.
package common

import (
	"context"
	"fmt"
	"net"

	"github.com/samosvalishe/free-turn-proxy/internal/transport/turndial"
	"github.com/samosvalishe/free-turn-proxy/internal/wire/rtpopus"
)

// GetCredsFunc разрешает TURN-реквизиты для streamID. Реализуется provider'ом
// (см. internal/provider): provider держит идентификатор сессии (link/room/key)
// внутри, pipeline передаёт только streamID.
type GetCredsFunc func(ctx context.Context, streamID int) (user, pass, rawURL string, err error)

// DialTURN получает реквизиты и открывает TURN-поток. Вызывающий отвечает
// за закрытие потока и политику retry при auth-ошибке (udprelay)
// или перезапуска сессии (tcpfwd).
func DialTURN(ctx context.Context, host, port string, udp bool, peer *net.UDPAddr, streamID int, getCreds GetCredsFunc) (*turndial.Stream, error) {
	user, pass, rawURL, err := getCreds(ctx, streamID)
	if err != nil {
		return nil, fmt.Errorf("get TURN creds: %w", err)
	}
	return turndial.Open(ctx, turndial.Config{
		HostOverride: host,
		PortOverride: port,
		TransportUDP: udp,
	}, peer, user, pass, rawURL)
}

// NewClientObf возвращает клиентский rtpopus.Conn если key нужной длины,
// иначе (nil, nil) — обфускация отключена. Ошибки NewConn пробрасываются вызывающему.
func NewClientObf(key []byte) (*rtpopus.Conn, error) {
	if len(key) != rtpopus.KeyLen {
		return nil, nil
	}
	return rtpopus.NewConn(key, false)
}
