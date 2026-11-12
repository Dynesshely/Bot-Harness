package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// embeddingServer serves one canned response body and records the request.
type embeddingRequestRecord struct {
	path          string
	authorization string
	body          map[string]any
}

func newEmbeddingServer(t *testing.T, status int, body string, seen *embeddingRequestRecord) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			seen.path = r.URL.Path
			seen.authorization = r.Header.Get("Authorization")
			_ = json.NewDecoder(r.Body).Decode(&seen.body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func mustEmbed(t *testing.T, client *Client, model string, input []string) [][]float32 {
	t.Helper()
	got, err := client.Embeddings(context.Background(), model, input)
	if err != nil {
		t.Fatalf("embeddings: %v", err)
	}
	return got
}

func TestEmbeddingsSendsTheOpenAIShape(t *testing.T) {
	var seen embeddingRequestRecord
	server := newEmbeddingServer(t, http.StatusOK, `{"data":[{"embedding":[0.1,0.2,0.3],"index":0}]}`, &seen)
	client := NewClient(server.URL+"/v1", "secret", &http.Client{Timeout: 5 * time.Second})

	got := mustEmbed(t, client, "text-embedding-3-small", []string{"你好"})
	if len(got) != 1 || len(got[0]) != 3 {
		t.Fatalf("vectors = %#v", got)
	}
	if math.Abs(float64(got[0][2])-0.3) > 1e-6 {
		t.Fatalf("vector = %v", got[0])
	}
	if seen.path != "/v1/embeddings" {
		t.Fatalf("path = %q", seen.path)
	}
	if seen.authorization != "Bearer secret" {
		t.Fatalf("authorization = %q", seen.authorization)
	}
	if seen.body["model"] != "text-embedding-3-small" {
		t.Fatalf("model in body = %#v", seen.body["model"])
	}
	inputs, ok := seen.body["input"].([]any)
	if !ok || len(inputs) != 1 || inputs[0] != "你好" {
		t.Fatalf("input in body = %#v", seen.body["input"])
	}
}

// 乱序返回也要按 index 还原成与 input 对齐的顺序。
func TestEmbeddingsSortsByIndex(t *testing.T) {
	server := newEmbeddingServer(t, http.StatusOK, `{"data":[
		{"embedding":[3,3],"index":2},
		{"embedding":[1,1],"index":0},
		{"embedding":[2,2],"index":1}
	]}`, nil)
	client := NewClient(server.URL+"/v1", "secret", &http.Client{Timeout: 5 * time.Second})

	got := mustEmbed(t, client, "m", []string{"a", "b", "c"})
	want := [][]float32{{1, 1}, {2, 2}, {3, 3}}
	if len(got) != len(want) {
		t.Fatalf("got %d vectors, want %d", len(got), len(want))
	}
	for i := range want {
		for j := range want[i] {
			if got[i][j] != want[i][j] {
				t.Fatalf("vector %d = %v, want %v", i, got[i], want[i])
			}
		}
	}
}

func TestEmbeddingsKeepsReturnOrderWithoutIndex(t *testing.T) {
	server := newEmbeddingServer(t, http.StatusOK, `{"data":[
		{"embedding":[9,9]},
		{"embedding":[1,1]},
		{"embedding":[5,5]}
	]}`, nil)
	client := NewClient(server.URL+"/v1", "secret", &http.Client{Timeout: 5 * time.Second})

	got := mustEmbed(t, client, "m", []string{"a", "b", "c"})
	want := [][]float32{{9, 9}, {1, 1}, {5, 5}}
	for i := range want {
		if len(got[i]) != 2 || got[i][0] != want[i][0] {
			t.Fatalf("vector %d = %v, want %v", i, got[i], want[i])
		}
	}
}

// 只给了部分 index 时不能猜：按返回顺序处理，而不是把缺 index 的当成 0。
func TestEmbeddingsMixedIndexFallsBackToReturnOrder(t *testing.T) {
	server := newEmbeddingServer(t, http.StatusOK, `{"data":[
		{"embedding":[1,0]},
		{"embedding":[0,1],"index":1}
	]}`, nil)
	client := NewClient(server.URL+"/v1", "secret", &http.Client{Timeout: 5 * time.Second})

	got := mustEmbed(t, client, "m", []string{"a", "b"})
	if got[0][0] != 1 || got[1][1] != 1 {
		t.Fatalf("vectors = %#v", got)
	}
}

func TestEmbeddingsRejectsBadResponses(t *testing.T) {
	cases := map[string]struct {
		body    string
		wantSub string
	}{
		"empty data":         {body: `{"data":[]}`, wantSub: "no vectors"},
		"missing data":       {body: `{"object":"list"}`, wantSub: "no vectors"},
		"count mismatch":     {body: `{"data":[{"embedding":[1],"index":0}]}`, wantSub: "1 vectors for 2 inputs"},
		"index out of range": {body: `{"data":[{"embedding":[1],"index":5},{"embedding":[2],"index":1}]}`, wantSub: "outside 0..1"},
		"duplicate index":    {body: `{"data":[{"embedding":[1],"index":0},{"embedding":[2],"index":0}]}`, wantSub: "duplicate index"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			server := newEmbeddingServer(t, http.StatusOK, tc.body, nil)
			client := NewClient(server.URL+"/v1", "secret", &http.Client{Timeout: 5 * time.Second})
			_, err := client.Embeddings(context.Background(), "m", []string{"a", "b"})
			if err == nil {
				t.Fatalf("expected an error for %s", name)
			}
			if tc.wantSub != "" && !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.wantSub)
			}
		})
	}
}

