// Package auth defines the Authenticator interface and tenant identity types
// for multi-tenant support.
//
// # Current state
//
// Only the scaffold is present. The [NopAuthenticator] is used everywhere and
// returns [Anonymous], preserving existing single-tenant behaviour.
//
// # Planned multi-tenant flow
//
//  1. A real Authenticator (e.g. HMACAuthenticator backed by SQLite) reads a
//     short token from the first bytes of conn, validates it against a per-tenant
//     shared secret, and returns the matching [TenantID].
//  2. bondserver.Registry uses the TenantID as part of its de-duplication key so
//     ConnIDs from different tenants never collide.
//  3. tcpfwdserver and udpserver resolve connectAddr per-tenant, allowing each
//     tenant to target a different backend.
//  4. Deep-link provisioning delivers per-tenant secrets to mobile clients without
//     manual configuration.
//
// None of the items above are implemented yet. See notes/AUTH_PLAN.md for the
// detailed design.
package auth
