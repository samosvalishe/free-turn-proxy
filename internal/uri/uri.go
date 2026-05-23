package uri

import (
	"errors"
	"net/url"
	"strings"
)

// Config представляет разобранную share-ссылку freeturn://
type Config struct {
	Provider   string
	Transport  string
	Mode       string
	Bond       bool
	Auth       bool
	ObfProfile string
	ObfKey     string
	Peer       string
	Comment    string
}

// Parse разбирает строку freeturn://...
// Формат: freeturn://<Provider>?<Transport><mode=...&obf-profile=...&bond=true>@<Peer>#<ObfKey>$<Comment>
func Parse(s string) (*Config, error) {
	if !strings.HasPrefix(s, "freeturn://") {
		return nil, errors.New("invalid scheme, expected freeturn://")
	}

	s = strings.TrimPrefix(s, "freeturn://")
	cfg := &Config{}

	// 1. Извлекаем Comment (после $)
	if idx := strings.Index(s, "$"); idx != -1 {
		cfg.Comment = s[idx+1:]
		s = s[:idx]
	}

	// 2. Извлекаем ObfKey (после #)
	if idx := strings.Index(s, "#"); idx != -1 {
		cfg.ObfKey = s[idx+1:]
		s = s[:idx]
	}

	// 3. Извлекаем Peer (после @)
	if idx := strings.LastIndex(s, "@"); idx != -1 {
		cfg.Peer = s[idx+1:]
		s = s[:idx]
	}

	// Оставшаяся часть: <Provider>?<Transport><key=val&...>
	parts := strings.SplitN(s, "?", 2)
	if len(parts) == 0 || parts[0] == "" {
		return nil, errors.New("missing provider")
	}
	cfg.Provider = parts[0]

	if len(parts) == 2 {
		transportPart := parts[1]

		// Извлекаем блок <...>
		if startIdx := strings.Index(transportPart, "<"); startIdx != -1 {
			if endIdx := strings.Index(transportPart, ">"); endIdx != -1 && endIdx > startIdx {
				payload := transportPart[startIdx+1 : endIdx]
				cfg.Transport = transportPart[:startIdx]

				// Парсим query-like параметры внутри < >
				vals, err := url.ParseQuery(payload)
				if err != nil {
					return nil, err
				}
				cfg.Mode = vals.Get("mode")
				cfg.ObfProfile = vals.Get("obf-profile")
				cfg.Bond = vals.Get("bond") == "true" || vals.Get("bond") == "1"
				cfg.Auth = vals.Get("auth") == "true" || vals.Get("auth") == "1"
			} else {
				cfg.Transport = transportPart
			}
		} else {
			cfg.Transport = transportPart
		}
	}

	return cfg, nil
}

// String генерирует URI из конфигурации
func (c *Config) String() string {
	var payload []string
	if c.Mode != "" {
		payload = append(payload, "mode="+url.QueryEscape(c.Mode))
	}
	if c.ObfProfile != "" {
		payload = append(payload, "obf-profile="+url.QueryEscape(c.ObfProfile))
	}
	if c.Bond {
		payload = append(payload, "bond=1")
	}
	if c.Auth {
		payload = append(payload, "auth=1")
	}

	transportStr := c.Transport
	if len(payload) > 0 {
		transportStr += "<" + strings.Join(payload, "&") + ">"
	}

	res := "freeturn://" + c.Provider
	if transportStr != "" {
		res += "?" + transportStr
	}
	if c.Peer != "" {
		res += "@" + c.Peer
	}
	if c.ObfKey != "" {
		res += "#" + c.ObfKey
	}
	if c.Comment != "" {
		res += "$" + c.Comment
	}

	return res
}
