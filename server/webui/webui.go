// Package webui embeds the built-in browser chat interface served by the
// Ollama server. It is a single self-contained HTML page that talks to the
// existing OpenAI-compatible /v1/chat/completions endpoint, giving Ollama a
// ChatGPT/Claude-style chat experience out of the box with no extra install.
package webui

import _ "embed"

//go:embed chat.html
var chatHTML []byte

// ChatHTML returns the bytes of the embedded chat page.
func ChatHTML() []byte {
	return chatHTML
}
