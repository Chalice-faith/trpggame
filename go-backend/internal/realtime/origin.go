package realtime

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

var ErrInvalidAllowedOrigins = errors.New("invalid websocket allowed origins")

// OriginSet 是初始化后只读的 WebSocket Origin 白名单。
type OriginSet struct {
	allowed map[string]struct{}
}

// ParseAllowedOrigins 解析逗号分隔的完整 HTTP(S) Origin。
func ParseAllowedOrigins(raw string) (*OriginSet, error) {
	allowed := make(map[string]struct{})
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		origin, err := normalizeOrigin(item)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidAllowedOrigins, err)
		}
		allowed[origin] = struct{}{}
	}
	if len(allowed) == 0 {
		return nil, fmt.Errorf("%w: at least one origin is required", ErrInvalidAllowedOrigins)
	}
	return &OriginSet{allowed: allowed}, nil
}

// Allows 判断请求的 Origin 是否允许。非浏览器客户端不携带 Origin 时允许继续 JWT 鉴权。
func (set *OriginSet) Allows(request *http.Request) bool {
	if set == nil || request == nil {
		return false
	}
	values := request.Header.Values("Origin")
	if len(values) == 0 {
		return true
	}
	if len(values) != 1 || strings.Contains(values[0], ",") {
		return false
	}
	origin, err := normalizeOrigin(strings.TrimSpace(values[0]))
	if err != nil {
		return false
	}
	_, ok := set.allowed[origin]
	return ok
}

func normalizeOrigin(raw string) (string, error) {
	if raw == "" || raw == "*" || strings.EqualFold(raw, "null") {
		return "", fmt.Errorf("origin %q is not allowed", raw)
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse origin: %w", err)
	}
	if parsed.Opaque != "" || parsed.User != nil || parsed.Host == "" {
		return "", errors.New("origin must contain only scheme and host")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", errors.New("origin scheme must be http or https")
	}
	if (parsed.Path != "" && parsed.Path != "/") || parsed.RawPath != "" ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", errors.New("origin must not contain path, query, or fragment")
	}

	hostname := strings.ToLower(parsed.Hostname())
	if hostname == "" {
		return "", errors.New("origin hostname is required")
	}
	if strings.HasSuffix(parsed.Host, ":") {
		return "", errors.New("origin port must not be empty")
	}
	port := parsed.Port()
	if port != "" {
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return "", errors.New("origin port is invalid")
		}
	}
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	host := hostname
	if strings.Contains(hostname, ":") {
		host = "[" + hostname + "]"
	}
	if port != "" {
		host += ":" + port
	}
	return scheme + "://" + host, nil
}
