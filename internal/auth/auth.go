package auth

import (
	"context"
	"net"
)

// TenantID identifies a tenant in a multi-tenant deployment.
// The zero value ("") is the [Anonymous] sentinel used in single-tenant mode.
type TenantID string

// Anonymous is the sentinel TenantID used when authentication is disabled
// (single-tenant / no-op mode). All call-sites pass this value until a real
// Authenticator is wired in.
const Anonymous TenantID = ""

// Authenticator authenticates an incoming connection and returns the
// TenantID it belongs to. Implementations must be safe for concurrent use.
//
// A nil error with [Anonymous] means the connection is accepted in
// non-multi-tenant mode. Any non-nil error must be treated as a rejection
// and the caller must close conn.
//
// This interface is stream-oriented: it consumes a net.Conn and is intended
// for bondserver/tcpfwdserver wiring. UDP-mode auth (when needed) will get
// a separate interface taking a token []byte read out-of-band from the
// handshake, since udpserver has no per-tenant stream abstraction.
type Authenticator interface {
	Authenticate(ctx context.Context, conn net.Conn) (TenantID, error)
}

// NopAuthenticator is a no-op Authenticator that always returns [Anonymous].
// It is the default when multi-tenant support is not configured.
type NopAuthenticator struct{}

// compile-time interface check.
var _ Authenticator = NopAuthenticator{}

// Authenticate implements [Authenticator]. It always succeeds and returns
// [Anonymous] without inspecting conn or ctx.
func (NopAuthenticator) Authenticate(_ context.Context, _ net.Conn) (TenantID, error) {
	return Anonymous, nil
}
