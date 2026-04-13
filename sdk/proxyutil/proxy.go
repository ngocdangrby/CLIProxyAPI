package proxyutil

import (
	"context"
	"fmt"
	mrand "math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/sirupsen/logrus"
	"golang.org/x/net/proxy"
)

// Mode describes how a proxy setting should be interpreted.
type Mode int

const (
	// ModeInherit means no explicit proxy behavior was configured.
	ModeInherit Mode = iota
	// ModeDirect means outbound requests must bypass proxies explicitly.
	ModeDirect
	// ModeProxy means a concrete proxy URL was configured.
	ModeProxy
	// ModeInvalid means the proxy setting is present but malformed or unsupported.
	ModeInvalid
)

// Setting is the normalized interpretation of a proxy configuration value.
type Setting struct {
	Raw  string
	Mode Mode
	URL  *url.URL
}

// Parse normalizes a proxy configuration value into inherit, direct, or proxy modes.
func Parse(raw string) (Setting, error) {
	trimmed := strings.TrimSpace(raw)
	setting := Setting{Raw: trimmed}

	if trimmed == "" {
		setting.Mode = ModeInherit
		return setting, nil
	}

	if strings.EqualFold(trimmed, "direct") || strings.EqualFold(trimmed, "none") {
		setting.Mode = ModeDirect
		return setting, nil
	}

	parsedURL, errParse := url.Parse(trimmed)
	if errParse != nil {
		setting.Mode = ModeInvalid
		return setting, fmt.Errorf("parse proxy URL failed: %w", errParse)
	}
	if parsedURL.Scheme == "" || parsedURL.Host == "" {
		setting.Mode = ModeInvalid
		return setting, fmt.Errorf("proxy URL missing scheme/host")
	}

	switch parsedURL.Scheme {
	case "socks5", "socks5h", "http", "https":
		setting.Mode = ModeProxy
		setting.URL = parsedURL
		return setting, nil
	default:
		setting.Mode = ModeInvalid
		return setting, fmt.Errorf("unsupported proxy scheme: %s", parsedURL.Scheme)
	}
}

func cloneDefaultTransport() *http.Transport {
	if transport, ok := http.DefaultTransport.(*http.Transport); ok && transport != nil {
		return transport.Clone()
	}
	return &http.Transport{}
}

// NewDirectTransport returns a transport that bypasses environment proxies.
func NewDirectTransport() *http.Transport {
	clone := cloneDefaultTransport()
	clone.Proxy = nil
	return clone
}

// BuildHTTPTransport constructs an HTTP transport for the provided proxy setting.
func BuildHTTPTransport(raw string) (*http.Transport, Mode, error) {
	setting, errParse := Parse(raw)
	if errParse != nil {
		return nil, setting.Mode, errParse
	}

	switch setting.Mode {
	case ModeInherit:
		return nil, setting.Mode, nil
	case ModeDirect:
		return NewDirectTransport(), setting.Mode, nil
	case ModeProxy:
		if setting.URL.Scheme == "socks5" || setting.URL.Scheme == "socks5h" {
			var proxyAuth *proxy.Auth
			if setting.URL.User != nil {
				username := setting.URL.User.Username()
				password, _ := setting.URL.User.Password()
				proxyAuth = &proxy.Auth{User: username, Password: password}
			}
			dialer, errSOCKS5 := proxy.SOCKS5("tcp", setting.URL.Host, proxyAuth, proxy.Direct)
			if errSOCKS5 != nil {
				return nil, setting.Mode, fmt.Errorf("create SOCKS5 dialer failed: %w", errSOCKS5)
			}
			transport := cloneDefaultTransport()
			transport.Proxy = nil
			transport.DialContext = func(_ context.Context, network, addr string) (net.Conn, error) {
				return dialer.Dial(network, addr)
			}
			return transport, setting.Mode, nil
		}
		transport := cloneDefaultTransport()
		transport.Proxy = http.ProxyURL(setting.URL)
		return transport, setting.Mode, nil
	default:
		return nil, setting.Mode, nil
	}
}

