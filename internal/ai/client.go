// Package ai implements a minimal OpenAI-compatible chat client.
//
// It deliberately avoids a vendor SDK so any endpoint that speaks the
// /chat/completions protocol works: the base URL is resolved per request from
// the database settings (后台「系统设置」), not from .env.
package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// ContentPart is one element of a multimodal message.
type ContentPart struct {
	Type     string    `json:"type"` // "text" | "image_url"
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

// ImageURL points at an image, either an https URL or a data: URI.
type ImageURL struct {
	URL string `json:"url"`
}

// TextPart builds a text content part.
func TextPart(text string) ContentPart { return ContentPart{Type: "text", Text: text} }

// ImagePart builds an image content part from a data URI (or an https URL).
func ImagePart(url string) ContentPart {
	return ContentPart{Type: "image_url", ImageURL: &ImageURL{URL: url}}
}

// DataURI renders bytes as a data URI for inline image submission.
func DataURI(contentType string, data []byte) string {
	if contentType == "" {
		contentType = "image/jpeg"
	}
	return "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(data)
}

// Message is one chat turn. Either Content (plain text) or Parts (multimodal)
// is set; Parts wins when present.
type Message struct {
	Role    string
	Content string
	Parts   []ContentPart
	// ToolCalls are the calls the assistant asked for; ToolCallID marks a
	// role=tool message that answers one of them.
	ToolCalls  []ToolCall
	ToolCallID string
}

// MarshalJSON emits the OpenAI shape: `content` is a string for plain text and
// an array of parts for multimodal messages.
func (m Message) MarshalJSON() ([]byte, error) {
	type wire struct {
		Role       string     `json:"role"`
		Content    any        `json:"content"`
		ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
		ToolCallID string     `json:"tool_call_id,omitempty"`
	}
	content := any(m.Content)
	if len(m.Parts) > 0 {
		content = m.Parts
	}
	return json.Marshal(wire{Role: m.Role, Content: content, ToolCalls: m.ToolCalls, ToolCallID: m.ToolCallID})
}

// UnmarshalJSON accepts both shapes (used by tests and by request echo).
func (m *Message) UnmarshalJSON(b []byte) error {
	var probe struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		ToolCalls  []ToolCall      `json:"tool_calls"`
		ToolCallID string          `json:"tool_call_id"`
	}
	if err := json.Unmarshal(b, &probe); err != nil {
		return err
	}
	m.Role = probe.Role
	m.ToolCalls = probe.ToolCalls
	m.ToolCallID = probe.ToolCallID
	m.Parts = nil
	m.Content = ""
	if len(probe.Content) == 0 {
		return nil
	}
	if probe.Content[0] == '[' {
		return json.Unmarshal(probe.Content, &m.Parts)
	}
	return json.Unmarshal(probe.Content, &m.Content)
}

// Role constants.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	// RoleTool carries a tool result back to the model.
	RoleTool = "tool"
)

// ToolDef declares one callable function. Parameters is a JSON Schema object.
type ToolDef struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction is the description half of a ToolDef.
type ToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// NewToolDef builds a function definition; kind is always "function".
func NewToolDef(name, description string, parameters json.RawMessage) ToolDef {
	return ToolDef{Type: "function", Function: ToolFunction{
		Name: name, Description: description, Parameters: parameters,
	}}
}

// ToolCall is one function invocation requested by the model.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type,omitempty"`
	Function ToolCallFunc `json:"function"`
}

// ToolCallFunc is the name/arguments pair of a ToolCall. Arguments is a JSON
// string exactly as the model produced it (it may be malformed — validate it
// before trusting).
type ToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolMessage builds the role=tool message that answers one call.
func ToolMessage(callID, content string) Message {
	return Message{Role: RoleTool, ToolCallID: callID, Content: content}
}

// Usage reports token accounting when the provider supplies it.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChatRequest is one completion request.
type ChatRequest struct {
	Model       string
	Messages    []Message
	Temperature float64
	MaxTokens   int
	Timeout     time.Duration
	// Stream requests server-sent events; results are accumulated into one text.
	Stream bool
	// Tools lets the model call functions; empty disables tool calling.
	Tools []ToolDef
	// ToolChoice is passed through as-is ("auto" / "none" / "required").
	ToolChoice string
}

