package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/ollama/ollama/envconfig"
)

// External (cloud) model providers, exposed alongside local models so frontier
// models like Claude Opus appear in the same chat. Provider API keys are read
// from the environment and stay server-side — they are never sent to browser
// clients. Each provider is enabled only when its key is configured.

// anthropicBaseURL is the Anthropic API base. It is a var so tests can point it
// at a local httptest server.
var anthropicBaseURL = "https://api.anthropic.com"

const anthropicVersion = "2023-06-01"

// anthropicModels are the Claude models surfaced when ANTHROPIC_API_KEY is set.
var anthropicModels = []string{
	"claude-opus-4-8",
	"claude-sonnet-4-6",
	"claude-haiku-4-5",
}

// defaultProviderMaxTokens caps the response when the client does not specify.
const defaultProviderMaxTokens = 4096

// maxProviderBodyBytes bounds the inbound request body to the proxy.
const maxProviderBodyBytes = 1 << 20 // 1 MiB

// maxUpstreamErrBytes bounds how much of an upstream error body we echo back,
// so a large/hostile upstream response can't be reflected verbatim.
const maxUpstreamErrBytes = 2048

type providerInfo struct {
	Name   string   `json:"name"`
	Models []string `json:"models"`
}

// ProvidersHandler reports which external providers are configured and the
// models they expose. Returns an empty list when none are configured.
func (s *Server) ProvidersHandler(c *gin.Context) {
	providers := []providerInfo{}
	if envconfig.AnthropicApiKey() != "" {
		providers = append(providers, providerInfo{Name: "anthropic", Models: anthropicModels})
	}
	c.JSON(http.StatusOK, gin.H{"providers": providers})
}

// openAIChatRequest is the subset of the OpenAI chat-completions request the
// proxy understands. The browser chat UI speaks this shape for both local and
// cloud models.
type openAIChatRequest struct {
	Model    string `json:"model"`
	Stream   bool   `json:"stream"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
	MaxTokens int `json:"max_tokens"`
}

// providerForModel maps a model id to its external provider, or "" if the model
// is not an external/cloud model.
func providerForModel(model string) string {
	if strings.HasPrefix(model, "claude") {
		return "anthropic"
	}
	return ""
}

// ProviderChatHandler proxies an OpenAI-style chat request to an external
// provider and streams the reply back in OpenAI SSE format, so the existing
// chat UI consumes local and cloud models identically.
func (s *Server) ProviderChatHandler(c *gin.Context) {
	// Cap the request body to guard against memory-exhaustion from oversized
	// payloads on this unauthenticated-by-default proxy path.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxProviderBodyBytes)

	var req openAIChatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	switch providerForModel(req.Model) {
	case "anthropic":
		s.anthropicChat(c, req)
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("model %q is not a known external provider model", req.Model)})
	}
}

// anthropicRequest is the Anthropic Messages API request body.
type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Stream    bool               `json:"stream"`
	Messages  []anthropicMessage `json:"messages"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// toAnthropic translates an OpenAI-style chat request into an Anthropic request.
// OpenAI system messages become Anthropic's top-level system field; user and
// assistant turns map directly. Note Opus 4.7/4.8 reject sampling params and
// require adaptive thinking, so we send neither temperature nor a fixed
// thinking budget.
func toAnthropic(req openAIChatRequest) anthropicRequest {
	out := anthropicRequest{
		Model:     req.Model,
		MaxTokens: req.MaxTokens,
		Stream:    req.Stream,
	}
	if out.MaxTokens <= 0 {
		out.MaxTokens = defaultProviderMaxTokens
	}
	var systems []string
	for _, m := range req.Messages {
		switch m.Role {
		case "system":
			systems = append(systems, m.Content)
		case "user", "assistant":
			out.Messages = append(out.Messages, anthropicMessage{Role: m.Role, Content: m.Content})
		}
	}
	out.System = strings.Join(systems, "\n\n")
	return out
}

func (s *Server) anthropicChat(c *gin.Context, req openAIChatRequest) {
	key := envconfig.AnthropicApiKey()
	if key == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Anthropic provider is not configured (set ANTHROPIC_API_KEY)"})
		return
	}

	body, err := json.Marshal(toAnthropic(req))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to build upstream request"})
		return
	}

	upstream, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, anthropicBaseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to build upstream request"})
		return
	}
	upstream.Header.Set("Content-Type", "application/json")
	upstream.Header.Set("x-api-key", key)
	upstream.Header.Set("anthropic-version", anthropicVersion)

	resp, err := http.DefaultClient.Do(upstream)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to reach Anthropic: " + err.Error()})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamErrBytes))
		c.JSON(resp.StatusCode, gin.H{"error": "anthropic error: " + strings.TrimSpace(string(msg))})
		return
	}

	if !req.Stream {
		anthropicNonStream(c, resp.Body, req.Model)
		return
	}
	anthropicStream(c, resp.Body, req.Model)
}

// anthropicStreamEvent is the slice of Anthropic SSE we care about.
type anthropicStreamEvent struct {
	Type  string `json:"type"`
	Delta struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"delta"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// anthropicStream reads Anthropic SSE and re-emits OpenAI-style SSE chunks so
// the browser UI's existing parser works unchanged.
func anthropicStream(c *gin.Context, upstream io.Reader, model string) {
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")

	flusher, _ := c.Writer.(http.Flusher)
	writeChunk := func(text string) {
		chunk := map[string]any{
			"object": "chat.completion.chunk",
			"model":  model,
			"choices": []map[string]any{
				{"index": 0, "delta": map[string]string{"content": text}},
			},
		}
		b, _ := json.Marshal(chunk)
		fmt.Fprintf(c.Writer, "data: %s\n\n", b)
		if flusher != nil {
			flusher.Flush()
		}
	}

	scanner := bufio.NewScanner(upstream)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(line[len("data:"):])
		if payload == "" {
			continue
		}
		var ev anthropicStreamEvent
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "content_block_delta":
			if ev.Delta.Type == "text_delta" && ev.Delta.Text != "" {
				writeChunk(ev.Delta.Text)
			}
		case "error":
			if ev.Error != nil {
				writeChunk("\n[error: " + ev.Error.Message + "]")
			}
		}
	}

	fmt.Fprint(c.Writer, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

// anthropicMessageResponse is the non-streaming Anthropic Messages response.
type anthropicMessageResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

func anthropicNonStream(c *gin.Context, upstream io.Reader, model string) {
	var resp anthropicMessageResponse
	if err := json.NewDecoder(upstream).Decode(&resp); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "invalid response from Anthropic"})
		return
	}
	var sb strings.Builder
	for _, block := range resp.Content {
		if block.Type == "text" {
			sb.WriteString(block.Text)
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"object": "chat.completion",
		"model":  model,
		"choices": []map[string]any{
			{"index": 0, "message": map[string]string{"role": "assistant", "content": sb.String()}, "finish_reason": "stop"},
		},
	})
}