// HTTP 非 2xx 的错误文案：带状态码与上游摘要，但不许回显 key 或 host。
func TestEmbeddingsHTTPErrorIsSanitized(t *testing.T) {
	const key = "sk-fake-embedding-key"
	server := newEmbeddingServer(t, http.StatusBadRequest, `{"error":{"message":"bad request"}}`, nil)
	client := NewClient(server.URL+"/v1", key, &http.Client{Timeout: 5 * time.Second})

	_, err := client.Embeddings(context.Background(), "m", []string{"a"})
	if err == nil {
		t.Fatal("expected an error")
	}
	message := err.Error()
	if !strings.HasPrefix(message, "ai: embeddings HTTP 400: ") {
		t.Fatalf("message = %q", message)
	}
	if !strings.Contains(message, "bad request") {
		t.Fatalf("message should carry the provider summary: %q", message)
	}
	assertNoCredentials(t, message, key, server.URL)
}

// 上游把请求 URL 或 Authorization 回显在响应体里，也不能漏出去。
func TestEmbeddingsProviderEchoIsRedacted(t *testing.T) {
	server := newEmbeddingServer(t, http.StatusBadGateway, "", nil)
	// body 里塞入真实 host、完整 URL 与 key，模拟网关回显。
	body := `{"error":{"message":"upstream %s/v1/embeddings rejected Bearer sk-fake-leaked-key"}}`
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(strings.Replace(body, "%s", server.URL, 1)))
	})

	const key = "sk-fake-embedding-key"
	client := NewClient(server.URL+"/v1", key, &http.Client{Timeout: 5 * time.Second})

	_, err := client.Embeddings(context.Background(), "m", []string{"a"})
	if err == nil {
		t.Fatal("expected an error")
	}
	message := err.Error()
	if !strings.Contains(message, "502") {
		t.Fatalf("message = %q", message)
	}
	assertNoCredentials(t, message, key, server.URL)
	if strings.Contains(message, "sk-fake-leaked-key") {
		t.Fatalf("the echoed key leaked: %q", message)
	}
}