// ChatResult is the accumulated completion.
type ChatResult struct {
	Text string
	// Reasoning is the model's chain of thought, kept for diagnostics only.
	// It is never delivered: it is a scratchpad, not an answer.
	Reasoning    string
	Model        string
	Usage        Usage
	LatencyMS    int64
	FinishReason string
	Streamed     bool
	// ToolCalls is non-empty when the model wants functions executed instead of
	// (or before) answering. Text may be empty in that case — that is not an
	// error.
	ToolCalls []ToolCall
}

// Client talks to an OpenAI-compatible endpoint.
type Client struct {
	baseURL string
	// resolver, when set, supplies the base URL for every request. The admin
	// panel can retarget the endpoint while the process runs, so the URL cannot
	// be baked in at construction time.
	resolver func() string
	// keyResolver does the same for the API key: switching to another
	// OpenAI-compatible service almost always means a different key.
	keyResolver func() string
	apiKey      string
	http        *http.Client
}

// NewClient builds a client. baseURL must already include the version segment
// (for example https://api.deepseek.com/v1).
//
// The supplied HTTP client carries the transport (and therefore the TLS trust
// configuration); pass nil to use a default transport with no client timeout,
// relying on the per-request context deadline instead.
func NewClient(baseURL, apiKey string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Transport: defaultTransport()}
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http:    hc,
	}
}

func defaultTransport() *http.Transport {
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 5 * time.Second,
	}
}

// SetBaseURLResolver makes every request read the endpoint from fn instead of
// the value passed to NewClient, so the settings panel can switch providers
// without a restart. An empty result falls back to the configured default.
//
// Call it once, before the client is used from other goroutines.
func (c *Client) SetBaseURLResolver(fn func() string) { c.resolver = fn }

// SetAPIKeyResolver makes every request read the API key from fn instead of the
// value passed to NewClient. An empty result falls back to the configured key.
//
// Call it once, before the client is used from other goroutines.
func (c *Client) SetAPIKeyResolver(fn func() string) { c.keyResolver = fn }

// token resolves the bearer token for one request.
func (c *Client) token() string {
	if c.keyResolver != nil {
		if v := strings.TrimSpace(c.keyResolver()); v != "" {
			return v
		}
	}
	return c.apiKey
}

// APIKeySet reports whether a key is configured, without revealing it.
func (c *Client) APIKeySet() bool { return c.token() != "" }

// endpoint resolves the base URL for one request.
func (c *Client) endpoint() string {
	if c.resolver != nil {
		if v := strings.TrimRight(strings.TrimSpace(c.resolver()), "/"); v != "" {
			return v
		}
	}
	return c.baseURL
}

// BaseURL returns the endpoint root currently in use.
func (c *Client) BaseURL() string { return c.endpoint() }

// ErrNotConfigured is returned when no endpoint (or no key) is configured yet.
//
// The endpoint used to come from .env with a built-in default; it now lives only
// in the database settings, so "nothing configured" is a real state that must
// produce a message an operator can act on instead of a URL parse error.
var ErrNotConfigured = errors.New("ai: 模型端点未配置，请到后台「系统设置」填写 openai_base_url（以及 openai_api_key）")

// ErrEmptyCompletion is returned when the provider produced no text at all.
var ErrEmptyCompletion = errors.New("empty completion")

// ErrReasoningOnlyCompletion is returned when the provider wrote reasoning but
// never produced a final answer.
//
// The reasoning text is deliberately not surfaced: the model's chain of thought
// is a scratchpad, and pasting it into a chat both confuses readers and leaks
// the framing of our own prompt. Callers get a sentinel they can recognise
// instead of a result that would be sent verbatim.
var ErrReasoningOnlyCompletion = errors.New("model produced reasoning but no final answer")

