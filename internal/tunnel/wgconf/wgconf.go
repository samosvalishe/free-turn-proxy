// Package wgconf выполняет парсинг конфигурации формата wg-quick в tunnel.Config.
package wgconf

import (
	"encoding/base64"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/samosvalishe/free-turn-proxy/internal/tunnel"
)

// ignoredKeys содержит ключи wg-quick, управляемые внешней платформой/ОС.
var ignoredKeys = map[string]bool{
	"table":       true,
	"fwmark":      true,
	"listenport":  true,
	"saveconfig":  true,
	"preup":       true,
	"postup":      true,
	"predown":     true,
	"postdown":    true,
	"description": true,
}

// Parse разбирает и валидирует текст конфигурации wg-quick.
func Parse(text string) (*tunnel.Config, error) {
	cfg := tunnel.Defaults()

	const (
		sectionNone = iota
		sectionInterface
		sectionPeer
	)
	section := sectionNone

	for i, rawLine := range strings.Split(text, "\n") {
		line := stripComment(rawLine)
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			switch strings.ToLower(line) {
			case "[interface]":
				section = sectionInterface
			case "[peer]":
				section = sectionPeer
				cfg.Peers = append(cfg.Peers, tunnel.Peer{})
			default:
				return nil, lineErr(i, fmt.Errorf("unknown section %s", line))
			}
			continue
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, lineErr(i, fmt.Errorf("expected key = value, got %q", line))
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		if ignoredKeys[key] {
			continue
		}

		var err error
		switch section {
		case sectionInterface:
			err = applyInterface(&cfg, key, value)
		case sectionPeer:
			err = applyPeer(&cfg.Peers[len(cfg.Peers)-1], key, value)
		default:
			err = fmt.Errorf("key %q outside of a section", key)
		}
		if err != nil {
			return nil, lineErr(i, err)
		}
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func applyInterface(cfg *tunnel.Config, key, value string) error {
	switch key {
	case "privatekey":
		k, err := parseKey(value)
		if err != nil {
			return err
		}
		cfg.PrivateKey = k
	case "address":
		prefixes, err := parsePrefixes(value)
		if err != nil {
			return err
		}
		cfg.Addresses = append(cfg.Addresses, prefixes...)
	case "dns":
		cfg.DNS = append(cfg.DNS, parseDNS(value)...)
	case "mtu":
		mtu, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("mtu: %w", err)
		}
		cfg.MTU = mtu
	default:
		return applyAmnezia(&cfg.Amnezia, key, value)
	}
	return nil
}

func applyAmnezia(p *tunnel.AmneziaParams, key, value string) error {
	switch key {
	case "jc", "jmin", "jmax", "s1", "s2", "s3", "s4":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		setAmneziaInt(p, key, n)
	case "h1":
		p.H1 = value
	case "h2":
		p.H2 = value
	case "h3":
		p.H3 = value
	case "h4":
		p.H4 = value
	case "i1", "i2", "i3", "i4", "i5":
		p.I[int(key[1]-'1')] = value
	case "headerprotectionkey":
		k, err := parseKey(value)
		if err != nil {
			return fmt.Errorf("headerprotectionkey: %w", err)
		}
		p.HeaderProtectionKey = k
	case "contentpaddingaddition", "rekeyaftertime", "rekeytimeout",
		"rejectaftertime", "keepalivetimeout", "maxhandshakeattempts":
		if err := tunnel.ValidateRange(value); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		setAmneziaRange(p, key, value)
	case "randomtrailers":
		return setBool(&p.RandomTrailers, key, value)
	case "disablecookies":
		return setBool(&p.DisableCookies, key, value)
	default:
		return fmt.Errorf("unknown [Interface] key %q", key)
	}
	return nil
}

func setBool(dst *bool, key, value string) error {
	b, err := parseBool(value)
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	*dst = b
	return nil
}

// AmneziaWG пишет булевы как on/off и yes/no.
func parseBool(value string) (bool, error) {
	switch strings.ToLower(value) {
	case "on", "yes":
		return true, nil
	case "off", "no":
		return false, nil
	}
	return strconv.ParseBool(value)
}

func setAmneziaRange(p *tunnel.AmneziaParams, key, value string) {
	switch key {
	case "contentpaddingaddition":
		p.ContentPaddingAddition = value
	case "rekeyaftertime":
		p.RekeyAfterTime = value
	case "rekeytimeout":
		p.RekeyTimeout = value
	case "rejectaftertime":
		p.RejectAfterTime = value
	case "keepalivetimeout":
		p.KeepaliveTimeout = value
	case "maxhandshakeattempts":
		p.MaxHandshakeAttempts = value
	}
}

func setAmneziaInt(p *tunnel.AmneziaParams, key string, n int) {
	switch key {
	case "jc":
		p.Jc = n
	case "jmin":
		p.Jmin = n
	case "jmax":
		p.Jmax = n
	case "s1":
		p.S1 = n
	case "s2":
		p.S2 = n
	case "s3":
		p.S3 = n
	case "s4":
		p.S4 = n
	}
}

func applyPeer(peer *tunnel.Peer, key, value string) error {
	switch key {
	case "publickey":
		k, err := parseKey(value)
		if err != nil {
			return err
		}
		peer.PublicKey = k
	case "presharedkey":
		k, err := parseKey(value)
		if err != nil {
			return err
		}
		peer.PresharedKey = k
	case "allowedips":
		prefixes, err := parsePrefixes(value)
		if err != nil {
			return err
		}
		peer.AllowedIPs = append(peer.AllowedIPs, prefixes...)
	case "endpoint":
		peer.Endpoint = value
	case "persistentkeepalive":
		if strings.EqualFold(value, "off") {
			peer.Keepalive = ""
			return nil
		}
		if err := tunnel.ValidateRange(value); err != nil {
			return fmt.Errorf("persistentkeepalive: %w", err)
		}
		peer.Keepalive = value
	default:
		return fmt.Errorf("unknown [Peer] key %q", key)
	}
	return nil
}

func parseKey(value string) (tunnel.Key, error) {
	var k tunnel.Key
	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return k, fmt.Errorf("key is not base64: %w", err)
	}
	if len(raw) != tunnel.KeyLen {
		return k, fmt.Errorf("key must be %d bytes, got %d", tunnel.KeyLen, len(raw))
	}
	copy(k[:], raw)
	return k, nil
}

// parsePrefixes разбирает список IP/CIDR (голый IP преобразуется в /32 или /128).
func parsePrefixes(value string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(item); err == nil {
			out = append(out, prefix)
			continue
		}
		addr, err := netip.ParseAddr(item)
		if err != nil {
			return nil, fmt.Errorf("bad address %q", item)
		}
		out = append(out, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return out, nil
}

// parseDNS извлекает только IP-адреса DNS, игнорируя search domains.
func parseDNS(value string) []netip.Addr {
	var out []netip.Addr
	for _, item := range strings.Split(value, ",") {
		if addr, err := netip.ParseAddr(strings.TrimSpace(item)); err == nil {
			out = append(out, addr)
		}
	}
	return out
}

func stripComment(line string) string {
	if idx := strings.IndexAny(line, "#;"); idx >= 0 {
		line = line[:idx]
	}
	return strings.TrimSpace(line)
}

func lineErr(idx int, err error) error {
	return fmt.Errorf("wgconf: line %d: %w", idx+1, err)
}
