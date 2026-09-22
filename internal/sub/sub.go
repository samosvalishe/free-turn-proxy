package sub

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/samosvalishe/free-turn-proxy/internal/client/dnsdial"
	"github.com/samosvalishe/free-turn-proxy/internal/logx"
	"github.com/samosvalishe/free-turn-proxy/internal/uri"
)

var logHolder logx.Holder

func SetLogger(l logx.Logger) { logHolder.Set(l) }

func log() logx.Logger { return logHolder.Get() }

// Sub представляет структуру подписки на серверы.
type Sub struct {
	Name      string
	Update    string
	Refresh   string
	Color     string
	Icon      string
	Used      string
	Available string
	Nodes     []Node
}

// Node представляет узел сервера из подписки.
type Node struct {
	URI       *uri.Config
	Name      string
	Color     string
	Icon      string
	Used      string
	Available string
	IP        string
	Comment   string
}

func Fetch(ctx context.Context, url string) (*Sub, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	dialer := dnsdial.AppDialer(dnsdial.DNSModeAuto)
	// Клон DefaultTransport, а не пустой: иначе теряются системный прокси и таймауты TLS.
	tr := &http.Transport{}
	if dt, ok := http.DefaultTransport.(*http.Transport); ok {
		tr = dt.Clone()
	}
	tr.DialContext = dialer.DialContext
	client := &http.Client{Transport: tr}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return Parse(resp.Body)
}

func Parse(r io.Reader) (*Sub, error) {
	s := &Sub{}
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case strings.HasPrefix(line, "##"):
			if key, val, ok := metaField(line, "##"); ok && len(s.Nodes) > 0 {
				s.Nodes[len(s.Nodes)-1].setMeta(key, val)
			}
		case strings.HasPrefix(line, "#"):
			if key, val, ok := metaField(line, "#"); ok {
				s.setMeta(key, val)
			}
		case strings.HasPrefix(line, "freeturn://"):
			cfg, err := uri.Parse(line)
			if err != nil {
				log().Warnf("[Sub] skipped invalid freeturn URI: %v", err)
				continue
			}
			s.Nodes = append(s.Nodes, Node{URI: cfg})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return s, nil
}

func metaField(line, prefix string) (key, val string, ok bool) {
	key, val, ok = strings.Cut(strings.TrimPrefix(line, prefix), ":")
	return strings.TrimSpace(key), strings.TrimSpace(val), ok
}

func (s *Sub) setMeta(key, val string) {
	switch key {
	case "name":
		s.Name = val
	case "update":
		s.Update = val
	case "refresh":
		s.Refresh = val
	case "color":
		s.Color = val
	case "icon":
		s.Icon = val
	case "used":
		s.Used = val
	case "available":
		s.Available = val
	}
}

func (n *Node) setMeta(key, val string) {
	switch key {
	case "name":
		n.Name = val
	case "color":
		n.Color = val
	case "icon":
		n.Icon = val
	case "used":
		n.Used = val
	case "available":
		n.Available = val
	case "ip":
		n.IP = val
	case "comment":
		n.Comment = val
	}
}
