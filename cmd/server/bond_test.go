package main

import (
	"testing"

	"github.com/samosvalishe/free-turn-proxy/internal/clientsdb"
	"github.com/samosvalishe/free-turn-proxy/internal/config"
)

func TestAdmitTCPAndBond(t *testing.T) {
	for _, mode := range []config.ProxyMode{config.ProxyModeTCP, config.ProxyModeUDP} {
		cfg := &config.Server{Proxy: config.ProxyOpts{Mode: mode}}
		for _, tag := range []byte{clientsdb.ModeUnset, clientsdb.ModeUDP, clientsdb.ModeTCP, clientsdb.ModeTCPBond, 255} {
			err := admitClient(cfg, nil, "test-client", tag)
			want := (mode == config.ProxyModeTCP && (tag == clientsdb.ModeTCP || tag == clientsdb.ModeTCPBond)) || (mode == config.ProxyModeUDP && (tag == clientsdb.ModeUnset || tag == clientsdb.ModeUDP))
			if (err == nil) != want {
				t.Fatalf("mode=%s tag=%d err=%v", mode, tag, err)
			}
		}
	}
}
