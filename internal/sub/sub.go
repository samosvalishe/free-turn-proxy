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
	tr, _ := http.DefaultTransport.(*http.Transport)
	tr = tr.Clone()
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

	var lastNode *Node

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "##") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				key := strings.TrimSpace(parts[0][1:])
				val := strings.TrimSpace(parts[1])
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
			continue
		}

		if strings.HasPrefix(line, "##") {
			if lastNode == nil {
				continue
			}
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				key := strings.TrimSpace(parts[0][2:])
				val := strings.TrimSpace(parts[1])
				switch key {
				case "name":
					lastNode.Name = val
				case "color":
					lastNode.Color = val
				case "icon":
					lastNode.Icon = val
				case "used":
					lastNode.Used = val
				case "available":
					lastNode.Available = val
				case "ip":
					lastNode.IP = val
				case "comment":
					lastNode.Comment = val
				}
			}
			continue
		}

		if strings.HasPrefix(line, "freeturn://") {
			cfg, err := uri.Parse(line)
			if err == nil {
				node := Node{URI: cfg}
				s.Nodes = append(s.Nodes, node)
				lastNode = &s.Nodes[len(s.Nodes)-1]
			} else {
				log().Warnf("[Sub] skipped invalid freeturn URI: %v", err)
			}
			continue
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return s, nil
}
