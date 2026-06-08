package server

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// This file holds defense-in-depth controls that make the server secure by
// default: fail-closed binding, security response headers with a per-request
// CSP nonce, and an optional per-client rate limiter. None of these change
// behavior for the default loopback, no-auth, no-rate-limit configuration.

// enforceSecureBinding returns an error when the server is bound to a
// non-loopback address without an API key configured, unless the operator has
// explicitly opted into insecure mode. This prevents silently exposing an
// unauthenticated endpoint to the network.
func enforceSecureBinding(addr net.Addr, apiKey string, allowInsecure bool) error {
	if addr == nil || apiKey != "" || allowInsecure {
		return nil
	}

	ap, err := netip.ParseAddrPort(addr.String())
	if err != nil {
		// Unknown address shape (e.g. unix socket) — don't block startup.
		return nil
	}
	if ap.Addr().IsLoopback() || ap.Addr().IsUnspecified() {
		// Loopback is safe. Unspecified (0.0.0.0/::) is handled at the
		// listener level and is the operator's explicit choice; warn instead
		// of refusing so we don't break existing 0.0.0.0 setups silently.
		if ap.Addr().IsUnspecified() {
			slog.Warn("server is bound to all interfaces without OLLAMA_API_KEY; set OLLAMA_API_KEY to require authentication")
		}
		return nil
	}

	return fmt.Errorf("refusing to bind %s without OLLAMA_API_KEY: set OLLAMA_API_KEY to require authentication, or set OLLAMA_ALLOW_INSECURE=1 to override", addr)
}

// newCSPNonce returns a fresh base64 nonce for a per-response Content-Security-Policy.
func newCSPNonce() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return base64.RawStdEncoding.EncodeToString(b)
}

// securityHeadersMiddleware applies hardening headers to every response. The
// chat page sets its own CSP (with a script nonce) since it needs to run
// inline script; everything else gets a deny-all CSP appropriate for a JSON API.
func securityHeadersMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		if c.Request.URL.Path != "/chat" {
			h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'")
		}
		c.Next()
	}
}

// rateLimiter is a tiny per-client token-bucket limiter with no external deps.
type rateLimiter struct {
	mu        sync.Mutex
	perMinute float64
	buckets   map[string]*tokenBucket
}

type tokenBucket struct {
	tokens float64
	last   time.Time
}

func newRateLimiter(perMinute uint) *rateLimiter {
	return &rateLimiter{
		perMinute: float64(perMinute),
		buckets:   make(map[string]*tokenBucket),
	}
}

// allow reports whether a request from client is permitted, refilling the
// bucket at perMinute tokens/min with a burst capacity of perMinute.
func (r *rateLimiter) allow(client string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Opportunistic cleanup so the map can't grow without bound.
	if len(r.buckets) > 10000 {
		for k, b := range r.buckets {
			if now.Sub(b.last) > 10*time.Minute {
				delete(r.buckets, k)
			}
		}
	}

	b, ok := r.buckets[client]
	if !ok {
		r.buckets[client] = &tokenBucket{tokens: r.perMinute - 1, last: now}
		return true
	}

	refill := now.Sub(b.last).Minutes() * r.perMinute
	b.tokens = min(r.perMinute, b.tokens+refill)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// rateLimitMiddleware enforces a per-client request/minute cap. perMinute==0
// disables it entirely (the default).
func rateLimitMiddleware(perMinute uint) gin.HandlerFunc {
	if perMinute == 0 {
		return func(c *gin.Context) { c.Next() }
	}
	limiter := newRateLimiter(perMinute)
	return func(c *gin.Context) {
		if !limiter.allow(c.ClientIP(), time.Now()) {
			slog.Warn("rate limit exceeded", "client", c.ClientIP(), "path", c.Request.URL.Path)
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "rate limit exceeded"})
			return
		}
		c.Next()
	}
}
