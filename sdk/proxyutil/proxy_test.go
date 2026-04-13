package proxyutil

import (
	"net/http"
	"testing"
)

func mustDefaultTransport(t *testing.T) *http.Transport {
	t.Helper()

	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok || transport == nil {
		t.Fatal("http.DefaultTransport is not an *http.Transport")
	}
	return transport
}

func TestParse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    Mode
		wantErr bool
	}{
		{name: "inherit", input: "", want: ModeInherit},
		{name: "direct", input: "direct", want: ModeDirect},
		{name: "none", input: "none", want: ModeDirect},
		{name: "http", input: "http://proxy.example.com:8080", want: ModeProxy},
		{name: "https", input: "https://proxy.example.com:8443", want: ModeProxy},
		{name: "socks5", input: "socks5://proxy.example.com:1080", want: ModeProxy},
		{name: "socks5h", input: "socks5h://proxy.example.com:1080", want: ModeProxy},
		{name: "invalid", input: "bad-value", want: ModeInvalid, wantErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			setting, errParse := Parse(tt.input)
			if tt.wantErr && errParse == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && errParse != nil {
				t.Fatalf("unexpected error: %v", errParse)
			}
			if setting.Mode != tt.want {
				t.Fatalf("mode = %d, want %d", setting.Mode, tt.want)
			}
		})
	}
}

func TestBuildHTTPTransportDirectBypassesProxy(t *testing.T) {
	t.Parallel()

	transport, mode, errBuild := BuildHTTPTransport("direct")
	if errBuild != nil {
		t.Fatalf("BuildHTTPTransport returned error: %v", errBuild)
	}
	if mode != ModeDirect {
		t.Fatalf("mode = %d, want %d", mode, ModeDirect)
	}
	if transport == nil {
		t.Fatal("expected transport, got nil")
	}
	if transport.Proxy != nil {
		t.Fatal("expected direct transport to disable proxy function")
	}
}

func TestBuildHTTPTransportHTTPProxy(t *testing.T) {
	t.Parallel()

	transport, mode, errBuild := BuildHTTPTransport("http://proxy.example.com:8080")
	if errBuild != nil {
		t.Fatalf("BuildHTTPTransport returned error: %v", errBuild)
	}
	if mode != ModeProxy {
		t.Fatalf("mode = %d, want %d", mode, ModeProxy)
	}
	if transport == nil {
		t.Fatal("expected transport, got nil")
	}

	req, errRequest := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if errRequest != nil {
		t.Fatalf("http.NewRequest returned error: %v", errRequest)
	}

	proxyURL, errProxy := transport.Proxy(req)
	if errProxy != nil {
		t.Fatalf("transport.Proxy returned error: %v", errProxy)
	}
	if proxyURL == nil || proxyURL.String() != "http://proxy.example.com:8080" {
		t.Fatalf("proxy URL = %v, want http://proxy.example.com:8080", proxyURL)
	}

	defaultTransport := mustDefaultTransport(t)
	if transport.ForceAttemptHTTP2 != defaultTransport.ForceAttemptHTTP2 {
		t.Fatalf("ForceAttemptHTTP2 = %v, want %v", transport.ForceAttemptHTTP2, defaultTransport.ForceAttemptHTTP2)
	}
	if transport.IdleConnTimeout != defaultTransport.IdleConnTimeout {
		t.Fatalf("IdleConnTimeout = %v, want %v", transport.IdleConnTimeout, defaultTransport.IdleConnTimeout)
	}
	if transport.TLSHandshakeTimeout != defaultTransport.TLSHandshakeTimeout {
		t.Fatalf("TLSHandshakeTimeout = %v, want %v", transport.TLSHandshakeTimeout, defaultTransport.TLSHandshakeTimeout)
	}
}

