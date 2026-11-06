package ai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func request() ChatRequest {
	return ChatRequest{
		Model:       "test-model",
		Messages:    []Message{{Role: RoleUser, Content: "你好"}},
		Temperature: 0.5,
		MaxTokens:   64,
		Timeout:     5 * time.Second,
		Stream:      true,
	}
}

func TestChatStreamingAccumulatesDeltas(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("authorization header = %q", got)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["stream"] != true {
			t.Errorf("expected stream=true, got %#v", body["stream"])
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		chunks := []string{
			`{"model":"test-model","choices":[{"delta":{"content":"你"}}]}`,
			`{"model":"test-model","choices":[{"delta":{"content":"好"}}]}`,
			`{"model":"test-model","choices":[{"delta":{"content":"呀"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":3,"total_tokens":6}}`,
		}
		for _, c := range chunks {
			_, _ = w.Write([]byte("data: " + c + "\n\n"))
			flusher.Flush()
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer server.Close()

	client := NewClient(server.URL+"/v1", "secret", &http.Client{Timeout: 10 * time.Second})
	res, err := client.Chat(context.Background(), request())
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if res.Text != "你好呀" {
		t.Fatalf("text = %q", res.Text)
	}
	if !res.Streamed {
		t.Fatal("expected the streamed flag")
	}
	if res.FinishReason != "stop" {
		t.Fatalf("finish reason = %q", res.FinishReason)
	}
	if res.Usage.TotalTokens != 6 {
		t.Fatalf("usage = %#v", res.Usage)
	}
}

func TestChatFallsBackWhenStreamingUnsupported(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["stream"] == true {
			// A compatible endpoint that advertises streaming but rejects it.
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"stream not supported"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"test-model","choices":[{"message":{"content":"非流式回答"},"finish_reason":"stop"}],"usage":{"total_tokens":9}}`))
	}))
	defer server.Close()

	client := NewClient(server.URL+"/v1", "secret", &http.Client{Timeout: 10 * time.Second})
	res, err := client.Chat(context.Background(), request())
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if res.Text != "非流式回答" {
		t.Fatalf("text = %q", res.Text)
	}
	if res.Streamed {
		t.Fatal("the fallback should not be marked as streamed")
	}
	if calls.Load() != 2 {
		t.Fatalf("expected one failed stream attempt plus one fallback, got %d calls", calls.Load())
	}
}

func TestChatPlainJSONResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"m","choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()

	req := request()
	req.Stream = false
	client := NewClient(server.URL+"/v1", "secret", &http.Client{Timeout: 10 * time.Second})
	res, err := client.Chat(context.Background(), req)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if res.Text != "ok" {
		t.Fatalf("text = %q", res.Text)
	}
}

func TestChatSurfacesProviderError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	defer server.Close()

	client := NewClient(server.URL+"/v1", "bad", &http.Client{Timeout: 10 * time.Second})
	_, err := client.Chat(context.Background(), request())
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "invalid api key") {
		t.Fatalf("error should mention the provider message, got %v", err)
	}
}

func TestChatRejectsEmptyCompletion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"m","choices":[{"message":{"content":"   "}}]}`))
	}))
	defer server.Close()

	client := NewClient(server.URL+"/v1", "secret", &http.Client{Timeout: 10 * time.Second})
	req := request()
	req.Stream = false
	if _, err := client.Chat(context.Background(), req); !errors.Is(err, ErrEmptyCompletion) {
		t.Fatalf("expected ErrEmptyCompletion, got %v", err)
	}
}

func TestChatHonoursContextTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer server.Close()

	req := request()
	req.Timeout = 80 * time.Millisecond
	client := NewClient(server.URL+"/v1", "secret", &http.Client{Timeout: 10 * time.Second})
	if _, err := client.Chat(context.Background(), req); err == nil {
		t.Fatal("expected a timeout error")
	}
}

/* ------------------------------------------------------------------ *
 * 模型目录
 * ------------------------------------------------------------------ */

func TestListModelsOpenAIEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[
			{"id":"z-model","object":"model","owned_by":"vendor"},
			{"id":"a-model","object":"model","owned_by":"vendor"}
		]}`))
	}))
	defer server.Close()

	client := NewClient(server.URL+"/v1", "secret", &http.Client{Timeout: 5 * time.Second})
	models, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatalf("list models: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	// The list is sorted so the admin selector is stable.
	if models[0].ID != "a-model" || models[1].ID != "z-model" {
		t.Fatalf("models should be sorted by id, got %#v", models)
	}
}

func TestListModelsAcceptsBareArrayAndModelsKey(t *testing.T) {
	cases := map[string]string{
		"bare array":  `["alpha","beta"]`,
		"models key":  `{"models":[{"id":"gamma"}]}`,
		"data string": `{"data":["delta","epsilon"]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()

			client := NewClient(server.URL+"/v1", "secret", &http.Client{Timeout: 5 * time.Second})
			models, err := client.ListModels(context.Background())
			if err != nil {
				t.Fatalf("list models: %v", err)
			}
			if len(models) == 0 {
				t.Fatalf("expected models for %s", name)
			}
		})
	}
}

func TestListModelsSurfacesErrors(t *testing.T) {
	t.Run("http error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
		}))
		defer server.Close()

		client := NewClient(server.URL+"/v1", "bad", &http.Client{Timeout: 5 * time.Second})
		_, err := client.ListModels(context.Background())
		if err == nil || !strings.Contains(err.Error(), "invalid api key") {
			t.Fatalf("expected the provider message, got %v", err)
		}
	})

	t.Run("unrecognised shape", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"unexpected":true}`))
		}))
		defer server.Close()

		client := NewClient(server.URL+"/v1", "secret", &http.Client{Timeout: 5 * time.Second})
		if _, err := client.ListModels(context.Background()); err == nil {
			t.Fatal("expected an error for an unrecognised response shape")
		}
	})
}

func TestEmptyStreamFallsBackToPlainRequest(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["stream"] == true {
			// 200 with an SSE body that carries no content at all.
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{}}]}\n\ndata: [DONE]\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"m","choices":[{"message":{"content":"非流式补救"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	client := NewClient(server.URL+"/v1", "secret", &http.Client{Timeout: 5 * time.Second})
	res, err := client.Chat(context.Background(), request())
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if res.Text != "非流式补救" {
		t.Fatalf("text = %q", res.Text)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected a stream attempt plus a plain retry, got %d calls", calls.Load())
	}
}

// A reasoning model that never writes a final answer must not have its chain of
// thought delivered as the reply: the client reports a recognisable error and
// returns nothing the caller could send.
func TestReasoningOnlyResponseIsNeverDelivered(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			chunks := []string{
				`{"choices":[{"delta":{"reasoning_content":"先分析一下"}}]}`,
				`{"choices":[{"delta":{"reasoning_content":"，再给结论"},"finish_reason":"stop"}]}`,
			}
			for _, c := range chunks {
				_, _ = w.Write([]byte("data: " + c + "\n\n"))
			}
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"m","choices":[{"message":{"content":"","reasoning_content":"先分析一下，再给结论"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	client := NewClient(server.URL+"/v1", "secret", &http.Client{Timeout: 5 * time.Second})
	res, err := client.Chat(context.Background(), request())
	if !errors.Is(err, ErrReasoningOnlyCompletion) {
		t.Fatalf("expected ErrReasoningOnlyCompletion, got %v", err)
	}
	if res != nil {
		t.Fatalf("a reasoning-only turn must not yield a deliverable result: %+v", res)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected a stream attempt plus a plain retry, got %d calls", calls.Load())
	}
}

// The plain retry is what keeps reasoning-only streams from turning into user
// visible errors: some gateways only deliver the answer off the stream.
func TestReasoningOnlyStreamIsRescuedByPlainRetry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"reasoning_content":"想了很久"}}]}` + "\n\n"))
			_, _ = w.Write([]byte(`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"m","choices":[{"message":{"content":"最终答案"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	client := NewClient(server.URL+"/v1", "secret", &http.Client{Timeout: 5 * time.Second})
	res, err := client.Chat(context.Background(), request())
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if res.Text != "最终答案" {
		t.Fatalf("text = %q", res.Text)
	}
	if strings.Contains(res.Text, "想了很久") {
		t.Fatal("the chain of thought leaked into the answer")
	}
}

func TestErrEmptyCompletionHasNoDoublePrefix(t *testing.T) {
	if strings.HasPrefix(ErrEmptyCompletion.Error(), "ai: ") {
		t.Fatalf("the sentinel must not pre-format the component prefix: %q", ErrEmptyCompletion.Error())
	}
	if strings.HasPrefix(ErrReasoningOnlyCompletion.Error(), "ai: ") {
		t.Fatalf("the sentinel must not pre-format the component prefix: %q", ErrReasoningOnlyCompletion.Error())
	}
}

func TestReasoningAccumulatedAlongsideContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"reasoning_content":"想","content":"答"}}]}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"content":"案"},"finish_reason":"stop"}]}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := NewClient(server.URL+"/v1", "secret", &http.Client{Timeout: 5 * time.Second})
	res, err := client.Chat(context.Background(), request())
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if res.Text != "答案" || res.Reasoning != "想" {
		t.Fatalf("text=%q reasoning=%q", res.Text, res.Reasoning)
	}
}

// 端点可以在运行时改（设置面板里的 openai_base_url）：每次请求都重新解析。
func TestBaseURLResolverOverridesTheConstructedURL(t *testing.T) {
	plain := func(body string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"model":"m","choices":[{"message":{"content":"` + body + `"},"finish_reason":"stop"}]}`))
		}))
	}
	first := plain("第一个端点")
	defer first.Close()
	second := plain("第二个端点")
	defer second.Close()

	client := NewClient(first.URL+"/v1", "secret", &http.Client{Timeout: 5 * time.Second})
	current := first.URL + "/v1"
	client.SetBaseURLResolver(func() string { return current })

	req := request()
	req.Stream = false

	res, err := client.Chat(context.Background(), req)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if res.Text != "第一个端点" {
		t.Fatalf("text = %q", res.Text)
	}

	// 末尾多一个斜杠也要收敛掉，否则会拼出 //chat/completions
	current = second.URL + "/v1/"
	res, err = client.Chat(context.Background(), req)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if res.Text != "第二个端点" {
		t.Fatalf("切换端点后 text = %q", res.Text)
	}
	if got := client.BaseURL(); got != second.URL+"/v1" {
		t.Fatalf("BaseURL() = %q", got)
	}

	// 解析器返回空 → 回退到构造时的地址（对应设置里留空 = 用 .env）
	current = ""
	if got := client.BaseURL(); got != first.URL+"/v1" {
		t.Fatalf("留空时应回退到原地址，实际 %q", got)
	}
	res, err = client.Chat(context.Background(), req)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if res.Text != "第一个端点" {
		t.Fatalf("回退后 text = %q", res.Text)
	}
}

