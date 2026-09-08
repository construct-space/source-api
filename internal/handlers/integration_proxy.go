// integration_proxy.go — reverse-proxy for /api/integration/* to the
// integration-api service. Auth middleware has already validated the
// caller and stamped X-User-ID; we re-sign as a peer service (the
// integration-api expects X-Internal-Secret + X-Auth-User-ID).
//
// The browser never sees the internal secret. The space-mail frontend
// can hit https://api.lisaos.dev/api/integration/google/start
// like any other route.
package handlers

import (
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

// integrationTransport is a single shared transport for proxying to
// integration-api. Idle conns are capped at 30s — CapRover's nginx
// closes idle upstream sockets around 60s, and reusing one of those
// dead sockets is what surfaces as ECONNRESET at the browser. 30s
// keeps the pool fresh while still amortizing TLS across rapid bursts
// (e.g. the inbox prefetching thread bodies in parallel).
var integrationTransport = &http.Transport{
	Proxy: http.ProxyFromEnvironment,
	DialContext: (&net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext,
	MaxIdleConns:          50,
	MaxIdleConnsPerHost:   20,
	IdleConnTimeout:       30 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ExpectContinueTimeout: 1 * time.Second,
	ResponseHeaderTimeout: 60 * time.Second,
	ForceAttemptHTTP2:     true,
}

// IntegrationProxy forwards the request to Cfg.IntegrationURL with
// the same path + query. Adds the internal-auth headers so the target
// trusts us. Strips Authorization (we authenticated upstream).
func IntegrationProxy(w http.ResponseWriter, r *http.Request) {
	if Cfg == nil || Cfg.IntegrationURL == "" {
		WriteJSON(w, 503, map[string]any{"error": "integration backend not configured"})
		return
	}
	target, err := url.Parse(Cfg.IntegrationURL)
	if err != nil {
		WriteJSON(w, 503, map[string]any{"error": "bad integration url: " + err.Error()})
		return
	}
	if Cfg.ServiceAPIKey == "" {
		WriteJSON(w, 503, map[string]any{"error": "service secret not configured"})
		return
	}
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		WriteJSON(w, 401, map[string]any{"error": "unauthenticated"})
		return
	}

	rp := httputil.NewSingleHostReverseProxy(target)
	rp.Transport = integrationTransport
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("[integration-proxy] %s %s upstream err: %v", r.Method, r.URL.Path, err)
		WriteJSON(w, http.StatusBadGateway, map[string]any{
			"error":  "integration upstream unavailable",
			"detail": err.Error(),
		})
	}
	origDirector := rp.Director
	rp.Director = func(req *http.Request) {
		origDirector(req)
		req.Host = target.Host
		// Strip caller's bearer; we're talking to a peer service now.
		req.Header.Del("Authorization")
		req.Header.Del("Cookie")
		req.Header.Set("X-Internal-Secret", Cfg.ServiceAPIKey)
		req.Header.Set("X-Auth-User-ID", userID)
		if email := r.Header.Get("X-User-Email"); email != "" {
			req.Header.Set("X-Auth-User-Email", email)
		}
		if orgID := r.Header.Get("X-Auth-Org-ID"); orgID != "" {
			req.Header.Set("X-Auth-Org-ID", orgID)
		}
	}
	rp.ServeHTTP(w, r)
}
