package helps

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

// roundRobinProxyHolder holds a RoundRobinProxy and its mutex for thread-safe access.
type roundRobinProxyHolder struct {
	rr *proxyutil.RoundRobinProxy
	mu sync.Mutex
}

// RoundRobinProxies maps auth ID to its round-robin proxy selector.
var roundRobinProxies = make(map[string]*roundRobinProxyHolder)
var proxiesMu sync.RWMutex

// getOrCreateRoundRobinProxy retrieves or creates a round-robin proxy selector for an auth.
func getOrCreateRoundRobinProxy(auth *cliproxyauth.Auth, proxyURLs string, includeNoProxy bool) (*proxyutil.RoundRobinProxy, error) {
	if auth == nil {
		return nil, fmt.Errorf("auth is nil")
	}

	proxiesMu.RLock()
	holder, exists := roundRobinProxies[auth.ID]
	proxiesMu.RUnlock()

	if exists {
		return holder.rr, nil
	}

	proxiesMu.Lock()
	defer proxiesMu.Unlock()

	// Double-check after acquiring write lock
	if holder, exists = roundRobinProxies[auth.ID]; exists {
		return holder.rr, nil
	}

	rr, err := proxyutil.NewRoundRobinProxy(proxyURLs, includeNoProxy)
	if err != nil {
		return nil, err
	}

	holder = &roundRobinProxyHolder{rr: rr}
	roundRobinProxies[auth.ID] = holder
	return rr, nil
}

// NewProxyAwareHTTPClient creates an HTTP client with proper proxy configuration priority:
// 1. Use auth.ProxyURL if configured (highest priority)
// 2. Use cfg.ProxyURLs (round-robin) if auth proxy is not configured
// 3. Use cfg.ProxyURL if auth proxy is not configured
// 4. Use RoundTripper from context if neither are configured
//
// Parameters:
//   - ctx: The context containing optional RoundTripper
//   - cfg: The application configuration
//   - auth: The authentication information
//   - timeout: The client timeout (0 means no timeout)
//
// Returns:
//   - *http.Client: An HTTP client with configured proxy or transport
func NewProxyAwareHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	httpClient := &http.Client{}
	if timeout > 0 {
		httpClient.Timeout = timeout
	}

	// Priority 1: Use auth.ProxyURL if configured
	var proxyURL string
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}

	// Priority 2: Use cfg.ProxyURLs (round-robin) if auth proxy is not configured
	if proxyURL == "" && cfg != nil {
		proxyURLs := strings.TrimSpace(cfg.ProxyURLs)
		if proxyURLs != "" {
			// Use round-robin proxy
			rr, err := getOrCreateRoundRobinProxy(auth, proxyURLs, cfg.ProxyRoundRobinIncludeNoProxy)
			if err != nil {
				log.Debugf("failed to create round-robin proxy: %v", err)
			} else {
				nextProxy := rr.Next()
				transport := buildProxyTransport(nextProxy)
				if transport != nil {
					httpClient.Transport = transport
					return httpClient
				}
			}
		}
		// Fallback to single proxy URL
		proxyURL = strings.TrimSpace(cfg.ProxyURL)
	}

	// If we have a proxy URL configured, set up the transport
	if proxyURL != "" {
		transport := buildProxyTransport(proxyURL)
		if transport != nil {
			httpClient.Transport = transport
			return httpClient
		}
		// If proxy setup failed, log and fall through to context RoundTripper
		log.Debugf("failed to setup proxy from URL: %s, falling back to context transport", proxyURL)
	}

	// Priority 3: Use RoundTripper from context (typically from RoundTripperFor)
	if rt, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && rt != nil {
		httpClient.Transport = rt
	}

	return httpClient
}

// buildProxyTransport creates an HTTP transport configured for the given proxy URL.
// It supports SOCKS5, HTTP, and HTTPS proxy protocols.
//
// Parameters:
//   - proxyURL: The proxy URL string (e.g., "socks5://user:pass@host:port", "http://host:port")
//
// Returns:
//   - *http.Transport: A configured transport, or nil if the proxy URL is invalid
func buildProxyTransport(proxyURL string) *http.Transport {
	transport, _, errBuild := proxyutil.BuildHTTPTransport(proxyURL)
	if errBuild != nil {
		log.Errorf("%v", errBuild)
		return nil
	}
	return transport
}
