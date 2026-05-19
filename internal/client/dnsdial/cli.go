package dnsdial

import (
	"net"
	"sync"
)

var (
	dohResolverOnce     sync.Once
	dohResolverInstance *DohResolver
)

func sharedDohResolver() *DohResolver {
	dohResolverOnce.Do(func() {
		dohResolverInstance = NewDohResolver(nil)
	})
	return dohResolverInstance
}

// AppDialer returns the net.Dialer used by tls-client and other HTTP callers.
// DNS transport is selected by mode (udp | doh | auto).
func AppDialer(mode string) net.Dialer {
	return buildDialer(mode, sharedDohResolver())
}

// InstallGlobalResolver wires net.DefaultResolver to the same DNS transport
// as AppDialer, so third-party libs that build their own http.Client without
// our Dialer still use DoH / auto-fallback instead of the OS resolver.
func InstallGlobalResolver(mode string) {
	d := AppDialer(mode)
	if d.Resolver != nil {
		net.DefaultResolver = d.Resolver
	}
}