func TestEmbeddingsRejectsOversizedInput(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		// 每条输入回一个向量，才能把「超限」与「条数不匹配」两类错误分开。
		items := make([]string, 0, len(body.Input))
		for i := range body.Input {
			items = append(items, fmt.Sprintf(`{"embedding":[1],"index":%d}`, i))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[` + strings.Join(items, ",") + `]}`))
	}))
	defer server.Close()
	client := NewClient(server.URL+"/v1", "secret", &http.Client{Timeout: 5 * time.Second})

	t.Run("too many inputs", func(t *testing.T) {
		input := make([]string, MaxEmbeddingInputs+1)
		for i := range input {
			input[i] = "x"
		}
		_, err := client.Embeddings(context.Background(), "m", input)
		if err == nil {
			t.Fatal("expected an error")
		}
		if !strings.Contains(err.Error(), "exceed the limit") {
			t.Fatalf("error = %v", err)
		}
		// 超限必须在本地拒绝，不能把账单打出去。
		if calls.Load() != 0 {
			t.Fatalf("oversized input was sent: %d calls", calls.Load())
		}
	})

	t.Run("too many characters", func(t *testing.T) {
		// 用中文验证按 rune 计数，而不是按 byte。
		text := strings.Repeat("字", MaxEmbeddingChars/2+100)
		_, err := client.Embeddings(context.Background(), "m", []string{text, text})
		if err == nil {
			t.Fatal("expected an error")
		}
		if !strings.Contains(err.Error(), "characters exceed the limit") {
			t.Fatalf("error = %v", err)
		}
		if calls.Load() != 0 {
			t.Fatalf("oversized input was sent: %d calls", calls.Load())
		}
	})

	t.Run("exactly at the limits is allowed", func(t *testing.T) {
		input := make([]string, MaxEmbeddingInputs)
		for i := range input {
			input[i] = "x"
		}
		if _, err := client.Embeddings(context.Background(), "m", input); err != nil {
			t.Fatalf("input at the cap should be accepted: %v", err)
		}
		if calls.Load() != 1 {
			t.Fatalf("calls = %d", calls.Load())
		}
	})
}

func TestEmbeddingsValidationErrors(t *testing.T) {
	t.Run("not configured", func(t *testing.T) {
		client := NewClient("", "secret", &http.Client{Timeout: time.Second})
		if _, err := client.Embeddings(context.Background(), "m", []string{"a"}); !errors.Is(err, ErrNotConfigured) {
			t.Fatalf("expected ErrNotConfigured, got %v", err)
		}
	})

	t.Run("missing model", func(t *testing.T) {
		client := NewClient("https://one-api.internal.example/v1", "secret", &http.Client{Timeout: time.Second})
		if _, err := client.Embeddings(context.Background(), "  ", []string{"a"}); err == nil {
			t.Fatal("expected an error for an empty model")
		}
	})

	t.Run("empty input", func(t *testing.T) {
		client := NewClient("https://one-api.internal.example/v1", "secret", &http.Client{Timeout: time.Second})
		if _, err := client.Embeddings(context.Background(), "m", nil); err == nil {
			t.Fatal("expected an error for an empty input")
		}
	})
}

func TestEmbeddingModelReady(t *testing.T) {
	client := NewClient("https://one-api.internal.example/v1", "secret", nil)
	if client.EmbeddingModelReady("") {
		t.Fatal("an empty model name means the feature is off")
	}
	if client.EmbeddingModelReady("   ") {
		t.Fatal("a blank model name means the feature is off")
	}
	if !client.EmbeddingModelReady("text-embedding-3-small") {
		t.Fatal("a configured model name should report ready")
	}
}

func TestEmbeddingsHonoursContextTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer server.Close()

	const key = "sk-fake-timeout-key"
	client := NewClient(server.URL+"/v1", key, &http.Client{Timeout: 10 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := client.Embeddings(ctx, "m", []string{"a"})
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if time.Since(start) > time.Second {
		t.Fatalf("the call ignored the context deadline: %v", time.Since(start))
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the timeout must stay discoverable via errors.Is, got %v", err)
	}
	assertNoCredentials(t, err.Error(), key, server.URL)
}

func TestCosine(t *testing.T) {
	cases := []struct {
		name string
		a, b []float32
		want float64
		tol  float64
	}{
		{name: "identical", a: []float32{1, 2, 3}, b: []float32{1, 2, 3}, want: 1},
		{name: "same direction scaled", a: []float32{1, 2, 3}, b: []float32{2, 4, 6}, want: 1},
		{name: "orthogonal", a: []float32{1, 0}, b: []float32{0, 1}, want: 0},
		{name: "opposite", a: []float32{1, 0}, b: []float32{-1, 0}, want: -1},
		{name: "zero vector left", a: []float32{0, 0}, b: []float32{1, 2}, want: 0},
		{name: "zero vector right", a: []float32{1, 2}, b: []float32{0, 0}, want: 0},
		{name: "both zero", a: []float32{0, 0}, b: []float32{0, 0}, want: 0},
		{name: "length mismatch", a: []float32{1, 2}, b: []float32{1, 2, 3}, want: 0},
		{name: "empty", a: nil, b: nil, want: 0},
		{name: "known angle", a: []float32{1, 0}, b: []float32{1, 1}, want: math.Sqrt2 / 2, tol: 1e-9},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Cosine(tc.a, tc.b)
			tol := tc.tol
			if tol == 0 {
				tol = 1e-9
			}
			if math.Abs(got-tc.want) > tol {
				t.Fatalf("Cosine(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// 大维度 + 大分量：float32 累加会溢出成 +Inf，float64 累加不会。
func TestCosineAccumulatesInFloat64(t *testing.T) {
	const n = 4096
	a := make([]float32, n)
	b := make([]float32, n)
	for i := range a {
		a[i] = 1e19
		b[i] = 1e19
	}
	got := Cosine(a, b)
	if math.IsNaN(got) || math.IsInf(got, 0) {
		t.Fatalf("Cosine overflowed: %v", got)
	}
	if math.Abs(got-1) > 1e-9 {
		t.Fatalf("Cosine = %v, want 1", got)
	}
}

func assertNoCredentials(t *testing.T, message, key, baseURL string) {
	t.Helper()
	if strings.Contains(message, key) {
		t.Fatalf("the API key leaked into the error: %q", message)
	}
	host := strings.TrimPrefix(baseURL, "http://")
	host = strings.TrimPrefix(host, "https://")
	if host == "" {
		t.Fatal("test bug: empty base URL")
	}
	if idx := strings.Index(host, "/"); idx >= 0 {
		host = host[:idx]
	}
	if strings.Contains(message, host) {
		t.Fatalf("the endpoint host %q leaked into the error: %q", host, message)
	}
	if strings.Contains(message, baseURL) {
		t.Fatalf("the full endpoint URL leaked into the error: %q", message)
	}
}
