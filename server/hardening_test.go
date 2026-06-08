package server

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestEnforceSecureBinding(t *testing.T) {
	lo := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 11434}
	lan := &net.TCPAddr{IP: net.ParseIP("192.168.1.50"), Port: 11434}

	cases := []struct {
		name          string
		addr          net.Addr
		key           string
		allowInsecure bool
		wantErr       bool
	}{
		{"nil addr", nil, "", false, false},
		{"loopback no key", lo, "", false, false},
		{"lan with key", lan, "secret", false, false},
		{"lan no key blocked", lan, "", false, true},
		{"lan no key but override", lan, "", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := enforceSecureBinding(tc.addr, tc.key, tc.allowInsecure)
			if (err != nil) != tc.wantErr {
				t.Fatalf("enforceSecureBinding err=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestRateLimiter(t *testing.T) {
	rl := newRateLimiter(2)
	now := time.Now()

	if !rl.allow("1.2.3.4", now) || !rl.allow("1.2.3.4", now) {
		t.Fatal("first two requests should be allowed")
	}
	if rl.allow("1.2.3.4", now) {
		t.Fatal("third request in the same instant should be blocked")
	}
	// A different client is independent.
	if !rl.allow("5.6.7.8", now) {
		t.Fatal("other client should be allowed")
	}
	// After a minute the bucket refills.
	if !rl.allow("1.2.3.4", now.Add(time.Minute)) {
		t.Fatal("request after refill should be allowed")
	}
}

func TestChatUICSPNonce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := Server{}
	h, err := s.GenerateRoutes(nil)
	if err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/chat", nil)
	h.ServeHTTP(w, req)

	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'nonce-") {
		t.Fatalf("expected nonce-based script-src in CSP, got %q", csp)
	}
	body := w.Body.String()
	if strings.Contains(body, "__CSP_NONCE__") {
		t.Fatal("CSP nonce placeholder was not substituted")
	}
	if !strings.Contains(body, "<script nonce=") {
		t.Fatal("served page is missing the nonced script tag")
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing X-Content-Type-Options header")
	}
}
