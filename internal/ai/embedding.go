package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Embeddings support for OpenAI-compatible endpoints.
//
// It is deliberately as small as the chat client: one POST to /embeddings and a
// plain result slice. Batching policy, persistence and the "which model"
// decision belong to the caller; this file only guarantees the wire protocol,
// bounded input and redacted errors.
const (
	// MaxEmbeddingInputs caps one request's input array. Embedding APIs charge
	// per token, so a runaway batch is a cost bug, not just a latency bug.
	MaxEmbeddingInputs = 64
	// MaxEmbeddingChars caps the total length (in runes) of all inputs in one
	// request. Oversized input is rejected instead of silently truncated:
	// a truncated text changes the vector and the caller never finds out.
	MaxEmbeddingChars = 32000
	// DefaultEmbeddingTimeout applies when the caller's context carries no
	// deadline of its own.
	DefaultEmbeddingTimeout = 30 * time.Second
)

// EmbeddingModelReady reports whether an embedding model is configured.
//
// It never probes the provider (that would cost a request and can hang startup);
// an empty or whitespace-only name simply means the feature is off.
func (c *Client) EmbeddingModelReady(model string) bool {
	return strings.TrimSpace(model) != ""
}

type embeddingRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embeddingResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		// Index is a pointer so a missing field can be told apart from index 0.
		Index *int `json:"index"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Embeddings turns input texts into vectors via POST {endpoint}/embeddings.
//
// The result is aligned with input: providers that report an index are sorted by
// it, providers that omit it keep their return order. No retries happen here;
// the caller decides whether a batch is worth retrying.
func (c *Client) Embeddings(ctx context.Context, model string, input []string) ([][]float32, error) {
	if strings.TrimSpace(model) == "" {
		return nil, errors.New("ai: embeddings: model is required")
	}
	if c.endpoint() == "" {
		return nil, ErrNotConfigured
	}
	if len(input) == 0 {
		return nil, errors.New("ai: embeddings: input is required")
	}
	if len(input) > MaxEmbeddingInputs {
		return nil, fmt.Errorf("ai: embeddings: %d inputs exceed the limit of %d", len(input), MaxEmbeddingInputs)
	}
	total := 0
	for _, s := range input {
		total += utf8.RuneCountInString(s)
	}
	if total > MaxEmbeddingChars {
		return nil, fmt.Errorf("ai: embeddings: %d characters exceed the limit of %d", total, MaxEmbeddingChars)
	}
	// Respect a caller's tighter deadline; only fill in the default when there is
	// none, so a short-lived turn cannot be extended by this call.
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultEmbeddingTimeout)
		defer cancel()
	}

	body, err := json.Marshal(embeddingRequest{Model: model, Input: input})
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint()+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ai: embeddings: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.token())

	resp, err := c.http.Do(httpReq)
	if err != nil {
		// The underlying *url.Error carries the full URL; the visible message is
		// scrubbed while Unwrap keeps errors.Is(context.DeadlineExceeded) working.
		return nil, &embeddingTransportError{
			message: "ai: embeddings request failed: " + c.redactEmbedding(err.Error()),
			cause:   err,
		}
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("ai: embeddings: read response: %w", err)
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("ai: embeddings HTTP %d: %s", resp.StatusCode, c.redactEmbedding(summarizeError(raw)))
	}

	var parsed embeddingResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("ai: embeddings: decode response: %w (body: %s)", err, truncate(c.redactEmbedding(string(raw)), 200))
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return nil, fmt.Errorf("ai: embeddings provider error: %s", truncate(c.redactEmbedding(parsed.Error.Message), 300))
	}
	if len(parsed.Data) == 0 {
		return nil, errors.New("ai: embeddings: provider returned no vectors")
	}
	if len(parsed.Data) != len(input) {
		return nil, fmt.Errorf("ai: embeddings: provider returned %d vectors for %d inputs", len(parsed.Data), len(input))
	}

	// Sorting only makes sense when the provider indexed every row; a mixed
	// response would need guesswork, so it keeps the return order instead.
	indexed := true
	for _, item := range parsed.Data {
		if item.Index == nil {
			indexed = false
			break
		}
	}
	out := make([][]float32, len(parsed.Data))
	for pos, item := range parsed.Data {
		slot := pos
		if indexed {
			slot = *item.Index
		}
		if slot < 0 || slot >= len(out) {
			return nil, fmt.Errorf("ai: embeddings: provider returned index %d outside 0..%d", slot, len(out)-1)
		}
		if out[slot] != nil {
			return nil, fmt.Errorf("ai: embeddings: provider returned duplicate index %d", slot)
		}
		out[slot] = item.Embedding
	}
	return out, nil
}

// Cosine returns the cosine similarity of a and b in [-1, 1].
//
// Zero for mismatched lengths (vectors from different embedding models are not
// comparable) and for zero vectors (no direction). Accumulation is float64 so
// that high-dimensional or large-magnitude vectors do not lose precision.
func Cosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		normA += x * x
		normB += y * y
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

// embeddingTransportError keeps the cause discoverable via errors.Is while its
// message stays free of the endpoint URL and the API key.
type embeddingTransportError struct {
	message string
	cause   error
}

func (e *embeddingTransportError) Error() string { return e.message }

func (e *embeddingTransportError) Unwrap() error { return e.cause }

// redactEmbedding scrubs the configured endpoint, its host and the API key, plus
// URL- and credential-shaped words, from a diagnostic string.
//
// Provider error bodies are untrusted: a gateway may echo the request URL or the
// Authorization header back in its message.
func (c *Client) redactEmbedding(s string) string {
	if s == "" {
		return ""
	}
	var urls []string
	if ep := strings.TrimSpace(c.endpoint()); ep != "" {
		urls = append(urls, ep)
		if u, err := url.Parse(ep); err == nil {
			if u.Host != "" {
				urls = append(urls, u.Host)
			}
			if h := u.Hostname(); h != "" && h != u.Host {
				urls = append(urls, h)
			}
		}
	}
	var secrets []string
	if tok := strings.TrimSpace(c.token()); len(tok) >= 8 {
		secrets = append(secrets, tok)
	}
	// Longest first: the full URL must win over the bare host it contains.
	sort.SliceStable(urls, func(i, j int) bool { return len(urls[i]) > len(urls[j]) })
	for _, u := range urls {
		s = strings.ReplaceAll(s, u, "<端点>")
	}
	for _, secret := range secrets {
		s = strings.ReplaceAll(s, secret, "***")
	}
	fields := strings.Fields(s)
	for i, f := range fields {
		lower := strings.ToLower(f)
		switch {
		case strings.Contains(lower, "://"),
			strings.HasPrefix(lower, "bearer"),
			strings.Contains(lower, "sk-"),
			strings.Contains(lower, "api_key="), strings.Contains(lower, "api-key="),
			strings.Contains(lower, "apikey="),
			strings.Contains(lower, "token="), strings.Contains(lower, "password="),
			strings.Contains(lower, "secret="):
			fields[i] = "***"
		}
	}
	return strings.Join(fields, " ")
}