func TestBuildHTTPTransportSOCKS5ProxyInheritsDefaultTransportSettings(t *testing.T) {
	t.Parallel()

	transport, mode, errBuild := BuildHTTPTransport("socks5://proxy.example.com:1080")
	if errBuild != nil {
		t.Fatalf("BuildHTTPTransport returned error: %v", errBuild)
	}
	if mode != ModeProxy {
		t.Fatalf("mode = %d, want %d", mode, ModeProxy)
	}
	if transport == nil {
		t.Fatal("expected transport, got nil")
	}
	if transport.Proxy != nil {
		t.Fatal("expected SOCKS5 transport to bypass http proxy function")
	}

	defaultTransport := mustDefaultTransport(t)
	if transport.ForceAttemptHTTP2 != defaultTransport.ForceAttemptHTTP2 {
		t.Fatalf("ForceAttemptHTTP2 = %v, want %v", transport.ForceAttemptHTTP2, defaultTransport.ForceAttemptHTTP2)
	}
	if transport.IdleConnTimeout != defaultTransport.IdleConnTimeout {
		t.Fatalf("IdleConnTimeout = %v, want %v", transport.IdleConnTimeout, defaultTransport.IdleConnTimeout)
	}
	if transport.TLSHandshakeTimeout != defaultTransport.TLSHandshakeTimeout {
		t.Fatalf("TLSHandshakeTimeout = %v, want %v", transport.TLSHandshakeTimeout, defaultTransport.TLSHandshakeTimeout)
	}
}

func TestBuildHTTPTransportSOCKS5HProxy(t *testing.T) {
	t.Parallel()

	transport, mode, errBuild := BuildHTTPTransport("socks5h://proxy.example.com:1080")
	if errBuild != nil {
		t.Fatalf("BuildHTTPTransport returned error: %v", errBuild)
	}
	if mode != ModeProxy {
		t.Fatalf("mode = %d, want %d", mode, ModeProxy)
	}
	if transport == nil {
		t.Fatal("expected transport, got nil")
	}
	if transport.Proxy != nil {
		t.Fatal("expected SOCKS5H transport to bypass http proxy function")
	}
	if transport.DialContext == nil {
		t.Fatal("expected SOCKS5H transport to have custom DialContext")
	}
}

func TestNewRoundRobinProxy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		rawList       string
		includeNoProxy bool
		wantCount     int
		wantErr       bool
	}{
		{
			name:      "two proxies",
			rawList:   "socks5://proxy1:1080||http://proxy2:8080",
			wantCount: 2,
			wantErr:   false,
		},
		{
			name:      "three proxies with spaces",
			rawList:   "  socks5://proxy1:1080  ||  http://proxy2:8080  ||  https://proxy3:8443  ",
			wantCount: 3,
			wantErr:   false,
		},
		{
			name:          "three proxies with no proxy",
			rawList:       "socks5://proxy1:1080||http://proxy2:8080||direct",
			includeNoProxy: true,
			wantCount:     4, // 3 proxies + direct
			wantErr:       false,
		},
		{
			name:      "invalid proxy in list",
			rawList:   "socks5://proxy1:1080||invalid-url",
			wantCount: 0,
			wantErr:   true,
		},
		{
			name:      "empty string",
			rawList:   "",
			wantCount: 0,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rr, err := NewRoundRobinProxy(tt.rawList, tt.includeNoProxy)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(rr.proxies) != tt.wantCount {
				t.Fatalf("proxy count = %d, want %d", len(rr.proxies), tt.wantCount)
			}
		})
	}
}

func TestRoundRobinProxyNext(t *testing.T) {
	t.Parallel()

	rr, err := NewRoundRobinProxy("socks5://proxy1:1080||http://proxy2:8080||https://proxy3:8443", false)
	if err != nil {
		t.Fatalf("NewRoundRobinProxy returned error: %v", err)
	}

	seen := make(map[string]bool)
	// Call Next() multiple times and verify we cycle through all proxies
	for i := 0; i < 10; i++ {
		proxy := rr.Next()
		if proxy == "" {
			t.Fatal("Next() returned empty string")
		}
		seen[proxy] = true
	}

	// Should have seen all 3 proxies
	if len(seen) != 3 {
		t.Fatalf("expected 3 different proxies seen, got %d: %v", len(seen), seen)
	}
}

func TestRoundRobinProxyNextWithNoProxy(t *testing.T) {
	t.Parallel()

	rr, err := NewRoundRobinProxy("socks5://proxy1:1080||http://proxy2:8080", true)
	if err != nil {
		t.Fatalf("NewRoundRobinProxy returned error: %v", err)
	}

	seen := make(map[string]bool)
	// Call Next() multiple times and verify we cycle through all proxies including direct
	for i := 0; i < 12; i++ {
		proxy := rr.Next()
		seen[proxy] = true
	}

	// Should have seen all 3 proxies (2 proxies + direct)
	if len(seen) != 3 {
		t.Fatalf("expected 3 different proxies seen (2 + direct), got %d: %v", len(seen), seen)
	}
	if !seen["direct"] {
		t.Fatal("expected 'direct' to be in rotation")
	}
}
