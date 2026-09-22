package config

import (
	"encoding/hex"
	"strconv"
	"strings"
)

// ClientArgs восстанавливает срез CLI-аргументов из Client структуры.
func ClientArgs(c *Client) []string {
	if c == nil {
		return nil
	}
	def := Defaults()
	var args []string
	addIf := func(cond bool, flag string, value ...string) {
		if cond {
			args = append(args, flag)
			args = append(args, value...)
		}
	}

	args = append(args, "-peer", c.Proxy.Peer)
	addIf(len(c.VK.Links) > 0, "-links", strings.Join(c.VK.Links, ","))
	addIf(c.Provider.Name != def.Provider.Name, "-provider", c.Provider.Name)
	addIf(c.Proxy.Listen != def.Proxy.Listen, "-listen", c.Proxy.Listen)
	addIf(c.TURN.Host != "", "-turn", c.TURN.Host)
	addIf(c.TURN.Port != "", "-port", c.TURN.Port)
	addIf(c.TURN.N != def.TURN.N, "-n", strconv.Itoa(c.TURN.N))
	addIf(c.VK.StreamsPerCred != def.VK.StreamsPerCred, "-streams-per-cred", strconv.Itoa(c.VK.StreamsPerCred))
	addIf(c.TURN.TransportUDP != def.TURN.TransportUDP, "-transport", TransportUDP)
	addIf(c.Proxy.Mode != def.Proxy.Mode, "-mode", string(c.Proxy.Mode))
	args = append(args, kcpArgs(c.KCP.Profile, def.KCP.Profile)...)
	addIf(c.Obf.Enabled(), "-obf-profile", string(c.Obf.Profile))
	addIf(c.Obf.Enabled(), "-obf-key", hex.EncodeToString(c.Obf.Key))
	addIf(c.Obf.Timing > 0, "-obf-timing", c.Obf.Timing.String())
	addIf(c.VK.ManualCaptcha, "-manual-captcha")
	addIf(c.VK.Platform != def.VK.Platform, "-platform", string(c.VK.Platform))
	addIf(c.DNS.Mode != def.DNS.Mode, "-dns-mode", c.DNS.Mode)
	addIf(len(c.DNS.Servers) > 0, "-dns-servers", strings.Join(c.DNS.Servers, ","))
	addIf(c.ClientID != "", "-client-id", c.ClientID)
	addIf(c.SubURL != "", "-sub", c.SubURL)
	addIf(c.Log.Debug, "-debug")
	return args
}
