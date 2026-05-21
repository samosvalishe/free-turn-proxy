package auth

import (
	"context"
	"net"
)

// TenantID идентифицирует тенанта в multi-tenant конфигурации.
// Нулевое значение ("") — sentinel [Anonymous] для single-tenant режима.
type TenantID string

// Anonymous — sentinel TenantID, когда аутентификация выключена
// (single-tenant / no-op). Все вызовы передают это значение, пока
// настоящий Authenticator не подключён.
const Anonymous TenantID = ""

// Authenticator аутентифицирует входящее соединение и возвращает TenantID.
// Реализации обязаны быть конкурентно безопасны.
//
// nil ошибка с [Anonymous] означает приём в не-multi-tenant режиме.
// Любая не-nil ошибка трактуется как отказ; вызывающий обязан закрыть conn.
//
// Интерфейс ориентирован на потоки (net.Conn) — для bondserver/tcpfwdserver.
// UDP-режиму (если потребуется) нужен отдельный интерфейс с токеном из
// out-of-band handshake, т.к. в udpserver нет per-tenant абстракции потока.
type Authenticator interface {
	Authenticate(ctx context.Context, conn net.Conn) (TenantID, error)
}

// NopAuthenticator — no-op Authenticator, всегда возвращает [Anonymous].
// Используется по умолчанию, когда multi-tenant не настроен.
type NopAuthenticator struct{}

var _ Authenticator = NopAuthenticator{}

// Authenticate реализует [Authenticator]: всегда успех и [Anonymous].
func (NopAuthenticator) Authenticate(_ context.Context, _ net.Conn) (TenantID, error) {
	return Anonymous, nil
}