// RoundRobinProxy is a proxy selector that cycles through a list of proxy URLs.
// It supports "direct" (no proxy) as one of the options in the rotation.
type RoundRobinProxy struct {
	proxies []string
	mu      sync.Mutex
	cursor  int
}

// NewRoundRobinProxy creates a new round-robin proxy selector from a "||"-separated string.
// If includeNoProxy is true, "direct" is appended as the last option in the rotation.
func NewRoundRobinProxy(rawList string, includeNoProxy bool) (*RoundRobinProxy, error) {
	rawList = strings.TrimSpace(rawList)
	if rawList == "" {
		return nil, fmt.Errorf("proxy list is empty")
	}

	parts := strings.Split(rawList, "||")
	proxies := make([]string, 0, len(parts)+1) // +1 for optional "direct"
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// Validate each proxy
		if _, err := Parse(p); err != nil {
			return nil, fmt.Errorf("invalid proxy in list: %s: %w", p, err)
		}
		proxies = append(proxies, p)
	}

	if len(proxies) == 0 {
		return nil, fmt.Errorf("no valid proxies in list")
	}

	rr := &RoundRobinProxy{
		proxies: proxies,
		cursor:  0,
	}

	if includeNoProxy {
		rr.proxies = append(rr.proxies, "direct")
	}

	// Shuffle initial cursor to randomize starting point
	rr.cursor = mrand.IntN(len(rr.proxies))

	return rr, nil
}

// Next returns the next proxy URL in the rotation.
func (r *RoundRobinProxy) Next() string {
	r.mu.Lock()
	defer r.mu.Unlock()

	idx := r.cursor
	r.cursor = (r.cursor + 1) % len(r.proxies)
	proxyURL := r.proxies[idx]
	logrus.Debugf("round-robin proxy using: %s", proxyURL)
	return proxyURL
}

// BuildHTTPTransportWithRoundRobin builds an HTTP transport using a round-robin proxy selector.
func BuildHTTPTransportWithRoundRobin(rawList string, includeNoProxy bool) (*http.Transport, *RoundRobinProxy, error) {
	rr, err := NewRoundRobinProxy(rawList, includeNoProxy)
	if err != nil {
		return nil, nil, err
	}

	// Get first proxy to build initial transport
	proxyURL := rr.Next()
	transport, mode, err := BuildHTTPTransport(proxyURL)
	if err != nil {
		return nil, rr, err
	}
	// If first proxy is direct, mode will be ModeDirect
	_ = mode

	return transport, rr, nil
}

// BuildDialerWithRoundRobin constructs a proxy dialer using a round-robin proxy selector.
func BuildDialerWithRoundRobin(rawList string, includeNoProxy bool) (proxy.Dialer, *RoundRobinProxy, error) {
	rr, err := NewRoundRobinProxy(rawList, includeNoProxy)
	if err != nil {
		return nil, nil, err
	}

	proxyURL := rr.Next()
	dialer, mode, err := BuildDialer(proxyURL)
	if err != nil {
		return nil, rr, err
	}
	_ = mode

	return dialer, rr, nil
}
func BuildDialer(raw string) (proxy.Dialer, Mode, error) {
	setting, errParse := Parse(raw)
	if errParse != nil {
		return nil, setting.Mode, errParse
	}

	switch setting.Mode {
	case ModeInherit:
		return nil, setting.Mode, nil
	case ModeDirect:
		return proxy.Direct, setting.Mode, nil
	case ModeProxy:
		dialer, errDialer := proxy.FromURL(setting.URL, proxy.Direct)
		if errDialer != nil {
			return nil, setting.Mode, fmt.Errorf("create proxy dialer failed: %w", errDialer)
		}
		return dialer, setting.Mode, nil
	default:
		return nil, setting.Mode, nil
	}
}
