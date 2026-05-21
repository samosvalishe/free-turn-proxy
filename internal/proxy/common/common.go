// Package common содержит хелперы, общие для udprelay и tcpfwd
// (TURN-dial + создание wrap-кодека). Два режима прокси по-разному компонуют DTLS и
// srtpmimicry, поэтому полная абстракция Engine/Handler намеренно не вводится —
// пакет собирает только действительно идентичный код.
package common

import (
	"context"
	"fmt"
	"net"

	"github.com/samosvalishe/btp/internal/transport/turndial"
	"github.com/samosvalishe/btp/internal/wire/srtpmimicry"
)

// GetCredsFunc разрешает VK TURN-реквизиты для пары (link, streamID).
// Соответствует vkauth.Client.GetCredentials.
type GetCredsFunc func(ctx context.Context, link string, streamID int) (user, pass, rawURL string, err error)

// DialTURN получает реквизиты и открывает TURN-поток. Вызывающий отвечает
// за закрытие потока и политику retry при auth-ошибке (udprelay)
// или перезапуска сессии (tcpfwd).
func DialTURN(ctx context.Context, host, port string, udp bool, peer *net.UDPAddr, link string, streamID int, getCreds GetCredsFunc) (*turndial.Stream, error) {
	user, pass, rawURL, err := getCreds(ctx, link, streamID)
	if err != nil {
		return nil, fmt.Errorf("get TURN creds: %w", err)
	}
	return turndial.Open(ctx, turndial.Config{
		HostOverride: host,
		PortOverride: port,
		TransportUDP: udp,
	}, peer, user, pass, rawURL)
}

// NewClientWrap возвращает клиентский srtpmimicry.Conn если key нужной длины,
// иначе (nil, nil) — wrap отключён. Ошибки NewConn пробрасываются вызывающему.
func NewClientWrap(key []byte) (*srtpmimicry.Conn, error) {
	if len(key) != srtpmimicry.KeyLen {
		return nil, nil
	}
	return srtpmimicry.NewConn(key, false)
}
