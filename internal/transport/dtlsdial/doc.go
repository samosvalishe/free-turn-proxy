// DTLS-as-obfuscation note (V2-8):
//
// DTLS in this project is used primarily as an *obfuscation* layer, not as a
// security boundary. The VK TURN content-filter drops payloads that do not
// look like a legitimate DTLS handshake followed by encrypted records, so the
// handshake is what gets us through. The encryption it provides is a free
// side-effect — we already trust the wrapped tunnel (WireGuard inside UDP
// mode, or smux+KCP inside tcpfwd mode) to handle confidentiality and
// integrity end-to-end.
//
// This is why the older `-no-dtls` / `Direct` flag was removed in V2-0: with
// DTLS off, VK drops packets and the proxy effectively does not work in
// production. There is no scenario in which disabling DTLS is the right
// call against the live VK filter.
//
// Implication for future changes: do not treat the DTLS layer as if it were
// security-critical (e.g. it is fine to use self-signed certs, send-only CID,
// and no peer-cert validation — those are deliberate, not oversights).
package dtlsdial
