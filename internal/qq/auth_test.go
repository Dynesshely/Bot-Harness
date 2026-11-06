package qq

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestTokenManagerCachesAndRefreshes(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/app/getAppAccessToken" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method %s", r.Method)
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["appId"] != "12345" || body["clientSecret"] != "secret" {
			t.Errorf("unexpected credentials %#v", body)
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		// expires_in is a string on the wire; the client must accept both forms.
		_, _ = w.Write([]byte(`{"access_token":"tok-123","expires_in":"7200"}`))
	}))
	defer server.Close()

	tm := NewTokenManager("12345", "secret", server.URL, nil)

	first, err := tm.Token(context.Background(), false)
	if err != nil {
		t.Fatalf("first token: %v", err)
	}
	if first != "tok-123" {
		t.Fatalf("unexpected token %q", first)
	}

	if _, err := tm.Token(context.Background(), false); err != nil {
		t.Fatalf("cached token: %v", err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("expected the token to be cached, got %d calls", n)
	}

	if _, err := tm.Token(context.Background(), true); err != nil {
		t.Fatalf("forced refresh: %v", err)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("expected a forced refresh, got %d calls", n)
	}

	st := tm.Status()
	if !st.Valid || st.Refreshes != 2 || st.ExpiresInSeconds <= 0 {
		t.Fatalf("unexpected status %#v", st)
	}
	if st.ExpiresInSeconds > 7200 {
		t.Fatalf("expiry looks wrong: %d", st.ExpiresInSeconds)
	}
}

func TestTokenManagerSurfacesBusinessErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// The platform answers HTTP 200 even when the call failed.
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":100016,"message":"invalid appid or secret"}`))
	}))
	defer server.Close()

	tm := NewTokenManager("bad", "bad", server.URL, nil)
	if _, err := tm.Token(context.Background(), false); err == nil {
		t.Fatal("expected an error for a business failure")
	}
	st := tm.Status()
	if st.Valid || st.LastError == "" {
		t.Fatalf("unexpected status %#v", st)
	}
}

func TestFlexIntAcceptsStringAndNumber(t *testing.T) {
	var v struct {
		A flexInt `json:"a"`
		B flexInt `json:"b"`
	}
	if err := json.Unmarshal([]byte(`{"a":"7200","b":60}`), &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if v.A != 7200 || v.B != 60 {
		t.Fatalf("unexpected values %d %d", v.A, v.B)
	}
}

func TestAPIErrorCodeHelpers(t *testing.T) {
	err := &APIError{HTTPStatus: 400, Code: ErrPassiveExceeded, Message: "expired"}
	if !IsPassiveExpired(err) {
		t.Fatal("expected IsPassiveExpired to be true")
	}
	if IsDuplicate(err) {
		t.Fatal("duplicate detection matched the wrong code")
	}
	if !CodeIs(err, ErrMsgIDExpired, ErrPassiveExceeded) {
		t.Fatal("CodeIs should match one of the listed codes")
	}
}
