package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// ctxBg is used for short, local database operations inside handlers.
func ctxBg() context.Context { return context.Background() }

// decodePatch reads a JSON object whose values are applied field by field.
func decodePatch(r *http.Request) (map[string]json.RawMessage, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, errors.New("请求体为空")
	}
	var patch map[string]json.RawMessage
	if err := json.Unmarshal(body, &patch); err != nil {
		return nil, errors.New("请求体必须是 JSON 对象: " + err.Error())
	}
	if len(patch) == 0 {
		return nil, errors.New("没有任何要修改的字段")
	}
	return patch, nil
}

func queryInt(r *http.Request, key string, def int) int {
	v := strings.TrimSpace(r.URL.Query().Get(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func queryInt64(r *http.Request, key string, def int64) int64 {
	v := strings.TrimSpace(r.URL.Query().Get(key))
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return def
	}
	return n
}

// Paging bounds shared by the list endpoints.
//
// The store clamps out-of-range values, so the handlers must clamp with the
// same bounds and echo the values actually applied. The panel renders
// "page_size" back to the operator; echoing the raw request would claim 999
// rows were returned when the store capped it at 200.
const (
	maxPageSize                 = 200
	defaultConversationPageSize = 20
	defaultMessagePageSize      = 50
)

// clampPage keeps the 1-based page number at or above 1.
func clampPage(n int) int {
	if n < 1 {
		return 1
	}
	return n
}

// clampPageSize falls back to the endpoint default outside [1, maxPageSize].
func clampPageSize(n, def int) int {
	if n < 1 || n > maxPageSize {
		return def
	}
	return n
}

func queryBoolPtr(r *http.Request, key string) *bool {
	v := strings.TrimSpace(r.URL.Query().Get(key))
	if v == "" {
		return nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return nil
	}
	return &b
}