// 未设解析器时行为不变。
func TestBaseURLWithoutResolver(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"m","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	client := NewClient(server.URL+"/v1", "secret", &http.Client{Timeout: 5 * time.Second})
	if got := client.BaseURL(); got != server.URL+"/v1" {
		t.Fatalf("BaseURL() = %q", got)
	}
}

// 换端点通常要同时换密钥：key 也要能运行时解析，且留空回退。
func TestAPIKeyResolverOverridesTheConstructedKey(t *testing.T) {
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"m","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	client := NewClient(server.URL+"/v1", "env-key", &http.Client{Timeout: 5 * time.Second})
	current := ""
	client.SetAPIKeyResolver(func() string { return current })

	req := request()
	req.Stream = false

	// 解析器返回空 → 用构造时的 key
	if _, err := client.Chat(context.Background(), req); err != nil {
		t.Fatalf("chat: %v", err)
	}
	current = "panel-key"
	if !client.APIKeySet() {
		t.Fatal("设置了密钥后 APIKeySet 应为 true")
	}
	if _, err := client.Chat(context.Background(), req); err != nil {
		t.Fatalf("chat: %v", err)
	}

	want := []string{"Bearer env-key", "Bearer panel-key"}
	if len(seen) != 2 || seen[0] != want[0] || seen[1] != want[1] {
		t.Fatalf("Authorization 序列不对：%#v", seen)
	}
}