// Chat performs a completion, streaming when requested.
func (c *Client) Chat(ctx context.Context, req ChatRequest) (*ChatResult, error) {
	if req.Timeout <= 0 {
		req.Timeout = 5 * time.Minute
	}
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, req.Timeout)
	defer cancel()
	if req.Model == "" {
		return nil, errors.New("ai: model is required")
	}
	if c.endpoint() == "" {
		return nil, ErrNotConfigured
	}
	if len(req.Messages) == 0 {
		return nil, errors.New("ai: no messages supplied")
	}

	start := time.Now()
	res, err := c.do(ctx, req, req.Stream)
	if err != nil && req.Stream {
		// Some compatible endpoints advertise streaming but misbehave; retry once
		// without it rather than failing the user's message.
		res, err = c.do(ctx, req, false)
	}
	if err == nil && req.Stream && strings.TrimSpace(res.Text) == "" && len(res.ToolCalls) == 0 {
		// The stream carried no answer. Some gateways cut the SSE body short,
		// and a reasoning-only stream says nothing about whether the model has
		// a final answer to give, so a plain request is always worth one try.
		if retry, retryErr := c.do(ctx, req, false); retryErr == nil {
			res = retry
		}
	}
	if err != nil {
		return nil, err
	}
	res.LatencyMS = time.Since(start).Milliseconds()

	if strings.TrimSpace(res.Text) == "" && len(res.ToolCalls) == 0 {
		// Never hand the chain of thought back as the answer. A reasoning model
		// that spent its whole budget thinking has produced nothing we may send.
		// (A tool-calling turn legitimately has no text and is not an error.)
		if strings.TrimSpace(res.Reasoning) != "" {
			return nil, ErrReasoningOnlyCompletion
		}
		return nil, ErrEmptyCompletion
	}
	return res, nil
}

type wireRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature float64   `json:"temperature,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Stream      bool      `json:"stream"`
	Tools       []ToolDef `json:"tools,omitempty"`
	ToolChoice  string    `json:"tool_choice,omitempty"`
}

type wireResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content          string     `json:"content"`
			ReasoningContent string     `json:"reasoning_content"`
			ToolCalls        []ToolCall `json:"tool_calls"`
		} `json:"message"`
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			// Streamed tool calls arrive as fragments keyed by index: the id and
			// name come in the first chunk, the arguments string is concatenated
			// across the rest.
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage Usage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	} `json:"error"`
}

func (c *Client) do(ctx context.Context, req ChatRequest, stream bool) (*ChatResult, error) {
	body, err := json.Marshal(wireRequest{
		Model:       req.Model,
		Messages:    req.Messages,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Stream:      stream,
		Tools:       req.Tools,
		ToolChoice:  req.ToolChoice,
	})
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint()+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.token())
	if stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ai: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return nil, fmt.Errorf("ai: HTTP %d: %s", resp.StatusCode, summarizeError(raw))
	}

	contentType := resp.Header.Get("Content-Type")
	if stream && strings.Contains(contentType, "text/event-stream") {
		return c.readStream(resp.Body)
	}
	// Either we asked for a plain response, or the endpoint ignored `stream`.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("ai: read response: %w", err)
	}
	var parsed wireResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("ai: decode response: %w (body: %s)", err, truncate(string(raw), 200))
	}
	if parsed.Error != nil {
		return nil, fmt.Errorf("ai: provider error: %s", parsed.Error.Message)
	}
	out := &ChatResult{Model: parsed.Model, Usage: parsed.Usage}
	for _, ch := range parsed.Choices {
		out.Text += ch.Message.Content
		out.Reasoning += ch.Message.ReasoningContent
		out.ToolCalls = append(out.ToolCalls, ch.Message.ToolCalls...)
		if ch.FinishReason != "" {
			out.FinishReason = ch.FinishReason
		}
	}
	return out, nil
}

func (c *Client) readStream(body io.Reader) (*ChatResult, error) {
	out := &ChatResult{Streamed: true}
	type partial struct {
		id, typ, name string
		args          strings.Builder
	}
	partials := map[int]*partial{}
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)

	var dataLines []string
	flushEvent := func() {
		if len(dataLines) == 0 {
			return
		}
		payload := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		if payload == "[DONE]" {
			return
		}
		var chunk wireResponse
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return
		}
		if chunk.Model != "" {
			out.Model = chunk.Model
		}
		if chunk.Usage.TotalTokens > 0 || chunk.Usage.PromptTokens > 0 {
			out.Usage = chunk.Usage
		}
		for _, ch := range chunk.Choices {
			out.Text += ch.Delta.Content
			out.Reasoning += ch.Delta.ReasoningContent
			if ch.Message.Content != "" {
				out.Text += ch.Message.Content
			}
			out.Reasoning += ch.Message.ReasoningContent
			for _, tc := range ch.Delta.ToolCalls {
				part := partials[tc.Index]
				if part == nil {
					part = &partial{}
					partials[tc.Index] = part
				}
				if tc.ID != "" {
					part.id = tc.ID
				}
				if tc.Type != "" {
					part.typ = tc.Type
				}
				if tc.Function.Name != "" {
					part.name = tc.Function.Name
				}
				part.args.WriteString(tc.Function.Arguments)
			}
			if ch.FinishReason != "" {
				out.FinishReason = ch.FinishReason
			}
		}
	}

	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		case strings.TrimSpace(line) == "":
			flushEvent()
		default:
			// Comment lines (": keep-alive") and event: fields are ignored.
		}
	}
	flushEvent()
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("ai: read stream: %w", err)
	}
	// Emit the accumulated calls in the order the model requested them.
	indexes := make([]int, 0, len(partials))
	for i := range partials {
		indexes = append(indexes, i)
	}
	sort.Ints(indexes)
	for _, i := range indexes {
		p := partials[i]
		typ := p.typ
		if typ == "" {
			typ = "function"
		}
		out.ToolCalls = append(out.ToolCalls, ToolCall{
			ID: p.id, Type: typ,
			Function: ToolCallFunc{Name: p.name, Arguments: p.args.String()},
		})
	}
	return out, nil
}

// ModelInfo is one entry returned by the /models endpoint.
type ModelInfo struct {
	ID string `json:"id"`
}

// ListModels fetches the model catalogue from the compatible endpoint.
//
// Both the OpenAI envelope ({"data":[...]}) and a bare JSON array are accepted,
// because gateway implementations differ.
func (c *Client) ListModels(ctx context.Context) ([]ModelInfo, error) {
	if c.endpoint() == "" {
		return nil, ErrNotConfigured
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint()+"/models", nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.token())
	httpReq.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ai: list models failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("ai: list models: read response: %w", err)
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("ai: list models HTTP %d: %s", resp.StatusCode, summarizeError(raw))
	}

	var probe struct {
		Data   json.RawMessage `json:"data"`
		Models json.RawMessage `json:"models"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &probe); err == nil {
		if probe.Error != nil && probe.Error.Message != "" {
			return nil, fmt.Errorf("ai: list models provider error: %s", probe.Error.Message)
		}
		if models, ok := parseModelList(probe.Data); ok {
			return normalizeModels(models), nil
		}
		if models, ok := parseModelList(probe.Models); ok {
			return normalizeModels(models), nil
		}
	}
	// A bare array is also accepted.
	if models, ok := parseModelList(raw); ok {
		return normalizeModels(models), nil
	}
	return nil, errors.New("ai: list models: unrecognised response shape")
}

// parseModelList accepts either [{"id":...}] or ["model-a","model-b"].
func parseModelList(raw json.RawMessage) ([]ModelInfo, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, false
	}
	var objects []ModelInfo
	if err := json.Unmarshal(raw, &objects); err == nil && len(objects) > 0 {
		return objects, true
	}
	var ids []string
	if err := json.Unmarshal(raw, &ids); err == nil && len(ids) > 0 {
		out := make([]ModelInfo, 0, len(ids))
		for _, id := range ids {
			out = append(out, ModelInfo{ID: id})
		}
		return out, true
	}
	return nil, false
}

func normalizeModels(in []ModelInfo) []ModelInfo {
	out := make([]ModelInfo, 0, len(in))
	for _, m := range in {
		if strings.TrimSpace(m.ID) == "" {
			continue
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func summarizeError(raw []byte) string {
	var parsed wireResponse
	if err := json.Unmarshal(raw, &parsed); err == nil && parsed.Error != nil {
		return parsed.Error.Message
	}
	return truncate(string(bytes.TrimSpace(raw)), 300)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}
