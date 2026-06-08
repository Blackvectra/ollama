package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestToAnthropic(t *testing.T) {
	req := openAIChatRequest{
		Model:  "claude-opus-4-8",
		Stream: true,
		Messages: []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}{
			{Role: "system", Content: "be brief"},
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "hello"},
			{Role: "user", Content: "bye"},
		},
	}
	out := toAnthropic(req)
	if out.System != "be brief" {
		t.Errorf("system = %q, want %q", out.System, "be brief")
	}
	if len(out.Messages) != 3 {
		t.Fatalf("messages = %d, want 3 (system extracted)", len(out.Messages))
	}
	if out.Messages[0].Role != "user" || out.Messages[0].Content != "hi" {
		t.Errorf("unexpected first message: %+v", out.Messages[0])
	}
	if out.MaxTokens != defaultProviderMaxTokens {
		t.Errorf("max_tokens = %d, want default %d", out.MaxTokens, defaultProviderMaxTokens)
	}
}

func TestProvidersHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := Server{}

	// No key configured -> empty providers.
	t.Setenv("ANTHROPIC_API_KEY", "")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/providers", nil)
	s.ProvidersHandler(c)
	if !strings.Contains(w.Body.String(), `"providers":[]`) {
		t.Errorf("expected empty providers, got %s", w.Body.String())
	}

	// Key set -> anthropic provider with models.
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/providers", nil)
	s.ProvidersHandler(c)
	if !strings.Contains(w.Body.String(), "anthropic") || !strings.Contains(w.Body.String(), "claude-opus-4-8") {
		t.Errorf("expected anthropic provider with opus, got %s", w.Body.String())
	}
}

func TestAnthropicChatStreamTranslation(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Fake Anthropic upstream emitting Messages SSE.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") == "" || r.Header.Get("anthropic-version") == "" {
			t.Errorf("missing anthropic auth/version headers")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"Hel\"}}\n\n"))
		w.Write([]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"lo\"}}\n\n"))
		w.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer upstream.Close()

	old := anthropicBaseURL
	anthropicBaseURL = upstream.URL
	defer func() { anthropicBaseURL = old }()

	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	s := Server{}
	body, _ := json.Marshal(openAIChatRequest{Model: "claude-opus-4-8", Stream: true})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/providers/chat/completions", strings.NewReader(string(body)))
	c.Request.Header.Set("Content-Type", "application/json")

	s.ProviderChatHandler(c)

	out := w.Body.String()
	if !strings.Contains(out, `"content":"Hel"`) || !strings.Contains(out, `"content":"lo"`) {
		t.Errorf("translated stream missing text chunks:\n%s", out)
	}
	if !strings.Contains(out, "data: [DONE]") {
		t.Errorf("translated stream missing [DONE]:\n%s", out)
	}
}

func TestProviderChatUnconfigured(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("ANTHROPIC_API_KEY", "")

	s := Server{}
	body, _ := json.Marshal(openAIChatRequest{Model: "claude-opus-4-8", Stream: true})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/providers/chat/completions", strings.NewReader(string(body)))
	c.Request.Header.Set("Content-Type", "application/json")

	s.ProviderChatHandler(c)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("unconfigured provider: got %d, want 503", w.Code)
	}
}

func TestProviderChatUnknownModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := Server{}
	body, _ := json.Marshal(openAIChatRequest{Model: "llama3", Stream: true})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/providers/chat/completions", strings.NewReader(string(body)))
	c.Request.Header.Set("Content-Type", "application/json")

	s.ProviderChatHandler(c)
	if w.Code != http.StatusBadRequest {
		t.Errorf("unknown provider model: got %d, want 400", w.Code)
	}
}

func TestProviderChatRejectsOversizedBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	s := Server{}
	// Body larger than maxProviderBodyBytes; MaxBytesReader should make the
	// JSON bind fail with a 400 rather than buffering it all into memory.
	big := `{"model":"claude-opus-4-8","messages":[{"role":"user","content":"` +
		strings.Repeat("A", maxProviderBodyBytes+1024) + `"}]}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/providers/chat/completions", strings.NewReader(big))
	c.Request.Header.Set("Content-Type", "application/json")

	s.ProviderChatHandler(c)
	if w.Code != http.StatusBadRequest {
		t.Errorf("oversized body: got %d, want 400", w.Code)
	}
}
